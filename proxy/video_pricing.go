// Package proxy / video_pricing.go
//
// M-R6 重构（2026-05-19）：从 video_generation.go 1131 行抽出 pricing 相关
// helper，纯文件物理拆分。业务逻辑零改动。

package proxy

import (
	"fmt"
	"strings"

	"daof-cpa/database"

	"github.com/tidwall/gjson"
)

func filterVideoRoutes(routes []*database.ChannelModel, endpoint string) []*database.ChannelModel {
	out := make([]*database.ChannelModel, 0, len(routes))
	for _, r := range routes {
		if r == nil {
			continue
		}
		database.NormalizeChannelModelMetadata(r)
		if r.ModelCategory != database.ModelCategoryVideo || r.BillingMode != database.BillingModeVideoSecond {
			continue
		}
		if !database.ChannelModelAllowsEndpoint(r, endpoint) {
			continue
		}
		out = append(out, r)
	}
	return out
}

func resolveVideoPrice(req videoGenerationRequest, costTicks int64) (videoPriceResolution, error) {
	qty := req.DurationSeconds
	if qty <= 0 {
		qty = defaultVideoDuration
	}
	if costTicks > 0 {
		amount := (costTicks + 9999) / 10000 // xAI cost ticks: 10B ticks = 1 USD; 10k ticks = 1 micro_usd.
		unitPrice := amount
		if qty > 0 {
			unitPrice = (amount + qty - 1) / qty
		}
		return videoPriceResolution{
			Quantity:       qty,
			UnitPriceMicro: unitPrice,
			AmountMicroUSD: amount,
			CostTicks:      costTicks,
			Resolution:     req.Resolution,
			Size:           req.Size,
			AspectRatio:    req.AspectRatio,
			CostSource:     "upstream_usage",
		}, nil
	}

	var rules []database.ModelPricingRule
	if err := database.DB.
		Where("(model_id = ? OR official_model_id = ?) AND unit = ? AND direction = ? AND price_micro_usd > 0",
			req.Model, req.Model, "video_second", "output").
		Find(&rules).Error; err != nil {
		return videoPriceResolution{}, err
	}
	var selected *database.ModelPricingRule
	for i := range rules {
		if strings.EqualFold(strings.TrimSpace(rules[i].Resolution), req.Resolution) {
			selected = &rules[i]
			break
		}
	}
	if selected == nil {
		for i := range rules {
			if strings.TrimSpace(rules[i].Resolution) == "" {
				selected = &rules[i]
				break
			}
		}
	}
	if selected == nil {
		return videoPriceResolution{}, fmt.Errorf("official video pricing rule not found for %s resolution=%s", req.Model, req.Resolution)
	}
	amount, ok := database.CheckedMulInt64(selected.PriceMicroUSD, qty)
	if !ok || amount <= 0 {
		return videoPriceResolution{}, fmt.Errorf("video price overflow")
	}
	return videoPriceResolution{
		RuleID:         selected.ID,
		UnitPriceMicro: selected.PriceMicroUSD,
		Quantity:       qty,
		AmountMicroUSD: amount,
		Resolution:     selected.Resolution,
		Size:           firstNonEmptyLocal(req.Size, selected.Size),
		AspectRatio:    firstNonEmptyLocal(req.AspectRatio, selected.AspectRatio),
		CostSource:     "official_matrix",
	}, nil
}

func costTicksFromMediaResponse(body []byte) int64 {
	for _, path := range []string{"usage.cost_in_usd_ticks", "usage.costInUsdTicks", "cost_in_usd_ticks"} {
		v := gjson.GetBytes(body, path)
		if v.Exists() && v.Int() > 0 {
			return v.Int()
		}
	}
	return 0
}
