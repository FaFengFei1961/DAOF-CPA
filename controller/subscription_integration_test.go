package controller

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"daof-cpa/database"
	"daof-cpa/middleware"
	"daof-cpa/proxy"

	"github.com/gofiber/fiber/v2"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// setupSubTestDB 用 in-memory SQLite + AutoMigrate 模拟生产 schema。
// 每次测试独立 DB（cache=private 防共享）。
func setupSubTestDB(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=private"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	// in-memory + cache=private 下，GORM 连接池每次新 open 都得到独立的空 DB。
	// 限制 MaxOpenConns=1 让所有 query/异步 goroutine 共享同一个 :memory: 实例，
	// 否则触发器（异步 Dispatch）跑到独立 conn 上会让主测试看不到 commit 的数据。
	if sqlDB, dbErr := db.DB(); dbErr == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	if err := db.AutoMigrate(
		&database.User{},
		&database.SysConfig{},
		&database.QuotaPlan{}, &database.Package{}, &database.PackagePlan{},
		&database.UserSubscription{}, &database.SubscriptionUsage{}, &database.Notification{},
		&database.OperationLog{},
		&database.TopupOrder{}, &database.TopupRefund{}, &database.PaymentWebhookReceipt{},
		&database.BillingEntry{}, &database.BillingReconciliation{},
		&database.CouponTemplate{}, &database.UserCoupon{}, // R23+2 优惠券系统
		&database.Ticket{}, &database.TicketMessage{}, // 工单系统（M23-A4 ticket CSRF 测试需要）
	); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	database.DB = db
	// proxy 缓存初始化（PurchasePackage 会调 InvalidateUserSubscriptionCache）
	proxy.SysConfigMutex.Lock()
	proxy.SysConfigCache = map[string]string{}
	proxy.SysConfigMutex.Unlock()
}

// seedTestUser 创建测试用户并塞进 Locals 模拟通过认证。
// balance 单位 USD（人友好），内部转 micro_usd。
func seedTestUser(t *testing.T, balance float64) *database.User {
	t.Helper()
	balanceMicro, _ := database.USDToMicro(balance)
	u := database.User{
		Username:     "tester",
		PasswordHash: "x",
		Token:        "sk-test-integration-token",
		Quota:        balanceMicro,
		Status:       1,
	}
	if err := database.DB.Create(&u).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return &u
}

// boolPtr helper（test 内部用，避免 lib 污染）
func boolPtr(b bool) *bool { return &b }

// seedPackage 创建一个标准套餐：1 个 plan，限 10000 request_count，月周期，价格 9.9
func seedPackage(t *testing.T, opts ...func(*database.Package)) *database.Package {
	t.Helper()
	plan := database.QuotaPlan{
		Name:          "test_plan_request_count",
		DisplayName:   "测试 Plan",
		ModelMatch:    `["*"]`,
		LimitUnit:     "request_count",
		LimitValue:    10000,
		WindowSeconds: 0,
		Priority:      1,
		Enabled:       boolPtr(true),
	}
	if err := database.DB.Create(&plan).Error; err != nil {
		t.Fatalf("seed plan: %v", err)
	}
	pkg := database.Package{
		Name:                 "TestPro",
		PriceAmount:          9_900_000, // $9.90
		PriceCurrency:        "USD",
		BillingPeriodSeconds: 30 * 24 * 3600,
		Stackable:            boolPtr(true),
		MaxActivePerUser:     3,
		Public:               true,
		Enabled:              boolPtr(true),
	}
	for _, opt := range opts {
		opt(&pkg)
	}
	if err := database.DB.Create(&pkg).Error; err != nil {
		t.Fatalf("seed pkg: %v", err)
	}
	if err := database.DB.Create(&database.PackagePlan{
		PackageID: pkg.ID, QuotaPlanID: plan.ID, QuantityMultiplier: 1,
	}).Error; err != nil {
		t.Fatalf("seed pkgplan: %v", err)
	}
	return &pkg
}

// newTestApp 注入 user 到 Locals 后挂 PurchasePackage / CancelSubscription
func newTestApp(user *database.User) *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("user", user)
		return c.Next()
	})
	app.Post("/purchase", PurchasePackage)
	app.Delete("/sub/:id", CancelSubscription)
	app.Get("/my", MySubscriptions)
	return app
}

func doJSON(t *testing.T, app *fiber.App, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// fix MAJOR M-A4（codex 第二十一轮）：默认带同源 Origin 让真实 AdminGuard CSRF 校验通过；
	// CSRF 阴性测试可单独覆盖 doJSONNoOrigin / doJSONCrossOrigin。
	req.Header.Set("Origin", "http://example.com")
	req.Host = "example.com"
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(bodyBytes, &m)
	return resp.StatusCode, m
}

