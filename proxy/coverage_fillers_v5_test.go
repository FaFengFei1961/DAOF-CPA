package proxy

// coverage_fillers_v5_test.go
//
// M-R3 增量 5：credits_pool 拆出 5 个子文件后，对其中不依赖 DB/HTTP/上游的
// 纯 helper 补 characterization 测试。
//
// 覆盖：
//   - credits_pool.go::anyToStr / computeNextRetryAt / IsRefreshing /
//     getRefreshIntervalDuration / getRetryIntervalDuration / getMaxRetries
//   - credits_pool_cpa.go::urlQueryEscape / projectIDRefreshInterval /
//     projectIDRefreshJitter / parseProjectIDFromAuthJSON /
//     SanitizeErrorMessage / getIntConfig
//   - credits_pool_google.go::normalizeGoogleTierBadge /
//     pickGoogleCodeAssistTier / antigravityModelUsedPct /
//     antigravityModelReset / geminiCliStr / geminiCliStripVertex /
//     geminiCliFraction / geminiCliFloat / geminiCliResetTime
//   - credits_pool_other.go::codexPickWindow / codexWindowSeconds /
//     codexBuildWindow
//   - content_moderation.go::highestCategory

import (
	"testing"
	"time"
)

// ─── 工具：在测试期间整体替换 SysConfigCache（与 moderation_test.go::withSysConfig
// 不同——那个是合并 kv 到既有缓存，这里需要完全替换以测试默认值分支）─────────────

func withSysConfigReplace(t *testing.T, kv map[string]string) {
	t.Helper()
	SysConfigMutex.Lock()
	old := SysConfigCache
	SysConfigCache = kv
	SysConfigMutex.Unlock()
	t.Cleanup(func() {
		SysConfigMutex.Lock()
		SysConfigCache = old
		SysConfigMutex.Unlock()
	})
}

// ─── credits_pool.go ──────────────────────────────────────────────────────

func TestAnyToStr(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"string", "hello", "hello"},
		{"float64 整数", float64(3), "3"},
		{"float64 小数", 1.5, "1.5"},
		{"int", 42, "42"},
		{"bool true", true, "true"},
		{"bool false", false, "false"},
		{"unsupported", []string{"x"}, ""},
		{"nil", nil, ""},
	}
	for _, c := range cases {
		got := anyToStr(c.in)
		if got != c.want {
			t.Errorf("%s: got=%q want=%q", c.name, got, c.want)
		}
	}
}

func TestComputeNextRetryAt(t *testing.T) {
	withSysConfigReplace(t, map[string]string{"credits_retry_interval": "1"}) // 1min base

	// retryCount=1 → base * 2^0 = 1min
	got := computeNextRetryAt(1)
	delta := time.Until(got)
	if delta < 30*time.Second || delta > 90*time.Second {
		t.Errorf("retry=1 应该 ~1 分钟, got %v", delta)
	}

	// retryCount=2 → base * 2 = 2min
	got = computeNextRetryAt(2)
	delta = time.Until(got)
	if delta < 90*time.Second || delta > 150*time.Second {
		t.Errorf("retry=2 应该 ~2 分钟, got %v", delta)
	}

	// 大 retryCount 应被 cap 在 60 分钟
	got = computeNextRetryAt(100)
	delta = time.Until(got)
	if delta < 50*time.Minute || delta > 70*time.Minute {
		t.Errorf("retry=100 应钳到 60 分钟, got %v", delta)
	}

	// 0 / 负数 → 当 1 处理
	got = computeNextRetryAt(0)
	delta = time.Until(got)
	if delta < 30*time.Second || delta > 90*time.Second {
		t.Errorf("retry=0 应该 ~1 分钟, got %v", delta)
	}
}

func TestIsRefreshing(t *testing.T) {
	// fix Phase B (2026-05-19)：原断言把"函数返回值"和"函数自己读的底层值"对比，
	// 永远成立 → 即使 IsRefreshing 实现写反也不会被发现。改为显式 Store 后断言。
	prev := creditsRefreshing.Load()
	t.Cleanup(func() { creditsRefreshing.Store(prev) })

	creditsRefreshing.Store(true)
	if !IsRefreshing() {
		t.Error("IsRefreshing()=false after Store(true)")
	}
	creditsRefreshing.Store(false)
	if IsRefreshing() {
		t.Error("IsRefreshing()=true after Store(false)")
	}
}

