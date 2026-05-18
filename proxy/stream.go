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
	"math"
	"math/big"
	mrand "math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"daof-cpa/database"

	"github.com/gofiber/fiber/v2"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"gorm.io/gorm"

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

type invalidAuthLogBucket struct {
	windowStart time.Time
	count       int
	suppressed  int
}

var (
	invalidAuthLogMu          sync.Mutex
	invalidAuthLogBuckets     = map[string]*invalidAuthLogBucket{}
	invalidAuthLogLastCleanup time.Time
)

type StatusAction int

const (
	StatusActionSuccess StatusAction = iota
	StatusActionRetryableTransient
	StatusActionRateLimit
	StatusActionUpstreamFatal
	StatusActionConfigError
	StatusActionClientError
	StatusActionAuthError
	StatusActionUnknown
)

func classifyUpstreamStatus(status int) StatusAction {
	if status >= 200 && status <= 299 {
		return StatusActionSuccess
	}
	switch status {
	case http.StatusRequestTimeout, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return StatusActionRetryableTransient
	case http.StatusTooManyRequests:
		return StatusActionRateLimit
	case http.StatusNotFound, http.StatusGone:
		return StatusActionConfigError
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return StatusActionClientError
	case http.StatusUnauthorized, http.StatusForbidden:
		return StatusActionAuthError
	}
	if status >= 500 {
		return StatusActionUpstreamFatal
	}
	return StatusActionUnknown
}

func parseRetryAfter(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(raw); err == nil {
		d := time.Until(at)
		if d <= 0 {
			return 0
		}
		return d
	}
	return 0
}

// safeTransport 是 http.DefaultTransport 的派生，带 DNS-rebinding-resistant DialContext。
// 仅在没有 proxyURL 时使用（直连上游）；走 HTTP 代理时由代理服务器自己解析 host，
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
func truncForLog(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "...(truncated)"
}

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
func estimateTextPrecheckTokens(s string) int {
	cjkRunes := 0
	asciiLikeRunes := 0
	otherRunes := 0
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Han, r), unicode.Is(unicode.Hiragana, r), unicode.Is(unicode.Katakana, r), unicode.Is(unicode.Hangul, r):
			cjkRunes++
		case r <= 0x7f:
			asciiLikeRunes++
		default:
			otherRunes++
		}
	}
	ceilDiv := func(n, d int) int {
		if n <= 0 {
			return 0
		}
		return (n + d - 1) / d
	}
	return cjkRunes + ceilDiv(asciiLikeRunes, 2) + ceilDiv(otherRunes, 2)
}

// estimatePrecheckTokens 给 Decide(IsPrecheck=true) 用的粗粒度 token 估算。
//
// 真实 token 数要等上游 tokenizer 跑过才能拿到；precheck 不能等。
//
// 累加范围：messages/prompt/input + Anthropic 顶层 system + tools/functions schema 字符数。
// 多模态非文本部分（image/audio/video/file）按固定常数加 token。
func estimatePrecheckTokens(body []byte) int {
	totalTokens := 0
	addText := func(s string) {
		totalTokens += estimateTextPrecheckTokens(s)
	}
	// 多模态非文本占位（image/audio/video）— 每个 part 加保守常数
	nonTextParts := 0
	addContentPart := func(p gjson.Result) {
		if p.Type == gjson.String {
			addText(p.String())
			return
		}
		t := strings.ToLower(strings.TrimSpace(p.Get("type").String()))
		switch t {
		case "text", "input_text", "output_text", "":
			if text := p.Get("text"); text.Exists() {
				addText(text.String())
				return
			}
			if p.Get("image_url").Exists() || p.Get("source").Exists() || p.Get("inline_data").Exists() || p.Get("file_data").Exists() {
				nonTextParts++
			}
		case "image", "image_url", "input_image", "input_audio", "audio", "video", "file", "input_file":
			nonTextParts++
		default:
			if text := p.Get("text"); text.Exists() {
				addText(text.String())
			} else {
				nonTextParts++
			}
		}
	}
	addContent := func(content gjson.Result) {
		if !content.Exists() {
			return
		}
		if content.IsArray() {
			content.ForEach(func(_, p gjson.Result) bool {
				addContentPart(p)
				return true
			})
			return
		}
		addContentPart(content)
	}

	// messages: [{role, content}] 数组（OpenAI/Anthropic 兼容）
	if msgs := gjson.GetBytes(body, "messages"); msgs.IsArray() {
		msgs.ForEach(func(_, m gjson.Result) bool {
			addText(m.Get("role").String())
			addContent(m.Get("content"))
			return true
		})
	}

	// Anthropic 顶层 system（与 messages 平级）
	if sys := gjson.GetBytes(body, "system"); sys.Exists() {
		if sys.IsArray() {
			sys.ForEach(func(_, p gjson.Result) bool {
				addText(p.Get("text").String())
				return true
			})
		} else {
			addText(sys.String())
		}
	}

	// prompt: 字符串或字符串数组（completions API）
	if prompt := gjson.GetBytes(body, "prompt"); prompt.Exists() {
		if prompt.IsArray() {
			prompt.ForEach(func(_, p gjson.Result) bool {
				addText(p.String())
				return true
			})
		} else {
			addText(prompt.String())
		}
	}
	// input: OpenAI Responses API / embeddings API
	if input := gjson.GetBytes(body, "input"); input.Exists() {
		if input.IsArray() {
			input.ForEach(func(_, item gjson.Result) bool {
				if item.Type == gjson.String {
					addText(item.String())
					return true
				}
				addText(item.Get("role").String())
				addContent(item.Get("content"))
				if text := item.Get("text"); text.Exists() {
					addText(text.String())
				}
				if output := item.Get("output"); output.Exists() {
					addContent(output)
				}
				if arguments := item.Get("arguments"); arguments.Exists() {
					addText(arguments.String())
				}
				return true
			})
		} else {
			addText(input.String())
		}
	}
	if ins := gjson.GetBytes(body, "instructions"); ins.Exists() {
		addText(ins.String())
	}
	// Gemini contents / systemInstruction
	if contents := gjson.GetBytes(body, "contents"); contents.IsArray() {
		contents.ForEach(func(_, c gjson.Result) bool {
			addText(c.Get("role").String())
			c.Get("parts").ForEach(func(_, p gjson.Result) bool {
				if text := p.Get("text"); text.Exists() {
					addText(text.String())
				} else {
					nonTextParts++
				}
				return true
			})
			return true
		})
	}
	if sys := gjson.GetBytes(body, "systemInstruction.parts"); sys.IsArray() {
		sys.ForEach(func(_, p gjson.Result) bool {
			if text := p.Get("text"); text.Exists() {
				addText(text.String())
			}
			return true
		})
	}
	// tools / functions schema（OpenAI tool calling）— description + parameters JSON 都计入
	if tools := gjson.GetBytes(body, "tools"); tools.IsArray() {
		tools.ForEach(func(_, p gjson.Result) bool {
			addText(p.Raw) // 整个 tool 定义当文本估算
			return true
		})
	}
	if functions := gjson.GetBytes(body, "functions"); functions.IsArray() {
		functions.ForEach(func(_, p gjson.Result) bool {
			addText(p.Raw)
			return true
		})
	}

	estimated := totalTokens + nonTextParts*200
	if estimated < 1 && totalTokens > 0 {
		estimated = 1
	}
	return estimated
}

func extractGeminiModelFromPath(path string) string {
	p, err := url.PathUnescape(path)
	if err != nil {
		p = path
	}
	lower := strings.ToLower(p)
	idx := strings.Index(lower, "/models/")
	if idx < 0 {
		return ""
	}
	modelAction := p[idx+len("/models/"):]
	if slash := strings.Index(modelAction, "/"); slash >= 0 {
		modelAction = modelAction[:slash]
	}
	if colon := strings.LastIndex(modelAction, ":"); colon >= 0 {
		modelAction = modelAction[:colon]
	}
	modelAction = strings.TrimSpace(strings.TrimPrefix(modelAction, "models/"))
	return modelAction
}

func isGeminiStreamPath(path string) bool {
	return strings.Contains(strings.ToLower(path), ":streamgeneratecontent")
}

