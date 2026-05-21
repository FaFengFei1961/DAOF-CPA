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
// 易付通异步通知薄壳。
//
// W-3-P3（2026-05-21）重构：原 inline 180 行实现已搬到通用 ProcessPaymentWebhook
// + YifutPaymentProvider.ParseAndVerifyWebhook。本函数仅作路由别名，未来 epusdt 路由
// 注册 `/api/payment/notify/epusdt` 同样调 ProcessPaymentWebhook(c, "epusdt")。
//
// 与 epusdt 路由的对称性：路由层挂 fiber handler 直接调通用入口，所有 provider 共享
// 同一套 IP allowlist / nonce 去重 / 订单查询 / 金额比对 / 入账事务 / 通知逻辑。
func YifutNotify(c *fiber.Ctx) error {
	return ProcessPaymentWebhook(c, database.TopupProviderYifut)
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
