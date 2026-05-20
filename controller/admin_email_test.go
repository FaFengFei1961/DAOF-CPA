// Package controller / admin_email_test.go
//
// Phase G-1.6 单元测试：Admin SMTP 配置 API。
//   - GET /api/admin/email/config 返回结构（password 脱敏）
//   - PUT /api/admin/email/config 增量更新 + 校验
//   - POST /api/admin/email/test-send（仅测输入校验 + SMTP 未配置时拒绝；
//     真实 SMTP 拨号在 G-1.9 mock SMTP 集成测）
package controller

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"daof-cpa/database"
	"daof-cpa/proxy"
	"daof-cpa/utils"

	"github.com/gofiber/fiber/v2"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// setupAdminEmailTestDB 准备一个 admin user + in-memory DB + 空 SysConfig，
// 给 admin_email.go 的 handler 共用。
func setupAdminEmailTestDB(t *testing.T) *database.User {
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
		&database.User{}, &database.SysConfig{}, &database.OperationLog{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	database.DB = db

	proxy.SysConfigMutex.Lock()
	proxy.SysConfigCache = map[string]string{}
	proxy.SysConfigMutex.Unlock()

	admin := database.User{
		Username:     "admin-tester",
		Role:         "admin",
		Token:        "sk-admin-email-cfg",
		PasswordHash: "x",
		Status:       1,
	}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	return &admin
}

func newAdminEmailTestApp(admin *database.User) *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("admin_user", admin)
		return c.Next()
	})
	app.Get("/email/config", GetAdminEmailConfig)
	app.Put("/email/config", UpdateAdminEmailConfig)
	app.Post("/email/test-send", SendAdminEmailTest)
	return app
}

func adminEmailDoJSON(t *testing.T, app *fiber.App, method, path string, body any) (int, map[string]any) {
	return adminEmailDoJSONWithToken(t, app, method, path, body, "sk-admin-email-cfg")
}

