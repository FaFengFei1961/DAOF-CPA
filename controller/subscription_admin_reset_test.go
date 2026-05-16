package controller

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"daof-ai-hub/database"
	"daof-ai-hub/middleware"

	"github.com/gofiber/fiber/v2"
)

func newAdminResetUsageTestApp(admin *database.User) *fiber.App {
	app := newRealAdminApp(admin)
	app.Post("/admin/sub/reset-usage", AdminResetSubscriptionUsage)
	return app
}

func seedResetUsageSubscription(t *testing.T, userID, packageID uint, status string, endAt time.Time) database.UserSubscription {
	t.Helper()
	sub := database.UserSubscription{
		UserID:          userID,
		PackageID:       packageID,
		Status:          status,
		PackageSnapshot: `{"package_name":"ResetTest"}`,
		StartAt:         endAt.Add(-24 * time.Hour),
		EndAt:           endAt,
	}
	if err := database.DB.Create(&sub).Error; err != nil {
		t.Fatalf("seed reset subscription: %v", err)
	}
	return sub
}

func seedResetUsageRow(t *testing.T, subID, planID uint, bucket string, startAt, endAt time.Time, consumed float64, consumedMicro int64) database.SubscriptionUsage {
	t.Helper()
	row := database.SubscriptionUsage{
		SubscriptionID:        subID,
		QuotaPlanID:           planID,
		ModelBucket:           bucket,
		WindowStartAt:         startAt,
		WindowEndAt:           endAt,
		ConsumedValue:         consumed,
		ConsumedValueMicroUSD: consumedMicro,
		RequestCount:          9,
	}
	if err := database.DB.Create(&row).Error; err != nil {
		t.Fatalf("seed reset usage row: %v", err)
	}
	return row
}

func resetUsageRequest(note string) map[string]any {
	return map[string]any{
		"confirm": "YES_RESET_USAGE",
		"note":    note,
	}
}

func TestAdminResetUsage_HappyPath(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 10)
	app := newAdminResetUsageTestApp(admin)

	now := time.Now().Truncate(time.Second)
	type window struct {
		start time.Time
		end   time.Time
	}
	windows := map[uint]window{}
	for i := 0; i < 5; i++ {
		sub := seedResetUsageSubscription(t, user.ID, 100, "active", now.Add(24*time.Hour))
		for j := 0; j < 2; j++ {
			start := now.Add(time.Duration(i+j) * time.Hour)
			end := start.Add(5 * time.Hour)
			row := seedResetUsageRow(t, sub.ID, uint(i*10+j+1), fmt.Sprintf("bucket-%d-%d", i, j), start, end, 12.5, 3_000_000)
			windows[row.ID] = window{start: start, end: end}
		}
	}

	code, resp := doJSON(t, app, "POST", "/admin/sub/reset-usage", resetUsageRequest("客服批量重置"))
	if code != 200 {
		t.Fatalf("expected 200, got %d body=%v", code, resp)
	}
	if resp["reset_count"] != float64(5) {
		t.Fatalf("reset_count=%v, want 5", resp["reset_count"])
	}

	var rows []database.SubscriptionUsage
	if err := database.DB.Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatalf("load usage rows: %v", err)
	}
	if len(rows) != 10 {
		t.Fatalf("usage rows=%d, want 10", len(rows))
	}
	for _, row := range rows {
		if row.ConsumedValue != 0 {
			t.Fatalf("usage row %d consumed_value=%v, want 0", row.ID, row.ConsumedValue)
		}
		if row.ConsumedValueMicroUSD != 0 {
			t.Fatalf("usage row %d consumed_micro=%d, want 0", row.ID, row.ConsumedValueMicroUSD)
		}
		if row.RequestCount != 0 {
			t.Fatalf("usage row %d request_count=%d, want 0", row.ID, row.RequestCount)
		}
		want := windows[row.ID]
		if !row.WindowStartAt.Equal(want.start) || !row.WindowEndAt.Equal(want.end) {
			t.Fatalf("usage row %d window changed got %s..%s want %s..%s",
				row.ID, row.WindowStartAt, row.WindowEndAt, want.start, want.end)
		}
	}
}