func TestGetRefreshIntervalDuration(t *testing.T) {
	withSysConfigReplace(t, map[string]string{"credits_refresh_interval": "30"})
	if d := getRefreshIntervalDuration(); d != 30*time.Minute {
		t.Errorf("config=30, got %v", d)
	}

	withSysConfigReplace(t, map[string]string{"credits_refresh_interval": "0"})
	if d := getRefreshIntervalDuration(); d != 15*time.Minute {
		t.Errorf("config=0 应退到默认 15min, got %v", d)
	}

	withSysConfigReplace(t, map[string]string{})
	if d := getRefreshIntervalDuration(); d != 15*time.Minute {
		t.Errorf("空配置应默认 15min, got %v", d)
	}
}

func TestGetRetryIntervalDuration(t *testing.T) {
	withSysConfigReplace(t, map[string]string{"credits_retry_interval": "10"})
	if d := getRetryIntervalDuration(); d != 10*time.Minute {
		t.Errorf("config=10, got %v", d)
	}

	withSysConfigReplace(t, map[string]string{"credits_retry_interval": "-5"})
	if d := getRetryIntervalDuration(); d != 5*time.Minute {
		t.Errorf("config<1 应退到默认 5min, got %v", d)
	}
}

func TestGetMaxRetries(t *testing.T) {
	withSysConfigReplace(t, map[string]string{"credits_max_retries": "7"})
	if n := getMaxRetries(); n != 7 {
		t.Errorf("config=7, got %d", n)
	}

	withSysConfigReplace(t, map[string]string{}) // 默认
	if n := getMaxRetries(); n != 3 {
		t.Errorf("空配置应默认 3, got %d", n)
	}

	// 0 显式表示 unlimited，应保留
	withSysConfigReplace(t, map[string]string{"credits_max_retries": "0"})
	if n := getMaxRetries(); n != 0 {
		t.Errorf("config=0 应保留为 0 (unlimited), got %d", n)
	}
}

// ─── credits_pool_cpa.go ──────────────────────────────────────────────────