// fix MAJOR M-A4（codex 第二十一轮）：admin 测试 helper 用真实 AdminGuard middleware ——
// 让 CSRF / status=1 / cookie 鉴权 / locals 注入这条核心安全链路被业务测试触达。
//
// 用法：
//
//	app := newRealAdminApp(admin)
//	app.Post("/admin/...", controller.AdminRefundTopup)
//
// 测试请求自动带 Cookie "daof_admin_token=<admin.Token>"，AdminGuard 走真实查询 + 注入 locals。
// 跨源 / 缺 Origin / 封禁 admin 等阴性用例直接构造 req 不带 Cookie 或换 Origin 验证拒绝。
func newRealAdminApp(admin *database.User) *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	// 注入 cookie 给 AdminGuard 读取
	app.Use(func(c *fiber.Ctx) error {
		c.Request().Header.SetCookie("daof_admin_token", admin.Token)
		return c.Next()
	})
	app.Use(middleware.AdminGuard)
	return app
}

// ─── 购买流程 ────────────────────────────────────────────────────

func TestIntegration_PurchasePackage_Success(t *testing.T) {
	setupSubTestDB(t)
	user := seedTestUser(t, 100)
	pkg := seedPackage(t)

	app := newTestApp(user)
	code, resp := doJSON(t, app, "POST", "/purchase", map[string]any{
		"package_id": pkg.ID, "quantity": 1,
	})
	if code != 200 {
		t.Fatalf("expected 200, got %d, body=%v", code, resp)
	}
	if resp["success"] != true {
		t.Errorf("success=false: %v", resp)
	}

	// 验证：1 条 active 订阅
	var subs []database.UserSubscription
	database.DB.Where("user_id = ?", user.ID).Find(&subs)
	if len(subs) != 1 {
		t.Errorf("got %d subs, want 1", len(subs))
	}
	if subs[0].Status != "active" {
		t.Errorf("status=%q, want active", subs[0].Status)
	}

	// 验证：扣款 9.9 USD
	var fresh database.User
	database.DB.First(&fresh, user.ID)
	wantBalance := int64(100*database.MicroPerUSD) - 9_900_000
	if fresh.Quota != wantBalance {
		t.Errorf("balance=%d, want %d", fresh.Quota, wantBalance)
	}
}

func TestIntegration_MySubscriptionsIncludesUsageSummary(t *testing.T) {
	setupSubTestDB(t)
	user := seedTestUser(t, 100)
	pkg := seedPackage(t)

	app := newTestApp(user)
	code, resp := doJSON(t, app, "POST", "/purchase", map[string]any{
		"package_id": pkg.ID, "quantity": 1,
	})
	if code != 200 {
		t.Fatalf("purchase expected 200, got %d, body=%v", code, resp)
	}

	var sub database.UserSubscription
	if err := database.DB.Where("user_id = ?", user.ID).First(&sub).Error; err != nil {
		t.Fatalf("load subscription: %v", err)
	}
	var plan database.QuotaPlan
	if err := database.DB.First(&plan).Error; err != nil {
		t.Fatalf("load quota plan: %v", err)
	}
	now := time.Now()
	if err := database.DB.Create(&database.SubscriptionUsage{
		SubscriptionID: sub.ID,
		QuotaPlanID:    plan.ID,
		ModelBucket:    "*",
		WindowStartAt:  now,
		WindowEndAt:    now.Add(time.Hour),
		ConsumedValue:  12.34,
		RequestCount:   2,
	}).Error; err != nil {
		t.Fatalf("seed usage: %v", err)
	}

	code, resp = doJSON(t, app, "GET", "/my", nil)
	if code != 200 {
		t.Fatalf("my subscriptions expected 200, got %d, body=%v", code, resp)
	}
	data, ok := resp["data"].([]any)
	if !ok || len(data) != 1 {
		t.Fatalf("expected one subscription row, got %#v", resp["data"])
	}
	row, ok := data[0].(map[string]any)
	if !ok {
		t.Fatalf("subscription row type=%T", data[0])
	}
	if row["package_name"] != "TestPro" {
		t.Fatalf("package_name missing or wrong: %#v", row["package_name"])
	}
	if got, _ := row["purchased_unit_price_usd"].(float64); got != 9.9 {
		t.Fatalf("purchased_unit_price_usd should be USD float 9.9, got %#v", row["purchased_unit_price_usd"])
	}
	summaries, ok := row["usage_summary"].([]any)
	if !ok || len(summaries) != 1 {
		t.Fatalf("usage_summary should be present with one row, got %#v", row["usage_summary"])
	}
	summary, ok := summaries[0].(map[string]any)
	if !ok {
		t.Fatalf("usage summary row type=%T", summaries[0])
	}
	if got, _ := summary["consumed"].(float64); got < 12.33 || got > 12.35 {
		t.Fatalf("consumed should include persisted subscription usage, got %#v", summary["consumed"])
	}
	if got, _ := summary["request_count"].(float64); got != 2 {
		t.Fatalf("request_count should include persisted subscription usage, got %#v", summary["request_count"])
	}
}

