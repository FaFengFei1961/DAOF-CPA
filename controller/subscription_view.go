// Package controller / subscription_view.go
//
// 订阅的 *显示层* 聚合：把 PackageSnapshot 里的 plans + SubscriptionUsage
// 折算成前端用的 subscriptionUsageSummary（包含 limit/consumed/pct/window）。
//
// 从 subscription.go 抽出（Phase D-5，2026-05-19）：只是物理拆分，无语义改动。
//
// 这些 helper 只读不写，给 MySubscriptions handler 用；admin 路径直接读
// SubscriptionUsage 表，不走这里。
package controller

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"daof-cpa/database"
)

type subscriptionUsageSummary struct {
	PlanID        uint       `json:"plan_id"`
	PlanName      string     `json:"plan_name"`
	Unit          string     `json:"unit"`
	ModelBucket   string     `json:"model_bucket"`
	WindowSeconds int        `json:"window_seconds"`
	WindowStartAt *time.Time `json:"window_start_at,omitempty"`
	WindowEndAt   *time.Time `json:"window_end_at,omitempty"`
	Consumed      float64    `json:"consumed"`
	Limit         float64    `json:"limit"`
	Remaining     float64    `json:"remaining"`
	UsagePct      float64    `json:"usage_pct"`
	RequestCount  int64      `json:"request_count"`
	IsUnlimited   bool       `json:"is_unlimited"`
}

func scaleMicroByFloatForDisplay(value int64, multiplier float64) int64 {
	if value <= 0 {
		return 0
	}
	if multiplier <= 0 || math.IsNaN(multiplier) || math.IsInf(multiplier, 0) {
		multiplier = 1
	}
	scaled := math.Round(float64(value) * multiplier)
	if scaled <= 0 || scaled >= float64(math.MaxInt64) {
		return 0
	}
	return int64(scaled)
}

func buildSubscriptionUsageSummary(snapshotJSON string, usages []database.SubscriptionUsage) []subscriptionUsageSummary {
	type planSnap struct {
		ID                 uint    `json:"id"`
		Name               string  `json:"name"`
		ModelMatch         string  `json:"model_match"`
		LimitUnit          string  `json:"limit_unit"`
		LimitValue         float64 `json:"limit_value"`
		LimitValueMicroUSD int64   `json:"limit_value_micro_usd"`
		WindowSeconds      int     `json:"window_seconds"`
		ExtraConfig        string  `json:"extra_config"`
		QuantityMultiplier float64 `json:"quantity_multiplier"`
	}
	var snap struct {
		Plans []planSnap `json:"plans"`
	}
	if err := json.Unmarshal([]byte(snapshotJSON), &snap); err != nil || len(snap.Plans) == 0 {
		return []subscriptionUsageSummary{}
	}
	now := time.Now()
	usageByPlanBucket := make(map[string]database.SubscriptionUsage, len(usages))
	// fix LOW（Phase E 复审）：补 plan→usage 二级 map，消除 fallback 的 O(N×M) 退化
	// （主路径已是 O(1) by (plan_id, bucket)，仅 bucket 不匹配时退化）。
	usageByPlanID := make(map[uint]database.SubscriptionUsage, len(usages))
	for _, u := range usages {
		key := fmt.Sprintf("%d\x00%s", u.QuotaPlanID, u.ModelBucket)
		usageByPlanBucket[key] = u
		// 同 plan 多 bucket 时保留最后一条（与原线性扫描行为一致：break on first match）
		if _, exists := usageByPlanID[u.QuotaPlanID]; !exists {
			usageByPlanID[u.QuotaPlanID] = u
		}
	}
	out := make([]subscriptionUsageSummary, 0, len(snap.Plans))
	for _, p := range snap.Plans {
		bucket := usageBucketFromPlanSnapshot(p.ExtraConfig, p.ModelMatch)
		key := fmt.Sprintf("%d\x00%s", p.ID, bucket)
		u, hasUsage := usageByPlanBucket[key]
		if !hasUsage {
			if candidate, ok := usageByPlanID[p.ID]; ok {
				u = candidate
				bucket = candidate.ModelBucket
				hasUsage = true
			}
		}
		mult := p.QuantityMultiplier
		if mult <= 0 {
			mult = 1
		}
		limit := p.LimitValue * mult
		if p.LimitUnit == "api_cost_usd" {
			// 旧快照（fixed-point 改造前购买的订阅）的 LimitValueMicroUSD=0，
			// 此时 fallback 用 LimitValue × 1e6 精确转换（无 float 漂移：25.0 USD × 1e6 = 25_000_000 整数）
			limitMicro := p.LimitValueMicroUSD
			if limitMicro == 0 && p.LimitValue > 0 {
				if m, ok := database.USDToMicro(p.LimitValue); ok {
					limitMicro = m
				}
			}
			limit = database.MicroToUSD(scaleMicroByFloatForDisplay(limitMicro, mult))
		}
		consumed := 0.0
		requestCount := int64(0)
		var windowStart *time.Time
		var windowEnd *time.Time
		if hasUsage {
			if displayConsumed, displayCount, active := subscriptionUsageValueForDisplay(u, p.LimitUnit, p.WindowSeconds, now); active {
				consumed = displayConsumed
				requestCount = displayCount
				ws := u.WindowStartAt
				we := u.WindowEndAt
				windowStart = &ws
				windowEnd = &we
			}
		}
		remaining := 0.0
		pct := 0.0
		unlimited := limit <= 0
		if unlimited {
			remaining = 0
		} else {
			remaining = limit - consumed
			if remaining < 0 {
				remaining = 0
			}
			pct = consumed / limit * 100
			if pct > 100 {
				pct = 100
			}
		}
		out = append(out, subscriptionUsageSummary{
			PlanID:        p.ID,
			PlanName:      p.Name,
			Unit:          p.LimitUnit,
			ModelBucket:   bucket,
			WindowSeconds: p.WindowSeconds,
			WindowStartAt: windowStart,
			WindowEndAt:   windowEnd,
			Consumed:      consumed,
			Limit:         limit,
			Remaining:     remaining,
			UsagePct:      pct,
			RequestCount:  requestCount,
			IsUnlimited:   unlimited,
		})
	}
	return out
}

func subscriptionUsageWindowExpiredForDisplay(u database.SubscriptionUsage, windowSeconds int, now time.Time) bool {
	return windowSeconds > 0 && !u.WindowEndAt.IsZero() && now.After(u.WindowEndAt)
}

func subscriptionUsageValueForDisplay(u database.SubscriptionUsage, unit string, windowSeconds int, now time.Time) (float64, int64, bool) {
	if subscriptionUsageWindowExpiredForDisplay(u, windowSeconds, now) {
		return 0, 0, false
	}
	if unit == "api_cost_usd" {
		return database.MicroToUSD(u.ConsumedValueMicroUSD), u.RequestCount, true
	}
	return u.ConsumedValue, u.RequestCount, true
}

func usageBucketFromPlanSnapshot(extraConfig, modelMatch string) string {
	if extraConfig != "" && extraConfig != "{}" {
		var cfg map[string]any
		if err := json.Unmarshal([]byte(extraConfig), &cfg); err == nil {
			for _, key := range []string{"bucket", "model_bucket"} {
				if v, ok := cfg[key].(string); ok && strings.TrimSpace(v) != "" {
					return strings.TrimSpace(v)
				}
			}
		}
	}
	var patterns []string
	if err := json.Unmarshal([]byte(modelMatch), &patterns); err == nil && len(patterns) > 0 {
		return patterns[0]
	}
	return "*"
}
