package proxy

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"daof-ai-hub/database"

	"github.com/gofiber/fiber/v2"
	"github.com/tidwall/gjson"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestChatCompletionFailover(t *testing.T) {
	// Initialize in-memory DB to prevent nil pointer panics on deductQuota
	var err error
	database.DB, err = gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("Failed to connect to in-memory database: %v", err)
	}
	// fix CRITICAL R23+2-C3：GetUserActiveSubscriptions / LookupModerationPolicy 改为 fail-closed，
	// 测试需 migrate 相关表否则会被当作 DB 失败 → 503
	// 还要 BillingEntry（commit 阶段 fallback 余额会写）
	database.DB.AutoMigrate(&database.ApiLog{}, &database.UserSubscription{},
		&database.Channel{}, &database.ChannelModel{}, &database.BillingEntry{})

	// 1. Mock DB and Caches
	AuthCache = make(map[string]*database.User)
	// 测试用户启用余额消费（fix CRITICAL：fallback 到余额需要 BalanceConsumeEnabled=true）
	// Status: 1 是新增的入口检查（codex 第五轮：封禁用户不能透过缓存）
	AuthCache["test-sk"] = &database.User{ID: 1, Quota: 100, Status: 1, BalanceConsumeEnabled: true}

	RouteCache = make(map[string][]*database.ChannelModel)
	ChannelMapCache = make(map[uint]*database.Channel)

	// 2. Mock Upstream Servers
	// Backend A: Simulating limit exceeded (429)
	backendA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		w.Write([]byte(`{"error": "Too Many Requests"}`))
	}))
	defer backendA.Close()

	// Backend B: Simulating success (200)
	backendB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"choices": [{"message": {"content": "Hello World"}}], "usage": {"prompt_tokens": 10, "completion_tokens": 20}}`))
	}))
	defer backendB.Close()

	// 3. Setup Routes Configuration
	ChannelMapCache[1] = &database.Channel{ID: 1, Type: "openai", BaseURL: backendA.URL, Key: "sk-A"}
	ChannelMapCache[2] = &database.Channel{ID: 2, Type: "openai", BaseURL: backendB.URL, Key: "sk-B"}

	RouteCache["gpt-fallback-test"] = []*database.ChannelModel{
		{ChannelID: 1, Weight: 10}, // Try A mostly
		{ChannelID: 2, Weight: 1},  // B is fallback
	}

	app := fiber.New()
	app.Post("/v1/chat/completions", ChatCompletionProxyHandler)

	// 4. Execution Request Payload
	payload := `{"model": "gpt-fallback-test", "messages": [{"role": "user", "content": "Hi"}], "stream": false}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-sk")

	resp, err := app.Test(req, -1) // -1 disables timeout
	if err != nil {
		t.Fatalf("Failed to execute request: %v", err)
	}

	if resp.StatusCode != 200 {
		t.Fatalf("Expected fallback to return 200 OK, got %v", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Hello World") {
		t.Fatalf("Fallback response did not contain expected body payload from Backend B. Got: %s", string(body))
	}

	t.Log("Fallover Engine correctly switched from 429 Backend A to Backend B and returned 200!")
}