func TestAdminResetUsage_RejectsWithoutConfirm(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	app := newAdminResetUsageTestApp(admin)

	code, resp := doJSON(t, app, "POST", "/admin/sub/reset-usage", map[string]any{
		"note": "missing confirm",
	})
	if code != 400 || resp["message_code"] != "ERR_RESET_CONFIRM_REQUIRED" {
		t.Fatalf("got code=%d resp=%v, want ERR_RESET_CONFIRM_REQUIRED", code, resp)
	}
}

func TestAdminResetUsage_RejectsWithoutNote(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	app := newAdminResetUsageTestApp(admin)

	code, resp := doJSON(t, app, "POST", "/admin/sub/reset-usage", map[string]any{
		"confirm": "YES_RESET_USAGE",
		"note":    "   ",
	})
	if code != 400 || resp["message_code"] != "ERR_RESET_NOTE_REQUIRED" {
		t.Fatalf("got code=%d resp=%v, want ERR_RESET_NOTE_REQUIRED", code, resp)
	}
}

func TestAdminResetUsage_RejectsNoteTooLong(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	app := newAdminResetUsageTestApp(admin)

	code, resp := doJSON(t, app, "POST", "/admin/sub/reset-usage", map[string]any{
		"confirm": "YES_RESET_USAGE",
		"note":    strings.Repeat("x", 501),
	})
	if code != 400 || resp["message_code"] != "ERR_RESET_NOTE_TOO_LONG" {
		t.Fatalf("got code=%d resp=%v, want ERR_RESET_NOTE_TOO_LONG", code, resp)
	}
}

func TestAdminResetUsage_FiltersByPackage(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 10)
	app := newAdminResetUsageTestApp(admin)
	now := time.Now().Truncate(time.Second)

	subA := seedResetUsageSubscription(t, user.ID, 11, "active", now.Add(24*time.Hour))
	subB := seedResetUsageSubscription(t, user.ID, 22, "active", now.Add(24*time.Hour))
	rowA := seedResetUsageRow(t, subA.ID, 1, "a", now, now.Add(time.Hour), 7, 0)
	rowB := seedResetUsageRow(t, subB.ID, 2, "b", now, now.Add(time.Hour), 9, 0)

	body := resetUsageRequest("按套餐重置")
	body["package_ids"] = []uint{11}
	code, resp := doJSON(t, app, "POST", "/admin/sub/reset-usage", body)
	if code != 200 || resp["reset_count"] != float64(1) {
		t.Fatalf("got code=%d resp=%v, want reset_count 1", code, resp)
	}
	var freshA, freshB database.SubscriptionUsage
	database.DB.First(&freshA, rowA.ID)
	database.DB.First(&freshB, rowB.ID)
	if freshA.ConsumedValue != 0 {
		t.Fatalf("package-matched row consumed=%v, want 0", freshA.ConsumedValue)
	}
	if freshB.ConsumedValue != 9 {
		t.Fatalf("non-matched row consumed=%v, want 9", freshB.ConsumedValue)
	}
}

