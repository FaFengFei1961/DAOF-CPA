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
	"log"
	"math"
	"strconv"
	"strings"
	"time"

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
// W-4-Manual（2026-05-21）：双模式分支。
//   - auto:   调 epusdt sidecar，全自动链上对账（原 W-3-P2 实现）
//   - manual: 本地生成订单 + 邮件通知 admin，admin 区块链浏览器验真后手工标记到账
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

	if req.AmountUSDMicro <= 0 {
		return nil, fmt.Errorf("%w: AmountUSDMicro must be > 0", ErrPaymentProviderInternal)
	}

	switch cfg.Mode {
	case proxy.EpusdtModeAuto:
		return p.createOrderAuto(ctx, cfg, req, token, network)
	case proxy.EpusdtModeManual:
		return p.createOrderManual(cfg, req, token, network)
	}
	return nil, fmt.Errorf("%w: unknown mode %q", ErrPaymentProviderInternal, cfg.Mode)
}

// createOrderAuto 走 epusdt sidecar 的全自动流程（原 W-3-P2 实现）。
func (p *EpusdtPaymentProvider) createOrderAuto(ctx context.Context, cfg proxy.EpusdtConfig, req *PaymentCreateOrderRequest, token, network string) (*PaymentCreateOrderResult, error) {
	usdtAmount := float64(req.AmountUSDMicro) / 1_000_000.0

	resp, err := proxy.CreateEpusdtOrder(ctx, cfg, proxy.EpusdtCreateOrderRequest{
		OrderID:   req.OutTradeNo,
		Amount:    usdtAmount,
		Token:     token,
		Network:   network,
		Currency:  "usd",
		NotifyURL: req.NotifyURL,
		Name:      req.ProductName,
	})
	if err != nil {
		if resp != nil && resp.TradeID == "" {
			return nil, fmt.Errorf("%w: %v", ErrPaymentGatewayReject, err)
		}
		return nil, fmt.Errorf("%w: %v", ErrPaymentUpstreamUnavailable, err)
	}

	// W-3 review H-6：marshal 错误显式返而非 silent
	payInfo, marshalErr := json.Marshal(map[string]any{
		"receive_address": resp.ReceiveAddress,
		"actual_amount":   resp.ActualAmount,
		"token":           resp.Token,
		"network":         network,
		"expire_at":       resp.ExpirationTime,
	})
	if marshalErr != nil {
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

// createOrderManual 走 manual 模式：本地生成订单 + 邮件通知 admin。
//
// 金额尾数策略：actual_amount_micro = AmountUSDMicro + (OrderID % 10000) * 100
// 即在 micro_usd 基础上加 0.0001 USDT 步长（与 epusdt sidecar 默认尾数一致）。
//
// **10000 订单环回限制**（Tier 3 M-10，2026-05-21）：
//   - 公测期单笔基础金额下，约 10000 个未决订单可同时存在不冲突
//   - 同一基础金额跨越 10000 倍数时尾数环回（订单 #1 和 #10001 的 actual_amount 相同）
//   - admin 仍能按 time + amount 双锚点定位订单，不丢钱但增加 admin 心智负担
//
// **扩展方案**（当真到达瓶颈）：
//   - 阈值上调：改 `OrderID % 100000 * 10`（10000 → 100000，步长 0.001 USDT；
//     代价：用户付款金额可读性下降 X.XXX → X.XXXX）
//   - 切 auto 模式：epusdt sidecar 用静态地址 + 唯一金额池，无此限制
//
// 订单过期：10 分钟（与 epusdt 默认 order_expiration_time 一致）。
//
// Tier 1 C-2 修复：manual 模式强依赖邮件通知，SMTP 未配齐时不应创建订单
// （否则用户付款但 admin 永不知情）。fail-closed 在订单创建前先验证 SMTP。
//
// Tier 2 L-2 修复：OrderID==0 防御（理论上 GORM auto-increment 不会返 0，
// 但守卫一下让万一出现时立即报错而不是给所有订单生成相同的尾数 0）。
func (p *EpusdtPaymentProvider) createOrderManual(cfg proxy.EpusdtConfig, req *PaymentCreateOrderRequest, token, network string) (*PaymentCreateOrderResult, error) {
	if req.OrderID == 0 {
		return nil, fmt.Errorf("%w: OrderID==0; DB insert may have failed silently", ErrPaymentProviderInternal)
	}

	address := cfg.ManualAddresses.AddressFor(network)
	if address == "" {
		return nil, fmt.Errorf("%w: admin has not configured %s address", ErrPaymentProviderNotConfigured, strings.ToUpper(network))
	}

	// C-2: SMTP 必须先配齐，否则 admin 收不到通知 → 用户付款也无人确认 → 钱丢
	if smtpCfg, smtpErr := proxy.LoadSMTPConfig(); smtpErr != nil || !smtpCfg.IsConfigured() {
		log.Printf("[EPUSDT-MANUAL] reject order: SMTP not configured (manual mode requires email notify) order_id=%d", req.OrderID)
		return nil, fmt.Errorf("%w: manual mode requires SMTP configured for admin notification", ErrPaymentProviderNotConfigured)
	}

	const amountStepMicro = int64(100) // 0.0001 USDT
	suffix := int64(req.OrderID%10000) * amountStepMicro
	actualMicro := req.AmountUSDMicro + suffix
	actualUSDT := float64(actualMicro) / 1_000_000.0

	const orderTTLSec = int64(10 * 60) // 10 分钟
	expireAt := time.Now().Unix() + orderTTLSec

	tradeID := fmt.Sprintf("manual-%s", req.OutTradeNo)

	payInfo, marshalErr := json.Marshal(map[string]any{
		"receive_address": address,
		"actual_amount":   actualUSDT,
		"token":           strings.ToUpper(token),
		"network":         network,
		"expire_at":       expireAt,
		"mode":            "manual", // 前端可据此显示"等待管理员确认"而非"等待链上确认"
	})
	if marshalErr != nil {
		return nil, fmt.Errorf("%w: marshal pay_info failed: %v", ErrPaymentProviderInternal, marshalErr)
	}

	// 异步发邮件给 admin（不阻塞用户下单；邮件失败仅 log）
	sendEpusdtManualAdminEmail(cfg.ManualAdminEmail, manualOrderEmailContext{
		OrderID:     req.OrderID,
		OutTradeNo:  req.OutTradeNo,
		UserID:      req.UserID,
		Network:     network,
		Token:       strings.ToUpper(token),
		Address:     address,
		AmountUSDT:  actualUSDT,
		ExpireAt:    expireAt,
		ProductName: req.ProductName,
	})

	// Tier 2 M-8 修复（2026-05-21）：补业务级别日志，让 admin 排查 "用户说付了但没到账"
	// 时能 grep [EPUSDT-MANUAL] 直接定位订单上下文。
	log.Printf("[EPUSDT-MANUAL] order created order_id=%d out_trade_no=%s user_id=%d network=%s token=%s amount=%.4f expire_at=%d",
		req.OrderID, req.OutTradeNo, req.UserID, network, strings.ToUpper(token), actualUSDT, expireAt)

	return &PaymentCreateOrderResult{
		ExternalTradeNo: tradeID,
		GatewayPayType:  "wallet_address",
		PayInfo:         string(payInfo),
		RawExtras: map[string]string{
			"network": network,
			"mode":    "manual",
		},
	}, nil
}

// manualOrderEmailContext 是 manual 模式订单通知邮件的渲染上下文。
type manualOrderEmailContext struct {
	OrderID     uint
	OutTradeNo  string
	UserID      uint
	Network     string
	Token       string
	Address     string
	AmountUSDT  float64
	ExpireAt    int64
	ProductName string
}

// sendEpusdtManualAdminEmail 异步发"USDT 新订单待确认"邮件给 admin。
// fire-and-forget：失败仅 log，不让订单创建失败（用户体验优先；admin 仍可从后台查）。
//
// Tier 3+4 L-3（2026-05-21）：拆出 epusdtChainLabel / epusdtManualEmailSubject /
// epusdtManualEmailBody 三个 pure helper，让此函数 < 50 行 + 三个 helper 可独立单测。
func sendEpusdtManualAdminEmail(adminEmail string, ctx manualOrderEmailContext) {
	if adminEmail == "" {
		return
	}
	enqueueErr := proxy.EnqueueEmail(proxy.EmailTask{
		To: adminEmail,
		Message: proxy.EmailMessage{
			To:       adminEmail,
			Subject:  epusdtManualEmailSubject(ctx.OrderID),
			TextBody: epusdtManualEmailBody(ctx),
		},
		Label:    "epusdt_manual_admin_notify",
		DedupKey: fmt.Sprintf("epusdt-manual:%d", ctx.OrderID),
	})
	if enqueueErr != nil {
		log.Printf("[EPUSDT-MANUAL] admin notify enqueue failed order=%d email=%s: %v",
			ctx.OrderID, maskEpusdtEmail(adminEmail), enqueueErr)
		proxy.IncEmailSendFailCount()
	}
}

// epusdtChainLabel 把 epusdt 协议的 network key（tron/ethereum/bsc/polygon）
// 转成 admin / 用户认识的链显示名（TRC20/ERC20/BEP20/Polygon）。
// 未知 network 返大写原值作 fallback。
func epusdtChainLabel(network string) string {
	switch network {
	case "tron":
		return "TRC20"
	case "ethereum":
		return "ERC20"
	case "bsc":
		return "BEP20"
	case "polygon":
		return "Polygon"
	}
	return strings.ToUpper(network)
}

// epusdtManualEmailSubject 渲染 admin 邮件主题。
//
// Tier 2 H-4：subject 不含金额 / 链类型 / token，避免移动端推送通知预览
// 弹窗直接暴露资金流向给旁观者（家人/同事看到屏幕预览）。详情仅在 body。
func epusdtManualEmailSubject(orderID uint) string {
	return fmt.Sprintf("[DAOF] 新充值订单待确认 #%d", orderID)
}

// epusdtManualEmailBody 渲染 admin 邮件正文。
// 含完整对账信息：订单号 / 用户 / 网络 / 地址 / 精确金额 / 过期时间。
func epusdtManualEmailBody(ctx manualOrderEmailContext) string {
	chainLabel := epusdtChainLabel(ctx.Network)
	expiresLocal := time.Unix(ctx.ExpireAt, 0).Format("2006-01-02 15:04:05")
	return fmt.Sprintf(`新的 USDT 充值订单需要您确认：

  订单号:     #%d
  商户单号:   %s
  用户 ID:    %d
  网络:       %s (%s)
  收款地址:   %s
  精确金额:   %.4f %s
  过期时间:   %s（本地时间）

请在区块链浏览器查到对应转账后，登录 DAOF admin 后台 → 充值订单管理 →
找到订单 #%d → 点"标记到账"。

注意金额必须严格匹配（精确到小数点后 4 位），用尾数区分不同订单。

—— DAOF Web3 USDT 通道（manual 模式）`,
		ctx.OrderID, ctx.OutTradeNo, ctx.UserID,
		chainLabel, ctx.Token, ctx.Address,
		ctx.AmountUSDT, ctx.Token,
		expiresLocal,
		ctx.OrderID,
	)
}

// maskEpusdtEmail 把邮箱中间打码，避免 log 泄漏 admin 邮件全文。
// "admin@example.com" → "a***n@example.com"
//
// Tier 2 M-3 修复（2026-05-21）：原 `at <= 1 → 返 raw email` 让 "a@x.com" / "@bad" /
// 空串 / 无 @ 字符串 silent 泄漏到 log。改为这些异常输入返常量占位符 "***@***"
// 而不是 raw 邮箱。
func maskEpusdtEmail(email string) string {
	at := strings.Index(email, "@")
	// 无 @ / @ 在最前 / 空串：返占位符（不暴露原文）
	if at <= 0 {
		return "***@***"
	}
	prefix := email[:at]
	if len(prefix) == 1 {
		// "a@x.com" → "a***@x.com"（让用户能从 log 认出自己的邮箱前缀字符，但不全暴露）
		return prefix + "***" + email[at:]
	}
	if len(prefix) == 2 {
		return prefix[:1] + "***" + email[at:]
	}
	return prefix[:1] + "***" + prefix[len(prefix)-1:] + email[at:]
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

	// W-4-Manual：methods 按 mode 决定来源
	//   - auto:   SysConfig epusdt_enabled_chains（admin 显式启用哪些链）
	//   - manual: 按 admin 配齐了哪些链地址自动过滤
	var enabledNetworks []string
	switch cfg.Mode {
	case proxy.EpusdtModeManual:
		enabledNetworks = cfg.ManualAddresses.EnabledNetworks()
	default: // auto
		enabledNetworks = splitCSV(readStringConfig("epusdt_enabled_chains", "tron"))
	}

	methods := []string{}
	for _, chain := range enabledNetworks {
		method := chainToMethod(chain)
		if method != "" {
			methods = append(methods, method)
		}
	}

	// W-3 review M-11：epusdt 用自己的 SysConfig key 命名空间，fallback 到 yifut_*
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

	// W-4-Manual：manual 模式下不应有 webhook 推送（admin 用 AdminMarkTopupPaid 入账）
	// 拒掉所有 manual 模式下进入 /api/payment/notify/epusdt 的请求，防错配 / 攻击者扫端口
	//
	// Tier 1 H-2 修复（2026-05-21）：改用 ErrWebhookUnsupported（405）替代 NotConfigured（503），
	// 让 admin 看到准确错误信号"manual 模式不接 webhook"而非"SysConfig 没配齐"。
	if cfg.Mode == proxy.EpusdtModeManual {
		return nil, fmt.Errorf("%w: manual mode", ErrWebhookUnsupported)
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

// 编译期 assertion（W-3 review L-4：interface 检查保留在生产文件，IPAllowlistedProvider 也加上）。
var (
	_ PaymentProvider       = (*EpusdtPaymentProvider)(nil)
	_ IPAllowlistedProvider = (*EpusdtPaymentProvider)(nil)
)

// 错误兜底（确保未使用的 sentinel 不被 linter 投诉）
var _ = errors.Is
