package database

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode"
)

const (
	ModelCategoryText  = "text"
	ModelCategoryImage = "image"
	ModelCategoryVideo = "video"

	BillingModeToken       = "token"
	BillingModeImage       = "image"
	BillingModeVideoSecond = "video_second"

	EndpointImagesGenerations = "/v1/images/generations"
	EndpointVideosGenerations = "/v1/videos/generations"
)

var (
	ErrImageGenerationUnsupported    = errors.New("image generation is not supported by the runtime yet")
	ErrVideoGenerationUnsupported    = errors.New("video generation is not supported by the runtime yet")
	ErrTextModelRequiresTokenBilling = errors.New("text models must use token billing")
	ErrTextModelRequiresTokenPricing = errors.New("enabled text models require at least one token price")
	ErrImageModelRequiresEndpoint    = errors.New("enabled image models must allow /v1/images/generations only")
	ErrImageModelRequiresPricing     = errors.New("enabled image models require official image pricing rules")
	ErrImageTokenBillingUnsupported  = errors.New("token-billed image models are only supported for runtime-confirmed token usage models")
	ErrVideoModelRequiresEndpoint    = errors.New("enabled video models must allow /v1/videos/generations only")
	ErrVideoModelRequiresPricing     = errors.New("enabled video models require official output video-second pricing rules")
)

func IsRuntimeImageModelSupported(modelID string) bool {
	_, ok := CanonicalRuntimeImageModel(modelID)
	return ok
}

func CanonicalRuntimeImageModel(modelID string) (string, bool) {
	raw := strings.ToLower(strings.TrimSpace(modelID))
	prefix := ""
	base := raw
	if idx := strings.LastIndex(raw, "/"); idx >= 0 && idx < len(raw)-1 {
		prefix = strings.TrimSpace(raw[:idx])
		base = strings.TrimSpace(raw[idx+1:])
	}
	if prefix != "" && prefix != "xai" && prefix != "x-ai" && prefix != "grok" {
		if prefix != "openai" {
			return "", false
		}
	}
	switch base {
	case "grok-imagine-image", "grok-imagine-image-quality":
		if prefix == "openai" {
			return "", false
		}
		return base, true
	case "gpt-image-2":
		if prefix != "" && prefix != "openai" {
			return "", false
		}
		return base, true
	default:
		return "", false
	}
}

func IsRuntimeTokenBilledImageModel(modelID string) bool {
	canonical, ok := CanonicalRuntimeImageModel(modelID)
	return ok && canonical == "gpt-image-2"
}

func IsRuntimeVideoModelSupported(modelID string) bool {
	_, ok := CanonicalRuntimeVideoModel(modelID)
	return ok
}

func CanonicalRuntimeVideoModel(modelID string) (string, bool) {
	raw := strings.ToLower(strings.TrimSpace(modelID))
	prefix := ""
	base := raw
	if idx := strings.LastIndex(raw, "/"); idx >= 0 && idx < len(raw)-1 {
		prefix = strings.TrimSpace(raw[:idx])
		base = strings.TrimSpace(raw[idx+1:])
	}
	if prefix != "" && prefix != "xai" && prefix != "x-ai" && prefix != "grok" {
		return "", false
	}
	if base == "grok-imagine-video" {
		return base, true
	}
	return "", false
}

// IsOpenAIModelID returns true for model IDs that belong to the OpenAI/Codex
// family exposed to customers, regardless of the underlying channel type.
func IsOpenAIModelID(modelID string) bool {
	id := strings.ToLower(strings.TrimSpace(modelID))
	if id == "" {
		return false
	}
	if strings.Contains(id, "openai") || IsOpenAIGPTModelID(id) {
		return true
	}
	if strings.HasPrefix(id, "chatgpt-") || strings.HasPrefix(id, "codex-") {
		return true
	}
	return isOpenAIOSeriesModelID(id)
}

func IsOpenAIGPTModelID(modelID string) bool {
	id := strings.ToLower(strings.TrimSpace(modelID))
	return hasOpenAIGPTSegment(id) || strings.HasPrefix(id, "chatgpt-")
}

