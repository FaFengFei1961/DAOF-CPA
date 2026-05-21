// Package proxy / yifut_client.go
//
// 易付通 V2 接口 HTTP 客户端（SHA256WithRSA 签名）。
//
// 文档：https://www.yifut.com/doc/index.html
//
// 接口清单：
//   - POST {gateway}/api/pay/create   统一下单（返回 pay_type + pay_info）
//   - POST {gateway}/api/pay/query    订单查询
//   - POST {gateway}/api/pay/refund   订单退款（需后台开退款 API 开关）
//   - POST {gateway}/api/pay/close    关闭订单
//
// 双向验签：商户私钥签出站请求；平台公钥验入站回调。
package proxy

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ─── 配置 ─────────────────────────────────────────────────

// YifutConfig 即时配置快照（避免下单中途 admin 改 SysConfig 导致签名/网关错配）
type YifutConfig struct {
	PID                string
	Gateway            string
	MerchantPrivateKey *rsa.PrivateKey
	PlatformPublicKey  *rsa.PublicKey
}

// SSRF 拒绝列表拆两档（2026-05-21 用户反馈"Clash TUN 模式下 yifut 调用被拒"）：
//
// alwaysDeny 是真私网/元数据范围 —— 任何场景都拒，因为这是 SSRF 攻击的真实目标
// （内部服务、云元数据 endpoint）。
//
// proxyEgress 是 CGNAT / RFC 2544 benchmark / IPv6 transition 段 —— 它们的合法
// 真实用途是 Clash TUN / Cloudflare WARP / V2Ray TUN 等本地代理的虚拟 egress IP。
// 攻击场景下命中这里几乎拿不到任何东西（这些段没人放真实服务），所以加一个
// admin opt-in SysConfig flag `yifut_allow_egress_proxy_ranges`，admin 在已知
// 本机走代理时显式打开，跳过这一档（alwaysDeny 仍然生效）。
var (
	yifutAlwaysDenyPrefixes = []netip.Prefix{
		netip.MustParsePrefix("169.254.0.0/16"),     // Link-local (含 IMDS)
		netip.MustParsePrefix("168.63.129.16/32"),   // Azure Wireserver
		netip.MustParsePrefix("100.100.100.200/32"), // 阿里云 IMDS
		netip.MustParsePrefix("fd00::/8"),           // IPv6 ULA
		netip.MustParsePrefix("fe80::/10"),          // IPv6 Link-local
		netip.MustParsePrefix("192.0.2.0/24"),       // RFC 5737 documentation（攻击者可能伪造此段）
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("192.168.0.0/16"),
		netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("::1/128"),
	}
	yifutProxyEgressPrefixes = []netip.Prefix{
		netip.MustParsePrefix("100.64.0.0/10"), // CGNAT - VPN / 代理常用 egress
		netip.MustParsePrefix("198.18.0.0/15"), // RFC 2544 benchmark - Clash TUN / WARP loopback
		netip.MustParsePrefix("2002::/16"),     // IPv6 6to4
		netip.MustParsePrefix("2001::/32"),     // IPv6 Teredo
	}
)

// yifutAllowEgressProxyRanges 读 admin 开关：是否在 SSRF 检查里允许 CGNAT/
// benchmark 等代理虚拟 egress 段。默认 false，admin 在 /admin/finance/payment
// 显式勾选后才生效。
func yifutAllowEgressProxyRanges() bool {
	SysConfigMutex.RLock()
	v := strings.TrimSpace(SysConfigCache["yifut_allow_egress_proxy_ranges"])
	SysConfigMutex.RUnlock()
	return v == "1" || strings.EqualFold(v, "true")
}

func isUnsafeYifutIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	for _, p := range yifutAlwaysDenyPrefixes {
		if p.Contains(ip) {
			return true
		}
	}
	if !yifutAllowEgressProxyRanges() {
		for _, p := range yifutProxyEgressPrefixes {
			if p.Contains(ip) {
				return true
			}
		}
	}
	return false
}

