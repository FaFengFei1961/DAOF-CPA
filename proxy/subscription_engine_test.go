package proxy

import (
	"encoding/json"
	"math"
	"testing"
)

// ─── matchModel ──────────────────────────────────────────────────

func TestMatchModel_EmptyMatchesAll(t *testing.T) {
	tests := []struct {
		name           string
		modelMatchJSON string
		model          string
		want           bool
	}{
		{"empty string matches all", "", "gpt-4o", true},
		{"empty array matches all", "[]", "claude-sonnet", true},
		// fix CRITICAL C-B4（codex 第二十一轮）：原行为是解析失败 fallback true（坏配置匹配所有模型，
		// 资金风险）。改为 fail-closed → 解析失败 return false。
		{"invalid json fails closed (no match)", "[broken", "gpt-4o", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchModel(tc.modelMatchJSON, tc.model); got != tc.want {
				t.Errorf("matchModel(%q, %q) = %v, want %v", tc.modelMatchJSON, tc.model, got, tc.want)
			}
		})
	}
}

func TestMatchModel_GlobPatterns(t *testing.T) {
	tests := []struct {
		name     string
		patterns []string
		model    string
		want     bool
	}{
		{"exact match", []string{"gpt-4o"}, "gpt-4o", true},
		{"wildcard prefix", []string{"gpt-*"}, "gpt-4o", true},
		{"wildcard prefix no match", []string{"claude-*"}, "gpt-4o", false},
		{"multiple patterns OR", []string{"claude-*", "gpt-*"}, "gpt-4o", true},
		{"multiple patterns OR negative", []string{"claude-*", "gemini-*"}, "gpt-4o", false},
		{"specific overrides general", []string{"gpt-4o-mini"}, "gpt-4o", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			j, _ := json.Marshal(tc.patterns)
			if got := matchModel(string(j), tc.model); got != tc.want {
				t.Errorf("matchModel(%v, %q) = %v, want %v", tc.patterns, tc.model, got, tc.want)
			}
		})
	}
}

// ─── computeDelta ────────────────────────────────────────────────

func TestComputeDelta_Messages(t *testing.T) {
	plan := snapshotPlan{LimitUnit: "messages", QuantityMultiplier: 1.0}
	d, unit := computeDelta(plan, EngineRequest{InputTokens: 9999, OutputTokens: 9999})
	if d != 1.0 || unit != "messages" {
		t.Errorf("messages: got delta=%v unit=%q, want 1.0/messages", d, unit)
	}
}

func TestComputeDelta_InputTokens(t *testing.T) {
	plan := snapshotPlan{LimitUnit: "input_tokens", QuantityMultiplier: 1.0}
	d, unit := computeDelta(plan, EngineRequest{InputTokens: 100, OutputTokens: 200})
	if d != 100 || unit != "input_tokens" {
		t.Errorf("input_tokens: got %v/%q, want 100/input_tokens", d, unit)
	}
}

func TestComputeDelta_OutputTokens(t *testing.T) {
	plan := snapshotPlan{LimitUnit: "output_tokens", QuantityMultiplier: 1.0}
	d, _ := computeDelta(plan, EngineRequest{InputTokens: 100, OutputTokens: 200})
	if d != 200 {
		t.Errorf("output_tokens: got %v, want 200", d)
	}
}

func TestComputeDelta_TotalTokens(t *testing.T) {
	plan := snapshotPlan{LimitUnit: "total_tokens", QuantityMultiplier: 1.0}
	d, _ := computeDelta(plan, EngineRequest{InputTokens: 100, OutputTokens: 200})
	if d != 300 {
		t.Errorf("total_tokens: got %v, want 300", d)
	}
}

