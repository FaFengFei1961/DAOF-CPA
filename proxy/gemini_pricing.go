// Package proxy / gemini_pricing.go
//
// M-R6 重构（2026-05-19）：从 gemini_native.go 1319 行单体抽出 pricing 相关
// helper，纯文件物理拆分。业务逻辑零改动。

package proxy

import (
	"fmt"
	"strings"

	"daof-cpa/database"

	"github.com/tidwall/gjson"
)

type geminiPriceResolution struct {
	BillingMode      string
	UnitPriceMicro   int64
	Quantity         int64
	AmountMicroUSD   int64
	PromptTokens     int
	CompletionTokens int
	CachedTokens     int
	ReasoningTokens  int
	ImageCount       int
	CostSource       string
}

// resolveGeminiPrecheckPrice 估算 precheck 阶段成本。Gemini text 用 estimate token
// 算法（与 chat completion stream.go 一致）；Gemini image / Imagen 默认按 1 张图保守估算。
func resolveGeminiPrecheckPrice(model string, body []byte, routes []*database.ChannelModel) (geminiPriceResolution, error) {
	// 找 BillingMode 决定 token vs image
	billingMode := geminiBillingMode(model, routes)

	if billingMode == database.BillingModeImage {
		// 找 image pricing rule（output direction）
		var rules []database.ModelPricingRule
		if err := database.DB.Where("(model_id = ? OR official_model_id = ?) AND unit = ? AND direction = ? AND price_micro_usd > 0",
			model, model, "image", "output").Find(&rules).Error; err != nil {
			return geminiPriceResolution{}, err
		}
		if len(rules) == 0 {
			return geminiPriceResolution{}, fmt.Errorf("Gemini image pricing rule not configured for %s; admin must add image/output ModelPricingRule before enabling", model)
		}
		// 默认按第 1 条 rule 估算 1 张图
		return geminiPriceResolution{
			BillingMode:    database.BillingModeImage,
			Quantity:       1,
			UnitPriceMicro: rules[0].PriceMicroUSD,
			AmountMicroUSD: rules[0].PriceMicroUSD,
			ImageCount:     1,
			CostSource:     "precheck_estimate",
		}, nil
	}

	// token 计费：找 token route，按 prompt size 估算
	var selected *database.ChannelModel
	for _, r := range routes {
		if r != nil && r.BillingMode == database.BillingModeToken && database.ChannelModelHasTokenPricing(r) {
			selected = r
			break
		}
	}
	if selected == nil {
		return geminiPriceResolution{}, fmt.Errorf("Gemini token pricing not configured for %s; admin must set ChannelModel input/output token prices", model)
	}
	estInput := estimatePrecheckTokens(body)
	estOutput := estInput / 2 // 保守估算 output ~ 1/2 input
	if estOutput < 128 {
		estOutput = 128
	}
	costMicroUSD, ok := checkedCostMicroUSD(
		estInput, selected.InputPricePicoPerToken,
		0, 0,
		0, 0,
		0, 0,
		estOutput, selected.OutputPricePicoPerToken,
		0, 0,
	)
	if !ok {
		return geminiPriceResolution{}, fmt.Errorf("Gemini token cost overflow")
	}
	return geminiPriceResolution{
		BillingMode:      database.BillingModeToken,
		Quantity:         int64(estInput + estOutput),
		AmountMicroUSD:   costMicroUSD,
		PromptTokens:     estInput,
		CompletionTokens: estOutput,
		CostSource:       "precheck_estimate",
	}, nil
}

