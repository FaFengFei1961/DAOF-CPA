package controller

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"daof-ai-hub/database"
	"daof-ai-hub/middleware"
	"daof-ai-hub/proxy"
	"daof-ai-hub/utils"

	"github.com/gofiber/fiber/v2"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func initializeMegaTestDB() *fiber.App {
	// crypto 必须初始化才能 Encrypt/Decrypt SysConfig 值
	utils.InitCrypto()

	var err error
	database.DB, err = gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		panic("failed to connect database")
	}
	database.DB.AutoMigrate(
		&database.User{}, &database.Channel{}, &database.ChannelModel{},
		&database.ApiLog{}, &database.AccessToken{}, &database.SysConfig{},
		&database.OperationLog{},
		// purgeUserDependents 也清这些表，需要建 schema 否则 DeleteUser 测试 500
		&database.Notification{},
		&database.NotificationBroadcastTarget{}, // codex 第五轮 fix：需要先建 schema 才能级联清理
		&database.UserSubscription{}, &database.SubscriptionUsage{},
		&database.TopupOrder{}, &database.NotificationPreference{},
		&database.Ticket{}, &database.TicketMessage{},
		&database.BillingEntry{}, // C2/C3 fix：UpdateUser/purgeUserDependents 现在写/删账单

	)

	// Clear out memory DB state
	database.DB.Exec("DELETE FROM users")
	database.DB.Exec("DELETE FROM channels")
	database.DB.Exec("DELETE FROM channel_models")
	database.DB.Exec("DELETE FROM api_logs")
	database.DB.Exec("DELETE FROM access_tokens")
	database.DB.Exec("DELETE FROM sys_configs")
	database.DB.Exec("DELETE FROM operation_logs")

	// Seeds
	// admin 用 root + 默认密码 hash，匹配 GodSetup 的 initial-setup 路径
	database.DB.Create(&database.User{ID: 1, Username: "root", Role: "admin", Token: "admin-token-777", Quota: 100 * database.MicroPerUSD, PasswordHash: utils.GenerateHash("123456")})
	database.DB.Create(&database.User{ID: 2, Username: "testUser1", Role: "user", Token: "sk-user-111", Quota: 50 * database.MicroPerUSD})

	// API Log Seed for Stats — 0.05 USD = 50_000 micro_usd
	database.DB.Create(&database.ApiLog{
		UserID: 2, TokenName: "test-token", ModelName: "gpt-4", PromptTokens: 10, CompletionTokens: 5, Cost: 50_000, CreatedAt: time.Now(),
	})

	// 同步 AuthCache，让 UserGuard 能用 Bearer token 查到用户
	proxy.SyncCacheConfig()

	app := fiber.New()

	// User Routes
	app.Get("/api/admin/users", GetUsers)
	app.Put("/api/admin/users/:id", UpdateUser)
	app.Delete("/api/admin/users/:id", DeleteUser)

	// Channel Routes
	app.Get("/api/admin/channels", GetAdminChannels)
	app.Post("/api/admin/channels", CreateChannel)
	app.Put("/api/admin/channels/:id", UpdateChannel)
	app.Delete("/api/admin/channels/:id", DeleteChannel)

	// Channel Model Routes
	app.Post("/api/admin/channels/:channelId/models", AddChannelModel)
	app.Put("/api/admin/channel_models/:id", UpdateChannelModel)
	app.Delete("/api/admin/channel_models/:id", RemoveChannelModel)
	app.Get("/api/admin/channels/:channelId/fetch_models", FetchUpstreamModels)
	app.Post("/api/admin/channels/:channelId/batch_models", AddChannelModelsBatch)
	app.Get("/v1/models", GetPublicModels)
	app.Get("/api/public/pricing", GetPublicPricing)
	app.Get("/api/admin/channels/:channelId/models", GetModelsByChannel)

	// Stats
	app.Get("/api/logs/stats", middleware.UserGuard, GetStats)

	// Admin Auth
	app.Post("/api/admin/sys_check", CheckSys)
	app.Post("/api/admin/login", GodLogin)
	app.Post("/api/admin/setup", GodSetup)
	app.Put("/api/admin/credentials", UpdateAdminCredentials)

	// API logs
	app.Get("/api/logs", middleware.UserGuard, GetLogs)
	app.Get("/api/admin/logs", middleware.UserGuard, GetLogs)

	// User scope
	app.Get("/api/user/data", middleware.UserGuard, GetSelfData)
	app.Get("/api/user/operations", middleware.UserGuard, GetUserOperations)

	// Tokens
	app.Get("/api/tokens", middleware.UserGuard, GetTokens)
	app.Post("/api/tokens", middleware.UserGuard, CreateToken)
	app.Put("/api/tokens/:id", middleware.UserGuard, UpdateTokenSettings)
	app.Delete("/api/tokens/:id", middleware.UserGuard, DeleteToken)

	// SysConfig
	app.Get("/api/admin/config", GetSysConfigs)
	app.Post("/api/admin/config", BatchUpdateSysConfigs)

	// I18N
	app.Get("/api/i18n/locales", GetLocalesList)
	app.Get("/api/i18n/locales/:lang", GetLocaleContent)
	app.Post("/api/admin/locales/:lang", UploadLocale)
	app.Delete("/api/admin/locales/:lang", DeleteLocale)

	// OAuth
	app.Post("/api/auth/github", GithubCallback)
	app.Get("/api/public-config", GetPublicConfig)
	app.Post("/api/auth/complete-risk", CompleteRisk)
	app.Post("/api/auth/complete-profile", CompleteProfile)

	return app
}

