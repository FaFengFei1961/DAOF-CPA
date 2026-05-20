// Package controller / billing_rules_test.go
//
// 单元测试覆盖 billing_rules.go 的纯函数：
//   - parseBillingRuleEffectiveAt（publish_mode 解析）
//   - billingRuleRevisionSource / billingRuleOperationType（标记派生）
//   - validateBillingRuleSet（pattern/weight 校验）
//   - parseBillingRulePayloads（JSON 反序列化）
//   - billingRuleRevisionIsFuture / billingRuleRevisionPublishedAt / billingRuleRevisionEffectiveAt
//   - billingRuleRevisionToResponse（状态字段计算）
//
// Phase F（2026-05-19）：billing_rules pure helper 全部 0% → 100% 覆盖。
package controller

import (
	"testing"
	"time"

	"daof-cpa/database"
)

func TestParseBillingRuleEffectiveAt(t *testing.T) {
	now := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name         string
		mode         string
		raw          string
		wantTime     time.Time
		wantActivate bool
		wantCode     string
	}{
		{"immediate ignores raw", "immediate", "ignored", now, true, ""},
		{"scheduled empty raw rejected", "scheduled", "", time.Time{}, false, "ERR_BILLING_RULES_EFFECTIVE_AT_REQUIRED"},
		{"scheduled empty whitespace rejected", "scheduled", "   ", time.Time{}, false, "ERR_BILLING_RULES_EFFECTIVE_AT_REQUIRED"},
		{"scheduled bad format rejected", "scheduled", "2026/05/20", time.Time{}, false, "ERR_BILLING_RULES_EFFECTIVE_AT_INVALID"},
		{"scheduled past time rejected", "scheduled", "2026-05-19T11:00:00Z", time.Time{}, false, "ERR_BILLING_RULES_EFFECTIVE_AT_TOO_SOON"},
		{"scheduled too-soon (< 30s) rejected", "scheduled", "2026-05-19T12:00:10Z", time.Time{}, false, "ERR_BILLING_RULES_EFFECTIVE_AT_TOO_SOON"},
		{"scheduled valid future", "scheduled", "2026-05-19T13:00:00Z", time.Date(2026, 5, 19, 13, 0, 0, 0, time.UTC), false, ""},
		{"unknown mode rejected", "draft", "", time.Time{}, false, "ERR_BILLING_RULES_PUBLISH_MODE_INVALID"},
		{"empty mode rejected", "", "", time.Time{}, false, "ERR_BILLING_RULES_PUBLISH_MODE_INVALID"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotTime, gotActivate, gotCode, _ := parseBillingRuleEffectiveAt(tc.mode, tc.raw, now)
			if gotCode != tc.wantCode {
				t.Errorf("code = %q; want %q", gotCode, tc.wantCode)
			}
			if gotActivate != tc.wantActivate {
				t.Errorf("activate = %v; want %v", gotActivate, tc.wantActivate)
			}
			if !gotTime.Equal(tc.wantTime) {
				t.Errorf("time = %v; want %v", gotTime, tc.wantTime)
			}
		})
	}
}

func TestBillingRuleRevisionSource(t *testing.T) {
	if got := billingRuleRevisionSource(true); got != "admin" {
		t.Errorf("activate=true → %q want 'admin'", got)
	}
	if got := billingRuleRevisionSource(false); got != "admin_scheduled" {
		t.Errorf("activate=false → %q want 'admin_scheduled'", got)
	}
}

func TestBillingRuleOperationType(t *testing.T) {
	if got := billingRuleOperationType(true); got != "BILLING_RULES_UPDATE" {
		t.Errorf("activate=true → %q want 'BILLING_RULES_UPDATE'", got)
	}
	if got := billingRuleOperationType(false); got != "BILLING_RULES_SCHEDULE" {
		t.Errorf("activate=false → %q want 'BILLING_RULES_SCHEDULE'", got)
	}
}

