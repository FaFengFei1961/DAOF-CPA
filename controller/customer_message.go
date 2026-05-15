// Package controller / customer_message.go
//
// 工单系统：用户与 admin 多轮会话。
//
// 路由：
//
//	POST   /api/tickets                          用户创建工单（subject + 第一条消息）
//	GET    /api/tickets/mine                     用户工单列表
//	GET    /api/tickets/:id                      用户/admin 工单详情 + 消息流
//	POST   /api/tickets/:id/messages             双方追加一条消息
//	POST   /api/tickets/:id/close                双方关闭工单
//	POST   /api/tickets/:id/read                 标记已读（清未读徽章）
//
//	GET    /api/admin/tickets                    admin 列表 + 状态过滤
//	POST   /api/admin/tickets/:id/messages       admin 回复（合并到上面 :id/messages，但需 admin guard）
//
// 关闭后 15 天 cron 物理删除（含 ticket_messages，外键级联）。
package controller

import (
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"daof-ai-hub/database"
	"daof-ai-hub/proxy"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

const (
	maxTicketSubjectLen = 200
	maxTicketBodyLen    = 5000
)

// errTicketClosed 哨兵：在已关闭工单上发消息
var errTicketClosed = errors.New("ticket already closed")

// ── 用户接口 ──────────────────────────────────────────────────

type createTicketRequest struct {
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

// CreateTicket POST /api/tickets
//
// 创建工单 + 第一条消息（事务）。
func CreateTicket(c *fiber.Ctx) error {
	user, err := getCurrentUser(c)
	if err != nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}
	var req createTicketRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_BAD_REQUEST"})
	}
	subject := strings.TrimSpace(req.Subject)
	body := strings.TrimSpace(req.Body)
	if subject == "" || body == "" {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_FIELDS_REQUIRED"})
	}
	if len(subject) > maxTicketSubjectLen {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_SUBJECT_TOO_LONG"})
	}
	if len(body) > maxTicketBodyLen {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_BODY_TOO_LONG"})
	}

	now := time.Now()
	ticket := database.Ticket{
		UserID:        user.ID,
		Subject:       subject,
		Status:        "open",
		LastMessageAt: now,
		UserReadAt:    &now,
		CreatedAt:     now,
	}
	txErr := database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&ticket).Error; err != nil {
			return fmt.Errorf("create ticket: %w", err)
		}
		msg := database.TicketMessage{
			TicketID:  ticket.ID,
			Sender:    "user",
			SenderID:  user.ID,
			Body:      body,
			CreatedAt: now,
		}
		if err := tx.Create(&msg).Error; err != nil {
			return fmt.Errorf("create message: %w", err)
		}
		return nil
	})
	if txErr != nil {
		log.Printf("[TICKET] create user=%d failed: %v", user.ID, txErr)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_INSERT"})
	}
	log.Printf("[TICKET] new ticket id=%d user=%d subject=%q", ticket.ID, user.ID, truncateLog(subject, 40))
	return c.JSON(fiber.Map{"success": true, "data": ticket, "message_code": "SUCCESS_CREATED"})
}

