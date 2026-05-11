// Package database / billing_schema.go
//
// 统一账单事实表（single source of truth）。所有"金钱/资源进出"事件都进此表，
// 充值、购买、退款、API 扣费等不再分散在多个表里需要 admin 拼凑。
//
// 设计原则：
//  1. 一行一笔事件，不可变（append-only）。任何修正都通过新增"反向条目"实现，
//     不更新历史行——账务可追溯。
//  2. AmountUSD 正负号语义：> 0 进账（quota+），< 0 出账（quota-）；用户视角"看一眼就懂"。
//  3. BalanceAfterUSD 是事件后的 user.quota 快照，便于离线对账（重算不依赖时序遍历）。
//  4. RelatedType + RelatedID 反向关联到来源记录（topup_orders / user_subscriptions / api_logs），
//     admin 点击账单行可以跳转到原始事件详情。
//  5. SourceSubscriptionID 仅 api_usage_sub / api_usage_addon 类型有意义，标识"扣自哪个 sub 实例"。
//
// 写入时机（共 7 处插桩，均在事务内）：
//
//	YifutNotify (paid)              → topup
//	purchaseAsInstant (success)     → purchase_sub | purchase_addon (+ bonus_credit if bonus>0)
//	AdminGrantSubscription          → admin_grant_sub | admin_grant_addon (AmountUSD=0; +bonus_credit if apply_bonus)
//	AdminRefundSubscription         → refund_sub
//	AdminRefundTopup                → refund_topup (+ admin_adjust if reclaim_quota=false 仍写一行 0 USD 解释)
//	stream.go deductQuotaAtomic     → api_consume_balance
//	stream.go Decide 命中 sub/addon → api_usage_sub | api_usage_addon (AmountUSD=0)
package database

import "time"

// BillingEntry 账单流水。按 occurred_at 倒序展示给用户。
//
// fix Major（codex+claude 第十四轮）：append-only 契约用 `gorm:"<-:create"` 写保护标签强制：
// GORM 在 Update / Save 时会跳过这些字段，任何后续修改尝试都不会改动列。
// 这把"账单不可变"从注释承诺升级为代码保证。**唯一可修改路径是 raw SQL**（admin DB 操作风险）。
type BillingEntry struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	UserID     uint      `gorm:"index;not null;<-:create" json:"user_id"`
	OccurredAt time.Time `gorm:"index;not null;<-:create" json:"occurred_at"`

	// EntryType 见上方文件头注释。索引用于按类型筛选。
	EntryType string `gorm:"index;not null;size:32;<-:create" json:"entry_type"`

	// AmountUSD 影响 user.quota 的净变动；仅 api_usage_sub/addon 类型为 0。
	// 单位：micro_usd（USD * 1e6），int64 全程整数算术杜绝累加误差。
	AmountUSD int64 `gorm:"not null;<-:create" json:"amount_usd"`
	// BalanceAfterUSD 事件后用户余额快照（micro_usd）。回填脚本可能填 0 标记"未知"。
	BalanceAfterUSD int64 `gorm:"<-:create" json:"balance_after_usd"`

	// 来源关联
	RelatedType string `gorm:"index;size:32;<-:create" json:"related_type"`
	RelatedID   uint   `gorm:"index;<-:create" json:"related_id"`

	// API 调用专属字段（其他类型留空）
	ModelName   string `gorm:"size:64;<-:create" json:"model_name,omitempty"`
	TokensTotal int    `gorm:"<-:create" json:"tokens_total,omitempty"` // prompt + completion（cached/reasoning 是这两者的子集，不重复加）

	// SourceSubscriptionID 语义因 EntryType 不同（fix m4，codex 第十四轮印证）：
	//   api_usage_sub / api_usage_addon → 此次 API 调用的额度来自哪个订阅实例（"quota source"）
	//   refund_sub                       → 被退款的订阅实例（"refunded subject"）
	// 查询时若不区分 EntryType 而仅按 SourceSubscriptionID 过滤会混淆这两种语义。
	SourceSubscriptionID *uint `gorm:"<-:create" json:"source_subscription_id,omitempty"`

	// 用户友好描述，前端列表直接展示。
	Description string `gorm:"type:text;<-:create" json:"description"`

	// 原币种审计（充值的 RMB / 退款的 RMB / 订阅的 USD 等）
	// CurrencyOriginal=USD 时单位是 micro_usd；CurrencyOriginal=RMB 时单位是 fen（分, RMB * 100）。
	CurrencyOriginal string `gorm:"size:8;<-:create" json:"currency_original,omitempty"`
	AmountOriginal   int64  `gorm:"<-:create" json:"amount_original,omitempty"`

	CreatedAt time.Time `json:"created_at"`
}

