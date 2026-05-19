package controller

import (
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	"daof-cpa/database"
	"daof-cpa/proxy"
	"daof-cpa/utils"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// GetPublicBillingRules exposes the public, auditable charging rules used to
// turn raw API-equivalent cost into subscription credits / balance deductions.
func GetPublicBillingRules(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{
		"success": true,
		"data":    proxy.GetPublicBillingRules(),
	})
}

type billingRuleRevisionResponse struct {
	ID                uint                 `json:"id"`
	Version           string               `json:"version"`
	EffectiveSince    string               `json:"effective_since"`
	PublishedAt       *time.Time           `json:"published_at"`
	EffectiveAt       *time.Time           `json:"effective_at"`
	Status            string               `json:"status"`
	CanceledAt        *time.Time           `json:"canceled_at,omitempty"`
	ModelWeights      []billingRulePayload `json:"model_weights"`
	HealthMultipliers []billingRulePayload `json:"health_multipliers"`
	ModelCount        int                  `json:"model_count"`
	HealthCount       int                  `json:"health_count"`
	Source            string               `json:"source"`
	CreatedAt         time.Time            `json:"created_at"`
}

// GetPublicBillingRuleHistory exposes immutable billing-rule snapshots so users
// can audit how the public deduction rules changed over time.
func GetPublicBillingRuleHistory(c *fiber.Ctx) error {
	limit := c.QueryInt("limit", 20)
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	var rows []database.BillingRuleRevision
	if err := database.DB.Order("created_at DESC, id DESC").Limit(limit).Find(&rows).Error; err != nil {
		log.Printf("[BILLING-RULES-HISTORY] load failed: %v", err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_READ"})
	}
	cancellations := loadBillingRuleCancellations(rows)
	currentRules := proxy.GetPublicBillingRules()
	now := time.Now()
	out := make([]billingRuleRevisionResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, billingRuleRevisionToResponse(row, cancellations[row.ID], currentRules.RevisionID, currentRules.Version, now))
	}
	return c.JSON(fiber.Map{"success": true, "data": out})
}

type billingRulePayload struct {
	Pattern        string  `json:"pattern"`
	Weight         float64 `json:"weight"`
	ThinkingWeight float64 `json:"thinking_weight,omitempty"`
	Label          string  `json:"label,omitempty"`
	Reason         string  `json:"reason,omitempty"`
}

type updateBillingRulesPayload struct {
	Version           string               `json:"version,omitempty"`
	PublishMode       string               `json:"publish_mode,omitempty"`
	EffectiveAt       string               `json:"effective_at,omitempty"`
	ModelWeights      []billingRulePayload `json:"model_weights"`
	HealthMultipliers []billingRulePayload `json:"health_multipliers"`
}

type cancelBillingRuleRevisionPayload struct {
	Reason string `json:"reason,omitempty"`
}

