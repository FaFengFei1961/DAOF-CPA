// Package proxy / image_pricing.go
//
// M-R6 重构（2026-05-19）：从 image_generation.go 1892 行单体抽出 pricing 相关
// helper，纯文件物理拆分。业务逻辑零改动。

package proxy

import (
	"errors"
	"fmt"
	"strings"

	"daof-cpa/database"

	"github.com/tidwall/gjson"
)

var errImageTokenUsageUnavailable = errors.New("token-billed image response omitted billable usage")

func resolveImagePrecheckPrice(req imageGenerationRequest, routes []*database.ChannelModel) (imagePriceResolution, error) {
	if database.IsRuntimeTokenBilledImageModel(req.Model) {
		return estimateTokenImagePrecheckPrice(req, routes)
	}
	return resolveImagePrice(req, 0, 0)
}

func resolveImageActualPrice(req imageGenerationRequest, body []byte, route *database.ChannelModel) (imagePriceResolution, error) {
	if database.IsRuntimeTokenBilledImageModel(req.Model) {
		return resolveTokenImagePrice(req, body, route)
	}
	return resolveImagePrice(req, countGeneratedImages(body), costTicksFromImageResponse(body))
}

func estimateTokenImagePrecheckPrice(req imageGenerationRequest, routes []*database.ChannelModel) (imagePriceResolution, error) {
	inputTokens := estimateTextPrecheckTokens(req.Prompt)
	if inputTokens <= 0 {
		inputTokens = 1
	}
	outputTokens := estimateGPTImageOutputTokens(req)
	if outputTokens <= 0 {
		outputTokens = 8192
	}
	var selected *database.ChannelModel
	for _, r := range routes {
		if r == nil || r.BillingMode != database.BillingModeToken {
			continue
		}
		if selected == nil || r.InputPricePicoPerToken+r.OutputPricePicoPerToken > selected.InputPricePicoPerToken+selected.OutputPricePicoPerToken {
			selected = r
		}
	}
	if selected == nil {
		return imagePriceResolution{}, fmt.Errorf("token image pricing route not found for %s", req.Model)
	}
	price, err := tokenImagePriceFromCounts(req, usageTokenCounts{
		PromptTokens:        inputTokens,
		CompletionTokens:    outputTokens,
		HasPromptTokens:     true,
		HasCompletionTokens: true,
	}, selected)
	if err != nil {
		return imagePriceResolution{}, err
	}
	price.CostSource = "precheck_estimate"
	return price, nil
}

func estimateGPTImageOutputTokens(req imageGenerationRequest) int {
	quality := strings.ToLower(strings.TrimSpace(req.Quality))
	size := strings.ToLower(strings.TrimSpace(req.Size))
	if quality == "" || quality == "auto" {
		quality = "high"
	}
	if size == "" || size == "auto" {
		size = "1024x1024"
	}
	// OpenAI's GPT Image 2 table prices output tokens at $30 / 1M tokens.
	// These estimates are the documented 1024/1536 prices rounded up to tokens.
	square := map[string]int{"low": 200, "medium": 1767, "high": 7034}
	wideOrTall := map[string]int{"low": 167, "medium": 1367, "high": 5500}
	switch size {
	case "1536x1024", "1024x1536":
		if tokens := wideOrTall[quality]; tokens > 0 {
			return tokens
		}
	default:
		if tokens := square[quality]; tokens > 0 {
			return tokens
		}
	}
	return square["high"]
}