func adminEmailDoJSONWithToken(t *testing.T, app *fiber.App, method, path string, body any, token string) (int, map[string]any) {
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
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
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

// ── 纯函数测试 ──

func TestFormatBoolConfig(t *testing.T) {
	if got := formatBoolConfig(true); got != "true" {
		t.Errorf("true → %q want 'true'", got)
	}
	if got := formatBoolConfig(false); got != "false" {
		t.Errorf("false → %q want 'false'", got)
	}
}

func TestMaskEmailForAdmin(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"", ""},
		{"   ", ""},
		{"noatsymbol", "***"},
		{"@example.com", "*@example.com"},
		{"a@example.com", "a***@example.com"},
		{"alice@example.com", "a***@example.com"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			if got := maskEmailForAdmin(tc.in); got != tc.want {
				t.Errorf("maskEmailForAdmin(%q) = %q want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSanitizeAdminTestField(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"plain", "plain"},
		{"<script>", "&lt;script&gt;"},
		{`"quoted"`, "&quot;quoted&quot;"},
		{"a&b", "a&amp;b"},
		{"'single'", "&#39;single&#39;"},
		{"a<b>c&d\"e'f", "a&lt;b&gt;c&amp;d&quot;e&#39;f"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			if got := sanitizeAdminTestField(tc.in); got != tc.want {
				t.Errorf("sanitizeAdminTestField(%q) = %q want %q", tc.in, got, tc.want)
			}
		})
	}
}

// ── 集成测试：GET ──

func TestGetAdminEmailConfig_DefaultsAndMasking(t *testing.T) {
	utils.InitCrypto()
	admin := setupAdminEmailTestDB(t)
	app := newAdminEmailTestApp(admin)

	status, body := adminEmailDoJSON(t, app, "GET", "/email/config", nil)
	if status != 200 {
		t.Fatalf("status %d body=%v", status, body)
	}
	data, _ := body["data"].(map[string]any)
	// 默认状态：所有 toggle 都 false，has_password=false，is_ready=false
	if data["email_enabled"] != false {
		t.Errorf("email_enabled = %v want false", data["email_enabled"])
	}
	if data["email_signup_enabled"] != false {
		t.Errorf("email_signup_enabled = %v want false", data["email_signup_enabled"])
	}
	if data["email_login_enabled"] != false {
		t.Errorf("email_login_enabled = %v want false", data["email_login_enabled"])
	}
	if data["has_password"] != false {
		t.Errorf("has_password = %v want false", data["has_password"])
	}
	if data["is_ready"] != false {
		t.Errorf("is_ready = %v want false", data["is_ready"])
	}
	// 默认限流值
	if data["rate_limit_per_email_hourly"] != float64(5) {
		t.Errorf("default per-email = %v", data["rate_limit_per_email_hourly"])
	}
	if data["rate_limit_per_ip_hourly"] != float64(20) {
		t.Errorf("default per-ip = %v", data["rate_limit_per_ip_hourly"])
	}
	// password 字段不出现
	if _, ok := data["smtp_password"]; ok {
		t.Error("smtp_password field should NEVER appear in response")
	}
}

func TestGetAdminEmailConfig_HasPasswordWhenConfigured(t *testing.T) {
	utils.InitCrypto()
	admin := setupAdminEmailTestDB(t)
	app := newAdminEmailTestApp(admin)

	// 直接往 SysConfigCache 写一个非空 password
	proxy.SysConfigMutex.Lock()
	proxy.SysConfigCache["smtp_password"] = "encrypted-blob-here"
	proxy.SysConfigMutex.Unlock()

	_, body := adminEmailDoJSON(t, app, "GET", "/email/config", nil)
	data, _ := body["data"].(map[string]any)
	if data["has_password"] != true {
		t.Errorf("has_password = %v want true", data["has_password"])
	}
	if _, ok := data["smtp_password"]; ok {
		t.Error("smtp_password should still not appear")
	}
}

func TestGetAdminEmailConfig_IsReadyWhenComplete(t *testing.T) {
	utils.InitCrypto()
	admin := setupAdminEmailTestDB(t)
	app := newAdminEmailTestApp(admin)

	proxy.SysConfigMutex.Lock()
	proxy.SysConfigCache["email_enabled"] = "true"
	proxy.SysConfigCache["smtp_host"] = "smtp.example.com"
	proxy.SysConfigCache["smtp_port"] = "587"
	proxy.SysConfigCache["smtp_username"] = "u"
	proxy.SysConfigCache["smtp_password"] = "x"
	proxy.SysConfigCache["smtp_from"] = "f"
	proxy.SysConfigMutex.Unlock()

	_, body := adminEmailDoJSON(t, app, "GET", "/email/config", nil)
	data, _ := body["data"].(map[string]any)
	if data["is_ready"] != true {
		t.Errorf("is_ready = %v want true (all fields set)", data["is_ready"])
	}
}

// ── 集成测试：PUT ──

func TestUpdateAdminEmailConfig_PartialUpdate(t *testing.T) {
	utils.InitCrypto()
	admin := setupAdminEmailTestDB(t)
	app := newAdminEmailTestApp(admin)

	// 只更新 email_enabled = true
	enabled := true
	status, body := adminEmailDoJSON(t, app, "PUT", "/email/config", map[string]any{
		"email_enabled": enabled,
	})
	if status != 200 {
		t.Fatalf("status %d body=%v", status, body)
	}
	if body["message_code"] != "SUCCESS_CONFIG_SAVED" {
		t.Errorf("unexpected msg: %v", body)
	}
	// 验证 DB 里有 email_enabled 一行（SyncCacheConfig 在测试环境因缺 channels 表会报错，
	// 但 SysConfig 写入已发生 —— 直接查 DB 验证持久化）
	if !dbHasSysConfig(t, "email_enabled", "true") {
		t.Error("email_enabled row missing or value wrong in DB")
	}
	// smtp_host 不在 body 中 → 不应被写入
	var smtpHost database.SysConfig
	if err := database.DB.Where("key = ?", "smtp_host").First(&smtpHost).Error; err == nil {
		t.Errorf("smtp_host should not have been written (got key=%q value=%q)", smtpHost.Key, smtpHost.Value)
	}
}

// dbHasSysConfig 直接从 DB 查 + 解密验证某 key 是否等于 expected 明文值。
// 测试用 helper，避免依赖 SysConfigCache（测试环境的 SyncCacheConfig 会因缺表失败）。
func dbHasSysConfig(t *testing.T, key, expected string) bool {
	t.Helper()
	var row database.SysConfig
	if err := database.DB.Where("key = ?", key).First(&row).Error; err != nil {
		return false
	}
	dec, err := utils.Decrypt(row.Value)
	if err != nil {
		t.Logf("decrypt %s: %v", key, err)
		return false
	}
	return dec == expected
}

func TestUpdateAdminEmailConfig_PasswordOmittedKeepsExisting(t *testing.T) {
	utils.InitCrypto()
	admin := setupAdminEmailTestDB(t)
	app := newAdminEmailTestApp(admin)

	// 先用 BatchUpdate 设置初始 password（模拟历史已配置状态）
	encPwd, _ := utils.Encrypt("original-pwd")
	proxy.SysConfigMutex.Lock()
	proxy.SysConfigCache["smtp_password"] = encPwd
	proxy.SysConfigMutex.Unlock()
	// 同时落 DB（模拟真实状态）
	if err := database.DB.Create(&database.SysConfig{Key: "smtp_password", Value: encPwd}).Error; err != nil {
		t.Fatalf("seed pwd: %v", err)
	}

	// 改其他字段，不传 smtp_password
	host := "smtp.gmail.com"
	status, _ := adminEmailDoJSON(t, app, "PUT", "/email/config", map[string]any{
		"smtp_host": host,
	})
	if status != 200 {
		t.Fatalf("status %d", status)
	}
	// password 应该还在
	if readSysConfigCached("smtp_password", "") != encPwd {
		t.Error("password should be preserved when not in body")
	}
}

func TestUpdateAdminEmailConfig_PasswordExplicitEmptyClears(t *testing.T) {
	utils.InitCrypto()
	admin := setupAdminEmailTestDB(t)
	app := newAdminEmailTestApp(admin)

	// 显式传 smtp_password=""
	emptyPwd := ""
	status, _ := adminEmailDoJSON(t, app, "PUT", "/email/config", map[string]any{
		"smtp_password": emptyPwd,
	})
	if status != 200 {
		t.Fatalf("status %d", status)
	}
	// 加密空字符串 → 仍是空字符串（utils.Encrypt 对 "" 返回 ""）
	if v := readSysConfigCached("smtp_password", ""); v != "" {
		t.Errorf("password should be cleared, got %q", v)
	}
}

func TestUpdateAdminEmailConfig_Port25Rejected(t *testing.T) {
	utils.InitCrypto()
	admin := setupAdminEmailTestDB(t)
	app := newAdminEmailTestApp(admin)

	port := 25
	status, body := adminEmailDoJSON(t, app, "PUT", "/email/config", map[string]any{
		"smtp_port": port,
	})
	if status != 400 {
		t.Errorf("status %d body=%v", status, body)
	}
	if body["message_code"] != "ERR_SMTP_PORT_PLAINTEXT" {
		t.Errorf("unexpected msg: %v", body)
	}
}

func TestUpdateAdminEmailConfig_PortOutOfRange(t *testing.T) {
	utils.InitCrypto()
	admin := setupAdminEmailTestDB(t)
	app := newAdminEmailTestApp(admin)

	port := 70000
	status, body := adminEmailDoJSON(t, app, "PUT", "/email/config", map[string]any{
		"smtp_port": port,
	})
	if status != 400 {
		t.Errorf("status %d", status)
	}
	if body["message_code"] != "ERR_SMTP_PORT_INVALID" {
		t.Errorf("unexpected msg: %v", body)
	}
}

func TestUpdateAdminEmailConfig_HostInvalidChars(t *testing.T) {
	utils.InitCrypto()
	admin := setupAdminEmailTestDB(t)
	app := newAdminEmailTestApp(admin)

	host := "evil host\r\n"
	status, body := adminEmailDoJSON(t, app, "PUT", "/email/config", map[string]any{
		"smtp_host": host,
	})
	if status != 400 {
		t.Errorf("status %d", status)
	}
	if body["message_code"] != "ERR_SMTP_HOST_INVALID" {
		t.Errorf("unexpected msg: %v", body)
	}
}

func TestUpdateAdminEmailConfig_RateLimitBounds(t *testing.T) {
	utils.InitCrypto()
	admin := setupAdminEmailTestDB(t)
	app := newAdminEmailTestApp(admin)

	tests := []struct {
		name      string
		body      map[string]any
		wantCode  string
	}{
		{"per_email zero rejected", map[string]any{"rate_limit_per_email_hourly": 0}, "ERR_EMAIL_RATE_LIMIT_INVALID"},
		{"per_email > 1000 rejected", map[string]any{"rate_limit_per_email_hourly": 1001}, "ERR_EMAIL_RATE_LIMIT_INVALID"},
		{"per_ip zero rejected", map[string]any{"rate_limit_per_ip_hourly": 0}, "ERR_EMAIL_RATE_LIMIT_INVALID"},
		{"per_ip > 10000 rejected", map[string]any{"rate_limit_per_ip_hourly": 10001}, "ERR_EMAIL_RATE_LIMIT_INVALID"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status, body := adminEmailDoJSON(t, app, "PUT", "/email/config", tc.body)
			if status != 400 {
				t.Errorf("status %d", status)
			}
			if body["message_code"] != tc.wantCode {
				t.Errorf("code = %v want %s", body["message_code"], tc.wantCode)
			}
		})
	}
}

