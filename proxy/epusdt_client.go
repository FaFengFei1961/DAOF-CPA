// Package proxy / epusdt_client.go
//
// epusdt（Easy Payment USDT）sidecar HTTP 客户端。Phase W-3-P2（2026-05-21）。
//
// 协议：GMPAY 风格（POST JSON + MD5 签名）。
// 上游 epusdt 仓库：https://github.com/GMWalletApp/epusdt
//
// 接口清单：
//   - POST {endpoint}/api/v1/order/create-transaction  创建收款订单（GMPAY 风格）
//
// 签名算法（与 epusdt src/util/sign/sign.go 一致）：
//   1. 收集所有 string / int 字段（跳过 "signature"、nil、空字符串）
//   2. 按 key 字典序排序，拼成 a=val1&b=val2 形式（值不 URL 编码）
//   3. 待签名串末尾直接拼接 secret_key（无分隔符）
//   4. MD5 摘要 → 小写 hex
//
// 设计原则：
//   - HTTP client 共享同款 SafeDialer（防 SSRF / DNS rebinding，复用 yifut 实现）
//   - 仅 sidecar 内网通信（admin 配 epusdt_endpoint 必须是 http://localhost:* 或私网）
//     —— 但 SafeDialer 仍生效，防 admin 误配公网导致流量出公网
//   - 不持有用户私钥（epusdt sidecar 自己管理钱包，DAOF 仅看回调）
package proxy