// UpdateBillingRules admin-only。规范化、校验三个 SysConfig（billing_model_weights_json /
// billing_health_multipliers_json / billing_rules_version）→ 事务化加密入库 →
// SyncCacheConfig 让规则立即生效 → 写 OperationLog 审计。
func UpdateBillingRules(c *fiber.Ctx) error {
	var payload updateBillingRulesPayload
	if err := c.BodyParser(&payload); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_PARSE_PAYLOAD"})
	}
	if len(payload.ModelWeights) == 0 {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message":      "model_weights 不能为空（至少保留一条通配规则）",
			"message_code": "ERR_BILLING_RULES_EMPTY",
		})
	}
	if len(payload.ModelWeights) > 200 || len(payload.HealthMultipliers) > 200 {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message":      "model_weights / health_multipliers 每组最多 200 条",
			"message_code": "ERR_BILLING_RULES_TOO_MANY",
		})
	}

	modelClean, code, msg := validateBillingRuleSet(payload.ModelWeights, true /* allowThinking */)
	if code != "" {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": code, "message": msg})
	}
	healthClean, code, msg := validateBillingRuleSet(payload.HealthMultipliers, false)
	if code != "" {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": code, "message": msg})
	}
	// 健康系数至少保留一条全通配规则，避免运行时回到 hardcode default
	if len(healthClean) == 0 {
		healthClean = []billingRulePayload{{Pattern: "*", Weight: 1, Label: "Normal", Reason: "默认无高峰加权"}}
	}

	now := time.Now().UTC()
	version := strings.TrimSpace(payload.Version)
	if version == "" {
		version = fmt.Sprintf("editor-%s", now.Format("2006-01-02-150405"))
	}
	if !billingRuleVersionRe.MatchString(version) {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message":      "version 仅允许字母、数字、点、连字符 (.-)，长度 1-64",
			"message_code": "ERR_BILLING_RULES_VERSION_INVALID",
		})
	}
	publishMode := strings.ToLower(strings.TrimSpace(payload.PublishMode))
	if publishMode == "" {
		publishMode = "immediate"
	}
	effectiveAt, activateNow, code, msg := parseBillingRuleEffectiveAt(publishMode, payload.EffectiveAt, now)
	if code != "" {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": code, "message": msg})
	}

	modelJSON, _ := json.Marshal(modelClean)
	healthJSON, _ := json.Marshal(healthClean)

	operatorID := uint(0)
	if v := c.Locals("admin_user_id"); v != nil {
		if id, ok := v.(uint); ok {
			operatorID = id
		}
	}
	revision, err := persistBillingRulesUpdate(map[string]string{
		proxy.BillingModelWeightsConfigKey:      string(modelJSON),
		proxy.BillingHealthMultipliersConfigKey: string(healthJSON),
		proxy.BillingRulesVersionConfigKey:      version,
		proxy.BillingRulesPublishedAtConfigKey:  now.Format(time.RFC3339),
		proxy.BillingRulesEffectiveAtConfigKey:  effectiveAt.Format(time.RFC3339),
	}, database.BillingRuleRevision{
		Version:               version,
		EffectiveSince:        effectiveAt.Format("2006-01-02"),
		PublishedAt:           &now,
		EffectiveAt:           &effectiveAt,
		ModelWeightsJSON:      string(modelJSON),
		HealthMultipliersJSON: string(healthJSON),
		ModelCount:            len(modelClean),
		HealthCount:           len(healthClean),
		Source:                billingRuleRevisionSource(activateNow),
		CreatedBy:             operatorID,
	}, activateNow)
	if err != nil {
		log.Printf("[BILLING-RULES-ADMIN] persist failed: %v", err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_WRITE"})
	}
	if activateNow {
		proxy.SyncCacheConfig()
	}

	details, _ := json.Marshal([]map[string]any{{
		"type":         billingRuleOperationType(activateNow),
		"version":      version,
		"model_count":  len(modelClean),
		"health_count": len(healthClean),
		"effective_at": effectiveAt.Format(time.RFC3339),
		"revision_id":  revision.ID,
	}})
	LogOperationBy(operatorID, operatorID, "admin", billingRuleOperationType(activateNow), c.IP(), string(details))

	messageCode := "SUCCESS_BILLING_RULES_SAVED"
	if !activateNow {
		messageCode = "SUCCESS_BILLING_RULES_SCHEDULED"
	}

	current := proxy.GetPublicBillingRules()
	return c.JSON(fiber.Map{
		"success":      true,
		"message_code": messageCode,
		"data":         current,
		"revision":     billingRuleRevisionToResponse(revision, nil, current.RevisionID, current.Version, time.Now()),
	})
}

// CancelBillingRuleRevision 撤销尚未生效的预发布版本。历史 revision 保持 append-only，
// 撤销事实写入独立 append-only 表，激活 cron 会自动跳过。
func CancelBillingRuleRevision(c *fiber.Ctx) error {
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil || id <= 0 {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_ID"})
	}
	var payload cancelBillingRuleRevisionPayload
	_ = c.BodyParser(&payload)
	reason := strings.TrimSpace(payload.Reason)
	if len(reason) > 500 {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_BILLING_RULES_CANCEL_REASON_TOO_LONG"})
	}

	var revision database.BillingRuleRevision
	if err := database.DB.First(&revision, uint(id)).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"success": false, "message_code": "ERR_NOT_FOUND"})
	}
	now := time.Now().UTC()
	if revision.EffectiveAt == nil || !revision.EffectiveAt.After(now) {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_BILLING_RULES_CANCEL_NOT_SCHEDULED",
			"message":      "只有尚未生效的预发布版本可以撤销",
		})
	}

	operatorID := uint(0)
	if v := c.Locals("admin_user_id"); v != nil {
		if id, ok := v.(uint); ok {
			operatorID = id
		}
	}
	cancel := database.BillingRuleRevisionCancellation{
		RevisionID: revision.ID,
		Reason:     reason,
		CreatedBy:  operatorID,
	}
	if err := database.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&cancel).Error; err != nil {
		log.Printf("[BILLING-RULES-ADMIN] cancel revision id=%d failed: %v", revision.ID, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_WRITE"})
	}
	if cancel.ID == 0 {
		_ = database.DB.Where("revision_id = ?", revision.ID).First(&cancel).Error
	}

	details, _ := json.Marshal([]map[string]any{{
		"type":        "BILLING_RULES_CANCEL",
		"revision_id": revision.ID,
		"version":     revision.Version,
		"reason":      reason,
	}})
	LogOperationBy(operatorID, operatorID, "admin", "BILLING_RULES_CANCEL", c.IP(), string(details))

	current := proxy.GetPublicBillingRules()
	return c.JSON(fiber.Map{
		"success":      true,
		"message_code": "SUCCESS_BILLING_RULES_CANCELED",
		"revision":     billingRuleRevisionToResponse(revision, &cancel, current.RevisionID, current.Version, time.Now()),
	})
}

