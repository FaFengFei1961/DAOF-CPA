// Package controller / subscription_admin.go
//
// admin 视角订阅管理：退款（AdminRefundSubscription）、撤回赠送
// （AdminRevokeGrantedSubscription）、订阅总览列表（AdminListSubscriptions）。
//
// 从 subscription.go 抽出（Phase D-5，2026-05-19）：只是物理拆分，无语义改动。
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
	"gorm.io/gorm/clause"
)

// errSubStateMachineMiss 表示事务内条件 UPDATE rowsAffected=0——
// 实际 sub.status 已脱离允许的源状态集合（被并发取消、退款、暂停等）。
// fix Minor（自审第十三轮）：原 sentinel 名为 errSubAlreadyCanceled 仅描述 cancel 场景，
// 但被 AdminRefundSubscription 复用于 paused/refunded 拒绝 → 名字误导后续维护者。
var errSubStateMachineMiss = errors.New("subscription state machine guard rejected: status not in expected set")
var errSubRefundDuplicate = errors.New("subscription refund billing already exists")

var (
	errRevokeGrantNotGranted = errors.New("subscription is not admin-granted")
	errRevokeGrantBadStatus  = errors.New("granted subscription status cannot be revoked")
)

// adminRefundSubscriptionRequest admin 触发订阅退款的请求体
//
// 金额入口使用 int64 micro_usd，禁止 USD float。
type adminRefundSubscriptionRequest struct {
	AmountMicroUSD int64  `json:"amount_micro_usd"` // 协商后的退款金额（micro_usd），必须 > 0 且 <= 购买价
	Reason         string `json:"reason"`           // 退款原因（写入审计）
	//
	// 业务规则（用户 2026-05-10 第三次反馈定稿）：取消/退款都**不**触碰优惠券。
	// admin 视情况想给用户发"补偿券"应**独立**走 AdminGrantCoupon 入口，
	// 不要在退款流程里捆绑——这样审计两边各自清晰，账单 / 券系统解耦。
}

// adminRevokeGrantedSubscriptionRequest admin 收回赠送订阅的请求体。
//
// 收回赠送只撤销权益，不做退款、不改变 user.quota。reason 必填，进入账单描述和审计日志。
type adminRevokeGrantedSubscriptionRequest struct {
	Reason string `json:"reason"`
}

