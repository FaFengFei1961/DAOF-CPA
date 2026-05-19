package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"daof-cpa/database"

	"github.com/gofiber/fiber/v2"
	"github.com/tidwall/gjson"
	"gorm.io/gorm"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

const (
	maxVideoPromptBytes     = 64 * 1024
	defaultVideoDuration    = int64(4)
	defaultVideoSize        = "1280x720"
	defaultVideoResolution  = "480p"
	defaultVideoAspectRatio = "16:9"
)

type videoGenerationRequest struct {
	Model       string          `json:"model"`
	Prompt      string          `json:"prompt,omitempty"`
	Seconds     json.RawMessage `json:"seconds,omitempty"`
	Duration    json.RawMessage `json:"duration,omitempty"`
	Size        string          `json:"size,omitempty"`
	AspectRatio string          `json:"aspect_ratio,omitempty"`
	Resolution  string          `json:"resolution,omitempty"`
	Stream      *bool           `json:"stream,omitempty"`

	// edit/extension-only 字段；generations 路径在 parseVideoGenerationRequest 显式拒绝
	// 这些键名出现。Video 是要编辑/扩展的源视频；Image / ReferenceImages 是图生视频引用。
	Video           json.RawMessage `json:"video,omitempty"`
	Image           json.RawMessage `json:"image,omitempty"`
	ImageURL        string          `json:"image_url,omitempty"`
	ReferenceImages json.RawMessage `json:"reference_images,omitempty"`
	InputReference  json.RawMessage `json:"input_reference,omitempty"`
	RequestID       string          `json:"request_id,omitempty"`
	ExtendSeconds   json.RawMessage `json:"extend_seconds,omitempty"`

	DurationSeconds int64 `json:"-"`
	// Endpoint 记录当前请求路径，由 processVideoRequest 在 parse 后写入。
	Endpoint string `json:"-"`
}

type videoPriceResolution struct {
	RuleID         uint
	UnitPriceMicro int64
	Quantity       int64
	AmountMicroUSD int64
	CostTicks      int64
	Resolution     string
	Size           string
	AspectRatio    string
	CostSource     string
}

// VideoGenerationProxyHandler opens the smallest auditable video surface:
// text-to-video only, xAI grok-imagine-video only, billed by output seconds.
// Image references, edits, extensions, and uploads stay closed until their
// extra metering facts are represented in ApiLogUsageLine. Retrieve polling is
// opened separately as a user-bound auxiliary endpoint.
// videoRequestParser 抽象不同视频端点的请求解析（generations 拒绝 input 媒体 /
// edits / extensions 接 input 视频或图片）。返回值与 parseVideoGenerationRequest 一致。
type videoRequestParser func(c *fiber.Ctx) (videoGenerationRequest, []byte, error)

// parseVideoGenerationRequestFromCtx 将 fiber 请求体读入并交给 parseVideoGenerationRequest。
func parseVideoGenerationRequestFromCtx(c *fiber.Ctx) (videoGenerationRequest, []byte, error) {
	rawBody := c.Body()
	body := make([]byte, len(rawBody))
	copy(body, rawBody)
	return parseVideoGenerationRequest(body)
}

// VideoGenerationProxyHandler 处理 POST /v1/videos/generations。
func VideoGenerationProxyHandler(c *fiber.Ctx) error {
	return processVideoRequest(c, database.EndpointVideosGenerations, parseVideoGenerationRequestFromCtx)
}

