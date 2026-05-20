// Package controller / topup_webhook.go
//
// 易付通 webhook（异步 notify + 同步 return）+ webhook 安全 helper：
// IP 白名单、CIDR 配置校验、nonce 去重、签名摘要、receipt 落库。
//
// 流程：
//   - GET /api/payment/notify/yifut → YifutNotify（异步，加额度）
//   - GET /api/payment/return/yifut → YifutReturn（同步跳转，不加额度）
//
// 从 topup.go 抽出（Phase D-5，2026-05-19）：只是物理拆分，无语义改动。
package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"daof-cpa/database"
	"daof-cpa/proxy"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// errTopupDuplicate 哨兵：当 status 条件 UPDATE 命中 0 行时（已 paid/refunded/failed）
var errTopupDuplicate = errors.New("topup notify duplicate")

// notifyTimestampSkewSeconds 允许的回调时间戳与服务器时间最大漂移。
// 防重放攻击：超出此范围的回调直接拒绝。
const notifyTimestampSkewSeconds = 300

// checkYifutTimestamp 校验 V2 回调的 timestamp 字段：
//   - 缺失 → 拒绝（防签名集合不完整的伪造）
//   - 格式非法 → 拒绝
//   - 与服务器时间漂移 > 300s → 拒绝（防重放）
func checkYifutTimestamp(ts, logKey, logPrefix string) bool {
	if ts == "" {
		log.Printf("[%s] timestamp missing out_trade_no=%s", logPrefix, logKey)
		return false
	}
	tsInt, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		log.Printf("[%s] timestamp not int out_trade_no=%s value=%s", logPrefix, logKey, ts)
		return false
	}
	diff := time.Now().Unix() - tsInt
	if diff < 0 {
		diff = -diff
	}
	if diff > notifyTimestampSkewSeconds {
		log.Printf("[%s] timestamp skew too large out_trade_no=%s diff=%ds", logPrefix, logKey, diff)
		return false
	}
	return true
}

