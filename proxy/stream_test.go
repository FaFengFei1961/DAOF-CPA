package proxy

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"daof-cpa/database"

	"github.com/gofiber/fiber/v2"
	"github.com/tidwall/gjson"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func pricePicoForTest(price float64) int64 {
	return int64(price * float64(database.PicoPerTokenPerUSDPerMTok))
}

func TestClassifyUpstreamStatus(t *testing.T) {
	cases := []struct {
		status int
		want   StatusAction
	}{
		{200, StatusActionSuccess},
		{204, StatusActionSuccess},
		{299, StatusActionSuccess},
		{408, StatusActionRetryableTransient},
		{502, StatusActionRetryableTransient},
		{503, StatusActionRetryableTransient},
		{504, StatusActionRetryableTransient},
		{429, StatusActionRateLimit},
		{500, StatusActionUpstreamFatal},
		{501, StatusActionUpstreamFatal},
		{505, StatusActionUpstreamFatal},
		{404, StatusActionConfigError},
		{410, StatusActionConfigError},
		{400, StatusActionClientError},
		{422, StatusActionClientError},
		{401, StatusActionAuthError},
		{403, StatusActionAuthError},
		{302, StatusActionUnknown},
		{418, StatusActionUnknown},
	}
	for _, tc := range cases {
		if got := classifyUpstreamStatus(tc.status); got != tc.want {
			t.Fatalf("status %d classified as %d, want %d", tc.status, got, tc.want)
		}
	}
}

