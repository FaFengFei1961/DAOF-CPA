package proxy

import (
	"math/big"
	"testing"

	"daof-cpa/database"
)

func TestResolveBillingRulesDefaults(t *testing.T) {
	old := replaceSysConfigForTest(map[string]string{})
	defer replaceSysConfigForTest(old)

	opus := ResolveBillingRules("claude-opus-4-7", nil, 0, ChannelTypeAnthropic, false).WithCosts(100)
	if opus.ModelWeight != 3.5 {
		t.Fatalf("opus weight = %v, want 3.5", opus.ModelWeight)
	}
	if opus.ChargedCostMicroUSD != 350 {
		t.Fatalf("opus charged = %d, want 350", opus.ChargedCostMicroUSD)
	}
	if opus.RequestedModel != opus.ServedModel {
		t.Fatalf("default path must not change model: %+v", opus)
	}

	// Thinking 倍率严格判定：必须同时有 reasoning_tokens > 0 + 请求里启用 thinking。
	// 仅有请求字段（precheck 状态）不应触发 ×5。
	thinkingPrecheck := ResolveBillingRules("claude-opus-4-7", []byte(`{"thinking":{"type":"enabled","budget_tokens":1024}}`), 0, ChannelTypeAnthropic, true).WithCosts(100)
	if thinkingPrecheck.ModelWeight != 3.5 {
		t.Fatalf("precheck without reasoning_tokens must NOT trigger thinking weight; got %v, want 3.5", thinkingPrecheck.ModelWeight)
	}

	// commit 时上游真的返回了 reasoning_tokens > 0 + 请求显式启用 thinking → ×5
	thinkingCommit := ResolveBillingRules("claude-opus-4-7", []byte(`{"thinking":{"type":"enabled","budget_tokens":1024}}`), 800, ChannelTypeAnthropic, true).WithCosts(100)
	if thinkingCommit.ModelWeight != 5 {
		t.Fatalf("commit with reasoning_tokens + explicit thinking must trigger ×5; got %v, want 5", thinkingCommit.ModelWeight)
	}
	if !thinkingCommit.FallbackUserOptIn {
		t.Fatalf("fallback opt-in should be recorded")
	}
}

