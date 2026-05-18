package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	mrand "math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"

	"daof-cpa/database"

	"github.com/gofiber/fiber/v2"
	"github.com/tidwall/gjson"
	"gorm.io/gorm"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

const maxImagePromptBytes = 64 * 1024

var imageBalanceLocks sync.Map

type imageGenerationRequest struct {
	Model             string `json:"model"`
	Prompt            string `json:"prompt"`
	N                 int    `json:"n,omitempty"`
	Size              string `json:"size,omitempty"`
	Quality           string `json:"quality,omitempty"`
	ResponseFormat    string `json:"response_format,omitempty"`
	OutputFormat      string `json:"output_format,omitempty"`
	Background        string `json:"background,omitempty"`
	Moderation        string `json:"moderation,omitempty"`
	OutputCompression *int   `json:"output_compression,omitempty"`
	Stream            *bool  `json:"stream,omitempty"`
	PartialImages     *int   `json:"partial_images,omitempty"`
	Resolution        string `json:"resolution,omitempty"`
	AspectRatio       string `json:"aspect_ratio,omitempty"`
}

type imagePriceResolution struct {
	BillingMode                string
	RuleID                     uint
	UnitPriceMicro             int64
	Quantity                   int64
	AmountMicroUSD             int64
	ResponseImages             int
	CostTicks                  int64
	PromptTokens               int
	CompletionTokens           int
	CachedTokens               int
	CacheWriteTokens           int
	CacheWrite5mTokens         int
	CacheWrite1hTokens         int
	ReasoningTokens            int
	InputPricePico             int64
	OutputPricePico            int64
	CachedInputPricePico       int64
	CacheWriteInputPricePico   int64
	CacheWrite1hInputPricePico int64
	Resolution                 string
	Size                       string
	Quality                    string
	AspectRatio                string
	CostSource                 string
}

type selectedImageUpstream struct {
	resp    *http.Response
	route   *database.ChannelModel
	channel *database.Channel
	cancel  context.CancelFunc
}

