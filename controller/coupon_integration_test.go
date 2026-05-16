package controller

import (
	"strings"
	"testing"
	"time"

	"daof-cpa/database"
	"daof-cpa/middleware"

	"github.com/gofiber/fiber/v2"
)

// fix MAJOR M22-A3（codex 第二十二轮）：内置真实 AdminGuard。
// AdminGuard 会自动注入 admin_user_id，所以下面手动注入可去（保留也无害，被覆盖）。
func newCouponAdminTestApp(admin *database.User) *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error {
		c.Request().Header.SetCookie("daof_admin_token", admin.Token)
		return c.Next()
	})
	app.Use(middleware.AdminGuard)
	app.Get("/admin/coupon-templates", AdminListCouponTemplates)
	app.Post("/admin/coupon-templates", AdminCreateCouponTemplate)
	app.Put("/admin/coupon-templates/:id", AdminUpdateCouponTemplate)
	app.Delete("/admin/coupon-templates/:id", AdminDeleteCouponTemplate)
	app.Post("/admin/coupons/grant", AdminGrantCoupon)
	app.Delete("/admin/coupons/:id", AdminRevokeCoupon)
	app.Get("/admin/users/:userId/coupons", AdminListUserCoupons)
	return app
}

func newCouponUserTestApp(user *database.User) *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("user", user)
		return c.Next()
	})
	app.Get("/coupons/my", MyCoupons)
	return app
}

func seedCouponTemplate(t *testing.T) *database.CouponTemplate {
	t.Helper()
	enabled := true
	tpl := database.CouponTemplate{
		Name:          "Test Coupon",
		DiscountType:  "fixed_price",
		DiscountValue: 5 * database.MicroPerUSD, // $5
		ValidDays:     30,
		Enabled:       &enabled,
	}
	if err := database.DB.Create(&tpl).Error; err != nil {
		t.Fatalf("seed template: %v", err)
	}
	return &tpl
}

// ─── Template CRUD ───────────────────────────────────────────────────

func TestCouponTemplate_CreateAndList(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	app := newCouponAdminTestApp(admin)

	code, resp := doJSON(t, app, "POST", "/admin/coupon-templates", map[string]any{
		"name":           "Holiday 50% Off",
		"discount_type":  "fixed_price",
		"discount_value": 4.99,
		"valid_days":     60,
	})
	if code != 200 {
		t.Fatalf("create template: expected 200 got %d body=%v", code, resp)
	}
	if resp["message_code"] != "SUCCESS_CREATED" {
		t.Errorf("expected SUCCESS_CREATED got %v", resp["message_code"])
	}

	code2, resp2 := doJSON(t, app, "GET", "/admin/coupon-templates", nil)
	if code2 != 200 {
		t.Fatalf("list templates: expected 200 got %d", code2)
	}
	data, _ := resp2["data"].([]any)
	if len(data) != 1 {
		t.Errorf("expected 1 template got %d", len(data))
	}
}

func TestCouponCreate_DiscountValueUSDWire(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	app := newCouponAdminTestApp(admin)

	code, resp := doJSON(t, app, "POST", "/admin/coupon-templates", map[string]any{
		"name":           "Micro USD Coupon",
		"discount_type":  "fixed_price",
		"discount_value": 3.25,
		"valid_days":     30,
	})
	if code != 200 {
		t.Fatalf("create template: expected 200 got %d body=%v", code, resp)
	}

	var tpl database.CouponTemplate
	if err := database.DB.Where("name = ?", "Micro USD Coupon").First(&tpl).Error; err != nil {
		t.Fatalf("query template: %v", err)
	}
	if tpl.DiscountValue != 3_250_000 {
		t.Fatalf("discount_value=%d want 3_250_000", tpl.DiscountValue)
	}
}

