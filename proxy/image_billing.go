// Package proxy / image_billing.go
//
// M-R6 重构（2026-05-19）：从 image_generation.go 1892 行单体抽出 billing 相关
// helper。最初是纯物理拆分（业务逻辑零改动），后续在本文件内补了 H2（window
// tracking 提前到 CAS 之前）+ R5（balanceInsufficient 时 ApiLog.Cost=0）两处
// 实质修复——这些是 monolith 时代就有的潜在 bug，拆分时一并修。

package proxy

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"daof-cpa/database"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

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
		// fix H2 (2026-05-19)：window tracking 必须在 CAS quota 前调用，与 text path 一致。
		// 原顺序在 quota 扣减后调用，window-track DB error 时会让 quota 已扣但 window 未增
		// → 攻击者可触发 transient error 绕过 window limit。
		if !TryConsumeBalanceTx(tx, user.ID, price.AmountMicroUSD, true) {
			log.Printf("[IMAGE-BILLING-WINDOW-TRACK-FAIL] user=%d model=%s raw_cost_micro=%d", user.ID, req.Model, price.AmountMicroUSD)
		}
		res := tx.Model(&database.User{}).
			Where("id = ? AND quota >= ?", user.ID, price.AmountMicroUSD).
			UpdateColumn("quota", gorm.Expr("quota - ?", price.AmountMicroUSD))
		if res.Error != nil {
			return fmt.Errorf("quota deduct: %w", res.Error)
		}
		balanceInsufficient := res.RowsAffected == 0
		// fix R5 (2026-05-19)：余额不足 → 走 pending_reconcile 分支，但 ApiLog
		// 已 INSERT 且 Cost=raw_cost 会污染 admin 报表 sum(cost) / sum(charged_cost)。
		// 改为：pending 分支 ApiLog.Cost=0 + ChargedCost=0，真实 raw cost 仅存
		// BillingEntry.EstimatedCostUSD 供对账员人工查看。
		apiLogCost := price.AmountMicroUSD
		apiLogChargedCost := billing.ChargedCostMicroUSD
		if balanceInsufficient {
			apiLogCost = 0
			apiLogChargedCost = 0
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
			Cost:                apiLogCost,
			ChargedCost:         apiLogChargedCost,
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
		if balanceInsufficient {
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
			RequestPath:    requestPathForImageRequest(req),
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
		RequestPath:    requestPathForImageRequest(req),
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

// requestPathForImageRequest 返回审计 ApiLogUsageLine.RequestPath，根据 imageGenerationRequest.Endpoint
// 区分 generations/edits；Endpoint 未设置时回退到 generations 保持 P1 之前的行为。
func requestPathForImageRequest(req imageGenerationRequest) string {
	if req.Endpoint != "" {
		return req.Endpoint
	}
	return database.EndpointImagesGenerations
}

func firstNonEmptyLocal(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// handleStreamingImageResponse 处理 gpt-image-2 的 SSE 流式响应。
//
// 流程：
//  1. 上游 4xx/5xx：复用非流式错误格式（不返回 SSE）+ 立即释放余额锁
//  2. 上游 2xx：设 SSE 响应头，进入 SetBodyStreamWriter callback
//  3. callback 内边读 scanner 边透传给客户端，监听 image_generation.completed /
//     image_edit.completed 事件累积 usage data
//  4. callback 末尾按情况计费：
//     a. 客户端断连 / 流意外早结束 / completed 缺 usage → pending reconcile（按 precheck estimate）
//     b. 正常 completed + 有 usage → 走非流式同款计费链路（commit / 套餐 / 余额 / referral）
//  5. callback 内最末释放余额锁
//
