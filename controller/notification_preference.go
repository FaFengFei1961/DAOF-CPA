// Package controller / notification_preference.go
//
// 用户通知偏好的 GET / PUT 接口。
//
// GET：返回当前用户偏好；未保存过的用户返回系统默认（lazy default）。
// PUT：upsert 偏好；写完调 proxy.InvalidatePrefCache 强制下次重载。
package controller

import (
	"log"

	"daof-cpa/database"
	"daof-cpa/proxy"

	"github.com/gofiber/fiber/v2"
)

// preferenceUpdateRequest PUT 请求体结构
type preferenceUpdateRequest struct {
	EnabledCategories map[string]bool `json:"enabled_categories"`
	UsageThresholds   []int           `json:"usage_thresholds"`
}

// GetMyNotificationPreference GET /api/notifications/preference
func GetMyNotificationPreference(c *fiber.Ctx) error {
	user, err := getCurrentUser(c)
	if err != nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}
	view := database.LoadPreference(user.ID)
	return c.JSON(fiber.Map{
		"success": true,
		"data": fiber.Map{
			"enabled_categories": view.EnabledCategories,
			"usage_thresholds":   view.UsageThresholds,
		},
	})
}

// UpdateMyNotificationPreference PUT /api/notifications/preference
func UpdateMyNotificationPreference(c *fiber.Ctx) error {
	user, err := getCurrentUser(c)
	if err != nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}

	var req preferenceUpdateRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_BAD_REQUEST"})
	}
	if req.EnabledCategories == nil {
		req.EnabledCategories = map[string]bool{}
	}
	if req.UsageThresholds == nil {
		req.UsageThresholds = []int{}
	}

	if err := database.SavePreference(user.ID, req.EnabledCategories, req.UsageThresholds); err != nil {
		log.Printf("[NOTIF-PREF] save user=%d failed: %v", user.ID, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_UPDATE"})
	}

	// 强制下次读偏好时重新从 DB 拉
	proxy.InvalidatePrefCache(user.ID)

	view := database.LoadPreference(user.ID)
	return c.JSON(fiber.Map{
		"success":      true,
		"message_code": "SUCCESS_SAVED",
		"data": fiber.Map{
			"enabled_categories": view.EnabledCategories,
			"usage_thresholds":   view.UsageThresholds,
		},
	})
}
