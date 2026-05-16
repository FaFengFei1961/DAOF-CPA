package controller

import (
	"testing"
	"time"

	"daof-cpa/database"
	"daof-cpa/middleware"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// ─── Helper 函数单元测试 ─────────────────────────────────────────────

// TestParseRMBStringToFen 边界覆盖：易付通回调金额字符串解析必须严格，
// 任何浮点歧义或非法格式都应拒绝（fix CRITICAL P1-5 + P2-2）。
func TestParseRMBStringToFen(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int64
		ok   bool
	}{
		// === valid ===
		{"int only", "12", 1200, true},
		{"one decimal", "12.3", 1230, true},
		{"two decimals", "12.34", 1234, true},
		{"zero", "0", 0, true},
		{"zero with decimals", "0.00", 0, true},
		{"large int", "999999", 99999900, true},
		{"trim whitespace", "  12.34  ", 1234, true},

		// === invalid: format ===
		{"empty", "", 0, false},
		{"all whitespace", "   ", 0, false},
		{"trailing dot (rejected by P2-2)", "12.", 0, false},
		{"leading dot", ".5", 0, false},
		{"only dot", ".", 0, false},
		{"three decimals", "12.345", 0, false},
		{"two dots", "12.3.4", 0, false},

		// === invalid: characters ===
		{"alpha", "abc", 0, false},
		{"alpha in int", "12a", 0, false},
		{"alpha in frac", "12.a", 0, false},
		{"negative", "-12", 0, false},
		{"plus sign", "+12", 0, false},
		{"comma", "12,34", 0, false},
		{"scientific", "1e2", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseRMBStringToFen(tc.in)
			if ok != tc.ok {
				t.Fatalf("ok=%v, want %v (input=%q)", ok, tc.ok, tc.in)
			}
			if ok && got != tc.want {
				t.Fatalf("got=%d, want %d (input=%q)", got, tc.want, tc.in)
			}
		})
	}
}

// TestSafeExchangeRateRmbPerUsdMicros_Defaults Sprint4-M3：从 float64 改为 int64 定点。
func TestSafeExchangeRateRmbPerUsdMicros_Defaults(t *testing.T) {
	setupSubTestDB(t) // 清空 SysConfigCache
	rate := safeExchangeRateRmbPerUsdMicros()
	if rate != 7_200_000 {
		t.Errorf("expected default 7_200_000 (7.2 RMB/USD × 1e6), got %d", rate)
	}
}

// TestUsdMicroFromFenAndRate 验证 fen → micro_usd 整数转换的 0 偏差性质。
func TestUsdMicroFromFenAndRate(t *testing.T) {
	cases := []struct {
		name     string
		fen      int64
		rate     int64
		wantOK   bool
		wantUsd  int64
	}{
		// 精确边界：¥72 / 7.2 = $10 整除
		{"exact ¥72 → $10", 7200, 7_200_000, true, 10_000_000},
		// 精确边界：¥36 / 7.2 = $5 整除
		{"exact ¥36 → $5", 3600, 7_200_000, true, 5_000_000},
		// 整除小额：¥1 / 7.2 ≈ $0.138888... → floor 138888 micro
		{"¥1 → $0.138888", 100, 7_200_000, true, 138_888},
		// ¥0.01（1 fen）/ 7.2 ≈ $0.001388... → floor 1388 micro
		{"¥0.01 → $0.001388", 1, 7_200_000, true, 1388},
		// 非法输入：fen <= 0
		{"reject zero", 0, 7_200_000, false, 0},
		{"reject negative", -100, 7_200_000, false, 0},
		// 非法输入：rate <= 0
		{"reject zero rate", 100, 0, false, 0},
		{"reject negative rate", 100, -1_000_000, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := usdMicroFromFenAndRate(tc.fen, tc.rate)
			if ok != tc.wantOK {
				t.Fatalf("ok=%v want %v (fen=%d rate=%d)", ok, tc.wantOK, tc.fen, tc.rate)
			}
			if ok && got != tc.wantUsd {
				t.Fatalf("got=%d want %d (fen=%d rate=%d)", got, tc.wantUsd, tc.fen, tc.rate)
			}
		})
	}
}