func TestValidateBillingRuleSet(t *testing.T) {
	tests := []struct {
		name      string
		in        []billingRulePayload
		thinking  bool
		wantCode  string
		wantCount int
	}{
		{
			name:      "empty input ok",
			in:        nil,
			wantCount: 0,
		},
		{
			name: "valid set passes",
			in: []billingRulePayload{
				{Pattern: "gpt-4*", Weight: 1.5, Label: "GPT-4 family"},
				{Pattern: "claude-3*", Weight: 1.2, Reason: "premium"},
			},
			wantCount: 2,
		},
		{
			name: "empty pattern rejected",
			in: []billingRulePayload{
				{Pattern: "", Weight: 1},
			},
			wantCode: "ERR_BILLING_RULES_PATTERN_EMPTY",
		},
		{
			name: "whitespace-only pattern rejected",
			in: []billingRulePayload{
				{Pattern: "   ", Weight: 1},
			},
			wantCode: "ERR_BILLING_RULES_PATTERN_EMPTY",
		},
		{
			name: "pattern too long (>80) rejected",
			in: []billingRulePayload{
				{Pattern: "x" + string(make([]byte, 80)), Weight: 1}, // 81 chars
			},
			wantCode: "ERR_BILLING_RULES_PATTERN_LONG",
		},
		{
			name: "duplicate pattern (case-insensitive) rejected",
			in: []billingRulePayload{
				{Pattern: "gpt-4", Weight: 1},
				{Pattern: "GPT-4", Weight: 2},
			},
			wantCode: "ERR_BILLING_RULES_PATTERN_DUP",
		},
		{
			name: "weight zero rejected",
			in: []billingRulePayload{
				{Pattern: "gpt-4", Weight: 0},
			},
			wantCode: "ERR_BILLING_RULES_WEIGHT_RANGE",
		},
		{
			name: "weight negative rejected",
			in: []billingRulePayload{
				{Pattern: "gpt-4", Weight: -1},
			},
			wantCode: "ERR_BILLING_RULES_WEIGHT_RANGE",
		},
		{
			name: "weight > 1000 rejected",
			in: []billingRulePayload{
				{Pattern: "gpt-4", Weight: 1000.1},
			},
			wantCode: "ERR_BILLING_RULES_WEIGHT_RANGE",
		},
		{
			name: "thinking_weight allowed when flag on",
			in: []billingRulePayload{
				{Pattern: "claude-3", Weight: 1, ThinkingWeight: 2.5},
			},
			thinking:  true,
			wantCount: 1,
		},
		{
			name: "thinking_weight ignored when flag off",
			in: []billingRulePayload{
				{Pattern: "claude-3", Weight: 1, ThinkingWeight: 2.5},
			},
			thinking:  false,
			wantCount: 1, // not rejected, just ignored
		},
		{
			name: "thinking_weight out of range rejected",
			in: []billingRulePayload{
				{Pattern: "claude-3", Weight: 1, ThinkingWeight: 1000.1},
			},
			thinking: true,
			wantCode: "ERR_BILLING_RULES_THINKING_RANGE",
		},
		{
			name: "label/reason trimmed",
			in: []billingRulePayload{
				{Pattern: "gpt-4", Weight: 1, Label: "  GPT-4  ", Reason: " test "},
			},
			wantCount: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, code, _ := validateBillingRuleSet(tc.in, tc.thinking)
			if code != tc.wantCode {
				t.Errorf("code = %q; want %q", code, tc.wantCode)
			}
			if tc.wantCode == "" && len(out) != tc.wantCount {
				t.Errorf("count = %d; want %d", len(out), tc.wantCount)
			}
		})
	}
}

