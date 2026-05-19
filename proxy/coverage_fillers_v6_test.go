package proxy

// coverage_fillers_v6_test.go
//
// M-R3 增量 6：prompt_extract.go 大量 0% 的 extract* helper +
// moderation_runner.go::ClassifyModerationAPIError / sanitizeErrText 补测试。
//
// 这批都是无 DB / 无 HTTP / 无锁 的纯解析函数（JSON in → segments out），
// 测起来便宜，但 prompt_extract.go 大部分函数对内容审核 path 至关重要——
// 一旦 schema 变化必须 break 这里的测试。

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// ─── prompt_extract.go::extractAnthropicSegments ──────────────────────────

func TestExtractAnthropicSegments(t *testing.T) {
	// system 数组 + system 字符串 + messages.content array/string + tool_use/tool_result + tools schema
	body := []byte(`{
  "system": [
    {"text": "system-array-text"},
    {"text": "second-sys"}
  ],
  "messages": [
    {"role": "user", "content": "string-content"},
    {"role": "user", "content": [
      {"type": "text", "text": "user-text"},
      {"type": "image", "source": {"data": "iVBORw", "media_type": "image/png"}},
      {"type": "image", "source": {"url": "https://example.com/x.jpg"}},
      {"type": "tool_use", "input": {"q": "x"}},
      {"type": "tool_result", "content": [{"text": "tool-out"}]}
    ]},
    {"role": "assistant", "content": [{"type": "text", "text": "assistant-text"}]}
  ],
  "tools": [
    {"description": "tool-desc", "input_schema": {"foo": "bar"}}
  ]
}`)
	segs, imgs := extractAnthropicSegments(body, nil, nil)
	texts := segsText(segs)
	if !testContainsAllHelper(texts, "system-array-text", "second-sys", "string-content", "user-text", "assistant-text", "tool-out", "tool-desc") {
		t.Errorf("missing key segment: %v", texts)
	}
	if !testContainsAnyHelper(texts, `"q":"x"`, `{"q": "x"}`, `{"q":"x"}`) {
		t.Errorf("tool_use input missing: %v", texts)
	}
	if !testContainsAnyHelper(texts, `"foo":"bar"`, `{"foo": "bar"}`, `{"foo":"bar"}`) {
		t.Errorf("tool input_schema missing: %v", texts)
	}
	if len(imgs) != 2 {
		t.Errorf("images=%v want 2 (base64 + url)", imgs)
	}
	if !strings.HasPrefix(imgs[0], "data:image/png;base64,") {
		t.Errorf("base64 image: %s", imgs[0])
	}
	if imgs[1] != "https://example.com/x.jpg" {
		t.Errorf("url image: %s", imgs[1])
	}
	// 验证 kind 标签
	if !hasKind(segs, SegmentSystemInstruction) {
		t.Error("应有 system_instruction segment")
	}
	if !hasKind(segs, SegmentUserMessage) {
		t.Error("应有 user_message segment")
	}
	if !hasKind(segs, SegmentAssistantMessage) {
		t.Error("应有 assistant_message segment")
	}
	if !hasKind(segs, SegmentToolCall) {
		t.Error("应有 tool_call segment")
	}
	if !hasKind(segs, SegmentToolResult) {
		t.Error("应有 tool_result segment")
	}
	if !hasKind(segs, SegmentToolSchema) {
		t.Error("应有 tool_schema segment")
	}
}

func TestExtractAnthropicSegments_SystemString(t *testing.T) {
	body := []byte(`{"system": "single-system-string", "messages":[]}`)
	segs, _ := extractAnthropicSegments(body, nil, nil)
	if !testContainsAllHelper(segsText(segs), "single-system-string") {
		t.Errorf("system string fallback: %v", segsText(segs))
	}
}

