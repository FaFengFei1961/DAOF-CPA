package proxy

// coverage_fillers_v4_test.go
//
// M-R3 增量 4：拆完大文件后挑第四批 0% 函数补 characterization 测试。
// 主要覆盖 stream_precheck.go 4 个 pure helper + video_parse.go 1 个 normalizer
// + subscription_cache.go 2 个 cache 操作。

import (
	"strings"
	"testing"
	"time"

	"daof-cpa/database"

	"github.com/gofiber/fiber/v2"
)

// ─── stream_precheck.go::firstNonEmptyString ──────────────────────────────────

func TestFirstNonEmptyString(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want string
	}{
		{"empty list", []string{}, ""},
		{"all empty", []string{"", "  ", "\t"}, ""},
		{"first non-empty", []string{"hello", "world"}, "hello"},
		{"skip blanks", []string{"", "  ", "first", "ignored"}, "first"},
		{"preserve original whitespace", []string{"  hello  "}, "  hello  "},
	}
	for _, c := range cases {
		got := firstNonEmptyString(c.in...)
		if got != c.want {
			t.Errorf("%s: got=%q want=%q", c.name, got, c.want)
		}
	}
}

// ─── stream_precheck.go::precheckQuotaMicroValues ─────────────────────────────

func TestPrecheckQuotaMicroValues(t *testing.T) {
	// Non api_cost_usd unit → 全 0
	d1 := EngineDecision{BlockUnit: "tokens", BlockLimitMicroUSD: 100}
	if l, u, r := precheckQuotaMicroValues(d1); l != 0 || u != 0 || r != 0 {
		t.Errorf("non api_cost_usd unit should yield 0,0,0; got %d,%d,%d", l, u, r)
	}

	// micro 字段已填 → 直接用
	d2 := EngineDecision{
		BlockUnit:              "api_cost_usd",
		BlockLimitMicroUSD:     5_000_000,
		BlockConsumedMicroUSD:  1_500_000,
		BlockRemainingMicroUSD: 3_500_000,
	}
	l, u, r := precheckQuotaMicroValues(d2)
	if l != 5_000_000 || u != 1_500_000 || r != 3_500_000 {
		t.Errorf("micro values not preserved: %d, %d, %d", l, u, r)
	}

	// micro 字段全 0 + USD float fallback
	d3 := EngineDecision{
		BlockUnit:           "api_cost_usd",
		BlockLimitValue:     2.5,
		BlockConsumedValue:  1.0,
		BlockRemaining:      1.5,
	}
	l, u, r = precheckQuotaMicroValues(d3)
	if l != 2_500_000 || u != 1_000_000 || r != 1_500_000 {
		t.Errorf("USD→micro fallback wrong: %d, %d, %d", l, u, r)
	}

	// 负 BlockRemaining → 钳到 0
	d4 := EngineDecision{BlockUnit: "api_cost_usd", BlockRemaining: -10}
	_, _, r = precheckQuotaMicroValues(d4)
	if r != 0 {
		t.Errorf("negative remaining should clamp to 0; got %d", r)
	}
}

// ─── stream_precheck.go::precheckLimitMessage ─────────────────────────────────

func TestPrecheckLimitMessage(t *testing.T) {
	billing := BillingRuleResolution{ChargedCostMicroUSD: 250_000}

	// api_cost_usd 分支
	d1 := EngineDecision{BlockUnit: "api_cost_usd", BlockRemaining: 1.5}
	msg := precheckLimitMessage(d1, billing)
	if !strings.Contains(msg, "credits") || !strings.Contains(msg, "0.250000") {
		t.Errorf("api_cost_usd branch: %s", msg)
	}

	// 其它 unit 分支
	d2 := EngineDecision{BlockUnit: "tokens", BlockDelta: 5000, BlockRemaining: 1000}
	msg = precheckLimitMessage(d2, billing)
	if !strings.Contains(msg, "5000 tokens") || !strings.Contains(msg, "1000 tokens") {
		t.Errorf("tokens branch: %s", msg)
	}

	// 空 unit → 通用消息
	d3 := EngineDecision{}
	msg = precheckLimitMessage(d3, billing)
	if !strings.Contains(msg, "超过当前窗口剩余额度") {
		t.Errorf("default branch: %s", msg)
	}
}