// LoadYifutConfig 从 SysConfigCache 拉一次配置 + 解析 RSA 密钥。
// 解析失败的字段保持 nil，IsConfigured 会判定未配置。
func LoadYifutConfig() YifutConfig {
	SysConfigMutex.RLock()
	pid := strings.TrimSpace(SysConfigCache["yifut_pid"])
	gateway := strings.TrimRight(strings.TrimSpace(SysConfigCache["yifut_gateway"]), "/")
	privPEM := strings.TrimSpace(SysConfigCache["yifut_merchant_private_key"])
	pubPEM := strings.TrimSpace(SysConfigCache["yifut_platform_public_key"])
	SysConfigMutex.RUnlock()

	cfg := YifutConfig{PID: pid, Gateway: gateway}
	if privPEM != "" {
		if priv, err := ParseRSAPrivateKey(privPEM); err == nil {
			cfg.MerchantPrivateKey = priv
		}
	}
	if pubPEM != "" {
		if pub, err := ParseRSAPublicKey(pubPEM); err == nil {
			cfg.PlatformPublicKey = pub
		}
	}
	return cfg
}

// IsConfigured 全部 4 项就绪才算配置好
func (c YifutConfig) IsConfigured() bool {
	return c.PID != "" && c.Gateway != "" && c.MerchantPrivateKey != nil && c.PlatformPublicKey != nil
}

// ValidateGateway 防 SSRF：scheme 必须 https；主机不能落入公网支付网关不应使用的特殊网段。
func ValidateGateway(raw string) error {
	if raw == "" {
		return fmt.Errorf("gateway empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse gateway: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("gateway scheme must be https (got %q)", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("gateway host empty")
	}
	if ip := net.ParseIP(host); ip != nil {
		addr, ok := netip.AddrFromSlice(ip)
		if !ok || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() || isUnsafeYifutIP(addr) {
			return fmt.Errorf("gateway IP %s not allowed", ip)
		}
	}
	lower := strings.ToLower(host)
	for _, blocked := range []string{"localhost", "127.0.0.1", "0.0.0.0", "::1"} {
		if lower == blocked {
			return fmt.Errorf("gateway host %s not allowed", lower)
		}
	}
	return nil
}

// ─── 下单：POST /api/pay/create ───────────────────────────────

type YifutCreateOrderRequest struct {
	OutTradeNo string
	PayType    string // alipay | wxpay
	NotifyURL  string
	ReturnURL  string
	Name       string
	Money      string // "0.01" 等 RMB 字符串
	ClientIP   string
	Method     string // web (默认) | jump | jsapi | app | scan | applet
	Device     string // pc | mobile | wechat | alipay | qq | douyin
	Param      string // 业务扩展参数
}

// YifutCreateOrderResponse 是易付通 V2 下单返回的核心字段。
type YifutCreateOrderResponse struct {
	Code    int
	Msg     string
	TradeNo string
	PayType string // jump/qrcode/jsapi/app/scan/wxplugin/wxapp/html/urlscheme
	PayInfo string // V2 原始 pay_info
}

// v2RawCreateOrderResponse 直接对应 V2 文档返回结构
type v2RawCreateOrderResponse struct {
	Code      int    `json:"code"` // 0=成功
	Msg       string `json:"msg"`
	TradeNo   string `json:"trade_no"`
	PayType   string `json:"pay_type"`
	PayInfo   string `json:"pay_info"`
	Timestamp string `json:"timestamp"`
	Sign      string `json:"sign"`
	SignType  string `json:"sign_type"`
}

// CreateYifutOrder 调 V2 /api/pay/create 统一下单。返回结构化响应。
func CreateYifutOrder(ctx context.Context, cfg YifutConfig, req YifutCreateOrderRequest) (*YifutCreateOrderResponse, error) {
	if !cfg.IsConfigured() {
		return nil, fmt.Errorf("yifut not configured")
	}
	if err := ValidateGateway(cfg.Gateway); err != nil {
		return nil, fmt.Errorf("yifut gateway invalid: %w", err)
	}

	method := req.Method
	if method == "" {
		method = "web" // V2 默认接口类型，自动按 device 返回 jump/qrcode/urlscheme
	}

	params := map[string]string{
		"pid":          cfg.PID,
		"method":       method,
		"type":         req.PayType,
		"out_trade_no": req.OutTradeNo,
		"notify_url":   req.NotifyURL,
		"return_url":   req.ReturnURL,
		"name":         req.Name,
		"money":        req.Money,
		"clientip":     req.ClientIP,
		"device":       req.Device,
		"param":        req.Param,
		"timestamp":    strconv.FormatInt(time.Now().Unix(), 10),
		"sign_type":    "RSA",
	}
	sig, err := SignYifutRSA(params, cfg.MerchantPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("sign create order: %w", err)
	}
	params["sign"] = sig

	body, err := postFormToYifut(ctx, cfg.Gateway+"/api/pay/create", params)
	if err != nil {
		return nil, fmt.Errorf("yifut create order: %w", err)
	}

	var raw v2RawCreateOrderResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode response: %w (body=%s)", err, truncate(string(body), 256))
	}
	if raw.Code != 0 {
		return &YifutCreateOrderResponse{Code: raw.Code, Msg: raw.Msg}, fmt.Errorf("yifut error code=%d msg=%s", raw.Code, raw.Msg)
	}

	// 验证响应签名（接口返回也需用平台公钥验签）
	respParams := map[string]string{
		"code":      strconv.Itoa(raw.Code),
		"msg":       raw.Msg,
		"trade_no":  raw.TradeNo,
		"pay_type":  raw.PayType,
		"pay_info":  raw.PayInfo,
		"timestamp": raw.Timestamp,
		"sign_type": raw.SignType,
		"sign":      raw.Sign,
	}
	if !VerifyYifutRSA(respParams, cfg.PlatformPublicKey) {
		return nil, fmt.Errorf("yifut response signature invalid")
	}
	// fix MAJOR M-B3（codex 第二十一轮）：notify/return 路径已校验 timestamp 防重放，
	// 但 create/query/refund 的**响应**没校验。攻击者中间人能用历史合法响应回放骗后端
	// 进入"以为创建成功"状态。±300s 窗口检查与 notify 一致。
	if err := verifyYifutResponseTimestamp(raw.Timestamp); err != nil {
		return nil, fmt.Errorf("yifut create resp: %w", err)
	}

	return &YifutCreateOrderResponse{
		Code:    raw.Code,
		Msg:     raw.Msg,
		TradeNo: raw.TradeNo,
		PayType: raw.PayType,
		PayInfo: raw.PayInfo,
	}, nil
}