func TestUrlQueryEscape(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"abc", "abc"},
		{"a b", "a+b"},
		{"a@b.com", "a%40b.com"},
		{"k=v&x=y", "k%3Dv%26x%3Dy"},
		{"", ""},
	}
	for _, c := range cases {
		if got := urlQueryEscape(c.in); got != c.want {
			t.Errorf("urlQueryEscape(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestProjectIDRefreshInterval(t *testing.T) {
	withSysConfigReplace(t, map[string]string{"cpa_project_id_refresh_seconds": "3600"})
	if d := projectIDRefreshInterval(); d != 1*time.Hour {
		t.Errorf("config=3600, got %v", d)
	}

	// < minSec (300) → 退到默认
	withSysConfigReplace(t, map[string]string{"cpa_project_id_refresh_seconds": "60"})
	if d := projectIDRefreshInterval(); d != 24*time.Hour {
		t.Errorf("config<minSec 应退到默认 24h, got %v", d)
	}

	// 空配置
	withSysConfigReplace(t, map[string]string{})
	if d := projectIDRefreshInterval(); d != 24*time.Hour {
		t.Errorf("空配置应默认 24h, got %v", d)
	}

	// 非整数 → 默认
	withSysConfigReplace(t, map[string]string{"cpa_project_id_refresh_seconds": "abc"})
	if d := projectIDRefreshInterval(); d != 24*time.Hour {
		t.Errorf("config 非整数应默认 24h, got %v", d)
	}
}

func TestProjectIDRefreshJitter(t *testing.T) {
	interval := 24 * time.Hour

	// 空 authID → 0
	if d := projectIDRefreshJitter("", interval); d != 0 {
		t.Errorf("authID 空应返回 0, got %v", d)
	}
	// 0 interval → 0
	if d := projectIDRefreshJitter("auth-x", 0); d != 0 {
		t.Errorf("interval 0 应返回 0, got %v", d)
	}

	// 确定性：相同 authID 每次都该返回相同值
	d1 := projectIDRefreshJitter("auth-1", interval)
	d2 := projectIDRefreshJitter("auth-1", interval)
	if d1 != d2 {
		t.Errorf("jitter 非确定性: %v vs %v", d1, d2)
	}

	// 区间 [0, interval/4)
	maxJitter := interval / 4
	for _, id := range []string{"a", "b", "c", "abcdef", "long-auth-id-value"} {
		d := projectIDRefreshJitter(id, interval)
		if d < 0 || d >= maxJitter {
			t.Errorf("jitter(%q)=%v 应落在 [0, %v)", id, d, maxJitter)
		}
	}
}

func TestParseProjectIDFromAuthJSON(t *testing.T) {
	cases := []struct {
		name string
		data string
		want string
	}{
		{
			"cloudaicompanionProject.id 优先",
			`{"cloudaicompanionProject":{"id":"proj-abc-123"},"metadata":{"project_id":"other-1"}}`,
			"proj-abc-123",
		},
		{
			"cloudaicompanionProject 字符串",
			`{"cloudaicompanionProject":"proj-string-id-1"}`,
			"proj-string-id-1",
		},
		{
			"metadata.project_id 候选",
			`{"metadata":{"project_id":"proj-meta-x9"}}`,
			"proj-meta-x9",
		},
		{
			"project_id 兜底",
			`{"project_id":"proj-bottom-id"}`,
			"proj-bottom-id",
		},
		{
			"GCP 格式非法应跳过",
			`{"project_id":"BAD_NAME_TOO!@#"}`,
			"",
		},
		{
			"object 候选不会被 stringify",
			`{"cloudaicompanionProject":{"complex":"x"}}`,
			"",
		},
		{
			"完全没有候选",
			`{"unrelated":"x"}`,
			"",
		},
	}
	for _, c := range cases {
		got := parseProjectIDFromAuthJSON([]byte(c.data))
		if got != c.want {
			t.Errorf("%s: got=%q want=%q", c.name, got, c.want)
		}
	}
}

func TestSanitizeErrorMessage(t *testing.T) {
	// Bearer token 脱敏
	out := SanitizeErrorMessage("Authorization: Bearer abc123def", 200)
	if out == "Authorization: Bearer abc123def" {
		t.Errorf("Bearer 未脱敏: %q", out)
	}
	// api_key=xxx 脱敏
	out = SanitizeErrorMessage("oops api_key=AKIA12345", 200)
	if out == "oops api_key=AKIA12345" {
		t.Errorf("api_key 未脱敏: %q", out)
	}
	// maxLen 截断
	long := "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
	out = SanitizeErrorMessage(long, 10)
	if len(out) > 30 { // sanitize 函数可能加省略号
		t.Errorf("未按 maxLen 截断: got len=%d", len(out))
	}
}

func TestGetIntConfig(t *testing.T) {
	withSysConfigReplace(t, map[string]string{"k1": "42", "k2": "  100  ", "k3": "abc"})

	if v := getIntConfig("k1", 7); v != 42 {
		t.Errorf("k1=42, got %d", v)
	}
	if v := getIntConfig("k2", 7); v != 100 {
		t.Errorf("k2 trimmed=100, got %d", v)
	}
	if v := getIntConfig("k3", 7); v != 7 {
		t.Errorf("k3 非整数应默认 7, got %d", v)
	}
	if v := getIntConfig("missing", 99); v != 99 {
		t.Errorf("missing 应默认 99, got %d", v)
	}
}

// ─── credits_pool_google.go ───────────────────────────────────────────────

func TestNormalizeGoogleTierBadge(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "UNKNOWN"},
		{"  ", "UNKNOWN"},
		{"ULTRA-TIER", "ULTRA"},
		{"  Ultra Annual  ", "ULTRA"},
		{"standard-tier", "FREE"},
		{"FREE-TIER", "FREE"},
		{"free-user", "FREE"},
		{"PRO-TIER", "PRO"},
		{"premium-monthly", "PRO"},
		{"other-thing", "UNKNOWN"},
	}
	for _, c := range cases {
		if got := normalizeGoogleTierBadge(c.in); got != c.want {
			t.Errorf("%q: got=%q want=%q", c.in, got, c.want)
		}
	}
}

func TestPickGoogleCodeAssistTier(t *testing.T) {
	// PaidTier 优先
	body := []byte(`{"paidTier":{"id":"ULTRA-TIER"},"currentTier":{"id":"FREE-TIER"}}`)
	if got := pickGoogleCodeAssistTier(body); got != "ULTRA" {
		t.Errorf("paidTier 优先: got %q", got)
	}

	// 无 PaidTier → CurrentTier
	body = []byte(`{"currentTier":{"id":"PRO-TIER"}}`)
	if got := pickGoogleCodeAssistTier(body); got != "PRO" {
		t.Errorf("currentTier: got %q", got)
	}

	// 无 Paid/Current → AllowedTiers isDefault
	body = []byte(`{"allowedTiers":[{"id":"ignore","isDefault":false},{"id":"FREE-TIER","isDefault":true}]}`)
	if got := pickGoogleCodeAssistTier(body); got != "FREE" {
		t.Errorf("allowedTiers default: got %q", got)
	}

	// 完全无候选 → 空
	body = []byte(`{}`)
	if got := pickGoogleCodeAssistTier(body); got != "" {
		t.Errorf("empty body: got %q want empty", got)
	}

	// 非法 JSON → 空
	if got := pickGoogleCodeAssistTier([]byte(`not json`)); got != "" {
		t.Errorf("invalid JSON: got %q want empty", got)
	}
}