func isClaudeCountTokensPath(path string) bool {
	return strings.Contains(strings.ToLower(path), "/messages/count_tokens")
}

func normalizeCLIProxyUpstreamPath(path string, srcFormat sdktranslator.Format) string {
	if srcFormat != sdktranslator.FormatClaude {
		return path
	}
	p := strings.TrimSpace(path)
	if p == "" {
		return path
	}
	if strings.HasPrefix(p, "//") {
		p = "/" + strings.TrimLeft(p, "/")
	}
	for strings.HasPrefix(p, "/v1/v1/") {
		p = "/v1/" + strings.TrimPrefix(p, "/v1/v1/")
	}
	return p
}

func isPlainCLIProxyRouteNotFound(channelType string, status int, body []byte) bool {
	if NormalizeChannelType(channelType) != ChannelTypeCLIProxy || status != http.StatusNotFound {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(string(body)))
	if msg == "404 page not found" {
		return true
	}
	if len(msg) <= 200 && strings.Contains(msg, "404 page not found") {
		return true
	}
	return false
}

func recordProxyApiLog(userID uint, token, modelName string, status int, clientIP string, startTime time.Time, requestPath, errorType, errorMessage string) {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		modelName = "unknown"
	}
	if status >= 200 && status < 400 {
		errorType = ""
		errorMessage = ""
	}
	database.DB.Create(&database.ApiLog{
		UserID:           userID,
		TokenName:        HashTokenForLog(token),
		ModelName:        modelName,
		RequestedModel:   modelName,
		ServedModel:      modelName,
		ModelWeight:      1,
		HealthMultiplier: 1,
		Status:           status,
		IPAddress:        clientIP,
		Latency:          time.Since(startTime).Milliseconds(),
		Cost:             0,
		RequestPath:      sanitizeError(requestPath, 160),
		ErrorType:        sanitizeError(errorType, 64),
		ErrorMessage:     sanitizeError(errorMessage, 512),
		CreatedAt:        time.Now(),
	})
}

func shouldRecordInvalidAuthApiLog(clientIP string) bool {
	return shouldRecordInvalidAuthApiLogAt(clientIP, time.Now())
}

func shouldRecordInvalidAuthApiLogAt(clientIP string, now time.Time) bool {
	key := strings.TrimSpace(clientIP)
	if key == "" {
		key = "<unknown>"
	}
	windowStart := now.Truncate(time.Minute)

	invalidAuthLogMu.Lock()
	if invalidAuthLogLastCleanup.IsZero() || windowStart.Sub(invalidAuthLogLastCleanup) >= time.Minute {
		cutoff := windowStart.Add(-2 * time.Minute)
		for ip, bucket := range invalidAuthLogBuckets {
			if bucket.windowStart.Before(cutoff) {
				delete(invalidAuthLogBuckets, ip)
			}
		}
		invalidAuthLogLastCleanup = windowStart
	}

	bucket := invalidAuthLogBuckets[key]
	if bucket == nil || !bucket.windowStart.Equal(windowStart) {
		bucket = &invalidAuthLogBucket{windowStart: windowStart}
		invalidAuthLogBuckets[key] = bucket
	}
	if bucket.count < invalidAuthLogLimitPerIPPerMinute {
		bucket.count++
		invalidAuthLogMu.Unlock()
		return true
	}

	bucket.suppressed++
	suppressed := bucket.suppressed
	shouldLog := suppressed == 1 || suppressed%1000 == 0
	invalidAuthLogMu.Unlock()

	if shouldLog {
		log.Printf("[AUTH-INVALID-SUPPRESSED] ip=%s window=%s suppressed=%d", key, windowStart.Format(time.RFC3339), suppressed)
	}
	return false
}

func recordProxyApiLogWithPrecheck(userID uint, token, modelName string, status int, clientIP string, startTime time.Time, requestPath, errorType, errorMessage string, inputTokens, outputTokens int, billing BillingRuleResolution, decision EngineDecision) {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		modelName = "unknown"
	}
	if status >= 200 && status < 400 {
		errorType = ""
		errorMessage = ""
	}
	quotaLimit, quotaUsed, quotaRemaining := precheckQuotaMicroValues(decision)
	database.DB.Create(&database.ApiLog{
		UserID:                 userID,
		TokenName:              HashTokenForLog(token),
		ModelName:              modelName,
		RequestedModel:         modelName,
		ServedModel:            modelName,
		ModelWeight:            billing.ModelWeight,
		HealthMultiplier:       billing.HealthMultiplier,
		BillingRulesVersion:    billing.BillingRulesVersion,
		FallbackUserOptIn:      billing.FallbackUserOptIn,
		Status:                 status,
		IPAddress:              clientIP,
		Latency:                time.Since(startTime).Milliseconds(),
		Cost:                   0,
		ChargedCost:            0,
		PrecheckInputTokens:    inputTokens,
		PrecheckOutputTokens:   outputTokens,
		PrecheckRawCost:        billing.RawCostMicroUSD,
		PrecheckChargedCost:    billing.ChargedCostMicroUSD,
		PrecheckQuotaPlanID:    decision.BlockQuotaPlanID,
		PrecheckQuotaLimit:     quotaLimit,
		PrecheckQuotaUsed:      quotaUsed,
		PrecheckQuotaRemaining: quotaRemaining,
		PrecheckWindowEndAt:    decision.BlockWindowEndAt,
		BlockReason:            sanitizeError(firstNonEmptyString(decision.BlockReason, errorType), 96),
		RequestPath:            sanitizeError(requestPath, 160),
		ErrorType:              sanitizeError(errorType, 64),
		ErrorMessage:           sanitizeError(errorMessage, 512),
		CreatedAt:              time.Now(),
	})
}

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func precheckQuotaMicroValues(decision EngineDecision) (limit, used, remaining int64) {
	if decision.BlockUnit != "api_cost_usd" {
		return 0, 0, 0
	}
	if decision.BlockLimitMicroUSD > 0 || decision.BlockConsumedMicroUSD > 0 || decision.BlockRemainingMicroUSD > 0 {
		return decision.BlockLimitMicroUSD, decision.BlockConsumedMicroUSD, decision.BlockRemainingMicroUSD
	}
	limit, _ = database.USDToMicro(decision.BlockLimitValue)
	used, _ = database.USDToMicro(decision.BlockConsumedValue)
	remaining, _ = database.USDToMicro(math.Max(0, decision.BlockRemaining))
	return
}

func precheckLimitMessage(decision EngineDecision, billing BillingRuleResolution) string {
	remaining := math.Max(0, decision.BlockRemaining)
	if decision.BlockUnit == "api_cost_usd" {
		return fmt.Sprintf("本次请求预估消耗 %.6f credits，超过当前窗口剩余额度 %.6f credits。请减少上下文、等待窗口恢复，或开启余额兜底。", database.MicroToUSD(billing.ChargedCostMicroUSD), remaining)
	}
	if decision.BlockUnit != "" {
		return fmt.Sprintf("本次请求预估消耗 %.0f %s，超过当前窗口剩余额度 %.0f %s。请减少上下文或等待窗口恢复。", decision.BlockDelta, decision.BlockUnit, remaining, decision.BlockUnit)
	}
	return "本次请求预估消耗超过当前窗口剩余额度。请减少上下文、等待窗口恢复，或开启余额兜底。"
}

func precheckLimitErrorPayload(message string, decision EngineDecision, inputTokens, outputTokens int, billing BillingRuleResolution) fiber.Map {
	details := fiber.Map{
		"block_reason":           "request_estimate_exceeds_window_remaining",
		"precheck_input_tokens":  inputTokens,
		"precheck_output_tokens": outputTokens,
		"precheck_raw_cost":      database.MicroToUSD(billing.RawCostMicroUSD),
		"precheck_charged_cost":  database.MicroToUSD(billing.ChargedCostMicroUSD),
		"model_weight":           billing.ModelWeight,
		"health_multiplier":      billing.HealthMultiplier,
		"quota_plan_id":          decision.BlockQuotaPlanID,
		"quota_unit":             decision.BlockUnit,
		"quota_limit":            decision.BlockLimitValue,
		"quota_used":             decision.BlockConsumedValue,
		"quota_remaining":        math.Max(0, decision.BlockRemaining),
	}
	if decision.BlockWindowEndAt != nil {
		details["window_end_at"] = decision.BlockWindowEndAt.Format(time.RFC3339)
	}
	return fiber.Map{"error": fiber.Map{
		"message":      message,
		"type":         "subscription_required",
		"code":         "request_estimate_exceeds_window_remaining",
		"message_code": "ERR_REQUEST_ESTIMATE_EXCEEDS_WINDOW_REMAINING",
		"details":      details,
	}}
}

