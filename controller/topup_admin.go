// Package controller / topup_admin.go
//
// admin 视角充值管理：列出订单（AdminListTopupOrders）、手动补登（AdminMarkTopupPaid）、
// 手动退款（AdminRefundTopup）+ 相关 sentinel 错误。
//
// 业务模型（第十七轮起）：
//   - 充值入账走易付通 webhook（topup_webhook.go）
//   - 退款不再调用 V2 退款 API；admin 在易付通后台手动退完后回到这里登记单号 + 退款金额
//
// 从 topup.go 抽出（Phase D-5，2026-05-19）：只是物理拆分，无语义改动。
package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
	"unicode"

	"daof-cpa/database"
	"daof-cpa/proxy"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// errAdminMarkRaced 哨兵：admin 手动标记充值订单时状态已被并发修改（如另一 admin 同时操作）
var errAdminMarkRaced = errors.New("topup order state changed during manual mark")

// errManualPaidRefDuplicate 哨兵：同一外部支付凭证已经被用于一次手动到账补登。
var errManualPaidRefDuplicate = errors.New("manual paid reference already used")

const manualPaidReceiptProvider = "manual_paid"

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

// adminMarkTopupPaidRequest admin 手动确认充值到账请求体。
//
// 该入口只用于"用户已在支付通道真实付款，但 notify_url 未送达 / 未入账"的补登。
// 它和普通用户余额调额不同：必须同时增加 quota 与 paid_quota。
type adminMarkTopupPaidRequest struct {
	ExternalTradeRef string `json:"external_trade_ref"` // 支付通道订单号 / 后台付款凭证，必填且全局幂等
	Reason           string `json:"reason"`             // 可选备注，写入审计
}

const topupManualPaidReasonMaxLen = 500

