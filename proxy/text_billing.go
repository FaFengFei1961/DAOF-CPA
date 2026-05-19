// Package proxy — text_billing.go
//
// 把 SSE (stream.go ChatCompletionProxyHandler) 与 WS (responses_websocket.go
// runResponsesWebsocketBridge) 共用的文本计费 pipeline 抽到顶层，让两条路径
// 调用同一组函数，从根上消除"改一边漏改另一边"的双写漂移风险。
//
// P8 重构（参考 plan_p8 评估报告）：
//   - 类型：[[ManualBillingStateInput]] / [[DeliveredCostEstimate]] / [[CommitTextContext]]
//   - 入口：[[CommitTextTurn]] / [[RecordManualBillingState]] / [[EstimateDeliveredCost]]
//   - 上下文：UpstreamRequestID(...) 抽 X-Request-Id；纯函数
//
// 设计原则：
//   - CommitTextContext 入参只读；handler 修改 apiErrorType/Message 后再调用
//   - 所有 DB 写入与外部副作用都在本文件，handler 只组装 context
//   - 与原闭包行为严格一一对应，不顺手做"看似合理的小修"
package proxy

import (
	"fmt"
	"log"
	"math/big"
	"net/http"
	"strings"
	"time"

	"daof-cpa/database"

	"gorm.io/gorm"
)

