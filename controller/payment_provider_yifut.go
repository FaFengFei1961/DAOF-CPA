// Package controller / payment_provider_yifut.go
//
// 易付通（yifut）PaymentProvider 适配器。Phase W-1（2026-05-21）。
//
// 实现 PaymentProvider interface。封装 yifut 现有的 LoadYifutConfig / CreateYifutOrder /
// SysConfig 读取，让上层 CreateTopup 与具体网关解耦。
//
// 行为兼容：本适配器**完全保持** yifut 原行为不变——SysConfig key 不改、HTTP 调用不改、
// 错误语义不改；只是把"controller 层 inline 调用 proxy.LoadYifutConfig + CreateYifutOrder"
// 包成接口实现。这样 W-3 引入 epusdt 时上层 CreateTopup 只需"换 provider key 取 adapter"。
package controller

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"daof-cpa/database"
	"daof-cpa/proxy"
)

// init 注册 yifut provider 到全局 registry。
// 注：这里不检查 IsConfigured() —— 即使 admin 还没配 PID / 密钥也注册 adapter，
// 实际调用 CreateOrder 才检查 SysConfig。这样 admin 后续配置生效不用重启进程。
func init() {
	RegisterPaymentProvider(NewYifutPaymentProvider())
}

// YifutPaymentProvider 易付通 adapter。私有结构，通过 RegisterPaymentProvider 注册。
//
// 注：cfg 不存进 struct，每次 CreateOrder / IsConfigured 都读全局 SysConfigCache。
// 这样 admin 后续改 PID / gateway / 密钥后下一次调用立即生效，不用重启。
type YifutPaymentProvider struct{}

// NewYifutPaymentProvider 构造 default yifut adapter。无配置；运行时读 SysConfig。
func NewYifutPaymentProvider() *YifutPaymentProvider { return &YifutPaymentProvider{} }

// Key 返回 "yifut"。
func (p *YifutPaymentProvider) Key() string { return database.TopupProviderYifut }

// IsConfigured 判定 admin 是否在 SysConfig 配齐 4 项：pid / gateway / 商户私钥 / 平台公钥。
func (p *YifutPaymentProvider) IsConfigured() bool {
	return proxy.LoadYifutConfig().IsConfigured()
}

// CreateOrder 调 yifut V2 /api/pay/create 统一下单。
//
// 必需 RawExtras：
//   - "pay_type"  string : alipay / wxpay / qqpay 等（由 controller 已校验白名单）
//   - "device"    string : pc / mobile / wechat / alipay 等
//
// 错误映射：
//   - 配置未齐 → ErrPaymentProviderNotConfigured
//   - 网关 5xx / 超时 / 验签失败 → ErrPaymentUpstreamUnavailable / ErrPaymentUpstreamMalformed
//   - 网关业务码 != 0 → ErrPaymentGatewayReject（msg 透传到日志，不下发给用户）
func (p *YifutPaymentProvider) CreateOrder(ctx context.Context, req *PaymentCreateOrderRequest) (*PaymentCreateOrderResult, error) {
	if req == nil {
		return nil, fmt.Errorf("%w: nil request", ErrPaymentProviderInternal)
	}

	cfg := proxy.LoadYifutConfig()
	if !cfg.IsConfigured() {
		return nil, ErrPaymentProviderNotConfigured
	}

	payType := req.RawExtras["pay_type"]
	device := req.RawExtras["device"]
	if payType == "" {
		return nil, fmt.Errorf("%w: missing pay_type in RawExtras", ErrPaymentProviderInternal)
	}
	if device == "" {
		device = "pc"
	}

	moneyStr := proxy.FormatMoneyFen(req.AmountFen)

	resp, err := proxy.CreateYifutOrder(ctx, cfg, proxy.YifutCreateOrderRequest{
		OutTradeNo: req.OutTradeNo,
		PayType:    payType,
		NotifyURL:  req.NotifyURL,
		ReturnURL:  req.ReturnURL,
		Name:       req.ProductName,
		Money:      moneyStr,
		ClientIP:   req.ClientIP,
		Device:     device,
	})
	if err != nil {
		// proxy.CreateYifutOrder 已经把 HTTP / 验签 / 反序列化错误都包装好，
		// 这里区分"网关业务拒绝"（resp.Code != 0）vs"网络/解析错误"。
		// resp != nil 表示拿到了响应但 code != 0 —— 网关业务拒绝。
		if resp != nil && resp.Code != 0 {
			return nil, fmt.Errorf("%w: yifut code=%d msg=%s", ErrPaymentGatewayReject, resp.Code, resp.Msg)
		}
		return nil, fmt.Errorf("%w: %v", ErrPaymentUpstreamUnavailable, err)
	}

	return &PaymentCreateOrderResult{
		ExternalTradeNo: resp.TradeNo,
		GatewayPayType:  resp.PayType,
		PayInfo:         resp.PayInfo,
	}, nil
}

