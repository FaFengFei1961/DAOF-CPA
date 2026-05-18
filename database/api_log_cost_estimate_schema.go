package database

import (
	"time"

	"gorm.io/gorm"
)

// ApiLogCostEstimate stores platform cost estimates outside api_logs.
//
// 契约（codex audit-integrity 第 26 轮明确）：
//   - GORM 层 INSERT-only：BeforeUpdate/BeforeDelete hook 拒绝 Update/Save/Delete
//   - **idempotent backfill 例外**：raw SQL `INSERT ... ON CONFLICT(api_log_id) DO UPDATE`
//     **允许覆盖** platform_cost_micro_usd / computed_at（见 controller/upstream_cost.go:733
//     的 refreshPlatformCostEstimateForAccount）。这是因为上游月费摊销重算时，已有 estimate
//     需要按新的 monthly_cost / capacity 重新分摊。
//   - 一个 api_log_id 最多对应一条 estimate 行（唯一索引保证）
//
// 与 ApiLogAttribution / ApiLogRevenue 的契约**有意不同**：后两者是事实记录（一次写入永不变），
// 此表是"摊销估算"（参数变了要重算）。这种区分是 product decision，不是 bug。
type ApiLogCostEstimate struct {
	ID                   uint      `gorm:"primaryKey;<-:create" json:"id"`
	ApiLogID             uint      `gorm:"uniqueIndex;not null;<-:create" json:"api_log_id"`
	PlatformCostMicroUSD int64     `gorm:"<-:create" json:"platform_cost_micro_usd"`
	ComputedAt           time.Time `gorm:"index;<-:create" json:"computed_at"`
	Method               string    `gorm:"size:64;<-:create" json:"method"`
	CreatedAt            time.Time `gorm:"<-:create" json:"created_at"`
}

func (e *ApiLogCostEstimate) BeforeUpdate(tx *gorm.DB) error {
	return ErrApiLogAppendOnly
}

func (e *ApiLogCostEstimate) BeforeDelete(tx *gorm.DB) error {
	return ErrApiLogAppendOnly
}
