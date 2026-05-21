// Package controller / payment_provider_epusdt.go
//
// epusdt（Easy Payment USDT）PaymentProvider 适配器。Phase W-3-P2（2026-05-21）。
//
// 实现 PaymentProvider interface。封装 epusdt sidecar 调用 + GMPAY 风格 webhook 解析。
//
// 设计要点：
//   - DAOF 不持有钱包私钥（epusdt sidecar 自己管理收款地址）
//   - 多链支持：TRC20 / ERC20 / BEP20 / Polygon（由 admin 在 epusdt 启用对应链）
//   - 1:1 USDT → USD 入账（用户充 10 USDT → DAOF 余额 +10 USD = 10_000_000 micro_usd）
//   - 平台承担 USDT 脱锚风险（W-3 后续可加 sysconfig usdt_min_peg_micros 急停开关）
//
// 协议参考：上游 epusdt src/util/sign/sign.go + src/mq/worker.go
//   - 签名：MD5(sorted(k=v) joined by "&" + secret_key)
//   - Webhook：POST JSON，body 含 signature 字段；DAOF 验签后回 "ok"
package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"daof-cpa/database"
	"daof-cpa/proxy"
)

// init 注册 epusdt provider 到全局 registry。
// 注：与 GitHub/Google adapter 一致——adapter 总是注册，IsConfigured 在每次调用时检查 SysConfig，
// 让 admin 配齐后下一次请求生效，不用重启。
func init() {
	RegisterPaymentProvider(NewEpusdtPaymentProvider())
}

// EpusdtPaymentProvider 是 epusdt sidecar 适配器。
//
// 状态：W-3-P2 实现完整接口；IsConfigured 需要 admin 在 SysConfig 配齐
// epusdt_endpoint / epusdt_pid / epusdt_secret_key 才返回 true。
type EpusdtPaymentProvider struct{}

// NewEpusdtPaymentProvider 构造 default adapter。无 struct 字段，运行时读 SysConfig。
func NewEpusdtPaymentProvider() *EpusdtPaymentProvider { return &EpusdtPaymentProvider{} }

// Key 返回 "epusdt"。
func (p *EpusdtPaymentProvider) Key() string { return database.TopupProviderEpusdt }

// IsConfigured 判定 admin 是否在 SysConfig 配齐 3 项必需：endpoint / pid / secret_key。
func (p *EpusdtPaymentProvider) IsConfigured() bool {
	return proxy.LoadEpusdtConfig().IsConfigured()
}

