// Package proxy / gemini_native.go
//
// /v1beta/models/*action 端点——Google Gemini 兼容 API 代理（P6）。
//
// 用客户端用 Google AI SDK / @google/generative-ai 直接调 DAOF，能用到所有
// CPA 通过 generateContent / :predict 路径暴露的 Gemini text + Gemini image +
// Imagen 模型。
//
// 协议要点（与 CPA `sdk/api/handlers/gemini/gemini_handlers.go` 对齐）：
//   - URL 形如 `/v1beta/models/<model>:<method>`，其中 method ∈
//     {generateContent, streamGenerateContent, countTokens}。Imagen 内部走 :predict
//     但 CPA 自动翻译，对客户端仍透出 :generateContent。
//   - 流式默认 SSE（`data: <json>\n\n`），由 `?alt=sse` 控制（与 Google API 一致）。
//   - 响应 usageMetadata 字段携带 promptTokenCount / candidatesTokenCount /
//     cachedContentTokenCount 等 token 计数；Imagen 响应被 CPA 翻译成 Gemini 格式
//     后 usage 全部为 0（CPA 注释：Imagen API 不返回 token count）。
//
// 计费策略：
//   - ModelCategory=text → token-based（用 usageMetadata 各字段）
//   - ModelCategory=image → image-based（按 candidates[].content.parts[].inlineData
//     数量计费），Imagen 也走此路径
//
// 安全边界：
//   - 拒绝 fileData.fileUri 引用（避免上游 fetch oracle / SSRF）
//   - 文件大小通过 fiber BodyLimit 限制
//   - 流式 SetBodyStreamWriter 包 panic recover 与 P1 一致
package proxy

import (
	"bufio"
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
)

const maxGeminiPromptBytes = 256 * 1024 // Gemini 接受更长 prompt 比图像/视频

// geminiNativeRequest 是从 URL action 解析出来的运行时状态，不与上游 JSON 对应。
type geminiNativeRequest struct {
	Model    string // canonical model ID（CanonicalRuntimeGeminiModel 归一化后）
	Method   string // generateContent / streamGenerateContent / countTokens
	IsStream bool
	Alt      string // ?alt=sse 等
}