func resolveTokenImagePrice(req imageGenerationRequest, body []byte, route *database.ChannelModel) (imagePriceResolution, error) {
	if route == nil || route.BillingMode != database.BillingModeToken || !database.ChannelModelHasTokenPricing(route) {
		return imagePriceResolution{}, fmt.Errorf("token image route has no token pricing for %s", req.Model)
	}
	if countGeneratedImages(body) <= 0 {
		return imagePriceResolution{}, errImageTokenUsageUnavailable
	}
	usageBlock := gjson.GetBytes(body, "usage")
	if !usageBlock.Exists() {
		return imagePriceResolution{}, errImageTokenUsageUnavailable
	}
	usage := extractUsageTokenCounts(usageBlock)
	if !usage.HasAny() || !usage.HasBillableTokens() {
		return imagePriceResolution{}, errImageTokenUsageUnavailable
	}
	return tokenImagePriceFromCounts(req, usage, route)
}

func tokenImagePriceFromCounts(req imageGenerationRequest, usage usageTokenCounts, route *database.ChannelModel) (imagePriceResolution, error) {
	usage = normalizeTokenImageUsage(usage)
	inputPricePico := route.InputPricePicoPerToken
	outputPricePico := route.OutputPricePicoPerToken
	cachedInputPricePico := route.CachedInputPricePicoPerToken
	if route.ContextPriceThreshold > 0 && usage.PromptTokens >= route.ContextPriceThreshold {
		if route.HighInputPricePicoPerToken > 0 {
			inputPricePico = route.HighInputPricePicoPerToken
		}
		if route.HighCachedInputPricePicoPerToken > 0 {
			cachedInputPricePico = route.HighCachedInputPricePicoPerToken
		}
		if route.HighOutputPricePicoPerToken > 0 {
			outputPricePico = route.HighOutputPricePicoPerToken
		}
	}
	cacheWriteInputPricePico := route.CacheWriteInputPricePicoPerToken
	if cacheWriteInputPricePico <= 0 {
		cacheWriteInputPricePico = inputPricePico
	}
	cacheWrite1hInputPricePico := route.CacheWrite1hInputPricePicoPerToken
	if cacheWrite1hInputPricePico <= 0 {
		cacheWrite1hInputPricePico = inputPricePico * 2
	}
	standardInputTokens := usage.PromptTokens - usage.CachedTokens - usage.CacheWriteTokens
	if standardInputTokens < 0 {
		standardInputTokens = 0
	}
	nonReasoningCompletion := usage.CompletionTokens - usage.ReasoningTokens
	if nonReasoningCompletion < 0 {
		nonReasoningCompletion = 0
	}
	costMicroUSD, ok := checkedCostMicroUSD(
		standardInputTokens, inputPricePico,
		usage.CachedTokens, cachedInputPricePico,
		usage.CacheWrite5mTokens, cacheWriteInputPricePico,
		usage.CacheWrite1hTokens, cacheWrite1hInputPricePico,
		nonReasoningCompletion, outputPricePico,
		usage.ReasoningTokens, outputPricePico,
	)
	if !ok || costMicroUSD <= 0 {
		return imagePriceResolution{}, fmt.Errorf("token image cost calculation failed")
	}
	return imagePriceResolution{
		BillingMode:                database.BillingModeToken,
		Quantity:                   int64(usage.PromptTokens + usage.CompletionTokens),
		AmountMicroUSD:             costMicroUSD,
		ResponseImages:             max(1, req.N),
		PromptTokens:               usage.PromptTokens,
		CompletionTokens:           usage.CompletionTokens,
		CachedTokens:               usage.CachedTokens,
		CacheWriteTokens:           usage.CacheWriteTokens,
		CacheWrite5mTokens:         usage.CacheWrite5mTokens,
		CacheWrite1hTokens:         usage.CacheWrite1hTokens,
		ReasoningTokens:            usage.ReasoningTokens,
		InputPricePico:             inputPricePico,
		OutputPricePico:            outputPricePico,
		CachedInputPricePico:       cachedInputPricePico,
		CacheWriteInputPricePico:   cacheWriteInputPricePico,
		CacheWrite1hInputPricePico: cacheWrite1hInputPricePico,
		Size:                       req.Size,
		Quality:                    req.Quality,
		CostSource:                 "upstream_usage",
	}, nil
}

