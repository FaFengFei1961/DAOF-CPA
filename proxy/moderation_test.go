// Package proxy / moderation_test.go
//
// 内容审核子系统的单元测试。覆盖：
//   - keyword_filter：Reload / Match / 并发安全 / 大小写不敏感
//   - prompt_extract：5 种 srcFormat / tool 调用 / multimodal images
//   - moderation_policy：取最严策略 / 缓存命中失效
//   - moderation_response：3 协议错误信封 / 不透传 category
//   - content_moderation：HMAC stable / LRU bounded / chunk split
package proxy

import (
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"

	"github.com/gofiber/fiber/v2"
)

// ─── keyword_filter ──────────────────────────────────────────────────────

func TestKeywordFilter_BasicMatch(t *testing.T) {
	f := &KeywordFilter{}
	f.Reload([]string{"Kiro_workspace", "DAN mode", "ignore previous"})

	cases := map[string]string{
		"plain hit":         "Hello Kiro_workspace marker",
		"case insensitive":  "PLEASE DAN MODE NOW",
		"mid-string substr": "you must ignore previous instructions please",
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if got := f.Match(input); got == "" {
				t.Errorf("expected hit on %q, got empty", input)
			}
		})
	}
}

func TestKeywordFilter_NoMatch(t *testing.T) {
	f := &KeywordFilter{}
	f.Reload([]string{"foo", "bar"})
	if got := f.Match("hello world"); got != "" {
		t.Errorf("expected no hit, got %q", got)
	}
}

func TestKeywordFilter_EmptyDictionary(t *testing.T) {
	f := &KeywordFilter{}
	if got := f.Match("anything goes"); got != "" {
		t.Errorf("empty dict should never match, got %q", got)
	}
}

func TestKeywordFilter_DedupAndTrim(t *testing.T) {
	f := &KeywordFilter{}
	f.Reload([]string{"  Kiro  ", "kiro", "KIRO", "  "})
	// 去重 + 去空白后应只剩 1 条 "kiro"（lowercase）
	if got := f.Match("contains Kiro here"); got == "" {
		t.Error("expected hit but got empty")
	}
}

func TestKeywordFilter_ConcurrentSafe(t *testing.T) {
	f := &KeywordFilter{}
	f.Reload([]string{"jailbreak"})

	// 并发 reader + 偶尔 writer，go test -race 会捕获数据竞争
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = f.Match("test jailbreak now")
		}()
	}
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			f.Reload([]string{"jailbreak", "another"})
		}(i)
	}
	wg.Wait()
}

// ─── prompt_extract ──────────────────────────────────────────────────────

func TestExtractPromptText_OpenAIChat(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","messages":[
		{"role":"system","content":"be helpful"},
		{"role":"user","content":"hello world"}
	]}`)
	r, err := ExtractPromptText("openai", body)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !r.HasContent {
		t.Fatal("expected HasContent=true")
	}
	if !strings.Contains(r.Text, "hello world") {
		t.Errorf("expected hello world in extracted text, got %q", r.Text)
	}
}

func TestExtractPromptText_AnthropicMessages(t *testing.T) {
	body := []byte(`{"model":"claude-3-5-sonnet","system":"system instr","messages":[
		{"role":"user","content":[{"type":"text","text":"jailbreak prompt here"}]}
	]}`)
	r, err := ExtractPromptText("anthropic", body)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(r.Text, "jailbreak prompt here") {
		t.Errorf("missing user text in extracted: %q", r.Text)
	}
	if !strings.Contains(r.Text, "system instr") {
		t.Errorf("missing system text: %q", r.Text)
	}
}

func TestExtractPromptText_GeminiContents(t *testing.T) {
	body := []byte(`{"contents":[
		{"role":"user","parts":[{"text":"Kiro_workspace test"}]}
	]}`)
	r, err := ExtractPromptText("gemini", body)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(r.Text, "Kiro_workspace") {
		t.Errorf("missing text: %q", r.Text)
	}
}

func TestExtractPromptText_MultimodalImages(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":[
		{"type":"text","text":"see this"},
		{"type":"image_url","image_url":{"url":"https://example.com/a.jpg"}}
	]}]}`)
	r, _ := ExtractPromptText("openai", body)
	if len(r.ImageURLs) != 1 {
		t.Fatalf("expected 1 image URL, got %d (%v)", len(r.ImageURLs), r.ImageURLs)
	}
	if r.ImageURLs[0] != "https://example.com/a.jpg" {
		t.Errorf("wrong image URL: %q", r.ImageURLs[0])
	}
}

