// Package controller / payment_provider.go
//
// 支付 provider 抽象。Phase W-1（2026-05-21）。
//
// 目标：把 CreateTopup 里 "调外部网关下单 + 返回前端展示信息" 这段 provider-specific 的
// 逻辑抽到 PaymentProvider interface 后面，让上层 handler 与具体网关解耦。
//
// 设计参考 controller/oauth_provider.go（OAuthProvider）：
//   - 每个 provider 一个 .go 文件（payment_provider_<key>.go）
//   - 启动时通过 RegisterPaymentProvider 注册到全局 registry（用 init() 自注册）
//   - Key() 返回字符串常量（database.TopupProvider* 常量对应）
//
// W-1 范围限制：只抽象 CreateOrder + 配置侧（IsConfigured / PublicOptions）。
// Webhook 路径（YifutNotify / YifutReturn）仍按现路由保留，未来 epusdt 加新路由即可。
// Webhook 通用层抽象留到 W-3（有 epusdt 实际场景时验证设计边界）。
//
// 与 OAuthProvider 的关键差异：
//   - PaymentProvider 不返回 access_token，返回前端展示信息（QR 码 / 跳转 URL / 钱包地址）
//   - PublicOptions 暴露给前端 /api/topup/options（金额预设 / 支付方式列表），不带敏感字段
package controller

import (
	"context"
	"errors"
	"sort"
	"sync"
)

// PaymentProvider 是一个支付网关适配器。
//
// 每个 provider 实现独立 .go 文件（payment_provider_<key>.go）。
// 启动时通过 RegisterPaymentProvider 注册到全局 registry。
type PaymentProvider interface {
	// Key 唯一 provider 标识，与 database.TopupProvider* 常量对应（"yifut" / "epusdt" / ...）。
	Key() string

	// IsConfigured 判定 admin 是否已在 SysConfig 配齐 provider 必需的凭据 / 端点 / 密钥。
	// 未配齐时，CreateOrder 应返回 ErrPaymentProviderNotConfigured。
	IsConfigured() bool

	// CreateOrder 调 provider 侧网关建订单，返回前端拼支付界面所需的全部信息。
	//
	// provider-specific 数据通过 RawExtras 透传给前端（例如 yifut 的 gateway_pay_type/pay_info、
	// epusdt 的 chain/wallet_address/exact_amount）—— 前端按 provider key 解析。
	//
	// 注意：CreateOrder 不写 DB（TopupOrder 由通用层 controller/topup.go 提前建好）。
	// provider 只负责"对外部网关发请求 + 解析响应"。
	CreateOrder(ctx context.Context, req *PaymentCreateOrderRequest) (*PaymentCreateOrderResult, error)

	// PublicOptions 给前端 /api/topup/options 用，返回前端渲染需要的配置：
	//   - 是否启用（IsConfigured）
	//   - 用户可选金额（presets）
	//   - 支付方式 / 链类型 / 币种（provider-specific 列表）
	//   - 金额上下限（min/max）
	//
	// 不包含敏感字段（pid / private_key / webhook_secret 等绝不下发）。
	PublicOptions() PaymentProviderPublicOptions
}

// PaymentCreateOrderRequest 是 controller 传给 provider 的下单请求快照。
// 通用字段（OutTradeNo / AmountFen / UserID / IPs）由 controller 装好，
// provider-specific 参数通过 RawExtras 传入。
type PaymentCreateOrderRequest struct {
	// OutTradeNo 商户订单号（已在 controller 层 generateOutTradeNo 生成并落库）。
	OutTradeNo string

	// UserID 发起充值的用户 ID（provider 可能记日志或对账用，不必传给网关）。
	UserID uint

	// AmountFen 用户支付的 RMB 金额（fen, int64）。对 yifut 直接用；
	// 对 USDT 等非 CNY provider，controller 层会换算到 amount_usd_micro 后传 RawExtras。
	AmountFen int64

	// AmountUSDMicro 入账的 USD 等额（micro_usd, int64）。
	// 对 epusdt 等 USDT/USDC 1:1 provider，这就是用户实际付的链上 token 数（micro 单位）。
	AmountUSDMicro int64

	// ExchangeRateRmbPerUsdMicros 下单时锁定的汇率快照。yifut webhook 金额校验时复用。
	ExchangeRateRmbPerUsdMicros int64

	// ClientIP 用户发起支付的 IP（部分网关需要传 client_ip 做风控）。
	ClientIP string

	// NotifyURL provider 应该往哪个 URL 推送异步通知（已由 controller 层 buildAbsoluteURL 装好）。
	NotifyURL string

	// ReturnURL 用户支付完成后 provider 跳转到哪（同上）。
	ReturnURL string

	// ProductName 商品名（提交给网关展示用）。
	ProductName string

	// RawExtras provider-specific 入参（如 yifut 的 pay_type/device，epusdt 的 chain/token）。
	// provider 自己从这里取它认识的 key；不认识的 key 忽略。
	RawExtras map[string]string
}

