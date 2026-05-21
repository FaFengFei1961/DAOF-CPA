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
