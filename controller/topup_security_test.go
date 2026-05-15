// Package controller / topup_security_test.go
//
// 覆盖充值/退款相关的 CRITICAL/Major 安全不变量。
// 见 subscription_security_test.go 的设计说明（同一套 helper 复用）。
package controller

import (
	"bytes"
	"net/http/httptest"
	"testing"

	"daof-ai-hub/database"
	"daof-ai-hub/middleware"

	"github.com/gofiber/fiber/v2"
)

// newAdminTopupTestApp 注册 admin topup 退款路由 + 注入 admin token。
// fix MAJOR M22-A3（codex 第二十二轮）：内置真实 AdminGuard
func newAdminTopupTestApp(admin *database.User) *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error {
		c.Request().Header.SetCookie("daof_admin_token", admin.Token)
		return c.Next()
	})
	app.Use(middleware.AdminGuard)
	app.Post("/admin/topup/orders/:id/refund", AdminRefundTopup)
	return app
}

// seedPaidTopupOrder 创建一个已支付的 topup 订单。
// rmb 单位 RMB（人友好），内部转 fen。
func seedPaidTopupOrder(t *testing.T, userID uint, rmb float64) *database.TopupOrder {
	t.Helper()
	rmbFen, _ := database.RMBToFen(rmb)
	amountMicro, _ := database.USDToMicro(round2(rmb / 7.2))
	o := database.TopupOrder{
		OutTradeNo:           "tp_test_" + itoaUint(userID),
		UserID:               userID,
		MoneyRMB:             rmbFen,
		AmountUSD:            amountMicro,
		ExchangeRateRmbPerUsdMicros: 7_200_000, // ¥7.2 = $1
		Status:               "paid",
	}
	if err := database.DB.Create(&o).Error; err != nil {
		t.Fatalf("seed topup: %v", err)
	}
	return &o
}

// ─── R11 CRITICAL: TopupOrder reclaim_quota 受未退订阅块防护 ───────

// TestSecurity_TopupRefund_ReclaimBlockedByActiveSub 验证：
// 用户用充值买了 active 订阅后，admin 退充值 reclaim_quota=true 必须被拒，
// 防止 quota 变负 + 订阅仍消费 plan 额度的"白嫖"。
//
// 攻击路径（codex r11）：
//
//	¥72 充值 → +$10 → 买 $10 月套餐（含 plan 额度）→
//	admin 退 ¥72 reclaim_quota=true → quota=-10 但 active 订阅仍持续消费
//	= 用户白嫖 plan 额度
//
// 防护：状态机检查所有 sub.status != 'refunded' 即拒绝（含 paused/canceled/expired）。
func TestSecurity_TopupRefund_ReclaimBlockedByActiveSub(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 0) // 用户已用完 quota（买完订阅后）
	app := newAdminTopupTestApp(admin)

	order := seedPaidTopupOrder(t, user.ID, 72.0) // ¥72 = $10

	// 用户当前持有 1 个 active 订阅
	sub := database.UserSubscription{
		UserID:          user.ID,
		PackageID:       1,
		Status:          "active",
		PackageSnapshot: `{"price_amount":10.0}`,
	}
	database.DB.Create(&sub)

	code, resp := doJSON(t, app, "POST",
		"/admin/topup/orders/"+itoaUint(order.ID)+"/refund",
		map[string]any{"money_rmb": 0, "reclaim_quota": true, "external_refund_ref": "rext_active"})

	if code != 409 {
		t.Errorf("expected 409 (unrefunded subs block), got %d body=%v", code, resp)
	}
	if resp["message_code"] != "ERR_USER_HAS_UNREFUNDED_SUBSCRIPTIONS" {
		t.Errorf("expected ERR_USER_HAS_UNREFUNDED_SUBSCRIPTIONS, got %v", resp["message_code"])
	}
	// 关键：active_subscription_ids 列表必须返回，让前端能引导 admin 先处理订阅
	ids, ok := resp["active_subscription_ids"].([]any)
	if !ok || len(ids) != 1 {
		t.Errorf("expected 1 active_subscription_id, got %v", resp["active_subscription_ids"])
	}

	// 验证：网关未被调用（订单仍保持 paid）
	var fresh database.TopupOrder
	database.DB.First(&fresh, order.ID)
	if fresh.Status != "paid" {
		t.Errorf("status changed to %s—gateway must NOT be called when blocked", fresh.Status)
	}
}

