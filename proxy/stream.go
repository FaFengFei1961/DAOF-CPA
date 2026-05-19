package proxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	mrand "math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"daof-cpa/database"

	"github.com/gofiber/fiber/v2"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	_ "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator/builtin"
)

var transportCache sync.Map

const (
	proxyNonStreamUpstreamTimeoutKey = "proxy_nonstream_upstream_timeout_seconds"
	defaultNonStreamUpstreamTimeout  = 15 * time.Minute
	minNonStreamUpstreamTimeout      = 30 * time.Second
	maxNonStreamUpstreamTimeout      = 60 * time.Minute

	invalidAuthLogLimitPerIPPerMinute = 60
)

// 我们的 DialContext 拿到的是代理 IP，无法防御代理之外的 rebinding，但代理本身是 admin 可信节点。
var safeTransport = func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = safeDialContext
	return t
}()

// SafeTransport 暴露给 controller 层调用上游模型探测 / 健康检查等场景，
// 让任何 admin 触发的 HTTP 请求都默认带 DNS rebinding 防护。
func SafeTransport() *http.Transport { return safeTransport }

func nonStreamUpstreamTimeout() time.Duration {
	SysConfigMutex.RLock()
	raw := strings.TrimSpace(SysConfigCache[proxyNonStreamUpstreamTimeoutKey])
	SysConfigMutex.RUnlock()
	if raw == "" {
		return defaultNonStreamUpstreamTimeout
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil {
		return defaultNonStreamUpstreamTimeout
	}
	timeout := time.Duration(seconds) * time.Second
	if timeout < minNonStreamUpstreamTimeout {
		return minNonStreamUpstreamTimeout
	}
	if timeout > maxNonStreamUpstreamTimeout {
		return maxNonStreamUpstreamTimeout
	}
	return timeout
}

func readReferralPaidSpendRewardConfig() (int64, int64) {
	SysConfigMutex.RLock()
	bpsRaw := strings.TrimSpace(SysConfigCache[database.ReferralPaidSpendRewardBPSConfigKey])
	windowRaw := strings.TrimSpace(SysConfigCache[database.ReferralPaidSpendRewardWindowSecondsConfigKey])
	SysConfigMutex.RUnlock()

	bps, err := strconv.ParseInt(bpsRaw, 10, 64)
	if err != nil {
		bps = 0
	}
	windowSeconds, err := strconv.ParseInt(windowRaw, 10, 64)
	if err != nil {
		windowSeconds = database.DefaultReferralPaidSpendRewardWindowSeconds
	}
	return database.NormalizeReferralRewardBPS(bps), database.NormalizeReferralRewardWindowSeconds(windowSeconds)
}

// truncForLog 把上游 body 截短供服务端日志使用，不让超大错误 body 撑爆 log。
func getTransport(proxyURL string) *http.Transport {
	if proxyURL == "" {
		return safeTransport
	}
	pURL, err := url.Parse(proxyURL)
	if err != nil {
		return safeTransport
	}
	// 仅当代理走本机回环时才允许 admin 通过 SysConfig 跳过 TLS 校验（开发自签名场景）。
	// 公网代理 → 强制校验。防止 admin 误开 / SysConfig 被入侵导致全平台 MITM。
	skipVerify := false
	if isLocalProxy(pURL.Hostname()) {
		SysConfigMutex.RLock()
		if v := strings.TrimSpace(SysConfigCache["proxy_tls_skip_verify"]); v == "true" {
			skipVerify = true
		}
		SysConfigMutex.RUnlock()
	}
	cacheKey := proxyURL + "|skip=" + strconv.FormatBool(skipVerify)
	if t, ok := transportCache.Load(cacheKey); ok {
		return t.(*http.Transport)
	}
	// 走 HTTP 代理：DialContext 是与代理服务器握手，仍用 safeDialContext 防代理 host 自身指向元数据 IP
	t := &http.Transport{
		Proxy:           http.ProxyURL(pURL),
		DialContext:     safeDialContext,
		TLSClientConfig: &tls.Config{InsecureSkipVerify: skipVerify}, //nolint:gosec // 仅 localhost 上游
	}
	actual, _ := transportCache.LoadOrStore(cacheKey, t)
	return actual.(*http.Transport)
}

// isLocalProxy 判断代理 host 是否为本机回环或 RFC1918 私有段。
// 决策点：proxy_tls_skip_verify 只对这些"明显由 admin 自己控制"的上游生效。
func isLocalProxy(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate()
}

func newCancelableUpstreamPostRequest(parent context.Context, upstreamURL string, payload []byte) (*http.Request, context.CancelFunc, error) {
	upstreamCtx, cancel := context.WithCancel(parent)
	req, err := http.NewRequestWithContext(upstreamCtx, http.MethodPost, upstreamURL, bytes.NewReader(payload))
	if err != nil {
		cancel()
		return nil, nil, err
	}
	return req, cancel, nil
}

// estimateTextPrecheckTokens is a fast, conservative text-token estimate.
// CJK text stays near 1 rune ≈ 1 token; ASCII/code/JSON uses 2 runes ≈ 1 token
// to avoid treating every English character as a full token in large Codex contexts.




// ChatCompletionProxyHandler intercept and forward OpenAI /v1/chat/completions stream
func ChatCompletionProxyHandler(c *fiber.Ctx) error {
	startTime := time.Now()
	clientIP := c.IP()
	path := strings.Clone(c.Path())
	srcFormat := inferSourceFormat(path)

	// 1. High Speed Auth Verification
	authHeader := string([]byte(c.Get("Authorization")))
	token := ""
	if strings.HasPrefix(strings.TrimSpace(authHeader), "Bearer ") {
		token = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(authHeader), "Bearer "))
	}
	if token == "" && srcFormat == sdktranslator.FormatGemini {
		token = strings.TrimSpace(c.Get("x-goog-api-key"))
	}

	// fix CRITICAL Sprint4-M2：user + subToken 在同一 RLock 段内读，保证一致快照
	// （AuthCache 与 AuthTokenCache 来自同一次 SyncCacheConfig 合并发布）
	authSnapshotMutex.RLock()
	user, exists := AuthCache[token]
	subToken, isSubToken := AuthTokenCache[token]
	authSnapshotMutex.RUnlock()

	if !exists {
		if shouldRecordInvalidAuthApiLog(clientIP) {
			recordProxyApiLog(0, token, "unknown", 401, clientIP, startTime, path, "auth_error", "Invalid API Key")
		}
		return c.Status(401).JSON(fiber.Map{"error": fiber.Map{"message": "Invalid API Key", "type": "auth_error"}})
	}
	// fix Major（codex 第五轮）：纵深防御——即使 RefreshUserAuth 漏过封禁用户的清理（DB 异步竞态），
	// 入口也要二次验证 user.Status==1，让封禁用户的旧 token 在到达 LLM 上游前被拦截。
	if user.Status != 1 {
		authSnapshotMutex.Lock()
		delete(AuthCache, token)
		authSnapshotMutex.Unlock()
		recordProxyApiLog(user.ID, token, "unknown", 403, clientIP, startTime, path, "auth_error", "Account suspended")
		return c.Status(403).JSON(fiber.Map{"error": fiber.Map{"message": "Account suspended", "type": "auth_error"}})
	}

	// Intercept Sub-token lifespan and quota logic
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

	// 2. Extract Model from Body
	rawBody := c.Body()
	body := make([]byte, len(rawBody))
	copy(body, rawBody)
	fallbackUserOptIn := parseAllowFallbackHeader(c)

	modelResult := gjson.GetBytes(body, "model")
	modelName := strings.TrimSpace(modelResult.String())
	if modelName == "" && srcFormat == sdktranslator.FormatGemini {
		modelName = extractGeminiModelFromPath(path)
	}
	if modelName == "" {
		recordProxyApiLog(user.ID, token, "unknown", 400, clientIP, startTime, path, "invalid_request", "Model is required")
		return c.Status(400).JSON(fiber.Map{"error": fiber.Map{"message": "Model is required", "type": "invalid_request"}})
	}
	isCountTokensRequest := isClaudeCountTokensPath(path)
	isStream := gjson.GetBytes(body, "stream").Bool()
	if srcFormat == sdktranslator.FormatGemini && isGeminiStreamPath(path) {
		isStream = true
	}

	var engineDecision EngineDecision
	if !isCountTokensRequest {
		// fix CRITICAL C1（codex 第十五轮）：precheck 必须传**估算的 token 数**，非 0。
		// Anthropic /messages/count_tokens 是官方免费辅助接口，不进入额度预检或扣费链路。
		precheckInputTokens := estimatePrecheckTokens(body)
		// fix CRITICAL R23+3-C1（codex 第四轮）：precheck 阶段给 OutputTokens 一个保守上界估算。
		precheckOutputTokens := 4096 // 默认保守上界
		if maxTok := gjson.GetBytes(body, "max_tokens").Int(); maxTok > 0 {
			precheckOutputTokens = int(maxTok)
		} else if maxTok := gjson.GetBytes(body, "max_output_tokens").Int(); maxTok > 0 {
			precheckOutputTokens = int(maxTok) // OpenAI Responses API
		} else if maxTok := gjson.GetBytes(body, "max_completion_tokens").Int(); maxTok > 0 {
			precheckOutputTokens = int(maxTok)
		} else if maxTok := gjson.GetBytes(body, "generationConfig.maxOutputTokens").Int(); maxTok > 0 {
			precheckOutputTokens = int(maxTok)
		}
		// 客户端可能传巨大值想绕开预检，cap 到合理上限（与窗口相比仍是有意义的占位）
		if precheckOutputTokens > 100000 {
			precheckOutputTokens = 100000
		}
		precheckCostMicroUSD := estimatePrecheckBalanceDelta(modelName, precheckInputTokens, precheckOutputTokens)
		precheckBilling := ResolveBillingRules(modelName, body, 0, "", fallbackUserOptIn).WithCosts(precheckCostMicroUSD)
		engineDecision = Decide(EngineRequest{
			UserID:       user.ID,
			ModelName:    modelName,
			InputTokens:  precheckInputTokens,
			OutputTokens: precheckOutputTokens,
			CostMicroUSD: precheckBilling.ChargedCostMicroUSD,
			IsPrecheck:   true,
		})
		if !engineDecision.Allowed {
			msg := engineDecision.BlockMessage
			if msg == "" {
				msg = "您的订阅额度已用尽，请购买套餐或充值余额"
			}
			// fix CRITICAL R23+2-C3：DB 加载失败 fail-closed 503（让客户端 backoff），
			// 而不是 402 让用户以为"额度用尽"
			if engineDecision.NeedsRetry {
				recordProxyApiLog(user.ID, token, modelName, 503, clientIP, startTime, path, "subscription_load_failed", msg)
				return c.Status(503).JSON(fiber.Map{"error": fiber.Map{"message": msg, "type": "service_unavailable", "code": "subscription_load_failed"}})
			}
			if engineDecision.BlockQuotaPlanID != 0 {
				msg = precheckLimitMessage(engineDecision, precheckBilling)
				recordProxyApiLogWithPrecheck(user.ID, token, modelName, 402, clientIP, startTime, path, "request_estimate_exceeds_window_remaining", msg, precheckInputTokens, precheckOutputTokens, precheckBilling, engineDecision)
				return c.Status(402).JSON(precheckLimitErrorPayload(msg, engineDecision, precheckInputTokens, precheckOutputTokens, precheckBilling))
			}
			recordProxyApiLog(user.ID, token, modelName, 402, clientIP, startTime, path, "subscription_required", msg)
			return c.Status(402).JSON(fiber.Map{"error": fiber.Map{"message": msg, "type": "subscription_required"}})
		}
		// fallback 到余额：必须 (1) BalanceConsumeEnabled (2) 窗口限额未用尽 (3) 余额>0。
		// 项目未上线，不保留绕过余额消费开关的旧直扣路径。
		if engineDecision.FallbackToBalance {
			if !user.BalanceConsumeEnabled {
				if engineDecision.BlockQuotaPlanID != 0 {
					msg := precheckLimitMessage(engineDecision, precheckBilling)
					recordProxyApiLogWithPrecheck(user.ID, token, modelName, 402, clientIP, startTime, path, "request_estimate_exceeds_window_remaining", msg, precheckInputTokens, precheckOutputTokens, precheckBilling, engineDecision)
					return c.Status(402).JSON(precheckLimitErrorPayload(msg, engineDecision, precheckInputTokens, precheckOutputTokens, precheckBilling))
				}
				recordProxyApiLog(user.ID, token, modelName, 402, clientIP, startTime, path, "subscription_required", "subscription quota unavailable and balance consume disabled")
				return c.Status(402).JSON(fiber.Map{"error": fiber.Map{
					"message":      "当前请求无法使用订阅额度。请购买套餐，或在「账号设置 → 余额消费控制」中开启余额消费。",
					"type":         "subscription_required",
					"message_code": "ERR_QUOTA_EXHAUSTED_BALANCE_DISABLED",
				}})
			}
			// 余额预检使用 rawCost：用户预付美元按上游真实成本扣，不应用订阅权重。
			if !CheckBalanceConsumeAllowed(user, precheckBilling.RawCostMicroUSD) {
				recordProxyApiLog(user.ID, token, modelName, 402, clientIP, startTime, path, "balance_limit_reached", "balance consume window limit reached")
				return c.Status(402).JSON(fiber.Map{"error": fiber.Map{
					"message":      "本周期余额消费已达上限，请提高限额或等待下次重置。",
					"type":         "balance_limit_reached",
					"message_code": "ERR_BALANCE_LIMIT_REACHED",
				}})
			}
			if user.Quota <= 0 {
				recordProxyApiLog(user.ID, token, modelName, 403, clientIP, startTime, path, "quota_exceeded", "insufficient balance")
				return c.Status(403).JSON(fiber.Map{"error": fiber.Map{
					"message":      "余额不足，请充值",
					"type":         "quota_exceeded",
					"message_code": "ERR_INSUFFICIENT_BALANCE",
				}})
			}
		}
		// 把决策结果存到 locals，事后 ApiLog 关联订阅 / plan 用
		c.Locals("subscription_decision", engineDecision)
	}

	// 3. Fast Routing & Weight calculation
	// fix CRITICAL Sprint4-M2：route + channel 在同一 RLock 段内读，保证一致快照
	// （旧实现两次独立 RLock，并发 SyncCacheConfig 可在中间换新 channel map，
	// 导致 routes 引用的 ChannelID 在新 ChannelMapCache 中查不到 → 路由失败）。
	gatewayMutex.RLock()
	routes, hasRoute := RouteCache[modelName]
	channelMapRef := ChannelMapCache
	gatewayMutex.RUnlock()

	if !hasRoute || len(routes) == 0 {
		recordProxyApiLog(user.ID, token, modelName, 404, clientIP, startTime, path, "model_not_found", "Model not available via any channel")
		return c.Status(404).JSON(fiber.Map{"error": fiber.Map{"message": "Model not available via any channel", "type": "model_not_found"}})
	}
	if filteredRoutes, blocked := filterRoutesByEndpointPolicy(routes, path, isStream); len(filteredRoutes) == 0 && blocked > 0 {
		msg := unsupportedEndpointMessage(modelName, path, isStream)
		recordProxyApiLog(user.ID, token, modelName, 400, clientIP, startTime, path, "unsupported_endpoint", msg)
		return c.Status(400).JSON(fiber.Map{"error": fiber.Map{
			"message":      msg,
			"type":         "unsupported_endpoint",
			"message_code": "ERR_MODEL_ENDPOINT_UNSUPPORTED",
		}})
	} else if blocked > 0 {
		routes = filteredRoutes
	}

	// 4. 内容审核（per-ChannelModel 风控）
	//
	// fix CRITICAL R23 v2（codex 第二十三轮反馈）：必须 AFTER Decide(IsPrecheck=true) 防"成本放大"——
	// 没余额的攻击者刷请求若每条都打智能审核服务 → 我方账单被打爆。Decide 先把没余额的卡掉。
	// AFTER 路由解析：404 模型不浪费审核配额；BEFORE 路由循环：拦在任何上游（含 cloaked 路径）之前。
	modPolicy := LookupModerationPolicy(modelName)
	// fix MAJOR R23-M3：LoadFailed 时 IsActive 仍为 false，但必须 fail-closed —— 不能放行裸奔
	if modPolicy.IsActive() || modPolicy.LoadFailed() {
		gate := &ModerationGate{
			Ctx:       c,
			UserID:    user.ID,
			TokenHash: HashTokenForLog(token),
			Body:      body,
			ModelName: modelName,
			SrcFormat: srcFormat,
			Policy:    modPolicy,
			ClientIP:  clientIP,
			StartTime: startTime,
		}
		if rejected, rerr := gate.Run(); rejected {
			return rerr
		}
	}

	finalPayloadTemplate := make([]byte, len(body))
	copy(finalPayloadTemplate, body)

	// stream_options.include_usage 是 OpenAI 协议专属字段，
	// Anthropic Messages API 会返回 400 "Extra inputs are not permitted"，
	// 所以仅在 OpenAI 源格式下注入。
	if isStream && srcFormat == sdktranslator.FormatOpenAI {
		if updated, err := sjson.SetBytes(finalPayloadTemplate, "stream_options.include_usage", true); err == nil {
			finalPayloadTemplate = updated
		}
	}

	failedChannels := make(map[uint]bool)
	maxRetries := len(routes)
	if maxRetries > 5 {
		maxRetries = 5 // Hard cap to prevent infinite loop
	}

	var httpResp *http.Response
	var lastErrResp []byte
	var lastErrStatus int
	var lastErrType string
	var lastErrMessage string
	var selectedPath *database.ChannelModel
	var selectedChan *database.Channel
	var targetFormat sdktranslator.Format
	var finalPayload []byte
	// successfulUpstreamCancel 是选中的（最终成功响应的）upstream HTTP context cancel。
	// 必须在请求处理结束时调用——无论正常完成还是 SSE 客户端断连，
	// 都能让 net/http 关闭上游连接，避免 fasthttp ctx 不传播 RST 导致的连接 hang。
	var successfulUpstreamCancel context.CancelFunc = func() {} // no-op fallback

	for attempt := 0; attempt < maxRetries; attempt++ {
		// fix CRITICAL Sprint5-M2：重试前指数退避 + jitter，给上游 thundering herd 缓冲
		// attempt=0 backoff=0 不退避。第 1/2/3/4 次重试退避 100ms / 200ms / 400ms / 800ms（+ 0-50% jitter）。
		if backoff := computeRetryBackoff(attempt); backoff > 0 {
			select {
			case <-time.After(backoff):
			case <-c.Context().Done():
				// 用户已断开 → 不再继续重试
				lastErrStatus = 499
				lastErrType = "client_disconnect_during_retry"
				lastErrMessage = "client disconnected during retry backoff"
				goto retryLoopExhausted
			}
		}

		// 1. Filter out failed routes
		// fix CRITICAL Sprint5-M2：除了本请求内已失败的 channel，还要跳过被 circuit breaker
		// 打开（open / half-open 已有 probe inflight）的 channel——防止本请求继续打挂的上游。
		var availableRoutes []*database.ChannelModel
		totalWeight := 0
		skippedRateLimited := 0
		skippedConfigUnhealthy := 0
		for _, r := range routes {
			if failedChannels[r.ChannelID] {
				continue
			}
			if IsChannelRateLimited(r.ChannelID) {
				skippedRateLimited++
				continue
			}
			if IsChannelModelUnhealthy(r.ChannelID, modelName) {
				skippedConfigUnhealthy++
				continue
			}
			if IsChannelCircuitOpen(r.ChannelID) {
				continue // 跨请求级 breaker 跳过；本请求不消耗其 retry slot
			}
			availableRoutes = append(availableRoutes, r)
			totalWeight += r.Weight
		}

		if len(availableRoutes) == 0 {
			if lastErrStatus == 0 && skippedRateLimited > 0 {
				lastErrStatus = http.StatusTooManyRequests
				lastErrType = "upstream_rate_limited"
				lastErrMessage = "all upstream channels are rate limited"
				lastErrResp = []byte(`{"error":{"message":"all upstream channels are rate limited","type":"upstream_rate_limited"}}`)
			} else if lastErrStatus == 0 && skippedConfigUnhealthy > 0 {
				lastErrStatus = http.StatusNotFound
				lastErrType = "channel_model_unhealthy"
				lastErrMessage = "all configured upstream routes for model are unhealthy"
				lastErrResp = []byte(`{"error":{"message":"all configured upstream routes for model are unhealthy","type":"channel_model_unhealthy"}}`)
			}
			break // No more healthy routes left
		}

		// 2. Select route
		if totalWeight <= 0 {
			selectedPath = availableRoutes[0]
		} else {
			// fix Major（自审第六轮）：math/rand 全局源对高并发负载均衡有竞争锁 + 可被攻击者
			// 通过观察上游响应时序预测下一跳。math/rand/v2 用 ChaCha8 + per-call source，
			// goroutine 安全且无全局锁。
			rNum := mrand.IntN(totalWeight)
			acc := 0
			for _, r := range availableRoutes {
				acc += r.Weight
				if rNum < acc {
					selectedPath = r
					break
				}
			}
		}
		selectedChan = channelMapRef[selectedPath.ChannelID]
		// fix Major（codex 第四轮）：route cache 与 channel map 是分锁快照，
		// 高并发下 admin 删除/禁用 channel 后 routes 引用的 channelID 可能在 channelMap 已不存在。
		// 不做 nil 检查会立即在下面 selectedChan.BaseURL 解引用 panic 拉垮整个进程。
		if selectedChan == nil {
			failedChannels[selectedPath.ChannelID] = true
			lastErrStatus = 502
			lastErrType = "channel_unavailable"
			lastErrMessage = "channel was disabled or removed mid-flight"
			lastErrResp = []byte(`{"error":{"message":"channel was disabled or removed mid-flight","type":"channel_unavailable"}}`)
			continue
		}

		// 3. Request Translation & Engine config
		finalPayload = make([]byte, len(finalPayloadTemplate))
		copy(finalPayload, finalPayloadTemplate)

		upstreamURL := strings.TrimRight(selectedChan.BaseURL, "/")
		pathSuffix := path

		channelType := NormalizeChannelType(selectedChan.Type)
		switch channelType {
		case ChannelTypeAnthropic:
			targetFormat = sdktranslator.FormatClaude
			upstreamURL += "/v1/messages"
		case ChannelTypeGemini:
			targetFormat = sdktranslator.FormatGemini
			action := "generateContent"
			if isStream {
				action = "streamGenerateContent"
			}
			upstreamURL += "/v1beta/models/" + url.PathEscape(modelName) + ":" + action + "?key=" + selectedChan.Key
		case ChannelTypeGoogleCLI:
			targetFormat = sdktranslator.FormatGeminiCLI
			upstreamURL += pathSuffix
		case ChannelTypeCodex:
			targetFormat = sdktranslator.FormatCodex
			upstreamURL += pathSuffix
		case ChannelTypeCLIProxy:
			// CLIProxyAPI is already a multi-protocol gateway. Preserve the client
			// protocol and path so Claude Code tools, Codex responses, and OpenAI
			// chat payloads are not cross-translated before reaching it. Claude
			// desktop often appends /v1/messages to a base URL that already ends in
			// /v1; normalize that compat path before forwarding to CLIProxyAPI.
			targetFormat = srcFormat
			upstreamURL += normalizeCLIProxyUpstreamPath(pathSuffix, srcFormat)
		case ChannelTypeOpenAI:
			targetFormat = sdktranslator.FormatOpenAI
			upstreamURL += pathSuffix
		default:
			failedChannels[selectedPath.ChannelID] = true
			lastErrStatus = 502
			lastErrType = "channel_misconfigured"
			lastErrMessage = "unsupported channel type"
			lastErrResp = []byte(`{"error":{"message":"unsupported channel type","type":"channel_misconfigured"}}`)
			log.Printf("[CHANNEL-MISCONFIG] channel=%d unsupported type=%q", selectedPath.ChannelID, selectedChan.Type)
			continue
		}

		// 仅在源格式与上游目标格式不一致时才执行翻译。
		// Anthropic 客户端 → Anthropic 上游：跳过翻译，直接透传原生 Messages 格式。
		if srcFormat != targetFormat {
			finalPayload = sdktranslator.TranslateRequest(srcFormat, targetFormat, modelName, finalPayload, isStream)
		}
		// 4. HTTP Client allocation
		// fix Major（codex 第九轮）：fasthttp RequestCtx 不在客户端 RST 时被取消，
		// 仅在 server.Shutdown 时取消。如果用 c.Context() 直接，stream timeout=0 + 客户端断开
		// → 上游连接长期占用、token 仍在计费。
		// 改为 derive cancelable child context；SSE 写失败时显式 cancel 中止 upstream Read。
		// 选中成功的 upstream 后，把其 cancel 函数保存到 successfulUpstreamCancel，
		// 让下面 SSE BodyStreamWriter 的 cleanup（断连/正常完成）都能调用到。
		httpReq, upstreamCancel, err := newCancelableUpstreamPostRequest(c.Context(), upstreamURL, finalPayload)
		if err != nil {
			failedChannels[selectedPath.ChannelID] = true
			lastErrStatus = 502
			lastErrType = "bad_gateway"
			lastErrMessage = err.Error()
			continue
		}

		httpReq.Header.Set("Content-Type", "application/json")
		if channelType == ChannelTypeOpenAI || channelType == ChannelTypeGoogleCLI || channelType == ChannelTypeCodex || channelType == ChannelTypeCLIProxy {
			httpReq.Header.Set("Authorization", "Bearer "+selectedChan.Key)
		} else if channelType == ChannelTypeAnthropic {
			httpReq.Header.Set("x-api-key", selectedChan.Key)
			httpReq.Header.Set("anthropic-version", "2023-06-01")
		}

		if isStream {
			httpReq.Header.Set("Accept", "text/event-stream")
			httpReq.Header.Set("Cache-Control", "no-cache")
			httpReq.Header.Set("Connection", "keep-alive")
		}

		if selectedChan.Headers != "" {
			var customHeaders map[string]string
			// fix LOW（codex 第十九轮）：原 if err==nil 静默吞 unmarshal 失败 → admin 配错 channel.headers 后
			// 自定义头不生效但毫无诊断；改为 log 异常（仅一次/请求，频率可控）。
			if err := json.Unmarshal([]byte(selectedChan.Headers), &customHeaders); err == nil {
				for k, v := range customHeaders {
					httpReq.Header.Set(k, v)
				}
			} else {
				log.Printf("[STREAM] channel %d invalid Headers json: %v (raw=%q)", selectedChan.ID, err, selectedChan.Headers)
			}
		}

		upstreamTimeout := nonStreamUpstreamTimeout()
		if isStream {
			upstreamTimeout = 0
		}
		httpClient := &http.Client{
			Transport: getTransport(selectedChan.ProxyURL),
			Timeout:   upstreamTimeout,
		}

		// 5. Execute Request
		resp, err := httpClient.Do(httpReq)
		if err != nil {
			upstreamCancel() // 失败的 upstream ctx 立即释放
			failedChannels[selectedPath.ChannelID] = true
			// fix CRITICAL Sprint5-M2：dial / connect 失败也累计到 circuit breaker
			// （TCP connect failure / DNS / TLS handshake 失败都属上游故障，应触发熔断）
			MarkChannelFailure(selectedPath.ChannelID, 0)
			lastErrStatus = 502
			lastErrType = "bad_gateway"
			lastErrMessage = err.Error()
			// fix CRITICAL（codex 第七轮）：原 message 拼了 err.Error()。
			// httpReq.URL 在 err 文本里包含完整 URL（含 Gemini 的 ?key=APIKEY 查询参数），
			// 连接失败时直接把 API 密钥回显给前端，等同凭证泄露。
			// 详细 err 仅记日志（且经过 sanitizeError 兜底脱敏），对外只回固定文案。
			log.Printf("[UPSTREAM-ERR-DIAL] channel=%d err=%s", selectedPath.ChannelID, sanitizeError(err.Error(), 256))
			errPayload := map[string]any{
				"error": map[string]any{
					"message": "upstream connection failed (channel rotated)",
					"type":    "bad_gateway",
				},
			}
			lastErrResp, _ = json.Marshal(errPayload)
			continue
		}

		// 6. Classify upstream status before deciding retry/circuit behavior.
		action := classifyUpstreamStatus(resp.StatusCode)
		switch action {
		case StatusActionSuccess:
			httpResp = resp
			// 保留这个 cancel 给 SSE 路径，确保最后能取消上游连接
			successfulUpstreamCancel = upstreamCancel
			MarkChannelSuccess(selectedPath.ChannelID)
			break
		case StatusActionClientError:
			httpResp = resp
			successfulUpstreamCancel = upstreamCancel
			releaseChannelProbe(selectedPath.ChannelID)
			break
		case StatusActionRateLimit:
			upstreamCancel()
			failedChannels[selectedPath.ChannelID] = true
			retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
			setChannelRateLimitCooldown(selectedPath.ChannelID, retryAfter)
			releaseChannelProbe(selectedPath.ChannelID)
			lastErrStatus = resp.StatusCode
			lastErrType = "upstream_rate_limited"
			respBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
			resp.Body.Close()
			lastErrMessage = string(respBytes)
			log.Printf("[UPSTREAM-RATE-LIMIT] channel=%d status=%d retry_after=%s body=%q", selectedPath.ChannelID, resp.StatusCode, resp.Header.Get("Retry-After"), truncForLog(respBytes, 256))
			lastErrResp, _ = json.Marshal(map[string]any{
				"error": map[string]any{
					"message": fmt.Sprintf("upstream returned %d (channel rate-limited)", resp.StatusCode),
					"type":    "upstream_rate_limited",
				},
			})
			continue
		case StatusActionConfigError:
			upstreamCancel()
			failedChannels[selectedPath.ChannelID] = true
			respBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
			resp.Body.Close()
			routeNotFound := isPlainCLIProxyRouteNotFound(selectedChan.Type, resp.StatusCode, respBytes)
			if !routeNotFound {
				markChannelModelUnhealthy(selectedPath.ChannelID, modelName)
			}
			releaseChannelProbe(selectedPath.ChannelID)
			lastErrStatus = resp.StatusCode
			lastErrMessage = string(respBytes)
			if routeNotFound {
				lastErrType = "upstream_route_not_found"
				log.Printf("[UPSTREAM-ROUTE-404] channel=%d model=%s path=%s body=%q", selectedPath.ChannelID, modelName, path, truncForLog(respBytes, 256))
				lastErrResp, _ = json.Marshal(map[string]any{
					"error": map[string]any{
						"message": "upstream route not found",
						"type":    "upstream_route_not_found",
					},
				})
			} else {
				lastErrType = "channel_model_unhealthy"
				log.Printf("[UPSTREAM-CONFIG-ERR] channel=%d model=%s status=%d body=%q", selectedPath.ChannelID, modelName, resp.StatusCode, truncForLog(respBytes, 256))
				lastErrResp, _ = json.Marshal(map[string]any{
					"error": map[string]any{
						"message": fmt.Sprintf("upstream returned %d for configured model (route marked unhealthy)", resp.StatusCode),
						"type":    "channel_model_unhealthy",
					},
				})
			}
			continue
		case StatusActionAuthError:
			upstreamCancel()
			failedChannels[selectedPath.ChannelID] = true
			MarkChannelSoftFailure(selectedPath.ChannelID)
			releaseChannelProbe(selectedPath.ChannelID)
			lastErrStatus = resp.StatusCode
			lastErrType = "upstream_auth_error"
			respBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
			resp.Body.Close()
			lastErrMessage = string(respBytes)
			log.Printf("[UPSTREAM-AUTH-ERR] channel=%d status=%d body=%q", selectedPath.ChannelID, resp.StatusCode, truncForLog(respBytes, 256))
			lastErrResp, _ = json.Marshal(map[string]any{
				"error": map[string]any{
					"message": fmt.Sprintf("upstream returned %d (channel rotated)", resp.StatusCode),
					"type":    "upstream_auth_error",
				},
			})
			continue
		case StatusActionRetryableTransient, StatusActionUpstreamFatal, StatusActionUnknown:
			upstreamCancel() // 失败的 upstream ctx 立即释放
			failedChannels[selectedPath.ChannelID] = true
			MarkChannelFailure(selectedPath.ChannelID, resp.StatusCode)
			lastErrStatus = resp.StatusCode
			lastErrType = "upstream_error"
			// fix Major（codex 第六轮）：原实现把上游 raw body 原样回给客户端，
			// 可能泄露上游 stack trace / SQL / 内部地址 / API key 回显。
			// 仅记录到服务端日志（带 channel + status），对客户端返回脱敏的通用消息。
			respBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
			resp.Body.Close()
			lastErrMessage = string(respBytes)
			log.Printf("[UPSTREAM-ERR] channel=%d status=%d action=%d body=%q", selectedPath.ChannelID, resp.StatusCode, action, truncForLog(respBytes, 256))
			lastErrResp, _ = json.Marshal(map[string]any{
				"error": map[string]any{
					"message": fmt.Sprintf("upstream returned %d (channel rotated)", resp.StatusCode),
					"type":    "upstream_error",
				},
			})
			continue
		}
		break
	}

