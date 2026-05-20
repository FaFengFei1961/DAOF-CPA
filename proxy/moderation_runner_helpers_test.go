// Package proxy / moderation_runner_helpers_test.go
//
// 单元测试覆盖 moderation_runner.go + moderation_keyword_ai.go 的纯函数 helper：
//   - classifyAPIError / ClassifyModerationAPIError（错误分类，避免泄漏上游细节）
//   - sanitizeErrText（rune-safe 截断）
//   - buildCLIProxyKeywordAIRequest（keyword AI 请求体构造）
//
// Phase F（2026-05-19）：这些函数原本 0% 覆盖率，本文件拉到 100%。
package proxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
)

func TestClassifyAPIError_TableDriven(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil returns empty", nil, ""},
		{"context deadline exceeded", context.DeadlineExceeded, "api_timeout"},
		{"plain 401 in message", errors.New("HTTP 401 Unauthorized"), "api_auth_failed"},
		{"unauthorized keyword", errors.New("request was unauthorized"), "api_auth_failed"},
		{"invalid_api_key", errors.New("invalid_api_key: please check"), "api_auth_failed"},
		{"api key not valid", errors.New("API key not valid for this request"), "api_auth_failed"},
		{"429 too many requests", errors.New("HTTP 429 too many requests"), "api_rate_limited"},
		{"rate limit lowercase", errors.New("upstream rate limit reached"), "api_rate_limited"},
		{"insufficient_quota", errors.New("insufficient_quota for this month"), "api_quota_or_billing"},
		{"billing keyword", errors.New("billing not configured"), "api_quota_or_billing"},
		{"quota keyword", errors.New("quota exhausted"), "api_quota_or_billing"},
		{"api status 5xx text", errors.New("upstream api status 503"), "api_5xx"},
		{"prompt too long", errors.New("prompt too long: 100000 tokens"), "input_too_long"},
		{"generic error fallback", errors.New("some random failure"), "api_error"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyAPIError(tc.err)
			if got != tc.want {
				t.Errorf("classifyAPIError(%v) = %q; want %q", tc.err, got, tc.want)
			}
		})
	}
}

func TestClassifyAPIError_NetTimeout(t *testing.T) {
	// net.Error with Timeout()=true → api_timeout
	netErr := &timeoutNetErr{}
	if got := classifyAPIError(netErr); got != "api_timeout" {
		t.Errorf("net timeout err = %q; want api_timeout", got)
	}
}

func TestClassifyAPIError_NetGeneric(t *testing.T) {
	// net.Error with Timeout()=false → api_network_error
	netErr := &nontimeoutNetErr{}
	if got := classifyAPIError(netErr); got != "api_network_error" {
		t.Errorf("net non-timeout err = %q; want api_network_error", got)
	}
}

func TestClassifyModerationAPIError_ExportedAlias(t *testing.T) {
	// ClassifyModerationAPIError should match classifyAPIError exactly.
	cases := []error{
		nil,
		errors.New("HTTP 401"),
		errors.New("rate limit"),
	}
	for i, err := range cases {
		if a, b := ClassifyModerationAPIError(err), classifyAPIError(err); a != b {
			t.Errorf("case %d: ClassifyModerationAPIError=%q lowercase=%q", i, a, b)
		}
	}
}

func TestSanitizeErrText_TableDriven(t *testing.T) {
	tests := []struct {
		name   string
		s      string
		maxLen int
		want   string
	}{
		{"empty stays empty", "", 10, ""},
		{"trim only whitespace", "   hello   ", 100, "hello"},
		{"under maxLen unchanged", "short", 100, "short"},
		{"at maxLen unchanged", "12345", 5, "12345"},
		{"over maxLen truncates with ellipsis", "12345678901234567890", 10, "1234567890..."},
		{"chinese rune-safe truncate", "中文测试很长很长很长", 5, "中文测试很..."},
		{"emoji rune-safe", "🚀🚀🚀🚀🚀🚀", 3, "🚀🚀🚀..."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeErrText(tc.s, tc.maxLen)
			if got != tc.want {
				t.Errorf("sanitizeErrText(%q, %d) = %q; want %q", tc.s, tc.maxLen, got, tc.want)
			}
		})
	}
}

func TestBuildCLIProxyKeywordAIRequest(t *testing.T) {
	t.Run("empty focus falls back to default", func(t *testing.T) {
		req := buildCLIProxyKeywordAIRequest("", []string{"old1", "old2"}, 10, "gpt-4o")
		messages, ok := req["messages"].([]map[string]string)
		if !ok || len(messages) != 2 {
			t.Fatalf("messages malformed: %#v", req["messages"])
		}
		userMsg := messages[1]["content"]
		// fallback text should mention "jailbreak"
		if !strings.Contains(userMsg, "jailbreak") {
			t.Errorf("fallback focus not embedded in user msg: %q", userMsg)
		}
	})

	t.Run("custom focus embedded", func(t *testing.T) {
		req := buildCLIProxyKeywordAIRequest("custom abuse vectors", []string{"foo"}, 5, "gpt-4o")
		messages := req["messages"].([]map[string]string)
		userMsg := messages[1]["content"]
		if !strings.Contains(userMsg, "custom abuse vectors") {
			t.Errorf("custom focus missing from user msg: %q", userMsg)
		}
		if !strings.Contains(userMsg, fmt.Sprintf("at most %d", 5)) {
			t.Errorf("max candidates missing from user msg: %q", userMsg)
		}
	})

	t.Run("model and temperature populated", func(t *testing.T) {
		req := buildCLIProxyKeywordAIRequest("test", nil, 1, "claude-3-opus")
		if req["model"] != "claude-3-opus" {
			t.Errorf("model = %v; want claude-3-opus", req["model"])
		}
		if req["temperature"] != 0.2 {
			t.Errorf("temperature = %v; want 0.2", req["temperature"])
		}
		if req["stream"] != false {
			t.Errorf("stream = %v; want false", req["stream"])
		}
		if req["max_tokens"] != 8192 {
			t.Errorf("max_tokens = %v; want 8192", req["max_tokens"])
		}
	})

	t.Run("existing keywords serialized into user msg", func(t *testing.T) {
		req := buildCLIProxyKeywordAIRequest("test", []string{"keyword_alpha", "keyword_beta"}, 3, "gpt-4o")
		messages := req["messages"].([]map[string]string)
		userMsg := messages[1]["content"]
		if !strings.Contains(userMsg, "keyword_alpha") {
			t.Errorf("existing keyword_alpha missing from user msg: %q", userMsg)
		}
		if !strings.Contains(userMsg, "keyword_beta") {
			t.Errorf("existing keyword_beta missing from user msg: %q", userMsg)
		}
	})
}

// timeoutNetErr is a fake net.Error returning Timeout()==true.
type timeoutNetErr struct{}

func (*timeoutNetErr) Error() string   { return "fake timeout" }
func (*timeoutNetErr) Timeout() bool   { return true }
func (*timeoutNetErr) Temporary() bool { return false }

// nontimeoutNetErr is a fake net.Error returning Timeout()==false.
type nontimeoutNetErr struct{}

func (*nontimeoutNetErr) Error() string   { return "fake net failure" }
func (*nontimeoutNetErr) Timeout() bool   { return false }
func (*nontimeoutNetErr) Temporary() bool { return true }

var _ net.Error = (*timeoutNetErr)(nil)
var _ net.Error = (*nontimeoutNetErr)(nil)