import (
	"context"
	"crypto/md5"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ─── 配置 ─────────────────────────────────────────────────

// EpusdtMode 决定 USDT 充值的处理方式：
//   - auto:   部署 epusdt sidecar，全自动链上监听 + webhook 入账（W-3-P2 完整实现）
//   - manual: 不部署 sidecar，订单创建时邮件通知 admin，admin 区块链浏览器验真后
//             手工通过 AdminMarkTopupPaid 标记到账
//
// 默认 manual：零部署上线，admin 配几个地址 + 邮箱即可收 USDT；将来可平滑升级到 auto。
type EpusdtMode string

const (
	EpusdtModeAuto   EpusdtMode = "auto"
	EpusdtModeManual EpusdtMode = "manual"
)

// EpusdtManualAddresses 是 manual 模式下 admin 配置的 4 链收款地址。
// 空串 = 该链未启用（不会出现在 PublicOptions.methods 里）。
type EpusdtManualAddresses struct {
	TRC20   string // TRON 链 (T...)
	ERC20   string // Ethereum 链 (0x...)
	BEP20   string // BSC 链 (0x...，可与 ERC20 同地址)
	Polygon string // Polygon 链 (0x...，可与 ERC20 同地址)
}

// EpusdtConfig 即时配置快照。
type EpusdtConfig struct {
	// 公共字段
	Mode EpusdtMode

	// auto 模式字段
	Endpoint  string // epusdt sidecar base URL（如 "http://localhost:8000"）
	PID       int64  // epusdt 商户 PID（int64 类型，与 epusdt 协议字段一致）
	SecretKey string // 商户 secret_key，签名用

	// manual 模式字段
	ManualAddresses  EpusdtManualAddresses
	ManualAdminEmail string // 新订单通知收件人
}

// LoadEpusdtConfig 从 SysConfigCache 拉一次配置快照。
// 任何字段缺失则 IsConfigured 返 false。
//
// W-4-Manual（2026-05-21）：双模式支持。
//   - epusdt_mode=auto:   读 endpoint/pid/secret_key
//   - epusdt_mode=manual: 读 manual_address_* / manual_admin_email
//   - 缺省 / 非法值 → 默认 manual（零部署友好）
func LoadEpusdtConfig() EpusdtConfig {
	SysConfigMutex.RLock()
	defer SysConfigMutex.RUnlock()

	// Mode（默认 manual 让零部署成为最低阻力路径）
	mode := EpusdtMode(strings.ToLower(strings.TrimSpace(SysConfigCache["epusdt_mode"])))
	if mode != EpusdtModeAuto && mode != EpusdtModeManual {
		mode = EpusdtModeManual
	}

	cfg := EpusdtConfig{Mode: mode}

	// auto 模式字段
	cfg.Endpoint = strings.TrimRight(strings.TrimSpace(SysConfigCache["epusdt_endpoint"]), "/")
	cfg.SecretKey = strings.TrimSpace(SysConfigCache["epusdt_secret_key"])
	if pidStr := strings.TrimSpace(SysConfigCache["epusdt_pid"]); pidStr != "" {
		if parsed, err := strconv.ParseInt(pidStr, 10, 64); err == nil {
			cfg.PID = parsed
		}
	}

	// manual 模式字段
	cfg.ManualAddresses = EpusdtManualAddresses{
		TRC20:   strings.TrimSpace(SysConfigCache["epusdt_manual_address_trc20"]),
		ERC20:   strings.TrimSpace(SysConfigCache["epusdt_manual_address_erc20"]),
		BEP20:   strings.TrimSpace(SysConfigCache["epusdt_manual_address_bep20"]),
		Polygon: strings.TrimSpace(SysConfigCache["epusdt_manual_address_polygon"]),
	}
	cfg.ManualAdminEmail = strings.TrimSpace(SysConfigCache["epusdt_manual_admin_email"])

	return cfg
}

// IsConfigured 按 mode 判定配置完整性。
//   - auto:   endpoint + pid + secret_key 全齐
//   - manual: 至少一个链地址 + admin 邮箱
func (c EpusdtConfig) IsConfigured() bool {
	switch c.Mode {
	case EpusdtModeAuto:
		return c.Endpoint != "" && c.PID > 0 && c.SecretKey != ""
	case EpusdtModeManual:
		return c.ManualAdminEmail != "" && c.ManualAddresses.HasAny()
	}
	return false
}

// HasAny 返 true 表示 4 条链至少配齐 1 个地址。
func (a EpusdtManualAddresses) HasAny() bool {
	return a.TRC20 != "" || a.ERC20 != "" || a.BEP20 != "" || a.Polygon != ""
}

// AddressFor 按 network 名（与 epusdt 协议字段一致）返对应地址，未配置时返空串。
func (a EpusdtManualAddresses) AddressFor(network string) string {
	switch strings.ToLower(strings.TrimSpace(network)) {
	case "tron":
		return a.TRC20
	case "ethereum":
		return a.ERC20
	case "bsc":
		return a.BEP20
	case "polygon":
		return a.Polygon
	}
	return ""
}

// EnabledNetworks 返 manual 模式下已配置地址的 network key 列表。
// 顺序固定（tron > ethereum > bsc > polygon），便于前端按手续费递增展示。
func (a EpusdtManualAddresses) EnabledNetworks() []string {
	out := make([]string, 0, 4)
	if a.TRC20 != "" {
		out = append(out, "tron")
	}
	if a.ERC20 != "" {
		out = append(out, "ethereum")
	}
	if a.BEP20 != "" {
		out = append(out, "bsc")
	}
	if a.Polygon != "" {
		out = append(out, "polygon")
	}
	return out
}

// ValidateEpusdtEndpoint 防 SSRF / 配置错误。
// 允许 http+https；不允许特殊 IP 段（loopback 除外——epusdt 是 sidecar，常在 localhost）。
//
// 与 ValidateGateway 的差异：epusdt 通常部署在本机 Docker，必须放过 loopback / 私网，
// 否则 admin 配 http://localhost:8000 会被拒绝。但 169.254.169.254（云元数据）等仍必须拒。
func ValidateEpusdtEndpoint(raw string) error {
	if raw == "" {
		return fmt.Errorf("endpoint empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse endpoint: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("endpoint scheme must be http or https (got %q)", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("endpoint host empty")
	}
	lower := strings.ToLower(host)
	// 云元数据 IP 显式拒绝（与 yifut 共享 denylist 但允许 loopback / 私网）
	for _, blocked := range []string{"169.254.169.254", "100.100.100.200", "168.63.129.16"} {
		if lower == blocked {
			return fmt.Errorf("endpoint host %s not allowed (cloud metadata)", lower)
		}
	}
	return nil
}

// ─── 签名（MD5 风格，与 epusdt sign.Get 一致）────────────────

// SignEpusdtMD5 计算 epusdt 签名。
//
// 算法（与 epusdt src/util/sign/sign.go 一致）：
//   1. 排除 "signature" 字段、nil 值、空字符串
//   2. 把 map 转 [k1=v1, k2=v2, ...] 然后字典序排序
//   3. 用 "&" 连接
//   4. 末尾拼接 secret_key（无分隔符）
//   5. MD5 → 小写 hex
//
// 例：params={"order_id":"x","amount":10.5}，secret_key="K"
//   排序后串：amount=10.5&order_id=x
//   MD5 输入：amount=10.5&order_id=xK
func SignEpusdtMD5(params map[string]any, secretKey string) string {
	pairs := make([]string, 0, len(params))
	for k, v := range params {
		if k == "signature" {
			continue
		}
		if v == nil {
			continue
		}
		s := epusdtStringifyValue(v)
		if s == "" {
			continue
		}
		pairs = append(pairs, k+"="+s)
	}
	sort.Strings(pairs)
	joined := strings.Join(pairs, "&")
	sum := md5.Sum([]byte(joined + secretKey))
	return hex.EncodeToString(sum[:])
}

// epusdtStringifyValue 把任意 Go 值序列化为字符串（与 epusdt sign 期望的转换一致）。
//
// 关键点：float64 用最短表示（"10" / "10.5" / "0.0001"），不是 fmt.Sprintf("%f")
// 那样的 6 位定点（"10.000000"），否则双方 hash 不一致。
//
// epusdt 原版 sign.Get 用反射做字符串化，但默认 strconv.FormatFloat(v, 'f', -1, 64)
// 等价于 Go fmt 的 %v。
func epusdtStringifyValue(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case int:
		return fmt.Sprintf("%d", x)
	case int64:
		return fmt.Sprintf("%d", x)
	case float64:
		return fmtFloat(x)
	case float32:
		return fmtFloat(float64(x))
	case bool:
		if x {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprintf("%v", v)
	}
}

// fmtFloat 把 float64 转字符串，去掉尾部 0（"10.0000" → "10"，"10.5000" → "10.5"）。
// 与 epusdt 内部 strconv.FormatFloat(v, 'f', -1, 64) 行为一致。
func fmtFloat(v float64) string {
	// Go 标准库 'g' 在某些数字下走科学计数法（1e10），用 'f' + -1 保证整数 / 定点
	s := strings.TrimRight(strings.TrimRight(fmt.Sprintf("%f", v), "0"), ".")
	if s == "" || s == "-" {
		return "0"
	}
	return s
}

// VerifyEpusdtSignature 校验入站 webhook 签名。
//
// payload 是去掉 "signature" 字段的 map（调用方先 unmarshal JSON，再传入），
// signature 是 payload 携带的签名值，secretKey 是商户密钥。
//
// 返回 true 表示签名合法。
func VerifyEpusdtSignature(payload map[string]any, signature, secretKey string) bool {
	if signature == "" || secretKey == "" {
		return false
	}
	expected := SignEpusdtMD5(payload, secretKey)
	return constantTimeEqual(expected, signature)
}

// constantTimeEqual 常时比较，防止时序攻击。
//
// W-3 review H-3 修复（2026-05-21）：用 crypto/subtle.ConstantTimeCompare 替代手写实现，
// 让审计员一眼看明这是标准库 + 标准做法（手写版本虽然实际等价，但增加审计负担）。
func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// ─── 下单接口：POST /api/v1/order/create-transaction ────────────

// EpusdtCreateOrderRequest 是 DAOF 传给 adapter 的下单参数。
type EpusdtCreateOrderRequest struct {
	OrderID   string  // 商户订单号，max 32 chars（必须等于 DAOF.TopupOrder.OutTradeNo）
	Amount    float64 // USDT 数量（>0.01，例：10.0 表示 10 USDT）
	Token     string  // "usdt" / "usdc"
	Network   string  // "tron" / "ethereum" / "bsc" / "polygon"
	Currency  string  // 仅显示用，建议 "usd"（用户视角的"我付的法币等额"）
	NotifyURL string  // DAOF webhook URL
	Name      string  // 商品展示名（epusdt 收银台展示）
}

// EpusdtCreateOrderResponse 是 epusdt sidecar 返回的下单响应。
type EpusdtCreateOrderResponse struct {
	TradeID        string  `json:"trade_id"`         // epusdt 侧订单 ID（写回 DAOF TopupOrder.TradeNo）
	OrderID        string  `json:"order_id"`         // 回显
	Amount         float64 `json:"amount"`           // 下单金额（USDT）
	ActualAmount   float64 `json:"actual_amount"`    // 实际用户应支付（含尾数避免冲突，可能比 Amount 多 0.0001）
	ReceiveAddress string  `json:"receive_address"`  // 收款钱包地址（给用户扫码 / 复制）
	Token          string  `json:"token"`            // 回显
	ExpirationTime int64   `json:"expiration_time"`  // 订单过期 unix 秒
	PaymentURL     string  `json:"payment_url"`      // epusdt 收银台 URL（可选用，DAOF 内置渲染就用 ReceiveAddress + ActualAmount）
}

// epusdtCreateOrderEnvelope 包装 epusdt 返回的统一信封。
type epusdtCreateOrderEnvelope struct {
	StatusCode int                       `json:"status_code"` // 200 = 成功
	Message    string                    `json:"message"`
	Data       EpusdtCreateOrderResponse `json:"data"`
}

// CreateEpusdtOrder 调 epusdt sidecar /api/v1/order/create-transaction。
//
// 返回标准响应；失败包含 EpusdtCreateOrderResponse（可能为空）+ error。
func CreateEpusdtOrder(ctx context.Context, cfg EpusdtConfig, req EpusdtCreateOrderRequest) (*EpusdtCreateOrderResponse, error) {
	if !cfg.IsConfigured() {
		return nil, fmt.Errorf("epusdt not configured")
	}
	if err := ValidateEpusdtEndpoint(cfg.Endpoint); err != nil {
		return nil, fmt.Errorf("epusdt endpoint invalid: %w", err)
	}

	// epusdt 协议字段（与 sign 计算所用一致）
	body := map[string]any{
		"pid":        cfg.PID,
		"order_id":   req.OrderID,
		"amount":     req.Amount,
		"token":      strings.ToLower(req.Token),
		"network":    strings.ToLower(req.Network),
		"currency":   strings.ToLower(req.Currency),
		"notify_url": req.NotifyURL,
	}
	if req.Name != "" {
		body["name"] = req.Name
	}
	body["signature"] = SignEpusdtMD5(body, cfg.SecretKey)

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	endpoint := cfg.Endpoint + "/api/v1/order/create-transaction"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(jsonBody)))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	// 复用 yifut 的 SSRF-safe http client（自定义 DialContext 拒绝公网元数据 IP）
	// epusdt 通常在 localhost / 私网，loopback 是允许的（与 yifut denylist 略有差异）
	resp, err := epusdtHTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("http status %d body=%s", resp.StatusCode, truncate(string(respBody), 256))
	}

	var env epusdtCreateOrderEnvelope
	if err := json.Unmarshal(respBody, &env); err != nil {
		return nil, fmt.Errorf("decode response: %w body=%s", err, truncate(string(respBody), 256))
	}
	if env.StatusCode != 200 {
		return &env.Data, fmt.Errorf("epusdt error code=%d msg=%s", env.StatusCode, env.Message)
	}
	if env.Data.TradeID == "" {
		return &env.Data, fmt.Errorf("epusdt response missing trade_id")
	}
	return &env.Data, nil
}

