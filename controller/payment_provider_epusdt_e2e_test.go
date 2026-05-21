// Package controller / payment_provider_epusdt_e2e_test.go
//
// W-4-Manual E2E 测试（2026-05-21）：覆盖完整 "下单 → 邮件通知 → admin 标记到账 → 用户 quota+=" 链路。
//
// 这是用户"我可以不用，但不能没有"心智的兜底——确保 manual 模式真能跑通：
//   1. admin 配 SysConfig（mode=manual + 邮箱 + TRC20 地址）
//   2. 用户 POST /topup/create 下单
//   3. 后端返 pay_info JSON（含 receive_address / actual_amount / 过期时间）
//   4. 邮件入队（subject 含订单号 + 金额 + 链；body 含完整对账信息）
//   5. admin POST /admin/topup/orders/:id/mark-paid（用 ExternalTradeRef = 链上 tx hash）
//   6. 验证：order.status=paid、user.quota+=、user.paid_quota+=、BillingEntry 写入
package controller

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"daof-cpa/database"
	"daof-cpa/middleware"
	"daof-cpa/proxy"
	"daof-cpa/utils"

	"github.com/gofiber/fiber/v2"
)

// captureEmailsForTest 拦截 SMTP send 让测试可断言邮件内容。
// 返回 sink 函数 + 一个会自动清理 cleanup 函数。
func captureEmailsForTest(t *testing.T) (sink func() []proxy.EmailMessage, cleanup func()) {
	t.Helper()
	var mu sync.Mutex
	var captured []proxy.EmailMessage

	proxy.SetEmailQueueSyncForTest(true)
	proxy.SetSendEmailViaSMTPHookForTest(func(cfg proxy.SMTPConfig, msg proxy.EmailMessage) error {
		mu.Lock()
		defer mu.Unlock()
		captured = append(captured, msg)
		return nil
	})
	// 配齐 SMTP 让 SMTPConfig.IsConfigured 返 true，让邮件流程跑到 SMTP hook
	// （hook 已 stub 不会真的拨号）。Password 必须加密存储否则 LoadSMTPConfig 不通过。
	utils.InitCrypto()
	encPwd, encErr := utils.Encrypt("test-fake-pwd")
	if encErr != nil {
		t.Fatalf("init encryption for SMTP password failed: %v", encErr)
	}
	proxy.SysConfigMutex.Lock()
	proxy.SysConfigCache["smtp_host"] = "localhost"
	proxy.SysConfigCache["smtp_port"] = "587"
	proxy.SysConfigCache["smtp_username"] = "test@example.com"
	proxy.SysConfigCache["smtp_password"] = encPwd
	proxy.SysConfigCache["smtp_from"] = "test@example.com"
	proxy.SysConfigMutex.Unlock()

	sink = func() []proxy.EmailMessage {
		mu.Lock()
		defer mu.Unlock()
		out := make([]proxy.EmailMessage, len(captured))
		copy(out, captured)
		return out
	}
	// W-4-Manual Tier 3 M-1/M-2 修复（2026-05-21）：cleanup 显式清掉 SMTP 配置和加密缓存，
	// 避免下个测试继承本测试的 SMTP 状态（导致 SMTP-not-configured 场景测试错过分支）。
	cleanup = func() {
		proxy.SetEmailQueueSyncForTest(false)
		proxy.SetSendEmailViaSMTPHookForTest(nil)
		proxy.SysConfigMutex.Lock()
		delete(proxy.SysConfigCache, "smtp_host")
		delete(proxy.SysConfigCache, "smtp_port")
		delete(proxy.SysConfigCache, "smtp_username")
		delete(proxy.SysConfigCache, "smtp_password")
		delete(proxy.SysConfigCache, "smtp_from")
		proxy.SysConfigMutex.Unlock()
	}
	return sink, cleanup
}