// TestIntegration_MySubscriptions_NoInternalFieldLeak 锁定 2026-05-28 审查 H1 修复：
// /api/subscriptions/mine 改成精确白名单 DTO 后，绝不能再泄漏 package_snapshot
// （内含 model_match / weight_factor / limit_unit 等内部计费配置）等内部字段。
// 防止未来有人图省事改回内嵌整个 database.UserSubscription。
func TestIntegration_MySubscriptions_NoInternalFieldLeak(t *testing.T) {
	setupSubTestDB(t)
	user := seedTestUser(t, 100)
	pkg := seedPackage(t)
	app := newTestApp(user)

	if code, resp := doJSON(t, app, "POST", "/purchase", map[string]any{
		"package_id": pkg.ID, "quantity": 1,
	}); code != 200 {
		t.Fatalf("purchase expected 200, got %d, body=%v", code, resp)
	}

	code, resp := doJSON(t, app, "GET", "/my", nil)
	if code != 200 {
		t.Fatalf("my subscriptions expected 200, got %d", code)
	}
	data, ok := resp["data"].([]any)
	if !ok || len(data) != 1 {
		t.Fatalf("expected one subscription row, got %#v", resp["data"])
	}
	row, ok := data[0].(map[string]any)
	if !ok {
		t.Fatalf("subscription row type=%T", data[0])
	}
	// 顶层不得出现这些内部 / 敏感字段（白名单 DTO 外的全部剔除）。
	for _, leaked := range []string{"package_snapshot", "grant_reason", "parent_subscription_id", "usage"} {
		if _, exists := row[leaked]; exists {
			t.Fatalf("MySubscriptions row leaked internal field %q: %#v", leaked, row)
		}
	}
	// 整体响应（含嵌套 usage_summary）不得出现内部计费配置 marker。
	// 注意：usage_summary.unit 仍含 "api_cost_usd"（前端依赖判断展示单位），不在此断言内。
	rawBytes, _ := json.Marshal(resp)
	raw := string(rawBytes)
	for _, marker := range []string{"package_snapshot", "model_match", "weight_factor"} {
		if strings.Contains(raw, marker) {
			t.Fatalf("MySubscriptions response leaked internal billing marker %q: %s", marker, raw)
		}
	}
	// 用户可见的必需字段必须保留。
	if row["package_name"] == nil {
		t.Fatalf("package_name must be present: %#v", row)
	}
	if row["usage_summary"] == nil {
		t.Fatalf("usage_summary must be present: %#v", row)
	}
}

func TestIntegration_MySubscriptionsDisplaysExpiredWindowAsFresh(t *testing.T) {
	setupSubTestDB(t)
	user := seedTestUser(t, 100)
	pkg := seedPackage(t)
	if err := database.DB.Model(&database.QuotaPlan{}).Where("1 = 1").Update("window_seconds", 3600).Error; err != nil {
		t.Fatalf("seed windowed quota plan: %v", err)
	}

	app := newTestApp(user)
	code, resp := doJSON(t, app, "POST", "/purchase", map[string]any{
		"package_id": pkg.ID, "quantity": 1,
	})
	if code != 200 {
		t.Fatalf("purchase expected 200, got %d, body=%v", code, resp)
	}

	var sub database.UserSubscription
	if err := database.DB.Where("user_id = ?", user.ID).First(&sub).Error; err != nil {
		t.Fatalf("load subscription: %v", err)
	}
	var plan database.QuotaPlan
	if err := database.DB.First(&plan).Error; err != nil {
		t.Fatalf("load quota plan: %v", err)
	}
	now := time.Now()
	if err := database.DB.Create(&database.SubscriptionUsage{
		SubscriptionID: sub.ID,
		QuotaPlanID:    plan.ID,
		ModelBucket:    "*",
		WindowStartAt:  now.Add(-2 * time.Hour),
		WindowEndAt:    now.Add(-time.Hour),
		ConsumedValue:  99,
		RequestCount:   7,
	}).Error; err != nil {
		t.Fatalf("seed expired usage: %v", err)
	}

	code, resp = doJSON(t, app, "GET", "/my", nil)
	if code != 200 {
		t.Fatalf("my subscriptions expected 200, got %d, body=%v", code, resp)
	}
	data := resp["data"].([]any)
	summaries := data[0].(map[string]any)["usage_summary"].([]any)
	summary := summaries[0].(map[string]any)
	if got, _ := summary["consumed"].(float64); got != 0 {
		t.Fatalf("expired window should display as fresh consumed=0, got %#v", summary["consumed"])
	}
	if got, _ := summary["request_count"].(float64); got != 0 {
		t.Fatalf("expired window should display as fresh request_count=0, got %#v", summary["request_count"])
	}
	if _, ok := summary["window_end_at"]; ok {
		t.Fatalf("expired window should not expose old window_end_at, got %#v", summary["window_end_at"])
	}
	if got, _ := summary["remaining"].(float64); got < 9999.9 {
		t.Fatalf("expired window should show full remaining quota, got %#v", summary["remaining"])
	}
}