// ManualBillingStateInput 是写 pending_reconcile 审计 + 账单时的入参。
// 等同于 P7 之前 ChatCompletionProxyHandler 内部 type，提到顶层供 SSE 与 WS 共用。
type ManualBillingStateInput struct {
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

// DeliveredCostEstimate 是按 deliveredBytes 反推 output token 估算的成本快照。
// 同时给出 raw（上游成本口径）与 charged（订阅 modelWeight 调整后口径）两路。
// 仅用于 pending_reconcile 审计行的 EstimatedRawCost / EstimatedChargedCost。
type DeliveredCostEstimate struct {
	RawCostMicroUSD     int64
	ChargedCostMicroUSD int64
}

// TextUsage 是 CommitTextTurn 入参 usage 字段类型，等同于 stream.go 内部
// usageTokenCounts。保留 alias 方便 caller 显式引用。
type TextUsage = usageTokenCounts

// 抽完 CommitTextContext / CommitTextTurn / RecordManualBillingState 后，
// 把它们 append 到本文件下方（保持小步快跑，每步独立可验证）。

// channelTypeOfSelected 从 *database.Channel 抽 normalized channel type 字符串。
// 把原 closure selectedChannelTypeForBilling 提到顶层，纯函数无副作用。
func channelTypeOfSelected(ch *database.Channel) string {
	if ch == nil {
		return ""
	}
	return ch.Type
}

// UpstreamRequestID 从 upstream 响应头按优先级 X-Request-Id / X-Cpa-Request-Id /
// Request-Id 抽取请求 ID，sanitize 后返回；缺失时退到 api_log:<id> 或 local:<user>:<ts>
// 二级 fallback。headers 可为 nil（WS 路径无 HTTP 响应头）。
//
// 此函数纯函数（除了 sanitizeError 的字符串处理），无副作用。
func UpstreamRequestID(headers http.Header, apiLogID uint, userID uint, startTime time.Time) string {
	if headers != nil {
		for _, header := range []string{"X-Request-Id", "X-Cpa-Request-Id", "Request-Id"} {
			if v := strings.TrimSpace(headers.Get(header)); v != "" {
				return sanitizeError(v, 128)
			}
		}
	}
	if apiLogID > 0 {
		return fmt.Sprintf("api_log:%d", apiLogID)
	}
	return fmt.Sprintf("local:%d:%d", userID, startTime.UnixNano())
}

// EstimateDeliveredCost 按 SSE deliveredBytes 反推 output token 数后估算
// raw / charged cost 两路。仅用于 pending_reconcile 审计行；不真正扣费。
// 公式：outputTokens = ceil(deliveredBytes / 4)（与 deductQuota 原闭包一致），
// rawCost = estimatePrecheckBalanceDelta(model, prompt, output)，
// chargedCost = ResolveBillingRules(...).WithCosts(rawCost).ChargedCostMicroUSD
//
// 入参 channelType 是 *database.Channel.Type 的小写化值，给 ResolveBillingRules
// 用以判断 grok/claude/openai 等供应商。
func EstimateDeliveredCost(modelName string, body []byte, deliveredBytes int64, reasoningTokens int, channelType string, fallbackUserOptIn bool) DeliveredCostEstimate {
	outputTokens := 0
	if deliveredBytes > 0 {
		outputTokens = int((deliveredBytes + 3) / 4)
	}
	rawCost := estimatePrecheckBalanceDelta(modelName, estimatePrecheckTokens(body), outputTokens)
	resolution := ResolveBillingRules(modelName, body, reasoningTokens, channelType, fallbackUserOptIn).WithCosts(rawCost)
	return DeliveredCostEstimate{
		RawCostMicroUSD:     rawCost,
		ChargedCostMicroUSD: resolution.ChargedCostMicroUSD,
	}
}

// writeBillingWithRetry 把账单写入重试 3 次，全失败时打 LOST-DEBT 告警。
// 与原闭包同口径，仅参数化 logger 所需的 user/model 字段。
func writeBillingWithRetry(entry database.BillingEntryInput, rawCostMicroUSD, chargedCostMicroUSD int64, apiLogID uint, userID uint, modelName string) {
	var billErr error
	for attempt := 1; attempt <= 3; attempt++ {
		billErr = database.WriteBillingEntryNonFatal(entry)
		if billErr == nil {
			return
		}
		log.Printf("[BILLING-PENDING-WRITE] attempt %d/3 failed user=%d model=%s state=%s: %v", attempt, userID, modelName, entry.BillingState, billErr)
		if attempt < 3 {
			time.Sleep(100 * time.Millisecond)
		}
	}
	log.Printf("[BILLING-LOST-DEBT] user=%d model=%s raw_cost_micro=%d charged_cost_micro=%d api_log_id=%d state=%s UNRECOVERABLE — manual reconcile from ApiLog required: %v",
		userID, modelName, rawCostMicroUSD, chargedCostMicroUSD, apiLogID, entry.BillingState, billErr)
}

// CommitTextContext 把原 ChatCompletionProxyHandler 内 deductQuota /
// recordManualBillingState 两个闭包捕获的所有外部变量提到入参。
//
// 字段语义：
//   - 身份: User / Token / SubToken / IsSubToken
//   - 请求形状: ModelName / Body / Path / ClientIP / StartTime / IsStream / FallbackUserOptIn
//   - 路由: SelectedPath / SelectedChan
//   - 预检: EngineDecision（precheck 阶段决策；commit 阶段会再 Decide 一次）
//   - 上游元数据: UpstreamHeaders（WS 路径传 nil）
//
// 不可变性约定：所有 *database.User / *database.AccessToken / *database.ChannelModel /
// *database.Channel / EngineDecision 字段都是浅引用——caller 准备好后传入，
// 函数内部不写回这些指针（只读 / DB CAS / AuthTokenCache 内部锁同步）。
type CommitTextContext struct {
	User       *database.User
	Token      string
	SubToken   *database.AccessToken
	IsSubToken bool

	ModelName         string
	Body              []byte
	Path              string
	ClientIP          string
	StartTime         time.Time
	IsStream          bool
	FallbackUserOptIn bool

	SelectedPath *database.ChannelModel
	SelectedChan *database.Channel

	EngineDecision EngineDecision

	UpstreamHeaders http.Header
}

// RecordManualBillingState 写一条 pending_reconcile 审计 ApiLog + 账单。
// 用于 upstream 没返 usage / cost 算不出 / commit 阶段订阅 DB 加载失败等"已交付但
// 无法常规计费"的情况。与原闭包 recordManualBillingState 行为等价。
//
// 行为细节：
//   - in.EstimatedInputTokens<=0 时按 estimatePrecheckTokens(body) 兜底
//   - in.Status==0 默认 200
//   - in.EstimatedChargedCostMicroUSD<=0 但 raw>0 时按 ResolveBillingRules 推算
//   - reconcileCost：subscription fallback 路径用 raw（按上游成本对账）；
//     非 fallback（订阅命中或纯订阅失败但不走余额）用 charged
//   - ApiLog.Cost=0 / ChargedCost=0（这是"已交付未结算"状态，不能让常规毛利
//     报表把这些行误算为真实成本）
//   - writeBillingWithRetry 失败 3 次打 LOST-DEBT 告警，由 admin 按 ApiLog 手工补账
func RecordManualBillingState(ctx CommitTextContext, in ManualBillingStateInput) {
	if ctx.User == nil {
		log.Printf("[BILLING-CRITICAL] RecordManualBillingState called with nil User")
		return
	}
	if in.EstimatedInputTokens <= 0 {
		in.EstimatedInputTokens = estimatePrecheckTokens(ctx.Body)
	}
	if in.Status == 0 {
		in.Status = 200
	}
	selectedChannelType := channelTypeOfSelected(ctx.SelectedChan)
	estimatedRawCostMicroUSD := in.EstimatedRawCostMicroUSD
	if estimatedRawCostMicroUSD < 0 {
		estimatedRawCostMicroUSD = 0
	}
	resolution := ResolveBillingRules(ctx.ModelName, ctx.Body, in.ReasoningTokens, selectedChannelType, ctx.FallbackUserOptIn).WithCosts(estimatedRawCostMicroUSD)
	estimatedChargedCostMicroUSD := in.EstimatedChargedCostMicroUSD
	if estimatedChargedCostMicroUSD <= 0 && estimatedRawCostMicroUSD > 0 {
		estimatedChargedCostMicroUSD = resolution.ChargedCostMicroUSD
	}
	if estimatedChargedCostMicroUSD < 0 {
		estimatedChargedCostMicroUSD = 0
	}
	reconcileCostMicroUSD := estimatedRawCostMicroUSD
	if !ctx.EngineDecision.FallbackToBalance {
		reconcileCostMicroUSD = estimatedChargedCostMicroUSD
	}
	apiLog := database.ApiLog{
		UserID:              ctx.User.ID,
		TokenName:           HashTokenForLog(ctx.Token),
		ModelName:           ctx.ModelName,
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
		Latency:             time.Since(ctx.StartTime).Milliseconds(),
		Status:              in.Status,
		IPAddress:           ctx.ClientIP,
		RequestPath:         sanitizeError(ctx.Path, 160),
		ErrorType:           sanitizeError(in.ErrorType, 64),
		ErrorMessage:        sanitizeError(in.ErrorMessage, 512),
		PrecheckInputTokens: in.EstimatedInputTokens,
		PrecheckRawCost:     estimatedRawCostMicroUSD,
		PrecheckChargedCost: estimatedChargedCostMicroUSD,
		CreatedAt:           time.Now(),
	}
	apiLogPersisted := true
	if err := database.DB.Create(&apiLog).Error; err != nil {
		log.Printf("[BILLING-CRITICAL] user=%d model=%s manual-state api_log create failed: %v", ctx.User.ID, ctx.ModelName, err)
		apiLogPersisted = false
	}
	relatedID := uint(0)
	relatedType := ""
	if apiLogPersisted {
		relatedID = apiLog.ID
		relatedType = "api_log"
	}
	requestID := UpstreamRequestID(ctx.UpstreamHeaders, relatedID, ctx.User.ID, ctx.StartTime)
	// fix M2 (2026-05-19)：ctx.User.Quota 是请求入口时的 snapshot，到此处可能因并发
	// 扣费已过期。重读一次让 BalanceAfterUSD 准确，方便对账员决策（扣 vs 退）。
	currentBalance := ctx.User.Quota
	var fresh database.User
	if err := database.DB.Select("id, quota").First(&fresh, ctx.User.ID).Error; err == nil {
		currentBalance = fresh.Quota
	} else {
		log.Printf("[BILLING-MANUAL-REFRESH-FAIL] user=%d: %v (keep stale snapshot)", ctx.User.ID, err)
	}
	entry := database.BillingEntryInput{
		UserID:               ctx.User.ID,
		EntryType:            database.BillingTypeApiUsagePendingReconcile,
		BillingState:         in.BillingState,
		AmountUSD:            0,
		BalanceAfterUSD:      currentBalance,
		ModelName:            ctx.ModelName,
		TokensTotal:          in.PromptTokens + in.CompletionTokens,
		RequestID:            requestID,
		DeliveredBytes:       in.DeliveredBytes,
		EstimatedInputTokens: in.EstimatedInputTokens,
		EstimatedCostUSD:     reconcileCostMicroUSD,
		RelatedType:          relatedType,
		RelatedID:            relatedID,
		Description: fmt.Sprintf("[%s] %s · request_id=%s · delivered_bytes=%d · estimated_input_tokens=%d · estimated_cost=%s · reconcile_cost=%s · %s",
			in.ReasonTag, ctx.ModelName, requestID, in.DeliveredBytes, in.EstimatedInputTokens,
			FormatChargedCostForDescription(estimatedRawCostMicroUSD, estimatedChargedCostMicroUSD),
			database.FormatMicroUSD(reconcileCostMicroUSD), in.ErrorMessage),
	}
	writeBillingWithRetry(entry, estimatedRawCostMicroUSD, estimatedChargedCostMicroUSD, relatedID, ctx.User.ID, ctx.ModelName)
}

// CommitTextTurn 是文本计费 pipeline 的入口（替代 stream.go 原闭包 deductQuota）。
//
// 责任：
//  1. token clamp 防御性归零（cached ≤ prompt / cacheWrite 5m+1h 与 prompt 守恒 / reasoning ≤ completion）
//  2. failedRequest（status<200 || >=400）跳过扣费，仅写 ApiLog Cost=0
//  3. 计算 raw cost (micro_usd)：ContextPriceThreshold 高低档切换 / claude 1.25× cacheWrite fallback
//  4. ResolveBillingRules 算 charged cost（订阅 modelWeight × healthMultiplier）
//  5. 写 ApiLog 主表
//  6. 写 ApiLogUsageLine（input/output token 各一行）— P8 起两条路径都写
//  7. Decide(IsPrecheck=false) 决定订阅命中 vs fallback
//  8. 订阅命中：写 api_usage_sub 账单 + RecordApiLogRevenue(subscription)
//  9. 订阅未命中（fallback）：
//     - User.BalanceConsumeEnabled=false → UNAUTHORIZED-FALLBACK pending_reconcile
//     - User.BalanceConsumeEnabled=true → commitTextBalanceTurn 原子 CAS 扣余额
// 10. 子 token UsedQuota 累加（按 effectiveRevenue 真实归口；balanceConsumed 守卫）
//
// 返回值：
//   - true: pipeline 完成（含 pending_reconcile / UNAUTHORIZED-FALLBACK / 成功扣费）
//   - false 路径有两种（caller 应将之视为 5xx）：
//       1) ctx.User == nil 或 ctx.SelectedPath == nil（编程错误，caller 应返 500/502）
//       2) !IsStream && checkedCostMicroUSD 溢出（caller 应返 502）
//     流式路径若 cost 算不出会走 RecordManualBillingState 写 pending_reconcile，
//     仍返回 false 表示"未扣费成功"；caller 通常已经在 SSE 流内 200 给客户端，
//     false 返回值只用于非流路径的 HTTP 响应决策。
func CommitTextTurn(ctx CommitTextContext, usage TextUsage, status int, deliveredBytes int64, apiErrorType, apiErrorMessage string) bool {
	if ctx.User == nil || ctx.SelectedPath == nil {
		log.Printf("[BILLING-CRITICAL] CommitTextTurn called with nil User or SelectedPath")
		return false
	}

	promptTokens := usage.PromptTokens
	completionTokens := usage.CompletionTokens
	cachedTokens := usage.CachedTokens
	cacheWriteTokens := usage.CacheWriteTokens
	cacheWrite5mTokens := usage.CacheWrite5mTokens
	cacheWrite1hTokens := usage.CacheWrite1hTokens
	reasoningTokens := usage.ReasoningTokens

	// token clamp 防御
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

	failedRequest := status < 200 || status >= 400

	inputPricePico := ctx.SelectedPath.InputPricePicoPerToken
	outputPricePico := ctx.SelectedPath.OutputPricePicoPerToken
	cachedInputPricePico := ctx.SelectedPath.CachedInputPricePicoPerToken
	if ctx.SelectedPath.ContextPriceThreshold > 0 && promptTokens >= ctx.SelectedPath.ContextPriceThreshold {
		if ctx.SelectedPath.HighInputPricePicoPerToken > 0 {
			inputPricePico = ctx.SelectedPath.HighInputPricePicoPerToken
		}
		if ctx.SelectedPath.HighCachedInputPricePicoPerToken > 0 {
			cachedInputPricePico = ctx.SelectedPath.HighCachedInputPricePicoPerToken
		}
		if ctx.SelectedPath.HighOutputPricePicoPerToken > 0 {
			outputPricePico = ctx.SelectedPath.HighOutputPricePicoPerToken
		}
	}
	cacheWriteInputPricePico := ctx.SelectedPath.CacheWriteInputPricePicoPerToken
	if cacheWriteInputPricePico <= 0 {
		// fix R3 (2026-05-19)：原来只识别 "claude" 字串，claude 别名 alias 漏掉 1.25 倍。
		// 改为：只要请求里出现 cache_write tokens（说明上游实际收了写费），就按 Anthropic
		// 官方公式 1.25 × inputPrice 计算；其他模型（如 OpenAI/Gemini）若返回 cache_write
		// 也按 1.25× — 这是 Anthropic 公开口径，与 GPT/Gemini 大模型 prompt-caching API
		// 兼容 (OpenAI 5min cache write 也是 base × 1.25 倍率)。
		if cacheWriteTokens > 0 {
			cacheWriteInputPricePico = (inputPricePico * 125) / 100
		} else {
			cacheWriteInputPricePico = inputPricePico
		}
	}
	cacheWrite1hInputPricePico := ctx.SelectedPath.CacheWrite1hInputPricePicoPerToken
	if cacheWrite1hInputPricePico <= 0 {
		cacheWrite1hInputPricePico = inputPricePico * 2
	}

	nonReasoningCompletion := completionTokens - reasoningTokens
	if nonReasoningCompletion < 0 {
		nonReasoningCompletion = 0
	}
	standardInputTokens := promptTokens - cachedTokens - cacheWriteTokens
	if standardInputTokens < 0 {
		standardInputTokens = 0
	}

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
				ctx.User.ID, ctx.ModelName, promptTokens, completionTokens, cachedTokens, cacheWriteTokens, cacheWrite5mTokens, cacheWrite1hTokens, reasoningTokens,
				inputPricePico, outputPricePico, cachedInputPricePico, cacheWriteInputPricePico, cacheWrite1hInputPricePico)
			if ctx.IsStream {
				estimatedCost := EstimateDeliveredCost(ctx.ModelName, ctx.Body, deliveredBytes, reasoningTokens, channelTypeOfSelected(ctx.SelectedChan), ctx.FallbackUserOptIn)
				RecordManualBillingState(ctx, ManualBillingStateInput{
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
			}
			return false // caller 应 502；非流路径才用该返回值
		}

		// fix H3 (财务审计 2026-05-19)：cost=0 但请求成功（非 failedRequest）+ 有任意 token 消耗
		// → admin 大概率把 ChannelModel.*PricePicoPerToken 全配成 0，导致用户白嫖且无告警。
		// 这里写一个 pending_reconcile billing entry + log，让 admin 知道有零成本流量需要排查。
		if costMicroUSD == 0 && (promptTokens > 0 || completionTokens > 0) {
			log.Printf("[BILLING-CRITICAL] user=%d model=%s ZERO-COST-PRICING-MISCONFIG prompt=%d completion=%d cached=%d input_pico=%d output_pico=%d cached_pico=%d — request succeeded but cost=0; admin should verify ChannelModel pricing",
				ctx.User.ID, ctx.ModelName, promptTokens, completionTokens, cachedTokens,
				inputPricePico, outputPricePico, cachedInputPricePico)
			if ctx.IsStream {
				RecordManualBillingState(ctx, ManualBillingStateInput{
					BillingState:                 database.BillingStatePendingReconcile,
					ReasonTag:                    "ZERO-COST-PRICING-MISCONFIG",
					ErrorType:                    "billing_zero_cost",
					ErrorMessage:                 "request succeeded but cost is 0 — verify ChannelModel pricing",
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
					EstimatedRawCostMicroUSD:     0,
					EstimatedChargedCostMicroUSD: 0,
				})
			}
			// 不阻塞请求完成（fail-open，已 200 给客户端），仅 audit + 告警
		}
	}

	selectedChannelType := channelTypeOfSelected(ctx.SelectedChan)
	billingResolution := ResolveBillingRules(ctx.ModelName, ctx.Body, reasoningTokens, selectedChannelType, ctx.FallbackUserOptIn).WithCosts(costMicroUSD)
	chargedCostMicroUSD := billingResolution.ChargedCostMicroUSD

	apiLog := database.ApiLog{
		UserID:              ctx.User.ID,
		TokenName:           HashTokenForLog(ctx.Token),
		ModelName:           ctx.ModelName,
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
		Latency:             time.Since(ctx.StartTime).Milliseconds(),
		Status:              status,
		IPAddress:           ctx.ClientIP,
		RequestPath:         sanitizeError(ctx.Path, 160),
		ErrorType:           sanitizeError(apiErrorType, 64),
		ErrorMessage:        sanitizeError(apiErrorMessage, 512),
		CreatedAt:           time.Now(),
	}
	apiLogPersisted := true
	if err := database.DB.Create(&apiLog).Error; err != nil {
		log.Printf("[BILLING-CRITICAL] user=%d model=%s api_log create failed: %v", ctx.User.ID, ctx.ModelName, err)
		apiLogPersisted = false
	}

	// P8 起：SSE/WS 两条路径都写 ApiLogUsageLine（原 SSE 路径漏写）
	if apiLogPersisted && !failedRequest && (promptTokens > 0 || completionTokens > 0) {
		writeTextUsageLines(apiLog.ID, ctx.ModelName, ctx.Path, promptTokens, completionTokens, inputPricePico, outputPricePico)
	}

	// commit 阶段订阅决策（与 precheck 解耦）
	commitOK := false
	var effectiveRevenueMicroUSD int64
	var commitDecision EngineDecision
	if !failedRequest {
		commitDecision = Decide(EngineRequest{
			UserID:       ctx.User.ID,
			ModelName:    ctx.ModelName,
			InputTokens:  promptTokens,
			OutputTokens: completionTokens,
			CostMicroUSD: chargedCostMicroUSD,
			IsPrecheck:   false,
		})
		commitOK = commitDecision.Allowed && !commitDecision.FallbackToBalance
		if !commitOK {
			log.Printf("[BILLING-FALLBACK] user=%d model=%s raw_cost_micro=%d charged_cost_micro=%d reason=%s allowed=%v fallback_balance=%v sub=%d plan=%d needs_retry=%v",
				ctx.User.ID, ctx.ModelName, costMicroUSD, chargedCostMicroUSD, commitDecision.BlockReason,
				commitDecision.Allowed, commitDecision.FallbackToBalance,
				commitDecision.SubscriptionID, commitDecision.QuotaPlanID, commitDecision.NeedsRetry)
		}
	}

	// 订阅 DB 加载失败 → 写 DB-RETRY pending_reconcile，不进 sub 账单也不 fallback 余额
	if !failedRequest && commitDecision.NeedsRetry {
		log.Printf("[BILLING-DB-RETRY] user=%d model=%s raw_cost_micro=%d charged_cost_micro=%d sub-load failed, recording for manual reconcile",
			ctx.User.ID, ctx.ModelName, costMicroUSD, chargedCostMicroUSD)
		relatedID := uint(0)
		relatedType := ""
		if apiLogPersisted {
			relatedID = apiLog.ID
			relatedType = "api_log"
		}
		pendingEntry := database.BillingEntryInput{
			UserID:               ctx.User.ID,
			EntryType:            database.BillingTypeApiUsagePendingReconcile,
			BillingState:         database.BillingStatePendingReconcile,
			AmountUSD:            0,
			BalanceAfterUSD:      ctx.User.Quota,
			ModelName:            ctx.ModelName,
			TokensTotal:          promptTokens + completionTokens,
			RequestID:            UpstreamRequestID(ctx.UpstreamHeaders, relatedID, ctx.User.ID, ctx.StartTime),
			EstimatedInputTokens: promptTokens,
			EstimatedCostUSD:     costMicroUSD,
			RelatedType:          relatedType,
			RelatedID:            relatedID,
			Description: fmt.Sprintf("[DB-RETRY] %s · %d+%d tokens · %s 待对账（订阅 DB 加载失败）",
				ctx.ModelName, promptTokens, completionTokens, FormatChargedCostForDescription(costMicroUSD, chargedCostMicroUSD)),
		}
		writeBillingWithRetry(pendingEntry, costMicroUSD, chargedCostMicroUSD, relatedID, ctx.User.ID, ctx.ModelName)
		return true
	}

	// 订阅命中：写 sub 账单 + Revenue
	if commitOK {
		subID := commitDecision.SubscriptionID
		tokensTotal := promptTokens + completionTokens
		relatedID := uint(0)
		relatedType := ""
		if apiLogPersisted {
			relatedID = apiLog.ID
			relatedType = "api_log"
		}
		// fix R6 (2026-05-19)：订阅审计 BillingEntry 改用 writeBillingWithRetry（3 次重试
		// + LOST-DEBT 日志），与 pending_reconcile 路径同样的可靠性。原 NonFatal 单次
		// 尝试失败 → 仅一行 log，admin 无法对账（quota 已扣但 BillingEntry 缺失）。
		writeBillingWithRetry(database.BillingEntryInput{
			UserID:               ctx.User.ID,
			EntryType:            database.BillingTypeApiUsageSub,
			AmountUSD:            0,
			BalanceAfterUSD:      ctx.User.Quota,
			ModelName:            ctx.ModelName,
			TokensTotal:          tokensTotal,
			SourceSubscriptionID: &subID,
			RelatedType:          relatedType,
			RelatedID:            relatedID,
			Description:          fmt.Sprintf("套餐 · %s · %d tokens · %s", ctx.ModelName, tokensTotal, FormatChargedCostForDescription(costMicroUSD, chargedCostMicroUSD)),
		}, costMicroUSD, chargedCostMicroUSD, relatedID, ctx.User.ID, ctx.ModelName)
		effectiveRevenueMicroUSD = subscriptionRevenueMicroUSD(chargedCostMicroUSD, commitDecision.SubscriptionIsGranted)
		if apiLogPersisted {
			RecordApiLogRevenue(apiLog.ID, database.RevenueSourceSubscription, effectiveRevenueMicroUSD, subID)
		}
	}

	// 订阅未命中 + cost > 0 → fallback 余额或 UNAUTHORIZED pending
	if !commitOK && chargedCostMicroUSD > 0 {
		if !ctx.User.BalanceConsumeEnabled {
			log.Printf("[BILLING-PENDING-DEBT] user=%d model=%s raw_cost_micro=%d charged_cost_micro=%d UNAUTHORIZED-FALLBACK reason=subscription_drained_during_request balance_consume_disabled — recording for admin reconcile",
				ctx.User.ID, ctx.ModelName, costMicroUSD, chargedCostMicroUSD)
			relatedID := uint(0)
			relatedType := ""
			if apiLogPersisted {
				relatedID = apiLog.ID
				relatedType = "api_log"
			}
			pendingEntry := database.BillingEntryInput{
				UserID:               ctx.User.ID,
				EntryType:            database.BillingTypeApiUsagePendingReconcile,
				BillingState:         database.BillingStatePendingReconcile,
				AmountUSD:            0,
				BalanceAfterUSD:      ctx.User.Quota,
				ModelName:            ctx.ModelName,
				TokensTotal:          promptTokens + completionTokens,
				RequestID:            UpstreamRequestID(ctx.UpstreamHeaders, relatedID, ctx.User.ID, ctx.StartTime),
				EstimatedInputTokens: promptTokens,
				EstimatedCostUSD:     costMicroUSD,
				RelatedType:          relatedType,
				RelatedID:            relatedID,
				Description: fmt.Sprintf("[UNAUTHORIZED-FALLBACK] %s · %d+%d tokens · %s 待对账（订阅 commit 期被耗尽 + 余额消费禁用）",
					ctx.ModelName, promptTokens, completionTokens, FormatChargedCostForDescription(costMicroUSD, chargedCostMicroUSD)),
			}
			writeBillingWithRetry(pendingEntry, costMicroUSD, chargedCostMicroUSD, relatedID, ctx.User.ID, ctx.ModelName)
		} else {
			effectiveRevenueMicroUSD = commitTextBalanceTurn(ctx, apiLog.ID, apiLogPersisted, costMicroUSD, chargedCostMicroUSD, promptTokens, completionTokens)
		}
	}

	// 子 token UsedQuota 累加（balanceConsumed 守卫已在 commitTextBalanceTurn 内通过返回值反映）
	if ctx.IsSubToken && ctx.SubToken != nil && effectiveRevenueMicroUSD > 0 && status >= 200 && status < 400 {
		res := database.DB.Model(&database.AccessToken{}).
			Where("id = ?", ctx.SubToken.ID).
			UpdateColumn("used_quota", gorm.Expr("used_quota + ?", effectiveRevenueMicroUSD))
		if res.Error != nil {
			log.Printf("[SUB-TOKEN-CRITICAL] token_id=%d effective_revenue_micro=%d UsedQuota-UPDATE-FAILED: %v", ctx.SubToken.ID, effectiveRevenueMicroUSD, res.Error)
		} else if res.RowsAffected == 0 {
			log.Printf("[SUB-TOKEN-CRITICAL] token_id=%d effective_revenue_micro=%d token-not-found-at-commit", ctx.SubToken.ID, effectiveRevenueMicroUSD)
		} else {
			if ctx.SubToken.QuotaLimit > 0 && ctx.SubToken.UsedQuota+effectiveRevenueMicroUSD > ctx.SubToken.QuotaLimit {
				log.Printf("[SUB-TOKEN-OVERLIMIT] token_id=%d effective_revenue_micro=%d used-quota-exceeded-limit", ctx.SubToken.ID, effectiveRevenueMicroUSD)
			}
			authSnapshotMutex.Lock()
			if existing, ok := AuthTokenCache[ctx.Token]; ok {
				updated := *existing
				updated.UsedQuota += effectiveRevenueMicroUSD
				AuthTokenCache[ctx.Token] = &updated
			}
			authSnapshotMutex.Unlock()
		}
	}
	return true
}

