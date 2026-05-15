// Package database / topup_schema.go
//
// 用户余额充值订单（对接易付通 V2 RSA 协议）+ 退款事实表。
//
// 状态机（TopupOrder）：
//
//	created  ── 本地下单成功，已生成支付信息，等待用户支付
//	paid     ── 收到 notify_url 异步回调 trade_status=TRADE_SUCCESS，已加额度
//	failed   ── 用户取消 / 超时 / 关单
//	refunded ── admin 已发起退款（部分或全额）
//
// 金额本位：用户支付 RMB（fen int64），amount_usd 列入额度（micro_usd int64）。
// 严禁 float64 参与金额计算。
//
// 退款事实表（TopupRefund）：fix CRITICAL Sprint1-P0-6
// 每笔退款独立一行，external_refund_ref 唯一约束防同一退款重复入账。
// TopupOrder.RefundedAmountRMB 是聚合视图，由 TopupRefund 累加而来（事务内同步）。
package database

import (
	"time"
)

// TopupOrder 用户充值订单
type TopupOrder struct {
	ID uint `gorm:"primaryKey" json:"id"`

	// OutTradeNo 商户订单号，全局唯一。形如 tp{userID}{unixmilli}{randhex4}
	OutTradeNo string `gorm:"uniqueIndex;not null;size:64" json:"out_trade_no"`
	// TradeNo 易付通系统订单号（回调里返回；下单时为空）
	TradeNo string `gorm:"index;size:64" json:"trade_no"`
	// ApiTradeNo 第三方（支付宝/微信）订单号（查询接口才有）
	ApiTradeNo string `gorm:"size:64" json:"api_trade_no"`

	UserID uint `gorm:"index;not null" json:"user_id"`

	// PayType 支付方式：alipay / wxpay / qqpay / bank / jdpay / paypal / douyinpay
	PayType string `gorm:"index;not null;size:16" json:"pay_type"`
	// Device 设备类型：pc / mobile / qq / wechat / alipay / douyin / jump
	Device string `gorm:"size:16" json:"device"`

	// MoneyRMB 用户支付的 RMB 金额（fen 分，1 RMB = 100 fen；提交易付通时除以 100 还原元）
	MoneyRMB int64 `gorm:"not null" json:"money_rmb"`
	// AmountUSD 入账的 USD 额度（micro_usd, USD * 1e6）。下单时按 exchange_rate 锁定；回调成功后加到 user.Quota。
	AmountUSD int64 `gorm:"not null" json:"amount_usd"`
	// ExchangeRateRmbPerUsdMicros 下单时使用的汇率快照：RMB per USD × 1e6（int64 定点）。
	// 例：7.2 RMB/USD → 7_200_000；7.2345 → 7_234_500。
	// fix CRITICAL Sprint4-M3：旧 ExchangeRateSnapshot float64 改为定点 int64，杜绝
	// IEEE 754 噪声进入审计字段。仅用于审计回溯（实际换算用 MoneyRMB / AmountUSD 持久化值）。
	ExchangeRateRmbPerUsdMicros int64 `gorm:"not null" json:"exchange_rate_rmb_per_usd_micros"`

	// Name 商品名称（充值显示用，提交给易付通的 name 字段）
	Name string `gorm:"size:127" json:"name"`
	// ClientIP 用户发起支付的 IP（提交给易付通）
	ClientIP string `gorm:"size:64" json:"client_ip"`
	// Param 业务扩展参数；当前留空备用
	Param string `gorm:"size:255" json:"param"`

	// GatewayPayType / PayInfo 是易付通 V2 原生下单返回。
	// GatewayPayType: jump | qrcode | jsapi | app | scan | wxplugin | wxapp | html | urlscheme
	GatewayPayType string `gorm:"size:32" json:"gateway_pay_type"`
	PayInfo        string `gorm:"type:text" json:"pay_info"`

	// PayMethod V2 接口类型：web | jump | jsapi | app | scan | applet（默认 web）
	PayMethod string `gorm:"size:16;default:'web'" json:"pay_method"`

	Status string `gorm:"index;not null;default:'created';size:16" json:"status"` // created | paid | failed | refunded

	// RefundedAmountRMB 累计已退款（fen, RMB * 100；部分退款累加，由 admin 在易付通后台分次退款时累计登记）
	RefundedAmountRMB int64 `gorm:"default:0" json:"refunded_amount_rmb"`
	// RefundNo / OutRefundNo 存 admin 在易付通后台填的退款单号，作为外部对账锚点。
	RefundNo    string `gorm:"size:64" json:"refund_no"`
	OutRefundNo string `gorm:"size:64" json:"out_refund_no"`

	CreatedAt time.Time  `gorm:"index" json:"created_at"`
	PaidAt    *time.Time `json:"paid_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// TopupRefund 充值退款事实表。一笔退款一行，append-only。
//
// fix CRITICAL Sprint1-P0-6：旧实现的退款幂等不成立——同一 ExternalRefundRef 多次提交
// 会让 TopupOrder.RefundedAmountRMB 累加（覆盖 RefundNo/OutRefundNo 字段），平台双扣余额
// 用户钱包却只到账一次。
//
// 新实现：每次退款先 INSERT 本表，ExternalRefundRef 唯一约束在 DB 层拦截重复提交。
// 业务字段加 `gorm:"<-:create"` 防 UPDATE 篡改（与 OperationLog append-only 同范式）。
//
// TopupOrder.RefundedAmountRMB 仍保留为聚合视图，由本表 SUM(amount_fen) 计算并在
// 同事务内同步——但 DB 层真相在 TopupRefund，订单表仅做查询便利。
type TopupRefund struct {
	ID uint `gorm:"primaryKey" json:"id"`

	// TopupOrderID 关联充值订单
	TopupOrderID uint `gorm:"<-:create;index;not null" json:"topup_order_id"`

	// ExternalRefundRef admin 在易付通后台填的商户退款单号，幂等键。
	// 全局唯一防同一退款单号重复入账。size 与 TopupOrder.RefundNo 对齐。
	ExternalRefundRef string `gorm:"<-:create;uniqueIndex;not null;size:128" json:"external_refund_ref"`

	// AmountFen 本次退款 fen 金额（RMB × 100）。> 0。
	AmountFen int64 `gorm:"<-:create;not null" json:"amount_fen"`

	// AmountMicroUSD 等值 micro_usd（按订单入账时锁定的 RMB/USD 比例换算）。
	// 仅当 reclaim_quota=true 时实际扣回额度；保留量统一记账。
	AmountMicroUSD int64 `gorm:"<-:create;not null" json:"amount_micro_usd"`

	// ReclaimQuota 退款是否扣回 user.quota。
	// false = 退钱但保留额度（客服补偿场景）；true = 完整冲账。
	ReclaimQuota bool `gorm:"<-:create;not null" json:"reclaim_quota"`

	// OperatorID 操作 admin 用户 ID。
	OperatorID uint `gorm:"<-:create;index;default:0" json:"operator_id"`

	// Reason 退款原因 / admin 备注（可选）。
	Reason string `gorm:"<-:create;type:text" json:"reason"`

	CreatedAt time.Time `gorm:"<-:create;index" json:"created_at"`
}
