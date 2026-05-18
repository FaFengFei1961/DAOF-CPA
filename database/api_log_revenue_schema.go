package database

import (
	"time"

	"gorm.io/gorm"
)

// Revenue source enums for ApiLogRevenue.RevenueSource.
// 与 docs/coding-conventions.md §1 一致：审计型 side table，INSERT-only。
const (
	RevenueSourceSubscription = "subscription" // 命中订阅，effective = chargedCost
	RevenueSourceBalance      = "balance"      // 走余额扣费，effective = rawCost
)

// ApiLogRevenue 把"这次请求实际从用户拿到多少钱"事实化记录在 api_logs 之外。
//
// 为什么不写进 api_logs.ChargedCost：
//   - api_logs 是 INSERT-only，写入时还不知道走订阅还是余额（决策在 ApiLog Create 之后）
//   - 顺 ApiLogAttribution / ApiLogCostEstimate 范式：一个 ApiLog 最多一条 revenue 行
//
// 报表口径：
//   - revenue_source = subscription → effective_revenue_micro_usd = ApiLog.ChargedCost
//   - revenue_source = balance      → effective_revenue_micro_usd = ApiLog.Cost (= rawCost)
//
// 余额不足挂 pending_reconcile 的请求**不写**这张表（没真实收到钱）。
type ApiLogRevenue struct {
	ID                       uint      `gorm:"primaryKey;<-:create" json:"id"`
	ApiLogID                 uint      `gorm:"uniqueIndex;not null;<-:create" json:"api_log_id"`
	RevenueSource            string    `gorm:"index;size:32;not null;<-:create" json:"revenue_source"`
	EffectiveRevenueMicroUSD int64     `gorm:"<-:create" json:"effective_revenue_micro_usd"`
	SubscriptionID           uint      `gorm:"index;default:0;<-:create" json:"subscription_id"`
	RecordedAt               time.Time `gorm:"index;<-:create" json:"recorded_at"`
	CreatedAt                time.Time `gorm:"<-:create" json:"created_at"`
}

func (r *ApiLogRevenue) BeforeUpdate(tx *gorm.DB) error {
	return ErrApiLogAppendOnly
}

func (r *ApiLogRevenue) BeforeDelete(tx *gorm.DB) error {
	return ErrApiLogAppendOnly
}
