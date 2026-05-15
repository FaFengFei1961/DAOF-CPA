package controller

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"daof-ai-hub/database"
	"daof-ai-hub/middleware"

	"github.com/gofiber/fiber/v2"
)

// newAdminGrantTestApp 给 AdminGrantSubscription 挂上模拟管理员认证 + 真实 AdminGuard。
// fix MAJOR M22-A3（codex 第二十二轮）：内置真实 AdminGuard 让 CSRF / status=1 链路被覆盖
func newAdminGrantTestApp(admin *database.User) *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error {
		c.Request().Header.SetCookie("daof_admin_token", admin.Token)
		return c.Next()
	})
	app.Use(middleware.AdminGuard)
	app.Post("/admin/sub/grant", AdminGrantSubscription)
	app.Post("/admin/sub/:id/revoke-grant", AdminRevokeGrantedSubscription)
	return app
}

// TestGrant_Success 基本 happy path：admin 给用户赠送 1 份订阅。
//   - 用户得到 1 条 active sub，IsGranted=true，GrantReason 正确
//   - 用户余额不变
//   - 写入 1 条 admin_grant_sub 账单（AmountUSD=0）
//   - 写入 1 条 OperationLog
func TestGrant_Success(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 0.0) // 余额 0
	pkg := seedPackage(t)        // 价格 9.9
	app := newAdminGrantTestApp(admin)

	code, resp := doJSON(t, app, "POST", "/admin/sub/grant", map[string]any{
		"user_id":    user.ID,
		"package_id": pkg.ID,
		"quantity":   1,
		"reason":     "客服补偿订单 #20260509-test",
	})
	if code != 200 {
		t.Fatalf("expected 200, got %d body=%v", code, resp)
	}
	if resp["success"] != true {
		t.Fatalf("success=false: %v", resp)
	}

	// 1 条 active 订阅 + IsGranted=true
	var subs []database.UserSubscription
	database.DB.Where("user_id = ?", user.ID).Find(&subs)
	if len(subs) != 1 {
		t.Fatalf("got %d subs, want 1", len(subs))
	}
	if subs[0].Status != "active" {
		t.Errorf("status=%q, want active", subs[0].Status)
	}
	if !subs[0].IsGranted {
		t.Errorf("IsGranted=false, want true")
	}
	if subs[0].GrantReason == "" {
		t.Errorf("GrantReason 为空，应该写入 reason")
	}

	// 余额不变
	var fresh database.User
	database.DB.First(&fresh, user.ID)
	if fresh.Quota != 0.0 {
		t.Errorf("balance=%v, want 0 (no charge on grant)", fresh.Quota)
	}

	// 1 条 admin_grant_sub 账单 + AmountUSD=0
	var entries []database.BillingEntry
	database.DB.Where("user_id = ? AND entry_type = ?",
		user.ID, database.BillingTypeAdminGrantSub).Find(&entries)
	if len(entries) != 1 {
		t.Errorf("got %d admin_grant_sub entries, want 1", len(entries))
	} else if entries[0].AmountUSD != 0 {
		t.Errorf("AmountUSD=%v, want 0 (grant 不动钱)", entries[0].AmountUSD)
	}

	// 1 条 OperationLog
	var logs []database.OperationLog
	database.DB.Where("target_user_id = ? AND action_type = ?",
		user.ID, "GRANT_SUBSCRIPTION").Find(&logs)
	if len(logs) != 1 {
		t.Errorf("got %d audit logs, want 1", len(logs))
	}
}

// TestGrant_QuantityDoesNotCreditBalance 多份赠送只创建订阅和 0 金额账单，不给 user.Quota 入账。
func TestGrant_QuantityDoesNotCreditBalance(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 1)
	pkg := seedPackage(t)
	app := newAdminGrantTestApp(admin)

	code, resp := doJSON(t, app, "POST", "/admin/sub/grant", map[string]any{
		"user_id":    user.ID,
		"package_id": pkg.ID,
		"quantity":   2,
		"reason":     "活动赠送",
	})
	if code != 200 {
		t.Fatalf("expected 200, got %d body=%v", code, resp)
	}

	var fresh database.User
	database.DB.First(&fresh, user.ID)
	wantMicro := int64(1 * database.MicroPerUSD)
	if fresh.Quota != wantMicro {
		t.Errorf("balance=%d, want unchanged %d", fresh.Quota, wantMicro)
	}

	// 2 条 grant（每份订阅一条），没有 bonus_credit。
	var grantCount, bonusCount int64
	database.DB.Model(&database.BillingEntry{}).
		Where("user_id = ? AND entry_type = ?", user.ID, database.BillingTypeAdminGrantSub).
		Count(&grantCount)
	database.DB.Model(&database.BillingEntry{}).
		Where("user_id = ? AND entry_type = ?", user.ID, database.BillingTypeBonusCredit).
		Count(&bonusCount)
	if grantCount != 2 {
		t.Errorf("admin_grant_sub count=%d, want 2", grantCount)
	}
	if bonusCount != 0 {
		t.Errorf("bonus_credit count=%d, want 0", bonusCount)
	}
}