func TestAntigravityModelUsedPct(t *testing.T) {
	// utilization 字段：parseUsedPercent 不做单位换算，原样返回
	m := map[string]any{
		"quota": map[string]any{"utilization": 42.0},
	}
	if v := antigravityModelUsedPct(m); v != 42 {
		t.Errorf("quota.utilization=42: got %v", v)
	}

	// quota.consumed/limit 兜底：自己计算并 clamp 到 [0,100]
	m = map[string]any{
		"quota": map[string]any{"consumed": 25.0, "limit": 100.0},
	}
	if v := antigravityModelUsedPct(m); v != 25 {
		t.Errorf("consumed/limit 25/100: got %v", v)
	}

	// consumed>limit → clamp 到 100
	m = map[string]any{
		"quota": map[string]any{"consumed": 150.0, "limit": 100.0},
	}
	if v := antigravityModelUsedPct(m); v != 100 {
		t.Errorf("consumed>limit 应 clamp 到 100, got %v", v)
	}

	// 顶层 utilization
	m = map[string]any{"utilization": 60.0}
	if v := antigravityModelUsedPct(m); v != 60 {
		t.Errorf("顶层 utilization=60: got %v", v)
	}

	// 啥都没有
	m = map[string]any{}
	if v := antigravityModelUsedPct(m); v != 0 {
		t.Errorf("空 map: got %v", v)
	}
}

func TestAntigravityModelReset(t *testing.T) {
	want, _ := time.Parse(time.RFC3339, "2026-12-31T00:00:00Z")

	// quota.resets_at
	m := map[string]any{
		"quota": map[string]any{"resets_at": "2026-12-31T00:00:00Z"},
	}
	if got := antigravityModelReset(m); !got.Equal(want) {
		t.Errorf("quota.resets_at: got %v want %v", got, want)
	}

	// 顶层 resetAt
	m = map[string]any{"resetAt": "2026-12-31T00:00:00Z"}
	if got := antigravityModelReset(m); !got.Equal(want) {
		t.Errorf("顶层 resetAt: got %v want %v", got, want)
	}

	// 无字段 → zero
	m = map[string]any{"other": "x"}
	if got := antigravityModelReset(m); !got.IsZero() {
		t.Errorf("无字段应返回 zero, got %v", got)
	}

	// 非法格式 → zero
	m = map[string]any{"resets_at": "not-a-date"}
	if got := antigravityModelReset(m); !got.IsZero() {
		t.Errorf("非法格式应返回 zero, got %v", got)
	}
}

func TestGeminiCliStr(t *testing.T) {
	// 第一个非空 string
	if got := geminiCliStr("", "  ", "hello", "world"); got != "hello" {
		t.Errorf("got %q want hello", got)
	}
	// 非 string 跳过
	if got := geminiCliStr(42, true, "real"); got != "real" {
		t.Errorf("non-string skip: got %q want real", got)
	}
	// 全空
	if got := geminiCliStr("", "  ", "\t"); got != "" {
		t.Errorf("全空 want empty, got %q", got)
	}
	// 空 variadic
	if got := geminiCliStr(); got != "" {
		t.Errorf("空 variadic want empty, got %q", got)
	}
}

func TestGeminiCliStripVertex(t *testing.T) {
	if got := geminiCliStripVertex("gemini-2.5-pro_vertex"); got != "gemini-2.5-pro" {
		t.Errorf("got %q", got)
	}
	if got := geminiCliStripVertex("gemini-2.5-pro"); got != "gemini-2.5-pro" {
		t.Errorf("无后缀不应改: got %q", got)
	}
	if got := geminiCliStripVertex(""); got != "" {
		t.Errorf("空串: got %q", got)
	}
	if got := geminiCliStripVertex("_vertex"); got != "" {
		t.Errorf("纯后缀: got %q", got)
	}
}

