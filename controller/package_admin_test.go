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
	app.Get("/packages/public", ListPublicPackages)
	return app
}

// ─── validatePackagePayload ─────────────────────────────────────────

func TestValidatePackagePayload(t *testing.T) {
	valid := database.Package{
		Name:                 "Test",
		PriceAmount:          9_900_000, // $9.90 micro_usd
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
		"price_amount":           9.9,
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
}

func TestCreatePackage_ValidationRejects(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	app := newPkgAdminTestApp(admin)

	cases := []struct {
		name string
		body map[string]any
	}{
		{"missing name", map[string]any{"price_amount": 10, "billing_period_seconds": 86400}},
		{"negative price", map[string]any{"name": "x", "price_amount": -1, "billing_period_seconds": 86400}},
		{"zero period", map[string]any{"name": "x", "price_amount": 10, "billing_period_seconds": 0}},
		{"deprecated bonus field", map[string]any{"name": "x", "price_amount": 10, "billing_period_seconds": 86400, "bonus_balance_usd": 0}},
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

// ─── UpdatePackage ───────────────────────────────────────────────────

func TestUpdatePackage_HappyPath(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	app := newPkgAdminTestApp(admin)

	pkg := seedPackage(t)
	code, resp := doJSON(t, app, "PUT", "/admin/packages/"+itoaUint(pkg.ID), map[string]any{
		"name":                   "Renamed",
		"price_amount":           19.9,
		"billing_period_seconds": 2592000,
	})
	if code != 200 {
		t.Fatalf("expected 200 got %d body=%v", code, resp)
	}
	data, _ := resp["data"].(map[string]any)
	if data["name"] != "Renamed" {
		t.Errorf("name not updated: %v", data["name"])
	}
}

func TestUpdatePackage_NotFound(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	app := newPkgAdminTestApp(admin)

	code, _ := doJSON(t, app, "PUT", "/admin/packages/99999", map[string]any{
		"name": "x", "price_amount": 10, "billing_period_seconds": 86400,
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

// ─── helpers ─────────────────────────────────────────────────────────
//
// 注（fix MAJOR M22-A1 Phase 1）：之前 nanFloat / infFloat 用于测试 float64 字段的
// NaN/Inf 边界，现在金额字段已切到 int64 micro_usd，不再需要这些 helper。
