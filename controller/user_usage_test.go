package controller

import (
	"net/http/httptest"
	"testing"
	"time"

	"daof-ai-hub/database"

	"github.com/gofiber/fiber/v2"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestGetUsersUsageScansAggregatedLastActiveAt(t *testing.T) {
	var err error
	database.DB, err = gorm.Open(sqlite.Open("file:user_usage_scan?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.DB.AutoMigrate(&database.User{}, &database.ApiLog{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	database.DB.Create(&database.User{ID: 1, Username: "admin", Role: "admin", Status: 1})
	database.DB.Create(&database.ApiLog{
		UserID:           1,
		TokenName:        "tok",
		ModelName:        "claude-opus-4-7",
		PromptTokens:     135608,
		CompletionTokens: 81,
		Cost:             2055,
		Status:           200,
		CreatedAt:        time.Date(2026, 5, 11, 7, 15, 18, 673908800, time.FixedZone("PDT", -7*3600)),
	})

	app := fiber.New()
	app.Get("/api/admin/users-usage", GetUsersUsage)

	resp, err := app.Test(httptest.NewRequest("GET", "/api/admin/users-usage?period=7d&sort=cost_desc&include_models=true", nil), -1)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}
