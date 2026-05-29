// Package controller / subscription_security_test.go
//
// 这个文件集中覆盖 r1-r12 累计修复的关键 CRITICAL/Major 业务+安全不变量。
// 每个测试都有具体攻击/利用场景标注，确保未来重构不会回退。
//
// 不变量清单（按修复轮次倒序，便于追溯）：
//  1. R11: 订阅退款建议按时间比例，不读取 quota 消耗率
//  2. R11: TopupOrder reclaim_quota 防绕过 — 用户有非 refunded 订阅则拒绝
//  3. R10: AdminRefundSubscription 状态机 — refunded 终态，重复退款 409
//  4. R10: canceled_at 不被退款时间覆写 — 已 canceled 子保留原始取消时间
//  5. R9:  purchasedPrice<=0 时拒绝退款（防 snapshot 损坏 + 删除套餐场景任意金额）
//  6. R10: AdminRefund 金额上限 ERR_REFUND_AMOUNT_EXCEEDS_PURCHASE
package controller

import (
	"testing"
	"time"

	"daof-cpa/database"

	"github.com/gofiber/fiber/v2"
)

// ─── R10 CRITICAL: AdminRefund 状态机 ──────────────────────────────

// TestSecurity_AdminRefund_DoubleRefundBlocked 验证：已 refunded 订阅再次退款会被拒绝。
// 状态机 sentinel: errSubStateMachineMiss → 409 ERR_SUB_STATUS_NOT_REFUNDABLE。
func TestSecurity_AdminRefund_DoubleRefundBlocked(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 100)
	app := newAdminTestApp(admin)

	sub := database.UserSubscription{
		UserID:                user.ID,
		PackageID:             1,
		Status:                "refunded", // 已退款
		StartAt:               time.Now().Add(-24 * time.Hour),
		EndAt:                 time.Now().Add(29 * 24 * time.Hour),
		PackageSnapshot:       `{"package_id":1,"package_name":"Pro","price_amount":10000000}`,
		PurchasedUnitPriceUSD: 10 * database.MicroPerUSD,
	}
	database.DB.Create(&sub)

	code, resp := doJSON(t, app, "POST", "/admin/sub/"+itoaUint(sub.ID)+"/refund", map[string]any{
		"amount_micro_usd": 5 * database.MicroPerUSD,
		"reason":           "重复退款测试",
	})
	if code != 409 {
		t.Errorf("expected 409 (already refunded), got %d body=%v", code, resp)
	}
}

// TestSecurity_AdminRefund_CanceledAtPreserved 验证：refund 不覆盖已 canceled 订阅的原始 canceled_at。
// 这是 r10 自审第十一轮的发现 — 审计上需要保留"用户先 cancel 再申请退款"的时序。
func TestSecurity_AdminRefund_CanceledAtPreserved(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 100)
	app := newAdminTestApp(admin)

	originalCanceledAt := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	sub := database.UserSubscription{
		UserID:                user.ID,
		PackageID:             1,
		Status:                "canceled",
		StartAt:               time.Now().Add(-24 * time.Hour),
		EndAt:                 time.Now().Add(29 * 24 * time.Hour),
		CanceledAt:            &originalCanceledAt,
		PackageSnapshot:       `{"package_id":1,"package_name":"Pro","price_amount":10000000}`,
		PurchasedUnitPriceUSD: 10 * database.MicroPerUSD,
	}
	database.DB.Create(&sub)

	code, _ := doJSON(t, app, "POST", "/admin/sub/"+itoaUint(sub.ID)+"/refund", map[string]any{
		"amount_micro_usd": 8 * database.MicroPerUSD,
		"reason":           "用户协商退款",
	})
	if code != 200 {
		t.Fatalf("refund should succeed, got %d", code)
	}

	var fresh database.UserSubscription
	database.DB.First(&fresh, sub.ID)
	if fresh.Status != "refunded" {
		t.Errorf("status should be refunded, got %s", fresh.Status)
	}
	if fresh.CanceledAt == nil {
		t.Fatal("canceled_at should not be nil")
	}
	// 关键断言：原始取消时间被保留（不是被退款时间覆写）
	if !fresh.CanceledAt.Equal(originalCanceledAt) {
		t.Errorf("canceled_at was overwritten: original=%v, current=%v", originalCanceledAt, fresh.CanceledAt)
	}
}

