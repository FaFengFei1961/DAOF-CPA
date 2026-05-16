// Package controller / notification.go
//
// 站内通知系统。文案模板从 SysConfig 读取，admin 可改。
package controller

import (
	"log"
	"strconv"
	"strings"
	"time"

	"daof-cpa/database"
	"daof-cpa/proxy"

	"github.com/gofiber/fiber/v2"
)

// readSysConfigCached 跨 controller 共享的小 helper：从 proxy.SysConfigCache 读 key，
// 留空时回退默认值。读时持 RLock 保证并发安全。
func readSysConfigCached(key, def string) string {
	proxy.SysConfigMutex.RLock()
	v := strings.TrimSpace(proxy.SysConfigCache[key])
	proxy.SysConfigMutex.RUnlock()
	if v == "" {
		return def
	}
	return v
}

// CreateNotification 内部 helper：转调 proxy.Dispatch（统一分发入口，会做偏好检查）
func CreateNotification(userID uint, category, severity, title, body, actionURL, actionText, relatedType string, relatedID uint) {
	proxy.Dispatch(userID, category, severity, title, body, actionURL, actionText, relatedType, relatedID, nil)
}

func createPurchaseNotification(userID uint, pkg *database.Package, qty int) {
	body := "已为您激活 " + strconv.Itoa(qty) + " 份「" + pkg.Name + "」"
	CreateNotification(
		userID, "subscription", "success",
		"购买成功",
		body, proxy.LinkUpgradeMine(), "查看订阅",
		"subscription", 0,
	)
}

// ─── HTTP Endpoints ────────────────────────────────────────────

// MyNotifications 当前用户通知列表
//
// fix Minor（gemini 第四轮）：原实现只 Limit(100) 强行截断且无 page/page_size 参数，
// 通知超过 100 条的用户永远看不到旧消息。增加分页 + 总数返回，前端可基于 has_more 加载历史。
func MyNotifications(c *fiber.Ctx) error {
	user, err := getCurrentUser(c)
	if err != nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}
	// 分页参数：默认 page=1, page_size=50；page_size 限 [1, 200] 防 DoS
	page, _ := strconv.Atoi(c.Query("page", "1"))
	if page < 1 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(c.Query("page_size", "50"))
	if pageSize < 1 {
		pageSize = 50
	}
	if pageSize > 200 {
		pageSize = 200
	}

	// 撤回的通知（admin 撤回群发后）一律不展示给用户
	q := database.DB.Model(&database.Notification{}).
		Where("user_id = ? AND revoked_at IS NULL", user.ID)
	if c.Query("unread") == "1" {
		q = q.Where("read_at IS NULL")
	}
	if cat := c.Query("category"); cat != "" {
		q = q.Where("category = ?", cat)
	}

	var total int64
	// fix MAJOR M-B10（codex 第二十一轮）：原 Count/Find/Count 三处不检 .Error → DB 故障
	// 时返回 unread=0 + 空列表，用户误以为"通知已清空"。改为 fail-closed。
	if err := q.Count(&total).Error; err != nil {
		log.Printf("[NOTIF-LIST] count failed user=%d: %v", user.ID, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}

	var rows []database.Notification
	if err := q.Order("id desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		log.Printf("[NOTIF-LIST] find failed user=%d: %v", user.ID, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}

	var unread int64
	if err := database.DB.Model(&database.Notification{}).
		Where("user_id = ? AND read_at IS NULL AND revoked_at IS NULL", user.ID).
		Count(&unread).Error; err != nil {
		log.Printf("[NOTIF-LIST] unread count failed user=%d: %v", user.ID, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}
	return c.JSON(fiber.Map{
		"success":      true,
		"data":         rows,
		"unread_count": unread,
		"total":        total,
		"page":         page,
		"page_size":    pageSize,
		"has_more":     int64(page*pageSize) < total,
	})
}

// MarkNotificationRead 标记单条已读
func MarkNotificationRead(c *fiber.Ctx) error {
	user, err := getCurrentUser(c)
	if err != nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}
	id, _ := strconv.Atoi(c.Params("id"))
	now := time.Now()
	// fix MAJOR M22-6（codex 第二十二轮）：mark-read 加 .Error 检查 → fail-closed
	res := database.DB.Model(&database.Notification{}).
		Where("id = ? AND user_id = ? AND read_at IS NULL", id, user.ID).
		Update("read_at", now)
	if res.Error != nil {
		log.Printf("[NOTIF-READ] mark single user=%d id=%d failed: %v", user.ID, id, res.Error)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_UPDATE"})
	}
	return c.JSON(fiber.Map{"success": true, "updated": res.RowsAffected})
}

// MarkAllNotificationsRead 全部已读
func MarkAllNotificationsRead(c *fiber.Ctx) error {
	user, err := getCurrentUser(c)
	if err != nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}
	now := time.Now()
	res := database.DB.Model(&database.Notification{}).
		Where("user_id = ? AND read_at IS NULL", user.ID).
		Update("read_at", now)
	if res.Error != nil {
		log.Printf("[NOTIF-READ] mark all user=%d failed: %v", user.ID, res.Error)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_UPDATE"})
	}
	if res.Error != nil {
		log.Printf("[NOTIFICATION] mark-all-read user=%d failed: %v", user.ID, res.Error)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_UPDATE"})
	}
	return c.JSON(fiber.Map{"success": true, "message_code": "SUCCESS_MARKED", "updated": res.RowsAffected})
}