func TestUpdateAdminEmailConfig_TTLBounds(t *testing.T) {
	utils.InitCrypto()
	admin := setupAdminEmailTestDB(t)
	app := newAdminEmailTestApp(admin)

	tests := []struct {
		name string
		body map[string]any
	}{
		{"verify_ttl_seconds < 60", map[string]any{"verify_ttl_seconds": 30}},
		{"verify_ttl_seconds > 86400", map[string]any{"verify_ttl_seconds": 100000}},
		{"reset_ttl_seconds < 60", map[string]any{"reset_ttl_seconds": 10}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status, body := adminEmailDoJSON(t, app, "PUT", "/email/config", tc.body)
			if status != 400 {
				t.Errorf("status %d", status)
			}
			if body["message_code"] != "ERR_EMAIL_TTL_INVALID" {
				t.Errorf("code = %v", body["message_code"])
			}
		})
	}
}

func TestUpdateAdminEmailConfig_AllValid(t *testing.T) {
	utils.InitCrypto()
	admin := setupAdminEmailTestDB(t)
	app := newAdminEmailTestApp(admin)

	body := map[string]any{
		"email_enabled":          true,
		"smtp_host":              "smtp.gmail.com",
		"smtp_port":              587,
		"smtp_username":          "noreply@example.com",
		"smtp_password":          "pwd123",
		"smtp_from":              "DAOF <noreply@example.com>",
		"smtp_use_implicit_tls":  false,
		"smtp_reply_to":          "support@example.com",
		"rate_limit_per_email_hourly": 10,
		"rate_limit_per_ip_hourly":    50,
		"verify_ttl_seconds":          7200,
		"reset_ttl_seconds":           1800,
	}
	status, respBody := adminEmailDoJSON(t, app, "PUT", "/email/config", body)
	if status != 200 {
		t.Fatalf("status %d body=%v", status, respBody)
	}
	// 验证全部写入（直接查 DB，跳过 SysConfigCache 因测试环境 SyncCacheConfig 会失败）
	if !dbHasSysConfig(t, "email_enabled", "true") {
		t.Error("email_enabled not set in DB")
	}
	if !dbHasSysConfig(t, "smtp_host", "smtp.gmail.com") {
		t.Error("smtp_host wrong in DB")
	}
	if !dbHasSysConfig(t, "smtp_port", "587") {
		t.Error("smtp_port wrong in DB")
	}
	// password 应被加密后存储（DB 里的 Value 不等于明文）
	var pwdRow database.SysConfig
	if err := database.DB.Where("key = ?", "smtp_password").First(&pwdRow).Error; err != nil {
		t.Fatalf("read pwd: %v", err)
	}
	if pwdRow.Value == "" || pwdRow.Value == "pwd123" {
		t.Errorf("smtp_password should be encrypted, got raw=%q", pwdRow.Value)
	}
	dec, err := utils.Decrypt(pwdRow.Value)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if dec != "pwd123" {
		t.Errorf("decrypted password = %q want 'pwd123'", dec)
	}
}