func TestGrant_RejectDeprecatedApplyBonus(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 1)
	pkg := seedPackage(t)
	app := newAdminGrantTestApp(admin)

	code, resp := doJSON(t, app, "POST", "/admin/sub/grant", map[string]any{
		"user_id":     user.ID,
		"package_id":  pkg.ID,
		"quantity":    1,
		"reason":      "旧字段拒绝测试",
		"apply_bonus": false,
	})
	if code != 400 {
		t.Fatalf("expected 400 for deprecated apply_bonus, got %d body=%v", code, resp)
	}
	if resp["message_code"] != "ERR_DEPRECATED_FIELD" {
		t.Errorf("expected ERR_DEPRECATED_FIELD, got %v", resp["message_code"])
	}
}

// Phase 8 只保留订阅产品类型。

// TestGrant_RejectSelf admin 不能给自己赠送。
func TestGrant_RejectSelf(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	pkg := seedPackage(t)
	app := newAdminGrantTestApp(admin)

	code, resp := doJSON(t, app, "POST", "/admin/sub/grant", map[string]any{
		"user_id":    admin.ID, // 给自己
		"package_id": pkg.ID,
		"quantity":   1,
		"reason":     "我自己",
	})
	if code != 400 {
		t.Fatalf("expected 400, got %d body=%v", code, resp)
	}
	if resp["message_code"] != "ERR_GRANT_SELF" {
		t.Errorf("expected ERR_GRANT_SELF, got %v", resp["message_code"])
	}
}

// TestGrant_RejectAnotherAdmin 不能给另一个 admin 赠送。
func TestGrant_RejectAnotherAdmin(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	otherAdmin := database.User{
		Username: "admin2", Role: "admin", Token: "sk-admin2", Status: 1,
	}
	database.DB.Create(&otherAdmin)
	pkg := seedPackage(t)
	app := newAdminGrantTestApp(admin)

	code, resp := doJSON(t, app, "POST", "/admin/sub/grant", map[string]any{
		"user_id":    otherAdmin.ID,
		"package_id": pkg.ID,
		"reason":     "给另一个 admin",
	})
	if code != 403 {
		t.Fatalf("expected 403, got %d body=%v", code, resp)
	}
	if resp["message_code"] != "ERR_TARGET_NOT_USER" {
		t.Errorf("expected ERR_TARGET_NOT_USER, got %v", resp["message_code"])
	}
}

// TestGrant_RejectBannedUser 被封禁用户不能赠送。
func TestGrant_RejectBannedUser(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 0.0)
	user.Status = 2
	database.DB.Save(user)
	pkg := seedPackage(t)
	app := newAdminGrantTestApp(admin)

	code, resp := doJSON(t, app, "POST", "/admin/sub/grant", map[string]any{
		"user_id":    user.ID,
		"package_id": pkg.ID,
		"reason":     "封禁用户",
	})
	if code != 400 {
		t.Fatalf("expected 400, got %d body=%v", code, resp)
	}
	if resp["message_code"] != "ERR_TARGET_USER_INACTIVE" {
		t.Errorf("expected ERR_TARGET_USER_INACTIVE, got %v", resp["message_code"])
	}
}

// TestGrant_RejectMissingReason reason 必填。
func TestGrant_RejectMissingReason(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 0.0)
	pkg := seedPackage(t)
	app := newAdminGrantTestApp(admin)

	code, resp := doJSON(t, app, "POST", "/admin/sub/grant", map[string]any{
		"user_id":    user.ID,
		"package_id": pkg.ID,
		// 没有 reason
	})
	if code != 400 {
		t.Fatalf("expected 400, got %d body=%v", code, resp)
	}
	if resp["message_code"] != "ERR_REASON_REQUIRED" {
		t.Errorf("expected ERR_REASON_REQUIRED, got %v", resp["message_code"])
	}
}