func TestIntegration_PurchasePackage_InsufficientBalance(t *testing.T) {
	setupSubTestDB(t)
	user := seedTestUser(t, 5) // 余额 5，套餐 9.9
	pkg := seedPackage(t)
	app := newTestApp(user)

	code, resp := doJSON(t, app, "POST", "/purchase", map[string]any{
		"package_id": pkg.ID, "quantity": 1,
	})
	if code != 402 {
		t.Errorf("expected 402, got %d body=%v", code, resp)
	}
	if resp["message_code"] != "ERR_INSUFFICIENT_BALANCE" {
		t.Errorf("expected ERR_INSUFFICIENT_BALANCE, got %v", resp["message_code"])
	}

	// 验证：未创建订阅、未扣款
	var subs []database.UserSubscription
	database.DB.Where("user_id = ?", user.ID).Find(&subs)
	if len(subs) != 0 {
		t.Errorf("should have 0 subs, got %d", len(subs))
	}
	var fresh database.User
	database.DB.First(&fresh, user.ID)
	if fresh.Quota != 5*database.MicroPerUSD {
		t.Errorf("balance changed: %d (want 5*MicroPerUSD)", fresh.Quota)
	}
}

func TestIntegration_PurchasePackage_StackingOK(t *testing.T) {
	setupSubTestDB(t)
	user := seedTestUser(t, 100)
	pkg := seedPackage(t) // MaxActivePerUser=3
	app := newTestApp(user)

	for i := 1; i <= 3; i++ {
		code, _ := doJSON(t, app, "POST", "/purchase", map[string]any{
			"package_id": pkg.ID, "quantity": 1,
		})
		if code != 200 {
			t.Fatalf("purchase #%d failed code=%d", i, code)
		}
	}

	// 第 4 次必须 409
	code, resp := doJSON(t, app, "POST", "/purchase", map[string]any{
		"package_id": pkg.ID, "quantity": 1,
	})
	if code != 409 {
		t.Errorf("4th purchase: expected 409, got %d body=%v", code, resp)
	}
	if resp["message_code"] != "ERR_STACK_LIMIT" {
		t.Errorf("expected ERR_STACK_LIMIT, got %v", resp["message_code"])
	}

	// 验证：恰好 3 条订阅、stack_index = 1,2,3
	var subs []database.UserSubscription
	database.DB.Where("user_id = ?", user.ID).Order("stack_index asc").Find(&subs)
	if len(subs) != 3 {
		t.Fatalf("got %d subs, want 3", len(subs))
	}
	for i, s := range subs {
		if s.StackIndex != i+1 {
			t.Errorf("sub[%d].stack_index = %d, want %d", i, s.StackIndex, i+1)
		}
	}
}

func TestIntegration_PurchasePackage_NotPublic(t *testing.T) {
	setupSubTestDB(t)
	user := seedTestUser(t, 100)
	pkg := seedPackage(t, func(p *database.Package) { p.Public = false })
	app := newTestApp(user)

	code, resp := doJSON(t, app, "POST", "/purchase", map[string]any{
		"package_id": pkg.ID, "quantity": 1,
	})
	if code != 403 {
		t.Errorf("expected 403 for non-public package, got %d body=%v", code, resp)
	}
	if resp["message_code"] != "ERR_PACKAGE_NOT_PUBLIC" {
		t.Errorf("expected ERR_PACKAGE_NOT_PUBLIC, got %v", resp["message_code"])
	}
}

func TestIntegration_PurchasePackage_RequiresFullPrice(t *testing.T) {
	setupSubTestDB(t)
	user := seedTestUser(t, 5)
	pkg := seedPackage(t, func(p *database.Package) { p.PriceAmount = 10 * database.MicroPerUSD })
	app := newTestApp(user)

	code, resp := doJSON(t, app, "POST", "/purchase", map[string]any{
		"package_id": pkg.ID, "quantity": 1,
	})
	if code != 402 {
		t.Fatalf("expected 402 for full-price requirement, got %d body=%v", code, resp)
	}
	if resp["message_code"] != "ERR_INSUFFICIENT_BALANCE" {
		t.Errorf("expected ERR_INSUFFICIENT_BALANCE, got %v", resp["message_code"])
	}
}

// ─── 取消/退款 ──────────────────────────────────────────────────

// 业务模型修订（产品反馈第十轮）：
//   - 用户 cancel 不再自动退款，只标记 canceled
//   - 退款是 admin 协商后手动触发（AdminRefundSubscription）
//   - 此处保留两个测试覆盖 cancel 状态机的"立即取消"和"中期取消"两种场景，
//     断言重点改为 status / canceled_at / **不动 quota**。
func TestIntegration_CancelSubscription_StatusOnly_Immediate(t *testing.T) {
	setupSubTestDB(t)
	user := seedTestUser(t, 100)
	pkg := seedPackage(t)
	app := newTestApp(user)

	// 购买
	doJSON(t, app, "POST", "/purchase", map[string]any{"package_id": pkg.ID, "quantity": 1})

	var sub database.UserSubscription
	database.DB.Where("user_id = ?", user.ID).First(&sub)

	// 记录购买后的 quota（应已扣 10）
	var afterPurchase database.User
	database.DB.First(&afterPurchase, user.ID)
	quotaAfterPurchase := afterPurchase.Quota

	// 立即取消：仅状态机变更，无任何退款
	code, resp := doJSON(t, app, "DELETE", "/sub/"+itoaUint(sub.ID), nil)
	if code != 200 {
		t.Fatalf("cancel: code=%d body=%v", code, resp)
	}
	// 响应不应再含 refund_usd
	if _, hasRefund := resp["refund_usd"]; hasRefund {
		t.Errorf("cancel response should NOT include refund_usd anymore (admin-driven refund flow)")
	}

	// quota 不变（cancel 不动钱）
	var afterCancel database.User
	database.DB.First(&afterCancel, user.ID)
	if afterCancel.Quota != quotaAfterPurchase {
		t.Errorf("quota should remain %d after cancel, got %d", quotaAfterPurchase, afterCancel.Quota)
	}

	// 状态机正确
	var fresh database.UserSubscription
	database.DB.First(&fresh, sub.ID)
	if fresh.Status != "canceled" {
		t.Errorf("status=%q, want canceled", fresh.Status)
	}
	if fresh.CanceledAt == nil {
		t.Error("canceled_at not set")
	}
}