func TestCouponTemplate_Update(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	app := newCouponAdminTestApp(admin)
	tpl := seedCouponTemplate(t)

	code, resp := doJSON(t, app, "PUT", "/admin/coupon-templates/"+itoaUint(tpl.ID), map[string]any{
		"name":           "Updated Name",
		"discount_type":  "fixed_price",
		"discount_value": 3.0,
		"valid_days":     90,
	})
	if code != 200 {
		t.Errorf("expected 200 got %d body=%v", code, resp)
	}
}

func TestCouponTemplate_Delete(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	app := newCouponAdminTestApp(admin)
	tpl := seedCouponTemplate(t)

	code, resp := doJSON(t, app, "DELETE", "/admin/coupon-templates/"+itoaUint(tpl.ID), nil)
	if code != 200 {
		t.Errorf("expected 200 got %d body=%v", code, resp)
	}
	if resp["message_code"] != "SUCCESS_DELETED" {
		t.Errorf("expected SUCCESS_DELETED got %v", resp["message_code"])
	}
}

// ─── AdminGrantCoupon ────────────────────────────────────────────────

func TestGrantCoupon_HappyPath(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 0)
	app := newCouponAdminTestApp(admin)
	tpl := seedCouponTemplate(t)

	code, resp := doJSON(t, app, "POST", "/admin/coupons/grant", map[string]any{
		"user_id":     user.ID,
		"template_id": tpl.ID,
		"reason":      "生日快乐",
	})
	if code != 200 {
		t.Fatalf("grant: expected 200 got %d body=%v", code, resp)
	}
	if resp["message_code"] != "SUCCESS_GRANTED" {
		t.Errorf("expected SUCCESS_GRANTED got %v", resp["message_code"])
	}
	if resp["granted"] != float64(1) {
		t.Errorf("expected granted=1 got %v", resp["granted"])
	}
	ids, _ := resp["coupon_ids"].([]any)
	if len(ids) != 1 {
		t.Errorf("expected 1 coupon_id got %v", resp["coupon_ids"])
	}

	// verify DB
	var uc database.UserCoupon
	database.DB.First(&uc, uint(ids[0].(float64)))
	if uc.Status != "available" {
		t.Errorf("coupon status=%q want available", uc.Status)
	}
	if uc.SnapshotValue != 5*database.MicroPerUSD {
		t.Errorf("snapshot_value=%d want 5*MicroPerUSD", uc.SnapshotValue)
	}
	if uc.GrantReason != "生日快乐" {
		t.Errorf("grant_reason=%q want '生日快乐'", uc.GrantReason)
	}
}

func TestGrantCoupon_MultiQuantity(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 0)
	app := newCouponAdminTestApp(admin)
	tpl := seedCouponTemplate(t)

	qty := 5
	code, resp := doJSON(t, app, "POST", "/admin/coupons/grant", map[string]any{
		"user_id":     user.ID,
		"template_id": tpl.ID,
		"quantity":    qty,
		"reason":      "批量补偿",
	})
	if code != 200 {
		t.Fatalf("expected 200 got %d body=%v", code, resp)
	}
	if resp["granted"] != float64(qty) {
		t.Errorf("expected granted=%d got %v", qty, resp["granted"])
	}
	ids, _ := resp["coupon_ids"].([]any)
	if len(ids) != qty {
		t.Errorf("expected %d coupon_ids got %d", qty, len(ids))
	}
}

func TestGrantCoupon_InvalidQuantity(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 0)
	app := newCouponAdminTestApp(admin)
	tpl := seedCouponTemplate(t)

	cases := []struct {
		name string
		qty  int
	}{
		{"zero", 0},
		{"negative", -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, resp := doJSON(t, app, "POST", "/admin/coupons/grant", map[string]any{
				"user_id": user.ID, "template_id": tpl.ID, "quantity": tc.qty,
			})
			if code != 400 {
				t.Errorf("expected 400 got %d body=%v", code, resp)
			}
			if resp["message_code"] != "ERR_INVALID_QUANTITY" {
				t.Errorf("expected ERR_INVALID_QUANTITY got %v", resp["message_code"])
			}
		})
	}
}