func TestGeminiCliFloat(t *testing.T) {
	b := map[string]any{
		"a": 3.14,
		"b": 7,
		"c": int64(99),
		"d": "1.5",
		"e": "50%",
		"f": "",
		"g": "not-a-number",
	}
	cases := []struct {
		name string
		keys []string
		want float64
		ok   bool
	}{
		{"float64", []string{"a"}, 3.14, true},
		{"int", []string{"b"}, 7, true},
		{"int64", []string{"c"}, 99, true},
		{"numeric string", []string{"d"}, 1.5, true},
		{"percent string", []string{"e"}, 0.5, true},
		{"empty string skip", []string{"f", "a"}, 3.14, true},
		{"bad string", []string{"g"}, 0, false},
		{"missing", []string{"missing"}, 0, false},
		{"fallback chain", []string{"missing1", "a"}, 3.14, true},
	}
	for _, c := range cases {
		v, ok := geminiCliFloat(b, c.keys...)
		if ok != c.ok || (ok && v != c.want) {
			t.Errorf("%s: got=(%v,%v) want=(%v,%v)", c.name, v, ok, c.want, c.ok)
		}
	}
}

func TestGeminiCliFraction(t *testing.T) {
	// remainingFraction 有值
	b := map[string]any{"remainingFraction": 0.75}
	if got := geminiCliFraction(b); got != 0.75 {
		t.Errorf("fraction=0.75: got %v", got)
	}

	// clamp 到 [0,1]
	b = map[string]any{"remainingFraction": 1.5}
	if got := geminiCliFraction(b); got != 1 {
		t.Errorf("clamp >1: got %v", got)
	}
	b = map[string]any{"remainingFraction": -0.2}
	if got := geminiCliFraction(b); got != 0 {
		t.Errorf("clamp <0: got %v", got)
	}

	// remainingAmount=0 → 0
	b = map[string]any{"remainingAmount": 0}
	if got := geminiCliFraction(b); got != 0 {
		t.Errorf("amount=0: got %v", got)
	}

	// 无数量但有 resetTime → 0（已耗尽）
	b = map[string]any{"resetTime": "2026-12-31T00:00:00Z"}
	if got := geminiCliFraction(b); got != 0 {
		t.Errorf("无数量+resetTime: got %v", got)
	}

	// 全空 → -1
	b = map[string]any{}
	if got := geminiCliFraction(b); got != -1 {
		t.Errorf("全空应返回 -1: got %v", got)
	}
}

func TestGeminiCliResetTime(t *testing.T) {
	want, _ := time.Parse(time.RFC3339, "2026-06-01T12:00:00Z")

	// resetTime
	if got := geminiCliResetTime(map[string]any{"resetTime": "2026-06-01T12:00:00Z"}); !got.Equal(want) {
		t.Errorf("resetTime: got %v", got)
	}
	// reset_time
	if got := geminiCliResetTime(map[string]any{"reset_time": "2026-06-01T12:00:00Z"}); !got.Equal(want) {
		t.Errorf("reset_time: got %v", got)
	}
	// resetsAt
	if got := geminiCliResetTime(map[string]any{"resetsAt": "2026-06-01T12:00:00Z"}); !got.Equal(want) {
		t.Errorf("resetsAt: got %v", got)
	}

	// 空 / 非法 → zero
	if got := geminiCliResetTime(map[string]any{}); !got.IsZero() {
		t.Errorf("empty map: got %v", got)
	}
	if got := geminiCliResetTime(map[string]any{"resetTime": "not-iso"}); !got.IsZero() {
		t.Errorf("非法格式: got %v", got)
	}
	if got := geminiCliResetTime(map[string]any{"resetTime": "  "}); !got.IsZero() {
		t.Errorf("空白: got %v", got)
	}
}

// ─── credits_pool_other.go ────────────────────────────────────────────────

func TestCodexWindowSeconds(t *testing.T) {
	cases := []struct {
		name string
		w    map[string]any
		want float64
		ok   bool
	}{
		{"snake_case float", map[string]any{"limit_window_seconds": 300.0}, 300, true},
		{"camelCase int", map[string]any{"limitWindowSeconds": 60}, 60, true},
		{"int64", map[string]any{"limit_window_seconds": int64(120)}, 120, true},
		{"missing", map[string]any{}, 0, false},
		{"non-numeric", map[string]any{"limit_window_seconds": "abc"}, 0, false},
	}
	for _, c := range cases {
		v, ok := codexWindowSeconds(c.w)
		if ok != c.ok || (ok && v != c.want) {
			t.Errorf("%s: got=(%v,%v) want=(%v,%v)", c.name, v, ok, c.want, c.ok)
		}
	}
}