// MyTickets GET /api/tickets/mine
func MyTickets(c *fiber.Ctx) error {
	user, err := getCurrentUser(c)
	if err != nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}
	page, _ := strconv.Atoi(c.Query("page", "1"))
	size, _ := strconv.Atoi(c.Query("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 50 {
		size = 20
	}
	var rows []database.Ticket
	if err := database.DB.Where("user_id = ?", user.ID).
		Order("last_message_at desc").
		Offset((page - 1) * size).Limit(size).
		Find(&rows).Error; err != nil {
		log.Printf("[TICKET] mine user=%d failed: %v", user.ID, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}
	// 附带每张工单的"用户未读消息数"（admin 发的、CreatedAt > UserReadAt）
	withUnread := make([]fiber.Map, 0, len(rows))
	for _, t := range rows {
		unread := countUnreadForUser(t.ID, t.UserReadAt)
		withUnread = append(withUnread, fiber.Map{
			"ticket":       t,
			"unread_count": unread,
		})
	}
	var total int64
	database.DB.Model(&database.Ticket{}).Where("user_id = ?", user.ID).Count(&total)
	return c.JSON(fiber.Map{
		"success": true,
		"data":    withUnread,
		"meta":    fiber.Map{"page": page, "page_size": size, "total": total},
	})
}

// GetTicket GET /api/tickets/:id
//
// 用户/admin 都能调用此接口。后端按 user.role 鉴权：普通用户只能看自己的；admin 任意。
func GetTicket(c *fiber.Ctx) error {
	user, isAdmin, err := loadCurrentRole(c)
	if err != nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}
	id, _ := strconv.Atoi(c.Params("id"))
	var ticket database.Ticket
	if err := database.DB.First(&ticket, id).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"success": false, "message_code": "ERR_NOT_FOUND"})
	}
	if !isAdmin && ticket.UserID != user.ID {
		return c.Status(403).JSON(fiber.Map{"success": false, "message_code": "ERR_FORBIDDEN"})
	}
	var msgs []database.TicketMessage
	if err := database.DB.Where("ticket_id = ?", id).Order("created_at asc").Find(&msgs).Error; err != nil {
		log.Printf("[TICKET] load messages id=%d failed: %v", id, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}
	// username 仅 admin 端可见——避免普通用户接口出现 PII 字段（即便目前用户拿到的是自己的 username 无害，
	// 接口 contract 上不应让"用户视图"返回这个字段）
	resp := fiber.Map{
		"ticket":   ticket,
		"messages": msgs,
	}
	if isAdmin {
		var owner database.User
		if database.DB.Select("id, username").First(&owner, ticket.UserID).Error == nil {
			resp["username"] = owner.Username
		}
	}
	return c.JSON(fiber.Map{"success": true, "data": resp})
}

type postMessageRequest struct {
	Body string `json:"body"`
}

// PostTicketMessage POST /api/tickets/:id/messages
//
// 双方都能在 open 工单内追加消息。
//   - user 发消息 → 给该工单的 admin 池发通知（强制送达 system 类）
//   - admin 发消息 → 给 ticket.UserID 发通知
//
// 关闭工单不能再发消息（errTicketClosed）。
func PostTicketMessage(c *fiber.Ctx) error {
	user, isAdmin, err := loadCurrentRole(c)
	if err != nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}
	id, _ := strconv.Atoi(c.Params("id"))
	var req postMessageRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_BAD_REQUEST"})
	}
	body := strings.TrimSpace(req.Body)
	if body == "" {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_BODY_REQUIRED"})
	}
	if len(body) > maxTicketBodyLen {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_BODY_TOO_LONG"})
	}

	var ticket database.Ticket
	if err := database.DB.First(&ticket, id).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"success": false, "message_code": "ERR_NOT_FOUND"})
	}
	if !isAdmin && ticket.UserID != user.ID {
		return c.Status(403).JSON(fiber.Map{"success": false, "message_code": "ERR_FORBIDDEN"})
	}

	sender := "user"
	if isAdmin {
		sender = "admin"
	}
	now := time.Now()

	// 在闭包外声明，事务内填充，事务后返回给前端做乐观追加
	var createdMsg database.TicketMessage

	txErr := database.DB.Transaction(func(tx *gorm.DB) error {
		// 乐观锁：必须 status='open' 才能发消息
		res := tx.Model(&database.Ticket{}).
			Where("id = ? AND status = ?", id, "open").
			Updates(map[string]any{"last_message_at": now})
		if res.Error != nil {
			return fmt.Errorf("update ticket: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return errTicketClosed
		}
		// 发送方的 readAt 同步刷新（自己刚发的消息当然算已读）
		if sender == "user" {
			tx.Model(&database.Ticket{}).Where("id = ?", id).Update("user_read_at", now)
		} else {
			tx.Model(&database.Ticket{}).Where("id = ?", id).Update("admin_read_at", now)
		}
		createdMsg = database.TicketMessage{
			TicketID:  uint(id),
			Sender:    sender,
			SenderID:  user.ID,
			Body:      body,
			CreatedAt: now,
		}
		if err := tx.Create(&createdMsg).Error; err != nil {
			return fmt.Errorf("create message: %w", err)
		}
		return nil
	})
	if errors.Is(txErr, errTicketClosed) {
		return c.Status(409).JSON(fiber.Map{"success": false, "message_code": "ERR_TICKET_CLOSED"})
	}
	if txErr != nil {
		log.Printf("[TICKET] post msg id=%d failed: %v", id, txErr)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_TRANSACTION"})
	}

	// 给对方发通知。改用 "ticket_message" 类——可被用户在通知偏好里关闭，
	// 而非之前的 "system" 强制送达。这与"客服回复=日常对话"语义更一致；
	// 真正的系统强制送达（如封号、安全告警）仍走 system/security。
	bodyPreview := body
	if len(bodyPreview) > 200 {
		bodyPreview = bodyPreview[:200] + "..."
	}
	dedupKey := fmt.Sprintf("ticket:%d:%d", id, now.UnixNano())
	if isAdmin {
		// admin 发消息 → 通知用户
		proxy.Dispatch(
			ticket.UserID, "ticket_message", "info",
			"客服已回复："+truncateLog(ticket.Subject, 60),
			bodyPreview,
			"/tickets",
			"查看工单",
			"ticket", uint(id),
			&dedupKey,
		)
	}
	// 用户发消息时不通知 admin（admin 主动到面板看；避免每条用户消息都给 admin 发铃铛）
	// 返回创建好的消息记录给前端做乐观追加（包含真实 id / created_at，前端不需要再 GET 一次详情）
	return c.JSON(fiber.Map{
		"success":      true,
		"message_code": "SUCCESS_SENT",
		"data":         createdMsg,
	})
}