// ImageGenerationProxyHandler exposes only the OpenAI-compatible text-to-image
// endpoint we can deterministically meter today. Edits, variations, uploads,
// remote image inputs, and video tasks stay closed until their billing facts are
// equally auditable.
func ImageGenerationProxyHandler(c *fiber.Ctx) error {
	startTime := time.Now()
	clientIP := c.IP()
	path := strings.Clone(c.Path())
	fallbackUserOptIn := parseAllowFallbackHeader(c)

	token := bearerTokenFromHeader(c.Get("Authorization"))
	user, subToken, isSubToken, ok := lookupLLMUser(token)
	if !ok {
		if shouldRecordInvalidAuthApiLog(clientIP) {
			recordProxyApiLog(0, token, "unknown", 401, clientIP, startTime, path, "auth_error", "Invalid API Key")
		}
		return c.Status(401).JSON(fiber.Map{"error": fiber.Map{"message": "Invalid API Key", "type": "auth_error"}})
	}
	if user.Status != 1 {
		authSnapshotMutex.Lock()
		delete(AuthCache, token)
		authSnapshotMutex.Unlock()
		recordProxyApiLog(user.ID, token, "unknown", 403, clientIP, startTime, path, "auth_error", "Account suspended")
		return c.Status(403).JSON(fiber.Map{"error": fiber.Map{"message": "Account suspended", "type": "auth_error"}})
	}
	if isSubToken {
		if subToken.Status != 1 {
			recordProxyApiLog(user.ID, token, "unknown", 401, clientIP, startTime, path, "auth_error", "API Key is disabled or frozen")
			return c.Status(401).JSON(fiber.Map{"error": fiber.Map{"message": "API Key is disabled or frozen", "type": "auth_error"}})
		}
		if subToken.ExpiredAt != nil && time.Now().After(*subToken.ExpiredAt) {
			recordProxyApiLog(user.ID, token, "unknown", 401, clientIP, startTime, path, "auth_error", "API Key has expired")
			return c.Status(401).JSON(fiber.Map{"error": fiber.Map{"message": "API Key has expired", "type": "auth_error"}})
		}
		if subToken.QuotaLimit > 0 && subToken.UsedQuota >= subToken.QuotaLimit {
			recordProxyApiLog(user.ID, token, "unknown", 403, clientIP, startTime, path, "quota_exceeded", "API Key has reached its quota limit")
			return c.Status(403).JSON(fiber.Map{"error": fiber.Map{"message": "API Key has reached its quota limit", "type": "quota_exceeded"}})
		}
	}

	rawBody := c.Body()
	body := make([]byte, len(rawBody))
	copy(body, rawBody)
	req, sanitizedBody, parseErr := parseImageGenerationRequest(body)
	if parseErr != nil {
		recordProxyApiLog(user.ID, token, "unknown", 400, clientIP, startTime, path, "invalid_request", parseErr.Error())
		return c.Status(400).JSON(fiber.Map{"error": fiber.Map{"message": parseErr.Error(), "type": "invalid_request"}})
	}
	body = sanitizedBody

	if _, ok := database.CanonicalRuntimeImageModel(req.Model); !ok {
		recordProxyApiLog(user.ID, token, req.Model, 400, clientIP, startTime, path, "unsupported_model", "image model is not enabled for runtime")
		return c.Status(400).JSON(fiber.Map{"error": fiber.Map{
			"message":      "image model is not enabled for runtime",
			"type":         "unsupported_model",
			"message_code": "ERR_IMAGE_MODEL_UNSUPPORTED",
		}})
	}

	gatewayMutex.RLock()
	routes := append([]*database.ChannelModel(nil), RouteCache[req.Model]...)
	channelMapRef := ChannelMapCache
	gatewayMutex.RUnlock()
	routes = filterImageRoutes(routes)
	if len(routes) == 0 {
		recordProxyApiLog(user.ID, token, req.Model, 404, clientIP, startTime, path, "model_not_found", "Image model not available via any channel")
		return c.Status(404).JSON(fiber.Map{"error": fiber.Map{"message": "Image model not available via any channel", "type": "model_not_found"}})
	}

	prePrice, priceErr := resolveImagePrecheckPrice(req, routes)
	if priceErr != nil {
		recordProxyApiLog(user.ID, token, req.Model, 400, clientIP, startTime, path, "pricing_unavailable", priceErr.Error())
		return c.Status(400).JSON(fiber.Map{"error": fiber.Map{"message": priceErr.Error(), "type": "pricing_unavailable"}})
	}
	precheckBilling := ResolveBillingRules(req.Model, body, 0, "", fallbackUserOptIn).WithCosts(prePrice.AmountMicroUSD)
	engineDecision := Decide(EngineRequest{
		UserID:       user.ID,
		ModelName:    req.Model,
		InputTokens:  imageDecisionInputUnits(prePrice),
		OutputTokens: imageDecisionOutputUnits(prePrice),
		CostMicroUSD: precheckBilling.ChargedCostMicroUSD,
		IsPrecheck:   true,
	})
	if !engineDecision.Allowed {
		msg := engineDecision.BlockMessage
		if msg == "" {
			msg = "您的订阅额度已用尽，请购买套餐或充值余额"
		}
		if engineDecision.NeedsRetry {
			recordProxyApiLog(user.ID, token, req.Model, 503, clientIP, startTime, path, "subscription_load_failed", msg)
			return c.Status(503).JSON(fiber.Map{"error": fiber.Map{"message": msg, "type": "service_unavailable", "code": "subscription_load_failed"}})
		}
		if engineDecision.BlockQuotaPlanID != 0 {
			msg = precheckLimitMessage(engineDecision, precheckBilling)
			recordProxyApiLogWithPrecheck(user.ID, token, req.Model, 402, clientIP, startTime, path, "request_estimate_exceeds_window_remaining", msg, imageDecisionInputUnits(prePrice), imageDecisionOutputUnits(prePrice), precheckBilling, engineDecision)
			return c.Status(402).JSON(precheckLimitErrorPayload(msg, engineDecision, imageDecisionInputUnits(prePrice), imageDecisionOutputUnits(prePrice), precheckBilling))
		}
		recordProxyApiLog(user.ID, token, req.Model, 402, clientIP, startTime, path, "subscription_required", msg)
		return c.Status(402).JSON(fiber.Map{"error": fiber.Map{"message": msg, "type": "subscription_required"}})
	}

	var unlockBalance func()
	if engineDecision.FallbackToBalance {
		if !user.BalanceConsumeEnabled {
			recordProxyApiLog(user.ID, token, req.Model, 402, clientIP, startTime, path, "subscription_required", "subscription quota unavailable and balance consume disabled")
			return c.Status(402).JSON(fiber.Map{"error": fiber.Map{
				"message":      "当前请求无法使用订阅额度。请购买套餐，或在「账号设置 → 余额消费控制」中开启余额消费。",
				"type":         "subscription_required",
				"message_code": "ERR_QUOTA_EXHAUSTED_BALANCE_DISABLED",
			}})
		}
		unlockBalance = lockImageBalance(user.ID)
		defer unlockBalance()
		fresh, freshErr := loadFreshUserForImageBalance(user.ID)
		if freshErr != nil {
			recordProxyApiLog(user.ID, token, req.Model, 503, clientIP, startTime, path, "user_load_failed", freshErr.Error())
			return c.Status(503).JSON(fiber.Map{"error": fiber.Map{"message": "用户余额状态暂时不可用，请稍后重试", "type": "service_unavailable"}})
		}
		user = fresh
		if !CheckBalanceConsumeAllowed(user, prePrice.AmountMicroUSD) {
			recordProxyApiLog(user.ID, token, req.Model, 402, clientIP, startTime, path, "balance_limit_reached", "balance consume window limit reached")
			return c.Status(402).JSON(fiber.Map{"error": fiber.Map{
				"message":      "本周期余额消费已达上限，请提高限额或等待下次重置。",
				"type":         "balance_limit_reached",
				"message_code": "ERR_BALANCE_LIMIT_REACHED",
			}})
		}
		if user.Quota < prePrice.AmountMicroUSD {
			recordProxyApiLog(user.ID, token, req.Model, 403, clientIP, startTime, path, "quota_exceeded", "insufficient balance")
			return c.Status(403).JSON(fiber.Map{"error": fiber.Map{
				"message":      "余额不足，请充值",
				"type":         "quota_exceeded",
				"message_code": "ERR_INSUFFICIENT_BALANCE",
			}})
		}
	}

	modPolicy := LookupModerationPolicy(req.Model)
	if modPolicy.IsActive() || modPolicy.LoadFailed() {
		gate := &ModerationGate{
			Ctx:       c,
			UserID:    user.ID,
			TokenHash: HashTokenForLog(token),
			Body:      body,
			ModelName: req.Model,
			SrcFormat: sdktranslator.FormatOpenAI,
			Policy:    modPolicy,
			ClientIP:  clientIP,
			StartTime: startTime,
		}
		if rejected, rerr := gate.Run(); rejected {
			return rerr
		}
	}

	upstream, upstreamErr := callImageUpstream(c, req.Model, body, routes, channelMapRef)
	if upstreamErr != nil {
		recordProxyApiLog(user.ID, token, req.Model, upstreamErr.status, clientIP, startTime, path, upstreamErr.errorType, upstreamErr.message)
		c.Set("Content-Type", "application/json")
		return c.Status(upstreamErr.status).Send(upstreamErr.body)
	}
	defer upstream.resp.Body.Close()
	if upstream.cancel != nil {
		defer upstream.cancel()
	}

	statusCode := upstream.resp.StatusCode
	bodyCopy, _ := io.ReadAll(upstream.resp.Body)
	if statusCode < 200 || statusCode >= 300 {
		log.Printf("[IMAGE-UPSTREAM-ERR] channel=%d status=%d body=%s", upstream.route.ChannelID, statusCode, sanitizeError(truncForLog(bodyCopy, 1024), 1024))
		recordProxyApiLog(user.ID, token, req.Model, statusCode, clientIP, startTime, path, "upstream_error", string(bodyCopy))
		c.Set("Content-Type", "application/json")
		return c.Status(statusCode).JSON(fiber.Map{"error": fiber.Map{
			"message": fmt.Sprintf("upstream returned %d", statusCode),
			"type":    "upstream_error",
		}})
	}

	actualPrice, priceErr := resolveImageActualPrice(req, bodyCopy, upstream.route)
	if priceErr != nil {
		if errors.Is(priceErr, errImageTokenUsageUnavailable) {
			pendingPrice, estimateErr := resolveImagePrecheckPrice(req, routes)
			if estimateErr != nil {
				pendingPrice = imagePriceResolution{
					BillingMode:    database.BillingModeToken,
					Quantity:       1,
					AmountMicroUSD: prePrice.AmountMicroUSD,
					ResponseImages: countGeneratedImages(bodyCopy),
					CostSource:     "pending_reconcile",
				}
			}
			pendingPrice.CostSource = "pending_reconcile"
			billingResolution := ResolveBillingRules(req.Model, body, 0, selectedChannelTypeForImage(upstream.channel), fallbackUserOptIn).WithCosts(pendingPrice.AmountMicroUSD)
			apiLogID := recordImagePendingReconcile(user, token, req, pendingPrice, billingResolution, selectedChannelTypeForImage(upstream.channel), statusCode, clientIP, path, startTime, "token usage missing after delivery")
			if apiLogID == 0 {
				log.Printf("[IMAGE-BILLING-CRITICAL] user=%d model=%s served but missing token usage and pending log failed", user.ID, req.Model)
			}
			copyImageResponseHeaders(c, upstream.resp.Header)
			setModelAuditHeaders(c, req.Model, req.Model, fallbackUserOptIn, "")
			return c.Status(statusCode).Send(bodyCopy)
		}
		log.Printf("[IMAGE-BILLING-CRITICAL] user=%d model=%s price resolve after delivery failed: %v", user.ID, req.Model, priceErr)
		recordProxyApiLog(user.ID, token, req.Model, 502, clientIP, startTime, path, "pricing_unavailable", priceErr.Error())
		return c.Status(502).JSON(fiber.Map{"error": fiber.Map{"message": "image pricing unavailable", "type": "pricing_unavailable"}})
	}
	selectedChannelType := ""
	if upstream.channel != nil {
		selectedChannelType = upstream.channel.Type
	}
	billingResolution := ResolveBillingRules(req.Model, body, 0, selectedChannelType, fallbackUserOptIn).WithCosts(actualPrice.AmountMicroUSD)
	chargedCostMicroUSD := billingResolution.ChargedCostMicroUSD
	var apiLogID uint

	commitDecision := Decide(EngineRequest{
		UserID:       user.ID,
		ModelName:    req.Model,
		InputTokens:  imageDecisionInputUnits(actualPrice),
		OutputTokens: imageDecisionOutputUnits(actualPrice),
		CostMicroUSD: chargedCostMicroUSD,
		IsPrecheck:   false,
	})
	if commitDecision.NeedsRetry {
		apiLogID = recordImagePendingReconcile(user, token, req, actualPrice, billingResolution, selectedChannelType, statusCode, clientIP, path, startTime, "subscription commit failed")
		copyImageResponseHeaders(c, upstream.resp.Header)
		setModelAuditHeaders(c, req.Model, req.Model, fallbackUserOptIn, "")
		return c.Status(statusCode).Send(bodyCopy)
	}
	commitOK := commitDecision.Allowed && !commitDecision.FallbackToBalance
	if !commitOK && !user.BalanceConsumeEnabled {
		apiLogID = recordImagePendingReconcile(user, token, req, actualPrice, billingResolution, selectedChannelType, statusCode, clientIP, path, startTime, "subscription commit fell back to disabled balance")
		copyImageResponseHeaders(c, upstream.resp.Header)
		setModelAuditHeaders(c, req.Model, req.Model, fallbackUserOptIn, "")
		return c.Status(statusCode).Send(bodyCopy)
	}

	var effectiveRevenueMicroUSD int64
	var referralReward database.ReferralPaidSpendRewardResult
	if commitOK {
		apiLogID = createImageApiLog(user.ID, token, req, actualPrice, billingResolution, selectedChannelType, statusCode, clientIP, path, startTime)
		subID := commitDecision.SubscriptionID
		if billErr := database.WriteBillingEntryNonFatal(database.BillingEntryInput{
			UserID:               user.ID,
			EntryType:            database.BillingTypeApiUsageSub,
			AmountUSD:            0,
			BalanceAfterUSD:      user.Quota,
			ModelName:            req.Model,
			TokensTotal:          imageTokenTotal(actualPrice),
			SourceSubscriptionID: &subID,
			RelatedType:          relatedTypeForApiLog(apiLogID),
			RelatedID:            apiLogID,
			Description:          fmt.Sprintf("套餐 · %s · %s · %s", req.Model, imageUsageDescription(actualPrice), FormatChargedCostForDescription(actualPrice.AmountMicroUSD, chargedCostMicroUSD)),
		}); billErr != nil {
			log.Printf("[IMAGE-BILLING-AUDIT-FAIL] user=%d sub=%d model=%s: %v", user.ID, subID, req.Model, billErr)
		}
		effectiveRevenueMicroUSD = subscriptionRevenueMicroUSD(chargedCostMicroUSD, commitDecision.SubscriptionIsGranted)
		if apiLogID != 0 {
			RecordApiLogRevenue(apiLogID, database.RevenueSourceSubscription, effectiveRevenueMicroUSD, subID)
		}
	} else {
		apiLogID, effectiveRevenueMicroUSD, referralReward = deductImageBalanceAndLog(user, token, req, actualPrice, billingResolution, selectedChannelType, statusCode, clientIP, path, startTime)
	}

	if isSubToken && effectiveRevenueMicroUSD > 0 {
		incrementSubTokenUsedQuota(token, subToken, effectiveRevenueMicroUSD)
	}
	if referralReward.ReferrerID != 0 && referralReward.RewardMicroUSD > 0 {
		RefreshUserAuth(referralReward.ReferrerID)
	}
	if apiLogID == 0 {
		log.Printf("[IMAGE-BILLING-CRITICAL] user=%d model=%s served but api_log missing", user.ID, req.Model)
	}

	copyImageResponseHeaders(c, upstream.resp.Header)
	setModelAuditHeaders(c, req.Model, req.Model, fallbackUserOptIn, "")
	return c.Status(statusCode).Send(bodyCopy)
}