func TestChatCompletionEdgeCases(t *testing.T) {
	// 初始化 in-memory DB 防 deductQuota / ApiLog 写入 nil panic
	var err error
	database.DB, err = gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("Failed to connect to in-memory database: %v", err)
	}
	// fix CRITICAL R23+2-C3：GetUserActiveSubscriptions / LookupModerationPolicy 改为 fail-closed，
	// 测试需 migrate 相关表否则会被当作 DB 失败 → 503
	// 还要 BillingEntry（commit 阶段 fallback 余额会写）
	database.DB.AutoMigrate(&database.ApiLog{}, &database.UserSubscription{},
		&database.Channel{}, &database.ChannelModel{}, &database.BillingEntry{})

	app := fiber.New()
	app.Post("/v1/chat/completions", ChatCompletionProxyHandler)

	AuthCache = make(map[string]*database.User)
	RouteCache = make(map[string][]*database.ChannelModel)
	ChannelMapCache = make(map[uint]*database.Channel)

	// 启用余额消费让 fallback 路径不被订阅引擎拦截；Status: 1 通过封禁检查
	AuthCache["good-token"] = &database.User{ID: 2, Quota: 10, Status: 1, BalanceConsumeEnabled: true}
	AuthCache["no-quota-token"] = &database.User{ID: 3, Quota: 0, Status: 1, BalanceConsumeEnabled: true}

	tests := []struct {
		name         string
		token        string
		payload      string
		expectedCode int
	}{
		{"No Auth", "", `{}`, 401},
		{"Bad Auth", "invalid", `{}`, 401},
		// 余额不足必须传完整 payload，否则会先在 model 校验阶段返回 400
		{"No Quota", "no-quota-token", `{"model": "gpt-unknown"}`, 403},
		{"No Model", "good-token", `{"stream": false}`, 400},
		{"No Route", "good-token", `{"model": "gpt-unknown"}`, 404},
	}

	for _, tt := range tests {
		req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(tt.payload))
		req.Header.Set("Content-Type", "application/json")
		if tt.token != "" {
			req.Header.Set("Authorization", "Bearer "+tt.token)
		}
		resp, _ := app.Test(req)
		if resp.StatusCode != tt.expectedCode {
			t.Errorf("%s: Expected %d, got %d", tt.name, tt.expectedCode, resp.StatusCode)
		}
	}
}

