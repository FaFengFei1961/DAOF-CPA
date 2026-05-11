// Package database / coupon_schema.go
//
// 优惠券系统：把"折扣"从 Package 上的隐式状态字段（FirstPurchasePrice）
// 提升为独立可流转实体，由 admin 显式控制发放/回收。
//
// 设计动机（用户 2026-05-10 反馈）：
//   - 取消订阅 ≠ 退款 → 取消不退权益、退款（admin 手动同意）才退权益
//   - "首单价"系统隐式追踪太复杂 → 改为显式优惠券 + admin 手动发
//   - 优惠券是独立实体 → admin 看得见、可撤销、可补发；财务对账清晰
//
// MVP 范围（用户拍板）：
//   - 仅支持 fixed_price 类型（直接定价 $10），不做 percent（避免精度问题）
//   - 适用范围：特定 package_ids 或全部
//   - 入口：admin 创建模板 → admin 给用户发 / 注册时按 SysConfig 自动发
//   - 不做：用户输 code 自助领、券与券叠加、复杂活动
//
// 流转（用户 2026-05-10 第三次反馈定稿）：
//   admin 创建模板 → admin 发券（创建 UserCoupon, status=available）
//   → 用户购买时选用 → status=used + 关联 sub_id
//   → 用户取消订阅 / admin 退款 → 券保持 used（取消≠退款都**不退权益**）
//   → admin 撤销 → status=revoked（仅对 status=available 的券有效）
//   → admin 视情况手动发新券作补偿 → 走 AdminGrantCoupon 独立路径
package database

import (
	"time"

	"gorm.io/gorm"
)

// CouponTemplate admin 创建的"券蓝本"。
// 每个 UserCoupon 引用一个 template，但发券时**快照** template 的关键字段进 UserCoupon，
// 确保 admin 修改 template 不影响已发出的券（与 PackageSnapshot 同一思路）。
type CouponTemplate struct {
	ID          uint   `gorm:"primaryKey" json:"id"`
	Name        string `gorm:"not null" json:"name"`        // 内部名（admin 看）
	Description string `gorm:"type:text" json:"description"` // 用户端文案

	// 优惠类型（MVP 仅支持 fixed_price）
	//   "fixed_price" - 用 DiscountValue 替换原价。如 DiscountValue=10_000_000(=$10) → 原价 $20 用券后 $10
	// fix MAJOR M22-A1 Phase 1：单位 micro_usd（int64）。
	DiscountType  string `gorm:"size:16;default:'fixed_price'" json:"discount_type"`
	DiscountValue int64  `gorm:"default:0" json:"discount_value"` // fixed_price 时为新价；0 = 免费券

	// 适用范围：JSON 数组 "[1,2,3]" / "" = 全部 package
	PackageIDs string `gorm:"type:text;default:''" json:"package_ids"`

	// 有效期（发放后 N 天有效；0 = 永久）
	ValidDays int `gorm:"default:0" json:"valid_days"`

	Enabled   *bool          `gorm:"default:true" json:"enabled"` // *bool 防 GORM 零值陷阱
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

// IsEnabled nil-safe getter
func (t *CouponTemplate) IsEnabled() bool {
	return t.Enabled == nil || *t.Enabled
}

// UserCoupon 用户持有的券实例。每张券有唯一 Code（CP-{userID}-{rand}）。
type UserCoupon struct {
	ID         uint   `gorm:"primaryKey" json:"id"`
	UserID     uint   `gorm:"index;not null" json:"user_id"`
	TemplateID uint   `gorm:"index;not null" json:"template_id"`
	Code       string `gorm:"uniqueIndex;not null" json:"code"`

	// "available" / "used" / "expired" / "revoked"
	Status string `gorm:"index;default:'available';size:16" json:"status"`

	// 模板快照（admin 改 template 不影响已发券）
	SnapshotName       string `gorm:"not null" json:"snapshot_name"`
	SnapshotType       string `gorm:"size:16;not null" json:"snapshot_type"`
	SnapshotValue      int64  `gorm:"not null" json:"snapshot_value"` // micro_usd
	SnapshotPackageIDs string `gorm:"type:text;default:''" json:"snapshot_package_ids"`

	GrantedBy   uint      `gorm:"default:0" json:"granted_by"` // admin user.ID；0 = 系统自动发
	GrantReason string    `gorm:"type:text" json:"grant_reason"`
	GrantedAt   time.Time `json:"granted_at"`
	ExpiresAt   *time.Time `gorm:"index" json:"expires_at"`

	UsedAt        *time.Time `json:"used_at"`
	UsedOnSubID   *uint      `gorm:"index" json:"used_on_sub_id"`
	UsedSavingUSD int64      `gorm:"default:0" json:"used_saving_usd"` // micro_usd

	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

// IsAvailable 券是否可用（status + 过期检查）。
func (uc *UserCoupon) IsAvailable(now time.Time) bool {
	if uc.Status != "available" {
		return false
	}
	if uc.ExpiresAt != nil && now.After(*uc.ExpiresAt) {
		return false
	}
	return true
}

// SnapshotEffectivePrice 给定原价（micro_usd），返回应用本券后的实际单价（micro_usd）。
//
// fixed_price：直接返回 SnapshotValue（但若 SnapshotValue > basePrice 则保护性退化为 basePrice）。
// 调用方应在事务内（持锁后）使用，结果可直接写入账单 AmountUSD。
func (uc *UserCoupon) SnapshotEffectivePrice(basePriceMicroUSD int64) int64 {
	switch uc.SnapshotType {
	case "fixed_price":
		// 防御：admin 创建模板时已校验 < basePrice，但 admin 后续改 package 价格可能导致 snapshot 反转
		if uc.SnapshotValue > basePriceMicroUSD {
			return basePriceMicroUSD // 不构成优惠时退回原价（券不浪费的责任在 UI 引导）
		}
		if uc.SnapshotValue < 0 {
			return 0
		}
		return uc.SnapshotValue
	default:
		// 未知类型 → 退回原价（不涨价、不打折）
		return basePriceMicroUSD
	}
}

// AppliesToPackage 判断本券是否适用于给定 package。
//
// 空 SnapshotPackageIDs → 适用全部；否则需 packageID 在 JSON 数组中。
// 调用方需 unmarshal SnapshotPackageIDs；这里走外部 helper（util/json 不放业务里）。
func (uc *UserCoupon) AppliesToPackage(packageID uint, allowedIDs []uint) bool {
	if len(allowedIDs) == 0 {
		return true // 空数组（或反序列化结果为空）= 全适用
	}
	for _, id := range allowedIDs {
		if id == packageID {
			return true
		}
	}
	return false
}
