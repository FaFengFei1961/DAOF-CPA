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
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.Write([]byte("data: {\"choices\": [{\"delta\": {\"content\": \"hello\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer backend.Close()

	AuthCache["stream-token"] = &database.User{ID: 1, Quota: 999, Status: 1, BalanceConsumeEnabled: true}
	ChannelMapCache[1] = &database.Channel{ID: 1, Type: "openai", BaseURL: backend.URL, Key: "sk-A"}
	RouteCache["gpt-stream"] = []*database.ChannelModel{{ChannelID: 1, Weight: 10, InputPrice: 1, OutputPrice: 1}}

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
}

func TestGetTransportEdge(t *testing.T) {
	tr1 := getTransport("")                      // Default
	tr2 := getTransport("http://127.0.0.1:1080") // Custom
	tr3 := getTransport("http://127.0.0.1:1080") // Cache
	if tr1 == nil || tr2 == nil || tr3 == nil {
		t.Error("Transport is nil")
	}
}
