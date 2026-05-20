// Package controller / subscription_view_test.go
//
// 单元测试覆盖 subscription_view.go 的纯函数（无 DB 依赖）：
//   - scaleMicroByFloatForDisplay
//   - usageBucketFromPlanSnapshot
//   - subscriptionUsageWindowExpiredForDisplay
//   - subscriptionUsageValueForDisplay
//   - buildSubscriptionUsageSummary
//
// Phase E-2（2026-05-19）：D-5 拆分后这些纯函数失去 integration test 间接覆盖之外的
// 直接保护，本文件补齐 table-driven 单测。
package controller

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"daof-cpa/database"
)

func TestScaleMicroByFloatForDisplay(t *testing.T) {
	tests := []struct {
		name       string
		value      int64
		multiplier float64
		want       int64
	}{
		{"value zero", 0, 1.5, 0},
		{"value negative", -100, 1.5, 0},
		{"multiplier zero falls back to 1x", 1_000_000, 0, 1_000_000},
		{"multiplier NaN falls back to 1x", 1_000_000, math.NaN(), 1_000_000},
		{"multiplier +Inf falls back to 1x", 1_000_000, math.Inf(1), 1_000_000},
		{"multiplier negative falls back to 1x", 1_000_000, -2, 1_000_000},
		{"normal 2x", 1_000_000, 2.0, 2_000_000},
		{"normal 0.5x", 1_000_000, 0.5, 500_000},
		{"rounding nearest", 999, 1.0, 999},
		{"overflow guard", math.MaxInt64 / 2, 3, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := scaleMicroByFloatForDisplay(tc.value, tc.multiplier)
			if got != tc.want {
				t.Errorf("scaleMicroByFloatForDisplay(%d, %g) = %d; want %d", tc.value, tc.multiplier, got, tc.want)
			}
		})
	}
}

func TestUsageBucketFromPlanSnapshot(t *testing.T) {
	tests := []struct {
		name        string
		extraConfig string
		modelMatch  string
		want        string
	}{
		{"empty configs default", "", `[]`, "*"},
		{"extra_config empty object default", "{}", `[]`, "*"},
		{"extra_config bucket key", `{"bucket":"premium"}`, `[]`, "premium"},
		{"extra_config model_bucket fallback", `{"model_bucket":"basic"}`, `[]`, "basic"},
		{"extra_config bucket overrides model_bucket", `{"bucket":"alpha","model_bucket":"beta"}`, `[]`, "alpha"},
		{"extra_config whitespace value trimmed", `{"bucket":"  trim  "}`, `[]`, "trim"},
		{"extra_config invalid JSON falls to modelMatch", `not json`, `["gpt-4"]`, "gpt-4"},
		{"modelMatch first pattern", "", `["gpt-4","claude-3"]`, "gpt-4"},
		{"modelMatch invalid JSON returns wildcard", "", `not json`, "*"},
		{"modelMatch empty array returns wildcard", "", `[]`, "*"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := usageBucketFromPlanSnapshot(tc.extraConfig, tc.modelMatch)
			if got != tc.want {
				t.Errorf("usageBucketFromPlanSnapshot(%q, %q) = %q; want %q",
					tc.extraConfig, tc.modelMatch, got, tc.want)
			}
		})
	}
}

func TestSubscriptionUsageWindowExpiredForDisplay(t *testing.T) {
	now := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name          string
		windowEndAt   time.Time
		windowSeconds int
		want          bool
	}{
		{"window seconds 0 never expires", now.Add(-time.Hour), 0, false},
		{"window end zero never expires", time.Time{}, 3600, false},
		{"window in past expired", now.Add(-time.Hour), 3600, true},
		{"window in future not expired", now.Add(time.Hour), 3600, false},
		{"window exactly now not expired", now, 3600, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			u := database.SubscriptionUsage{WindowEndAt: tc.windowEndAt}
			got := subscriptionUsageWindowExpiredForDisplay(u, tc.windowSeconds, now)
			if got != tc.want {
				t.Errorf("expired = %v; want %v", got, tc.want)
			}
		})
	}
}

