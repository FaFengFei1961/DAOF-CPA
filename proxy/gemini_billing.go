// Package proxy / gemini_billing.go
//
// M-R6 重构（2026-05-19）：从 gemini_native.go 1319 行单体抽出 billing 相关
// helper。最初是纯物理拆分，后续在本文件内补了 H2 / R5 / R8 三处实质修复
// （与 image_billing.go / video_billing.go 对齐），不再是"零改动"。

package proxy

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"daof-cpa/database"

	"gorm.io/gorm"
)

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
	// fix R8 (2026-05-19)：原仅 1 次尝试 + LOST-DEBT，其他 3 路（text/image/video）都已
	// 用 writeBillingWithRetry 做 3 次重试 + LOST-DEBT。统一到同样的可靠性级别。
	writeBillingWithRetry(entry, price.AmountMicroUSD, billing.ChargedCostMicroUSD, apiLogID, user.ID, modelName)
	return apiLogID
}

func deductGeminiBalanceAndLog(user *database.User, token, modelName string, geminiReq geminiNativeRequest, price geminiPriceResolution, billing BillingRuleResolution, channelType string, statusCode int, clientIP, path string, startTime time.Time) (uint, int64, database.ReferralPaidSpendRewardResult) {
	var apiLogID uint
	balanceConsumed := false
	var referralReward database.ReferralPaidSpendRewardResult
	referralRewardBPS, referralRewardWindowSeconds := readReferralPaidSpendRewardConfig()
	txErr := database.DB.Transaction(func(tx *gorm.DB) error {
		// fix H2：window tracking 必须在 CAS quota 前调用，与 text path 一致
		if !TryConsumeBalanceTx(tx, user.ID, price.AmountMicroUSD, true) {
			log.Printf("[GEMINI-BILLING-WINDOW-TRACK-FAIL] user=%d model=%s amount=%d", user.ID, modelName, price.AmountMicroUSD)
		}
		res := tx.Model(&database.User{}).
			Where("id = ? AND quota >= ?", user.ID, price.AmountMicroUSD).
			UpdateColumn("quota", gorm.Expr("quota - ?", price.AmountMicroUSD))
		if res.Error != nil {
			return fmt.Errorf("quota deduct: %w", res.Error)
		}
		balanceInsufficient := res.RowsAffected == 0
		// fix R5：余额不足 → pending_reconcile，ApiLog.Cost / ChargedCost 设 0 防污染报表
		apiLogCost := price.AmountMicroUSD
		apiLogChargedCost := billing.ChargedCostMicroUSD
		if balanceInsufficient {
			apiLogCost = 0
			apiLogChargedCost = 0
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
		if balanceInsufficient {
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