// newTopupE2EApp 组装一个支持 user 下单 + admin 标记到账的测试 app。
// W-4-Manual E2E 专用：把两条路径（user / admin）都挂上，模拟真实路由。
func newTopupE2EApp(user, admin *database.User) *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})

	// user-side 路由组（注入 user.Locals）
	userGroup := app.Group("/", func(c *fiber.Ctx) error {
		c.Locals("user", user)
		return c.Next()
	})
	userGroup.Post("/topup/create", CreateTopup)
	userGroup.Get("/topup/mine", MyTopupOrders)

	// admin-side 路由组（Cookie + AdminGuard）
	adminGroup := app.Group("/admin",
		func(c *fiber.Ctx) error {
			c.Request().Header.SetCookie("daof_admin_token", admin.Token)
			return c.Next()
		},
		middleware.AdminGuard,
	)
	adminGroup.Post("/topup/orders/:id/mark-paid", AdminMarkTopupPaid)

	return app
}

func TestEpusdtManual_E2E_FullHappyPath(t *testing.T) {
	setupSubTestDB(t)

	// ─── 1. admin 配 SysConfig：manual 模式 + 邮箱 + TRC20 地址 ───
	configureEpusdtManualForTest(t, "admin@daof.test",
		"TMBjEGgFAPMt6DxDPKqcxsAQvWMAua8gHk", // TRC20 收款地址
		"", "", "", // 其它链不配，验证"只配 TRC20 也能上线"
	)
	disableSignupBonusForTest(t)

	emailSink, emailCleanup := captureEmailsForTest(t)
	defer emailCleanup()

	user := seedTestUser(t, 0)    // 初始 quota = 0
	admin := seedAdminUser(t)     // root admin with token

	app := newTopupE2EApp(user, admin)

	// ─── 2. 用户下单：POST /topup/create with provider=epusdt method=trc20-usdt ───
	code, resp := doJSON(t, app, "POST", "/topup/create", map[string]any{
		"provider":   "epusdt",
		"method":     "trc20-usdt",
		"amount_fen": 1000, // ¥10
	})
	if code != 200 {
		t.Fatalf("create order: expected 200 got %d body=%v", code, resp)
	}
	data, ok := resp["data"].(map[string]any)
	if !ok {
		t.Fatalf("response missing data: %v", resp)
	}
	outTradeNo := data["out_trade_no"].(string)
	if outTradeNo == "" {
		t.Fatal("out_trade_no empty")
	}
	if data["provider"] != "epusdt" {
		t.Errorf("provider=%v want epusdt", data["provider"])
	}
	if data["gateway_pay_type"] != "wallet_address" {
		t.Errorf("gateway_pay_type=%v want wallet_address", data["gateway_pay_type"])
	}

	// ─── 3. 验证 PayInfo JSON 包含完整字段 ───
	payInfoStr := data["pay_info"].(string)
	var payInfo map[string]any
	if err := json.Unmarshal([]byte(payInfoStr), &payInfo); err != nil {
		t.Fatalf("pay_info not valid JSON: %v", err)
	}
	if payInfo["receive_address"] != "TMBjEGgFAPMt6DxDPKqcxsAQvWMAua8gHk" {
		t.Errorf("receive_address=%v", payInfo["receive_address"])
	}
	if payInfo["mode"] != "manual" {
		t.Errorf("PayInfo.mode=%v want manual", payInfo["mode"])
	}
	if payInfo["token"] != "USDT" {
		t.Errorf("token=%v want USDT", payInfo["token"])
	}
	if payInfo["network"] != "tron" {
		t.Errorf("network=%v want tron", payInfo["network"])
	}
	actualAmount, ok := payInfo["actual_amount"].(float64)
	if !ok || actualAmount <= 0 {
		t.Errorf("actual_amount=%v want positive float", payInfo["actual_amount"])
	}
	expireAt, ok := payInfo["expire_at"].(float64)
	if !ok || expireAt <= 0 {
		t.Errorf("expire_at=%v want positive unix sec", payInfo["expire_at"])
	}

	// ─── 4. 验证邮件已入队（manual 模式核心：通知 admin）───
	emails := emailSink()
	if len(emails) != 1 {
		t.Fatalf("expected 1 email, got %d (emails=%+v)", len(emails), emails)
	}
	mail := emails[0]
	if mail.To != "admin@daof.test" {
		t.Errorf("email To=%q want admin@daof.test", mail.To)
	}
	// Tier 2 H-4 修复后：subject 不暴露金额 / 链类型给移动端推送预览
	if !strings.Contains(mail.Subject, "新充值订单待确认") {
		t.Errorf("subject doesn't mention 充值订单: %q", mail.Subject)
	}
	if strings.Contains(mail.Subject, "USDT") || strings.Contains(mail.Subject, "TRC20") {
		t.Errorf("subject should NOT leak token/chain (mobile notification preview risk): %q", mail.Subject)
	}
	// 详情仍在 body
	if !strings.Contains(mail.TextBody, "USDT") {
		t.Errorf("body must contain USDT: %q", mail.TextBody)
	}
	if !strings.Contains(mail.TextBody, "TRC20") {
		t.Errorf("body must contain TRC20: %q", mail.TextBody)
	}
	if !strings.Contains(mail.TextBody, outTradeNo) {
		t.Errorf("body missing outTradeNo %q", outTradeNo)
	}
	if !strings.Contains(mail.TextBody, "TMBjEGgFAPMt6DxDPKqcxsAQvWMAua8gHk") {
		t.Error("body missing receive address")
	}
	// 邮件应该包含精确金额（精度匹配 0.0001）
	expectedAmountStr := fmt.Sprintf("%.4f", actualAmount)
	if !strings.Contains(mail.TextBody, expectedAmountStr) {
		t.Errorf("body missing exact amount %q (actualAmount=%f)", expectedAmountStr, actualAmount)
	}

	// ─── 5. 取本地订单 ID（admin 标记到账要用） ───
	var order database.TopupOrder
	if err := database.DB.Where("out_trade_no = ?", outTradeNo).First(&order).Error; err != nil {
		t.Fatalf("load order: %v", err)
	}
	if order.Provider != "epusdt" {
		t.Errorf("order.Provider=%q want epusdt", order.Provider)
	}
	if order.Status != "created" {
		t.Errorf("order.Status=%q want created", order.Status)
	}
	if order.UserID != user.ID {
		t.Errorf("order.UserID=%d want %d", order.UserID, user.ID)
	}

	// ─── 6. admin 在区块链浏览器验真后，标记到账 ───
	// ExternalTradeRef 用模拟的 TRON tx hash（实际场景 admin 从 tronscan.org 复制粘贴）
	fakeChainTxHash := "0xabcdef1234567890" + outTradeNo
	code2, resp2 := doJSON(t, app, "POST",
		fmt.Sprintf("/admin/topup/orders/%d/mark-paid", order.ID),
		map[string]any{
			"external_trade_ref": fakeChainTxHash,
			"reason":             "tronscan 验证转账，e2e 测试",
		})
	if code2 != 200 {
		t.Fatalf("mark-paid: expected 200 got %d body=%v", code2, resp2)
	}

	// ─── 7. 验证完整入账事务效果 ───

	// 7a. 订单状态 paid
	var paidOrder database.TopupOrder
	if err := database.DB.First(&paidOrder, order.ID).Error; err != nil {
		t.Fatalf("reload order: %v", err)
	}
	if paidOrder.Status != "paid" {
		t.Errorf("order.Status=%q want paid", paidOrder.Status)
	}
	if paidOrder.TradeNo != fakeChainTxHash {
		t.Errorf("trade_no=%q want %q (admin's external_trade_ref)", paidOrder.TradeNo, fakeChainTxHash)
	}
	if paidOrder.PaidAt == nil {
		t.Error("paid_at not set")
	}

	// 7b. user.quota 已增加（应等于 order.AmountUSD）
	var updatedUser database.User
	if err := database.DB.First(&updatedUser, user.ID).Error; err != nil {
		t.Fatalf("reload user: %v", err)
	}
	if updatedUser.Quota != paidOrder.AmountUSD {
		t.Errorf("user.Quota=%d want %d (= order.AmountUSD)", updatedUser.Quota, paidOrder.AmountUSD)
	}
	if updatedUser.PaidQuota != paidOrder.AmountUSD {
		t.Errorf("user.PaidQuota=%d want %d", updatedUser.PaidQuota, paidOrder.AmountUSD)
	}

	// 7c. BillingEntry 写入（topup 类型，金额匹配，related_id 指向订单）
	var billingCount int64
	if err := database.DB.Model(&database.BillingEntry{}).
		Where("related_type = ? AND related_id = ? AND entry_type = ?",
			"topup_order", paidOrder.ID, database.BillingTypeTopup).
		Count(&billingCount).Error; err != nil {
		t.Fatalf("count billing entries: %v", err)
	}
	if billingCount != 1 {
		t.Errorf("BillingEntry count=%d want 1", billingCount)
	}

	// 7d. PaymentWebhookReceipt 有"manual paid"记录（防同 ExternalTradeRef 重复入账）
	var receiptCount int64
	if err := database.DB.Model(&database.PaymentWebhookReceipt{}).
		Where("out_trade_no = ? AND status = ?", paidOrder.OutTradeNo, "accepted_manual").
		Count(&receiptCount).Error; err != nil {
		t.Fatalf("count receipts: %v", err)
	}
	if receiptCount != 1 {
		t.Errorf("PaymentWebhookReceipt accepted_manual count=%d want 1", receiptCount)
	}
}