func TestComputeDelta_QuantityMultiplier_NoMultiplyOnDelta(t *testing.T) {
	// fix CRITICAL C-B3（codex 第二十一轮）：multiplier 现在作用于"限额"而非"消费 delta"。
	// 之前 multiplier=3 让消费翻 3 倍（业务反向），现在 computeDelta 不再乘 multiplier，
	// 调用方在 atomicConsume 用 effectiveLimit = LimitValue * multiplier。
	plan := snapshotPlan{LimitUnit: "total_tokens", QuantityMultiplier: 3.0}
	d, _ := computeDelta(plan, EngineRequest{InputTokens: 100, OutputTokens: 200})
	if d != 300 {
		t.Errorf("computeDelta 不应乘 multiplier: got %v, want 300", d)
	}
}

func TestComputeDelta_WeightedTokensWithInOut(t *testing.T) {
	plan := snapshotPlan{
		LimitUnit:          "weighted_tokens",
		QuantityMultiplier: 1.0,
		// claude-sonnet 输入权重 1.0, 输出权重 5.0
		WeightFactor: `{"claude-sonnet": {"input": 1.0, "output": 5.0}}`,
	}
	d, unit := computeDelta(plan, EngineRequest{
		ModelName:    "claude-sonnet",
		InputTokens:  1000,
		OutputTokens: 500,
	})
	want := 1000.0*1.0 + 500.0*5.0 // 3500
	if d != want || unit != "weighted_tokens" {
		t.Errorf("weighted_tokens: got %v/%q, want %v/weighted_tokens", d, unit, want)
	}
}

func TestComputeDelta_USDEquivalent(t *testing.T) {
	plan := snapshotPlan{
		LimitUnit:          "usd_equivalent",
		QuantityMultiplier: 1.0,
		// gpt-4o 输入 $2.5/M, 输出 $10/M
		WeightFactor: `{"gpt-4o": {"input": 2.5, "output": 10.0}}`,
	}
	d, _ := computeDelta(plan, EngineRequest{
		ModelName:    "gpt-4o",
		InputTokens:  1_000_000,
		OutputTokens: 100_000,
	})
	want := (1_000_000.0*2.5 + 100_000.0*10.0) / 1_000_000.0 // 3.5 USD
	if math.Abs(d-want) > 1e-9 {
		t.Errorf("usd_equivalent: got %v, want %v", d, want)
	}
}

func TestComputeDelta_USDEquivalentWithoutWeightReturnsMinusOne(t *testing.T) {
	// M-5 修复：usd_equivalent 没配权重 → -1，让上层跳过
	plan := snapshotPlan{
		LimitUnit:          "usd_equivalent",
		QuantityMultiplier: 1.0,
		WeightFactor:       "", // 缺权重配置
	}
	d, _ := computeDelta(plan, EngineRequest{InputTokens: 100, OutputTokens: 200})
	if d != -1 {
		t.Errorf("usd_equivalent without weight: got %v, want -1", d)
	}
}

// ─── parseWeightFactor ───────────────────────────────────────────

func TestParseWeightFactor_ScalarSingle(t *testing.T) {
	single, _ := parseWeightFactor(`{"gpt-4o": 2.5}`, "gpt-4o")
	if single != 2.5 {
		t.Errorf("scalar weight: got %v, want 2.5", single)
	}
}

func TestParseWeightFactor_GlobMatch(t *testing.T) {
	single, _ := parseWeightFactor(`{"claude-*": 3.0}`, "claude-sonnet")
	if single != 3.0 {
		t.Errorf("glob weight: got %v, want 3.0", single)
	}
}

func TestParseWeightFactor_NoMatchDefaultsToOne(t *testing.T) {
	single, _ := parseWeightFactor(`{"claude-*": 3.0}`, "gpt-4o")
	if single != 1.0 {
		t.Errorf("no match: got %v, want 1.0 default", single)
	}
}