// TestGrant_RejectControlCharsInReason reason 不能含换行 / Tab。
func TestGrant_RejectControlCharsInReason(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 0.0)
	pkg := seedPackage(t)
	app := newAdminGrantTestApp(admin)

	code, resp := doJSON(t, app, "POST", "/admin/sub/grant", map[string]any{
		"user_id":    user.ID,
		"package_id": pkg.ID,
		"reason":     "正常\n第二行",
	})
	if code != 400 {
		t.Fatalf("expected 400, got %d", code)
	}
	if resp["message_code"] != "ERR_REASON_CTRL_CHAR" {
		t.Errorf("expected ERR_REASON_CTRL_CHAR, got %v", resp["message_code"])
	}
}

// TestGrant_RejectReasonTooLong reason ≤ 500 rune。
func TestGrant_RejectReasonTooLong(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 0.0)
	pkg := seedPackage(t)
	app := newAdminGrantTestApp(admin)

	long := strings.Repeat("a", 501)
	code, resp := doJSON(t, app, "POST", "/admin/sub/grant", map[string]any{
		"user_id":    user.ID,
		"package_id": pkg.ID,
		"reason":     long,
	})
	if code != 400 {
		t.Fatalf("expected 400, got %d", code)
	}
	if resp["message_code"] != "ERR_REASON_TOO_LONG" {
		t.Errorf("expected ERR_REASON_TOO_LONG, got %v", resp["message_code"])
	}
}

// TestGrant_StackLimit 超过 stack 上限拒绝。
func TestGrant_StackLimit(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 0.0)
	pkg := seedPackage(t, func(p *database.Package) {
		p.MaxActivePerUser = 2 // 限 2 份
	})
	app := newAdminGrantTestApp(admin)

	// 先赠送 2 份（合法）
	code, _ := doJSON(t, app, "POST", "/admin/sub/grant", map[string]any{
		"user_id":    user.ID,
		"package_id": pkg.ID,
		"quantity":   2,
		"reason":     "前置赠送",
	})
	if code != 200 {
		t.Fatalf("seed grant 200 expected, got %d", code)
	}

	// 第 3 份应该被 stack limit 拒绝
	code, resp := doJSON(t, app, "POST", "/admin/sub/grant", map[string]any{
		"user_id":    user.ID,
		"package_id": pkg.ID,
		"quantity":   1,
		"reason":     "超额尝试",
	})
	if code != 409 {
		t.Fatalf("expected 409, got %d body=%v", code, resp)
	}
	if resp["message_code"] != "ERR_STACK_LIMIT" {
		t.Errorf("expected ERR_STACK_LIMIT, got %v", resp["message_code"])
	}
}

// TestGrant_ThenRefund_Rejected 关键安全测试：赠送的订阅不能被退款。
// 这验证 IsGranted=true 字段对 AdminRefundSubscription 的拒绝路径生效。
func TestGrant_ThenRefund_Rejected(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 0.0)
	pkg := seedPackage(t)

	// 1) admin 赠送
	grantApp := newAdminGrantTestApp(admin)
	code, resp := doJSON(t, grantApp, "POST", "/admin/sub/grant", map[string]any{
		"user_id":    user.ID,
		"package_id": pkg.ID,
		"reason":     "赠送测试",
	})
	if code != 200 {
		t.Fatalf("grant failed: %d body=%v", code, resp)
	}
	// 拿到刚赠送的 sub.id
	var sub database.UserSubscription
	database.DB.Where("user_id = ?", user.ID).First(&sub)

	// 2) admin 尝试退款 → 应该被拒绝
	refundApp := newAdminTestApp(admin)
	code, resp = doJSON(t, refundApp, "POST", "/admin/sub/"+itoaUint(sub.ID)+"/refund", map[string]any{
		"amount_micro_usd": 5 * database.MicroPerUSD,
		"reason":           "退个赠送的看会不会被拒",
	})
	if code != 400 {
		t.Fatalf("expected 400 for granted sub refund, got %d body=%v", code, resp)
	}
	if resp["message_code"] != "ERR_REFUND_GRANTED_SUB" {
		t.Errorf("expected ERR_REFUND_GRANTED_SUB, got %v", resp["message_code"])
	}

	// 3) 用户余额必须仍为 0（拒绝路径不能动钱）
	var fresh database.User
	database.DB.First(&fresh, user.ID)
	if fresh.Quota != 0.0 {
		t.Errorf("balance=%v after refused refund, want 0", fresh.Quota)
	}

	// 4) 订阅状态必须仍为 active（拒绝路径不能改状态）
	var subAfter database.UserSubscription
	database.DB.First(&subAfter, sub.ID)
	if subAfter.Status != "active" {
		t.Errorf("status=%q after refused refund, want active", subAfter.Status)
	}
}