func TestSubscriptionUsageValueForDisplay(t *testing.T) {
	now := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)

	t.Run("expired window returns zero inactive", func(t *testing.T) {
		u := database.SubscriptionUsage{
			ConsumedValue:         42,
			ConsumedValueMicroUSD: 1_000_000,
			RequestCount:          5,
			WindowEndAt:           now.Add(-time.Hour),
		}
		consumed, count, active := subscriptionUsageValueForDisplay(u, "api_calls", 3600, now)
		if active || consumed != 0 || count != 0 {
			t.Errorf("expired window should return (0,0,false); got (%g,%d,%v)", consumed, count, active)
		}
	})

	t.Run("api_cost_usd returns micro converted to USD", func(t *testing.T) {
		u := database.SubscriptionUsage{
			ConsumedValueMicroUSD: 1_500_000, // $1.5
			RequestCount:          3,
			WindowEndAt:           now.Add(time.Hour),
		}
		consumed, count, active := subscriptionUsageValueForDisplay(u, "api_cost_usd", 3600, now)
		if !active {
			t.Fatal("active should be true")
		}
		if consumed != 1.5 {
			t.Errorf("consumed = %g; want 1.5", consumed)
		}
		if count != 3 {
			t.Errorf("count = %d; want 3", count)
		}
	})

	t.Run("api_calls returns ConsumedValue directly", func(t *testing.T) {
		u := database.SubscriptionUsage{
			ConsumedValue: 12345,
			RequestCount:  7,
			WindowEndAt:   now.Add(time.Hour),
		}
		consumed, count, active := subscriptionUsageValueForDisplay(u, "api_calls", 3600, now)
		if !active || consumed != 12345 || count != 7 {
			t.Errorf("got (%g,%d,%v); want (12345,7,true)", consumed, count, active)
		}
	})
}