// YifutNotify GET /api/payment/notify/yifut
//
// 易付通异步通知。验签 + 金额校验 + 幂等加额度 + 通知用户。必须返回纯文本 "success"。
//
// 原子性保证：status 'created'→'paid' 与 quota+= 必须在同一事务内，
// 否则两步之间另一个并发回调到达可触发双加额度，或 quota 加失败回滚 status 又失败时
// 钱已扣但额度永远不到账。
func YifutNotify(c *fiber.Ctx) error {
	cfg := proxy.LoadYifutConfig()
	if !cfg.IsConfigured() {
		log.Printf("[TOPUP-NOTIFY] received but not configured")
		return c.Status(503).SendString("not_configured")
	}

	params := collectQueryParams(c)
	logKey := params["out_trade_no"]
	if logKey == "" {
		logKey = "<empty>"
	}

	remoteIP := c.IP()

	// fix CRITICAL Sprint4-M3：IP 白名单（最外层防御，比签名校验更早，节省密码学开销）
	// 默认 SysConfig yifut_notify_allowed_cidrs 为空 → 跳过 IP 检查；admin 配置后强制校验。
	if !checkYifutNotifyIPAllowed(remoteIP) {
		// 不写 webhook receipt — 这种情况未经签名验证，无可信 nonce
		log.Printf("[TOPUP-NOTIFY] IP not allowed out_trade_no=%s ip=%s", logKey, remoteIP)
		return c.Status(403).SendString("ip_not_allowed")
	}

	if !proxy.VerifyYifutRSA(params, cfg.PlatformPublicKey) {
		log.Printf("[TOPUP-NOTIFY] sign verify FAILED out_trade_no=%s ip=%s", logKey, remoteIP)
		return c.Status(403).SendString("sign_invalid")
	}

	// fix CRITICAL（codex 第四轮）：仅 RSA 验签不足以防跨商户重放——
	// 攻击者可在自己的易付通商户用相同 out_trade_no/money 创建订单付款，
	// 拿到平台合法签名的回调后投递到本站 notify。回调"签名有效"，但 pid 不属于我们。
	// 必须强制 params["pid"] == cfg.PID，缺失或不一致即拒绝。
	if cfg.PID == "" || params["pid"] != cfg.PID {
		log.Printf("[TOPUP-NOTIFY] pid mismatch out_trade_no=%s expected=%s got=%s ip=%s", logKey, cfg.PID, params["pid"], remoteIP)
		recordWebhookReceipt("yifut", params, logKey, remoteIP, "rejected_pid", "pid mismatch")
		return c.Status(403).SendString("pid_mismatch")
	}

	// 防重放：timestamp 必填，且与服务器时间漂移 ≤300 秒
	if !checkYifutTimestamp(params["timestamp"], logKey, "TOPUP-NOTIFY") {
		recordWebhookReceipt("yifut", params, logKey, remoteIP, "rejected_timestamp", "timestamp drift > 300s")
		return c.Status(403).SendString("timestamp_invalid")
	}

	// fix CRITICAL Sprint4-M3：nonce 防重放（最强防线）
	// 即使签名 + pid + timestamp 全过，同一回调（out_trade_no + sign 前 16 字符）也不能入账两次。
	// 这层在 TopupOrder.status CAS 之外，提供独立审计 + 跨订单重放兜底（万一上游 bug 复用 sign）。
	if duplicate, err := recordWebhookReceiptOnce("yifut", params, logKey, remoteIP); err != nil {
		// DB 故障 → 500 让易付通重试（事务尚未提交，状态未变）
		log.Printf("[TOPUP-NOTIFY] webhook receipt insert failed out_trade_no=%s: %v", logKey, err)
		return c.Status(500).SendString("receipt_failed")
	} else if duplicate {
		// 同一 (provider, nonce) 已存在 → 重放，直接 success 让易付通停止重试
		log.Printf("[TOPUP-NOTIFY] webhook duplicate (nonce already used) out_trade_no=%s ip=%s", logKey, remoteIP)
		return c.SendString("success")
	}

	if params["trade_status"] != "TRADE_SUCCESS" {
		log.Printf("[TOPUP-NOTIFY] non-success status=%s out_trade_no=%s", params["trade_status"], logKey)
		return c.SendString("success") // 仍返回 success，避免易付通持续重试
	}

	// 金额双校验：回调 money（RMB 元字符串）必须精确等于本地 money_rmb（fen 整数）。
	//
	// fix CRITICAL（多模型审计第二十五轮）：原实现 float 解析 + approxEqual(0.001) 容差，
	// float64 精度问题让攻击者可提交差 0.09 分的金额仍通过校验，等价绕过精确金额校验。
	// 改为：把回调字符串当作"元.分"格式，按整数 fen 解析（小数点切两段拼接 → int64），
	// 与本地 order.MoneyRMB（fen）做严格 == 比较，彻底消除浮点误差与人为容差。
	gotFen, ok := parseRMBStringToFen(params["money"])
	if !ok {
		log.Printf("[TOPUP-NOTIFY] bad money=%s out_trade_no=%s", params["money"], logKey)
		return c.Status(400).SendString("bad_money")
	}

	var order database.TopupOrder
	if err := database.DB.Where("out_trade_no = ?", logKey).First(&order).Error; err != nil {
		log.Printf("[TOPUP-NOTIFY] order not found out_trade_no=%s", logKey)
		return c.Status(404).SendString("order_not_found")
	}
	if gotFen != order.MoneyRMB {
		log.Printf("[TOPUP-NOTIFY] money mismatch out_trade_no=%s callback_fen=%d local_fen=%d",
			logKey, gotFen, order.MoneyRMB)
		return c.Status(400).SendString("money_mismatch")
	}

	// 原子事务：status 'created'→'paid' + quota+= 必须同时成功或同时回滚
	now := time.Now()
	// 防御性长度截断：网关签名已校验，但即便如此也不让外部串污染我们的 schema
	tradeNo := params["trade_no"]
	if len(tradeNo) > 128 {
		tradeNo = tradeNo[:128]
	}
	apiTradeNo := params["api_trade_no"]
	if len(apiTradeNo) > 128 {
		apiTradeNo = apiTradeNo[:128]
	}
	txErr := database.DB.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&database.TopupOrder{}).
			Where("out_trade_no = ? AND status = ?", logKey, "created").
			Updates(map[string]any{
				"status":       "paid",
				"trade_no":     tradeNo,
				"api_trade_no": apiTradeNo,
				"paid_at":      now,
			})
		if res.Error != nil {
			return fmt.Errorf("update order: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			// 已被另一个回调处理过（或订单状态非 created）
			return errTopupDuplicate
		}
		// 加额度（不限制 quota>=0；充值只会增加，永远成立）。
		// paid_quota 单独记录"充值通道来源的尚未消费余额"，用于拉新消费返佣归因。
		if err := tx.Model(&database.User{}).
			Where("id = ?", order.UserID).
			Updates(map[string]any{
				"quota":      gorm.Expr("quota + ?", order.AmountUSD),
				"paid_quota": gorm.Expr("paid_quota + ?", order.AmountUSD),
			}).Error; err != nil {
			return fmt.Errorf("add quota: %w", err)
		}
		// 账单流水：充值入账（与 quota+= 同事务，原子）
		// 重新读 user.quota 拿到 quota+= 后的精确余额作为账单快照
		var freshUser database.User
		if err := tx.Select("id, quota").First(&freshUser, order.UserID).Error; err != nil {
			return fmt.Errorf("fetch fresh quota: %w", err)
		}
		if err := database.WriteBillingEntry(tx, database.BillingEntryInput{
			UserID:           order.UserID,
			OccurredAt:       now,
			EntryType:        database.BillingTypeTopup,
			AmountUSD:        order.AmountUSD,
			BalanceAfterUSD:  freshUser.Quota,
			RelatedType:      "topup_order",
			RelatedID:        order.ID,
			Description:      fmt.Sprintf("充值 ¥%s（%s）", database.FormatFen(order.MoneyRMB), order.PayType),
			CurrencyOriginal: "CNY",
			AmountOriginal:   order.MoneyRMB, // fen
		}); err != nil {
			return fmt.Errorf("write billing entry: %w", err)
		}
		return nil
	})
	if errors.Is(txErr, errTopupDuplicate) {
		log.Printf("[TOPUP-NOTIFY] duplicate callback out_trade_no=%s (already processed)", logKey)
		return c.SendString("success") // 让易付通停止重试
	}
	if txErr != nil {
		log.Printf("[TOPUP-NOTIFY] tx failed out_trade_no=%s: %v", logKey, txErr)
		// 回 500 让易付通重试。事务已回滚，status 仍为 created，下次重试可正确推进
		return c.Status(500).SendString("tx_failed")
	}

	// 失效用户缓存（不影响订阅但避免关联缓存陈旧）
	proxy.InvalidateUserSubscriptionCache(order.UserID)
	// 关键：刷新 AuthCache 里的 user 实例，否则下次 /api/user/me 仍返回旧 quota
	proxy.RefreshUserAuth(order.UserID)

	// 充值通知（异步，dedupKey 兼容 trade_no 缺失场景）
	// tradeNo 在前面已截断为 ≤128 字节
	if tradeNo == "" {
		tradeNo = logKey // 兜底：用 out_trade_no
	}
	title := readSysConfigCached("notif_topup_title", "充值成功")
	bodyTpl := readSysConfigCached("notif_topup_body", "您充值的 ¥{amount_rmb} 已到账，等额 {amount_usd} USD 已加入余额。")
	body := strings.ReplaceAll(bodyTpl, "{amount_rmb}", database.FormatFen(order.MoneyRMB))
	body = strings.ReplaceAll(body, "{amount_usd}", database.FormatMicroUSD(order.AmountUSD))
	dedupKey := "topup:" + tradeNo
	proxy.Dispatch(order.UserID, "topup", "success", title, body,
		proxy.LinkTopup(), "查看", "topup", order.ID, &dedupKey)

	log.Printf("[TOPUP-NOTIFY] OK out_trade_no=%s user=%d rmb_fen=%d usd_micro=%d",
		logKey, order.UserID, order.MoneyRMB, order.AmountUSD)
	return c.SendString("success")
}

