// Package controller / balance_consume.go
//
// 用户余额消费控制（参照 Claude Extra usage 三段消费模型）。
// 用户接口允许查询/修改：是否启用、限额、重置周期。
package controller

import (
	"log"
	"math"

	"daof-ai-hub/database"
	"daof-ai-hub/proxy"

	"github.com/gofiber/fiber/v2"
)

// LimitUSD 来自前端：单位 USD（float64），后端转 micro_usd 落库。
// 这是 API 边界的格式转换，不是 backward compat —— 前端展示天然用 USD，存储用 micro_usd。
type balanceConsumeUpdateRequest struct {
	Enabled       *bool    `json:"enabled"`
	LimitUSD      *float64 `json:"limit_usd"`
	WindowSeconds *int     `json:"window_seconds"`
}

// GetMyBalanceConsumePreference GET /api/balance-consume/preference
//
// 返回当前余额消费控制状态 + 当前余额（顶栏一致）。
func GetMyBalanceConsumePreference(c *fiber.Ctx) error {
	user, err := getCurrentUser(c)
	if err != nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}
	status := proxy.GetBalanceConsumeStatus(user)
	return c.JSON(fiber.Map{
		"success": true,
		"data": fiber.Map{
			"enabled":            status.Enabled,
			"limit_usd":          database.MicroToUSD(status.LimitMicroUSD),
			"window_seconds":     status.WindowSeconds,
			"window_start_at":    status.WindowStartAt,
			"consumed_in_window": database.MicroToUSD(status.ConsumedInWindowMicroUSD),
			"resets_at":          status.ResetsAt,
			"current_balance":    database.MicroToUSD(user.Quota),
		},
	})
}

// UpdateMyBalanceConsumePreference PUT /api/balance-consume/preference
//
// 任一字段为 null 表示不修改。修改窗口长度立即重置当前窗口。
func UpdateMyBalanceConsumePreference(c *fiber.Ctx) error {
	user, err := getCurrentUser(c)
	if err != nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}
	var req balanceConsumeUpdateRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_BAD_REQUEST"})
	}

	updates := map[string]any{}
	if req.Enabled != nil {
		updates["balance_consume_enabled"] = *req.Enabled
	}
	if req.LimitUSD != nil {
		v := *req.LimitUSD
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_LIMIT_INVALID"})
		}
		micro, ok := database.USDToMicro(v)
		if !ok {
			return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_LIMIT_INVALID"})
		}
		updates["balance_consume_limit_usd"] = micro
	}
	if req.WindowSeconds != nil {
		w := *req.WindowSeconds
		// 合理范围：60 秒（1 分钟）到 365 天，避免极端值（0=每秒重置免限额；超大值=失去窗口意义）
		const minWindow = 60
		const maxWindow = 365 * 86400
		if w < minWindow || w > maxWindow {
			return c.Status(400).JSON(fiber.Map{
				"success":      false,
				"message_code": "ERR_WINDOW_INVALID",
				"min_seconds":  minWindow,
				"max_seconds":  maxWindow,
			})
		}
		// 防滥用：仅当窗口长度真的发生变化时才允许重置（同值视为无操作，避免循环 reset 免限额）
		if w != user.BalanceConsumeWindowSeconds {
			updates["balance_consume_window_seconds"] = w
			// 改窗口长度立即重置（避免长窗口改短后已消费数据失真）
			updates["balance_consume_window_start_at"] = nil
			updates["balance_consumed_in_window"] = int64(0)
		}
	}

	if len(updates) == 0 {
		return c.JSON(fiber.Map{"success": true, "message_code": "SUCCESS_NO_CHANGE"})
	}

	if err := database.DB.Model(&database.User{}).Where("id = ?", user.ID).Updates(updates).Error; err != nil {
		log.Printf("[BALANCE-CONSUME] update user=%d err=%v", user.ID, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_UPDATE"})
	}
	proxy.RefreshUserAuth(user.ID)

	// 重新读返回最新状态
	var fresh database.User
	if err := database.DB.First(&fresh, user.ID).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}
	status := proxy.GetBalanceConsumeStatus(&fresh)
	return c.JSON(fiber.Map{
		"success":      true,
		"message_code": "SUCCESS_SAVED",
		"data": fiber.Map{
			"enabled":            status.Enabled,
			"limit_usd":          database.MicroToUSD(status.LimitMicroUSD),
			"window_seconds":     status.WindowSeconds,
			"window_start_at":    status.WindowStartAt,
			"consumed_in_window": database.MicroToUSD(status.ConsumedInWindowMicroUSD),
			"resets_at":          status.ResetsAt,
			"current_balance":    database.MicroToUSD(fresh.Quota),
		},
	})
}
