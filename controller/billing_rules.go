package controller

import (
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"daof-cpa/database"
	"daof-cpa/proxy"
	"daof-cpa/utils"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
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
	out := make([]billingRuleRevisionResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, billingRuleRevisionResponse{
			ID:                row.ID,
			Version:           row.Version,
			EffectiveSince:    row.EffectiveSince,
			ModelWeights:      parseBillingRulePayloads(row.ModelWeightsJSON),
			HealthMultipliers: parseBillingRulePayloads(row.HealthMultipliersJSON),
			ModelCount:        row.ModelCount,
			HealthCount:       row.HealthCount,
			Source:            row.Source,
			CreatedAt:         row.CreatedAt,
		})
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
	ModelWeights      []billingRulePayload `json:"model_weights"`
	HealthMultipliers []billingRulePayload `json:"health_multipliers"`
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

	version := strings.TrimSpace(payload.Version)
	if version == "" {
		// fix P3（codex review verify-r5）：原格式 `editor-YYYY-MM-DD-HHMM` 尾段是 `MM-DD-HHMM`，
		// extractEffectiveSinceFromVersion 取最后 10 字符 + 校验 `XXXX-XX-XX` 失败 → effective_since 空。
		// 改纯日期后缀，公示页能正确显示生效日。
		version = fmt.Sprintf("editor-%s", time.Now().UTC().Format("2006-01-02"))
	}
	if !billingRuleVersionRe.MatchString(version) {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message":      "version 仅允许字母、数字、点、连字符 (.-)，长度 1-64",
			"message_code": "ERR_BILLING_RULES_VERSION_INVALID",
		})
	}

	modelJSON, _ := json.Marshal(modelClean)
	healthJSON, _ := json.Marshal(healthClean)

	operatorID := uint(0)
	if v := c.Locals("admin_user_id"); v != nil {
		if id, ok := v.(uint); ok {
			operatorID = id
		}
	}
	if err := persistBillingRulesUpdate(map[string]string{
		proxy.BillingModelWeightsConfigKey:      string(modelJSON),
		proxy.BillingHealthMultipliersConfigKey: string(healthJSON),
		proxy.BillingRulesVersionConfigKey:      version,
	}, database.BillingRuleRevision{
		Version:               version,
		EffectiveSince:        extractEffectiveSinceLocal(version),
		ModelWeightsJSON:      string(modelJSON),
		HealthMultipliersJSON: string(healthJSON),
		ModelCount:            len(modelClean),
		HealthCount:           len(healthClean),
		Source:                "admin",
		CreatedBy:             operatorID,
	}); err != nil {
		log.Printf("[BILLING-RULES-ADMIN] persist failed: %v", err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_WRITE"})
	}
	proxy.SyncCacheConfig()

	details, _ := json.Marshal([]map[string]any{{
		"type":         "BILLING_RULES_UPDATE",
		"version":      version,
		"model_count":  len(modelClean),
		"health_count": len(healthClean),
	}})
	LogOperationBy(operatorID, operatorID, "admin", "BILLING_RULES_UPDATE", c.IP(), string(details))

	return c.JSON(fiber.Map{
		"success":      true,
		"message_code": "SUCCESS_BILLING_RULES_SAVED",
		"data":         proxy.GetPublicBillingRules(),
	})
}

// version 字面量校验：与 message_code 守护逻辑同精神——拒绝把不可控字符塞进 audit log。
var billingRuleVersionRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

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

func persistBillingRulesUpdate(values map[string]string, revision database.BillingRuleRevision) error {
	return database.DB.Transaction(func(tx *gorm.DB) error {
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
		if err := tx.Create(&revision).Error; err != nil {
			return fmt.Errorf("create billing rule revision: %w", err)
		}
		return nil
	})
}

func parseBillingRulePayloads(raw string) []billingRulePayload {
	var rows []billingRulePayload
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		return []billingRulePayload{}
	}
	return rows
}

func extractEffectiveSinceLocal(version string) string {
	v := strings.TrimSpace(version)
	if len(v) < 10 {
		return ""
	}
	tail := v[len(v)-10:]
	if tail[4] != '-' || tail[7] != '-' {
		return ""
	}
	for i, c := range tail {
		if i == 4 || i == 7 {
			continue
		}
		if c < '0' || c > '9' {
			return ""
		}
	}
	return tail
}