// AdminRefundSubscription POST /api/admin/subscriptions/:id/refund
//
// 业务模型：用户通过工单提交退款申请 → admin 协商金额 → 调本接口执行实际退款。
//
// 状态机：
//   - active / canceled / expired / paused → refunded（最终态）
//   - refunded → 拒绝（已退款终态资金已结算）
//
// 原子性：
//   - 条件 UPDATE 保证 status 只能从允许态转 refunded（防并发双退款）
//   - 同事务内 user.Quota += amount（amount 来自 admin 协商，不再用任何自动公式）
//   - 写 OperationLog 审计
//   - 发通知给用户
func AdminRefundSubscription(c *fiber.Ctx) error {
	op := loadAdminUser(c)
	if op == nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_PARAMS"})
	}
	var req adminRefundSubscriptionRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_PARSE_PAYLOAD"})
	}
	refundAmountMicro := req.AmountMicroUSD
	if refundAmountMicro <= 0 {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message":      "amount_micro_usd 必须为正整数",
			"message_code": "ERR_REFUND_AMOUNT_INVALID",
		})
	}

	var sub database.UserSubscription
	if err := database.DB.First(&sub, id).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"success": false, "message_code": "ERR_NOT_FOUND"})
	}
	// fix CRITICAL（grant 改造）：admin 赠送的订阅 net_cost = 0，用户没付钱过，
	// 退款 = 平台白送钱给用户（甚至比购买套利还离谱）。直接拒绝。
	// admin 想"撤回"赠送必须走 AdminRevokeGrantedSubscription（标记 status=revoked，不动 quota）。
	if sub.IsGranted {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message":      "管理员赠送的订阅不能退款（用户未付费），如需停止该订阅请使用『取消赠送』入口",
			"message_code": "ERR_REFUND_GRANTED_SUB",
		})
	}
	// 防超额：退款不能超过用户实际成交价。
	//
	// fix CRITICAL R23+2-C1（codex 全方面审查 第二轮）：
	// PurchasedUnitPriceUSD 是购买时持久化的实际成交价（含券折扣）。
	//
	// 关键：免费券购买（用户用免费券拿到 sub）→ PurchasedUnitPriceUSD == 0
	//       → 退款上限 0 → admin 不能退款（用户没付钱），符合预期。
	//
	// 项目未上线：不再用 snapshot.price_amount 作为兜底——历史快照可能是原价，
	// 让免费券购买被错误退款。统一规则：**只读** sub.PurchasedUnitPriceUSD，
	// 等于 0 表示"实际未付费"（免费券或赠送），不允许退款。
	if sub.PurchasedUnitPriceUSD <= 0 {
		log.Printf("[SUB-REFUND] BLOCKED purchased_unit_price_usd=0 sub=%d user=%d coupon_id=%d (free-coupon purchase or granted subscription)",
			sub.ID, sub.UserID, sub.AppliedCouponID)
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message":      "用户未实际付费（可能用免费券购买或赠送），无可退金额。如需补偿请用『补发优惠券』",
			"message_code": "ERR_REFUND_ZERO_PAID",
		})
	}
	purchasedPriceMicro := sub.PurchasedUnitPriceUSD
	// fix CRITICAL（多模型审计第二十五轮）：严格 `>`，无容差。退款金额比较走 int64 micro_usd 域。
	// 任何 admin 想退超过实际支付的金额都必须明确出错（"退款≤已收"铁律不容许灰色地带）。
	if refundAmountMicro > purchasedPriceMicro {
		return c.Status(400).JSON(fiber.Map{
			"success": false,
			"message": fmt.Sprintf("退款金额超过用户实际支付金额 $%s",
				database.FormatMicroUSD(purchasedPriceMicro)),
			"message_code": "ERR_REFUND_AMOUNT_EXCEEDS_PURCHASE",
		})
	}

	// fix Major M6（claude type-design 第十五轮）：paused 加入可退清单。
	// paused 语义是"已付款但消费暂停"，与 active/canceled 一样代表"用户有未结算钱"，
	// 应允许退款；refunded 是终态资金已结算，仍排除。
	// 状态机：active / canceled / expired / paused 可退；仅 refunded 拒绝
	now := time.Now()
	txErr := database.DB.Transaction(func(tx *gorm.DB) error {
		// fix MAJOR R23+2-B1（codex 全方面审查）：退款事务必须锁 user 行，与购买事务串行化。
		// 否则退款 + 购买并发可能让 quota 余额状态机错位（同时给同时扣）。
		if err := lockUserForUpdate(tx, sub.UserID); err != nil {
			return fmt.Errorf("lock user: %w", err)
		}
		var existingRefundCount int64
		if err := tx.Model(&database.BillingEntry{}).
			Where("related_type IN ? AND related_id = ? AND entry_type = ?",
				[]string{"subscription_refund", "subscription"}, sub.ID, database.BillingTypeRefundSub).
			Count(&existingRefundCount).Error; err != nil {
			return fmt.Errorf("check refund billing duplicate: %w", err)
		}
		if existingRefundCount > 0 {
			return errSubRefundDuplicate
		}
		// fix Major（自审第十一轮）：原 Updates 强制写 canceled_at = now 会覆盖已 canceled 订阅的
		// 原始取消时间，让审计日志里"用户先 cancel 再申请退款"的时序信息丢失。
		// 改为：只更新 status；canceled_at 仅在还为 NULL 时补（直接 active→refunded 路径）。
		// fix MAJOR（codex 第二十轮）：事务内 UPDATE 加 is_granted = false 条件作为防御深度。
		res := tx.Model(&database.UserSubscription{}).
			Where("id = ? AND is_granted = ? AND status IN ?", sub.ID, false, []string{"active", "canceled", "expired", "paused"}).
			Updates(map[string]any{
				"status":      "refunded",
				"canceled_at": gorm.Expr("CASE WHEN canceled_at IS NULL THEN ? ELSE canceled_at END", now),
			})
		if res.Error != nil {
			return fmt.Errorf("update sub status: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return errSubStateMachineMiss // 状态非可退态 / 或 is_granted=true（事务内不可退）
		}
		// 给用户加 quota（micro_usd）
		if err := tx.Model(&database.User{}).Where("id = ?", sub.UserID).
			UpdateColumn("quota", gorm.Expr("quota + ?", refundAmountMicro)).Error; err != nil {
			return fmt.Errorf("refund quota: %w", err)
		}
		// 账单流水：退款入账
		var freshUser database.User
		if err := tx.Select("id, quota").First(&freshUser, sub.UserID).Error; err != nil {
			return fmt.Errorf("fetch fresh quota: %w", err)
		}
		pkgName := readPackageNameFromSnapshot(sub.PackageSnapshot)
		if pkgName == "" {
			pkgName = fmt.Sprintf("套餐#%d", sub.PackageID)
		}
		desc := fmt.Sprintf("退款：「%s」", pkgName)
		if req.Reason != "" {
			desc += " · " + req.Reason
		}
		subID := sub.ID
		if err := database.WriteBillingEntry(tx, database.BillingEntryInput{
			UserID:               sub.UserID,
			OccurredAt:           now,
			EntryType:            database.BillingTypeRefundSub,
			AmountUSD:            refundAmountMicro,
			BalanceAfterUSD:      freshUser.Quota,
			RelatedType:          "subscription_refund",
			RelatedID:            sub.ID,
			SourceSubscriptionID: &subID,
			Description:          desc,
		}); err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "unique") {
				return errSubRefundDuplicate
			}
			return fmt.Errorf("write billing refund_sub: %w", err)
		}
		// 业务规则（用户 2026-05-10 第三次反馈定稿）：取消/退款**完全不触碰**优惠券。
		// 已用券永远保持 'used'，admin 想补偿用户应独立走 AdminGrantCoupon 端点。
		// 退款审计只记录"原 sub 当时用了哪张券"作为追溯线索，不做任何状态变更。
		auditDetails, err := json.Marshal(map[string]any{
			"type":              "REFUND_SUBSCRIPTION",
			"sub_id":            sub.ID,
			"amount_micro_usd":  refundAmountMicro, // 精确审计（int64）
			"reason":            req.Reason,
			"prev":              sub.Status,
			"package":           sub.PackageID,
			"applied_coupon_id": sub.AppliedCouponID, // 仅信息：该 sub 当时用过哪张券（保持 used，不恢复）
		})
		if err != nil {
			return fmt.Errorf("marshal audit details: %w", err)
		}
		return LogOperationByTx(tx, op.ID, sub.UserID, "admin", "REFUND_SUBSCRIPTION", c.IP(), string(auditDetails))
	})

	if txErr != nil {
		if errors.Is(txErr, errSubRefundDuplicate) {
			return c.Status(409).JSON(fiber.Map{
				"success":      false,
				"message":      "该订阅退款已入账，请勿重复提交",
				"message_code": "ERR_SUB_REFUND_DUPLICATE",
			})
		}
		if errors.Is(txErr, errSubStateMachineMiss) {
			return c.Status(409).JSON(fiber.Map{
				"success":      false,
				"message":      "订阅状态不可退款（已退款 / 状态已变化，请刷新后重试）",
				"message_code": "ERR_SUB_STATUS_NOT_REFUNDABLE",
			})
		}
		log.Printf("[SUB-REFUND] tx failed admin=%d sub=%d err=%v", op.ID, sub.ID, txErr)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_TRANSACTION"})
	}

	proxy.InvalidateUserSubscriptionCache(sub.UserID)
	proxy.RefreshUserAuth(sub.UserID)

	// fix Major（codex r10 + regression）：本轮重构去掉了退款通知的 Dispatch 调用，
	// 用户被退款但完全无感知。恢复异步通知 — admin 已手动协商，用户必须收到回执。
	pkgName := readPackageNameFromSnapshot(sub.PackageSnapshot)
	if pkgName == "" {
		var pkg database.Package
		if database.DB.Select("id, name").First(&pkg, sub.PackageID).Error == nil {
			pkgName = pkg.Name
		}
	}
	title := readSysConfigCached("notif_refund_title", "退款已到账")
	bodyTpl := readSysConfigCached("notif_refund_body", "「{package_name}」已退款 {amount} {currency}，到账您的余额。")
	body := strings.ReplaceAll(bodyTpl, "{package_name}", pkgName)
	body = strings.ReplaceAll(body, "{amount}", database.FormatMicroUSD(refundAmountMicro))
	body = strings.ReplaceAll(body, "{currency}", "USD")
	dedupKey := fmt.Sprintf("refund:sub_%d", sub.ID)
	proxy.Dispatch(sub.UserID, "refund", "success", title, body,
		proxy.LinkUpgradeMine(), "查看", "subscription", sub.ID, &dedupKey)

	return c.JSON(fiber.Map{
		"success":          true,
		"refund_micro_usd": refundAmountMicro,
		"message_code":     "SUCCESS_REFUNDED",
	})
}

