// Package controller / topup.go
//
// 余额充值（易付通 V2 SHA256WithRSA 协议）—— 用户视角 HTTP 入口。
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
//
// 文件物理分布（Phase D-5，2026-05-19）：
//   - topup.go：用户视角 handler（本文件）
//   - topup_webhook.go：易付通 webhook（YifutNotify / YifutReturn）+ webhook 安全 helper
//   - topup_admin.go：admin 视角 handler + 退款 sentinel
//   - topup_money.go：金额单位换算 + SysConfig 读取等纯函数
package controller

import (
	"context"
	"log"
	"strconv"
	"strings"
	"time"

	"daof-cpa/database"
	"daof-cpa/proxy"

	"github.com/gofiber/fiber/v2"
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
			"message":      "支付订单创建失败，请重试或提交工单",
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