// processVideoRequest 是 /v1/videos/generations、/v1/videos/edits、
// /v1/videos/extensions 共用的核心处理流程，通过 endpoint + parseFn 参数化区分。
func processVideoRequest(c *fiber.Ctx, endpoint string, parseFn videoRequestParser) error {
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

	req, sanitizedBody, parseErr := parseFn(c)
	if parseErr != nil {
		recordProxyApiLog(user.ID, token, "unknown", 400, clientIP, startTime, path, "invalid_request", parseErr.Error())
		return c.Status(400).JSON(fiber.Map{"error": fiber.Map{"message": parseErr.Error(), "type": "invalid_request"}})
	}
	req.Endpoint = endpoint
	body := sanitizedBody

	if _, ok := database.CanonicalRuntimeVideoModel(req.Model); !ok {
		recordProxyApiLog(user.ID, token, req.Model, 400, clientIP, startTime, path, "unsupported_model", "video model is not enabled for runtime")
		return c.Status(400).JSON(fiber.Map{"error": fiber.Map{
			"message":      "video model is not enabled for runtime",
			"type":         "unsupported_model",
			"message_code": "ERR_VIDEO_MODEL_UNSUPPORTED",
		}})
	}

	gatewayMutex.RLock()
	routes := append([]*database.ChannelModel(nil), RouteCache[req.Model]...)
	channelMapRef := ChannelMapCache
	gatewayMutex.RUnlock()
	routes = filterVideoRoutes(routes, endpoint)
	if len(routes) == 0 {
		recordProxyApiLog(user.ID, token, req.Model, 404, clientIP, startTime, path, "model_not_found", "Video model not available via any channel")
		return c.Status(404).JSON(fiber.Map{"error": fiber.Map{"message": "Video model not available via any channel", "type": "model_not_found"}})
	}

	prePrice, priceErr := resolveVideoPrice(req, 0)
	if priceErr != nil {
		recordProxyApiLog(user.ID, token, req.Model, 400, clientIP, startTime, path, "pricing_unavailable", priceErr.Error())
		return c.Status(400).JSON(fiber.Map{"error": fiber.Map{"message": priceErr.Error(), "type": "pricing_unavailable"}})
	}
	precheckBilling := ResolveBillingRules(req.Model, body, 0, "", fallbackUserOptIn).WithCosts(prePrice.AmountMicroUSD)
	engineDecision := Decide(EngineRequest{
		UserID:       user.ID,
		ModelName:    req.Model,
		OutputTokens: int(prePrice.Quantity),
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
			recordProxyApiLogWithPrecheck(user.ID, token, req.Model, 402, clientIP, startTime, path, "request_estimate_exceeds_window_remaining", msg, 0, int(prePrice.Quantity), precheckBilling, engineDecision)
			return c.Status(402).JSON(precheckLimitErrorPayload(msg, engineDecision, 0, int(prePrice.Quantity), precheckBilling))
		}
		recordProxyApiLog(user.ID, token, req.Model, 402, clientIP, startTime, path, "subscription_required", msg)
		return c.Status(402).JSON(fiber.Map{"error": fiber.Map{"message": msg, "type": "subscription_required"}})
	}

	if engineDecision.FallbackToBalance {
		if !user.BalanceConsumeEnabled {
			recordProxyApiLog(user.ID, token, req.Model, 402, clientIP, startTime, path, "subscription_required", "subscription quota unavailable and balance consume disabled")
			return c.Status(402).JSON(fiber.Map{"error": fiber.Map{
				"message":      "当前请求无法使用订阅额度。请购买套餐，或在「账号设置 → 余额消费控制」中开启余额消费。",
				"type":         "subscription_required",
				"message_code": "ERR_QUOTA_EXHAUSTED_BALANCE_DISABLED",
			}})
		}
		unlockBalance := lockImageBalance(user.ID)
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

	upstream, upstreamErr := callVideoUpstream(c, req.Model, body, routes, channelMapRef, endpoint)
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
		log.Printf("[VIDEO-UPSTREAM-ERR] channel=%d status=%d body=%s", upstream.route.ChannelID, statusCode, sanitizeError(truncForLog(bodyCopy, 1024), 1024))
		recordProxyApiLog(user.ID, token, req.Model, statusCode, clientIP, startTime, path, "upstream_error", string(bodyCopy))
		c.Set("Content-Type", "application/json")
		return c.Status(statusCode).JSON(fiber.Map{"error": fiber.Map{
			"message": fmt.Sprintf("upstream returned %d", statusCode),
			"type":    "upstream_error",
		}})
	}

	actualPrice, priceErr := resolveVideoPrice(req, costTicksFromMediaResponse(bodyCopy))
	if priceErr != nil {
		log.Printf("[VIDEO-BILLING-CRITICAL] user=%d model=%s price resolve after delivery failed: %v", user.ID, req.Model, priceErr)
		recordProxyApiLog(user.ID, token, req.Model, 502, clientIP, startTime, path, "pricing_unavailable", priceErr.Error())
		return c.Status(502).JSON(fiber.Map{"error": fiber.Map{"message": "video pricing unavailable", "type": "pricing_unavailable"}})
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
		OutputTokens: int(actualPrice.Quantity),
		CostMicroUSD: chargedCostMicroUSD,
		IsPrecheck:   false,
	})
	if commitDecision.NeedsRetry {
		apiLogID = recordVideoPendingReconcile(user, token, req, actualPrice, billingResolution, selectedChannelType, statusCode, clientIP, path, startTime, "subscription commit failed")
		recordVideoGenerationJob(user.ID, upstream.route.ChannelID, req.Model, path, bodyCopy, apiLogID)
		copyImageResponseHeaders(c, upstream.resp.Header)
		setModelAuditHeaders(c, req.Model, req.Model, fallbackUserOptIn, "")
		return c.Status(statusCode).Send(bodyCopy)
	}
	commitOK := commitDecision.Allowed && !commitDecision.FallbackToBalance
	if !commitOK && !user.BalanceConsumeEnabled {
		apiLogID = recordVideoPendingReconcile(user, token, req, actualPrice, billingResolution, selectedChannelType, statusCode, clientIP, path, startTime, "subscription commit fell back to disabled balance")
		recordVideoGenerationJob(user.ID, upstream.route.ChannelID, req.Model, path, bodyCopy, apiLogID)
		copyImageResponseHeaders(c, upstream.resp.Header)
		setModelAuditHeaders(c, req.Model, req.Model, fallbackUserOptIn, "")
		return c.Status(statusCode).Send(bodyCopy)
	}

	var effectiveRevenueMicroUSD int64
	var referralReward database.ReferralPaidSpendRewardResult
	if commitOK {
		apiLogID = createVideoApiLog(user.ID, token, req, actualPrice, billingResolution, selectedChannelType, statusCode, clientIP, path, startTime)
		subID := commitDecision.SubscriptionID
		if billErr := database.WriteBillingEntryNonFatal(database.BillingEntryInput{
			UserID:               user.ID,
			EntryType:            database.BillingTypeApiUsageSub,
			AmountUSD:            0,
			BalanceAfterUSD:      user.Quota,
			ModelName:            req.Model,
			TokensTotal:          int(actualPrice.Quantity),
			SourceSubscriptionID: &subID,
			RelatedType:          relatedTypeForApiLog(apiLogID),
			RelatedID:            apiLogID,
			Description:          fmt.Sprintf("套餐 · %s · %d 秒视频 · %s", req.Model, actualPrice.Quantity, FormatChargedCostForDescription(actualPrice.AmountMicroUSD, chargedCostMicroUSD)),
		}); billErr != nil {
			log.Printf("[VIDEO-BILLING-AUDIT-FAIL] user=%d sub=%d model=%s: %v", user.ID, subID, req.Model, billErr)
		}
		effectiveRevenueMicroUSD = subscriptionRevenueMicroUSD(chargedCostMicroUSD, commitDecision.SubscriptionIsGranted)
		if apiLogID != 0 {
			RecordApiLogRevenue(apiLogID, database.RevenueSourceSubscription, effectiveRevenueMicroUSD, subID)
		}
	} else {
		apiLogID, effectiveRevenueMicroUSD, referralReward = deductVideoBalanceAndLog(user, token, req, actualPrice, billingResolution, selectedChannelType, statusCode, clientIP, path, startTime)
	}

	if isSubToken && effectiveRevenueMicroUSD > 0 {
		incrementSubTokenUsedQuota(token, subToken, effectiveRevenueMicroUSD)
	}
	if referralReward.ReferrerID != 0 && referralReward.RewardMicroUSD > 0 {
		RefreshUserAuth(referralReward.ReferrerID)
	}
	if apiLogID == 0 {
		log.Printf("[VIDEO-BILLING-CRITICAL] user=%d model=%s served but api_log missing", user.ID, req.Model)
	}
	recordVideoGenerationJob(user.ID, upstream.route.ChannelID, req.Model, path, bodyCopy, apiLogID)

	copyImageResponseHeaders(c, upstream.resp.Header)
	setModelAuditHeaders(c, req.Model, req.Model, fallbackUserOptIn, "")
	return c.Status(statusCode).Send(bodyCopy)
}