// EntryType 常量集。新增类型时在此处加常量并更新 IsConsumeEntry / IsCreditEntry 的判定。
const (
	BillingTypeTopup             = "topup"               // 充值入账
	BillingTypePurchaseSub       = "purchase_sub"        // 购买周期套餐
	BillingTypePurchaseAddon     = "purchase_addon"      // 购买增量包
	BillingTypeBonusCredit       = "bonus_credit"        // 套餐附赠 USD
	BillingTypeRefundSub         = "refund_sub"          // 订阅退款
	BillingTypeRefundTopup       = "refund_topup"        // 充值退款（reclaim_quota=true 时 AmountUSD<0）
	BillingTypeAdminAdjust       = "admin_adjust"        // 管理员手动调整
	BillingTypeAdminGrantSub     = "admin_grant_sub"     // 管理员赠送订阅（AmountUSD=0，不动钱）
	BillingTypeAdminGrantAddon   = "admin_grant_addon"   // 管理员赠送增量包（AmountUSD=0，不动钱）
	BillingTypeApiConsumeBalance = "api_consume_balance" // API 扣余额（quota-）
	BillingTypeApiUsageSub       = "api_usage_sub"       // API 扣订阅额度（不动 quota）
	BillingTypeApiUsageAddon     = "api_usage_addon"     // API 扣增量包额度（不动 quota）
	// fix MAJOR R23+3-B5（codex 第四轮）：commit 阶段订阅 DB 加载失败时的"待对账"标记。
	// 与 ApiUsageSub 区分：admin 看到这个类型知道"上游已服务但订阅状态当时不可读"，
	// 需要人工介入对账（修复订阅状态后补扣 / 免扣）。
	BillingTypeApiUsagePendingReconcile = "api_usage_pending_reconcile"
)

// IsCreditEntry 是否为入账类型（用于汇总；AmountUSD 单位 micro_usd）
func (b *BillingEntry) IsCreditEntry() bool {
	return b.AmountUSD > 0
}

// IsConsumeEntry 是否为消费类型（仅 API 扣费 + 购买；不含退款回收）
func (b *BillingEntry) IsConsumeEntry() bool {
	switch b.EntryType {
	case BillingTypePurchaseSub, BillingTypePurchaseAddon, BillingTypeApiConsumeBalance:
		return true
	}
	return false
}

// IsKnownBillingType EntryType 是否在合法常量集合内。
// fix Minor m2（codex 第十四轮）：写路径需要 invariant 检查，避免 typo 落库后
// 在汇总查询里"消失"（IsCreditEntry/IsConsumeEntry 默认 false）。
//
// fix CRITICAL（codex 第十五轮）：BillingTypeApiUsagePendingReconcile 必须列入白名单，
// 否则 stream.go [DB-RETRY] 路径写"待对账"账单会被 WriteBillingEntry 拒绝，
// 形成 fail-closed 假象（实际是 silent drop）。
func IsKnownBillingType(t string) bool {
	switch t {
	case BillingTypeTopup, BillingTypePurchaseSub, BillingTypePurchaseAddon,
		BillingTypeBonusCredit, BillingTypeRefundSub, BillingTypeRefundTopup,
		BillingTypeAdminAdjust, BillingTypeAdminGrantSub, BillingTypeAdminGrantAddon,
		BillingTypeApiConsumeBalance,
		BillingTypeApiUsageSub, BillingTypeApiUsageAddon,
		BillingTypeApiUsagePendingReconcile:
		return true
	}
	return false
}

// IsApiUsageType 是否为"仅审计 token 数、不动 quota"的 API 用量类型。
// 这两种类型必须 AmountUSD == 0；非零即破坏汇总（错误计入 totalIn/totalOut）。
func IsApiUsageType(t string) bool {
	return t == BillingTypeApiUsageSub || t == BillingTypeApiUsageAddon
}

// IsZeroAmountBillingType 是否为 AmountUSD 必须为 0 的类型（仅审计 / 占位 / 待对账）。
//
// fix Minor（codex 第十六轮）：把"零金额 invariant"独立成函数，让写路径能统一校验。
// 包含：
//   - api_usage_sub / api_usage_addon：扣订阅额度（不动 user.quota）
//   - api_usage_pending_reconcile：commit 阶段订阅 DB 加载失败时的待对账标记，AmountUSD 必须 0
//   - admin_grant_sub / admin_grant_addon：管理员赠送订阅，AmountUSD 必须 0
func IsZeroAmountBillingType(t string) bool {
	switch t {
	case BillingTypeApiUsageSub, BillingTypeApiUsageAddon,
		BillingTypeApiUsagePendingReconcile,
		BillingTypeAdminGrantSub, BillingTypeAdminGrantAddon:
		return true
	}
	return false
}
