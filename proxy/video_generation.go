package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"daof-cpa/database"

	"github.com/gofiber/fiber/v2"

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

// 以支持 /v1/videos/generations、/v1/videos/edits、/v1/videos/extensions 共用 route cache。