// GeminiNativeProxyHandler 处理 POST /v1beta/models/*action。
func GeminiNativeProxyHandler(c *fiber.Ctx) error {
	startTime := time.Now()
	clientIP := c.IP()
	path := strings.Clone(c.Path())
	fallbackUserOptIn := parseAllowFallbackHeader(c)

	// auth: 支持 Bearer + Google AI SDK 风格的 ?key=xxx query 参数
	token := bearerTokenFromHeader(c.Get("Authorization"))
	if token == "" {
		token = strings.TrimSpace(c.Query("key"))
	}
	if token == "" {
		token = strings.TrimSpace(c.Get("x-goog-api-key"))
	}
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

	// listModels 模式：客户端 GET /v1beta/models（无 :method action）→ 透传 CPA
	// 给 Google AI SDK 的 listModels() 调用使用。不计费、不写 ApiLogUsageLine，
	// 仅透传响应（CPA `s.geminiModelsHandler` 返回 Gemini 兼容的 {models: [...]}
	// 格式）。
	if isGeminiListModelsRequest(c) {
		return forwardGeminiListModels(c, user, token, clientIP, startTime, path)
	}

	// parse action
	geminiReq, parseErr := parseGeminiNativeAction(c)
	if parseErr != nil {
		recordProxyApiLog(user.ID, token, "unknown", 400, clientIP, startTime, path, "invalid_request", parseErr.Error())
		return c.Status(400).JSON(fiber.Map{"error": fiber.Map{"message": parseErr.Error(), "type": "invalid_request"}})
	}

	// canonical model
	canonical, ok := database.CanonicalRuntimeGeminiModel(geminiReq.Model)
	if !ok {
		recordProxyApiLog(user.ID, token, geminiReq.Model, 400, clientIP, startTime, path, "unsupported_model", "Gemini model is not enabled for runtime")
		return c.Status(400).JSON(fiber.Map{"error": fiber.Map{
			"message":      "Gemini model is not enabled for runtime",
			"type":         "unsupported_model",
			"message_code": "ERR_GEMINI_MODEL_UNSUPPORTED",
		}})
	}
	geminiReq.Model = canonical

	// RouteCache + filter Gemini-native routes（admin 必须显式开 /v1beta/models endpoint）
	gatewayMutex.RLock()
	routes := append([]*database.ChannelModel(nil), RouteCache[canonical]...)
	channelMapRef := ChannelMapCache
	gatewayMutex.RUnlock()
	routes = filterGeminiNativeRoutes(routes)
	if len(routes) == 0 {
		recordProxyApiLog(user.ID, token, canonical, 404, clientIP, startTime, path, "model_not_found", "Gemini model not available via any channel")
		return c.Status(404).JSON(fiber.Map{"error": fiber.Map{"message": "Gemini model not available via any channel", "type": "model_not_found"}})
	}

	// 读 body（generateContent / countTokens / streamGenerateContent 都用 POST body）
	rawBody := c.Body()
	body := make([]byte, len(rawBody))
	copy(body, rawBody)

	// 安全：拒绝 fileData.fileUri 引用（避免上游 fetch oracle）
	if err := rejectGeminiNativeFileURIRefs(body); err != nil {
		recordProxyApiLog(user.ID, token, canonical, 400, clientIP, startTime, path, "invalid_request", err.Error())
		return c.Status(400).JSON(fiber.Map{"error": fiber.Map{"message": err.Error(), "type": "invalid_request"}})
	}
	// prompt size sanity
	if int64(len(body)) > maxGeminiPromptBytes*8 { // body 含 inlineData 等可较大，但还是上限保护
		recordProxyApiLog(user.ID, token, canonical, 413, clientIP, startTime, path, "request_too_large", "request body too large")
		return c.Status(413).JSON(fiber.Map{"error": fiber.Map{"message": "request body too large", "type": "request_too_large"}})
	}

	// countTokens 透传——不计费（只是 metadata 查询）
	if geminiReq.Method == "countTokens" {
		return forwardGeminiCountTokens(c, user, token, canonical, body, routes, channelMapRef, clientIP, startTime, path, geminiReq.Alt)
	}

	// precheck price
	prePrice, priceErr := resolveGeminiPrecheckPrice(canonical, body, routes)
	if priceErr != nil {
		recordProxyApiLog(user.ID, token, canonical, 400, clientIP, startTime, path, "pricing_unavailable", priceErr.Error())
		return c.Status(400).JSON(fiber.Map{"error": fiber.Map{"message": priceErr.Error(), "type": "pricing_unavailable"}})
	}
	precheckBilling := ResolveBillingRules(canonical, body, 0, "", fallbackUserOptIn).WithCosts(prePrice.AmountMicroUSD)
	engineDecision := Decide(EngineRequest{
		UserID:       user.ID,
		ModelName:    canonical,
		InputTokens:  int(prePrice.PromptTokens),
		OutputTokens: int(prePrice.CompletionTokens),
		CostMicroUSD: precheckBilling.ChargedCostMicroUSD,
		IsPrecheck:   true,
	})
	if !engineDecision.Allowed {
		msg := engineDecision.BlockMessage
		if msg == "" {
			msg = "您的订阅额度已用尽，请购买套餐或充值余额"
		}
		if engineDecision.NeedsRetry {
			recordProxyApiLog(user.ID, token, canonical, 503, clientIP, startTime, path, "subscription_load_failed", msg)
			return c.Status(503).JSON(fiber.Map{"error": fiber.Map{"message": msg, "type": "service_unavailable", "code": "subscription_load_failed"}})
		}
		if engineDecision.BlockQuotaPlanID != 0 {
			msg = precheckLimitMessage(engineDecision, precheckBilling)
			recordProxyApiLogWithPrecheck(user.ID, token, canonical, 402, clientIP, startTime, path, "request_estimate_exceeds_window_remaining", msg, int(prePrice.PromptTokens), int(prePrice.CompletionTokens), precheckBilling, engineDecision)
			return c.Status(402).JSON(precheckLimitErrorPayload(msg, engineDecision, int(prePrice.PromptTokens), int(prePrice.CompletionTokens), precheckBilling))
		}
		recordProxyApiLog(user.ID, token, canonical, 402, clientIP, startTime, path, "subscription_required", msg)
		return c.Status(402).JSON(fiber.Map{"error": fiber.Map{"message": msg, "type": "subscription_required"}})
	}

	// balance lock（fallback to balance 时锁定 user，避免并发耗光）
	var unlockBalance func()
	if engineDecision.FallbackToBalance {
		if !user.BalanceConsumeEnabled {
			recordProxyApiLog(user.ID, token, canonical, 402, clientIP, startTime, path, "subscription_required", "subscription quota unavailable and balance consume disabled")
			return c.Status(402).JSON(fiber.Map{"error": fiber.Map{
				"message":      "当前请求无法使用订阅额度。请购买套餐，或在「账号设置 → 余额消费控制」中开启余额消费。",
				"type":         "subscription_required",
				"message_code": "ERR_QUOTA_EXHAUSTED_BALANCE_DISABLED",
			}})
		}
		unlockBalance = lockImageBalance(user.ID)
		if !geminiReq.IsStream {
			defer unlockBalance()
		}
		fresh, freshErr := loadFreshUserForImageBalance(user.ID)
		if freshErr != nil {
			recordProxyApiLog(user.ID, token, canonical, 503, clientIP, startTime, path, "user_load_failed", freshErr.Error())
			return c.Status(503).JSON(fiber.Map{"error": fiber.Map{"message": "用户余额状态暂时不可用，请稍后重试", "type": "service_unavailable"}})
		}
		user = fresh
		if !CheckBalanceConsumeAllowed(user, prePrice.AmountMicroUSD) {
			recordProxyApiLog(user.ID, token, canonical, 402, clientIP, startTime, path, "balance_limit_reached", "balance consume window limit reached")
			return c.Status(402).JSON(fiber.Map{"error": fiber.Map{
				"message":      "本周期余额消费已达上限，请提高限额或等待下次重置。",
				"type":         "balance_limit_reached",
				"message_code": "ERR_BALANCE_LIMIT_REACHED",
			}})
		}
		if user.Quota < prePrice.AmountMicroUSD {
			recordProxyApiLog(user.ID, token, canonical, 403, clientIP, startTime, path, "quota_exceeded", "insufficient balance")
			return c.Status(403).JSON(fiber.Map{"error": fiber.Map{
				"message":      "余额不足，请充值",
				"type":         "quota_exceeded",
				"message_code": "ERR_INSUFFICIENT_BALANCE",
			}})
		}
	}

	// upstream call
	upstream, upstreamErr := callGeminiNativeUpstream(c, canonical, geminiReq, body, routes, channelMapRef)
	if upstreamErr != nil {
		if geminiReq.IsStream && unlockBalance != nil {
			unlockBalance()
		}
		recordProxyApiLog(user.ID, token, canonical, upstreamErr.status, clientIP, startTime, path, upstreamErr.errorType, upstreamErr.message)
		c.Set("Content-Type", "application/json")
		return c.Status(upstreamErr.status).Send(upstreamErr.body)
	}

	if geminiReq.IsStream {
		return handleStreamingGeminiResponse(c, user, token, subToken, isSubToken, canonical, geminiReq, body, upstream, prePrice, fallbackUserOptIn, clientIP, path, startTime, unlockBalance)
	}

	defer upstream.resp.Body.Close()
	if upstream.cancel != nil {
		defer upstream.cancel()
	}

	statusCode := upstream.resp.StatusCode
	bodyCopy, _ := io.ReadAll(upstream.resp.Body)
	if statusCode < 200 || statusCode >= 300 {
		log.Printf("[GEMINI-UPSTREAM-ERR] channel=%d status=%d body=%s", upstream.route.ChannelID, statusCode, sanitizeError(truncForLog(bodyCopy, 1024), 1024))
		recordProxyApiLog(user.ID, token, canonical, statusCode, clientIP, startTime, path, "upstream_error", string(bodyCopy))
		c.Set("Content-Type", "application/json")
		return c.Status(statusCode).Send(bodyCopy)
	}

	// 非流式计费链路
	selectedChannelType := ""
	if upstream.channel != nil {
		selectedChannelType = upstream.channel.Type
	}
	actualPrice, priceErr := resolveGeminiActualPrice(canonical, bodyCopy, upstream.route)
	if priceErr != nil {
		log.Printf("[GEMINI-BILLING-CRITICAL] user=%d model=%s price resolve after delivery failed: %v", user.ID, canonical, priceErr)
		// fallback pending reconcile（按 precheck estimate 写账）
		billingResolution := ResolveBillingRules(canonical, body, 0, selectedChannelType, fallbackUserOptIn).WithCosts(prePrice.AmountMicroUSD)
		recordGeminiPendingReconcile(user, token, canonical, geminiReq, prePrice, billingResolution, selectedChannelType, statusCode, clientIP, path, startTime, fmt.Sprintf("price resolve failed: %v", priceErr))
		copyImageResponseHeaders(c, upstream.resp.Header)
		setModelAuditHeaders(c, canonical, canonical, fallbackUserOptIn, "")
		return c.Status(statusCode).Send(bodyCopy)
	}

	billingResolution := ResolveBillingRules(canonical, body, actualPrice.ReasoningTokens, selectedChannelType, fallbackUserOptIn).WithCosts(actualPrice.AmountMicroUSD)
	chargedCostMicroUSD := billingResolution.ChargedCostMicroUSD

	commitDecision := Decide(EngineRequest{
		UserID:       user.ID,
		ModelName:    canonical,
		InputTokens:  actualPrice.PromptTokens,
		OutputTokens: actualPrice.CompletionTokens,
		CostMicroUSD: chargedCostMicroUSD,
		IsPrecheck:   false,
	})

	var apiLogID uint
	if commitDecision.NeedsRetry {
		apiLogID = recordGeminiPendingReconcile(user, token, canonical, geminiReq, actualPrice, billingResolution, selectedChannelType, statusCode, clientIP, path, startTime, "subscription commit failed")
		copyImageResponseHeaders(c, upstream.resp.Header)
		setModelAuditHeaders(c, canonical, canonical, fallbackUserOptIn, "")
		return c.Status(statusCode).Send(bodyCopy)
	}
	commitOK := commitDecision.Allowed && !commitDecision.FallbackToBalance
	if !commitOK && !user.BalanceConsumeEnabled {
		apiLogID = recordGeminiPendingReconcile(user, token, canonical, geminiReq, actualPrice, billingResolution, selectedChannelType, statusCode, clientIP, path, startTime, "subscription commit fell back to disabled balance")
		copyImageResponseHeaders(c, upstream.resp.Header)
		setModelAuditHeaders(c, canonical, canonical, fallbackUserOptIn, "")
		return c.Status(statusCode).Send(bodyCopy)
	}

	var (
		effectiveRevenueMicroUSD int64
		referralReward           database.ReferralPaidSpendRewardResult
	)
	if commitOK {
		apiLogID = createGeminiApiLog(user.ID, token, canonical, geminiReq, actualPrice, billingResolution, selectedChannelType, statusCode, clientIP, path, startTime)
		subID := commitDecision.SubscriptionID
		if billErr := database.WriteBillingEntryNonFatal(database.BillingEntryInput{
			UserID:               user.ID,
			EntryType:            database.BillingTypeApiUsageSub,
			AmountUSD:            0,
			BalanceAfterUSD:      user.Quota,
			ModelName:            canonical,
			TokensTotal:          actualPrice.PromptTokens + actualPrice.CompletionTokens,
			SourceSubscriptionID: &subID,
			RelatedType:          relatedTypeForApiLog(apiLogID),
			RelatedID:            apiLogID,
			Description:          fmt.Sprintf("套餐 · %s · gemini native · %s · %s", canonical, geminiReq.Method, FormatChargedCostForDescription(actualPrice.AmountMicroUSD, chargedCostMicroUSD)),
		}); billErr != nil {
			log.Printf("[GEMINI-BILLING-AUDIT-FAIL] user=%d sub=%d model=%s: %v", user.ID, subID, canonical, billErr)
		}
		effectiveRevenueMicroUSD = subscriptionRevenueMicroUSD(chargedCostMicroUSD, commitDecision.SubscriptionIsGranted)
		if apiLogID != 0 {
			RecordApiLogRevenue(apiLogID, database.RevenueSourceSubscription, effectiveRevenueMicroUSD, subID)
		}
	} else {
		apiLogID, effectiveRevenueMicroUSD, referralReward = deductGeminiBalanceAndLog(user, token, canonical, geminiReq, actualPrice, billingResolution, selectedChannelType, statusCode, clientIP, path, startTime)
	}

	if isSubToken && effectiveRevenueMicroUSD > 0 {
		incrementSubTokenUsedQuota(token, subToken, effectiveRevenueMicroUSD)
	}
	if referralReward.ReferrerID != 0 && referralReward.RewardMicroUSD > 0 {
		RefreshUserAuth(referralReward.ReferrerID)
	}
	if apiLogID == 0 {
		log.Printf("[GEMINI-BILLING-CRITICAL] user=%d model=%s served but api_log missing", user.ID, canonical)
	}

	copyImageResponseHeaders(c, upstream.resp.Header)
	setModelAuditHeaders(c, canonical, canonical, fallbackUserOptIn, "")
	return c.Status(statusCode).Send(bodyCopy)
}