// AdminMarkTopupPaid POST /api/admin/topup/orders/:id/mark-paid
//
// 手动补登充值到账。状态机：created → paid。
// 真实资金已由 admin 在支付通道后台确认；本接口只负责把本地订单补推进到账状态。
func AdminMarkTopupPaid(c *fiber.Ctx) error {
	op := loadAdminUser(c)
	if op == nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}
	id, parseErr := strconv.Atoi(c.Params("id"))
	if parseErr != nil || id <= 0 {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_PARAMS"})
	}
	var req adminMarkTopupPaidRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_BAD_REQUEST"})
	}
	externalRef := sanitizeExternalRef(strings.TrimSpace(req.ExternalTradeRef))
	if externalRef == "" {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_EXTERNAL_REF_REQUIRED"})
	}
	reason := strings.TrimSpace(req.Reason)
	if runeLen := len([]rune(reason)); runeLen > topupManualPaidReasonMaxLen {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message":      fmt.Sprintf("reason 长度不能超过 %d 字符（当前 %d）", topupManualPaidReasonMaxLen, runeLen),
			"message_code": "ERR_REASON_TOO_LONG",
		})
	}
	for _, r := range reason {
		if unicode.IsControl(r) {
			return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_REASON_CTRL_CHAR"})
		}
	}

	var order database.TopupOrder
	if err := database.DB.First(&order, id).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"success": false, "message_code": "ERR_NOT_FOUND"})
	}
	if order.Status != "created" {
		return c.Status(409).JSON(fiber.Map{"success": false, "message_code": "ERR_TOPUP_NOT_PENDING"})
	}

	now := time.Now()
	txErr := database.DB.Transaction(func(tx *gorm.DB) error {
		if err := lockUserForUpdate(tx, order.UserID); err != nil {
			return fmt.Errorf("lock user: %w", err)
		}

		var freshOrder database.TopupOrder
		if err := tx.First(&freshOrder, order.ID).Error; err != nil {
			return fmt.Errorf("read order: %w", err)
		}
		if freshOrder.Status != "created" {
			return errAdminMarkRaced
		}

		receipt := database.PaymentWebhookReceipt{
			Provider:      manualPaidReceiptProvider,
			Nonce:         externalRef,
			SignatureHash: signatureHash("manual-paid:" + freshOrder.OutTradeNo + ":" + externalRef),
			OutTradeNo:    freshOrder.OutTradeNo,
			RemoteIP:      c.IP(),
			Status:        "accepted_manual",
			Reason:        reason,
			ReceivedAt:    now,
		}
		if err := tx.Create(&receipt).Error; err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "unique") {
				return errManualPaidRefDuplicate
			}
			return fmt.Errorf("insert manual paid receipt: %w", err)
		}

		res := tx.Model(&database.TopupOrder{}).
			Where("id = ? AND status = ?", freshOrder.ID, "created").
			Updates(map[string]any{
				"status":       "paid",
				"trade_no":     externalRef,
				"api_trade_no": externalRef,
				"paid_at":      now,
			})
		if res.Error != nil {
			return fmt.Errorf("mark order paid: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return errAdminMarkRaced
		}

		if err := tx.Model(&database.User{}).
			Where("id = ?", freshOrder.UserID).
			Updates(map[string]any{
				"quota":      gorm.Expr("quota + ?", freshOrder.AmountUSD),
				"paid_quota": gorm.Expr("paid_quota + ?", freshOrder.AmountUSD),
			}).Error; err != nil {
			return fmt.Errorf("add quota: %w", err)
		}

		var freshUser database.User
		if err := tx.Select("id, quota").First(&freshUser, freshOrder.UserID).Error; err != nil {
			return fmt.Errorf("fetch fresh quota: %w", err)
		}
		desc := fmt.Sprintf("充值补登 ¥%s（%s，凭证 %s）", database.FormatFen(freshOrder.MoneyRMB), freshOrder.PayType, externalRef)
		if reason != "" {
			desc += " · " + reason
		}
		if err := database.WriteBillingEntry(tx, database.BillingEntryInput{
			UserID:           freshOrder.UserID,
			OccurredAt:       now,
			EntryType:        database.BillingTypeTopup,
			AmountUSD:        freshOrder.AmountUSD,
			BalanceAfterUSD:  freshUser.Quota,
			RelatedType:      "topup_order",
			RelatedID:        freshOrder.ID,
			Description:      desc,
			CurrencyOriginal: "CNY",
			AmountOriginal:   freshOrder.MoneyRMB,
		}); err != nil {
			return fmt.Errorf("write billing entry: %w", err)
		}

		auditDetails, _ := json.Marshal(map[string]any{
			"type":               "TOPUP_MANUAL_MARK_PAID",
			"topup_order_id":     freshOrder.ID,
			"out_trade_no":       freshOrder.OutTradeNo,
			"external_trade_ref": externalRef,
			"amount_micro_usd":   freshOrder.AmountUSD,
			"money_fen":          freshOrder.MoneyRMB,
			"reason":             reason,
		})
		return LogOperationByTx(tx, op.ID, freshOrder.UserID, "admin", "TOPUP_MANUAL_MARK_PAID", c.IP(), string(auditDetails))
	})
	if errors.Is(txErr, errAdminMarkRaced) {
		return c.Status(409).JSON(fiber.Map{"success": false, "message_code": "ERR_TOPUP_NOT_PENDING"})
	}
	if errors.Is(txErr, errManualPaidRefDuplicate) {
		return c.Status(409).JSON(fiber.Map{"success": false, "message_code": "ERR_TOPUP_MANUAL_REF_DUPLICATE"})
	}
	if txErr != nil {
		log.Printf("[TOPUP-MANUAL-PAID] tx failed order=%s admin=%d err=%v", order.OutTradeNo, op.ID, txErr)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_TRANSACTION"})
	}

	proxy.InvalidateUserSubscriptionCache(order.UserID)
	proxy.RefreshUserAuth(order.UserID)

	title := readSysConfigCached("notif_topup_title", "充值成功")
	bodyTpl := readSysConfigCached("notif_topup_body", "您充值的 ¥{amount_rmb} 已到账，等额 {amount_usd} USD 已加入余额。")
	body := strings.ReplaceAll(bodyTpl, "{amount_rmb}", database.FormatFen(order.MoneyRMB))
	body = strings.ReplaceAll(body, "{amount_usd}", database.FormatMicroUSD(order.AmountUSD))
	dedupKey := fmt.Sprintf("topup_manual_paid:%s", order.OutTradeNo)
	proxy.Dispatch(order.UserID, "topup", "success", title, body,
		proxy.LinkBills("topup"), "查看账单", "topup_order", order.ID, &dedupKey)

	log.Printf("[TOPUP-MANUAL-PAID] OK order=%s admin=%d ref=%q usd_micro=%d",
		order.OutTradeNo, op.ID, externalRef, order.AmountUSD)
	return c.JSON(fiber.Map{"success": true, "message_code": "SUCCESS_TOPUP_MANUAL_PAID"})
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

		userUpdates := map[string]any{
			"paid_quota": gorm.Expr(
				"CASE WHEN paid_quota >= ? THEN paid_quota - ? ELSE 0 END",
				usdToReclaimMicro, usdToReclaimMicro,
			),
		}
		if req.ReclaimQuota {
			userUpdates["quota"] = gorm.Expr("quota - ?", usdToReclaimMicro)
		}
		if err := tx.Model(&database.User{}).
			Where("id = ?", order.UserID).
			Updates(userUpdates).Error; err != nil {
			return fmt.Errorf("reclassify refunded paid quota: %w", err)
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