// AdminRevokeGrantedSubscription POST /api/admin/subscriptions/:id/revoke-grant
//
// 业务模型：管理员赠送出去的是免费权益，不存在退款；如果发错或内测需要回收，
// 只能走本接口把赠送权益置为 revoked。
//
// 状态机：
//   - active / paused → revoked（终态）
//   - paid subscription / canceled / expired / refunded / revoked → 拒绝
//
// 原子性：
//   - 事务内锁 user 行，与购买/退款/消费串行化
//   - 条件 UPDATE 同时检查 is_granted=true + status IN(active, paused)
//   - 写 0 金额账单和 OperationLog；不触碰 user.quota
func AdminRevokeGrantedSubscription(c *fiber.Ctx) error {
	op := loadAdminUser(c)
	if op == nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil || id <= 0 {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_PARAMS"})
	}
	var req adminRevokeGrantedSubscriptionRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_PARSE_PAYLOAD"})
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message":      "reason 必填（用于审计 / 用户客服查询）",
			"message_code": "ERR_REASON_REQUIRED",
		})
	}
	if runeLen := len([]rune(reason)); runeLen > grantReasonMaxLen {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message":      fmt.Sprintf("reason 长度不能超过 %d 字符（当前 %d）", grantReasonMaxLen, runeLen),
			"message_code": "ERR_REASON_TOO_LONG",
		})
	}
	for _, r := range reason {
		if unicode.IsControl(r) {
			return c.Status(400).JSON(fiber.Map{
				"success":      false,
				"message":      "reason 不能包含控制字符（换行 / 制表符 / NUL / ESC 等）",
				"message_code": "ERR_REASON_CTRL_CHAR",
			})
		}
	}

	var sub database.UserSubscription
	if err := database.DB.First(&sub, id).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"success": false, "message_code": "ERR_NOT_FOUND"})
	}
	if !sub.IsGranted {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message":      "只能收回管理员赠送的订阅",
			"message_code": "ERR_REVOKE_NOT_GRANTED",
		})
	}
	if sub.Status != "active" && sub.Status != "paused" {
		return c.Status(409).JSON(fiber.Map{
			"success":      false,
			"message":      "该赠送权益当前状态不可收回（可能已取消、过期、退款或已收回）",
			"message_code": "ERR_REVOKE_GRANTED_STATUS",
		})
	}

	now := time.Now()
	pkgName := readPackageNameFromSnapshot(sub.PackageSnapshot)
	if pkgName == "" {
		pkgName = fmt.Sprintf("套餐#%d", sub.PackageID)
	}
	txErr := database.DB.Transaction(func(tx *gorm.DB) error {
		if err := lockUserForUpdate(tx, sub.UserID); err != nil {
			return fmt.Errorf("lock user: %w", err)
		}

		// AdminRefundSubscription 不做这步是因为它的状态机靠条件 UPDATE 一步完成；
		// 本接口需要在 UPDATE 前先读 fresh IsGranted/Status 决定 sentinel 错误码（区分
		// "已不是 granted 了" vs "状态不在允许集"），所以必须先持锁读后再 UPDATE。
		// FOR UPDATE 在 SQLite 上是 no-op（GORM 无害降级）但在 PG/MySQL 上锁定 sub 行。
		var lockedSub database.UserSubscription
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&lockedSub, sub.ID).Error; err != nil {
			return fmt.Errorf("lock sub: %w", err)
		}
		if !lockedSub.IsGranted {
			return errRevokeGrantNotGranted
		}
		if lockedSub.Status != "active" && lockedSub.Status != "paused" {
			return errRevokeGrantBadStatus
		}
		pkgName = readPackageNameFromSnapshot(lockedSub.PackageSnapshot)
		if pkgName == "" {
			pkgName = fmt.Sprintf("套餐#%d", lockedSub.PackageID)
		}

		res := tx.Model(&database.UserSubscription{}).
			Where("id = ? AND is_granted = ? AND status IN ?", lockedSub.ID, true, []string{"active", "paused"}).
			Updates(map[string]any{
				"status":      "revoked",
				"canceled_at": gorm.Expr("CASE WHEN canceled_at IS NULL THEN ? ELSE canceled_at END", now),
			})
		if res.Error != nil {
			return fmt.Errorf("update granted sub status: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return errSubStateMachineMiss
		}

		var freshUser database.User
		if err := tx.Select("id, quota").First(&freshUser, lockedSub.UserID).Error; err != nil {
			return fmt.Errorf("fetch fresh quota: %w", err)
		}
		if err := database.WriteBillingEntry(tx, database.BillingEntryInput{
			UserID:          lockedSub.UserID,
			OccurredAt:      now,
			EntryType:       database.BillingTypeAdminRevokeGrant,
			AmountUSD:       0,
			BalanceAfterUSD: freshUser.Quota,
			RelatedType:     "subscription",
			RelatedID:       lockedSub.ID,
			Description:     fmt.Sprintf("管理员收回赠送「%s」 · admin#%d · %s", pkgName, op.ID, reason),
		}); err != nil {
			return fmt.Errorf("write billing revoke grant: %w", err)
		}

		auditDetails, err := json.Marshal(map[string]any{
			"type":         "REVOKE_GRANTED_SUBSCRIPTION",
			"sub_id":       lockedSub.ID,
			"user_id":      lockedSub.UserID,
			"package_id":   lockedSub.PackageID,
			"package_name": pkgName,
			"reason":       reason,
			"prev":         lockedSub.Status,
		})
		if err != nil {
			return fmt.Errorf("marshal audit details: %w", err)
		}
		return LogOperationByTx(tx, op.ID, lockedSub.UserID, "admin", "REVOKE_GRANTED_SUBSCRIPTION", c.IP(), string(auditDetails))
	})

	if txErr != nil {
		switch {
		case errors.Is(txErr, errRevokeGrantNotGranted):
			return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_REVOKE_NOT_GRANTED"})
		case errors.Is(txErr, errRevokeGrantBadStatus), errors.Is(txErr, errSubStateMachineMiss):
			return c.Status(409).JSON(fiber.Map{"success": false, "message_code": "ERR_REVOKE_GRANTED_STATUS"})
		default:
			log.Printf("[SUB-REVOKE-GRANT] tx failed admin=%d sub=%d err=%v", op.ID, sub.ID, txErr)
			return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_TRANSACTION"})
		}
	}

	proxy.InvalidateUserSubscriptionCache(sub.UserID)
	proxy.RefreshUserAuth(sub.UserID)
	dedupKey := fmt.Sprintf("revoke-grant:sub_%d", sub.ID)
	proxy.Dispatch(
		sub.UserID,
		"system",
		"warning",
		"赠送权益已收回",
		fmt.Sprintf("管理员已收回赠送的「%s」。如有疑问，请提交工单。", pkgName),
		proxy.LinkTickets(),
		"提交工单",
		"subscription",
		sub.ID,
		&dedupKey,
	)

	return c.JSON(fiber.Map{
		"success":      true,
		"message_code": "SUCCESS_GRANT_REVOKED",
	})
}

