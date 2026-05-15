// Package controller / quota_plan.go
//
// 配额计划 (QuotaPlan) 管理。所有限额规则的最小复用单元，admin CRUD。
package controller

import (
	"encoding/json"
	"errors"
	"log"
	"math"
	"strconv"

	"daof-ai-hub/database"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// isFinite 浮点数边界检查：拒绝 NaN / +Inf / -Inf。
// 用于 admin 配置入口验证，避免脏数据让消费引擎的算术比较退化。
func isFinite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

var allowedQuotaLimitUnits = map[string]bool{
	"api_cost_usd":    true,
	"request_count":   true,
	"input_tokens":    true,
	"output_tokens":   true,
	"total_tokens":    true,
	"weighted_tokens": true,
}

func validateQuotaPlanUnit(unit string) bool {
	return allowedQuotaLimitUnits[unit]
}

// allowedOverflowStrategies 列出 admin 可配置的 overflow_strategy 枚举。
// Sprint2-M4 删除了未实现的 "allow" / "degrade_model"（旧实现字段未被引擎读取，全部等价）。
var allowedOverflowStrategies = map[string]bool{
	"block":             true, // 用尽即停：拒绝请求，不尝试下一订阅 / 不 fallback 余额
	"next_subscription": true, // 软跳过：让 Decide 继续尝试下一订阅 + 余额（默认）
}

// isValidOverflowStrategy 校验 admin 输入的 overflow_strategy 是否为合法枚举。
// 空串视为非法（必须显式指定，避免歧义）。
func isValidOverflowStrategy(s string) bool {
	return allowedOverflowStrategies[s]
}

func validateJSONText(v string, fallback string) (string, bool) {
	if v == "" {
		v = fallback
	}
	var out any
	if err := json.Unmarshal([]byte(v), &out); err != nil {
		return v, false
	}
	return v, true
}

func normalizeQuotaPlanCostLimit(p *database.QuotaPlan) error {
	if p.LimitUnit != "api_cost_usd" {
		p.LimitValueMicroUSD = 0
		return nil
	}
	micro, ok := database.USDToMicro(p.LimitValue)
	if !ok {
		return errors.New("invalid api_cost_usd limit")
	}
	p.LimitValueMicroUSD = micro
	return nil
}

// ListQuotaPlans 返回所有配额计划。支持 ?enabled=1 / ?unit=request_count 等过滤。
func ListQuotaPlans(c *fiber.Ctx) error {
	// 排序：先按 admin 优先级 → 窗口大小（5h 在 7d 前面）→ 额度递增（Pro < Max 5x < Max 20x）
	// 同 tier 同窗口的 2 个 plan 用 id 兜底。grid 3 列布局下天然形成"短窗口一行 / 长窗口一行"。
	q := database.DB.Model(&database.QuotaPlan{}).Order("priority asc, window_seconds asc, limit_value asc, id asc")
	if v := c.Query("enabled"); v == "1" {
		q = q.Where("enabled = ?", true)
	}
	if v := c.Query("unit"); v != "" {
		q = q.Where("limit_unit = ?", v)
	}
	var plans []database.QuotaPlan
	if err := q.Find(&plans).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "message": "查询失败", "message_code": "ERR_DB_QUERY"})
	}
	return c.JSON(fiber.Map{"success": true, "data": plans})
}

// GetQuotaPlan 单条详情，并附带"被多少 Package 引用"信息。
func GetQuotaPlan(c *fiber.Ctx) error {
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_PARAMS"})
	}
	var p database.QuotaPlan
	if err := database.DB.First(&p, id).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"success": false, "message_code": "ERR_NOT_FOUND"})
	}
	var refCount int64
	database.DB.Model(&database.PackagePlan{}).Where("quota_plan_id = ?", id).Count(&refCount)
	return c.JSON(fiber.Map{
		"success":   true,
		"data":      p,
		"ref_count": refCount,
	})
}