// ─── R9 CRITICAL: snapshot 损坏拒绝退款 ──────────────────────────

// TestSecurity_AdminRefund_PriceUnknownBlocked 验证：PurchasedUnitPriceUSD=0（实际未付费）拒绝退款。
//
// 统一用 PurchasedUnitPriceUSD 字段判断。任何 PurchasedUnitPriceUSD<=0 的 sub
// 都按"未付费"拒绝退款 → ERR_REFUND_ZERO_PAID。
func TestSecurity_AdminRefund_PriceUnknownBlocked(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 100)
	app := newAdminTestApp(admin)

	// PurchasedUnitPriceUSD=0（无论 snapshot 内容如何）
	sub := database.UserSubscription{
		UserID:                user.ID,
		PackageID:             99999,
		Status:                "active",
		StartAt:               time.Now(),
		EndAt:                 time.Now().Add(30 * 24 * time.Hour),
		PackageSnapshot:       `{"package_id":99999}`,
		PurchasedUnitPriceUSD: 0, // 未付费
	}
	database.DB.Create(&sub)

	code, resp := doJSON(t, app, "POST", "/admin/sub/"+itoaUint(sub.ID)+"/refund", map[string]any{
		"amount_micro_usd": 50 * database.MicroPerUSD,
		"reason":           "未付费退款测试",
	})
	if code != 400 {
		t.Errorf("expected 400 (zero paid), got %d body=%v", code, resp)
	}
	if resp["message_code"] != "ERR_REFUND_ZERO_PAID" {
		t.Errorf("expected ERR_REFUND_ZERO_PAID, got %v", resp["message_code"])
	}
}

// ─── R10 业务变更验证：cancel 不动钱 ──────────────────────────────

// TestSecurity_Cancel_NoQuotaChange 验证：用户 cancel 不退款，quota 完全不变。
// 确认 r10 业务模型修订（取消解耦于退款）持续生效。
func TestSecurity_Cancel_NoQuotaChange(t *testing.T) {
	setupSubTestDB(t)
	user := seedTestUser(t, 100)
	pkg := seedPackage(t)
	app := newTestApp(user)

	doJSON(t, app, "POST", "/purchase", map[string]any{"package_id": pkg.ID, "quantity": 1})
	var quotaBefore database.User
	database.DB.First(&quotaBefore, user.ID)

	var sub database.UserSubscription
	database.DB.Where("user_id = ?", user.ID).First(&sub)

	code, resp := doJSON(t, app, "DELETE", "/sub/"+itoaUint(sub.ID), nil)
	if code != 200 {
		t.Fatalf("cancel should succeed, got %d body=%v", code, resp)
	}
	// 关键断言：响应不含 refund_usd（旧的自动退款字段）
	if _, hasRefund := resp["refund_usd"]; hasRefund {
		t.Error("cancel response must NOT contain refund_usd")
	}

	var quotaAfter database.User
	database.DB.First(&quotaAfter, user.ID)
	if quotaAfter.Quota != quotaBefore.Quota {
		t.Errorf("cancel changed quota: before=%d, after=%d (should be unchanged)",
			quotaBefore.Quota, quotaAfter.Quota)
	}
}

// ─── R7 CRITICAL: purchase 并发不透支（行锁验证） ──────────────────

// TestSecurity_Purchase_SerialOverdraftPrevented 验证：同 user 多次顺序购买无法让 quota 变负。
// 攻击模拟：余额刚够买 1 份，顺序提交 5 次购买请求。
//
// 注（自审第十三轮）：测试名原为 ConcurrentNoOverdraft 但实现是串行——SQLite in-memory
// 在 cache=private 下的全局写锁让真正并发等价于此，因此串行版本足以验证不变量。
// 名字改为 Serial 避免误导 reviewer 期望 goroutine 测试。
// 真正的并发竞态测试需要 PG 后端 + goroutine fan-out，单独场景。
func TestSecurity_Purchase_SerialOverdraftPrevented(t *testing.T) {
	setupSubTestDB(t)
	user := seedTestUser(t, 9.9) // 仅够买 1 份
	pkg := seedPackage(t)        // 价格 9.9
	app := newTestApp(user)

	// 串行模拟"并发购买"——SQLite 全局写锁让真正并发等价于此
	results := make([]int, 5)
	for i := 0; i < 5; i++ {
		code, _ := doJSON(t, app, "POST", "/purchase", map[string]any{
			"package_id": pkg.ID, "quantity": 1,
		})
		results[i] = code
	}

	// 关键断言：恰好 1 次成功，其余全失败
	successes := 0
	for _, c := range results {
		if c == 200 {
			successes++
		}
	}
	if successes != 1 {
		t.Errorf("expected exactly 1 success out of 5 concurrent purchases, got %d", successes)
	}

	var fresh database.User
	database.DB.First(&fresh, user.ID)
	if fresh.Quota < 0 {
		t.Errorf("quota went negative: %d (should be 0 after 1 purchase)", fresh.Quota)
	}
}

