// Package controller / billing_reconcile.go
//
// 账单对账 admin endpoint (Sprint5-M8)。
//
// 状态机：BillingEntry.BillingState='pending_reconcile' / 'upstream_unmetered'
//         → admin POST /api/admin/billing/:id/reconcile  → INSERT BillingReconciliation
//
// 设计：
//   - 一笔账单只能被对账一次（DB unique 约束兜底）
//   - reconcile 决策结果三选一：absorbed（平台吸收）/ charged（补扣用户）/ voided（作废）
//   - 不修改原 BillingEntry（append-only）；charged 时写新的 admin_adjust 反向账单
//   - 所有操作落 OperationLog 审计链
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

// reconcileBillingPayload admin 提交的对账决策
type reconcileBillingPayload struct {
	Result string `json:"result"` // absorbed / charged / voided
	Note   string `json:"note"`   // 必填，至少描述决策原因
}

// errReconcileNotPending 哨兵：尝试对账已 settled 的账单
var errReconcileNotPending = errors.New("billing entry is not in pending state")

// errReconcileAlreadyDone 哨兵：该 entry 已被对账过（unique 约束触发）
var errReconcileAlreadyDone = errors.New("billing entry already reconciled")

const reconcileNoteMaxLen = 500

// AdminReconcileBillingEntry POST /api/admin/billing/:id/reconcile
//
// 把 pending_reconcile / upstream_unmetered 状态的账单标记为已处理。
// 决策结果三选一：
//   - absorbed: 平台吸收，不动用户余额（仅记 reconciliation 行）
//   - charged:  admin 决定补扣用户余额（写 admin_adjust 反向账单 + reconciliation 行）
//   - voided:   该 pending entry 视为无效（仅记 reconciliation 行）
//
// 整个流程单事务原子：reconciliation INSERT + 可选 adjust BillingEntry INSERT + OperationLog。
// 任何步骤失败整批回滚。
func AdminReconcileBillingEntry(c *fiber.Ctx) error {
	op := loadAdminUser(c)
	if op == nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}

	id, parseErr := strconv.Atoi(c.Params("id"))
	if parseErr != nil || id <= 0 {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_PARAMS"})
	}

	var req reconcileBillingPayload
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_BAD_REQUEST"})
	}
	if !database.IsKnownReconcileResult(req.Result) {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message":      "result 只能是 absorbed / charged / voided",
			"message_code": "ERR_RECONCILE_RESULT_INVALID",
		})
	}
	note := strings.TrimSpace(req.Note)
	if note == "" {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message":      "对账必须填写决策说明",
			"message_code": "ERR_RECONCILE_NOTE_REQUIRED",
		})
	}
	if runeLen := len([]rune(note)); runeLen > reconcileNoteMaxLen {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message":      fmt.Sprintf("note 长度不能超过 %d 字符（当前 %d）", reconcileNoteMaxLen, runeLen),
			"message_code": "ERR_RECONCILE_NOTE_TOO_LONG",
		})
	}
	for _, r := range note {
		if unicode.IsControl(r) {
			return c.Status(400).JSON(fiber.Map{
				"success":      false,
				"message_code": "ERR_REASON_CTRL_CHAR",
			})
		}
	}

	// 加载原 entry 一次快路径（事务内仍会重读 + lock-by-uniqueness）
	var entry database.BillingEntry
	if err := database.DB.First(&entry, id).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"success": false, "message_code": "ERR_NOT_FOUND"})
	}
	if entry.BillingState != database.BillingStatePendingReconcile &&
		entry.BillingState != database.BillingStateUpstreamUnmetered {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message":      fmt.Sprintf("账单当前状态 %q 不可对账（仅 pending_reconcile / upstream_unmetered 可对账）", entry.BillingState),
			"message_code": "ERR_RECONCILE_NOT_PENDING",
		})
	}

	var (
		reconcileID  uint
		adjustEntry  database.BillingEntry
		adjustExists bool
	)

	txErr := database.DB.Transaction(func(tx *gorm.DB) error {
		// 锁住目标用户的所有相关操作（购买/退款/扣费等都走 lockUserForUpdate 同一路径）
		if err := lockUserForUpdate(tx, entry.UserID); err != nil {
			return fmt.Errorf("lock user: %w", err)
		}

		// 事务内重读 entry 防 admin 并发对账
		var fresh database.BillingEntry
		if err := tx.First(&fresh, id).Error; err != nil {
			return fmt.Errorf("re-read entry: %w", err)
		}
		if fresh.BillingState != database.BillingStatePendingReconcile &&
			fresh.BillingState != database.BillingStateUpstreamUnmetered {
			return errReconcileNotPending
		}

		// 若 Result=charged，先写反向 admin_adjust 账单（实际扣 quota）
		if req.Result == database.ReconcileResultCharged {
			// charged 必须有 estimated_cost 才能补扣（pending entry 的 AmountUSD=0，
			// 真正待补扣的金额在 EstimatedCostUSD 字段）
			if fresh.EstimatedCostUSD <= 0 {
				return fmt.Errorf("entry %d has no estimated_cost to charge", fresh.ID)
			}
			// 原子 CAS：扣余额必须 quota >= cost（防打负）
			res := tx.Model(&database.User{}).
				Where("id = ? AND quota >= ?", fresh.UserID, fresh.EstimatedCostUSD).
				UpdateColumn("quota", gorm.Expr("quota - ?", fresh.EstimatedCostUSD))
			if res.Error != nil {
				return fmt.Errorf("charge quota: %w", res.Error)
			}
			if res.RowsAffected == 0 {
				// 余额不足 → 拒绝（admin 可改 result=absorbed）
				return fmt.Errorf("user %d quota insufficient for charged reconcile (need %d micro_usd)", fresh.UserID, fresh.EstimatedCostUSD)
			}
			var freshUser database.User
			if err := tx.Select("id, quota").First(&freshUser, fresh.UserID).Error; err != nil {
				return fmt.Errorf("re-select quota: %w", err)
			}
			adjustEntry = database.BillingEntry{
				UserID:          fresh.UserID,
				OccurredAt:      time.Now(),
				EntryType:       database.BillingTypeAdminAdjust,
				BillingState:    database.BillingStateSettled,
				AmountUSD:       -fresh.EstimatedCostUSD,
				BalanceAfterUSD: freshUser.Quota,
				RelatedType:     "billing_entry",
				RelatedID:       fresh.ID,
				Description:     fmt.Sprintf("对账补扣（reconcile id=%d / 原 pending entry #%d）：%s", 0, fresh.ID, note),
				ModelName:       fresh.ModelName,
				TokensTotal:     fresh.TokensTotal,
			}
			if err := tx.Create(&adjustEntry).Error; err != nil {
				return fmt.Errorf("insert admin_adjust entry: %w", err)
			}
			adjustExists = true
		}

		// 写 reconciliation 行（unique on billing_entry_id 兜底）
		reconcile := database.BillingReconciliation{
			BillingEntryID:           fresh.ID,
			Result:                   req.Result,
			AdjustmentBillingEntryID: 0,
			OperatorID:               op.ID,
			OperatorRole:             "admin",
			Note:                     note,
			CreatedAt:                time.Now(),
		}
		if adjustExists {
			reconcile.AdjustmentBillingEntryID = adjustEntry.ID
		}
		if err := tx.Create(&reconcile).Error; err != nil {
			// unique 违反 → 已被对账过
			if strings.Contains(strings.ToLower(err.Error()), "unique") {
				return errReconcileAlreadyDone
			}
			return fmt.Errorf("insert reconciliation: %w", err)
		}
		reconcileID = reconcile.ID

		// OperationLog 审计
		auditDetails, _ := json.Marshal(map[string]any{
			"type":                       "BILLING_RECONCILE",
			"admin_id":                   op.ID,
			"billing_entry_id":           fresh.ID,
			"reconcile_id":               reconcile.ID,
			"result":                     req.Result,
			"estimated_cost_micro_usd":   fresh.EstimatedCostUSD,
			"adjustment_billing_id":     reconcile.AdjustmentBillingEntryID,
			"note":                       note,
		})
		return LogOperationByTx(tx, op.ID, fresh.UserID, "admin", "BILLING_RECONCILE", c.IP(), string(auditDetails))
	})

	if errors.Is(txErr, errReconcileNotPending) {
		return c.Status(409).JSON(fiber.Map{
			"success":      false,
			"message":      "账单状态已变化（可能被其他 admin 对账过）",
			"message_code": "ERR_RECONCILE_RACED",
		})
	}
	if errors.Is(txErr, errReconcileAlreadyDone) {
		return c.Status(409).JSON(fiber.Map{
			"success":      false,
			"message":      "该账单已被对账过，不能重复处理",
			"message_code": "ERR_RECONCILE_ALREADY_DONE",
		})
	}
	if txErr != nil {
		log.Printf("[BILLING-RECONCILE] tx failed entry=%d admin=%d result=%s: %v",
			id, op.ID, req.Result, txErr)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_TRANSACTION"})
	}

	// 余额变更后刷新用户 auth cache
	if adjustExists {
		proxy.RefreshUserAuth(entry.UserID)
	}

	log.Printf("[BILLING-RECONCILE] OK entry=%d admin=%d result=%s reconcile_id=%d adjust_entry=%d",
		id, op.ID, req.Result, reconcileID, adjustEntry.ID)

	resp := fiber.Map{
		"success":      true,
		"message_code": "SUCCESS_RECONCILED",
		"data": fiber.Map{
			"reconcile_id":     reconcileID,
			"billing_entry_id": id,
			"result":           req.Result,
		},
	}
	if adjustExists {
		resp["data"].(fiber.Map)["adjustment_billing_entry_id"] = adjustEntry.ID
	}
	return c.JSON(resp)
}
