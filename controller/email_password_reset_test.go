// Package controller / email_password_reset_test.go
//
// Phase G-2.4 单元测试：ForgotPassword + ResetPassword。
//
// 重点：
//   - 邮箱枚举防御 —— forgot-password 对存在 / 不存在 / 未验证 / OAuth-only 用户
//     返回同一 message_code，且 DB 不留 token 行（仅"真正发邮件"路径才插入）
//   - token 一次性 / 短 TTL / Purpose 严格 filter
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

func setupResetPwdTestDB(t *testing.T) {
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

func newResetPwdTestApp() *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Post("/forgot-password", ForgotPassword)
	app.Post("/reset-password", ResetPassword)
	return app
}

func resetPwdDoJSON(t *testing.T, app *fiber.App, path string, body any) (int, map[string]any) {
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

type resetUserOpts struct {
	username string
	password string // empty = OAuth-only (no PasswordHash)
	email    string
	verified bool
}

func seedResetUser(t *testing.T, opts resetUserOpts) *database.User {
	t.Helper()
	u := database.User{
		Username: opts.username,
		Token:    "sk-reset-" + opts.username,
		Status:   1,
		Email:    opts.email,
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

func countResetTokens(t *testing.T, userID uint) int64 {
	t.Helper()
	var c int64
	database.DB.Model(&database.EmailVerification{}).
		Where("user_id = ? AND purpose = ?", userID, database.EmailVerificationPurposeResetPassword).
		Count(&c)
	return c
}

// =============================================================================
// ForgotPassword
// =============================================================================

func TestForgotPassword_Success(t *testing.T) {
	utils.InitCrypto()
	setupResetPwdTestDB(t)
	user := seedResetUser(t, resetUserOpts{
		username: "alice", password: "oldpass1", email: "alice@example.com", verified: true,
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

	app := newResetPwdTestApp()
	status, body := resetPwdDoJSON(t, app, "/forgot-password", map[string]string{
		"email": "alice@example.com",
	})
	if status != 200 {
		t.Fatalf("status %d body=%v", status, body)
	}
	if body["message_code"] != "SUCCESS_PASSWORD_RESET_EMAIL_SENT" {
		t.Errorf("msg = %v", body)
	}
	if len(captured) != 1 || captured[0] != "alice@example.com" {
		t.Errorf("expected 1 email to alice@example.com, got %v", captured)
	}
	if n := countResetTokens(t, user.ID); n != 1 {
		t.Errorf("expected 1 reset token, got %d", n)
	}
}

func TestForgotPassword_NonexistentEmail_SilentNoOp(t *testing.T) {
	utils.InitCrypto()
	setupResetPwdTestDB(t)
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

	app := newResetPwdTestApp()
	status, body := resetPwdDoJSON(t, app, "/forgot-password", map[string]string{
		"email": "nobody@example.com",
	})
	if status != 200 {
		t.Errorf("status %d", status)
	}
	if body["message_code"] != "SUCCESS_PASSWORD_RESET_EMAIL_SENT" {
		t.Errorf("枚举防御：响应必须与存在用户一致；got %v", body)
	}
	if len(captured) != 0 {
		t.Errorf("不应发出邮件 — got %v", captured)
	}
	// DB 不应有任何 reset token
	var n int64
	database.DB.Model(&database.EmailVerification{}).Where("purpose = ?", database.EmailVerificationPurposeResetPassword).Count(&n)
	if n != 0 {
		t.Errorf("不应留下 token 行 — got %d", n)
	}
}

func TestForgotPassword_UnverifiedEmail_SilentNoOp(t *testing.T) {
	utils.InitCrypto()
	setupResetPwdTestDB(t)
	user := seedResetUser(t, resetUserOpts{
		username: "u", password: "oldpass1", email: "u@e.com", verified: false,
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

	app := newResetPwdTestApp()
	status, body := resetPwdDoJSON(t, app, "/forgot-password", map[string]string{"email": "u@e.com"})
	if status != 200 {
		t.Errorf("status %d", status)
	}
	if body["message_code"] != "SUCCESS_PASSWORD_RESET_EMAIL_SENT" {
		t.Errorf("枚举防御响应；got %v", body)
	}
	if len(captured) != 0 {
		t.Error("未验证邮箱不应发重置邮件")
	}
	if n := countResetTokens(t, user.ID); n != 0 {
		t.Errorf("不应留 token；got %d", n)
	}
}

func TestForgotPassword_NoPassword_OAuthUser_SilentNoOp(t *testing.T) {
	utils.InitCrypto()
	setupResetPwdTestDB(t)
	user := seedResetUser(t, resetUserOpts{
		username: "oauthie", password: "", email: "o@e.com", verified: true,
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

	app := newResetPwdTestApp()
	status, body := resetPwdDoJSON(t, app, "/forgot-password", map[string]string{"email": "o@e.com"})
	if status != 200 {
		t.Errorf("status %d", status)
	}
	if body["message_code"] != "SUCCESS_PASSWORD_RESET_EMAIL_SENT" {
		t.Errorf("枚举防御响应；got %v", body)
	}
	if len(captured) != 0 {
		t.Error("OAuth-only 用户（无 PasswordHash）不应走 reset 流程；应走 G-2.5 set-password")
	}
	if n := countResetTokens(t, user.ID); n != 0 {
		t.Errorf("不应留 token；got %d", n)
	}
}

func TestForgotPassword_MasterDisabled(t *testing.T) {
	utils.InitCrypto()
	setupResetPwdTestDB(t)
	proxy.SysConfigMutex.Lock()
	proxy.SysConfigCache["email_enabled"] = "false"
	proxy.SysConfigMutex.Unlock()

	app := newResetPwdTestApp()
	status, body := resetPwdDoJSON(t, app, "/forgot-password", map[string]string{"email": "a@e.com"})
	if status != 503 {
		t.Errorf("status %d", status)
	}
	if body["message_code"] != "ERR_EMAIL_FEATURE_DISABLED" {
		t.Errorf("msg = %v", body)
	}
}

func TestForgotPassword_InvalidEmail(t *testing.T) {
	utils.InitCrypto()
	setupResetPwdTestDB(t)
	app := newResetPwdTestApp()
	status, body := resetPwdDoJSON(t, app, "/forgot-password", map[string]string{"email": "not-an-email"})
	if status != 400 {
		t.Errorf("status %d", status)
	}
	if body["message_code"] != "ERR_EMAIL_INVALID_FORMAT" {
		t.Errorf("msg = %v", body)
	}
}

func TestForgotPassword_InvalidatesPriorTokens(t *testing.T) {
	utils.InitCrypto()
	setupResetPwdTestDB(t)
	user := seedResetUser(t, resetUserOpts{
		username: "alice", password: "oldpass1", email: "alice@example.com", verified: true,
	})
	proxy.SetEmailQueueSyncForTest(true)
	proxy.SetSendEmailViaSMTPHookForTest(func(cfg proxy.SMTPConfig, msg proxy.EmailMessage) error { return nil })
	defer func() {
		proxy.SetEmailQueueSyncForTest(false)
		proxy.SetSendEmailViaSMTPHookForTest(nil)
	}()

	app := newResetPwdTestApp()
	// 第一次：发 token1
	if status, _ := resetPwdDoJSON(t, app, "/forgot-password", map[string]string{"email": "alice@example.com"}); status != 200 {
		t.Fatalf("first call status %d", status)
	}
	// 再次申请：发 token2，token1 应被作废（consumed_at != nil）
	// 等 dedup TTL —— 但 SendEmailDeduped 的 dedup 不阻塞 DB 写入，所以 token2 行仍会建
	// 但邮件可能因 dedup 不发。这里只验 DB 状态。
	if status, _ := resetPwdDoJSON(t, app, "/forgot-password", map[string]string{"email": "alice@example.com"}); status != 200 {
		t.Fatalf("second call status %d", status)
	}

	var rows []database.EmailVerification
	if err := database.DB.Where("user_id = ? AND purpose = ?", user.ID, database.EmailVerificationPurposeResetPassword).
		Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 token rows, got %d", len(rows))
	}
	if rows[0].ConsumedAt == nil {
		t.Error("prior token should have been invalidated (consumed_at != nil)")
	}
	if rows[1].ConsumedAt != nil {
		t.Error("new token should be active (consumed_at == nil)")
	}
}

// =============================================================================
// ResetPassword
// =============================================================================

func TestResetPassword_Success(t *testing.T) {
	utils.InitCrypto()
	setupResetPwdTestDB(t)
	user := seedResetUser(t, resetUserOpts{
		username: "alice", password: "oldpass1", email: "alice@example.com", verified: true,
	})
	// 直接 seed 一个 reset token
	rawToken, tokenHash, err := generateEmailToken()
	if err != nil {
		t.Fatalf("token gen: %v", err)
	}
	now := time.Now()
	v := database.EmailVerification{
		UserID:    user.ID,
		Email:     user.Email,
		TokenHash: tokenHash,
		Purpose:   database.EmailVerificationPurposeResetPassword,
		ExpiresAt: now.Add(15 * time.Minute),
		CreatedAt: now,
	}
	if err := database.DB.Create(&v).Error; err != nil {
		t.Fatalf("seed token: %v", err)
	}

	app := newResetPwdTestApp()
	status, body := resetPwdDoJSON(t, app, "/reset-password", map[string]string{
		"token":        rawToken,
		"new_password": "newpass1",
	})
	if status != 200 {
		t.Fatalf("status %d body=%v", status, body)
	}
	if body["message_code"] != "SUCCESS_PASSWORD_RESET" {
		t.Errorf("msg = %v", body)
	}

	// 新密码应能匹配，旧密码失效
	var u database.User
	database.DB.First(&u, user.ID)
	if !utils.CheckHash("newpass1", u.PasswordHash) {
		t.Error("new password should match new hash")
	}
	if utils.CheckHash("oldpass1", u.PasswordHash) {
		t.Error("old password must no longer match")
	}
	// token 已消费
	var consumed database.EmailVerification
	database.DB.First(&consumed, v.ID)
	if consumed.ConsumedAt == nil {
		t.Error("token should be marked consumed")
	}
}

func TestResetPassword_TokenInvalid(t *testing.T) {
	utils.InitCrypto()
	setupResetPwdTestDB(t)
	app := newResetPwdTestApp()
	status, body := resetPwdDoJSON(t, app, "/reset-password", map[string]string{
		"token":        "nonexistent-token",
		"new_password": "newpass1",
	})
	if status != 400 {
		t.Errorf("status %d", status)
	}
	if body["message_code"] != "ERR_EMAIL_TOKEN_INVALID" {
		t.Errorf("msg = %v", body)
	}
}

func TestResetPassword_TokenConsumed(t *testing.T) {
	utils.InitCrypto()
	setupResetPwdTestDB(t)
	user := seedResetUser(t, resetUserOpts{
		username: "alice", password: "oldpass1", email: "a@e.com", verified: true,
	})
	rawToken, hash, _ := generateEmailToken()
	now := time.Now()
	consumed := now.Add(-1 * time.Minute)
	v := database.EmailVerification{
		UserID: user.ID, Email: user.Email, TokenHash: hash,
		Purpose: database.EmailVerificationPurposeResetPassword,
		ExpiresAt: now.Add(15 * time.Minute), CreatedAt: now, ConsumedAt: &consumed,
	}
	if err := database.DB.Create(&v).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	app := newResetPwdTestApp()
	status, body := resetPwdDoJSON(t, app, "/reset-password", map[string]string{
		"token": rawToken, "new_password": "newpass1",
	})
	if status != 409 {
		t.Errorf("status %d", status)
	}
	if body["message_code"] != "ERR_EMAIL_TOKEN_CONSUMED" {
		t.Errorf("msg = %v", body)
	}
}

func TestResetPassword_TokenExpired(t *testing.T) {
	utils.InitCrypto()
	setupResetPwdTestDB(t)
	user := seedResetUser(t, resetUserOpts{
		username: "alice", password: "oldpass1", email: "a@e.com", verified: true,
	})
	rawToken, hash, _ := generateEmailToken()
	now := time.Now()
	v := database.EmailVerification{
		UserID: user.ID, Email: user.Email, TokenHash: hash,
		Purpose: database.EmailVerificationPurposeResetPassword,
		ExpiresAt: now.Add(-1 * time.Minute), CreatedAt: now,
	}
	if err := database.DB.Create(&v).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	app := newResetPwdTestApp()
	status, body := resetPwdDoJSON(t, app, "/reset-password", map[string]string{
		"token": rawToken, "new_password": "newpass1",
	})
	if status != 410 {
		t.Errorf("status %d", status)
	}
	if body["message_code"] != "ERR_EMAIL_TOKEN_EXPIRED" {
		t.Errorf("msg = %v", body)
	}
}

func TestResetPassword_WrongPurpose_VerifyTokenRejected(t *testing.T) {
	utils.InitCrypto()
	setupResetPwdTestDB(t)
	user := seedResetUser(t, resetUserOpts{
		username: "alice", password: "oldpass1", email: "a@e.com", verified: true,
	})
	// seed verify token（Purpose=verify），尝试用它走 reset 路径必须被拒
	rawToken, hash, _ := generateEmailToken()
	now := time.Now()
	v := database.EmailVerification{
		UserID: user.ID, Email: user.Email, TokenHash: hash,
		Purpose:   database.EmailVerificationPurposeVerify,
		ExpiresAt: now.Add(15 * time.Minute), CreatedAt: now,
	}
	if err := database.DB.Create(&v).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	app := newResetPwdTestApp()
	status, body := resetPwdDoJSON(t, app, "/reset-password", map[string]string{
		"token": rawToken, "new_password": "newpass1",
	})
	if status != 400 {
		t.Errorf("status %d (should reject verify-purpose token)", status)
	}
	if body["message_code"] != "ERR_EMAIL_TOKEN_INVALID" {
		t.Errorf("msg = %v", body)
	}
	// 旧密码应仍生效（reset 没发生）
	var u database.User
	database.DB.First(&u, user.ID)
	if !utils.CheckHash("oldpass1", u.PasswordHash) {
		t.Error("old password must still match — reset must NOT have happened")
	}
}

func TestResetPassword_WeakPassword(t *testing.T) {
	utils.InitCrypto()
	setupResetPwdTestDB(t)
	user := seedResetUser(t, resetUserOpts{
		username: "alice", password: "oldpass1", email: "a@e.com", verified: true,
	})
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

	app := newResetPwdTestApp()
	status, body := resetPwdDoJSON(t, app, "/reset-password", map[string]string{
		"token": rawToken, "new_password": "short",
	})
	if status != 400 {
		t.Errorf("status %d", status)
	}
	if body["message_code"] != "ERR_PASSWORD_TOO_SHORT" {
		t.Errorf("msg = %v", body)
	}
	// token 未消费（强度校验失败不应消费 token）
	var still database.EmailVerification
	database.DB.First(&still, v.ID)
	if still.ConsumedAt != nil {
		t.Error("token should NOT be consumed when password is weak")
	}
}

func TestResetPassword_PasswordSameAsUsername(t *testing.T) {
	utils.InitCrypto()
	setupResetPwdTestDB(t)
	user := seedResetUser(t, resetUserOpts{
		username: "myusername", password: "oldpass1", email: "a@e.com", verified: true,
	})
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

	app := newResetPwdTestApp()
	status, body := resetPwdDoJSON(t, app, "/reset-password", map[string]string{
		"token": rawToken, "new_password": "myusername",
	})
	if status != 400 {
		t.Errorf("status %d", status)
	}
	if body["message_code"] != "ERR_PASSWORD_SAME_AS_USERNAME" {
		t.Errorf("msg = %v", body)
	}
}

func TestResetPassword_MasterDisabled(t *testing.T) {
	utils.InitCrypto()
	setupResetPwdTestDB(t)
	proxy.SysConfigMutex.Lock()
	proxy.SysConfigCache["email_enabled"] = "false"
	proxy.SysConfigMutex.Unlock()

	app := newResetPwdTestApp()
	status, body := resetPwdDoJSON(t, app, "/reset-password", map[string]string{
		"token": "anytoken", "new_password": "newpass1",
	})
	if status != 503 {
		t.Errorf("status %d", status)
	}
	if body["message_code"] != "ERR_EMAIL_FEATURE_DISABLED" {
		t.Errorf("msg = %v", body)
	}
}

func TestResetPassword_ReplayRejected(t *testing.T) {
	utils.InitCrypto()
	setupResetPwdTestDB(t)
	user := seedResetUser(t, resetUserOpts{
		username: "alice", password: "oldpass1", email: "a@e.com", verified: true,
	})
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

	app := newResetPwdTestApp()
	// 第一次：成功
	status, _ := resetPwdDoJSON(t, app, "/reset-password", map[string]string{
		"token": rawToken, "new_password": "newpass1",
	})
	if status != 200 {
		t.Fatalf("first reset status %d", status)
	}
	// 第二次（replay）：必须拒绝
	status, body := resetPwdDoJSON(t, app, "/reset-password", map[string]string{
		"token": rawToken, "new_password": "anotherpass1",
	})
	if status != 409 {
		t.Errorf("replay status %d", status)
	}
	if body["message_code"] != "ERR_EMAIL_TOKEN_CONSUMED" {
		t.Errorf("replay msg = %v", body)
	}
}