// CreateOrder 调 epusdt sidecar 创建收款订单。
//
// 入参约定（PaymentCreateOrderRequest.RawExtras）：
//   - "method"   string : "trc20-usdt" / "erc20-usdt" / "bep20-usdt" / "polygon-usdt"
//                         （由 controller 层从前端选择映射；adapter 内拆 token + network）
//
// 金额处理：
//   - req.AmountUSDMicro 是入账目标（micro_usd），按 1:1 换算成 USDT 数量（float64）
//   - epusdt 协议要求 amount 是 float64（如 10.5 表示 10.5 USDT）
//   - DAOF 内部用 int64 micro_usd 保精度，调外部时才转 float64
//
// 出参约定（PaymentCreateOrderResult）：
//   - ExternalTradeNo: epusdt 侧 trade_id
//   - GatewayPayType:  "wallet_address"（前端按此渲染收款地址 + 精确金额展示）
//   - PayInfo: JSON 包 receive_address / actual_amount / network / token / expire_at（前端解析渲染）
//   - RawExtras: 透传 payment_url（epusdt 收银台跳转 URL，可选）
func (p *EpusdtPaymentProvider) CreateOrder(ctx context.Context, req *PaymentCreateOrderRequest) (*PaymentCreateOrderResult, error) {
	if req == nil {
		return nil, fmt.Errorf("%w: nil request", ErrPaymentProviderInternal)
	}

	cfg := proxy.LoadEpusdtConfig()
	if !cfg.IsConfigured() {
		return nil, ErrPaymentProviderNotConfigured
	}

	method := req.RawExtras["method"]
	if method == "" {
		return nil, fmt.Errorf("%w: missing method in RawExtras", ErrPaymentProviderInternal)
	}
	token, network, ok := parseEpusdtMethod(method)
	if !ok {
		return nil, fmt.Errorf("%w: invalid method %q (expected trc20-usdt/erc20-usdt/bep20-usdt/polygon-usdt)", ErrPaymentProviderInternal, method)
	}

	// 1:1 换算：req.AmountUSDMicro (int64) → USDT (float64)
	// 注：epusdt 内部用 float64，DAOF 用 int64 micro_usd；这是精度边界，单次转换 acceptable
	// （micro_usd 6 位精度 ≈ USDT 6 位精度对齐）
	if req.AmountUSDMicro <= 0 {
		return nil, fmt.Errorf("%w: AmountUSDMicro must be > 0", ErrPaymentProviderInternal)
	}
	usdtAmount := float64(req.AmountUSDMicro) / 1_000_000.0

	resp, err := proxy.CreateEpusdtOrder(ctx, cfg, proxy.EpusdtCreateOrderRequest{
		OrderID:   req.OutTradeNo,
		Amount:    usdtAmount,
		Token:     token,
		Network:   network,
		Currency:  "usd", // 用户视角的法币口径
		NotifyURL: req.NotifyURL,
		Name:      req.ProductName,
	})
	if err != nil {
		// epusdt 网关侧业务码错误 vs 网络错误 —— resp.TradeID 是否拿到判断
		if resp != nil && resp.TradeID == "" {
			return nil, fmt.Errorf("%w: %v", ErrPaymentGatewayReject, err)
		}
		return nil, fmt.Errorf("%w: %v", ErrPaymentUpstreamUnavailable, err)
	}

	// 把"用户付钱所需信息"打包成 JSON 给前端
	// W-3 review H-6 修复（2026-05-21）：原 `_ := json.Marshal` 错误吞会让用户看到空白
	// 二维码 / 收款页（前端拿不到 receive_address）。改为显式 fail-closed。
	payInfo, marshalErr := json.Marshal(map[string]any{
		"receive_address": resp.ReceiveAddress,
		"actual_amount":   resp.ActualAmount, // 含 0.0001 尾数避免冲突
		"token":           resp.Token,
		"network":         network,
		"expire_at":       resp.ExpirationTime,
	})
	if marshalErr != nil {
		// 理论上 map[string]any (string/float64/string/string/int64) 不会 marshal 失败，
		// 但万一 epusdt 响应字段含非 UTF-8 / NaN / Inf 让 marshal 失败时，让用户看到错误
		// 而不是哑铃式空白页。订单已经在 epusdt 侧创建，需要 admin 手工对账。
		return nil, fmt.Errorf("%w: marshal pay_info failed: %v", ErrPaymentProviderInternal, marshalErr)
	}

	return &PaymentCreateOrderResult{
		ExternalTradeNo: resp.TradeID,
		GatewayPayType:  "wallet_address",
		PayInfo:         string(payInfo),
		RawExtras: map[string]string{
			"payment_url": resp.PaymentURL,
			"network":     network,
		},
	}, nil
}