// TestEpusdtManual_E2E_DuplicateMarkPaidRejected：验证同一 ExternalTradeRef 不能重复入账
// （admin 误点 / 网络重试时的金钱安全防线）
func TestEpusdtManual_E2E_DuplicateMarkPaidRejected(t *testing.T) {
	setupSubTestDB(t)
	configureEpusdtManualForTest(t, "admin@daof.test",
		"TMBjEGgFAPMt6DxDPKqcxsAQvWMAua8gHk", "", "", "")
	disableSignupBonusForTest(t)

	_, emailCleanup := captureEmailsForTest(t)
	defer emailCleanup()

	user := seedTestUser(t, 0)
	admin := seedAdminUser(t)
	app := newTopupE2EApp(user, admin)

	// 下单
	_, resp := doJSON(t, app, "POST", "/topup/create", map[string]any{
		"provider":   "epusdt",
		"method":     "trc20-usdt",
		"amount_fen": 500,
	})
	outTradeNo := resp["data"].(map[string]any)["out_trade_no"].(string)

	var order database.TopupOrder
	database.DB.Where("out_trade_no = ?", outTradeNo).First(&order)

	// 第一次标记到账 — 应成功
	code1, _ := doJSON(t, app, "POST", fmt.Sprintf("/admin/topup/orders/%d/mark-paid", order.ID),
		map[string]any{"external_trade_ref": "tx-duplicate-test", "reason": "first time"})
	if code1 != 200 {
		t.Fatalf("first mark-paid should succeed, got %d", code1)
	}

	// 第二次同 order ID + 同 ExternalTradeRef — 应拒（订单已 paid）
	code2, resp2 := doJSON(t, app, "POST", fmt.Sprintf("/admin/topup/orders/%d/mark-paid", order.ID),
		map[string]any{"external_trade_ref": "tx-duplicate-test", "reason": "duplicate"})
	if code2 != 409 {
		t.Errorf("duplicate mark-paid should be 409 (already paid), got %d body=%v", code2, resp2)
	}

	// 用户 quota 不应被加 2 次
	var u database.User
	database.DB.First(&u, user.ID)
	if u.Quota != order.AmountUSD {
		t.Errorf("quota=%d want %d (must not double-credit on duplicate mark-paid)", u.Quota, order.AmountUSD)
	}
}

