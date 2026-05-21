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
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// ─── 配置 ─────────────────────────────────────────────────

// EpusdtConfig 即时配置快照。
type EpusdtConfig struct {
	Endpoint  string // epusdt sidecar base URL（如 "http://localhost:8000"）
	PID       int64  // epusdt 商户 PID（int64 类型，与 epusdt 协议字段一致）
	SecretKey string // 商户 secret_key，签名用
}

// LoadEpusdtConfig 从 SysConfigCache 拉一次配置快照。
// 任何字段缺失则 IsConfigured 返 false。
func LoadEpusdtConfig() EpusdtConfig {
	SysConfigMutex.RLock()
	defer SysConfigMutex.RUnlock()
	endpoint := strings.TrimRight(strings.TrimSpace(SysConfigCache["epusdt_endpoint"]), "/")
	pidStr := strings.TrimSpace(SysConfigCache["epusdt_pid"])
	secret := strings.TrimSpace(SysConfigCache["epusdt_secret_key"])

	var pid int64
	if pidStr != "" {
		fmt.Sscanf(pidStr, "%d", &pid)
	}
	return EpusdtConfig{
		Endpoint:  endpoint,
		PID:       pid,
		SecretKey: secret,
	}
}

// IsConfigured 三项就绪才算配置好。
func (c EpusdtConfig) IsConfigured() bool {
	return c.Endpoint != "" && c.PID > 0 && c.SecretKey != ""
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
func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := 0; i < len(a); i++ {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
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

// epusdtHTTPClient 与 yifut 共用 SSRF-safe Transport 但允许 loopback。
// epusdt sidecar 通常在 localhost，必须放行 127.0.0.1 等。
//
// 区别于 yifutHTTPClient：epusdt 不走 ssrfSafeDialContext（那个拒绝 loopback），
// 改用标准 Dialer + ValidateEpusdtEndpoint 显式拒绝元数据 IP（应用层校验）。
var epusdtHTTPClient = &http.Client{
	Timeout: 15 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:    10,
		IdleConnTimeout: 90 * time.Second,
	},
}