// commitTextBalanceTurn 在订阅未命中 + BalanceConsumeEnabled=true 时走原子 CAS 扣余额。
// 与原闭包 deductQuotaAtomic 一一对应。
//
// 返回 effectiveRevenueMicroUSD：CAS 成功才记 revenue；pending_reconcile 路径返回 0。
// 该返回值用来：(1) 写 ApiLogRevenue (2) 累加子 token UsedQuota（caller 处理）。
func commitTextBalanceTurn(ctx CommitTextContext, apiLogID uint, apiLogPersisted bool, costMicroUSD, chargedCostMicroUSD int64, promptTokens, completionTokens int) int64 {
	balanceConsumeMicroUSD := costMicroUSD // 余额按 rawCost 扣（产品策略，不是 bug 回退）
	balanceConsumed := false
	var referralReward database.ReferralPaidSpendRewardResult
	referralRewardBPS, referralRewardWindowSeconds := readReferralPaidSpendRewardConfig()

	txErr := database.DB.Transaction(func(tx *gorm.DB) error {
		if !TryConsumeBalanceTx(tx, ctx.User.ID, balanceConsumeMicroUSD, true /* forceTrack */) {
			log.Printf("[BILLING-WINDOW-TRACK-FAIL] user=%d model=%s raw_cost_micro=%d charged_cost_micro=%d forceTrack failed (DB issue), continuing quota deduct", ctx.User.ID, ctx.ModelName, balanceConsumeMicroUSD, chargedCostMicroUSD)
		}

		res := tx.Model(&database.User{}).
			Where("id = ? AND quota >= ?", ctx.User.ID, balanceConsumeMicroUSD).
			UpdateColumn("quota", gorm.Expr("quota - ?", balanceConsumeMicroUSD))
		if res.Error != nil {
			return fmt.Errorf("quota deduct: %w", res.Error)
		}

		tokensTotal := promptTokens + completionTokens
		relatedID := uint(0)
		relatedType := ""
		if apiLogPersisted {
			relatedID = apiLogID
			relatedType = "api_log"
		}

		if res.RowsAffected == 0 {
			// CAS 失败：重查区分用户缺失 vs 余额不足
			var u database.User
			if err := tx.Select("id, quota").First(&u, ctx.User.ID).Error; err != nil {
				return fmt.Errorf("user row missing: %w", err)
			}
			log.Printf("[BILLING-INSUFFICIENT-BALANCE] user=%d model=%s raw_cost_micro=%d charged_cost_micro=%d current_quota=%d — recording pending_reconcile (service already delivered)",
				ctx.User.ID, ctx.ModelName, balanceConsumeMicroUSD, chargedCostMicroUSD, u.Quota)
			return database.WriteBillingEntry(tx, database.BillingEntryInput{
				UserID:               ctx.User.ID,
				EntryType:            database.BillingTypeApiUsagePendingReconcile,
				BillingState:         database.BillingStatePendingReconcile,
				AmountUSD:            0,
				BalanceAfterUSD:      u.Quota,
				ModelName:            ctx.ModelName,
				TokensTotal:          tokensTotal,
				RequestID:            UpstreamRequestID(ctx.UpstreamHeaders, relatedID, ctx.User.ID, ctx.StartTime),
				EstimatedInputTokens: promptTokens,
				EstimatedCostUSD:     balanceConsumeMicroUSD,
				RelatedType:          relatedType,
				RelatedID:            relatedID,
				Description: fmt.Sprintf("[INSUFFICIENT-BALANCE] %s · %d tokens · 余额不足，已交付服务待对账（按 raw 上游成本计 $%s）",
					ctx.ModelName, tokensTotal, database.FormatMicroUSD(balanceConsumeMicroUSD)),
			})
		}

		var freshUser database.User
		if err := tx.Select("id, quota").First(&freshUser, ctx.User.ID).Error; err != nil {
			return fmt.Errorf("re-select quota: %w", err)
		}

		if err := database.WriteBillingEntry(tx, database.BillingEntryInput{
			UserID:          ctx.User.ID,
			EntryType:       database.BillingTypeApiConsumeBalance,
			AmountUSD:       -balanceConsumeMicroUSD,
			BalanceAfterUSD: freshUser.Quota,
			ModelName:       ctx.ModelName,
			TokensTotal:     tokensTotal,
			RelatedType:     relatedType,
			RelatedID:       relatedID,
			Description:     fmt.Sprintf("余额扣费 · %s · %d tokens · %s", ctx.ModelName, tokensTotal, FormatChargedCostForDescription(costMicroUSD, chargedCostMicroUSD)),
		}); err != nil {
			return fmt.Errorf("write billing: %w", err)
		}
		reward, err := database.ApplyReferralPaidSpendRewardTx(
			tx,
			ctx.User.ID,
			balanceConsumeMicroUSD,
			referralRewardBPS,
			referralRewardWindowSeconds,
			time.Now(),
			relatedType,
			relatedID,
			fmt.Sprintf("余额扣费 · %s", ctx.ModelName),
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
			ctx.User.ID, ctx.ModelName, costMicroUSD, chargedCostMicroUSD, txErr)
		return 0
	}

	if balanceConsumed {
		if apiLogPersisted {
			RecordApiLogRevenue(apiLogID, database.RevenueSourceBalance, balanceConsumeMicroUSD, 0)
		}
		RefreshUserAuth(ctx.User.ID)
		if referralReward.ReferrerID != 0 && referralReward.RewardMicroUSD > 0 {
			RefreshUserAuth(referralReward.ReferrerID)
		}
		return balanceConsumeMicroUSD
	}
	// CAS 失败（pending_reconcile）：仍要 RefreshUserAuth 让缓存与 DB 一致
	RefreshUserAuth(ctx.User.ID)
	return 0
}

// writeTextUsageLines 写一对 ApiLogUsageLine（input + output token）。
// P8 起 SSE/WS 两条路径都写，让 admin UI 能看到 token 计量明细。
//
// fix D2 (2026-05-19)：原实现用 floor (`tokens × pico / 1e9`)，与 CommitTextTurn 主路径
// checkedCostMicroUSD 的 ceil-div 不一致，每 1M token 差 1 micro_usd 级别 → admin
// 报表"按 usage line"vs"按 api_log.cost"系统性差额。现统一改 ceil-div。
func writeTextUsageLines(apiLogID uint, modelName, requestPath string, promptTokens, completionTokens int, inputPricePico, outputPricePico int64) {
	now := time.Now()
	// fix SF-C1 (2026-05-19)：原 `_ = DB.Create().Error` 静默丢弃错误。usage lines
	// 不影响真实扣费（金额已在 ApiLog.cost 落地），但 admin token-level 报表是
	// 按 ApiLogUsageLine 聚合的，写失败会让 admin 看到 0 且无任何告警。改为
	// 记 `[BILLING-USAGE-LINE-LOST]` 日志，便于后期对账。
	if promptTokens > 0 {
		amountMicro := ceilDivPicoToMicro(int64(promptTokens), inputPricePico)
		if err := database.DB.Create(&database.ApiLogUsageLine{
			ApiLogID:       apiLogID,
			ModelName:      modelName,
			RequestPath:    sanitizeError(requestPath, 160),
			Unit:           "token",
			Direction:      "input",
			Quantity:       int64(promptTokens),
			UnitPriceMicro: inputPricePico / int64(1_000_000_000),
			AmountMicroUSD: amountMicro,
			CostSource:     "upstream_usage",
			CreatedAt:      now,
		}).Error; err != nil {
			log.Printf("[BILLING-USAGE-LINE-LOST] api_log_id=%d direction=input tokens=%d amount_micro=%d: %v",
				apiLogID, promptTokens, amountMicro, err)
		}
	}
	if completionTokens > 0 {
		amountMicro := ceilDivPicoToMicro(int64(completionTokens), outputPricePico)
		if err := database.DB.Create(&database.ApiLogUsageLine{
			ApiLogID:       apiLogID,
			ModelName:      modelName,
			RequestPath:    sanitizeError(requestPath, 160),
			Unit:           "token",
			Direction:      "output",
			Quantity:       int64(completionTokens),
			UnitPriceMicro: outputPricePico / int64(1_000_000_000),
			AmountMicroUSD: amountMicro,
			CostSource:     "upstream_usage",
			CreatedAt:      now,
		}).Error; err != nil {
			log.Printf("[BILLING-USAGE-LINE-LOST] api_log_id=%d direction=output tokens=%d amount_micro=%d: %v",
				apiLogID, completionTokens, amountMicro, err)
		}
	}
}

// ceilDivPicoToMicro 把 (tokens × pico_per_token) ÷ 1e9 转成 micro_usd，
// 用 big.Int 做 ceil-div 避开 int64 中间溢出 + 与 checkedCostMicroUSD 一致。
// fix D2：原 floor 截断让 ApiLogUsageLine.AmountMicroUSD 与 ApiLog.Cost 系统性差额。
func ceilDivPicoToMicro(tokens, picoPerToken int64) int64 {
	if tokens <= 0 || picoPerToken <= 0 {
		return 0
	}
	prod := new(big.Int).Mul(big.NewInt(tokens), big.NewInt(picoPerToken))
	div := big.NewInt(1_000_000_000)
	rem := new(big.Int)
	q, rem := new(big.Int).QuoRem(prod, div, rem)
	if rem.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}
	if !q.IsInt64() {
		return 0 // 极端溢出 fail-closed；调用方仍记 token 数，AmountMicroUSD=0
	}
	return q.Int64()
}