// YifutReturn GET /api/payment/return/yifut
//
// 同步跳转。验签后 redirect 到前端结果页（不在这里加额度——加额度只走 notify）。
//
// fix P2（codex review verify-1 + verify-final）：前端 hashRedirect.js 已删除，hash 路由
// 不再被改写到 path 路由。默认 return_path 改为 `/topup-result`，TopupResult.jsx 从
// window.location.search 解析 query。升级安装上 SysConfig 可能仍存旧值 `/#topup_result`
// （Seed 不覆盖已存在 key），运行时强制规范化到新路径。
//
// 路径形如：/topup-result?status=success&out_trade_no=xxx
func YifutReturn(c *fiber.Ctx) error {
	cfg := proxy.LoadYifutConfig()
	resultPath := readStringConfig("yifut_return_path", "/topup-result")
	// fix P2（codex review verify-final）：规范化历史 hash 路径。旧 SysConfig 可能仍是
	// "/#topup_result"，去掉 hashRedirect 后会落到 React Router 解析不到的 URL → silent 跳首页。
	if strings.HasPrefix(resultPath, "/#topup_result") || resultPath == "/#topup_result" {
		log.Printf("[TOPUP-RETURN] yifut_return_path legacy hash %q → normalized to /topup-result", resultPath)
		resultPath = "/topup-result"
	}
	// fix Minor（codex 第四轮）：yifut_return_path 是 SysConfig 项，被污染后可指向外部站点
	// 形成支付返回 open redirect。强制必须以单 `/` 开头、不能 `//`、不能含控制字符。
	if !isSafeReturnPath(resultPath) {
		log.Printf("[TOPUP-RETURN] yifut_return_path unsafe=%q, fallback to default", resultPath)
		resultPath = "/topup-result"
	}

	buildRedirect := func(status, outTradeNo string) string {
		q := url.Values{}
		q.Set("status", status)
		if outTradeNo != "" {
			q.Set("out_trade_no", outTradeNo)
		}
		// resultPath 形如 "/#topup_result"；query 直接附在 hash 之后
		sep := "?"
		if strings.Contains(resultPath, "?") {
			sep = "&"
		}
		return resultPath + sep + q.Encode()
	}

	if !cfg.IsConfigured() {
		return c.Redirect(buildRedirect("unavailable", ""))
	}

	params := collectQueryParams(c)
	outTradeNo := params["out_trade_no"]

	if !proxy.VerifyYifutRSA(params, cfg.PlatformPublicKey) {
		log.Printf("[TOPUP-RETURN] sign verify FAILED out_trade_no=%s", outTradeNo)
		return c.Redirect(buildRedirect("sign_invalid", outTradeNo))
	}
	// fix CRITICAL（codex 第四轮）：return 链接也必须验 pid，否则攻击者用自家
	// 商户的成功跳转 URL 给受害者（参数都有合法签名）会引导用户看到"成功"提示
	// 而本地订单仍是 created。
	if cfg.PID == "" || params["pid"] != cfg.PID {
		log.Printf("[TOPUP-RETURN] pid mismatch out_trade_no=%s expected=%s got=%s", outTradeNo, cfg.PID, params["pid"])
		return c.Redirect(buildRedirect("pid_mismatch", outTradeNo))
	}
	// 防重放：return 跳转也必须做 timestamp 校验（防签名 URL 被反复回放骗用户）
	if !checkYifutTimestamp(params["timestamp"], outTradeNo, "TOPUP-RETURN") {
		return c.Redirect(buildRedirect("timestamp_invalid", outTradeNo))
	}
	if params["trade_status"] == "TRADE_SUCCESS" {
		return c.Redirect(buildRedirect("success", outTradeNo))
	}
	return c.Redirect(buildRedirect("pending", outTradeNo))
}