retryLoopExhausted:
	if httpResp == nil {
		// 全部 upstream 失败：所有 cancel 已在 continue 处调用，无需额外清理
		if lastErrStatus == 0 {
			lastErrStatus = 502
			lastErrType = "backend_exhausted"
			lastErrMessage = "All upstream channels exhausted or failing"
			lastErrResp = []byte(`{"error":{"message":"All upstream channels exhausted or failing", "type": "backend_exhausted"}}`)
		}
		recordProxyApiLog(user.ID, token, modelName, lastErrStatus, clientIP, startTime, path, lastErrType, lastErrMessage)

		c.Set("Content-Type", "application/json")
		return c.Status(lastErrStatus).Send(lastErrResp)
	}

	// Helper for Atomic Quota Deduction
	apiErrorType := ""
	apiErrorMessage := ""
	// P8.1：原 inner type 提到包级（proxy/text_billing.go）。这里保留 type alias 让闭包
	// 内部代码无需大规模 rename；后续 P8.3 把闭包整体抽到顶层时直接用包级类型。
	type manualBillingStateInput = ManualBillingStateInput
	type deliveredCostEstimate = DeliveredCostEstimate
	// P8.2：以下 4 个 closure 改为薄包装，把实现委托到 proxy/text_billing.go
	// 顶层函数。捕获状态（selectedChan / httpResp / user / startTime / modelName）
	// 只在调用时按需取，handler 仍可在 retry 循环里换 selectedChan/httpResp。
	selectedChannelTypeForBilling := func() string {
		return channelTypeOfSelected(selectedChan)
	}
	// upstreamRequestID 闭包在 P8.4 后已不需要（RecordManualBillingState /
	// CommitTextTurn 都直接用顶层 UpstreamRequestID（ctx.UpstreamHeaders, ...））。
	// P8.3：writeBillingWithRetry / RecordManualBillingState 全部转为调 text_billing.go
	// 顶层函数；deductQuota 闭包内的 3-retry 还是 inline，P8.4 抽 CommitTextTurn 时一起搬。
	// P8.3：recordManualBillingState 整段抽到顶层 RecordManualBillingState；
	// 闭包改为薄包装，组装 CommitTextContext 后委托。
	buildCommitContext := func() CommitTextContext {
		var hdr http.Header
		if httpResp != nil {
			hdr = httpResp.Header
		}
		return CommitTextContext{
			User:              user,
			Token:             token,
			SubToken:          subToken,
			IsSubToken:        isSubToken,
			ModelName:         modelName,
			Body:              body,
			Path:              path,
			ClientIP:          clientIP,
			StartTime:         startTime,
			IsStream:          isStream,
			FallbackUserOptIn: fallbackUserOptIn,
			SelectedPath:      selectedPath,
			SelectedChan:      selectedChan,
			EngineDecision:    engineDecision,
			UpstreamHeaders:   hdr,
		}
	}
	recordManualBillingState := func(in manualBillingStateInput) {
		RecordManualBillingState(buildCommitContext(), in)
	}
	estimateDeliveredCost := func(deliveredBytes int64, reasoningTokens int) deliveredCostEstimate {
		return EstimateDeliveredCost(modelName, body, deliveredBytes, reasoningTokens, selectedChannelTypeForBilling(), fallbackUserOptIn)
	}
	deductQuota := func(promptTokens, completionTokens, cachedTokens, cacheWriteTokens, cacheWrite5mTokens, cacheWrite1hTokens, reasoningTokens, status int, deliveredBytes int64) bool {
		usage := usageTokenCounts{
			PromptTokens:       promptTokens,
			CompletionTokens:   completionTokens,
			CachedTokens:       cachedTokens,
			CacheWriteTokens:   cacheWriteTokens,
			CacheWrite5mTokens: cacheWrite5mTokens,
			CacheWrite1hTokens: cacheWrite1hTokens,
			ReasoningTokens:    reasoningTokens,
		}
		// P8.4：整段 deductQuota 算式抽到 proxy/text_billing.go CommitTextTurn。
		// 这里维持原 closure 签名 + 返回语义，handler 内调用点零改动。apiErrorType /
		// apiErrorMessage 在 closure 外被 handler mutated（如 upstream_error 路径），
		// closure 捕获是引用类型，调用时读到最新值。
		return CommitTextTurn(buildCommitContext(), usage, status, deliveredBytes, apiErrorType, apiErrorMessage)
	}

	// fix Major（codex 第七轮）：原实现把上游所有响应头透传给客户端，
	// 包括 Set-Cookie / CORS / CSP / 跳转头等可被恶意上游用来污染本站的字段。
	// 改为白名单：只透传与 LLM 协议密切相关的头。
	statusCode := httpResp.StatusCode
	c.Status(statusCode)
	upstreamHeaderAllowlist := map[string]bool{
		"Content-Type":      true,
		"Cache-Control":     true,
		"Pragma":            true,
		"Expires":           true,
		"Anthropic-Version": true,
		"Openai-Version":    true,
		"X-Request-Id":      true,
	}
	for k, v := range httpResp.Header {
		if upstreamHeaderAllowlist[k] && len(v) > 0 {
			c.Set(k, v[0])
		}
	}
	if ct := httpResp.Header.Get("Content-Type"); ct != "" {
		c.Set("Content-Type", ct)
	}
	setModelAuditHeaders(c, modelName, modelName, fallbackUserOptIn, "")

	// Non-Stream handling
	if !isStream || statusCode >= 300 {
		defer successfulUpstreamCancel() // 释放上游 ctx
		defer httpResp.Body.Close()
		bodyCopy, _ := io.ReadAll(httpResp.Body)

		// 把上游响应翻译回客户端使用的协议（srcFormat），而不是硬编码 OpenAI。
		if statusCode >= 200 && statusCode < 300 && srcFormat != targetFormat {
			var param any
			bodyCopy = sdktranslator.TranslateNonStream(context.Background(), targetFormat, srcFormat, modelName, body, finalPayload, bodyCopy, &param)
		}
		// fix Major（codex 第七轮）：状态码 >= 400 的响应 body 不能原样透传——
		// 上游可能在 4xx 错误里回显请求 URL（含 ?key= API 密钥）/ stack / 内部地址。
		// 详细 body 仅服务端日志记录，对客户端返回脱敏的统一 error。
		if statusCode >= 400 {
			apiErrorType = "upstream_error"
			apiErrorMessage = string(bodyCopy)
			log.Printf("[UPSTREAM-ERR-NONRETRY] channel=%d status=%d body=%s", selectedPath.ChannelID, statusCode, sanitizeError(truncForLog(bodyCopy, 1024), 1024))
			generic, _ := json.Marshal(map[string]any{
				"error": map[string]any{
					"message": fmt.Sprintf("upstream returned %d", statusCode),
					"type":    "upstream_error",
				},
			})
			bodyCopy = generic
			c.Set("Content-Type", "application/json")
		}
		if statusCode >= 200 && statusCode < 300 && isCountTokensRequest {
			inputTokens := int(gjson.GetBytes(bodyCopy, "input_tokens").Int())
			if inputTokens < 0 {
				inputTokens = 0
			}
			// Anthropic token counting is a free helper endpoint. Keep an ApiLog
			// for observability, but never consume subscription/balance quota and
			// never write BillingEntry/Revenue rows.
			resolution := ResolveBillingRules(modelName, body, 0, selectedChannelTypeForBilling(), fallbackUserOptIn).WithCosts(0)
			if err := database.DB.Create(&database.ApiLog{
				UserID:              user.ID,
				TokenName:           HashTokenForLog(token),
				ModelName:           modelName,
				RequestedModel:      resolution.RequestedModel,
				ServedModel:         resolution.ServedModel,
				PromptTokens:        inputTokens,
				Cost:                0,
				ChargedCost:         0,
				ModelWeight:         resolution.ModelWeight,
				HealthMultiplier:    resolution.HealthMultiplier,
				BillingRulesVersion: resolution.BillingRulesVersion,
				FallbackUserOptIn:   resolution.FallbackUserOptIn,
				FallbackReason:      sanitizeError(resolution.FallbackReason, 160),
				UpstreamProvider:    sanitizeError(strings.ToLower(strings.TrimSpace(selectedChannelTypeForBilling())), 64),
				Latency:             time.Since(startTime).Milliseconds(),
				Status:              statusCode,
				IPAddress:           clientIP,
				RequestPath:         sanitizeError(path, 160),
				CreatedAt:           time.Now(),
			}).Error; err != nil {
				log.Printf("[BILLING-AUDIT-FAIL] free count_tokens api_log create failed user=%d model=%s: %v", user.ID, modelName, err)
			}
			return c.Send(bodyCopy)
		}

		usageBlock := gjson.GetBytes(bodyCopy, "usage")
		if !usageBlock.Exists() {
			usageBlock = gjson.GetBytes(bodyCopy, "usageMetadata")
		}
		usage := extractUsageTokenCounts(usageBlock)
		if statusCode >= 200 && statusCode < 300 && !usage.HasAny() {
			log.Printf("[BILLING-UNMETERED] user=%d model=%s non-stream upstream omitted usage metadata; refusing unmetered success", user.ID, modelName)
			recordProxyApiLog(user.ID, token, modelName, 502, clientIP, startTime, path, "upstream_unmetered", "upstream response omitted usage metadata")
			c.Set("Content-Type", "application/json")
			return c.Status(502).JSON(fiber.Map{"error": fiber.Map{
				"message": "upstream response omitted usage metadata",
				"type":    "upstream_unmetered",
			}})
		}
		if statusCode >= 200 && statusCode < 300 && usage.HasAny() && !usage.HasBillableTokens() {
			log.Printf("[BILLING-UNMETERED] user=%d model=%s non-stream upstream returned usage metadata with zero billable tokens", user.ID, modelName)
			recordManualBillingState(manualBillingStateInput{
				BillingState:         database.BillingStateUpstreamUnmetered,
				ReasonTag:            "UPSTREAM-UNMETERED",
				ErrorType:            "upstream_unmetered",
				ErrorMessage:         "upstream usage metadata had zero billable tokens",
				Status:               statusCode,
				PromptTokens:         usage.PromptTokens,
				CompletionTokens:     usage.CompletionTokens,
				CachedTokens:         usage.CachedTokens,
				CacheWriteTokens:     usage.CacheWriteTokens,
				CacheWrite5mTokens:   usage.CacheWrite5mTokens,
				CacheWrite1hTokens:   usage.CacheWrite1hTokens,
				ReasoningTokens:      usage.ReasoningTokens,
				EstimatedInputTokens: estimatePrecheckTokens(body),
			})
			return c.Send(bodyCopy)
		}
		if !deductQuota(usage.PromptTokens, usage.CompletionTokens, usage.CachedTokens, usage.CacheWriteTokens, usage.CacheWrite5mTokens, usage.CacheWrite1hTokens, usage.ReasoningTokens, statusCode, 0) {
			c.Set("Content-Type", "application/json")
			return c.Status(502).JSON(fiber.Map{"error": fiber.Map{
				"message": "billing cost calculation failed",
				"type":    "billing_cost_invalid",
			}})
		}

		return c.Send(bodyCopy)
	}

	// 6. Real True Streaming Mode
	c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
		// fix Major（gemini 第七轮）：SSE BodyStreamWriter 是 fasthttp 异步调用的 goroutine，
		// 内部 panic 会冒到 fasthttp 的根 recover（如果有）或直接挂掉整个进程。
		// 显式加一道 recover 让任何流处理路径的 panic 都被捕获 + 日志记录，
		// 不让一个用户的请求拖垮整个服务。
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[STREAM-PANIC] user=%d model=%s recovered: %v", user.ID, modelName, r)
			}
		}()
		// fix Major（codex 第九轮）：客户端 RST / 正常完成 都需要显式取消上游 ctx，
		// 避免 fasthttp 不传播 RST 导致的上游连接 hang + 持续读取浪费 token。
		defer successfulUpstreamCancel()
		defer httpResp.Body.Close()

		scanner := bufio.NewScanner(httpResp.Body)
		// 4MB 足以容纳常见 SSE chunk（含 base64 vision 响应）。可由 SysConfig 调整。
		bufLimit := 4 * 1024 * 1024
		SysConfigMutex.RLock()
		if v := SysConfigCache["stream_scanner_buffer_bytes"]; v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 64*1024 {
				bufLimit = n
			}
		}
		SysConfigMutex.RUnlock()
		scanner.Buffer(make([]byte, 64*1024), bufLimit)

		promptTokens := 0
		completionTokens := 0
		cachedTokens := 0
		cacheWriteTokens := 0
		cacheWrite5mTokens := 0
		cacheWrite1hTokens := 0
		reasoningTokens := 0
		sawUsageMetadata := false
		sawBillableUsage := false
		deliveredBytes := int64(0)
		var param any

		extractUsage := func(jsonData []byte) {
			// OpenAI Chat Completions / Anthropic Messages：usage 在根级
			// OpenAI Responses API (Codex) SSE：usage 嵌套在 response.usage 里
			//   data: {"type":"response.completed","response":{"usage":{"input_tokens":18123,...}}}
			// Gemini 原生 SSE：usageMetadata 在根级。
			usageBlock := gjson.GetBytes(jsonData, "usage")
			if !usageBlock.Exists() {
				usageBlock = gjson.GetBytes(jsonData, "response.usage")
			}
			if !usageBlock.Exists() {
				usageBlock = gjson.GetBytes(jsonData, "usageMetadata")
			}
			if !usageBlock.Exists() {
				return
			}
			usage := extractUsageTokenCounts(usageBlock)
			if usage.HasAny() {
				sawUsageMetadata = true
			}
			if usage.HasBillableTokens() {
				sawBillableUsage = true
			}
			if usage.HasPromptTokens {
				promptTokens = usage.PromptTokens
			}
			if usage.HasCompletionTokens {
				completionTokens = usage.CompletionTokens
			}
			if usage.HasCachedTokens {
				cachedTokens = usage.CachedTokens
			}
			if usage.HasCacheWriteTokens {
				cacheWriteTokens = usage.CacheWriteTokens
				cacheWrite5mTokens = usage.CacheWrite5mTokens
				cacheWrite1hTokens = usage.CacheWrite1hTokens
			}
			if usage.HasReasoningTokens {
				reasoningTokens = usage.ReasoningTokens
			}
		}

		// jsonPayload behaves identically to CLIProxyAPI usage_helpers.go
		jsonPayload := func(line []byte) []byte {
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) == 0 {
				return nil
			}
			if bytes.Equal(trimmed, []byte("data: [DONE]")) || bytes.Equal(trimmed, []byte("[DONE]")) {
				return nil
			}
			if bytes.HasPrefix(trimmed, []byte("event:")) {
				return nil
			}
			if bytes.HasPrefix(trimmed, []byte("data:")) {
				trimmed = bytes.TrimSpace(trimmed[len("data:"):])
			}
			if len(trimmed) == 0 || trimmed[0] != '{' {
				return nil
			}
			return trimmed
		}

		// fix Major（codex 第六轮）：客户端断连时及时退出，避免上游继续读取占用 goroutine + 错误计费。
		// 检测策略：每次 Flush 后查错；w.Flush 内部会把数据交给 fasthttp 写出，
		// 写失败（broken pipe / closed connection）会冒泡。一旦发现 w.Flush() 返回错误就立即 break。
		// 同时 ctx 已通过 c.Context() 关联到 httpReq，断连会让 scanner.Scan 自然退出。
		clientDisconnected := false
		flushOrBail := func() bool {
			if err := w.Flush(); err != nil {
				clientDisconnected = true
				log.Printf("[STREAM-CLIENT-DISCONNECT] user=%d model=%s err=%v", user.ID, modelName, err)
				return false
			}
			return true
		}

		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				w.Write([]byte("\n"))
				if !flushOrBail() {
					break
				}
				continue
			}

			if srcFormat != targetFormat {
				chunks := sdktranslator.TranslateStream(context.Background(), targetFormat, srcFormat, modelName, body, finalPayload, line, &param)
				for _, chunk := range chunks {
					if jsonData := jsonPayload(chunk); jsonData != nil {
						extractUsage(jsonData)
					}

					if len(chunk) > 0 && !bytes.HasPrefix(chunk, []byte("data:")) && !bytes.HasPrefix(chunk, []byte("data: ")) {
						w.Write([]byte("data: "))
					}
					w.Write(chunk)
					w.Write([]byte("\n\n"))
					deliveredBytes += int64(len(chunk))
				}
			} else {
				if jsonData := jsonPayload(line); jsonData != nil {
					extractUsage(jsonData)
				}
				w.Write(line)
				w.Write([]byte("\n"))
				deliveredBytes += int64(len(line))
			}

			if !flushOrBail() {
				break
			}
		}
		// 断连情况下不再写额外 SSE 事件（写不出去）
		if clientDisconnected {
			// fix Major（codex 第九轮）：客户端 RST → 立即 cancel 上游 ctx 释放连接（defer 也会调，
			// 这里显式调一次让上游 Read 立刻返回 err，scanner 退出更快、token 计费更准确）。
			successfulUpstreamCancel()
			// 仍然走 deductQuota（已经接收到的 token 应当计费），但跳过下面的 [DONE] / error 事件
			if !sawUsageMetadata {
				log.Printf("[BILLING-UNMETERED] user=%d model=%s stream disconnected before usage metadata; delivered portion not billed", user.ID, modelName)
				apiErrorType = "client_disconnected_unmetered"
				apiErrorMessage = "client disconnected before usage metadata"
				estimatedCost := estimateDeliveredCost(deliveredBytes, reasoningTokens)
				recordManualBillingState(manualBillingStateInput{
					BillingState:                 database.BillingStatePendingReconcile,
					ReasonTag:                    "CLIENT-DISCONNECT",
					ErrorType:                    apiErrorType,
					ErrorMessage:                 apiErrorMessage,
					Status:                       499,
					PromptTokens:                 promptTokens,
					CompletionTokens:             completionTokens,
					CachedTokens:                 cachedTokens,
					CacheWriteTokens:             cacheWriteTokens,
					CacheWrite5mTokens:           cacheWrite5mTokens,
					CacheWrite1hTokens:           cacheWrite1hTokens,
					ReasoningTokens:              reasoningTokens,
					DeliveredBytes:               deliveredBytes,
					EstimatedInputTokens:         estimatePrecheckTokens(body),
					EstimatedRawCostMicroUSD:     estimatedCost.RawCostMicroUSD,
					EstimatedChargedCostMicroUSD: estimatedCost.ChargedCostMicroUSD,
				})
				return
			}
			if !sawBillableUsage {
				log.Printf("[BILLING-UNMETERED] user=%d model=%s stream disconnected after zero-token usage metadata", user.ID, modelName)
				recordManualBillingState(manualBillingStateInput{
					BillingState:         database.BillingStateUpstreamUnmetered,
					ReasonTag:            "UPSTREAM-UNMETERED",
					ErrorType:            "upstream_unmetered",
					ErrorMessage:         "upstream usage metadata had zero billable tokens",
					Status:               200,
					PromptTokens:         promptTokens,
					CompletionTokens:     completionTokens,
					CachedTokens:         cachedTokens,
					CacheWriteTokens:     cacheWriteTokens,
					CacheWrite5mTokens:   cacheWrite5mTokens,
					CacheWrite1hTokens:   cacheWrite1hTokens,
					ReasoningTokens:      reasoningTokens,
					DeliveredBytes:       deliveredBytes,
					EstimatedInputTokens: estimatePrecheckTokens(body),
				})
				return
			}
			deductQuota(promptTokens, completionTokens, cachedTokens, cacheWriteTokens, cacheWrite5mTokens, cacheWrite1hTokens, reasoningTokens, 200, deliveredBytes)
			return
		}

		// fix Minor（gemini 第六轮）：scanner.Scan() 退出后必须查 Err()，否则
		// bufio.ErrTooLong 等错误会被静默吞掉（特别是 vision/large base64 chunk 超过 4MB
		// 缓冲区时）。客户端响应被截断、服务端无任何日志、也不会触发降级——难以排查。
		// 把错误浮上来；写一条 SSE error 事件让客户端能感知中断。
		if scanErr := scanner.Err(); scanErr != nil {
			log.Printf("[STREAM-SCANNER-ERR] user=%d model=%s err=%v (consider raising stream_scanner_buffer_bytes if ErrTooLong)", user.ID, modelName, scanErr)
			// 给客户端发一条 SSE error 事件再正常关闭——这样前端 ChatBox 能感知到截断
			fmt.Fprintf(w, "event: error\ndata: {\"error\":{\"message\":\"upstream stream interrupted\",\"type\":\"stream_truncated\"}}\n\n")
			w.Flush()
		}

		if (srcFormat == sdktranslator.FormatOpenAI || srcFormat == sdktranslator.FormatOpenAIResponse) && targetFormat != sdktranslator.FormatOpenAI {
			w.Write([]byte("data: [DONE]\n\n"))
			w.Flush()
		}

		if !sawUsageMetadata {
			// fix CRITICAL Sprint1-P0-5：上游 SSE 流结束但未给 usage metadata，
			// 上游已向客户端交付内容，平台必须记账。旧实现 `deductQuota(..., 502, ...)` 走
			// failedRequest 分支 cost=0 → "免费消耗"。改为写 pending_reconcile，与客户端
			// 断连路径（line 1779-1801）口径一致：按 deliveredBytes 估算成本供 admin 对账。
			log.Printf("[BILLING-PENDING] user=%d model=%s stream upstream omitted usage metadata after delivery; recording pending_reconcile (admin reconcile)", user.ID, modelName)
			apiErrorType = "upstream_unmetered"
			apiErrorMessage = "upstream stream omitted usage metadata"
			estimatedCost := estimateDeliveredCost(deliveredBytes, reasoningTokens)
			recordManualBillingState(manualBillingStateInput{
				BillingState:                 database.BillingStatePendingReconcile,
				ReasonTag:                    "UPSTREAM-NO-USAGE",
				ErrorType:                    apiErrorType,
				ErrorMessage:                 apiErrorMessage,
				Status:                       502,
				PromptTokens:                 promptTokens,
				CompletionTokens:             completionTokens,
				CachedTokens:                 cachedTokens,
				CacheWriteTokens:             cacheWriteTokens,
				CacheWrite5mTokens:           cacheWrite5mTokens,
				CacheWrite1hTokens:           cacheWrite1hTokens,
				ReasoningTokens:              reasoningTokens,
				DeliveredBytes:               deliveredBytes,
				EstimatedInputTokens:         estimatePrecheckTokens(body),
				EstimatedRawCostMicroUSD:     estimatedCost.RawCostMicroUSD,
				EstimatedChargedCostMicroUSD: estimatedCost.ChargedCostMicroUSD,
			})
			return
		}

		if !sawBillableUsage {
			log.Printf("[BILLING-UNMETERED] user=%d model=%s stream upstream returned usage metadata with zero billable tokens", user.ID, modelName)
			recordManualBillingState(manualBillingStateInput{
				BillingState:         database.BillingStateUpstreamUnmetered,
				ReasonTag:            "UPSTREAM-UNMETERED",
				ErrorType:            "upstream_unmetered",
				ErrorMessage:         "upstream usage metadata had zero billable tokens",
				Status:               200,
				PromptTokens:         promptTokens,
				CompletionTokens:     completionTokens,
				CachedTokens:         cachedTokens,
				CacheWriteTokens:     cacheWriteTokens,
				CacheWrite5mTokens:   cacheWrite5mTokens,
				CacheWrite1hTokens:   cacheWrite1hTokens,
				ReasoningTokens:      reasoningTokens,
				DeliveredBytes:       deliveredBytes,
				EstimatedInputTokens: estimatePrecheckTokens(body),
			})
			return
		}

		deductQuota(promptTokens, completionTokens, cachedTokens, cacheWriteTokens, cacheWrite5mTokens, cacheWrite1hTokens, reasoningTokens, 200, deliveredBytes)
	})

	return nil
}

// estimatePrecheckBalanceDelta 计算余额预检的悲观估算（micro_usd），用于 CheckBalanceConsumeAllowed。
//
// fix MAJOR M4（codex 第二十轮）：原实现只乘 inputTokens × 平铺 $1/1M。
// 修复：在 RouteCache 中找最贵路由，按真实 input/output 单价计算。
//
// fix MAJOR M22-A1 Phase 1（codex 第二十三轮）：返回值从 float64 USD 改为 int64 micro_usd。
// 数学：tokens × (USD/1M tok) = micro_usd（恒等：USD/1M tok 单位 × token 数 = USD/1M = micro_usd）
//
// 找不到路由（极少数情况，路由刚被同步）→ 用保守上界 $30/1M（覆盖 Claude Opus / GPT-4 Turbo）。