// CloseTicket POST /api/tickets/:id/close
//
// 双方都能关闭，关闭后 15 天 cron 物理清除。
func CloseTicket(c *fiber.Ctx) error {
	user, isAdmin, err := loadCurrentRole(c)
	if err != nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}
	id, _ := strconv.Atoi(c.Params("id"))
	var ticket database.Ticket
	if err := database.DB.First(&ticket, id).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"success": false, "message_code": "ERR_NOT_FOUND"})
	}
	if !isAdmin && ticket.UserID != user.ID {
		return c.Status(403).JSON(fiber.Map{"success": false, "message_code": "ERR_FORBIDDEN"})
	}

	now := time.Now()
	updates := map[string]any{
		"status":    "closed",
		"closed_at": now,
	}
	if isAdmin {
		updates["closed_by_admin"] = true
		updates["closed_by_admin_id"] = user.ID
	} else {
		updates["closed_by_user"] = true
	}
	res := database.DB.Model(&database.Ticket{}).
		Where("id = ? AND status = ?", id, "open").
		Updates(updates)
	if res.Error != nil {
		log.Printf("[TICKET] close id=%d failed: %v", id, res.Error)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_UPDATE"})
	}
	if res.RowsAffected == 0 {
		return c.Status(409).JSON(fiber.Map{"success": false, "message_code": "ERR_ALREADY_CLOSED"})
	}
	return c.JSON(fiber.Map{"success": true, "message_code": "SUCCESS_CLOSED"})
}

// MarkTicketRead POST /api/tickets/:id/read
//
// 标记当前角色（user/admin）已读，用于清除未读徽章。
func MarkTicketRead(c *fiber.Ctx) error {
	user, isAdmin, err := loadCurrentRole(c)
	if err != nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}
	id, _ := strconv.Atoi(c.Params("id"))
	var ticket database.Ticket
	if err := database.DB.First(&ticket, id).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"success": false, "message_code": "ERR_NOT_FOUND"})
	}
	if !isAdmin && ticket.UserID != user.ID {
		return c.Status(403).JSON(fiber.Map{"success": false, "message_code": "ERR_FORBIDDEN"})
	}
	field := "user_read_at"
	if isAdmin {
		field = "admin_read_at"
	}
	if err := database.DB.Model(&database.Ticket{}).Where("id = ?", id).Update(field, time.Now()).Error; err != nil {
		log.Printf("[TICKET] mark read id=%d field=%s err=%v", id, field, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_UPDATE"})
	}
	return c.JSON(fiber.Map{"success": true})
}

// ── Admin 列表 ───────────────────────────────────────────────

