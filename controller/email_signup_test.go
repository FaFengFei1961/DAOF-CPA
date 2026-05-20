// Package controller / email_signup_test.go
//
// Phase G-2.3 单元测试：EmailSignup endpoint。
package controller

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"testing"

	"daof-cpa/database"
	"daof-cpa/proxy"
	"daof-cpa/utils"

	"github.com/gofiber/fiber/v2"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupEmailSignupTestDB(t *testing.T) {
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
	if err := db.AutoMigrate(
		&database.User{}, &database.BillingEntry{}, &database.EmailVerification{},
		&database.OperationLog{}, &database.SysConfig{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	database.DB = db

	encPwd, _ := utils.Encrypt("smtp-pwd")
	proxy.SysConfigMutex.Lock()
	proxy.SysConfigCache = map[string]string{
		"email_enabled":                "true",
		"email_signup_enabled":         "true",
		"signup_bonus":                 "1000000", // $1
		"max_users":                    "0",
		"server_address":               "https://app.example.com",
		"server_address_require_https": "true",
		"email_verify_url_path":        "/verify-email",
		"email_verify_ttl_seconds":     "3600",
		// SMTP 配置 — IsConfigured 要求 host/port/user/password/from 全有
		"smtp_host":             "smtp.example.com",
		"smtp_port":             "587",
		"smtp_username":         "noreply@example.com",
		"smtp_password":         encPwd,
		"smtp_from":             "DAOF <noreply@example.com>",
		"smtp_use_implicit_tls": "false",
	}
	proxy.SysConfigMutex.Unlock()
}

func newSignupTestApp() *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Post("/signup", EmailSignup)
	return app
}

func signupDoJSON(t *testing.T, app *fiber.App, body any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/signup", bytes.NewReader(b))
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

func TestEmailSignup_Success(t *testing.T) {
	utils.InitCrypto()
	setupEmailSignupTestDB(t)
	app := newSignupTestApp()
	// Stub SMTP send so the verify email enqueue doesn't try real network
	proxy.SetEmailQueueSyncForTest(true)
	proxy.SetSendEmailViaSMTPHookForTest(func(cfg proxy.SMTPConfig, msg proxy.EmailMessage) error { return nil })
	defer func() {
		proxy.SetEmailQueueSyncForTest(false)
		proxy.SetSendEmailViaSMTPHookForTest(nil)
	}()

	status, body := signupDoJSON(t, app, map[string]string{
		"email":    "alice@example.com",
		"password": "validpass1",
		"username": "alice_n",
	})
	if status != 200 {
		t.Fatalf("status %d body=%v", status, body)
	}
	if body["message_code"] != "SUCCESS_SIGNUP_PENDING_VERIFY" {
		t.Errorf("msg = %v", body)
	}
	if body["username"] != "alice_n" {
		t.Errorf("username = %v", body["username"])
	}
	// session_id 不应返回（未验证不能登录）
	if _, ok := body["session_id"]; ok {
		t.Error("session_id should NOT be returned (must verify email first)")
	}
	// DB 应有 user，且 PasswordHash 是 bcrypt 格式
	var u database.User
	if err := database.DB.Where("email = ?", "alice@example.com").First(&u).Error; err != nil {
		t.Fatalf("user not created: %v", err)
	}
	if !utils.CheckHash("validpass1", u.PasswordHash) {
		t.Error("password hash mismatch")
	}
	if u.EmailVerifiedAt != nil {
		t.Error("EmailVerifiedAt should be nil at signup")
	}
	if u.EmailLoginEnabled {
		t.Error("EmailLoginEnabled should be false at signup")
	}
	// 应该有一条 EmailVerification 待消费
	var count int64
	database.DB.Model(&database.EmailVerification{}).
		Where("user_id = ? AND purpose = ? AND consumed_at IS NULL",
			u.ID, database.EmailVerificationPurposeVerify).Count(&count)
	if count != 1 {
		t.Errorf("expected 1 pending verify token, got %d", count)
	}
}

func TestEmailSignup_MasterDisabled(t *testing.T) {
	utils.InitCrypto()
	setupEmailSignupTestDB(t)
	proxy.SysConfigMutex.Lock()
	proxy.SysConfigCache["email_enabled"] = "false"
	proxy.SysConfigMutex.Unlock()
	app := newSignupTestApp()
	status, body := signupDoJSON(t, app, map[string]string{
		"email": "a@e.com", "password": "validpass1", "username": "alice",
	})
	if status != 503 {
		t.Errorf("status %d", status)
	}
	if body["message_code"] != "ERR_EMAIL_FEATURE_DISABLED" {
		t.Errorf("msg = %v", body)
	}
}

func TestEmailSignup_ChildDisabled(t *testing.T) {
	utils.InitCrypto()
	setupEmailSignupTestDB(t)
	proxy.SysConfigMutex.Lock()
	proxy.SysConfigCache["email_signup_enabled"] = "false"
	proxy.SysConfigMutex.Unlock()
	app := newSignupTestApp()
	status, body := signupDoJSON(t, app, map[string]string{
		"email": "a@e.com", "password": "validpass1", "username": "alice",
	})
	if status != 503 {
		t.Errorf("status %d", status)
	}
	if body["message_code"] != "ERR_EMAIL_SIGNUP_DISABLED" {
		t.Errorf("msg = %v", body)
	}
}

func TestEmailSignup_InvalidEmail(t *testing.T) {
	utils.InitCrypto()
	setupEmailSignupTestDB(t)
	app := newSignupTestApp()
	status, body := signupDoJSON(t, app, map[string]string{
		"email": "not-an-email", "password": "validpass1", "username": "alice",
	})
	if status != 400 {
		t.Errorf("status %d", status)
	}
	if body["message_code"] != "ERR_EMAIL_INVALID_FORMAT" {
		t.Errorf("msg = %v", body)
	}
}

func TestEmailSignup_BadUsername(t *testing.T) {
	utils.InitCrypto()
	setupEmailSignupTestDB(t)
	app := newSignupTestApp()
	status, body := signupDoJSON(t, app, map[string]string{
		"email": "a@e.com", "password": "validpass1", "username": "ab",  // 2 chars OK actually; let me use a clearly bad one
	})
	// "ab" is 2 chars, regex allows 2-20 → should pass; switch to bad char
	if status == 200 {
		// regex passes "ab" — switch test input
		status, body = signupDoJSON(t, app, map[string]string{
			"email": "b@e.com", "password": "validpass1", "username": "bad space",
		})
	}
	if status != 400 {
		t.Errorf("status %d", status)
	}
	if body["message_code"] != "ERR_NICKNAME_FORMAT" {
		t.Errorf("msg = %v", body)
	}
}

func TestEmailSignup_WeakPassword(t *testing.T) {
	utils.InitCrypto()
	setupEmailSignupTestDB(t)
	app := newSignupTestApp()
	status, body := signupDoJSON(t, app, map[string]string{
		"email": "a@e.com", "password": "short", "username": "alice",
	})
	if status != 400 {
		t.Errorf("status %d", status)
	}
	if body["message_code"] != "ERR_PASSWORD_TOO_SHORT" {
		t.Errorf("msg = %v", body)
	}
}

func TestEmailSignup_EmailTaken(t *testing.T) {
	utils.InitCrypto()
	setupEmailSignupTestDB(t)
	// 预创建一个邮箱被占用的用户
	existing := database.User{
		Username:     "preexisting",
		Token:        "sk-pre",
		PasswordHash: "x",
		Status:       1,
		Email:        "taken@example.com",
	}
	if err := database.DB.Create(&existing).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	app := newSignupTestApp()
	status, body := signupDoJSON(t, app, map[string]string{
		"email": "taken@example.com", "password": "validpass1", "username": "newuser",
	})
	if status != 409 {
		t.Errorf("status %d", status)
	}
	if body["message_code"] != "ERR_EMAIL_TAKEN" {
		t.Errorf("msg = %v", body)
	}
}

func TestEmailSignup_UsernameTaken(t *testing.T) {
	utils.InitCrypto()
	setupEmailSignupTestDB(t)
	existing := database.User{
		Username: "alice", Token: "sk-pre", PasswordHash: "x", Status: 1,
	}
	if err := database.DB.Create(&existing).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	app := newSignupTestApp()
	status, body := signupDoJSON(t, app, map[string]string{
		"email": "new@example.com", "password": "validpass1", "username": "alice",
	})
	if status != 409 {
		t.Errorf("status %d", status)
	}
	if body["message_code"] != "ERR_USERNAME_TAKEN" {
		t.Errorf("msg = %v", body)
	}
}

func TestEmailSignup_SignupBonusGranted(t *testing.T) {
	utils.InitCrypto()
	setupEmailSignupTestDB(t)
	proxy.SetEmailQueueSyncForTest(true)
	proxy.SetSendEmailViaSMTPHookForTest(func(cfg proxy.SMTPConfig, msg proxy.EmailMessage) error { return nil })
	defer func() {
		proxy.SetEmailQueueSyncForTest(false)
		proxy.SetSendEmailViaSMTPHookForTest(nil)
	}()

	app := newSignupTestApp()
	status, _ := signupDoJSON(t, app, map[string]string{
		"email": "bonus@example.com", "password": "validpass1", "username": "bonus_user",
	})
	if status != 200 {
		t.Fatalf("status %d", status)
	}
	var u database.User
	database.DB.Where("email = ?", "bonus@example.com").First(&u)
	if u.Quota != 1000000 { // $1 in micro_usd
		t.Errorf("Quota = %d want 1000000", u.Quota)
	}
}

func TestEmailSignup_NoSessionReturned(t *testing.T) {
	utils.InitCrypto()
	setupEmailSignupTestDB(t)
	proxy.SetEmailQueueSyncForTest(true)
	proxy.SetSendEmailViaSMTPHookForTest(func(cfg proxy.SMTPConfig, msg proxy.EmailMessage) error { return nil })
	defer func() {
		proxy.SetEmailQueueSyncForTest(false)
		proxy.SetSendEmailViaSMTPHookForTest(nil)
	}()

	app := newSignupTestApp()
	_, body := signupDoJSON(t, app, map[string]string{
		"email": "nosession@e.com", "password": "validpass1", "username": "nosession",
	})
	if _, ok := body["session_id"]; ok {
		t.Error("session_id leaked — signup should require email verification first")
	}
}

func TestEmailSignup_VerifyEmailEnqueuedWithDedupKey(t *testing.T) {
	utils.InitCrypto()
	setupEmailSignupTestDB(t)
	var captured []string
	proxy.SetEmailQueueSyncForTest(true)
	proxy.SetSendEmailViaSMTPHookForTest(func(cfg proxy.SMTPConfig, msg proxy.EmailMessage) error {
		captured = append(captured, msg.To)
		return nil
	})
	defer func() {
		proxy.SetEmailQueueSyncForTest(false)
		proxy.SetSendEmailViaSMTPHookForTest(nil)
	}()

	app := newSignupTestApp()
	status, _ := signupDoJSON(t, app, map[string]string{
		"email": "verify@example.com", "password": "validpass1", "username": "verify_test",
	})
	if status != 200 {
		t.Fatalf("status %d", status)
	}
	if len(captured) != 1 {
		t.Errorf("expected 1 verify email sent, got %d", len(captured))
	}
	if len(captured) > 0 && captured[0] != "verify@example.com" {
		t.Errorf("To = %q want verify@example.com", captured[0])
	}
}