// TestGrant_ThenRevoke_Success 赠送权益可以被 admin 收回；收回只改订阅状态，
// 不退款、不改变 user.Quota，并留下 0 金额账单 + 审计日志。
func TestGrant_ThenRevoke_Success(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 12.34)
	pkg := seedPackage(t)
	app := newAdminGrantTestApp(admin)

	code, resp := doJSON(t, app, "POST", "/admin/sub/grant", map[string]any{
		"user_id":    user.ID,
		"package_id": pkg.ID,
		"reason":     "内测补发",
	})
	if code != 200 {
		t.Fatalf("grant failed: %d body=%v", code, resp)
	}

	var sub database.UserSubscription
	if err := database.DB.Where("user_id = ?", user.ID).First(&sub).Error; err != nil {
		t.Fatalf("load granted sub: %v", err)
	}

	code, resp = doJSON(t, app, "POST", "/admin/sub/"+itoaUint(sub.ID)+"/revoke-grant", map[string]any{
		"reason": "发放错误，收回",
	})
	if code != 200 {
		t.Fatalf("expected revoke 200, got %d body=%v", code, resp)
	}
	if resp["message_code"] != "SUCCESS_GRANT_REVOKED" {
		t.Errorf("expected SUCCESS_GRANT_REVOKED, got %v", resp["message_code"])
	}

	var freshSub database.UserSubscription
	database.DB.First(&freshSub, sub.ID)
	if freshSub.Status != "revoked" {
		t.Errorf("status=%q, want revoked", freshSub.Status)
	}
	if !freshSub.IsGranted {
		t.Errorf("IsGranted=false after revoke, want true for audit trace")
	}
	if freshSub.CanceledAt == nil {
		t.Errorf("CanceledAt nil, want revoke timestamp")
	}

	var freshUser database.User
	database.DB.First(&freshUser, user.ID)
	wantQuota, _ := database.USDToMicro(12.34)
	if freshUser.Quota != wantQuota {
		t.Errorf("quota=%d, want unchanged %d", freshUser.Quota, wantQuota)
	}

	var revokeEntry database.BillingEntry
	if err := database.DB.Where("user_id = ? AND entry_type = ?", user.ID, database.BillingTypeAdminRevokeGrant).
		First(&revokeEntry).Error; err != nil {
		t.Fatalf("missing admin_revoke_grant billing entry: %v", err)
	}
	if revokeEntry.AmountUSD != 0 || revokeEntry.BalanceAfterUSD != wantQuota {
		t.Errorf("billing amount/balance=%d/%d, want 0/%d", revokeEntry.AmountUSD, revokeEntry.BalanceAfterUSD, wantQuota)
	}

	var logs []database.OperationLog
	database.DB.Where("target_user_id = ? AND action_type = ?", user.ID, "REVOKE_GRANTED_SUBSCRIPTION").Find(&logs)
	if len(logs) != 1 {
		t.Fatalf("got %d revoke audit logs, want 1", len(logs))
	}
	var details map[string]any
	if err := json.Unmarshal([]byte(logs[0].Details), &details); err != nil {
		t.Fatalf("revoke details not valid json: %v\n%s", err, logs[0].Details)
	}
	if details["reason"] != "发放错误，收回" {
		t.Errorf("reason=%v, want 发放错误，收回", details["reason"])
	}
}