func TestStreamHandling(t *testing.T) {
	var err error
	database.DB, err = gorm.Open(sqlite.Open("file:stream-handling?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("Failed to connect to in-memory database: %v", err)
	}
	if err := database.DB.AutoMigrate(&database.User{}, &database.ApiLog{}, &database.UserSubscription{},
		&database.SubscriptionUsage{}, &database.Channel{}, &database.ChannelModel{}, &database.BillingEntry{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	user := database.User{ID: 1, Username: "stream-user", Token: "stream-token", Quota: database.MicroPerUSD, Status: 1, BalanceConsumeEnabled: true}
	if err := database.DB.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	AuthCache = map[string]*database.User{user.Token: &user}
	AuthTokenCache = map[string]*database.AccessToken{}
	RouteCache = map[string][]*database.ChannelModel{}
	ChannelMapCache = map[uint]*database.Channel{}

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.Write([]byte("data: {\"choices\": [{\"delta\": {\"content\": \"hello\"}}]}\n\ndata: {\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":3}}\n\ndata: [DONE]\n\n"))
	}))
	defer backend.Close()

	ChannelMapCache[1] = &database.Channel{ID: 1, Type: "openai", BaseURL: backend.URL, Key: "sk-A"}
	RouteCache["gpt-stream"] = []*database.ChannelModel{{ChannelID: 1, Weight: 10, InputPrice: 1, OutputPrice: 1, ModerationLevel: "off", ModerationFailMode: "open"}}

	app := fiber.New()
	app.Post("/v1/chat/completions", ChatCompletionProxyHandler)

	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(`{"model": "gpt-stream", "stream": true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer stream-token")

	resp, _ := app.Test(req)
	if resp.StatusCode != 200 {
		t.Errorf("Stream expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "hello") {
		t.Errorf("Stream response did not contain SSE payload. Got: %s", string(body))
	}

	var row database.ApiLog
	if err := database.DB.Where("user_id = ? AND model_name = ?", user.ID, "gpt-stream").First(&row).Error; err != nil {
		t.Fatalf("expected api log for metered stream: %v", err)
	}
	if row.Status != 200 || row.PromptTokens != 2 || row.CompletionTokens != 3 || row.Cost != 5 {
		t.Fatalf("unexpected metered stream log: %+v", row)
	}
}

func TestCLIProxyChannelPreservesClaudeMessagesTools(t *testing.T) {
	var err error
	database.DB, err = gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("Failed to connect to in-memory database: %v", err)
	}
	if err := database.DB.AutoMigrate(&database.ApiLog{}, &database.UserSubscription{},
		&database.Channel{}, &database.ChannelModel{}, &database.BillingEntry{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	AuthCache = map[string]*database.User{
		"claude-token": &database.User{ID: 9, Quota: 100000000, Status: 1, BalanceConsumeEnabled: true},
	}
	AuthTokenCache = map[string]*database.AccessToken{}
	RouteCache = map[string][]*database.ChannelModel{}
	ChannelMapCache = map[uint]*database.Channel{}

	var gotPath, gotAuth, gotBody string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"type":"message","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":12,"output_tokens":3}}`))
	}))
	defer backend.Close()

	ChannelMapCache[1] = &database.Channel{ID: 1, Type: ChannelTypeCLIProxy, BaseURL: backend.URL, Key: "cpa-key"}
	RouteCache["claude-opus-4-7"] = []*database.ChannelModel{{ChannelID: 1, Weight: 1}}

	app := fiber.New()
	app.Post("/v1/messages", ChatCompletionProxyHandler)

	payload := `{"model":"claude-opus-4-7","max_tokens":64,"tools":[{"type":"bash_20250124","name":"bash"}],"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/messages", bytes.NewBufferString(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer claude-token")

	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d body=%s", resp.StatusCode, string(body))
	}
	if gotPath != "/v1/messages" {
		t.Fatalf("expected upstream /v1/messages, got %q", gotPath)
	}
	if gotAuth != "Bearer cpa-key" {
		t.Fatalf("expected CLIProxyAPI bearer auth, got %q", gotAuth)
	}
	if strings.Contains(gotBody, `"type":"function"`) {
		t.Fatalf("Claude tool was translated to OpenAI function tool: %s", gotBody)
	}
	if !strings.Contains(gotBody, `"type":"bash_20250124"`) {
		t.Fatalf("Claude Code tool tag was not preserved: %s", gotBody)
	}
	var logRow database.ApiLog
	if err := database.DB.Where("user_id = ? AND model_name = ?", 9, "claude-opus-4-7").First(&logRow).Error; err != nil {
		t.Fatalf("expected api log for claude request: %v", err)
	}
	if logRow.RequestPath != "/v1/messages" {
		t.Fatalf("expected stable request path /v1/messages, got %q", logRow.RequestPath)
	}
}

func TestCLIProxyChannelNormalizesClaudeCountTokensPath(t *testing.T) {
	var err error
	database.DB, err = gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("Failed to connect to in-memory database: %v", err)
	}
	if err := database.DB.AutoMigrate(&database.ApiLog{}, &database.UserSubscription{},
		&database.Channel{}, &database.ChannelModel{}, &database.BillingEntry{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	AuthCache = map[string]*database.User{
		"claude-token": &database.User{ID: 10, Quota: 100000000, Status: 1, BalanceConsumeEnabled: true},
	}
	AuthTokenCache = map[string]*database.AccessToken{}
	RouteCache = map[string][]*database.ChannelModel{}
	ChannelMapCache = map[uint]*database.Channel{}

	var gotPath string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"input_tokens":42}`))
	}))
	defer backend.Close()

	ChannelMapCache[1] = &database.Channel{ID: 1, Type: ChannelTypeCLIProxy, BaseURL: backend.URL, Key: "cpa-key"}
	RouteCache["claude-sonnet-4-6"] = []*database.ChannelModel{{ChannelID: 1, Weight: 1, InputPrice: 1000, OutputPrice: 1000}}

	app := fiber.New()
	app.Post("/v1/v1/messages/count_tokens", ChatCompletionProxyHandler)

	payload := `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/v1/messages/count_tokens", bytes.NewBufferString(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer claude-token")

	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d body=%s", resp.StatusCode, string(body))
	}
	if gotPath != "/v1/messages/count_tokens" {
		t.Fatalf("expected normalized upstream /v1/messages/count_tokens, got %q", gotPath)
	}
	if !strings.Contains(string(body), `"input_tokens":42`) {
		t.Fatalf("unexpected count_tokens body: %s", string(body))
	}
	var row database.ApiLog
	if err := database.DB.Where("user_id = ? AND request_path = ?", 10, "/v1/v1/messages/count_tokens").First(&row).Error; err != nil {
		t.Fatalf("expected api log for count_tokens: %v", err)
	}
	if row.Cost != 0 || row.PromptTokens != 42 {
		t.Fatalf("count_tokens should log tokens without billing, got cost=%d prompt=%d", row.Cost, row.PromptTokens)
	}
}

func TestGeminiNativeRouteUsesPathModelAndApiKeyHeader(t *testing.T) {
	var err error
	database.DB, err = gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("Failed to connect to in-memory database: %v", err)
	}
	if err := database.DB.AutoMigrate(&database.User{}, &database.ApiLog{}, &database.UserSubscription{},
		&database.SubscriptionUsage{}, &database.Channel{}, &database.ChannelModel{}, &database.BillingEntry{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	user := database.User{ID: 12, Username: "gemini-cli", Token: "sk-daof-test", Quota: database.MicroPerUSD, Status: 1, BalanceConsumeEnabled: true}
	if err := database.DB.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	AuthCache = map[string]*database.User{
		user.Token: &user,
	}
	AuthTokenCache = map[string]*database.AccessToken{}
	RouteCache = map[string][]*database.ChannelModel{}
	ChannelMapCache = map[uint]*database.Channel{}

	var gotPath, gotAuth, gotBody string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]}}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":3,"totalTokenCount":5}}`))
	}))
	defer backend.Close()

	ChannelMapCache[1] = &database.Channel{ID: 1, Type: ChannelTypeCLIProxy, BaseURL: backend.URL, Key: "cpa-key"}
	RouteCache["gemini-3.1-pro-preview"] = []*database.ChannelModel{{ChannelID: 1, Weight: 1, InputPrice: 1, OutputPrice: 1}}

	app := fiber.New()
	app.All("/v1beta/models/:modelAction", ChatCompletionProxyHandler)

	payload := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":64}}`
	req := httptest.NewRequest("POST", "/v1beta/models/gemini-3.1-pro-preview:generateContent", bytes.NewBufferString(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", user.Token)

	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d body=%s", resp.StatusCode, string(body))
	}
	if gotPath != "/v1beta/models/gemini-3.1-pro-preview:generateContent" {
		t.Fatalf("expected Gemini path preserved, got %q", gotPath)
	}
	if gotAuth != "Bearer cpa-key" {
		t.Fatalf("expected CLIProxyAPI bearer auth, got %q", gotAuth)
	}
	if gotBody != payload {
		t.Fatalf("Gemini request body should pass through unchanged.\nwant: %s\n got: %s", payload, gotBody)
	}
	if !strings.Contains(string(body), `"usageMetadata"`) {
		t.Fatalf("expected Gemini response body, got %s", string(body))
	}
}