func TestUpdateAdminEmailConfig_NoFieldsReturnsSuccess(t *testing.T) {
	utils.InitCrypto()
	admin := setupAdminEmailTestDB(t)
	app := newAdminEmailTestApp(admin)

	status, body := adminEmailDoJSON(t, app, "PUT", "/email/config", map[string]any{})
	if status != 200 {
		t.Errorf("status %d", status)
	}
	if body["message_code"] != "SUCCESS_NO_CHANGE" {
		t.Errorf("expected SUCCESS_NO_CHANGE, got %v", body)
	}
}

// ── 集成测试：test-send ──

func TestSendAdminEmailTest_SMTPNotConfigured(t *testing.T) {
	utils.InitCrypto()
	admin := setupAdminEmailTestDB(t)
	app := newAdminEmailTestApp(admin)

	// 完全空的 SysConfig → IsConfigured 返回 false
	status, body := adminEmailDoJSON(t, app, "POST", "/email/test-send", map[string]string{"to": "admin@example.com"})
	if status != 400 {
		t.Errorf("status %d body=%v", status, body)
	}
	if body["message_code"] != "ERR_SMTP_NOT_CONFIGURED" {
		t.Errorf("unexpected msg: %v", body)
	}
}

func TestSendAdminEmailTest_InvalidEmailFormat(t *testing.T) {
	utils.InitCrypto()
	admin := setupAdminEmailTestDB(t)
	app := newAdminEmailTestApp(admin)

	status, body := adminEmailDoJSON(t, app, "POST", "/email/test-send", map[string]string{"to": "not-an-email"})
	if status != 400 {
		t.Errorf("status %d", status)
	}
	if body["message_code"] != "ERR_EMAIL_INVALID_FORMAT" {
		t.Errorf("unexpected msg: %v", body)
	}
}