// PaymentCreateOrderResult 是 provider 下单成功后返回给 controller 的标准化响应。
// controller 把它写回 TopupOrder（TradeNo / GatewayPayType / PayInfo）+ 反给前端。
type PaymentCreateOrderResult struct {
	// ExternalTradeNo provider 侧订单号（yifut: trade_no；epusdt: 内部 order_id 或 wallet 收款 ID）。
	ExternalTradeNo string

	// GatewayPayType 支付/收款类型，前端用此决定如何展示（"qrcode" / "jump" / "wallet_address" 等）。
	GatewayPayType string

	// PayInfo provider-specific 的支付信息（QR 码内容 / 跳转 URL / 钱包地址 + 精确金额）。
	// 前端按 GatewayPayType 解析。
	PayInfo string

	// RawExtras provider-specific 的额外字段（链类型 / token / 过期时间等），透传给前端。
	RawExtras map[string]string
}

// PaymentProviderPublicOptions 是 provider 暴露给前端 /api/topup/options 的元数据。
// 字段都属于"公开值"（金额预设 / 支付方式列表 / IsConfigured 等），无敏感性。
type PaymentProviderPublicOptions struct {
	// Key 与 PaymentProvider.Key() 一致。前端用此识别 provider。
	Key string `json:"key"`

	// Label 用户可见的展示名（"易付通 (CNY)" / "Web3 USDT" 等）。
	Label string `json:"label"`

	// Configured admin 是否配齐凭据。未 configured 时前端不渲染该 provider 按钮。
	Configured bool `json:"configured"`

	// Currency 提示用户该 provider 的本位币（"CNY" / "USDT" / "USDC"）。
	// 用于前端显示"¥100" vs "100 USDT"。
	Currency string `json:"currency"`

	// PresetsFen 用户可选的金额预设（与历史 yifut 字段一致：fen int64）。
	// 对非 CNY provider，前端按 ExchangeRateRmbPerUsdMicros 换算展示等额 USDT。
	PresetsFen []int64 `json:"presets_fen"`

	// MinAmountFen / MaxAmountFen fen 上下限。
	MinAmountFen int64 `json:"min_amount_fen"`
	MaxAmountFen int64 `json:"max_amount_fen"`

	// Methods provider-specific 支付方式列表：
	//   - yifut: ["alipay", "wxpay"]
	//   - epusdt: ["trc20-usdt", "erc20-usdt", "bep20-usdt", "polygon-usdt"]
	Methods []string `json:"methods"`

	// IconKey 前端按此 key 选内置 brand SVG（"yifut" / "epusdt" / "fallback"）。
	IconKey string `json:"icon_key"`
}

// ErrPayment* 是 PaymentProvider 错误的标准 sentinel。
// 上层 handler 用 errors.Is 映射到 HTTP status + i18n message_code。
var (
	// ErrPaymentProviderNotConfigured admin 未配齐凭据 / 端点。503。
	ErrPaymentProviderNotConfigured = errors.New("payment: provider not configured by admin")
	// ErrPaymentGatewayReject 网关侧业务拒绝（错误码 / 金额超限 / 商户被冻等）。502。
	ErrPaymentGatewayReject = errors.New("payment: gateway rejected order")
	// ErrPaymentUpstreamUnavailable 网络故障 / 5xx / 超时。502。
	ErrPaymentUpstreamUnavailable = errors.New("payment: upstream unavailable")
	// ErrPaymentUpstreamMalformed 响应解析失败 / 签名校验失败。502。
	ErrPaymentUpstreamMalformed = errors.New("payment: upstream response malformed")
	// ErrPaymentProviderInternal provider adapter 自己出错（如 marshal 失败）。500。
	ErrPaymentProviderInternal = errors.New("payment: provider internal error")
)

// 全局 provider registry。
// 启动时由各 payment_provider_<key>.go 用 init() 注册。
var (
	paymentProvidersMu sync.RWMutex
	paymentProviders   = map[string]PaymentProvider{}
)

// RegisterPaymentProvider 把一个 provider 加入全局 registry。
// 重复注册同一 Key 会覆盖（用于测试 stub）。
func RegisterPaymentProvider(p PaymentProvider) {
	if p == nil || p.Key() == "" {
		return
	}
	paymentProvidersMu.Lock()
	defer paymentProvidersMu.Unlock()
	paymentProviders[p.Key()] = p
}

// GetPaymentProvider 按 key 取 provider。第二个返回值表示是否注册过。
func GetPaymentProvider(key string) (PaymentProvider, bool) {
	paymentProvidersMu.RLock()
	defer paymentProvidersMu.RUnlock()
	p, ok := paymentProviders[key]
	return p, ok
}

// ListConfiguredPaymentProviderOptions 返回当前 admin 已配齐凭据的 provider 完整选项。
// 用于 GET /api/topup/options 暴露给前端"用户当前可选哪些充值方式"。
//
// 返回顺序：按 Key 字典序排序，保证前端渲染稳定（map 遍历顺序不固定）。
func ListConfiguredPaymentProviderOptions() []PaymentProviderPublicOptions {
	paymentProvidersMu.RLock()
	defer paymentProvidersMu.RUnlock()
	out := make([]PaymentProviderPublicOptions, 0, len(paymentProviders))
	for _, p := range paymentProviders {
		if p.IsConfigured() {
			out = append(out, p.PublicOptions())
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// ResetPaymentProvidersForTest 测试 hook：清空 registry。仅测试使用。
func ResetPaymentProvidersForTest() {
	paymentProvidersMu.Lock()
	defer paymentProvidersMu.Unlock()
	paymentProviders = map[string]PaymentProvider{}
}