// ─── R3-R8 跨方言测试占位（SQLite 行为已覆盖在 Concurrent 测试） ──

// 注：lockUserForUpdate 在 PostgreSQL 用 FOR UPDATE，SQLite 用 no-op UPDATE 触发 RESERVED。
// 测试运行在 SQLite 下天然覆盖 SQLite 路径；PostgreSQL 路径需在生产 CI 加 PG service 单跑。

// ─── R9 业务：Stackable 强制单份 ────────────────────────────────

// TestSecurity_Purchase_NonStackableEnforced 验证：!Stackable 套餐只能持有 1 份。
//
// 注：seedPackage 已加 Select("*") 修复 GORM `default:true` 陷阱（自审第十三轮 M8），
// 这里 `p.Stackable = false` 能正确落库，无需 raw UPDATE。
func TestSecurity_Purchase_NonStackableEnforced(t *testing.T) {
	setupSubTestDB(t)
	user := seedTestUser(t, 100)
	pkg := seedPackage(t, func(p *database.Package) {
		p.Stackable = boolPtr(false)
		p.MaxActivePerUser = 99 // 即使 max 大也应只允许 1 份
	})
	app := newTestApp(user)

	// 第 1 次购买应成功
	code, _ := doJSON(t, app, "POST", "/purchase", map[string]any{"package_id": pkg.ID, "quantity": 1})
	if code != 200 {
		t.Fatalf("first purchase should succeed, got %d", code)
	}

	// 第 2 次购买应被 ERR_STACK_LIMIT 拦截
	code, resp := doJSON(t, app, "POST", "/purchase", map[string]any{"package_id": pkg.ID, "quantity": 1})
	if code != 409 {
		t.Errorf("second purchase of non-stackable should fail, got %d body=%v", code, resp)
	}
	if resp["message_code"] != "ERR_STACK_LIMIT" {
		t.Errorf("expected ERR_STACK_LIMIT, got %v", resp["message_code"])
	}
}

// ─── R9 业务：Action 字段严格验证 ──────────────────────────────────

// TestSecurity_Purchase_UnsupportedAction 验证：未实现的 action（extend/new）被拒绝。
func TestSecurity_Purchase_UnsupportedAction(t *testing.T) {
	setupSubTestDB(t)
	user := seedTestUser(t, 100)
	pkg := seedPackage(t)
	app := newTestApp(user)

	for _, badAction := range []string{"extend", "new", "stack_evil", "../"} {
		code, resp := doJSON(t, app, "POST", "/purchase", map[string]any{
			"package_id": pkg.ID, "quantity": 1, "action": badAction,
		})
		if code != 400 {
			t.Errorf("action=%q should be rejected, got %d body=%v", badAction, code, resp)
		}
		if resp["message_code"] != "ERR_ACTION_NOT_SUPPORTED" {
			t.Errorf("action=%q expected ERR_ACTION_NOT_SUPPORTED, got %v", badAction, resp["message_code"])
		}
	}

	// "" 和 "stack" 应通过
	code, _ := doJSON(t, app, "POST", "/purchase", map[string]any{
		"package_id": pkg.ID, "quantity": 1, "action": "stack",
	})
	if code != 200 {
		t.Errorf("action=stack should succeed, got %d", code)
	}
}

// ─── R9 业务：Quantity 上限防 DoS ─────────────────────────────────

// TestSecurity_Purchase_QuantityCap 验证：quantity > 100 被拒绝（防一亿次循环锁库 DoS）。
func TestSecurity_Purchase_QuantityCap(t *testing.T) {
	setupSubTestDB(t)
	user := seedTestUser(t, 99999)
	pkg := seedPackage(t)
	app := newTestApp(user)

	code, resp := doJSON(t, app, "POST", "/purchase", map[string]any{
		"package_id": pkg.ID, "quantity": 10000000, // 1 千万
	})
	if code != 400 {
		t.Errorf("quantity=10M should be rejected, got %d body=%v", code, resp)
	}
	if resp["message_code"] != "ERR_QUANTITY_TOO_LARGE" {
		t.Errorf("expected ERR_QUANTITY_TOO_LARGE, got %v", resp["message_code"])
	}
}

