package proxy

// Package proxy / media_billing.go
//
// fix A-P0-3 (2026-05-19)：image_billing.go / video_billing.go / gemini_billing.go
// 三套 deductXxxBalanceAndLog 80% 重复，约 750 行同构代码。统一抽到本文件的
// DeductMediaBalanceAndLog，三路 caller 改为薄包装传入 MediaBillingInput。
//
// 业务行为不变：保留 H2 / R5 / SF-H4 / SF-H6 等所有已有 fix。

import (
	"fmt"
	"log"
	"strings"
	"time"

	"daof-cpa/database"

	"gorm.io/gorm"
)

// MediaBillingInput 是图像/视频/Gemini 媒体生成 commit 阶段的通用入参。
// caller 把请求侧差异（请求 model 名 / token 计数 / usage line 构造 / 文案
// 模板）填入字段；通用流程负责事务、CAS quota、ApiLog、pending_reconcile
// 兜底、推荐奖励、RefreshUserAuth、RecordApiLogRevenue。
type MediaBillingInput struct {
	// 身份与上下文
	User      *database.User
	Token     string
	ModelName string
	ClientIP  string
	Path      string
	StartTime time.Time

	// 计费
	AmountMicroUSD int64
	Billing        BillingRuleResolution
	ChannelType    string
	StatusCode     int

	// ApiLog 多余字段（image 路径填 token 计数；video/gemini 留 0）
	PromptTokens       int
	CompletionTokens   int
	CachedTokens       int
	CacheWriteTokens   int
	CacheWrite5mTokens int
	CacheWrite1hTokens int
	ReasoningTokens    int

	// pending entry tokens_total（image=imageTokenTotal(price), video=int(price.Quantity), gemini=prompt+completion）
	TokensTotal int

	// 子流程定制
	BuildUsageLines func(apiLogID uint) []database.ApiLogUsageLine
	BuildRequestID  func(apiLogID uint) string

	// 文案
	LogPrefix        string // 日志前缀，如 "IMAGE" / "VIDEO" / "GEMINI"
	InsufficientDesc string // 余额不足时 BillingEntry.Description
	SuccessDesc      string // 余额扣费成功时 BillingEntry.Description
	ReferralDesc     string // 推荐奖励 BillingEntry.Description

	// 事务失败兜底：caller 用 record*PendingReconcile 写一条 pending 行
	OnTxFailed func() uint
}