// isGeminiListModelsRequest 检测请求是 listModels 模式：GET 方法 + action 为空
// （/v1beta/models 直接路径，无 :method 后缀）。
func isGeminiListModelsRequest(c *fiber.Ctx) bool {
	if c.Method() != http.MethodGet {
		return false
	}
	action := strings.TrimPrefix(c.Params("*"), "/")
	return action == ""
}

// forwardGeminiListModels 透传 GET /v1beta/models 到 CPA，让 Google AI SDK 拿到
// Gemini 兼容的模型列表。不计费、不写 ApiLogUsageLine，但 ApiLog 会记一条用于审计。
func forwardGeminiListModels(c *fiber.Ctx, user *database.User, token, clientIP string, startTime time.Time, path string) error {
	// 找一个活跃的 cliproxy channel 转发
	var ch database.Channel
	if err := database.DB.Where("type = ? AND status = ?", ChannelTypeCLIProxy, 1).First(&ch).Error; err != nil {
		recordProxyApiLog(user.ID, token, "list_models", 503, clientIP, startTime, path, "channel_unavailable", "no active CLIProxyAPI channel for listModels")
		return c.Status(503).JSON(fiber.Map{"error": fiber.Map{
			"message": "no active CLIProxyAPI channel available for /v1beta/models listing",
			"type":    "service_unavailable",
		}})
	}
	upstreamURL := strings.TrimRight(ch.BaseURL, "/") + database.EndpointGeminiNative
	// SEC-FIX-M1: ?alt= 值需 url.QueryEscape，防客户端注入额外 query 参数 / 路径 segment
	if alt := strings.TrimSpace(c.Query("alt")); alt != "" {
		upstreamURL += "?alt=" + url.QueryEscape(alt)
	}
	upstreamCtx, upstreamCancel := context.WithCancel(c.Context())
	defer upstreamCancel()
	httpReq, err := http.NewRequestWithContext(upstreamCtx, http.MethodGet, upstreamURL, nil)
	if err != nil {
		recordProxyApiLog(user.ID, token, "list_models", 502, clientIP, startTime, path, "bad_gateway", err.Error())
		return c.Status(502).JSON(fiber.Map{"error": fiber.Map{"message": "upstream request build failed", "type": "bad_gateway"}})
	}
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+ch.Key)
	if ch.Headers != "" {
		var customHeaders map[string]string
		if err := json.Unmarshal([]byte(ch.Headers), &customHeaders); err == nil {
			for k, v := range customHeaders {
				httpReq.Header.Set(k, v)
			}
		}
	}
	httpClient := &http.Client{
		Transport: getTransport(ch.ProxyURL),
		Timeout:   nonStreamUpstreamTimeout(),
	}
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		MarkChannelFailure(ch.ID, 0)
		recordProxyApiLog(user.ID, token, "list_models", 502, clientIP, startTime, path, "bad_gateway", "upstream listModels connection failed")
		return c.Status(502).JSON(fiber.Map{"error": fiber.Map{"message": "upstream connection failed", "type": "bad_gateway"}})
	}
	defer resp.Body.Close()
	bodyCopy, _ := io.ReadAll(resp.Body)
	copyImageResponseHeaders(c, resp.Header)
	recordProxyApiLog(user.ID, token, "list_models", resp.StatusCode, clientIP, startTime, path, "", "")
	return c.Status(resp.StatusCode).Send(bodyCopy)
}