func TestResolveBillingRulesThinkingDetection(t *testing.T) {
	old := replaceSysConfigForTest(map[string]string{})
	defer replaceSysConfigForTest(old)

	cases := []struct {
		name      string
		body      string
		reasoning int
		want      float64
	}{
		// === both conditions hold → thinking weight ===
		{name: "anthropic enabled + reasoning tokens", body: `{"thinking":{"type":"enabled","budget_tokens":1024}}`, reasoning: 500, want: 5},
		{name: "budget enables + reasoning tokens", body: `{"thinking":{"budget_tokens":1024}}`, reasoning: 500, want: 5},
		{name: "openai effort medium + reasoning tokens", body: `{"reasoning":{"effort":"medium"}}`, reasoning: 100, want: 5},
		{name: "top-level reasoning effort low + reasoning tokens", body: `{"reasoning_effort":"low"}`, reasoning: 100, want: 5},

		// === only one condition holds → fall back to base weight ===
		{name: "request has thinking but reasoning=0 (precheck)", body: `{"thinking":{"type":"enabled","budget_tokens":1024}}`, reasoning: 0, want: 3.5},
		{name: "openai effort high but no reasoning tokens", body: `{"reasoning":{"effort":"high"}}`, reasoning: 0, want: 3.5},
		{name: "reasoning tokens but request has no thinking field", body: `{}`, reasoning: 1, want: 3.5},
		{name: "reasoning tokens but thinking explicitly disabled", body: `{"thinking":{"type":"disabled"}}`, reasoning: 1, want: 3.5},

		// === neither holds → base weight ===
		{name: "no thinking field, no reasoning tokens", body: `{}`, want: 3.5},
		{name: "anthropic thinking disabled", body: `{"thinking":{"type":"disabled"}}`, want: 3.5},
		{name: "empty thinking object", body: `{"thinking":{}}`, want: 3.5},
		{name: "zero budget", body: `{"thinking":{"budget_tokens":0}}`, want: 3.5},
		{name: "openai reasoning effort none", body: `{"reasoning":{"effort":"none"}}`, want: 3.5},
		{name: "top-level reasoning effort none", body: `{"reasoning_effort":"none"}`, want: 3.5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveBillingRules("claude-opus-4-7", []byte(tc.body), tc.reasoning, ChannelTypeAnthropic, false).ModelWeight
			if got != tc.want {
				t.Fatalf("model weight = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestResolveBillingRulesFromConfig(t *testing.T) {
	old := replaceSysConfigForTest(map[string]string{
		BillingModelWeightsConfigKey:        `[{"pattern":"special-*","weight":2.25}]`,
		BillingHealthMultipliersConfigKey:   `[{"pattern":"special-*","weight":1.2}]`,
		BillingProviderCostFactorsConfigKey: `{"openai":0.4}`,
		BillingRulesVersionConfigKey:        "test-v1",
	})
	defer replaceSysConfigForTest(old)

	r := ResolveBillingRules("special-model", nil, 0, ChannelTypeOpenAI, false).WithCosts(100)
	if r.BillingRulesVersion != "test-v1" {
		t.Fatalf("version = %q", r.BillingRulesVersion)
	}
	if r.ChargedCostMicroUSD != 270 {
		t.Fatalf("charged = %d, want 270", r.ChargedCostMicroUSD)
	}
	if r.PlatformCostEstimateMicro != 40 {
		t.Fatalf("platform estimate = %d, want 40", r.PlatformCostEstimateMicro)
	}
}

// TestMultiplierFixedPoint 验证 applyBillingMultiplier 使用 ceil-div（Sprint4-M2 fix）。
// 余数 > 0 时向上进位，保证正数成本不被截断到 0；与 checkedCostMicroUSD 同款 ceil 语义。
func TestMultiplierFixedPoint(t *testing.T) {
	cases := []struct {
		cost       int64
		multiplier float64
	}{
		{cost: 101, multiplier: 0.5},
		{cost: 101, multiplier: 0.333},
		{cost: 101, multiplier: 3.14},
		{cost: 999_999_937, multiplier: 3.14},
	}
	for _, tc := range cases {
		ppm, ok := multiplierPPMFromFloat(tc.multiplier)
		if !ok {
			t.Fatalf("multiplierPPMFromFloat(%v) failed", tc.multiplier)
		}
		// 期望 ceil-div: ⌈cost × ppm / base⌉
		product := new(big.Int).Mul(big.NewInt(tc.cost), big.NewInt(ppm))
		divisor := big.NewInt(database.MultiplierPPMBase)
		adjusted := new(big.Int).Add(product, new(big.Int).Sub(divisor, big.NewInt(1)))
		expected := new(big.Int).Quo(adjusted, divisor)
		if !expected.IsInt64() {
			t.Fatalf("test expected overflowed: %s", expected.String())
		}
		if got := applyBillingMultiplier(tc.cost, tc.multiplier); got != expected.Int64() {
			t.Fatalf("cost=%d multiplier=%v got=%d want=%d", tc.cost, tc.multiplier, got, expected.Int64())
		}
	}
}

// TestApplyBillingMultiplier_CeilPreventsSubMicroLoss 验证 ceil-div 防 sub-1-micro 免费消耗：
// cost × multiplier 落在 (0, 1) micro 范围 → 必须进位到 1，旧 floor 会截断到 0（免费）。
func TestApplyBillingMultiplier_CeilPreventsSubMicroLoss(t *testing.T) {
	// 2 micro × 0.3 = 0.6 micro → ceil = 1（旧 floor = 0 即免费消耗）
	if got := applyBillingMultiplier(2, 0.3); got != 1 {
		t.Errorf("cost=2 mult=0.3 expect ceil to 1 micro, got %d (was 0 before Sprint4-M2 fix)", got)
	}
	// 1 micro × 0.5 = 0.5 micro → ceil = 1
	if got := applyBillingMultiplier(1, 0.5); got != 1 {
		t.Errorf("cost=1 mult=0.5 expect ceil to 1, got %d", got)
	}
	// 边界：1 micro × 1.0 = 1 micro，整除不应误进位
	if got := applyBillingMultiplier(1, 1.0); got != 1 {
		t.Errorf("cost=1 mult=1.0 expect exact 1, got %d", got)
	}
	// 0 成本仍为 0
	if got := applyBillingMultiplier(0, 0.5); got != 0 {
		t.Errorf("cost=0 expect 0, got %d", got)
	}
	// 负成本返回 0
	if got := applyBillingMultiplier(-5, 0.5); got != 0 {
		t.Errorf("cost=-5 expect 0, got %d", got)
	}
}

func replaceSysConfigForTest(next map[string]string) map[string]string {
	SysConfigMutex.Lock()
	defer SysConfigMutex.Unlock()
	old := SysConfigCache
	SysConfigCache = next
	return old
}