// PublicOptions 给前端 /api/topup/options 用。
//
// methods 列表由 SysConfig `epusdt_enabled_chains` 决定（admin 控制，CSV 格式）：
//   - "tron,ethereum,bsc,polygon" 全开 → 前端 4 个按钮
//   - "tron" 只 TRC20 → 仅 TRC20 一个按钮（手续费低）
//
// presets / min / max 沿用 yifut 的 fen 字段约定，前端按 ExchangeRateRmbPerUsdMicros 换算
// 展示等额 USDT（DAOF 内部仍用 fen 配置 admin 心智模型一致）。
func (p *EpusdtPaymentProvider) PublicOptions() PaymentProviderPublicOptions {
	cfg := proxy.LoadEpusdtConfig()

	chains := readStringConfig("epusdt_enabled_chains", "tron")
	methods := []string{}
	for _, chain := range splitCSV(chains) {
		method := chainToMethod(chain)
		if method != "" {
			methods = append(methods, method)
		}
	}

	// W-3 review M-11 修复：epusdt 用自己的 SysConfig key 命名空间，fallback 到 yifut_*
	// 让 admin 心智模型清晰（"epusdt 跟 yifut 是平行 provider"），又不强制 admin 重新配。
	presets := []int64{}
	presetCSV := readStringConfig("epusdt_preset_amounts_fen", readStringConfig("yifut_preset_amounts_fen", "1000,3000,5000,10000,30000,50000"))
	for _, s := range splitCSV(presetCSV) {
		if v, ok := parsePositiveInt64Helper(s); ok {
			presets = append(presets, v)
		}
	}

	return PaymentProviderPublicOptions{
		Key:          database.TopupProviderEpusdt,
		Label:        "Web3 USDT",
		Configured:   cfg.IsConfigured(),
		Currency:     "USDT",
		PresetsFen:   presets,
		MinAmountFen: readInt64Config("epusdt_min_amount_fen", readInt64Config("yifut_min_amount_fen", 100)),
		MaxAmountFen: readInt64Config("epusdt_max_amount_fen", readInt64Config("yifut_max_amount_fen", 1_000_000)),
		Methods:      methods,
		IconKey:      "epusdt",
	}
}