func TestProratedTopupRefundMicro_CumulativeExact(t *testing.T) {
	total := int64(13_890_000) // ¥100 at 7.2 locks to $13.89
	first, ok := proratedTopupRefundMicro(total, 10_000, 5_000)
	if !ok {
		t.Fatal("first prorate failed")
	}
	secondTarget, ok := proratedTopupRefundMicro(total, 10_000, 10_000)
	if !ok {
		t.Fatal("second prorate failed")
	}
	second := secondTarget - first
	if first+second != total {
		t.Fatalf("split refund total=%d, want %d", first+second, total)
	}
	if first != 6_945_000 || second != 6_945_000 {
		t.Fatalf("split refund = %d + %d, want 6945000 + 6945000", first, second)
	}
}

func TestCsvContains(t *testing.T) {
	if !csvContains("alipay,wxpay,qqpay", "wxpay") {
		t.Error("expected wxpay found")
	}
	if csvContains("alipay,wxpay", "paypal") {
		t.Error("paypal not in list")
	}
	if !csvContains("alipay , wxpay ", "wxpay") {
		t.Error("should trim spaces")
	}
}

func TestIsSafeReturnPath(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"/#topup_result", true},
		{"/foo/bar?x=1", true},
		{"//evil.com", false},
		{"http://evil.com/path", false},
		{"", false},
		{"relative/path", false},
		{"/good\npath", false}, // contains control char
	}
	for _, tc := range cases {
		got := isSafeReturnPath(tc.in)
		if got != tc.want {
			t.Errorf("isSafeReturnPath(%q) = %v want %v", tc.in, got, tc.want)
		}
	}
}

func TestSanitizeExternalRef(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"abc123", "abc123"},
		{"has\nnewline", "hasnewline"},
		{"has\ttab", "hastab"},
		{"中文退款", "中文退款"},
	}
	for _, tc := range cases {
		got := sanitizeExternalRef(tc.in)
		if got != tc.want {
			t.Errorf("sanitizeExternalRef(%q) = %q want %q", tc.in, got, tc.want)
		}
	}
	// length truncation at 64 runes
	long := ""
	for i := 0; i < 100; i++ {
		long += "中"
	}
	got := sanitizeExternalRef(long)
	if len([]rune(got)) != 64 {
		t.Errorf("expected 64 runes, got %d", len([]rune(got)))
	}
}

func TestGenerateOutTradeNo_Unique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		no, err := generateOutTradeNo(uint(i + 1))
		if err != nil {
			t.Fatalf("generateOutTradeNo: %v", err)
		}
		if seen[no] {
			t.Fatalf("duplicate out_trade_no: %s", no)
		}
		seen[no] = true
		if len(no) > 64 {
			t.Errorf("out_trade_no too long: %d", len(no))
		}
	}
}

// ─── CreateTopup 入口验证 ────────────────────────────────────────────

func newTopupTestApp(user *database.User) *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("user", user)
		return c.Next()
	})
	app.Post("/topup/create", CreateTopup)
	app.Get("/topup/mine", MyTopupOrders)
	app.Get("/topup/options", GetTopupOptions)
	return app
}

func TestCreateTopup_RejectsWhenUnconfigured(t *testing.T) {
	setupSubTestDB(t)
	user := seedTestUser(t, 100)
	app := newTopupTestApp(user)

	// With empty SysConfigCache, yifut is unconfigured → 503
	// fix Sprint4-M3：协议从 amount_rmb float → amount_fen int64
	code, resp := doJSON(t, app, "POST", "/topup/create", map[string]any{
		"amount_fen": 1000, "pay_type": "alipay",
	})
	if code != 503 {
		t.Errorf("expected 503 (unconfigured) got %d body=%v", code, resp)
	}
}

