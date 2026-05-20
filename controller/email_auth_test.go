// Package controller / email_auth_test.go
//
// Phase G-2.1 单元测试：密码强度校验 + EmailLoginEnabled toggle endpoint。
package controller

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"daof-cpa/database"
	"daof-cpa/proxy"
	"daof-cpa/utils"

	"github.com/gofiber/fiber/v2"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// ── 纯函数测试 ──

func TestValidatePasswordStrength(t *testing.T) {
	tests := []struct {
		name     string
		password string
		username string
		wantCode string
		wantOK   bool
	}{
		{"empty rejected", "", "", "ERR_PASSWORD_EMPTY", false},
		{"too short 7 chars", "1234567", "", "ERR_PASSWORD_TOO_SHORT", false},
		{"min length 8 ok", "12345678", "alice", "", true},
		{"chinese 8 runes ok", "密码很长很安全好", "alice", "", true},
		{"chinese byte over 72 rejected", strings.Repeat("中", 30), "alice", "ERR_PASSWORD_TOO_LONG", false},
		{"73 ASCII rejected", strings.Repeat("a", 73), "alice", "ERR_PASSWORD_TOO_LONG", false},
		{"all whitespace rejected", "        ", "alice", "ERR_PASSWORD_WHITESPACE", false},
		{"all same char rejected", "aaaaaaaa", "alice", "ERR_PASSWORD_WEAK", false},
		{"all same digit rejected", "11111111", "alice", "ERR_PASSWORD_WEAK", false},
		{"same as username rejected", "alice", "alice", "ERR_PASSWORD_TOO_SHORT", false}, // 5 chars too short first
		{"case-sensitive same as username", "alicepass1", "alicepass1", "ERR_PASSWORD_SAME_AS_USERNAME", false},
		{"case-different from username ok", "AlicePass1", "alicepass1", "", true},
		{"control char (newline) rejected", "abcd\n1234", "alice", "ERR_PASSWORD_CTRL_CHAR", false},
		{"control char (tab) rejected", "abc\t1234", "alice", "ERR_PASSWORD_CTRL_CHAR", false},
		{"unicode without control ok", "héllo123", "alice", "", true},
		{"no complexity required (long passphrase ok)", "thisisareallylongpasspharseokay", "alice", "", true},
		{"empty username allowed", "validpass1", "", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, ok := validatePasswordStrength(tc.password, tc.username)
			if ok != tc.wantOK {
				t.Errorf("ok = %v want %v (code=%q)", ok, tc.wantOK, code)
			}
			if code != tc.wantCode {
				t.Errorf("code = %q want %q", code, tc.wantCode)
			}
		})
	}
}

func TestIsAllSameRune(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"a", true},
		{"aaa", true},
		{"aaaa", true},
		{"aab", false},
		{"   ", true},
		{"1111", true},
		{"中中中", true},
		{"中文中", false},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			if got := isAllSameRune(tc.in); got != tc.want {
				t.Errorf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestRequireEmailFeatureEnabled(t *testing.T) {
	// fix：原版在外层 Lock，子测试体内调 requireEmailFeatureEnabled → readBoolConfig
	// → RLock 同一把锁 → 死锁。改为每个子测试 setup 时短临界区写入后立即解锁。
	prev := func() map[string]string {
		proxy.SysConfigMutex.RLock()
		defer proxy.SysConfigMutex.RUnlock()
		out := make(map[string]string, len(proxy.SysConfigCache))
		for k, v := range proxy.SysConfigCache {
			out[k] = v
		}
		return out
	}()
	defer func() {
		proxy.SysConfigMutex.Lock()
		proxy.SysConfigCache = prev
		proxy.SysConfigMutex.Unlock()
	}()
	setCache := func(m map[string]string) {
		proxy.SysConfigMutex.Lock()
		proxy.SysConfigCache = m
		proxy.SysConfigMutex.Unlock()
	}

	t.Run("master off → both false", func(t *testing.T) {
		setCache(map[string]string{})
		childOK, masterOK := requireEmailFeatureEnabled("email_login_enabled")
		if childOK || masterOK {
			t.Errorf("got %v %v want both false", childOK, masterOK)
		}
	})
	t.Run("master on child off", func(t *testing.T) {
		setCache(map[string]string{"email_enabled": "true"})
		childOK, masterOK := requireEmailFeatureEnabled("email_login_enabled")
		if childOK || !masterOK {
			t.Errorf("got %v %v want false true", childOK, masterOK)
		}
	})
	t.Run("master + child on", func(t *testing.T) {
		setCache(map[string]string{"email_enabled": "true", "email_login_enabled": "true"})
		childOK, masterOK := requireEmailFeatureEnabled("email_login_enabled")
		if !childOK || !masterOK {
			t.Errorf("got %v %v want both true", childOK, masterOK)
		}
	})
	t.Run("empty child = only check master", func(t *testing.T) {
		setCache(map[string]string{"email_enabled": "true"})
		childOK, masterOK := requireEmailFeatureEnabled("")
		if !childOK || !masterOK {
			t.Errorf("got %v %v want both true (empty child)", childOK, masterOK)
		}
	})
}

// ── PutMyEmailLoginEnabled 集成测试 ──

func setupToggleTestDB(t *testing.T) *database.User {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=private"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if sqlDB, dbErr := db.DB(); dbErr == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	if err := db.AutoMigrate(&database.User{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	database.DB = db
	now := time.Now()
	u := database.User{
		Username:        "alice",
		Token:           "sk-toggle-test",
		PasswordHash:    utils.GenerateHashForTest("validpass1"),
		Status:          1,
		Email:           "alice@example.com",
		EmailVerifiedAt: &now,
	}
	if err := db.Create(&u).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return &u
}

func newToggleTestApp(user *database.User) *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("user", user)
		return c.Next()
	})
	app.Put("/toggle", PutMyEmailLoginEnabled)
	return app
}