// ParseAndVerifyWebhook 解析 + 验签 epusdt GMPAY 风格回调。
//
// 协议（与 epusdt src/mq/worker.go OrderNotifyResponse 一致）：
//   - HTTP: POST application/json
//   - Body 字段：pid / trade_id / order_id / amount / actual_amount / receive_address /
//                token / block_transaction_id / status / signature
//   - 签名：MD5(sorted(body fields except 'signature') joined by '&' + secret_key)
//   - 状态：status=2 表示已支付（StatusPaySuccess）；其它为非终态
//
// 响应（通用层会回）：plain text "ok"（epusdt isCallbackAck 接受 "ok" / "success"）
func (p *EpusdtPaymentProvider) ParseAndVerifyWebhook(input *PaymentWebhookInput) (*PaymentWebhookEvent, error) {
	if input == nil {
		return nil, fmt.Errorf("%w: nil input", ErrWebhookMalformed)
	}

	cfg := proxy.LoadEpusdtConfig()
	if !cfg.IsConfigured() {
		return nil, ErrWebhookProviderNotConfigured
	}

	// epusdt 回调是 POST application/json
	if len(input.Body) == 0 {
		return nil, fmt.Errorf("%w: empty body", ErrWebhookMalformed)
	}

	var payload map[string]any
	if err := json.Unmarshal(input.Body, &payload); err != nil {
		return nil, fmt.Errorf("%w: invalid JSON: %v", ErrWebhookMalformed, err)
	}

	// 取出 signature 字段做验签（验签输入要去掉 signature 字段）
	sigVal, sigOK := payload["signature"].(string)
	if !sigOK || sigVal == "" {
		return nil, fmt.Errorf("%w: signature missing or wrong type", ErrWebhookMalformed)
	}
	verifyPayload := make(map[string]any, len(payload))
	for k, v := range payload {
		if k == "signature" {
			continue
		}
		verifyPayload[k] = v
	}
	if !proxy.VerifyEpusdtSignature(verifyPayload, sigVal, cfg.SecretKey) {
		return nil, ErrWebhookSignatureInvalid
	}

	// pid 校验（防跨商户重放）
	// W-3 review H-2/H-7 修复（2026-05-21）：原 _ := payload["pid"].(float64) silent 失败：
	//   - 缺失：pidFloat=0 → != cfg.PID → ErrWebhookPIDMismatch（fail-closed 巧合）
	//   - 错类型（字符串 "1"）：同上
	//   - NaN/Inf：int64(NaN) 实现定义 → 巧合不等于 cfg.PID
	// 改为显式拒，错误码语义更准（malformed 而非 pid_mismatch）。
	pidRaw, pidExists := payload["pid"]
	if !pidExists {
		return nil, fmt.Errorf("%w: missing pid", ErrWebhookMalformed)
	}
	pidFloat, pidOK := pidRaw.(float64)
	if !pidOK {
		return nil, fmt.Errorf("%w: pid unexpected type %T", ErrWebhookMalformed, pidRaw)
	}
	if math.IsNaN(pidFloat) || math.IsInf(pidFloat, 0) {
		return nil, fmt.Errorf("%w: pid NaN/Inf", ErrWebhookMalformed)
	}
	if int64(pidFloat) != cfg.PID {
		return nil, ErrWebhookPIDMismatch
	}

	// 必填字段
	orderID, _ := payload["order_id"].(string)
	if strings.TrimSpace(orderID) == "" {
		return nil, fmt.Errorf("%w: missing order_id", ErrWebhookMalformed)
	}
	tradeID, _ := payload["trade_id"].(string)
	if strings.TrimSpace(tradeID) == "" {
		return nil, fmt.Errorf("%w: missing trade_id", ErrWebhookMalformed)
	}
	if len(tradeID) > 128 {
		tradeID = tradeID[:128]
	}

	// 金额解析（float64 → micro_usd int64，1:1 换算）
	amountFloat, ok := payload["amount"].(float64)
	if !ok || amountFloat <= 0 {
		return nil, fmt.Errorf("%w: invalid amount", ErrWebhookMalformed)
	}
	if math.IsNaN(amountFloat) || math.IsInf(amountFloat, 0) {
		return nil, fmt.Errorf("%w: amount NaN/Inf", ErrWebhookMalformed)
	}
	// W-3 review H-5 注释（2026-05-21）：float64 * 1e6 + 0.5 四舍五入到 micro。
	// 精度边界：epusdt 实际只用 ≤ 4 位小数（amount + 0.0001 尾数策略），不会超出 float64
	// 13 位有效数字的安全区。攻击者构造金额让 int64 截断比对结果偏 1 micro = $0.000001，
	// 几乎不可利用。验签已确保 amount 来自 epusdt 网关。
	amountMicroUSD := int64(amountFloat*1_000_000 + 0.5)

	// 状态映射：epusdt mdb.StatusPaySuccess = 2
	// W-3 review H-8 修复（2026-05-21）：原 _ := payload["status"].(float64) silent 失败：
	//   - 缺失：statusFloat=0 → fall through → WebhookEventNonTerminal + ack（burn nonce）
	//   - 错类型：同上
	// 改为显式拒，让 admin 看到错误而不是 silent 吞掉。
	statusRaw, statusExists := payload["status"]
	if !statusExists {
		return nil, fmt.Errorf("%w: missing status", ErrWebhookMalformed)
	}
	statusFloat, statusOK := statusRaw.(float64)
	if !statusOK {
		return nil, fmt.Errorf("%w: status unexpected type %T", ErrWebhookMalformed, statusRaw)
	}
	kind := WebhookEventNonTerminal
	switch int(statusFloat) {
	case 2: // StatusPaySuccess
		kind = WebhookEventPaid
	case 3: // StatusPayExpired
		kind = WebhookEventFailed
	}

	// nonce：(provider, trade_id) 联合 unique。
	// W-3 review H-4 修复（2026-05-21）：原 nonce 用 block_transaction_id（如有），
	// 但该字段 *不在签名集合内*（epusdt sign.Get 把 nil/空跳过；攻击者拦截合法回调后
	// 修改 block_tx_id 仍能通过签名验证 + 绕过 nonce 唯一性）。
	// 改回严格用 trade_id（epusdt 内部订单 ID，签名覆盖，攻击者改不了）。
	// 链上重组防御交给 epusdt sidecar 侧的确认数策略。
	nonce := database.TopupProviderEpusdt + ":" + orderID + ":" + tradeID

	// W-3 review M-10 修复：CurrencyOriginal 按实际 token（USDT / USDC）填，让审计字段准确。
	tokenStr, _ := payload["token"].(string)
	currencyOriginal := strings.ToUpper(strings.TrimSpace(tokenStr))
	if currencyOriginal == "" {
		currencyOriginal = "USDT" // fallback：epusdt 默认 USDT
	}

	// RawParams：把 JSON 字段都序列化成字符串供审计 / 通用层入账事务用
	rawParams := make(map[string]string, len(payload))
	for k, v := range payload {
		rawParams[k] = epusdtStringify(v)
	}

	return &PaymentWebhookEvent{
		Kind:             kind,
		OutTradeNo:       orderID,
		ExternalTradeNo:  tradeID,
		Nonce:            nonce,
		SignatureHash:    signatureHash(sigVal),
		AmountKind:       AmountKindMicroUSD,
		AmountRaw:        amountMicroUSD,
		CurrencyOriginal: currencyOriginal,
		RawParams:        rawParams,
	}, nil
}