func IsOpenAIGPTTextModelID(modelID string) bool {
	id := strings.ToLower(strings.TrimSpace(modelID))
	if id == "" || strings.Contains(id, "image") {
		return false
	}
	return IsOpenAIGPTModelID(id)
}

func hasOpenAIGPTSegment(id string) bool {
	for _, part := range strings.FieldsFunc(id, func(r rune) bool {
		switch r {
		case '/', ':', ' ', '\t':
			return true
		default:
			return false
		}
	}) {
		if part == "gpt" || strings.HasPrefix(part, "gpt-") || strings.HasPrefix(part, "gpt_") {
			return true
		}
	}
	return false
}

func isOpenAIOSeriesModelID(id string) bool {
	if len(id) < 2 || id[0] != 'o' {
		return false
	}
	return unicode.IsDigit(rune(id[1]))
}

func NormalizeModelCategory(category, modelID string) string {
	switch strings.ToLower(strings.TrimSpace(category)) {
	case ModelCategoryText, ModelCategoryImage, ModelCategoryVideo:
		return strings.ToLower(strings.TrimSpace(category))
	default:
		return InferModelCategory(modelID)
	}
}

func InferModelCategory(modelID string) string {
	id := strings.ToLower(strings.TrimSpace(modelID))
	switch {
	case id == "":
		return ModelCategoryText
	case strings.Contains(id, "video"):
		return ModelCategoryVideo
	case strings.Contains(id, "image"), strings.Contains(id, "imagine"), strings.Contains(id, "imagen"):
		return ModelCategoryImage
	default:
		return ModelCategoryText
	}
}

func NormalizeBillingMode(mode, category string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case BillingModeToken, BillingModeImage, BillingModeVideoSecond:
		return strings.ToLower(strings.TrimSpace(mode))
	default:
		switch NormalizeModelCategory(category, "") {
		case ModelCategoryImage:
			return BillingModeImage
		case ModelCategoryVideo:
			return BillingModeVideoSecond
		default:
			return BillingModeToken
		}
	}
}

func DefaultAllowedEndpointsForCategory(category string) string {
	switch NormalizeModelCategory(category, "") {
	case ModelCategoryImage:
		return `["` + EndpointImagesGenerations + `"]`
	case ModelCategoryVideo:
		return `["` + EndpointVideosGenerations + `"]`
	default:
		return ""
	}
}

func NormalizeChannelModelMetadata(cm *ChannelModel) {
	if cm == nil {
		return
	}
	cm.ModelID = strings.TrimSpace(cm.ModelID)
	if canonical, ok := CanonicalRuntimeImageModel(cm.ModelID); ok {
		cm.ModelID = canonical
	} else if canonical, ok := CanonicalRuntimeVideoModel(cm.ModelID); ok {
		cm.ModelID = canonical
	}
	cm.DisplayName = strings.TrimSpace(cm.DisplayName)
	if cm.DisplayName == "" {
		cm.DisplayName = cm.ModelID
	}
	cm.ModelCategory = NormalizeModelCategory(cm.ModelCategory, cm.ModelID)
	cm.BillingMode = NormalizeBillingMode(cm.BillingMode, cm.ModelCategory)
	if strings.TrimSpace(cm.OfficialModelID) == "" {
		cm.OfficialModelID = cm.ModelID
	}
	if strings.TrimSpace(cm.AllowedEndpoints) == "" {
		cm.AllowedEndpoints = DefaultAllowedEndpointsForCategory(cm.ModelCategory)
	}
}

func ChannelModelHasTokenPricing(cm *ChannelModel) bool {
	if cm == nil {
		return false
	}
	return cm.InputPricePicoPerToken > 0 ||
		cm.OutputPricePicoPerToken > 0 ||
		cm.CachedInputPricePicoPerToken > 0 ||
		cm.CacheWriteInputPricePicoPerToken > 0 ||
		cm.CacheWrite1hInputPricePicoPerToken > 0 ||
		cm.HighInputPricePicoPerToken > 0 ||
		cm.HighCachedInputPricePicoPerToken > 0 ||
		cm.HighOutputPricePicoPerToken > 0
}