type upstreamImageError struct {
	status    int
	errorType string
	message   string
	body      []byte
}

func bearerTokenFromHeader(authHeader string) string {
	authHeader = strings.TrimSpace(authHeader)
	if !strings.HasPrefix(authHeader, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer "))
}

func lookupLLMUser(token string) (*database.User, *database.AccessToken, bool, bool) {
	authSnapshotMutex.RLock()
	defer authSnapshotMutex.RUnlock()
	user, ok := AuthCache[token]
	subToken, isSubToken := AuthTokenCache[token]
	return user, subToken, isSubToken, ok
}

func parseImageGenerationRequest(body []byte) (imageGenerationRequest, []byte, error) {
	if len(body) == 0 {
		return imageGenerationRequest{}, nil, fmt.Errorf("request body is required")
	}
	if !gjson.ValidBytes(body) {
		return imageGenerationRequest{}, nil, fmt.Errorf("request body must be valid JSON")
	}
	for _, field := range []string{
		"image", "images", "image_url", "image_urls", "input_image", "input_images",
		"mask", "reference_image", "reference_images", "init_image", "video", "videos",
	} {
		if gjson.GetBytes(body, field).Exists() {
			return imageGenerationRequest{}, nil, fmt.Errorf("%s is not supported on /v1/images/generations", field)
		}
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var req imageGenerationRequest
	if err := dec.Decode(&req); err != nil {
		return imageGenerationRequest{}, nil, fmt.Errorf("unsupported image request field or invalid body: %w", err)
	}
	canonicalModel, ok := database.CanonicalRuntimeImageModel(req.Model)
	req.Model = strings.TrimSpace(req.Model)
	req.Prompt = strings.TrimSpace(req.Prompt)
	req.Size = strings.TrimSpace(req.Size)
	req.Quality = strings.TrimSpace(req.Quality)
	req.ResponseFormat = strings.TrimSpace(req.ResponseFormat)
	req.OutputFormat = strings.TrimSpace(req.OutputFormat)
	req.Background = strings.TrimSpace(req.Background)
	req.Moderation = strings.TrimSpace(req.Moderation)
	if req.Model == "" {
		return imageGenerationRequest{}, nil, fmt.Errorf("model is required")
	}
	if ok {
		req.Model = canonicalModel
	}
	if req.Prompt == "" {
		return imageGenerationRequest{}, nil, fmt.Errorf("prompt is required")
	}
	if len([]byte(req.Prompt)) > maxImagePromptBytes {
		return imageGenerationRequest{}, nil, fmt.Errorf("prompt is too large")
	}
	if req.N == 0 {
		req.N = 1
	}
	if req.N < 1 || req.N > 10 {
		return imageGenerationRequest{}, nil, fmt.Errorf("n must be between 1 and 10")
	}
	responseFormat, err := normalizeImageResponseFormat(req.ResponseFormat)
	if err != nil {
		return imageGenerationRequest{}, nil, err
	}
	req.ResponseFormat = responseFormat
	if req.Stream != nil && *req.Stream {
		return imageGenerationRequest{}, nil, fmt.Errorf("streaming image generation is not supported")
	}
	if req.PartialImages != nil && *req.PartialImages != 0 {
		return imageGenerationRequest{}, nil, fmt.Errorf("partial image streaming is not supported")
	}

	payload := map[string]any{
		"model":  req.Model,
		"prompt": req.Prompt,
	}
	if req.ResponseFormat != "" {
		payload["response_format"] = req.ResponseFormat
	}

	if database.IsRuntimeTokenBilledImageModel(req.Model) {
		if req.N != 1 {
			return imageGenerationRequest{}, nil, fmt.Errorf("n must be 1 for gpt-image-2")
		}
		if req.ResponseFormat == "url" {
			return imageGenerationRequest{}, nil, fmt.Errorf("response_format=url is not supported for gpt-image-2; use b64_json")
		}
		if req.Resolution != "" || req.AspectRatio != "" {
			return imageGenerationRequest{}, nil, fmt.Errorf("resolution/aspect_ratio are not supported for gpt-image-2; use size")
		}
		if req.Size != "" {
			size, err := normalizeGPTImageSize(req.Size)
			if err != nil {
				return imageGenerationRequest{}, nil, err
			}
			req.Size = size
			payload["size"] = req.Size
		}
		if req.Quality != "" {
			quality, err := normalizeGPTImageQuality(req.Quality)
			if err != nil {
				return imageGenerationRequest{}, nil, err
			}
			req.Quality = quality
			payload["quality"] = req.Quality
		}
		if req.OutputFormat != "" {
			outputFormat, err := normalizeGPTImageOutputFormat(req.OutputFormat)
			if err != nil {
				return imageGenerationRequest{}, nil, err
			}
			req.OutputFormat = outputFormat
			payload["output_format"] = req.OutputFormat
		}
		if req.Background != "" {
			background, err := normalizeGPTImageBackground(req.Background)
			if err != nil {
				return imageGenerationRequest{}, nil, err
			}
			req.Background = background
			if req.Background == "transparent" {
				return imageGenerationRequest{}, nil, fmt.Errorf("background=transparent is not supported for gpt-image-2")
			}
			payload["background"] = req.Background
		}
		if req.Moderation != "" {
			moderation, err := normalizeGPTImageModeration(req.Moderation)
			if err != nil {
				return imageGenerationRequest{}, nil, err
			}
			req.Moderation = moderation
			payload["moderation"] = req.Moderation
		}
		if req.OutputCompression != nil {
			if *req.OutputCompression < 0 || *req.OutputCompression > 100 {
				return imageGenerationRequest{}, nil, fmt.Errorf("output_compression must be between 0 and 100")
			}
			payload["output_compression"] = *req.OutputCompression
		}
	} else {
		if req.Quality != "" || req.OutputFormat != "" || req.Background != "" || req.Moderation != "" || req.OutputCompression != nil {
			return imageGenerationRequest{}, nil, fmt.Errorf("quality/output_format/background/moderation/output_compression are not supported for xAI image models")
		}
		req.Resolution = normalizeImageResolution(req.Resolution, req.Size)
		rawAspectRatio := strings.TrimSpace(req.AspectRatio)
		req.AspectRatio = normalizeImageAspectRatio(req.AspectRatio, req.Size)
		if req.Resolution == "" {
			req.Resolution = "1K"
		}
		if rawAspectRatio != "" && req.AspectRatio == "" {
			return imageGenerationRequest{}, nil, fmt.Errorf("aspect_ratio must be 1:1, 16:9, 9:16, 4:3, 3:4, 3:2, or 2:3 for xAI image models")
		}
		if req.AspectRatio == "" {
			req.AspectRatio = "1:1"
		}
		payload["n"] = req.N
		payload["resolution"] = strings.ToLower(req.Resolution)
		if req.AspectRatio != "" {
			payload["aspect_ratio"] = req.AspectRatio
		}
	}
	sanitized, err := json.Marshal(payload)
	if err != nil {
		return imageGenerationRequest{}, nil, fmt.Errorf("build sanitized request: %w", err)
	}
	return req, sanitized, nil
}

func normalizeImageResolution(resolution, size string) string {
	r := strings.ToUpper(strings.TrimSpace(resolution))
	if r == "" {
		r = strings.ToUpper(strings.TrimSpace(size))
	}
	switch r {
	case "1K", "1024X1024", "1024":
		return "1K"
	case "2K", "2048X2048", "2048":
		return "2K"
	default:
		return ""
	}
}

func normalizeImageAspectRatio(aspectRatio, size string) string {
	normalized := ""
	switch strings.ToLower(strings.TrimSpace(aspectRatio)) {
	case "square", "1:1":
		normalized = "1:1"
	case "landscape", "16:9":
		normalized = "16:9"
	case "portrait", "9:16":
		normalized = "9:16"
	case "4:3", "3:4", "3:2", "2:3":
		normalized = strings.ToLower(strings.TrimSpace(aspectRatio))
	}
	switch strings.ToLower(strings.TrimSpace(size)) {
	case "1024x1024", "2048x2048", "1:1":
		return "1:1"
	case "1792x1024", "16:9":
		return "16:9"
	case "1024x1792", "9:16":
		return "9:16"
	case "1536x1024", "3:2":
		return "3:2"
	case "1024x1536", "2:3":
		return "2:3"
	default:
		return normalized
	}
}

func normalizeImageResponseFormat(responseFormat string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(responseFormat)) {
	case "":
		return "", nil
	case "url":
		return "url", nil
	case "b64_json":
		return "b64_json", nil
	default:
		return "", fmt.Errorf("response_format must be url or b64_json")
	}
}

func normalizeGPTImageSize(size string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(size)) {
	case "", "auto":
		return "auto", nil
	case "1024x1024", "1536x1024", "1024x1536":
		return strings.ToLower(strings.TrimSpace(size)), nil
	default:
		return "", fmt.Errorf("size must be auto, 1024x1024, 1536x1024, or 1024x1536 for gpt-image-2")
	}
}

