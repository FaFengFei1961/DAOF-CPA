package proxy

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"daof-cpa/database"

	"github.com/gofiber/fiber/v2"
	gorillaws "github.com/gorilla/websocket"
)

// 帮助函数：启动一个监听本地随机端口的 fiber app，返回 ws:// 基地址 + cleanup。
// 用真实 net.Listener 而非 app.Test 是因为 WebSocket 升级需要真正的 TCP socket，
// fasthttp 内置 Test mode 不模拟 hijack。
func startFiberWithWebsocket(t *testing.T) (string, *fiber.App) {
	t.Helper()
	app := fiber.New()
	app.Get("/v1/responses", ResponsesWebsocketProxyHandler)
	app.Get("/backend-api/codex/responses", ResponsesWebsocketProxyHandler)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ready := make(chan struct{})
	go func() {
		close(ready)
		_ = app.Listener(ln)
	}()
	<-ready
	t.Cleanup(func() {
		_ = app.Shutdown()
	})
	return "ws://" + ln.Addr().String(), app
}

// 帮助函数：启动模拟上游 CPA WebSocket 服务器。handler 在每个连接被升级后调用。
// 通过 strings.HasPrefix 把 http:// → ws:// 转换由调用方负责。
func startMockUpstreamCPA(t *testing.T, handler func(conn *gorillaws.Conn)) string {
	t.Helper()
	upgrader := gorillaws.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		handler(conn)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestResponsesWebsocket_RejectsNonUpgrade(t *testing.T) {
	app := fiber.New()
	app.Get("/v1/responses", ResponsesWebsocketProxyHandler)
	req := httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	req.Header.Set("Authorization", "Bearer doesnotmatter")
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("test request: %v", err)
	}
	if resp.StatusCode != http.StatusUpgradeRequired {
		t.Errorf("status=%d want 426", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "ERR_RESPONSES_WEBSOCKET_REQUIRED") {
		t.Errorf("body=%s want ERR_RESPONSES_WEBSOCKET_REQUIRED", body)
	}
}

func TestResponsesWebsocket_RejectsInvalidToken(t *testing.T) {
	setupImageGenerationTest(t)
	base, _ := startFiberWithWebsocket(t)
	dialer := gorillaws.DefaultDialer
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer nonexistent-token")
	_, resp, err := dialer.Dial(base+"/v1/responses", hdr)
	if err == nil {
		t.Fatal("expected dial to fail with 401")
	}
	if resp == nil {
		t.Fatalf("no http response: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status=%d want 401 (err=%v)", resp.StatusCode, err)
	}
}

func TestResponsesWebsocket_RejectsNoChannel(t *testing.T) {
	db := setupImageGenerationTest(t)
	user := database.User{ID: 701, Username: "ws-no-channel", Token: "sk-ws-no-channel", Status: 1, Quota: 1_000_000, BalanceConsumeEnabled: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	AuthCache[user.Token] = &user
	// RouteCache 是空的——预期 503
	base, _ := startFiberWithWebsocket(t)
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+user.Token)
	_, resp, err := gorillaws.DefaultDialer.Dial(base+"/v1/responses", hdr)
	if err == nil {
		t.Fatal("expected dial to fail with 503")
	}
	if resp == nil {
		t.Fatalf("no http response: %v", err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status=%d want 503", resp.StatusCode)
	}
}

func TestResponsesWebsocket_RejectsSuspendedUser(t *testing.T) {
	db := setupImageGenerationTest(t)
	user := database.User{ID: 702, Username: "ws-suspended", Token: "sk-ws-suspended", Status: 1, Quota: 1_000_000, BalanceConsumeEnabled: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	// GORM default:1 在 Create 时把 Status:0 升为 1，所以这里 Create 后再强行降到 0 模拟封禁。
	user.Status = 0
	AuthCache[user.Token] = &user
	base, _ := startFiberWithWebsocket(t)
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+user.Token)
	_, resp, err := gorillaws.DefaultDialer.Dial(base+"/v1/responses", hdr)
	if err == nil {
		t.Fatal("expected dial to fail with 403")
	}
	if resp == nil {
		t.Fatalf("no http response: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status=%d want 403", resp.StatusCode)
	}
}

func TestResponsesWebsocket_RejectsZeroQuota(t *testing.T) {
	db := setupImageGenerationTest(t)
	user := database.User{ID: 703, Username: "ws-zero", Token: "sk-ws-zero", Status: 1, Quota: 0, BalanceConsumeEnabled: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	AuthCache[user.Token] = &user
	// 配上 WS 渠道——但用户零额度应该被拦在握手期
	ChannelMapCache[71] = &database.Channel{ID: 71, Type: ChannelTypeCLIProxy, BaseURL: "http://placeholder", Key: "k", Status: 1}
	RouteCache["gpt-5-codex"] = []*database.ChannelModel{{
		ID: 71, ChannelID: 71, ModelID: "gpt-5-codex",
		ModelCategory: database.ModelCategoryText, BillingMode: database.BillingModeToken,
		AllowedEndpoints: `["/v1/responses/ws"]`, Weight: 1, Status: 1,
	}}
	base, _ := startFiberWithWebsocket(t)
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+user.Token)
	_, resp, err := gorillaws.DefaultDialer.Dial(base+"/v1/responses", hdr)
	if err == nil {
		t.Fatal("expected dial to fail with 402")
	}
	if resp == nil {
		t.Fatalf("no http response: %v", err)
	}
	if resp.StatusCode != http.StatusPaymentRequired {
		t.Errorf("status=%d want 402", resp.StatusCode)
	}
}

func TestSelectResponsesWebsocketChannel_WeightedRandom(t *testing.T) {
	setupImageGenerationTest(t)
	// 两个 channel 都支持 WS，但只有一个健康
	ChannelMapCache[81] = &database.Channel{ID: 81, Type: ChannelTypeCLIProxy, BaseURL: "http://a", Key: "ka", Status: 1}
	ChannelMapCache[82] = &database.Channel{ID: 82, Type: ChannelTypeGemini, BaseURL: "http://b", Key: "kb", Status: 1} // gemini 不支持 WS
	RouteCache["gpt-5"] = []*database.ChannelModel{
		{ID: 81, ChannelID: 81, ModelID: "gpt-5", ModelCategory: database.ModelCategoryText, BillingMode: database.BillingModeToken,
			AllowedEndpoints: `["/v1/responses/ws"]`, Weight: 10, Status: 1},
		{ID: 82, ChannelID: 82, ModelID: "gpt-5", ModelCategory: database.ModelCategoryText, BillingMode: database.BillingModeToken,
			AllowedEndpoints: `["/v1/responses/ws"]`, Weight: 10, Status: 1},
	}
	sel, err := selectResponsesWebsocketChannel()
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if sel.Channel.ID != 81 {
		t.Errorf("selected channel=%d want 81 (only cliproxy/codex types are eligible)", sel.Channel.ID)
	}
}

func TestSelectResponsesWebsocketChannel_NoCandidates(t *testing.T) {
	setupImageGenerationTest(t)
	// RouteCache 没人开 WS 端点
	ChannelMapCache[91] = &database.Channel{ID: 91, Type: ChannelTypeCLIProxy, BaseURL: "http://x", Key: "k", Status: 1}
	RouteCache["gpt-5"] = []*database.ChannelModel{{
		ID: 91, ChannelID: 91, ModelID: "gpt-5", ModelCategory: database.ModelCategoryText,
		AllowedEndpoints: `[]`, Status: 1,
	}}
	_, err := selectResponsesWebsocketChannel()
	if err == nil {
		t.Fatal("expected error when no channel allows ws endpoint")
	}
}

func TestBuildUpstreamWebsocketURL(t *testing.T) {
	cases := []struct {
		base    string
		path    string
		want    string
		wantErr bool
	}{
		{"http://cpa.local:7080", "/v1/responses", "ws://cpa.local:7080/v1/responses", false},
		{"https://cpa.example.com", "/backend-api/codex/responses", "wss://cpa.example.com/backend-api/codex/responses", false},
		{"http://cpa.local/api", "/v1/responses", "ws://cpa.local/api/v1/responses", false},
		{"http://cpa.local/", "v1/responses", "ws://cpa.local/v1/responses", false},
		{"ws://cpa.local", "/v1/responses", "ws://cpa.local/v1/responses", false},
		{"", "/v1/responses", "", true},
		{"ftp://invalid", "/v1/responses", "", true},
	}
	for _, tc := range cases {
		got, err := buildUpstreamWebsocketURL(tc.base, tc.path)
		if tc.wantErr {
			if err == nil {
				t.Errorf("base=%q path=%q want error got %q", tc.base, tc.path, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("base=%q path=%q unexpected err: %v", tc.base, tc.path, err)
			continue
		}
		if got != tc.want {
			t.Errorf("base=%q path=%q got=%q want=%q", tc.base, tc.path, got, tc.want)
		}
	}
}

func TestChannelModelAllowsResponsesWebsocket(t *testing.T) {
	cases := []struct {
		name     string
		cm       *database.ChannelModel
		want     bool
	}{
		{"nil", nil, false},
		{"inactive", &database.ChannelModel{Status: 0, AllowedEndpoints: `["/v1/responses/ws"]`}, false},
		{"empty endpoints", &database.ChannelModel{Status: 1, ModelCategory: database.ModelCategoryText, AllowedEndpoints: `[]`}, false},
		{"text default", &database.ChannelModel{Status: 1, ModelCategory: database.ModelCategoryText, AllowedEndpoints: ""}, false},
		{"only ws endpoint", &database.ChannelModel{Status: 1, AllowedEndpoints: `["/v1/responses/ws"]`}, true},
		{"ws + native", &database.ChannelModel{Status: 1, AllowedEndpoints: `["/v1beta/models","/v1/responses/ws"]`}, true},
		{"only native", &database.ChannelModel{Status: 1, AllowedEndpoints: `["/v1beta/models"]`}, false},
	}
	for _, tc := range cases {
		got := ChannelModelAllowsResponsesWebsocket(tc.cm)
		if got != tc.want {
			t.Errorf("%s: got=%v want=%v", tc.name, got, tc.want)
		}
	}
}

// 端到端测试：完整 WebSocket 握手 + bidirectional pump + billing on response.completed
func TestResponsesWebsocket_FullRoundTripWithBilling(t *testing.T) {
	db := setupImageGenerationTest(t)

	// 模拟上游 CPA：echo response.create 并发送 response.completed（含 usage）
	upstreamHits := &sync.WaitGroup{}
	upstreamHits.Add(1)
	gotAuthHeader := ""
	upstream := startMockUpstreamCPA(t, func(conn *gorillaws.Conn) {
		// 注意：upgrader 升级后 r.Header 已经被消费——但 conn 不直接访问原始头
		// 我们改用 r.Header.Get 方式必须在升级前提取，这里通过 wrap 来获取
		// （简化处理：直接从握手 hook 里取）
		// 上游收到 client 帧
		mt, payload, err := conn.ReadMessage()
		if err != nil {
			t.Errorf("upstream read: %v", err)
			return
		}
		if mt != gorillaws.TextMessage {
			t.Errorf("upstream got msgType=%d want text", mt)
		}
		if !strings.Contains(string(payload), `"type":"response.create"`) {
			t.Errorf("upstream got payload=%s want response.create", payload)
		}
		// 回 response.completed
		completed := `{"type":"response.completed","response":{"id":"rsp_1","model":"gpt-5-codex","usage":{"input_tokens":100,"output_tokens":50}}}`
		if err := conn.WriteMessage(gorillaws.TextMessage, []byte(completed)); err != nil {
			t.Errorf("upstream write: %v", err)
		}
		upstreamHits.Done()
		// 上游主动关
	})
	// 通过 wrap 拿到 Authorization
	upstreamWithAuth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthHeader = r.Header.Get("Authorization")
		upgrader := gorillaws.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_, payload, _ := conn.ReadMessage()
		if !strings.Contains(string(payload), `"type":"response.create"`) {
			t.Errorf("upstream got payload=%s want response.create", payload)
		}
		completed := `{"type":"response.completed","response":{"id":"rsp_1","model":"gpt-5-codex","usage":{"input_tokens":100,"output_tokens":50}}}`
		_ = conn.WriteMessage(gorillaws.TextMessage, []byte(completed))
	}))
	t.Cleanup(upstreamWithAuth.Close)
	_ = upstream // 留着备用

	user := database.User{ID: 801, Username: "ws-bill", Token: "sk-ws-bill", Status: 1, Quota: 10_000_000, BalanceConsumeEnabled: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	AuthCache[user.Token] = &user

	ChannelMapCache[101] = &database.Channel{ID: 101, Type: ChannelTypeCLIProxy, BaseURL: upstreamWithAuth.URL, Key: "upstream-key", Status: 1}
	RouteCache["gpt-5-codex"] = []*database.ChannelModel{{
		ID: 101, ChannelID: 101, ModelID: "gpt-5-codex",
		ModelCategory:           database.ModelCategoryText,
		BillingMode:             database.BillingModeToken,
		AllowedEndpoints:        `["/v1/responses/ws"]`,
		InputPricePicoPerToken:  1250 * database.PicoPerTokenPerUSDPerMTok / 1000, // $1.25 / 1M
		OutputPricePicoPerToken: 10_000 * database.PicoPerTokenPerUSDPerMTok / 1000, // $10 / 1M
		Weight:                  1,
		Status:                  1,
	}}

	base, _ := startFiberWithWebsocket(t)
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+user.Token)
	client, resp, err := gorillaws.DefaultDialer.Dial(base+"/v1/responses", hdr)
	if err != nil {
		t.Fatalf("dial: %v (resp=%v)", err, resp)
	}
	defer client.Close()

	create := `{"type":"response.create","model":"gpt-5-codex","prompt":"hi"}`
	if err := client.WriteMessage(gorillaws.TextMessage, []byte(create)); err != nil {
		t.Fatalf("client write: %v", err)
	}

	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	mt, payload, err := client.ReadMessage()
	if err != nil {
		t.Fatalf("client read: %v", err)
	}
	if mt != gorillaws.TextMessage {
		t.Errorf("got msgType=%d want text", mt)
	}
	if !strings.Contains(string(payload), `"type":"response.completed"`) {
		t.Errorf("payload=%s want response.completed", payload)
	}

	// 收到 completed 后给计费 goroutine 充分时间扣费
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var fresh database.User
		if err := db.First(&fresh, user.ID).Error; err == nil && fresh.Quota < 10_000_000 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// 计费校验：input=100×$1.25/M + output=50×$10/M = $0.000125 + $0.0005 = $0.000625 = 625 micro_usd
	var fresh database.User
	if err := db.First(&fresh, user.ID).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	consumed := int64(10_000_000) - fresh.Quota
	if consumed < 500 || consumed > 800 {
		t.Fatalf("quota consumed=%d want ~625 (100×$1.25/M + 50×$10/M); fresh.Quota=%d", consumed, fresh.Quota)
	}

	// ApiLog 应当有一行
	var apiLog database.ApiLog
	if err := db.Where("model_name = ?", "gpt-5-codex").First(&apiLog).Error; err != nil {
		t.Fatalf("load api log: %v", err)
	}
	if apiLog.PromptTokens != 100 || apiLog.CompletionTokens != 50 {
		t.Errorf("api log tokens=(%d,%d) want (100,50)", apiLog.PromptTokens, apiLog.CompletionTokens)
	}
	if apiLog.RequestPath != "/v1/responses" {
		t.Errorf("api log path=%q want /v1/responses", apiLog.RequestPath)
	}

	// ApiLogUsageLine 应当有 input + output 两行
	var lines []database.ApiLogUsageLine
	if err := db.Where("api_log_id = ?", apiLog.ID).Find(&lines).Error; err != nil {
		t.Fatalf("load usage lines: %v", err)
	}
	if len(lines) != 2 {
		t.Errorf("usage lines len=%d want 2", len(lines))
	}

	// BillingEntry 应当有一行 api_consume_balance（用户走余额）
	var entries []database.BillingEntry
	if err := db.Where("user_id = ?", user.ID).Find(&entries).Error; err != nil {
		t.Fatalf("load billing entries: %v", err)
	}
	if len(entries) == 0 {
		t.Errorf("no billing entry written")
	}
	sawBalance := false
	for _, e := range entries {
		if e.EntryType == database.BillingTypeApiConsumeBalance {
			sawBalance = true
		}
	}
	if !sawBalance {
		t.Errorf("no api_consume_balance entry; entries=%+v", entries)
	}

	// 上游收到 Authorization 应该是 channel key
	if gotAuthHeader != "Bearer upstream-key" {
		t.Errorf("upstream got auth=%q want Bearer upstream-key", gotAuthHeader)
	}
}

// 测试 upstream 不返 usage 时写 pending_reconcile
func TestResponsesWebsocket_NoUsageWritesPendingReconcile(t *testing.T) {
	db := setupImageGenerationTest(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := gorillaws.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_, _, _ = conn.ReadMessage()
		// 故意不带 usage
		_ = conn.WriteMessage(gorillaws.TextMessage, []byte(`{"type":"response.completed","response":{"id":"rsp_no_usage","model":"gpt-5-codex"}}`))
	}))
	t.Cleanup(upstream.Close)

	user := database.User{ID: 802, Username: "ws-nou", Token: "sk-ws-nou", Status: 1, Quota: 5_000_000, BalanceConsumeEnabled: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	AuthCache[user.Token] = &user
	ChannelMapCache[111] = &database.Channel{ID: 111, Type: ChannelTypeCLIProxy, BaseURL: upstream.URL, Key: "k", Status: 1}
	RouteCache["gpt-5-codex"] = []*database.ChannelModel{{
		ID: 111, ChannelID: 111, ModelID: "gpt-5-codex",
		ModelCategory:    database.ModelCategoryText,
		BillingMode:      database.BillingModeToken,
		AllowedEndpoints: `["/v1/responses/ws"]`,
		Weight:           1,
		Status:           1,
	}}

	base, _ := startFiberWithWebsocket(t)
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+user.Token)
	client, _, err := gorillaws.DefaultDialer.Dial(base+"/v1/responses", hdr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	_ = client.WriteMessage(gorillaws.TextMessage, []byte(`{"type":"response.create","model":"gpt-5-codex"}`))
	_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _, _ = client.ReadMessage()

	// 收尾审计写入需要时间
	deadline := time.Now().Add(2 * time.Second)
	var entries []database.BillingEntry
	for time.Now().Before(deadline) {
		_ = db.Where("user_id = ?", user.ID).Find(&entries).Error
		if len(entries) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	sawPending := false
	for _, e := range entries {
		if e.EntryType == database.BillingTypeApiUsagePendingReconcile && e.BillingState == database.BillingStatePendingReconcile {
			sawPending = true
		}
	}
	if !sawPending {
		t.Errorf("expected pending_reconcile entry; entries=%+v", entries)
	}

	// 余额不应被扣
	var fresh database.User
	_ = db.First(&fresh, user.ID).Error
	if fresh.Quota != 5_000_000 {
		t.Errorf("quota changed to %d (should remain 5_000_000)", fresh.Quota)
	}
}

// 测试 ParseChannelCustomHeaders helper
func TestParseChannelCustomHeaders(t *testing.T) {
	cases := []struct {
		raw     string
		want    map[string]string
		wantNil bool
	}{
		{"", nil, true},
		{`{"X-Foo":"bar"}`, map[string]string{"X-Foo": "bar"}, false},
		{`invalid`, nil, true},
	}
	for _, tc := range cases {
		got := parseChannelCustomHeaders(tc.raw)
		if tc.wantNil {
			if got != nil {
				t.Errorf("raw=%q got=%v want nil", tc.raw, got)
			}
			continue
		}
		gotJSON, _ := json.Marshal(got)
		wantJSON, _ := json.Marshal(tc.want)
		if string(gotJSON) != string(wantJSON) {
			t.Errorf("raw=%q got=%s want=%s", tc.raw, gotJSON, wantJSON)
		}
	}
}

// 测试 wsRandIntN 通过 mock 重放
func TestSelectResponsesWebsocketChannel_WeightedRandomDistribution(t *testing.T) {
	setupImageGenerationTest(t)
	ChannelMapCache[121] = &database.Channel{ID: 121, Type: ChannelTypeCLIProxy, BaseURL: "http://a", Key: "ka", Status: 1}
	ChannelMapCache[122] = &database.Channel{ID: 122, Type: ChannelTypeCodex, BaseURL: "http://b", Key: "kb", Status: 1}
	RouteCache["gpt-5"] = []*database.ChannelModel{
		{ID: 121, ChannelID: 121, ModelID: "gpt-5", ModelCategory: database.ModelCategoryText, BillingMode: database.BillingModeToken,
			AllowedEndpoints: `["/v1/responses/ws"]`, Weight: 1, Status: 1},
		{ID: 122, ChannelID: 122, ModelID: "gpt-5", ModelCategory: database.ModelCategoryText, BillingMode: database.BillingModeToken,
			AllowedEndpoints: `["/v1/responses/ws"]`, Weight: 1, Status: 1},
	}
	// Mock rand：始终返回 0 → 第一个候选；返回 totalWeight-1 → 最后一个
	origRand := wsRandIntN
	t.Cleanup(func() { wsRandIntN = origRand })

	wsRandIntN = func(n int) int { return 0 }
	sel, err := selectResponsesWebsocketChannel()
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	firstPickID := sel.Channel.ID
	wsRandIntN = func(n int) int { return n - 1 }
	sel, err = selectResponsesWebsocketChannel()
	if err != nil {
		t.Fatalf("select 2: %v", err)
	}
	if sel.Channel.ID == firstPickID {
		t.Errorf("both random extremes returned same channel id=%d", firstPickID)
	}
}