func TestGrantCoupon_QuantityTooLarge(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 0)
	app := newCouponAdminTestApp(admin)
	tpl := seedCouponTemplate(t)

	code, resp := doJSON(t, app, "POST", "/admin/coupons/grant", map[string]any{
		"user_id": user.ID, "template_id": tpl.ID, "quantity": 200,
	})
	if code != 400 {
		t.Errorf("expected 400 got %d body=%v", code, resp)
	}
	if resp["message_code"] != "ERR_QUANTITY_TOO_LARGE" {
		t.Errorf("expected ERR_QUANTITY_TOO_LARGE got %v", resp["message_code"])
	}
}

func TestGrantCoupon_DisabledTemplate(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 0)
	app := newCouponAdminTestApp(admin)

	disabled := false
	tpl := database.CouponTemplate{
		Name: "Disabled", DiscountType: "fixed_price", DiscountValue: 5, Enabled: &disabled,
	}
	database.DB.Create(&tpl)

	code, resp := doJSON(t, app, "POST", "/admin/coupons/grant", map[string]any{
		"user_id": user.ID, "template_id": tpl.ID,
	})
	if code != 400 {
		t.Errorf("expected 400 (disabled) got %d body=%v", code, resp)
	}
	if resp["message_code"] != "ERR_TEMPLATE_DISABLED" {
		t.Errorf("expected ERR_TEMPLATE_DISABLED got %v", resp["message_code"])
	}
}

func TestGrantCoupon_UserNotFound(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	app := newCouponAdminTestApp(admin)
	tpl := seedCouponTemplate(t)

	code, resp := doJSON(t, app, "POST", "/admin/coupons/grant", map[string]any{
		"user_id": 99999, "template_id": tpl.ID,
	})
	if code != 404 {
		t.Errorf("expected 404 got %d body=%v", code, resp)
	}
}

func TestGrantCoupon_ReasonTooLong(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 0)
	app := newCouponAdminTestApp(admin)
	tpl := seedCouponTemplate(t)

	longReason := strings.Repeat("x", 501)
	code, resp := doJSON(t, app, "POST", "/admin/coupons/grant", map[string]any{
		"user_id": user.ID, "template_id": tpl.ID, "reason": longReason,
	})
	if code != 400 {
		t.Errorf("expected 400 got %d body=%v", code, resp)
	}
	if resp["message_code"] != "ERR_REASON_TOO_LONG" {
		t.Errorf("expected ERR_REASON_TOO_LONG got %v", resp["message_code"])
	}
}

func TestGrantCoupon_ReasonControlChars(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 0)
	app := newCouponAdminTestApp(admin)
	tpl := seedCouponTemplate(t)

	code, resp := doJSON(t, app, "POST", "/admin/coupons/grant", map[string]any{
		"user_id": user.ID, "template_id": tpl.ID, "reason": "bad\x00reason",
	})
	if code != 400 {
		t.Errorf("expected 400 got %d body=%v", code, resp)
	}
	if resp["message_code"] != "ERR_REASON_CTRL_CHAR" {
		t.Errorf("expected ERR_REASON_CTRL_CHAR got %v", resp["message_code"])
	}
}

// ─── AdminRevokeCoupon ───────────────────────────────────────────────