func TestIntegration_CancelSubscription_StatusOnly_MidPeriod(t *testing.T) {
	setupSubTestDB(t)
	user := seedTestUser(t, 100)
	pkg := seedPackage(t)
	app := newTestApp(user)

	doJSON(t, app, "POST", "/purchase", map[string]any{"package_id": pkg.ID, "quantity": 1})

	var sub database.UserSubscription
	database.DB.Where("user_id = ?", user.ID).First(&sub)

	// 模拟"已使用半周期"——对 cancel 路径无影响（不再有按时间退款）
	half := 15 * 24 * time.Hour
	database.DB.Model(&sub).Updates(map[string]any{
		"start_at": sub.StartAt.Add(-half),
		"end_at":   sub.EndAt.Add(-half),
	})

	var afterPurchase database.User
	database.DB.First(&afterPurchase, user.ID)
	quotaBefore := afterPurchase.Quota

	code, resp := doJSON(t, app, "DELETE", "/sub/"+itoaUint(sub.ID), nil)
	if code != 200 {
		t.Fatalf("cancel: code=%d body=%v", code, resp)
	}
	if _, hasRefund := resp["refund_usd"]; hasRefund {
		t.Errorf("cancel response should NOT include refund_usd")
	}

	var afterCancel database.User
	database.DB.First(&afterCancel, user.ID)
	if afterCancel.Quota != quotaBefore {
		t.Errorf("quota changed after cancel: %d → %d (should be unchanged)", quotaBefore, afterCancel.Quota)
	}
}

func TestIntegration_CancelSubscription_NotOwner(t *testing.T) {
	setupSubTestDB(t)
	owner := seedTestUser(t, 100)
	pkg := seedPackage(t)
	ownerApp := newTestApp(owner)
	doJSON(t, ownerApp, "POST", "/purchase", map[string]any{"package_id": pkg.ID, "quantity": 1})

	var sub database.UserSubscription
	database.DB.Where("user_id = ?", owner.ID).First(&sub)

	// 另一个用户尝试取消
	other := database.User{Username: "other", PasswordHash: "x", Token: "sk-other", Quota: 10, Status: 1}
	database.DB.Create(&other)
	otherApp := newTestApp(&other)

	code, resp := doJSON(t, otherApp, "DELETE", "/sub/"+itoaUint(sub.ID), nil)
	if code != 403 {
		t.Errorf("expected 403, got %d body=%v", code, resp)
	}
}

// newAdminTestApp 模拟 AdminGuard 已通过的状态，让 AdminRefundSubscription 能找到 admin user。
// loadAdminUser 走 cookie/Authorization 头 → DB 查 token=? AND role=admin AND status=1。
// 在测试中通过 Cookie 注入 admin.Token，admin 已在 seedAdminUser 中入库。
// fix MAJOR M22-A3（codex 第二十二轮）：内置真实 AdminGuard 让 CSRF / status=1 / cookie 鉴权
// 这条核心安全链路被业务测试覆盖到。doJSON 默认带同源 Origin，所以 happy-path 测试无需改动。
func newAdminTestApp(admin *database.User) *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error {
		c.Request().Header.SetCookie("daof_admin_token", admin.Token)
		return c.Next()
	})
	app.Use(middleware.AdminGuard)
	app.Post("/admin/sub/:id/refund", AdminRefundSubscription)
	return app
}

func seedAdminUser(t *testing.T) *database.User {
	t.Helper()
	u := database.User{
		Username: "admin",
		Role:     "admin",
		Token:    "sk-admin-token",
		Status:   1,
	}
	if err := database.DB.Create(&u).Error; err != nil {
		t.Fatalf("seed admin user: %v", err)
	}
	return &u
}