// collectQueryParams 收集所有 query 参数到 map（用于验签）。
// 注意：易付通回调用 GET，参数都在 URL query。
//
// fix Minor（自审第十三轮，staticcheck SA1019）：fasthttp 的 VisitAll 已弃用，
// 改用 All() 返回 iter.Seq2，配合 Go 1.23+ range-over-func 语法。
func collectQueryParams(c *fiber.Ctx) map[string]string {
	out := map[string]string{}
	for k, v := range c.Context().QueryArgs().All() {
		out[string(k)] = string(v)
	}
	return out
}

// buildAbsoluteURL 构建供易付通服务器回调的绝对 URL。
//
// 强制要求 SysConfig.server_address 必须配置——绝不 fallback 到 c.Hostname()，
// 否则攻击者可伪造 Host 头让 notify_url 指向任意域名导致合法支付永远不到账。
//
// fix CRITICAL Sprint4-M3：默认强制 https://；admin 误配 http:// 会让 notify_url
// 在网关侧明文传输 + 易受 MitM 篡改。可通过 SysConfig server_address_require_https=false
// 显式关闭（仅开发期，生产部署应保持 true）。
func buildAbsoluteURL(path string) (string, error) {
	base := strings.TrimSpace(readStringConfig("server_address", ""))
	if base == "" {
		return "", fmt.Errorf("server_address SysConfig not configured")
	}
	if readBoolConfig("server_address_require_https", true) {
		lower := strings.ToLower(base)
		if !strings.HasPrefix(lower, "https://") {
			return "", fmt.Errorf("server_address must use https:// (got %q); to disable set SysConfig server_address_require_https=false", base)
		}
	}
	return strings.TrimRight(base, "/") + path, nil
}

