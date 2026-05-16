// Package controller / notification_broadcast.go
//
// 管理员系统通知群发：CRUD + 撤回 + 预览触达。
//
// 群发流程（AdminCreateBroadcast）：
//  1. 创建 NotificationBroadcast 主记录
//  2. 解析 TargetMode + TargetSpec → 一组 user_ids
//  3. 事务内批量 INSERT notifications + INSERT notification_broadcast_targets
//  4. 更新主记录 status=sent / recipient_count
//
// V1 限制：群发的通知 category="broadcast"，强制送达（绕开用户偏好）。
package controller

import (
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"daof-cpa/database"
	"daof-cpa/middleware"
	"daof-cpa/proxy"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// ============================================================================
// 请求/响应结构
// ============================================================================

type broadcastTargetSpec struct {
	PackageID uint   `json:"package_id,omitempty"`
	UserIDs   []uint `json:"user_ids,omitempty"`
}

type broadcastCreateRequest struct {
	Title      string              `json:"title"`
	Body       string              `json:"body"`
	Severity   string              `json:"severity"`
	ActionURL  string              `json:"action_url"`
	ActionText string              `json:"action_text"`
	TargetMode string              `json:"target_mode"`
	TargetSpec broadcastTargetSpec `json:"target_spec"`
}

// ============================================================================
// 公共：解析目标 → user_ids
// ============================================================================

// resolveBroadcastTargets 根据 mode + spec 返回一组活跃用户 ID。
// 失败返回 (nil, error)；空列表合法。
func resolveBroadcastTargets(mode string, spec broadcastTargetSpec) ([]uint, error) {
	switch mode {
	case "all":
		var ids []uint
		// status=1 表示正常用户；2=封禁；不给封禁用户发广播
		if err := database.DB.Model(&database.User{}).
			Where("status = ?", 1).
			Pluck("id", &ids).Error; err != nil {
			return nil, fmt.Errorf("query all users: %w", err)
		}
		return ids, nil

	case "package":
		if spec.PackageID == 0 {
			return nil, fmt.Errorf("package_id required for target_mode=package")
		}
		// 已购该套餐且当前活跃订阅（去重 user_id）
		// fix MAJOR M22-A5（codex 第二十二轮）：必须 join users 过滤 status=1（封禁/已删用户不应再收广播）。
		// 否则被 admin 封禁的用户、已"软删除"匿名化的用户仍能收到 package 群发广播。
		var ids []uint
		if err := database.DB.Table("user_subscriptions").
			Joins("INNER JOIN users ON users.id = user_subscriptions.user_id AND users.status = ? AND users.deleted_at IS NULL", 1).
			Where("user_subscriptions.package_id = ? AND user_subscriptions.status = ? AND user_subscriptions.end_at > ?", spec.PackageID, "active", time.Now()).
			Distinct("user_subscriptions.user_id").
			Pluck("user_subscriptions.user_id", &ids).Error; err != nil {
			return nil, fmt.Errorf("query package subscribers: %w", err)
		}
		return ids, nil

	case "user_ids":
		if len(spec.UserIDs) == 0 {
			return nil, fmt.Errorf("user_ids required for target_mode=user_ids")
		}
		// 过滤掉不存在 / 封禁用户
		var ids []uint
		if err := database.DB.Model(&database.User{}).
			Where("id IN ? AND status = ?", spec.UserIDs, 1).
			Pluck("id", &ids).Error; err != nil {
			return nil, fmt.Errorf("query user_ids: %w", err)
		}
		return ids, nil
	}
	return nil, fmt.Errorf("unsupported target_mode: %s", mode)
}

// ============================================================================
// HTTP 端点
// ============================================================================

// AdminCreateBroadcast POST /api/admin/notifications/broadcasts
func AdminCreateBroadcast(c *fiber.Ctx) error {
	operator := loadAdminUser(c)
	if operator == nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}

	var req broadcastCreateRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_BAD_REQUEST"})
	}
	if req.Title == "" {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_TITLE_REQUIRED"})
	}
	if req.TargetMode == "" {
		req.TargetMode = "all"
	}
	if req.Severity == "" {
		req.Severity = "info"
	}
	// fix Major（codex 第五轮）：admin 群发直写 broadcast/notification 表绕过了
	// proxy.Dispatch 的 IsSafeActionURL 校验。在入口拒绝非站内 URL，统一防钓鱼。
	if !proxy.IsSafeActionURL(req.ActionURL) {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message":      "action_url 必须为站内绝对路径（以 / 开头），禁止外部 URL / javascript: / 协议相对 URL",
			"message_code": "ERR_ACTION_URL_UNSAFE",
		})
	}

	userIDs, err := resolveBroadcastTargets(req.TargetMode, req.TargetSpec)
	if err != nil {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_TARGETS_INVALID",
			"message":      err.Error(),
		})
	}

	// 序列化 spec 入库
	specJSON, _ := json.Marshal(req.TargetSpec)

	now := time.Now()
	bcast := database.NotificationBroadcast{
		OperatorID:     operator.ID,
		Title:          req.Title,
		Body:           req.Body,
		Severity:       req.Severity,
		ActionURL:      req.ActionURL,
		ActionText:     req.ActionText,
		TargetMode:     req.TargetMode,
		TargetSpec:     string(specJSON),
		Status:         "sent",
		RecipientCount: 0,
		CreatedAt:      now,
		SentAt:         &now,
	}

	// 事务：先创建 broadcast 拿到 ID，再批量发 notifications + targets
	// fix MAJOR M22-A5（codex 第二十二轮）：原实现单条失败仅 log 后 continue，
	// 接口仍返回 success → 用户被告知"已发送 N 条"实际只送达 M 条。
	// 改为收集 failedCount，接口最终返回 partial_failed 状态 + 失败数量。
	var failedCount int
	err = database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&bcast).Error; err != nil {
			return fmt.Errorf("create broadcast: %w", err)
		}
		for _, uid := range userIDs {
			dedupKey := fmt.Sprintf("broadcast:%d:%d", bcast.ID, uid)
			n := database.Notification{
				UserID:      uid,
				Category:    "broadcast",
				Severity:    req.Severity,
				Title:       req.Title,
				Body:        req.Body,
				ActionURL:   req.ActionURL,
				ActionText:  req.ActionText,
				RelatedType: "broadcast",
				RelatedID:   bcast.ID,
				DedupKey:    &dedupKey,
				CreatedAt:   now,
			}
			if err := tx.Create(&n).Error; err != nil {
				// 单条失败不阻断整体（dedup 冲突属预期）；记日志 + 计入 failed
				log.Printf("[BROADCAST] create notif uid=%d failed: %v", uid, err)
				failedCount++
				continue
			}
			target := database.NotificationBroadcastTarget{
				BroadcastID:    bcast.ID,
				UserID:         uid,
				NotificationID: n.ID,
				CreatedAt:      now,
			}
			if err := tx.Create(&target).Error; err != nil {
				// fix MAJOR M23-A2（codex 第二十三轮）：target 失败原本只 log + continue，
				// 导致孤儿 notification（用户能收到但 admin 撤回按 target join notifications，漏撤）。
				// 修复：删掉已建的 notification 让本用户失败原子化，failedCount++。
				log.Printf("[BROADCAST] create target uid=%d failed (delete orphan notif=%d): %v", uid, n.ID, err)
				if delErr := tx.Delete(&n).Error; delErr != nil {
					log.Printf("[BROADCAST] failed to delete orphan notif=%d: %v", n.ID, delErr)
					// 删孤儿失败 → 升级为整体事务错误，回滚整批广播
					return fmt.Errorf("delete orphan notif after target fail: %w", delErr)
				}
				failedCount++
				continue
			}
			bcast.RecipientCount++
		}
		// 部分失败 → 标记 broadcast 状态为 partial_failed（前端可显形警示）
		updates := map[string]any{"recipient_count": bcast.RecipientCount}
		if failedCount > 0 {
			updates["status"] = "partial_failed"
			// fix Minor Mi23-1（codex 第二十三轮）：本地 bcast 副本同步状态，避免 API 返回 status='sent' 但 DB 是 partial_failed
			bcast.Status = "partial_failed"
		}
		return tx.Model(&bcast).Updates(updates).Error
	})
	if err != nil {
		log.Printf("[BROADCAST] transaction failed: %v", err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_TRANSACTION"})
	}

	return c.JSON(fiber.Map{
		"success":      true,
		"message_code": "SUCCESS_BROADCAST_SENT",
		"data":         bcast,
		// fix MAJOR M22-A5（codex 第二十二轮）：返回失败数让前端能给 admin 显示部分失败警示
		"failed_count": failedCount,
		"target_count": len(userIDs),
	})
}

