package database

import (
	"time"

	"gorm.io/gorm"
)

// ApiLogCostEstimate stores platform cost estimates outside api_logs.
// The table is append-only: a log can receive at most one estimate row.
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