func normalizeGPTImageQuality(quality string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(quality)) {
	case "", "auto":
		return "auto", nil
	case "low", "medium", "high":
		return strings.ToLower(strings.TrimSpace(quality)), nil
	default:
		return "", fmt.Errorf("quality must be auto, low, medium, or high for gpt-image-2")
	}
}

func normalizeGPTImageOutputFormat(format string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "png":
		return "png", nil
	case "jpeg", "webp":
		return strings.ToLower(strings.TrimSpace(format)), nil
	default:
		return "", fmt.Errorf("output_format must be png, jpeg, or webp for gpt-image-2")
	}
}

func normalizeGPTImageBackground(background string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(background)) {
	case "", "auto":
		return "auto", nil
	case "opaque", "transparent":
		return strings.ToLower(strings.TrimSpace(background)), nil
	default:
		return "", fmt.Errorf("background must be auto, opaque, or transparent for gpt-image-2")
	}
}

func normalizeGPTImageModeration(moderation string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(moderation)) {
	case "", "auto":
		return "auto", nil
	case "low":
		return "low", nil
	default:
		return "", fmt.Errorf("moderation must be auto or low for gpt-image-2")
	}
}

func filterImageRoutes(routes []*database.ChannelModel) []*database.ChannelModel {
	out := make([]*database.ChannelModel, 0, len(routes))
	for _, r := range routes {
		if r == nil {
			continue
		}
		database.NormalizeChannelModelMetadata(r)
		if r.ModelCategory != database.ModelCategoryImage {
			continue
		}
		if !database.ChannelModelAllowsOnlyEndpoint(r, database.EndpointImagesGenerations) {
			continue
		}
		if database.IsRuntimeTokenBilledImageModel(r.ModelID) {
			if r.BillingMode != database.BillingModeToken || !database.ChannelModelHasTokenPricing(r) {
				continue
			}
		} else if r.BillingMode != database.BillingModeImage {
			continue
		}
		out = append(out, r)
	}
	return out
}

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

