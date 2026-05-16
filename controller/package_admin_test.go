package controller

import (
	"testing"
	"time"

	"daof-ai-hub/database"
	"daof-ai-hub/middleware"

	"github.com/gofiber/fiber/v2"
)

// fix MAJOR M22-A3（codex 第二十二轮）：内置真实 AdminGuard
func newPkgAdminTestApp(admin *database.User) *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error {
		c.Request().Header.SetCookie("daof_admin_token", admin.Token)
		return c.Next()
	})
	app.Use(middleware.AdminGuard)
	app.Get("/admin/packages", ListPackagesAdmin)
	app.Get("/admin/packages/:id", GetPackageAdmin)
	app.Post("/admin/packages", CreatePackage)
	app.Put("/admin/packages/:id", UpdatePackage)
	app.Delete("/admin/packages/:id", DeletePackage)
	app.Post("/admin/packages/reorder", ReorderPackages)
	app.Get("/packages/public", ListPublicPackages)
	return app
}

// ─── validatePackagePayload ─────────────────────────────────────────

func TestValidatePackagePayload(t *testing.T) {
	valid := database.Package{
		Name:                 "Test",
		PriceAmount:          9_900_000, // $9.90 micro_usd
		CostFloorMicroUSD:    2_000_000,
		BillingPeriodSeconds: 2592000,
		MaxActivePerUser:     5,
		SortOrder:            0,
	}
	if err := validatePackagePayload(&valid); err != nil {
		t.Errorf("valid payload rejected: %v", err)
	}

	// fix MAJOR M22-A1 Phase 1：金额已是 int64，NaN/Inf 浮点路径不再适用。
	// 仍保留负值边界检查。
	cases := []struct {
		name string
		mod  func(*database.Package)
	}{
		{"negative price", func(p *database.Package) { p.PriceAmount = -1 }},
		{"negative cost floor", func(p *database.Package) { p.CostFloorMicroUSD = -1 }},
		{"cost floor exceeds price", func(p *database.Package) { p.CostFloorMicroUSD = p.PriceAmount + 1 }},
		{"zero billing period", func(p *database.Package) { p.BillingPeriodSeconds = 0 }},
		{"negative billing period", func(p *database.Package) { p.BillingPeriodSeconds = -1 }},
		{"negative max active", func(p *database.Package) { p.MaxActivePerUser = -1 }},
		{"negative sort order", func(p *database.Package) { p.SortOrder = -1 }},
		// C4 第二十轮: 上限校验
		{"period exceeds 5y cap", func(p *database.Package) {
			p.BillingPeriodSeconds = MaxBillingPeriodSeconds + 1
		}},
		{"period int64 overflow attempt", func(p *database.Package) {
			p.BillingPeriodSeconds = 9_223_372_036
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := valid
			tc.mod(&p)
			if err := validatePackagePayload(&p); err == nil {
				t.Error("expected validation error")
			}
		})
	}
}

// ─── CreatePackage ───────────────────────────────────────────────────

func TestCreatePackage_HappyPath(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	app := newPkgAdminTestApp(admin)

	code, resp := doJSON(t, app, "POST", "/admin/packages", map[string]any{
		"name":                   "Pro Plan",
		"price_micro_usd":        int64(9_900_000),
		"cost_floor_micro_usd":   2_000_000,
		"billing_period_seconds": 2592000,
		"max_active_per_user":    5,
	})
	if code != 200 {
		t.Fatalf("expected 200 got %d body=%v", code, resp)
	}
	if resp["message_code"] != "SUCCESS_CREATED" {
		t.Errorf("expected SUCCESS_CREATED got %v", resp["message_code"])
	}
	data, _ := resp["data"].(map[string]any)
	if data == nil || data["id"] == nil {
		t.Fatal("expected data.id")
	}
	if data["cost_floor_micro_usd"] != float64(2_000_000) {
		t.Fatalf("cost_floor_micro_usd=%v want 2000000", data["cost_floor_micro_usd"])
	}
	var pkg database.Package
	if err := database.DB.First(&pkg, uint(data["id"].(float64))).Error; err != nil {
		t.Fatalf("load created package: %v", err)
	}
	if pkg.CostFloorMicroUSD != 2_000_000 {
		t.Fatalf("db cost_floor_micro_usd=%d want 2000000", pkg.CostFloorMicroUSD)
	}
	if pkg.PriceAmount != 9_900_000 {
		t.Fatalf("db price_amount=%d want 9900000", pkg.PriceAmount)
	}
}