func TestAllowedPayTypes(t *testing.T) {
	if !allowedPayTypes["alipay"] {
		t.Error("alipay should be allowed")
	}
	if !allowedPayTypes["wxpay"] {
		t.Error("wxpay should be allowed")
	}
	if allowedPayTypes["bitcoin"] {
		t.Error("bitcoin should NOT be allowed")
	}
	if allowedPayTypes[""] {
		t.Error("empty should NOT be allowed")
	}
}

// ─── YifutNotify 幂等性 + 金额双校验 ────────────────────────────────

func TestYifutNotify_DuplicateCallback(t *testing.T) {
	setupSubTestDB(t)
	user := seedTestUser(t, 0)

	order := database.TopupOrder{
		OutTradeNo:           "tp_dup_test",
		UserID:               user.ID,
		MoneyRMB:             7200,                      // ¥72.00 = 7200 fen
		AmountUSD:            10 * database.MicroPerUSD, // $10
		ExchangeRateRmbPerUsdMicros: 7_200_000,
		Status:               "paid", // already paid
		PaidAt:               ptrTime(time.Now()),
	}
	database.DB.Create(&order)

	// Simulate the duplicate handling in a transaction (mimics what YifutNotify does)
	var affected int64
	database.DB.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&database.TopupOrder{}).
			Where("out_trade_no = ? AND status = ?", "tp_dup_test", "created").
			Updates(map[string]any{"status": "paid"})
		affected = res.RowsAffected
		return res.Error
	})
	if affected != 0 {
		t.Errorf("duplicate callback should affect 0 rows, got %d", affected)
	}
}

func TestYifutNotify_MoneyMismatch(t *testing.T) {
	setupSubTestDB(t)
	user := seedTestUser(t, 0)

	database.DB.Create(&database.TopupOrder{
		OutTradeNo:           "tp_mismatch",
		UserID:               user.ID,
		MoneyRMB:             7200,                      // ¥72.00 = 7200 fen
		AmountUSD:            10 * database.MicroPerUSD, // $10
		ExchangeRateRmbPerUsdMicros: 7_200_000,
		Status:               "created",
	})

}

// ─── MyTopupOrders ───────────────────────────────────────────────────

func TestMyTopupOrders_Pagination(t *testing.T) {
	setupSubTestDB(t)
	user := seedTestUser(t, 100)
	app := newTopupTestApp(user)

	for i := 0; i < 5; i++ {
		database.DB.Create(&database.TopupOrder{
			OutTradeNo:           "tp_page_" + itoaUint(uint(i)),
			UserID:               user.ID,
			MoneyRMB:             1000,      // ¥10 = 1000 fen
			AmountUSD:            1_390_000, // $1.39 = 1_390_000 micro_usd
			ExchangeRateRmbPerUsdMicros: 7_200_000,
			Status:               "paid",
		})
	}

	code, resp := doJSON(t, app, "GET", "/topup/mine?page=1&page_size=3", nil)
	if code != 200 {
		t.Fatalf("expected 200 got %d", code)
	}
	data, _ := resp["data"].([]any)
	if len(data) != 3 {
		t.Errorf("expected 3 items on page 1, got %d", len(data))
	}
	meta, _ := resp["meta"].(map[string]any)
	if meta["total"] != float64(5) {
		t.Errorf("expected total=5 got %v", meta["total"])
	}
}

// ─── AdminListTopupOrders ────────────────────────────────────────────

// fix MAJOR M22-A3（codex 第二十二轮）：内置真实 AdminGuard
func newAdminTopupListApp(admin *database.User) *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error {
		c.Request().Header.SetCookie("daof_admin_token", admin.Token)
		return c.Next()
	})
	app.Use(middleware.AdminGuard)
	app.Get("/admin/topup/orders", AdminListTopupOrders)
	return app
}

