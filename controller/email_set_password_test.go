// Package controller / email_set_password_test.go
//
// Phase G-2.5 单元测试：RequestSetPassword + SetPassword。
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

func setupSetPwdTestDB(t *testing.T) {
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
		&database.User{}, &database.EmailVerification{},
		&database.OperationLog{}, &database.SysConfig{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	database.DB = db

	encPwd, _ := utils.Encrypt("smtp-pwd")
	proxy.SysConfigMutex.Lock()
	proxy.SysConfigCache = map[string]string{
		"email_enabled":                "true",
		"server_address":               "https://app.example.com",
		"server_address_require_https": "true",
		"email_reset_url_path":         "/reset-password",
		"email_reset_ttl_seconds":      "900",
		"smtp_host":                    "smtp.example.com",
		"smtp_port":                    "587",
		"smtp_username":                "noreply@example.com",
		"smtp_password":                encPwd,
		"smtp_from":                    "DAOF <noreply@example.com>",
		"smtp_use_implicit_tls":        "false",
	}
	proxy.SysConfigMutex.Unlock()
}

type setPwdUserOpts struct {
	username     string
	password     string // empty = OAuth-only
	email        string
	verified     bool
	loginEnabled bool
}

func seedSetPwdUser(t *testing.T, opts setPwdUserOpts) *database.User {
	t.Helper()
	u := database.User{
		Username:          opts.username,
		Token:             "sk-setpwd-" + opts.username,
		Status:            1,
		Email:             opts.email,
		EmailLoginEnabled: opts.loginEnabled,
	}
	if opts.password != "" {
		u.PasswordHash = utils.GenerateHashForTest(opts.password)
	}
	if opts.verified {
		now := time.Now()
		u.EmailVerifiedAt = &now
	}
	if err := database.DB.Create(&u).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return &u
}

func newRequestSetPwdApp(user *database.User) *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error {
		if user != nil {
			c.Locals("user", user)
		}
		return c.Next()
	})
	app.Post("/request-set-password", RequestSetPassword)
	return app
}

func newSetPwdApp() *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Post("/set-password", SetPassword)
	return app
}

