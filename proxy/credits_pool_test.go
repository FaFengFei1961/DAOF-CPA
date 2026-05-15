package proxy

import (
	"strings"
	"testing"
)

func TestClaudeBuildWindowTreatsUtilizationAsUsedPercent(t *testing.T) {
	// Anthropic OAuth usage 契约：utilization 始终是 0-100 百分数。
	// 与 CPAMC `parseClaudeUsagePayload` (quotaConfigs.ts:924) 行为对齐。
	def := claudeWindowDef{Key: "five_hour", ID: "five-hour", Label: "5 小时限额"}

	w := claudeBuildWindow(def, map[string]any{"utilization": 2.0})
	if w.UsedPercent != 2 || w.RemainingPercent != 98 {
		t.Fatalf("expected utilization=2 to mean 2%% used, got used=%.1f remaining=%.1f", w.UsedPercent, w.RemainingPercent)
	}

	w = claudeBuildWindow(def, map[string]any{"utilization": 76.0})
	if w.UsedPercent != 76 || w.RemainingPercent != 24 {
		t.Fatalf("expected utilization=76 to mean 76%% used, got used=%.1f remaining=%.1f", w.UsedPercent, w.RemainingPercent)
	}

	// 边界回归（防止再次出现"f ≤ 1.0 视作比例 × 100"启发式）：
	// utilization=1.0 必须是 1% used，而不是 100% used。
	// 这是原始 bug 的触发条件：5h 在 ≤1% 使用率下显示 0% remaining。
	w = claudeBuildWindow(def, map[string]any{"utilization": 1.0})
	if w.UsedPercent != 1 || w.RemainingPercent != 99 {
		t.Fatalf("expected utilization=1 to mean 1%% used, got used=%.1f remaining=%.1f", w.UsedPercent, w.RemainingPercent)
	}

	w = claudeBuildWindow(def, map[string]any{"utilization": 0.5})
	if w.UsedPercent != 0.5 || w.RemainingPercent != 99.5 {
		t.Fatalf("expected utilization=0.5 to mean 0.5%% used, got used=%.1f remaining=%.1f", w.UsedPercent, w.RemainingPercent)
	}
}

// TestSanitizeError_RedactsAllSensitivePatterns 验证 Sprint2-M6 修复：
// Cookie / Set-Cookie / URL query secrets / URL userinfo 必须与 Bearer/api_key 同入口清洗。
// 否则 cliproxy_usage_sync.FailBody 等错误日志落库时会泄漏凭证。
func TestSanitizeError_RedactsAllSensitivePatterns(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		mustMiss  []string // 不能出现在输出中（原始敏感值）
		mustHave  []string // 必须出现的脱敏标记
	}{
		{
			name:     "bearer header",
			input:    "request failed: Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.payload.sig",
			mustMiss: []string{"eyJhbGciOiJIUzI1NiJ9.payload.sig"},
			mustHave: []string{"Bearer ***"},
		},
		{
			name:     "set-cookie header",
			input:    "upstream 401: Set-Cookie: session=abc123secret; Path=/; HttpOnly",
			mustMiss: []string{"abc123secret"},
			mustHave: []string{"Set-Cookie:", "***"},
		},
		{
			name:     "cookie request header",
			input:    "client: Cookie: auth=verysecretvalue; tracking=ok",
			mustMiss: []string{"verysecretvalue"},
			mustHave: []string{"Cookie:", "***"},
		},
		{
			name:     "url query secrets",
			input:    "GET https://api.example.com/x?api_key=sk-secret123&token=mytoken789 failed",
			mustMiss: []string{"sk-secret123", "mytoken789"},
			mustHave: []string{"api_key=***", "token=***"},
		},
		{
			name:     "url userinfo",
			input:    "dial https://admin:p4ssw0rd@host.example/path failed",
			mustMiss: []string{"admin:p4ssw0rd", "p4ssw0rd"},
			mustHave: []string{"https://***@host.example"},
		},
		{
			name:     "jwt bare",
			input:    "error: eyJaaaaaaaaaaa.bbbbbbbbbb.cccccccccccc invalid",
			mustMiss: []string{"eyJaaaaaaaaaaa.bbbbbbbbbb.cccccccccccc"},
			mustHave: []string{"[JWT]"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeError(tc.input, 1024)
			for _, miss := range tc.mustMiss {
				if strings.Contains(got, miss) {
					t.Errorf("input=%q\noutput=%q\nshould NOT contain %q", tc.input, got, miss)
				}
			}
			for _, have := range tc.mustHave {
				if !strings.Contains(got, have) {
					t.Errorf("input=%q\noutput=%q\nshould contain %q", tc.input, got, have)
				}
			}
		})
	}
}

func TestComputeHealthyTrustsFreshQuotaWindowsOverStaleStatus(t *testing.T) {
	entry := &CreditEntry{
		Provider: "claude",
		Status:   "error",
		Windows:  []CreditWindow{{ID: "five-hour", RemainingPercent: 63}},
	}

	if !computeHealthy(entry) {
		t.Fatal("expected fresh quota windows with remaining capacity to be healthy even when CPA status is stale error")
	}
}

func TestComputeHealthyKeepsDisabledAndExhaustedUnhealthy(t *testing.T) {
	t.Run("disabled status wins", func(t *testing.T) {
		entry := &CreditEntry{
			Provider: "claude",
			Status:   "disabled",
			Windows:  []CreditWindow{{ID: "five-hour", RemainingPercent: 63}},
		}
		if computeHealthy(entry) {
			t.Fatal("expected disabled credential to stay unhealthy")
		}
	})

	t.Run("all quota windows exhausted", func(t *testing.T) {
		entry := &CreditEntry{
			Provider: "claude",
			Status:   "active",
			Windows: []CreditWindow{
				{ID: "five-hour", RemainingPercent: 5},
				{ID: "weekly", RemainingPercent: 2},
			},
		}
		if computeHealthy(entry) {
			t.Fatal("expected credential with no window above threshold to be unhealthy")
		}
	})

	t.Run("empty windows are not healthy unless provider is allowlisted", func(t *testing.T) {
		entry := &CreditEntry{Provider: "claude", Status: "active"}
		if computeHealthy(entry) {
			t.Fatal("expected empty numeric windows to be unhealthy")
		}
	})

	t.Run("last error wins", func(t *testing.T) {
		entry := &CreditEntry{
			Provider:  "claude",
			Status:    "active",
			LastError: "quota fetch failed",
			Windows:   []CreditWindow{{ID: "five-hour", RemainingPercent: 63}},
		}
		if computeHealthy(entry) {
			t.Fatal("expected fetch error to be unhealthy")
		}
	})
}
