package database

import "time"

// UpstreamAccountCost 维护 CPA usage auth_index 到真实账号成本的映射。
//
// 金额字段仍使用 micro_usd：MonthlyCostUSD 表示该账号官方订阅/月费成本，
// EstimatedMonthlyCapacityUSD 表示这个账号每月预计能承载的 API 等值 raw_cost。
// 毛利报表按 raw_cost × MonthlyCostUSD / EstimatedMonthlyCapacityUSD 分摊平台成本。
type UpstreamAccountCost struct {
	ID                          uint      `gorm:"primaryKey" json:"id"`
	Provider                    string    `gorm:"index;size:64;not null;uniqueIndex:idx_upstream_account_provider_auth" json:"provider"`
	AuthIndex                   string    `gorm:"index;size:64;not null;uniqueIndex:idx_upstream_account_provider_auth" json:"auth_index"`
	AuthType                    string    `gorm:"size:64;default:''" json:"auth_type"`
	Label                       string    `gorm:"size:160;default:''" json:"label"`
	PlanName                    string    `gorm:"size:160;default:''" json:"plan_name"`
	MonthlyCostUSD              int64     `gorm:"default:0" json:"monthly_cost_usd"`
	EstimatedMonthlyCapacityUSD int64     `gorm:"default:0" json:"estimated_monthly_capacity_usd"`
	Active                      bool      `gorm:"index;default:true" json:"active"`
	Notes                       string    `gorm:"type:text" json:"notes"`
	CreatedAt                   time.Time `json:"created_at"`
	UpdatedAt                   time.Time `json:"updated_at"`
}
