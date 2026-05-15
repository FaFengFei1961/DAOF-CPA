package controller

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"testing"

	"daof-ai-hub/database"

	"github.com/gofiber/fiber/v2"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupBulkQuotaPreviewDB(t *testing.T) {
	t.Helper()
	prev := database.DB
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=private"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sql db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&database.User{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	database.DB = db
	t.Cleanup(func() {
		database.DB = prev
		_ = sqlDB.Close()
	})
}

func newBulkQuotaPreviewApp() *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Post("/api/admin/users/bulk-quota/preview", BulkAdjustQuotaPreview)
	return app
}

func postBulkQuotaPreview(t *testing.T, app *fiber.App, body any) (int, map[string]any) {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest("POST", "/api/admin/users/bulk-quota/preview", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	var decoded map[string]any
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		t.Fatalf("decode response %q: %v", string(respBody), err)
	}
	return resp.StatusCode, decoded
}

func TestBulkAdjustQuotaPreview_HappyPath(t *testing.T) {
	setupBulkQuotaPreviewDB(t)
	app := newBulkQuotaPreviewApp()

	users := []database.User{
		{ID: 1, Username: "alice", Role: "user", Token: "sk-alice", Quota: 10 * database.MicroPerUSD},
		{ID: 2, Username: "bob", Role: "user", Token: "sk-bob", Quota: 1_500_000},
		{ID: 3, Username: "root", Role: "admin", Token: "sk-root", Quota: 999 * database.MicroPerUSD},
	}
	if err := database.DB.Create(&users).Error; err != nil {
		t.Fatalf("seed users: %v", err)
	}

	code, resp := postBulkQuotaPreview(t, app, map[string]any{
		"user_ids":         []int64{1, 2, 2, 3},
		"action":           "subtract",
		"amount_micro_usd": int64(3_250_000),
	})
	if code != 200 {
		t.Fatalf("expected 200, got %d: %v", code, resp)
	}
	data := resp["data"].(map[string]any)
	if got := data["affected_count"].(float64); got != 2 {
		t.Fatalf("affected_count=%v, want 2", got)
	}
	if got := data["total_delta_usd"].(float64); got != -4.75 {
		t.Fatalf("total_delta_usd=%v, want -4.75", got)
	}
	previewUsers := data["users"].([]any)
	if len(previewUsers) != 2 {
		t.Fatalf("users len=%d, want 2", len(previewUsers))
	}
	first := previewUsers[0].(map[string]any)
	if first["username"] != "alice" || first["current_usd"].(float64) != 10 || first["future_usd"].(float64) != 6.75 {
		t.Fatalf("unexpected first preview user: %#v", first)
	}
	second := previewUsers[1].(map[string]any)
	if second["username"] != "bob" || second["current_usd"].(float64) != 1.5 || second["future_usd"].(float64) != 0 {
		t.Fatalf("unexpected second preview user: %#v", second)
	}

	var fresh database.User
	if err := database.DB.First(&fresh, 1).Error; err != nil {
		t.Fatalf("reload user: %v", err)
	}
	if fresh.Quota != 10*database.MicroPerUSD {
		t.Fatalf("preview wrote quota=%d, want unchanged", fresh.Quota)
	}
}

func TestBulkAdjustQuotaPreview_OverLimit(t *testing.T) {
	setupBulkQuotaPreviewDB(t)
	app := newBulkQuotaPreviewApp()
	userIDs := make([]int64, bulkQuotaPreviewUserLimit+1)
	for i := range userIDs {
		userIDs[i] = int64(i + 1)
	}

	code, resp := postBulkQuotaPreview(t, app, map[string]any{
		"user_ids":         userIDs,
		"action":           "add",
		"amount_micro_usd": int64(database.MicroPerUSD),
	})
	if code != 400 {
		t.Fatalf("expected 400, got %d: %v", code, resp)
	}
	if resp["message_code"] != MessageCodeBulkPreviewLimit {
		t.Fatalf("message_code=%v, want %s", resp["message_code"], MessageCodeBulkPreviewLimit)
	}
}
