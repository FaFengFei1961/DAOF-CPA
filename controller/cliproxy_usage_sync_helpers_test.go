// Package controller / cliproxy_usage_sync_helpers_test.go
//
// 单元测试覆盖 cliproxy_usage_sync.go 的纯函数 helper：
//   - parseUsageSyncCount（query 参数解析 + 默认/上限钳位）
//   - usageTimeDistance（ApiLog 与 upstream record 时间差计算）
//   - usageEndpointPath（HTTP method+path 字符串解析）
//   - compactStrings（去重 + 去空白）
//
// Phase F（2026-05-19）：把这些 pure helper 从 0% 拉到 100%。
// 主测试文件 cliproxy_usage_sync_test.go 已存在（覆盖归因主流程），
// 本文件只补 pure helper 的小单元。
package controller

import (
	"testing"
	"time"

	"daof-cpa/database"
)

func TestParseUsageSyncCount(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want int
	}{
		{"empty default", "", defaultCLIProxyUsageSyncCount},
		{"whitespace default", "   ", defaultCLIProxyUsageSyncCount},
		{"non-integer default", "abc", defaultCLIProxyUsageSyncCount},
		{"zero falls to default", "0", defaultCLIProxyUsageSyncCount},
		{"negative falls to default", "-5", defaultCLIProxyUsageSyncCount},
		{"valid within range", "50", 50},
		{"valid at default", "100", 100},
		{"over max clamped", "9999", maxCLIProxyUsageSyncCount},
		{"exactly max", "1000", 1000},
		{"trims whitespace", "  42  ", 42},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseUsageSyncCount(tc.raw)
			if got != tc.want {
				t.Errorf("parseUsageSyncCount(%q) = %d; want %d", tc.raw, got, tc.want)
			}
		})
	}
}

func TestUsageTimeDistance(t *testing.T) {
	base := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		logTime   time.Time
		recTime   time.Time
		latencyMS int64
		want      time.Duration
	}{
		{
			name:    "zero rec timestamp returns zero",
			logTime: base,
			recTime: time.Time{},
			want:    0,
		},
		{
			name:    "exact match returns zero",
			logTime: base,
			recTime: base,
			want:    0,
		},
		{
			name:    "log after rec",
			logTime: base.Add(5 * time.Second),
			recTime: base,
			want:    5 * time.Second,
		},
		{
			name:    "log before rec absolute value",
			logTime: base,
			recTime: base.Add(5 * time.Second),
			want:    5 * time.Second,
		},
		{
			name:      "latency shifts rec target forward",
			logTime:   base.Add(2 * time.Second),
			recTime:   base,
			latencyMS: 2000, // shifts rec target to base+2s, matches log → 0
			want:      0,
		},
		{
			name:      "latency partial offset",
			logTime:   base.Add(3 * time.Second),
			recTime:   base,
			latencyMS: 1000, // rec target = base+1s, log = base+3s, diff = 2s
			want:      2 * time.Second,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			logRow := database.ApiLog{}
			logRow.CreatedAt = tc.logTime
			rec := &database.UpstreamUsageRecord{
				Timestamp: tc.recTime,
				Latency:   tc.latencyMS,
			}
			got := usageTimeDistance(logRow, rec)
			if got != tc.want {
				t.Errorf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestUsageEndpointPath(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		want     string
	}{
		{"empty returns empty", "", ""},
		{"whitespace returns empty", "   ", ""},
		{"path-only with leading slash", "/v1/chat/completions", "/v1/chat/completions"},
		{"path-only with trailing whitespace", "/v1/chat  ", "/v1/chat"},
		{"method + path takes path", "POST /v1/chat/completions", "/v1/chat/completions"},
		{"GET + path", "GET /v1/models", "/v1/models"},
		{"3 fields takes second", "POST /v1/chat HTTP/1.1", "/v1/chat"},
		{"single non-path word returns empty", "POST", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := usageEndpointPath(tc.endpoint)
			if got != tc.want {
				t.Errorf("usageEndpointPath(%q) = %q; want %q", tc.endpoint, got, tc.want)
			}
		})
	}
}

func TestCompactStrings(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil input", nil, []string{}},
		{"all empty", []string{"", "  ", ""}, []string{}},
		{"trims whitespace", []string{"  a  ", "b"}, []string{"a", "b"}},
		{"removes duplicates", []string{"a", "b", "a", "c", "b"}, []string{"a", "b", "c"}},
		{"removes empty after trim", []string{"a", "", " ", "b"}, []string{"a", "b"}},
		{"order preserved", []string{"c", "a", "b"}, []string{"c", "a", "b"}},
		{"case sensitive dedup", []string{"A", "a"}, []string{"A", "a"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := compactStrings(tc.in...)
			if len(got) != len(tc.want) {
				t.Errorf("len = %d; want %d (got %#v)", len(got), len(tc.want), got)
				return
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("got[%d] = %q; want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}
