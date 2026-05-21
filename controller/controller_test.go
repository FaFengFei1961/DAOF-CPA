package controller

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"daof-cpa/database"
	"daof-cpa/middleware"
	"daof-cpa/proxy"
	"daof-cpa/utils"

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
		&database.ModelCatalog{}, &database.ModelPricingRule{},
		&database.ApiLog{}, &database.ApiLogUsageLine{}, &database.AccessToken{}, &database.SysConfig{},
		&database.OperationLog{},
		// purgeUserDependents 也清这些表，需要建 schema 否则 DeleteUser 测试 500
		&database.Notification{},
		&database.NotificationBroadcastTarget{}, // codex 第五轮 fix：需要先建 schema 才能级联清理
		&database.UserSubscription{}, &database.SubscriptionUsage{},
		&database.TopupOrder{}, &database.TopupRefund{}, &database.PaymentWebhookReceipt{}, &database.NotificationPreference{},
		&database.Ticket{}, &database.TicketMessage{},
		&database.BillingEntry{}, &database.BillingReconciliation{}, // C2/C3 fix + Sprint5-M8 对账事实表
		// Phase H-3b：GetUsers 子查询 oauth_identities，即使是 0 行也得让表存在
		&database.OAuthIdentity{},
	)

	// Clear out memory DB state
	database.DB.Exec("DELETE FROM users")
	database.DB.Exec("DELETE FROM channels")
	database.DB.Exec("DELETE FROM channel_models")
	database.DB.Exec("DELETE FROM api_logs")
	database.DB.Exec("DELETE FROM api_log_usage_lines")
	database.DB.Exec("DELETE FROM access_tokens")
	database.DB.Exec("DELETE FROM sys_configs")
	database.DB.Exec("DELETE FROM operation_logs")

	// Seeds
	// admin 用 root + 默认密码 hash，匹配 GodSetup 的 initial-setup 路径
	database.DB.Create(&database.User{ID: 1, Username: "root", Role: "admin", Token: "admin-token-777", Quota: 100 * database.MicroPerUSD, PasswordHash: utils.GenerateHashForTest("123456")})
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
	// fix A-M1 (2026-05-19)：production 走 middleware.AdminGuard 注入 admin_user 到
	// c.Locals；测试必须挂同一守卫，否则 UpdateAdminCredentials 读不到 locals。
	app.Put("/api/admin/credentials", middleware.AdminGuard, UpdateAdminCredentials)

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
	app.Post("/api/auth/oauth/:provider/callback", OAuthCallback)
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
	status := 2
	payload := UserPayload{Quota: 10, Status: &status}
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
	database.DB.Create(&database.Channel{
		ID:   1,
		Name: "Platform CLIProxy",
		Type: "cliproxy",
		Key:  "sk-platform-cleartext-123456",
	})
	resp := sendRequest(app, "GET", "/api/admin/channels", nil, "")
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var body struct {
		Success bool               `json:"success"`
		Data    []database.Channel `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode channels: %v", err)
	}
	if !body.Success || len(body.Data) != 1 {
		t.Fatalf("unexpected response: %+v", body)
	}
	if body.Data[0].Key != "sk-platform-cleartext-123456" {
		t.Fatalf("channel key=%q want cleartext", body.Data[0].Key)
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
	payload := map[string]interface{}{"input_price_pico_per_token": int64(500_000_000), "status": 2}
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

func TestGetPublicPricingScansCacheWrite1hAndIgnoresZeroPrices(t *testing.T) {
	app := initializeMegaTestDB()
	database.DB.Create(&database.Channel{ID: 1, Name: "pricing", Key: "x", Type: "anthropic"})
	database.DB.Create(&database.ChannelModel{
		ChannelID: 1,
		ModelID:   "claude-opus-4-7",
		Status:    1,
	})
	database.DB.Create(&database.ChannelModel{
		ChannelID:                          1,
		ModelID:                            "claude-opus-4-7",
		InputPricePicoPerToken:             5 * database.PicoPerTokenPerUSDPerMTok,
		OutputPricePicoPerToken:            25 * database.PicoPerTokenPerUSDPerMTok,
		CachedInputPricePicoPerToken:       database.PicoPerTokenPerUSDPerMTok / 2,
		CacheWriteInputPricePicoPerToken:   25 * database.PicoPerTokenPerUSDPerMTok / 4,
		CacheWrite1hInputPricePicoPerToken: 10 * database.PicoPerTokenPerUSDPerMTok,
		Status:                             1,
	})

	resp := sendRequest(app, "GET", "/api/public/pricing", nil, "")
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var payload struct {
		Success bool `json:"success"`
		Data    []struct {
			ModelID              string  `json:"model_id"`
			MinInputPrice        float64 `json:"min_input_price"`
			MinCachePrice        float64 `json:"min_cache_price"`
			MinCacheWritePrice   float64 `json:"min_cache_write_price"`
			MinCacheWrite1hPrice float64 `json:"min_cache_write_1h_price"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode pricing response: %v", err)
	}
	if !payload.Success || len(payload.Data) != 1 {
		t.Fatalf("unexpected pricing payload: %#v", payload)
	}
	row := payload.Data[0]
	if row.MinInputPrice != 5 || row.MinCachePrice != 0.5 || row.MinCacheWritePrice != 6.25 || row.MinCacheWrite1hPrice != 10 {
		t.Fatalf("unexpected pricing row: %#v", row)
	}
}

