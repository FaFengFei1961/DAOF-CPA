// Package controller / json_wire_format_test.go
//
// 端到端 API JSON wire format 验证（fix MAJOR M22-A1 Phase 3）：
//
// 验证 admin / user 关键 GET 接口返回的 JSON 中：
//   - 金额字段一律 USD float（前端可直接 formatCurrency 消费）
//   - 不会泄漏 raw int64 micro_usd（如 quota=99000000000 这种"看起来像 999 亿美金"的脏值）
//
// 这一层测试与 database/marshaling_test.go 的差别：
//   - marshaling_test：单元测试 struct.MarshalJSON 直接行为
//   - 本文件：通过实际 HTTP handler + Fiber c.JSON 链路，验证整条 API 没有 leak
package controller

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"daof-ai-hub/database"
	"daof-ai-hub/middleware"

	"github.com/gofiber/fiber/v2"
)

// runAdminGETAndDecode 复用：跑 admin 鉴权 + handler + 解析 JSON 响应
func runAdminGETAndDecode(t *testing.T, admin *database.User, route, path string, handler fiber.Handler) map[string]any {
	t.Helper()
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error {
		c.Request().Header.SetCookie("daof_admin_token", admin.Token)
		return c.Next()
	})
	app.Use(middleware.AdminGuard)
	app.Get(route, handler)

	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("Origin", "http://example.com")
	req.Host = "example.com"
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

// TestWireFormat_AdminUsers_QuotaInUSDFloat /api/admin/users 返回的 user 列表中
// quota 字段必须是 USD float（被 User.MarshalJSON 转换），不是 raw micro_usd 整数。
func TestWireFormat_AdminUsers_QuotaInUSDFloat(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 99.9) // 内部存 99_900_000 micro

	out := runAdminGETAndDecode(t, admin, "/admin/users", "/admin/users", GetUsers)
	data, _ := out["data"].([]any)
	if len(data) < 1 {
		t.Fatalf("expected at least 1 user, got %v", out)
	}

	// 找到我们刚 seed 的 user
	var u map[string]any
	for _, item := range data {
		m, _ := item.(map[string]any)
		if m["id"] == float64(user.ID) {
			u = m
			break
		}
	}
	if u == nil {
		t.Fatalf("user %d not found in list", user.ID)
	}

	// 关键断言：quota 是 USD float，不是 raw int64 micro
	q, ok := u["quota"].(float64)
	if !ok {
		t.Fatalf("quota should be float, got %T: %v", u["quota"], u["quota"])
	}
	if q != 99.9 {
		t.Errorf("quota should be 99.9 USD, got %v", q)
	}
	// 防御：raw int64 micro 会让 q 变成 99_900_000.0，明显不合理
	if q > 1e6 {
		t.Errorf("quota %v looks like raw micro_usd leakage (should be 99.9, not millions)", q)
	}
}

// TestWireFormat_AdminTopupOrders 验证 admin topup 列表中 money_rmb / amount_usd / refunded_amount_rmb 都转换。
func TestWireFormat_AdminTopupOrders(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 0)

	// seed 3 笔不同状态的 topup
	database.DB.Create(&database.TopupOrder{
		OutTradeNo: "tp_wire_1", UserID: user.ID,
		MoneyRMB:             7200,                      // ¥72.00
		AmountUSD:            10 * database.MicroPerUSD, // $10
		RefundedAmountRMB:    3600,                      // 已退 ¥36
		ExchangeRateSnapshot: 7.2, Status: "paid",
	})

	out := runAdminGETAndDecode(t, admin, "/admin/topup/orders", "/admin/topup/orders?status=paid", AdminListTopupOrders)
	data, _ := out["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("expected 1 paid order, got %d", len(data))
	}
	row, _ := data[0].(map[string]any)

	// money_rmb: 7200 fen → 72.0 RMB float
	if v, _ := row["money_rmb"].(float64); v != 72.0 {
		t.Errorf("money_rmb: got %v, want 72.0", row["money_rmb"])
	}
	// amount_usd: 10_000_000 micro → 10.0 USD float
	if v, _ := row["amount_usd"].(float64); v != 10.0 {
		t.Errorf("amount_usd: got %v, want 10.0", row["amount_usd"])
	}
	// refunded_amount_rmb: 3600 fen → 36.0 RMB float
	if v, _ := row["refunded_amount_rmb"].(float64); v != 36.0 {
		t.Errorf("refunded_amount_rmb: got %v, want 36.0", row["refunded_amount_rmb"])
	}
}

// TestWireFormat_GetSelfData /api/user/me 返回 quota 是 USD float（显式 map 路径）
func TestWireFormat_GetSelfData(t *testing.T) {
	setupSubTestDB(t)
	user := seedTestUser(t, 50)

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("user", user)
		return c.Next()
	})
	app.Get("/me", GetSelfData)

	req := httptest.NewRequest("GET", "/me", nil)
	resp, _ := app.Test(req, -1)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	data, _ := out["data"].(map[string]any)
	q, ok := data["quota"].(float64)
	if !ok {
		t.Fatalf("quota type: %T value: %v", data["quota"], data["quota"])
	}
	if q != 50.0 {
		t.Errorf("expected quota=50.0, got %v", q)
	}
}