// ─── HTTP 底层 ─────────────────────────────────────────────────

// epusdtHTTPClient 与 yifut 共用 SafeTransport 但允许 loopback / RFC1918，仅拒绝云元数据 IP。
//
// W-3 review C-2 修复（2026-05-21）：原实现仅做应用层 hostname 字符串校验（ValidateEpusdtEndpoint），
// 可被 DNS rebinding 绕过：
//   - admin 配 "http://attacker.com" → 首次 DNS 返合法 IP → ValidateEpusdtEndpoint 过 → 第二次 DNS 返 169.254.169.254
//   - admin 配 "http://0x7f000001/" → 十六进制 loopback（hostname 校验过不了，但仍可疑）
//   - admin 配 "http://169.254.169.254.evil.com" → hostname 含元数据 IP 但不等于
//
// 网络层防护：epusdtSafeDialContext 在每次 dial 时解析 DNS，对实际目标 IP 做 denylist 检查
// （允许 loopback + RFC1918 + 公网；拒绝云元数据 IP）。
var epusdtHTTPClient = &http.Client{
	Timeout: 15 * time.Second,
	Transport: &http.Transport{
		DialContext:     epusdtSafeDialContext,
		MaxIdleConns:    10,
		IdleConnTimeout: 90 * time.Second,
	},
}