// PublicOptions 给前端 /api/topup/options 用。
//
// 注意：这里返回的字段都是公开值（金额预设 / 启用的支付方式列表），
// pid / 密钥等敏感字段绝对不下发。
func (p *YifutPaymentProvider) PublicOptions() PaymentProviderPublicOptions {
	cfg := proxy.LoadYifutConfig()
	enabled := readStringConfig("yifut_enabled_methods", "alipay,wxpay")

	methods := []string{}
	for _, m := range splitCSV(enabled) {
		if allowedPayTypes[m] {
			methods = append(methods, m)
		}
	}

	presets := []int64{}
	for _, s := range splitCSV(readStringConfig("yifut_preset_amounts_fen", "1000,3000,5000,10000,30000,50000")) {
		if v, err := strconv.ParseInt(s, 10, 64); err == nil && v > 0 {
			presets = append(presets, v)
		}
	}

	return PaymentProviderPublicOptions{
		Key:          database.TopupProviderYifut,
		Label:        "易付通 (CNY)",
		Configured:   cfg.IsConfigured(),
		Currency:     "CNY",
		PresetsFen:   presets,
		MinAmountFen: readInt64Config("yifut_min_amount_fen", 100),
		MaxAmountFen: readInt64Config("yifut_max_amount_fen", 1_000_000),
		Methods:      methods,
		IconKey:      "yifut",
	}
}

