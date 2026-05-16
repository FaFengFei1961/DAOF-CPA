package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"daof-ai-hub/database"
	"daof-ai-hub/proxy"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

type AdminResetUsagePayload struct {
	PackageIDs []uint   `json:"package_ids"`
	UserIDs    []uint   `json:"user_ids"`
	Statuses   []string `json:"statuses"`
	Confirm    string   `json:"confirm"`
	Note       string   `json:"note"`
}

const (
	resetUsageConfirmPhrase = "YES_RESET_USAGE"
	resetUsageNoteMaxLen    = 500
	resetUsageScopeMax      = 10000
)

var errResetUsageScopeTooLarge = errors.New("subscription usage reset scope too large")

// AdminResetSubscriptionUsage POST /api/admin/subscriptions/reset-usage
//
// 批量重置订阅当前窗口已用额度，不移动 WindowStartAt / WindowEndAt。
// 每个匹配订阅写一条 0 金额 admin_adjust 账单，一批操作写一条聚合 OperationLog。
func AdminResetSubscriptionUsage(c *fiber.Ctx) error {
	startedAt := time.Now()
	op := loadAdminUser(c)
	if op == nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}

	var req AdminResetUsagePayload
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_PARSE_PAYLOAD"})
	}
	if req.Confirm != resetUsageConfirmPhrase {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_RESET_CONFIRM_REQUIRED"})
	}

	note := strings.TrimSpace(req.Note)
	if note == "" {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_RESET_NOTE_REQUIRED"})
	}
	if len([]rune(note)) > resetUsageNoteMaxLen {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_RESET_NOTE_TOO_LONG"})
	}

	packageIDs := normalizeResetUsageIDs(req.PackageIDs)
	userIDs := normalizeResetUsageIDs(req.UserIDs)
	statuses, defaultActiveWindow, err := normalizeResetUsageStatuses(req.Statuses)
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_BAD_STATUS"})
	}

	now := time.Now()
	var (
		resetCount       int64
		updatedUsageRows int64
		affectedUserIDs  []uint
	)

	txErr := database.DB.Transaction(func(tx *gorm.DB) error {
		scope := applyResetUsageScope(tx, packageIDs, userIDs, statuses, defaultActiveWindow, now)

		var matchedCount int64
		if err := scope.Count(&matchedCount).Error; err != nil {
			return fmt.Errorf("count reset usage scope: %w", err)
		}
		if matchedCount > resetUsageScopeMax {
			resetCount = matchedCount
			return errResetUsageScopeTooLarge
		}

		var subs []database.UserSubscription
		if err := applyResetUsageScope(tx, packageIDs, userIDs, statuses, defaultActiveWindow, now).
			Select("id, user_id, package_id, status").
			Order("id ASC").
			Find(&subs).Error; err != nil {
			return fmt.Errorf("load reset usage subscriptions: %w", err)
		}
		resetCount = int64(len(subs))

		subIDs := make([]uint, 0, len(subs))
		uniqueUserIDs := make([]uint, 0, len(subs))
		seenUsers := make(map[uint]struct{}, len(subs))
		for _, sub := range subs {
			subIDs = append(subIDs, sub.ID)
			if _, ok := seenUsers[sub.UserID]; ok {
				continue
			}
			seenUsers[sub.UserID] = struct{}{}
			uniqueUserIDs = append(uniqueUserIDs, sub.UserID)
		}
		affectedUserIDs = uniqueUserIDs

		userQuotaByID := make(map[uint]int64, len(uniqueUserIDs))
		if len(uniqueUserIDs) > 0 {
			var users []database.User
			if err := tx.Select("id, quota").Where("id IN ?", uniqueUserIDs).Find(&users).Error; err != nil {
				return fmt.Errorf("load reset usage user quotas: %w", err)
			}
			for _, user := range users {
				userQuotaByID[user.ID] = user.Quota
			}
		}

		existingResetBillingBySubID := make(map[uint]struct{}, len(subs))
		if len(subIDs) > 0 {
			var existingRelatedIDs []uint
			if err := tx.Model(&database.BillingEntry{}).
				Where("entry_type = ? AND related_type = ? AND related_id IN ?",
					database.BillingTypeAdminAdjust, "subscription_usage_reset", subIDs).
				Pluck("related_id", &existingRelatedIDs).Error; err != nil {
				return fmt.Errorf("load existing reset usage billing relations: %w", err)
			}
			for _, id := range existingRelatedIDs {
				existingResetBillingBySubID[id] = struct{}{}
			}
		}

		billingOccurredAt := now
		for _, sub := range subs {
			res := tx.Model(&database.SubscriptionUsage{}).
				Where("subscription_id = ?", sub.ID).
				Updates(map[string]any{
					"consumed_value":           0,
					"consumed_value_micro_usd": 0,
					"request_count":            0,
					"updated_at":               now,
				})
			if res.Error != nil {
				return fmt.Errorf("reset usage sub=%d: %w", sub.ID, res.Error)
			}
			updatedUsageRows += res.RowsAffected

			relatedID := sub.ID
			if _, exists := existingResetBillingBySubID[sub.ID]; exists {
				relatedID = 0
			}
			if err := database.WriteBillingEntry(tx, database.BillingEntryInput{
				UserID:          sub.UserID,
				OccurredAt:      billingOccurredAt,
				EntryType:       database.BillingTypeAdminAdjust,
				AmountUSD:       0,
				BalanceAfterUSD: userQuotaByID[sub.UserID],
				RelatedType:     "subscription_usage_reset",
				RelatedID:       relatedID,
				Description:     fmt.Sprintf("[%s] 重置已用额度 by admin#%d", note, op.ID),
			}); err != nil {
				return fmt.Errorf("write reset usage billing sub=%d: %w", sub.ID, err)
			}
			billingOccurredAt = billingOccurredAt.Add(time.Microsecond)
		}

		detailsJSON, err := json.Marshal(map[string]any{
			"matched_count":      resetCount,
			"updated_usage_rows": updatedUsageRows,
			"package_ids":        packageIDs,
			"user_ids":           userIDs,
			"statuses":           statuses,
			"note":               note,
		})
		if err != nil {
			return fmt.Errorf("marshal reset usage audit details: %w", err)
		}
		return LogOperationByTx(tx, op.ID, 0, "admin", "SUBSCRIPTION_USAGE_RESET", c.IP(), string(detailsJSON))
	})

	elapsed := time.Since(startedAt)
	if errors.Is(txErr, errResetUsageScopeTooLarge) {
		log.Printf("[SUB-USAGE-RESET] rejected scope too large admin=%d matched=%d elapsed=%s",
			op.ID, resetCount, elapsed)
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_RESET_SCOPE_TOO_LARGE"})
	}
	if txErr != nil {
		log.Printf("[SUB-USAGE-RESET] tx failed admin=%d elapsed=%s err=%v", op.ID, elapsed, txErr)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_TRANSACTION"})
	}

	for _, uid := range affectedUserIDs {
		proxy.InvalidateUserSubscriptionCache(uid)
	}
	log.Printf("[SUB-USAGE-RESET] success admin=%d reset_count=%d usage_rows=%d elapsed=%s",
		op.ID, resetCount, updatedUsageRows, elapsed)

	return c.JSON(fiber.Map{
		"success":      true,
		"reset_count":  resetCount,
		"message_code": "SUCCESS_USAGE_RESET",
	})
}