// TestSecurity_TopupRefund_ReclaimBlockedByPausedSub 验证：
// paused 状态订阅也应阻止 reclaim_quota（r12 修订把检查从 status='active' 改成 status != 'refunded'）。
//
// 旧实现仅查 active，paused 用户能绕过保护。
func TestSecurity_TopupRefund_ReclaimBlockedByPausedSub(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 0)
	app := newAdminTopupTestApp(admin)
	order := seedPaidTopupOrder(t, user.ID, 72.0)

	// paused 而非 active
	sub := database.UserSubscription{
		UserID:          user.ID,
		PackageID:       1,
		Status:          "paused",
		PackageSnapshot: `{"price_amount":10.0}`,
	}
	database.DB.Create(&sub)

	code, resp := doJSON(t, app, "POST",
		"/admin/topup/orders/"+itoaUint(order.ID)+"/refund",
		map[string]any{"reclaim_quota": true, "external_refund_ref": "rext_paused"})
	if code != 409 {
		t.Errorf("paused sub should also block reclaim, got %d body=%v", code, resp)
	}
	if resp["message_code"] != "ERR_USER_HAS_UNREFUNDED_SUBSCRIPTIONS" {
		t.Errorf("expected ERR_USER_HAS_UNREFUNDED_SUBSCRIPTIONS, got %v", resp["message_code"])
	}
}

// TestSecurity_TopupRefund_ReclaimAllowedWhenAllRefunded 验证：
// 所有订阅都 refunded 时，reclaim_quota 应被允许（继续到网关 ERR_GATEWAY_REJECT，因测试无网关）。
//
// 第十七轮：手动退款工作流改造后，登记成功 = 200 + 订单 status 变为 refunded / paid（部分退款）+
// BillingEntry 写入。不再涉及网关 rollback 路径。
func TestSecurity_TopupRefund_ReclaimAllowedWhenAllRefunded(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 0)
	app := newAdminTopupTestApp(admin)
	order := seedPaidTopupOrder(t, user.ID, 72.0)

	sub := database.UserSubscription{
		UserID: user.ID, PackageID: 1, Status: "refunded",
		PackageSnapshot: `{"price_amount":10.0}`,
	}
	database.DB.Create(&sub)

	code, resp := doJSON(t, app, "POST",
		"/admin/topup/orders/"+itoaUint(order.ID)+"/refund",
		map[string]any{"reclaim_quota": true, "external_refund_ref": "rext_123"})

	// 关键：不是 R11 守卫错误（否则说明守卫过严）
	if code == 409 && resp["message_code"] == "ERR_USER_HAS_UNREFUNDED_SUBSCRIPTIONS" {
		t.Fatalf("refunded sub must NOT block reclaim, got %v", resp)
	}
	// 手动退款工作流：登记成功
	if code != 200 {
		t.Fatalf("expected 200 after manual refund mark, got %d body=%v", code, resp)
	}
	// 订单状态 → refunded
	var fresh database.TopupOrder
	database.DB.First(&fresh, order.ID)
	if fresh.Status != "refunded" {
		t.Errorf("order status=%q want refunded", fresh.Status)
	}
	// 外部退款单号写入
	if fresh.RefundNo != "rext_123" {
		t.Errorf("refund_no=%q want rext_123", fresh.RefundNo)
	}
	// quota 扣回（用户原本 0，退款 ~$10 → -10 USD = -10_000_000 micro_usd）
	var u database.User
	database.DB.First(&u, user.ID)
	if u.Quota > int64(-9_500_000) || u.Quota < int64(-10_500_000) {
		t.Errorf("user quota=%d want around -10_000_000 micro_usd (reclaimed)", u.Quota)
	}
	// BillingEntry 写入
	var billCount int64
	database.DB.Model(&database.BillingEntry{}).
		Where("user_id = ? AND entry_type = ?", user.ID, database.BillingTypeRefundTopup).
		Count(&billCount)
	if billCount != 1 {
		t.Errorf("expected 1 refund_topup billing entry, got %d", billCount)
	}
}

