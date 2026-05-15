package controller

import (
	"testing"

	"daof-ai-hub/database"

	"github.com/gofiber/fiber/v2"
)

func TestDeleteQuotaPlan_TransactionCountError(t *testing.T) {
	setupSubTestDB(t)
	plan := database.QuotaPlan{
		Name:          "delete_count_error",
		DisplayName:   "Delete Count Error",
		ModelMatch:    `["*"]`,
		LimitUnit:     "request_count",
		LimitValue:    100,
		WindowSeconds: 0,
		Enabled:       boolPtr(true),
	}
	if err := database.DB.Create(&plan).Error; err != nil {
		t.Fatalf("create plan: %v", err)
	}
	if err := database.DB.Exec("DROP TABLE package_plans").Error; err != nil {
		t.Fatalf("drop package_plans: %v", err)
	}

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Delete("/admin/quota-plans/:id", DeleteQuotaPlan)
	code, resp := doJSON(t, app, "DELETE", "/admin/quota-plans/"+itoaUint(plan.ID), nil)
	if code != 500 || resp["message_code"] != "ERR_DB_DELETE" {
		t.Fatalf("delete with count error got %d/%v, want 500/ERR_DB_DELETE", code, resp["message_code"])
	}

	var count int64
	if err := database.DB.Model(&database.QuotaPlan{}).Where("id = ?", plan.ID).Count(&count).Error; err != nil {
		t.Fatalf("count plan: %v", err)
	}
	if count != 1 {
		t.Fatalf("plan count=%d, want 1", count)
	}
}