func TestValidateBillingRuleSet_TrimsLabel(t *testing.T) {
	in := []billingRulePayload{{Pattern: " gpt-4 ", Weight: 1, Label: "  L  ", Reason: " R "}}
	out, code, _ := validateBillingRuleSet(in, false)
	if code != "" {
		t.Fatalf("code = %q", code)
	}
	if out[0].Pattern != "gpt-4" {
		t.Errorf("pattern = %q want 'gpt-4'", out[0].Pattern)
	}
	if out[0].Label != "L" {
		t.Errorf("label = %q want 'L'", out[0].Label)
	}
	if out[0].Reason != "R" {
		t.Errorf("reason = %q want 'R'", out[0].Reason)
	}
}

func TestParseBillingRulePayloads(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantCount int
	}{
		{"empty string", "", 0},
		{"malformed", "not json", 0},
		{"empty array", "[]", 0},
		{
			"single entry",
			`[{"pattern":"gpt-4","weight":1.5}]`,
			1,
		},
		{
			"multiple entries",
			`[{"pattern":"gpt-4","weight":1.5},{"pattern":"claude-3","weight":1.2,"label":"x"}]`,
			2,
		},
		{"non-array object returns empty", `{"pattern":"x"}`, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseBillingRulePayloads(tc.raw)
			if len(got) != tc.wantCount {
				t.Errorf("count = %d; want %d (got %#v)", len(got), tc.wantCount, got)
			}
		})
	}
}

