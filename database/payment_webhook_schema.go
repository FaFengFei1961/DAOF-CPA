// Package database / payment_webhook_schema.go
//
// 支付网关 webhook 回执事实表（Sprint4-M3 加固）。
//
// 用途：
//   1. 防重放：(provider, nonce) 唯一索引 → 同一回调即使签名通过，重投不再入账
//   2. 审计可追溯：每次回调记录来源 IP / 签名摘要 / 接收时间，事故时可追溯
//   3. 旁证：与 TopupOrder.Status 的 CAS 联动，多层防线
//
// 该表 append-only，与 OperationLog / TopupRefund 同范式（业务字段 `<-:create` 防篡改）。
package database

import "time"

// PaymentWebhookReceipt 支付网关 webhook 回执（每次接受/拒绝都落 1 行）。
//
// nonce 选取策略：
//   - yifut：使用 `out_trade_no + ":" + sign 前 16 字符` 拼接，保证同一订单同一签名仅入账一次
//   - 其他网关：各自决定，但 (provider, nonce) 全局唯一
//
// SignatureHash 是签名字符串的 SHA-256，用于事后比对而不存原始签名（最小化敏感面）。
type PaymentWebhookReceipt struct {
	ID uint `gorm:"primaryKey" json:"id"`

	// Provider 网关标识，例如 "yifut"。与 nonce 联合唯一。
	Provider string `gorm:"<-:create;not null;size:32;uniqueIndex:idx_provider_nonce_unique" json:"provider"`

	// Nonce 业务级幂等键（如 out_trade_no:sign16）。同 provider 内全局唯一。
	Nonce string `gorm:"<-:create;not null;size:160;uniqueIndex:idx_provider_nonce_unique" json:"nonce"`

	// SignatureHash 原始签名的 SHA-256，用于审计比对。
	SignatureHash string `gorm:"<-:create;not null;size:64" json:"signature_hash"`

	// OutTradeNo 本地订单号，方便 admin 查询时按订单回溯。
	OutTradeNo string `gorm:"<-:create;index;size:64" json:"out_trade_no"`

	// RemoteIP 回调来源 IP（已过 sanitizer，去掉 X-Forwarded-For 注入）。
	RemoteIP string `gorm:"<-:create;size:64" json:"remote_ip"`

	// Status 处理结果：accepted / rejected_pid / rejected_money / rejected_ip / rejected_duplicate
	Status string `gorm:"<-:create;not null;size:32" json:"status"`

	// Reason 拒绝原因 / 备注（可选）。
	Reason string `gorm:"<-:create;type:text" json:"reason,omitempty"`

	ReceivedAt time.Time `gorm:"<-:create;index" json:"received_at"`
}
