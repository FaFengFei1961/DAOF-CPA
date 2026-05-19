// Package proxy / video_billing.go
//
// M-R6 重构（2026-05-19）：从 video_generation.go 1131 行抽出 billing 相关
// helper，纯文件物理拆分。业务逻辑零改动。

package proxy

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"daof-cpa/database"

	"github.com/tidwall/gjson"
	"gorm.io/gorm"
)

func createVideoApiLog(userID uint, token string, req videoGenerationRequest, price videoPriceResolution, billing BillingRuleResolution, channelType string, statusCode int, clientIP, path string, startTime time.Time) uint {
	apiLog := database.ApiLog{
		UserID:              userID,
		TokenName:           HashTokenForLog(token),
		ModelName:           req.Model,
		RequestedModel:      billing.RequestedModel,
		ServedModel:         billing.ServedModel,
		PromptTokens:        0,
		CompletionTokens:    0,
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
			ModelName:      req.Model,
			RequestPath:    requestPathForVideoRequest(req),
			Unit:           "video_second",
			Direction:      "output",
			Quantity:       price.Quantity,
			UnitPriceMicro: price.UnitPriceMicro,
			AmountMicroUSD: price.AmountMicroUSD,
			PricingRuleID:  price.RuleID,
			CostSource:     price.CostSource,
			Size:           price.Size,
			Resolution:     price.Resolution,
			AspectRatio:    price.AspectRatio,
			MetadataJSON:   videoUsageMetadata(req, price),
			CreatedAt:      time.Now(),
		}
		return tx.Create(&line).Error
	})
	if err != nil {
		log.Printf("[VIDEO-BILLING-CRITICAL] api log/usage line create failed user=%d model=%s: %v", userID, req.Model, err)
		return 0
	}
	return apiLog.ID
}