func toggleDoJSON(t *testing.T, app *fiber.App, body any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("PUT", "/toggle", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()
	bb, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	_ = json.Unmarshal(bb, &out)
	return resp.StatusCode, out
}

func TestPutMyEmailLoginEnabled_Success(t *testing.T) {
	user := setupToggleTestDB(t)
	app := newToggleTestApp(user)
	status, body := toggleDoJSON(t, app, map[string]any{
		"enabled":          true,
		"current_password": "validpass1",
	})
	if status != 200 {
		t.Fatalf("status %d body=%v", status, body)
	}
	if body["message_code"] != "SUCCESS_EMAIL_LOGIN_TOGGLED" {
		t.Errorf("msg = %v", body)
	}
	if body["enabled"] != true {
		t.Errorf("enabled = %v want true", body["enabled"])
	}
	// 验证 DB
	var fresh database.User
	if err := database.DB.First(&fresh, user.ID).Error; err != nil {
		t.Fatalf("read: %v", err)
	}
	if !fresh.EmailLoginEnabled {
		t.Error("EmailLoginEnabled should be true after toggle")
	}
}

func TestPutMyEmailLoginEnabled_WrongPassword(t *testing.T) {
	user := setupToggleTestDB(t)
	app := newToggleTestApp(user)
	status, body := toggleDoJSON(t, app, map[string]any{
		"enabled":          true,
		"current_password": "wrong-password",
	})
	if status != 401 {
		t.Errorf("status %d", status)
	}
	if body["message_code"] != "ERR_PASSWORD_INCORRECT" {
		t.Errorf("msg = %v", body)
	}
}

func TestPutMyEmailLoginEnabled_MissingCurrentPassword(t *testing.T) {
	user := setupToggleTestDB(t)
	app := newToggleTestApp(user)
	status, body := toggleDoJSON(t, app, map[string]any{"enabled": true})
	if status != 400 {
		t.Errorf("status %d", status)
	}
	if body["message_code"] != "ERR_CURRENT_PASSWORD_REQUIRED" {
		t.Errorf("msg = %v", body)
	}
}

func TestPutMyEmailLoginEnabled_NoVerifiedEmail(t *testing.T) {
	user := setupToggleTestDB(t)
	// 清掉 verified
	user.EmailVerifiedAt = nil
	database.DB.Save(user)
	app := newToggleTestApp(user)
	status, body := toggleDoJSON(t, app, map[string]any{
		"enabled":          true,
		"current_password": "validpass1",
	})
	if status != 400 {
		t.Errorf("status %d", status)
	}
	if body["message_code"] != "ERR_EMAIL_NOT_VERIFIED" {
		t.Errorf("msg = %v", body)
	}
}

func TestPutMyEmailLoginEnabled_NoPasswordSet(t *testing.T) {
	user := setupToggleTestDB(t)
	user.PasswordHash = ""
	database.DB.Save(user)
	app := newToggleTestApp(user)
	status, body := toggleDoJSON(t, app, map[string]any{
		"enabled":          true,
		"current_password": "anything",
	})
	if status != 400 {
		t.Errorf("status %d", status)
	}
	if body["message_code"] != "ERR_PASSWORD_NOT_SET" {
		t.Errorf("msg = %v", body)
	}
}

func TestPutMyEmailLoginEnabled_NoOp(t *testing.T) {
	user := setupToggleTestDB(t)
	user.EmailLoginEnabled = true
	database.DB.Save(user)
	app := newToggleTestApp(user)
	// 已经是 true 时再次提交 true → no-op
	status, body := toggleDoJSON(t, app, map[string]any{
		"enabled":          true,
		"current_password": "validpass1",
	})
	if status != 200 {
		t.Errorf("status %d", status)
	}
	if body["message_code"] != "SUCCESS_NO_CHANGE" {
		t.Errorf("expected NO_CHANGE, got %v", body)
	}
}

func TestPutMyEmailLoginEnabled_Disable(t *testing.T) {
	user := setupToggleTestDB(t)
	user.EmailLoginEnabled = true
	database.DB.Save(user)
	app := newToggleTestApp(user)
	status, body := toggleDoJSON(t, app, map[string]any{
		"enabled":          false,
		"current_password": "validpass1",
	})
	if status != 200 {
		t.Fatalf("status %d body=%v", status, body)
	}
	var fresh database.User
	database.DB.First(&fresh, user.ID)
	if fresh.EmailLoginEnabled {
		t.Error("should be disabled after toggle off")
	}
}
