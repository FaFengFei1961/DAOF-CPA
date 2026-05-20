// Package proxy / video_billing.go
//
// M-R6 重构（2026-05-19）：从 video_generation.go 1131 行抽出 billing 相关
// helper。最初是纯物理拆分，后续在本文件内补了 H2 / R5 / SF-H4 三处实质修复
// （与 image_billing.go 对齐），不再是"零改动"。

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
	// fix SF-H4 (2026-05-19)：原手写 3-retry 用 WriteBillingEntryNonFatal（单次尝试），
	// 与 gemini / text / image 三路统一改用 writeBillingWithRetry，
	// 自动产出 [BILLING-LOST-DEBT] 告警以备对账。
	writeBillingWithRetry(entry, price.AmountMicroUSD, billing.ChargedCostMicroUSD, apiLogID, user.ID, req.Model)
	return apiLogID
}

func deductVideoBalanceAndLog(user *database.User, token string, req videoGenerationRequest, price videoPriceResolution, billing BillingRuleResolution, channelType string, statusCode int, clientIP, path string, startTime time.Time) (uint, int64, database.ReferralPaidSpendRewardResult) {
	// fix A-P0-3：合并到 DeductMediaBalanceAndLog 统一流程
	return DeductMediaBalanceAndLog(MediaBillingInput{
		User:           user,
		Token:          token,
		ModelName:      req.Model,
		ClientIP:       clientIP,
		Path:           path,
		StartTime:      startTime,
		AmountMicroUSD: price.AmountMicroUSD,
		Billing:        billing,
		ChannelType:    channelType,
		StatusCode:     statusCode,
		TokensTotal:    int(price.Quantity),
		BuildUsageLines: func(apiLogID uint) []database.ApiLogUsageLine {
			return []database.ApiLogUsageLine{{
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
			}}
		},
		BuildRequestID:   func(apiLogID uint) string { return videoRequestID(user.ID, startTime, apiLogID) },
		LogPrefix:        "VIDEO",
		InsufficientDesc: fmt.Sprintf("[VIDEO-INSUFFICIENT-BALANCE] %s · %d 秒视频 · 余额不足，已交付服务待对账（按 raw 上游成本计 $%s）", req.Model, price.Quantity, database.FormatMicroUSD(price.AmountMicroUSD)),
		SuccessDesc:      fmt.Sprintf("余额扣费 · %s · %d 秒视频 · %s", req.Model, price.Quantity, FormatChargedCostForDescription(price.AmountMicroUSD, billing.ChargedCostMicroUSD)),
		ReferralDesc:     fmt.Sprintf("视频生成 · %s", req.Model),
		OnTxFailed: func() uint {
			return recordVideoPendingReconcile(user, token, req, price, billing, channelType, statusCode, clientIP, path, startTime, "balance transaction failed")
		},
	})
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