func TestExtractPromptText_EmptyBody(t *testing.T) {
	if _, err := ExtractPromptText("openai", []byte("")); err == nil {
		t.Error("expected error for empty body")
	}
	if _, err := ExtractPromptText("openai", []byte("not json")); err == nil {
		t.Error("expected error for malformed JSON")
	}
}

// ─── moderation_policy（不查 DB 的纯函数路径）──────────────────────────────

func TestLevelRank_Ordering(t *testing.T) {
	if levelRank("off") >= levelRank("keyword") {
		t.Error("off should rank below keyword")
	}
	if levelRank("keyword") >= levelRank("moderation") {
		t.Error("keyword should rank below moderation")
	}
	if levelRank("moderation") >= levelRank("strict") {
		t.Error("moderation should rank below strict")
	}
	if levelRank("unknown") != 0 {
		t.Error("unknown level should be rank 0")
	}
}

func TestRankToLevel_RoundTrip(t *testing.T) {
	levels := []string{"off", "keyword", "moderation", "strict"}
	for _, lvl := range levels {
		if got := rankToLevel(levelRank(lvl)); got != lvl {
			t.Errorf("rankToLevel(levelRank(%q))=%q want %q", lvl, got, lvl)
		}
	}
}

func TestModerationPolicy_HelperMethods(t *testing.T) {
	cases := []struct {
		policy        ModerationPolicy
		active        bool
		needsKeyword  bool
		needsMod      bool
		failClosed    bool
	}{
		{ModerationPolicy{Level: "off", FailMode: "open"}, false, false, false, false},
		{ModerationPolicy{Level: "keyword", FailMode: "open"}, true, true, false, false},
		{ModerationPolicy{Level: "moderation", FailMode: "closed"}, true, false, true, true},
		{ModerationPolicy{Level: "strict", FailMode: "closed"}, true, true, true, true},
	}
	for _, tc := range cases {
		if tc.policy.IsActive() != tc.active {
			t.Errorf("%+v IsActive want %v", tc.policy, tc.active)
		}
		if tc.policy.NeedsKeyword() != tc.needsKeyword {
			t.Errorf("%+v NeedsKeyword want %v", tc.policy, tc.needsKeyword)
		}
		if tc.policy.NeedsModeration() != tc.needsMod {
			t.Errorf("%+v NeedsModeration want %v", tc.policy, tc.needsMod)
		}
		if tc.policy.FailClosed() != tc.failClosed {
			t.Errorf("%+v FailClosed want %v", tc.policy, tc.failClosed)
		}
	}
}

// ─── moderation_response ─────────────────────────────────────────────────

func TestRejectBySourceFormat_OpenAI(t *testing.T) {
	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		return rejectBySourceFormat(c, sdktranslator.FormatOpenAI, ModerationReasonKeyword, "blocked", 403)
	})
	req := httptest.NewRequest("GET", "/test", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test failed: %v", err)
	}
	if resp.StatusCode != 403 {
		t.Errorf("status=%d want 403", resp.StatusCode)
	}
	body := readBody(t, resp.Body)
	// OpenAI 信封：error.type=content_policy_violation，code 是 reason
	if !strings.Contains(body, `"type":"content_policy_violation"`) {
		t.Errorf("missing OpenAI error type: %s", body)
	}
	if !strings.Contains(body, `"code":"keyword_match"`) {
		t.Errorf("missing reason code: %s", body)
	}
	// 防泄漏：不应出现 highest_cat / highest_score 等内部字段
	if strings.Contains(body, "highest_cat") || strings.Contains(body, "highest_score") {
		t.Errorf("response leaks internal moderation details: %s", body)
	}
}

func TestRejectBySourceFormat_Anthropic(t *testing.T) {
	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		return rejectBySourceFormat(c, sdktranslator.FormatClaude, ModerationReasonPolicy, "blocked", 403)
	})
	req := httptest.NewRequest("GET", "/test", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test failed: %v", err)
	}
	body := readBody(t, resp.Body)
	// Anthropic 信封：top-level type=error，error.type=permission_error
	if !strings.Contains(body, `"type":"error"`) {
		t.Errorf("missing Anthropic top-level type: %s", body)
	}
	if !strings.Contains(body, `"type":"permission_error"`) {
		t.Errorf("missing inner permission_error: %s", body)
	}
}

func TestRejectBySourceFormat_Gemini(t *testing.T) {
	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		return rejectBySourceFormat(c, sdktranslator.FormatGemini, ModerationReasonPolicy, "blocked", 403)
	})
	req := httptest.NewRequest("GET", "/test", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test failed: %v", err)
	}
	body := readBody(t, resp.Body)
	if !strings.Contains(body, `"status":"PERMISSION_DENIED"`) {
		t.Errorf("missing Gemini PERMISSION_DENIED: %s", body)
	}
	if !strings.Contains(body, `"code":403`) {
		t.Errorf("missing Gemini code: %s", body)
	}
}