func TestCostCalculationZeroBias(t *testing.T) {
	const iterations = 1_000_000
	pricePico := pricePicoForTest(1)
	var sum int64
	for i := 0; i < iterations; i++ {
		got, ok := checkedCostMicroUSD(1, pricePico, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
		if !ok {
			t.Fatalf("checkedCostMicroUSD failed at iteration %d", i)
		}
		sum += got
	}

	expected := new(big.Int).Mul(big.NewInt(iterations), big.NewInt(pricePico))
	expected.Div(expected, big.NewInt(database.PicoPerMicroUSD))
	if !expected.IsInt64() {
		t.Fatalf("test expected value overflowed int64: %s", expected.String())
	}
	if sum != expected.Int64() {
		t.Fatalf("sum=%d want %d", sum, expected.Int64())
	}
}

// TestCheckedCostMicroUSD_CeilDivPreventsFreeMicroService 验证 Sprint1-P0-4 修复：
// pico_usd → micro_usd 转换使用 ceil-div，亚 1-micro 成本不会被截断到 0。
//
// 触发场景：低单价模型 × 少 token 请求。如 $0.1/Mtoken × 1 token = 0.1 micro_usd。
// 旧 floor 实现：0 micro → "免费消耗"。
// 新 ceil 实现：1 micro → 平台至少收 $1e-6。
func TestCheckedCostMicroUSD_CeilDivPreventsFreeMicroService(t *testing.T) {
	// 1 token × 1e8 pico = 1e8 pico_usd cost = 0.1 micro_usd
	// floor → 0（bug）；ceil → 1（修复）
	got, ok := checkedCostMicroUSD(1, int64(1e8), 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
	if !ok {
		t.Fatalf("checkedCostMicroUSD unexpected fail-closed")
	}
	if got != 1 {
		t.Errorf("ceil-div should round up sub-1-micro cost to 1: got %d (was 0 before P0-4 fix)", got)
	}

	// 边界测试：精确整除时不要多进位（防御 ceil-div 实现 bug）
	// 1 token × 1e9 pico = 1e9 pico = 精确 1 micro_usd
	exact, ok := checkedCostMicroUSD(1, int64(1e9), 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
	if !ok {
		t.Fatalf("checkedCostMicroUSD unexpected fail-closed (exact-boundary case)")
	}
	if exact != 1 {
		t.Errorf("exact integer division should not over-round: got %d, want 1", exact)
	}

	// 零成本仍是零（ceil(0/N) = 0，不能误进位）
	zero, ok := checkedCostMicroUSD(0, int64(1e9), 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
	if !ok {
		t.Fatalf("checkedCostMicroUSD unexpected fail-closed (zero-cost case)")
	}
	if zero != 0 {
		t.Errorf("zero pico cost should remain 0 micro: got %d", zero)
	}

	// 略超 1 micro 仍只进 1 位（验证 ceil 而非 floor 的反向：不应直接走 floor）
	// 1 token × 1.5e9 pico = 1.5e9 pico = 1.5 micro_usd → ceil = 2
	sesqui, ok := checkedCostMicroUSD(1, int64(1.5e9), 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
	if !ok {
		t.Fatalf("checkedCostMicroUSD unexpected fail-closed (1.5 micro case)")
	}
	if sesqui != 2 {
		t.Errorf("1.5 micro pico cost should ceil to 2 micro: got %d", sesqui)
	}
}

func TestCostCalculationOverflowDefense(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("requires 64-bit int tokens to exceed int64 micro_usd after division")
	}
	_, ok := checkedCostMicroUSD(int(^uint(0)>>1), database.MaxChannelModelPricePicoPerToken, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
	if ok {
		t.Fatalf("expected overflow to return ok=false")
	}
}

func TestNonStreamUpstreamTimeoutFromSysConfig(t *testing.T) {
	SysConfigMutex.Lock()
	old := SysConfigCache
	SysConfigCache = map[string]string{}
	SysConfigMutex.Unlock()
	defer func() {
		SysConfigMutex.Lock()
		SysConfigCache = old
		SysConfigMutex.Unlock()
	}()

	if got := nonStreamUpstreamTimeout(); got != defaultNonStreamUpstreamTimeout {
		t.Fatalf("default timeout=%v want %v", got, defaultNonStreamUpstreamTimeout)
	}

	set := func(v string) {
		SysConfigMutex.Lock()
		SysConfigCache[proxyNonStreamUpstreamTimeoutKey] = v
		SysConfigMutex.Unlock()
	}

	set("901")
	if got := nonStreamUpstreamTimeout(); got != 901*time.Second {
		t.Fatalf("configured timeout=%v want 901s", got)
	}
	set("1")
	if got := nonStreamUpstreamTimeout(); got != minNonStreamUpstreamTimeout {
		t.Fatalf("min-clamped timeout=%v want %v", got, minNonStreamUpstreamTimeout)
	}
	set("7200")
	if got := nonStreamUpstreamTimeout(); got != maxNonStreamUpstreamTimeout {
		t.Fatalf("max-clamped timeout=%v want %v", got, maxNonStreamUpstreamTimeout)
	}
	set("bad")
	if got := nonStreamUpstreamTimeout(); got != defaultNonStreamUpstreamTimeout {
		t.Fatalf("invalid timeout=%v want %v", got, defaultNonStreamUpstreamTimeout)
	}
}

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
	RouteCache["gpt-stream"] = []*database.ChannelModel{{ChannelID: 1, Weight: 10, InputPricePicoPerToken: pricePicoForTest(1), OutputPricePicoPerToken: pricePicoForTest(1), ModerationLevel: "off", ModerationFailMode: "open"}}

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

func TestEndpointPolicyBlocksNonStreamingChatBeforeUpstream(t *testing.T) {
	var err error
	database.DB, err = gorm.Open(sqlite.Open("file:endpoint-policy-block?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	if err := database.DB.AutoMigrate(&database.User{}, &database.ApiLog{}, &database.UserSubscription{},
		&database.SubscriptionUsage{}, &database.Channel{}, &database.ChannelModel{}, &database.BillingEntry{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	user := database.User{ID: 51, Username: "endpoint-user", Token: "endpoint-token", Quota: 100 * database.MicroPerUSD, Status: 1, BalanceConsumeEnabled: true}
	if err := database.DB.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	AuthCache = map[string]*database.User{user.Token: &user}
	AuthTokenCache = map[string]*database.AccessToken{}
	RouteCache = map[string][]*database.ChannelModel{}
	ChannelMapCache = map[uint]*database.Channel{}

	upstreamHits := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		w.WriteHeader(200)
		w.Write([]byte(`{"choices":[{"message":{"content":"unexpected"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer backend.Close()

	ChannelMapCache[1] = &database.Channel{ID: 1, Type: ChannelTypeCLIProxy, BaseURL: backend.URL, Key: "upstream-key"}
	RouteCache["gpt-5.5"] = []*database.ChannelModel{{
		ChannelID:               1,
		Weight:                  1,
		InputPricePicoPerToken:  pricePicoForTest(1),
		OutputPricePicoPerToken: pricePicoForTest(1),
		EndpointPolicy:          database.EndpointPolicyNoChatNonStream,
		ModerationLevel:         "off",
	}}

	app := fiber.New()
	app.Post("/v1/chat/completions", ChatCompletionProxyHandler)

	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hi"}],"stream":false}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer endpoint-token")
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("status=%d want 400 body=%s", resp.StatusCode, string(body))
	}
	if !strings.Contains(string(body), "ERR_MODEL_ENDPOINT_UNSUPPORTED") {
		t.Fatalf("response should explain endpoint policy, got %s", string(body))
	}
	if upstreamHits != 0 {
		t.Fatalf("upstreamHits=%d want 0", upstreamHits)
	}

	var row database.ApiLog
	if err := database.DB.Where("model_name = ? AND error_type = ?", "gpt-5.5", "unsupported_endpoint").First(&row).Error; err != nil {
		t.Fatalf("expected unsupported_endpoint api log: %v", err)
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

// TestCLIProxyChatPassesClaudeOpus47Temperature 验证 cliproxy 通道现在是完全 passthrough：
// 客户端发的 temperature 字段会直接到上游，不再被网关删除。
//
// 网关侧不再做 deprecated 模型字段裁剪（dropDeprecatedClaudeTemperature shim 已删除）。
// 如果 claude-opus-4-7 不支持 temperature，由 Anthropic 上游返回 4xx，客户端自行修正调用。
func TestCLIProxyChatPassesClaudeOpus47Temperature(t *testing.T) {
	var err error
	database.DB, err = gorm.Open(sqlite.Open("file:cliproxy-claude-temperature?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	if err := database.DB.AutoMigrate(&database.ApiLog{}, &database.UserSubscription{},
		&database.Channel{}, &database.ChannelModel{}, &database.BillingEntry{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	AuthCache = map[string]*database.User{
		"claude-temp-token": &database.User{ID: 91, Quota: 100000000, Status: 1, BalanceConsumeEnabled: true},
	}
	AuthTokenCache = map[string]*database.AccessToken{}
	RouteCache = map[string][]*database.ChannelModel{}
	ChannelMapCache = map[uint]*database.Channel{}

	var gotBody string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":2,"completion_tokens":3}}`))
	}))
	defer backend.Close()

	ChannelMapCache[1] = &database.Channel{ID: 1, Type: ChannelTypeCLIProxy, BaseURL: backend.URL, Key: "cpa-key"}
	RouteCache["claude-opus-4-7"] = []*database.ChannelModel{{ChannelID: 1, Weight: 1, InputPricePicoPerToken: pricePicoForTest(1), OutputPricePicoPerToken: pricePicoForTest(1), ModerationLevel: "off"}}

	app := fiber.New()
	app.Post("/v1/chat/completions", ChatCompletionProxyHandler)

	payload := `{"model":"claude-opus-4-7","temperature":0.2,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer claude-temp-token")

	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d body=%s", resp.StatusCode, string(body))
	}
	// 验证：网关不再裁剪 temperature 字段，passthrough 透传到上游
	if !gjson.Get(gotBody, "temperature").Exists() {
		t.Fatalf("temperature should pass through to CLIProxyAPI (no gateway-side scrubbing): %s", gotBody)
	}
	if gjson.Get(gotBody, "temperature").Float() != 0.2 {
		t.Fatalf("temperature value should match request, got %v: %s", gjson.Get(gotBody, "temperature").Float(), gotBody)
	}
	if gjson.Get(gotBody, "messages.0.content").String() != "hi" {
		t.Fatalf("chat payload should otherwise be preserved: %s", gotBody)
	}
}

// TestCLIProxyChannelPassesClaudeCountTokensPath 验证 cliproxy 上游对 count_tokens
// 是 passthrough：客户端发 /v1/messages/count_tokens，上游就收到完全一致的路径。
//
// 不再做旧 `/v1/v1/messages/count_tokens` → `/v1/messages/count_tokens` 的路径修正
// （normalizeCLIProxyPath shim 已删除）。请求 /v1/v1/... 客户端应自行修正。
func TestCLIProxyChannelPassesClaudeCountTokensPath(t *testing.T) {
	var err error
	database.DB, err = gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("Failed to connect to in-memory database: %v", err)
	}
	if err := database.DB.AutoMigrate(&database.User{}, &database.ApiLog{}, &database.UserSubscription{},
		&database.Channel{}, &database.ChannelModel{}, &database.BillingEntry{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	user := &database.User{ID: 10, Username: "claude-count", Token: "claude-token", Quota: 100000000, Status: 1, BalanceConsumeEnabled: true}
	if err := database.DB.Create(user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	AuthCache = map[string]*database.User{
		"claude-token": user,
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
	RouteCache["claude-sonnet-4-6"] = []*database.ChannelModel{{ChannelID: 1, Weight: 1, InputPricePicoPerToken: pricePicoForTest(1000), OutputPricePicoPerToken: pricePicoForTest(1000)}}

	app := fiber.New()
	app.Post("/v1/messages/count_tokens", ChatCompletionProxyHandler)

	payload := `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/messages/count_tokens", bytes.NewBufferString(payload))
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
		t.Fatalf("expected passthrough upstream /v1/messages/count_tokens, got %q", gotPath)
	}
	if !strings.Contains(string(body), `"input_tokens":42`) {
		t.Fatalf("unexpected count_tokens body: %s", string(body))
	}
	var row database.ApiLog
	if err := database.DB.Where("user_id = ? AND request_path = ?", 10, "/v1/messages/count_tokens").First(&row).Error; err != nil {
		t.Fatalf("expected api log for count_tokens: %v", err)
	}
	if row.Cost != 42000 || row.PromptTokens != 42 {
		t.Fatalf("count_tokens should bill input tokens, got cost=%d prompt=%d", row.Cost, row.PromptTokens)
	}
	var fresh database.User
	if err := database.DB.First(&fresh, 10).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	if fresh.Quota != 99958000 {
		t.Fatalf("count_tokens should deduct quota, got %d", fresh.Quota)
	}
	var bill database.BillingEntry
	if err := database.DB.Where("user_id = ? AND related_type = ?", 10, "api_log").First(&bill).Error; err != nil {
		t.Fatalf("expected billing entry for count_tokens: %v", err)
	}
	if bill.AmountUSD != -42000 || bill.TokensTotal != 42 {
		t.Fatalf("billing amount/tokens = %d/%d, want -42000/42", bill.AmountUSD, bill.TokensTotal)
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
	RouteCache["gemini-3.1-pro-preview"] = []*database.ChannelModel{{ChannelID: 1, Weight: 1, InputPricePicoPerToken: pricePicoForTest(1), OutputPricePicoPerToken: pricePicoForTest(1)}}

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
	RouteCache["gemini-3.1-pro-preview"] = []*database.ChannelModel{{ChannelID: 1, Weight: 1, InputPricePicoPerToken: pricePicoForTest(1), OutputPricePicoPerToken: pricePicoForTest(1)}}

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
	RouteCache["gpt-meter-test"] = []*database.ChannelModel{{ChannelID: 1, Weight: 1, InputPricePicoPerToken: pricePicoForTest(1), OutputPricePicoPerToken: pricePicoForTest(1)}}

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
		ChannelID:               1,
		Weight:                  1,
		InputPricePicoPerToken:  pricePicoForTest(1),
		OutputPricePicoPerToken: pricePicoForTest(1),
		ModerationLevel:         "off",
		ModerationFailMode:      "open",
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

func TestNonStreamZeroUsageWritesUpstreamUnmeteredBillingState(t *testing.T) {
	app, cleanup := setupBillingFormulaTest(t, "gpt-zero-usage-test", `{
		"choices":[{"message":{"content":"ok"}}],
		"usage":{"prompt_tokens":0,"completion_tokens":0}
	}`, &database.ChannelModel{
		ChannelID:               1,
		Weight:                  1,
		InputPricePicoPerToken:  pricePicoForTest(1),
		OutputPricePicoPerToken: pricePicoForTest(1),
		ModerationLevel:         "off",
		ModerationFailMode:      "open",
	})
	defer cleanup()

	payload := `{"model":"gpt-zero-usage-test","messages":[{"role":"user","content":"hi"}]}`
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
		t.Fatalf("expected upstream success to pass through, got %d body=%s", resp.StatusCode, string(body))
	}

	var bill database.BillingEntry
	if err := database.DB.Where("model_name = ? AND billing_state = ?", "gpt-zero-usage-test", database.BillingStateUpstreamUnmetered).First(&bill).Error; err != nil {
		t.Fatalf("expected upstream_unmetered billing entry: %v", err)
	}
	if bill.AmountUSD != 0 || bill.EntryType != database.BillingTypeApiUsagePendingReconcile {
		t.Fatalf("unexpected billing entry: %+v", bill)
	}
	var logRow database.ApiLog
	if err := database.DB.Where("model_name = ?", "gpt-zero-usage-test").First(&logRow).Error; err != nil {
		t.Fatalf("expected api log: %v", err)
	}
	if logRow.Status != 200 || logRow.ErrorType != "upstream_unmetered" || logRow.Cost != 0 {
		t.Fatalf("unexpected api log: %+v", logRow)
	}
}

func TestNonStreamCostCalculationFailureReturns502WithoutBilling(t *testing.T) {
	app, cleanup := setupBillingFormulaTest(t, "gpt-cost-invalid-test", `{
		"choices":[{"message":{"content":"ok"}}],
		"usage":{"prompt_tokens":10,"completion_tokens":5}
	}`, &database.ChannelModel{
		ChannelID:               1,
		Weight:                  1,
		InputPricePicoPerToken:  database.MaxChannelModelPricePicoPerToken + 1,
		OutputPricePicoPerToken: pricePicoForTest(1),
		ModerationLevel:         "off",
		ModerationFailMode:      "open",
	})
	defer cleanup()

	payload := `{"model":"gpt-cost-invalid-test","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-billing-formula")
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 502 {
		t.Fatalf("expected 502 for invalid cost, got %d body=%s", resp.StatusCode, string(body))
	}
	var count int64
	database.DB.Model(&database.BillingEntry{}).Where("model_name = ?", "gpt-cost-invalid-test").Count(&count)
	if count != 0 {
		t.Fatalf("non-stream invalid cost must not write billing entries, got %d", count)
	}
}

func TestStreamCostCalculationFailureWritesPendingReconcile(t *testing.T) {
	app, cleanup := setupStreamStateTest(t, "gpt-stream-cost-invalid", "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ndata: {\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5}}\n\ndata: [DONE]\n\n", &database.ChannelModel{
		ChannelID:               1,
		Weight:                  1,
		InputPricePicoPerToken:  database.MaxChannelModelPricePicoPerToken + 1,
		OutputPricePicoPerToken: pricePicoForTest(1),
		ModerationLevel:         "off",
		ModerationFailMode:      "open",
	})
	defer cleanup()

	resp := invokeStreamStateRequest(t, app, "gpt-stream-cost-invalid")
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("stream status should remain 200 after delivery, got %d body=%s", resp.StatusCode, string(body))
	}
	resp.Body.Close()

	var bill database.BillingEntry
	if err := database.DB.Where("model_name = ? AND billing_state = ?", "gpt-stream-cost-invalid", database.BillingStatePendingReconcile).First(&bill).Error; err != nil {
		t.Fatalf("expected pending_reconcile billing entry: %v", err)
	}
	if bill.AmountUSD != 0 || bill.EstimatedCostUSD <= 0 {
		t.Fatalf("unexpected pending billing entry: %+v", bill)
	}
}

func TestStreamClientDisconnectBeforeUsageWritesPendingReconcile(t *testing.T) {
	var err error
	database.DB, err = gorm.Open(sqlite.Open("file:stream-disconnect-pending?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	if err := database.DB.AutoMigrate(&database.User{}, &database.ApiLog{}, &database.UserSubscription{},
		&database.SubscriptionUsage{}, &database.Channel{}, &database.ChannelModel{}, &database.BillingEntry{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	user := database.User{ID: 101, Username: "stream-disconnect", Token: "sk-stream-disconnect", Quota: 100 * database.MicroPerUSD, Status: 1, BalanceConsumeEnabled: true}
	if err := database.DB.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	AuthCache = map[string]*database.User{user.Token: &user}
	AuthTokenCache = map[string]*database.AccessToken{}
	RouteCache = map[string][]*database.ChannelModel{}
	ChannelMapCache = map[uint]*database.Channel{}

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Request-Id", "req-disconnect-test")
		w.WriteHeader(200)
		flusher, _ := w.(http.Flusher)
		for i := 0; i < 100; i++ {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"chunk-%d\"}}]}\n\n", i)
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(5 * time.Millisecond)
		}
	}))
	defer backend.Close()
	ChannelMapCache[1] = &database.Channel{ID: 1, Type: ChannelTypeOpenAI, BaseURL: backend.URL, Key: "upstream-key"}
	RouteCache["gpt-disconnect-pending"] = []*database.ChannelModel{{
		ChannelID:               1,
		Weight:                  1,
		InputPricePicoPerToken:  pricePicoForTest(1),
		OutputPricePicoPerToken: pricePicoForTest(1),
		ModerationLevel:         "off",
		ModerationFailMode:      "open",
	}}

	app := fiber.New()
	app.Post("/v1/chat/completions", ChatCompletionProxyHandler)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- app.Listener(ln)
	}()
	defer func() {
		_ = app.Shutdown()
		<-errCh
	}()

	payload := `{"model":"gpt-disconnect-pending","messages":[{"role":"user","content":"hi"}],"stream":true}`
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial app: %v", err)
	}
	_, _ = fmt.Fprintf(conn, "POST /v1/chat/completions HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nAuthorization: Bearer %s\r\nContent-Length: %d\r\n\r\n%s",
		ln.Addr().String(), user.Token, len(payload), payload)
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	reader := bufio.NewReader(conn)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read response header: %v", err)
		}
		if line == "\r\n" {
			break
		}
	}
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatalf("read first stream line: %v", err)
	}
	_ = conn.Close()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var bill database.BillingEntry
		err := database.DB.Where("model_name = ? AND billing_state = ?", "gpt-disconnect-pending", database.BillingStatePendingReconcile).First(&bill).Error
		if err == nil {
			if bill.DeliveredBytes <= 0 || bill.EstimatedInputTokens <= 0 || bill.EstimatedCostUSD <= 0 || bill.RequestID == "" {
				t.Fatalf("pending entry missing reconcile facts: %+v", bill)
			}
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("expected pending_reconcile billing entry after client disconnect")
}

func TestGetTransportEdge(t *testing.T) {
	tr1 := getTransport("")                      // Default
	tr2 := getTransport("http://127.0.0.1:1080") // Custom
	tr3 := getTransport("http://127.0.0.1:1080") // Cache
	if tr1 == nil || tr2 == nil || tr3 == nil {
		t.Error("Transport is nil")
	}
}

func TestTransportCache_NoDoubleWrite(t *testing.T) {
	transportCache = sync.Map{}
	const proxyURL = "http://127.0.0.1:1080"
	const workers = 128

	start := make(chan struct{})
	results := make(chan *http.Transport, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- getTransport(proxyURL)
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	seen := make(map[*http.Transport]struct{})
	for tr := range results {
		seen[tr] = struct{}{}
	}
	if len(seen) != 1 {
		t.Fatalf("getTransport returned %d transport pointers for one cache key, want 1", len(seen))
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
		ChannelID:                        1,
		Weight:                           1,
		InputPricePicoPerToken:           pricePicoForTest(1),
		OutputPricePicoPerToken:          pricePicoForTest(10),
		CacheWriteInputPricePicoPerToken: pricePicoForTest(1.25),
		ModerationLevel:                  "off",
		ModerationFailMode:               "open",
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
		ChannelID:                          1,
		Weight:                             1,
		InputPricePicoPerToken:             pricePicoForTest(5),
		CachedInputPricePicoPerToken:       pricePicoForTest(0.5),
		CacheWriteInputPricePicoPerToken:   pricePicoForTest(6.25),
		CacheWrite1hInputPricePicoPerToken: pricePicoForTest(10),
		OutputPricePicoPerToken:            pricePicoForTest(25),
		ModerationLevel:                    "off",
		ModerationFailMode:                 "open",
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
		ChannelID:                        1,
		Weight:                           1,
		InputPricePicoPerToken:           pricePicoForTest(2.5),
		CachedInputPricePicoPerToken:     pricePicoForTest(0.25),
		OutputPricePicoPerToken:          pricePicoForTest(15),
		ContextPriceThreshold:            272000,
		HighInputPricePicoPerToken:       pricePicoForTest(5),
		HighCachedInputPricePicoPerToken: pricePicoForTest(0.5),
		HighOutputPricePicoPerToken:      pricePicoForTest(22.5),
		ModerationLevel:                  "off",
		ModerationFailMode:               "open",
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
		ChannelID:                   1,
		Weight:                      1,
		InputPricePicoPerToken:      pricePicoForTest(1),
		OutputPricePicoPerToken:     pricePicoForTest(2),
		ContextPriceThreshold:       272000,
		HighInputPricePicoPerToken:  pricePicoForTest(10),
		HighOutputPricePicoPerToken: pricePicoForTest(20),
		ModerationLevel:             "off",
		ModerationFailMode:          "open",
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

func setupStreamStateTest(t *testing.T, modelName, upstreamBody string, route *database.ChannelModel) (*fiber.App, func()) {
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
	user := database.User{ID: 100, Username: "stream-state", Token: "sk-stream-state", Quota: 100 * database.MicroPerUSD, Status: 1, BalanceConsumeEnabled: true}
	if err := database.DB.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	AuthCache = map[string]*database.User{user.Token: &user}
	AuthTokenCache = map[string]*database.AccessToken{}
	RouteCache = map[string][]*database.ChannelModel{}
	ChannelMapCache = map[uint]*database.Channel{}

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Request-Id", "req-state-test")
		w.WriteHeader(200)
		w.Write([]byte(upstreamBody))
	}))
	ChannelMapCache[1] = &database.Channel{ID: 1, Type: ChannelTypeOpenAI, BaseURL: backend.URL, Key: "upstream-key"}
	RouteCache[modelName] = []*database.ChannelModel{route}

	app := fiber.New()
	app.Post("/v1/chat/completions", ChatCompletionProxyHandler)
	return app, backend.Close
}

func invokeStreamStateRequest(t *testing.T, app *fiber.App, modelName string) *http.Response {
	t.Helper()
	payload := `{"model":"` + modelName + `","messages":[{"role":"user","content":"hi"}],"stream":true}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-stream-state")
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	return resp
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