func TestIntegration_AdminRefund_Success(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 90) // 100 - 9.9 ≈ 90
	pkg := seedPackage(t)
	app := newAdminTestApp(admin)

	// 前置：用户已购买（含实际成交价 9.9 — R23+2 退款按这个字段算）
	sub := database.UserSubscription{
		UserID:                user.ID,
		PackageID:             pkg.ID,
		Status:                "active",
		PackageSnapshot:       `{"price_amount": 9900000}`, // micro_usd
		PurchasedUnitPriceUSD: 9_900_000,
	}
	database.DB.Create(&sub)
	quotaBefore := user.Quota

	// Admin 退款 5 USD
	code, resp := doJSON(t, app, "POST", "/admin/sub/"+itoaUint(sub.ID)+"/refund", map[string]any{
		"amount_micro_usd": 5 * database.MicroPerUSD,
		"reason":           "协商退款",
	})
	if code != 200 {
		t.Fatalf("expected 200, got %d, body=%v", code, resp)
	}
	if resp["success"] != true {
		t.Errorf("success=false: %v", resp)
	}

	// 验证：订阅状态
	var freshSub database.UserSubscription
	database.DB.First(&freshSub, sub.ID)
	if freshSub.Status != "refunded" {
		t.Errorf("status=%q, want refunded", freshSub.Status)
	}

	// 验证：用户余额
	var freshUser database.User
	database.DB.First(&freshUser, user.ID)
	want := quotaBefore + 5*database.MicroPerUSD
	if freshUser.Quota != want {
		t.Errorf("balance got %d, want %d", freshUser.Quota, want)
	}

	// 验证：审计日志
	var log database.OperationLog
	database.DB.Where("target_user_id = ?", user.ID).First(&log)
	if log.ID == 0 {
		t.Fatal("no audit log created")
	}
	if log.ActionType != "REFUND_SUBSCRIPTION" {
		t.Errorf("log action type: got %q", log.ActionType)
	}
	if !strings.Contains(log.Details, `"amount_micro_usd":5000000`) {
		t.Errorf("log details missing amount_micro_usd field: %s", log.Details)
	}
}

func TestAdminRefundSubscription_AmountMicroUSD(t *testing.T) {
	t.Run("accepts micro usd amount", func(t *testing.T) {
		setupSubTestDB(t)
		admin := seedAdminUser(t)
		user := seedTestUser(t, 100)
		pkg := seedPackage(t)
		app := newAdminTestApp(admin)

		sub := database.UserSubscription{
			UserID:                user.ID,
			PackageID:             pkg.ID,
			Status:                "active",
			PackageSnapshot:       `{"price_amount": 9900000}`,
			PurchasedUnitPriceUSD: 9_900_000,
		}
		database.DB.Create(&sub)

		code, resp := doJSON(t, app, "POST", "/admin/sub/"+itoaUint(sub.ID)+"/refund", map[string]any{
			"amount_micro_usd": 5_000_000,
			"reason":           "micro dto",
		})
		if code != 200 {
			t.Fatalf("expected 200, got %d body=%v", code, resp)
		}
		if resp["refund_micro_usd"] != float64(5_000_000) {
			t.Fatalf("refund_micro_usd=%v want 5000000", resp["refund_micro_usd"])
		}
	})

	t.Run("rejects amount above purchase", func(t *testing.T) {
		setupSubTestDB(t)
		admin := seedAdminUser(t)
		user := seedTestUser(t, 100)
		pkg := seedPackage(t)
		app := newAdminTestApp(admin)

		sub := database.UserSubscription{
			UserID:                user.ID,
			PackageID:             pkg.ID,
			Status:                "active",
			PackageSnapshot:       `{"price_amount": 9900000}`,
			PurchasedUnitPriceUSD: 9_900_000,
		}
		database.DB.Create(&sub)

		code, resp := doJSON(t, app, "POST", "/admin/sub/"+itoaUint(sub.ID)+"/refund", map[string]any{
			"amount_micro_usd": 10_000_000,
			"reason":           "too much",
		})
		if code != 400 {
			t.Fatalf("expected 400, got %d body=%v", code, resp)
		}
		if resp["message_code"] != "ERR_REFUND_AMOUNT_EXCEEDS_PURCHASE" {
			t.Fatalf("message_code=%v want ERR_REFUND_AMOUNT_EXCEEDS_PURCHASE", resp["message_code"])
		}
	})
}

func TestAdminRefundSubscription_Idempotent(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 100)
	app := newAdminTestApp(admin)
	sub := database.UserSubscription{
		UserID:                user.ID,
		PackageID:             1,
		Status:                "active",
		PackageSnapshot:       `{"package_id":1,"package_name":"Pro","price_amount":10000000}`,
		PurchasedUnitPriceUSD: 10 * database.MicroPerUSD,
	}
	if err := database.DB.Create(&sub).Error; err != nil {
		t.Fatalf("create sub: %v", err)
	}
	beforeQuota := user.Quota

	body := map[string]any{
		"amount_micro_usd": 4 * database.MicroPerUSD,
		"reason":           "idempotent retry",
	}
	code, resp := doJSON(t, app, "POST", "/admin/sub/"+itoaUint(sub.ID)+"/refund", body)
	if code != 200 {
		t.Fatalf("first refund got %d body=%v", code, resp)
	}
	code, resp = doJSON(t, app, "POST", "/admin/sub/"+itoaUint(sub.ID)+"/refund", body)
	if code != 409 {
		t.Fatalf("second refund got %d body=%v, want 409", code, resp)
	}

	var freshUser database.User
	if err := database.DB.First(&freshUser, user.ID).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	if freshUser.Quota != beforeQuota+4*database.MicroPerUSD {
		t.Fatalf("quota=%d, want %d", freshUser.Quota, beforeQuota+4*database.MicroPerUSD)
	}
	var refundCount int64
	if err := database.DB.Model(&database.BillingEntry{}).
		Where("user_id = ? AND entry_type = ? AND related_type = ? AND related_id = ?",
			user.ID, database.BillingTypeRefundSub, "subscription_refund", sub.ID).
		Count(&refundCount).Error; err != nil {
		t.Fatalf("count refund billing: %v", err)
	}
	if refundCount != 1 {
		t.Fatalf("refund billing count=%d, want 1", refundCount)
	}
}