// AdminListTickets GET /api/admin/tickets?status=open
func AdminListTickets(c *fiber.Ctx) error {
	if loadAdminUser(c) == nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}
	page, _ := strconv.Atoi(c.Query("page", "1"))
	size, _ := strconv.Atoi(c.Query("page_size", "30"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 200 {
		size = 30
	}
	q := database.DB.Model(&database.Ticket{})
	if s := c.Query("status"); s != "" {
		switch s {
		case "open", "closed":
			q = q.Where("status = ?", s)
		default:
			return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_BAD_STATUS"})
		}
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}
	var rows []database.Ticket
	if err := q.Order("last_message_at desc").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}
	// fix N+1：批量查 username（page_size 上限 200，单 IN 查询完全可行）
	usernameByID := make(map[uint]string, len(rows))
	if len(rows) > 0 {
		uids := make([]uint, 0, len(rows))
		seen := make(map[uint]struct{}, len(rows))
		for _, t := range rows {
			if _, ok := seen[t.UserID]; ok {
				continue
			}
			seen[t.UserID] = struct{}{}
			uids = append(uids, t.UserID)
		}
		var owners []database.User
		if err := database.DB.Select("id, username").Where("id IN ?", uids).Find(&owners).Error; err == nil {
			for _, u := range owners {
				usernameByID[u.ID] = u.Username
			}
		}
	}
	withMeta := make([]fiber.Map, 0, len(rows))
	for _, t := range rows {
		unread := countUnreadForAdmin(t.ID, t.AdminReadAt)
		withMeta = append(withMeta, fiber.Map{
			"ticket":       t,
			"username":     usernameByID[t.UserID],
			"unread_count": unread,
		})
	}
	return c.JSON(fiber.Map{
		"success": true,
		"data":    withMeta,
		"meta":    fiber.Map{"page": page, "page_size": size, "total": total},
	})
}

// ── helper ───────────────────────────────────────────────────

// loadCurrentRole 区分当前请求是普通用户（Bearer header）还是 admin（HttpOnly cookie）。
// 双角色路由（如 GetTicket）需要这个 helper。
//
// fix MAJOR M23-A4（codex 第二十三轮）：Bearer 优先于 cookie。
// 旧实现先 ExtractAdminToken（cookie 优先 Bearer），导致：
//   - admin 浏览器同时持 admin cookie + 用户 SDK 设的 Bearer header → 被识别为 admin
//   - 用户在 admin 解锁页面回工单 → ticket message 被记成 admin 行为
//
// 新策略：Bearer header = 用户 SDK / curl / 程序化访问；cookie = 浏览器后台。
// 优先 Bearer 让 SDK 永远走 user 分支；admin 走管理后台必须用 cookie（且自动免疫 CSRF token 抢占）。
func loadCurrentRole(c *fiber.Ctx) (*database.User, bool, error) {
	// 1) Bearer header 优先（SDK / curl / 用户脚本场景）
	authHeader := c.Get("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") || strings.HasPrefix(authHeader, "bearer ") {
		token := strings.TrimSpace(authHeader[7:])
		if token != "" {
			if u := proxy.LookupUserByToken(token); u != nil {
				if u.Status == 2 {
					return nil, false, fmt.Errorf("ERR_BANNED")
				}
				// Bearer 只代表"用户 SDK 视角"；即使 token 属于 admin 账户，本路径下也以普通用户身份处理。
				// admin 操作工单必须走管理后台浏览器（cookie），与下面的 admin cookie 分支区分。
				return u, false, nil
			}
		}
	}

	// 2) Cookie 路径 = admin 管理后台（仅 admin 账户）
	if cookie := strings.TrimSpace(c.Cookies("daof_admin_token")); cookie != "" {
		if u := proxy.LookupUserByToken(cookie); u != nil && u.Role == "admin" {
			if u.Status == 2 {
				return nil, true, fmt.Errorf("ERR_BANNED")
			}
			return u, true, nil
		}
	}

	return nil, false, fmt.Errorf("ERR_NO_AUTH")
}

// countUnreadForUser 统计该工单内 admin 发的、CreatedAt > userReadAt 的消息数
func countUnreadForUser(ticketID uint, userReadAt *time.Time) int64 {
	q := database.DB.Model(&database.TicketMessage{}).
		Where("ticket_id = ? AND sender = ?", ticketID, "admin")
	if userReadAt != nil {
		q = q.Where("created_at > ?", *userReadAt)
	}
	var n int64
	q.Count(&n)
	return n
}

// countUnreadForAdmin 统计该工单内 user 发的、CreatedAt > adminReadAt 的消息数
func countUnreadForAdmin(ticketID uint, adminReadAt *time.Time) int64 {
	q := database.DB.Model(&database.TicketMessage{}).
		Where("ticket_id = ? AND sender = ?", ticketID, "user")
	if adminReadAt != nil {
		q = q.Where("created_at > ?", *adminReadAt)
	}
	var n int64
	q.Count(&n)
	return n
}

// truncateLog 防止日志中带超长 subject 引起日志爆炸
// truncateLog 按 rune 切片以避免在 UTF-8 多字节字符中间截断（中文 subject 用 byte 切片会乱码）
func truncateLog(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}