// checkYifutNotifyIPAllowed 检查回调来源 IP 是否在 SysConfig yifut_notify_allowed_cidrs
// 配置的白名单内。空配置 = 允许所有 IP（仅依赖签名 + nonce 防重放）。
//
// fix CRITICAL Sprint4-M3：旧实现完全依赖签名作为唯一防线，签名密钥若泄漏 / 易付通侧
// 异常签发，无法靠业务侧防御。增加 IP CIDR 白名单作为最外层防线（生产建议配置）。
func checkYifutNotifyIPAllowed(remoteIP string) bool {
	csv := strings.TrimSpace(readStringConfig("yifut_notify_allowed_cidrs", ""))
	if csv == "" {
		return true // 未配置 → 默认允许，仅依赖下游签名/nonce 防御
	}
	ip := net.ParseIP(strings.TrimSpace(remoteIP))
	if ip == nil {
		log.Printf("[TOPUP-NOTIFY] cannot parse remote IP %q", remoteIP)
		return false
	}
	for _, raw := range strings.Split(csv, ",") {
		cidr := strings.TrimSpace(raw)
		if cidr == "" {
			continue
		}
		_, ipnet, err := net.ParseCIDR(cidr)
		if err != nil {
			log.Printf("[TOPUP-NOTIFY] bad CIDR config %q: %v", cidr, err)
			return false
		}
		if ipnet.Contains(ip) {
			return true
		}
	}
	return false
}

func validateYifutNotifyCIDRConfig() error {
	csv := strings.TrimSpace(readStringConfig("yifut_notify_allowed_cidrs", ""))
	if csv == "" {
		log.Printf("[TOPUP-NOTIFY] WARN yifut_notify_allowed_cidrs is empty; webhook IP allowlist is disabled")
		return nil
	}
	for _, raw := range strings.Split(csv, ",") {
		cidr := strings.TrimSpace(raw)
		if cidr == "" {
			continue
		}
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return fmt.Errorf("invalid CIDR %q: %w", cidr, err)
		}
	}
	return nil
}

// webhookNonce 由 out_trade_no + sign 前 16 字符拼接，保证：
//   - 同一订单同一签名只能入账一次（重放被 unique 约束拒绝）
//   - 不同订单的回调不互相冲突
//   - sign 缺失时退化为 out_trade_no:notimestamp:no_sign（仍可作 nonce）
func webhookNonce(provider string, params map[string]string) string {
	outTradeNo := strings.TrimSpace(params["out_trade_no"])
	sign := strings.TrimSpace(params["sign"])
	if len(sign) > 16 {
		sign = sign[:16]
	}
	if sign == "" {
		sign = "no_sign"
	}
	return provider + ":" + outTradeNo + ":" + sign
}

// signatureHash 把回调签名做 SHA-256 摘要，落库审计时不存原始签名以最小化敏感面。
func signatureHash(sign string) string {
	if sign == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(sign))
	return hex.EncodeToString(sum[:])
}

// recordWebhookReceiptOnce 写入 PaymentWebhookReceipt，唯一约束触发即返回 duplicate=true。
//
// 调用方应在 RSA + pid + timestamp 校验**全部通过后**调用，将处理结果记为 "accepted"。
// 重放（同 nonce 再次到达）由 DB unique 索引兜底，返回 (true, nil)。
func recordWebhookReceiptOnce(provider string, params map[string]string, outTradeNo, remoteIP string) (bool, error) {
	receipt := database.PaymentWebhookReceipt{
		Provider:      provider,
		Nonce:         webhookNonce(provider, params),
		SignatureHash: signatureHash(params["sign"]),
		OutTradeNo:    outTradeNo,
		RemoteIP:      remoteIP,
		Status:        "accepted",
		ReceivedAt:    time.Now(),
	}
	if err := database.DB.Create(&receipt).Error; err != nil {
		// unique 违反 = nonce 已存在 = 重放
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return true, nil
		}
		return false, err
	}
	return false, nil
}

// recordWebhookReceipt 记录"被拒绝"的回调（pid/timestamp 等失败），用于审计；
// 失败本身不返回 error 让 caller 继续走拒绝流程（即使 receipt 落库失败也不能让 callback 通过）。
func recordWebhookReceipt(provider string, params map[string]string, outTradeNo, remoteIP, status, reason string) {
	receipt := database.PaymentWebhookReceipt{
		Provider:      provider,
		Nonce:         webhookNonce(provider, params) + ":" + status, // 拒绝路径附加 status 避免与 accepted 互相冲突
		SignatureHash: signatureHash(params["sign"]),
		OutTradeNo:    outTradeNo,
		RemoteIP:      remoteIP,
		Status:        status,
		Reason:        reason,
		ReceivedAt:    time.Now(),
	}
	if err := database.DB.Create(&receipt).Error; err != nil {
		// 失败时仅 log，不影响主流程（caller 已经决定拒绝）
		log.Printf("[TOPUP-NOTIFY] webhook receipt log failed status=%s out_trade_no=%s: %v",
			status, outTradeNo, err)
	}
}