func TestSendAdminEmailTest_PasswordDecryptFailureGracefullyHandled(t *testing.T) {
	utils.InitCrypto()
	admin := setupAdminEmailTestDB(t)
	app := newAdminEmailTestApp(admin)

	// 故意写非法加密 blob → LoadSMTPConfig 报错
	proxy.SysConfigMutex.Lock()
	proxy.SysConfigCache["smtp_host"] = "smtp.example.com"
	proxy.SysConfigCache["smtp_port"] = "587"
	proxy.SysConfigCache["smtp_username"] = "u"
	proxy.SysConfigCache["smtp_password"] = "not-a-valid-encrypted-blob"
	proxy.SysConfigCache["smtp_from"] = "f"
	proxy.SysConfigMutex.Unlock()

	status, body := adminEmailDoJSON(t, app, "POST", "/email/test-send", map[string]string{"to": "admin@example.com"})
	if status != 400 {
		t.Errorf("status %d body=%v", status, body)
	}
	if body["message_code"] != "ERR_SMTP_NOT_CONFIGURED" {
		t.Errorf("unexpected msg: %v", body)
	}
}

// proxyRenderTestEmail 的 HTML 转义在 sanitizeAdminTestField 里覆盖；
// 这里再针对 RenderTestEmail 调用做一次集成性 sanity 检查（构造的 HTML 包含转义后的 admin 名）。
func TestProxyRenderTestEmail_EscapesAdminUsername(t *testing.T) {
	msg, err := proxyRenderTestEmail("zh", "u@e.com", `<script>alert(1)</script>`, "Sub", "Title", "Body")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if strings.Contains(msg.HTMLBody, "<script>alert(1)</script>") {
		t.Error("admin username should be HTML-escaped in test email HTML body")
	}
	if !strings.Contains(msg.HTMLBody, "&lt;script&gt;") {
		t.Error("expected escaped form")
	}
	// text body 不转义
	if !strings.Contains(msg.TextBody, `<script>alert(1)</script>`) {
		t.Error("text body should not escape (text is text)")
	}
}
