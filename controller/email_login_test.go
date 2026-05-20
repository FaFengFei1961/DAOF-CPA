// Package controller / email_login_test.go
//
// Phase G-2.2 单元测试：EmailLogin endpoint。
//
// 重点：邮箱枚举防御（所有失败原因都返回统一 ERR_LOGIN_FAILED）。
package controller

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
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

func setupEmailLoginTestDB(t *testing.T) {
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
	if err := db.AutoMigrate(&database.User{}, &database.UserSession{}, &database.OperationLog{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	database.DB = db

	proxy.SysConfigMutex.Lock()
	proxy.SysConfigCache = map[string]string{
		"email_enabled":       "true",
		"email_login_enabled": "true",
	}
	proxy.SysConfigMutex.Unlock()
}

func seedLoginUser(t *testing.T, opts loginUserOpts) *database.User {
	t.Helper()
	now := time.Now()
	verifiedAt := &now
	if !opts.verified {
		verifiedAt = nil
	}
	u := database.User{
		Username:          opts.username,
		Token:             "sk-login-" + opts.username,
		PasswordHash:      utils.GenerateHashForTest(opts.password),
		Status:            opts.status,
		Email:             opts.email,
		EmailVerifiedAt:   verifiedAt,
		EmailLoginEnabled: opts.loginEnabled,
	}
	if u.Status == 0 {
		u.Status = 1
	}
	if err := database.DB.Create(&u).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return &u
}

type loginUserOpts struct {
	username     string
	password     string
	email        string
	verified     bool
	loginEnabled bool
	status       int
}

func newLoginTestApp() *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Post("/login", EmailLogin)
	return app
}

func loginDoJSON(t *testing.T, app *fiber.App, body any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/login", bytes.NewReader(b))
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

func TestEmailLogin_Success(t *testing.T) {
	utils.InitCrypto()
	setupEmailLoginTestDB(t)
	seedLoginUser(t, loginUserOpts{
		username:     "alice",
		password:     "validpass1",
		email:        "alice@example.com",
		verified:     true,
		loginEnabled: true,
	})
	app := newLoginTestApp()
	status, body := loginDoJSON(t, app, map[string]string{
		"email":    "alice@example.com",
		"password": "validpass1",
	})
	if status != 200 {
		t.Fatalf("status %d body=%v", status, body)
	}
	if body["message_code"] != "SUCCESS_LOGIN" {
		t.Errorf("msg = %v", body)
	}
	if _, ok := body["session_id"].(string); !ok {
		t.Error("session_id missing")
	}
	if body["username"] != "alice" {
		t.Errorf("username = %v", body["username"])
	}
}

func TestEmailLogin_MasterDisabled(t *testing.T) {
	utils.InitCrypto()
	setupEmailLoginTestDB(t)
	proxy.SysConfigMutex.Lock()
	proxy.SysConfigCache["email_enabled"] = "false"
	proxy.SysConfigMutex.Unlock()
	app := newLoginTestApp()
	status, body := loginDoJSON(t, app, map[string]string{"email": "any@e.com", "password": "x"})
	if status != 503 {
		t.Errorf("status %d", status)
	}
	if body["message_code"] != "ERR_EMAIL_FEATURE_DISABLED" {
		t.Errorf("msg = %v", body)
	}
}

func TestEmailLogin_LoginChildDisabled(t *testing.T) {
	utils.InitCrypto()
	setupEmailLoginTestDB(t)
	proxy.SysConfigMutex.Lock()
	proxy.SysConfigCache["email_login_enabled"] = "false"
	proxy.SysConfigMutex.Unlock()
	app := newLoginTestApp()
	status, body := loginDoJSON(t, app, map[string]string{"email": "any@e.com", "password": "x"})
	if status != 503 {
		t.Errorf("status %d", status)
	}
	if body["message_code"] != "ERR_EMAIL_LOGIN_DISABLED" {
		t.Errorf("msg = %v", body)
	}
}

func TestEmailLogin_EnumerationDefense(t *testing.T) {
	utils.InitCrypto()
	setupEmailLoginTestDB(t)
	seedLoginUser(t, loginUserOpts{
		username: "alice", password: "validpass1", email: "alice@example.com",
		verified: true, loginEnabled: true,
	})
	app := newLoginTestApp()

	tests := []struct {
		name     string
		body     map[string]string
		wantCode string
	}{
		{"wrong password", map[string]string{"email": "alice@example.com", "password": "wrong"}, "ERR_LOGIN_FAILED"},
		{"unknown email", map[string]string{"email": "ghost@example.com", "password": "validpass1"}, "ERR_LOGIN_FAILED"},
		{"invalid email format", map[string]string{"email": "not-email", "password": "x"}, "ERR_LOGIN_FAILED"},
		{"empty password", map[string]string{"email": "alice@example.com", "password": ""}, "ERR_LOGIN_FAILED"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status, body := loginDoJSON(t, app, tc.body)
			if status != 401 {
				t.Errorf("status %d want 401", status)
			}
			if body["message_code"] != tc.wantCode {
				t.Errorf("code = %v want %s", body["message_code"], tc.wantCode)
			}
		})
	}
}

func TestEmailLogin_UnverifiedEmailReturnsLoginFailed(t *testing.T) {
	utils.InitCrypto()
	setupEmailLoginTestDB(t)
	// Verified=false：登录应该失败但返回统一 LOGIN_FAILED（防枚举）
	seedLoginUser(t, loginUserOpts{
		username: "alice", password: "validpass1", email: "alice@example.com",
		verified: false, loginEnabled: true,
	})
	app := newLoginTestApp()
	status, body := loginDoJSON(t, app, map[string]string{
		"email": "alice@example.com", "password": "validpass1",
	})
	if status != 401 {
		t.Errorf("status %d", status)
	}
	if body["message_code"] != "ERR_LOGIN_FAILED" {
		t.Errorf("code = %v want ERR_LOGIN_FAILED (not leaking 'email not verified')", body["message_code"])
	}
}

func TestEmailLogin_EmailLoginDisabledForUserReturnsLoginFailed(t *testing.T) {
	utils.InitCrypto()
	setupEmailLoginTestDB(t)
	// loginEnabled=false：用户自己没开邮箱登录，与"密码错"返回同样错误（防枚举）
	seedLoginUser(t, loginUserOpts{
		username: "alice", password: "validpass1", email: "alice@example.com",
		verified: true, loginEnabled: false,
	})
	app := newLoginTestApp()
	status, body := loginDoJSON(t, app, map[string]string{
		"email": "alice@example.com", "password": "validpass1",
	})
	if status != 401 {
		t.Errorf("status %d", status)
	}
	if body["message_code"] != "ERR_LOGIN_FAILED" {
		t.Errorf("code = %v want ERR_LOGIN_FAILED", body["message_code"])
	}
}

func TestEmailLogin_BannedUserReturnsLoginFailed(t *testing.T) {
	utils.InitCrypto()
	setupEmailLoginTestDB(t)
	seedLoginUser(t, loginUserOpts{
		username: "alice", password: "validpass1", email: "alice@example.com",
		verified: true, loginEnabled: true, status: 2, // banned
	})
	app := newLoginTestApp()
	status, body := loginDoJSON(t, app, map[string]string{
		"email": "alice@example.com", "password": "validpass1",
	})
	// banned user is filtered by "status = 1" SQL → looks like "user not found" → LOGIN_FAILED
	if status != 401 {
		t.Errorf("status %d", status)
	}
	if body["message_code"] != "ERR_LOGIN_FAILED" {
		t.Errorf("code = %v want ERR_LOGIN_FAILED", body["message_code"])
	}
}

func TestEmailLogin_CaseInsensitiveEmail(t *testing.T) {
	utils.InitCrypto()
	setupEmailLoginTestDB(t)
	// 创建时已是小写
	seedLoginUser(t, loginUserOpts{
		username: "alice", password: "validpass1", email: "alice@example.com",
		verified: true, loginEnabled: true,
	})
	app := newLoginTestApp()
	// 用大写登录应该 normalize 成小写后匹配
	status, body := loginDoJSON(t, app, map[string]string{
		"email": "ALICE@EXAMPLE.COM", "password": "validpass1",
	})
	if status != 200 {
		t.Errorf("status %d (should be case-insensitive) body=%v", status, body)
	}
}

func TestEmailLogin_BadRequestParse(t *testing.T) {
	utils.InitCrypto()
	setupEmailLoginTestDB(t)
	app := newLoginTestApp()
	req := httptest.NewRequest("POST", "/login", bytes.NewReader([]byte("not json")))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != 400 {
		t.Errorf("status %d", resp.StatusCode)
	}
}