func costTicksFromImageResponse(body []byte) int64 {
	for _, path := range []string{"usage.cost_in_usd_ticks", "usage.costInUsdTicks", "cost_in_usd_ticks"} {
		v := gjson.GetBytes(body, path)
		if v.Exists() && v.Int() > 0 {
			return v.Int()
		}
	}
	return 0
}

func countGeneratedImages(body []byte) int {
	data := gjson.GetBytes(body, "data")
	if !data.IsArray() {
		return 0
	}
	count := 0
	data.ForEach(func(_, _ gjson.Result) bool {
		count++
		return true
	})
	return count
}

func lockImageBalance(userID uint) func() {
	v, _ := imageBalanceLocks.LoadOrStore(userID, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func loadFreshUserForImageBalance(userID uint) (*database.User, error) {
	var user database.User
	if err := database.DB.Select("id, username, role, token, quota, paid_quota, status, balance_consume_enabled, balance_consume_limit_usd, balance_consume_window_seconds, balance_consume_window_start_at, balance_consumed_in_window").
		First(&user, userID).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

func callImageUpstream(c *fiber.Ctx, modelName string, body []byte, routes []*database.ChannelModel, channelMapRef map[uint]*database.Channel) (*selectedImageUpstream, *upstreamImageError) {
	failedChannels := make(map[uint]bool)
	maxRetries := len(routes)
	if maxRetries > 5 {
		maxRetries = 5
	}
	var last *upstreamImageError
	for attempt := 0; attempt < maxRetries; attempt++ {
		if backoff := computeRetryBackoff(attempt); backoff > 0 {
			select {
			case <-time.After(backoff):
			case <-c.Context().Done():
				return nil, imageErr(499, "client_disconnect_during_retry", "client disconnected during retry backoff")
			}
		}
		available, totalWeight := availableImageRoutes(routes, failedChannels, modelName)
		if len(available) == 0 {
			if last != nil {
				return nil, last
			}
			return nil, imageErr(502, "backend_exhausted", "All image upstream channels exhausted or failing")
		}
		selected := chooseWeightedImageRoute(available, totalWeight)
		ch := channelMapRef[selected.ChannelID]
		if ch == nil {
			failedChannels[selected.ChannelID] = true
			last = imageErr(502, "channel_unavailable", "channel was disabled or removed mid-flight")
			continue
		}
		channelType := NormalizeChannelType(ch.Type)
		if channelType != ChannelTypeCLIProxy {
			failedChannels[selected.ChannelID] = true
			last = imageErr(502, "channel_misconfigured", "image generation is only supported through CLIProxyAPI channels")
			continue
		}
		upstreamURL := strings.TrimRight(ch.BaseURL, "/") + database.EndpointImagesGenerations
		upstreamCtx, upstreamCancel := context.WithCancel(c.Context())
		httpReq, err := http.NewRequestWithContext(upstreamCtx, http.MethodPost, upstreamURL, bytes.NewReader(body))
		if err != nil {
			upstreamCancel()
			failedChannels[selected.ChannelID] = true
			last = imageErr(502, "bad_gateway", err.Error())
			continue
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+ch.Key)
		if ch.Headers != "" {
			var customHeaders map[string]string
			if err := json.Unmarshal([]byte(ch.Headers), &customHeaders); err == nil {
				for k, v := range customHeaders {
					httpReq.Header.Set(k, v)
				}
			} else {
				log.Printf("[IMAGE] channel %d invalid Headers json: %v (raw=%q)", ch.ID, err, ch.Headers)
			}
		}
		httpClient := &http.Client{
			Transport: getTransport(ch.ProxyURL),
			Timeout:   nonStreamUpstreamTimeout(),
		}
		resp, err := httpClient.Do(httpReq)
		if err != nil {
			upstreamCancel()
			failedChannels[selected.ChannelID] = true
			MarkChannelFailure(selected.ChannelID, 0)
			log.Printf("[IMAGE-UPSTREAM-DIAL] channel=%d err=%s", selected.ChannelID, sanitizeError(err.Error(), 256))
			last = imageErr(502, "bad_gateway", "upstream connection failed (channel rotated)")
			continue
		}
		action := classifyUpstreamStatus(resp.StatusCode)
		switch action {
		case StatusActionSuccess, StatusActionClientError:
			MarkChannelSuccess(selected.ChannelID)
			return &selectedImageUpstream{resp: resp, route: selected, channel: ch, cancel: upstreamCancel}, nil
		case StatusActionRateLimit:
			failedChannels[selected.ChannelID] = true
			setChannelRateLimitCooldown(selected.ChannelID, parseRetryAfter(resp.Header.Get("Retry-After")))
			bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			upstreamCancel()
			log.Printf("[IMAGE-UPSTREAM-RATE-LIMIT] channel=%d status=%d body=%q", selected.ChannelID, resp.StatusCode, truncForLog(bodyBytes, 256))
			last = imageErr(http.StatusTooManyRequests, "upstream_rate_limited", "all upstream channels are rate limited")
		case StatusActionConfigError:
			failedChannels[selected.ChannelID] = true
			bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			upstreamCancel()
			markChannelModelUnhealthy(selected.ChannelID, modelName)
			log.Printf("[IMAGE-UPSTREAM-CONFIG] channel=%d model=%s status=%d body=%q", selected.ChannelID, modelName, resp.StatusCode, truncForLog(bodyBytes, 256))
			last = imageErr(resp.StatusCode, "channel_model_unhealthy", "upstream returned config error for configured image model")
		default:
			failedChannels[selected.ChannelID] = true
			bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			upstreamCancel()
			MarkChannelFailure(selected.ChannelID, resp.StatusCode)
			log.Printf("[IMAGE-UPSTREAM-ERR] channel=%d status=%d body=%q", selected.ChannelID, resp.StatusCode, truncForLog(bodyBytes, 256))
			last = imageErr(resp.StatusCode, "upstream_error", fmt.Sprintf("upstream returned %d (channel rotated)", resp.StatusCode))
		}
	}
	if last != nil {
		return nil, last
	}
	return nil, imageErr(502, "backend_exhausted", "All image upstream channels exhausted or failing")
}

func imageErr(status int, typ, message string) *upstreamImageError {
	if status <= 0 {
		status = 502
	}
	body, _ := json.Marshal(map[string]any{"error": map[string]any{"message": message, "type": typ}})
	return &upstreamImageError{status: status, errorType: typ, message: message, body: body}
}

func availableImageRoutes(routes []*database.ChannelModel, failed map[uint]bool, modelName string) ([]*database.ChannelModel, int) {
	out := make([]*database.ChannelModel, 0, len(routes))
	totalWeight := 0
	for _, r := range routes {
		if r == nil || failed[r.ChannelID] || IsChannelRateLimited(r.ChannelID) || IsChannelCircuitOpen(r.ChannelID) || IsChannelModelUnhealthy(r.ChannelID, modelName) {
			continue
		}
		out = append(out, r)
		totalWeight += r.Weight
	}
	return out, totalWeight
}

func chooseWeightedImageRoute(routes []*database.ChannelModel, totalWeight int) *database.ChannelModel {
	if len(routes) == 1 || totalWeight <= 0 {
		return routes[0]
	}
	rNum := mrand.IntN(totalWeight)
	acc := 0
	for _, r := range routes {
		acc += r.Weight
		if rNum < acc {
			return r
		}
	}
	return routes[0]
}

func createImageApiLog(userID uint, token string, req imageGenerationRequest, price imagePriceResolution, billing BillingRuleResolution, channelType string, statusCode int, clientIP, path string, startTime time.Time) uint {
	apiLog := database.ApiLog{
		UserID:              userID,
		TokenName:           HashTokenForLog(token),
		ModelName:           req.Model,
		RequestedModel:      billing.RequestedModel,
		ServedModel:         billing.ServedModel,
		PromptTokens:        price.PromptTokens,
		CompletionTokens:    price.CompletionTokens,
		CachedTokens:        price.CachedTokens,
		CacheWriteTokens:    price.CacheWriteTokens,
		CacheWrite5mTokens:  price.CacheWrite5mTokens,
		CacheWrite1hTokens:  price.CacheWrite1hTokens,
		ReasoningTokens:     price.ReasoningTokens,
		Cost:                price.AmountMicroUSD,
		ChargedCost:         billing.ChargedCostMicroUSD,
		ModelWeight:         billing.ModelWeight,
		HealthMultiplier:    billing.HealthMultiplier,
		BillingRulesVersion: billing.BillingRulesVersion,
		FallbackUserOptIn:   billing.FallbackUserOptIn,
		FallbackReason:      sanitizeError(billing.FallbackReason, 160),
		UpstreamProvider:    sanitizeError(strings.ToLower(strings.TrimSpace(channelType)), 64),
		Latency:             time.Since(startTime).Milliseconds(),
		Status:              statusCode,
		IPAddress:           clientIP,
		RequestPath:         sanitizeError(path, 160),
		CreatedAt:           time.Now(),
	}
	err := database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&apiLog).Error; err != nil {
			return err
		}
		lines := imageUsageLines(apiLog.ID, req, price)
		if len(lines) == 0 {
			return nil
		}
		return tx.Create(&lines).Error
	})
	if err != nil {
		log.Printf("[IMAGE-BILLING-CRITICAL] api log/usage line create failed user=%d model=%s: %v", userID, req.Model, err)
		return 0
	}
	return apiLog.ID
}