// ─── 订单查询：POST /api/pay/query ───────────────────────────────

type YifutOrderQuery struct {
	Code        int    `json:"code"`
	Msg         string `json:"msg"`
	TradeNo     string `json:"trade_no"`
	OutTradeNo  string `json:"out_trade_no"`
	ApiTradeNo  string `json:"api_trade_no"`
	Type        string `json:"type"`
	PID         int    `json:"pid"`
	AddTime     string `json:"addtime"`
	EndTime     string `json:"endtime"`
	Name        string `json:"name"`
	Money       string `json:"money"`
	RefundMoney string `json:"refundmoney"`
	Status      int    `json:"status"` // 0=未支付 1=已支付 2=已退款 3=已冻结 4=预授权
	Param       string `json:"param"`
	Buyer       string `json:"buyer"`
	ClientIP    string `json:"clientip"`
	Timestamp   string `json:"timestamp"`
	Sign        string `json:"sign"`
	SignType    string `json:"sign_type"`
}

// QueryYifutOrder POST /api/pay/query 查询订单（用 out_trade_no 或 trade_no，二选一）
func QueryYifutOrder(ctx context.Context, cfg YifutConfig, outTradeNo string) (*YifutOrderQuery, error) {
	if !cfg.IsConfigured() {
		return nil, fmt.Errorf("yifut not configured")
	}
	if err := ValidateGateway(cfg.Gateway); err != nil {
		return nil, fmt.Errorf("yifut gateway invalid: %w", err)
	}

	params := map[string]string{
		"pid":          cfg.PID,
		"out_trade_no": outTradeNo,
		"timestamp":    strconv.FormatInt(time.Now().Unix(), 10),
		"sign_type":    "RSA",
	}
	sig, err := SignYifutRSA(params, cfg.MerchantPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("sign query: %w", err)
	}
	params["sign"] = sig

	body, err := postFormToYifut(ctx, cfg.Gateway+"/api/pay/query", params)
	if err != nil {
		return nil, fmt.Errorf("yifut query: %w", err)
	}
	var resp YifutOrderQuery
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode query: %w (body=%s)", err, truncate(string(body), 256))
	}
	if resp.Code != 0 {
		return &resp, fmt.Errorf("yifut query error code=%d msg=%s", resp.Code, resp.Msg)
	}
	// 强制响应验签：防 MITM 伪造订单状态（status=1=已支付）
	respParams := map[string]string{
		"code":         strconv.Itoa(resp.Code),
		"msg":          resp.Msg,
		"trade_no":     resp.TradeNo,
		"out_trade_no": resp.OutTradeNo,
		"api_trade_no": resp.ApiTradeNo,
		"type":         resp.Type,
		"pid":          strconv.Itoa(resp.PID),
		"addtime":      resp.AddTime,
		"endtime":      resp.EndTime,
		"name":         resp.Name,
		"money":        resp.Money,
		"refundmoney":  resp.RefundMoney,
		"status":       strconv.Itoa(resp.Status),
		"param":        resp.Param,
		"buyer":        resp.Buyer,
		"clientip":     resp.ClientIP,
		"timestamp":    resp.Timestamp,
		"sign_type":    resp.SignType,
		"sign":         resp.Sign,
	}
	if !VerifyYifutRSA(respParams, cfg.PlatformPublicKey) {
		return nil, fmt.Errorf("yifut query response signature invalid")
	}
	// fix MAJOR M-B3（codex 第二十一轮）：query 响应也校验 timestamp（防重放，与 notify 一致 ±300s）
	if err := verifyYifutResponseTimestamp(resp.Timestamp); err != nil {
		return nil, fmt.Errorf("yifut query resp: %w", err)
	}
	return &resp, nil
}