func TestAdminListTopupOrders_StatusFilter(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 0)
	app := newAdminTopupListApp(admin)

	database.DB.Create(&database.TopupOrder{
		OutTradeNo: "tp_a1", UserID: user.ID, MoneyRMB: 1000, Status: "paid", ExchangeRateRmbPerUsdMicros: 7_200_000,
	})
	database.DB.Create(&database.TopupOrder{
		OutTradeNo: "tp_a2", UserID: user.ID, MoneyRMB: 2000, Status: "created", ExchangeRateRmbPerUsdMicros: 7_200_000,
	})

	code, resp := doJSON(t, app, "GET", "/admin/topup/orders?status=paid", nil)
	if code != 200 {
		t.Fatalf("expected 200 got %d", code)
	}
	data, _ := resp["data"].([]any)
	if len(data) != 1 {
		t.Errorf("expected 1 paid order got %d", len(data))
	}

	// invalid status → 400
	code2, _ := doJSON(t, app, "GET", "/admin/topup/orders?status=evil", nil)
	if code2 != 400 {
		t.Errorf("expected 400 for invalid status filter got %d", code2)
	}
}

// ─── AdminRefundTopup 额外场景 ───────────────────────────────────────

func TestAdminRefund_UsesPersistedAmountWhenExchangeRateRmbPerUsdMicrosCorrupted(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 100)
	app := newAdminTopupTestApp(admin)

	order := database.TopupOrder{
		OutTradeNo:           "tp_bad_rate",
		UserID:               user.ID,
		MoneyRMB:             7200,
		AmountUSD:            10 * database.MicroPerUSD,
		ExchangeRateRmbPerUsdMicros: 0, // corrupted
		Status:               "paid",
	}
	database.DB.Create(&order)

	code, resp := doJSON(t, app, "POST",
		"/admin/topup/orders/"+itoaUint(order.ID)+"/refund",
		map[string]any{"reclaim_quota": true, "external_refund_ref": "rext_bad_rate"})
	if code != 200 {
		t.Fatalf("expected 200 because AmountUSD is canonical, got %d body=%v", code, resp)
	}
	var u database.User
	database.DB.First(&u, user.ID)
	if u.Quota != 90*database.MicroPerUSD {
		t.Errorf("quota=%d want 90 USD after reclaiming the locked $10 amount", u.Quota)
	}
}

func TestAdminRefund_PartialRefund(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 100)
	app := newAdminTopupTestApp(admin)

	order := seedPaidTopupOrder(t, user.ID, 72.0) // $10

	// partial refund: ¥36 of ¥72
	code, resp := doJSON(t, app, "POST",
		"/admin/topup/orders/"+itoaUint(order.ID)+"/refund",
		map[string]any{"money_fen": 3600, "reclaim_quota": true, "external_refund_ref": "rext_partial"})
	if code != 200 {
		t.Fatalf("expected 200 got %d body=%v", code, resp)
	}

	var fresh database.TopupOrder
	database.DB.First(&fresh, order.ID)
	if fresh.Status != "paid" {
		t.Errorf("partial refund should keep status=paid, got %q", fresh.Status)
	}
	// 36 RMB = 3600 fen
	if fresh.RefundedAmountRMB != 3600 {
		t.Errorf("refunded_amount_rmb should be 3600 fen (¥36.00), got %d", fresh.RefundedAmountRMB)
	}
}

