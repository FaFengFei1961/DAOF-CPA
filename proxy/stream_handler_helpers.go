package proxy

// Package proxy / stream_handler_helpers.go
//
// fix Phase D-2 (2026-05-19)：把 ChatCompletionProxyHandler 1056 行单体拆出
// 早期 setup 阶段（auth / request 解析 / precheck / route 选择）。每个 helper
// 返回 (result, halt bool)：halt=true 表示已 send response，caller return nil
// 即可。业务行为完全等价。
//
// 余下的"retry loop + 上游执行 + commit"仍在 stream.go 顶层 handler 里，因为
// 闭包深 + 大量 retry-mutable state，移到外部反而破坏可读性。

import (
	"strings"
	"time"

	"daof-cpa/database"

	"github.com/gofiber/fiber/v2"
	"github.com/tidwall/gjson"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

// gatewayAuthCtx 是 setup 阶段解析出的鉴权 + 子凭证快照。
type gatewayAuthCtx struct {
	User       *database.User
	Token      string
	SubToken   *database.AccessToken
	IsSubToken bool
}

// gatewayResolveAuth 解析 Bearer / x-goog-api-key → user + 子凭证快照，
// 校验 user.Status + 子凭证 lifecycle/quota。失败 send response + halt=true。
func gatewayResolveAuth(c *fiber.Ctx, srcFormat sdktranslator.Format, clientIP, path string, startTime time.Time) (gatewayAuthCtx, bool) {
	authHeader := string([]byte(c.Get("Authorization")))
	token := ""
	if strings.HasPrefix(strings.TrimSpace(authHeader), "Bearer ") {
		token = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(authHeader), "Bearer "))
	}
	if token == "" && srcFormat == sdktranslator.FormatGemini {
		token = strings.TrimSpace(c.Get("x-goog-api-key"))
	}

	// fix CRITICAL Sprint4-M2：user + subToken 同一 RLock 段，保证一致快照
	authSnapshotMutex.RLock()
	user, exists := AuthCache[token]
	subToken, isSubToken := AuthTokenCache[token]
	authSnapshotMutex.RUnlock()

	if !exists {
		if shouldRecordInvalidAuthApiLog(clientIP) {
			recordProxyApiLog(0, token, "unknown", 401, clientIP, startTime, path, "auth_error", "Invalid API Key")
		}
		_ = c.Status(401).JSON(fiber.Map{"error": fiber.Map{"message": "Invalid API Key", "type": "auth_error"}})
		return gatewayAuthCtx{}, true
	}
	// fix Major（codex 第五轮）：纵深防御——封禁 user 在 RefreshUserAuth 漏过时也要拦
	if user.Status != 1 {
		authSnapshotMutex.Lock()
		delete(AuthCache, token)
		authSnapshotMutex.Unlock()
		recordProxyApiLog(user.ID, token, "unknown", 403, clientIP, startTime, path, "auth_error", "Account suspended")
		_ = c.Status(403).JSON(fiber.Map{"error": fiber.Map{"message": "Account suspended", "type": "auth_error"}})
		return gatewayAuthCtx{}, true
	}

	if isSubToken {
		if subToken.Status != 1 {
			recordProxyApiLog(user.ID, token, "unknown", 401, clientIP, startTime, path, "auth_error", "API Key is disabled or frozen")
			_ = c.Status(401).JSON(fiber.Map{"error": fiber.Map{"message": "API Key is disabled or frozen", "type": "auth_error"}})
			return gatewayAuthCtx{}, true
		}
		if subToken.ExpiredAt != nil && time.Now().After(*subToken.ExpiredAt) {
			recordProxyApiLog(user.ID, token, "unknown", 401, clientIP, startTime, path, "auth_error", "API Key has expired")
			_ = c.Status(401).JSON(fiber.Map{"error": fiber.Map{"message": "API Key has expired", "type": "auth_error"}})
			return gatewayAuthCtx{}, true
		}
		if subToken.QuotaLimit > 0 && subToken.UsedQuota >= subToken.QuotaLimit {
			recordProxyApiLog(user.ID, token, "unknown", 403, clientIP, startTime, path, "quota_exceeded", "API Key has reached its quota limit")
			_ = c.Status(403).JSON(fiber.Map{"error": fiber.Map{"message": "API Key has reached its quota limit", "type": "quota_exceeded"}})
			return gatewayAuthCtx{}, true
		}
	}

	return gatewayAuthCtx{User: user, Token: token, SubToken: subToken, IsSubToken: isSubToken}, false
}

// gatewayRequestCtx 是请求体解析出的不变量。
type gatewayRequestCtx struct {
	Body                 []byte
	ModelName            string
	IsStream             bool
	IsCountTokensRequest bool
	FallbackUserOptIn    bool
}

