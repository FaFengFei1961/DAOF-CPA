package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"daof-cpa/database"

	"github.com/gofiber/fiber/v2"

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

	// edit-only 字段，仅 /v1/images/edits 路径使用；/v1/images/generations 在
	// parseImageGenerationRequest 显式拒绝以避免误用。
	Image         json.RawMessage  `json:"image,omitempty"`
	Images        []imageReference `json:"images,omitempty"`
	Mask          *imageReference  `json:"mask,omitempty"`
	InputFidelity string           `json:"input_fidelity,omitempty"`

	// 计算字段（不序列化）：parse 阶段从 Images/Mask 提取出来供审计 + 计费用
	InputImageCount int    `json:"-"`
	MaskImageURL    string `json:"-"`
	// Endpoint 记录当前请求路径（/v1/images/generations 或 /v1/images/edits）。
	// 由 processImageRequest 在解析后写入，供 imageUsageLines / 审计字段使用。
	Endpoint string `json:"-"`
}

// imageReference 表示一张引用图（OpenAI images.edits 输入图 / mask）。
// 严格只支持 image_url（data URL 或 http/https URL）；file_id 在解析阶段被拒绝，
// 避免上游用 DAOF 当作 fetch oracle（SSRF 风险）以及避免引入跨用户 file_id 重放。
type imageReference struct {
	ImageURL string `json:"image_url,omitempty"`
	FileID   string `json:"file_id,omitempty"`
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

// imageRequestParser 抽象不同端点的请求解析（generations 直接解 JSON / edits 还要
// 处理 multipart）。返回值与 parseImageGenerationRequest 一致：sanitized JSON body 用于
// 转发上游。
type imageRequestParser func(c *fiber.Ctx) (imageGenerationRequest, []byte, error)

// parseImageGenerationRequestFromCtx 将 fiber 请求体读入并交给 parseImageGenerationRequest。
// /v1/images/generations 端点用。
func parseImageGenerationRequestFromCtx(c *fiber.Ctx) (imageGenerationRequest, []byte, error) {
	rawBody := c.Body()
	body := make([]byte, len(rawBody))
	copy(body, rawBody)
	return parseImageGenerationRequest(body)
}

// ImageGenerationProxyHandler exposes the OpenAI-compatible text-to-image
// endpoint. Edit + multipart 路径走 ImageEditProxyHandler。
func ImageGenerationProxyHandler(c *fiber.Ctx) error {
	return processImageRequest(c, database.EndpointImagesGenerations, parseImageGenerationRequestFromCtx)
}

// processImageRequest 是 /v1/images/generations 和 /v1/images/edits 共用的核心处理流程，
// 通过 endpoint + parseFn 参数化区分。所有上游调用、计费、流式、pending reconcile 均共用。
func processImageRequest(c *fiber.Ctx, endpoint string, parseFn imageRequestParser) error {
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
	isStream := req.Stream != nil && *req.Stream

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
	routes = filterImageRoutes(routes, endpoint)
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
		if !isStream {
			// 非流式：handler 全程持锁，return 时释放。
			// 流式时不在这里 defer，所有权移交给 handleStreamingImageResponse 在
			// SetBodyStreamWriter callback 末尾释放——否则锁会在 callback 异步执行前
			// 就被 handler return 的 defer 释放掉，失去并发保护。
			defer unlockBalance()
		}
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

	upstream, upstreamErr := callImageUpstream(c, req.Model, body, routes, channelMapRef, isStream, endpoint)
	if upstreamErr != nil {
		if isStream && unlockBalance != nil {
			unlockBalance()
		}
		recordProxyApiLog(user.ID, token, req.Model, upstreamErr.status, clientIP, startTime, path, upstreamErr.errorType, upstreamErr.message)
		c.Set("Content-Type", "application/json")
		return c.Status(upstreamErr.status).Send(upstreamErr.body)
	}

	if isStream {
		return handleStreamingImageResponse(c, user, token, subToken, isSubToken, req, body, upstream, prePrice, fallbackUserOptIn, clientIP, path, startTime, unlockBalance)
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
		// 提前 return 分支：只需落 pending reconcile 日志的副作用，不用其返回的 apiLogID。
		recordImagePendingReconcile(user, token, req, actualPrice, billingResolution, selectedChannelType, statusCode, clientIP, path, startTime, "subscription commit failed")
		copyImageResponseHeaders(c, upstream.resp.Header)
		setModelAuditHeaders(c, req.Model, req.Model, fallbackUserOptIn, "")
		return c.Status(statusCode).Send(bodyCopy)
	}
	commitOK := commitDecision.Allowed && !commitDecision.FallbackToBalance
	if !commitOK && !user.BalanceConsumeEnabled {
		recordImagePendingReconcile(user, token, req, actualPrice, billingResolution, selectedChannelType, statusCode, clientIP, path, startTime, "subscription commit fell back to disabled balance")
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

// 参数以支持 /v1/images/generations 和 /v1/images/edits 共用同一 route cache。
func filterImageRoutes(routes []*database.ChannelModel, endpoint string) []*database.ChannelModel {
	out := make([]*database.ChannelModel, 0, len(routes))
	for _, r := range routes {
		if r == nil {
			continue
		}
		database.NormalizeChannelModelMetadata(r)
		if r.ModelCategory != database.ModelCategoryImage {
			continue
		}
		if !database.ChannelModelAllowsEndpoint(r, endpoint) {
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



// 注意：fasthttp SetBodyStreamWriter 是注册 callback，handler return 后才异步执行。
// 余额锁的所有权必须移交给 callback，主 handler 不得 defer 提前释放。
