// Package controller / payment_webhook.go
//
// 通用支付 webhook 处理框架。Phase W-3-P3（2026-05-21）。
//
// 责任分工：
//   - PaymentProvider.ParseAndVerifyWebhook(input) → event   ：验签 + 解析（无 DB）
//   - ProcessPaymentWebhook(c, providerKey)                  ：本文件，IP/receipt/order/amount/tx
//
// 调用方流程：
//   1. 路由层用 ProcessPaymentWebhook(c, "yifut") / (c, "epusdt") 接 GET / POST 回调
//   2. 本函数从 c 抽出 PaymentWebhookInput → provider 验签 → nonce 去重 → 查订单 →
//      金额比对 → 单事务入账 → 缓存失效 + 通知 → ack
//
// 与原 inline YifutNotify 的等价性：
//   - 顺序：IsConfigured → IP allowlist → 验签 → pid → timestamp → nonce → 金额 → 事务
//   - 错误码：与 inline 完全一致（403 / 400 / 404 / 500 / 200 "success"）
//   - receipt 写入：与 inline 一致（accepted 全过、rejected 仅 pid/timestamp）
package controller

import (
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"time"

	"daof-cpa/database"
	"daof-cpa/proxy"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// ProcessPaymentWebhook 是所有 PaymentProvider 共享的回调处理通用层。
//
// 路由层调 ProcessPaymentWebhook(c, "yifut") / ProcessPaymentWebhook(c, "epusdt")，
// 本函数按 provider key 取 adapter 完成全套验签 / 入账 / 通知流程。
//
// HTTP 响应约定（与原 YifutNotify 兼容）：
//   - 成功入账 / nonce 重放 / 非终态：200 "success"
//   - provider 未注册：404
//   - configured 失败：503
//   - 验签 / pid / timestamp 失败：403
//   - 金额不一致 / 订单不存在：400 / 404
//   - DB 故障：500（让网关重试）
func ProcessPaymentWebhook(c *fiber.Ctx, providerKey string) error {
	provider, ok := GetPaymentProvider(providerKey)
	if !ok {
		log.Printf("[WEBHOOK-%s] provider not registered", providerKey)
		return c.Status(404).SendString("provider_unknown")
	}

	input := buildPaymentWebhookInput(c)
	logKey := input.QueryParams["out_trade_no"]
	if logKey == "" {
		logKey = "<empty>"
	}

	// W-3 review M-1 修复：body size 硬上限（防 DoS / OOM）。
	// 合法 webhook 实际 < 2 KB（JSON），超 64 KB 几乎肯定是恶意。
	if len(input.Body) > MaxWebhookBodyBytes {
		log.Printf("[WEBHOOK-%s] body too large size=%d ip=%s", providerKey, len(input.Body), input.RemoteIP)
		return c.Status(413).SendString("payload_too_large")
	}

	// W-3 review M-2/M-7 修复：IP allowlist 走 optional IPAllowlistedProvider interface
	// 替代原 hardcoded `if providerKey == "yifut"` switch。新 provider 实现该方法即生效。
	if ipProvider, ok := provider.(IPAllowlistedProvider); ok {
		cidrs := strings.TrimSpace(ipProvider.AllowedRemoteIPCIDRs())
		if cidrs != "" && !isRemoteIPInCIDRs(input.RemoteIP, cidrs) {
			log.Printf("[WEBHOOK-%s] IP not allowed out_trade_no=%s ip=%s", providerKey, logKey, input.RemoteIP)
			return c.Status(403).SendString("ip_not_allowed")
		}
	}

	// provider 验签 + 解析
	event, err := provider.ParseAndVerifyWebhook(input)
	if err != nil {
		return handleWebhookError(c, providerKey, logKey, input, err)
	}

	// 拿到 event 后补充 logKey（POST body provider 的 out_trade_no 在 body 不在 query）
	if logKey == "<empty>" && event.OutTradeNo != "" {
		logKey = event.OutTradeNo
	}

	// nonce 防重放（在 (provider, nonce) 联合 unique 上）
	//
	// W-3 review C-1 修复（2026-05-21）：用 event.Nonce（provider 已计算）而非 input.QueryParams
	// （POST body provider 拿不到字段，原实现会让 epusdt nonce 永远是 "epusdt::no_sign"，第一笔
	// 之后全部 silent ack 不入账）。
	if duplicate, rerr := recordWebhookReceiptForEvent(providerKey, event, input.RemoteIP); rerr != nil {
		log.Printf("[WEBHOOK-%s] receipt insert failed out_trade_no=%s: %v", providerKey, logKey, rerr)
		return c.Status(500).SendString("receipt_failed")
	} else if duplicate {
		log.Printf("[WEBHOOK-%s] duplicate (nonce already used) out_trade_no=%s ip=%s", providerKey, logKey, input.RemoteIP)
		return c.SendString("success")
	}

	// 非终态：ack 防网关重试（不入账）
	if event.Kind != WebhookEventPaid {
		log.Printf("[WEBHOOK-%s] non-terminal kind=%s out_trade_no=%s", providerKey, event.Kind, logKey)
		return c.SendString("success")
	}

	// 查订单
	var order database.TopupOrder
	if err := database.DB.Where("out_trade_no = ?", event.OutTradeNo).First(&order).Error; err != nil {
		log.Printf("[WEBHOOK-%s] order not found out_trade_no=%s", providerKey, event.OutTradeNo)
		return c.Status(404).SendString("order_not_found")
	}

	// 防跨 provider 重放：订单创建时锁定的 provider 必须等于本次回调来源
	// 例：攻击者用 epusdt 的合法 callback 投递到 /api/payment/notify/yifut → order.Provider="epusdt"
	// !=  "yifut" → 拒绝
	if order.Provider != providerKey {
		log.Printf("[WEBHOOK-%s] provider mismatch order.Provider=%s out_trade_no=%s ip=%s",
			providerKey, order.Provider, event.OutTradeNo, input.RemoteIP)
		return c.Status(403).SendString("provider_mismatch")
	}

	// 金额比对（按 AmountKind 分支：fen_cny 比 MoneyRMB；micro_usd 比 AmountUSD）
	if err := validateWebhookAmount(event, &order); err != nil {
		log.Printf("[WEBHOOK-%s] amount mismatch out_trade_no=%s: %v", providerKey, event.OutTradeNo, err)
		return c.Status(400).SendString("money_mismatch")
	}

	// 单事务入账
	if err := finalizePaidTopupTransaction(&order, event); err != nil {
		if errors.Is(err, errTopupDuplicate) {
			log.Printf("[WEBHOOK-%s] duplicate callback out_trade_no=%s (already processed)", providerKey, event.OutTradeNo)
			return c.SendString("success")
		}
		log.Printf("[WEBHOOK-%s] tx failed out_trade_no=%s: %v", providerKey, event.OutTradeNo, err)
		return c.Status(500).SendString("tx_failed")
	}

	// 缓存失效 + 通知（事务外，失败不回滚已入账）
	proxy.InvalidateUserSubscriptionCache(order.UserID)
	proxy.RefreshUserAuth(order.UserID)
	dispatchTopupSuccessNotification(&order, event.ExternalTradeNo)

	log.Printf("[WEBHOOK-%s] OK out_trade_no=%s user=%d rmb_fen=%d usd_micro=%d",
		providerKey, event.OutTradeNo, order.UserID, order.MoneyRMB, order.AmountUSD)
	return c.SendString("success")
}

// isRemoteIPInCIDRs 判断 remote IP 是否在 CSV CIDR 列表内。
// W-3 review M-2 引入：通用层 IP allowlist helper，替代 yifut-specific 实现。
// CIDR 配置非法时返回 false（fail-closed），但仅记日志不崩进程。
func isRemoteIPInCIDRs(remoteIP, csv string) bool {
	ip := net.ParseIP(strings.TrimSpace(remoteIP))
	if ip == nil {
		log.Printf("[WEBHOOK] cannot parse remote IP %q", remoteIP)
		return false
	}
	for _, raw := range strings.Split(csv, ",") {
		cidr := strings.TrimSpace(raw)
		if cidr == "" {
			continue
		}
		_, ipnet, err := net.ParseCIDR(cidr)
		if err != nil {
			log.Printf("[WEBHOOK] bad CIDR config %q: %v", cidr, err)
			return false
		}
		if ipnet.Contains(ip) {
			return true
		}
	}
	return false
}

// buildPaymentWebhookInput 从 fiber.Ctx 抽出 PaymentWebhookInput 快照。
// 让 provider.ParseAndVerifyWebhook 不依赖 fiber，便于单测。
func buildPaymentWebhookInput(c *fiber.Ctx) *PaymentWebhookInput {
	headers := map[string]string{}
	c.Request().Header.VisitAll(func(k, v []byte) {
		headers[strings.ToLower(string(k))] = string(v)
	})
	return &PaymentWebhookInput{
		Method:      string(c.Request().Header.Method()),
		Headers:     headers,
		QueryParams: collectQueryParams(c),
		Body:        c.Body(),
		RemoteIP:    c.IP(),
	}
}

// validateWebhookAmount 按 event.AmountKind 与 order 对账（严格相等，零容差）。
//
// fix CRITICAL（多模型审计第二十五轮）：金额比对必须用 int64 严格 ==，
// 不能引入任何 float 容差或近似匹配（攻击者可通过差小数分的金额绕过近似检查）。
func validateWebhookAmount(event *PaymentWebhookEvent, order *database.TopupOrder) error {
	switch event.AmountKind {
	case AmountKindFenCNY:
		// yifut: 回调声明的 fen 必须严格 == order.MoneyRMB
		if event.AmountRaw != order.MoneyRMB {
			return fmt.Errorf("fen mismatch: callback=%d local=%d", event.AmountRaw, order.MoneyRMB)
		}
	case AmountKindMicroUSD:
		// epusdt: 回调声明的 micro_usd 必须严格 == order.AmountUSD
		if event.AmountRaw != order.AmountUSD {
			return fmt.Errorf("micro_usd mismatch: callback=%d local=%d", event.AmountRaw, order.AmountUSD)
		}
	default:
		return fmt.Errorf("unknown amount kind %q", event.AmountKind)
	}
	return nil
}

// finalizePaidTopupTransaction 单事务入账：status CAS + quota+= + paid_quota+= + WriteBillingEntry。
//
// 原子性保证（与原 inline YifutNotify 一致）：
//   - status 'created'→'paid' 用条件 UPDATE，RowsAffected=0 表示已被其它回调入账 → errTopupDuplicate
//   - quota+= 与 status CAS 必须同事务，否则双加 / 钱扣额度没到账
//   - WriteBillingEntry 与上面同事务，保证账单流水原子可见
func finalizePaidTopupTransaction(order *database.TopupOrder, event *PaymentWebhookEvent) error {
	now := time.Now()

	// trade_no / api_trade_no 防御性截断（与原 inline 一致）
	tradeNo := event.ExternalTradeNo
	if len(tradeNo) > 128 {
		tradeNo = tradeNo[:128]
	}
	apiTradeNo := event.RawParams["api_trade_no"] // yifut-specific；epusdt 不会有此字段（空串无害）
	if len(apiTradeNo) > 128 {
		apiTradeNo = apiTradeNo[:128]
	}

	return database.DB.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&database.TopupOrder{}).
			Where("out_trade_no = ? AND status = ?", event.OutTradeNo, "created").
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
			return errTopupDuplicate
		}
		// 加额度（quota 充值只会增加，不需要 >=0 守卫）
		// paid_quota 记录"充值通道余额"用于消费返佣归因
		if err := tx.Model(&database.User{}).
			Where("id = ?", order.UserID).
			Updates(map[string]any{
				"quota":      gorm.Expr("quota + ?", order.AmountUSD),
				"paid_quota": gorm.Expr("paid_quota + ?", order.AmountUSD),
			}).Error; err != nil {
			return fmt.Errorf("add quota: %w", err)
		}
		var freshUser database.User
		if err := tx.Select("id, quota").First(&freshUser, order.UserID).Error; err != nil {
			return fmt.Errorf("fetch fresh quota: %w", err)
		}
		// W-3 review M-10 修复：用 event.CurrencyOriginal（provider 自填准确币种）
		// 替代 webhookCurrencyOriginal 一刀切映射。fallback 兜底防 provider 未填字段。
		currencyOriginal := event.CurrencyOriginal
		if currencyOriginal == "" {
			currencyOriginal = webhookCurrencyOriginal(event.AmountKind)
		}
		return database.WriteBillingEntry(tx, database.BillingEntryInput{
			UserID:           order.UserID,
			OccurredAt:       now,
			EntryType:        database.BillingTypeTopup,
			AmountUSD:        order.AmountUSD,
			BalanceAfterUSD:  freshUser.Quota,
			RelatedType:      "topup_order",
			RelatedID:        order.ID,
			Description:      buildTopupBillingDescription(order),
			CurrencyOriginal: currencyOriginal,
			AmountOriginal:   event.AmountRaw,
		})
	})
}

