// Package database / notification_schema.go
//
// 通知系统增强相关数据模型：
//   - NotificationPreference：用户级订阅开关与阈值（lazy default：未保存的用户读 SysConfig）
//   - NotificationBroadcast：管理员群发任务（一次创建对应 N 条 Notification）
//   - NotificationBroadcastTarget：群发→Notification 的关联（便于撤回 + 已读率统计）
//
// 设计原则与 subscription_schema.go 一致：
//  1. 业务参数全走配置/字段，**绝不写死**
//  2. JSON 字段统一 type:text + default:'{}' / '[]'，避免 NULL 写入歧义
//  3. DeletedAt 不加（这三类记录无软删除场景；Broadcast 改状态为 revoked）
package database

import (
	"time"
)

// ============================================================================
// NotificationPreference ─ 用户通知偏好
// ============================================================================
//
// 一个用户一行（UserID 唯一）。未创建偏好行时，业务层（database.LoadPreference）
// 会从 SysConfig 读取系统默认（notif_default_categories / notif_default_thresholds_csv）。
//
// EnabledCategories：JSON 对象，缺失的 key 视为"启用"。仅显式 false 才屏蔽。
// 例：{"subscription_expiring":false,"subscription_usage_warn":true,"refund":true}
// 注意：security 和 system / broadcast 不受此控制，强制送达（Dispatch 入口绕开）。
//
// UsageThresholds：JSON 数组，套餐使用率告警阈值（%）。
// 例：[80, 100]；空数组 `[]` 表示完全关闭用量预警。
type NotificationPreference struct {
	ID     uint `gorm:"primaryKey" json:"id"`
	UserID uint `gorm:"uniqueIndex;not null" json:"user_id"`

	EnabledCategories string `gorm:"type:text;not null;default:'{}'" json:"enabled_categories"`
	UsageThresholds   string `gorm:"type:text;not null;default:'[80,100]'" json:"usage_thresholds"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ============================================================================
// NotificationBroadcast ─ 管理员群发任务
// ============================================================================
//
// admin 在"通知管理"页面创建一条 broadcast 后：
//  1. 解析 TargetMode + TargetSpec → 一组 user_ids
//  2. 事务批量 INSERT notifications（dedupKey="broadcast:{bid}:{uid}"）
//     + INSERT notification_broadcast_targets
//  3. 更新 broadcast.status="sent" + recipient_count
//
// TargetMode 枚举（admin 端可见）：
//
//	all        ── 全员；TargetSpec 留 "{}"
//	package    ── 已购指定套餐；TargetSpec={"package_id":3}
//	user_ids   ── 指定用户列表；TargetSpec={"user_ids":[1,2,3]}
//
// Status 枚举：
//
//	draft   ── 已创建未发送（V1 不支持，预留）
//	sent    ── 已发送
//	revoked ── 已撤回（仅改状态，已发的 Notification 不删，避免割裂用户体验）
type NotificationBroadcast struct {
	ID         uint   `gorm:"primaryKey" json:"id"`
	OperatorID uint   `gorm:"index;not null" json:"operator_id"`
	Title      string `gorm:"not null" json:"title"`
	Body       string `gorm:"type:text" json:"body"`
	Severity   string `gorm:"default:'info'" json:"severity"`

	ActionURL  string `json:"action_url"`
	ActionText string `json:"action_text"`

	TargetMode string `gorm:"index;not null" json:"target_mode"`
	TargetSpec string `gorm:"type:text;not null;default:'{}'" json:"target_spec"`

	Status         string `gorm:"index;not null;default:'sent'" json:"status"`
	RecipientCount int    `gorm:"default:0" json:"recipient_count"`
	ReadCount      int    `gorm:"default:0" json:"read_count"`

	CreatedAt time.Time  `gorm:"index" json:"created_at"`
	SentAt    *time.Time `json:"sent_at"`
}

// ============================================================================
// NotificationBroadcastTarget ─ 群发→通知关联
// ============================================================================
//
// 用于：
//   - 已读率统计：JOIN notifications 看 read_at IS NOT NULL 占比
//   - 撤回时定位需要标记的目标（V1 不删，仅 broadcast.status='revoked'）
type NotificationBroadcastTarget struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	BroadcastID    uint      `gorm:"index;not null;uniqueIndex:idx_bcast_user" json:"broadcast_id"`
	UserID         uint      `gorm:"index;not null;uniqueIndex:idx_bcast_user" json:"user_id"`
	NotificationID uint      `gorm:"index;not null" json:"notification_id"`
	CreatedAt      time.Time `json:"created_at"`
}