func TestRevokeCoupon_HappyPath(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 0)
	app := newCouponAdminTestApp(admin)
	tpl := seedCouponTemplate(t)

	// grant first
	uc := database.UserCoupon{
		UserID: user.ID, TemplateID: tpl.ID, Code: "CP-revoke-test",
		Status: "available", SnapshotType: "fixed_price", SnapshotValue: 5,
		GrantedAt: time.Now(),
	}
	database.DB.Create(&uc)

	code, resp := doJSON(t, app, "DELETE", "/admin/coupons/"+itoaUint(uc.ID), map[string]any{
		"reason": "误发",
	})
	if code != 200 {
		t.Fatalf("revoke: expected 200 got %d body=%v", code, resp)
	}
	if resp["message_code"] != "SUCCESS_REVOKED" {
		t.Errorf("expected SUCCESS_REVOKED got %v", resp["message_code"])
	}

	// verify DB status
	var fresh database.UserCoupon
	database.DB.First(&fresh, uc.ID)
	if fresh.Status != "revoked" {
		t.Errorf("status=%q want revoked", fresh.Status)
	}
}

func TestRevokeCoupon_AlreadyUsed(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 0)
	app := newCouponAdminTestApp(admin)

	uc := database.UserCoupon{
		UserID: user.ID, Code: "CP-used",
		Status: "used", SnapshotType: "fixed_price", SnapshotValue: 5,
		GrantedAt: time.Now(),
	}
	database.DB.Create(&uc)

	code, resp := doJSON(t, app, "DELETE", "/admin/coupons/"+itoaUint(uc.ID), nil)
	if code != 409 {
		t.Errorf("expected 409 (not revokable) got %d body=%v", code, resp)
	}
}

func TestRevokeCoupon_DoubleRevoke(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 0)
	app := newCouponAdminTestApp(admin)

	uc := database.UserCoupon{
		UserID: user.ID, Code: "CP-double",
		Status: "available", SnapshotType: "fixed_price", SnapshotValue: 5,
		GrantedAt: time.Now(),
	}
	database.DB.Create(&uc)

	// first revoke succeeds
	code1, _ := doJSON(t, app, "DELETE", "/admin/coupons/"+itoaUint(uc.ID), nil)
	if code1 != 200 {
		t.Fatalf("first revoke should succeed, got %d", code1)
	}

	// second revoke fails (already revoked)
	code2, resp2 := doJSON(t, app, "DELETE", "/admin/coupons/"+itoaUint(uc.ID), nil)
	if code2 != 409 {
		t.Errorf("expected 409 on double revoke got %d body=%v", code2, resp2)
	}
}

// ─── AdminListUserCoupons ────────────────────────────────────────────

func TestAdminListUserCoupons_Pagination(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 0)
	app := newCouponAdminTestApp(admin)

	for i := 0; i < 5; i++ {
		database.DB.Create(&database.UserCoupon{
			UserID: user.ID, Code: "CP-list-" + itoaUint(uint(i)),
			Status: "available", SnapshotType: "fixed_price", SnapshotValue: 5,
			GrantedAt: time.Now(),
		})
	}

	code, resp := doJSON(t, app, "GET", "/admin/users/"+itoaUint(user.ID)+"/coupons?page=1&page_size=3", nil)
	if code != 200 {
		t.Fatalf("expected 200 got %d", code)
	}
	data, _ := resp["data"].([]any)
	if len(data) != 3 {
		t.Errorf("expected 3 items got %d", len(data))
	}
	meta, _ := resp["meta"].(map[string]any)
	if meta["total"] != float64(5) {
		t.Errorf("expected total=5 got %v", meta["total"])
	}
}

// ─── MyCoupons (user endpoint) ───────────────────────────────────────