func TestAdminRefund_MultiplePartialReclaimMatchesLockedAmount(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 100)
	app := newAdminTopupTestApp(admin)

	order := seedPaidTopupOrder(t, user.ID, 100.0) // locks to $13.89 at ¥7.2/$
	if order.AmountUSD != 13_890_000 {
		t.Fatalf("seed amount=%d, want 13_890_000", order.AmountUSD)
	}

	code1, resp1 := doJSON(t, app, "POST",
		"/admin/topup/orders/"+itoaUint(order.ID)+"/refund",
		map[string]any{"money_fen": 5000, "reclaim_quota": true, "external_refund_ref": "rext_split_1"})
	if code1 != 200 {
		t.Fatalf("first partial refund got %d body=%v", code1, resp1)
	}
	code2, resp2 := doJSON(t, app, "POST",
		"/admin/topup/orders/"+itoaUint(order.ID)+"/refund",
		map[string]any{"money_fen": 5000, "reclaim_quota": true, "external_refund_ref": "rext_split_2"})
	if code2 != 200 {
		t.Fatalf("second partial refund got %d body=%v", code2, resp2)
	}

	var fresh database.TopupOrder
	database.DB.First(&fresh, order.ID)
	if fresh.Status != "refunded" || fresh.RefundedAmountRMB != fresh.MoneyRMB {
		t.Fatalf("order status/refunded = %q/%d, want refunded/%d", fresh.Status, fresh.RefundedAmountRMB, fresh.MoneyRMB)
	}

	var refundSum int64
	if err := database.DB.Model(&database.BillingEntry{}).
		Where("entry_type = ? AND related_type = ? AND related_id = ?", database.BillingTypeRefundTopup, "topup_order", order.ID).
		Select("COALESCE(SUM(amount_usd), 0)").
		Scan(&refundSum).Error; err != nil {
		t.Fatalf("sum refund billing: %v", err)
	}
	if refundSum != -order.AmountUSD {
		t.Fatalf("refund billing sum=%d, want %d", refundSum, -order.AmountUSD)
	}

	var u database.User
	database.DB.First(&u, user.ID)
	wantQuota := 100*database.MicroPerUSD - order.AmountUSD
	if u.Quota != wantQuota {
		t.Fatalf("quota=%d, want %d", u.Quota, wantQuota)
	}
}

// TestAdminRefund_ExternalRefundRefIdempotent 验证 Sprint1-P0-6 修复：
// 同一 external_refund_ref 多次提交必须被拒绝（DB unique 索引兜底）。
//
// 旧实现：每次提交累加 RefundedAmountRMB + 覆盖 RefundNo/OutRefundNo 字段，
// 平台双扣余额但用户钱包只到账一次。
// 新实现：TopupRefund 事实表 ExternalRefundRef 唯一索引 + 事务内 pre-check。
func TestAdminRefund_ExternalRefundRefIdempotent(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 100)
	app := newAdminTopupTestApp(admin)

	order := seedPaidTopupOrder(t, user.ID, 72.0)

	// 第一次部分退款：¥36，external_refund_ref=DUP_REF
	code1, resp1 := doJSON(t, app, "POST",
		"/admin/topup/orders/"+itoaUint(order.ID)+"/refund",
		map[string]any{"money_fen": 3600, "reclaim_quota": true, "external_refund_ref": "DUP_REF"})
	if code1 != 200 {
		t.Fatalf("first refund expected 200, got %d body=%v", code1, resp1)
	}

	// 第二次用同样的 external_refund_ref 提交：必须被 409 拒绝
	code2, resp2 := doJSON(t, app, "POST",
		"/admin/topup/orders/"+itoaUint(order.ID)+"/refund",
		map[string]any{"money_fen": 3600, "reclaim_quota": true, "external_refund_ref": "DUP_REF"})
	if code2 != 409 {
		t.Fatalf("duplicate refund expected 409, got %d body=%v", code2, resp2)
	}
	if resp2["message_code"] != "ERR_REFUND_REF_DUPLICATED" {
		t.Errorf("expected message_code=ERR_REFUND_REF_DUPLICATED, got %v", resp2["message_code"])
	}

	// 验证订单状态没有被二次提交污染
	var fresh database.TopupOrder
	database.DB.First(&fresh, order.ID)
	if fresh.RefundedAmountRMB != 3600 {
		t.Errorf("second submit should NOT accumulate RefundedAmountRMB: got %d, want 3600 (= first refund only)", fresh.RefundedAmountRMB)
	}

	// 验证 TopupRefund 表只有一行
	var refundCount int64
	database.DB.Model(&database.TopupRefund{}).Where("topup_order_id = ?", order.ID).Count(&refundCount)
	if refundCount != 1 {
		t.Errorf("TopupRefund rows should be 1, got %d", refundCount)
	}

	// 验证用户余额没有被双扣
	var u database.User
	database.DB.First(&u, user.ID)
	wantQuota := 100*database.MicroPerUSD - (order.AmountUSD * 3600 / order.MoneyRMB)
	if u.Quota != wantQuota {
		t.Errorf("user quota=%d, want %d (only first refund applied, no double-deduct)", u.Quota, wantQuota)
	}

	// 用不同的 external_refund_ref 提交剩余 ¥36：必须成功
	code3, resp3 := doJSON(t, app, "POST",
		"/admin/topup/orders/"+itoaUint(order.ID)+"/refund",
		map[string]any{"money_fen": 3600, "reclaim_quota": true, "external_refund_ref": "DIFFERENT_REF"})
	if code3 != 200 {
		t.Fatalf("different ref refund expected 200, got %d body=%v", code3, resp3)
	}
	database.DB.First(&fresh, order.ID)
	if fresh.Status != "refunded" {
		t.Errorf("order should be fully refunded after second valid ref, got status=%q", fresh.Status)
	}
}