func TestIntegration_AdminRefund_ExceedsPrice(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 100)
	pkg := seedPackage(t)
	app := newAdminTestApp(admin)

	sub := database.UserSubscription{
		UserID:                user.ID,
		PackageID:             pkg.ID,
		Status:                "active",
		PackageSnapshot:       `{"price_amount": 9900000}`,
		PurchasedUnitPriceUSD: 9_900_000,
	}
	database.DB.Create(&sub)

	// 退款 10 USD > 购买价 9.9
	code, resp := doJSON(t, app, "POST", "/admin/sub/"+itoaUint(sub.ID)+"/refund", map[string]any{
		"amount_micro_usd": 10 * database.MicroPerUSD,
		"reason":           "超额",
	})
	if code != 400 {
		t.Errorf("expected 400, got %d, body=%v", code, resp)
	}
	if resp["message_code"] != "ERR_REFUND_AMOUNT_EXCEEDS_PURCHASE" {
		t.Errorf("expected ERR_REFUND_AMOUNT_EXCEEDS_PURCHASE, got %v", resp["message_code"])
	}
}

func TestIntegration_AdminRefund_AlreadyRefunded(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 100)
	pkg := seedPackage(t)
	app := newAdminTestApp(admin)

	sub := database.UserSubscription{
		UserID:                user.ID,
		PackageID:             pkg.ID,
		Status:                "refunded", // 已退款
		PackageSnapshot:       `{"price_amount": 9900000}`,
		PurchasedUnitPriceUSD: 9_900_000,
	}
	database.DB.Create(&sub)

	code, resp := doJSON(t, app, "POST", "/admin/sub/"+itoaUint(sub.ID)+"/refund", map[string]any{
		"amount_micro_usd": 1 * database.MicroPerUSD,
		"reason":           "二次退款",
	})
	if code != 409 {
		t.Errorf("expected 409, got %d, body=%v", code, resp)
	}
	if resp["message_code"] != "ERR_SUB_STATUS_NOT_REFUNDABLE" {
		t.Errorf("expected ERR_SUB_STATUS_NOT_REFUNDABLE, got %v", resp["message_code"])
	}
}

// ─── R23+2 二轮：退款不退券 + 免费券保护 ─────────────

// fix CRITICAL R23+2-C1：免费券购买（PurchasedUnitPriceUSD=0）退款应被拒绝
func TestIntegration_AdminRefund_FreeCoupon_RejectZeroPaid(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 100)
	pkg := seedPackage(t)
	app := newAdminTestApp(admin)

	// 模拟：用户用免费券购买，PurchasedUnitPriceUSD=0
	sub := database.UserSubscription{
		UserID:                user.ID,
		PackageID:             pkg.ID,
		Status:                "active",
		PackageSnapshot:       `{"price_amount": 20000000}`, // snapshot 原价 $20 = 20_000_000 micro_usd
		PurchasedUnitPriceUSD: 0,                            // 实际成交 0（免费券）
		AppliedCouponID:       1,                            // 用了券
	}
	database.DB.Create(&sub)

	code, resp := doJSON(t, app, "POST", "/admin/sub/"+itoaUint(sub.ID)+"/refund", map[string]any{
		"amount_micro_usd": 5 * database.MicroPerUSD, // admin 试图退 $5
		"reason":           "试图套利",
	})
	if code != 400 {
		t.Errorf("expected 400 for free-coupon sub, got %d body=%v", code, resp)
	}
	if resp["message_code"] != "ERR_REFUND_ZERO_PAID" {
		t.Errorf("expected ERR_REFUND_ZERO_PAID, got %v", resp["message_code"])
	}
}