func TestBillingRuleRevisionIsFuture(t *testing.T) {
	now := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	past := now.Add(-time.Hour)

	tests := []struct {
		name string
		row  database.BillingRuleRevision
		want bool
	}{
		{"nil EffectiveAt not future", database.BillingRuleRevision{EffectiveAt: nil}, false},
		{"past EffectiveAt not future", database.BillingRuleRevision{EffectiveAt: &past}, false},
		{"now EffectiveAt not future", database.BillingRuleRevision{EffectiveAt: &now}, false},
		{"future EffectiveAt is future", database.BillingRuleRevision{EffectiveAt: &future}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := billingRuleRevisionIsFuture(tc.row, now); got != tc.want {
				t.Errorf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestBillingRuleRevisionPublishedAt(t *testing.T) {
	now := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	explicit := now.Add(time.Hour)
	created := now.Add(-time.Hour)

	t.Run("nil PublishedAt nil CreatedAt returns nil", func(t *testing.T) {
		row := database.BillingRuleRevision{}
		if got := billingRuleRevisionPublishedAt(row); got != nil {
			t.Errorf("got %v want nil", got)
		}
	})

	t.Run("explicit PublishedAt wins", func(t *testing.T) {
		row := database.BillingRuleRevision{PublishedAt: &explicit}
		got := billingRuleRevisionPublishedAt(row)
		if got == nil || !got.Equal(explicit) {
			t.Errorf("got %v want %v", got, explicit)
		}
	})

	t.Run("nil PublishedAt falls back to CreatedAt", func(t *testing.T) {
		row := database.BillingRuleRevision{}
		row.CreatedAt = created
		got := billingRuleRevisionPublishedAt(row)
		if got == nil || !got.Equal(created) {
			t.Errorf("got %v want %v", got, created)
		}
	})
}

func TestBillingRuleRevisionEffectiveAt(t *testing.T) {
	now := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	explicit := now.Add(time.Hour)
	created := now.Add(-time.Hour)

	t.Run("nil EffectiveAt nil CreatedAt returns nil", func(t *testing.T) {
		row := database.BillingRuleRevision{}
		if got := billingRuleRevisionEffectiveAt(row); got != nil {
			t.Errorf("got %v want nil", got)
		}
	})

	t.Run("explicit EffectiveAt wins", func(t *testing.T) {
		row := database.BillingRuleRevision{EffectiveAt: &explicit}
		got := billingRuleRevisionEffectiveAt(row)
		if got == nil || !got.Equal(explicit) {
			t.Errorf("got %v want %v", got, explicit)
		}
	})

	t.Run("nil EffectiveAt falls back to CreatedAt", func(t *testing.T) {
		row := database.BillingRuleRevision{}
		row.CreatedAt = created
		got := billingRuleRevisionEffectiveAt(row)
		if got == nil || !got.Equal(created) {
			t.Errorf("got %v want %v", got, created)
		}
	})
}

func TestBillingRuleRevisionToResponse(t *testing.T) {
	now := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	past := now.Add(-time.Hour)
	canceledTime := now.Add(-30 * time.Minute)

	t.Run("active by id match", func(t *testing.T) {
		row := database.BillingRuleRevision{Version: "v1.0", EffectiveAt: &past}
		row.ID = 100
		row.CreatedAt = past
		resp := billingRuleRevisionToResponse(row, nil, 100, "", now)
		if resp.Status != "active" {
			t.Errorf("status = %q want 'active'", resp.Status)
		}
		if resp.ID != 100 || resp.Version != "v1.0" {
			t.Errorf("ID/Version mismatch: %d/%q", resp.ID, resp.Version)
		}
	})

	t.Run("active by version match (legacy fallback)", func(t *testing.T) {
		row := database.BillingRuleRevision{Version: "v1.0", EffectiveAt: &past}
		row.ID = 100
		row.CreatedAt = past
		resp := billingRuleRevisionToResponse(row, nil, 0, "v1.0", now)
		if resp.Status != "active" {
			t.Errorf("status = %q want 'active'", resp.Status)
		}
	})

	t.Run("canceled status when cancel exists", func(t *testing.T) {
		row := database.BillingRuleRevision{Version: "v2.0"}
		row.ID = 200
		cancel := &database.BillingRuleRevisionCancellation{RevisionID: 200}
		cancel.CreatedAt = canceledTime
		resp := billingRuleRevisionToResponse(row, cancel, 100, "", now)
		if resp.Status != "canceled" {
			t.Errorf("status = %q want 'canceled'", resp.Status)
		}
		if resp.CanceledAt == nil || !resp.CanceledAt.Equal(canceledTime) {
			t.Errorf("canceled_at = %v want %v", resp.CanceledAt, canceledTime)
		}
	})

	t.Run("scheduled when future and not active", func(t *testing.T) {
		row := database.BillingRuleRevision{Version: "v3.0", EffectiveAt: &future}
		row.ID = 300
		resp := billingRuleRevisionToResponse(row, nil, 100, "", now)
		if resp.Status != "scheduled" {
			t.Errorf("status = %q want 'scheduled'", resp.Status)
		}
	})

	t.Run("superseded by default", func(t *testing.T) {
		row := database.BillingRuleRevision{Version: "v0.5", EffectiveAt: &past}
		row.ID = 5
		resp := billingRuleRevisionToResponse(row, nil, 100, "v1.0", now)
		if resp.Status != "superseded" {
			t.Errorf("status = %q want 'superseded'", resp.Status)
		}
	})

	t.Run("payload fields populated", func(t *testing.T) {
		row := database.BillingRuleRevision{
			Version:               "v1.0",
			EffectiveSince:        now.Format("2006-01-02"),
			ModelWeightsJSON:      `[{"pattern":"gpt-4","weight":1.5}]`,
			HealthMultipliersJSON: `[{"pattern":"claude-3","weight":1.0}]`,
			ModelCount:            1,
			HealthCount:           1,
			Source:                "admin",
		}
		row.ID = 10
		row.CreatedAt = past
		resp := billingRuleRevisionToResponse(row, nil, 10, "v1.0", now)
		if len(resp.ModelWeights) != 1 || resp.ModelWeights[0].Pattern != "gpt-4" {
			t.Errorf("model_weights mismatch: %#v", resp.ModelWeights)
		}
		if len(resp.HealthMultipliers) != 1 || resp.HealthMultipliers[0].Pattern != "claude-3" {
			t.Errorf("health_multipliers mismatch: %#v", resp.HealthMultipliers)
		}
		if resp.ModelCount != 1 || resp.HealthCount != 1 || resp.Source != "admin" {
			t.Errorf("count/source mismatch: model=%d health=%d source=%q", resp.ModelCount, resp.HealthCount, resp.Source)
		}
	})
}