func TestAdminResetUsage_FiltersByUser(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	userA := seedTestUser(t, 10)
	userB := database.User{Username: "other", Role: "user", Token: "sk-reset-other", Status: 1}
	if err := database.DB.Create(&userB).Error; err != nil {
		t.Fatalf("seed other user: %v", err)
	}
	app := newAdminResetUsageTestApp(admin)
	now := time.Now().Truncate(time.Second)

	subA := seedResetUsageSubscription(t, userA.ID, 33, "active", now.Add(24*time.Hour))
	subB := seedResetUsageSubscription(t, userB.ID, 33, "active", now.Add(24*time.Hour))
	rowA := seedResetUsageRow(t, subA.ID, 1, "a", now, now.Add(time.Hour), 4, 0)
	rowB := seedResetUsageRow(t, subB.ID, 2, "b", now, now.Add(time.Hour), 8, 0)

	body := resetUsageRequest("按用户重置")
	body["user_ids"] = []uint{userA.ID}
	code, resp := doJSON(t, app, "POST", "/admin/sub/reset-usage", body)
	if code != 200 || resp["reset_count"] != float64(1) {
		t.Fatalf("got code=%d resp=%v, want reset_count 1", code, resp)
	}
	var freshA, freshB database.SubscriptionUsage
	database.DB.First(&freshA, rowA.ID)
	database.DB.First(&freshB, rowB.ID)
	if freshA.ConsumedValue != 0 {
		t.Fatalf("user-matched row consumed=%v, want 0", freshA.ConsumedValue)
	}
	if freshB.ConsumedValue != 8 {
		t.Fatalf("non-matched row consumed=%v, want 8", freshB.ConsumedValue)
	}
}

func TestAdminResetUsage_FiltersByStatus(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 10)
	app := newAdminResetUsageTestApp(admin)
	now := time.Now().Truncate(time.Second)

	activeSub := seedResetUsageSubscription(t, user.ID, 44, "active", now.Add(24*time.Hour))
	canceledSub := seedResetUsageSubscription(t, user.ID, 44, "canceled", now.Add(24*time.Hour))
	expiredSub := seedResetUsageSubscription(t, user.ID, 44, "expired", now.Add(-time.Hour))
	activeRow := seedResetUsageRow(t, activeSub.ID, 1, "active", now, now.Add(time.Hour), 1, 0)
	canceledRow := seedResetUsageRow(t, canceledSub.ID, 2, "canceled", now, now.Add(time.Hour), 2, 0)
	expiredRow := seedResetUsageRow(t, expiredSub.ID, 3, "expired", now, now.Add(time.Hour), 3, 0)

	body := resetUsageRequest("按状态重置")
	body["statuses"] = []string{"canceled", "expired"}
	code, resp := doJSON(t, app, "POST", "/admin/sub/reset-usage", body)
	if code != 200 || resp["reset_count"] != float64(2) {
		t.Fatalf("got code=%d resp=%v, want reset_count 2", code, resp)
	}
	var activeUsage, canceledUsage, expiredUsage database.SubscriptionUsage
	database.DB.First(&activeUsage, activeRow.ID)
	database.DB.First(&canceledUsage, canceledRow.ID)
	database.DB.First(&expiredUsage, expiredRow.ID)
	if activeUsage.ConsumedValue != 1 {
		t.Fatalf("active row consumed=%v, want unchanged 1", activeUsage.ConsumedValue)
	}
	if canceledUsage.ConsumedValue != 0 || expiredUsage.ConsumedValue != 0 {
		t.Fatalf("status-matched rows not reset: canceled=%v expired=%v", canceledUsage.ConsumedValue, expiredUsage.ConsumedValue)
	}
}

func TestAdminResetUsage_WritesBillingEntry(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 10)
	app := newAdminResetUsageTestApp(admin)
	now := time.Now().Truncate(time.Second)

	for i := 0; i < 3; i++ {
		sub := seedResetUsageSubscription(t, user.ID, 55, "active", now.Add(24*time.Hour))
		seedResetUsageRow(t, sub.ID, uint(i+1), fmt.Sprintf("b-%d", i), now, now.Add(time.Hour), float64(i+1), 0)
	}

	code, resp := doJSON(t, app, "POST", "/admin/sub/reset-usage", resetUsageRequest("财务审计重置"))
	if code != 200 {
		t.Fatalf("expected 200, got %d body=%v", code, resp)
	}

	var entries []database.BillingEntry
	if err := database.DB.Where("entry_type = ? AND related_type = ?",
		database.BillingTypeAdminAdjust, "subscription_usage_reset").Order("related_id ASC").Find(&entries).Error; err != nil {
		t.Fatalf("load billing entries: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("billing entries=%d, want 3", len(entries))
	}
	for _, entry := range entries {
		if entry.AmountUSD != 0 {
			t.Fatalf("entry %d amount=%d, want 0", entry.ID, entry.AmountUSD)
		}
		if !strings.Contains(entry.Description, "财务审计重置") || !strings.Contains(entry.Description, fmt.Sprintf("admin#%d", admin.ID)) {
			t.Fatalf("entry description missing note/admin: %q", entry.Description)
		}
		if entry.RelatedID == 0 {
			t.Fatalf("entry %d related_id=0, want subscription id", entry.ID)
		}
	}
}