// VideoRetrieveProxyHandler is a free auxiliary endpoint for async video jobs.
// It only retrieves request ids created through this platform and always routes
// back to the original CLIProxyAPI channel, so request ids cannot be used to
// probe or drain upstream credentials.
func VideoRetrieveProxyHandler(c *fiber.Ctx) error {
	startTime := time.Now()
	clientIP := c.IP()
	path := strings.Clone(c.Path())
	requestID := strings.TrimSpace(c.Params("request_id"))
	if !validVideoRequestID(requestID) {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"error": fiber.Map{"message": "invalid video request_id", "type": "invalid_request"}})
	}

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
	}

	var job database.MediaGenerationJob
	if err := database.DB.Where("request_id = ?", requestID).First(&job).Error; err != nil || job.UserID != user.ID {
		recordProxyApiLog(user.ID, token, "grok-imagine-video", 404, clientIP, startTime, path, "video_not_found", "video request was not created by this account")
		return c.Status(http.StatusNotFound).JSON(fiber.Map{"error": fiber.Map{"message": "video request not found", "type": "video_not_found"}})
	}
	if job.ModelName == "" {
		job.ModelName = "grok-imagine-video"
	}

	var ch database.Channel
	if err := database.DB.First(&ch, job.ChannelID).Error; err != nil || ch.Status != 1 {
		recordProxyApiLog(user.ID, token, job.ModelName, 503, clientIP, startTime, path, "channel_unavailable", "original video channel is unavailable")
		return c.Status(http.StatusServiceUnavailable).JSON(fiber.Map{"error": fiber.Map{"message": "original video channel is unavailable", "type": "channel_unavailable"}})
	}
	if NormalizeChannelType(ch.Type) != ChannelTypeCLIProxy {
		recordProxyApiLog(user.ID, token, job.ModelName, 502, clientIP, startTime, path, "channel_misconfigured", "video retrieval is only supported through CLIProxyAPI channels")
		return c.Status(http.StatusBadGateway).JSON(fiber.Map{"error": fiber.Map{"message": "video retrieval is only supported through CLIProxyAPI channels", "type": "channel_misconfigured"}})
	}

	upstreamURL := strings.TrimRight(ch.BaseURL, "/") + "/v1/videos/" + url.PathEscape(requestID)
	upstreamCtx, upstreamCancel := context.WithCancel(c.Context())
	defer upstreamCancel()
	httpReq, err := http.NewRequestWithContext(upstreamCtx, http.MethodGet, upstreamURL, nil)
	if err != nil {
		recordProxyApiLog(user.ID, token, job.ModelName, 502, clientIP, startTime, path, "bad_gateway", err.Error())
		return c.Status(http.StatusBadGateway).JSON(fiber.Map{"error": fiber.Map{"message": "bad upstream request", "type": "bad_gateway"}})
	}
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+ch.Key)
	if ch.Headers != "" {
		var customHeaders map[string]string
		if err := json.Unmarshal([]byte(ch.Headers), &customHeaders); err == nil {
			for k, v := range customHeaders {
				httpReq.Header.Set(k, v)
			}
		} else {
			log.Printf("[VIDEO-RETRIEVE] channel %d invalid Headers json: %v (raw=%q)", ch.ID, err, ch.Headers)
		}
	}

	httpClient := &http.Client{
		Transport: getTransport(ch.ProxyURL),
		Timeout:   nonStreamUpstreamTimeout(),
	}
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		MarkChannelFailure(ch.ID, 0)
		recordProxyApiLog(user.ID, token, job.ModelName, 502, clientIP, startTime, path, "bad_gateway", "upstream connection failed")
		return c.Status(http.StatusBadGateway).JSON(fiber.Map{"error": fiber.Map{"message": "upstream connection failed", "type": "bad_gateway"}})
	}
	defer resp.Body.Close()

	bodyCopy, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 500 {
		MarkChannelFailure(ch.ID, resp.StatusCode)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		setChannelRateLimitCooldown(ch.ID, parseRetryAfter(resp.Header.Get("Retry-After")))
	}
	copyImageResponseHeaders(c, resp.Header)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		recordProxyApiLog(user.ID, token, job.ModelName, resp.StatusCode, clientIP, startTime, path, "upstream_error", string(bodyCopy))
		return c.Status(resp.StatusCode).Send(bodyCopy)
	}
	recordProxyApiLog(user.ID, token, job.ModelName, resp.StatusCode, clientIP, startTime, path, "", "")
	setModelAuditHeaders(c, job.ModelName, job.ModelName, false, "")
	return c.Status(resp.StatusCode).Send(bodyCopy)
}