// AdminListBroadcasts GET /api/admin/notifications/broadcasts?page=1&page_size=20
func AdminListBroadcasts(c *fiber.Ctx) error {
	if loadAdminUser(c) == nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}

	page, _ := strconv.Atoi(c.Query("page", "1"))
	pageSize, _ := strconv.Atoi(c.Query("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	// fix MEDIUM（silent-failure 第十八轮）：原 Count + Find 静默 → admin 看到"无广播"可能误以为
	// 列表清空，重发时造成通知风暴。fail-closed 500 让 admin 知道是 DB 故障。
	var total int64
	if err := database.DB.Model(&database.NotificationBroadcast{}).Count(&total).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}

	var rows []database.NotificationBroadcast
	if err := database.DB.Order("id desc").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&rows).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}

	// 为每条 broadcast 计算已读数
	results := make([]fiber.Map, 0, len(rows))
	for _, r := range rows {
		readCount := countBroadcastReadCount(r.ID)
		readRate := 0.0
		if r.RecipientCount > 0 {
			readRate = float64(readCount) / float64(r.RecipientCount)
		}
		results = append(results, fiber.Map{
			"id":              r.ID,
			"operator_id":     r.OperatorID,
			"title":           r.Title,
			"body":            r.Body,
			"severity":        r.Severity,
			"action_url":      r.ActionURL,
			"action_text":     r.ActionText,
			"target_mode":     r.TargetMode,
			"target_spec":     r.TargetSpec,
			"status":          r.Status,
			"recipient_count": r.RecipientCount,
			"read_count":      readCount,
			"read_rate":       readRate,
			"created_at":      r.CreatedAt,
			"sent_at":         r.SentAt,
		})
	}

	return c.JSON(fiber.Map{
		"success": true,
		"data":    results,
		"meta": fiber.Map{
			"page":      page,
			"page_size": pageSize,
			"total":     total,
		},
	})
}

