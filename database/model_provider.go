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
	EndpointImagesEdits       = "/v1/images/edits"
	EndpointVideosGenerations = "/v1/videos/generations"
	EndpointVideosEdits       = "/v1/videos/edits"
	EndpointVideosExtensions  = "/v1/videos/extensions"
	// EndpointGeminiNative 是 Google Gemini 兼容 API 入口（generateContent /
	// streamGenerateContent / countTokens / :predict for Imagen）的端点前缀。
	// 在 ChannelModel.AllowedEndpoints 中作为标记表示该 model 暴露给 /v1beta/models
	// 路由——P6 引入，让客户端用 Google AI SDK 直接调 DAOF。
	EndpointGeminiNative = "/v1beta/models"
	// EndpointResponsesWebsocket 是 Codex Responses WebSocket v2 入口的端点标记。
	// 实际 URL 是 `GET /v1/responses` 与 `GET /backend-api/codex/responses`（Codex
	// CLI / 桌面端的默认拨号点）。AllowedEndpoints 中加上此条 admin 才会允许该模型
	// 接受 WebSocket 长连。P7 引入；默认 disabled，admin 显式启用。
	EndpointResponsesWebsocket = "/v1/responses/ws"
)

// allowedImageEndpoints 是图像类 ChannelModel.AllowedEndpoints 的合法子集。
// admin 可任意组合开关：generations / edits / Gemini native。Gemini image / Imagen
// 必须用 EndpointGeminiNative；OpenAI/xAI 图像走 generations/edits；admin 也可让
// 同一个 model 同时挂多端点（不常见但合法）。
//
// 仅 package-internal：外部代码改 ChannelModel.AllowedEndpoints 应通过 admin
// API（controller/channel_model.go），那条路径会经 ValidateChannelModelActivation
// 自动验证。
var allowedImageEndpoints = []string{EndpointImagesGenerations, EndpointImagesEdits, EndpointGeminiNative}

// allowedVideoEndpoints 是视频类 ChannelModel.AllowedEndpoints 的合法子集。
// 同 allowedImageEndpoints，package-internal。
var allowedVideoEndpoints = []string{EndpointVideosGenerations, EndpointVideosEdits, EndpointVideosExtensions}

// 注：text 类没有独立的 endpoint subset 限制——文本走 /v1/chat/completions 等通用
// 入口，路径由 main.go 直接路由，无需 ChannelModel.AllowedEndpoints 子集校验。
// admin 启用 Gemini native / Codex WS 时直接挂端点常量 EndpointGeminiNative /
// EndpointResponsesWebsocket 即可。历史 AllowedTextEndpoints 变量从未被引用，已删除。

var (
	ErrImageGenerationUnsupported    = errors.New("image generation is not supported by the runtime yet")
	ErrVideoGenerationUnsupported    = errors.New("video generation is not supported by the runtime yet")
	ErrTextModelRequiresTokenBilling = errors.New("text models must use token billing")
	ErrTextModelRequiresTokenPricing = errors.New("enabled text models require at least one token price")
	ErrImageModelRequiresEndpoint    = errors.New("enabled image models must only allow /v1/images/generations and/or /v1/images/edits")
	ErrImageModelRequiresPricing     = errors.New("enabled image models require official image pricing rules")
	ErrImageEditMissingInputPricing  = errors.New("enabled image models allowing /v1/images/edits require an input image pricing rule")
	ErrImageTokenBillingUnsupported  = errors.New("token-billed image models are only supported for runtime-confirmed token usage models")
	ErrVideoModelRequiresEndpoint    = errors.New("enabled video models must only allow /v1/videos/generations, /v1/videos/edits, and/or /v1/videos/extensions")
	ErrVideoModelRequiresPricing     = errors.New("enabled video models require official output video-second pricing rules")
	ErrVideoEditMissingInputPricing  = errors.New("enabled video models allowing /v1/videos/edits or /v1/videos/extensions require an input video-second pricing rule")
)

func IsRuntimeImageModelSupported(modelID string) bool {
	_, ok := CanonicalRuntimeImageModel(modelID)
	return ok
}

// CanonicalRuntimeImageModel 把客户端传入的图像 model_id 归一化为运行时正式名。
//
// 双层 lookup：
//   - **静态内置**：gpt-image-2 + grok-imagine-image{,-quality}。pure function，
//     测试简单且 fast path。
//   - **动态 admin 注册**：查 ModelCatalog WHERE category=image AND supported=true，
//     支持 admin 通过新增 ModelCatalog row 注册任意 OpenAI 兼容图像服务
//     （fal.ai / Replicate / 自托管 OpenAI 兼容 endpoint 等）。
//     依赖 [[media_endpoint_allowlist]] 2026-05-19 策略修订：代码全支持，
//     管理后台显式启用。
//
// 客户端前缀（xai/x-ai/grok/openai/<provider>/）会被剥除后查 base。如果
// admin 注册时带前缀（如 "fal/sd-3.5"），则原 raw 也会查一次。
func CanonicalRuntimeImageModel(modelID string) (string, bool) {
	raw := strings.ToLower(strings.TrimSpace(modelID))
	if raw == "" {
		return "", false
	}
	// 静态内置 fast path
	if canonical, ok := canonicalStaticImageModel(raw); ok {
		return canonical, true
	}
	// admin-registered fallback：查 ModelCatalog
	return canonicalDBImageModel(raw)
}