func TestExtractAnthropicSegments_ImageDefaultMime(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"image","source":{"data":"AAA"}}]}]}`)
	_, imgs := extractAnthropicSegments(body, nil, nil)
	if len(imgs) != 1 || !strings.HasPrefix(imgs[0], "data:image/jpeg;base64,") {
		t.Errorf("缺 media_type 应默认 image/jpeg, got %v", imgs)
	}
}

// ─── prompt_extract.go::appendAnthropicToolResultSegments ─────────────────

func TestAppendAnthropicToolResultSegments(t *testing.T) {
	body := []byte(`{"content": [{"text": "tr1"}, {"text": "tr2"}]}`)
	r := gjson.GetBytes(body, "content")
	var segs []ModerationSegment
	appendAnthropicToolResultSegments(r, "user", "p", &segs)
	if !testContainsAllHelper(segsText(segs), "tr1", "tr2") {
		t.Errorf("array tool_result: %v", segsText(segs))
	}
	for _, s := range segs {
		if s.Kind != SegmentToolResult {
			t.Errorf("kind=%v want tool_result", s.Kind)
		}
	}

	// string content fallback
	body2 := []byte(`{"content": "single-tool-result"}`)
	r2 := gjson.GetBytes(body2, "content")
	var segs2 []ModerationSegment
	appendAnthropicToolResultSegments(r2, "user", "p", &segs2)
	if !testContainsAllHelper(segsText(segs2), "single-tool-result") {
		t.Errorf("string tool_result: %v", segsText(segs2))
	}
}

// ─── prompt_extract.go::extractGeminiSegments ─────────────────────────────

func TestExtractGeminiSegments(t *testing.T) {
	body := []byte(`{
  "contents": [
    {"role": "user", "parts": [
      {"text": "user-gemini-text"},
      {"inline_data": {"mime_type": "image/png", "data": "AAAA"}},
      {"file_data": {"file_uri": "gs://bucket/file.jpg"}},
      {"functionCall": {"name": "f", "args": {"a": 1}}},
      {"functionResponse": {"response": {"r": 2}}},
      {"executableCode": {"code": "print(1)"}}
    ]},
    {"role": "model", "parts": [{"text": "model-reply"}]},
    {"role": "function", "parts": [{"text": "func-out"}]}
  ],
  "systemInstruction": {"parts": [{"text": "system-instr"}]},
  "tools": [
    {"functionDeclarations": [
      {"description": "fn-desc", "parameters": {"p1": "x"}}
    ]}
  ]
}`)
	segs, imgs := extractGeminiSegments(body, nil, nil)
	texts := segsText(segs)
	if !testContainsAllHelper(texts, "user-gemini-text", "model-reply", "func-out", "system-instr", "fn-desc", "print(1)") {
		t.Errorf("missing key segment: %v", texts)
	}
	// 验证 functionCall args / functionResponse / parameters 都进入
	if !testContainsAnyHelper(texts, `"a":1`, `{"a":1}`, `{"a": 1}`) {
		t.Errorf("functionCall.args missing: %v", texts)
	}
	if !testContainsAnyHelper(texts, `"r":2`, `{"r":2}`, `{"r": 2}`) {
		t.Errorf("functionResponse.response missing: %v", texts)
	}
	if !testContainsAnyHelper(texts, `"p1":"x"`, `{"p1":"x"}`, `{"p1": "x"}`) {
		t.Errorf("parameters missing: %v", texts)
	}
	if len(imgs) != 2 {
		t.Errorf("images=%v want 2", imgs)
	}
	// 角色映射
	if !hasKind(segs, SegmentUserMessage) {
		t.Error("user_message missing")
	}
	if !hasKind(segs, SegmentAssistantMessage) {
		t.Error("assistant_message (model) missing")
	}
	if !hasKind(segs, SegmentFunctionOutput) {
		t.Error("function_output missing")
	}
	if !hasKind(segs, SegmentSystemInstruction) {
		t.Error("system_instruction missing")
	}
	if !hasKind(segs, SegmentToolSchema) {
		t.Error("tool_schema missing")
	}
}

// ─── prompt_extract.go::extractOpenAIReview ───────────────────────────────

func TestExtractOpenAIReview(t *testing.T) {
	body := []byte(`{
  "messages": [
    {"role": "system", "content": "skip-system"},
    {"role": "user", "content": "user-msg"},
    {"role": "assistant", "content": "skip-assistant"},
    {"role": "tool", "content": "tool-msg"},
    {"role": "function", "content": "function-msg"},
    {"role": "", "content": "no-role-treated-as-user"},
    {"role": "user", "content": [
      {"type": "text", "text": "user-multi"},
      {"type": "image_url", "image_url": {"url": "https://example.com/a.png"}}
    ]}
  ]
}`)
	parts, imgs := extractOpenAIReview(body, nil, nil)
	if !testContainsAllHelper(parts, "user-msg", "tool-msg", "function-msg", "no-role-treated-as-user", "user-multi") {
		t.Errorf("parts: %v", parts)
	}
	// system / assistant 应被过滤
	if testContainsAnyHelper(parts, "skip-system", "skip-assistant") {
		t.Errorf("system/assistant 不应进 review: %v", parts)
	}
	if len(imgs) != 1 || imgs[0] != "https://example.com/a.png" {
		t.Errorf("imgs: %v", imgs)
	}
}

// ─── prompt_extract.go::extractOpenAIResponsesReview ──────────────────────

func TestExtractOpenAIResponsesReview(t *testing.T) {
	body := []byte(`{
  "input": [
    {"role": "system", "text": "skip-sys"},
    {"role": "user", "text": "user-text"},
    {"role": "user", "content": [{"text": "user-content"}]},
    {"role": "assistant", "text": "skip-asst"},
    {"role": "tool", "text": "tool-text"},
    {"type": "function_call_output", "output": "fn-output"}
  ]
}`)
	parts, _ := extractOpenAIResponsesReview(body, nil, nil)
	if !testContainsAllHelper(parts, "user-text", "user-content", "tool-text", "fn-output") {
		t.Errorf("parts: %v", parts)
	}
	if testContainsAnyHelper(parts, "skip-sys", "skip-asst") {
		t.Errorf("system/assistant 不应进: %v", parts)
	}
}

// ─── prompt_extract.go::extractAnthropicReview ────────────────────────────

func TestExtractAnthropicReview(t *testing.T) {
	body := []byte(`{
  "messages": [
    {"role": "assistant", "content": "skip-asst"},
    {"role": "user", "content": "plain-user"},
    {"role": "user", "content": [
      {"type": "text", "text": "user-text-block"},
      {"type": "image", "source": {"data": "AAA", "media_type": "image/png"}},
      {"type": "image", "source": {"url": "https://example.com/img.jpg"}},
      {"type": "tool_result", "content": [{"text": "tr-array-text"}]},
      {"type": "tool_result", "content": "tr-string"}
    ]}
  ]
}`)
	parts, imgs := extractAnthropicReview(body, nil, nil)
	if !testContainsAllHelper(parts, "plain-user", "user-text-block", "tr-array-text", "tr-string") {
		t.Errorf("parts: %v", parts)
	}
	if testContainsAnyHelper(parts, "skip-asst") {
		t.Errorf("assistant 不应进: %v", parts)
	}
	if len(imgs) != 2 {
		t.Errorf("imgs %v want 2", imgs)
	}
}

// ─── prompt_extract.go::extractGeminiReview ───────────────────────────────

func TestExtractGeminiReview(t *testing.T) {
	body := []byte(`{
  "contents": [
    {"role": "model", "parts": [{"text": "skip-model"}]},
    {"role": "user", "parts": [
      {"text": "user-gemini-text"},
      {"inline_data": {"mime_type": "image/png", "data": "BBB"}},
      {"file_data": {"file_uri": "gs://x"}}
    ]},
    {"role": "function", "parts": [{"functionResponse": {"response": {"k": "v"}}}]}
  ]
}`)
	parts, imgs := extractGeminiReview(body, nil, nil)
	if !testContainsAllHelper(parts, "user-gemini-text") {
		t.Errorf("parts: %v", parts)
	}
	if !testContainsAnyHelper(parts, `"k":"v"`, `{"k":"v"}`, `{"k": "v"}`) {
		t.Errorf("functionResponse missing: %v", parts)
	}
	if testContainsAnyHelper(parts, "skip-model") {
		t.Errorf("model 不应进: %v", parts)
	}
	if len(imgs) != 2 {
		t.Errorf("imgs %v want 2", imgs)
	}
}

// ─── prompt_extract.go::appendReviewContent ───────────────────────────────

func TestAppendReviewContent(t *testing.T) {
	// 嵌套数组递归
	body := []byte(`{"c": [
    {"text": "t1"},
    [{"text": "nested-t2"}, {"image_url": {"url": "https://example.com/x.png"}}],
    "plain-string"
  ]}`)
	r := gjsonGet(body, "c")
	var parts []string
	var imgs []string
	if ok := appendReviewContent(r, &parts, &imgs); !ok {
		t.Error("array should return true")
	}
	// "plain-string" 在数组内会被当作 item，再调一次 appendReviewContent，
	// 它既不是 array 也没有 text/image_url 字段 → 不 append
	if !testContainsAllHelper(parts, "t1", "nested-t2") {
		t.Errorf("parts: %v", parts)
	}
	if len(imgs) != 1 || imgs[0] != "https://example.com/x.png" {
		t.Errorf("imgs: %v", imgs)
	}
}

// ─── moderation_runner.go::ClassifyModerationAPIError / sanitizeErrText ───

func TestClassifyModerationAPIError(t *testing.T) {
	if ClassifyModerationAPIError(nil) != "" {
		t.Error("nil err -> empty tag")
	}

	cases := []struct {
		name string
		err  error
		want string
	}{
		{"unauthorized 401", errors.New("HTTP 401 unauthorized"), "api_auth_failed"},
		{"invalid api key string", errors.New("oops API KEY NOT VALID. please pass a valid API key"), "api_auth_failed"},
		{"rate limit 429", errors.New("HTTP 429 too many requests"), "api_rate_limited"},
		{"insufficient_quota", errors.New("insufficient_quota for this account"), "api_quota_or_billing"},
		{"5xx by phrase", errors.New("upstream returned api status 500"), "api_5xx"},
		{"prompt too long", errors.New("PROMPT TOO LONG"), "input_too_long"},
		{"generic unknown", errors.New("something random"), "api_error"},
		{"context deadline exceeded", context.DeadlineExceeded, "api_timeout"},
		{"net timeout", &fakeNetErr{timeout: true}, "api_timeout"},
		{"net other", &fakeNetErr{}, "api_network_error"},
	}
	for _, c := range cases {
		got := ClassifyModerationAPIError(c.err)
		if got != c.want {
			t.Errorf("%s: got=%q want=%q", c.name, got, c.want)
		}
	}
}

// ─── 辅助 ─────────────────────────────────────────────────────────────────

type fakeNetErr struct {
	timeout bool
}

func (e *fakeNetErr) Error() string   { return fmt.Sprintf("fake net error timeout=%v", e.timeout) }
func (e *fakeNetErr) Timeout() bool   { return e.timeout }
func (e *fakeNetErr) Temporary() bool { return false }

// 编译期断言：fakeNetErr 必须实现 net.Error
var _ net.Error = (*fakeNetErr)(nil)

func TestSanitizeErrText_RuneAware(t *testing.T) {
	// 短串原样（去前后空白）
	if got := sanitizeErrText("  hello  ", 100); got != "hello" {
		t.Errorf("trim: got=%q", got)
	}
	// 超长截断（按 rune 数）
	long := strings.Repeat("a", 300)
	got := sanitizeErrText(long, 50)
	runes := []rune(got)
	if len(runes) > 60 { // sanitize 可能加"..."
		t.Errorf("rune count=%d want <=60", len(runes))
	}
	// 多字节字符按 rune 计数（确保 UTF-8 安全）
	cn := strings.Repeat("中", 100)
	got = sanitizeErrText(cn, 20)
	if r := []rune(got); len(r) > 30 {
		t.Errorf("CJK rune count=%d want <=30", len(r))
	}
}

func TestSanitizeErrText_EmptyOrAllWhitespace(t *testing.T) {
	if got := sanitizeErrText("", 50); got != "" {
		t.Errorf("empty: got=%q", got)
	}
	if got := sanitizeErrText("   \t\n  ", 50); got != "" {
		t.Errorf("whitespace only: got=%q", got)
	}
}

// ─── 小工具 ───────────────────────────────────────────────────────────────

func segsText(segs []ModerationSegment) []string {
	out := make([]string, 0, len(segs))
	for _, s := range segs {
		out = append(out, s.Text)
	}
	return out
}

func hasKind(segs []ModerationSegment, k ModerationSegmentKind) bool {
	for _, s := range segs {
		if s.Kind == k {
			return true
		}
	}
	return false
}

func testContainsAllHelper(haystack []string, needles ...string) bool {
	for _, n := range needles {
		found := false
		for _, h := range haystack {
			if strings.Contains(h, n) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func testContainsAnyHelper(haystack []string, needles ...string) bool {
	for _, n := range needles {
		for _, h := range haystack {
			if strings.Contains(h, n) {
				return true
			}
		}
	}
	return false
}

func gjsonGet(body []byte, path string) gjson.Result {
	return gjson.GetBytes(body, path)
}