// AdminGetBroadcast GET /api/admin/notifications/broadcasts/:id
func AdminGetBroadcast(c *fiber.Ctx) error {
	if loadAdminUser(c) == nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}
	id, _ := strconv.Atoi(c.Params("id"))

	var bcast database.NotificationBroadcast
	if err := database.DB.First(&bcast, id).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"success": false, "message_code": "ERR_NOT_FOUND"})
	}
	readCount := countBroadcastReadCount(bcast.ID)
	readRate := 0.0
	if bcast.RecipientCount > 0 {
		readRate = float64(readCount) / float64(bcast.RecipientCount)
	}
	return c.JSON(fiber.Map{
		"success": true,
		"data": fiber.Map{
			"broadcast":  bcast,
			"read_count": readCount,
			"read_rate":  readRate,
		},
	})
}

// AdminRevokeBroadcast POST /api/admin/notifications/broadcasts/:id/revoke
//
// 撤回流程（事务）：
//  1. broadcast.status 'sent' → 'revoked'（条件 UPDATE，并发只能成功一次）
//  2. 该 broadcast 关联的所有 notifications 设 revoked_at=now
//     → 用户 MyNotifications 接口过滤 revoked_at IS NULL，铃铛立即不再展示
//
// 保留审计：notifications 行不删除，broadcast 表也保留；下次还能看到撤回历史。
func AdminRevokeBroadcast(c *fiber.Ctx) error {
	if loadAdminUser(c) == nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}
	id, _ := strconv.Atoi(c.Params("id"))

	now := time.Now()
	err := database.DB.Transaction(func(tx *gorm.DB) error {
		// fix MAJOR M23-A1（codex 第二十三轮）：partial_failed 也可撤回。否则部分失败的群发反而
		// 比完全成功的群发更难补救（admin 想撤回错误群发但接口拒绝）。
		res := tx.Model(&database.NotificationBroadcast{}).
			Where("id = ? AND status IN ?", id, []string{"sent", "partial_failed"}).
			Update("status", "revoked")
		if res.Error != nil {
			return fmt.Errorf("update broadcast: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return errBroadcastAlreadyRevoked
		}

		// 标记关联的 notifications 为已撤回
		var notifIDs []uint
		if err := tx.Model(&database.NotificationBroadcastTarget{}).
			Where("broadcast_id = ?", id).
			Pluck("notification_id", &notifIDs).Error; err != nil {
			return fmt.Errorf("pluck targets: %w", err)
		}
		if len(notifIDs) > 0 {
			if err := tx.Model(&database.Notification{}).
				Where("id IN ?", notifIDs).
				Update("revoked_at", now).Error; err != nil {
				return fmt.Errorf("revoke notifications: %w", err)
			}
		}
		return nil
	})

	if err == errBroadcastAlreadyRevoked {
		return c.Status(404).JSON(fiber.Map{"success": false, "message_code": "ERR_NOT_FOUND_OR_ALREADY_REVOKED"})
	}
	if err != nil {
		log.Printf("[BROADCAST-REVOKE] failed id=%d: %v", id, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_TRANSACTION"})
	}
	return c.JSON(fiber.Map{"success": true, "message_code": "SUCCESS_REVOKED"})
}

// errBroadcastAlreadyRevoked 哨兵：状态条件 UPDATE 命中 0 行
var errBroadcastAlreadyRevoked = fmt.Errorf("broadcast not found or already revoked")

// AdminPreviewBroadcastTargets GET /api/admin/notifications/preview-targets?mode=...&package_id=...&user_ids=1,2,3
func AdminPreviewBroadcastTargets(c *fiber.Ctx) error {
	if loadAdminUser(c) == nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}

	mode := c.Query("mode", "all")
	spec := broadcastTargetSpec{}
	if pidStr := c.Query("package_id"); pidStr != "" {
		pid, _ := strconv.Atoi(pidStr)
		spec.PackageID = uint(pid)
	}
	if uidsStr := c.Query("user_ids"); uidsStr != "" {
		var uids []uint
		// "1,2,3" → [1,2,3]
		for _, p := range splitCSV(uidsStr) {
			if v, err := strconv.Atoi(p); err == nil && v > 0 {
				uids = append(uids, uint(v))
			}
		}
		spec.UserIDs = uids
	}

	ids, err := resolveBroadcastTargets(mode, spec)
	if err != nil {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_TARGETS_INVALID",
			"message":      err.Error(),
		})
	}
	return c.JSON(fiber.Map{
		"success": true,
		"data":    fiber.Map{"count": len(ids)},
	})
}