// buildTopupBillingDescription 按 provider 生成账单描述。
//
// 与原 YifutNotify 一致：yifut 用 "充值 ¥X.XX（alipay）"。
// epusdt 用 "充值 X.XX USDT"。
// 其它 provider 退化到通用格式。
func buildTopupBillingDescription(order *database.TopupOrder) string {
	switch order.Provider {
	case database.TopupProviderYifut:
		return fmt.Sprintf("充值 ¥%s（%s）", database.FormatFen(order.MoneyRMB), order.PayType)
	case database.TopupProviderEpusdt:
		return fmt.Sprintf("充值 %s USDT", database.FormatMicroUSD(order.AmountUSD))
	default:
		return fmt.Sprintf("充值 %s USD（%s）", database.FormatMicroUSD(order.AmountUSD), order.Provider)
	}
}

// webhookCurrencyOriginal 把 AmountKind 映射到 BillingEntry.CurrencyOriginal 字段值。
func webhookCurrencyOriginal(kind PaymentAmountKind) string {
	switch kind {
	case AmountKindFenCNY:
		return "CNY"
	case AmountKindMicroUSD:
		return "USDT" // 链上 token 名，与 BillingEntry 标识对齐
	default:
		return ""
	}
}

// dispatchTopupSuccessNotification 发"充值成功"站内通知（事务外，失败仅 log）。
// 与原 YifutNotify 行为一致。
func dispatchTopupSuccessNotification(order *database.TopupOrder, externalTradeNo string) {
	if externalTradeNo == "" {
		externalTradeNo = order.OutTradeNo
	}
	if len(externalTradeNo) > 128 {
		externalTradeNo = externalTradeNo[:128]
	}
	title := readSysConfigCached("notif_topup_title", "充值成功")
	bodyTpl := readSysConfigCached("notif_topup_body", "您充值的 ¥{amount_rmb} 已到账，等额 {amount_usd} USD 已加入余额。")
	body := strings.ReplaceAll(bodyTpl, "{amount_rmb}", database.FormatFen(order.MoneyRMB))
	body = strings.ReplaceAll(body, "{amount_usd}", database.FormatMicroUSD(order.AmountUSD))
	dedupKey := "topup:" + externalTradeNo
	proxy.Dispatch(order.UserID, "topup", "success", title, body,
		proxy.LinkTopup(), "查看", "topup", order.ID, &dedupKey)
}