func TestMyCoupons_HappyPath(t *testing.T) {
	setupSubTestDB(t)
	user := seedTestUser(t, 0)
	app := newCouponUserTestApp(user)

	now := time.Now()
	expired := now.Add(-1 * time.Hour)
	future := now.Add(24 * time.Hour)

	database.DB.Create(&database.UserCoupon{
		UserID: user.ID, Code: "CP-avail", Status: "available",
		SnapshotType: "fixed_price", SnapshotValue: 5, ExpiresAt: &future,
		GrantedAt: now,
	})
	database.DB.Create(&database.UserCoupon{
		UserID: user.ID, Code: "CP-expired-status", Status: "available",
		SnapshotType: "fixed_price", SnapshotValue: 5, ExpiresAt: &expired,
		GrantedAt: now,
	})
	database.DB.Create(&database.UserCoupon{
		UserID: user.ID, Code: "CP-used", Status: "used",
		SnapshotType: "fixed_price", SnapshotValue: 5, UsedAt: &now,
		GrantedAt: now,
	})

	code, resp := doJSON(t, app, "GET", "/coupons/my", nil)
	if code != 200 {
		t.Fatalf("expected 200 got %d", code)
	}
	data, _ := resp["data"].([]any)
	if len(data) != 3 {
		t.Errorf("expected 3 coupons got %d", len(data))
	}
	// verify effective_status for expired coupon
	for _, item := range data {
		m, _ := item.(map[string]any)
		if m["code"] == "CP-expired-status" {
			if m["effective_status"] != "expired" {
				t.Errorf("expected effective_status=expired for expired coupon, got %v", m["effective_status"])
			}
		}
	}
	meta, _ := resp["meta"].(map[string]any)
	if meta["total"] != float64(3) {
		t.Errorf("expected total=3 got %v", meta["total"])
	}
}

func TestMyCoupons_Pagination(t *testing.T) {
	setupSubTestDB(t)
	user := seedTestUser(t, 0)
	app := newCouponUserTestApp(user)

	for i := 0; i < 10; i++ {
		database.DB.Create(&database.UserCoupon{
			UserID: user.ID, Code: "CP-p-" + itoaUint(uint(i)),
			Status: "available", SnapshotType: "fixed_price", SnapshotValue: 5,
			GrantedAt: time.Now(),
		})
	}

	code, resp := doJSON(t, app, "GET", "/coupons/my?page=2&page_size=3", nil)
	if code != 200 {
		t.Fatalf("expected 200 got %d", code)
	}
	data, _ := resp["data"].([]any)
	if len(data) != 3 {
		t.Errorf("page 2 size 3: expected 3 items got %d", len(data))
	}
}

// ─── Grant + Revoke flow integration ────────────────────────���────────

func TestGrantThenRevoke_Flow(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 0)
	app := newCouponAdminTestApp(admin)
	tpl := seedCouponTemplate(t)

	// grant
	code1, resp1 := doJSON(t, app, "POST", "/admin/coupons/grant", map[string]any{
		"user_id": user.ID, "template_id": tpl.ID, "reason": "test flow",
	})
	if code1 != 200 {
		t.Fatalf("grant failed: %d %v", code1, resp1)
	}
	ids, _ := resp1["coupon_ids"].([]any)
	couponID := uint(ids[0].(float64))

	// verify available
	var uc database.UserCoupon
	database.DB.First(&uc, couponID)
	if uc.Status != "available" {
		t.Fatalf("expected available got %q", uc.Status)
	}

	// revoke
	code2, resp2 := doJSON(t, app, "DELETE", "/admin/coupons/"+itoaUint(couponID), map[string]any{
		"reason": "取消发放",
	})
	if code2 != 200 {
		t.Fatalf("revoke failed: %d %v", code2, resp2)
	}

	// verify revoked
	database.DB.First(&uc, couponID)
	if uc.Status != "revoked" {
		t.Errorf("expected revoked got %q", uc.Status)
	}

	// verify audit log written
	var logCount int64
	database.DB.Model(&database.OperationLog{}).
		Where("action_type = ? AND target_user_id = ?", "GRANT_COUPON", user.ID).
		Count(&logCount)
	if logCount != 1 {
		t.Errorf("expected 1 GRANT_COUPON audit log got %d", logCount)
	}
	database.DB.Model(&database.OperationLog{}).
		Where("action_type = ? AND target_user_id = ?", "REVOKE_COUPON", user.ID).
		Count(&logCount)
	if logCount != 1 {
		t.Errorf("expected 1 REVOKE_COUPON audit log got %d", logCount)
	}
}