// CreateQuotaPlan 新建配额计划。配额单位收紧为明确白名单，避免未知字符串在热路径产生歧义。
func CreateQuotaPlan(c *fiber.Ctx) error {
	var p database.QuotaPlan
	if err := c.BodyParser(&p); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "请求体解析失败", "message_code": "ERR_PARSE_PAYLOAD"})
	}
	if p.Name == "" || p.LimitUnit == "" {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "name 与 limit_unit 必填", "message_code": "ERR_REQUIRED"})
	}
	if !validateQuotaPlanUnit(p.LimitUnit) {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "limit_unit 不受支持", "message_code": "ERR_INVALID_LIMIT_UNIT"})
	}
	// fix Minor（codex 第五轮）：拒绝异常数值，避免 NaN/Inf/负数渗入引擎逻辑
	if !isFinite(p.LimitValue) || p.LimitValue < 0 {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "limit_value 必须 ≥ 0 且为有限数", "message_code": "ERR_INVALID_LIMIT"})
	}
	if err := normalizeQuotaPlanCostLimit(&p); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "limit_value 必须 ≥ 0 且为有限数", "message_code": "ERR_INVALID_LIMIT"})
	}
	if p.WindowSeconds < 0 {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "window_seconds 必须 ≥ 0", "message_code": "ERR_INVALID_WINDOW"})
	}
	if v, ok := validateJSONText(p.ModelMatch, "[]"); ok {
		p.ModelMatch = v
	} else {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "model_match 必须是合法 JSON", "message_code": "ERR_INVALID_JSON"})
	}
	if v, ok := validateJSONText(p.WeightFactor, "{}"); ok {
		p.WeightFactor = v
	} else {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "weight_factor 必须是合法 JSON", "message_code": "ERR_INVALID_JSON"})
	}
	if v, ok := validateJSONText(p.ExtraConfig, "{}"); ok {
		p.ExtraConfig = v
	} else {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "extra_config 必须是合法 JSON", "message_code": "ERR_INVALID_JSON"})
	}
	// fix CRITICAL Sprint2-M4：OverflowStrategy 仅接受 canonical 枚举值；admin 误传非法值拒绝。
	// 不再接受任意字符串（旧实现允许任意值，但引擎也从不读取该字段）。
	if !isValidOverflowStrategy(p.OverflowStrategy) {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message":      "overflow_strategy 仅支持 block / next_subscription",
			"message_code": "ERR_INVALID_OVERFLOW_STRATEGY",
		})
	}
	// 注：QuotaPlan.Enabled 已改为 `*bool`（自审第十三轮），
	// admin 显式 `enabled=false` → ptr(false) → 写入 false；不传 → nil → DB default true。
	if err := database.DB.Create(&p).Error; err != nil {
		log.Printf("[QUOTA-PLAN] create failed: %v", err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_CREATE"})
	}
	return c.JSON(fiber.Map{"success": true, "data": p, "message_code": "SUCCESS_CREATED"})
}