// handleWebhookError 把 provider 返回的 ErrWebhook* sentinel 映射到 HTTP 响应。
// 同时按错误类型决定是否写 rejected receipt（pid/timestamp 失败写，签名失败不写）。
//
// 与原 YifutNotify 行为一致：
//   - 签名失败 → 403 sign_invalid（不写 receipt：nonce 可能伪造）
//   - pid 不一致 → 403 pid_mismatch（写 rejected_pid receipt 审计）
//   - timestamp 漂移 → 403 timestamp_invalid（写 rejected_timestamp receipt）
//   - malformed → 400 bad_payload（不写 receipt：根本没合法签名）
//   - not_configured → 503 not_configured
//   - 未知 → 500
func handleWebhookError(c *fiber.Ctx, providerKey, logKey string, input *PaymentWebhookInput, err error) error {
	switch {
	case errors.Is(err, ErrWebhookProviderNotConfigured):
		log.Printf("[WEBHOOK-%s] not configured out_trade_no=%s", providerKey, logKey)
		return c.Status(503).SendString("not_configured")

	case errors.Is(err, ErrWebhookSignatureInvalid):
		log.Printf("[WEBHOOK-%s] sign verify FAILED out_trade_no=%s ip=%s", providerKey, logKey, input.RemoteIP)
		return c.Status(403).SendString("sign_invalid")

	case errors.Is(err, ErrWebhookPIDMismatch):
		log.Printf("[WEBHOOK-%s] pid mismatch out_trade_no=%s ip=%s", providerKey, logKey, input.RemoteIP)
		recordWebhookReceipt(providerKey, input.QueryParams, logKey, input.RemoteIP, "rejected_pid", "pid mismatch")
		return c.Status(403).SendString("pid_mismatch")

	case errors.Is(err, ErrWebhookTimestampDrift):
		log.Printf("[WEBHOOK-%s] timestamp drift out_trade_no=%s ip=%s", providerKey, logKey, input.RemoteIP)
		recordWebhookReceipt(providerKey, input.QueryParams, logKey, input.RemoteIP, "rejected_timestamp", "timestamp drift > 300s")
		return c.Status(403).SendString("timestamp_invalid")

	case errors.Is(err, ErrWebhookMalformed):
		log.Printf("[WEBHOOK-%s] payload malformed out_trade_no=%s: %v", providerKey, logKey, err)
		return c.Status(400).SendString("bad_payload")

	default:
		log.Printf("[WEBHOOK-%s] internal error out_trade_no=%s: %v", providerKey, logKey, err)
		return c.Status(500).SendString("internal")
	}
}