// parseGeminiNativeAction 从 fiber 路由参数解析 "<model>:<method>"。
// fiber 用 `*` 通配符拿到 catch-all 部分。
func parseGeminiNativeAction(c *fiber.Ctx) (geminiNativeRequest, error) {
	action := c.Params("*")
	if action == "" {
		// 兜底：从 URL 推导
		path := c.Path()
		idx := strings.Index(path, "/models/")
		if idx >= 0 {
			action = path[idx+len("/models/"):]
		}
	}
	action = strings.TrimPrefix(action, "/")
	if action == "" {
		return geminiNativeRequest{}, fmt.Errorf("action is required")
	}
	parts := strings.SplitN(action, ":", 2)
	if len(parts) != 2 {
		return geminiNativeRequest{}, fmt.Errorf("action must be '<model>:<method>'")
	}
	model := strings.TrimSpace(parts[0])
	method := strings.TrimSpace(parts[1])
	if model == "" || method == "" {
		return geminiNativeRequest{}, fmt.Errorf("action must contain non-empty model and method")
	}
	switch method {
	case "generateContent", "streamGenerateContent", "countTokens":
	default:
		return geminiNativeRequest{}, fmt.Errorf("unsupported method %q (only generateContent/streamGenerateContent/countTokens are exposed)", method)
	}
	return geminiNativeRequest{
		Model:    model,
		Method:   method,
		IsStream: method == "streamGenerateContent",
		Alt:      strings.TrimSpace(c.Query("alt")),
	}, nil
}

// rejectGeminiNativeFileURIRefs 拒绝任意 fileData.fileUri 引用（Google File API），
// 避免上游 fetch oracle 风险；客户端必须用 inlineData base64 直接上传。
func rejectGeminiNativeFileURIRefs(body []byte) error {
	if len(body) == 0 {
		return nil
	}
	if !gjson.ValidBytes(body) {
		return fmt.Errorf("request body must be valid JSON")
	}
	contents := gjson.GetBytes(body, "contents")
	var found bool
	if contents.IsArray() {
		contents.ForEach(func(_, item gjson.Result) bool {
			parts := item.Get("parts")
			if parts.IsArray() {
				parts.ForEach(func(_, p gjson.Result) bool {
					if p.Get("fileData.fileUri").Exists() || p.Get("fileData.file_uri").Exists() {
						found = true
						return false
					}
					return true
				})
			}
			return !found
		})
	}
	if found {
		return fmt.Errorf("fileData.fileUri is not supported; embed inlineData base64 instead")
	}
	return nil
}