func ChannelModelAllowsEndpoint(cm *ChannelModel, endpoint string) bool {
	if cm == nil {
		return false
	}
	allowed := strings.TrimSpace(cm.AllowedEndpoints)
	if allowed == "" {
		allowed = DefaultAllowedEndpointsForCategory(cm.ModelCategory)
	}
	return strings.Contains(allowed, `"`+endpoint+`"`)
}

func ChannelModelAllowsOnlyEndpoint(cm *ChannelModel, endpoint string) bool {
	if cm == nil {
		return false
	}
	allowed := strings.TrimSpace(cm.AllowedEndpoints)
	if allowed == "" {
		allowed = DefaultAllowedEndpointsForCategory(cm.ModelCategory)
	}
	var endpoints []string
	if err := json.Unmarshal([]byte(allowed), &endpoints); err == nil {
		if len(endpoints) != 1 {
			return false
		}
		return strings.TrimSpace(endpoints[0]) == endpoint
	}
	return allowed == `["`+endpoint+`"]`
}

func ModelHasUsagePricingRule(modelID, unit string) bool {
	if DB == nil {
		return false
	}
	var count int64
	err := DB.Model(&ModelPricingRule{}).
		Where("(model_id = ? OR official_model_id = ?) AND unit = ? AND price_micro_usd > 0", modelID, modelID, unit).
		Count(&count).Error
	return err == nil && count > 0
}

func ModelHasUsagePricingRuleForDirection(modelID, unit, direction string) bool {
	if DB == nil {
		return false
	}
	var count int64
	err := DB.Model(&ModelPricingRule{}).
		Where("(model_id = ? OR official_model_id = ?) AND unit = ? AND direction = ? AND price_micro_usd > 0",
			modelID, modelID, unit, direction).
		Count(&count).Error
	return err == nil && count > 0
}

// ValidateChannelModelActivation rejects route-cache-visible models that the
// current runtime cannot bill or serve deterministically. It is intentionally
// stricter than ValidateChannelModelPricing, which only checks numeric bounds.
func ValidateChannelModelActivation(cm *ChannelModel) error {
	if cm == nil || cm.Status != 1 {
		return nil
	}
	NormalizeChannelModelMetadata(cm)
	switch cm.ModelCategory {
	case ModelCategoryVideo:
		if !IsRuntimeVideoModelSupported(cm.ModelID) {
			return ErrVideoGenerationUnsupported
		}
		if !ChannelModelAllowsOnlyEndpoint(cm, EndpointVideosGenerations) {
			return ErrVideoModelRequiresEndpoint
		}
		if cm.BillingMode != BillingModeVideoSecond {
			return ErrVideoModelRequiresPricing
		}
		if !ModelHasUsagePricingRuleForDirection(cm.ModelID, "video_second", "output") {
			return ErrVideoModelRequiresPricing
		}
	case ModelCategoryImage:
		if !IsRuntimeImageModelSupported(cm.ModelID) {
			return ErrImageGenerationUnsupported
		}
		if !ChannelModelAllowsOnlyEndpoint(cm, EndpointImagesGenerations) {
			return ErrImageModelRequiresEndpoint
		}
		switch cm.BillingMode {
		case BillingModeToken:
			if !IsRuntimeTokenBilledImageModel(cm.ModelID) {
				return ErrImageTokenBillingUnsupported
			}
			if !ChannelModelHasTokenPricing(cm) {
				return ErrImageModelRequiresPricing
			}
		case BillingModeImage:
			if IsRuntimeTokenBilledImageModel(cm.ModelID) {
				return ErrImageTokenBillingUnsupported
			}
			if !ModelHasUsagePricingRuleForDirection(cm.ModelID, "image", "output") {
				return ErrImageModelRequiresPricing
			}
		default:
			return ErrImageModelRequiresPricing
		}
	case ModelCategoryText:
		if cm.BillingMode != BillingModeToken {
			return ErrTextModelRequiresTokenBilling
		}
		if !ChannelModelHasTokenPricing(cm) {
			return ErrTextModelRequiresTokenPricing
		}
	}
	return nil
}