// fix R23+2 业务：退款**不**自动恢复券
func TestIntegration_AdminRefund_DoesNotRestoreCoupon(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 100)
	pkg := seedPackage(t)
	app := newAdminTestApp(admin)

	// 创建一张已用券
	usedCoupon := database.UserCoupon{
		UserID: user.ID, Code: "CP-test-used", Status: "used",
		SnapshotType: "fixed_price", SnapshotValue: 10 * database.MicroPerUSD,
	}
	database.DB.Create(&usedCoupon)

	// 该券绑定到一份订阅
	sub := database.UserSubscription{
		UserID:                user.ID,
		PackageID:             pkg.ID,
		Status:                "active",
		PackageSnapshot:       `{"price_amount": 20000000}`,
		PurchasedUnitPriceUSD: 10 * database.MicroPerUSD, // 用券买的
		AppliedCouponID:       usedCoupon.ID,
	}
	database.DB.Create(&sub)

	// 退款 $5
	code, _ := doJSON(t, app, "POST", "/admin/sub/"+itoaUint(sub.ID)+"/refund", map[string]any{
		"amount_micro_usd": 5 * database.MicroPerUSD,
		"reason":           "客户申请退款",
	})
	if code != 200 {
		t.Fatalf("refund should succeed, got %d", code)
	}

	// 关键断言：券保持 used，不被恢复
	var freshCoupon database.UserCoupon
	database.DB.First(&freshCoupon, usedCoupon.ID)
	if freshCoupon.Status != "used" {
		t.Errorf("券应保持 used（业务规则：退款不退券），got %q", freshCoupon.Status)
	}
}

// fix R23+2 业务定稿（第三次反馈）：退款不触碰券、不补发券；admin 想补偿走 AdminGrantCoupon。
// 此测试验证退款流程**没有**在 user 名下创建任何新券。
func TestIntegration_AdminRefund_DoesNotGrantNewCoupon(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 100)
	pkg := seedPackage(t)
	app := newAdminTestApp(admin)

	sub := database.UserSubscription{
		UserID:                user.ID,
		PackageID:             pkg.ID,
		Status:                "active",
		PackageSnapshot:       `{"price_amount": 20000000}`,
		PurchasedUnitPriceUSD: 20 * database.MicroPerUSD,
	}
	database.DB.Create(&sub)

	// 退款（普通 amount + reason，不含 regrant 字段）
	code, _ := doJSON(t, app, "POST", "/admin/sub/"+itoaUint(sub.ID)+"/refund", map[string]any{
		"amount_micro_usd": 5 * database.MicroPerUSD,
		"reason":           "误购买",
	})
	if code != 200 {
		t.Fatalf("refund should succeed, got %d", code)
	}

	// 关键断言：用户名下不应有任何新券（退款不补发）
	var couponCount int64
	database.DB.Model(&database.UserCoupon{}).Where("user_id = ?", user.ID).Count(&couponCount)
	if couponCount != 0 {
		t.Errorf("退款不应创建新券，发现 %d 张券", couponCount)
	}
}

func TestIntegration_PurchaseWithBelowFloorCouponRejected(t *testing.T) {
	setupSubTestDB(t)
	user := seedTestUser(t, 100)

	// 套餐价 $20，cost_floor 未配置时必须 fallback 到 price_amount，不能用 $0 券。
	pkg := database.Package{
		Name:                 "CouponPkg",
		PriceAmount:          20 * database.MicroPerUSD,
		BillingPeriodSeconds: 2592000,
		Public:               true,
	}
	enabled := true
	pkg.Enabled = &enabled
	stack := true
	pkg.Stackable = &stack
	pkg.MaxActivePerUser = 5
	database.DB.Create(&pkg)

	// 给用户一张低于 fallback 下限的 $1 券
	lowCoupon := database.UserCoupon{
		UserID: user.ID, Code: "CP-test-low-price", Status: "available",
		SnapshotType: "fixed_price", SnapshotValue: database.MicroPerUSD, SnapshotPackageIDs: "",
	}
	database.DB.Create(&lowCoupon)

	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error { c.Locals("user", user); return c.Next() })
	app.Post("/purchase", PurchasePackage)

	quotaBefore := user.Quota
	code, resp := doJSON(t, app, "POST", "/purchase", map[string]any{
		"package_id": pkg.ID,
		"quantity":   1,
		"coupon_id":  lowCoupon.ID,
	})
	if code != 409 {
		t.Fatalf("purchase should reject below-floor coupon, got %d body=%v", code, resp)
	}
	if resp["message_code"] != "ERR_COUPON_SNAPSHOT_BELOW_COST_FLOOR" {
		t.Fatalf("message_code=%v want ERR_COUPON_SNAPSHOT_BELOW_COST_FLOOR", resp["message_code"])
	}

	// 关键断言：拒绝路径不能改余额，也不能把券标记为 used。
	var fresh database.User
	database.DB.First(&fresh, user.ID)
	deltaBalance := fresh.Quota - quotaBefore
	if deltaBalance != 0 {
		t.Errorf("rejected coupon changed balance: delta=%d micro_usd want 0", deltaBalance)
	}

	var freshCoupon database.UserCoupon
	database.DB.First(&freshCoupon, lowCoupon.ID)
	if freshCoupon.Status != "available" {
		t.Errorf("rejected coupon status=%q want available", freshCoupon.Status)
	}
}

// itoaUint 极简 uint→string，避免 strconv 导入
func itoaUint(u uint) string {
	if u == 0 {
		return "0"
	}
	buf := make([]byte, 0, 10)
	for u > 0 {
		buf = append([]byte{byte('0' + u%10)}, buf...)
		u /= 10
	}
	return string(buf)
}