// gatewayResolveRequest 拷贝 body + 解析 model name + 推断 isStream / countTokens。
// modelName 空 → 400 response + halt=true。
func gatewayResolveRequest(c *fiber.Ctx, srcFormat sdktranslator.Format, path string, auth gatewayAuthCtx, clientIP string, startTime time.Time) (gatewayRequestCtx, bool) {
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
		recordProxyApiLog(auth.User.ID, auth.Token, "unknown", 400, clientIP, startTime, path, "invalid_request", "Model is required")
		_ = c.Status(400).JSON(fiber.Map{"error": fiber.Map{"message": "Model is required", "type": "invalid_request"}})
		return gatewayRequestCtx{}, true
	}

	isCountTokensRequest := isClaudeCountTokensPath(path)
	isStream := gjson.GetBytes(body, "stream").Bool()
	if srcFormat == sdktranslator.FormatGemini && isGeminiStreamPath(path) {
		isStream = true
	}
	return gatewayRequestCtx{
		Body:                 body,
		ModelName:            modelName,
		IsStream:             isStream,
		IsCountTokensRequest: isCountTokensRequest,
		FallbackUserOptIn:    fallbackUserOptIn,
	}, false
}

// gatewayRunPrecheck 跑 Decide(IsPrecheck=true)，处理 subscription / balance fallback。
// 成功返回 engineDecision（caller 用于后续 commit 阶段）。失败 send response + halt=true。
// 非 count_tokens 请求才调用；count_tokens 是免费辅助接口，跳过精检。
func gatewayRunPrecheck(c *fiber.Ctx, auth gatewayAuthCtx, req gatewayRequestCtx, path, clientIP string, startTime time.Time) (EngineDecision, bool) {
	user := auth.User
	token := auth.Token
	modelName := req.ModelName
	body := req.Body

	precheckInputTokens := estimatePrecheckTokens(body)
	precheckOutputTokens := 4096
	if maxTok := gjson.GetBytes(body, "max_tokens").Int(); maxTok > 0 {
		precheckOutputTokens = int(maxTok)
	} else if maxTok := gjson.GetBytes(body, "max_output_tokens").Int(); maxTok > 0 {
		precheckOutputTokens = int(maxTok) // OpenAI Responses API
	} else if maxTok := gjson.GetBytes(body, "max_completion_tokens").Int(); maxTok > 0 {
		precheckOutputTokens = int(maxTok)
	} else if maxTok := gjson.GetBytes(body, "generationConfig.maxOutputTokens").Int(); maxTok > 0 {
		precheckOutputTokens = int(maxTok)
	}
	if precheckOutputTokens > 100000 {
		precheckOutputTokens = 100000
	}
	precheckCostMicroUSD := estimatePrecheckBalanceDelta(modelName, precheckInputTokens, precheckOutputTokens)
	precheckBilling := ResolveBillingRules(modelName, body, 0, "", req.FallbackUserOptIn).WithCosts(precheckCostMicroUSD)
	decision := Decide(EngineRequest{
		UserID:       user.ID,
		ModelName:    modelName,
		InputTokens:  precheckInputTokens,
		OutputTokens: precheckOutputTokens,
		CostMicroUSD: precheckBilling.ChargedCostMicroUSD,
		IsPrecheck:   true,
	})
	if !decision.Allowed {
		msg := decision.BlockMessage
		if msg == "" {
			msg = "您的订阅额度已用尽，请购买套餐或充值余额"
		}
		// fix CRITICAL R23+2-C3：DB 加载失败 fail-closed 503，不是 402
		if decision.NeedsRetry {
			recordProxyApiLog(user.ID, token, modelName, 503, clientIP, startTime, path, "subscription_load_failed", msg)
			_ = c.Status(503).JSON(fiber.Map{"error": fiber.Map{"message": msg, "type": "service_unavailable", "code": "subscription_load_failed"}})
			return decision, true
		}
		if decision.BlockQuotaPlanID != 0 {
			msg = precheckLimitMessage(decision, precheckBilling)
			recordProxyApiLogWithPrecheck(user.ID, token, modelName, 402, clientIP, startTime, path, "request_estimate_exceeds_window_remaining", msg, precheckInputTokens, precheckOutputTokens, precheckBilling, decision)
			_ = c.Status(402).JSON(precheckLimitErrorPayload(msg, decision, precheckInputTokens, precheckOutputTokens, precheckBilling))
			return decision, true
		}
		recordProxyApiLog(user.ID, token, modelName, 402, clientIP, startTime, path, "subscription_required", msg)
		_ = c.Status(402).JSON(fiber.Map{"error": fiber.Map{"message": msg, "type": "subscription_required"}})
		return decision, true
	}
	// fallback 到余额：必须 (1) BalanceConsumeEnabled (2) 窗口限额未用尽 (3) 余额>0
	if decision.FallbackToBalance {
		if !user.BalanceConsumeEnabled {
			if decision.BlockQuotaPlanID != 0 {
				msg := precheckLimitMessage(decision, precheckBilling)
				recordProxyApiLogWithPrecheck(user.ID, token, modelName, 402, clientIP, startTime, path, "request_estimate_exceeds_window_remaining", msg, precheckInputTokens, precheckOutputTokens, precheckBilling, decision)
				_ = c.Status(402).JSON(precheckLimitErrorPayload(msg, decision, precheckInputTokens, precheckOutputTokens, precheckBilling))
				return decision, true
			}
			recordProxyApiLog(user.ID, token, modelName, 402, clientIP, startTime, path, "subscription_required", "subscription quota unavailable and balance consume disabled")
			_ = c.Status(402).JSON(fiber.Map{"error": fiber.Map{
				"message":      "当前请求无法使用订阅额度。请购买套餐，或在「账号设置 → 余额消费控制」中开启余额消费。",
				"type":         "subscription_required",
				"message_code": "ERR_QUOTA_EXHAUSTED_BALANCE_DISABLED",
			}})
			return decision, true
		}
		// 余额预检使用 rawCost：用户预付美元按上游真实成本扣，不应用订阅权重
		if !CheckBalanceConsumeAllowed(user, precheckBilling.RawCostMicroUSD) {
			recordProxyApiLog(user.ID, token, modelName, 402, clientIP, startTime, path, "balance_limit_reached", "balance consume window limit reached")
			_ = c.Status(402).JSON(fiber.Map{"error": fiber.Map{
				"message":      "本周期余额消费已达上限，请提高限额或等待下次重置。",
				"type":         "balance_limit_reached",
				"message_code": "ERR_BALANCE_LIMIT_REACHED",
			}})
			return decision, true
		}
		if user.Quota <= 0 {
			recordProxyApiLog(user.ID, token, modelName, 403, clientIP, startTime, path, "quota_exceeded", "insufficient balance")
			_ = c.Status(403).JSON(fiber.Map{"error": fiber.Map{
				"message":      "余额不足，请充值",
				"type":         "quota_exceeded",
				"message_code": "ERR_INSUFFICIENT_BALANCE",
			}})
			return decision, true
		}
	}
	// 决策结果暴露给后续 retry loop（subscription_decision locals 给 ApiLog 关联）
	//
	// 跨文件契约（Phase D-2 拆分后注释 — 2026-05-19）：
	// 这条 c.Locals 写入会被 stream.go ChatCompletionProxyHandler 的 commit closure
	// 通过 c.Locals("subscription_decision") 读回，用来把 EngineDecision 关联到
	// ApiLog.SubscriptionID。helper 同时**返回**了 decision 给 caller，但 caller 也
	// 必须保留这条 locals 写入——commit closure 在 helper 返回 *之后* 才会触发，
	// 且不在同一作用域，没法直接拿到 decision 变量。
	c.Locals("subscription_decision", decision)
	return decision, false
}

