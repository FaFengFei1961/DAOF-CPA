// Package controller / topup.go
//
// 余额充值（易付通 V2 SHA256WithRSA 协议）。
//
// 流程：
//  1. 用户调 POST /api/topup/create → 后端建本地订单 + 调 V2 /api/pay/create
//     拿到 pay_type + pay_info → 返回前端
//  2. 用户在支付宝/微信完成支付 → 易付通 GET /api/payment/notify/yifut 异步回调
//     → 平台公钥验签 + 金额双校验 + timestamp 防重放 + 条件 UPDATE 加额度 + Dispatch + echo "success"
//  3. 用户被支付页面跳转回 /api/payment/return/yifut → 同样验签后 redirect 到前端结果页
//
// 安全要点：
//   - 回调路径必须放公网；其他充值接口经 UserGuard / AdminGuard
//   - 验签用 proxy.VerifyYifutRSA（平台公钥校验回调签名）
//   - 金额双校验：回调 money（字符串解析为 fen int64）== 本地 money_rmb（严格相等，零容差）
//   - timestamp 防重放：拒绝服务器时间漂移 ±300 秒之外的回调
//   - 幂等：条件 UPDATE 'status=created → paid' 保证只加一次额度
package controller

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"math/big"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"daof-cpa/database"
	"daof-cpa/proxy"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// 支持的支付方式（易付通 V2 type 字段）
var allowedPayTypes = map[string]bool{
	"alipay":    true,
	"wxpay":     true,
	"qqpay":     true,
	"bank":      true,
	"jdpay":     true,
	"paypal":    true,
	"douyinpay": true,
}

// ─── 公开：用户充值入口 ────────────────────────────────────────

// topupCreateRequest 充值下单请求。fix CRITICAL Sprint4-M3：amount 以 fen int64 上送，
// 杜绝 float64 进入金额计算链路。前端在提交前将"元"输入 × 100 取整为 fen。
type topupCreateRequest struct {
	AmountFen int64  `json:"amount_fen"` // RMB × 100，必须 > 0
	PayType   string `json:"pay_type"`   // alipay / wxpay 等
	Device    string `json:"device"`     // pc / mobile / wechat / alipay / jump（默认 pc）
}