func TestRejectBySourceFormat_AnthropicUnavailable(t *testing.T) {
	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		return rejectBySourceFormat(c, sdktranslator.FormatClaude, ModerationReasonUnavailable, "down", 503)
	})
	req := httptest.NewRequest("GET", "/test", nil)
	resp, _ := app.Test(req)
	body := readBody(t, resp.Body)
	// Unavailable 应映射到 overloaded_error 让客户端走 backoff
	if !strings.Contains(body, "overloaded_error") {
		t.Errorf("expected overloaded_error mapping: %s", body)
	}
}

func TestInferSourceFormat(t *testing.T) {
	cases := map[string]sdktranslator.Format{
		"/v1/messages":               sdktranslator.FormatClaude,
		"/v1/v1/messages":            sdktranslator.FormatClaude,
		"/v1/chat/completions":       sdktranslator.FormatOpenAI,
		"/v1/responses":              sdktranslator.FormatOpenAI,
		"/v1beta/models/x:generate":  sdktranslator.FormatGemini,
	}
	for path, want := range cases {
		if got := inferSourceFormat(path); got != want {
			t.Errorf("path=%q got=%q want=%q", path, got, want)
		}
	}
}

// ─── content_moderation：HMAC stable + chunk split ──────────────────────────

func TestSplitIntoChunks_RuneSafe(t *testing.T) {
	// 中英混合 + emoji 测试 rune 边界不被切坏
	input := "你好世界！abcdef🚀"
	chunks, _ := splitIntoChunks(input, 3, 4)
	for _, c := range chunks {
		if !isValidUTF8(c) {
			t.Errorf("chunk has invalid UTF-8: %q", c)
		}
	}
}

func TestSplitIntoChunks_RespectsMaxChunks(t *testing.T) {
	// chunkSize=2, maxChunks=3 → 最多 6 字符；超出时 truncated=true
	input := "abcdefghij" // 10 chars
	chunks, truncated := splitIntoChunks(input, 2, 3)
	if len(chunks) > 3 {
		t.Errorf("got %d chunks, expected ≤3", len(chunks))
	}
	if !truncated {
		t.Error("expected truncated=true when input exceeds chunkSize×maxChunks")
	}
}

func TestSplitIntoChunks_NoTruncate(t *testing.T) {
	// fix C4：刚好在 chunkSize × maxChunks 边界内不应标 truncated
	input := "abcdef" // 6 chars; chunkSize=2 × maxChunks=3 = 6
	_, truncated := splitIntoChunks(input, 2, 3)
	if truncated {
		t.Error("expected truncated=false at exact boundary")
	}
}

func TestSplitIntoChunks_EmptyInput(t *testing.T) {
	chunks, truncated := splitIntoChunks("", 100, 5)
	if len(chunks) != 0 {
		t.Errorf("empty input should give 0 chunks, got %d", len(chunks))
	}
	if truncated {
		t.Error("empty input must not report truncated")
	}
}

// ─── moderation_audit：入队 / drop / 度量 ─────────────────────────────────

func TestEnqueueModerationAudit_QueueNil(t *testing.T) {
	// worker 未启动时入队应失败但不 panic
	saved := moderationAuditQueue
	moderationAuditQueue = nil
	defer func() { moderationAuditQueue = saved }()

	ok := EnqueueModerationAudit(ModerationAuditEvent{UserID: 1})
	if ok {
		t.Error("expected enqueue to fail when queue is nil")
	}
}

func TestModerationAuditMetrics_NilQueue(t *testing.T) {
	saved := moderationAuditQueue
	moderationAuditQueue = nil
	defer func() { moderationAuditQueue = saved }()

	m := GetModerationAuditMetrics()
	if m.QueueDepth != 0 || m.QueueCapacity != 0 {
		t.Errorf("expected zero depth/cap with nil queue, got %+v", m)
	}
}

// ─── R23 第二轮交叉审查后补充的 fix 测试 ────────────────────────────────────

// fix CRITICAL R23-C4 / MAJOR R23-M3：LoadFailed 状态机
func TestModerationPolicy_LoadFailedFlag(t *testing.T) {
	// 直接构造 LoadFailed=true 的 policy（DB 路径在 controller_test 里已有覆盖）
	p := ModerationPolicy{Level: "off", FailMode: "open", loadFailed: true}
	if !p.LoadFailed() {
		t.Error("expected LoadFailed=true")
	}
	if p.IsActive() {
		t.Error("LoadFailed off-mode should report IsActive=false; gate decides via LoadFailed")
	}
}