func TestParseWeightFactor_InOutSplit(t *testing.T) {
	_, inout := parseWeightFactor(`{"gpt-4o": {"input": 2.5, "output": 10.0}}`, "gpt-4o")
	if !inout.HasInOut {
		t.Fatal("expected HasInOut=true")
	}
	if inout.Input != 2.5 || inout.Output != 10.0 {
		t.Errorf("got input=%v output=%v, want 2.5/10.0", inout.Input, inout.Output)
	}
}

func TestParseWeightFactor_InvalidJSON(t *testing.T) {
	single, inout := parseWeightFactor(`{not json`, "gpt-4o")
	if single != 1.0 || inout.HasInOut {
		t.Errorf("invalid JSON should default: got single=%v hasInOut=%v", single, inout.HasInOut)
	}
}

// ─── extractPlansFromSnapshot ────────────────────────────────────

func TestExtractPlansFromSnapshot_SortByPriority(t *testing.T) {
	snap := map[string]any{
		"plans": []any{
			map[string]any{"id": float64(1), "priority": float64(10)},
			map[string]any{"id": float64(2), "priority": float64(1)},
			map[string]any{"id": float64(3), "priority": float64(5)},
		},
	}
	plans := extractPlansFromSnapshot(snap)
	if len(plans) != 3 {
		t.Fatalf("got %d plans, want 3", len(plans))
	}
	if plans[0].ID != 2 || plans[1].ID != 3 || plans[2].ID != 1 {
		t.Errorf("priority sort wrong: ids = %d,%d,%d (want 2,3,1)", plans[0].ID, plans[1].ID, plans[2].ID)
	}
}

func TestExtractPlansFromSnapshot_QuantityMultiplierDefault(t *testing.T) {
	// QuantityMultiplier <= 0 应被默认成 1.0
	snap := map[string]any{
		"plans": []any{
			map[string]any{"id": float64(1), "quantity_multiplier": float64(0)},
			map[string]any{"id": float64(2)}, // 缺字段
		},
	}
	plans := extractPlansFromSnapshot(snap)
	for _, p := range plans {
		if p.QuantityMultiplier != 1.0 {
			t.Errorf("plan %d: multiplier=%v, want 1.0 default", p.ID, p.QuantityMultiplier)
		}
	}
}

func TestExtractPlansFromSnapshot_NilSafe(t *testing.T) {
	if plans := extractPlansFromSnapshot(nil); plans != nil {
		t.Errorf("nil snapshot should return nil, got %v", plans)
	}
	if plans := extractPlansFromSnapshot(map[string]any{}); plans != nil {
		t.Errorf("snapshot without 'plans' key should return nil, got %v", plans)
	}
}

// ─── normalizeModelBucket ────────────────────────────────────────

func TestNormalizeModelBucket(t *testing.T) {
	tests := []struct {
		name      string
		matchJSON string
		model     string
		want      string
	}{
		{"glob pattern bucket", `["claude-*"]`, "claude-sonnet-4", "claude-*"},
		{"exact pattern bucket", `["gpt-4o"]`, "gpt-4o", "gpt-4o"},
		{"no match → use model name", `["other-*"]`, "gpt-4o", "gpt-4o"},
		{"empty patterns → use model name", `[]`, "gpt-4o", "gpt-4o"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeModelBucket(snapshotPlan{ModelMatch: tc.matchJSON}, tc.model)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// ─── 工具函数 ────────────────────────────────────────────────────

func TestStringFromAny_NumericIsEmpty(t *testing.T) {
	// 防止数字被当成 glob pattern 错配
	if got := stringFromAny(float64(42)); got != "" {
		t.Errorf("numeric should not be stringified, got %q", got)
	}
	if got := stringFromAny(nil); got != "" {
		t.Errorf("nil should be empty, got %q", got)
	}
	if got := stringFromAny("real"); got != "real" {
		t.Errorf("string passes through")
	}
}

func TestUintFromAny(t *testing.T) {
	if uintFromAny(float64(42)) != 42 {
		t.Error("float64 → uint")
	}
	if uintFromAny("string") != 0 {
		t.Error("non-float → 0")
	}
}
