// Package database / subscription_schema.go
//
// 平台套餐订阅系统数据模型（共享配额池模式）。
//
// 设计原则：所有限额、价格、周期、状态文案均通过表字段或 SysConfig 配置，
// **绝不在代码中写死任何业务参数**。
package database

import (
	"time"

	"gorm.io/gorm"
)

// ============================================================================
// QuotaPlan ─ 配额计划库（最小复用单元）
// ============================================================================
// admin 维护的可复用配额规则，被多个 Package 通过 PackagePlan 关联引用。
type QuotaPlan struct {
	ID          uint   `gorm:"primaryKey" json:"id"`
	Name        string `gorm:"index;not null" json:"name"`
	DisplayName string `gorm:"not null" json:"display_name"`
	Description string `gorm:"type:text" json:"description"`
	// ModelMatch 匹配规则，JSON 数组：["claude-sonnet-*", "claude-haiku-*"]
	ModelMatch string `gorm:"type:text;not null;default:'[]'" json:"model_match"`

	// LimitUnit 计量单位：
	//   api_cost_usd  = 按本次请求真实 API 等值成本扣减（主订阅池）
	//   request_count = 按调用次数扣减（图像/任务类模型可用）
	//   input_tokens | output_tokens | total_tokens | weighted_tokens
	//
	// 未知单位在引擎侧 fail-closed，不再当成 1 次调用兜底。
	LimitUnit  string  `gorm:"index;not null;default:'request_count'" json:"limit_unit"`
	LimitValue float64 `gorm:"not null;default:0" json:"limit_value"`

	WindowSeconds int `gorm:"not null;default:0" json:"window_seconds"` // 0 = 套餐周期内累计

	// WeightFactor 权重系数（按模型）。两种格式：
	//   单值: {"claude-opus-*": 5.0}
	//   分输入输出: {"gpt-5": {"input": 5.0, "output": 15.0}}
	WeightFactor string `gorm:"type:text;default:'{}'" json:"weight_factor"`

	AutoSyncFromChannelModels bool `gorm:"default:false" json:"auto_sync_from_channel_models"`

	Priority int `gorm:"default:100" json:"priority"`

	// OverflowStrategy 超额时行为：block | next_subscription | degrade_model | 自定义
	OverflowStrategy string `gorm:"default:'block'" json:"overflow_strategy"`

	ExtraConfig string `gorm:"type:text;default:'{}'" json:"extra_config"`

	// fix Major（自审第十三轮）：bool + `gorm:"default:true"` 是 GORM 经典陷阱——
	// Go bool 零值（false）被视为"未设置"自动替换成 DB 默认 true。`Select("*")` 也救不了。
	// 用 `*bool` 让 GORM 区分 "未设置"(nil → DB default true) 与 "显式 false"(*=false)。
	// 读取时用 helper `IsEnabled()` 兜底 nil → 视为 true（与 DB default 一致）。
	Enabled   *bool          `gorm:"default:true" json:"enabled"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

// IsEnabled QuotaPlan.Enabled nil-safe getter（nil → 视为 enabled，与 DB default true 一致）
func (q *QuotaPlan) IsEnabled() bool {
	return q.Enabled == nil || *q.Enabled
}

// ============================================================================
// Package ─ 销售套餐
// ============================================================================
type Package struct {
	ID          uint   `gorm:"primaryKey" json:"id"`
	Name        string `gorm:"not null" json:"name"`
	Description string `gorm:"type:text" json:"description"`

	// ProductType 决定消费引擎排序优先级
	//   subscription = 周期套餐（先扣，默认 30 天周期）
	// Phase 8：所有套餐都是 subscription。
	ProductType string `gorm:"index;not null;default:'subscription';size:16" json:"product_type"`

	// 视觉元数据（admin 自由配）
	IconKey      string `json:"icon_key"`
	BadgeColor   string `json:"badge_color"`
	Gradient     string `json:"gradient"`
	HighlightTag string `json:"highlight_tag"`

	// 计费 — 套餐只有一个常规售价 PriceAmount。
	// 任何"优惠"通过独立的优惠券系统（CouponTemplate / UserCoupon）实现：
	// admin 创建券模板 → 给用户发券 → 用户购买时使用券 → 价格在结账层算，
	// 套餐本身的定价模型保持简单，不掺业务规则。
	//
	// fix MAJOR M22-A1 Phase 1：单位 micro_usd（int64），USD * 1e6。
	PriceAmount   int64  `gorm:"not null;default:0" json:"price_amount"`
	PriceCurrency string `gorm:"default:'USD'" json:"price_currency"`

	BillingPeriodSeconds int `gorm:"not null;default:2592000" json:"billing_period_seconds"`

	// 叠加策略
	// fix Major（自审第十三轮）：bool + `gorm:"default:true"` 经典 GORM 陷阱：Go bool 零值（false）
	// 会被自动替换成 DB 默认 true，admin"禁用叠加"被静默忽略。改 `*bool` + nil-safe getter。
	// `Public/SortOrder` 默认 false/0，与 Go 零值一致，无陷阱，保持 bool。
	Stackable         *bool  `gorm:"default:true" json:"stackable"`
	MaxActivePerUser  int    `gorm:"default:5" json:"max_active_per_user"`     // 0 = 无限
	PurchaseWhenOwned string `gorm:"default:'ask'" json:"purchase_when_owned"` // stack | extend | ask

	Public    bool  `gorm:"default:false" json:"public"`
	SortOrder int   `gorm:"default:0" json:"sort_order"`
	Enabled   *bool `gorm:"default:true" json:"enabled"`

	ExtraConfig string `gorm:"type:text;default:'{}'" json:"extra_config"`

	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

// PackageSnapshotCurrentVersion 当前 PackageSnapshot JSON schema 版本号。
// 序列化时写入 `schema_version` 字段；当前快照语义为 QuantityMultiplier 放大限额。
const PackageSnapshotCurrentVersion = 2

// IsStackable Package.Stackable nil-safe getter（nil → 视为 stackable，与 DB default true 一致）
func (p *Package) IsStackable() bool {
	return p.Stackable == nil || *p.Stackable
}

// IsEnabled Package.Enabled nil-safe getter
func (p *Package) IsEnabled() bool {
	return p.Enabled == nil || *p.Enabled
}

// PackagePlan 套餐 ↔ 配额计划 M2M 关联
type PackagePlan struct {
	ID                 uint      `gorm:"primaryKey" json:"id"`
	PackageID          uint      `gorm:"index;not null;uniqueIndex:idx_pkg_plan" json:"package_id"`
	QuotaPlanID        uint      `gorm:"index;not null;uniqueIndex:idx_pkg_plan" json:"quota_plan_id"`
	QuantityMultiplier float64   `gorm:"default:1" json:"quantity_multiplier"`
	SortOrder          int       `gorm:"default:0" json:"sort_order"`
	CreatedAt          time.Time `json:"created_at"`
}

// ============================================================================
// UserSubscription ─ 用户订阅实例
// ============================================================================
type UserSubscription struct {
	ID        uint `gorm:"primaryKey" json:"id"`
	UserID    uint `gorm:"index;not null" json:"user_id"`
	PackageID uint `gorm:"index" json:"package_id"`

	// PackageSnapshot 购买时的完整 Package + 关联 Plans 定义快照（JSON）
	// 已购用户不受后续 admin 改 Package 影响
	PackageSnapshot string `gorm:"type:text;not null" json:"package_snapshot"`

	StartAt    time.Time  `gorm:"index" json:"start_at"`
	EndAt      time.Time  `gorm:"index" json:"end_at"`
	CanceledAt *time.Time `json:"canceled_at"`

	// FIFO 排序键（unix_micro of created_at）
	ConsumptionOrder int64 `gorm:"index" json:"consumption_order"`

	// 同套餐第几份（叠加显示用）
	StackIndex int `gorm:"default:1" json:"stack_index"`

	ParentSubscriptionID *uint `json:"parent_subscription_id"`

	Status string `gorm:"index;default:'active'" json:"status"` // active | expired | canceled | refunded | paused | revoked

	AutoRenew bool `gorm:"default:false" json:"auto_renew"`

	// IsGranted 表示该订阅来自管理员赠送（AdminGrantSubscription），非用户付费购买。
	// 退款时这类订阅 netCost 强制为 0 → 永远不能退款（用户没出钱，退款 = 平台给用户白送钱）。
	// 购买路径保持 false；赠送路径必须显式置 true。
	IsGranted bool `gorm:"default:false;index" json:"is_granted"`

	// GrantReason 赠送理由（admin 必填，便于审计 / 用户客服查询）。
	// 仅 IsGranted=true 时有意义；空字符串表示购买路径写入。
	GrantReason string `gorm:"type:text" json:"grant_reason,omitempty"`

	// PurchasedUnitPriceUSD 购买时实际成交价（含券折扣后），单位 micro_usd。
	// fix CRITICAL R23+2-C1（codex 全方面审查）：
	// 之前退款只读 PackageSnapshot.price_amount → 用户用券价 $10 买能退原价 $20 → 套利。
	// 现在持久化实际成交价，退款只读这个字段，不再有歧义。
	// IsGranted=true 的赠送 sub 此值为 0（退款路径已强制 netCost=0）。
	PurchasedUnitPriceUSD int64 `gorm:"default:0" json:"purchased_unit_price_usd"`

	// AppliedCouponID 购买时使用的券 ID（0 = 没用券）。
	//
	// 业务规则（用户 2026-05-10 第三次反馈定稿）：取消/退款都**不**触碰券。
	// 该字段仅用于：
	//   - admin 在 AdminListSubscriptions 看"这份订阅当时用过哪张券"作为补偿决策辅助
	//   - 审计回溯：账单写明"用券折后价 = $X"
	// admin 想给用户补偿券应**独立**走 AdminGrantCoupon 入口，不与退款流程耦合。
	AppliedCouponID uint `gorm:"index;default:0" json:"applied_coupon_id"`

	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

// ============================================================================
// SubscriptionUsage ─ 每窗口用量计数（最热表）
// ============================================================================
type SubscriptionUsage struct {
	ID             uint   `gorm:"primaryKey" json:"id"`
	SubscriptionID uint   `gorm:"index;not null;uniqueIndex:idx_sub_plan_bucket" json:"subscription_id"`
	QuotaPlanID    uint   `gorm:"index;not null;uniqueIndex:idx_sub_plan_bucket" json:"quota_plan_id"`
	ModelBucket    string `gorm:"index;not null;uniqueIndex:idx_sub_plan_bucket" json:"model_bucket"`

	WindowStartAt time.Time `gorm:"index" json:"window_start_at"`
	WindowEndAt   time.Time `gorm:"index" json:"window_end_at"`

	ConsumedValue float64 `gorm:"default:0" json:"consumed_value"`
	RequestCount  int64   `gorm:"default:0" json:"request_count"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ============================================================================
// Notification ─ 站内通知
// ============================================================================
type Notification struct {
	ID     uint `gorm:"primaryKey" json:"id"`
	UserID uint `gorm:"index;not null" json:"user_id"`

	// Category 由 admin 自由扩展：subscription | system | promo | ...
	Category string `gorm:"index" json:"category"`
	Severity string `gorm:"default:'info'" json:"severity"` // info | success | warning | error

	Title string `gorm:"not null" json:"title"`
	Body  string `gorm:"type:text" json:"body"`

	ActionURL  string `json:"action_url"`
	ActionText string `json:"action_text"`

	RelatedType string `gorm:"index" json:"related_type"`
	RelatedID   uint   `gorm:"index" json:"related_id"`

	// DedupKey 用于跨进程去重（如 "expire_warn:sub_42:2026-04-30"）。
	// 用 *string + uniqueIndex：NULL 在唯一索引中互不冲突（SQLite/PostgreSQL 行为），
	// 仅显式 dedup 通知（cron 预警）才设值，普通通知留 NULL 互不影响。
	DedupKey *string `gorm:"uniqueIndex;default:null" json:"dedup_key,omitempty"`

	ReadAt *time.Time `json:"read_at"`
	// RevokedAt admin 撤回群发时设置，查询时过滤掉这些行（用户铃铛不再展示）
	RevokedAt *time.Time `gorm:"index" json:"revoked_at,omitempty"`
	CreatedAt time.Time  `gorm:"index" json:"created_at"`
}