// adminSubItem 是 admin 订阅总览的扁平化行——含用户身份、套餐名、价格、剩余天数、消费率，
// 让 admin 在协商退款时一屏看到所有关键信息，不必去 join 多个表。
//
// fix Major（产品反馈第十一/十二轮）：
//   - 第十一轮：原 AdminListSubscriptions 只返回 raw UserSubscription，admin 必须人肉对照 users 表 + 解 snapshot
//   - 第十二轮：包月套餐**按剩余天数**计算退款是行业惯例。原把消费率 (usage_max_pct) 当主要决策依据是错的——
//     消费率反映 plan 的"周期内最大用量"（如每月 100 万 token），但退款是看"还剩多少天"。
//     现在把"剩余天数 + 建议退款金额"作为主决策字段，消费率仅作辅助参考（识别"已耗尽"边缘场景）。
type adminSubItem struct {
	ID                uint       `json:"id"`
	UserID            uint       `json:"user_id"`
	Username          string     `json:"username"`
	UserPhone         string     `json:"user_phone"` // 已 maskPhone 脱敏
	UserGithubID      string     `json:"user_github_id"`
	PackageID         uint       `json:"package_id"`
	PackageName       string     `json:"package_name"`        // 从 snapshot 提取（套餐改名/删除后仍准确）
	ProductType       string     `json:"product_type"`        // 始终是 subscription
	PurchasedPriceUSD float64    `json:"purchased_price_usd"` // 从 snapshot 提取购买时价格
	Status            string     `json:"status"`              // active | canceled | expired | refunded | paused | revoked
	StartAt           time.Time  `json:"start_at"`
	EndAt             time.Time  `json:"end_at"`
	CanceledAt        *time.Time `json:"canceled_at"`

	// ★★★ 退款决策主字段 ★★★
	// TotalDays / RemainingDays / TimeRemainingPct：基于订阅时间窗口计算
	// SuggestedRefundUSD：按 remaining_days/total_days 比例 × 购买价的建议退款金额，admin 可在此基础上调整
	TotalDays          float64 `json:"total_days"`           // 订阅总天数（EndAt-StartAt）
	RemainingDays      float64 `json:"remaining_days"`       // 剩余天数（EndAt-now，已过期则 0）
	TimeRemainingPct   float64 `json:"time_remaining_pct"`   // 剩余时间百分比（0-100）
	SuggestedRefundUSD float64 `json:"suggested_refund_usd"` // 建议退款金额（按时间比例算）

	// 消费率：辅助信息——识别"已耗尽却时间剩余"的边缘场景，避免按时间退款被套利
	// 用户的 plan 可能配了"每 N 小时/天/周/月"的最大用量限制（rolling window），
	// usage_max_pct 反映这些 plan 中已消耗最多的那个的当前 consumed/limit 比例
	UsageMaxPct      float64 `json:"usage_max_pct"`
	UsageDetailsJSON string  `json:"usage_details_json"` // 各 plan 的 consumed/limit/unit JSON，便于面板展开

	// 赠送相关字段（IsGranted=true 时不可退款；前端用此渲染"赠送"标记 + 禁用退款按钮）
	IsGranted   bool   `json:"is_granted"`
	GrantReason string `json:"grant_reason,omitempty"`

	// fix MAJOR R23+2 第三轮（codex）：AdminListSubscriptions 返回 AppliedCouponID 让 admin
	// 在退款前能看到"这份订阅当时用过哪张券"作为补偿决策辅助。退款本身不触碰券（用户业务规则定稿）。
	AppliedCouponID uint `json:"applied_coupon_id,omitempty"`
}