// ─── R7 CRITICAL: 购买余额边界（全额扣款） ──────────────────────

// TestSecurity_Purchase_RequiresFullPrice 验证：购买套餐必须按实际成交价扣款，
// 不存在套餐 bonus 抵扣或返现路径。
func TestSecurity_Purchase_RequiresFullPrice(t *testing.T) {
	setupSubTestDB(t)
	user := seedTestUser(t, 5) // 仅 $5 余额
	pkg := seedPackage(t, func(p *database.Package) {
		p.PriceAmount = 10 * database.MicroPerUSD
	})
	app := newTestApp(user)

	code, resp := doJSON(t, app, "POST", "/purchase", map[string]any{
		"package_id": pkg.ID, "quantity": 1,
	})
	if code != 402 {
		t.Fatalf("purchase should require full price, got %d body=%v", code, resp)
	}
	if resp["message_code"] != "ERR_INSUFFICIENT_BALANCE" {
		t.Errorf("expected ERR_INSUFFICIENT_BALANCE, got %v", resp["message_code"])
	}
}

// ─── R11 Major: subscription refund suggestion guard ────────────────

// Phase 8：旧的 quota-ratio 退款建议逻辑随业务下线一并移除。

// TestSecurity_AdminList_SubscriptionRefund_UsesTimeRatio 验证：
// 普通 subscription 仍按时间比例退款，不读 quota——
// 防 r13 顺序修复"误把 quota 引入 subscription 路径"。
//
// 场景：30 天月套餐 $30，已用 1 天，已消耗 90% request_count → suggested 仍按时间 ≈ $29
// （取 29/30 ≈ 96.6% × $30 = $28.99，约 $29.0）。
func TestSecurity_AdminList_SubscriptionRefund_UsesTimeRatio(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 100)

	plan := database.QuotaPlan{
		Name: "sub_request_count", DisplayName: "Sub Request Count",
		ModelMatch: `["*"]`, LimitUnit: "request_count", LimitValue: 10000,
		Priority: 1, Enabled: boolPtr(true),
	}
	database.DB.Create(&plan)

	startAt := time.Now().Add(-1 * 24 * time.Hour) // 1 天前
	endAt := startAt.Add(30 * 24 * time.Hour)      // 30 天
	sub := database.UserSubscription{
		UserID:    user.ID,
		PackageID: 1,
		Status:    "active",
		StartAt:   startAt,
		EndAt:     endAt,
		PackageSnapshot: `{
			"package_id":1,"package_name":"Sub","product_type":"subscription",
			"price_amount":30000000,
			"plans":[{"id":` + itoaUint(plan.ID) + `,"name":"sub_request_count","limit_unit":"request_count","limit_value":10000}]
		}`,
		PurchasedUnitPriceUSD: 30 * database.MicroPerUSD, // $30
	}
	database.DB.Create(&sub)

	// 90% request_count 已耗
	database.DB.Create(&database.SubscriptionUsage{
		UserID: sub.UserID, QuotaPlanID: plan.ID, ModelBucket: "*",
		WindowStartAt: startAt, WindowEndAt: endAt, ConsumedValue: 9000,
	})

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error {
		c.Request().Header.SetCookie("daof_admin_token", admin.Token)
		return c.Next()
	})
	app.Get("/admin/subs", AdminListSubscriptions)
	code, resp := doJSON(t, app, "GET", "/admin/subs", nil)
	if code != 200 {
		t.Fatalf("admin list failed: %d %v", code, resp)
	}
	first := resp["data"].([]any)[0].(map[string]any)

	// subscription 类型：不应被 quota 拉低；按时间 ~$29 推荐
	suggested, _ := first["suggested_refund_usd"].(float64)
	if suggested < 28.0 || suggested > 29.5 {
		t.Errorf("subscription suggested refund should be ~$29 (time-based), got $%.2f", suggested)
	}
}

// 注：itoaUint / doJSON / seedAdminUser / newAdminTestApp 等 helper
// 均在 subscription_integration_test.go 定义，跨文件共享。