func TestCreatePackage_ValidationRejects(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	app := newPkgAdminTestApp(admin)

	cases := []struct {
		name string
		body map[string]any
	}{
		{"missing name", map[string]any{"price_micro_usd": int64(10_000_000), "billing_period_seconds": 86400}},
		{"negative price", map[string]any{"name": "x", "price_micro_usd": int64(-1), "billing_period_seconds": 86400}},
		{"negative cost floor", map[string]any{"name": "x", "price_micro_usd": int64(10_000_000), "cost_floor_micro_usd": -1, "billing_period_seconds": 86400}},
		{"cost floor exceeds price", map[string]any{"name": "x", "price_micro_usd": int64(10_000_000), "cost_floor_micro_usd": 11_000_000, "billing_period_seconds": 86400}},
		{"zero period", map[string]any{"name": "x", "price_micro_usd": int64(10_000_000), "billing_period_seconds": 0}},
		{"deprecated bonus field", map[string]any{"name": "x", "price_micro_usd": int64(10_000_000), "billing_period_seconds": 86400, "bonus_balance_usd": 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _ := doJSON(t, app, "POST", "/admin/packages", tc.body)
			if code != 400 {
				t.Errorf("expected 400 got %d", code)
			}
		})
	}
}

func TestCreatePackage_CostFloorInvalidMessageCode(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	app := newPkgAdminTestApp(admin)

	code, resp := doJSON(t, app, "POST", "/admin/packages", map[string]any{
		"name":                   "Bad Floor",
		"price_micro_usd":        int64(10_000_000),
		"cost_floor_micro_usd":   10_000_001,
		"billing_period_seconds": 86400,
	})
	if code != 400 {
		t.Fatalf("expected 400 got %d body=%v", code, resp)
	}
	if resp["message_code"] != MessageCodePackageCostFloorInvalid {
		t.Fatalf("message_code=%v want %s", resp["message_code"], MessageCodePackageCostFloorInvalid)
	}
}

func TestPackageAdmin_PriceAndCostFloorMicroUSD(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	app := newPkgAdminTestApp(admin)

	code, resp := doJSON(t, app, "POST", "/admin/packages", map[string]any{
		"name":                   "Micro Exact",
		"price_micro_usd":        int64(12_345_678),
		"cost_floor_micro_usd":   int64(6_543_210),
		"billing_period_seconds": 86400,
	})
	if code != 200 {
		t.Fatalf("create expected 200 got %d body=%v", code, resp)
	}
	data, _ := resp["data"].(map[string]any)
	pkgID := uint(data["id"].(float64))
	var created database.Package
	if err := database.DB.First(&created, pkgID).Error; err != nil {
		t.Fatalf("load created package: %v", err)
	}
	if created.PriceAmount != 12_345_678 || created.CostFloorMicroUSD != 6_543_210 {
		t.Fatalf("created price/cost_floor=(%d,%d), want (12345678,6543210)", created.PriceAmount, created.CostFloorMicroUSD)
	}

	code, resp = doJSON(t, app, "PUT", "/admin/packages/"+itoaUint(pkgID), map[string]any{
		"name":                   "Micro Exact Updated",
		"price_micro_usd":        int64(22_000_001),
		"cost_floor_micro_usd":   int64(7_000_001),
		"billing_period_seconds": 86400,
	})
	if code != 200 {
		t.Fatalf("update expected 200 got %d body=%v", code, resp)
	}
	var updated database.Package
	if err := database.DB.First(&updated, pkgID).Error; err != nil {
		t.Fatalf("load updated package: %v", err)
	}
	if updated.PriceAmount != 22_000_001 || updated.CostFloorMicroUSD != 7_000_001 {
		t.Fatalf("updated price/cost_floor=(%d,%d), want (22000001,7000001)", updated.PriceAmount, updated.CostFloorMicroUSD)
	}
}

// ─── GetPackageAdmin ─────────────────────────────────────────────────

func TestGetPackageAdmin_NotFound(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	app := newPkgAdminTestApp(admin)

	code, resp := doJSON(t, app, "GET", "/admin/packages/99999", nil)
	if code != 404 {
		t.Errorf("expected 404 got %d body=%v", code, resp)
	}
}

func TestGetPackageAdmin_Found(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	app := newPkgAdminTestApp(admin)

	pkg := seedPackage(t)
	code, resp := doJSON(t, app, "GET", "/admin/packages/"+itoaUint(pkg.ID), nil)
	if code != 200 {
		t.Fatalf("expected 200 got %d body=%v", code, resp)
	}
	data, _ := resp["data"].(map[string]any)
	if data == nil {
		t.Fatal("expected data")
	}
	if data["name"] != pkg.Name {
		t.Errorf("name mismatch: got %v want %v", data["name"], pkg.Name)
	}
}

func TestReorderPackages_StaleIDReturns404(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	app := newPkgAdminTestApp(admin)

	code, resp := doJSON(t, app, "POST", "/admin/packages/reorder", map[string]any{
		"ids": []uint{99999},
	})
	if code != 404 || resp["message_code"] != "ERR_REORDER_STALE_ID" {
		t.Fatalf("reorder stale id got %d/%v, want 404/ERR_REORDER_STALE_ID", code, resp["message_code"])
	}
}