func parseAllowFallbackHeader(c *fiber.Ctx) bool {
	v := strings.ToLower(strings.TrimSpace(c.Get("X-Allow-Fallback")))
	return v == "true" || v == "1" || v == "yes" || v == "on"
}

func setModelAuditHeaders(c *fiber.Ctx, requestedModel, servedModel string, fallbackOptIn bool, fallbackReason string) {
	if strings.TrimSpace(requestedModel) != "" {
		c.Set("X-Requested-Model", requestedModel)
	}
	if strings.TrimSpace(servedModel) != "" {
		c.Set("X-Served-Model", servedModel)
	}
	c.Set("X-Fallback-Allowed", strconv.FormatBool(fallbackOptIn))
	c.Set("X-Fallback-Applied", strconv.FormatBool(fallbackReason != ""))
	if fallbackReason != "" {
		c.Set("X-Fallback-Reason", sanitizeError(fallbackReason, 160))
	}
}

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
	type manualBillingStateInput struct {
		BillingState                 string
		ReasonTag                    string
		ErrorType                    string
		ErrorMessage                 string
		Status                       int
		PromptTokens                 int
		CompletionTokens             int
		CachedTokens                 int
		CacheWriteTokens             int
		CacheWrite5mTokens           int
		CacheWrite1hTokens           int
		ReasoningTokens              int
		DeliveredBytes               int64
		EstimatedInputTokens         int
		EstimatedRawCostMicroUSD     int64
		EstimatedChargedCostMicroUSD int64
	}
	type deliveredCostEstimate struct {
		RawCostMicroUSD     int64
		ChargedCostMicroUSD int64
	}
	selectedChannelTypeForBilling := func() string {
		if selectedChan == nil {
			return ""
		}
		return selectedChan.Type
	}
	upstreamRequestID := func(apiLogID uint) string {
		for _, header := range []string{"X-Request-Id", "X-Cpa-Request-Id", "Request-Id"} {
			if v := strings.TrimSpace(httpResp.Header.Get(header)); v != "" {
				return sanitizeError(v, 128)
			}
		}
		if apiLogID > 0 {
			return fmt.Sprintf("api_log:%d", apiLogID)
		}
		return fmt.Sprintf("local:%d:%d", user.ID, startTime.UnixNano())
	}
	writeBillingWithRetry := func(entry database.BillingEntryInput, rawCostMicroUSD, chargedCostMicroUSD int64, apiLogID uint) {
		var billErr error
		for attempt := 1; attempt <= 3; attempt++ {
			billErr = database.WriteBillingEntryNonFatal(entry)
			if billErr == nil {
				return
			}
			log.Printf("[BILLING-PENDING-WRITE] attempt %d/3 failed user=%d model=%s state=%s: %v", attempt, user.ID, modelName, entry.BillingState, billErr)
			if attempt < 3 {
				time.Sleep(100 * time.Millisecond)
			}
		}
		log.Printf("[BILLING-LOST-DEBT] user=%d model=%s raw_cost_micro=%d charged_cost_micro=%d api_log_id=%d state=%s UNRECOVERABLE — manual reconcile from ApiLog required: %v",
			user.ID, modelName, rawCostMicroUSD, chargedCostMicroUSD, apiLogID, entry.BillingState, billErr)
	}
	recordManualBillingState := func(in manualBillingStateInput) {
		if in.EstimatedInputTokens <= 0 {
			in.EstimatedInputTokens = estimatePrecheckTokens(body)
		}
		if in.Status == 0 {
			in.Status = 200
		}
		selectedChannelType := selectedChannelTypeForBilling()
		estimatedRawCostMicroUSD := in.EstimatedRawCostMicroUSD
		if estimatedRawCostMicroUSD < 0 {
			estimatedRawCostMicroUSD = 0
		}
		resolution := ResolveBillingRules(modelName, body, in.ReasoningTokens, selectedChannelType, fallbackUserOptIn).WithCosts(estimatedRawCostMicroUSD)
		estimatedChargedCostMicroUSD := in.EstimatedChargedCostMicroUSD
		if estimatedChargedCostMicroUSD <= 0 && estimatedRawCostMicroUSD > 0 {
			estimatedChargedCostMicroUSD = resolution.ChargedCostMicroUSD
		}
		if estimatedChargedCostMicroUSD < 0 {
			estimatedChargedCostMicroUSD = 0
		}
		reconcileCostMicroUSD := estimatedRawCostMicroUSD
		if !engineDecision.FallbackToBalance {
			reconcileCostMicroUSD = estimatedChargedCostMicroUSD
		}
		apiLog := database.ApiLog{
			UserID:              user.ID,
			TokenName:           HashTokenForLog(token),
			ModelName:           modelName,
			RequestedModel:      resolution.RequestedModel,
			ServedModel:         resolution.ServedModel,
			PromptTokens:        in.PromptTokens,
			CompletionTokens:    in.CompletionTokens,
			CachedTokens:        in.CachedTokens,
			CacheWriteTokens:    in.CacheWriteTokens,
			CacheWrite5mTokens:  in.CacheWrite5mTokens,
			CacheWrite1hTokens:  in.CacheWrite1hTokens,
			ReasoningTokens:     in.ReasoningTokens,
			Cost:                0,
			ChargedCost:         0,
			ModelWeight:         resolution.ModelWeight,
			HealthMultiplier:    resolution.HealthMultiplier,
			BillingRulesVersion: resolution.BillingRulesVersion,
			FallbackUserOptIn:   resolution.FallbackUserOptIn,
			FallbackReason:      sanitizeError(resolution.FallbackReason, 160),
			UpstreamProvider:    sanitizeError(strings.ToLower(strings.TrimSpace(selectedChannelType)), 64),
			Latency:             time.Since(startTime).Milliseconds(),
			Status:              in.Status,
			IPAddress:           clientIP,
			RequestPath:         sanitizeError(path, 160),
			ErrorType:           sanitizeError(in.ErrorType, 64),
			ErrorMessage:        sanitizeError(in.ErrorMessage, 512),
			PrecheckInputTokens: in.EstimatedInputTokens,
			PrecheckRawCost:     estimatedRawCostMicroUSD,
			PrecheckChargedCost: estimatedChargedCostMicroUSD,
			CreatedAt:           time.Now(),
		}
		apiLogPersisted := true
		if err := database.DB.Create(&apiLog).Error; err != nil {
			log.Printf("[BILLING-CRITICAL] user=%d model=%s manual-state api_log create failed: %v", user.ID, modelName, err)
			apiLogPersisted = false
		}
		relatedID := uint(0)
		relatedType := ""
		if apiLogPersisted {
			relatedID = apiLog.ID
			relatedType = "api_log"
		}
		requestID := upstreamRequestID(relatedID)
		entry := database.BillingEntryInput{
			UserID:               user.ID,
			EntryType:            database.BillingTypeApiUsagePendingReconcile,
			BillingState:         in.BillingState,
			AmountUSD:            0,
			BalanceAfterUSD:      user.Quota,
			ModelName:            modelName,
			TokensTotal:          in.PromptTokens + in.CompletionTokens,
			RequestID:            requestID,
			DeliveredBytes:       in.DeliveredBytes,
			EstimatedInputTokens: in.EstimatedInputTokens,
			EstimatedCostUSD:     reconcileCostMicroUSD,
			RelatedType:          relatedType,
			RelatedID:            relatedID,
			Description: fmt.Sprintf("[%s] %s · request_id=%s · delivered_bytes=%d · estimated_input_tokens=%d · estimated_cost=%s · reconcile_cost=%s · %s",
				in.ReasonTag, modelName, requestID, in.DeliveredBytes, in.EstimatedInputTokens,
				FormatChargedCostForDescription(estimatedRawCostMicroUSD, estimatedChargedCostMicroUSD),
				database.FormatMicroUSD(reconcileCostMicroUSD), in.ErrorMessage),
		}
		writeBillingWithRetry(entry, estimatedRawCostMicroUSD, estimatedChargedCostMicroUSD, relatedID)
	}
	estimateDeliveredCost := func(deliveredBytes int64, reasoningTokens int) deliveredCostEstimate {
		outputTokens := 0
		if deliveredBytes > 0 {
			outputTokens = int((deliveredBytes + 3) / 4)
		}
		rawCost := estimatePrecheckBalanceDelta(modelName, estimatePrecheckTokens(body), outputTokens)
		resolution := ResolveBillingRules(modelName, body, reasoningTokens, selectedChannelTypeForBilling(), fallbackUserOptIn).WithCosts(rawCost)
		return deliveredCostEstimate{
			RawCostMicroUSD:     rawCost,
			ChargedCostMicroUSD: resolution.ChargedCostMicroUSD,
		}
	}
	deductQuota := func(promptTokens, completionTokens, cachedTokens, cacheWriteTokens, cacheWrite5mTokens, cacheWrite1hTokens, reasoningTokens, status int, deliveredBytes int64) bool {
		// fix CRITICAL Phase 4-codex（第二十四轮）：所有 token 必须 clamp >= 0；
		// cached 必须 ≤ prompt（cached 是 prompt 子集），否则 (prompt-cached) 为负让 cost 变负，
		// 进入 `if costMicroUSD > 0` 分支被跳过 → 用户得到免费服务且 ApiLog.Cost 污染统计。
		if promptTokens < 0 {
			promptTokens = 0
		}
		if completionTokens < 0 {
			completionTokens = 0
		}
		if cachedTokens < 0 {
			cachedTokens = 0
		}
		if cacheWriteTokens < 0 {
			cacheWriteTokens = 0
		}
		if cacheWrite5mTokens < 0 {
			cacheWrite5mTokens = 0
		}
		if cacheWrite1hTokens < 0 {
			cacheWrite1hTokens = 0
		}
		if reasoningTokens < 0 {
			reasoningTokens = 0
		}
		// Anthropic 协议 usage 必须按 5m / 1h 分桶给出 cache_creation_input_tokens 细分。
		// 不接受 legacy "single counter" fallback：上游必须显式提供分桶，否则不计费 cache write。
		cacheWriteTokens = cacheWrite5mTokens + cacheWrite1hTokens
		if cachedTokens > promptTokens {
			cachedTokens = promptTokens
		}
		if cachedTokens+cacheWriteTokens > promptTokens {
			cacheWriteTokens = promptTokens - cachedTokens
			if cacheWriteTokens < 0 {
				cacheWriteTokens = 0
			}
		}
		if cacheWrite5mTokens+cacheWrite1hTokens > cacheWriteTokens {
			overflow := cacheWrite5mTokens + cacheWrite1hTokens - cacheWriteTokens
			if cacheWrite5mTokens >= overflow {
				cacheWrite5mTokens -= overflow
			} else {
				overflow -= cacheWrite5mTokens
				cacheWrite5mTokens = 0
				cacheWrite1hTokens -= overflow
				if cacheWrite1hTokens < 0 {
					cacheWrite1hTokens = 0
				}
			}
		}
		if reasoningTokens > completionTokens {
			reasoningTokens = completionTokens
		}

		// fix MAJOR Phase 4-codex（第二十四轮）：失败请求（status < 200 || >= 400）不扣费，
		// 上游已 4xx 响应说明服务未交付，不应进入订阅 commit / 余额扣费。
		// 仅写 ApiLog 用作错误统计，cost = 0。
		failedRequest := status < 200 || status >= 400

		inputPricePico := selectedPath.InputPricePicoPerToken
		outputPricePico := selectedPath.OutputPricePicoPerToken
		cachedInputPricePico := selectedPath.CachedInputPricePicoPerToken

		if selectedPath.ContextPriceThreshold > 0 && promptTokens >= selectedPath.ContextPriceThreshold {
			if selectedPath.HighInputPricePicoPerToken > 0 {
				inputPricePico = selectedPath.HighInputPricePicoPerToken
			}
			if selectedPath.HighCachedInputPricePicoPerToken > 0 {
				cachedInputPricePico = selectedPath.HighCachedInputPricePicoPerToken
			}
			if selectedPath.HighOutputPricePicoPerToken > 0 {
				outputPricePico = selectedPath.HighOutputPricePicoPerToken
			}
		}
		cacheWriteInputPricePico := selectedPath.CacheWriteInputPricePicoPerToken
		if cacheWriteInputPricePico <= 0 {
			cacheWriteInputPricePico = inputPricePico
			if strings.Contains(strings.ToLower(modelName), "claude") {
				cacheWriteInputPricePico = (inputPricePico * 125) / 100
			}
		}
		cacheWrite1hInputPricePico := selectedPath.CacheWrite1hInputPricePicoPerToken
		if cacheWrite1hInputPricePico <= 0 {
			cacheWrite1hInputPricePico = inputPricePico * 2
		}

		// fix MAJOR M-B5（codex 第二十一轮）：原成本公式漏掉 reasoningTokens（OpenAI o1/o3、Claude
		// thinking 等推理模型会单独返回 reasoning_tokens，与 completion_tokens 解耦计费）。
		nonReasoningCompletion := completionTokens - reasoningTokens
		if nonReasoningCompletion < 0 {
			nonReasoningCompletion = 0
		}
		standardInputTokens := promptTokens - cachedTokens - cacheWriteTokens
		if standardInputTokens < 0 {
			standardInputTokens = 0
		}
		// fix MAJOR M22-A1 Phase 1：cost 单位 micro_usd（int64）。
		// fix MAJOR Phase 4-codex：用 checkedCostMicroUSD 加固，failedRequest 直接 0，
		// 浮点结果 NaN/Inf/超出 int64 上下界都返回 (0, false) → fail-closed（写 0 cost 不扣不计）。
		var costMicroUSD int64
		var costOK bool
		if failedRequest {
			costMicroUSD, costOK = 0, true
		} else {
			costMicroUSD, costOK = checkedCostMicroUSD(
				standardInputTokens, inputPricePico,
				cachedTokens, cachedInputPricePico,
				cacheWrite5mTokens, cacheWriteInputPricePico,
				cacheWrite1hTokens, cacheWrite1hInputPricePico,
				nonReasoningCompletion, outputPricePico,
				reasoningTokens, outputPricePico,
			)
			if !costOK {
				log.Printf("[BILLING-CRITICAL] user=%d model=%s cost overflow/invalid; prompt=%d completion=%d cached_read=%d cache_write=%d cache_write_5m=%d cache_write_1h=%d reasoning=%d inputPricePico=%d outputPricePico=%d cachedPricePico=%d cacheWrite5mPricePico=%d cacheWrite1hPricePico=%d — failing closed (0 cost)",
					user.ID, modelName, promptTokens, completionTokens, cachedTokens, cacheWriteTokens, cacheWrite5mTokens, cacheWrite1hTokens, reasoningTokens,
					inputPricePico, outputPricePico, cachedInputPricePico, cacheWriteInputPricePico, cacheWrite1hInputPricePico)
				if isStream {
					estimatedCost := estimateDeliveredCost(deliveredBytes, reasoningTokens)
					recordManualBillingState(manualBillingStateInput{
						BillingState:                 database.BillingStatePendingReconcile,
						ReasonTag:                    "COST-CALC-FAILED",
						ErrorType:                    "billing_cost_invalid",
						ErrorMessage:                 "stream delivered but cost calculation failed",
						Status:                       200,
						PromptTokens:                 promptTokens,
						CompletionTokens:             completionTokens,
						CachedTokens:                 cachedTokens,
						CacheWriteTokens:             cacheWriteTokens,
						CacheWrite5mTokens:           cacheWrite5mTokens,
						CacheWrite1hTokens:           cacheWrite1hTokens,
						ReasoningTokens:              reasoningTokens,
						DeliveredBytes:               deliveredBytes,
						EstimatedInputTokens:         promptTokens,
						EstimatedRawCostMicroUSD:     estimatedCost.RawCostMicroUSD,
						EstimatedChargedCostMicroUSD: estimatedCost.ChargedCostMicroUSD,
					})
				} else {
					recordProxyApiLog(user.ID, token, modelName, 502, clientIP, startTime, path, "billing_cost_invalid", "cost calculation failed")
				}
				return false
			}
		}
		selectedChannelType := ""
		if selectedChan != nil {
			selectedChannelType = selectedChan.Type
		}
		billingResolution := ResolveBillingRules(modelName, body, reasoningTokens, selectedChannelType, fallbackUserOptIn).WithCosts(costMicroUSD)
		chargedCostMicroUSD := billingResolution.ChargedCostMicroUSD

		apiLog := database.ApiLog{
			UserID:              user.ID,
			TokenName:           HashTokenForLog(token),
			ModelName:           modelName,
			RequestedModel:      billingResolution.RequestedModel,
			ServedModel:         billingResolution.ServedModel,
			PromptTokens:        promptTokens,
			CompletionTokens:    completionTokens,
			CachedTokens:        cachedTokens,
			CacheWriteTokens:    cacheWriteTokens,
			CacheWrite5mTokens:  cacheWrite5mTokens,
			CacheWrite1hTokens:  cacheWrite1hTokens,
			ReasoningTokens:     reasoningTokens,
			Cost:                costMicroUSD,
			ChargedCost:         chargedCostMicroUSD,
			ModelWeight:         billingResolution.ModelWeight,
			HealthMultiplier:    billingResolution.HealthMultiplier,
			BillingRulesVersion: billingResolution.BillingRulesVersion,
			FallbackUserOptIn:   billingResolution.FallbackUserOptIn,
			FallbackReason:      sanitizeError(billingResolution.FallbackReason, 160),
			UpstreamProvider:    sanitizeError(strings.ToLower(strings.TrimSpace(selectedChannelType)), 64),
			Latency:             time.Since(startTime).Milliseconds(), // Parity Tracker
			Status:              status,                               // Parity Tracker
			IPAddress:           clientIP,                             // Parity Tracker
			RequestPath:         sanitizeError(path, 160),
			ErrorType:           sanitizeError(apiErrorType, 64),
			ErrorMessage:        sanitizeError(apiErrorMessage, 512),
			CreatedAt:           time.Now(),
		}
		// fix Major（codex 第十四轮）：原 Create 未检 .Error → apiLog.ID=0 时下游账单
		// RelatedID 写空指针。失败仅日志告警，但账单条目对应 RelatedID 留空避免假关联。
		apiLogPersisted := true
		if err := database.DB.Create(&apiLog).Error; err != nil {
			log.Printf("[BILLING-CRITICAL] user=%d model=%s api_log create failed: %v", user.ID, modelName, err)
			apiLogPersisted = false
		}

		// 订阅扣费：实扣（基于真实 token 数）。命中订阅则不扣 USD 余额。
		// 失败的请求（status < 200 || >= 400）不扣订阅额度也不扣余额。
		commitOK := false
		// effectiveRevenueMicroUSD 是"本次请求真实从用户那拿到多少钱"。
		// 订阅命中 → chargedCost；余额扣费成功 → rawCost (=costMicroUSD)；其它（失败/pending）→ 0。
		// 同时驱动两件事：ApiLogRevenue.effective_revenue + 子 token UsedQuota 累加。
		var effectiveRevenueMicroUSD int64
		var commitDecision EngineDecision
		if !failedRequest {
			commitDecision = Decide(EngineRequest{
				UserID:       user.ID,
				ModelName:    modelName,
				InputTokens:  promptTokens,
				OutputTokens: completionTokens,
				CostMicroUSD: chargedCostMicroUSD,
				IsPrecheck:   false,
			})
			commitOK = commitDecision.Allowed && !commitDecision.FallbackToBalance
			if !commitOK {
				log.Printf("[BILLING-FALLBACK] user=%d model=%s raw_cost_micro=%d charged_cost_micro=%d reason=%s allowed=%v fallback_balance=%v sub=%d plan=%d needs_retry=%v",
					user.ID, modelName, costMicroUSD, chargedCostMicroUSD, commitDecision.BlockReason,
					commitDecision.Allowed, commitDecision.FallbackToBalance,
					commitDecision.SubscriptionID, commitDecision.QuotaPlanID, commitDecision.NeedsRetry)
			}
		}
		// fix CRITICAL R23+2-C3 + MAJOR R23+3-B5（codex 第四轮）：
		// commit 阶段订阅 DB 加载失败时落一条**独立 EntryType** 的待对账账单
		//
		// fix CRITICAL Phase 4-codex（第二十四轮）：原用 NonFatal 写失败后只 log → return，
		// 形成"已交付服务但无扣费、无待对账记录"的财务黑洞。改为重试 3 次 + 失败后写日志放大警报，
		// 让 admin 看 [BILLING-LOST-DEBT] 必要时按 ApiLog 手工补账。
		if !failedRequest && commitDecision.NeedsRetry {
			log.Printf("[BILLING-DB-RETRY] user=%d model=%s raw_cost_micro=%d charged_cost_micro=%d sub-load failed, recording for manual reconcile",
				user.ID, modelName, costMicroUSD, chargedCostMicroUSD)
			relatedID := uint(0)
			relatedType := ""
			if apiLogPersisted {
				relatedID = apiLog.ID
				relatedType = "api_log"
			}
			pendingEntry := database.BillingEntryInput{
				UserID:               user.ID,
				EntryType:            database.BillingTypeApiUsagePendingReconcile,
				BillingState:         database.BillingStatePendingReconcile,
				AmountUSD:            0,
				BalanceAfterUSD:      user.Quota,
				ModelName:            modelName,
				TokensTotal:          promptTokens + completionTokens, // cached/reasoning 是 prompt/completion 子集（cost 算法保持一致）
				RequestID:            upstreamRequestID(relatedID),
				EstimatedInputTokens: promptTokens,
				EstimatedCostUSD:     costMicroUSD,
				RelatedType:          relatedType,
				RelatedID:            relatedID,
				Description: fmt.Sprintf("[DB-RETRY] %s · %d+%d tokens · %s 待对账（订阅 DB 加载失败）",
					modelName, promptTokens, completionTokens, FormatChargedCostForDescription(costMicroUSD, chargedCostMicroUSD)),
			}
			// 重试 3 次：每次新事务，失败 → 100ms backoff
			var billErr error
			for attempt := 1; attempt <= 3; attempt++ {
				billErr = database.WriteBillingEntryNonFatal(pendingEntry)
				if billErr == nil {
					break
				}
				log.Printf("[BILLING-DB-RETRY] write attempt %d/3 failed: %v", attempt, billErr)
				if attempt < 3 {
					time.Sleep(100 * time.Millisecond)
				}
			}
			if billErr != nil {
				// 所有重试都失败 → 财务损失警报。admin 必须按 ApiLog（已写入）手工补账。
				log.Printf("[BILLING-LOST-DEBT] user=%d model=%s raw_cost_micro=%d charged_cost_micro=%d api_log_id=%d UNRECOVERABLE — manual reconcile from ApiLog required: %v",
					user.ID, modelName, costMicroUSD, chargedCostMicroUSD, apiLog.ID, billErr)
			}
			return true // 不走 sub 账单 + 不走 balance fallback 扣费
		}

		// 账单流水：命中订阅扣额度（不动 quota，AmountUSD=0，仅审计 token 数）
		// 失败时仅日志，不影响请求 — 上游已成功，账单是审计层而非阻塞层。
		// Phase 8：addon 已移除，所有命中订阅都走 api_usage_sub
		if commitOK {
			entryType := database.BillingTypeApiUsageSub
			productLabel := "套餐"
			subID := commitDecision.SubscriptionID
			tokensTotal := promptTokens + completionTokens // cached/reasoning 是子集
			// fix Major（codex 第十四轮）：失败 ApiLog 时 RelatedID 留空，避免账单挂死链
			relatedID := uint(0)
			relatedType := ""
			if apiLogPersisted {
				relatedID = apiLog.ID
				relatedType = "api_log"
			}
			// 命中订阅不动 quota，余额不变；这里 user.Quota 是缓存快照，
			// 在订阅命中场景下数值无金额变动，仅作为审计参考写入。
			if billErr := database.WriteBillingEntryNonFatal(database.BillingEntryInput{
				UserID:               user.ID,
				EntryType:            entryType,
				AmountUSD:            0,
				BalanceAfterUSD:      user.Quota,
				ModelName:            modelName,
				TokensTotal:          tokensTotal,
				SourceSubscriptionID: &subID,
				RelatedType:          relatedType,
				RelatedID:            relatedID,
				Description:          fmt.Sprintf("%s · %s · %d tokens · %s", productLabel, modelName, tokensTotal, FormatChargedCostForDescription(costMicroUSD, chargedCostMicroUSD)),
			}); billErr != nil {
				log.Printf("[BILLING-AUDIT-FAIL] user=%d sub=%d type=%s: %v", user.ID, subID, entryType, billErr)
			}
			// 事实化记录：本次请求收入归订阅。付费订阅 = chargedCost；管理员赠送订阅 = 0。
			// 赠送订阅仍会保留 ApiLog.cost 作为真实上游成本，但不应污染真实营收与毛利分子。
			effectiveRevenueMicroUSD = subscriptionRevenueMicroUSD(chargedCostMicroUSD, commitDecision.SubscriptionIsGranted)
			if apiLogPersisted {
				RecordApiLogRevenue(apiLog.ID, database.RevenueSourceSubscription, effectiveRevenueMicroUSD, subID)
			}
		}

		if !commitOK && chargedCostMicroUSD > 0 {
			// 三段消费 fallback 到余额。
			//
			// fix CRITICAL（多模型审计第二十五轮）：本路径下扣减用户余额必须使用 chargedCostMicroUSD（套餐口径）
			// 而不是 raw costMicroUSD。否则模型权重对余额扣费失效，Haiku 多扣（weight=0.3）、Opus 少扣（weight=3.5），
			// 违反三账分离原则（raw_cost 仅记账，charged_cost 才是用户实扣）。
			//
			// PRODUCT REVERSAL（本次决策）：经业务确认，余额扣费改回 rawCost。理由：
			//   - 订阅是"模型组合包月"产品，modelWeight 调配 Haiku/Opus 含金量是产品定义
			//   - 余额是用户预付美元，应按上游真实成本扣，不应被 modelWeight 重定价
			//   - 这是产品策略变更，不是 bug 回退
			//   - 业务影响：纯余额用户 Opus 扣费下降（3.5x→1x），Haiku 扣费上升（0.3x→1x）
			// fix CRITICAL Sprint1-P0-3：余额扣减必须 CAS 原子化
			// 旧实现：`UPDATE quota = quota - ?` 无 WHERE quota >= ? 守卫 → 并发请求可把余额打负。
			// 新实现：`WHERE id=? AND quota >= ?` 条件 UPDATE：
			//   - 命中（RowsAffected==1）→ 正常扣费 + 写 api_consume_balance 账单
			//   - 未命中（RowsAffected==0）→ 重查区分用户缺失 vs 余额不足；
			//     余额不足时上游服务已交付，不打负余额，改写 api_usage_pending_reconcile 待对账。
			deductQuotaAtomic := func() {
				var balanceAfterMicroUSD int64
				balanceConsumeMicroUSD := costMicroUSD // rawCost：余额路径按上游真实成本扣费
				balanceConsumed := false               // CAS 成功（非 pending）才记 revenue
				var referralReward database.ReferralPaidSpendRewardResult
				referralRewardBPS, referralRewardWindowSeconds := readReferralPaidSpendRewardConfig()
				txErr := database.DB.Transaction(func(tx *gorm.DB) error {
					if !TryConsumeBalanceTx(tx, user.ID, balanceConsumeMicroUSD, true /* forceTrack */) {
						log.Printf("[BILLING-WINDOW-TRACK-FAIL] user=%d model=%s raw_cost_micro=%d charged_cost_micro=%d forceTrack failed (DB issue), continuing quota deduct", user.ID, modelName, balanceConsumeMicroUSD, chargedCostMicroUSD)
					}

					// 原子 CAS：仅在余额 ≥ 扣费金额时执行 UPDATE
					res := tx.Model(&database.User{}).
						Where("id = ? AND quota >= ?", user.ID, balanceConsumeMicroUSD).
						UpdateColumn("quota", gorm.Expr("quota - ?", balanceConsumeMicroUSD))
					if res.Error != nil {
						return fmt.Errorf("quota deduct: %w", res.Error)
					}

					tokensTotal := promptTokens + completionTokens // cached/reasoning 是 prompt/completion 子集
					relatedID := uint(0)
					relatedType := ""
					if apiLogPersisted {
						relatedID = apiLog.ID
						relatedType = "api_log"
					}

					if res.RowsAffected == 0 {
						// CAS 失败：用户不存在 OR 余额不足。重查区分。
						var u database.User
						if err := tx.Select("id, quota").First(&u, user.ID).Error; err != nil {
							return fmt.Errorf("user row missing: %w", err)
						}
						// 用户存在 → 余额不足。上游服务已交付，写 pending_reconcile，
						// 不动 quota（保证余额永不为负），admin 人工对账或免扣。
						log.Printf("[BILLING-INSUFFICIENT-BALANCE] user=%d model=%s raw_cost_micro=%d charged_cost_micro=%d current_quota=%d — recording pending_reconcile (service already delivered)",
							user.ID, modelName, balanceConsumeMicroUSD, chargedCostMicroUSD, u.Quota)
						return database.WriteBillingEntry(tx, database.BillingEntryInput{
							UserID:               user.ID,
							EntryType:            database.BillingTypeApiUsagePendingReconcile,
							BillingState:         database.BillingStatePendingReconcile,
							AmountUSD:            0, // 未实际扣减
							BalanceAfterUSD:      u.Quota,
							ModelName:            modelName,
							TokensTotal:          tokensTotal,
							RequestID:            upstreamRequestID(relatedID),
							EstimatedInputTokens: promptTokens,
							EstimatedCostUSD:     balanceConsumeMicroUSD,
							RelatedType:          relatedType,
							RelatedID:            relatedID,
							Description: fmt.Sprintf("[INSUFFICIENT-BALANCE] %s · %d tokens · 余额不足，已交付服务待对账（按 raw 上游成本计 $%s）",
								modelName, tokensTotal, database.FormatMicroUSD(balanceConsumeMicroUSD)),
						})
					}

					// CAS 成功 → 余额已扣减，写正常 api_consume_balance 账单
					var freshUser database.User
					if err := tx.Select("id, quota").First(&freshUser, user.ID).Error; err != nil {
						return fmt.Errorf("re-select quota: %w", err)
					}
					balanceAfterMicroUSD = freshUser.Quota

					if err := database.WriteBillingEntry(tx, database.BillingEntryInput{
						UserID:          user.ID,
						EntryType:       database.BillingTypeApiConsumeBalance,
						AmountUSD:       -balanceConsumeMicroUSD,
						BalanceAfterUSD: balanceAfterMicroUSD,
						ModelName:       modelName,
						TokensTotal:     tokensTotal,
						RelatedType:     relatedType,
						RelatedID:       relatedID,
						Description:     fmt.Sprintf("余额扣费 · %s · %d tokens · %s", modelName, tokensTotal, FormatChargedCostForDescription(costMicroUSD, chargedCostMicroUSD)),
					}); err != nil {
						return fmt.Errorf("write billing: %w", err)
					}
					reward, err := database.ApplyReferralPaidSpendRewardTx(
						tx,
						user.ID,
						balanceConsumeMicroUSD,
						referralRewardBPS,
						referralRewardWindowSeconds,
						time.Now(),
						relatedType,
						relatedID,
						fmt.Sprintf("余额扣费 · %s", modelName),
					)
					if err != nil {
						return fmt.Errorf("apply referral spend reward: %w", err)
					}
					referralReward = reward
					balanceConsumed = true
					return nil
				})
				if txErr != nil {
					log.Printf("[BILLING-CRITICAL] user=%d model=%s raw_cost_micro=%d charged_cost_micro=%d QUOTA-DEDUCT-TX-FAILED reason=balance-fallback: %v",
						user.ID, modelName, costMicroUSD, chargedCostMicroUSD, txErr)
					return
				}
				// 事实化记录：CAS 成功的余额扣费路径才记 revenue；pending_reconcile 分支未真实扣钱不记
				if balanceConsumed {
					effectiveRevenueMicroUSD = balanceConsumeMicroUSD
					if apiLogPersisted {
						RecordApiLogRevenue(apiLog.ID, database.RevenueSourceBalance, balanceConsumeMicroUSD, 0)
					}
				}
				RefreshUserAuth(user.ID)
				if referralReward.ReferrerID != 0 && referralReward.RewardMicroUSD > 0 {
					RefreshUserAuth(referralReward.ReferrerID)
				}
			}

			if !user.BalanceConsumeEnabled {
				// fix CRITICAL Phase 4-codex（第二十四轮）：UNAUTHORIZED-FALLBACK 路径——
				// 订阅在 commit 阶段被并发耗尽 + 余额消费禁用 → 上游已交付服务但平台无路扣费。
				// 原实现仅 log，留下"已服务但无账"黑洞。改为写 api_usage_pending_reconcile 待对账，
				// AmountUSD=0（确实没扣 quota）+ Description 标注 cost 让 admin 决策补扣或免扣。
				log.Printf("[BILLING-PENDING-DEBT] user=%d model=%s raw_cost_micro=%d charged_cost_micro=%d UNAUTHORIZED-FALLBACK reason=subscription_drained_during_request balance_consume_disabled — recording for admin reconcile",
					user.ID, modelName, costMicroUSD, chargedCostMicroUSD)
				relatedID := uint(0)
				relatedType := ""
				if apiLogPersisted {
					relatedID = apiLog.ID
					relatedType = "api_log"
				}
				pendingEntry := database.BillingEntryInput{
					UserID:               user.ID,
					EntryType:            database.BillingTypeApiUsagePendingReconcile,
					BillingState:         database.BillingStatePendingReconcile,
					AmountUSD:            0,
					BalanceAfterUSD:      user.Quota,
					ModelName:            modelName,
					TokensTotal:          promptTokens + completionTokens, // cached/reasoning 是 prompt/completion 子集（cost 算法保持一致）
					RequestID:            upstreamRequestID(relatedID),
					EstimatedInputTokens: promptTokens,
					EstimatedCostUSD:     costMicroUSD,
					RelatedType:          relatedType,
					RelatedID:            relatedID,
					Description: fmt.Sprintf("[UNAUTHORIZED-FALLBACK] %s · %d+%d tokens · %s 待对账（订阅 commit 期被耗尽 + 余额消费禁用）",
						modelName, promptTokens, completionTokens, FormatChargedCostForDescription(costMicroUSD, chargedCostMicroUSD)),
				}
				var billErr error
				for attempt := 1; attempt <= 3; attempt++ {
					billErr = database.WriteBillingEntryNonFatal(pendingEntry)
					if billErr == nil {
						break
					}
					if attempt < 3 {
						time.Sleep(100 * time.Millisecond)
					}
				}
				if billErr != nil {
					log.Printf("[BILLING-LOST-DEBT] user=%d model=%s raw_cost_micro=%d charged_cost_micro=%d api_log_id=%d UNRECOVERABLE — manual reconcile from ApiLog required: %v",
						user.ID, modelName, costMicroUSD, chargedCostMicroUSD, apiLog.ID, billErr)
				}
			} else {
				deductQuotaAtomic()
			}
		}

		// 子 token UsedQuota 累加（详见原注释 — 选择"无条件累加 + precheck 拦截"模型）
		//
		// 按 effectiveRevenueMicroUSD 累加，与"用户实际花了多少"对齐：
		//   - 订阅命中 → effective = chargedCost（与 SubscriptionUsage.ConsumedValue 同口径）
		//   - 余额扣费 → effective = rawCost（与 User.Quota 扣减同口径）
		//   - pending / 失败 → effective = 0，不累加
		//
		// 这样 sub-token.QuotaLimit 永远反映"用户实际花了多少美元"，无论后端走哪条扣减路径。
		// 旧实现（一刀切按 chargedCost）会让纯余额用户：Opus weight=3.5 时 UsedQuota 虚高 3.5×，
		// Haiku weight=0.3 时 UsedQuota 虚低 3.3× —— 子 token 限额语义被破坏。
		if isSubToken && effectiveRevenueMicroUSD > 0 && status >= 200 && status < 400 {
			res := database.DB.Model(&database.AccessToken{}).
				Where("id = ?", subToken.ID).
				UpdateColumn("used_quota", gorm.Expr("used_quota + ?", effectiveRevenueMicroUSD))
			if res.Error != nil {
				log.Printf("[SUB-TOKEN-CRITICAL] token_id=%d effective_revenue_micro=%d UsedQuota-UPDATE-FAILED: %v", subToken.ID, effectiveRevenueMicroUSD, res.Error)
			} else if res.RowsAffected == 0 {
				log.Printf("[SUB-TOKEN-CRITICAL] token_id=%d effective_revenue_micro=%d token-not-found-at-commit", subToken.ID, effectiveRevenueMicroUSD)
			} else {
				if subToken.QuotaLimit > 0 && subToken.UsedQuota+effectiveRevenueMicroUSD > subToken.QuotaLimit {
					log.Printf("[SUB-TOKEN-OVERLIMIT] token_id=%d effective_revenue_micro=%d used-quota-exceeded-limit", subToken.ID, effectiveRevenueMicroUSD)
				}
				// clone-on-write 防 data race
				authSnapshotMutex.Lock()
				if existing, ok := AuthTokenCache[token]; ok {
					updated := *existing
					updated.UsedQuota += effectiveRevenueMicroUSD
					AuthTokenCache[token] = &updated
				}
				authSnapshotMutex.Unlock()
			}
		}
		return true
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
func estimatePrecheckBalanceDelta(modelName string, inputTokens, outputTokens int) int64 {
	const fallbackPricePicoPerToken = 30 * database.PicoPerTokenPerUSDPerMTok // $30/M tokens 保守上界
	const minDeltaMicroUSD = int64(100)                                       // $0.0001 = 100 micro_usd 最低估算下限

	maxInputPico := int64(0)
	maxOutputPico := int64(0)

	gatewayMutex.RLock()
	routes := RouteCache[modelName]
	gatewayMutex.RUnlock()

	for _, r := range routes {
		if r == nil {
			continue
		}
		inP := r.InputPricePicoPerToken
		outP := r.OutputPricePicoPerToken
		// 与 commit 路径保持一致：只有估算输入 token 达到上下文阈值时，才启用长上下文高价档。
		if r.ContextPriceThreshold > 0 && inputTokens >= r.ContextPriceThreshold {
			if r.HighInputPricePicoPerToken > 0 {
				inP = r.HighInputPricePicoPerToken
			}
			if r.HighOutputPricePicoPerToken > 0 {
				outP = r.HighOutputPricePicoPerToken
			}
		}
		if inP > maxInputPico {
			maxInputPico = inP
		}
		if outP > maxOutputPico {
			maxOutputPico = outP
		}
	}
	if maxInputPico <= 0 {
		maxInputPico = fallbackPricePicoPerToken
	}
	if maxOutputPico <= 0 {
		maxOutputPico = fallbackPricePicoPerToken
	}

	// tokens × pico_usd/token ÷ 1e9 = micro_usd。
	// 用 checkedCostMicroUSD 加固以防负数/溢出 → fail-closed 时退到最低估算（避免免费透支）
	delta, ok := checkedCostMicroUSD(
		inputTokens, maxInputPico,
		0, 0,
		outputTokens, maxOutputPico,
		0, 0,
		0, 0,
		0, 0,
	)
	if !ok || delta < minDeltaMicroUSD {
		delta = minDeltaMicroUSD
	}
	return delta
}

type usageTokenCounts struct {
	PromptTokens        int
	CompletionTokens    int
	CachedTokens        int
	CacheWriteTokens    int
	CacheWrite5mTokens  int
	CacheWrite1hTokens  int
	ReasoningTokens     int
	HasPromptTokens     bool
	HasCompletionTokens bool
	HasCachedTokens     bool
	HasCacheWriteTokens bool
	HasReasoningTokens  bool
}

func (u usageTokenCounts) HasAny() bool {
	return u.HasPromptTokens || u.HasCompletionTokens || u.HasCachedTokens || u.HasCacheWriteTokens || u.HasReasoningTokens
}

func (u usageTokenCounts) HasBillableTokens() bool {
	return u.PromptTokens+u.CompletionTokens > 0
}

func extractUsageTokenCounts(usage gjson.Result) usageTokenCounts {
	var out usageTokenCounts
	if !usage.Exists() {
		return out
	}

	promptTokens, hasPromptTokens := usageInt(usage, "prompt_tokens")
	inputTokens, hasInputTokens := usageInt(usage, "input_tokens")
	geminiPromptTokens, hasGeminiPromptTokens := usageInt(usage, "promptTokenCount", "prompt_token_count")
	if hasPromptTokens {
		out.PromptTokens = promptTokens
		out.HasPromptTokens = true
	} else if hasInputTokens {
		out.PromptTokens = inputTokens
		out.HasPromptTokens = true
	} else if hasGeminiPromptTokens {
		out.PromptTokens = geminiPromptTokens
		out.HasPromptTokens = true
	}

	if v, ok := usageInt(usage,
		"completion_tokens",
		"output_tokens",
		"candidatesTokenCount",
		"candidates_token_count",
	); ok {
		out.CompletionTokens = v
		out.HasCompletionTokens = true
	}
	if v, ok := usageInt(usage,
		"prompt_tokens_details.cached_tokens",
		"input_tokens_details.cached_tokens",
		"cache_read_input_tokens",
		"cachedContentTokenCount",
		"cached_content_token_count",
	); ok {
		out.CachedTokens = v
		out.HasCachedTokens = true
	}
	cacheWrite5mTokens, hasCacheWrite5mTokens := usageInt(usage,
		"cache_creation.ephemeral_5m_input_tokens",
		"cache_creation.ephemeral5m_input_tokens",
		"cache_creation_5m_input_tokens",
		"cache_write_5m_input_tokens",
	)
	cacheWrite1hTokens, hasCacheWrite1hTokens := usageInt(usage,
		"cache_creation.ephemeral_1h_input_tokens",
		"cache_creation.ephemeral1h_input_tokens",
		"cache_creation_1h_input_tokens",
		"cache_write_1h_input_tokens",
	)
	if hasCacheWrite5mTokens || hasCacheWrite1hTokens {
		out.CacheWrite5mTokens = cacheWrite5mTokens
		out.CacheWrite1hTokens = cacheWrite1hTokens
		out.CacheWriteTokens = cacheWrite5mTokens + cacheWrite1hTokens
		out.HasCacheWriteTokens = true
	} else if v, ok := usageInt(usage,
		"cache_creation_input_tokens",
		"cache_write_input_tokens",
		"prompt_tokens_details.cache_creation_tokens",
		"input_tokens_details.cache_creation_tokens",
	); ok {
		out.CacheWriteTokens = v
		out.CacheWrite5mTokens = v
		out.HasCacheWriteTokens = true
	}
	if v, ok := usageInt(usage,
		"completion_tokens_details.reasoning_tokens",
		"output_tokens_details.reasoning_tokens",
		"reasoning_tokens",
		"thoughtsTokenCount",
		"thoughts_token_count",
	); ok {
		out.ReasoningTokens = v
		out.HasReasoningTokens = true
	}
	// Gemini usageMetadata reports candidatesTokenCount and thoughtsTokenCount separately.
	// Treat thoughts as output-side reasoning so billing and charts include the full delivered output.
	if out.HasReasoningTokens && (usage.Get("thoughtsTokenCount").Exists() || usage.Get("thoughts_token_count").Exists()) {
		out.CompletionTokens += out.ReasoningTokens
		out.HasCompletionTokens = true
	}
	if !out.HasPromptTokens && (out.HasCachedTokens || out.HasCacheWriteTokens) {
		out.PromptTokens = out.CachedTokens + out.CacheWriteTokens
		out.HasPromptTokens = true
	}

	// OpenAI prompt/input token totals already include cached tokens when details are present.
	// Anthropic Messages reports cache read/write tokens as separate top-level counters, so
	// add them into the total prompt side for billing and observability.
	promptIncludesCache := hasPromptTokens ||
		hasGeminiPromptTokens ||
		usage.Get("prompt_tokens_details").Exists() ||
		usage.Get("input_tokens_details").Exists() ||
		usage.Get("promptTokenCount").Exists() ||
		usage.Get("prompt_token_count").Exists()
	if out.HasPromptTokens && !promptIncludesCache {
		out.PromptTokens += out.CachedTokens + out.CacheWriteTokens
	}

	return out
}

func usageInt(usage gjson.Result, paths ...string) (int, bool) {
	for _, path := range paths {
		v := usage.Get(path)
		if v.Exists() {
			return int(v.Int()), true
		}
	}
	return 0, false
}

// checkedCostMicroUSD 用 fixed-point int64 + big.Int 守护的整数化 cost 计算。
//
// 公式：sum(tokens_i × pico_usd_per_token_i) ÷ 1e9 → micro_usd。
//
// fix CRITICAL Phase 3：价格从 USD/M-token float 改为 pico_usd/token int64。
// 所有乘法在 big.Int 中完成，最后只做一次整数除法，杜绝 float round 累积偏差。
//
// 负 token、负价格、异常高价或 int64 溢出都会破坏财务守恒。本函数 fail-closed：
// 异常返回 (0, false)，调用方不扣不计。
//
// 参数采用 (token, pricePicoPerToken) 6 对，与 deductQuota 费用项对齐。
// 0 价格档位（如无 cached price）传 0/0 即可，对结果无贡献。
//
// fix CRITICAL Sprint1-P0-4：pico_usd → micro_usd 转换使用 **ceil-div**（正数向上取整）。
// 旧实现 `total.Div(total, ...)` 是 floor，低价 × 小 token 请求（pico cost < 1e9）会被
// 截断到 0 micro_usd，形成"免费消耗"。改为 ceil 后：
//   - 0 pico → 0 micro_usd（保持）
//   - 1..1e9 pico → 1 micro_usd（最小 1 micro 收费）
//   - 1e9+k pico → 2 micro_usd（向上进位 1）
//
// 平台侧永不少收。pico 是 1e-15 USD，1 micro_usd 进位上限约 1e-6 USD，单请求误差可忽略。
func checkedCostMicroUSD(t1 int, p1 int64, t2 int, p2 int64, t3 int, p3 int64, t4 int, p4 int64, t5 int, p5 int64, t6 int, p6 int64) (int64, bool) {
	total := new(big.Int)
	add := func(tokens int, pricePico int64) bool {
		if tokens < 0 || pricePico < 0 || pricePico > database.MaxChannelModelPricePicoPerToken {
			return false
		}
		if tokens == 0 || pricePico == 0 {
			return true
		}
		term := new(big.Int).Mul(big.NewInt(int64(tokens)), big.NewInt(pricePico))
		total.Add(total, term)
		return true
	}
	if !add(t1, p1) || !add(t2, p2) || !add(t3, p3) || !add(t4, p4) || !add(t5, p5) || !add(t6, p6) {
		return 0, false
	}
	// Ceil-div：(total + divisor - 1) / divisor 对 total ≥ 0 等价于 ⌈total/divisor⌉
	divisor := big.NewInt(database.PicoPerMicroUSD)
	if total.Sign() > 0 {
		adjustment := new(big.Int).Sub(divisor, big.NewInt(1))
		total.Add(total, adjustment)
	}
	total.Quo(total, divisor)
	if !total.IsInt64() {
		return 0, false
	}
	return total.Int64(), true
}