func parseVideoGenerationRequest(body []byte) (videoGenerationRequest, []byte, error) {
	if len(body) == 0 {
		return videoGenerationRequest{}, nil, fmt.Errorf("request body is required")
	}
	if !gjson.ValidBytes(body) {
		return videoGenerationRequest{}, nil, fmt.Errorf("request body must be valid JSON")
	}
	for _, field := range []string{
		"image", "images", "image_url", "image_urls", "input_image", "input_images",
		"input_reference", "reference_image", "reference_images", "reference_image_urls",
		"file_id", "mask", "video", "videos",
	} {
		if gjson.GetBytes(body, field).Exists() {
			return videoGenerationRequest{}, nil, fmt.Errorf("%s is not supported on /v1/videos/generations", field)
		}
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var req videoGenerationRequest
	if err := dec.Decode(&req); err != nil {
		return videoGenerationRequest{}, nil, fmt.Errorf("unsupported video request field or invalid body: %w", err)
	}
	canonicalModel, ok := database.CanonicalRuntimeVideoModel(req.Model)
	req.Model = strings.TrimSpace(req.Model)
	req.Prompt = strings.TrimSpace(req.Prompt)
	req.Size = strings.TrimSpace(req.Size)
	req.AspectRatio = strings.TrimSpace(req.AspectRatio)
	req.Resolution = strings.TrimSpace(req.Resolution)
	if req.Model == "" {
		return videoGenerationRequest{}, nil, fmt.Errorf("model is required")
	}
	if ok {
		req.Model = canonicalModel
	}
	if req.Prompt == "" {
		return videoGenerationRequest{}, nil, fmt.Errorf("prompt is required")
	}
	if len([]byte(req.Prompt)) > maxVideoPromptBytes {
		return videoGenerationRequest{}, nil, fmt.Errorf("prompt is too large")
	}
	if req.Stream != nil && *req.Stream {
		return videoGenerationRequest{}, nil, fmt.Errorf("streaming video generation is not supported")
	}
	duration, err := normalizeVideoDuration(req.Seconds, req.Duration)
	if err != nil {
		return videoGenerationRequest{}, nil, err
	}
	req.DurationSeconds = duration

	size, aspectRatio, resolution, err := normalizeVideoSizeOptions(req.Size)
	if err != nil {
		return videoGenerationRequest{}, nil, err
	}
	if req.AspectRatio != "" {
		aspectRatio, err = normalizeVideoAspectRatio(req.AspectRatio)
		if err != nil {
			return videoGenerationRequest{}, nil, err
		}
	}
	if req.Resolution != "" {
		resolution, err = normalizeVideoResolution(req.Resolution)
		if err != nil {
			return videoGenerationRequest{}, nil, err
		}
	}
	req.Size = size
	req.AspectRatio = aspectRatio
	req.Resolution = resolution

	payload := map[string]any{
		"model":        req.Model,
		"prompt":       req.Prompt,
		"duration":     req.DurationSeconds,
		"aspect_ratio": req.AspectRatio,
		"resolution":   req.Resolution,
	}
	sanitized, err := json.Marshal(payload)
	if err != nil {
		return videoGenerationRequest{}, nil, fmt.Errorf("build sanitized request: %w", err)
	}
	return req, sanitized, nil
}

func normalizeVideoDuration(secondsRaw, durationRaw json.RawMessage) (int64, error) {
	seconds, hasSeconds, err := parseVideoInteger(secondsRaw, "seconds")
	if err != nil {
		return 0, err
	}
	duration, hasDuration, err := parseVideoInteger(durationRaw, "duration")
	if err != nil {
		return 0, err
	}
	if hasSeconds && hasDuration && seconds != duration {
		return 0, fmt.Errorf("seconds and duration conflict")
	}
	value := defaultVideoDuration
	if hasSeconds {
		value = seconds
	} else if hasDuration {
		value = duration
	}
	if value < 1 {
		value = 1
	}
	if value > 15 {
		value = 15
	}
	return value, nil
}

func parseVideoInteger(raw json.RawMessage, field string) (int64, bool, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return 0, false, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return 0, false, fmt.Errorf("%s must be an integer", field)
	}
	switch x := v.(type) {
	case json.Number:
		n, err := x.Int64()
		if err != nil {
			return 0, false, fmt.Errorf("%s must be an integer", field)
		}
		return n, true, nil
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		if err != nil {
			return 0, false, fmt.Errorf("%s must be an integer", field)
		}
		return n, true, nil
	default:
		return 0, false, fmt.Errorf("%s must be an integer", field)
	}
}