func setPwdDoJSON(t *testing.T, app *fiber.App, path string, body any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", path, bytes.NewReader(b))
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

// =============================================================================
// RequestSetPassword
// =============================================================================

func TestRequestSetPassword_Success_OAuthUser(t *testing.T) {
	utils.InitCrypto()
	setupSetPwdTestDB(t)
	// OAuth 用户：已验邮箱，但无密码
	user := seedSetPwdUser(t, setPwdUserOpts{
		username: "github_user", password: "", email: "gh@example.com", verified: true,
	})
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

	app := newRequestSetPwdApp(user)
	status, body := setPwdDoJSON(t, app, "/request-set-password", map[string]any{})
	if status != 200 {
		t.Fatalf("status %d body=%v", status, body)
	}
	if body["message_code"] != "SUCCESS_SET_PASSWORD_EMAIL_SENT" {
		t.Errorf("msg = %v", body)
	}
	if len(captured) != 1 || captured[0] != "gh@example.com" {
		t.Errorf("expected 1 email to gh@example.com; got %v", captured)
	}
	// DB 应有一条 set_password 待消费 token
	var n int64
	database.DB.Model(&database.EmailVerification{}).
		Where("user_id = ? AND purpose = ?", user.ID, database.EmailVerificationPurposeSetPassword).
		Count(&n)
	if n != 1 {
		t.Errorf("expected 1 token; got %d", n)
	}
}

func TestRequestSetPassword_EmailNotVerified_Rejected(t *testing.T) {
	utils.InitCrypto()
	setupSetPwdTestDB(t)
	user := seedSetPwdUser(t, setPwdUserOpts{
		username: "u", password: "", email: "u@e.com", verified: false,
	})
	app := newRequestSetPwdApp(user)
	status, body := setPwdDoJSON(t, app, "/request-set-password", map[string]any{})
	if status != 400 {
		t.Errorf("status %d", status)
	}
	if body["message_code"] != "ERR_EMAIL_NOT_VERIFIED" {
		t.Errorf("msg = %v", body)
	}
}

func TestRequestSetPassword_AlreadyHasPassword_Rejected(t *testing.T) {
	utils.InitCrypto()
	setupSetPwdTestDB(t)
	user := seedSetPwdUser(t, setPwdUserOpts{
		username: "u", password: "existing1", email: "u@e.com", verified: true,
	})
	app := newRequestSetPwdApp(user)
	status, body := setPwdDoJSON(t, app, "/request-set-password", map[string]any{})
	if status != 409 {
		t.Errorf("status %d (already-set users must use forgot-password)", status)
	}
	if body["message_code"] != "ERR_PASSWORD_ALREADY_SET" {
		t.Errorf("msg = %v", body)
	}
}

func TestRequestSetPassword_NoAuth_Rejected(t *testing.T) {
	utils.InitCrypto()
	setupSetPwdTestDB(t)
	app := newRequestSetPwdApp(nil) // no user in Locals
	status, body := setPwdDoJSON(t, app, "/request-set-password", map[string]any{})
	if status != 401 {
		t.Errorf("status %d", status)
	}
	if body["message_code"] != "ERR_NO_AUTH" {
		t.Errorf("msg = %v", body)
	}
}

func TestRequestSetPassword_MasterDisabled(t *testing.T) {
	utils.InitCrypto()
	setupSetPwdTestDB(t)
	user := seedSetPwdUser(t, setPwdUserOpts{
		username: "u", password: "", email: "u@e.com", verified: true,
	})
	proxy.SysConfigMutex.Lock()
	proxy.SysConfigCache["email_enabled"] = "false"
	proxy.SysConfigMutex.Unlock()

	app := newRequestSetPwdApp(user)
	status, body := setPwdDoJSON(t, app, "/request-set-password", map[string]any{})
	if status != 503 {
		t.Errorf("status %d", status)
	}
	if body["message_code"] != "ERR_EMAIL_FEATURE_DISABLED" {
		t.Errorf("msg = %v", body)
	}
}

// =============================================================================
// SetPassword
// =============================================================================

func TestSetPassword_Success(t *testing.T) {
	utils.InitCrypto()
	setupSetPwdTestDB(t)
	user := seedSetPwdUser(t, setPwdUserOpts{
		username: "github_user", password: "", email: "gh@example.com", verified: true,
		loginEnabled: false, // 还没开启
	})
	rawToken, tokenHash, err := generateEmailToken()
	if err != nil {
		t.Fatalf("token gen: %v", err)
	}
	now := time.Now()
	v := database.EmailVerification{
		UserID:    user.ID,
		Email:     user.Email,
		TokenHash: tokenHash,
		Purpose:   database.EmailVerificationPurposeSetPassword,
		ExpiresAt: now.Add(15 * time.Minute),
		CreatedAt: now,
	}
	if err := database.DB.Create(&v).Error; err != nil {
		t.Fatalf("seed token: %v", err)
	}

	app := newSetPwdApp()
	status, body := setPwdDoJSON(t, app, "/set-password", map[string]string{
		"token":        rawToken,
		"new_password": "validpass1",
	})
	if status != 200 {
		t.Fatalf("status %d body=%v", status, body)
	}
	if body["message_code"] != "SUCCESS_PASSWORD_SET" {
		t.Errorf("msg = %v", body)
	}

	var u database.User
	database.DB.First(&u, user.ID)
	if !utils.CheckHash("validpass1", u.PasswordHash) {
		t.Error("password hash mismatch")
	}
	if !u.EmailLoginEnabled {
		t.Error("EmailLoginEnabled should be auto-flipped to true after set-password")
	}
	var consumed database.EmailVerification
	database.DB.First(&consumed, v.ID)
	if consumed.ConsumedAt == nil {
		t.Error("token should be consumed")
	}
}

func TestSetPassword_TokenInvalid(t *testing.T) {
	utils.InitCrypto()
	setupSetPwdTestDB(t)
	app := newSetPwdApp()
	status, body := setPwdDoJSON(t, app, "/set-password", map[string]string{
		"token": "nonexistent", "new_password": "validpass1",
	})
	if status != 400 {
		t.Errorf("status %d", status)
	}
	if body["message_code"] != "ERR_EMAIL_TOKEN_INVALID" {
		t.Errorf("msg = %v", body)
	}
}

func TestSetPassword_WrongPurpose_ResetTokenRejected(t *testing.T) {
	utils.InitCrypto()
	setupSetPwdTestDB(t)
	user := seedSetPwdUser(t, setPwdUserOpts{
		username: "u", password: "", email: "u@e.com", verified: true,
	})
	// 用 reset_password purpose 的 token 走 set-password → 必拒
	rawToken, hash, _ := generateEmailToken()
	now := time.Now()
	v := database.EmailVerification{
		UserID: user.ID, Email: user.Email, TokenHash: hash,
		Purpose: database.EmailVerificationPurposeResetPassword,
		ExpiresAt: now.Add(15 * time.Minute), CreatedAt: now,
	}
	if err := database.DB.Create(&v).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	app := newSetPwdApp()
	status, body := setPwdDoJSON(t, app, "/set-password", map[string]string{
		"token": rawToken, "new_password": "validpass1",
	})
	if status != 400 {
		t.Errorf("status %d", status)
	}
	if body["message_code"] != "ERR_EMAIL_TOKEN_INVALID" {
		t.Errorf("msg = %v", body)
	}
}

func TestSetPassword_TokenConsumed(t *testing.T) {
	utils.InitCrypto()
	setupSetPwdTestDB(t)
	user := seedSetPwdUser(t, setPwdUserOpts{
		username: "u", password: "", email: "u@e.com", verified: true,
	})
	rawToken, hash, _ := generateEmailToken()
	now := time.Now()
	consumed := now.Add(-1 * time.Minute)
	v := database.EmailVerification{
		UserID: user.ID, Email: user.Email, TokenHash: hash,
		Purpose: database.EmailVerificationPurposeSetPassword,
		ExpiresAt: now.Add(15 * time.Minute), CreatedAt: now, ConsumedAt: &consumed,
	}
	if err := database.DB.Create(&v).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	app := newSetPwdApp()
	status, body := setPwdDoJSON(t, app, "/set-password", map[string]string{
		"token": rawToken, "new_password": "validpass1",
	})
	if status != 409 {
		t.Errorf("status %d", status)
	}
	if body["message_code"] != "ERR_EMAIL_TOKEN_CONSUMED" {
		t.Errorf("msg = %v", body)
	}
}

func TestSetPassword_TokenExpired(t *testing.T) {
	utils.InitCrypto()
	setupSetPwdTestDB(t)
	user := seedSetPwdUser(t, setPwdUserOpts{
		username: "u", password: "", email: "u@e.com", verified: true,
	})
	rawToken, hash, _ := generateEmailToken()
	now := time.Now()
	v := database.EmailVerification{
		UserID: user.ID, Email: user.Email, TokenHash: hash,
		Purpose: database.EmailVerificationPurposeSetPassword,
		ExpiresAt: now.Add(-1 * time.Minute), CreatedAt: now,
	}
	if err := database.DB.Create(&v).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	app := newSetPwdApp()
	status, body := setPwdDoJSON(t, app, "/set-password", map[string]string{
		"token": rawToken, "new_password": "validpass1",
	})
	if status != 410 {
		t.Errorf("status %d", status)
	}
	if body["message_code"] != "ERR_EMAIL_TOKEN_EXPIRED" {
		t.Errorf("msg = %v", body)
	}
}

func TestSetPassword_RaceUserAlreadyHasPassword(t *testing.T) {
	utils.InitCrypto()
	setupSetPwdTestDB(t)
	// 用户 seed 时 PasswordHash 为空（OAuth-only），生成 set_password token
	user := seedSetPwdUser(t, setPwdUserOpts{
		username: "u", password: "", email: "u@e.com", verified: true,
	})
	rawToken, hash, _ := generateEmailToken()
	now := time.Now()
	v := database.EmailVerification{
		UserID: user.ID, Email: user.Email, TokenHash: hash,
		Purpose: database.EmailVerificationPurposeSetPassword,
		ExpiresAt: now.Add(15 * time.Minute), CreatedAt: now,
	}
	if err := database.DB.Create(&v).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	// 模拟并发：在 SetPassword 调用之前从其他渠道写了 PasswordHash
	if err := database.DB.Model(&database.User{}).
		Where("id = ?", user.ID).
		Update("password_hash", utils.GenerateHashForTest("setbyrace1")).Error; err != nil {
		t.Fatalf("race update: %v", err)
	}

	app := newSetPwdApp()
	status, body := setPwdDoJSON(t, app, "/set-password", map[string]string{
		"token": rawToken, "new_password": "validpass1",
	})
	if status != 409 {
		t.Errorf("status %d", status)
	}
	if body["message_code"] != "ERR_PASSWORD_ALREADY_SET" {
		t.Errorf("msg = %v", body)
	}
	// 旧密码（race 写入的）仍生效 — set-password 必须没发生
	var u database.User
	database.DB.First(&u, user.ID)
	if !utils.CheckHash("setbyrace1", u.PasswordHash) {
		t.Error("race-set password should still match — set-password must NOT have overwritten")
	}
}

func TestSetPassword_WeakPassword(t *testing.T) {
	utils.InitCrypto()
	setupSetPwdTestDB(t)
	user := seedSetPwdUser(t, setPwdUserOpts{
		username: "u", password: "", email: "u@e.com", verified: true,
	})
	rawToken, hash, _ := generateEmailToken()
	now := time.Now()
	v := database.EmailVerification{
		UserID: user.ID, Email: user.Email, TokenHash: hash,
		Purpose: database.EmailVerificationPurposeSetPassword,
		ExpiresAt: now.Add(15 * time.Minute), CreatedAt: now,
	}
	if err := database.DB.Create(&v).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	app := newSetPwdApp()
	status, body := setPwdDoJSON(t, app, "/set-password", map[string]string{
		"token": rawToken, "new_password": "short",
	})
	if status != 400 {
		t.Errorf("status %d", status)
	}
	if body["message_code"] != "ERR_PASSWORD_TOO_SHORT" {
		t.Errorf("msg = %v", body)
	}
	// token 未消费
	var still database.EmailVerification
	database.DB.First(&still, v.ID)
	if still.ConsumedAt != nil {
		t.Error("token should NOT be consumed when password validation fails")
	}
}

func TestSetPassword_MasterDisabled(t *testing.T) {
	utils.InitCrypto()
	setupSetPwdTestDB(t)
	proxy.SysConfigMutex.Lock()
	proxy.SysConfigCache["email_enabled"] = "false"
	proxy.SysConfigMutex.Unlock()
	app := newSetPwdApp()
	status, body := setPwdDoJSON(t, app, "/set-password", map[string]string{
		"token": "x", "new_password": "validpass1",
	})
	if status != 503 {
		t.Errorf("status %d", status)
	}
	if body["message_code"] != "ERR_EMAIL_FEATURE_DISABLED" {
		t.Errorf("msg = %v", body)
	}
}