func TestBuildSubscriptionUsageSummary(t *testing.T) {
	now := time.Now()
	windowEnd := now.Add(24 * time.Hour)

	t.Run("empty snapshot returns empty", func(t *testing.T) {
		got := buildSubscriptionUsageSummary("", nil)
		if len(got) != 0 {
			t.Errorf("empty snapshot should yield empty result, got %d items", len(got))
		}
	})

	t.Run("malformed snapshot returns empty", func(t *testing.T) {
		got := buildSubscriptionUsageSummary("not json", nil)
		if len(got) != 0 {
			t.Errorf("malformed snapshot should yield empty result, got %d items", len(got))
		}
	})

	t.Run("api_cost_usd plan computes pct correctly", func(t *testing.T) {
		snap := map[string]any{
			"plans": []map[string]any{
				{
					"id":                    uint(101),
					"name":                  "Premium",
					"model_match":           `["gpt-4"]`,
					"limit_unit":            "api_cost_usd",
					"limit_value":           10.0,
					"limit_value_micro_usd": int64(10_000_000), // $10
					"window_seconds":        3600,
					"quantity_multiplier":   1.0,
				},
			},
		}
		snapJSON, _ := json.Marshal(snap)
		usages := []database.SubscriptionUsage{
			{
				QuotaPlanID:           101,
				ModelBucket:           "gpt-4",
				ConsumedValueMicroUSD: 5_000_000, // $5 used
				RequestCount:          10,
				WindowStartAt:         now,
				WindowEndAt:           windowEnd,
			},
		}
		got := buildSubscriptionUsageSummary(string(snapJSON), usages)
		if len(got) != 1 {
			t.Fatalf("want 1 item, got %d", len(got))
		}
		item := got[0]
		if item.PlanID != 101 || item.Unit != "api_cost_usd" {
			t.Errorf("plan_id=%d unit=%s want 101/api_cost_usd", item.PlanID, item.Unit)
		}
		if item.Limit != 10 {
			t.Errorf("limit=%g want 10", item.Limit)
		}
		if item.Consumed != 5 {
			t.Errorf("consumed=%g want 5", item.Consumed)
		}
		if item.UsagePct != 50 {
			t.Errorf("usage_pct=%g want 50", item.UsagePct)
		}
		if item.Remaining != 5 {
			t.Errorf("remaining=%g want 5", item.Remaining)
		}
		if item.RequestCount != 10 {
			t.Errorf("request_count=%d want 10", item.RequestCount)
		}
		if item.IsUnlimited {
			t.Error("should not be unlimited")
		}
	})

	t.Run("zero limit means unlimited", func(t *testing.T) {
		snap := map[string]any{
			"plans": []map[string]any{
				{
					"id":                  uint(202),
					"name":                "Free Tier",
					"model_match":         `["*"]`,
					"limit_unit":          "api_calls",
					"limit_value":         0.0,
					"window_seconds":      0,
					"quantity_multiplier": 1.0,
				},
			},
		}
		snapJSON, _ := json.Marshal(snap)
		got := buildSubscriptionUsageSummary(string(snapJSON), nil)
		if len(got) != 1 {
			t.Fatalf("want 1 item, got %d", len(got))
		}
		if !got[0].IsUnlimited {
			t.Error("limit_value=0 should produce IsUnlimited=true")
		}
	})

	t.Run("multiplier scales limit", func(t *testing.T) {
		snap := map[string]any{
			"plans": []map[string]any{
				{
					"id":                  uint(303),
					"name":                "Triple Pack",
					"model_match":         `["gpt-4"]`,
					"limit_unit":          "api_calls",
					"limit_value":         100.0,
					"window_seconds":      3600,
					"quantity_multiplier": 3.0,
				},
			},
		}
		snapJSON, _ := json.Marshal(snap)
		got := buildSubscriptionUsageSummary(string(snapJSON), nil)
		if len(got) != 1 {
			t.Fatalf("want 1 item, got %d", len(got))
		}
		if got[0].Limit != 300 {
			t.Errorf("limit=%g want 300 (100×3)", got[0].Limit)
		}
	})

	t.Run("plan miss falls back to any usage with same plan_id", func(t *testing.T) {
		// fix LOW Phase E-8: plan→usage 二级 map 用于 bucket 不匹配时的 fallback
		snap := map[string]any{
			"plans": []map[string]any{
				{
					"id":                  uint(404),
					"name":                "Gemini Plan",
					"model_match":         `["gpt-4"]`, // snapshot says gpt-4
					"limit_unit":          "api_calls",
					"limit_value":         100.0,
					"window_seconds":      3600,
					"quantity_multiplier": 1.0,
				},
			},
		}
		snapJSON, _ := json.Marshal(snap)
		usages := []database.SubscriptionUsage{
			{
				QuotaPlanID:    404,
				ModelBucket:    "claude-3", // mismatch bucket
				ConsumedValue:  30,
				RequestCount:   5,
				WindowStartAt:  now,
				WindowEndAt:    windowEnd,
				SubscriptionID: 1,
			},
		}
		got := buildSubscriptionUsageSummary(string(snapJSON), usages)
		if len(got) != 1 {
			t.Fatalf("want 1 item, got %d", len(got))
		}
		if got[0].Consumed != 30 {
			t.Errorf("fallback consumed=%g want 30", got[0].Consumed)
		}
		if got[0].ModelBucket != "claude-3" {
			t.Errorf("fallback bucket=%q want claude-3", got[0].ModelBucket)
		}
	})

	t.Run("consumed clamped at limit for pct", func(t *testing.T) {
		snap := map[string]any{
			"plans": []map[string]any{
				{
					"id":                  uint(505),
					"name":                "Overused",
					"model_match":         `["gpt-4"]`,
					"limit_unit":          "api_calls",
					"limit_value":         10.0,
					"window_seconds":      3600,
					"quantity_multiplier": 1.0,
				},
			},
		}
		snapJSON, _ := json.Marshal(snap)
		usages := []database.SubscriptionUsage{
			{
				QuotaPlanID:    505,
				ModelBucket:    "gpt-4",
				ConsumedValue:  50, // way over
				RequestCount:   100,
				WindowStartAt:  now,
				WindowEndAt:    windowEnd,
				SubscriptionID: 1,
			},
		}
		got := buildSubscriptionUsageSummary(string(snapJSON), usages)
		if len(got) != 1 {
			t.Fatalf("want 1 item, got %d", len(got))
		}
		if got[0].UsagePct != 100 {
			t.Errorf("usage_pct=%g want clamped 100", got[0].UsagePct)
		}
		if got[0].Remaining != 0 {
			t.Errorf("remaining=%g want clamped 0", got[0].Remaining)
		}
	})

	t.Run("expired window shows zero consumed", func(t *testing.T) {
		snap := map[string]any{
			"plans": []map[string]any{
				{
					"id":                  uint(606),
					"name":                "Window Plan",
					"model_match":         `["gpt-4"]`,
					"limit_unit":          "api_calls",
					"limit_value":         100.0,
					"window_seconds":      3600,
					"quantity_multiplier": 1.0,
				},
			},
		}
		snapJSON, _ := json.Marshal(snap)
		usages := []database.SubscriptionUsage{
			{
				QuotaPlanID:    606,
				ModelBucket:    "gpt-4",
				ConsumedValue:  77,
				RequestCount:   3,
				WindowStartAt:  now.Add(-2 * time.Hour),
				WindowEndAt:    now.Add(-time.Hour), // expired
				SubscriptionID: 1,
			},
		}
		got := buildSubscriptionUsageSummary(string(snapJSON), usages)
		if len(got) != 1 {
			t.Fatalf("want 1 item, got %d", len(got))
		}
		// expired window → display layer treats as new window (consumed=0)
		if got[0].Consumed != 0 {
			t.Errorf("expired window consumed=%g want 0", got[0].Consumed)
		}
	})
}