// ─── stream_precheck.go::precheckLimitErrorPayload ────────────────────────────

func TestPrecheckLimitErrorPayload(t *testing.T) {
	end := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	d := EngineDecision{
		BlockUnit:        "api_cost_usd",
		BlockQuotaPlanID: 7,
		BlockLimitValue:  10.0,
		BlockConsumedValue: 8.5,
		BlockRemaining:   1.5,
		BlockWindowEndAt: &end,
	}
	billing := BillingRuleResolution{
		RawCostMicroUSD:     500_000,
		ChargedCostMicroUSD: 750_000,
		ModelWeight:         1.5,
		HealthMultiplier:    1.0,
	}
	payload := precheckLimitErrorPayload("超额", d, 100, 50, billing)

	errMap, ok := payload["error"].(fiber.Map)
	if !ok {
		t.Fatalf("payload[error] not fiber.Map: %T", payload["error"])
	}
	if errMap["message"] != "超额" {
		t.Errorf("message=%v want 超额", errMap["message"])
	}
	if errMap["message_code"] != "ERR_REQUEST_ESTIMATE_EXCEEDS_WINDOW_REMAINING" {
		t.Errorf("message_code=%v", errMap["message_code"])
	}
	details, ok := errMap["details"].(fiber.Map)
	if !ok {
		t.Fatalf("details not fiber.Map: %T", errMap["details"])
	}
	if details["quota_plan_id"] != uint(7) {
		t.Errorf("quota_plan_id=%v", details["quota_plan_id"])
	}
	if details["precheck_input_tokens"] != 100 {
		t.Errorf("precheck_input_tokens=%v", details["precheck_input_tokens"])
	}
	if details["window_end_at"] != "2026-05-19T12:00:00Z" {
		t.Errorf("window_end_at=%v", details["window_end_at"])
	}

	// 不带 window_end_at 时 details 不应有该字段
	d2 := EngineDecision{BlockUnit: "api_cost_usd"}
	payload2 := precheckLimitErrorPayload("nope", d2, 1, 1, billing)
	errMap2 := payload2["error"].(fiber.Map)
	details2 := errMap2["details"].(fiber.Map)
	if _, has := details2["window_end_at"]; has {
		t.Errorf("nil window_end_at should not be in details")
	}
}

// ─── video_parse.go::normalizeVideoAspectRatio ────────────────────────────────

func TestNormalizeVideoAspectRatio(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"1:1", "1:1"},
		{"  square  ", "1:1"},
		{"SQUARE", "1:1"},
		{"16:9", "16:9"},
		{"landscape", "16:9"},
		{"9:16", "9:16"},
		{"portrait", "9:16"},
		{"4:3", "4:3"},
		{"3:4", "3:4"},
		{"3:2", "3:2"},
		{"2:3", "2:3"},
	}
	for _, c := range cases {
		got, err := normalizeVideoAspectRatio(c.in)
		if err != nil {
			t.Errorf("%q: unexpected error %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("%q: got=%q want=%q", c.in, got, c.want)
		}
	}

	// 拒绝非法
	invalid := []string{"", "  ", "5:7", "1080p", "ratio"}
	for _, in := range invalid {
		if _, err := normalizeVideoAspectRatio(in); err == nil {
			t.Errorf("%q should be rejected", in)
		}
	}
}

// ─── subscription_cache.go::InvalidateUserSubscriptionCache + evictCacheLocked ─