func normalizeVideoSizeOptions(raw string) (size string, aspectRatio string, resolution string, err error) {
	size = strings.TrimSpace(raw)
	if size == "" {
		return defaultVideoSize, defaultVideoAspectRatio, defaultVideoResolution, nil
	}
	switch strings.ToLower(size) {
	case "720x1280":
		return size, "9:16", "720p", nil
	case "1280x720":
		return size, "16:9", "720p", nil
	default:
		return "", "", "", fmt.Errorf("size must be 720x1280 or 1280x720")
	}
}

func normalizeVideoAspectRatio(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1:1", "square":
		return "1:1", nil
	case "16:9", "landscape":
		return "16:9", nil
	case "9:16", "portrait":
		return "9:16", nil
	case "4:3":
		return "4:3", nil
	case "3:4":
		return "3:4", nil
	case "3:2":
		return "3:2", nil
	case "2:3":
		return "2:3", nil
	default:
		return "", fmt.Errorf("aspect_ratio is invalid")
	}
}

func normalizeVideoResolution(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "480p":
		return "480p", nil
	case "720p":
		return "720p", nil
	default:
		return "", fmt.Errorf("resolution must be 480p or 720p")
	}
}

// filterVideoRoutes 过滤可服务指定 endpoint 的 video route。P3 后接 endpoint 参数
// 以支持 /v1/videos/generations、/v1/videos/edits、/v1/videos/extensions 共用 route cache。
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