// ParseAndVerifyWebhook 解析 + 验签易付通 V2 异步通知。
//
// 责任边界（W-3-P1）：
//   - 验签：proxy.VerifyYifutRSA（平台公钥 SHA256WithRSA）
//   - pid 校验：params.pid == cfg.PID（防跨商户重放）
//   - timestamp ±300s：防回放
//   - 金额解析：money 字符串 → fen int64（严格不引入 float）
//   - nonce 拼接：webhookNonce(provider, params) — 与 inline 实现一致
//   - 状态映射：trade_status=TRADE_SUCCESS → WebhookEventPaid；其它 → WebhookEventNonTerminal
//
// 不做的事：
//   - 查 DB（订单不存在 / 金额不一致 / 重放检测都由通用层 ProcessPaymentWebhook 处理）
//   - 写 receipt（通用层在 Verify 通过后单独写）
//   - 入账事务（通用层 finalizePaidTopup 用单事务执行）
//
// 错误使用 ErrWebhook* sentinel，通用层 errors.Is 映射 HTTP status。
func (p *YifutPaymentProvider) ParseAndVerifyWebhook(input *PaymentWebhookInput) (*PaymentWebhookEvent, error) {
	if input == nil {
		return nil, fmt.Errorf("%w: nil input", ErrWebhookMalformed)
	}

	cfg := proxy.LoadYifutConfig()
	if !cfg.IsConfigured() {
		return nil, ErrWebhookProviderNotConfigured
	}

	// yifut 回调是 GET，参数都在 QueryParams
	params := input.QueryParams
	if len(params) == 0 {
		return nil, fmt.Errorf("%w: empty query params", ErrWebhookMalformed)
	}

	// 1. RSA 签名（最强防线，先过这层避免后续 DB / log 开销暴露给伪造请求）
	if !proxy.VerifyYifutRSA(params, cfg.PlatformPublicKey) {
		return nil, ErrWebhookSignatureInvalid
	}

	// 2. pid 校验（防跨商户重放：攻击者用自家 yifut 商户产生合法签名回调投递到本站）
	if cfg.PID == "" || params["pid"] != cfg.PID {
		return nil, ErrWebhookPIDMismatch
	}

	// 3. timestamp ±300s 漂移
	if !yifutVerifyTimestamp(params["timestamp"]) {
		return nil, ErrWebhookTimestampDrift
	}

	// 4. 金额解析（保留 fen int64，让通用层用 order.ExchangeRateRmbPerUsdMicros 换算 micro_usd）
	moneyFen, ok := parseRMBStringToFen(params["money"])
	if !ok {
		return nil, fmt.Errorf("%w: bad money=%q", ErrWebhookMalformed, params["money"])
	}

	// 5. out_trade_no 必填（通用层用此查订单）
	outTradeNo := strings.TrimSpace(params["out_trade_no"])
	if outTradeNo == "" {
		return nil, fmt.Errorf("%w: missing out_trade_no", ErrWebhookMalformed)
	}

	// 6. trade_no 截断（与 YifutNotify 防御一致：网关签名已校验，但仍防外部串污染 schema）
	tradeNo := params["trade_no"]
	if len(tradeNo) > 128 {
		tradeNo = tradeNo[:128]
	}

	// 7. 状态映射：yifut V2 只在 TRADE_SUCCESS 时让平台入账；其它（TRADE_FINISHED / WAIT_BUYER_PAY 等）
	//    都不入账。通用层收到 NonTerminal 应直接 ack "success" 避免易付通重试。
	kind := WebhookEventNonTerminal
	if params["trade_status"] == "TRADE_SUCCESS" {
		kind = WebhookEventPaid
	}

	return &PaymentWebhookEvent{
		Kind:             kind,
		OutTradeNo:       outTradeNo,
		ExternalTradeNo:  tradeNo,
		Nonce:            webhookNonce(database.TopupProviderYifut, params),
		SignatureHash:    signatureHash(params["sign"]),
		AmountKind:       AmountKindFenCNY,
		AmountRaw:        moneyFen,
		CurrencyOriginal: "CNY",
		RawParams:        params,
	}, nil
}

// AllowedRemoteIPCIDRs 满足 IPAllowlistedProvider 可选接口（W-3 review M-2/M-7）。
// 返回 SysConfig yifut_notify_allowed_cidrs 配置（空 = 允许所有，仅依赖 RSA + nonce）。
func (p *YifutPaymentProvider) AllowedRemoteIPCIDRs() string {
	return readStringConfig("yifut_notify_allowed_cidrs", "")
}

// 编译期 assertion（W-3 review L-4 修复：从测试文件搬到生产文件，让 go build 即捕获）。
var (
	_ PaymentProvider       = (*YifutPaymentProvider)(nil)
	_ IPAllowlistedProvider = (*YifutPaymentProvider)(nil)
)

// yifutVerifyTimestamp 是 checkYifutTimestamp 的纯函数版本（无 log，便于 adapter 自检）。
// 接受 unix 秒字符串，与服务器时间漂移 ≤ notifyTimestampSkewSeconds（300s）则通过。
func yifutVerifyTimestamp(ts string) bool {
	ts = strings.TrimSpace(ts)
	if ts == "" {
		return false
	}
	tsInt, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return false
	}
	diff := time.Now().Unix() - tsInt
	if diff < 0 {
		diff = -diff
	}
	return diff <= notifyTimestampSkewSeconds
}