// TestRevokeGrant_PaidSubscriptionRejected 收回入口只能用于 IsGranted=true 的记录；
// 付费订阅必须继续走退款/取消状态机，避免 admin 绕过退款审计。
func TestRevokeGrant_PaidSubscriptionRejected(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 0)
	pkg := seedPackage(t)
	app := newAdminGrantTestApp(admin)

	sub := database.UserSubscription{
		UserID:                user.ID,
		PackageID:             pkg.ID,
		PackageSnapshot:       `{"package_name":"paid","product_type":"subscription"}`,
		StartAt:               time.Now(),
		EndAt:                 time.Now().Add(24 * time.Hour),
		Status:                "active",
		PurchasedUnitPriceUSD: pkg.PriceAmount,
		IsGranted:             false,
	}
	if err := database.DB.Create(&sub).Error; err != nil {
		t.Fatalf("seed paid sub: %v", err)
	}

	code, resp := doJSON(t, app, "POST", "/admin/sub/"+itoaUint(sub.ID)+"/revoke-grant", map[string]any{
		"reason": "误操作测试",
	})
	if code != 400 {
		t.Fatalf("expected 400, got %d body=%v", code, resp)
	}
	if resp["message_code"] != "ERR_REVOKE_NOT_GRANTED" {
		t.Errorf("expected ERR_REVOKE_NOT_GRANTED, got %v", resp["message_code"])
	}

	var freshSub database.UserSubscription
	database.DB.First(&freshSub, sub.ID)
	if freshSub.Status != "active" {
		t.Errorf("status changed to %q, want active", freshSub.Status)
	}
	var count int64
	database.DB.Model(&database.BillingEntry{}).
		Where("user_id = ? AND entry_type = ?", user.ID, database.BillingTypeAdminRevokeGrant).
		Count(&count)
	if count != 0 {
		t.Errorf("admin_revoke_grant billing count=%d, want 0", count)
	}
}

// TestGrant_NonAdminUnauthorized 未认证（无 admin cookie）拒绝。
// 这覆盖 loadAdminUser==nil 的路径。
func TestGrant_NonAdminUnauthorized(t *testing.T) {
	setupSubTestDB(t)
	user := seedTestUser(t, 0.0)
	pkg := seedPackage(t)

	// 不挂 cookie 注入 → loadAdminUser 返回 nil
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Post("/admin/sub/grant", AdminGrantSubscription)

	code, resp := doJSON(t, app, "POST", "/admin/sub/grant", map[string]any{
		"user_id":    user.ID,
		"package_id": pkg.ID,
		"reason":     "无 admin",
	})
	if code != 401 {
		t.Fatalf("expected 401, got %d body=%v", code, resp)
	}
}

// TestGrant_DisabledPackage 禁用套餐拒绝。
func TestGrant_DisabledPackage(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 0.0)
	pkg := seedPackage(t, func(p *database.Package) {
		p.Enabled = boolPtr(false)
	})
	app := newAdminGrantTestApp(admin)

	code, resp := doJSON(t, app, "POST", "/admin/sub/grant", map[string]any{
		"user_id":    user.ID,
		"package_id": pkg.ID,
		"reason":     "禁用套餐",
	})
	if code != 400 {
		t.Fatalf("expected 400, got %d", code)
	}
	if resp["message_code"] != "ERR_PACKAGE_DISABLED" {
		t.Errorf("expected ERR_PACKAGE_DISABLED, got %v", resp["message_code"])
	}
}

// TestGrant_NonexistentUser user_id 不存在。
func TestGrant_NonexistentUser(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	pkg := seedPackage(t)
	app := newAdminGrantTestApp(admin)

	code, resp := doJSON(t, app, "POST", "/admin/sub/grant", map[string]any{
		"user_id":    99999,
		"package_id": pkg.ID,
		"reason":     "幽灵用户",
	})
	if code != 404 {
		t.Fatalf("expected 404, got %d", code)
	}
	if resp["message_code"] != "ERR_USER_NOT_FOUND" {
		t.Errorf("expected ERR_USER_NOT_FOUND, got %v", resp["message_code"])
	}
}

// TestGrant_RejectControlChars_Unicode 第二十轮加固：除 \r\n\t 外，所有 Unicode 控制字符也拒绝。
// 测试 NUL (\x00) 和 ESC (\x1b) 这类容易被忽略的低级字符。
func TestGrant_RejectControlChars_Unicode(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 0.0)
	pkg := seedPackage(t)
	app := newAdminGrantTestApp(admin)

	for _, badReason := range []string{
		"含 NUL\x00 字符",
		"含 ESC\x1b 终端伪造",
		"含 BEL\x07 蜂鸣",
	} {
		code, resp := doJSON(t, app, "POST", "/admin/sub/grant", map[string]any{
			"user_id":    user.ID,
			"package_id": pkg.ID,
			"reason":     badReason,
		})
		if code != 400 || resp["message_code"] != "ERR_REASON_CTRL_CHAR" {
			t.Errorf("reason=%q: expected 400 ERR_REASON_CTRL_CHAR, got %d %v", badReason, code, resp["message_code"])
		}
	}
}