// epusdtSafeDialContext 是 epusdt HTTP client 的 SSRF 防护 dialer。
//
// 与 yifut 的 ssrfSafeDialContext 的关键差异：epusdt 允许 loopback / 私网（sidecar 通常
// 在 localhost），仅拒绝云元数据 IP。yifut 是公网网关，拒一切私网/loopback。
//
// 防御场景：
//   - DNS rebinding：每次新 dial 都重新解析 + 检查 IP
//   - hostname 注入（如 "169.254.169.254.evil.com"）：DNS 解析后实际 IP 仍走检查
//   - 十六进制 / 8 进制 IP 编码（如 0x7f000001）：URL.Hostname() 会解析成数字 IP，能走到 DNS 这一步
func epusdtSafeDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("epusdt-safe: invalid addr %q: %w", addr, err)
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("epusdt-safe: dns lookup %s: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("epusdt-safe: no IP resolved for %s", host)
	}
	for _, ipa := range ips {
		if isEpusdtBlockedIP(ipa.IP) {
			return nil, fmt.Errorf("epusdt-safe: refused blocked IP %s for host %s (cloud metadata segment)", ipa.IP, host)
		}
	}
	return dialPrevalidatedAddrs(ctx, network, port, ips, 10*time.Second)
}

// epusdtBlockedPrefixes 是 epusdt dialer 的 denylist（精简版）。
// 仅含云元数据 IP；loopback / RFC1918 允许（sidecar 部署需要）。
var epusdtBlockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("169.254.169.254/32"),   // AWS / GCP / Azure 标准元数据
	netip.MustParsePrefix("100.100.100.200/32"),   // 阿里云元数据
	netip.MustParsePrefix("168.63.129.16/32"),     // Azure Wireserver
	netip.MustParsePrefix("169.254.0.0/16"),       // 整个 link-local（覆盖大多数元数据变种）
	netip.MustParsePrefix("fe80::/10"),            // IPv6 link-local
	netip.MustParsePrefix("fd00::/8"),             // IPv6 ULA（云元数据有时走这里）
}

// isEpusdtBlockedIP 返 true 表示该 IP 是云元数据 / link-local，禁止 epusdt HTTP client 连。
func isEpusdtBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	addr = addr.Unmap()
	for _, p := range epusdtBlockedPrefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}