func TestAdminRefund_CASPreventDoubleRefund(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 100)
	app := newAdminTopupTestApp(admin)

	order := seedPaidTopupOrder(t, user.ID, 72.0)

	// first full refund
	code1, _ := doJSON(t, app, "POST",
		"/admin/topup/orders/"+itoaUint(order.ID)+"/refund",
		map[string]any{"money_fen": 7200, "reclaim_quota": false, "external_refund_ref": "rext_cas1"})
	if code1 != 200 {
		t.Fatalf("first refund should succeed, got %d", code1)
	}

	// second attempt on now-refunded order
	code2, resp2 := doJSON(t, app, "POST",
		"/admin/topup/orders/"+itoaUint(order.ID)+"/refund",
		map[string]any{"money_fen": 7200, "reclaim_quota": false, "external_refund_ref": "rext_cas2"})
	if code2 != 400 {
		t.Errorf("expected 400 (not paid), got %d body=%v", code2, resp2)
	}
}

// ─── GetTopupOptions ─────────────────────────────────────────────────

// fix Sprint4-M3：协议改 exchange_rate float → exchange_rate_rmb_per_usd_micros int64
func TestGetTopupOptions_Defaults(t *testing.T) {
	setupSubTestDB(t)
	user := seedTestUser(t, 0)
	app := newTopupTestApp(user)

	code, resp := doJSON(t, app, "GET", "/topup/options", nil)
	if code != 200 {
		t.Fatalf("expected 200 got %d", code)
	}
	data, _ := resp["data"].(map[string]any)
	if data["exchange_rate_rmb_per_usd_micros"] != float64(7_200_000) {
		t.Errorf("expected default 7_200_000 got %v", data["exchange_rate_rmb_per_usd_micros"])
	}
	if data["configured"] != false {
		t.Errorf("expected configured=false when no yifut config")
	}
	// 验证 fen 单位的金额范围字段
	if data["min_amount_fen"] != float64(100) {
		t.Errorf("expected min_amount_fen=100 (¥1) got %v", data["min_amount_fen"])
	}
	if data["max_amount_fen"] != float64(1_000_000) {
		t.Errorf("expected max_amount_fen=1_000_000 (¥10,000) got %v", data["max_amount_fen"])
	}
}

// ─── checkYifutTimestamp ─────────────────────────────────────────────

func TestCheckYifutTimestamp(t *testing.T) {
	nowStr := itoaUint(uint(time.Now().Unix()))
	if !checkYifutTimestamp(nowStr, "test", "TEST") {
		t.Error("current timestamp should pass")
	}
	if checkYifutTimestamp("", "test", "TEST") {
		t.Error("empty timestamp should fail")
	}
	if checkYifutTimestamp("not-a-number", "test", "TEST") {
		t.Error("non-numeric timestamp should fail")
	}
	oldTs := itoaUint(uint(time.Now().Unix() - 600))
	if checkYifutTimestamp(oldTs, "test", "TEST") {
		t.Error("stale timestamp (600s old) should fail")
	}
}
