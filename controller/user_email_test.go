// Package controller / user_email_test.go
//
// Phase G-1.5 单元测试：
//   1) 纯 helper：normalizeEmail / hashEmailToken / generateEmailToken /
//      loadEmailVerifyTTL / buildEmailVerifyURL / ttlDisplay / truncateUserAgent
//   2) 集成（in-memory SQLite）：BindEmail / VerifyEmail / UnbindEmail / GetMyEmailStatus 主流程
//
// 不覆盖：真实 SMTP 拨号（队列层在没配置 SMTP 时静默跳过；G-1.9 mock SMTP 集成测）。
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

// ── 纯 helper 测试 ──

func TestNormalizeEmail(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"", "", false},
		{"  ", "", false},
		{"alice@example.com", "alice@example.com", true},
		{"  Alice@Example.COM  ", "alice@example.com", true},
		{"user+tag@example.com", "user+tag@example.com", true},
		{"not-an-email", "", false},
		{"@example.com", "", false},
		{"user@", "", false},
		{"a a@example.com", "", false},
		{"Display Name <user@example.com>", "", false}, // 带 display name 拒绝
		{strings.Repeat("a", 250) + "@example.com", "", false},  // 超 254 字符（local 250 + @example.com = 262）
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := normalizeEmail(tc.in)
			if ok != tc.ok {
				t.Errorf("normalizeEmail(%q) ok = %v; want %v", tc.in, ok, tc.ok)
			}
			if ok && got != tc.want {
				t.Errorf("normalizeEmail(%q) = %q; want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestHashEmailToken_Deterministic(t *testing.T) {
	a := hashEmailToken("hello")
	b := hashEmailToken("hello")
	if a != b {
		t.Errorf("same input should produce same hash, got %q vs %q", a, b)
	}
	if len(a) != 64 {
		t.Errorf("hash should be 64 hex chars, got %d", len(a))
	}
	c := hashEmailToken("hello!")
	if a == c {
		t.Errorf("different inputs should produce different hashes")
	}
}

func TestGenerateEmailToken_Random(t *testing.T) {
	r1, h1, err := generateEmailToken()
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	r2, h2, err := generateEmailToken()
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if r1 == r2 {
		t.Error("two random tokens should differ")
	}
	if h1 == h2 {
		t.Error("two random hashes should differ")
	}
	if hashEmailToken(r1) != h1 {
		t.Error("returned hash should equal hashEmailToken(raw)")
	}
	// base64url no padding: 32 bytes → 43 chars
	if len(r1) != 43 {
		t.Errorf("raw token expected 43 chars, got %d", len(r1))
	}
}

func TestLoadEmailVerifyTTL(t *testing.T) {
	proxy.SysConfigMutex.Lock()
	prev := proxy.SysConfigCache
	proxy.SysConfigCache = map[string]string{}
	proxy.SysConfigMutex.Unlock()
	defer func() {
		proxy.SysConfigMutex.Lock()
		proxy.SysConfigCache = prev
		proxy.SysConfigMutex.Unlock()
	}()

	tests := []struct {
		name string
		val  string
		want time.Duration
	}{
		{"missing → default 1h", "", time.Hour},
		{"valid 600s", "600", 10 * time.Minute},
		{"non-int → default", "abc", time.Hour},
		{"zero → default", "0", time.Hour},
		{"negative → default", "-1", time.Hour},
		{"over 24h → default", "100000", time.Hour},
		{"exactly 24h → accepted", "86400", 24 * time.Hour},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			proxy.SysConfigMutex.Lock()
			proxy.SysConfigCache["email_verify_ttl_seconds"] = tc.val
			proxy.SysConfigMutex.Unlock()
			got := loadEmailVerifyTTL()
			if got != tc.want {
				t.Errorf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestBuildEmailVerifyURL(t *testing.T) {
	proxy.SysConfigMutex.Lock()
	prev := proxy.SysConfigCache
	proxy.SysConfigCache = map[string]string{}
	proxy.SysConfigMutex.Unlock()
	defer func() {
		proxy.SysConfigMutex.Lock()
		proxy.SysConfigCache = prev
		proxy.SysConfigMutex.Unlock()
	}()

	t.Run("missing server_address rejected", func(t *testing.T) {
		_, err := buildEmailVerifyURL("token123")
		if err == nil {
			t.Error("expected error when server_address missing")
		}
	})

	t.Run("non-https rejected by default", func(t *testing.T) {
		proxy.SysConfigMutex.Lock()
		proxy.SysConfigCache["server_address"] = "http://example.com"
		proxy.SysConfigMutex.Unlock()
		_, err := buildEmailVerifyURL("token123")
		if err == nil {
			t.Error("expected error for http://")
		}
	})

	t.Run("https path assembled", func(t *testing.T) {
		proxy.SysConfigMutex.Lock()
		proxy.SysConfigCache["server_address"] = "https://app.example.com"
		proxy.SysConfigCache["email_verify_url_path"] = "/verify-email"
		proxy.SysConfigMutex.Unlock()
		url, err := buildEmailVerifyURL("abc-xyz_123")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		want := "https://app.example.com/verify-email?token=abc-xyz_123"
		if url != want {
			t.Errorf("got %q want %q", url, want)
		}
	})

	t.Run("trailing slash trimmed + path normalized", func(t *testing.T) {
		proxy.SysConfigMutex.Lock()
		proxy.SysConfigCache["server_address"] = "https://app.example.com/"
		proxy.SysConfigCache["email_verify_url_path"] = "verify-email" // 无前导 /
		proxy.SysConfigMutex.Unlock()
		url, err := buildEmailVerifyURL("tok")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		want := "https://app.example.com/verify-email?token=tok"
		if url != want {
			t.Errorf("got %q want %q", url, want)
		}
	})
}

func TestTTLDisplay(t *testing.T) {
	tests := []struct {
		ttl    time.Duration
		locale string
		want   string
	}{
		{time.Hour, "zh", "1 小时"},
		{2 * time.Hour, "zh", "2 小时"},
		{15 * time.Minute, "zh", "15 分钟"},
		{90 * time.Minute, "zh", "90 分钟"},
		{time.Hour, "en", "1 hour"},
		{2 * time.Hour, "en", "2 hours"},
		{time.Minute, "en", "1 minute"},
		{15 * time.Minute, "en", "15 minutes"},
		{time.Hour, "", "1 小时"}, // default zh
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			got := ttlDisplay(tc.ttl, tc.locale)
			if got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestTruncateUserAgent(t *testing.T) {
	short := "Mozilla/5.0"
	if got := truncateUserAgent(short); got != short {
		t.Errorf("short UA changed: %q", got)
	}
	long := strings.Repeat("x", 300)
	got := truncateUserAgent(long)
	if len(got) != 255 {
		t.Errorf("long UA should be truncated to 255, got %d", len(got))
	}
}

// ── 集成测试：bind/verify/unbind 主流程 ──

func setupEmailControllerTestDB(t *testing.T) (*gorm.DB, *database.User) {
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
		&database.User{}, &database.EmailVerification{}, &database.SysConfig{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	database.DB = db

	// 邮箱功能 master switch 打开 + server_address 配置
	proxy.SysConfigMutex.Lock()
	proxy.SysConfigCache = map[string]string{
		"email_enabled":               "true",
		"server_address":              "https://app.example.com",
		"server_address_require_https": "true",
		"email_verify_url_path":       "/verify-email",
		"email_verify_ttl_seconds":    "3600",
		"site_name":                   "DAOF-CPA-Test",
	}
	proxy.SysConfigMutex.Unlock()

	user := database.User{
		Username:     "alice",
		Token:        "sk-test-email-controller",
		PasswordHash: "x",
		Status:       1,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return db, &user
}

func newEmailTestApp(user *database.User) *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("user", user)
		return c.Next()
	})
	app.Get("/email", GetMyEmailStatus)
	app.Post("/email/bind", BindEmail)
	app.Post("/email/verify", VerifyEmail)
	app.Post("/email/resend", ResendVerificationEmail)
	app.Delete("/email", UnbindEmail)
	return app
}

func emailDoJSON(t *testing.T, app *fiber.App, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	_ = json.Unmarshal(bodyBytes, &out)
	return resp.StatusCode, out
}

func TestBindEmail_Success(t *testing.T) {
	utils.InitCrypto()
	_, user := setupEmailControllerTestDB(t)
	proxy.SetRecordApiLogRevenueSyncForTest(true)

	app := newEmailTestApp(user)
	status, body := emailDoJSON(t, app, "POST", "/email/bind", map[string]string{"email": "alice@example.com"})
	if status != 200 {
		t.Fatalf("status %d body=%v", status, body)
	}
	if body["message_code"] != "SUCCESS_EMAIL_BIND_SENT" {
		t.Errorf("unexpected msg: %v", body)
	}
	// DB 里应该有一条 EmailVerification
	var count int64
	database.DB.Model(&database.EmailVerification{}).
		Where("user_id = ? AND purpose = ?", user.ID, database.EmailVerificationPurposeVerify).
		Count(&count)
	if count != 1 {
		t.Errorf("expected 1 EmailVerification row, got %d", count)
	}
}

func TestBindEmail_FeatureDisabled(t *testing.T) {
	utils.InitCrypto()
	_, user := setupEmailControllerTestDB(t)
	proxy.SysConfigMutex.Lock()
	proxy.SysConfigCache["email_enabled"] = "false"
	proxy.SysConfigMutex.Unlock()

	app := newEmailTestApp(user)
	status, body := emailDoJSON(t, app, "POST", "/email/bind", map[string]string{"email": "u@e.com"})
	if status != 503 {
		t.Errorf("status %d body=%v", status, body)
	}
	if body["message_code"] != "ERR_EMAIL_FEATURE_DISABLED" {
		t.Errorf("unexpected msg: %v", body)
	}
}

func TestBindEmail_InvalidFormat(t *testing.T) {
	utils.InitCrypto()
	_, user := setupEmailControllerTestDB(t)

	app := newEmailTestApp(user)
	status, body := emailDoJSON(t, app, "POST", "/email/bind", map[string]string{"email": "not-an-email"})
	if status != 400 {
		t.Errorf("status %d", status)
	}
	if body["message_code"] != "ERR_EMAIL_INVALID_FORMAT" {
		t.Errorf("unexpected msg: %v", body)
	}
}

func TestBindEmail_TakenByOther(t *testing.T) {
	utils.InitCrypto()
	_, user := setupEmailControllerTestDB(t)

	// 创建另一个已绑邮箱的用户
	other := database.User{
		Username:     "bob",
		Token:        "sk-bob",
		PasswordHash: "x",
		Status:       1,
		Email:        "taken@example.com",
	}
	if err := database.DB.Create(&other).Error; err != nil {
		t.Fatalf("seed other: %v", err)
	}

	app := newEmailTestApp(user)
	status, body := emailDoJSON(t, app, "POST", "/email/bind", map[string]string{"email": "taken@example.com"})
	if status != 409 {
		t.Errorf("status %d", status)
	}
	if body["message_code"] != "ERR_EMAIL_TAKEN" {
		t.Errorf("unexpected msg: %v", body)
	}
}

func TestBindEmail_AlreadyBoundDifferent(t *testing.T) {
	utils.InitCrypto()
	_, user := setupEmailControllerTestDB(t)
	user.Email = "old@example.com"
	now := time.Now()
	user.EmailVerifiedAt = &now
	database.DB.Save(user)

	app := newEmailTestApp(user)
	status, body := emailDoJSON(t, app, "POST", "/email/bind", map[string]string{"email": "new@example.com"})
	if status != 409 {
		t.Errorf("status %d", status)
	}
	if body["message_code"] != "ERR_EMAIL_BIND_BLOCKED" {
		t.Errorf("unexpected msg: %v", body)
	}
}

func TestBindEmail_AlreadyVerifiedSameEmail(t *testing.T) {
	utils.InitCrypto()
	_, user := setupEmailControllerTestDB(t)
	user.Email = "alice@example.com"
	now := time.Now()
	user.EmailVerifiedAt = &now
	database.DB.Save(user)

	app := newEmailTestApp(user)
	status, body := emailDoJSON(t, app, "POST", "/email/bind", map[string]string{"email": "alice@example.com"})
	if status != 200 {
		t.Errorf("status %d", status)
	}
	if body["message_code"] != "SUCCESS_EMAIL_ALREADY_VERIFIED" {
		t.Errorf("unexpected msg: %v", body)
	}
}

func TestVerifyEmail_HappyPath(t *testing.T) {
	utils.InitCrypto()
	_, user := setupEmailControllerTestDB(t)

	// 直接 INSERT 一个有效 token（不走 BindEmail，因为它会触发 SMTP enqueue）
	rawToken, hash, err := generateEmailToken()
	if err != nil {
		t.Fatalf("gen token: %v", err)
	}
	if err := database.DB.Create(&database.EmailVerification{
		UserID:    user.ID,
		Email:     "alice@example.com",
		TokenHash: hash,
		Purpose:   database.EmailVerificationPurposeVerify,
		ExpiresAt: time.Now().Add(time.Hour),
		CreatedAt: time.Now(),
	}).Error; err != nil {
		t.Fatalf("seed verification: %v", err)
	}

	app := newEmailTestApp(user)
	status, body := emailDoJSON(t, app, "POST", "/email/verify", map[string]string{"token": rawToken})
	if status != 200 {
		t.Fatalf("status %d body=%v", status, body)
	}
	if body["message_code"] != "SUCCESS_EMAIL_VERIFIED" {
		t.Errorf("unexpected msg: %v", body)
	}
	// User 表应该被更新
	var fresh database.User
	if err := database.DB.First(&fresh, user.ID).Error; err != nil {
		t.Fatalf("read user: %v", err)
	}
	if fresh.Email != "alice@example.com" {
		t.Errorf("Email not set: %q", fresh.Email)
	}
	if fresh.EmailVerifiedAt == nil {
		t.Error("EmailVerifiedAt not set")
	}
}

func TestVerifyEmail_TokenInvalid(t *testing.T) {
	utils.InitCrypto()
	_, user := setupEmailControllerTestDB(t)
	app := newEmailTestApp(user)
	status, body := emailDoJSON(t, app, "POST", "/email/verify", map[string]string{"token": "definitely-not-a-real-token"})
	if status != 400 {
		t.Errorf("status %d", status)
	}
	if body["message_code"] != "ERR_EMAIL_TOKEN_INVALID" {
		t.Errorf("unexpected msg: %v", body)
	}
}

func TestVerifyEmail_TokenExpired(t *testing.T) {
	utils.InitCrypto()
	_, user := setupEmailControllerTestDB(t)
	rawToken, hash, _ := generateEmailToken()
	if err := database.DB.Create(&database.EmailVerification{
		UserID:    user.ID,
		Email:     "alice@example.com",
		TokenHash: hash,
		Purpose:   database.EmailVerificationPurposeVerify,
		ExpiresAt: time.Now().Add(-time.Hour), // 已过期
		CreatedAt: time.Now().Add(-2 * time.Hour),
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	app := newEmailTestApp(user)
	status, body := emailDoJSON(t, app, "POST", "/email/verify", map[string]string{"token": rawToken})
	if status != 410 {
		t.Errorf("status %d body=%v", status, body)
	}
	if body["message_code"] != "ERR_EMAIL_TOKEN_EXPIRED" {
		t.Errorf("unexpected msg: %v", body)
	}
}

func TestVerifyEmail_TokenAlreadyConsumed(t *testing.T) {
	utils.InitCrypto()
	_, user := setupEmailControllerTestDB(t)
	rawToken, hash, _ := generateEmailToken()
	now := time.Now()
	if err := database.DB.Create(&database.EmailVerification{
		UserID:     user.ID,
		Email:      "alice@example.com",
		TokenHash:  hash,
		Purpose:    database.EmailVerificationPurposeVerify,
		ExpiresAt:  now.Add(time.Hour),
		ConsumedAt: &now, // 已消费
		CreatedAt:  now,
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	app := newEmailTestApp(user)
	status, body := emailDoJSON(t, app, "POST", "/email/verify", map[string]string{"token": rawToken})
	if status != 409 {
		t.Errorf("status %d", status)
	}
	if body["message_code"] != "ERR_EMAIL_TOKEN_CONSUMED" {
		t.Errorf("unexpected msg: %v", body)
	}
}

func TestVerifyEmail_TokenBelongsToOtherUser(t *testing.T) {
	utils.InitCrypto()
	_, user := setupEmailControllerTestDB(t)
	other := database.User{Username: "bob", Token: "sk-bob", PasswordHash: "x", Status: 1}
	database.DB.Create(&other)

	rawToken, hash, _ := generateEmailToken()
	if err := database.DB.Create(&database.EmailVerification{
		UserID:    other.ID, // 注意：token 属于 bob，不是 alice
		Email:     "bob@example.com",
		TokenHash: hash,
		Purpose:   database.EmailVerificationPurposeVerify,
		ExpiresAt: time.Now().Add(time.Hour),
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	app := newEmailTestApp(user) // alice 登录
	status, body := emailDoJSON(t, app, "POST", "/email/verify", map[string]string{"token": rawToken})
	if status != 403 {
		t.Errorf("status %d body=%v want 403 (token belongs to other user)", status, body)
	}
}

func TestUnbindEmail(t *testing.T) {
	utils.InitCrypto()
	_, user := setupEmailControllerTestDB(t)
	user.Email = "alice@example.com"
	now := time.Now()
	user.EmailVerifiedAt = &now
	user.EmailLoginEnabled = true
	database.DB.Save(user)

	app := newEmailTestApp(user)
	status, body := emailDoJSON(t, app, "DELETE", "/email", nil)
	if status != 200 {
		t.Errorf("status %d body=%v", status, body)
	}
	if body["message_code"] != "SUCCESS_EMAIL_UNBOUND" {
		t.Errorf("unexpected msg: %v", body)
	}
	var fresh database.User
	database.DB.First(&fresh, user.ID)
	if fresh.Email != "" {
		t.Errorf("Email not cleared: %q", fresh.Email)
	}
	if fresh.EmailVerifiedAt != nil {
		t.Error("EmailVerifiedAt not cleared")
	}
	if fresh.EmailLoginEnabled {
		t.Error("EmailLoginEnabled not cleared")
	}
}

func TestUnbindEmail_NoBoundEmail(t *testing.T) {
	utils.InitCrypto()
	_, user := setupEmailControllerTestDB(t)
	app := newEmailTestApp(user)
	status, body := emailDoJSON(t, app, "DELETE", "/email", nil)
	if status != 400 {
		t.Errorf("status %d", status)
	}
	if body["message_code"] != "ERR_EMAIL_NOT_BOUND" {
		t.Errorf("unexpected msg: %v", body)
	}
}

func TestGetMyEmailStatus(t *testing.T) {
	utils.InitCrypto()
	_, user := setupEmailControllerTestDB(t)
	user.Email = "alice@example.com"
	now := time.Now()
	user.EmailVerifiedAt = &now
	database.DB.Save(user)

	app := newEmailTestApp(user)
	status, body := emailDoJSON(t, app, "GET", "/email", nil)
	if status != 200 {
		t.Fatalf("status %d", status)
	}
	data, _ := body["data"].(map[string]any)
	if data["email"] != "alice@example.com" {
		t.Errorf("email field wrong: %v", data["email"])
	}
	if data["email_verified_at"] == nil {
		t.Error("email_verified_at should be set")
	}
	if data["feature_enabled"] != true {
		t.Error("feature_enabled should be true")
	}
}

func TestBindThenVerify_FullFlow(t *testing.T) {
	utils.InitCrypto()
	_, user := setupEmailControllerTestDB(t)
	app := newEmailTestApp(user)

	// 1. bind
	status, _ := emailDoJSON(t, app, "POST", "/email/bind", map[string]string{"email": "flow@example.com"})
	if status != 200 {
		t.Fatalf("bind status %d", status)
	}

	// 2. 拿到 DB 里的 verification 行，重新生成等效 token（测试无法拿到原始 token）
	// 改为直接查 token_hash 然后通过反向构造 token：这不可能（hash 单向）。
	// 正确测法：在 BindEmail 之后人为再 INSERT 一个已知 token，绕过原始 token 走完测试。
	// 这里改测 "bind + 直接验证 DB 里有 pending row" + 单独验证 verify 流程
	var pending database.EmailVerification
	if err := database.DB.Where("user_id = ? AND consumed_at IS NULL", user.ID).First(&pending).Error; err != nil {
		t.Fatalf("no pending verification: %v", err)
	}
	if pending.Email != "flow@example.com" {
		t.Errorf("pending email wrong: %q", pending.Email)
	}
	if pending.Purpose != database.EmailVerificationPurposeVerify {
		t.Errorf("pending purpose wrong: %q", pending.Purpose)
	}

	// 3. 再 bind 一次：应作废旧 token，创建新 token（旧的 ConsumedAt 应被填）
	status, _ = emailDoJSON(t, app, "POST", "/email/bind", map[string]string{"email": "flow@example.com"})
	if status != 200 {
		t.Fatalf("second bind status %d", status)
	}
	var oldRow database.EmailVerification
	database.DB.First(&oldRow, pending.ID)
	if oldRow.ConsumedAt == nil {
		t.Error("old pending token should be invalidated (ConsumedAt set)")
	}
}