func recordImagePendingReconcile(user *database.User, token string, req imageGenerationRequest, price imagePriceResolution, billing BillingRuleResolution, channelType string, statusCode int, clientIP, path string, startTime time.Time, reason string) uint {
	if user == nil {
		return 0
	}
	apiLogID := createImageApiLog(user.ID, token, req, price, billing, channelType, statusCode, clientIP, path, startTime)
	entry := database.BillingEntryInput{
		UserID:           user.ID,
		EntryType:        database.BillingTypeApiUsagePendingReconcile,
		BillingState:     database.BillingStatePendingReconcile,
		AmountUSD:        0,
		BalanceAfterUSD:  user.Quota,
		ModelName:        req.Model,
		TokensTotal:      imageTokenTotal(price),
		RequestID:        imageRequestID(user.ID, startTime, apiLogID),
		EstimatedCostUSD: price.AmountMicroUSD,
		RelatedType:      relatedTypeForApiLog(apiLogID),
		RelatedID:        apiLogID,
		Description:      fmt.Sprintf("[IMAGE-PENDING] %s · %s · %s 待对账（%s）", req.Model, imageUsageDescription(price), FormatChargedCostForDescription(price.AmountMicroUSD, billing.ChargedCostMicroUSD), reason),
	}
	var billErr error
	for attempt := 1; attempt <= 3; attempt++ {
		billErr = database.WriteBillingEntryNonFatal(entry)
		if billErr == nil {
			break
		}
		log.Printf("[IMAGE-BILLING-PENDING-RETRY] attempt=%d/3 user=%d model=%s: %v", attempt, user.ID, req.Model, billErr)
		if attempt < 3 {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if billErr != nil {
		log.Printf("[IMAGE-BILLING-LOST-DEBT] user=%d model=%s raw_cost_micro=%d charged_cost_micro=%d api_log_id=%d UNRECOVERABLE — manual reconcile from ApiLog required: %v",
			user.ID, req.Model, price.AmountMicroUSD, billing.ChargedCostMicroUSD, apiLogID, billErr)
	}
	return apiLogID
}

func deductImageBalanceAndLog(user *database.User, token string, req imageGenerationRequest, price imagePriceResolution, billing BillingRuleResolution, channelType string, statusCode int, clientIP, path string, startTime time.Time) (uint, int64, database.ReferralPaidSpendRewardResult) {
	var apiLogID uint
	var balanceAfter int64
	balanceConsumed := false
	var referralReward database.ReferralPaidSpendRewardResult
	referralRewardBPS, referralRewardWindowSeconds := readReferralPaidSpendRewardConfig()
	txErr := database.DB.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&database.User{}).
			Where("id = ? AND quota >= ?", user.ID, price.AmountMicroUSD).
			UpdateColumn("quota", gorm.Expr("quota - ?", price.AmountMicroUSD))
		if res.Error != nil {
			return fmt.Errorf("quota deduct: %w", res.Error)
		}
		apiLog := database.ApiLog{
			UserID:              user.ID,
			TokenName:           HashTokenForLog(token),
			ModelName:           req.Model,
			RequestedModel:      billing.RequestedModel,
			ServedModel:         billing.ServedModel,
			PromptTokens:        price.PromptTokens,
			CompletionTokens:    price.CompletionTokens,
			CachedTokens:        price.CachedTokens,
			CacheWriteTokens:    price.CacheWriteTokens,
			CacheWrite5mTokens:  price.CacheWrite5mTokens,
			CacheWrite1hTokens:  price.CacheWrite1hTokens,
			ReasoningTokens:     price.ReasoningTokens,
			Cost:                price.AmountMicroUSD,
			ChargedCost:         billing.ChargedCostMicroUSD,
			ModelWeight:         billing.ModelWeight,
			HealthMultiplier:    billing.HealthMultiplier,
			BillingRulesVersion: billing.BillingRulesVersion,
			FallbackUserOptIn:   billing.FallbackUserOptIn,
			FallbackReason:      sanitizeError(billing.FallbackReason, 160),
			UpstreamProvider:    sanitizeError(strings.ToLower(strings.TrimSpace(channelType)), 64),
			Latency:             time.Since(startTime).Milliseconds(),
			Status:              statusCode,
			IPAddress:           clientIP,
			RequestPath:         sanitizeError(path, 160),
			CreatedAt:           time.Now(),
		}
		if err := tx.Create(&apiLog).Error; err != nil {
			return fmt.Errorf("create api log: %w", err)
		}
		apiLogID = apiLog.ID
		lines := imageUsageLines(apiLogID, req, price)
		if len(lines) > 0 {
			if err := tx.Create(&lines).Error; err != nil {
				return fmt.Errorf("create usage line: %w", err)
			}
		}
		if res.RowsAffected == 0 {
			var current database.User
			if err := tx.Select("id, quota").First(&current, user.ID).Error; err != nil {
				return fmt.Errorf("user row missing: %w", err)
			}
			balanceAfter = current.Quota
			return database.WriteBillingEntry(tx, database.BillingEntryInput{
				UserID:           user.ID,
				EntryType:        database.BillingTypeApiUsagePendingReconcile,
				BillingState:     database.BillingStatePendingReconcile,
				AmountUSD:        0,
				BalanceAfterUSD:  balanceAfter,
				ModelName:        req.Model,
				TokensTotal:      imageTokenTotal(price),
				RequestID:        imageRequestID(user.ID, startTime, apiLogID),
				EstimatedCostUSD: price.AmountMicroUSD,
				RelatedType:      "api_log",
				RelatedID:        apiLogID,
				Description:      fmt.Sprintf("[IMAGE-INSUFFICIENT-BALANCE] %s · %s · 余额不足，已交付服务待对账（按 raw 上游成本计 $%s）", req.Model, imageUsageDescription(price), database.FormatMicroUSD(price.AmountMicroUSD)),
			})
		}
		if !TryConsumeBalanceTx(tx, user.ID, price.AmountMicroUSD, true) {
			log.Printf("[IMAGE-BILLING-WINDOW-TRACK-FAIL] user=%d model=%s raw_cost_micro=%d", user.ID, req.Model, price.AmountMicroUSD)
		}
		var fresh database.User
		if err := tx.Select("id, quota").First(&fresh, user.ID).Error; err != nil {
			return fmt.Errorf("re-select quota: %w", err)
		}
		balanceAfter = fresh.Quota
		if err := database.WriteBillingEntry(tx, database.BillingEntryInput{
			UserID:          user.ID,
			EntryType:       database.BillingTypeApiConsumeBalance,
			AmountUSD:       -price.AmountMicroUSD,
			BalanceAfterUSD: balanceAfter,
			ModelName:       req.Model,
			TokensTotal:     imageTokenTotal(price),
			RelatedType:     "api_log",
			RelatedID:       apiLogID,
			Description:     fmt.Sprintf("余额扣费 · %s · %s · %s", req.Model, imageUsageDescription(price), FormatChargedCostForDescription(price.AmountMicroUSD, billing.ChargedCostMicroUSD)),
		}); err != nil {
			return fmt.Errorf("write billing: %w", err)
		}
		reward, err := database.ApplyReferralPaidSpendRewardTx(
			tx,
			user.ID,
			price.AmountMicroUSD,
			referralRewardBPS,
			referralRewardWindowSeconds,
			time.Now(),
			"api_log",
			apiLogID,
			fmt.Sprintf("图片生成 · %s", req.Model),
		)
		if err != nil {
			return fmt.Errorf("apply referral spend reward: %w", err)
		}
		referralReward = reward
		balanceConsumed = true
		return nil
	})
	if txErr != nil {
		log.Printf("[IMAGE-BILLING-CRITICAL] user=%d model=%s balance tx failed: %v", user.ID, req.Model, txErr)
		apiLogID = recordImagePendingReconcile(user, token, req, price, billing, channelType, statusCode, clientIP, path, startTime, "balance transaction failed")
		return apiLogID, 0, database.ReferralPaidSpendRewardResult{}
	}
	RefreshUserAuth(user.ID)
	effectiveRevenue := int64(0)
	if balanceConsumed {
		effectiveRevenue = price.AmountMicroUSD
	}
	if apiLogID != 0 && balanceConsumed {
		RecordApiLogRevenue(apiLogID, database.RevenueSourceBalance, price.AmountMicroUSD, 0)
	}
	_ = balanceAfter
	return apiLogID, effectiveRevenue, referralReward
}

