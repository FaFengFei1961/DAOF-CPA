// Package database / customer_message_schema.go
//
// 工单系统（取代旧的 G1 单条留言）：
//   - Ticket：一次客服会话的容器，包含 subject + 状态机 + 时间戳
//   - TicketMessage：会话内的消息流（user/admin 双方）
//
// 状态机：
//
//	open    ── 进行中（默认）
//	closed  ── 已结束（任一方点击关闭）
//
// 自动清除：closed 状态超过 15 天的工单（含其全部消息）由 cron 物理删除
package database

import "time"

// Ticket 工单（一次客服会话）
type Ticket struct {
	ID      uint   `gorm:"primaryKey" json:"id"`
	UserID  uint   `gorm:"index;not null" json:"user_id"`
	Subject string `gorm:"size:200;not null" json:"subject"`

	Status string `gorm:"index;not null;default:'open';size:16" json:"status"` // open | closed

	// LastMessageAt 用于列表按"最后活动"排序
	LastMessageAt time.Time `gorm:"index" json:"last_message_at"`

	// 关闭信息
	ClosedAt        *time.Time `gorm:"index" json:"closed_at"` // 索引用于 cron 扫 15 天前的关闭工单
	ClosedByUser    bool       `json:"closed_by_user"`         // 用户主动关闭
	ClosedByAdmin   bool       `json:"closed_by_admin"`        // admin 关闭
	ClosedByAdminID uint       `json:"closed_by_admin_id"`

	// 已读跟踪：用户/管理员上次查看时间，用于显示未读徽章
	UserReadAt  *time.Time `json:"user_read_at"`
	AdminReadAt *time.Time `json:"admin_read_at"`

	CreatedAt time.Time `gorm:"index" json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TicketMessage 工单内的单条消息
type TicketMessage struct {
	ID       uint `gorm:"primaryKey" json:"id"`
	TicketID uint `gorm:"index;not null" json:"ticket_id"`

	// Sender：'user' | 'admin'
	Sender   string `gorm:"size:8;not null" json:"sender"`
	SenderID uint   `json:"sender_id"` // user.id（无论 user 还是 admin）

	Body string `gorm:"type:text;not null" json:"body"`

	// 关联通知 id：发消息时给对方发的站内通知（便于审计）
	NotificationID *uint `json:"notification_id,omitempty"`

	CreatedAt time.Time `gorm:"index" json:"created_at"`
}