// version 字面量校验：与 message_code 守护逻辑同精神——拒绝把不可控字符塞进 audit log。
var billingRuleVersionRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

func parseBillingRuleEffectiveAt(mode, raw string, now time.Time) (time.Time, bool, string, string) {
	switch mode {
	case "immediate":
		return now, true, "", ""
	case "scheduled":
		if strings.TrimSpace(raw) == "" {
			return time.Time{}, false, "ERR_BILLING_RULES_EFFECTIVE_AT_REQUIRED", "预发布必须填写生效时间"
		}
		effectiveAt, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
		if err != nil {
			return time.Time{}, false, "ERR_BILLING_RULES_EFFECTIVE_AT_INVALID", "effective_at 必须是 RFC3339 时间"
		}
		effectiveAt = effectiveAt.UTC()
		if !effectiveAt.After(now.Add(30 * time.Second)) {
			return time.Time{}, false, "ERR_BILLING_RULES_EFFECTIVE_AT_TOO_SOON", "预发布生效时间必须晚于当前时间至少 30 秒"
		}
		return effectiveAt, false, "", ""
	default:
		return time.Time{}, false, "ERR_BILLING_RULES_PUBLISH_MODE_INVALID", "publish_mode 只能是 immediate 或 scheduled"
	}
}

func billingRuleRevisionSource(activateNow bool) string {
	if activateNow {
		return "admin"
	}
	return "admin_scheduled"
}

func billingRuleOperationType(activateNow bool) string {
	if activateNow {
		return "BILLING_RULES_UPDATE"
	}
	return "BILLING_RULES_SCHEDULE"
}

func validateBillingRuleSet(in []billingRulePayload, allowThinking bool) ([]billingRulePayload, string, string) {
	out := make([]billingRulePayload, 0, len(in))
	seen := map[string]struct{}{}
	for i, r := range in {
		pattern := strings.TrimSpace(r.Pattern)
		if pattern == "" {
			return nil, "ERR_BILLING_RULES_PATTERN_EMPTY", fmt.Sprintf("第 %d 条 pattern 不能为空", i+1)
		}
		if len(pattern) > 80 {
			return nil, "ERR_BILLING_RULES_PATTERN_LONG", fmt.Sprintf("第 %d 条 pattern 长度超过 80", i+1)
		}
		lower := strings.ToLower(pattern)
		if _, dup := seen[lower]; dup {
			return nil, "ERR_BILLING_RULES_PATTERN_DUP", fmt.Sprintf("pattern %q 重复", pattern)
		}
		seen[lower] = struct{}{}
		if !(r.Weight > 0 && r.Weight <= 1000) {
			return nil, "ERR_BILLING_RULES_WEIGHT_RANGE", fmt.Sprintf("第 %d 条 weight 必须 > 0 且 ≤ 1000", i+1)
		}
		clean := billingRulePayload{
			Pattern: pattern,
			Weight:  r.Weight,
			Label:   strings.TrimSpace(r.Label),
			Reason:  strings.TrimSpace(r.Reason),
		}
		if allowThinking && r.ThinkingWeight != 0 {
			if !(r.ThinkingWeight > 0 && r.ThinkingWeight <= 1000) {
				return nil, "ERR_BILLING_RULES_THINKING_RANGE",
					fmt.Sprintf("第 %d 条 thinking_weight 必须 > 0 且 ≤ 1000", i+1)
			}
			clean.ThinkingWeight = r.ThinkingWeight
		}
		out = append(out, clean)
	}
	return out, "", ""
}