// resolveGeminiActualPrice 从上游响应 body 解析真实 usage 后计费。
func resolveGeminiActualPrice(model string, body []byte, route *database.ChannelModel) (geminiPriceResolution, error) {
	billingMode := database.BillingModeToken
	if route != nil {
		billingMode = route.BillingMode
	}

	if billingMode == database.BillingModeImage {
		// 按响应 candidates[].content.parts[].inlineData 数量计费
		imageCount := countGeminiInlineImages(body)
		if imageCount <= 0 {
			return geminiPriceResolution{}, fmt.Errorf("no image data in Gemini response")
		}
		// 找 pricing rule
		var rules []database.ModelPricingRule
		if err := database.DB.Where("(model_id = ? OR official_model_id = ?) AND unit = ? AND direction = ? AND price_micro_usd > 0",
			model, model, "image", "output").Find(&rules).Error; err != nil {
			return geminiPriceResolution{}, err
		}
		if len(rules) == 0 {
			return geminiPriceResolution{}, fmt.Errorf("Gemini image pricing rule not found for %s", model)
		}
		unitPrice := rules[0].PriceMicroUSD
		amount, ok := database.CheckedMulInt64(unitPrice, int64(imageCount))
		if !ok || amount <= 0 {
			return geminiPriceResolution{}, fmt.Errorf("Gemini image price overflow")
		}
		return geminiPriceResolution{
			BillingMode:    database.BillingModeImage,
			Quantity:       int64(imageCount),
			UnitPriceMicro: unitPrice,
			AmountMicroUSD: amount,
			ImageCount:     imageCount,
			CostSource:     "upstream_usage",
		}, nil
	}

	// token 计费：从 usageMetadata 抽
	if route == nil || !database.ChannelModelHasTokenPricing(route) {
		return geminiPriceResolution{}, fmt.Errorf("Gemini token route has no pricing for %s", model)
	}
	prompt := int(gjson.GetBytes(body, "usageMetadata.promptTokenCount").Int())
	candidates := int(gjson.GetBytes(body, "usageMetadata.candidatesTokenCount").Int())
	cached := int(gjson.GetBytes(body, "usageMetadata.cachedContentTokenCount").Int())
	thinking := int(gjson.GetBytes(body, "usageMetadata.thoughtsTokenCount").Int())
	if prompt == 0 && candidates == 0 {
		return geminiPriceResolution{}, fmt.Errorf("Gemini response omitted usageMetadata")
	}
	inputPrice := route.InputPricePicoPerToken
	outputPrice := route.OutputPricePicoPerToken
	cachedPrice := route.CachedInputPricePicoPerToken
	if route.ContextPriceThreshold > 0 && prompt >= route.ContextPriceThreshold {
		if route.HighInputPricePicoPerToken > 0 {
			inputPrice = route.HighInputPricePicoPerToken
		}
		if route.HighOutputPricePicoPerToken > 0 {
			outputPrice = route.HighOutputPricePicoPerToken
		}
		if route.HighCachedInputPricePicoPerToken > 0 {
			cachedPrice = route.HighCachedInputPricePicoPerToken
		}
	}
	standardInput := prompt - cached
	if standardInput < 0 {
		standardInput = 0
	}
	cost, ok := checkedCostMicroUSD(
		standardInput, inputPrice,
		cached, cachedPrice,
		0, 0,
		0, 0,
		candidates, outputPrice,
		thinking, outputPrice,
	)
	if !ok || cost <= 0 {
		return geminiPriceResolution{}, fmt.Errorf("Gemini token cost calculation failed")
	}
	return geminiPriceResolution{
		BillingMode:      database.BillingModeToken,
		Quantity:         int64(prompt + candidates),
		AmountMicroUSD:   cost,
		PromptTokens:     prompt,
		CompletionTokens: candidates,
		CachedTokens:     cached,
		ReasoningTokens:  thinking,
		CostSource:       "upstream_usage",
	}, nil
}

// countGeminiInlineImages 数响应中 candidates[].content.parts[].inlineData 数量。
func countGeminiInlineImages(body []byte) int {
	count := 0
	gjson.GetBytes(body, "candidates").ForEach(func(_, cand gjson.Result) bool {
		cand.Get("content.parts").ForEach(func(_, part gjson.Result) bool {
			if part.Get("inlineData.data").Exists() {
				count++
			}
			return true
		})
		return true
	})
	return count
}

// geminiBillingMode 决定 Gemini model 计费模式（按 ModelCatalog 或 ChannelModel）。
func geminiBillingMode(model string, routes []*database.ChannelModel) string {
	for _, r := range routes {
		if r != nil && r.ModelID == model && r.BillingMode != "" {
			return r.BillingMode
		}
	}
	// fallback：查 ModelCatalog
	var cat database.ModelCatalog
	if err := database.DB.Where("LOWER(model_id) = ?", strings.ToLower(model)).First(&cat).Error; err == nil {
		return cat.BillingMode
	}
	return database.BillingModeToken
}