// filterGeminiNativeRoutes 仅留挂了 /v1beta/models endpoint 的 ChannelModel。
func filterGeminiNativeRoutes(routes []*database.ChannelModel) []*database.ChannelModel {
	out := make([]*database.ChannelModel, 0, len(routes))
	for _, r := range routes {
		if r == nil {
			continue
		}
		database.NormalizeChannelModelMetadata(r)
		if !database.ChannelModelAllowsEndpoint(r, database.EndpointGeminiNative) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// geminiPriceResolution 是 Gemini native 路径下的计费快照。
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

// callGeminiNativeUpstream 把 client 请求转发到 CPA /v1beta/models/<action>。
func callGeminiNativeUpstream(c *fiber.Ctx, modelName string, geminiReq geminiNativeRequest, body []byte, routes []*database.ChannelModel, channelMapRef map[uint]*database.Channel) (*selectedImageUpstream, *upstreamImageError) {
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
			return nil, imageErr(502, "backend_exhausted", "All Gemini upstream channels exhausted or failing")
		}
		selected := chooseWeightedImageRoute(available, totalWeight)
		ch := channelMapRef[selected.ChannelID]
		if ch == nil {
			failedChannels[selected.ChannelID] = true
			last = imageErr(502, "channel_unavailable", "channel was disabled or removed mid-flight")
			continue
		}
		if NormalizeChannelType(ch.Type) != ChannelTypeCLIProxy {
			failedChannels[selected.ChannelID] = true
			last = imageErr(502, "channel_misconfigured", "Gemini native is only supported through CLIProxyAPI channels")
			continue
		}
		// SEC-FIX-M2: modelName 经 DB 白名单校验，但纵深防御加 url.PathEscape；
		// SEC-FIX-M1: alt 经 url.QueryEscape 防 query 注入
		urlPath := fmt.Sprintf("%s/%s:%s", strings.TrimRight(ch.BaseURL, "/")+database.EndpointGeminiNative, url.PathEscape(modelName), geminiReq.Method)
		if geminiReq.Alt != "" {
			urlPath += "?alt=" + url.QueryEscape(geminiReq.Alt)
		}
		upstreamCtx, upstreamCancel := context.WithCancel(c.Context())
		httpReq, err := http.NewRequestWithContext(upstreamCtx, http.MethodPost, urlPath, bytes.NewReader(body))
		if err != nil {
			upstreamCancel()
			failedChannels[selected.ChannelID] = true
			last = imageErr(502, "bad_gateway", err.Error())
			continue
		}
		httpReq.Header.Set("Content-Type", "application/json")
		if geminiReq.IsStream {
			httpReq.Header.Set("Accept", "text/event-stream")
		} else {
			httpReq.Header.Set("Accept", "application/json")
		}
		// CPA 用 Bearer 或 ?key=  — 走 Bearer
		httpReq.Header.Set("Authorization", "Bearer "+ch.Key)
		if ch.Headers != "" {
			var customHeaders map[string]string
			if err := json.Unmarshal([]byte(ch.Headers), &customHeaders); err == nil {
				for k, v := range customHeaders {
					httpReq.Header.Set(k, v)
				}
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
			resp.Body.Close()
			upstreamCancel()
			last = imageErr(http.StatusTooManyRequests, "upstream_rate_limited", "all upstream channels are rate limited")
		case StatusActionConfigError:
			failedChannels[selected.ChannelID] = true
			resp.Body.Close()
			upstreamCancel()
			markChannelModelUnhealthy(selected.ChannelID, modelName)
			last = imageErr(resp.StatusCode, "channel_model_unhealthy", "upstream returned config error for Gemini model")
		default:
			failedChannels[selected.ChannelID] = true
			resp.Body.Close()
			upstreamCancel()
			MarkChannelFailure(selected.ChannelID, resp.StatusCode)
			last = imageErr(resp.StatusCode, "upstream_error", fmt.Sprintf("upstream returned %d (channel rotated)", resp.StatusCode))
		}
	}
	if last != nil {
		return nil, last
	}
	return nil, imageErr(502, "backend_exhausted", "All Gemini upstream channels exhausted or failing")
}

// forwardGeminiCountTokens countTokens 透传 — 不计费（只查 metadata）。
func forwardGeminiCountTokens(c *fiber.Ctx, user *database.User, token, modelName string, body []byte, routes []*database.ChannelModel, channelMapRef map[uint]*database.Channel, clientIP string, startTime time.Time, path, alt string) error {
	geminiReq := geminiNativeRequest{Model: modelName, Method: "countTokens", Alt: alt}
	upstream, upstreamErr := callGeminiNativeUpstream(c, modelName, geminiReq, body, routes, channelMapRef)
	if upstreamErr != nil {
		recordProxyApiLog(user.ID, token, modelName, upstreamErr.status, clientIP, startTime, path, upstreamErr.errorType, upstreamErr.message)
		c.Set("Content-Type", "application/json")
		return c.Status(upstreamErr.status).Send(upstreamErr.body)
	}
	defer upstream.resp.Body.Close()
	if upstream.cancel != nil {
		defer upstream.cancel()
	}
	statusCode := upstream.resp.StatusCode
	bodyCopy, _ := io.ReadAll(upstream.resp.Body)
	recordProxyApiLog(user.ID, token, modelName, statusCode, clientIP, startTime, path, "", "")
	copyImageResponseHeaders(c, upstream.resp.Header)
	return c.Status(statusCode).Send(bodyCopy)
}

// createGeminiApiLog 写 ApiLog + ApiLogUsageLine（INSERT-only）。
func createGeminiApiLog(userID uint, token, modelName string, geminiReq geminiNativeRequest, price geminiPriceResolution, billing BillingRuleResolution, channelType string, statusCode int, clientIP, path string, startTime time.Time) uint {
	apiLog := database.ApiLog{
		UserID:              userID,
		TokenName:           HashTokenForLog(token),
		ModelName:           modelName,
		RequestedModel:      billing.RequestedModel,
		ServedModel:         billing.ServedModel,
		PromptTokens:        price.PromptTokens,
		CompletionTokens:    price.CompletionTokens,
		CachedTokens:        price.CachedTokens,
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
		line := database.ApiLogUsageLine{
			ApiLogID:       apiLog.ID,
			ModelName:      modelName,
			RequestPath:    database.EndpointGeminiNative,
			Unit:           geminiUsageUnit(price),
			Direction:      "total",
			Quantity:       price.Quantity,
			UnitPriceMicro: price.UnitPriceMicro,
			AmountMicroUSD: price.AmountMicroUSD,
			CostSource:     price.CostSource,
			MetadataJSON:   geminiUsageMetadataJSON(geminiReq, price),
			CreatedAt:      time.Now(),
		}
		return tx.Create(&line).Error
	})
	if err != nil {
		log.Printf("[GEMINI-BILLING-CRITICAL] api log/usage line create failed user=%d model=%s: %v", userID, modelName, err)
		return 0
	}
	return apiLog.ID
}

