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
	"github.com/tidwall/gjson"
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
		// 提前 return 分支：只需落 pending reconcile 日志的副作用，不用其返回的 apiLogID。
		recordGeminiPendingReconcile(user, token, canonical, geminiReq, actualPrice, billingResolution, selectedChannelType, statusCode, clientIP, path, startTime, "subscription commit failed")
		copyImageResponseHeaders(c, upstream.resp.Header)
		setModelAuditHeaders(c, canonical, canonical, fallbackUserOptIn, "")
		return c.Status(statusCode).Send(bodyCopy)
	}
	commitOK := commitDecision.Allowed && !commitDecision.FallbackToBalance
	if !commitOK && !user.BalanceConsumeEnabled {
		recordGeminiPendingReconcile(user, token, canonical, geminiReq, actualPrice, billingResolution, selectedChannelType, statusCode, clientIP, path, startTime, "subscription commit fell back to disabled balance")
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
// S7-1 后路由是 `/v1beta/models/:modelAction`，c.Params("modelAction") 拿到单段。
// 老 `c.Params("*")` 也保留兜底，兼容测试 / 历史 wildcard 注册。
func parseGeminiNativeAction(c *fiber.Ctx) (geminiNativeRequest, error) {
	action := c.Params("modelAction")
	if action == "" {
		action = c.Params("*")
	}
	if action == "" {
		// 最终兜底：从 URL 推导（防 fiber 行为变化或路由注册笔误）
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
// callGeminiNativeUpstream 把 client 请求转发到 CPA /v1beta/models/<action>。
// createGeminiApiLog 写 ApiLog + ApiLogUsageLine（INSERT-only）。