// TestGrant_PackageInvalidNumeric 第二十轮加固：pkg 数值 invariant 损坏（DB 直改）后赠送路径拒绝。
func TestGrant_PackageInvalidNumeric(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 0.0)
	pkg := seedPackage(t) // PriceAmount=9.9
	// 模拟 admin 误操作 / DB 损坏让 price_amount 变负
	database.DB.Model(&database.Package{}).Where("id = ?", pkg.ID).
		UpdateColumn("price_amount", -5*database.MicroPerUSD)
	app := newAdminGrantTestApp(admin)

	code, resp := doJSON(t, app, "POST", "/admin/sub/grant", map[string]any{
		"user_id":    user.ID,
		"package_id": pkg.ID,
		"reason":     "测试负价格防御",
	})
	if code != 500 {
		t.Errorf("expected 500, got %d", code)
	}
	if resp["message_code"] != "ERR_PACKAGE_INVALID_NUMERIC" {
		t.Errorf("expected ERR_PACKAGE_INVALID_NUMERIC, got %v", resp["message_code"])
	}
}

// TestGrant_DoesNotBlockReclaim 第二十轮回归测试：
// 关键业务场景——用户先收到赠送，然后充值，然后申请充值退款 + reclaim_quota=true。
// 之前的 bug：reclaim 守卫扫"任何非 refunded 订阅"，会因为 IsGranted=true 永远不能 refund 而被永久阻塞。
// 修复后：守卫排除 IsGranted=true，赠送不再卡住合法的充值退款。
func TestGrant_DoesNotBlockReclaim(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 0.0)
	pkg := seedPackage(t)

	// 1) admin 赠送一份订阅给 user
	grantApp := newAdminGrantTestApp(admin)
	code, _ := doJSON(t, grantApp, "POST", "/admin/sub/grant", map[string]any{
		"user_id":    user.ID,
		"package_id": pkg.ID,
		"reason":     "回归测试",
	})
	if code != 200 {
		t.Fatalf("grant failed: %d", code)
	}

	// 2) 直接验证守卫的 SQL 条件：含 is_granted=false 时不应命中 granted sub
	var unrefunded []uint
	database.DB.Model(&database.UserSubscription{}).
		Where("user_id = ? AND status != ? AND is_granted = ?", user.ID, "refunded", false).
		Pluck("id", &unrefunded)
	if len(unrefunded) != 0 {
		t.Errorf("guard with is_granted=false hit %d granted subs (should be 0)", len(unrefunded))
	}

	// 对照：不加 is_granted 条件会命中（确认确实有数据）
	var allUnrefunded []uint
	database.DB.Model(&database.UserSubscription{}).
		Where("user_id = ? AND status != ?", user.ID, "refunded").
		Pluck("id", &allUnrefunded)
	if len(allUnrefunded) != 1 {
		t.Errorf("expected 1 unrefunded sub (the granted one), got %d", len(allUnrefunded))
	}
}

// TestGrant_AuditDetailsContainsContext OperationLog 必须含 sub_ids / reason / pkg / qty。
func TestGrant_AuditDetailsContainsContext(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 0.0)
	pkg := seedPackage(t)
	app := newAdminGrantTestApp(admin)

	doJSON(t, app, "POST", "/admin/sub/grant", map[string]any{
		"user_id":    user.ID,
		"package_id": pkg.ID,
		"quantity":   2,
		"reason":     "审计测试",
	})

	var auditRow database.OperationLog
	database.DB.Where("target_user_id = ? AND action_type = ?",
		user.ID, "GRANT_SUBSCRIPTION").First(&auditRow)
	if auditRow.OperatorID != admin.ID {
		t.Errorf("OperatorID=%d, want %d", auditRow.OperatorID, admin.ID)
	}
	// details 是 JSON 字符串，至少能解析出 reason 字段
	var arr []map[string]any
	if err := json.Unmarshal([]byte(auditRow.Details), &arr); err != nil {
		t.Fatalf("details not valid json: %v\n%s", err, auditRow.Details)
	}
	if len(arr) == 0 {
		t.Fatalf("details array empty")
	}
	if arr[0]["reason"] != "审计测试" {
		t.Errorf("reason=%v, want 审计测试", arr[0]["reason"])
	}
	if arr[0]["quantity"].(float64) != 2 {
		t.Errorf("quantity=%v, want 2", arr[0]["quantity"])
	}
}