// TestSecurity_TopupRefund_NonReclaimSkipsCheck 验证：
// reclaim_quota=false（仅退款，不动 quota）时，不应执行未退订阅检查。
//
// 第十七轮：手动退款工作流下，仅退款 = 200 + 订单 refunded + quota 不变 + 账单 0 USD。
func TestSecurity_TopupRefund_NonReclaimSkipsCheck(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 0)
	app := newAdminTopupTestApp(admin)
	order := seedPaidTopupOrder(t, user.ID, 72.0)

	sub := database.UserSubscription{
		UserID: user.ID, PackageID: 1, Status: "active",
		PackageSnapshot: `{"price_amount":10.0}`,
	}
	database.DB.Create(&sub)

	code, resp := doJSON(t, app, "POST",
		"/admin/topup/orders/"+itoaUint(order.ID)+"/refund",
		map[string]any{"reclaim_quota": false, "external_refund_ref": "rext_nonreclaim"})

	if code == 409 && resp["message_code"] == "ERR_USER_HAS_UNREFUNDED_SUBSCRIPTIONS" {
		t.Fatalf("reclaim_quota=false should NOT trigger guard, got %v", resp)
	}
	if code != 200 {
		t.Fatalf("expected 200 (manual mark allowed), got %d body=%v", code, resp)
	}
	// 关键：reclaim_quota=false 时，user.quota 不能变（仅"钱回"，不扣余额）
	var u database.User
	database.DB.First(&u, user.ID)
	if u.Quota != 0 {
		t.Errorf("non-reclaim refund must NOT change user quota; got %d", u.Quota)
	}
}

// fix C3 第二十轮: external_refund_ref 必填校验
// fix MAJOR M-A4（codex 第二十一轮）：CSRF / 鉴权 / status=1 经过真实 AdminGuard。
// 这些路径如果在 admin handler 测试里被绕过，CSRF 等回归无法被高价值业务测试发现。
func TestSecurity_TopupRefund_CSRF_RealAdminGuard(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 0)
	app := newRealAdminApp(admin)
	app.Post("/admin/topup/orders/:id/refund", AdminRefundTopup)
	order := seedPaidTopupOrder(t, user.ID, 72.0)

	// 阴性 1：跨源 Origin 应被 AdminGuard CSRF 拦截 → 403 ERR_CSRF_ORIGIN_MISMATCH
	req := httptest.NewRequest("POST",
		"/admin/topup/orders/"+itoaUint(order.ID)+"/refund",
		bytes.NewReader([]byte(`{"reclaim_quota":false,"external_refund_ref":"rext_csrf"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://evil.example.org")
	req.Host = "example.com"
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Errorf("cross-origin write should be 403 (CSRF), got %d", resp.StatusCode)
	}

	// 阴性 2：缺 Origin 也应被拦截（admin 写操作必有浏览器标头）
	req2 := httptest.NewRequest("POST",
		"/admin/topup/orders/"+itoaUint(order.ID)+"/refund",
		bytes.NewReader([]byte(`{"reclaim_quota":false,"external_refund_ref":"rext_csrf2"}`)))
	req2.Header.Set("Content-Type", "application/json")
	req2.Host = "example.com"
	resp2, _ := app.Test(req2, -1)
	defer resp2.Body.Close()
	if resp2.StatusCode != 403 {
		t.Errorf("missing Origin should be 403 (CSRF), got %d", resp2.StatusCode)
	}

	// 阳性 3：同源请求应通过 CSRF（doJSON 默认带 Origin: http://example.com 配 Host: example.com）
	code, resp3 := doJSON(t, app, "POST",
		"/admin/topup/orders/"+itoaUint(order.ID)+"/refund",
		map[string]any{"reclaim_quota": false, "external_refund_ref": "rext_ok"})
	if code != 200 {
		t.Errorf("same-origin request should pass CSRF, got %d body=%v", code, resp3)
	}
}

func TestSecurity_TopupRefund_RequiresExternalRef(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 0)
	app := newAdminTopupTestApp(admin)
	order := seedPaidTopupOrder(t, user.ID, 72.0)

	cases := []struct {
		name string
		ref  any
	}{
		{"missing field", nil},
		{"empty string", ""},
		{"whitespace only", "   "},
		{"control chars only", "\n\r\t"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]any{"reclaim_quota": false}
			if tc.ref != nil {
				body["external_refund_ref"] = tc.ref
			}
			code, resp := doJSON(t, app, "POST",
				"/admin/topup/orders/"+itoaUint(order.ID)+"/refund", body)
			if code != 400 {
				t.Errorf("expected 400 got %d body=%v", code, resp)
			}
			if resp["message_code"] != "ERR_EXTERNAL_REF_REQUIRED" {
				t.Errorf("expected ERR_EXTERNAL_REF_REQUIRED got %v", resp["message_code"])
			}
		})
	}
}