func normalizeResetUsageIDs(in []uint) []uint {
	out := make([]uint, 0, len(in))
	seen := make(map[uint]struct{}, len(in))
	for _, id := range in {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func normalizeResetUsageStatuses(in []string) ([]string, bool, error) {
	statuses := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, raw := range in {
		status := strings.TrimSpace(strings.ToLower(raw))
		if status == "" {
			continue
		}
		if !isResetUsageStatusAllowed(status) {
			return nil, false, fmt.Errorf("bad status %q", raw)
		}
		if _, ok := seen[status]; ok {
			continue
		}
		seen[status] = struct{}{}
		statuses = append(statuses, status)
	}
	if len(statuses) == 0 {
		return []string{"active"}, true, nil
	}
	return statuses, false, nil
}

func isResetUsageStatusAllowed(status string) bool {
	switch status {
	case "active", "expired", "canceled", "refunded", "paused", "revoked":
		return true
	default:
		return false
	}
}

func applyResetUsageScope(tx *gorm.DB, packageIDs, userIDs []uint, statuses []string, defaultActiveWindow bool, now time.Time) *gorm.DB {
	q := tx.Model(&database.UserSubscription{}).Where("status IN ?", statuses)
	if defaultActiveWindow {
		q = q.Where("end_at > ?", now)
	}
	if len(packageIDs) > 0 {
		q = q.Where("package_id IN ?", packageIDs)
	}
	if len(userIDs) > 0 {
		q = q.Where("user_id IN ?", userIDs)
	}
	return q
}