// verifyYifutResponseTimestamp 校验易付通响应中的 timestamp 字段是否在合理时间窗内（±300s）。
//
// fix MAJOR M-B3（codex 第二十一轮）：原仅 notify/return 走 ±300s 窗口；
// create/query 响应缺这层防护，理论上中间人可重放历史合法响应骗后端 / 让 admin 看到
// 已过期订单状态。统一所有响应路径都过这道。
//
// 输入：服务端响应里的 timestamp 字符串（unix 秒）
// 返回：err 表示校验失败（缺失 / 格式非法 / 漂移过大）
func verifyYifutResponseTimestamp(ts string) error {
	if strings.TrimSpace(ts) == "" {
		return fmt.Errorf("response timestamp missing (replay protection failed)")
	}
	tsInt, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return fmt.Errorf("response timestamp not int: %w", err)
	}
	now := time.Now().Unix()
	const skewSec = int64(300)
	delta := now - tsInt
	if delta < -skewSec || delta > skewSec {
		return fmt.Errorf("response timestamp drift too large: server=%d local=%d delta=%ds (max %ds)", tsInt, now, delta, skewSec)
	}
	return nil
}

// 注（第十七轮）：原 RefundYifutOrder + YifutRefundResponse 实现 V2 退款 API 调用。
// 现在平台改为**手动退款工作流**：admin 在易付通后台直接退款，平台只标记订单状态 +
// 扣回 quota + 写账单。攻击面缩小一半（删除 RSA 退款签名 / 响应验签 / outRefundNo 绑定 /
// timestamp 漂移检查 / MITM 防御等约 100 行），账面一致性由 admin 双步骤工作流保证。
// 该函数已无调用方，删除。如未来要恢复自动退款，从 git 历史可恢复。