// TestEpusdtManual_E2E_SMTPUnconfiguredRejected：验证 C-2 修复
// SMTP 没配齐时 manual 模式拒绝创建订单（fail-closed，避免用户付款但 admin 永不知）
func TestEpusdtManual_E2E_SMTPUnconfiguredRejected(t *testing.T) {
	setupSubTestDB(t)
	// 故意只配 epusdt 不配 SMTP
	configureEpusdtManualForTest(t, "admin@daof.test",
		"TMBjEGgFAPMt6DxDPKqcxsAQvWMAua8gHk", "", "", "")
	disableSignupBonusForTest(t)
	// 不调 captureEmailsForTest 让 SMTP 保持未配齐

	user := seedTestUser(t, 0)
	admin := seedAdminUser(t)
	app := newTopupE2EApp(user, admin)

	code, resp := doJSON(t, app, "POST", "/topup/create", map[string]any{
		"provider":   "epusdt",
		"method":     "trc20-usdt",
		"amount_fen": 1000,
	})
	// C-2 修复：SMTP 未配齐 → 503 ERR_PAYMENT_UNAVAILABLE，订单不创建
	if code != 503 {
		t.Errorf("expected 503 (SMTP not configured) got %d body=%v", code, resp)
	}
	if resp["message_code"] != "ERR_PAYMENT_UNAVAILABLE" {
		t.Errorf("message_code=%v want ERR_PAYMENT_UNAVAILABLE", resp["message_code"])
	}
	// 验证：订单已建但立即标 failed（CreateTopup 先 Create 再调 provider，provider 拒后 mark failed）
	// 关键不变量：没有 status=created 的 epusdt 订单悬挂等待付款
	var createdCount int64
	database.DB.Model(&database.TopupOrder{}).
		Where("provider = ? AND status = ?", "epusdt", "created").
		Count(&createdCount)
	if createdCount != 0 {
		t.Errorf("epusdt order in created state despite SMTP unconfigured: count=%d (would let user pay but admin never knows)", createdCount)
	}
}

