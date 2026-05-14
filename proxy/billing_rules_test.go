package proxy

import "testing"

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

func replaceSysConfigForTest(next map[string]string) map[string]string {
	SysConfigMutex.Lock()
	defer SysConfigMutex.Unlock()
	old := SysConfigCache
	SysConfigCache = next
	return old
}