func TestAdminResetUsage_WritesOperationLog(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 10)
	app := newAdminResetUsageTestApp(admin)
	now := time.Now().Truncate(time.Second)

	sub := seedResetUsageSubscription(t, user.ID, 66, "active", now.Add(24*time.Hour))
	seedResetUsageRow(t, sub.ID, 1, "op", now, now.Add(time.Hour), 5, 0)

	body := resetUsageRequest("审计日志备注")
	body["package_ids"] = []uint{66}
	body["user_ids"] = []uint{user.ID}
	body["statuses"] = []string{"active"}
	code, resp := doJSON(t, app, "POST", "/admin/sub/reset-usage", body)
	if code != 200 {
		t.Fatalf("expected 200, got %d body=%v", code, resp)
	}

	var opLog database.OperationLog
	if err := database.DB.Where("action_type = ?", "SUBSCRIPTION_USAGE_RESET").First(&opLog).Error; err != nil {
		t.Fatalf("load operation log: %v", err)
	}
	if opLog.OperatorID != admin.ID || opLog.TargetUserID != 0 {
		t.Fatalf("operation log operator/target=%d/%d, want %d/0", opLog.OperatorID, opLog.TargetUserID, admin.ID)
	}
	var details map[string]any
	if err := json.Unmarshal([]byte(opLog.Details), &details); err != nil {
		t.Fatalf("details is not json: %v", err)
	}
	if details["matched_count"] != float64(1) || details["note"] != "审计日志备注" {
		t.Fatalf("unexpected details: %v", details)
	}
}

func TestAdminResetUsage_ScopeTooLarge(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 10)
	app := newAdminResetUsageTestApp(admin)
	now := time.Now().Truncate(time.Second)

	subs := make([]database.UserSubscription, 0, resetUsageScopeMax+1)
	for i := 0; i < resetUsageScopeMax+1; i++ {
		subs = append(subs, database.UserSubscription{
			UserID:          user.ID,
			PackageID:       77,
			Status:          "active",
			PackageSnapshot: `{"package_name":"HugeScope"}`,
			StartAt:         now.Add(-time.Hour),
			EndAt:           now.Add(24 * time.Hour),
		})
	}
	if err := database.DB.CreateInBatches(subs, 1000).Error; err != nil {
		t.Fatalf("seed large scope: %v", err)
	}

	code, resp := doJSON(t, app, "POST", "/admin/sub/reset-usage", resetUsageRequest("范围过大"))
	if code != 400 || resp["message_code"] != "ERR_RESET_SCOPE_TOO_LARGE" {
		t.Fatalf("got code=%d resp=%v, want ERR_RESET_SCOPE_TOO_LARGE", code, resp)
	}
}

func TestAdminResetUsage_NonAdminRejected(t *testing.T) {
	setupSubTestDB(t)
	user := seedTestUser(t, 10)
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error {
		c.Request().Header.SetCookie("daof_admin_token", user.Token)
		return c.Next()
	})
	app.Use(middleware.AdminGuard)
	app.Post("/admin/sub/reset-usage", AdminResetSubscriptionUsage)

	code, resp := doJSON(t, app, "POST", "/admin/sub/reset-usage", resetUsageRequest("普通用户越权"))
	if code != 403 {
		t.Fatalf("expected 403, got %d body=%v", code, resp)
	}
}