// DeductMediaBalanceAndLog 通用媒体生成扣费流程，覆盖：
//   - TryConsumeBalanceTx 先于 CAS quota（fix H2，与 text path 一致）
//   - CAS UPDATE quota - cost，RowsAffected==0 → balanceInsufficient
//   - 构造 ApiLog（balanceInsufficient 时 Cost=0 + ChargedCost=0，防污染报表 fix R5）
//   - INSERT ApiLog + usage lines（由 caller 通过 BuildUsageLines 提供）
//   - balanceInsufficient → 写 pending_reconcile BillingEntry，return
//   - 正常 → 写 api_consume_balance BillingEntry + ApplyReferralPaidSpendRewardTx
//   - 事务外 RefreshUserAuth + RecordApiLogRevenue（fix SF-H6 异步）
//
// 返回 (apiLogID, effectiveRevenue, referralReward)。tx 失败走 OnTxFailed 兜底。
func DeductMediaBalanceAndLog(in MediaBillingInput) (uint, int64, database.ReferralPaidSpendRewardResult) {
	var apiLogID uint
	balanceConsumed := false
	var referralReward database.ReferralPaidSpendRewardResult
	referralRewardBPS, referralRewardWindowSeconds := readReferralPaidSpendRewardConfig()

	txErr := database.DB.Transaction(func(tx *gorm.DB) error {
		// fix H2：window tracking 先于 CAS quota，与 text path 对齐
		if !TryConsumeBalanceTx(tx, in.User.ID, in.AmountMicroUSD, true) {
			log.Printf("[%s-BILLING-WINDOW-TRACK-FAIL] user=%d model=%s raw_cost_micro=%d",
				in.LogPrefix, in.User.ID, in.ModelName, in.AmountMicroUSD)
		}
		res := tx.Model(&database.User{}).
			Where("id = ? AND quota >= ?", in.User.ID, in.AmountMicroUSD).
			UpdateColumn("quota", gorm.Expr("quota - ?", in.AmountMicroUSD))
		if res.Error != nil {
			return fmt.Errorf("quota deduct: %w", res.Error)
		}
		balanceInsufficient := res.RowsAffected == 0

		// fix R5：余额不足 → pending_reconcile，ApiLog.Cost / ChargedCost 设 0
		apiLogCost := in.AmountMicroUSD
		apiLogChargedCost := in.Billing.ChargedCostMicroUSD
		if balanceInsufficient {
			apiLogCost = 0
			apiLogChargedCost = 0
		}
		apiLog := database.ApiLog{
			UserID:              in.User.ID,
			TokenName:           HashTokenForLog(in.Token),
			ModelName:           in.ModelName,
			RequestedModel:      in.Billing.RequestedModel,
			ServedModel:         in.Billing.ServedModel,
			PromptTokens:        in.PromptTokens,
			CompletionTokens:    in.CompletionTokens,
			CachedTokens:        in.CachedTokens,
			CacheWriteTokens:    in.CacheWriteTokens,
			CacheWrite5mTokens:  in.CacheWrite5mTokens,
			CacheWrite1hTokens:  in.CacheWrite1hTokens,
			ReasoningTokens:     in.ReasoningTokens,
			Cost:                apiLogCost,
			ChargedCost:         apiLogChargedCost,
			ModelWeight:         in.Billing.ModelWeight,
			HealthMultiplier:    in.Billing.HealthMultiplier,
			BillingRulesVersion: in.Billing.BillingRulesVersion,
			FallbackUserOptIn:   in.Billing.FallbackUserOptIn,
			FallbackReason:      sanitizeError(in.Billing.FallbackReason, 160),
			UpstreamProvider:    sanitizeError(strings.ToLower(strings.TrimSpace(in.ChannelType)), 64),
			Latency:             time.Since(in.StartTime).Milliseconds(),
			Status:              in.StatusCode,
			IPAddress:           in.ClientIP,
			RequestPath:         sanitizeError(in.Path, 160),
			CreatedAt:           time.Now(),
		}
		if err := tx.Create(&apiLog).Error; err != nil {
			return fmt.Errorf("create api log: %w", err)
		}
		apiLogID = apiLog.ID

		// usage lines 由 caller 提供（image/video/gemini 各自结构不同）
		if in.BuildUsageLines != nil {
			lines := in.BuildUsageLines(apiLogID)
			if len(lines) > 0 {
				if err := tx.Create(&lines).Error; err != nil {
					return fmt.Errorf("create usage line: %w", err)
				}
			}
		}

		if balanceInsufficient {
			var current database.User
			if err := tx.Select("id, quota").First(&current, in.User.ID).Error; err != nil {
				return fmt.Errorf("user row missing: %w", err)
			}
			requestID := ""
			if in.BuildRequestID != nil {
				requestID = in.BuildRequestID(apiLogID)
			}
			return database.WriteBillingEntry(tx, database.BillingEntryInput{
				UserID:           in.User.ID,
				EntryType:        database.BillingTypeApiUsagePendingReconcile,
				BillingState:     database.BillingStatePendingReconcile,
				AmountUSD:        0,
				BalanceAfterUSD:  current.Quota,
				ModelName:        in.ModelName,
				TokensTotal:      in.TokensTotal,
				RequestID:        requestID,
				EstimatedCostUSD: in.AmountMicroUSD,
				RelatedType:      "api_log",
				RelatedID:        apiLogID,
				Description:      in.InsufficientDesc,
			})
		}

		var fresh database.User
		if err := tx.Select("id, quota").First(&fresh, in.User.ID).Error; err != nil {
			return fmt.Errorf("re-select quota: %w", err)
		}
		if err := database.WriteBillingEntry(tx, database.BillingEntryInput{
			UserID:          in.User.ID,
			EntryType:       database.BillingTypeApiConsumeBalance,
			AmountUSD:       -in.AmountMicroUSD,
			BalanceAfterUSD: fresh.Quota,
			ModelName:       in.ModelName,
			TokensTotal:     in.TokensTotal,
			RelatedType:     "api_log",
			RelatedID:       apiLogID,
			Description:     in.SuccessDesc,
		}); err != nil {
			return fmt.Errorf("write billing: %w", err)
		}
		reward, err := database.ApplyReferralPaidSpendRewardTx(
			tx,
			in.User.ID,
			in.AmountMicroUSD,
			referralRewardBPS,
			referralRewardWindowSeconds,
			time.Now(),
			"api_log",
			apiLogID,
			in.ReferralDesc,
		)
		if err != nil {
			return fmt.Errorf("apply referral spend reward: %w", err)
		}
		referralReward = reward
		balanceConsumed = true
		return nil
	})

	if txErr != nil {
		log.Printf("[%s-BILLING-CRITICAL] user=%d model=%s balance tx failed: %v",
			in.LogPrefix, in.User.ID, in.ModelName, txErr)
		if in.OnTxFailed != nil {
			apiLogID = in.OnTxFailed()
		}
		return apiLogID, 0, database.ReferralPaidSpendRewardResult{}
	}

	RefreshUserAuth(in.User.ID)
	effectiveRevenue := int64(0)
	if balanceConsumed {
		effectiveRevenue = in.AmountMicroUSD
		if apiLogID != 0 {
			RecordApiLogRevenue(apiLogID, database.RevenueSourceBalance, in.AmountMicroUSD, 0)
		}
	}
	return apiLogID, effectiveRevenue, referralReward
}