func TestGeminiNativeStreamDoesNotAppendOpenAIDone(t *testing.T) {
	var err error
	database.DB, err = gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("Failed to connect to in-memory database: %v", err)
	}
	if err := database.DB.AutoMigrate(&database.User{}, &database.ApiLog{}, &database.UserSubscription{},
		&database.SubscriptionUsage{}, &database.Channel{}, &database.ChannelModel{}, &database.BillingEntry{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	user := database.User{ID: 13, Username: "gemini-cli-stream", Token: "sk-daof-stream", Quota: database.MicroPerUSD, Status: 1, BalanceConsumeEnabled: true}
	if err := database.DB.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	AuthCache = map[string]*database.User{user.Token: &user}
	AuthTokenCache = map[string]*database.AccessToken{}
	RouteCache = map[string][]*database.ChannelModel{}
	ChannelMapCache = map[uint]*database.Channel{}

	var gotAccept string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.Write([]byte("data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"ok\"}]}}],\"usageMetadata\":{\"promptTokenCount\":2,\"candidatesTokenCount\":3,\"totalTokenCount\":5}}\n\n"))
	}))
	defer backend.Close()

	ChannelMapCache[1] = &database.Channel{ID: 1, Type: ChannelTypeCLIProxy, BaseURL: backend.URL, Key: "cpa-key"}
	RouteCache["gemini-3.1-pro-preview"] = []*database.ChannelModel{{ChannelID: 1, Weight: 1, InputPrice: 1, OutputPrice: 1}}

	app := fiber.New()
	app.All("/v1beta/models/:modelAction", ChatCompletionProxyHandler)

	req := httptest.NewRequest("POST", "/v1beta/models/gemini-3.1-pro-preview:streamGenerateContent", bytes.NewBufferString(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", user.Token)

	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d body=%s", resp.StatusCode, string(body))
	}
	if gotAccept != "text/event-stream" {
		t.Fatalf("expected upstream SSE Accept header, got %q", gotAccept)
	}
	if strings.Contains(string(body), "[DONE]") {
		t.Fatalf("Gemini native stream must not receive OpenAI DONE frame: %s", string(body))
	}
	if !strings.Contains(string(body), `"usageMetadata"`) {
		t.Fatalf("expected Gemini SSE payload, got %s", string(body))
	}
}