// AdminListSubscriptions admin 看所有订阅总览。支持分页 + 多维过滤：
//
//	?page=1&page_size=50&user_id=&status=&package_id=&q=（用户名/手机号模糊匹配）
func AdminListSubscriptions(c *fiber.Ctx) error {
	page, _ := strconv.Atoi(c.Query("page", "1"))
	if page < 1 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(c.Query("page_size", "50"))
	if pageSize < 1 || pageSize > 200 {
		pageSize = 50
	}

	q := database.DB.Model(&database.UserSubscription{})
	if v := c.Query("user_id"); v != "" {
		q = q.Where("user_id = ?", v)
	}
	if v := c.Query("package_id"); v != "" {
		q = q.Where("package_id = ?", v)
	}
	if v := c.Query("status"); v != "" {
		switch v {
		case "active", "expired", "canceled", "refunded", "paused", "revoked":
			q = q.Where("status = ?", v)
		default:
			return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_BAD_STATUS"})
		}
	}
	// 通过用户名/手机号模糊匹配筛选（先查用户 IDs 再 Where IN，跨方言安全）
	// fix Minor（codex + gemini r11）：
	//   1) 限长 64 防"100KB _ 通配符" DoS（codex 攻击场景）
	//   2) 转义 % _ 防 catch-all 慢查询
	//   3) 至少 2 字符（gemini 建议防 q=% / q=_ 全表扫描）
	if qStr := strings.TrimSpace(c.Query("q")); qStr != "" {
		if len(qStr) > 64 {
			return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_QUERY_TOO_LONG"})
		}
		// 太短拒绝，避免触发全表 LIKE 扫描
		if len([]rune(qStr)) < 2 {
			return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_QUERY_TOO_SHORT", "message": "搜索关键字至少 2 个字符"})
		}
		// 转义 SQL LIKE 元字符 % 和 _，防止 admin 误填或恶意构造 catch-all
		escaped := strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(qStr)
		var uids []uint
		like := "%" + escaped + "%"
		// fix Minor（codex 第十六轮）：Pluck 错误必须冒泡，DB 故障时不能让 admin 看到"无结果"
		// 假象（实际是查不到）。
		if err := database.DB.Model(&database.User{}).
			Where(`username LIKE ? ESCAPE '\' OR phone LIKE ? ESCAPE '\' OR github_id LIKE ? ESCAPE '\'`, like, like, like).
			Limit(500).
			Pluck("id", &uids).Error; err != nil {
			log.Printf("[ADMIN-SUBS] user search failed q=%q: %v", qStr, err)
			return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
		}
		if len(uids) == 0 {
			return c.JSON(fiber.Map{"success": true, "data": []adminSubItem{}, "meta": fiber.Map{"total": 0, "page": page, "page_size": pageSize}})
		}
		q = q.Where("user_id IN ?", uids)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		log.Printf("[ADMIN-SUBS] count failed: %v", err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}

	var rows []database.UserSubscription
	if err := q.Order("id desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		log.Printf("[ADMIN-SUBS] find failed: %v", err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}

	// 批量预加载 users + usages，避免 N+1
	userIDSet := make(map[uint]struct{}, len(rows))
	subIDs := make([]uint, 0, len(rows))
	for _, r := range rows {
		userIDSet[r.UserID] = struct{}{}
		subIDs = append(subIDs, r.ID)
	}
	userIDs := make([]uint, 0, len(userIDSet))
	for id := range userIDSet {
		userIDs = append(userIDs, id)
	}
	// fix CRITICAL（自审第十三轮）：原 Find 调用未检 .Error。
	// DB 故障 → users / allUsages 全空 map → admin panel 看到所有 username 空、
	// UsageMaxPct=0、SuggestedRefundUSD=$0 → admin 误以为"用户已用完"批 $0 退款。
	// fail-closed：任一查询失败立即 500，不渲染半成品给 admin 做决策。
	var users []database.User
	if len(userIDs) > 0 {
		if err := database.DB.Select("id, username, phone, github_id").Where("id IN ?", userIDs).Find(&users).Error; err != nil {
			log.Printf("[ADMIN-SUBS] users batch load failed: %v", err)
			return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
		}
	}
	userByID := make(map[uint]database.User, len(users))
	for _, u := range users {
		userByID[u.ID] = u
	}

	var allUsages []database.SubscriptionUsage
	if len(subIDs) > 0 {
		if err := database.DB.Where("subscription_id IN ?", subIDs).Find(&allUsages).Error; err != nil {
			log.Printf("[ADMIN-SUBS] usages batch load failed: %v", err)
			return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
		}
	}
	usagesBySubID := make(map[uint][]database.SubscriptionUsage, len(rows))
	for _, u := range allUsages {
		usagesBySubID[u.SubscriptionID] = append(usagesBySubID[u.SubscriptionID], u)
	}

	now := time.Now()
	out := make([]adminSubItem, 0, len(rows))
	for _, sub := range rows {
		item := adminSubItem{
			ID:              sub.ID,
			UserID:          sub.UserID,
			PackageID:       sub.PackageID,
			Status:          sub.Status,
			StartAt:         sub.StartAt,
			EndAt:           sub.EndAt,
			CanceledAt:      sub.CanceledAt,
			IsGranted:       sub.IsGranted,
			GrantReason:     sub.GrantReason,
			AppliedCouponID: sub.AppliedCouponID, // R23+2 第三轮：让 admin 看到"该 sub 用过哪张券"
		}
		// 计算时间相关字段（退款决策主字段）
		const secsPerDay = 86400.0
		totalSec := sub.EndAt.Sub(sub.StartAt).Seconds()
		if totalSec <= 0 {
			totalSec = 1 // 防 0 除
		}
		item.TotalDays = totalSec / secsPerDay
		// 已 canceled / refunded / revoked 的订阅，"剩余"以终止时刻为准；active 才以 now 计
		anchor := now
		if sub.CanceledAt != nil && (sub.Status == "canceled" || sub.Status == "refunded" || sub.Status == "revoked") {
			anchor = *sub.CanceledAt
		}
		remainSec := sub.EndAt.Sub(anchor).Seconds()
		if remainSec < 0 {
			remainSec = 0
		}
		item.RemainingDays = remainSec / secsPerDay
		item.TimeRemainingPct = remainSec / totalSec * 100
		if u, ok := userByID[sub.UserID]; ok {
			item.Username = u.Username
			item.UserPhone = maskPhone(u.Phone)
			item.UserGithubID = u.GithubID
		}
		// 从 snapshot 解出 package_name / product_type / plans
		var snap struct {
			PackageName string `json:"package_name"`
			ProductType string `json:"product_type"`
			Plans       []struct {
				ID                 uint    `json:"id"`
				Name               string  `json:"name"`
				LimitUnit          string  `json:"limit_unit"`
				LimitValue         float64 `json:"limit_value"`
				LimitValueMicroUSD int64   `json:"limit_value_micro_usd"`
				WindowSeconds      int     `json:"window_seconds"`
				ExtraConfig        string  `json:"extra_config"`
				QuantityMultiplier float64 `json:"quantity_multiplier"`
			} `json:"plans"`
		}
		if err := json.Unmarshal([]byte(sub.PackageSnapshot), &snap); err == nil {
			item.PackageName = snap.PackageName
			item.ProductType = snap.ProductType
			// fix CRITICAL R23+2-C1（codex 全方面审查 第二轮）：
			// 优先用 sub.PurchasedUnitPriceUSD（实际成交价含券折扣），snapshot 仅作"展示原价"用。
			// 免费券 sub 的 PurchasedUnitPriceUSD=0 → 建议退款金额 0 → admin 看不到误导值。
			if sub.PurchasedUnitPriceUSD > 0 {
				item.PurchasedPriceUSD = database.MicroToUSD(sub.PurchasedUnitPriceUSD)
			} else {
				// 免费券或赠送 sub：实际未付费，建议退款 0
				item.PurchasedPriceUSD = 0
			}
		} else {
			// fix Minor（自审第十三轮）：原 unmarshal 错误完全静默——admin 看到 $0 建议退款
			// 完全无线索为何。加日志让 admin 能从服务器 log 检索到 snapshot 损坏。
			log.Printf("[ADMIN-SUBS] sub %d snapshot unmarshal failed: %v (PackageName/Price 退化为零值)", sub.ID, err)
		}
		// 计算消费率：取所有限额 plan 中 consumed/limit 最大的那个
		usages := usagesBySubID[sub.ID]
		usageRowsByPlan := make(map[uint][]database.SubscriptionUsage, len(usages))
		for _, u := range usages {
			usageRowsByPlan[u.QuotaPlanID] = append(usageRowsByPlan[u.QuotaPlanID], u)
		}
		maxPct := 0.0
		usageDetails := make([]map[string]any, 0, len(snap.Plans))
		for _, p := range snap.Plans {
			mult := p.QuantityMultiplier
			if mult <= 0 {
				mult = 1
			}
			effectiveLimit := p.LimitValue * mult
			if p.LimitUnit == "api_cost_usd" {
				// 同 line 587-595：旧快照 LimitValueMicroUSD=0 时 fallback
				limitMicro := p.LimitValueMicroUSD
				if limitMicro == 0 && p.LimitValue > 0 {
					if m, ok := database.USDToMicro(p.LimitValue); ok {
						limitMicro = m
					}
				}
				effectiveLimit = database.MicroToUSD(scaleMicroByFloatForDisplay(limitMicro, mult))
			}
			consumed := 0.0
			requestCount := int64(0)
			var latestUsage *database.SubscriptionUsage
			for i := range usageRowsByPlan[p.ID] {
				usage := &usageRowsByPlan[p.ID][i]
				displayConsumed, displayCount, active := subscriptionUsageValueForDisplay(*usage, p.LimitUnit, p.WindowSeconds, now)
				if !active {
					continue
				}
				consumed += displayConsumed
				requestCount += displayCount
				// 多 bucket 时取 window_end_at 最近（最新）的一条作为 admin 展示参考。
				if latestUsage == nil ||
					(!usage.WindowEndAt.IsZero() && (latestUsage.WindowEndAt.IsZero() || usage.WindowEndAt.After(latestUsage.WindowEndAt))) {
					latestUsage = usage
				}
			}
			d := map[string]any{
				"plan_id":        p.ID,
				"name":           p.Name,
				"unit":           p.LimitUnit,
				"limit":          effectiveLimit,
				"window_seconds": p.WindowSeconds,
				"extra_config":   p.ExtraConfig,
				"consumed":       consumed,
				"request_count":  requestCount,
			}
			// 补当前活跃窗口信息；已过期窗口在展示层视为可用新窗口，运行时仍由首次请求开窗。
			if usage := latestUsage; usage != nil {
				if !usage.WindowStartAt.IsZero() {
					d["window_start_at"] = usage.WindowStartAt
				}
				if !usage.WindowEndAt.IsZero() {
					d["window_end_at"] = usage.WindowEndAt
				}
			}
			if effectiveLimit > 0 {
				pct := consumed / effectiveLimit * 100
				if pct > maxPct {
					maxPct = pct
				}
				d["pct"] = pct
			}
			usageDetails = append(usageDetails, d)
		}
		item.UsageMaxPct = maxPct
		if b, err := json.Marshal(usageDetails); err == nil {
			item.UsageDetailsJSON = string(b)
		}

		// Phase 8：所有套餐都是 subscription，按时间比例退款
		if sub.Status != "refunded" {
			if item.PurchasedPriceUSD > 0 {
				ratio := item.TimeRemainingPct / 100.0
				suggested := item.PurchasedPriceUSD * ratio
				item.SuggestedRefundUSD = float64(int(suggested*100)) / 100
			}
		}
		out = append(out, item)
	}

	return c.JSON(fiber.Map{
		"success": true,
		"data":    out,
		"meta":    fiber.Map{"total": total, "page": page, "page_size": pageSize},
	})
}