// AllowedRemoteIPCIDRs 满足 IPAllowlistedProvider 可选接口（W-3 review M-2）。
// 默认仅放行 loopback（sidecar 同机部署最常见），admin 可改 SysConfig 加远程 IP。
// 空串明确"允许所有"——admin 需要显式 unset 才解除 IP 限制。
func (p *EpusdtPaymentProvider) AllowedRemoteIPCIDRs() string {
	return readStringConfig("epusdt_notify_allowed_cidrs", "127.0.0.1/32,::1/128")
}

// ─── helpers ────────────────────────────────────────────────

// parseEpusdtMethod 把前端 method 字符串拆成 (token, network)。
//   - "trc20-usdt"   → ("usdt", "tron")
//   - "erc20-usdt"   → ("usdt", "ethereum")
//   - "bep20-usdt"   → ("usdt", "bsc")
//   - "polygon-usdt" → ("usdt", "polygon")
func parseEpusdtMethod(method string) (token, network string, ok bool) {
	switch strings.ToLower(strings.TrimSpace(method)) {
	case "trc20-usdt":
		return "usdt", "tron", true
	case "erc20-usdt":
		return "usdt", "ethereum", true
	case "bep20-usdt":
		return "usdt", "bsc", true
	case "polygon-usdt":
		return "usdt", "polygon", true
	}
	return "", "", false
}

// chainToMethod 把 SysConfig epusdt_enabled_chains 的 chain key 转成前端 method 字符串。
func chainToMethod(chain string) string {
	switch strings.ToLower(strings.TrimSpace(chain)) {
	case "tron":
		return "trc20-usdt"
	case "ethereum":
		return "erc20-usdt"
	case "bsc":
		return "bep20-usdt"
	case "polygon":
		return "polygon-usdt"
	}
	return ""
}

// parsePositiveInt64Helper 解析正整数（>0）；失败返 0,false。
// W-3 review M-6 修复：原 fmt.Sscanf 会 silent 接受 trailing garbage（"123abc" 解析为 123）。
// 改用 strconv.ParseInt 严格拒非数字字符。
func parsePositiveInt64Helper(s string) (int64, bool) {
	v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || v <= 0 {
		return 0, false
	}
	return v, true
}

// epusdtStringify 序列化 JSON 解析后的 any 值为字符串（落 RawParams 审计用）。
func epusdtStringify(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		// JSON 数字默认 float64；用最短表示
		if x == float64(int64(x)) {
			return fmt.Sprintf("%d", int64(x))
		}
		return fmt.Sprintf("%g", x)
	case bool:
		if x {
			return "true"
		}
		return "false"
	case nil:
		return ""
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

// 编译期 assertion：EpusdtPaymentProvider 必须实现 PaymentProvider interface。
var _ PaymentProvider = (*EpusdtPaymentProvider)(nil)

// 错误兜底（确保未使用的 sentinel 不被 linter 投诉）
var _ = errors.Is