// ─── UpdatePackage ───────────────────────────────────────────────────

func TestUpdatePackage_HappyPath(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	app := newPkgAdminTestApp(admin)

	pkg := seedPackage(t)
	code, resp := doJSON(t, app, "PUT", "/admin/packages/"+itoaUint(pkg.ID), map[string]any{
		"name":                   "Renamed",
		"price_micro_usd":        int64(19_900_000),
		"cost_floor_micro_usd":   3_000_000,
		"billing_period_seconds": 2592000,
	})
	if code != 200 {
		t.Fatalf("expected 200 got %d body=%v", code, resp)
	}
	data, _ := resp["data"].(map[string]any)
	if data["name"] != "Renamed" {
		t.Errorf("name not updated: %v", data["name"])
	}
	if data["cost_floor_micro_usd"] != float64(3_000_000) {
		t.Errorf("cost_floor_micro_usd=%v want 3000000", data["cost_floor_micro_usd"])
	}
	var fresh database.Package
	if err := database.DB.First(&fresh, pkg.ID).Error; err != nil {
		t.Fatalf("load updated package: %v", err)
	}
	if fresh.CostFloorMicroUSD != 3_000_000 {
		t.Errorf("db cost_floor_micro_usd=%d want 3000000", fresh.CostFloorMicroUSD)
	}
	if fresh.PriceAmount != 19_900_000 {
		t.Errorf("db price_amount=%d want 19900000", fresh.PriceAmount)
	}
}

func TestUpdatePackage_NotFound(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	app := newPkgAdminTestApp(admin)

	code, _ := doJSON(t, app, "PUT", "/admin/packages/99999", map[string]any{
		"name": "x", "price_micro_usd": int64(10_000_000), "billing_period_seconds": 86400,
	})
	if code != 404 {
		t.Errorf("expected 404 got %d", code)
	}
}

// ─── DeletePackage ───────────────────────────────────────────────────

func TestDeletePackage_HappyPath(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	app := newPkgAdminTestApp(admin)

	pkg := seedPackage(t)
	code, resp := doJSON(t, app, "DELETE", "/admin/packages/"+itoaUint(pkg.ID), nil)
	if code != 200 {
		t.Fatalf("expected 200 got %d body=%v", code, resp)
	}
	if resp["message_code"] != "SUCCESS_DELETED" {
		t.Errorf("expected SUCCESS_DELETED got %v", resp["message_code"])
	}
	// verify gone
	code2, _ := doJSON(t, app, "GET", "/admin/packages/"+itoaUint(pkg.ID), nil)
	if code2 != 404 {
		t.Errorf("package should be gone, got %d", code2)
	}
}

func TestDeletePackage_BlockedByActiveSubs(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	app := newPkgAdminTestApp(admin)

	pkg := seedPackage(t)
	user := seedTestUser(t, 100)
	database.DB.Create(&database.UserSubscription{
		UserID:          user.ID,
		PackageID:       pkg.ID,
		Status:          "active",
		StartAt:         time.Now(),
		EndAt:           time.Now().Add(30 * 24 * time.Hour),
		PackageSnapshot: `{"price_amount":9.9}`,
	})

	code, resp := doJSON(t, app, "DELETE", "/admin/packages/"+itoaUint(pkg.ID), nil)
	if code != 409 {
		t.Fatalf("expected 409 (active subs) got %d body=%v", code, resp)
	}
	if resp["message_code"] != "ERR_PACKAGE_HAS_ACTIVE_SUBS" {
		t.Errorf("expected ERR_PACKAGE_HAS_ACTIVE_SUBS got %v", resp["message_code"])
	}
	count, _ := resp["active_count"].(float64)
	if count != 1 {
		t.Errorf("expected active_count=1 got %v", resp["active_count"])
	}
}

func TestDeletePackage_AllowedWhenSubExpired(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	app := newPkgAdminTestApp(admin)

	pkg := seedPackage(t)
	user := seedTestUser(t, 100)
	database.DB.Create(&database.UserSubscription{
		UserID:          user.ID,
		PackageID:       pkg.ID,
		Status:          "active",
		StartAt:         time.Now().Add(-60 * 24 * time.Hour),
		EndAt:           time.Now().Add(-1 * time.Hour), // already expired
		PackageSnapshot: `{"price_amount":9.9}`,
	})

	code, resp := doJSON(t, app, "DELETE", "/admin/packages/"+itoaUint(pkg.ID), nil)
	if code != 200 {
		t.Errorf("expired sub should not block delete, got %d body=%v", code, resp)
	}
}