func normalizeTokenImageUsage(usage usageTokenCounts) usageTokenCounts {
	if usage.PromptTokens < 0 {
		usage.PromptTokens = 0
	}
	if usage.CompletionTokens < 0 {
		usage.CompletionTokens = 0
	}
	if usage.CachedTokens < 0 {
		usage.CachedTokens = 0
	}
	if usage.CacheWriteTokens < 0 {
		usage.CacheWriteTokens = 0
	}
	if usage.CacheWrite5mTokens < 0 {
		usage.CacheWrite5mTokens = 0
	}
	if usage.CacheWrite1hTokens < 0 {
		usage.CacheWrite1hTokens = 0
	}
	if usage.ReasoningTokens < 0 {
		usage.ReasoningTokens = 0
	}
	usage.CacheWriteTokens = usage.CacheWrite5mTokens + usage.CacheWrite1hTokens
	if usage.CachedTokens > usage.PromptTokens {
		usage.CachedTokens = usage.PromptTokens
	}
	if usage.CachedTokens+usage.CacheWriteTokens > usage.PromptTokens {
		usage.CacheWriteTokens = usage.PromptTokens - usage.CachedTokens
		if usage.CacheWriteTokens < 0 {
			usage.CacheWriteTokens = 0
		}
		if usage.CacheWrite5mTokens+usage.CacheWrite1hTokens > usage.CacheWriteTokens {
			usage.CacheWrite5mTokens = usage.CacheWriteTokens
			usage.CacheWrite1hTokens = 0
		}
	}
	if usage.ReasoningTokens > usage.CompletionTokens {
		usage.ReasoningTokens = usage.CompletionTokens
	}
	return usage
}

func resolveImagePrice(req imageGenerationRequest, responseImages int, costTicks int64) (imagePriceResolution, error) {
	qty := int64(req.N)
	if responseImages > 0 {
		qty = int64(responseImages)
	}
	if qty <= 0 {
		qty = 1
	}
	if costTicks > 0 {
		amount := (costTicks + 9999) / 10000 // xAI cost ticks: 10B ticks = 1 USD; 10k ticks = 1 micro_usd.
		unitPrice := amount
		if qty > 0 {
			unitPrice = (amount + qty - 1) / qty
		}
		return imagePriceResolution{
			BillingMode:    database.BillingModeImage,
			Quantity:       qty,
			UnitPriceMicro: unitPrice,
			AmountMicroUSD: amount,
			ResponseImages: responseImages,
			CostTicks:      costTicks,
			Resolution:     req.Resolution,
			Size:           req.Size,
			Quality:        req.Quality,
			AspectRatio:    req.AspectRatio,
			CostSource:     "upstream_usage",
		}, nil
	}

	var rules []database.ModelPricingRule
	if err := database.DB.
		Where("(model_id = ? OR official_model_id = ?) AND unit = ? AND direction = ? AND price_micro_usd > 0",
			req.Model, req.Model, "image", "output").
		Find(&rules).Error; err != nil {
		return imagePriceResolution{}, err
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
		return imagePriceResolution{}, fmt.Errorf("official image pricing rule not found for %s resolution=%s", req.Model, req.Resolution)
	}
	amount, ok := database.CheckedMulInt64(selected.PriceMicroUSD, qty)
	if !ok || amount <= 0 {
		return imagePriceResolution{}, fmt.Errorf("image price overflow")
	}
	return imagePriceResolution{
		BillingMode:    database.BillingModeImage,
		RuleID:         selected.ID,
		UnitPriceMicro: selected.PriceMicroUSD,
		Quantity:       qty,
		AmountMicroUSD: amount,
		ResponseImages: responseImages,
		Resolution:     selected.Resolution,
		Size:           selected.Size,
		Quality:        selected.Quality,
		AspectRatio:    firstNonEmptyLocal(req.AspectRatio, selected.AspectRatio),
		CostSource:     "official_matrix",
	}, nil
}