// TestWireFormat_AdminListPackages /admin/packages 列表 PriceAmount 应是 USD float，且不再暴露套餐 bonus 字段。
func TestWireFormat_AdminListPackages(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)

	pkg := seedPackage(t, func(p *database.Package) {
		p.PriceAmount = 19_900_000 // $19.90
	})

	out := runAdminGETAndDecode(t, admin, "/admin/packages/:id", "/admin/packages/"+strconv.FormatUint(uint64(pkg.ID), 10), GetPackageAdmin)
	data, _ := out["data"].(map[string]any)
	if data == nil {
		t.Fatalf("data missing in %v", out)
	}
	if v, _ := data["price_amount"].(float64); v != 19.9 {
		t.Errorf("price_amount: got %v, want 19.9", data["price_amount"])
	}
	if _, ok := data["bonus_balance_usd"]; ok {
		t.Errorf("bonus_balance_usd should not be exposed: %v", data["bonus_balance_usd"])
	}
	plans, _ := data["plans"].([]any)
	if len(plans) != 1 {
		t.Fatalf("package detail should expose plans, got %v", data["plans"])
	}

	listOut := runAdminGETAndDecode(t, admin, "/admin/packages", "/admin/packages", ListPackagesAdmin)
	list, _ := listOut["data"].([]any)
	if len(list) != 1 {
		t.Fatalf("list data len=%d out=%v", len(list), listOut)
	}
	row, _ := list[0].(map[string]any)
	if v, _ := row["price_amount"].(float64); v != 19.9 {
		t.Errorf("list price_amount: got %v, want 19.9", row["price_amount"])
	}
	if _, ok := row["plan_count"]; !ok {
		t.Fatalf("list row missing plan_count: %v", row)
	}
	if _, ok := row["active_subs_count"]; !ok {
		t.Fatalf("list row missing active_subs_count: %v", row)
	}
}

func TestWireFormat_PublicPackagesHideInternalQuotaUnit(t *testing.T) {
	setupSubTestDB(t)
	seedPackage(t)
	if err := database.DB.Model(&database.QuotaPlan{}).Where("name = ?", "test_plan_request_count").
		Updates(map[string]any{
			"limit_unit":  "api_cost_usd",
			"limit_value": 125,
		}).Error; err != nil {
		t.Fatalf("update plan: %v", err)
	}

	app := fiber.New()
	app.Get("/api/packages", ListPublicPackages)
	req := httptest.NewRequest("GET", "/api/packages", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	text := string(body)
	if strings.Contains(text, "api_cost_usd") || strings.Contains(text, "model_match") || strings.Contains(text, "weight_factor") {
		t.Fatalf("public package response leaks internal quota fields: %s", text)
	}
	if !strings.Contains(text, "API 等值额度") {
		t.Fatalf("public package response should include user-facing quota label: %s", text)
	}
}

// TestWireFormat_AdminListCouponTemplates 模板列表 discount_value 应是 USD float
func TestWireFormat_AdminListCouponTemplates(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)

	enabled := true
	database.DB.Create(&database.CouponTemplate{
		Name: "Wire Test", DiscountType: "fixed_price",
		DiscountValue: 4_990_000, // $4.99
		ValidDays:     30, Enabled: &enabled,
	})

	out := runAdminGETAndDecode(t, admin, "/admin/coupon-templates", "/admin/coupon-templates", AdminListCouponTemplates)
	data, _ := out["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("expected 1 template, got %d", len(data))
	}
	tpl, _ := data[0].(map[string]any)
	if v, _ := tpl["discount_value"].(float64); v != 4.99 {
		t.Errorf("discount_value: got %v, want 4.99", tpl["discount_value"])
	}
}

// TestWireFormat_NoMicroUSDLeak 通用 leak 检查：扫描整个 admin user 列表 JSON，
// 任何字段值若是数字且 > 1e9，几乎必是 micro_usd leak（合理 USD 不可能 > 10 亿）。
func TestWireFormat_NoMicroUSDLeak(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	// seed 多个 user 不同金额
	for i := 1; i <= 3; i++ {
		database.DB.Create(&database.User{
			Username: "leakcheck_" + strconv.Itoa(i),
			Token:    "sk-leak-" + strconv.Itoa(i),
			Quota:    int64(i) * 1000 * database.MicroPerUSD, // $1000 / $2000 / $3000
			Status:   1,
			Role:     "user",
		})
	}

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error {
		c.Request().Header.SetCookie("daof_admin_token", admin.Token)
		return c.Next()
	})
	app.Use(middleware.AdminGuard)
	app.Get("/admin/users", GetUsers)

	req := httptest.NewRequest("GET", "/admin/users", nil)
	req.Header.Set("Origin", "http://example.com")
	req.Host = "example.com"
	resp, _ := app.Test(req, -1)
	defer resp.Body.Close()

	// 读 raw JSON body 字符串扫描：任何数字 > 1e9 都疑似 leak
	buf := make([]byte, 0, 8192)
	tmp := make([]byte, 1024)
	for {
		n, err := resp.Body.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			break
		}
	}
	body := string(buf)

	// 简单正则不适合做"数字提取"，但已知 user 们 quota=$1000/2000/3000 USD，
	// 序列化后应是 1000 / 2000 / 3000（最大 4 位数字）。
	// 如果 leak 成 raw micro，会出现 1000000000 / 2000000000 / 3000000000（10 位）。
	for _, badNum := range []string{"1000000000", "2000000000", "3000000000"} {
		if strings.Contains(body, badNum) {
			t.Errorf("micro_usd LEAK detected: %s appears in JSON body (should be USD float)", badNum)
		}
	}

	// 防御断言：USD 数值应可见
	if !strings.Contains(body, "1000") {
		t.Errorf("expected USD value 1000 in body, got: %s", body[:min(500, len(body))])
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