func imageUsageMetadata(req imageGenerationRequest, price imagePriceResolution) string {
	b, _ := json.Marshal(map[string]any{
		"n":                                      req.N,
		"resolution":                             req.Resolution,
		"aspect_ratio":                           req.AspectRatio,
		"size":                                   req.Size,
		"quality":                                req.Quality,
		"output_format":                          req.OutputFormat,
		"background":                             req.Background,
		"moderation":                             req.Moderation,
		"response_format":                        req.ResponseFormat,
		"billing_mode":                           price.BillingMode,
		"billed_quantity":                        price.Quantity,
		"response_images":                        price.ResponseImages,
		"cost_source":                            price.CostSource,
		"cost_ticks":                             price.CostTicks,
		"prompt_tokens":                          price.PromptTokens,
		"completion_tokens":                      price.CompletionTokens,
		"cached_tokens":                          price.CachedTokens,
		"cache_write_tokens":                     price.CacheWriteTokens,
		"cache_write_5m_tokens":                  price.CacheWrite5mTokens,
		"cache_write_1h_tokens":                  price.CacheWrite1hTokens,
		"reasoning_tokens":                       price.ReasoningTokens,
		"input_price_pico_per_token":             price.InputPricePico,
		"output_price_pico_per_token":            price.OutputPricePico,
		"cached_input_price_pico_per_token":      price.CachedInputPricePico,
		"cache_write_input_price_pico_per_token": price.CacheWriteInputPricePico,
		"cache_write_1h_input_price_pico_per_token": price.CacheWrite1hInputPricePico,
	})
	return string(b)
}