func TestGetPublicPricingIncludesImageBillingMode(t *testing.T) {
	app := initializeMegaTestDB()
	if err := database.DB.Create(&database.Channel{ID: 31, Name: "xai", Key: "x", Type: "cliproxy", Status: 1}).Error; err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	if err := database.DB.Create(&database.ChannelModel{
		ChannelID:        31,
		ModelID:          "grok-imagine-image",
		ModelCategory:    database.ModelCategoryImage,
		BillingMode:      database.BillingModeImage,
		AllowedEndpoints: database.DefaultAllowedEndpointsForCategory(database.ModelCategoryImage),
		Status:           1,
	}).Error; err != nil {
		t.Fatalf("seed image model: %v", err)
	}
	if err := database.DB.Create(&database.ModelPricingRule{
		RuleKey:         "test|grok-imagine-image|image|output|1K",
		PricingVersion:  "test",
		ProviderKey:     "xai",
		ModelID:         "grok-imagine-image",
		OfficialModelID: "grok-imagine-image",
		BillingMode:     database.BillingModeImage,
		Unit:            "image",
		Direction:       "output",
		Resolution:      "1K",
		PriceMicroUSD:   20_000,
	}).Error; err != nil {
		t.Fatalf("seed image price: %v", err)
	}

	resp := sendRequest(app, "GET", "/api/public/pricing", nil, "")
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var payload struct {
		Success bool `json:"success"`
		Data    []struct {
			ModelID       string  `json:"model_id"`
			ModelCategory string  `json:"model_category"`
			BillingMode   string  `json:"billing_mode"`
			MinImagePrice float64 `json:"min_image_price"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode pricing response: %v", err)
	}
	for _, row := range payload.Data {
		if row.ModelID == "grok-imagine-image" {
			if row.ModelCategory != database.ModelCategoryImage || row.BillingMode != database.BillingModeImage || row.MinImagePrice != 0.02 {
				t.Fatalf("unexpected image pricing row: %#v", row)
			}
			return
		}
	}
	t.Fatalf("grok-imagine-image missing from public pricing: %#v", payload.Data)
}

func TestGetPublicPricingIncludesTokenBilledImagePrices(t *testing.T) {
	app := initializeMegaTestDB()
	if err := database.DB.Create(&database.Channel{ID: 32, Name: "openai-image", Key: "x", Type: "cliproxy", Status: 1}).Error; err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	if err := database.DB.Create(&database.ChannelModel{
		ChannelID:                        32,
		ModelID:                          "gpt-image-2",
		ModelCategory:                    database.ModelCategoryImage,
		BillingMode:                      database.BillingModeToken,
		AllowedEndpoints:                 database.DefaultAllowedEndpointsForCategory(database.ModelCategoryImage),
		InputPricePicoPerToken:           5 * database.PicoPerTokenPerUSDPerMTok,
		OutputPricePicoPerToken:          30 * database.PicoPerTokenPerUSDPerMTok,
		CachedInputPricePicoPerToken:     5 * database.PicoPerTokenPerUSDPerMTok / 4,
		CacheWriteInputPricePicoPerToken: 5 * database.PicoPerTokenPerUSDPerMTok,
		Status:                           1,
	}).Error; err != nil {
		t.Fatalf("seed token image model: %v", err)
	}

	resp := sendRequest(app, "GET", "/api/public/pricing", nil, "")
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var payload struct {
		Success bool `json:"success"`
		Data    []struct {
			ModelID        string  `json:"model_id"`
			ModelCategory  string  `json:"model_category"`
			BillingMode    string  `json:"billing_mode"`
			MinInputPrice  float64 `json:"min_input_price"`
			MinOutputPrice float64 `json:"min_output_price"`
			MinCachePrice  float64 `json:"min_cache_price"`
			MinImagePrice  float64 `json:"min_image_price"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode pricing response: %v", err)
	}
	for _, row := range payload.Data {
		if row.ModelID == "gpt-image-2" {
			if row.ModelCategory != database.ModelCategoryImage ||
				row.BillingMode != database.BillingModeToken ||
				row.MinInputPrice != 5 ||
				row.MinOutputPrice != 30 ||
				row.MinCachePrice != 1.25 ||
				row.MinImagePrice != 0 {
				t.Fatalf("unexpected token image pricing row: %#v", row)
			}
			return
		}
	}
	t.Fatalf("gpt-image-2 missing from public pricing: %#v", payload.Data)
}

func TestGetLogsIncludesImageUsageLines(t *testing.T) {
	app := initializeMegaTestDB()
	logRow := database.ApiLog{
		UserID:      2,
		TokenName:   "test-token",
		ModelName:   "grok-imagine-image",
		Cost:        40_000,
		ChargedCost: 40_000,
		Status:      200,
		RequestPath: database.EndpointImagesGenerations,
		CreatedAt:   time.Now(),
	}
	if err := database.DB.Create(&logRow).Error; err != nil {
		t.Fatalf("seed image api log: %v", err)
	}
	if err := database.DB.Create(&database.ApiLogUsageLine{
		ApiLogID:       logRow.ID,
		ModelName:      "grok-imagine-image",
		RequestPath:    database.EndpointImagesGenerations,
		Unit:           "image",
		Direction:      "output",
		Quantity:       2,
		UnitPriceMicro: 20_000,
		AmountMicroUSD: 40_000,
		Resolution:     "1K",
		AspectRatio:    "1:1",
		CostSource:     "official_matrix",
		MetadataJSON:   `{"billed_quantity":2,"response_images":2}`,
		CreatedAt:      time.Now(),
	}).Error; err != nil {
		t.Fatalf("seed usage line: %v", err)
	}

	resp := sendRequest(app, "GET", "/api/logs?limit=10", nil, "sk-user-111")
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var payload struct {
		Success bool `json:"success"`
		Data    struct {
			Logs []struct {
				ID         uint `json:"id"`
				UsageLines []struct {
					Unit       string         `json:"unit"`
					Direction  string         `json:"direction"`
					Quantity   int64          `json:"quantity"`
					UnitPrice  float64        `json:"unit_price"`
					Amount     float64        `json:"amount"`
					Resolution string         `json:"resolution"`
					CostSource string         `json:"cost_source"`
					Metadata   map[string]any `json:"metadata"`
				} `json:"usage_lines"`
			} `json:"logs"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode logs: %v", err)
	}
	for _, row := range payload.Data.Logs {
		if row.ID != logRow.ID {
			continue
		}
		if len(row.UsageLines) != 1 {
			t.Fatalf("usage lines len=%d want 1: %#v", len(row.UsageLines), row)
		}
		line := row.UsageLines[0]
		if line.Unit != "image" || line.Direction != "output" || line.Quantity != 2 || line.UnitPrice != 0.02 || line.Amount != 0.04 || line.Resolution != "1K" || line.CostSource != "official_matrix" {
			t.Fatalf("unexpected usage line: %#v", line)
		}
		if line.Metadata["billed_quantity"] != float64(2) {
			t.Fatalf("metadata missing billed quantity: %#v", line.Metadata)
		}
		return
	}
	t.Fatalf("image log missing from /api/logs payload: %#v", payload.Data.Logs)
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

	// Admin Update User
	sendRequest(app, "PUT", "/api/admin/users/2", map[string]interface{}{"quota": 999.0, "status": 1, "username": "testUser1"}, "")
	sendRequest(app, "PUT", "/api/admin/users/2", `invalid JSON`, "") // bad format
	sendRequest(app, "PUT", "/api/admin/users/999", map[string]interface{}{"quota": 999.0, "status": 1}, "")

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

	// fix H-Audit M9：路由已切到 /api/auth/oauth/:provider/callback；旧别名删除
	sendRequest(app, "POST", "/api/auth/oauth/github/callback", GithubAuthRequest{}, "")
	// code 存在但 state 未在 oauthStateStore → 403 (PKCE state 校验失败)
	sendRequest(app, "POST", "/api/auth/oauth/github/callback?code=123&state=bogus", GithubAuthRequest{}, "")

	proxy.SysConfigMutex.Lock()
	proxy.SysConfigCache["github_client_id"] = "test"
	proxy.SysConfigCache["github_client_secret"] = "test"
	proxy.SysConfigMutex.Unlock()
	// 配置存在但 state 仍非法 → 同样 403，覆盖到 SysConfigCache 读路径
	sendRequest(app, "POST", "/api/auth/oauth/github/callback?code=123&state=bogus", GithubAuthRequest{}, "")

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

	// Valid token testing via utils.Encrypt（Phase H-2 6 段格式：clean|provider|ext|user|ref|ts）
	validToken, _ := utils.Encrypt("clean|github|12345|testName||999999999")
	seedUser := database.User{Username: "testName", Token: "sk-testName", Role: "user", Status: 1}
	if err := database.DB.Create(&seedUser).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	// 模拟"已有用户绑定外部 github_id=123456"，这样新请求的 ext=12345 不应冲突
	database.DB.Create(&database.OAuthIdentity{
		UserID: seedUser.ID, Provider: database.OAuthProviderGitHub,
		ExternalID: "123456", LinkedAt: time.Now(),
	})
	sendRequest(app, "POST", "/api/auth/complete-profile", map[string]interface{}{"username": "validName", "tmp_token": validToken}, "") // should pass because external_id 12345 isn't 123456
}