func persistBillingRulesUpdate(values map[string]string, revision database.BillingRuleRevision, activateNow bool) (database.BillingRuleRevision, error) {
	err := database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&revision).Error; err != nil {
			return fmt.Errorf("create billing rule revision: %w", err)
		}
		if !activateNow {
			return nil
		}
		values[proxy.BillingRulesRevisionIDConfigKey] = strconv.Itoa(int(revision.ID))
		for k, v := range values {
			enc, err := utils.Encrypt(v)
			if err != nil {
				return fmt.Errorf("encrypt %s: %w", k, err)
			}
			var existing database.SysConfig
			res := tx.Where("key = ?", k).First(&existing)
			if res.RowsAffected > 0 {
				existing.Value = enc
				if err := tx.Save(&existing).Error; err != nil {
					return fmt.Errorf("save %s: %w", k, err)
				}
			} else {
				if err := tx.Create(&database.SysConfig{Key: k, Value: enc}).Error; err != nil {
					return fmt.Errorf("create %s: %w", k, err)
				}
			}
		}
		return nil
	})
	return revision, err
}

func parseBillingRulePayloads(raw string) []billingRulePayload {
	var rows []billingRulePayload
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		return []billingRulePayload{}
	}
	return rows
}

func loadBillingRuleCancellations(rows []database.BillingRuleRevision) map[uint]*database.BillingRuleRevisionCancellation {
	out := map[uint]*database.BillingRuleRevisionCancellation{}
	if len(rows) == 0 {
		return out
	}
	ids := make([]uint, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	var cancellations []database.BillingRuleRevisionCancellation
	if err := database.DB.Where("revision_id IN ?", ids).Find(&cancellations).Error; err != nil {
		log.Printf("[BILLING-RULES-HISTORY] load cancellations failed: %v", err)
		return out
	}
	for i := range cancellations {
		cancel := &cancellations[i]
		out[cancel.RevisionID] = cancel
	}
	return out
}

func billingRuleRevisionToResponse(
	row database.BillingRuleRevision,
	cancel *database.BillingRuleRevisionCancellation,
	activeRevisionID uint,
	activeVersion string,
	now time.Time,
) billingRuleRevisionResponse {
	status := "superseded"
	var canceledAt *time.Time
	if cancel != nil {
		status = "canceled"
		canceledAt = &cancel.CreatedAt
	} else if activeRevisionID != 0 && row.ID == activeRevisionID {
		status = "active"
	} else if activeRevisionID == 0 && row.Version == activeVersion && !billingRuleRevisionIsFuture(row, now) {
		status = "active"
	} else if billingRuleRevisionIsFuture(row, now) {
		status = "scheduled"
	}
	return billingRuleRevisionResponse{
		ID:                row.ID,
		Version:           row.Version,
		EffectiveSince:    row.EffectiveSince,
		PublishedAt:       billingRuleRevisionPublishedAt(row),
		EffectiveAt:       billingRuleRevisionEffectiveAt(row),
		Status:            status,
		CanceledAt:        canceledAt,
		ModelWeights:      parseBillingRulePayloads(row.ModelWeightsJSON),
		HealthMultipliers: parseBillingRulePayloads(row.HealthMultipliersJSON),
		ModelCount:        row.ModelCount,
		HealthCount:       row.HealthCount,
		Source:            row.Source,
		CreatedAt:         row.CreatedAt,
	}
}

func billingRuleRevisionIsFuture(row database.BillingRuleRevision, now time.Time) bool {
	return row.EffectiveAt != nil && row.EffectiveAt.After(now)
}

func billingRuleRevisionPublishedAt(row database.BillingRuleRevision) *time.Time {
	if row.PublishedAt != nil {
		return row.PublishedAt
	}
	if !row.CreatedAt.IsZero() {
		return &row.CreatedAt
	}
	return nil
}

func billingRuleRevisionEffectiveAt(row database.BillingRuleRevision) *time.Time {
	if row.EffectiveAt != nil {
		return row.EffectiveAt
	}
	if !row.CreatedAt.IsZero() {
		return &row.CreatedAt
	}
	return nil
}