func TestExtractGeminiModelFromPath(t *testing.T) {
	tests := map[string]string{
		"/v1beta/models/gemini-3.1-pro-preview:generateContent":       "gemini-3.1-pro-preview",
		"/v1/models/gemini-3-flash-preview:streamGenerateContent":     "gemini-3-flash-preview",
		"/v1beta/models/publishers/google/models/gemini:generateText": "publishers",
		"/v1/chat/completions": "",
	}
	for path, want := range tests {
		if got := extractGeminiModelFromPath(path); got != want {
			t.Fatalf("extractGeminiModelFromPath(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestNonStreamSuccessWithoutUsageFailsClosed(t *testing.T) {
	var err error
	database.DB, err = gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("Failed to connect to in-memory database: %v", err)
	}
	if err := database.DB.AutoMigrate(&database.ApiLog{}, &database.UserSubscription{},
		&database.Channel{}, &database.ChannelModel{}, &database.BillingEntry{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	AuthCache = map[string]*database.User{
		"meter-token": &database.User{ID: 10, Quota: 100000000, Status: 1, BalanceConsumeEnabled: true},
	}
	AuthTokenCache = map[string]*database.AccessToken{}
	RouteCache = map[string][]*database.ChannelModel{}
	ChannelMapCache = map[uint]*database.Channel{}

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"id":"chatcmpl-test","choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer backend.Close()

	ChannelMapCache[1] = &database.Channel{ID: 1, Type: ChannelTypeOpenAI, BaseURL: backend.URL, Key: "upstream-key"}
	RouteCache["gpt-meter-test"] = []*database.ChannelModel{{ChannelID: 1, Weight: 1, InputPrice: 1, OutputPrice: 1}}

	app := fiber.New()
	app.Post("/v1/chat/completions", ChatCompletionProxyHandler)

	payload := `{"model":"gpt-meter-test","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer meter-token")

	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != 502 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 502 for unmetered success, got %d body=%s", resp.StatusCode, string(body))
	}

	var logRow database.ApiLog
	if err := database.DB.Where("user_id = ? AND model_name = ?", 10, "gpt-meter-test").First(&logRow).Error; err != nil {
		t.Fatalf("expected api log for unmetered refusal: %v", err)
	}
	if logRow.Status != 502 || logRow.Cost != 0 || logRow.PromptTokens != 0 || logRow.CompletionTokens != 0 {
		t.Fatalf("unexpected api log for unmetered refusal: %+v", logRow)
	}
}

func TestStreamSuccessWithoutUsageIsAuditedAsUpstreamUnmetered(t *testing.T) {
	var err error
	database.DB, err = gorm.Open(sqlite.Open("file:stream-unmetered?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("Failed to connect to in-memory database: %v", err)
	}
	if err := database.DB.AutoMigrate(&database.User{}, &database.ApiLog{}, &database.UserSubscription{},
		&database.SubscriptionUsage{}, &database.Channel{}, &database.ChannelModel{}, &database.BillingEntry{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	user := database.User{ID: 11, Username: "stream-unmetered", Token: "stream-unmetered-token", Quota: database.MicroPerUSD, Status: 1, BalanceConsumeEnabled: true}
	if err := database.DB.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	AuthCache = map[string]*database.User{user.Token: &user}
	AuthTokenCache = map[string]*database.AccessToken{}
	RouteCache = map[string][]*database.ChannelModel{}
	ChannelMapCache = map[uint]*database.Channel{}

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.Write([]byte("data: {\"choices\": [{\"delta\": {\"content\": \"hello\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer backend.Close()

	ChannelMapCache[1] = &database.Channel{ID: 1, Type: ChannelTypeOpenAI, BaseURL: backend.URL, Key: "upstream-key"}
	RouteCache["gpt-stream-unmetered"] = []*database.ChannelModel{{
		ChannelID:          1,
		Weight:             1,
		InputPrice:         1,
		OutputPrice:        1,
		ModerationLevel:    "off",
		ModerationFailMode: "open",
	}}

	app := fiber.New()
	app.Post("/v1/chat/completions", ChatCompletionProxyHandler)

	payload := `{"model":"gpt-stream-unmetered","messages":[{"role":"user","content":"hi"}],"stream":true}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+user.Token)

	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("stream client status remains 200 after headers are sent, got %d body=%s", resp.StatusCode, string(body))
	}
	if !strings.Contains(string(body), "hello") {
		t.Fatalf("expected streamed payload to pass through, got %s", string(body))
	}

	var logRow database.ApiLog
	if err := database.DB.Where("user_id = ? AND model_name = ?", user.ID, "gpt-stream-unmetered").First(&logRow).Error; err != nil {
		t.Fatalf("expected api log for unmetered stream: %v", err)
	}
	if logRow.Status != 502 || logRow.ErrorType != "upstream_unmetered" || logRow.Cost != 0 || logRow.PromptTokens != 0 || logRow.CompletionTokens != 0 {
		t.Fatalf("unexpected api log for unmetered stream: %+v", logRow)
	}
}

func TestGetTransportEdge(t *testing.T) {
	tr1 := getTransport("")                      // Default
	tr2 := getTransport("http://127.0.0.1:1080") // Custom
	tr3 := getTransport("http://127.0.0.1:1080") // Cache
	if tr1 == nil || tr2 == nil || tr3 == nil {
		t.Error("Transport is nil")
	}
}

func TestExtractUsageTokenCountsCacheReadWrite(t *testing.T) {
	anthropic := extractUsageTokenCounts(gjson.Parse(`{
		"input_tokens": 10,
		"cache_creation_input_tokens": 200,
		"cache_read_input_tokens": 30,
		"output_tokens": 7
	}`))
	if anthropic.PromptTokens != 240 || anthropic.CachedTokens != 30 || anthropic.CacheWriteTokens != 200 || anthropic.CacheWrite5mTokens != 200 || anthropic.CacheWrite1hTokens != 0 || anthropic.CompletionTokens != 7 {
		t.Fatalf("anthropic usage parse mismatch: %+v", anthropic)
	}

	anthropicBuckets := extractUsageTokenCounts(gjson.Parse(`{
		"input_tokens": 10,
		"cache_creation": {"ephemeral_5m_input_tokens": 120, "ephemeral_1h_input_tokens": 80},
		"cache_read_input_tokens": 30,
		"output_tokens": 7
	}`))
	if anthropicBuckets.PromptTokens != 240 || anthropicBuckets.CacheWriteTokens != 200 || anthropicBuckets.CacheWrite5mTokens != 120 || anthropicBuckets.CacheWrite1hTokens != 80 {
		t.Fatalf("anthropic bucketed usage parse mismatch: %+v", anthropicBuckets)
	}

	openai := extractUsageTokenCounts(gjson.Parse(`{
		"input_tokens": 240,
		"input_tokens_details": {"cached_tokens": 30},
		"output_tokens": 7
	}`))
	if openai.PromptTokens != 240 || openai.CachedTokens != 30 || openai.CacheWriteTokens != 0 || openai.CompletionTokens != 7 {
		t.Fatalf("openai usage parse mismatch: %+v", openai)
	}

	gemini := extractUsageTokenCounts(gjson.Parse(`{
		"promptTokenCount": 240,
		"cachedContentTokenCount": 30,
		"candidatesTokenCount": 7,
		"thoughtsTokenCount": 3
	}`))
	if gemini.PromptTokens != 240 || gemini.CachedTokens != 30 || gemini.CacheWriteTokens != 0 || gemini.CompletionTokens != 10 || gemini.ReasoningTokens != 3 {
		t.Fatalf("gemini usage parse mismatch: %+v", gemini)
	}
}

func TestBillingUsesConfiguredCacheWritePrice(t *testing.T) {
	app, cleanup := setupBillingFormulaTest(t, "gpt-cache-write-test", `{
		"choices":[{"message":{"content":"ok"}}],
		"usage":{"prompt_tokens":1000,"cache_creation_input_tokens":200,"completion_tokens":10}
	}`, &database.ChannelModel{
		ChannelID:            1,
		Weight:               1,
		InputPrice:           1,
		OutputPrice:          10,
		CacheWriteInputPrice: 1.25,
		ModerationLevel:      "off",
		ModerationFailMode:   "open",
	})
	defer cleanup()

	row := invokeBillingFormulaTest(t, app, "gpt-cache-write-test")
	if row.PromptTokens != 1000 || row.CacheWriteTokens != 200 || row.CompletionTokens != 10 {
		t.Fatalf("unexpected logged tokens: %+v", row)
	}
	// standard input 800*$1 + cache write 200*$1.25 + output 10*$10 = 1150 micro_usd.
	if row.Cost != 1150 {
		t.Fatalf("cost=%d want 1150", row.Cost)
	}
}

func TestBillingSeparatesClaudeCacheWriteTTLPrices(t *testing.T) {
	app, cleanup := setupBillingFormulaTest(t, "claude-cache-write-ttl-test", `{
		"type":"message",
		"content":[{"type":"text","text":"ok"}],
		"usage":{
			"input_tokens":100,
			"cache_creation":{"ephemeral_5m_input_tokens":200,"ephemeral_1h_input_tokens":300},
			"cache_read_input_tokens":50,
			"output_tokens":10
		}
	}`, &database.ChannelModel{
		ChannelID:              1,
		Weight:                 1,
		InputPrice:             5,
		CachedInputPrice:       0.5,
		CacheWriteInputPrice:   6.25,
		CacheWrite1hInputPrice: 10,
		OutputPrice:            25,
		ModerationLevel:        "off",
		ModerationFailMode:     "open",
	})
	defer cleanup()

	row := invokeBillingFormulaTest(t, app, "claude-cache-write-ttl-test")
	if row.PromptTokens != 650 || row.CachedTokens != 50 || row.CacheWriteTokens != 500 || row.CacheWrite5mTokens != 200 || row.CacheWrite1hTokens != 300 || row.CompletionTokens != 10 {
		t.Fatalf("unexpected logged tokens: %+v", row)
	}
	// standard 100*$5 + read 50*$0.5 + write5m 200*$6.25 + write1h 300*$10 + output 10*$25 = 5025 micro_usd.
	if row.Cost != 5025 {
		t.Fatalf("cost=%d want 5025", row.Cost)
	}
}

func TestBillingUsesHighCachedInputPriceForLongPrompt(t *testing.T) {
	app, cleanup := setupBillingFormulaTest(t, "gpt-high-cache-test", `{
		"choices":[{"message":{"content":"ok"}}],
		"usage":{"prompt_tokens":300000,"prompt_tokens_details":{"cached_tokens":200000},"completion_tokens":1000}
	}`, &database.ChannelModel{
		ChannelID:             1,
		Weight:                1,
		InputPrice:            2.5,
		CachedInputPrice:      0.25,
		OutputPrice:           15,
		ContextPriceThreshold: 272000,
		HighInputPrice:        5,
		HighCachedInputPrice:  0.5,
		HighOutputPrice:       22.5,
		ModerationLevel:       "off",
		ModerationFailMode:    "open",
	})
	defer cleanup()

	row := invokeBillingFormulaTest(t, app, "gpt-high-cache-test")
	if row.PromptTokens != 300000 || row.CachedTokens != 200000 || row.CompletionTokens != 1000 {
		t.Fatalf("unexpected logged tokens: %+v", row)
	}
	// standard 100000*$5 + cached 200000*$0.5 + output 1000*$22.5 = 622500 micro_usd.
	if row.Cost != 622500 {
		t.Fatalf("cost=%d want 622500", row.Cost)
	}
}

func TestBillingLongContextThresholdUsesPromptTokensOnly(t *testing.T) {
	app, cleanup := setupBillingFormulaTest(t, "gpt-threshold-prompt-only-test", `{
		"choices":[{"message":{"content":"ok"}}],
		"usage":{"prompt_tokens":260000,"completion_tokens":20000}
	}`, &database.ChannelModel{
		ChannelID:             1,
		Weight:                1,
		InputPrice:            1,
		OutputPrice:           2,
		ContextPriceThreshold: 272000,
		HighInputPrice:        10,
		HighOutputPrice:       20,
		ModerationLevel:       "off",
		ModerationFailMode:    "open",
	})
	defer cleanup()

	row := invokeBillingFormulaTest(t, app, "gpt-threshold-prompt-only-test")
	if row.PromptTokens != 260000 || row.CompletionTokens != 20000 {
		t.Fatalf("unexpected logged tokens: %+v", row)
	}
	// prompt is below 272k, so output tokens must not push the request into high tier.
	if row.Cost != 300000 {
		t.Fatalf("cost=%d want 300000", row.Cost)
	}
}

func setupBillingFormulaTest(t *testing.T, modelName, upstreamBody string, route *database.ChannelModel) (*fiber.App, func()) {
	t.Helper()
	var err error
	database.DB, err = gorm.Open(sqlite.Open("file:"+modelName+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	if err := database.DB.AutoMigrate(&database.User{}, &database.ApiLog{}, &database.UserSubscription{},
		&database.SubscriptionUsage{}, &database.Channel{}, &database.ChannelModel{}, &database.BillingEntry{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	user := database.User{ID: 99, Username: "billing-formula", Token: "sk-billing-formula", Quota: 100 * database.MicroPerUSD, Status: 1, BalanceConsumeEnabled: true}
	if err := database.DB.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	AuthCache = map[string]*database.User{user.Token: &user}
	AuthTokenCache = map[string]*database.AccessToken{}
	RouteCache = map[string][]*database.ChannelModel{}
	ChannelMapCache = map[uint]*database.Channel{}

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(upstreamBody))
	}))
	ChannelMapCache[1] = &database.Channel{ID: 1, Type: ChannelTypeOpenAI, BaseURL: backend.URL, Key: "upstream-key"}
	RouteCache[modelName] = []*database.ChannelModel{route}

	app := fiber.New()
	app.Post("/v1/chat/completions", ChatCompletionProxyHandler)
	return app, backend.Close
}

func invokeBillingFormulaTest(t *testing.T, app *fiber.App, modelName string) database.ApiLog {
	t.Helper()
	payload := `{"model":"` + modelName + `","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-billing-formula")
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d body=%s", resp.StatusCode, string(body))
	}
	var row database.ApiLog
	if err := database.DB.Where("model_name = ?", modelName).First(&row).Error; err != nil {
		t.Fatalf("read api log: %v", err)
	}
	return row
}
