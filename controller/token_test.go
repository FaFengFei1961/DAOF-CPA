package controller

import (
	"testing"

	"daof-ai-hub/database"

	"github.com/gofiber/fiber/v2"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupTokenControllerTestDB(t *testing.T) *database.User {
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
	if err := db.AutoMigrate(
		&database.User{},
		&database.AccessToken{},
		&database.Channel{},
		&database.ChannelModel{},
		&database.SysConfig{},
		&database.OperationLog{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	database.DB = db
	user := database.User{
		Username: "token-owner",
		Role:     "user",
		Token:    "sk-token-owner",
		Status:   1,
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

func newTokenTestApp(user *database.User) *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("user", user)
		return c.Next()
	})
	app.Post("/tokens", CreateToken)
	app.Put("/tokens/:id", UpdateTokenSettings)
	return app
}

func TestCreateToken_QuotaLimitUSDWire(t *testing.T) {
	user := setupTokenControllerTestDB(t)
	app := newTokenTestApp(user)

	code, resp := doJSON(t, app, "POST", "/tokens", map[string]any{
		"name":            "limited",
		"quota_limit_usd": 2.500001,
	})
	if code != 200 {
		t.Fatalf("expected 200 got %d body=%v", code, resp)
	}

	var token database.AccessToken
	if err := database.DB.Where("user_id = ? AND name = ?", user.ID, "limited").First(&token).Error; err != nil {
		t.Fatalf("load token: %v", err)
	}
	if token.QuotaLimit != 2_500_001 {
		t.Fatalf("quota_limit=%d want 2500001", token.QuotaLimit)
	}
}

func TestUpdateTokenSettings_QuotaLimitUSDWire(t *testing.T) {
	user := setupTokenControllerTestDB(t)
	app := newTokenTestApp(user)
	token := database.AccessToken{
		UserID:     user.ID,
		Name:       "existing",
		Key:        "sk-existing-child",
		QuotaLimit: 1,
		Status:     1,
	}
	if err := database.DB.Create(&token).Error; err != nil {
		t.Fatalf("seed token: %v", err)
	}

	code, resp := doJSON(t, app, "PUT", "/tokens/"+itoaUint(token.ID), map[string]any{
		"quota_limit_usd": 7.654321,
	})
	if code != 200 {
		t.Fatalf("expected 200 got %d body=%v", code, resp)
	}

	var fresh database.AccessToken
	if err := database.DB.First(&fresh, token.ID).Error; err != nil {
		t.Fatalf("reload token: %v", err)
	}
	if fresh.QuotaLimit != 7_654_321 {
		t.Fatalf("quota_limit=%d want 7654321", fresh.QuotaLimit)
	}
}