func TestDeletePackage_NotFound(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	app := newPkgAdminTestApp(admin)

	code, resp := doJSON(t, app, "DELETE", "/admin/packages/99999", nil)
	if code != 404 {
		t.Errorf("expected 404 got %d body=%v", code, resp)
	}
}

// ─── ListPackagesAdmin ───────────────────────────────────────────────

func TestListPackagesAdmin_Empty(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	app := newPkgAdminTestApp(admin)

	code, resp := doJSON(t, app, "GET", "/admin/packages", nil)
	if code != 200 {
		t.Fatalf("expected 200 got %d", code)
	}
	data, _ := resp["data"].([]any)
	if len(data) != 0 {
		t.Errorf("expected empty list got %d items", len(data))
	}
}

func TestListPackagesAdmin_WithData(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	app := newPkgAdminTestApp(admin)

	first := seedPackage(t)
	seedPackage(t, func(p *database.Package) { p.Name = "Second" })
	if err := database.DB.Create(&database.UserSubscription{
		UserID:                admin.ID,
		PackageID:             first.ID,
		Status:                "active",
		IsGranted:             true,
		StartAt:               time.Now(),
		EndAt:                 time.Now().Add(30 * 24 * time.Hour),
		PackageSnapshot:       `{"package_name":"TestPro"}`,
		ConsumptionOrder:      time.Now().UnixMicro(),
		PurchasedUnitPriceUSD: 0,
	}).Error; err != nil {
		t.Fatalf("seed granted subscription: %v", err)
	}

	code, resp := doJSON(t, app, "GET", "/admin/packages", nil)
	if code != 200 {
		t.Fatalf("expected 200 got %d", code)
	}
	data, _ := resp["data"].([]any)
	if len(data) != 2 {
		t.Errorf("expected 2 packages got %d", len(data))
	}
	var got map[string]any
	for _, raw := range data {
		row, _ := raw.(map[string]any)
		if row["id"] == float64(first.ID) {
			got = row
			break
		}
	}
	if got == nil {
		t.Fatalf("seeded package not found in response: %v", data)
	}
	if got["plan_count"] != float64(1) {
		t.Fatalf("plan_count=%v want 1; row=%v", got["plan_count"], got)
	}
	if got["active_subs_count"] != float64(1) {
		t.Fatalf("active_subs_count=%v want 1; row=%v", got["active_subs_count"], got)
	}
}

// ─── ListPublicPackages ──────────────────────────────────────────────

func TestListPublicPackages_FiltersCorrectly(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	app := newPkgAdminTestApp(admin)

	enabled := true
	disabled := false
	// public + enabled
	database.DB.Create(&database.Package{
		Name: "Pub", PriceAmount: 10, BillingPeriodSeconds: 86400,
		Public: true, Enabled: &enabled,
	})
	// public + disabled — should NOT appear
	database.DB.Create(&database.Package{
		Name: "Dis", PriceAmount: 10, BillingPeriodSeconds: 86400,
		Public: true, Enabled: &disabled,
	})
	// private + enabled — should NOT appear
	database.DB.Create(&database.Package{
		Name: "Priv", PriceAmount: 10, BillingPeriodSeconds: 86400,
		Public: false, Enabled: &enabled,
	})

	code, resp := doJSON(t, app, "GET", "/packages/public", nil)
	if code != 200 {
		t.Fatalf("expected 200 got %d", code)
	}
	data, _ := resp["data"].([]any)
	if len(data) != 1 {
		t.Errorf("expected 1 public+enabled package got %d", len(data))
	}
}

func TestListPublicPackages_HidesCostFloor(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	app := newPkgAdminTestApp(admin)

	enabled := true
	if err := database.DB.Create(&database.Package{
		Name: "Pub", PriceAmount: 10 * database.MicroPerUSD, CostFloorMicroUSD: 8 * database.MicroPerUSD,
		BillingPeriodSeconds: 86400, Public: true, Enabled: &enabled,
	}).Error; err != nil {
		t.Fatalf("seed package: %v", err)
	}

	code, resp := doJSON(t, app, "GET", "/packages/public", nil)
	if code != 200 {
		t.Fatalf("expected 200 got %d body=%v", code, resp)
	}
	data, _ := resp["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("expected 1 public package got %d", len(data))
	}
	row, _ := data[0].(map[string]any)
	if _, ok := row["cost_floor_micro_usd"]; ok {
		t.Fatalf("public package response must not expose cost_floor_micro_usd: %v", row)
	}
}

// ─── helpers ─────────────────────────────────────────────────────────
//
// 注（fix MAJOR M22-A1 Phase 1）：之前 nanFloat / infFloat 用于测试 float64 字段的
// NaN/Inf 边界，现在金额字段已切到 int64 micro_usd，不再需要这些 helper。