// gatewayResolveRoutes 选可服务的 channel route + endpoint policy 过滤。
// 找不到 → 404 model_not_found；endpoint policy 全 block → 400 unsupported_endpoint。
func gatewayResolveRoutes(c *fiber.Ctx, auth gatewayAuthCtx, req gatewayRequestCtx, path, clientIP string, startTime time.Time) ([]*database.ChannelModel, map[uint]*database.Channel, bool) {
	// fix CRITICAL Sprint4-M2：route + channel 同一 RLock 段，保证一致快照
	gatewayMutex.RLock()
	routes, hasRoute := RouteCache[req.ModelName]
	channelMapRef := ChannelMapCache
	gatewayMutex.RUnlock()

	if !hasRoute || len(routes) == 0 {
		recordProxyApiLog(auth.User.ID, auth.Token, req.ModelName, 404, clientIP, startTime, path, "model_not_found", "Model not available via any channel")
		_ = c.Status(404).JSON(fiber.Map{"error": fiber.Map{"message": "Model not available via any channel", "type": "model_not_found"}})
		return nil, nil, true
	}
	if filteredRoutes, blocked := filterRoutesByEndpointPolicy(routes, path, req.IsStream); len(filteredRoutes) == 0 && blocked > 0 {
		msg := unsupportedEndpointMessage(req.ModelName, path, req.IsStream)
		recordProxyApiLog(auth.User.ID, auth.Token, req.ModelName, 400, clientIP, startTime, path, "unsupported_endpoint", msg)
		_ = c.Status(400).JSON(fiber.Map{"error": fiber.Map{
			"message":      msg,
			"type":         "unsupported_endpoint",
			"message_code": "ERR_MODEL_ENDPOINT_UNSUPPORTED",
		}})
		return nil, nil, true
	} else if blocked > 0 {
		routes = filteredRoutes
	}
	return routes, channelMapRef, false
}
