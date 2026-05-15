package proxy

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestCPAClient_UsesSafeTransport(t *testing.T) {
	if cpaHTTPClient.Transport != SafeTransport() {
		t.Fatalf("cpaHTTPClient must use SafeTransport, got %#v", cpaHTTPClient.Transport)
	}
	if cpaAuthFilesClient.Transport != SafeTransport() {
		t.Fatalf("cpaAuthFilesClient must use SafeTransport, got %#v", cpaAuthFilesClient.Transport)
	}
	if cpaHTTPClient.CheckRedirect == nil {
		t.Fatal("cpaHTTPClient must install redirect guard")
	}
	if cpaAuthFilesClient.CheckRedirect == nil {
		t.Fatal("cpaAuthFilesClient must install redirect guard")
	}

	req, err := http.NewRequest(http.MethodGet, "http://169.254.169.254/latest/meta-data/", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if err := cpaHTTPClient.CheckRedirect(req, nil); err == nil {
		t.Fatal("cpaHTTPClient redirect guard should block metadata redirects")
	}
}

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
		name     string
		input    string
		mustMiss []string // 不能出现在输出中（原始敏感值）
		mustHave []string // 必须出现的脱敏标记
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

// withCreditsCache 临时替换 creditsCache，并在测试结束时恢复，避免污染其他测试。
func withCreditsCache(t *testing.T, snapshot map[string]*CreditEntry, fn func()) {
	t.Helper()
	creditsMu.Lock()
	prev := creditsCache
	creditsCache = snapshot
	creditsMu.Unlock()
	defer func() {
		creditsMu.Lock()
		creditsCache = prev
		creditsMu.Unlock()
	}()
	fn()
}

// TestValidateAuthFilesResponse_ColdStartEmpty 冷启动（creditsCache 为空）时，
// CPA 空响应不能被当作可信快照提交。
func TestValidateAuthFilesResponse_ColdStartEmpty(t *testing.T) {
	withCreditsCache(t, map[string]*CreditEntry{}, func() {
		if validateAuthFilesResponse(nil) {
			t.Errorf("cold start with nil input should ABORT")
		}
		if validateAuthFilesResponse([]authFileLite{}) {
			t.Errorf("cold start with empty input should ABORT")
		}
		if !validateAuthFilesResponse([]authFileLite{
			{ID: "a1", Provider: "claude"},
		}) {
			t.Errorf("cold start with 1 valid entry should be accepted")
		}
	})
}

// TestValidateAuthFilesResponse_FullEmptyAbortsWhenCacheHasEntries 上轮有 N 凭证，
// 本轮全空 → 永远视作上游异常 abort，保留 cache。
// 这是 Sprint4-M6 核心防御：CPA 瞬时异常不能瞬间清空全平台号池。
func TestValidateAuthFilesResponse_FullEmptyAbortsWhenCacheHasEntries(t *testing.T) {
	cache := map[string]*CreditEntry{
		"a1": {AuthID: "a1", Provider: "claude"},
		"a2": {AuthID: "a2", Provider: "antigravity"},
	}
	withCreditsCache(t, cache, func() {
		// nil 输入（CPA 返回 files 字段缺失或为 null）→ abort
		if validateAuthFilesResponse(nil) {
			t.Errorf("empty input with non-empty cache should ABORT")
		}
		// 空 slice 输入（CPA 返回 files=[]）→ abort
		if validateAuthFilesResponse([]authFileLite{}) {
			t.Errorf("empty slice with non-empty cache should ABORT")
		}
		// 全 malformed 也等价于 valid=0 → abort
		if validateAuthFilesResponse([]authFileLite{
			{ID: "", Provider: "claude"}, // missing ID
			{ID: "a3", Provider: ""},     // missing provider
			{ID: "   ", Provider: "   "}, // whitespace only
		}) {
			t.Errorf("all-malformed input should ABORT (valid_count=0)")
		}
	})
}

// TestValidateAuthFilesResponse_ShrinkBelowThresholdAborts 上轮 10 条，本轮 4 条（< 50%）
// → abort 防止"批量误删"。
func TestValidateAuthFilesResponse_ShrinkBelowThresholdAborts(t *testing.T) {
	cache := make(map[string]*CreditEntry, 10)
	for i := 0; i < 10; i++ {
		k := fmt.Sprintf("a%d", i)
		cache[k] = &CreditEntry{AuthID: k, Provider: "claude"}
	}
	withCreditsCache(t, cache, func() {
		// 默认阈值 50%：10 条 → 50% = 5 条。本轮 4 条 < 5 → abort
		shrunk := []authFileLite{
			{ID: "a0", Provider: "claude"},
			{ID: "a1", Provider: "claude"},
			{ID: "a2", Provider: "claude"},
			{ID: "a3", Provider: "claude"},
		}
		if validateAuthFilesResponse(shrunk) {
			t.Errorf("shrink from 10→4 (40%%) should ABORT")
		}
		// 5 条恰好达到阈值（50%）→ 通过（边界包含等于）
		atThreshold := append(shrunk, authFileLite{ID: "a4", Provider: "claude"})
		if !validateAuthFilesResponse(atThreshold) {
			t.Errorf("shrink from 10→5 (exactly 50%%) should be accepted (boundary)")
		}
		// 6 条 > 50% → 通过
		above := append(atThreshold, authFileLite{ID: "a5", Provider: "claude"})
		if !validateAuthFilesResponse(above) {
			t.Errorf("shrink from 10→6 (60%%) should be accepted")
		}
	})
}

// TestValidateAuthFilesResponse_MalformedFilteredButValidPassesThreshold 部分 malformed
// 不影响 valid 部分的判定：valid 数量过阈值就接受，过滤的 malformed 仅日志记录。
func TestValidateAuthFilesResponse_MalformedFilteredButValidPassesThreshold(t *testing.T) {
	cache := map[string]*CreditEntry{
		"a1": {AuthID: "a1", Provider: "claude"},
		"a2": {AuthID: "a2", Provider: "claude"},
		"a3": {AuthID: "a3", Provider: "claude"},
		"a4": {AuthID: "a4", Provider: "claude"},
	}
	withCreditsCache(t, cache, func() {
		// 4 条上轮 → 阈值 = 2 条
		// 本轮：2 valid + 3 malformed → valid_count=2 >= 2 → 通过
		mixed := []authFileLite{
			{ID: "a1", Provider: "claude"},
			{ID: "a2", Provider: "claude"},
			{ID: "", Provider: "claude"},
			{ID: "a3", Provider: ""},
			{ID: "   ", Provider: "   "},
		}
		if !validateAuthFilesResponse(mixed) {
			t.Errorf("2 valid + 3 malformed (valid>=threshold) should be accepted")
		}
	})
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