// TestEpusdtManual_E2E_AmountSuffixUniquePerOrder：验证不同订单的精确金额不冲突
// （epusdt manual 模式核心机制：用 OrderID % 10000 * 0.0001 USDT 区分订单）
func TestEpusdtManual_E2E_AmountSuffixUniquePerOrder(t *testing.T) {
	setupSubTestDB(t)
	configureEpusdtManualForTest(t, "admin@daof.test",
		"TMBjEGgFAPMt6DxDPKqcxsAQvWMAua8gHk", "", "", "")
	disableSignupBonusForTest(t)

	_, emailCleanup := captureEmailsForTest(t)
	defer emailCleanup()

	user := seedTestUser(t, 0)
	admin := seedAdminUser(t)
	app := newTopupE2EApp(user, admin)

	// 同一用户 + 同一金额连续下 3 个订单 → 应得到 3 个不同的 actual_amount
	seen := make(map[float64]bool)
	for i := 0; i < 3; i++ {
		_, resp := doJSON(t, app, "POST", "/topup/create", map[string]any{
			"provider":   "epusdt",
			"method":     "trc20-usdt",
			"amount_fen": 1000, // 都是 ¥10
		})
		data := resp["data"].(map[string]any)
		var payInfo map[string]any
		json.Unmarshal([]byte(data["pay_info"].(string)), &payInfo)
		actualAmount := payInfo["actual_amount"].(float64)
		if seen[actualAmount] {
			t.Errorf("actual_amount %f collided on order #%d (suffix not unique)", actualAmount, i+1)
		}
		seen[actualAmount] = true
	}
}