func TestInvalidateUserSubscriptionCache(t *testing.T) {
	// 保存现有 cache，测后还原
	subCacheMu.Lock()
	orig := subCache
	subCache = map[uint]*userSubsBucket{
		11: {expiresAt: time.Now().Add(1 * time.Hour)},
		12: {expiresAt: time.Now().Add(1 * time.Hour)},
	}
	subCacheMu.Unlock()
	t.Cleanup(func() {
		subCacheMu.Lock()
		subCache = orig
		subCacheMu.Unlock()
	})

	InvalidateUserSubscriptionCache(11)
	subCacheMu.RLock()
	_, has11 := subCache[11]
	_, has12 := subCache[12]
	size := len(subCache)
	subCacheMu.RUnlock()
	if has11 {
		t.Error("InvalidateUserSubscriptionCache(11) failed: still has 11")
	}
	if !has12 {
		t.Error("invalidating 11 evicted wrong entry")
	}
	if size != 1 {
		t.Errorf("after invalidate, size=%d want 1", size)
	}

	// invalidate 不存在的 key 安全（no-op）
	InvalidateUserSubscriptionCache(999)
	subCacheMu.RLock()
	size = len(subCache)
	subCacheMu.RUnlock()
	if size != 1 {
		t.Errorf("invalidating non-existent key changed size to %d", size)
	}
}

func TestEvictCacheLocked_DropsExpiredEntries(t *testing.T) {
	subCacheMu.Lock()
	orig := subCache
	now := time.Now()
	subCache = map[uint]*userSubsBucket{
		1: {expiresAt: now.Add(-1 * time.Hour)},  // 过期
		2: {expiresAt: now.Add(-1 * time.Minute)}, // 过期
		3: {expiresAt: now.Add(1 * time.Hour)},   // 未过期
		4: {expiresAt: now.Add(2 * time.Hour)},   // 未过期
	}
	// 容量上限大于现有 → 仅清过期
	evictCacheLocked(100, now)
	defer func() {
		subCache = orig
		subCacheMu.Unlock()
	}()

	if _, has := subCache[1]; has {
		t.Error("expired entry 1 should be evicted")
	}
	if _, has := subCache[2]; has {
		t.Error("expired entry 2 should be evicted")
	}
	if _, has := subCache[3]; !has {
		t.Error("non-expired entry 3 should remain")
	}
	if _, has := subCache[4]; !has {
		t.Error("non-expired entry 4 should remain")
	}
}

func TestEvictCacheLocked_LRUWhenOverCapacity(t *testing.T) {
	subCacheMu.Lock()
	orig := subCache
	now := time.Now()
	subCache = map[uint]*userSubsBucket{
		1: {expiresAt: now.Add(time.Hour)},
		2: {expiresAt: now.Add(time.Hour)},
		3: {expiresAt: now.Add(time.Hour)},
		4: {expiresAt: now.Add(time.Hour)},
		5: {expiresAt: now.Add(time.Hour)},
	}
	// 设置 lastUsed 让 1 最旧、5 最新
	for i := uint(1); i <= 5; i++ {
		subCache[i].lastUsedNS.Store(int64(i) * int64(time.Second))
	}
	// 容量 = 5 → 不驱逐
	evictCacheLocked(5, now)
	if len(subCache) != 5 {
		t.Errorf("at capacity should not evict; got %d", len(subCache))
	}
	// 容量 = 2 → 驱逐到 target = 2 * 8/10 = 1
	evictCacheLocked(2, now)
	if len(subCache) > 2 {
		t.Errorf("over capacity evict: got %d want <=2", len(subCache))
	}
	// 5 (最新) 应该还在
	if _, has := subCache[5]; !has {
		t.Error("most-recently-used (5) should be kept")
	}
	subCache = orig
	subCacheMu.Unlock()
}

// 用 USDToMicro 验证我们没用错单位
func TestUSDToMicroSanity(t *testing.T) {
	m, _ := database.USDToMicro(1.0)
	if m != 1_000_000 {
		t.Errorf("USDToMicro(1.0)=%d want 1_000_000", m)
	}
	m, _ = database.USDToMicro(0.000001)
	if m != 1 {
		t.Errorf("USDToMicro(0.000001)=%d want 1", m)
	}
}