func recordGeminiPendingReconcile(user *database.User, token, modelName string, geminiReq geminiNativeRequest, price geminiPriceResolution, billing BillingRuleResolution, channelType string, statusCode int, clientIP, path string, startTime time.Time, reason string) uint {
	if user == nil {
		return 0
	}
	apiLogID := createGeminiApiLog(user.ID, token, modelName, geminiReq, price, billing, channelType, statusCode, clientIP, path, startTime)
	entry := database.BillingEntryInput{
		UserID:           user.ID,
		EntryType:        database.BillingTypeApiUsagePendingReconcile,
		BillingState:     database.BillingStatePendingReconcile,
		AmountUSD:        0,
		BalanceAfterUSD:  user.Quota,
		ModelName:        modelName,
		TokensTotal:      price.PromptTokens + price.CompletionTokens,
		RequestID:        fmt.Sprintf("api_log:%d", apiLogID),
		EstimatedCostUSD: price.AmountMicroUSD,
		RelatedType:      relatedTypeForApiLog(apiLogID),
		RelatedID:        apiLogID,
		Description:      fmt.Sprintf("[GEMINI-PENDING] %s · %s · %s 待对账（%s）", modelName, geminiReq.Method, FormatChargedCostForDescription(price.AmountMicroUSD, billing.ChargedCostMicroUSD), reason),
	}
	if err := database.WriteBillingEntryNonFatal(entry); err != nil {
		log.Printf("[GEMINI-BILLING-LOST-DEBT] user=%d model=%s amount_micro=%d: %v", user.ID, modelName, price.AmountMicroUSD, err)
	}
	return apiLogID
}

func deductGeminiBalanceAndLog(user *database.User, token, modelName string, geminiReq geminiNativeRequest, price geminiPriceResolution, billing BillingRuleResolution, channelType string, statusCode int, clientIP, path string, startTime time.Time) (uint, int64, database.ReferralPaidSpendRewardResult) {
	var apiLogID uint
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
			ModelName:           modelName,
			RequestedModel:      billing.RequestedModel,
			ServedModel:         billing.ServedModel,
			PromptTokens:        price.PromptTokens,
			CompletionTokens:    price.CompletionTokens,
			CachedTokens:        price.CachedTokens,
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
		if err := tx.Create(&database.ApiLogUsageLine{
			ApiLogID:       apiLogID,
			ModelName:      modelName,
			RequestPath:    database.EndpointGeminiNative,
			Unit:           geminiUsageUnit(price),
			Direction:      "total",
			Quantity:       price.Quantity,
			UnitPriceMicro: price.UnitPriceMicro,
			AmountMicroUSD: price.AmountMicroUSD,
			CostSource:     price.CostSource,
			MetadataJSON:   geminiUsageMetadataJSON(geminiReq, price),
			CreatedAt:      time.Now(),
		}).Error; err != nil {
			return fmt.Errorf("create usage line: %w", err)
		}
		if res.RowsAffected == 0 {
			// 并发耗光：写 pending reconcile（同 image/video 模式）
			var current database.User
			if err := tx.Select("id, quota").First(&current, user.ID).Error; err != nil {
				return fmt.Errorf("user row missing: %w", err)
			}
			return database.WriteBillingEntry(tx, database.BillingEntryInput{
				UserID:           user.ID,
				EntryType:        database.BillingTypeApiUsagePendingReconcile,
				BillingState:     database.BillingStatePendingReconcile,
				AmountUSD:        0,
				BalanceAfterUSD:  current.Quota,
				ModelName:        modelName,
				TokensTotal:      price.PromptTokens + price.CompletionTokens,
				RequestID:        fmt.Sprintf("api_log:%d", apiLogID),
				EstimatedCostUSD: price.AmountMicroUSD,
				RelatedType:      "api_log",
				RelatedID:        apiLogID,
				Description:      fmt.Sprintf("[GEMINI-INSUFFICIENT-BALANCE] %s · %s · 余额不足，已交付服务待对账", modelName, geminiReq.Method),
			})
		}
		if !TryConsumeBalanceTx(tx, user.ID, price.AmountMicroUSD, true) {
			log.Printf("[GEMINI-BILLING-WINDOW-TRACK-FAIL] user=%d model=%s amount=%d", user.ID, modelName, price.AmountMicroUSD)
		}
		var fresh database.User
		if err := tx.Select("id, quota").First(&fresh, user.ID).Error; err != nil {
			return fmt.Errorf("re-select quota: %w", err)
		}
		if err := database.WriteBillingEntry(tx, database.BillingEntryInput{
			UserID:          user.ID,
			EntryType:       database.BillingTypeApiConsumeBalance,
			AmountUSD:       -price.AmountMicroUSD,
			BalanceAfterUSD: fresh.Quota,
			ModelName:       modelName,
			RelatedType:     "api_log",
			RelatedID:       apiLogID,
			Description:     fmt.Sprintf("余额扣费 · %s · gemini native · %s · %s", modelName, geminiReq.Method, FormatChargedCostForDescription(price.AmountMicroUSD, billing.ChargedCostMicroUSD)),
		}); err != nil {
			return fmt.Errorf("write billing: %w", err)
		}
		reward, err := database.ApplyReferralPaidSpendRewardTx(
			tx, user.ID, price.AmountMicroUSD, referralRewardBPS, referralRewardWindowSeconds,
			time.Now(), "api_log", apiLogID, fmt.Sprintf("Gemini native · %s", modelName),
		)
		if err != nil {
			return fmt.Errorf("apply referral spend reward: %w", err)
		}
		referralReward = reward
		balanceConsumed = true
		return nil
	})
	if txErr != nil {
		log.Printf("[GEMINI-BILLING-CRITICAL] user=%d model=%s balance tx failed: %v", user.ID, modelName, txErr)
		apiLogID = recordGeminiPendingReconcile(user, token, modelName, geminiReq, price, billing, channelType, statusCode, clientIP, path, startTime, "balance transaction failed")
		return apiLogID, 0, database.ReferralPaidSpendRewardResult{}
	}
	RefreshUserAuth(user.ID)
	effectiveRevenue := int64(0)
	if balanceConsumed {
		effectiveRevenue = price.AmountMicroUSD
		if apiLogID != 0 {
			RecordApiLogRevenue(apiLogID, database.RevenueSourceBalance, price.AmountMicroUSD, 0)
		}
	}
	return apiLogID, effectiveRevenue, referralReward
}

