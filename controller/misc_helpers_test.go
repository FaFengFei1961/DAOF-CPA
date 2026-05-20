// Package controller / misc_helpers_test.go
//
// 单元测试覆盖几个跨文件的纯 helper：
//   - firstNonEmptyString (channel_model.go)
//   - truncateLog (customer_message.go)
//   - moderationTestErrorResponse (moderation.go)
//   - sanitizeModerationDiagnostic (moderation.go)
//
// Phase F batch 2（2026-05-19）。
package controller

import (
	"net/http"
	"strings"
	"testing"
)

func TestFirstNonEmptyString(t *testing.T) {
	tests := []struct {
		name   string
		values []string
		want   string
	}{
		{"all empty", []string{"", "", ""}, ""},
		{"nil slice", nil, ""},
		{"single value", []string{"hello"}, "hello"},
		{"first wins", []string{"a", "b", "c"}, "a"},
		{"skips empty/whitespace", []string{"", "   ", "actual"}, "actual"},
		{"trims result", []string{"  trimmed  "}, "trimmed"},
		{"empty after empty", []string{"", ""}, ""},
		{"unicode preserved", []string{"", "中文"}, "中文"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := firstNonEmptyString(tc.values...)
			if got != tc.want {
				t.Errorf("firstNonEmptyString(%v) = %q; want %q", tc.values, got, tc.want)
			}
		})
	}
}

func TestTruncateLog(t *testing.T) {
	tests := []struct {
		name string
		s    string
		n    int
		want string
	}{
		{"empty stays empty", "", 5, ""},
		{"under n unchanged", "hi", 10, "hi"},
		{"exactly n unchanged", "12345", 5, "12345"},
		{"over n truncates", "1234567890", 5, "12345..."},
		{"chinese rune-safe", "中文测试很长很长", 4, "中文测试..."},
		{"emoji rune-safe", "🚀🚀🚀🚀🚀🚀", 3, "🚀🚀🚀..."},
		{"n=0 always truncates", "hi", 0, "..."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateLog(tc.s, tc.n)
			if got != tc.want {
				t.Errorf("truncateLog(%q, %d) = %q; want %q", tc.s, tc.n, got, tc.want)
			}
		})
	}
}

func TestModerationTestErrorResponse(t *testing.T) {
	tests := []struct {
		tag          string
		wantStatus   string
		wantCode     string
		wantHTTP     int
		wantInMsgRaw string // substring expected in message (zh)
	}{
		{"api_auth_failed", "auth_failed", "ERR_MODERATION_AUTH_FAILED", http.StatusBadGateway, "鉴权失败"},
		{"api_rate_limited", "rate_limited", "ERR_MODERATION_RATE_LIMITED", http.StatusTooManyRequests, "限流"},
		{"api_quota_or_billing", "billing_or_quota", "ERR_MODERATION_BILLING_OR_QUOTA", http.StatusBadGateway, "quota"},
		{"api_timeout", "timeout", "ERR_MODERATION_TIMEOUT", http.StatusGatewayTimeout, "超时"},
		{"api_network_error", "network_error", "ERR_MODERATION_NETWORK", http.StatusBadGateway, "网络"},
		{"api_5xx", "api_5xx", "ERR_MODERATION_UPSTREAM_5XX", http.StatusBadGateway, "上游"},
		{"input_too_long", "input_too_long", "ERR_MODERATION_TEST_INPUT_TOO_LONG", http.StatusBadRequest, "长度"},
		{"api_error", "api_error", "ERR_MODERATION_API_ERROR", http.StatusBadGateway, ""},
		{"unknown_tag_fallback", "api_error", "ERR_MODERATION_API_ERROR", http.StatusBadGateway, ""},
	}
	for _, tc := range tests {
		t.Run(tc.tag, func(t *testing.T) {
			status, code, msg, httpCode := moderationTestErrorResponse(tc.tag)
			if status != tc.wantStatus {
				t.Errorf("status = %q; want %q", status, tc.wantStatus)
			}
			if code != tc.wantCode {
				t.Errorf("code = %q; want %q", code, tc.wantCode)
			}
			if httpCode != tc.wantHTTP {
				t.Errorf("httpStatus = %d; want %d", httpCode, tc.wantHTTP)
			}
			if tc.wantInMsgRaw != "" && !strings.Contains(msg, tc.wantInMsgRaw) {
				t.Errorf("message %q does not contain %q", msg, tc.wantInMsgRaw)
			}
		})
	}
}

func TestSanitizeModerationDiagnostic(t *testing.T) {
	tests := []struct {
		name     string
		s        string
		maxRunes int
		want     string
	}{
		{"empty stays empty", "", 100, ""},
		{"whitespace collapses to single space", "a   b\nc\td", 100, "a b c d"},
		{"trim outer whitespace", "  hello  ", 100, "hello"},
		{"under maxRunes unchanged", "short", 10, "short"},
		{"zero maxRunes no truncate", "longer string", 0, "longer string"},
		{"negative maxRunes no truncate", "longer string", -1, "longer string"},
		{"truncates at rune boundary", "123456789012345", 10, "1234567890..."},
		{"chinese rune-safe truncate", "中文很长很长很长", 4, "中文很长..."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeModerationDiagnostic(tc.s, tc.maxRunes)
			if got != tc.want {
				t.Errorf("sanitizeModerationDiagnostic(%q, %d) = %q; want %q", tc.s, tc.maxRunes, got, tc.want)
			}
		})
	}
}