func callVideoUpstream(c *fiber.Ctx, modelName string, body []byte, routes []*database.ChannelModel, channelMapRef map[uint]*database.Channel, endpoint string) (*selectedImageUpstream, *upstreamImageError) {
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
			return nil, imageErr(502, "backend_exhausted", "All video upstream channels exhausted or failing")
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
			last = imageErr(502, "channel_misconfigured", "video generation is only supported through CLIProxyAPI channels")
			continue
		}
		upstreamURL := strings.TrimRight(ch.BaseURL, "/") + endpoint
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
		if key := strings.TrimSpace(c.Get("x-idempotency-key")); key != "" {
			httpReq.Header.Set("x-idempotency-key", key)
		}
		if ch.Headers != "" {
			var customHeaders map[string]string
			if err := json.Unmarshal([]byte(ch.Headers), &customHeaders); err == nil {
				for k, v := range customHeaders {
					httpReq.Header.Set(k, v)
				}
			} else {
				log.Printf("[VIDEO] channel %d invalid Headers json: %v (raw=%q)", ch.ID, err, ch.Headers)
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
			log.Printf("[VIDEO-UPSTREAM-DIAL] channel=%d err=%s", selected.ChannelID, sanitizeError(err.Error(), 256))
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
			log.Printf("[VIDEO-UPSTREAM-RATE-LIMIT] channel=%d status=%d body=%q", selected.ChannelID, resp.StatusCode, truncForLog(bodyBytes, 256))
			last = imageErr(http.StatusTooManyRequests, "upstream_rate_limited", "all upstream channels are rate limited")
		case StatusActionConfigError:
			failedChannels[selected.ChannelID] = true
			bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			upstreamCancel()
			markChannelModelUnhealthy(selected.ChannelID, modelName)
			log.Printf("[VIDEO-UPSTREAM-CONFIG] channel=%d model=%s status=%d body=%q", selected.ChannelID, modelName, resp.StatusCode, truncForLog(bodyBytes, 256))
			last = imageErr(resp.StatusCode, "channel_model_unhealthy", "upstream returned config error for configured video model")
		default:
			failedChannels[selected.ChannelID] = true
			bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			upstreamCancel()
			MarkChannelFailure(selected.ChannelID, resp.StatusCode)
			log.Printf("[VIDEO-UPSTREAM-ERR] channel=%d status=%d body=%q", selected.ChannelID, resp.StatusCode, truncForLog(bodyBytes, 256))
			last = imageErr(resp.StatusCode, "upstream_error", fmt.Sprintf("upstream returned %d (channel rotated)", resp.StatusCode))
		}
	}
	if last != nil {
		return nil, last
	}
	return nil, imageErr(502, "backend_exhausted", "All video upstream channels exhausted or failing")
}