// ─── HTTP 底层 ─────────────────────────────────────────────────
//
// fix MAJOR M-B2（codex 第二十一轮）：易付通 HTTP client 原本用默认 Transport，
// 默认 Dialer 解析任何 IP 都会连。攻击者控制 SysConfig.yifut_gateway 或 DNS 劫持
// （ISP / 路由器 / 内网 DNS 服务器）可以让 yifut 域名解析到：
//   - 127.0.0.1（loopback）→ 攻击者本机服务
//   - 169.254.169.254（云元数据，AWS/GCP/阿里云）→ 偷云凭证
//   - 192.168.x.x / 10.x.x.x（内网）→ 攻击内网服务
//
// 自定义 SafeDialer：每次 DNS 解析后检查 IP 是否在私有/保留段，是则拒绝连接。
// 通过 Transport.DialContext 替换。每次连接建立都过这道，DNS rebinding 也能拦。
var yifutHTTPClient = &http.Client{
	Timeout: 15 * time.Second,
	Transport: &http.Transport{
		// 自定义 DialContext 注入 SSRF 检查
		DialContext: ssrfSafeDialContext,
		// 关键：设置 DNS 解析后不缓存（默认 Transport 会缓存空闲连接）
		// 让 DNS rebinding 攻击的"先返回真 IP，下一次返回 127.0.0.1"模式被严格检查
		DisableKeepAlives: false, // 保留连接复用，但每次新连接都过 SafeDialer
		MaxIdleConns:      10,
		IdleConnTimeout:   90 * time.Second,
	},
}

// ssrfSafeDialContext 拦截每次 TCP 拨号：解析 host → 检查 IP 是否安全 → 是才拨号。
// 拒绝所有 RFC1918 / 链路本地 / 回环 / 元数据 / 多播段。
//
// 注意：此函数是给易付通 HTTP client 用的，公网 API 应解析到公网 IP。
// 任何 yifut 域名突然解析到内网，都说明配置错误或 DNS 劫持，应该报警拒绝。
func ssrfSafeDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("ssrf-safe: invalid addr %q: %w", addr, err)
	}
	// 解析所有 IP（IPv4 + IPv6）
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("ssrf-safe: dns lookup %s: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("ssrf-safe: no IP resolved for %s", host)
	}
	// 任一 IP 命中黑名单都拒绝（防 DNS round-robin 混入内网 IP）
	for _, ipa := range ips {
		if isUnsafeIP(ipa.IP) {
			// 给 admin 一条可操作的线索：常见误报是本机走 Clash TUN / WARP / V2Ray
			// 之类的代理，DNS 被劫持到 198.18.0.0/15 / 100.64.0.0/10 等代理 egress
			// 段。这种情况下 admin 可以打开 yifut_allow_egress_proxy_ranges 开关。
			addr, _ := netip.AddrFromSlice(ipa.IP)
			hint := "private/loopback/link-local/metadata"
			for _, p := range yifutProxyEgressPrefixes {
				if p.Contains(addr.Unmap()) {
					hint = "proxy egress range (CGNAT/benchmark/IPv6 transition) — if you intentionally route through Clash/WARP/VPN, enable yifut_allow_egress_proxy_ranges in admin finance settings"
					break
				}
			}
			return nil, fmt.Errorf("ssrf-safe: refused unsafe IP %s for host %s (%s)", ipa.IP, host, hint)
		}
	}
	// 所有 IP 都安全：对预校验地址做交错拨号，避免固定第一条导致 IPv4/IPv6 失败拖慢。
	return dialPrevalidatedAddrs(ctx, network, port, ips, 10*time.Second)
}

// isUnsafeIP 返回 true 表示该 IP 不应被 yifut HTTP client 连接。
// 覆盖：私网（RFC1918）、链路本地、回环、未指定、元数据、多播、组播等。
func isUnsafeIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	if ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	return isUnsafeYifutIP(addr)
}

// postFormToYifut 把 params map 序列化为 application/x-www-form-urlencoded POST 出去。
// 注意：跳过空值（与签名规则一致）。
func postFormToYifut(ctx context.Context, endpoint string, params map[string]string) ([]byte, error) {
	form := url.Values{}
	for k, v := range params {
		if v == "" {
			continue
		}
		form.Set(k, v)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := yifutHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return body, fmt.Errorf("http status %d", resp.StatusCode)
	}
	return body, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(" + strconv.Itoa(len(s)-n) + " more)"
}