// ============================================================================
// Helper
// ============================================================================

// loadAdminUser 从 cookie/header 取 admin token 并查 user。失败返回 nil。
// AdminGuard 已挂在路由组，这里再查一次只为拿到 operator user 实例。
//
// fix Major（自审第十三轮）：原实现用黑名单 `Status==2` 拒绝，AdminGuard 用白名单 `status=1`，
// 语义不一致——任何未来引入 status=0/3+ 的 admin 在 loadAdminUser 处放行（虽然走不到这里）。
// 改为与 AdminGuard 完全一致的 SQL 级 `status = 1` 白名单，消除防御纵深漏洞。
func loadAdminUser(c *fiber.Ctx) *database.User {
	token := middleware.ExtractAdminToken(c)
	if token == "" {
		return nil
	}
	var u database.User
	if err := database.DB.Where("token = ? AND role = ? AND status = ?", token, "admin", 1).First(&u).Error; err != nil {
		return nil
	}
	return &u
}

// countBroadcastReadCount 统计某条 broadcast 已被读的目标数。
// 撤回后的通知不计入（admin 视角：统计在撤回前的已读情况）。
func countBroadcastReadCount(broadcastID uint) int {
	var cnt int64
	database.DB.Table("notification_broadcast_targets AS t").
		Joins("JOIN notifications AS n ON n.id = t.notification_id").
		Where("t.broadcast_id = ? AND n.read_at IS NOT NULL AND n.revoked_at IS NULL", broadcastID).
		Count(&cnt)
	return int(cnt)
}

// splitCSV 简单 CSV 切分（"1,2, 3" → ["1","2","3"]，去 trim 后空值过滤）
func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