func sendRequest(app *fiber.App, method, route string, body interface{}, auth string) *http.Response {
	var req *http.Request
	if body != nil {
		payload, _ := json.Marshal(body)
		req = httptest.NewRequest(method, route, bytes.NewBuffer(payload))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, route, nil)
	}

	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}

	// bcrypt CheckHash ~70ms 单次（race detector 下放大数倍）；扩到 30s 留足余量
	resp, _ := app.Test(req, 30000)
	return resp
}

// ----------------------
// Users
// ----------------------
func TestGetUsersMega(t *testing.T) {
	app := initializeMegaTestDB()
	resp := sendRequest(app, "GET", "/api/admin/users?search=test&sort=quota_desc", nil, "")
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestUpdateUserMega(t *testing.T) {
	app := initializeMegaTestDB()
	payload := UserPayload{Quota: 10, Status: 2}
	resp := sendRequest(app, "PUT", "/api/admin/users/2", payload, "")
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestUpdateUserMega_BadBody(t *testing.T) {
	app := initializeMegaTestDB()
	req := httptest.NewRequest("PUT", "/api/admin/users/2", bytes.NewBuffer([]byte(`{bad json`)))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	if resp.StatusCode != 400 {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestDeleteUserMega(t *testing.T) {
	app := initializeMegaTestDB()
	resp := sendRequest(app, "DELETE", "/api/admin/users/2", nil, "")
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

// ----------------------
// Channels
// ----------------------
func TestGetChannelsMega(t *testing.T) {
	app := initializeMegaTestDB()
	resp := sendRequest(app, "GET", "/api/admin/channels", nil, "")
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestCreateChannelMega(t *testing.T) {
	app := initializeMegaTestDB()
	payload := database.Channel{Type: "openai", Key: "123", Name: "Test"}
	resp := sendRequest(app, "POST", "/api/admin/channels", payload, "")
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	// Create Bad
	badPayload := database.Channel{Type: "", Key: "", Name: ""}
	resp2 := sendRequest(app, "POST", "/api/admin/channels", badPayload, "")
	if resp2.StatusCode != 400 {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestUpdateChannelMega(t *testing.T) {
	app := initializeMegaTestDB()
	database.DB.Create(&database.Channel{ID: 1, Name: "Old", Key: "123", Type: "x"})
	payload := database.Channel{Name: "New"}
	resp := sendRequest(app, "PUT", "/api/admin/channels/1", payload, "")
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestDeleteChannelMega(t *testing.T) {
	app := initializeMegaTestDB()
	database.DB.Create(&database.Channel{ID: 1, Name: "Old", Key: "123", Type: "x"})
	resp := sendRequest(app, "DELETE", "/api/admin/channels/1", nil, "")
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

// ----------------------
// Channel Models
// ----------------------
func TestAddChannelModelMega(t *testing.T) {
	app := initializeMegaTestDB()
	database.DB.Create(&database.Channel{ID: 1, Name: "Old", Key: "123", Type: "x"})
	payload := database.ChannelModel{ModelID: "gpt-4"}
	resp := sendRequest(app, "POST", "/api/admin/channels/1/models", payload, "")
	if resp.StatusCode != 200 {
		buf := new(bytes.Buffer)
		buf.ReadFrom(resp.Body)
		t.Errorf("expected 200, got %d, body: %s", resp.StatusCode, buf.String())
	}
}

func TestUpdateChannelModelMega(t *testing.T) {
	app := initializeMegaTestDB()
	database.DB.Create(&database.ChannelModel{ID: 1, ChannelID: 1, ModelID: "gpt-4"})
	payload := database.ChannelModel{InputPrice: 0.5, Status: 2}
	resp := sendRequest(app, "PUT", "/api/admin/channel_models/1", payload, "")
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestDeleteChannelModelMega(t *testing.T) {
	app := initializeMegaTestDB()
	database.DB.Create(&database.ChannelModel{ID: 1, ChannelID: 1, ModelID: "gpt-4"})
	resp := sendRequest(app, "DELETE", "/api/admin/channel_models/1", nil, "")
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestMoreChannelModelMega(t *testing.T) {
	app := initializeMegaTestDB()
	database.DB.Create(&database.Channel{ID: 1, Name: "up", Key: "x", Type: "openai"})

	app.Test(httptest.NewRequest("GET", "/v1/models", nil))
	app.Test(httptest.NewRequest("GET", "/api/public/pricing", nil))
	app.Test(httptest.NewRequest("GET", "/api/admin/channels/1/models", nil))

	// Fetch upstream models
	mockUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{"data":[{"id":"gpt-4"}]}`))
	}))
	defer mockUpstream.Close()
	database.DB.Create(&database.Channel{ID: 2, Name: "up2", Key: "x", Type: "openai", BaseURL: mockUpstream.URL})
	sendRequest(app, "GET", "/api/admin/channels/2/fetch_models", nil, "")

	// Batch Add
	payload, _ := json.Marshal(map[string]interface{}{"models": []string{"gpt-4"}})
	req := httptest.NewRequest("POST", "/api/admin/channels/2/batch_models", bytes.NewBuffer(payload))
	req.Header.Set("Content-Type", "application/json")
	sendRequest(app, "POST", "/api/admin/channels/2/batch_models", map[string]interface{}{"models": []string{"gpt-4", "gpt-3.5-turbo"}}, "")
	sendRequest(app, "POST", "/api/admin/channels/2/batch_models", `invalid json`, "")

	// Error cases
	sendRequest(app, "GET", "/api/admin/channels/999/fetch_models", nil, "")
	sendRequest(app, "POST", "/api/admin/channels/999/batch_models", nil, "")
	sendRequest(app, "PUT", "/api/admin/channel_models/999", nil, "")
	sendRequest(app, "DELETE", "/api/admin/channel_models/999", nil, "")
}

// ----------------------
// Stats / Others
// ----------------------
func TestGetStatsMega(t *testing.T) {
	app := initializeMegaTestDB()

	// Unauthorized
	sendRequest(app, "GET", "/api/logs/stats?period=24h", nil, "")

	// Success cases
	resp := sendRequest(app, "GET", "/api/logs/stats?period=24h", nil, "sk-user-111")
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	resp2 := sendRequest(app, "GET", "/api/logs/stats?period=7d", nil, "sk-user-111")
	if resp2.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	resp3 := sendRequest(app, "GET", "/api/logs/stats?period=30d", nil, "sk-user-111")
	if resp3.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

}

// 整数精度测试：micro_usd 切换后所有金额是 int64，整数算术天然精确，
// 不再需要 float 累加误差测试。这里改为验证 int64 micro_usd 的"加减还原"无损。
func TestMicroUSDPrecisionMega(t *testing.T) {
	initializeMegaTestDB()
	// 10.5 USD = 10_500_000 micro_usd
	user := database.User{Username: "micro_tester", Quota: 10_500_000, Role: "admin"}
	database.DB.Create(&user)
	// 减 100 micro_usd（= $0.0001）
	user.Quota -= 100
	database.DB.Save(&user)

	var fetched database.User
	database.DB.First(&fetched, user.ID)
	if fetched.Quota != 10_499_900 {
		t.Fatalf("Micro precision test failed: expected 10_499_900, got %d", fetched.Quota)
	}
}

// ----------------------
// Admin Auth & Setup
// ----------------------
func TestAdminAuthMega(t *testing.T) {
	app := initializeMegaTestDB()

	// CheckSys：admin 是 root+默认密码 → setup_required=true
	payload1 := map[string]string{"sys": "root"}
	resp1 := sendRequest(app, "POST", "/api/admin/sys_check", payload1, "")
	if resp1.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp1.StatusCode)
	}

	// GodLogin 错密码必拒
	payload2 := GodLoginRequest{Username: "root", Password: "wrong"}
	resp2 := sendRequest(app, "POST", "/api/admin/login", payload2, "")
	if resp2.StatusCode != 401 {
		t.Errorf("expected 401, got %d", resp2.StatusCode)
	}

	// GodSetup：root + initial setup → 免 OldPassword
	payload3 := GodSetupRequest{CurrentUsername: "root", NewUsername: "testAdmin2", NewPassword: "123"}
	resp3 := sendRequest(app, "POST", "/api/admin/setup", payload3, "")
	if resp3.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp3.StatusCode)
	}

	// Update Credentials：上一步已轮换 token，重新拿
	var rotated database.User
	database.DB.Where("username = ?", "testAdmin2").First(&rotated)
	payload4 := AdminCredentialsPayload{Username: "testAdmin3", Password: "456"}
	resp4 := sendRequest(app, "PUT", "/api/admin/credentials", payload4, rotated.Token)
	if resp4.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp4.StatusCode)
	}
}

// ----------------------
// User Scope & Logs
// ----------------------
func TestUserScopeAndLogsMega(t *testing.T) {
	app := initializeMegaTestDB()

	// GetSelfData
	resp1 := sendRequest(app, "GET", "/api/user/data", nil, "sk-user-111")
	if resp1.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp1.StatusCode)
	}

	// Admin Get Users
	sendRequest(app, "GET", "/api/admin/users?page=1&size=10", nil, "")

	// Admin Update User — 必须带 status=1 否则会变成 0 让用户被踢出 AuthCache
	sendRequest(app, "PUT", "/api/admin/users/2", map[string]interface{}{"quota": 999, "status": 1, "username": "testUser1"}, "")
	sendRequest(app, "PUT", "/api/admin/users/2", `invalid JSON`, "") // bad format
	sendRequest(app, "PUT", "/api/admin/users/999", map[string]interface{}{"quota": 999, "status": 1}, "")

	// Admin Delete User
	sendRequest(app, "DELETE", "/api/admin/users/999", nil, "")

	// GetUserOperations
	resp2 := sendRequest(app, "GET", "/api/user/operations", nil, "sk-user-111")
	if resp2.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp2.StatusCode)
	}

	// GetLogs
	sendRequest(app, "GET", "/api/logs?page=0&limit=5", nil, "sk-user-111")
	sendRequest(app, "GET", "/api/logs", nil, "") // Unauthorized

	// GetLogs
	resp3 := sendRequest(app, "GET", "/api/admin/logs", nil, "admin-token-777")
	if resp3.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp3.StatusCode)
	}
}

// ----------------------
// Tokens
// ----------------------
func TestTokensMega(t *testing.T) {
	app := initializeMegaTestDB()

	// Test unauthorized
	sendRequest(app, "GET", "/api/tokens", nil, "")           // Should fail missing auth
	sendRequest(app, "GET", "/api/tokens", nil, "sk-invalid") // Should fail untraceable
	database.DB.Create(&database.User{ID: 99, Token: "sk-banned", Status: 2})
	sendRequest(app, "GET", "/api/tokens", nil, "sk-banned") // Should fail banned

	// GET
	resp1 := sendRequest(app, "GET", "/api/tokens", nil, "sk-user-111")
	if resp1.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp1.StatusCode)
	}

	// POST
	payload1 := map[string]string{"name": "testToken"}
	resp2 := sendRequest(app, "POST", "/api/tokens", payload1, "sk-user-111")
	if resp2.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp2.StatusCode)
	}

	sendRequest(app, "POST", "/api/tokens", `invalid json`, "sk-user-111") // Bad body parser

	// PUT
	database.DB.Create(&database.AccessToken{ID: 1, UserID: 2, Name: "updatable", Status: 1})
	payload2 := map[string]interface{}{"name": "newname", "status": 2}
	resp3 := sendRequest(app, "PUT", "/api/tokens/1", payload2, "sk-user-111")
	if resp3.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp3.StatusCode)
	}

	sendRequest(app, "PUT", "/api/tokens/999", payload2, "sk-user-111")     // Not found
	sendRequest(app, "PUT", "/api/tokens/1", `invalid json`, "sk-user-111") // Bad body

	// DELETE
	resp4 := sendRequest(app, "DELETE", "/api/tokens/1", nil, "sk-user-111")
	if resp4.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp4.StatusCode)
	}
	sendRequest(app, "DELETE", "/api/tokens/999", nil, "sk-user-111") // Not found
}

// ----------------------
// SysConfig
// ----------------------
func TestSysConfigMega(t *testing.T) {
	app := initializeMegaTestDB()

	// Seed some config
	encVal, _ := utils.Encrypt("123")
	database.DB.Create(&database.SysConfig{Key: "test", Value: encVal})

	// GET
	resp1 := sendRequest(app, "GET", "/api/admin/config", nil, "")
	if resp1.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp1.StatusCode)
	}

	// POST Update
	payload := map[string]string{"new_key": "456", "test": "789"}
	resp2 := sendRequest(app, "POST", "/api/admin/config", payload, "")
	if resp2.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp2.StatusCode)
	}
	sendRequest(app, "POST", "/api/admin/config", `invalid`, "")
}

// ----------------------
// I18N
// ----------------------
func TestI18NMega(t *testing.T) {
	app := initializeMegaTestDB()
	app.Test(httptest.NewRequest("GET", "/api/i18n/locales", nil))
	app.Test(httptest.NewRequest("GET", "/api/i18n/locales/zh-CN", nil))

	req := httptest.NewRequest("POST", "/api/admin/locales/fr-FR", bytes.NewBuffer([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	app.Test(req)

	app.Test(httptest.NewRequest("DELETE", "/api/admin/locales/fr-FR", nil))
}

// ----------------------
// OAuth
// ----------------------
func TestOAuthMega(t *testing.T) {
	app := initializeMegaTestDB()

	// Test Public Config
	sendRequest(app, "GET", "/api/public-config", nil, "")

	// Test GithubCallback empty
	sendRequest(app, "POST", "/api/auth/github", GithubAuthRequest{}, "")
	// Test GithubCallback no config
	sendRequest(app, "POST", "/api/auth/github", GithubAuthRequest{Code: "123"}, "")

	proxy.SysConfigMutex.Lock()
	proxy.SysConfigCache["github_client_id"] = "test"
	proxy.SysConfigCache["github_client_secret"] = "test"
	proxy.SysConfigMutex.Unlock()
	// Let it hit HTTP req
	sendRequest(app, "POST", "/api/auth/github", GithubAuthRequest{Code: "123"}, "")

	// CompleteRisk Edge Cases
	// 1. empty body
	sendRequest(app, "POST", "/api/auth/complete-risk", `[]`, "")
	// 2. wrong sms
	sendRequest(app, "POST", "/api/auth/complete-risk", map[string]interface{}{"is_robot": false, "sms_code": "9999", "tmp_token": ""}, "")
	// 3. invalid token parsing (decryption fail)
	sendRequest(app, "POST", "/api/auth/complete-risk", map[string]interface{}{"sms_code": "1234", "tmp_token": "broken_token_format"}, "")

	payload1 := map[string]interface{}{"is_robot": false, "sms_code": "1234", "tmp_token": "something_encrypted"}
	resp1 := sendRequest(app, "POST", "/api/auth/complete-risk", payload1, "")
	if resp1.StatusCode != 403 && resp1.StatusCode != 200 {
		t.Errorf("risk expected 403/200, got %d", resp1.StatusCode)
	}

	// CompleteProfile Edge Cases
	// 1. empty body
	sendRequest(app, "POST", "/api/auth/complete-profile", `[]`, "")
	// 2. badly formatted nick
	payload2 := map[string]interface{}{"username": "+++"}
	resp2 := sendRequest(app, "POST", "/api/auth/complete-profile", payload2, "")
	if resp2.StatusCode != 400 && resp2.StatusCode != 200 {
		t.Errorf("profile expected 400/200, got %d", resp2.StatusCode)
	}
	// 3. good nick, bad token
	sendRequest(app, "POST", "/api/auth/complete-profile", map[string]interface{}{"username": "tester", "tmp_token": "broken"}, "")

	// Valid token testing via utils.Encrypt
	validToken, _ := utils.Encrypt("clean|12345|testName|999999999")
	database.DB.Create(&database.User{Username: "testName", GithubID: "123456"})
	sendRequest(app, "POST", "/api/auth/complete-profile", map[string]interface{}{"username": "validName", "tmp_token": validToken}, "") // should pass because githubid 12345 isn't 123456
}
