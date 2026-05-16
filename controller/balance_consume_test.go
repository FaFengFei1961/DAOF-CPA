package controller

import (
	"testing"

	"daof-ai-hub/database"

	"github.com/gofiber/fiber/v2"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupBalanceConsumeControllerTestDB(t *testing.T) *database.User {
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
	user := database.User{
		Username:                    "balance-consume-owner",
		Role:                        "user",
		Token:                       "sk-balance-consume-owner",
		Quota:                       100 * database.MicroPerUSD,
		Status:                      1,
		BalanceConsumeWindowSeconds: 2592000,
	}
	if err := database.DB.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() {
		database.DB = prev
		_ = sqlDB.Close()
	})
	return &user
}

func newBalanceConsumeTestApp(user *database.User) *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("user", user)
		return c.Next()
	})
	app.Put("/balance-consume/preference", UpdateMyBalanceConsumePreference)
	return app
}

func TestBalanceConsume_UsesLimitUSDWire(t *testing.T) {
	user := setupBalanceConsumeControllerTestDB(t)
	app := newBalanceConsumeTestApp(user)

	code, resp := doJSON(t, app, "PUT", "/balance-consume/preference", map[string]any{
		"enabled":   true,
		"limit_usd": 4.200123,
	})
	if code != 200 {
		t.Fatalf("expected 200 got %d body=%v", code, resp)
	}

	var fresh database.User
	if err := database.DB.First(&fresh, user.ID).Error; err != nil {
		t.Fatalf("reload user: %v", err)
	}
	if fresh.BalanceConsumeLimitUSD != 4_200_123 {
		t.Fatalf("balance_consume_limit_usd=%d want 4200123", fresh.BalanceConsumeLimitUSD)
	}
	if !fresh.BalanceConsumeEnabled {
		t.Fatalf("balance consume should be enabled")
	}
}