func recordVideoPendingReconcile(user *database.User, token string, req videoGenerationRequest, price videoPriceResolution, billing BillingRuleResolution, channelType string, statusCode int, clientIP, path string, startTime time.Time, reason string) uint {
	if user == nil {
		return 0
	}
	apiLogID := createVideoApiLog(user.ID, token, req, price, billing, channelType, statusCode, clientIP, path, startTime)
	entry := database.BillingEntryInput{
		UserID:           user.ID,
		EntryType:        database.BillingTypeApiUsagePendingReconcile,
		BillingState:     database.BillingStatePendingReconcile,
		AmountUSD:        0,
		BalanceAfterUSD:  user.Quota,
		ModelName:        req.Model,
		TokensTotal:      int(price.Quantity),
		RequestID:        videoRequestID(user.ID, startTime, apiLogID),
		EstimatedCostUSD: price.AmountMicroUSD,
		RelatedType:      relatedTypeForApiLog(apiLogID),
		RelatedID:        apiLogID,
		Description:      fmt.Sprintf("[VIDEO-PENDING] %s · %d 秒视频 · %s 待对账（%s）", req.Model, price.Quantity, FormatChargedCostForDescription(price.AmountMicroUSD, billing.ChargedCostMicroUSD), reason),
	}
	var billErr error
	for attempt := 1; attempt <= 3; attempt++ {
		billErr = database.WriteBillingEntryNonFatal(entry)
		if billErr == nil {
			break
		}
		log.Printf("[VIDEO-BILLING-PENDING-RETRY] attempt=%d/3 user=%d model=%s: %v", attempt, user.ID, req.Model, billErr)
		if attempt < 3 {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if billErr != nil {
		log.Printf("[VIDEO-BILLING-LOST-DEBT] user=%d model=%s raw_cost_micro=%d charged_cost_micro=%d api_log_id=%d UNRECOVERABLE - manual reconcile from ApiLog required: %v",
			user.ID, req.Model, price.AmountMicroUSD, billing.ChargedCostMicroUSD, apiLogID, billErr)
	}
	return apiLogID
}

func deductVideoBalanceAndLog(user *database.User, token string, req videoGenerationRequest, price videoPriceResolution, billing BillingRuleResolution, channelType string, statusCode int, clientIP, path string, startTime time.Time) (uint, int64, database.ReferralPaidSpendRewardResult) {
	var apiLogID uint
	var balanceAfter int64
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
			ModelName:           req.Model,
			RequestedModel:      billing.RequestedModel,
			ServedModel:         billing.ServedModel,
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
			ModelName:      req.Model,
			RequestPath:    requestPathForVideoRequest(req),
			Unit:           "video_second",
			Direction:      "output",
			Quantity:       price.Quantity,
			UnitPriceMicro: price.UnitPriceMicro,
			AmountMicroUSD: price.AmountMicroUSD,
			PricingRuleID:  price.RuleID,
			CostSource:     price.CostSource,
			Size:           price.Size,
			Resolution:     price.Resolution,
			AspectRatio:    price.AspectRatio,
			MetadataJSON:   videoUsageMetadata(req, price),
			CreatedAt:      time.Now(),
		}).Error; err != nil {
			return fmt.Errorf("create usage line: %w", err)
		}
		if res.RowsAffected == 0 {
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
				TokensTotal:      int(price.Quantity),
				RequestID:        videoRequestID(user.ID, startTime, apiLogID),
				EstimatedCostUSD: price.AmountMicroUSD,
				RelatedType:      "api_log",
				RelatedID:        apiLogID,
				Description:      fmt.Sprintf("[VIDEO-INSUFFICIENT-BALANCE] %s · %d 秒视频 · 余额不足，已交付服务待对账（按 raw 上游成本计 $%s）", req.Model, price.Quantity, database.FormatMicroUSD(price.AmountMicroUSD)),
			})
		}
		if !TryConsumeBalanceTx(tx, user.ID, price.AmountMicroUSD, true) {
			log.Printf("[VIDEO-BILLING-WINDOW-TRACK-FAIL] user=%d model=%s raw_cost_micro=%d", user.ID, req.Model, price.AmountMicroUSD)
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
			RelatedType:     "api_log",
			RelatedID:       apiLogID,
			Description:     fmt.Sprintf("余额扣费 · %s · %d 秒视频 · %s", req.Model, price.Quantity, FormatChargedCostForDescription(price.AmountMicroUSD, billing.ChargedCostMicroUSD)),
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
			fmt.Sprintf("视频生成 · %s", req.Model),
		)
		if err != nil {
			return fmt.Errorf("apply referral spend reward: %w", err)
		}
		referralReward = reward
		balanceConsumed = true
		return nil
	})
	if txErr != nil {
		log.Printf("[VIDEO-BILLING-CRITICAL] user=%d model=%s balance tx failed: %v", user.ID, req.Model, txErr)
		apiLogID = recordVideoPendingReconcile(user, token, req, price, billing, channelType, statusCode, clientIP, path, startTime, "balance transaction failed")
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

// requestPathForVideoRequest 返回审计 ApiLogUsageLine.RequestPath，根据 videoGenerationRequest.Endpoint
// 区分 generations/edits/extensions；Endpoint 未设置时回退到 generations。
func requestPathForVideoRequest(req videoGenerationRequest) string {
	if req.Endpoint != "" {
		return req.Endpoint
	}
	return database.EndpointVideosGenerations
}

func videoUsageMetadata(req videoGenerationRequest, price videoPriceResolution) string {
	b, _ := json.Marshal(map[string]any{
		"duration":        req.DurationSeconds,
		"size":            req.Size,
		"resolution":      req.Resolution,
		"aspect_ratio":    req.AspectRatio,
		"billed_quantity": price.Quantity,
		"cost_source":     price.CostSource,
		"cost_ticks":      price.CostTicks,
	})
	return string(b)
}

func videoRequestID(userID uint, startTime time.Time, apiLogID uint) string {
	if apiLogID > 0 {
		return fmt.Sprintf("api_log:%d", apiLogID)
	}
	return fmt.Sprintf("local:%d:%d", userID, startTime.UnixNano())
}

func recordVideoGenerationJob(userID uint, channelID uint, modelName string, requestPath string, body []byte, apiLogID uint) {
	requestID := strings.TrimSpace(gjson.GetBytes(body, "request_id").String())
	if requestID == "" {
		requestID = strings.TrimSpace(gjson.GetBytes(body, "id").String())
	}
	if !validVideoRequestID(requestID) {
		log.Printf("[VIDEO-JOB-MISSING] user=%d model=%s api_log_id=%d response omitted valid request_id", userID, modelName, apiLogID)
		return
	}
	job := database.MediaGenerationJob{
		RequestID:      requestID,
		UserID:         userID,
		ChannelID:      channelID,
		ModelName:      modelName,
		RequestPath:    sanitizeError(requestPath, 160),
		CreateApiLogID: apiLogID,
		CreatedAt:      time.Now(),
	}
	if err := database.DB.Create(&job).Error; err != nil {
		log.Printf("[VIDEO-JOB-CREATE-FAIL] user=%d model=%s request_id=%s api_log_id=%d: %v", userID, modelName, requestID, apiLogID, err)
	}
}

func validVideoRequestID(requestID string) bool {
	if requestID == "" || len(requestID) > 160 {
		return false
	}
	for _, r := range requestID {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			continue
		}
		switch r {
		case '_', '-', '.', ':':
			continue
		default:
			return false
		}
	}
	return true
}
