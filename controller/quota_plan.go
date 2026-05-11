// Package controller / quota_plan.go
//
// 配额计划 (QuotaPlan) 管理。所有限额规则的最小复用单元，admin CRUD。
package controller

import (
	"log"
	"math"
	"strconv"

	"daof-ai-hub/database"

	"github.com/gofiber/fiber/v2"
)

// isFinite 浮点数边界检查：拒绝 NaN / +Inf / -Inf。
// 用于 admin 配置入口验证，避免脏数据让消费引擎的算术比较退化。
func isFinite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

// ListQuotaPlans 返回所有配额计划。支持 ?enabled=1 / ?unit=messages 等过滤。
func ListQuotaPlans(c *fiber.Ctx) error {
	q := database.DB.Model(&database.QuotaPlan{}).Order("priority asc, id desc")
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

// CreateQuotaPlan 新建配额计划。所有字段都由 admin 自由配置，不做强制 enum 校验。
func CreateQuotaPlan(c *fiber.Ctx) error {
	var p database.QuotaPlan
	if err := c.BodyParser(&p); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "请求体解析失败", "message_code": "ERR_PARSE_PAYLOAD"})
	}
	if p.Name == "" || p.LimitUnit == "" {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "name 与 limit_unit 必填", "message_code": "ERR_REQUIRED"})
	}
	// fix Minor（codex 第五轮）：拒绝异常数值，避免 NaN/Inf/负数渗入引擎逻辑
	if !isFinite(p.LimitValue) || p.LimitValue < 0 {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "limit_value 必须 ≥ 0 且为有限数", "message_code": "ERR_INVALID_LIMIT"})
	}
	if p.WindowSeconds < 0 {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "window_seconds 必须 ≥ 0", "message_code": "ERR_INVALID_WINDOW"})
	}
	if p.ModelMatch == "" {
		p.ModelMatch = "[]"
	}
	if p.WeightFactor == "" {
		p.WeightFactor = "{}"
	}
	if p.ExtraConfig == "" {
		p.ExtraConfig = "{}"
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
