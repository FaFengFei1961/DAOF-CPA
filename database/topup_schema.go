// Package database / topup_schema.go
//
// 用户余额充值订单（对接易付通 V2 RSA 协议）。
//
// 状态机：
//
//	created  ── 本地下单成功，已生成支付信息，等待用户支付
//	paid     ── 收到 notify_url 异步回调 trade_status=TRADE_SUCCESS，已加额度
//	failed   ── 用户取消 / 超时 / 关单
//	refunded ── admin 已发起退款（部分或全额）
//
// 金额本位：用户支付 RMB，但 amount_usd 列入额度（按下单时 exchange_rate 锁定）。
// 这样不破坏现有 User.Quota 是 USD 的本位约定。
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
	// ExchangeRateSnapshot 下单时使用的汇率（USD→RMB），float64 是因为汇率本身可能 7.2345 这种小数，
	// 不直接进入金额计算，仅用于审计回溯（实际换算用 MoneyRMB / AmountUSD 持久化值）。
	ExchangeRateSnapshot float64 `gorm:"not null" json:"exchange_rate_snapshot"`

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