// UpdateQuotaPlan 更新。仅传过来的字段会改，未传字段保持原值（用 map 而非 struct）。
func UpdateQuotaPlan(c *fiber.Ctx) error {
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_PARAMS"})
	}
	var p database.QuotaPlan
	if err := database.DB.First(&p, id).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"success": false, "message_code": "ERR_NOT_FOUND"})
	}
	var payload map[string]any
	if err := c.BodyParser(&payload); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_PARSE_PAYLOAD"})
	}
	// 白名单字段，避免误改 ID/CreatedAt
	allowed := map[string]bool{
		"name": true, "display_name": true, "description": true,
		"model_match": true, "limit_unit": true, "limit_value": true,
		"window_seconds": true, "weight_factor": true,
		"auto_sync_from_channel_models": true, "priority": true,
		"overflow_strategy": true, "extra_config": true, "enabled": true,
	}
	updates := map[string]any{}
	for k, v := range payload {
		if allowed[k] {
			updates[k] = v
		}
	}
	// fix Major（自审第八轮）：UpdateQuotaPlan 缺 CreateQuotaPlan 的数值边界校验。
	// admin 可发 {"limit_value": null} 把 plan 限额置 0（引擎中 limitValue==0 = 不限额）→
	// 全套限额机制被绕过；window_seconds 设负数会被引擎按"永不过期"处理。
	// 与 CreateQuotaPlan 校验路径一致：必须有限数 + 非负。
	if v, ok := updates["limit_value"]; ok {
		f, fok := v.(float64)
		if !fok || !isFinite(f) || f < 0 {
			return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_LIMIT", "message": "limit_value 必须 >= 0 且为有限数"})
		}
	}
	if v, ok := updates["limit_unit"]; ok {
		unit, uok := v.(string)
		if !uok || !validateQuotaPlanUnit(unit) {
			return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_LIMIT_UNIT", "message": "limit_unit 不受支持"})
		}
	}
	// fix CRITICAL Sprint2-M4：UpdateQuotaPlan 也必须校验 overflow_strategy（与 Create 一致）
	if v, ok := updates["overflow_strategy"]; ok {
		s, sok := v.(string)
		if !sok || !isValidOverflowStrategy(s) {
			return c.Status(400).JSON(fiber.Map{
				"success":      false,
				"message_code": "ERR_INVALID_OVERFLOW_STRATEGY",
				"message":      "overflow_strategy 仅支持 block / next_subscription",
			})
		}
	}
	for _, key := range []string{"model_match", "weight_factor", "extra_config"} {
		if v, ok := updates[key]; ok {
			s, sok := v.(string)
			if !sok {
				return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_JSON", "message": key + " 必须是 JSON 字符串"})
			}
			if _, valid := validateJSONText(s, "{}"); !valid {
				return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_JSON", "message": key + " 必须是合法 JSON"})
			}
		}
	}
	if v, ok := updates["window_seconds"]; ok {
		f, fok := v.(float64) // JSON 数字默认解为 float64
		if !fok || !isFinite(f) || f < 0 {
			return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_WINDOW", "message": "window_seconds 必须 >= 0"})
		}
	}
	if v, ok := updates["priority"]; ok {
		f, fok := v.(float64)
		if !fok || !isFinite(f) {
			return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_PRIORITY"})
		}
	}
	nextUnit := p.LimitUnit
	if v, ok := updates["limit_unit"]; ok {
		nextUnit = v.(string)
	}
	nextLimitValue := p.LimitValue
	if v, ok := updates["limit_value"]; ok {
		nextLimitValue = v.(float64)
	}
	normalized := database.QuotaPlan{LimitUnit: nextUnit, LimitValue: nextLimitValue}
	if err := normalizeQuotaPlanCostLimit(&normalized); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_LIMIT", "message": "limit_value 必须 >= 0 且为有限数"})
	}
	updates["limit_value_micro_usd"] = normalized.LimitValueMicroUSD
	if err := database.DB.Model(&p).Updates(updates).Error; err != nil {
		log.Printf("[QUOTA-PLAN] update id=%d failed: %v", id, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_UPDATE"})
	}
	database.DB.First(&p, id)
	return c.JSON(fiber.Map{"success": true, "data": p, "message_code": "SUCCESS_UPDATED"})
}

// DeleteQuotaPlan 删除。如果还被 Package 引用，拒绝。
func DeleteQuotaPlan(c *fiber.Ctx) error {
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_PARAMS"})
	}
	var refCount int64
	database.DB.Model(&database.PackagePlan{}).Where("quota_plan_id = ?", id).Count(&refCount)
	if refCount > 0 {
		return c.Status(409).JSON(fiber.Map{
			"success":      false,
			"message":      "该配额计划仍被套餐引用，无法删除",
			"message_code": "ERR_PLAN_IN_USE",
			"ref_count":    refCount,
		})
	}
	if err := database.DB.Delete(&database.QuotaPlan{}, id).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_DELETE"})
	}
	return c.JSON(fiber.Map{"success": true, "message_code": "SUCCESS_DELETED"})
}

// ReorderQuotaPlans 批量重排 priority。
// 请求体：{ "ids": [10, 12, 11] } — 按此顺序 priority = 10, 20, 30 ...
// ListQuotaPlans 已用 priority asc 排序，重排后立即生效。
func ReorderQuotaPlans(c *fiber.Ctx) error {
	var req struct {
		IDs []uint `json:"ids"`
	}
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_PARSE_PAYLOAD"})
	}
	if len(req.IDs) == 0 {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_PARAMS"})
	}
	if err := database.DB.Transaction(func(tx *gorm.DB) error {
		for i, id := range req.IDs {
			if err := tx.Model(&database.QuotaPlan{}).Where("id = ?", id).Update("priority", (i+1)*10).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		log.Printf("[QUOTA-PLAN-REORDER] failed: %v", err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}
	return c.JSON(fiber.Map{"success": true, "message_code": "SUCCESS_REORDERED"})
}