// CreateTopup POST /api/topup/create
//
// 用户发起充值。建本地订单 + 调易付通 V2 下单接口拿支付信息。
func CreateTopup(c *fiber.Ctx) error {
	user, err := getCurrentUser(c)
	if err != nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}

	var req topupCreateRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_BAD_REQUEST"})
	}

	cfg := proxy.LoadYifutConfig()
	if !cfg.IsConfigured() {
		return c.Status(503).JSON(fiber.Map{"success": false, "message_code": "ERR_PAYMENT_UNAVAILABLE"})
	}

	// fen int64 不会有 NaN/Inf，仅需 > 0 校验
	if req.AmountFen <= 0 {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_AMOUNT_INVALID"})
	}
	// 金额范围（fen int64）
	minFen := readInt64Config("yifut_min_amount_fen", 100)       // 默认 ¥1.00
	maxFen := readInt64Config("yifut_max_amount_fen", 1_000_000) // 默认 ¥10,000.00
	if req.AmountFen < minFen || req.AmountFen > maxFen {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_AMOUNT_OUT_OF_RANGE",
			"min_fen":      minFen,
			"max_fen":      maxFen,
		})
	}

	// 支付方式校验：先看是否允许（白名单），再看 admin 是否启用
	if !allowedPayTypes[req.PayType] {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_PAY_TYPE_INVALID"})
	}
	enabledMethods := readStringConfig("yifut_enabled_methods", "alipay,wxpay")
	if !csvContains(enabledMethods, req.PayType) {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_PAY_TYPE_DISABLED"})
	}

	device := req.Device
	if device == "" {
		device = "pc"
	}

	// fix CRITICAL Sprint4-M3：用 big.Int 整数算术做 fen → micro_usd 转换，杜绝 float64。
	// 公式：usd_micro = fen × 1e10 / rate_rmb_per_usd_micros
	//   （rate 单位是 micro_usd 域：7.2 RMB/USD → 7_200_000）
	rateRmbPerUsdMicros := safeExchangeRateRmbPerUsdMicros()
	amountUSDMicro, ok := usdMicroFromFenAndRate(req.AmountFen, rateRmbPerUsdMicros)
	if !ok {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_AMOUNT"})
	}

	outTradeNo, err := generateOutTradeNo(user.ID)
	if err != nil {
		log.Printf("[TOPUP] generate out_trade_no failed user=%d: %v", user.ID, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_INTERNAL"})
	}
	moneyStr := proxy.FormatMoneyFen(req.AmountFen)
	productName := readStringConfig("yifut_product_name", "DAOF-CPA 余额充值")

	// notify/return 路径硬编码，绝不从 SysConfig 读（防 admin 误改导致回调指向任意路径）
	const notifyPath = "/api/payment/notify/yifut"
	const returnPath = "/api/payment/return/yifut"
	notifyURL, err := buildAbsoluteURL(notifyPath)
	if err != nil {
		log.Printf("[TOPUP] cannot build notify_url: %v", err)
		return c.Status(503).JSON(fiber.Map{"success": false, "message_code": "ERR_SERVER_ADDRESS_NOT_CONFIGURED"})
	}
	returnURL, _ := buildAbsoluteURL(returnPath) // err 与上面同源，已校验

	// 1. 先建本地订单（status=created）
	order := database.TopupOrder{
		OutTradeNo:                  outTradeNo,
		UserID:                      user.ID,
		PayType:                     req.PayType,
		Device:                      device,
		MoneyRMB:                    req.AmountFen,
		AmountUSD:                   amountUSDMicro,
		ExchangeRateRmbPerUsdMicros: rateRmbPerUsdMicros,
		Name:                        productName,
		ClientIP:                    c.IP(),
		Status:                      "created",
		CreatedAt:                   time.Now(),
	}
	if err := database.DB.Create(&order).Error; err != nil {
		log.Printf("[TOPUP] create local order failed user=%d: %v", user.ID, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_INSERT"})
	}

	// 2. 调易付通下单
	ctx, cancel := context.WithTimeout(c.Context(), 12*time.Second)
	defer cancel()
	resp, err := proxy.CreateYifutOrder(ctx, cfg, proxy.YifutCreateOrderRequest{
		OutTradeNo: outTradeNo,
		PayType:    req.PayType,
		NotifyURL:  notifyURL,
		ReturnURL:  returnURL,
		Name:       productName,
		Money:      moneyStr,
		ClientIP:   c.IP(),
		Device:     device,
	})
	if err != nil {
		log.Printf("[TOPUP] yifut create failed order=%s user=%d: %v", outTradeNo, user.ID, err)
		// 标记本地订单为失败（错误不下发，避免泄露网关内部信息）
		if updErr := database.DB.Model(&order).Updates(map[string]any{"status": "failed"}).Error; updErr != nil {
			log.Printf("[TOPUP] mark failed order=%s err=%v", outTradeNo, updErr)
		}
		return c.Status(502).JSON(fiber.Map{"success": false, "message_code": "ERR_GATEWAY_REJECT"})
	}

	// 3. 持久化网关返回
	// fix Major（自审第十三轮）：持久化失败前是仅日志返回 200，前端拿 QR 码扫码付款后
	// notify 回调虽然能用 callback 数据回写 trade_no 跑通主流程，但页面刷新 → 数据库读不到 trade_no
	// → 用户看到"订单异常"困惑。改 fail-closed：标 failed 后让用户重新下单，避免 UI 状态分裂。
	if err := database.DB.Model(&order).Updates(map[string]any{
		"trade_no":         resp.TradeNo,
		"gateway_pay_type": resp.PayType,
		"pay_info":         resp.PayInfo,
	}).Error; err != nil {
		log.Printf("[TOPUP] persist gateway response failed order=%s: %v — marking failed", outTradeNo, err)
		// 尽力回滚到 failed，避免悬挂 created 订单（用户付款无法对账）
		if rbErr := database.DB.Model(&order).Updates(map[string]any{"status": "failed"}).Error; rbErr != nil {
			log.Printf("[TOPUP] mark failed after persist error fallback also failed order=%s: %v (订单悬挂 created，需人工介入)", outTradeNo, rbErr)
		}
		return c.Status(500).JSON(fiber.Map{
			"success":      false,
			"message":      "支付订单创建失败，请重试或联系客服",
			"message_code": "ERR_PERSIST_GATEWAY_RESP",
		})
	}

	return c.JSON(fiber.Map{
		"success": true,
		"data": fiber.Map{
			"out_trade_no":     outTradeNo,
			"trade_no":         resp.TradeNo,
			"gateway_pay_type": resp.PayType,
			"pay_info":         resp.PayInfo,
			"money_rmb":        database.FenToRMB(order.MoneyRMB),
			"amount_usd":       database.MicroToUSD(order.AmountUSD),
		},
	})
}

// ─── 公开：易付通异步回调（不经 UserGuard，不可放 LanGuard） ─────────

// errTopupDuplicate 哨兵：当 status 条件 UPDATE 命中 0 行时（已 paid/refunded/failed）
var errTopupDuplicate = errors.New("topup notify duplicate")

// errAdminMarkRaced 哨兵：admin 手动标记退款时订单状态已被并发修改（如另一 admin 同时操作）
var errAdminMarkRaced = errors.New("topup order state changed during manual refund mark")

// errRefundAmountInvalid 哨兵：tx 内基于 freshOrder 重新计算后发现金额非法
// （0=全额时已无可退、显式值越界、汇率快照损坏等）。配合 errAdminMarkRaced 一同回 4xx 而非 500。
//
// fix MAJOR M1（codex 第二十轮）：原默认值/上限校验在 tx 外，并发部分退款时旧 RefundedAmountRMB
// 让两个 admin 都能通过校验，第二个进入 tx 才被 errAdminMarkRaced 拦截。改为 tx 内统一处理。
var errRefundAmountInvalid = errors.New("refund amount invalid in fresh tx")

// errReclaimBlocked 哨兵：reclaim_quota 守卫检测到用户仍有未退款订阅，事务内拒绝继续。
// fix CRITICAL NEW-C1（codex 第十八轮）：守卫挪入事务后，需要 sentinel 把订阅 ID 列表
// 带回 handler 层渲染响应。
//
// fix MEDIUM M19-4（codex 第十九轮）：警告——任何中间层若用 fmt.Errorf("xxx: %v", err) 来
// 包装这个 error，errors.As(&errReclaimBlocked{}) 都会失败。**必须用 %w 或直接返回原 err**，
// 否则 handler 层的 `if errors.As(txErr, &blocked)` 会拿不到 ids 列表，错把"被守卫拒绝"
// 当成"未知 DB 错误"，给用户一个 ERR_DB_*** 而不是真正的"还有未退款订阅"提示。
//
// 安全做法：在事务函数体内 return &errReclaimBlocked{ids:...} 直接返回，不再经过任何
// fmt.Errorf 包装层。GORM 的 Transaction 会原样向上传递 sentinel 给 caller。
type errReclaimBlocked struct {
	ids []uint
}

func (e *errReclaimBlocked) Error() string {
	return fmt.Sprintf("reclaim blocked by %d unrefunded subscriptions", len(e.ids))
}

// errRefundRefDuplicate 哨兵：同一 ExternalRefundRef 已有 TopupRefund 记录，拒绝重复提交。
//
// fix CRITICAL Sprint1-P0-6：旧实现退款幂等不成立 —— 同一 ExternalRefundRef 多次提交会让
// TopupOrder.RefundedAmountRMB 累加（覆盖 RefundNo/OutRefundNo 字段），平台双扣余额但用户
// 钱包只到账一次。新实现：每笔退款先 INSERT TopupRefund（unique on ExternalRefundRef），
// 二次提交在 DB 层被拦截，整笔事务回滚。
type errRefundRefDuplicate struct {
	existing database.TopupRefund
}

func (e *errRefundRefDuplicate) Error() string {
	return fmt.Sprintf("external_refund_ref already used by refund id=%d at %s", e.existing.ID, e.existing.CreatedAt.Format(time.RFC3339))
}

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
		// 加额度（不限制 quota>=0；充值只会增加，永远成立）
		if err := tx.Model(&database.User{}).
			Where("id = ?", order.UserID).
			Update("quota", gorm.Expr("quota + ?", order.AmountUSD)).Error; err != nil {
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

// ─── 公开：用户支付完成跳转 ─────────────────────────────────────

// YifutReturn GET /api/payment/return/yifut
//
// 同步跳转。验签后 redirect 到前端结果页（不在这里加额度——加额度只走 notify）。
//
// 前端是 hash 路由（/#topup_result）；约定把 query 放在 hash 之后：
//
//	/#topup_result?status=success&out_trade_no=xxx
//
// TopupResult.jsx 从 window.location.hash 解析 query。
func YifutReturn(c *fiber.Ctx) error {
	cfg := proxy.LoadYifutConfig()
	resultPath := readStringConfig("yifut_return_path", "/#topup_result")
	// fix Minor（codex 第四轮）：yifut_return_path 是 SysConfig 项，被污染后可指向外部站点
	// 形成支付返回 open redirect。强制必须以单 `/` 开头、不能 `//`、不能含控制字符。
	if !isSafeReturnPath(resultPath) {
		log.Printf("[TOPUP-RETURN] yifut_return_path unsafe=%q, fallback to default", resultPath)
		resultPath = "/#topup_result"
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

// ─── 用户：我的充值记录 ────────────────────────────────────────

// MyTopupOrders GET /api/topup/mine?page=1&page_size=20
func MyTopupOrders(c *fiber.Ctx) error {
	user, err := getCurrentUser(c)
	if err != nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}
	page, _ := strconv.Atoi(c.Query("page", "1"))
	size, _ := strconv.Atoi(c.Query("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}
	var rows []database.TopupOrder
	if err := database.DB.Where("user_id = ?", user.ID).
		Order("id desc").
		Offset((page - 1) * size).
		Limit(size).
		Find(&rows).Error; err != nil {
		log.Printf("[TOPUP-MINE] find failed user=%d: %v", user.ID, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}
	// fix Major M7（codex+claude 第十四轮）：原 count 错误仅日志、execution 继续
	// → total=0 但 rows 非空，前端分页 UI 显示"共 0 条"截断后续页。
	// 与 AdminListTopupOrders 错误处理对齐。
	var total int64
	if err := database.DB.Model(&database.TopupOrder{}).Where("user_id = ?", user.ID).Count(&total).Error; err != nil {
		log.Printf("[TOPUP-MINE] count failed user=%d: %v", user.ID, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}
	return c.JSON(fiber.Map{
		"success": true,
		"data":    topupOrderViewsFrom(rows),
		"meta":    fiber.Map{"page": page, "page_size": size, "total": total},
	})
}

// ─── 用户：充值前端配置（金额预设、启用通道等） ────────────────

// GetTopupOptions GET /api/topup/options
//
// 给前端的下拉选项 + 预设金额。不需要敏感字段（pid/md5_key 不返回）。
func GetTopupOptions(c *fiber.Ctx) error {
	cfg := proxy.LoadYifutConfig()
	enabled := readStringConfig("yifut_enabled_methods", "alipay,wxpay")
	methods := []string{}
	for _, m := range strings.Split(enabled, ",") {
		m = strings.TrimSpace(m)
		if m != "" && allowedPayTypes[m] {
			methods = append(methods, m)
		}
	}
	// fix CRITICAL Sprint4-M3：所有金额改为 fen int64 + 汇率改为 micros int64
	presets := []int64{}
	for _, s := range strings.Split(readStringConfig("yifut_preset_amounts_fen", "1000,3000,5000,10000,30000,50000"), ",") {
		s = strings.TrimSpace(s)
		if v, err := strconv.ParseInt(s, 10, 64); err == nil && v > 0 {
			presets = append(presets, v)
		}
	}
	return c.JSON(fiber.Map{
		"success": true,
		"data": fiber.Map{
			"configured":                       cfg.IsConfigured(),
			"methods":                          methods,
			"presets_fen":                      presets,
			"min_amount_fen":                   readInt64Config("yifut_min_amount_fen", 100),
			"max_amount_fen":                   readInt64Config("yifut_max_amount_fen", 1_000_000),
			"exchange_rate_rmb_per_usd_micros": safeExchangeRateRmbPerUsdMicros(),
			"product_name":                     readStringConfig("yifut_product_name", "DAOF-CPA 余额充值"),
		},
	})
}

// ─── Admin：充值订单管理 ───────────────────────────────────────

// AdminListTopupOrders GET /api/admin/topup/orders?page=&page_size=&status=&user_id=
func AdminListTopupOrders(c *fiber.Ctx) error {
	if loadAdminUser(c) == nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}
	page, _ := strconv.Atoi(c.Query("page", "1"))
	size, _ := strconv.Atoi(c.Query("page_size", "30"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 200 {
		size = 30
	}
	q := database.DB.Model(&database.TopupOrder{})
	if s := c.Query("status"); s != "" {
		// 白名单：避免 admin 拼任意字符串导致索引扫不到 / 误匹配
		switch s {
		case "created", "paid", "refunded", "failed":
			q = q.Where("status = ?", s)
		default:
			return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_BAD_STATUS"})
		}
	}
	if uidStr := c.Query("user_id"); uidStr != "" {
		uid, err := strconv.Atoi(uidStr)
		if err != nil || uid <= 0 {
			return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_BAD_USER_ID"})
		}
		q = q.Where("user_id = ?", uid)
	}
	// fix CRITICAL（自审第十三轮）：原 count 错误仅日志、execution 继续 → total=0 但 rows 非空，
	// 分页 UI 显示"共 0 条"截断后续页。与紧邻的 find 错误处理对齐：失败立即 500。
	var total int64
	if err := q.Count(&total).Error; err != nil {
		log.Printf("[TOPUP-ADMIN-LIST] count failed: %v", err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}
	var rows []database.TopupOrder
	if err := q.Order("id desc").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
		log.Printf("[TOPUP-ADMIN-LIST] find failed: %v", err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}
	return c.JSON(fiber.Map{
		"success": true,
		"data":    topupOrderViewsFrom(rows),
		"meta":    fiber.Map{"page": page, "page_size": size, "total": total},
	})
}

// adminRefundRequest admin 退款请求体。fix CRITICAL Sprint4-M3：从 float64 RMB 改为
// fen int64，杜绝 float 进入金额计算。0 = 全额退款。
type adminRefundRequest struct {
	MoneyFen     int64 `json:"money_fen"`     // RMB × 100，0 = 全额；> 0 = 显式部分退款
	ReclaimQuota bool  `json:"reclaim_quota"` // true=退款+退货（扣回用户额度）；false=仅退款（保留额度）
	// fix CRITICAL C3（codex 第二十轮）：手动退款工作流的对账锚点 —— **必填**。
	// admin 必须先在易付通后台手动完成退款拿到商户退款单号，再在此填入。
	// 写入 BillingEntry.Description + TopupOrder.RefundNo 供财务对账；
	// 空字符串 / 仅控制字符直接 400 拒绝，避免"已退款但无凭证"的财务黑洞。
	ExternalRefundRef string `json:"external_refund_ref"`
}

// AdminRefundTopup POST /api/admin/topup/orders/:id/refund
//
// admin 登记手动退款。状态机：paid → paid（部分退款）/ refunded（全额）。
// reclaim_quota=true 时扣回本次退款对应的 USD 额度，允许让 quota 变负（用户已欠平台）。
//
// 幂等保护：事务内重读订单并用 refunded_amount_rmb 做 CAS，防止 admin 双击或并发触发双重退款。
func AdminRefundTopup(c *fiber.Ctx) error {
	op := loadAdminUser(c)
	if op == nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}
	// fix Minor（自审第十三轮）：原 `id, _ := strconv.Atoi(...)` 静默吞错误，
	// 非数字 id 退化为 0 → First(0) 返回 record-not-found → 404 看起来"安全"但是脚雷。
	// 显式 400 拒绝非法 id，让 admin 拿到精确反馈。
	id, parseErr := strconv.Atoi(c.Params("id"))
	if parseErr != nil || id <= 0 {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_PARAMS"})
	}
	var order database.TopupOrder
	if err := database.DB.First(&order, id).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"success": false, "message_code": "ERR_NOT_FOUND"})
	}
	if order.Status != "paid" {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_NOT_PAID"})
	}

	var req adminRefundRequest
	if err := c.BodyParser(&req); err != nil {
		log.Printf("[TOPUP-REFUND] bad body order=%s err=%v", order.OutTradeNo, err)
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_BAD_REQUEST"})
	}
	// fix CRITICAL C3（codex 第二十轮）：external_refund_ref 必填，sanitize 后空值拒绝
	cleanedRef := sanitizeExternalRef(strings.TrimSpace(req.ExternalRefundRef))
	if cleanedRef == "" {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message":      "请填入易付通后台的商户退款单号（external_refund_ref）",
			"message_code": "ERR_EXTERNAL_REF_REQUIRED",
		})
	}
	req.ExternalRefundRef = cleanedRef
	// fix MAJOR M1（codex 第二十轮）：仅对负数做 tx 外快速失败；
	// "0=全额"和"超额上限"判断必须在 tx 内基于 freshOrder.RefundedAmountRMB 做，
	// 否则两个 admin 浏览器并发提交会用各自的旧 RefundedAmountRMB 算上限 → 进入 tx 后才发现累加越界
	// 报 409 给用户，状态机语义不稳定。
	//
	// fix CRITICAL Sprint4-M3：DTO 改为 int64 fen，无需 NaN/Inf 防护。
	if req.MoneyFen < 0 {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_REFUND_AMOUNT_INVALID"})
	}
	// 0 = 全额（tx 内基于 freshOrder.MoneyRMB - RefundedAmountRMB 算）
	requestedFen := req.MoneyFen

	// fix CRITICAL（codex r11）：admin 退 TopupOrder 且 reclaim_quota=true 时，
	// 如果用户已用这部分 USD 买了 active 订阅，会导致：
	//   - quota 变负（已 reclaim 的额度 - 订阅扣的额度）
	//   - 但 active 订阅仍持续消费 plan 额度 → 用户人民币已退 + 服务继续 = 白嫖
	// 攻击：充 ¥72→$10→买 $10 月套餐→admin 退充值 reclaim_quota=true → quota=-10 但月包还在
	// 防护：在网关调用前（避免无谓退款）先检查；有未退订阅就拒绝，要求 admin 先处理。
	//
	// fix Major（自审第十二轮）：原仅查 status='active' → paused 订阅可绕过保护。
	// schema 中 status 取值：active | expired | canceled | refunded | paused。
	// 真正"仍占用过 USD 且未退款"的状态是 NOT IN (refunded)——
	//   - canceled / expired / paused 都可能由 admin 后续触发 AdminRefundSubscription 退款
	//   - 仅 refunded 是终态资金已结算
	// 改为更严格的"已结算"判定：只在用户所有订阅都是 refunded 时才允许 reclaim quota。
	// fix 第十七轮（**手动退款工作流**）：平台不再调用易付通 V2 退款 API。
	//
	// 工作流：
	//   1. 用户提交退款工单
	//   2. admin 核实后**手动登录易付通后台**完成退款（钱回到用户支付宝/微信）
	//   3. admin 在平台填"商户退款单号"（external_refund_ref）+ 退款金额 + 是否扣回 quota
	//   4. 平台执行：标记订单状态 + 扣回 quota（可选）+ 写账单 + 通知用户
	//
	// 手动退款工作流不接入网关退款 API，攻击面更小，账面保持一致。
	//
	// 安全保留：reclaim_quota 守卫（防用户有未退订阅时退充值导致白嫖）+
	// 订阅退款上限 + csvSanitize 等。
	//
	// fix CRITICAL NEW-C1（codex 第十八轮）：原 reclaim_quota 守卫在事务**外**执行：
	// 攻击窗口 — admin 调用退款 → 守卫检查"用户所有订阅都是 refunded"通过 →
	// 攻击者并发购买订阅创建 active sub → 退款事务进入扣 quota → 用户拿回钱 + 订阅仍 active。
	// 修复：守卫挪入事务，并先 lockUserForUpdate 串行化所有该用户的购买/退款，确保
	// 守卫期间订阅状态不会变化。
	// fix MAJOR M1（codex 第二十轮）：refundRMB / usdToReclaim 全部在 tx 内基于 freshOrder 计算，
	// 防 admin 并发提交时各自用旧 RefundedAmountRMB 通过外部校验后在 tx 内才被 CAS 拒。
	var (
		refundFen         int64 // tx 内确定的本次退款 fen（写日志用）
		usdToReclaimMicro int64 // tx 内确定的等值 micro_usd（写账单 + reclaim quota 用）
		responseOrder     database.TopupOrder
	)
	txErr := database.DB.Transaction(func(tx *gorm.DB) error {
		// 串行化所有该 user 的购买/扣款/退款链路（与 purchaseAsInstant 用同一锁路径）
		if err := lockUserForUpdate(tx, order.UserID); err != nil {
			return fmt.Errorf("lock user: %w", err)
		}

		// fix CRITICAL Sprint1-P0-6：先检查 ExternalRefundRef 唯一性
		// 同一 ExternalRefundRef 已用过则拒绝。lockUserForUpdate 已串行化该用户所有退款，
		// 避免两个 admin 同时拿同一 ref 进入此检查（DB unique 索引兜底跨用户场景）。
		var existingRefund database.TopupRefund
		err := tx.Where("external_refund_ref = ?", req.ExternalRefundRef).First(&existingRefund).Error
		if err == nil {
			return &errRefundRefDuplicate{existing: existingRefund}
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("check refund ref uniqueness: %w", err)
		}

		// 守卫现在在锁后 + 事务内执行：检查到事务提交前，订阅状态都不会被并发改变
		//
		// fix MAJOR（codex 第二十轮）：此守卫原本要 block "reclaim 时用户还有未退款付费订阅"，
		// 但 IsGranted=true 的赠送订阅永远不能 refund（设计如此），如果不在此排除会导致
		// 用户一旦收到任何赠送，未来所有充值的 reclaim_quota 路径**永久阻塞** —— 真实业务回归。
		// 排除 IsGranted=true 是正确做法：赠送订阅与"用户付了多少钱"无关，不该影响 reclaim 决策。
		if req.ReclaimQuota {
			var unrefundedSubIDs []uint
			if err := tx.Model(&database.UserSubscription{}).
				Where("user_id = ? AND status != ? AND is_granted = ?", order.UserID, "refunded", false).
				Pluck("id", &unrefundedSubIDs).Error; err != nil {
				return fmt.Errorf("reclaim guard query: %w", err)
			}
			if len(unrefundedSubIDs) > 0 {
				return &errReclaimBlocked{ids: unrefundedSubIDs}
			}
		}

		// fix MEDIUM（type-design 第十八轮）：事务内**重读** order 拿最新 RefundedAmountRMB，
		// 防 admin 双浏览器并发提交部分退款累加超 MoneyRMB（lockUserForUpdate 串行化 user 但
		// 不锁 order，两次入口读的副本 RefundedAmountRMB 可能都为 0）。
		// 配合 CAS 条件 UPDATE（WHERE refunded_amount_rmb = old）防双写。
		var freshOrder database.TopupOrder
		if err := tx.First(&freshOrder, order.ID).Error; err != nil {
			return fmt.Errorf("re-read order: %w", err)
		}
		// fix MAJOR M1（codex 第二十轮）：基于 freshOrder 计算本次退款 fen
		//   - 0 = 全额（剩余可退）
		//   - > 0 = 显式金额，必须 ≤ 剩余可退
		remainingFen := freshOrder.MoneyRMB - freshOrder.RefundedAmountRMB
		if requestedFen > 0 {
			refundFen = requestedFen
		} else {
			refundFen = remainingFen
		}
		if refundFen <= 0 || refundFen > remainingFen {
			return errRefundAmountInvalid
		}
		newRefundedFen := freshOrder.RefundedAmountRMB + refundFen
		if newRefundedFen > freshOrder.MoneyRMB {
			return errAdminMarkRaced // 累加越界 — 让前端刷新看最新已退金额
		}
		// 使用订单入账时锁定的 AmountUSD 做累计比例差值，而不是每笔按汇率独立 round2。
		// 这样 ¥100 → $13.89 拆成两笔 ¥50 退款时，两笔扣回合计仍精确等于 $13.89。
		prevRefundedMicro, ok := proratedTopupRefundMicro(freshOrder.AmountUSD, freshOrder.MoneyRMB, freshOrder.RefundedAmountRMB)
		if !ok {
			return errRefundAmountInvalid
		}
		newRefundedMicro, ok := proratedTopupRefundMicro(freshOrder.AmountUSD, freshOrder.MoneyRMB, newRefundedFen)
		if !ok || newRefundedMicro < prevRefundedMicro {
			return errRefundAmountInvalid
		}
		usdToReclaimMicro = newRefundedMicro - prevRefundedMicro
		newStatus := "paid" // 部分退款保持 paid，允许继续退
		if newRefundedFen == freshOrder.MoneyRMB {
			newStatus = "refunded"
		}
		updates := map[string]any{
			"refunded_amount_rmb": newRefundedFen,
			"status":              newStatus,
			// C3：external_refund_ref 已在入口必填校验通过，直接写入对账字段
			"refund_no":     req.ExternalRefundRef,
			"out_refund_no": req.ExternalRefundRef,
		}
		// CAS：只在 refunded_amount_rmb 仍是事务入口读到的值时才更新
		res := tx.Model(&database.TopupOrder{}).
			Where("id = ? AND status = ? AND refunded_amount_rmb = ?",
				order.ID, "paid", freshOrder.RefundedAmountRMB).
			Updates(updates)
		if res.Error != nil {
			return fmt.Errorf("update order: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return errAdminMarkRaced
		}
		responseOrder = freshOrder
		responseOrder.Status = newStatus
		responseOrder.RefundedAmountRMB = newRefundedFen
		responseOrder.RefundNo = req.ExternalRefundRef
		responseOrder.OutRefundNo = req.ExternalRefundRef

		if req.ReclaimQuota {
			if err := tx.Model(&database.User{}).
				Where("id = ?", order.UserID).
				Update("quota", gorm.Expr("quota - ?", usdToReclaimMicro)).Error; err != nil {
				return fmt.Errorf("reclaim quota: %w", err)
			}
		}

		// 账单流水
		var freshUser database.User
		if err := tx.Select("id, quota").First(&freshUser, order.UserID).Error; err != nil {
			return fmt.Errorf("fetch fresh quota: %w", err)
		}
		var amountMicroUSD int64
		desc := fmt.Sprintf("充值退款 ¥%s（admin 已在易付通后台退款）· 退款单号 %s",
			database.FormatFen(refundFen), req.ExternalRefundRef)
		if req.ReclaimQuota {
			amountMicroUSD = -usdToReclaimMicro
			desc += "（已扣回额度）"
		} else {
			amountMicroUSD = 0
			desc += "（保留额度，客服补偿）"
		}
		if err := database.WriteBillingEntry(tx, database.BillingEntryInput{
			UserID:           order.UserID,
			OccurredAt:       time.Now(),
			EntryType:        database.BillingTypeRefundTopup,
			AmountUSD:        amountMicroUSD,
			BalanceAfterUSD:  freshUser.Quota,
			RelatedType:      "topup_order",
			RelatedID:        order.ID,
			Description:      desc,
			CurrencyOriginal: "CNY",
			AmountOriginal:   -refundFen,
		}); err != nil {
			return fmt.Errorf("write billing refund_topup: %w", err)
		}
		// fix CRITICAL Sprint1-P0-6：写 TopupRefund 事实表（唯一索引兜底幂等）
		// 已在事务入口检查过 ExternalRefundRef 不存在，正常路径下 INSERT 必然成功；
		// 极端并发场景（两个 admin 同时提交，pre-check 都通过但 INSERT 抢一个）由 DB 层
		// unique 索引拒绝第二个，整个事务回滚 → 退款效果只发生一次。
		refundRow := database.TopupRefund{
			TopupOrderID:      order.ID,
			ExternalRefundRef: req.ExternalRefundRef,
			AmountFen:         refundFen,
			AmountMicroUSD:    usdToReclaimMicro,
			ReclaimQuota:      req.ReclaimQuota,
			OperatorID:        op.ID,
			Reason:            "",
			CreatedAt:         time.Now(),
		}
		if err := tx.Create(&refundRow).Error; err != nil {
			// unique 违反 = pre-check 后到 INSERT 之间另一事务抢先 → 拒绝当前请求
			if strings.Contains(strings.ToLower(err.Error()), "unique") {
				return &errRefundRefDuplicate{existing: refundRow}
			}
			return fmt.Errorf("insert topup_refund: %w", err)
		}

		auditDetails, _ := json.Marshal(map[string]any{
			"type":                "REFUND_TOPUP",
			"admin_id":            op.ID,
			"order_id":            order.ID,
			"refund_id":           refundRow.ID,
			"out_trade_no":        freshOrder.OutTradeNo,
			"amount_rmb":          fenToRMBFloat(refundFen),
			"amount_fen":          refundFen,
			"amount_micro_usd":    amountMicroUSD,
			"external_refund_ref": req.ExternalRefundRef,
			"reclaim_quota":       req.ReclaimQuota,
		})
		return LogOperationByTx(tx, op.ID, order.UserID, "admin", "REFUND_TOPUP", c.IP(), string(auditDetails))
	})
	if errors.Is(txErr, errAdminMarkRaced) {
		return c.Status(409).JSON(fiber.Map{
			"success":      false,
			"message":      "订单状态已变化，请刷新后重试",
			"message_code": "ERR_REFUND_RACED",
		})
	}
	// fix CRITICAL Sprint1-P0-6：external_refund_ref 重复提交（同一退款单号多次入账尝试）
	var dup *errRefundRefDuplicate
	if errors.As(txErr, &dup) {
		log.Printf("[TOPUP-REFUND-MANUAL] DUPLICATE external_refund_ref=%q order=%s admin=%d existing_refund_id=%d",
			req.ExternalRefundRef, order.OutTradeNo, op.ID, dup.existing.ID)
		return c.Status(409).JSON(fiber.Map{
			"success":              false,
			"message":              "该退款单号已被使用过，无法重复入账。如需新一笔退款请使用不同的商户退款单号。",
			"message_code":         "ERR_REFUND_REF_DUPLICATED",
			"existing_refund_id":   dup.existing.ID,
			"existing_refunded_at": dup.existing.CreatedAt.Format(time.RFC3339),
		})
	}
	// fix MAJOR M1：tx 内 fresh-based 校验失败 → 4xx 而非 500
	if errors.Is(txErr, errRefundAmountInvalid) {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message":      "退款金额非法或超过当前剩余可退（请刷新后查看最新已退金额）",
			"message_code": "ERR_REFUND_AMOUNT_INVALID",
		})
	}
	// fix CRITICAL NEW-C1：reclaim 守卫在事务内拦截，sentinel 带订阅 ID 列表回来渲染
	var blocked *errReclaimBlocked
	if errors.As(txErr, &blocked) {
		log.Printf("[TOPUP-REFUND-MANUAL] BLOCKED reclaim_quota for user=%d (has %d unrefunded subs %v)",
			order.UserID, len(blocked.ids), blocked.ids)
		return c.Status(409).JSON(fiber.Map{
			"success":                 false,
			"message":                 "用户有未退款的订阅记录（含 active/canceled/expired/paused）。请先在【订阅总览】处理这些订阅，再退充值。",
			"message_code":            "ERR_USER_HAS_UNREFUNDED_SUBSCRIPTIONS",
			"active_subscription_ids": blocked.ids,
		})
	}
	if txErr != nil {
		log.Printf("[TOPUP-REFUND-MANUAL] tx failed order=%s admin=%d rmb_fen=%d: %v",
			order.OutTradeNo, op.ID, refundFen, txErr)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_TRANSACTION"})
	}

	// quota 变更后刷新 AuthCache（仅退款不扣额度也建议刷新一次保证状态一致）
	proxy.RefreshUserAuth(order.UserID)

	// 退款通知。文案明确表达"退款已确认（请查收易付通退款）"，与之前"已发起"区分。
	title := readSysConfigCached("notif_topup_refund_title", "退款已确认")
	bodyTpl := readSysConfigCached("notif_topup_refund_body", "您的充值订单 {package_name} 已退款 {amount} {currency}，请查收支付宝/微信。如未到账请提交工单。")
	body := strings.ReplaceAll(bodyTpl, "{package_name}", order.OutTradeNo)
	body = strings.ReplaceAll(body, "{amount}", database.FormatFen(refundFen))
	body = strings.ReplaceAll(body, "{currency}", "RMB")
	dedupKey := fmt.Sprintf("topup_refund:%s:%d", order.OutTradeNo, time.Now().UnixNano())
	proxy.Dispatch(order.UserID, "refund", "success", title, body,
		proxy.LinkTickets(), "提交工单", "topup", order.ID, &dedupKey)

	log.Printf("[TOPUP-REFUND-MANUAL] OK order=%s admin=%d rmb_fen=%d reclaim_quota=%v ref=%q",
		order.OutTradeNo, op.ID, refundFen, req.ReclaimQuota, req.ExternalRefundRef)
	return c.JSON(fiber.Map{
		"success":      true,
		"data":         topupOrderViewFrom(responseOrder),
		"message_code": "SUCCESS_REFUNDED",
	})
}

// ─── helpers ───────────────────────────────────────────────────

// generateOutTradeNo 生成全局唯一商户订单号。
// 格式 "tp{userID}{unixmilli}{rand16}"，最大 ~38 字节（仍 <64 即数据库列约束）。
// 16 hex 字符 = 8 字节随机 = 2^64 熵，单毫秒同 user 撞概率几乎为零。
func generateOutTradeNo(userID uint) (string, error) {
	r := make([]byte, 8)
	if _, err := rand.Read(r); err != nil {
		return "", fmt.Errorf("crypto/rand: %w", err)
	}
	return fmt.Sprintf("tp%d%d%s", userID, time.Now().UnixMilli(), hex.EncodeToString(r)), nil
}

// 注（第十七轮）：原 generateOutRefundNo 用于 V2 退款 API 的商户退款单号生成，
// 现在平台改为手动退款工作流（admin 在易付通后台退款），不再调用 V2 退款 API。
// admin 通过 ExternalRefundRef 字段填入易付通后台的退款单号做对账锚点，
// 该函数已无调用方，删除。

// sanitizeExternalRef 清理 admin 输入的退款单号：剥离控制字符（\n \r \t 等），
// 截断到 64 字符。
//
// fix LOW（security 第十八轮）：原实现仅 TrimSpace + len 截断，未防控制字符注入。
// admin 在易付通后台复制粘贴时可能误带换行符，落入 BillingEntry.Description 后会被
// 外部日志解析工具误读为多行结构，破坏对账。
//
// fix LOW（codex 第十九轮）：原 cleaned[:64] 是 byte 截断 → 多字节 rune（中文/emoji）会被
// 截在 UTF-8 中段产生无效字节序列，最终持久化的 description 在 JSON 序列化时变为 �。
// 改为 rune-based 截断保证 cut 永远在合法边界，并且"长度上限"以语义字符数计而非字节数。
func sanitizeExternalRef(s string) string {
	if s == "" {
		return ""
	}
	// 仅保留可打印 ASCII + 常见 Unicode 字母数字（易付通退款单号实际只有 ASCII 字母数字 + 短横）
	cleaned := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f { // 控制字符 + DEL
			return -1
		}
		return r
	}, s)
	if utf8.RuneCountInString(cleaned) > 64 {
		// 取前 64 个 rune，逐 rune 累计字节长度后切片 → 永远在 rune 边界
		runes := []rune(cleaned)
		cleaned = string(runes[:64])
	}
	return cleaned
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

func ValidateYifutNotifyCIDRConfig() error {
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

func readStringConfig(key, def string) string {
	proxy.SysConfigMutex.RLock()
	v := strings.TrimSpace(proxy.SysConfigCache[key])
	proxy.SysConfigMutex.RUnlock()
	if v == "" {
		return def
	}
	return v
}

// readBoolConfig 把 SysConfig 字符串转 bool（"true"/"1" → true，其他 → false）。
func readBoolConfig(key string, def bool) bool {
	v := readStringConfig(key, "")
	if v == "" {
		return def
	}
	switch strings.ToLower(v) {
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		return false
	}
	return def
}

// readInt64Config 读取存为整数字符串的 SysConfig 项。
// fix CRITICAL Sprint4-M3：fen / micros 等定点金额单位入口，杜绝 float 解析。
func readInt64Config(key string, def int64) int64 {
	v := readStringConfig(key, "")
	if v == "" {
		return def
	}
	i, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return def
	}
	return i
}

// readMicroUSDConfig 读取存为 micro_usd 整数字符串的 SysConfig 项。
// fix P0-δ A-10：金额配置入口整数化，拒绝 USD float string。
func readMicroUSDConfig(key string, defMicroUSD int64) int64 {
	v := readStringConfig(key, "")
	if v == "" {
		return defMicroUSD
	}
	micro, err := strconv.ParseInt(v, 10, 64)
	if err != nil || micro < 0 {
		return defMicroUSD
	}
	return micro
}

// safeExchangeRateRmbPerUsdMicros 永远返回 >0 的汇率（RMB per USD × 1e6）。
// fix CRITICAL Sprint4-M3：从 float64 改为 int64 定点。
// SysConfig 配 "0" / 负数 / 缺失都回退默认 7_200_000（= 7.2 RMB/USD）。
func safeExchangeRateRmbPerUsdMicros() int64 {
	rate := readInt64Config("exchange_rate_rmb_per_usd_micros", 7_200_000)
	if rate <= 0 {
		return 7_200_000
	}
	return rate
}

// usdMicroFromFenAndRate 把 RMB fen + 汇率（RMB/USD × 1e6）换算成 USD micro_usd。
//
// 公式：usd_micro = fen × 1e10 / rate_rmb_per_usd_micros
// 推导：
//
//	amount_rmb       = fen / 100                  (yuan)
//	amount_rmb_micros = fen × 1e4                  (RMB × 1e6 微元)
//	rate             = rate_micros / 1e6            (RMB/USD)
//	amount_usd_micros = amount_rmb_micros / rate    = fen × 1e4 × 1e6 / rate_micros = fen × 1e10 / rate_micros
//
// floor 截断（保守入账：用户存 ¥72.5 / 7.2 ¥ rate → $10.069444 USD，floor 不多送）。
// fix CRITICAL Sprint4-M3：用 big.Int 全整数运算，杜绝 float64 IEEE 754 噪声。
func usdMicroFromFenAndRate(fen int64, rateRmbPerUsdMicros int64) (int64, bool) {
	if fen <= 0 || rateRmbPerUsdMicros <= 0 {
		return 0, false
	}
	// fen × 1e10 可达 9.2e18 > int64 上限（fen ≤ 9.2e8 即 ¥9.2M），用 big.Int 避免溢出
	product := new(big.Int).Mul(big.NewInt(fen), big.NewInt(10_000_000_000))
	result := new(big.Int).Quo(product, big.NewInt(rateRmbPerUsdMicros))
	if !result.IsInt64() || result.Sign() <= 0 {
		return 0, false
	}
	return result.Int64(), true
}

func csvContains(csv, val string) bool {
	for _, s := range strings.Split(csv, ",") {
		if strings.TrimSpace(s) == val {
			return true
		}
	}
	return false
}

// isSafeReturnPath 校验 SysConfig.yifut_return_path 仅为站内绝对路径，
// 拒绝外部 URL/协议相对 URL/控制字符，防 open redirect。
func isSafeReturnPath(p string) bool {
	s := strings.TrimSpace(p)
	if s == "" {
		return false
	}
	if strings.ContainsAny(s, "\r\n\t") {
		return false
	}
	if strings.HasPrefix(s, "//") {
		return false
	}
	if !strings.HasPrefix(s, "/") {
		return false
	}
	// 含 scheme（http://...）的字符串"恰好以 /"开头几乎不可能，但兜底用 url.Parse 拒绝
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil {
		return false
	}
	return true
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

// parseRMBStringToFen 把 "12.34" / "12" / "12.3" 这类 RMB 元字符串解析为 fen 整数。
//
// 设计原因（fix CRITICAL 多模型审计第二十五轮）：
//   - 易付通回调金额是字符串，原实现 float 解析 + approxEqual(0.001) 容差有精度漏洞
//   - 直接整数化后与 order.MoneyRMB（fen int64）做严格 == 比较彻底消除浮点误差
//
// 规则：
//   - 至多 2 位小数；超过返回 false
//   - 拒绝负数 / 空 / 非数字字符 / 多个小数点 / 尾随小数点（"12."）
//   - "12" → 1200; "12.3" → 1230; "12.34" → 1234; "12.345" → false; "12." → false; ".5" → false
func parseRMBStringToFen(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasPrefix(s, "-") {
		return 0, false
	}
	// 分离整数 / 小数部分
	intPart := s
	fracPart := ""
	if idx := strings.Index(s, "."); idx >= 0 {
		intPart = s[:idx]
		fracPart = s[idx+1:]
		if strings.Contains(fracPart, ".") {
			return 0, false
		}
		// fix MINOR（多模型审计第二十五轮 P2）：拒绝尾随小数点（"12."）
		// 严格金额格式：要么没小数点，要么小数点后必须有 1-2 位数字
		if fracPart == "" {
			return 0, false
		}
	}
	if intPart == "" {
		return 0, false
	}
	// 整数部分必须全数字
	for _, ch := range intPart {
		if ch < '0' || ch > '9' {
			return 0, false
		}
	}
	// 小数部分至多 2 位且全数字
	if len(fracPart) > 2 {
		return 0, false
	}
	for _, ch := range fracPart {
		if ch < '0' || ch > '9' {
			return 0, false
		}
	}
	// 补齐到 2 位（"3" → "30"）
	for len(fracPart) < 2 {
		fracPart += "0"
	}
	combined := intPart + fracPart
	v, err := strconv.ParseInt(combined, 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// proratedTopupRefundMicro 返回"累计退款到 refundedFen 时"应累计扣回的 micro_usd。
//
// 充值订单的 USD 入账金额已经在 TopupOrder.AmountUSD 中锁定。退款时必须围绕这个
// 金额做比例分摊，不能每笔重新按汇率换算并四舍五入，否则多次部分退款会和原始入账
// 金额对不上。调用方用 newTarget - oldTarget 得到本次扣回额，最终全额退款时天然等于
// order.AmountUSD。
func proratedTopupRefundMicro(amountMicro, moneyFen, refundedFen int64) (int64, bool) {
	if amountMicro <= 0 || moneyFen <= 0 || refundedFen < 0 || refundedFen > moneyFen {
		return 0, false
	}
	if refundedFen == 0 {
		return 0, true
	}
	if refundedFen == moneyFen {
		return amountMicro, true
	}

	numerator := new(big.Int).Mul(big.NewInt(amountMicro), big.NewInt(refundedFen))
	// positive half-up rounding: floor((2*numerator + denominator) / (2*denominator))
	numerator.Mul(numerator, big.NewInt(2))
	denominator := big.NewInt(moneyFen)
	numerator.Add(numerator, denominator)
	denominator.Mul(denominator, big.NewInt(2))
	quotient := new(big.Int).Quo(numerator, denominator)
	if !quotient.IsInt64() {
		return 0, false
	}
	return quotient.Int64(), true
}
