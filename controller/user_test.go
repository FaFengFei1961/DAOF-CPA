package controller

import (
	"testing"

	"daof-ai-hub/database"

	"github.com/gofiber/fiber/v2"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupUserControllerTestDB(t *testing.T) {
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
		&database.UserSession{},
		&database.AccessToken{},
		&database.Channel{},
		&database.ChannelModel{},
		&database.SysConfig{},
		&database.OperationLog{},
		&database.BillingEntry{},
		&database.Notification{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	database.DB = db
	t.Cleanup(func() {
		database.DB = prev
		_ = sqlDB.Close()
	})
}

func newUpdateUserTestApp() *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Put("/admin/users/:id", UpdateUser)
	return app
}

func seedUpdateUserTarget(t *testing.T, quotaMicro int64, status int) database.User {
	t.Helper()
	u := database.User{
		Username: "user-update-target",
		Role:     "user",
		Token:    "sk-user-update-target",
		Quota:    quotaMicro,
		Status:   status,
	}
	if err := database.DB.Create(&u).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return u
}

func TestUpdateUser_UsesQuotaMicroUSD(t *testing.T) {
	setupUserControllerTestDB(t)
	app := newUpdateUserTestApp()
	user := seedUpdateUserTarget(t, 5*database.MicroPerUSD, 1)

	code, resp := doJSON(t, app, "PUT", "/admin/users/"+itoaUint(user.ID), map[string]any{
		"username":        user.Username,
		"quota_micro_usd": int64(12_345_678),
		"status":          1,
		"ban_reason":      "",
	})
	if code != 200 {
		t.Fatalf("expected 200 got %d body=%v", code, resp)
	}

	var fresh database.User
	if err := database.DB.First(&fresh, user.ID).Error; err != nil {
		t.Fatalf("reload user: %v", err)
	}
	if fresh.Quota != 12_345_678 {
		t.Fatalf("quota=%d want 12345678", fresh.Quota)
	}
}

func TestUpdateUser_BanRevokesSessions(t *testing.T) {
	setupUserControllerTestDB(t)
	app := newUpdateUserTestApp()
	user := seedUpdateUserTarget(t, 10*database.MicroPerUSD, 1)
	sessionID, err := database.CreateUserSession(user.ID, "test-agent", "127.0.0.1")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if got, ok := database.LookupUserBySession(sessionID); !ok || got.ID != user.ID {
		t.Fatalf("session should resolve before ban, got user=%v ok=%v", got, ok)
	}

	code, resp := doJSON(t, app, "PUT", "/admin/users/"+itoaUint(user.ID), map[string]any{
		"username":        user.Username,
		"quota_micro_usd": user.Quota,
		"status":          2,
		"ban_reason":      "policy",
	})
	if code != 200 {
		t.Fatalf("expected 200 got %d body=%v", code, resp)
	}

	var session database.UserSession
	if err := database.DB.Where("session_id = ?", sessionID).First(&session).Error; err != nil {
		t.Fatalf("reload session: %v", err)
	}
	if session.RevokedAt == nil {
		t.Fatalf("session revoked_at is nil after ban")
	}
	if got, ok := database.LookupUserBySession(sessionID); ok || got != nil {
		t.Fatalf("revoked session should not resolve, got user=%v ok=%v", got, ok)
	}
}