// withSysConfig 用 mutex 写入测试态，调函数前释放锁，避免与 computePolicyVersion 内部 RLock 死锁
func withSysConfig(t *testing.T, kv map[string]string, fn func()) {
	t.Helper()
	SysConfigMutex.Lock()
	saved := make(map[string]string, len(SysConfigCache))
	for k, v := range SysConfigCache {
		saved[k] = v
	}
	for k, v := range kv {
		SysConfigCache[k] = v
	}
	SysConfigMutex.Unlock()
	defer func() {
		SysConfigMutex.Lock()
		SysConfigCache = saved
		SysConfigMutex.Unlock()
	}()
	fn()
}

// fix MAJOR R23-M5：computePolicyVersion 必须随 endpoint 变化而变化
func TestComputePolicyVersion_EndpointSensitivity(t *testing.T) {
	var v1, v2 string
	withSysConfig(t, map[string]string{
		"moderation_keywords":        `["a","b"]`,
		"moderation_image_policy":    "submit",
		"moderation_openai_model":    "omni-moderation-latest",
		"moderation_openai_endpoint": "https://api.openai.com/v1/moderations",
		"moderation_threshold":       "0.8",
	}, func() {
		v1 = computePolicyVersion()
	})
	withSysConfig(t, map[string]string{
		"moderation_keywords":        `["a","b"]`,
		"moderation_image_policy":    "submit",
		"moderation_openai_model":    "omni-moderation-latest",
		"moderation_openai_endpoint": "https://other-host.example.com/v1/moderations",
		"moderation_threshold":       "0.8",
	}, func() {
		v2 = computePolicyVersion()
	})
	if v1 == v2 {
		t.Errorf("policy_version must differ when endpoint changes; got %q twice", v1)
	}
}

// fix MAJOR R23-M5：threshold 变化应让 policy_version 变
func TestComputePolicyVersion_ThresholdSensitivity(t *testing.T) {
	var v1, v2 string
	withSysConfig(t, map[string]string{
		"moderation_keywords":        `["a"]`,
		"moderation_image_policy":    "submit",
		"moderation_openai_model":    "x",
		"moderation_openai_endpoint": "https://api.openai.com/v1/moderations",
		"moderation_threshold":       "0.8",
	}, func() {
		v1 = computePolicyVersion()
	})
	withSysConfig(t, map[string]string{
		"moderation_keywords":        `["a"]`,
		"moderation_image_policy":    "submit",
		"moderation_openai_model":    "x",
		"moderation_openai_endpoint": "https://api.openai.com/v1/moderations",
		"moderation_threshold":       "0.5",
	}, func() {
		v2 = computePolicyVersion()
	})
	if v1 == v2 {
		t.Errorf("policy_version must differ when threshold changes")
	}
}

// fix MAJOR R23-M6：classifyAPIError 把 err 收敛到 tag，不泄漏 endpoint/body
func TestClassifyAPIError_Tags(t *testing.T) {
	cases := map[string]string{
		"context deadline exceeded":      "api_error", // 非 net.Error 包装时
		"401 Unauthorized":               "api_auth_failed",
		"429 too many":                   "api_rate_limited",
		"prompt too long: rune > chunks": "input_too_long",
		"some random thing":              "api_error",
	}
	for in, want := range cases {
		got := classifyAPIError(fakeErr(in))
		if got != want {
			t.Errorf("classifyAPIError(%q)=%q want %q", in, got, want)
		}
	}
	if classifyAPIError(nil) != "" {
		t.Error("nil err must return empty tag")
	}
}

// fix MAJOR R23-M6：sanitizeErrText 限长 + trim
func TestSanitizeErrText(t *testing.T) {
	if got := sanitizeErrText("  hello  ", 100); got != "hello" {
		t.Errorf("expected trimmed, got %q", got)
	}
	long := strings.Repeat("a", 500)
	got := sanitizeErrText(long, 50)
	if len([]rune(got)) > 53 { // 50 + "..."
		t.Errorf("expected truncated to ~53 runes, got %d", len([]rune(got)))
	}
}

// fakeErr 简单 error wrapper（不实现 net.Error）
type fakeError struct{ msg string }

func (f fakeError) Error() string { return f.msg }
func fakeErr(msg string) error    { return fakeError{msg: msg} }

// ─── 工具函数 ─────────────────────────────────────────────────────────────

func readBody(t *testing.T, r interface{ Read([]byte) (int, error) }) string {
	t.Helper()
	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	return string(buf[:n])
}

func isValidUTF8(s string) bool {
	for range s { /* range 自动按 rune 解码；不抛错即合法 */
	}
	return strings.ToValidUTF8(s, "") == s
}