func createVideoApiLog(userID uint, token string, req videoGenerationRequest, price videoPriceResolution, billing BillingRuleResolution, channelType string, statusCode int, clientIP, path string, startTime time.Time) uint {
	apiLog := database.ApiLog{
		UserID:              userID,
		TokenName:           HashTokenForLog(token),
		ModelName:           req.Model,
		RequestedModel:      billing.RequestedModel,
		ServedModel:         billing.ServedModel,
		PromptTokens:        0,
		CompletionTokens:    0,
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
		line := database.ApiLogUsageLine{
			ApiLogID:       apiLog.ID,
			ModelName:      req.Model,
			RequestPath:    requestPathForVideoRequest(req),
			Unit:           "video_second",
			Direction:      "output",
			Quantity:       price.Quantity,
			UnitPriceMicro: price.UnitPriceMicro,
			AmountMicroUSD: price.AmountMicroUSD,
			PricingRuleID:  price.RuleID,
			CostSource:     price.CostSource,
			Size:           price.Size,
			Resolution:     price.Resolution,
			AspectRatio:    price.AspectRatio,
			MetadataJSON:   videoUsageMetadata(req, price),
			CreatedAt:      time.Now(),
		}
		return tx.Create(&line).Error
	})
	if err != nil {
		log.Printf("[VIDEO-BILLING-CRITICAL] api log/usage line create failed user=%d model=%s: %v", userID, req.Model, err)
		return 0
	}
	return apiLog.ID
}