func geminiUsageUnit(price geminiPriceResolution) string {
	if price.BillingMode == database.BillingModeImage {
		return "image"
	}
	return "token"
}

func geminiUsageMetadataJSON(geminiReq geminiNativeRequest, price geminiPriceResolution) string {
	b, _ := json.Marshal(map[string]any{
		"method":            geminiReq.Method,
		"alt":               geminiReq.Alt,
		"prompt_tokens":     price.PromptTokens,
		"completion_tokens": price.CompletionTokens,
		"cached_tokens":     price.CachedTokens,
		"reasoning_tokens":  price.ReasoningTokens,
		"image_count":       price.ImageCount,
		"billing_mode":      price.BillingMode,
		"cost_source":       price.CostSource,
	})
	return string(b)
}

// handleStreamingGeminiResponse 处理 streamGenerateContent SSE 透传 + 流末 usage 抽取。
// 与 P1 image streaming 框架同样的 SetBodyStreamWriter 模式，但解析 Google SSE 格式。
func handleStreamingGeminiResponse(
	c *fiber.Ctx,
	user *database.User,
	token string,
	subToken *database.AccessToken,
	isSubToken bool,
	modelName string,
	geminiReq geminiNativeRequest,
	body []byte,
	upstream *selectedImageUpstream,
	prePrice geminiPriceResolution,
	fallbackUserOptIn bool,
	clientIP, path string,
	startTime time.Time,
	unlockBalance func(),
) error {
	statusCode := upstream.resp.StatusCode
	if statusCode < 200 || statusCode >= 300 {
		defer upstream.resp.Body.Close()
		if upstream.cancel != nil {
			defer upstream.cancel()
		}
		if unlockBalance != nil {
			unlockBalance()
		}
		bodyBytes, _ := io.ReadAll(upstream.resp.Body)
		log.Printf("[GEMINI-STREAM-UPSTREAM-ERR] channel=%d status=%d body=%s", upstream.route.ChannelID, statusCode, sanitizeError(truncForLog(bodyBytes, 1024), 1024))
		recordProxyApiLog(user.ID, token, modelName, statusCode, clientIP, startTime, path, "upstream_error", string(bodyBytes))
		c.Set("Content-Type", "application/json")
		return c.Status(statusCode).Send(bodyBytes)
	}

	copyImageResponseHeaders(c, upstream.resp.Header)
	if geminiReq.Alt == "" {
		c.Set("Content-Type", "text/event-stream")
	}
	c.Set("Cache-Control", "no-cache")
	c.Set("Connection", "keep-alive")
	c.Set("X-Accel-Buffering", "no")
	setModelAuditHeaders(c, modelName, modelName, fallbackUserOptIn, "")
	c.Status(statusCode)

	selectedChannelType := ""
	if upstream.channel != nil {
		selectedChannelType = upstream.channel.Type
	}

	c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[GEMINI-STREAM-PANIC] user=%d model=%s recovered: %v", user.ID, modelName, r)
			}
			_ = upstream.resp.Body.Close()
			if upstream.cancel != nil {
				upstream.cancel()
			}
			if unlockBalance != nil {
				unlockBalance()
			}
		}()

		scanner := bufio.NewScanner(upstream.resp.Body)
		bufLimit := 16 * 1024 * 1024
		SysConfigMutex.RLock()
		if v := SysConfigCache["gemini_stream_scanner_buffer_bytes"]; v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 256*1024 {
				bufLimit = n
			}
		}
		SysConfigMutex.RUnlock()
		scanner.Buffer(make([]byte, 64*1024), bufLimit)

		flushOrBail := func() bool {
			if err := w.Flush(); err != nil {
				log.Printf("[GEMINI-STREAM-CLIENT-DISCONNECT] user=%d model=%s err=%v", user.ID, modelName, err)
				return false
			}
			return true
		}

		var (
			lastChunkJSON      []byte
			imageCount         int
			clientDisconnected bool
			sawUsage           bool
		)
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) > 0 {
				w.Write(line)
			}
			w.Write([]byte("\n"))
			if !flushOrBail() {
				clientDisconnected = true
				break
			}
			trimmed := bytes.TrimRight(line, "\r")
			payload := geminiSSEJsonPayload(trimmed)
			if len(payload) > 0 {
				lastChunkJSON = append(lastChunkJSON[:0], payload...)
				if gjson.GetBytes(payload, "usageMetadata").Exists() {
					sawUsage = true
				}
				imageCount += countGeminiInlineImages(payload)
			}
		}

		if err := scanner.Err(); err != nil {
			log.Printf("[GEMINI-STREAM-SCANNER-ERR] user=%d model=%s err=%v", user.ID, modelName, err)
		}

		performStreamingGeminiBilling(streamingGeminiBillingInput{
			User:                user,
			Token:               token,
			SubToken:            subToken,
			IsSubToken:          isSubToken,
			ModelName:           modelName,
			GeminiReq:           geminiReq,
			Body:                body,
			Upstream:            upstream,
			PrePrice:            prePrice,
			FallbackUserOptIn:   fallbackUserOptIn,
			ClientIP:            clientIP,
			Path:                path,
			StartTime:           startTime,
			SelectedChannelType: selectedChannelType,
			LastChunkJSON:       lastChunkJSON,
			ImageCount:          imageCount,
			SawUsage:            sawUsage,
			ClientDisconnected:  clientDisconnected,
			StatusCode:          statusCode,
		})
	})
	return nil
}

type streamingGeminiBillingInput struct {
	User                *database.User
	Token               string
	SubToken            *database.AccessToken
	IsSubToken          bool
	ModelName           string
	GeminiReq           geminiNativeRequest
	Body                []byte
	Upstream            *selectedImageUpstream
	PrePrice            geminiPriceResolution
	FallbackUserOptIn   bool
	ClientIP            string
	Path                string
	StartTime           time.Time
	SelectedChannelType string
	LastChunkJSON       []byte
	ImageCount          int
	SawUsage            bool
	ClientDisconnected  bool
	StatusCode          int
}