func TestCodexPickWindow(t *testing.T) {
	primary := map[string]any{"limit_window_seconds": 300.0, "label": "p"}
	secondary := map[string]any{"limit_window_seconds": 86400.0, "label": "s"}
	rl := map[string]any{
		"primary_window":   primary,
		"secondary_window": secondary,
	}

	// 匹配 primary
	if got := codexPickWindow(rl, 300); got["label"] != "p" {
		t.Errorf("匹配 primary 失败: got %v", got)
	}
	// 匹配 secondary
	if got := codexPickWindow(rl, 86400); got["label"] != "s" {
		t.Errorf("匹配 secondary 失败: got %v", got)
	}
	// 都不匹配
	if got := codexPickWindow(rl, 60); got != nil {
		t.Errorf("60s 不匹配应返回 nil, got %v", got)
	}
	// nil rl
	if got := codexPickWindow(nil, 300); got != nil {
		t.Errorf("nil rl 应返回 nil, got %v", got)
	}
	// camelCase 兼容
	rlCamel := map[string]any{
		"primaryWindow": map[string]any{"limitWindowSeconds": 300.0, "label": "pc"},
	}
	if got := codexPickWindow(rlCamel, 300); got["label"] != "pc" {
		t.Errorf("camelCase: got %v", got)
	}
}

func TestCodexBuildWindow(t *testing.T) {
	resetT, _ := time.Parse(time.RFC3339, "2026-08-15T10:00:00Z")

	// used_percent 直接给定
	w := map[string]any{
		"used_percent": 35.0,
		"resets_at":    "2026-08-15T10:00:00Z",
	}
	got := codexBuildWindow("primary", "Primary 5h", w, nil)
	if got.ID != "primary" || got.Label != "Primary 5h" {
		t.Errorf("ID/Label: %+v", got)
	}
	if got.UsedPercent != 35 || got.RemainingPercent != 65 {
		t.Errorf("used=35: got used=%v remain=%v", got.UsedPercent, got.RemainingPercent)
	}
	if !got.ResetsAt.Equal(resetT) {
		t.Errorf("resets_at: got %v want %v", got.ResetsAt, resetT)
	}

	// limit_reached + 有 reset_at → 兜底 100%
	w = map[string]any{"resets_at": "2026-08-15T10:00:00Z"}
	rl := map[string]any{"limit_reached": true}
	got = codexBuildWindow("p", "P", w, rl)
	if got.UsedPercent != 100 {
		t.Errorf("limit_reached+reset 应 100%%, got %v", got.UsedPercent)
	}

	// limit_reached 但无 reset_at → 不兜底（视为永久封禁/配置错误）
	w = map[string]any{}
	rl = map[string]any{"limit_reached": true}
	got = codexBuildWindow("p", "P", w, rl)
	if got.UsedPercent != 0 {
		t.Errorf("limit_reached 无 reset 不应兜底, got %v", got.UsedPercent)
	}

	// allowed=false + 有 reset_at → 兜底 100%
	w = map[string]any{"resets_at": "2026-08-15T10:00:00Z"}
	rl = map[string]any{"allowed": false}
	got = codexBuildWindow("p", "P", w, rl)
	if got.UsedPercent != 100 {
		t.Errorf("allowed=false+reset 应 100%%, got %v", got.UsedPercent)
	}
}

// ─── content_moderation.go ─────────────────────────────────────────────────

func TestHighestCategory(t *testing.T) {
	// 正常
	cats := map[string]float64{"hate": 0.1, "violence": 0.7, "sexual": 0.3}
	name, score := highestCategory(cats)
	if name != "violence" || score != 0.7 {
		t.Errorf("got=(%q,%v) want=(violence,0.7)", name, score)
	}

	// 空
	name, score = highestCategory(map[string]float64{})
	if name != "" || score != 0 {
		t.Errorf("empty: got=(%q,%v)", name, score)
	}

	// 全 0 → 名字保持空（hi==0 不会被更新）
	name, score = highestCategory(map[string]float64{"a": 0, "b": 0})
	if name != "" || score != 0 {
		t.Errorf("全 0: got=(%q,%v)", name, score)
	}
}