func recordVideoPendingReconcile(user *database.User, token string, req videoGenerationRequest, price videoPriceResolution, billing BillingRuleResolution, channelType string, statusCode int, clientIP, path string, startTime time.Time, reason string) uint {
	if user == nil {
		return 0
	}
	apiLogID := createVideoApiLog(user.ID, token, req, price, billing, channelType, statusCode, clientIP, path, startTime)
	entry := database.BillingEntryInput{
		UserID:           user.ID,
		EntryType:        database.BillingTypeApiUsagePendingReconcile,
		BillingState:     database.BillingStatePendingReconcile,
		AmountUSD:        0,
		BalanceAfterUSD:  user.Quota,
		ModelName:        req.Model,
		TokensTotal:      int(price.Quantity),
		RequestID:        videoRequestID(user.ID, startTime, apiLogID),
		EstimatedCostUSD: price.AmountMicroUSD,
		RelatedType:      relatedTypeForApiLog(apiLogID),
		RelatedID:        apiLogID,
		Description:      fmt.Sprintf("[VIDEO-PENDING] %s · %d 秒视频 · %s 待对账（%s）", req.Model, price.Quantity, FormatChargedCostForDescription(price.AmountMicroUSD, billing.ChargedCostMicroUSD), reason),
	}
	var billErr error
	for attempt := 1; attempt <= 3; attempt++ {
		billErr = database.WriteBillingEntryNonFatal(entry)
		if billErr == nil {
			break
		}
		log.Printf("[VIDEO-BILLING-PENDING-RETRY] attempt=%d/3 user=%d model=%s: %v", attempt, user.ID, req.Model, billErr)
		if attempt < 3 {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if billErr != nil {
		log.Printf("[VIDEO-BILLING-LOST-DEBT] user=%d model=%s raw_cost_micro=%d charged_cost_micro=%d api_log_id=%d UNRECOVERABLE - manual reconcile from ApiLog required: %v",
			user.ID, req.Model, price.AmountMicroUSD, billing.ChargedCostMicroUSD, apiLogID, billErr)
	}
	return apiLogID
}

func deductVideoBalanceAndLog(user *database.User, token string, req videoGenerationRequest, price videoPriceResolution, billing BillingRuleResolution, channelType string, statusCode int, clientIP, path string, startTime time.Time) (uint, int64, database.ReferralPaidSpendRewardResult) {
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
		if err := tx.Create(&database.ApiLogUsageLine{
			ApiLogID:       apiLogID,
			ModelName:      req.Model,
			RequestPath:    requestPathForVideoRequest(req),
			Unit:           "video_second",
			Direction:      "output",
			Quantity:       price.Quantity,
			UnitPriceMicro: price.UnitPriceMicro,
			AmountMicroUSD: price.AmountMicroUSD,
			PricingRuleID:  price.RuleID,
			CostSource:     price.CostSource,
			Size:           price.Size,
			Resolution:     price.Resolution,
			AspectRatio:    price.AspectRatio,
			MetadataJSON:   videoUsageMetadata(req, price),
			CreatedAt:      time.Now(),
		}).Error; err != nil {
			return fmt.Errorf("create usage line: %w", err)
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
				TokensTotal:      int(price.Quantity),
				RequestID:        videoRequestID(user.ID, startTime, apiLogID),
				EstimatedCostUSD: price.AmountMicroUSD,
				RelatedType:      "api_log",
				RelatedID:        apiLogID,
				Description:      fmt.Sprintf("[VIDEO-INSUFFICIENT-BALANCE] %s · %d 秒视频 · 余额不足，已交付服务待对账（按 raw 上游成本计 $%s）", req.Model, price.Quantity, database.FormatMicroUSD(price.AmountMicroUSD)),
			})
		}
		if !TryConsumeBalanceTx(tx, user.ID, price.AmountMicroUSD, true) {
			log.Printf("[VIDEO-BILLING-WINDOW-TRACK-FAIL] user=%d model=%s raw_cost_micro=%d", user.ID, req.Model, price.AmountMicroUSD)
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
			RelatedType:     "api_log",
			RelatedID:       apiLogID,
			Description:     fmt.Sprintf("余额扣费 · %s · %d 秒视频 · %s", req.Model, price.Quantity, FormatChargedCostForDescription(price.AmountMicroUSD, billing.ChargedCostMicroUSD)),
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
			fmt.Sprintf("视频生成 · %s", req.Model),
		)
		if err != nil {
			return fmt.Errorf("apply referral spend reward: %w", err)
		}
		referralReward = reward
		balanceConsumed = true
		return nil
	})
	if txErr != nil {
		log.Printf("[VIDEO-BILLING-CRITICAL] user=%d model=%s balance tx failed: %v", user.ID, req.Model, txErr)
		apiLogID = recordVideoPendingReconcile(user, token, req, price, billing, channelType, statusCode, clientIP, path, startTime, "balance transaction failed")
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

// requestPathForVideoRequest 返回审计 ApiLogUsageLine.RequestPath，根据 videoGenerationRequest.Endpoint
// 区分 generations/edits/extensions；Endpoint 未设置时回退到 generations。
func requestPathForVideoRequest(req videoGenerationRequest) string {
	if req.Endpoint != "" {
		return req.Endpoint
	}
	return database.EndpointVideosGenerations
}

func videoUsageMetadata(req videoGenerationRequest, price videoPriceResolution) string {
	b, _ := json.Marshal(map[string]any{
		"duration":        req.DurationSeconds,
		"size":            req.Size,
		"resolution":      req.Resolution,
		"aspect_ratio":    req.AspectRatio,
		"billed_quantity": price.Quantity,
		"cost_source":     price.CostSource,
		"cost_ticks":      price.CostTicks,
	})
	return string(b)
}

func videoRequestID(userID uint, startTime time.Time, apiLogID uint) string {
	if apiLogID > 0 {
		return fmt.Sprintf("api_log:%d", apiLogID)
	}
	return fmt.Sprintf("local:%d:%d", userID, startTime.UnixNano())
}

func recordVideoGenerationJob(userID uint, channelID uint, modelName string, requestPath string, body []byte, apiLogID uint) {
	requestID := strings.TrimSpace(gjson.GetBytes(body, "request_id").String())
	if requestID == "" {
		requestID = strings.TrimSpace(gjson.GetBytes(body, "id").String())
	}
	if !validVideoRequestID(requestID) {
		log.Printf("[VIDEO-JOB-MISSING] user=%d model=%s api_log_id=%d response omitted valid request_id", userID, modelName, apiLogID)
		return
	}
	job := database.MediaGenerationJob{
		RequestID:      requestID,
		UserID:         userID,
		ChannelID:      channelID,
		ModelName:      modelName,
		RequestPath:    sanitizeError(requestPath, 160),
		CreateApiLogID: apiLogID,
		CreatedAt:      time.Now(),
	}
	if err := database.DB.Create(&job).Error; err != nil {
		log.Printf("[VIDEO-JOB-CREATE-FAIL] user=%d model=%s request_id=%s api_log_id=%d: %v", userID, modelName, requestID, apiLogID, err)
	}
}

func validVideoRequestID(requestID string) bool {
	if requestID == "" || len(requestID) > 160 {
		return false
	}
	for _, r := range requestID {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			continue
		}
		switch r {
		case '_', '-', '.', ':':
			continue
		default:
			return false
		}
	}
	return true
}
