// Package database / billing_helper.go
//
// 账单写入辅助函数。设计要点：
//  1. 必须在调用方持有的事务（tx *gorm.DB）内调用——不开新事务，与业务原子提交。
//  2. 账单写入失败时，调用方决定是否中止（账单是审计层，部分场景宁可丢账单也要让业务过——
//     例如 stream.go 扣费成功后的账单写入失败不应让请求 502 给用户。具体策略由调用方决定，
//     此 helper 只负责"忠实写入或返回错误"。）
//  3. 不读取 user.quota——调用方传入 BalanceAfterUSD（避免重复 SELECT）。
package database

import (
	"fmt"
	"time"

	"gorm.io/gorm"
)

// BillingEntryInput 业务侧调用 helper 时填的参数包。
//
// 必填：UserID, EntryType, AmountUSD, BalanceAfterUSD, Description
// 可选：其他来源/审计字段
//
// 单位约定（fix MAJOR M22-A1 Phase 1）：
//   - AmountUSD / BalanceAfterUSD：int64 micro_usd（USD * 1e6）
//   - AmountOriginal：CurrencyOriginal=USD → micro_usd；CurrencyOriginal=RMB → fen（分）
type BillingEntryInput struct {
	UserID          uint
	EntryType       string // 见 billing_schema.go 的 BillingType* 常量
	BillingState    string // 默认 settled；待人工处理时传 pending_reconcile / upstream_unmetered
	AmountUSD       int64  // 入账正、出账负；api_usage_sub 传 0（单位 micro_usd）
	BalanceAfterUSD int64  // 调用方算好（quota+= 后的值，单位 micro_usd）
	Description     string

	// 来源关联（可选）
	RelatedType string
	RelatedID   uint

	// API 调用专属（可选）
	ModelName            string
	TokensTotal          int
	RequestID            string
	DeliveredBytes       int64
	EstimatedInputTokens int
	EstimatedCostUSD     int64
	SourceSubscriptionID *uint

	// 原币审计（可选）
	CurrencyOriginal string
	AmountOriginal   int64

	// OccurredAt 默认 time.Now()。允许调用方覆盖以反映"事件实际时刻"
	// （例如 YifutNotify 用 paid_at 而不是写入时刻，保留语义准确性）。
	OccurredAt time.Time
}

// WriteBillingEntry 在给定事务内插入一条账单流水。
// 返回错误时调用方应回滚事务。
func WriteBillingEntry(tx *gorm.DB, in BillingEntryInput) error {
	if in.UserID == 0 {
		return fmt.Errorf("billing: UserID required")
	}
	if in.EntryType == "" {
		return fmt.Errorf("billing: EntryType required")
	}
	// fix Minor m2（codex 第十四轮）：写路径白名单校验，typo 不能落库
	if !IsKnownBillingType(in.EntryType) {
		return fmt.Errorf("billing: unknown EntryType=%q (typo would corrupt summaries)", in.EntryType)
	}
	billingState := in.BillingState
	if billingState == "" {
		billingState = BillingStateSettled
	}
	if !IsKnownBillingState(billingState) {
		return fmt.Errorf("billing: unknown BillingState=%q", billingState)
	}
	// fix Minor m3：零金额类型 invariant — AmountUSD 必须为 0
	if IsZeroAmountBillingType(in.EntryType) && in.AmountUSD != 0 {
		return fmt.Errorf("billing: %s entries require AmountUSD=0, got %v", in.EntryType, in.AmountUSD)
	}
	occurredAt := in.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = time.Now()
	}
	entry := BillingEntry{
		UserID:               in.UserID,
		OccurredAt:           occurredAt,
		EntryType:            in.EntryType,
		BillingState:         billingState,
		AmountUSD:            in.AmountUSD,
		BalanceAfterUSD:      in.BalanceAfterUSD,
		Description:          in.Description,
		RelatedType:          in.RelatedType,
		RelatedID:            in.RelatedID,
		ModelName:            in.ModelName,
		TokensTotal:          in.TokensTotal,
		RequestID:            in.RequestID,
		DeliveredBytes:       in.DeliveredBytes,
		EstimatedInputTokens: in.EstimatedInputTokens,
		EstimatedCostUSD:     in.EstimatedCostUSD,
		SourceSubscriptionID: in.SourceSubscriptionID,
		CurrencyOriginal:     in.CurrencyOriginal,
		AmountOriginal:       in.AmountOriginal,
	}
	if err := tx.Create(&entry).Error; err != nil {
		return fmt.Errorf("billing: insert entry (type=%s user=%d): %w", in.EntryType, in.UserID, err)
	}
	return nil
}

// WriteBillingEntryNonFatal 非事务版本（独立连接），失败仅返回错误供调用方处理。
//
// **使用约束**（fix Phase 4-codex 第二十四轮 Suggestion）：
//   - 只用于"业务侧已最终成功，账单失败也不能影响主流程"的纯审计路径
//     （如 stream.go 命中订阅时的 api_usage_sub 仅审计 token 数）
//   - **不能用于财务路径的待对账记录**：调用方必须自己包重试 +
//     `[BILLING-LOST-DEBT]` 警报日志，让 admin 能从 ApiLog 手工补账
//
// 当前正确用法见 proxy/stream.go：
//   - api_usage_sub: 直接 NonFatal（仅审计）
//   - api_usage_pending_reconcile (DB-RETRY / UNAUTHORIZED-FALLBACK): 重试 3 次 + 失败 LOST-DEBT 警报
func WriteBillingEntryNonFatal(in BillingEntryInput) error {
	return WriteBillingEntry(DB, in)
}
