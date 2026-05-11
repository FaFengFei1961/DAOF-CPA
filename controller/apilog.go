package controller

import (
	"daof-ai-hub/database"
	"strconv"

	"github.com/gofiber/fiber/v2"
)

// GetLogs 查询令牌调用细则
func GetLogs(c *fiber.Ctx) error {
	user, err := getCurrentUser(c)
	if err != nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message": "鉴权失败", "message_code": err.Error()})
	}

	page, _ := strconv.Atoi(c.Query("page", "1"))
	if page < 1 {
		page = 1
	}
	limit, _ := strconv.Atoi(c.Query("limit", "15"))
	if limit < 1 {
		limit = 15
	}
	if limit > 200 {
		limit = 200
	}

	var logs []database.ApiLog
	var total int64

	// 只拉取当前主账号下的流通流水
	// fix MEDIUM（silent-failure 第十八轮）：原 Count + Find 完全不检查 .Error
	// → DB 故障时返回 200 + 空 logs，用户以为"没有日志"。fail-closed 500 让前端重试。
	query := database.DB.Model(&database.ApiLog{}).Where("user_id = ?", user.ID)
	if err := query.Count(&total).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}
	if err := query.Order("id desc").Offset((page - 1) * limit).Limit(limit).Find(&logs).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}

	return c.JSON(fiber.Map{
		"success": true,
		"data": map[string]interface{}{
			"logs":  logs,
			"total": total,
			"page":  page,
			"limit": limit,
		},
	})
}