// canonicalStaticImageModel 是内置 OpenAI 兼容图像模型的硬编码白名单。
// gpt-image-2 + grok-imagine-image{,-quality}，含前缀 alias 处理。
// 提取出来便于单元测试 + DB-less 环境（如 seed 测试）保持兼容。
func canonicalStaticImageModel(raw string) (string, bool) {
	prefix := ""
	base := raw
	if idx := strings.LastIndex(raw, "/"); idx >= 0 && idx < len(raw)-1 {
		prefix = strings.TrimSpace(raw[:idx])
		base = strings.TrimSpace(raw[idx+1:])
	}
	if prefix != "" && prefix != "xai" && prefix != "x-ai" && prefix != "grok" && prefix != "openai" {
		return "", false
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

// canonicalDBImageModel 查 ModelCatalog 看是否有 admin 注册的图像模型。
// 先查 raw（含前缀，admin 可能用 "fal/sd-3.5" 注册），再查 base（剥前缀）。
// 命中要求 Category=image AND Supported=true（admin 在 ModelCatalog 标 Supported
// 即表示"代码侧已接通该模型"——是否在客户端 RouteCache 暴露由 ChannelModel.Status 决定）。
func canonicalDBImageModel(raw string) (string, bool) {
	if DB == nil {
		return "", false
	}
	base := raw
	if idx := strings.LastIndex(raw, "/"); idx >= 0 && idx < len(raw)-1 {
		base = strings.TrimSpace(raw[idx+1:])
	}
	candidates := []string{raw}
	if base != raw {
		candidates = append(candidates, base)
	}
	for _, candidate := range candidates {
		var count int64
		err := DB.Model(&ModelCatalog{}).
			Where("LOWER(model_id) = ? AND category = ? AND supported = ?", candidate, ModelCategoryImage, true).
			Count(&count).Error
		if err == nil && count > 0 {
			return candidate, true
		}
	}
	return "", false
}

// IsRuntimeTokenBilledImageModel 判断指定 image model 是否走 token 计费路径
// （gpt-image-2 系列）。静态内置只识别 gpt-image-2；admin 注册的模型由
// ModelCatalog.BillingMode 字段决定。
func IsRuntimeTokenBilledImageModel(modelID string) bool {
	canonical, ok := CanonicalRuntimeImageModel(modelID)
	if !ok {
		return false
	}
	// 静态内置 fast path
	if canonical == "gpt-image-2" {
		return true
	}
	// admin-registered：查 ModelCatalog.BillingMode
	if DB == nil {
		return false
	}
	var cat ModelCatalog
	if err := DB.Where("LOWER(model_id) = ? AND category = ?", canonical, ModelCategoryImage).First(&cat).Error; err != nil {
		return false
	}
	return cat.BillingMode == BillingModeToken
}

func IsRuntimeVideoModelSupported(modelID string) bool {
	_, ok := CanonicalRuntimeVideoModel(modelID)
	return ok
}

// IsRuntimeGeminiModelSupported returns true when the model can be served via
// the Gemini native API path (/v1beta/models). admin 必须先在 ModelCatalog 中
// 注册该 model（provider_key="google" + supported=true）才返回 true。
func IsRuntimeGeminiModelSupported(modelID string) bool {
	_, ok := CanonicalRuntimeGeminiModel(modelID)
	return ok
}

// CanonicalRuntimeGeminiModel 把 client 传入的 Gemini model_id 归一化为运行时
// 正式名。剥前缀（models/、tunedModels/ 等 Google API URI 前缀）后查
// ModelCatalog WHERE provider_key="google" AND category IN (text, image) AND
// supported=true。
//
// 与 CanonicalRuntimeImageModel 不同点：Gemini 系列全是 admin 注册（没有静态
// white-list fast path），因为 Gemini text + image + Imagen 数量多变。
func CanonicalRuntimeGeminiModel(modelID string) (string, bool) {
	raw := strings.ToLower(strings.TrimSpace(modelID))
	if raw == "" {
		return "", false
	}
	// 剥 Gemini API URI 风格的前缀（admin 注册时一般用 base name）
	base := raw
	for _, prefix := range []string{"models/", "tunedmodels/"} {
		if strings.HasPrefix(base, prefix) {
			base = strings.TrimPrefix(base, prefix)
			break
		}
	}
	// 再剥通用 provider 前缀
	if idx := strings.LastIndex(base, "/"); idx >= 0 && idx < len(base)-1 {
		base = strings.TrimSpace(base[idx+1:])
	}

	if DB == nil {
		return "", false
	}
	candidates := []string{base, raw}
	if raw != base {
		candidates = []string{raw, base}
	}
	for _, candidate := range candidates {
		var cat ModelCatalog
		err := DB.Where("LOWER(model_id) = ? AND provider_key = ? AND supported = ?", candidate, "google", true).First(&cat).Error
		if err == nil {
			return cat.ModelID, true
		}
	}
	return "", false
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

// ChannelModelHasTokenPricing 判断 ChannelModel 是否填了 token pricing 字段。
// fix H3 (2026-05-19)：原实现只要任一字段 >0 就返回 true，admin 误把
// Input/Output 都设 0 但 Cached 留旧值时会绕过激活校验 → commit 路径 cost=0
// 用户白嫖。改为要求 Input 与 Output 都 > 0（每个文本请求都会有 input+output
// token，缺一就会零成本）。Cached 等可选字段不要求。长上下文档若启用
// (ContextPriceThreshold>0) 也必须 HighInput/HighOutput 都 > 0。
func ChannelModelHasTokenPricing(cm *ChannelModel) bool {
	if cm == nil {
		return false
	}
	if cm.InputPricePicoPerToken <= 0 || cm.OutputPricePicoPerToken <= 0 {
		return false
	}
	if cm.ContextPriceThreshold > 0 {
		if cm.HighInputPricePicoPerToken <= 0 || cm.HighOutputPricePicoPerToken <= 0 {
			return false
		}
	}
	return true
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

// ChannelModelAllowedEndpointsList 解析 cm.AllowedEndpoints 为 []string；
// 空值时回退到 DefaultAllowedEndpointsForCategory。
func ChannelModelAllowedEndpointsList(cm *ChannelModel) []string {
	if cm == nil {
		return nil
	}
	allowed := strings.TrimSpace(cm.AllowedEndpoints)
	if allowed == "" {
		allowed = DefaultAllowedEndpointsForCategory(cm.ModelCategory)
	}
	if allowed == "" {
		return nil
	}
	var endpoints []string
	if err := json.Unmarshal([]byte(allowed), &endpoints); err != nil {
		return nil
	}
	out := make([]string, 0, len(endpoints))
	for _, e := range endpoints {
		e = strings.TrimSpace(e)
		if e != "" {
			out = append(out, e)
		}
	}
	return out
}

// ChannelModelAllowsEndpointsSubset 校验 cm.AllowedEndpoints 是 allowed 白名单的子集且非空。
// 用于 P2 后的多端点放宽：允许 admin 自由组合开关 generations / edits，但不允许引入
// 白名单外的 endpoint（防止 admin 误配置接通未审计的上游路径）。
func ChannelModelAllowsEndpointsSubset(cm *ChannelModel, allowed []string) bool {
	eps := ChannelModelAllowedEndpointsList(cm)
	if len(eps) == 0 {
		return false
	}
	allowedSet := make(map[string]bool, len(allowed))
	for _, e := range allowed {
		allowedSet[strings.TrimSpace(e)] = true
	}
	for _, e := range eps {
		if !allowedSet[e] {
			return false
		}
	}
	return true
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
		if !ChannelModelAllowsEndpointsSubset(cm, allowedVideoEndpoints) {
			return ErrVideoModelRequiresEndpoint
		}
		if cm.BillingMode != BillingModeVideoSecond {
			return ErrVideoModelRequiresPricing
		}
		if !ModelHasUsagePricingRuleForDirection(cm.ModelID, "video_second", "output") {
			return ErrVideoModelRequiresPricing
		}
		// 启用了 edits 或 extensions 端点的视频模型必须额外配置 input video_second
		// 计费规则。xAI 视频 edits 输入视频按秒计费（fal.ai 公开口径 $0.01/sec），
		// extensions 同样基于原视频秒数 + 新增秒数双段计费——缺 input pricing 会让
		// fallback 估算偏差。
		if (ChannelModelAllowsEndpoint(cm, EndpointVideosEdits) || ChannelModelAllowsEndpoint(cm, EndpointVideosExtensions)) &&
			!ModelHasUsagePricingRuleForDirection(cm.ModelID, "video_second", "input") {
			return ErrVideoEditMissingInputPricing
		}
	case ModelCategoryImage:
		if !IsRuntimeImageModelSupported(cm.ModelID) {
			return ErrImageGenerationUnsupported
		}
		if !ChannelModelAllowsEndpointsSubset(cm, allowedImageEndpoints) {
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
			// 启用了 /v1/images/edits 端点的模型必须额外配置 input image 计费规则
			// （图生图：参考图按张计费，xAI grok-imagine-image-quality 上游 $0.01/image）。
			if ChannelModelAllowsEndpoint(cm, EndpointImagesEdits) &&
				!ModelHasUsagePricingRuleForDirection(cm.ModelID, "image", "input") {
				return ErrImageEditMissingInputPricing
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