func imageUsageLines(apiLogID uint, req imageGenerationRequest, price imagePriceResolution) []database.ApiLogUsageLine {
	if price.BillingMode == database.BillingModeToken {
		return []database.ApiLogUsageLine{{
			ApiLogID:       apiLogID,
			ModelName:      req.Model,
			RequestPath:    database.EndpointImagesGenerations,
			Unit:           "token",
			Direction:      "total",
			Quantity:       int64(imageTokenTotal(price)),
			AmountMicroUSD: price.AmountMicroUSD,
			CostSource:     price.CostSource,
			Quality:        price.Quality,
			Size:           price.Size,
			MetadataJSON:   imageUsageMetadata(req, price),
			CreatedAt:      time.Now(),
		}}
	}
	return []database.ApiLogUsageLine{{
		ApiLogID:       apiLogID,
		ModelName:      req.Model,
		RequestPath:    database.EndpointImagesGenerations,
		Unit:           "image",
		Direction:      "output",
		Quantity:       price.Quantity,
		UnitPriceMicro: price.UnitPriceMicro,
		AmountMicroUSD: price.AmountMicroUSD,
		PricingRuleID:  price.RuleID,
		CostSource:     price.CostSource,
		Quality:        price.Quality,
		Size:           price.Size,
		Resolution:     price.Resolution,
		AspectRatio:    price.AspectRatio,
		MetadataJSON:   imageUsageMetadata(req, price),
		CreatedAt:      time.Now(),
	}}
}

func imageTokenTotal(price imagePriceResolution) int {
	if price.BillingMode == database.BillingModeToken {
		return price.PromptTokens + price.CompletionTokens
	}
	return 0
}

func imageDecisionInputUnits(price imagePriceResolution) int {
	if price.BillingMode == database.BillingModeToken {
		return price.PromptTokens
	}
	return 0
}

func imageDecisionOutputUnits(price imagePriceResolution) int {
	if price.BillingMode == database.BillingModeToken {
		return price.CompletionTokens
	}
	return int(price.Quantity)
}

func imageUsageDescription(price imagePriceResolution) string {
	if price.BillingMode == database.BillingModeToken {
		return fmt.Sprintf("%d tokens", imageTokenTotal(price))
	}
	return fmt.Sprintf("%d images", price.Quantity)
}

func selectedChannelTypeForImage(ch *database.Channel) string {
	if ch == nil {
		return ""
	}
	return ch.Type
}

func relatedTypeForApiLog(apiLogID uint) string {
	if apiLogID == 0 {
		return ""
	}
	return "api_log"
}

func imageRequestID(userID uint, startTime time.Time, apiLogID uint) string {
	if apiLogID > 0 {
		return fmt.Sprintf("api_log:%d", apiLogID)
	}
	return fmt.Sprintf("local:%d:%d", userID, startTime.UnixNano())
}

func incrementSubTokenUsedQuota(token string, subToken *database.AccessToken, amount int64) {
	if subToken == nil || amount <= 0 {
		return
	}
	res := database.DB.Model(&database.AccessToken{}).
		Where("id = ?", subToken.ID).
		UpdateColumn("used_quota", gorm.Expr("used_quota + ?", amount))
	if res.Error != nil {
		log.Printf("[SUB-TOKEN-CRITICAL] token_id=%d effective_revenue_micro=%d UsedQuota-UPDATE-FAILED: %v", subToken.ID, amount, res.Error)
		return
	}
	authSnapshotMutex.Lock()
	if existing, ok := AuthTokenCache[token]; ok {
		updated := *existing
		updated.UsedQuota += amount
		AuthTokenCache[token] = &updated
	}
	authSnapshotMutex.Unlock()
}

func copyImageResponseHeaders(c *fiber.Ctx, h http.Header) {
	for _, k := range []string{"Content-Type", "Cache-Control", "Pragma", "Expires", "Openai-Version", "X-Request-Id"} {
		if v := h.Get(k); v != "" {
			c.Set(k, v)
		}
	}
	if h.Get("Content-Type") == "" {
		c.Set("Content-Type", "application/json")
	}
}

func firstNonEmptyLocal(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