func performStreamingGeminiBilling(in streamingGeminiBillingInput) {
	needsPending := false
	reason := ""
	if in.ClientDisconnected {
		needsPending = true
		reason = "client disconnected before stream completed"
	} else if !in.SawUsage && in.ImageCount == 0 {
		needsPending = true
		reason = "stream ended without usageMetadata or image data"
	}

	var (
		actualPrice geminiPriceResolution
		priceErr    error
	)
	if !needsPending {
		actualPrice, priceErr = resolveGeminiActualPrice(in.ModelName, in.LastChunkJSON, in.Upstream.route)
		if priceErr != nil {
			needsPending = true
			reason = fmt.Sprintf("price resolve failed: %v", priceErr)
		}
	}

	billingResolution := ResolveBillingRules(in.ModelName, in.Body, actualPrice.ReasoningTokens, in.SelectedChannelType, in.FallbackUserOptIn).WithCosts(actualPrice.AmountMicroUSD)
	if needsPending {
		// 用 precheck estimate 写 pending
		pendingPrice := in.PrePrice
		pendingPrice.CostSource = "pending_reconcile"
		fallbackBilling := ResolveBillingRules(in.ModelName, in.Body, 0, in.SelectedChannelType, in.FallbackUserOptIn).WithCosts(pendingPrice.AmountMicroUSD)
		if id := recordGeminiPendingReconcile(in.User, in.Token, in.ModelName, in.GeminiReq, pendingPrice, fallbackBilling, in.SelectedChannelType, in.StatusCode, in.ClientIP, in.Path, in.StartTime, reason); id == 0 {
			log.Printf("[GEMINI-STREAM-BILLING-CRITICAL] user=%d model=%s streamed but pending reconcile write failed", in.User.ID, in.ModelName)
		}
		return
	}

	chargedCostMicroUSD := billingResolution.ChargedCostMicroUSD
	commitDecision := Decide(EngineRequest{
		UserID:       in.User.ID,
		ModelName:    in.ModelName,
		InputTokens:  actualPrice.PromptTokens,
		OutputTokens: actualPrice.CompletionTokens,
		CostMicroUSD: chargedCostMicroUSD,
		IsPrecheck:   false,
	})
	if commitDecision.NeedsRetry {
		recordGeminiPendingReconcile(in.User, in.Token, in.ModelName, in.GeminiReq, actualPrice, billingResolution, in.SelectedChannelType, in.StatusCode, in.ClientIP, in.Path, in.StartTime, "subscription commit failed")
		return
	}
	commitOK := commitDecision.Allowed && !commitDecision.FallbackToBalance
	if !commitOK && !in.User.BalanceConsumeEnabled {
		recordGeminiPendingReconcile(in.User, in.Token, in.ModelName, in.GeminiReq, actualPrice, billingResolution, in.SelectedChannelType, in.StatusCode, in.ClientIP, in.Path, in.StartTime, "subscription commit fell back to disabled balance")
		return
	}

	var (
		apiLogID                 uint
		effectiveRevenueMicroUSD int64
		referralReward           database.ReferralPaidSpendRewardResult
	)
	if commitOK {
		apiLogID = createGeminiApiLog(in.User.ID, in.Token, in.ModelName, in.GeminiReq, actualPrice, billingResolution, in.SelectedChannelType, in.StatusCode, in.ClientIP, in.Path, in.StartTime)
		subID := commitDecision.SubscriptionID
		if billErr := database.WriteBillingEntryNonFatal(database.BillingEntryInput{
			UserID:               in.User.ID,
			EntryType:            database.BillingTypeApiUsageSub,
			AmountUSD:            0,
			BalanceAfterUSD:      in.User.Quota,
			ModelName:            in.ModelName,
			TokensTotal:          actualPrice.PromptTokens + actualPrice.CompletionTokens,
			SourceSubscriptionID: &subID,
			RelatedType:          relatedTypeForApiLog(apiLogID),
			RelatedID:            apiLogID,
			Description:          fmt.Sprintf("套餐 · %s · gemini stream · %s", in.ModelName, FormatChargedCostForDescription(actualPrice.AmountMicroUSD, chargedCostMicroUSD)),
		}); billErr != nil {
			log.Printf("[GEMINI-STREAM-BILLING-AUDIT-FAIL] user=%d sub=%d model=%s: %v", in.User.ID, subID, in.ModelName, billErr)
		}
		effectiveRevenueMicroUSD = subscriptionRevenueMicroUSD(chargedCostMicroUSD, commitDecision.SubscriptionIsGranted)
		if apiLogID != 0 {
			RecordApiLogRevenue(apiLogID, database.RevenueSourceSubscription, effectiveRevenueMicroUSD, subID)
		}
	} else {
		apiLogID, effectiveRevenueMicroUSD, referralReward = deductGeminiBalanceAndLog(in.User, in.Token, in.ModelName, in.GeminiReq, actualPrice, billingResolution, in.SelectedChannelType, in.StatusCode, in.ClientIP, in.Path, in.StartTime)
	}

	if in.IsSubToken && effectiveRevenueMicroUSD > 0 {
		incrementSubTokenUsedQuota(in.Token, in.SubToken, effectiveRevenueMicroUSD)
	}
	if referralReward.ReferrerID != 0 && referralReward.RewardMicroUSD > 0 {
		RefreshUserAuth(referralReward.ReferrerID)
	}
	if apiLogID == 0 {
		log.Printf("[GEMINI-STREAM-BILLING-CRITICAL] user=%d model=%s streamed but api_log missing", in.User.ID, in.ModelName)
	}
}

// geminiSSEJsonPayload 从 SSE 一行抽 JSON payload（剥 "data: " 前缀，跳过 event/空行）。
func geminiSSEJsonPayload(line []byte) []byte {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return nil
	}
	if bytes.HasPrefix(trimmed, []byte("event:")) || bytes.HasPrefix(trimmed, []byte(":")) {
		return nil
	}
	if bytes.HasPrefix(trimmed, []byte("data:")) {
		trimmed = bytes.TrimSpace(trimmed[len("data:"):])
	}
	if len(trimmed) == 0 || trimmed[0] != '{' && trimmed[0] != '[' {
		return nil
	}
	return trimmed
}
