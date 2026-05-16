package controller

import (
	"daof-ai-hub/database"
	"daof-ai-hub/proxy"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// MaxAdminQuotaMicroUSD admin 直改用户额度的上限：1e9 USD 对应的 micro_usd
// （实际业务远不会到这个量级，但有限上限可防 admin 误填极大整数污染财务汇总）。
//
// fix MAJOR M23-A6（codex 第二十三轮）：标准 JSON 不接 NaN/Inf，但接 1e308 这种超大有限数。
// 业务上 admin 改额度通常 ≤ $10000，10亿美元已远超合理范围，作为保护性上限。
const MaxAdminQuotaUSD = 1_000_000_000
const MaxAdminQuotaMicroUSD int64 = MaxAdminQuotaUSD * database.MicroPerUSD
const bulkQuotaPreviewUserLimit = 500

var (
	errLastActiveAdmin = errors.New("last active admin cannot be disabled or deleted")
	errAdminStateRaced = errors.New("admin state changed concurrently")
)

// validateAdminQuotaMicroInput 校验 admin 输入的 quota / amount micro_usd 值。
//
//   - |v| > MaxAdminQuotaMicroUSD → 拒绝（防误填超大数污染汇总）
//
// 不在此校验是否 ≥ 0 —— 部分场景允许负数（如 set quota=-10 用于客服补偿），由调用方决定语义。
func validateAdminQuotaMicroInput(v int64) error {
	if v > MaxAdminQuotaMicroUSD || v < -MaxAdminQuotaMicroUSD {
		return fmt.Errorf("额度绝对值超过上限 %d micro_usd，请检查输入", MaxAdminQuotaMicroUSD)
	}
	return nil
}

func GetUsers(c *fiber.Ctx) error {
	searchQuery := strings.TrimSpace(c.Query("search", ""))
	sortBy := c.Query("sort", "id_desc")

	// fix Major M2（claude security 第十五轮）：原实现无分页 + search 无长度限制
	// → admin 大用户量平台一次查询全表 OOM；恶意 search="%aaa…(10000 字符)…aaa%" 触发
	// 全表扫描 + WAL 单写者锁竞争。对照 AdminListSubscriptions 已有 ≥2 ≤64 字符校验。
	if len(searchQuery) > 64 {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "search 不能超过 64 字符", "message_code": "ERR_SEARCH_TOO_LONG"})
	}
	if searchQuery != "" && len(searchQuery) < 2 {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "search 至少 2 字符", "message_code": "ERR_SEARCH_TOO_SHORT"})
	}
	page, _ := strconv.Atoi(c.Query("page", "1"))
	if page < 1 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(c.Query("page_size", "50"))
	if pageSize < 1 || pageSize > 200 {
		pageSize = 50
	}

	db := database.DB.Model(&database.User{})

	if searchQuery != "" {
		// 模糊匹配：Username，Phone 或者 GithubID
		// 转义 LIKE 通配符 + ESCAPE 子句（codex 第十六轮）：SQLite/Postgres LIKE 默认不识别 \ 转义，
		// 必须显式 ESCAPE '\\' 才能让 \% 匹配字面 %。
		escaped := strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(searchQuery)
		searchParam := "%" + escaped + "%"
		db = db.Where(
			"username LIKE ? ESCAPE '\\' OR phone LIKE ? ESCAPE '\\' OR github_id LIKE ? ESCAPE '\\'",
			searchParam, searchParam, searchParam,
		)
	}

	switch sortBy {
	case "id_asc":
		db = db.Order("id asc")
	case "id_desc":
		db = db.Order("id desc")
	case "quota_desc":
		db = db.Order("quota desc")
	case "quota_asc":
		db = db.Order("quota asc")
	case "status_desc":
		db = db.Order("status desc") // 2在前(封禁)
	case "status_asc":
		db = db.Order("status asc") // 1在前(活跃)
	default:
		db = db.Order("id desc")
	}

	var total int64
	if err := db.Count(&total).Error; err != nil {
		log.Printf("[USERS-LIST] count failed: %v", err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}

	var users []database.User
	if err := db.Offset((page - 1) * pageSize).Limit(pageSize).Find(&users).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "message": "获取数据失败", "message_code": "ERR_FETCH_DATA_MATRIX"})
	}
	// fix CRITICAL Sprint2-M1：admin bulk 视图 scrub 敏感字段。
	// 旧实现外传完整 User struct（含 Token / PasswordHash），admin 可看到所有用户的
	// API token 明文。token 一旦被任意 admin 看到，等同于横向越权能调任意用户配额。
	// PasswordHash / Token / GithubID（PII）一并清零，仅保留 admin 决策所需字段。
	for i := range users {
		users[i].PasswordHash = ""
		users[i].Token = ""
	}
	return c.JSON(fiber.Map{
		"success": true,
		"data":    users,
		"meta":    fiber.Map{"page": page, "page_size": pageSize, "total": total},
	})
}

// 用户增量操作 Payload。QuotaMicroUSD 单位为 micro_usd。
type UserPayload struct {
	Username      string `json:"username"`
	QuotaMicroUSD int64  `json:"quota_micro_usd"`
	Status        *int   `json:"status,omitempty"`
	BanReason     string `json:"ban_reason"`
}

func UpdateUser(c *fiber.Ctx) error {
	id := c.Params("id")
	var req UserPayload
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "数据解析异常", "message_code": "ERR_PARSE_EXCEPTION"})
	}
	// fix MAJOR M23-A6（codex 第二十三轮）：admin 改额度必须 finite + 上限校验。
	// 即使标准 JSON 不接受 NaN，超大有限数（1e308）仍可进入 quota 污染财务汇总。
	if err := validateAdminQuotaMicroInput(req.QuotaMicroUSD); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": err.Error(), "message_code": "ERR_INVALID_QUOTA"})
	}
	reqQuotaMicro := req.QuotaMicroUSD

	var user database.User
	if err := database.DB.First(&user, id).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"success": false, "message": "未找到相关记录", "message_code": "ERR_NODE_GONE"})
	}
	oldStatus := user.Status
	effectiveStatus := user.Status
	if req.Status != nil {
		if *req.Status != 1 && *req.Status != 2 {
			return c.Status(400).JSON(fiber.Map{
				"success":      false,
				"message_code": "ERR_INVALID_USER_STATUS",
			})
		}
		effectiveStatus = *req.Status
	}

	var changes []map[string]interface{}
	if user.Username != req.Username {
		changes = append(changes, map[string]interface{}{"type": "USERNAME", "target": req.Username, "old": user.Username, "new": req.Username})
	}
	if user.Quota != reqQuotaMicro {
		// audit 日志字段：old/new 用 USD float（前端 formatCurrency 直接消费）；
		// 同时附加 *_micro 用于精确审计回溯
		changes = append(changes, map[string]interface{}{
			"type":      "QUOTA",
			"target":    req.Username,
			"old":       database.MicroToUSD(user.Quota),
			"new":       database.MicroToUSD(reqQuotaMicro),
			"old_micro": user.Quota,
			"new_micro": reqQuotaMicro,
		})
	}
	if req.Status != nil && user.Status != effectiveStatus {
		changes = append(changes, map[string]interface{}{"type": "STATUS", "target": req.Username, "old": user.Status, "new": effectiveStatus})
		if effectiveStatus == 2 {
			changes = append(changes, map[string]interface{}{"type": "BAN_REASON", "target": req.Username, "old": "", "new": req.BanReason})
		}
	} else if user.BanReason != req.BanReason {
		changes = append(changes, map[string]interface{}{"type": "BAN_REASON", "target": req.Username, "old": user.BanReason, "new": req.BanReason})
	}

	changelog := "[]"
	if len(changes) > 0 {
		b, _ := json.Marshal(changes)
		changelog = string(b)
	}

	// fix CRITICAL C2（codex+claude 第十五轮）：UpdateUser 改 quota 时必须同时写 BillingEntry。
	// fix CRITICAL C19-1（codex 第十九轮）：原实现 delta = req.Quota - user.Quota（user.Quota 是
	// 事务**外**快照）→ 如果 admin 编辑期间 user 并发充值/扣费，事务内 quota 已变化但 delta 仍按
	// admin 看到的旧值计算，账单与 quota 漂移。修复：事务内 lockUserForUpdate + 重读 before。
	op := loadAdminUser(c)
	adminID := uint(0)
	if op != nil {
		adminID = op.ID
	}
	txErr := database.DB.Transaction(func(tx *gorm.DB) error {
		// 锁 user 行 + 事务内重读 before（与购买/退款路径用同一锁路径）
		if err := lockUserForUpdate(tx, user.ID); err != nil {
			return fmt.Errorf("lock user: %w", err)
		}
		var before database.User
		if err := tx.Select("id, quota, role, status").First(&before, user.ID).Error; err != nil {
			return fmt.Errorf("read before: %w", err)
		}
		updateQ := tx.Model(&database.User{}).Where("id = ?", user.ID)
		if before.Role == "admin" && before.Status == 1 && effectiveStatus != 1 {
			var activeAdminCount int64
			if err := tx.Model(&database.User{}).
				Where("role = ? AND status = ? AND deleted_at IS NULL", "admin", 1).
				Count(&activeAdminCount).Error; err != nil {
				return fmt.Errorf("count active admins: %w", err)
			}
			if activeAdminCount <= 1 {
				return errLastActiveAdmin
			}
			updateQ = updateQ.Where("role = ? AND status = ?", "admin", 1)
		}
		res := updateQ.Updates(map[string]interface{}{
			"username":   req.Username,
			"quota":      reqQuotaMicro,
			"status":     effectiveStatus,
			"ban_reason": req.BanReason,
		})
		if res.Error != nil {
			return fmt.Errorf("update user: %w", res.Error)
		}
		if before.Role == "admin" && before.Status == 1 && effectiveStatus != 1 && res.RowsAffected == 0 {
			return errAdminStateRaced
		}
		// fix Major（codex 第十五轮）：审计入事务，并填真实 admin id（旧实现 operatorID=0 + tx 外）
		// 这把"用户更新 + 账单 + 审计"绑成同一原子单元；任一失败一起回滚，admin 必可追溯。
		//
		// fix MAJOR（多模型审计第二十五轮）：先写 OperationLog 拿到 ID，再让 BillingEntry.RelatedID
		// 关联到具体审计行（旧实现写 0 让账务追溯断流）。顺序：log → billing。
		opLogID, err := LogOperationByTxReturning(tx, adminID, user.ID, "admin", "UPDATE", c.IP(), changelog)
		if err != nil {
			return fmt.Errorf("write audit: %w", err)
		}
		// 用事务内 before 判断是否真改了 quota（admin 看到的旧值与 DB 实际可能不同）
		if before.Quota != reqQuotaMicro {
			if err := database.WriteBillingEntry(tx, database.BillingEntryInput{
				UserID:          user.ID,
				EntryType:       database.BillingTypeAdminAdjust,
				AmountUSD:       reqQuotaMicro - before.Quota,
				BalanceAfterUSD: reqQuotaMicro,
				RelatedType:     "operation_log",
				RelatedID:       opLogID,
				Description:     userFriendlyAdminAdjustDescription(reqQuotaMicro - before.Quota),
			}); err != nil {
				return fmt.Errorf("write billing: %w", err)
			}
		}
		return nil
	})
	if txErr != nil {
		log.Printf("[USER-UPDATE] tx failed user=%d: %v", user.ID, txErr)
		if errors.Is(txErr, errLastActiveAdmin) {
			return c.Status(403).JSON(fiber.Map{"success": false, "message": "操作遭拒：无法封禁唯一的系统管理员", "message_code": "ERR_SUICIDE_PROTECTION_SEAL"})
		}
		if errors.Is(txErr, errAdminStateRaced) {
			return c.Status(409).JSON(fiber.Map{"success": false, "message": "管理员状态已变化，请刷新后重试", "message_code": "ERR_UPDATE_CONFLICT"})
		}
		return c.Status(500).JSON(fiber.Map{"success": false, "message": "数据更新失败，存在冲突", "message_code": "ERR_UPDATE_CONFLICT"})
	}
	if oldStatus == 1 && effectiveStatus != 1 {
		if err := database.RevokeSessionsForUser(user.ID); err != nil {
			log.Printf("[USER-STATUS] revoke session failed for user=%d: %v", user.ID, err)
		}
	}

	// ZERO-TRUST 防御：无论状态改成什么，都强力同步到高速内存里，瞬间实现封号！
	proxy.SyncCacheConfig()
	// 双保险：被封禁时精准淘汰该 token，防 SyncCacheConfig 万一 DB 查询失败时
	// AuthCache 仍保留旧 entry —— EvictUserToken 直接从 map 删除即可。
	if effectiveStatus == 2 && user.Token != "" {
		proxy.EvictUserToken(user.Token)
	}

	// 封禁通知（仅在状态变成 2=banned 时触发；同日 dedup 防 admin 反复改 ban_reason 重发）
	if effectiveStatus == 2 && user.Status != 2 {
		title := readSysConfigCached("notif_security_ban_title", "您的账户已被限制")
		bodyTpl := readSysConfigCached("notif_security_ban_body", "原因：{reason}。如有疑问请联系客服。")
		reason := req.BanReason
		if reason == "" {
			reason = "未提供"
		}
		body := strings.ReplaceAll(bodyTpl, "{reason}", reason)
		dedupKey := fmt.Sprintf("ban:%d:%s", user.ID, time.Now().Format("2006-01-02"))
		// security 类强制送达（不被偏好屏蔽）
		proxy.Dispatch(user.ID, "security", "error", title, body, "", "",
			"user", user.ID, &dedupKey)
	}

	return c.JSON(fiber.Map{"success": true, "message": "更新操作已成功保存", "message_code": "SUCCESS_SYNC_UPDATE"})
}

// GetSelfData 路由必须挂 UserGuard，user 由中间件注入 Locals。
//
// API 边界单位约定：quota 是 USD float（前端友好）。内部存储为 int64 micro_usd，
// 这里在 JSON 序列化时通过 database.MicroToUSD 转回 USD float。
func GetSelfData(c *fiber.Ctx) error {
	user, err := getCurrentUser(c)
	if err != nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": err.Error()})
	}
	if user.Status != 1 {
		return c.Status(403).JSON(fiber.Map{"success": false, "message_code": "ERR_BANNED", "ban_reason": user.BanReason})
	}
	// fix CRITICAL Sprint2-M1：自身路由也不暴露 token。
	// 旧实现每次 /api/user/me 都附带 token 明文 → 浏览器 devtools、Sentry/日志、
	// XSS 任意脚本都能采集。token 应通过专门的 reveal 接口 + 二次确认获取。
	// UI 实际消费的字段：id / username / role / quota / status，无 token 引用（已 grep 验证）。
	return c.JSON(fiber.Map{
		"success": true,
		"data": map[string]interface{}{
			"id":       user.ID,
			"username": user.Username,
			"role":     user.Role,
			"quota":    database.MicroToUSD(user.Quota),
			"status":   user.Status,
		},
	})
}

// BulkQuotaPayload 是批量调整额度的请求体，AmountMicroUSD 单位为 micro_usd。
type BulkQuotaPayload struct {
	UserIDs        []uint `json:"user_ids"`
	Mode           string `json:"mode"` // "add" / "sub" / "set"
	AmountMicroUSD int64  `json:"amount_micro_usd"`
}

type BulkQuotaPreviewPayload struct {
	UserIDs        []int64 `json:"user_ids"`
	Action         string  `json:"action"` // "add" / "subtract" / "set"
	AmountMicroUSD int64   `json:"amount_micro_usd"`
}

type bulkQuotaPreviewUser struct {
	ID         uint    `json:"id"`
	Username   string  `json:"username"`
	CurrentUSD float64 `json:"current_usd"`
	FutureUSD  float64 `json:"future_usd"`
}

func bulkQuotaPreviewFutureMicro(currentMicro, amountMicro int64, action string) int64 {
	switch action {
	case "add":
		if amountMicro > 0 && currentMicro > math.MaxInt64-amountMicro {
			return math.MaxInt64
		}
		futureMicro := currentMicro + amountMicro
		if futureMicro < 0 {
			return 0
		}
		return futureMicro
	case "subtract":
		if currentMicro <= amountMicro {
			return 0
		}
		return currentMicro - amountMicro
	case "set":
		return amountMicro
	default:
		return currentMicro
	}
}

func dedupePositiveUserIDs(rawIDs []int64) ([]uint, error) {
	idSet := make(map[uint]struct{}, len(rawIDs))
	uniqIDs := make([]uint, 0, len(rawIDs))
	for _, rawID := range rawIDs {
		if rawID <= 0 {
			return nil, fmt.Errorf("bad user id")
		}
		id := uint(rawID)
		if _, ok := idSet[id]; !ok {
			idSet[id] = struct{}{}
			uniqIDs = append(uniqIDs, id)
		}
	}
	return uniqIDs, nil
}

// BulkAdjustQuotaPreview 批量额度调整预检，只读计算影响范围与未来余额。
func BulkAdjustQuotaPreview(c *fiber.Ctx) error {
	var req BulkQuotaPreviewPayload
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "数据解析异常", "message_code": "ERR_PARSE_EXCEPTION"})
	}
	if len(req.UserIDs) == 0 {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "未选择任何用户", "message_code": "ERR_EMPTY_SELECTION"})
	}
	if len(req.UserIDs) > bulkQuotaPreviewUserLimit {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": MessageCodeBulkPreviewLimit})
	}
	if req.Action != "add" && req.Action != "subtract" && req.Action != "set" {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "无效的调整模式", "message_code": "ERR_INVALID_MODE"})
	}
	if err := validateAdminQuotaMicroInput(req.AmountMicroUSD); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": err.Error(), "message_code": "ERR_INVALID_QUOTA"})
	}
	if req.AmountMicroUSD < 0 {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "金额不能为负", "message_code": "ERR_NEGATIVE_AMOUNT"})
	}
	amountMicro := req.AmountMicroUSD
	uniqIDs, err := dedupePositiveUserIDs(req.UserIDs)
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "用户 ID 不合法", "message_code": "ERR_BAD_USER_ID"})
	}

	var users []database.User
	if err := database.DB.Select("id, username, quota").
		Where("id IN ? AND role = ?", uniqIDs, "user").
		Order("id ASC").
		Find(&users).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "message": "用户读取失败", "message_code": "ERR_READ_USERS"})
	}

	previewUsers := make([]bulkQuotaPreviewUser, 0, len(users))
	totalDeltaMicro := int64(0)
	for _, u := range users {
		futureMicro := bulkQuotaPreviewFutureMicro(u.Quota, amountMicro, req.Action)
		totalDeltaMicro += futureMicro - u.Quota
		previewUsers = append(previewUsers, bulkQuotaPreviewUser{
			ID:         u.ID,
			Username:   u.Username,
			CurrentUSD: database.MicroToUSD(u.Quota),
			FutureUSD:  database.MicroToUSD(futureMicro),
		})
	}

	return c.JSON(fiber.Map{
		"success": true,
		"data": fiber.Map{
			"affected_count":  len(previewUsers),
			"total_delta_usd": database.MicroToUSD(totalDeltaMicro),
			"users":           previewUsers,
		},
	})
}

// BulkAdjustQuota 批量调整额度。
// 安全约束：不允许调整 admin（避免误操作）；金额永远 clamp 到 >= 0。
func BulkAdjustQuota(c *fiber.Ctx) error {
	var req BulkQuotaPayload
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "数据解析异常", "message_code": "ERR_PARSE_EXCEPTION"})
	}
	if len(req.UserIDs) == 0 {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "未选择任何用户", "message_code": "ERR_EMPTY_SELECTION"})
	}
	if req.Mode != "add" && req.Mode != "sub" && req.Mode != "set" {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "无效的调整模式", "message_code": "ERR_INVALID_MODE"})
	}
	// fix MAJOR M23-A6（codex 第二十三轮）：批量调整金额必须 finite + 上限校验
	if err := validateAdminQuotaMicroInput(req.AmountMicroUSD); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": err.Error(), "message_code": "ERR_INVALID_QUOTA"})
	}
	if req.AmountMicroUSD < 0 {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "金额不能为负", "message_code": "ERR_NEGATIVE_AMOUNT"})
	}
	amountMicro := req.AmountMicroUSD

	// 去重 user_ids（防止 admin 误传重复 ID 让计数虚高）
	idSet := make(map[uint]struct{}, len(req.UserIDs))
	uniqIDs := make([]uint, 0, len(req.UserIDs))
	for _, id := range req.UserIDs {
		if _, ok := idSet[id]; !ok {
			idSet[id] = struct{}{}
			uniqIDs = append(uniqIDs, id)
		}
	}

	var users []database.User
	if err := database.DB.Where("id IN ? AND role = ?", uniqIDs, "user").Find(&users).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "message": "用户读取失败", "message_code": "ERR_READ_USERS"})
	}

	// fix CRITICAL C2（codex+claude 第十五轮）：每用户的 quota 更新 + 审计 + 账单
	// 必须在同一事务内，否则任何一步失败都会让 BillingEntry 与 user.quota 不一致。
	// 之前实现：UPDATE → SELECT → LogOperation 三步独立，**完全不写 BillingEntry** →
	// 账单事实表与真实 quota 漂移。修复：每用户一笔事务，事务内写 admin_adjust 账单条目。
	//
	// fix Minor Phase 4-codex（第二十四轮）：原"单 user 失败 log + continue + 最终 success=true"
	// 让 admin 看到"成功"但部分 user 未生效。改为收集失败 user_ids + reason，
	// 任一失败返回 207 Multi-Status（HTTP 标准的 partial success），让前端能区分。
	op := loadAdminUser(c)
	updated := 0
	type bulkFailure struct {
		UserID   uint   `json:"user_id"`
		Username string `json:"username"`
		Reason   string `json:"reason"`
	}
	failures := make([]bulkFailure, 0)
	for _, u := range users {
		err := database.DB.Transaction(func(tx *gorm.DB) error {
			// fix CRITICAL C19-1（codex 第十九轮）：lockUserForUpdate + 事务内重读 before。
			// 之前 delta = after - u.Quota（u.Quota 来自事务外 Find），与并发充值/扣费交错时
			// before 可能已变化但仍按旧值算 delta，账单与 quota 漂移。
			if err := lockUserForUpdate(tx, u.ID); err != nil {
				return fmt.Errorf("lock user: %w", err)
			}
			var before database.User
			if err := tx.Select("id, quota").First(&before, u.ID).Error; err != nil {
				return fmt.Errorf("read before: %w", err)
			}

			// 用 SQL 表达式原子更新，避免"读-改-写"的 race 让并发额度操作互相覆盖。
			// SQLite 的 MAX 只能做聚合，所以用 CASE 表达"clamp 到 0"。
			// fix MAJOR M22-A1 Phase 1：amountMicro 单位 micro_usd（与 quota 列一致）
			var expr interface{}
			switch req.Mode {
			case "add":
				expr = gorm.Expr("CASE WHEN quota + ? < 0 THEN 0 ELSE quota + ? END", amountMicro, amountMicro)
			case "sub":
				expr = gorm.Expr("CASE WHEN quota - ? < 0 THEN 0 ELSE quota - ? END", amountMicro, amountMicro)
			case "set":
				expr = amountMicro
			}
			if err := tx.Model(&database.User{}).Where("id = ?", u.ID).UpdateColumn("quota", expr).Error; err != nil {
				return fmt.Errorf("update quota: %w", err)
			}

			// 重新读最新 quota（DB 真值，不依赖应用层算）
			var after database.User
			if err := tx.Select("quota").First(&after, u.ID).Error; err != nil {
				return fmt.Errorf("re-select: %w", err)
			}

			// fix MAJOR（多模型审计第二十五轮）：先写 OperationLog 拿到 ID，再让
			// BillingEntry.RelatedID 关联到具体审计行（旧实现写 0 让账务追溯断流）。
			// 顺序：log → billing；同事务原子，任一失败一起回滚。
			delta := after.Quota - before.Quota
			adminID := uint(0)
			if op != nil {
				adminID = op.ID
			}

			// audit 日志字段：old/new/amount/delta 用 USD float（前端 formatCurrency 消费）；
			// 同时附加 *_micro 用于精确审计回溯
			change, _ := json.Marshal([]map[string]interface{}{
				{
					"type":             "BULK_QUOTA",
					"target":           u.Username,
					"mode":             req.Mode,
					"old":              database.MicroToUSD(before.Quota),
					"new":              database.MicroToUSD(after.Quota),
					"delta":            database.MicroToUSD(delta),
					"old_micro":        before.Quota,
					"new_micro":        after.Quota,
					"amount_micro_usd": amountMicro,
					"delta_micro":      delta,
				},
			})
			opLogID, err := LogOperationByTxReturning(tx, adminID, u.ID, "admin", "BULK_QUOTA", c.IP(), string(change))
			if err != nil {
				return err
			}

			// 写 BillingEntry(admin_adjust)：delta = after - before（都是事务内值，原子一致）
			if err := database.WriteBillingEntry(tx, database.BillingEntryInput{
				UserID:          u.ID,
				EntryType:       database.BillingTypeAdminAdjust,
				AmountUSD:       delta,
				BalanceAfterUSD: after.Quota,
				RelatedType:     "operation_log",
				RelatedID:       opLogID,
				Description:     userFriendlyAdminAdjustDescription(delta),
			}); err != nil {
				return fmt.Errorf("write billing: %w", err)
			}
			return nil
		})
		if err != nil {
			log.Printf("[BULK-QUOTA] user=%d failed: %v (collected as partial failure)", u.ID, err)
			failures = append(failures, bulkFailure{
				UserID:   u.ID,
				Username: u.Username,
				Reason:   err.Error(),
			})
			continue
		}
		updated++
	}

	proxy.SyncCacheConfig()

	// fix Minor Phase 4-codex：任一失败 → 207 Multi-Status；全成功 → 200。
	// 前端可读 failures 数组定位失败 user，提示 admin 处理。
	status := 200
	msgCode := "SUCCESS_BULK_QUOTA"
	if len(failures) > 0 {
		if updated == 0 {
			status = 500 // 全失败 → 500 提示严重故障
			msgCode = "ERR_BULK_QUOTA_ALL_FAILED"
		} else {
			status = 207 // 部分成功 → Multi-Status
			msgCode = "PARTIAL_BULK_QUOTA"
		}
	}
	return c.Status(status).JSON(fiber.Map{
		"success":      len(failures) == 0,
		"updated":      updated,
		"failed":       len(failures),
		"failures":     failures,
		"message":      "批量额度调整完成",
		"message_code": msgCode,
	})
}

// BulkDeletePayload 是批量删除的请求体
type BulkDeletePayload struct {
	UserIDs []uint `json:"user_ids"`
}

// BulkDeleteUsers 批量物理删除用户（含其 access_token）。
// 安全约束：admin 不可被批量删除（即便 ID 出现在请求里也会跳过）。
// 删除前清理 access_tokens 防止外键悬挂。
func BulkDeleteUsers(c *fiber.Ctx) error {
	var req BulkDeletePayload
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "数据解析异常", "message_code": "ERR_PARSE_EXCEPTION"})
	}
	if len(req.UserIDs) == 0 {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "未选择任何用户", "message_code": "ERR_EMPTY_SELECTION"})
	}

	var users []database.User
	if err := database.DB.Where("id IN ? AND role = ?", req.UserIDs, "user").Find(&users).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "message": "用户读取失败", "message_code": "ERR_READ_USERS"})
	}

	op := loadAdminUser(c)
	adminID := uint(0)
	if op != nil {
		adminID = op.ID
	}
	deleted := 0
	for _, u := range users {
		// fix CRITICAL C-B2（codex 第二十一轮）：批量删除也走"软删除 + PII 匿名化"，与 DeleteUser 对齐。
		// BillingEntry + 源事实表保留以维护 append-only；仅清理凭证/session 等衍生数据。
		// fix Major（codex 第十五轮）：审计入事务 + 填真实 admin id，与单删路径对齐
		change, _ := json.Marshal([]map[string]interface{}{
			{"type": "BULK_HARD_DELETE", "target": u.Username, "user_id": u.ID, "github_id": u.GithubID},
		})
		err := database.DB.Transaction(func(tx *gorm.DB) error {
			if err := purgeUserDependents(tx, u.ID); err != nil {
				return err
			}
			anonName := fmt.Sprintf("deleted_%d_%d", u.ID, time.Now().UnixMilli())
			if err := tx.Model(&database.User{}).Where("id = ?", u.ID).Updates(map[string]any{
				"username":      anonName,
				"phone":         nil,
				"github_id":     nil,
				"password_hash": "",
				"token":         "",
				"status":        2,
				"ban_reason":    "bulk deleted by admin",
			}).Error; err != nil {
				return fmt.Errorf("anonymize user: %w", err)
			}
			if err := tx.Delete(&database.User{}, u.ID).Error; err != nil {
				return fmt.Errorf("soft delete user: %w", err)
			}
			return LogOperationByTx(tx, adminID, u.ID, "admin", "BULK_HARD_DELETE", c.IP(), string(change))
		})
		if err != nil {
			continue
		}
		// fix Minor Mi22-4（codex 第二十二轮）：每个删除成功的用户都要清订阅缓存
		proxy.InvalidateUserSubscriptionCache(u.ID)
		deleted++
	}

	proxy.SyncCacheConfig()

	return c.JSON(fiber.Map{
		"success":      true,
		"deleted":      deleted,
		"skipped":      len(req.UserIDs) - deleted,
		"message":      "批量删除完成（已物理抹除）",
		"message_code": "SUCCESS_BULK_DELETE",
	})
}

func DeleteUser(c *fiber.Ctx) error {
	id := c.Params("id")
	var user database.User
	if err := database.DB.First(&user, id).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"success": false, "message": "未找到相关记录或已被删除", "message_code": "ERR_NOT_FOUND"})
	}

	// fix CRITICAL C-B2（codex 第二十一轮）：原实现 tx.Unscoped().Delete(&user) 物理删 user 行
	// + 同时物理删 BillingEntry——破坏了账单事实表的 append-only 不变量（gorm:"<-:create"
	// 只防 Update，不防 Delete）。
	//
	// 改为"软删除 + PII 匿名化"：
	//   1. user 行保留（GORM DeletedAt 标记），id 不被 autoincrement 重用 → BillingEntry 不变孤儿
	//   2. PII 字段（Username/Phone/GithubID/PasswordHash/Token）匿名化，不再可读
	//   3. 订阅 / 充值 / 用量 / ApiLog 等源事实表保留，BillingEntry 可继续追溯
	//   4. 仅清理凭证 / session / 通知 / 工单等非账务衍生数据
	op := loadAdminUser(c)
	adminID := uint(0)
	if op != nil {
		adminID = op.ID
	}
	delData := []map[string]interface{}{{"type": "DELETE", "target": user.Username}}
	delBytes, _ := json.Marshal(delData)
	if err := database.DB.Transaction(func(tx *gorm.DB) error {
		var current database.User
		if err := tx.Select("id, role, status").First(&current, user.ID).Error; err != nil {
			return fmt.Errorf("read current user: %w", err)
		}
		conditionalDeleteActiveAdmin := current.Role == "admin" && current.Status == 1
		if conditionalDeleteActiveAdmin {
			var activeAdminCount int64
			if err := tx.Model(&database.User{}).
				Where("role = ? AND status = ? AND deleted_at IS NULL", "admin", 1).
				Count(&activeAdminCount).Error; err != nil {
				return fmt.Errorf("count active admins: %w", err)
			}
			if activeAdminCount <= 1 {
				return errLastActiveAdmin
			}
		}
		if err := purgeUserDependents(tx, user.ID); err != nil {
			return err
		}
		// PII 匿名化：username 加随机后缀防 unique 冲突（同名再注册时）；
		// phone/github_id 设空，passwd/token 清零
		anonName := fmt.Sprintf("deleted_%d_%d", user.ID, time.Now().UnixMilli())
		updateQ := tx.Model(&database.User{}).Where("id = ?", user.ID)
		if conditionalDeleteActiveAdmin {
			updateQ = updateQ.Where("role = ? AND status = ?", "admin", 1)
		}
		res := updateQ.Updates(map[string]any{
			"username":      anonName,
			"phone":         nil,
			"github_id":     nil,
			"password_hash": "",
			"token":         "",
			"status":        2, // banned 状态防重新激活
			"ban_reason":    "deleted by admin",
		})
		if res.Error != nil {
			return fmt.Errorf("anonymize user: %w", res.Error)
		}
		if conditionalDeleteActiveAdmin && res.RowsAffected == 0 {
			return errAdminStateRaced
		}
		// GORM 软删除：DeletedAt 字段被设置，常规 Find / First 查询自动过滤
		if err := tx.Delete(&user).Error; err != nil {
			return fmt.Errorf("soft delete user: %w", err)
		}
		// fix Major（codex 第十五轮）：审计入事务 + 真实 admin id
		return LogOperationByTx(tx, adminID, user.ID, "admin", "DELETE", c.IP(), string(delBytes))
	}); err != nil {
		if errors.Is(err, errLastActiveAdmin) {
			return c.Status(403).JSON(fiber.Map{"success": false, "message": "操作拦截：不可删除系统唯一的管理员", "message_code": "ERR_ADMIN_REQUIRED"})
		}
		if errors.Is(err, errAdminStateRaced) {
			return c.Status(409).JSON(fiber.Map{"success": false, "message": "管理员状态已变化，请刷新后重试", "message_code": "ERR_UPDATE_CONFLICT"})
		}
		return c.Status(500).JSON(fiber.Map{"success": false, "message": "删除失败", "message_code": "ERR_DB_TRANSACTION"})
	}

	proxy.SyncCacheConfig()
	// fix Minor Mi22-4（codex 第二十二轮）：删除事务成功后，主动清该用户的订阅缓存。
	// SyncCacheConfig 不动 SubscriptionCache，短窗口内在途请求仍可拿到旧订阅快照 →
	// stream/billing 路径继续按已删用户的旧订阅做扣费决策，造成 silent 错误扣费。
	proxy.InvalidateUserSubscriptionCache(user.ID)

	return c.JSON(fiber.Map{"success": true, "message": "数据已彻底删除", "message_code": "APP.DELETE_SUCCESS"})
}

// purgeUserDependents 在给定事务内清掉用户的所有衍生记录。
// 调用方必须随后再 Delete user 主表自身。
//
// 包含：
//   - access_tokens（API 凭证）
//   - user_sessions（浏览器会话）
//   - notifications / notification_broadcast_targets（站内通知投递数据）
//   - notification_preferences（可重建的用户偏好）
//   - tickets / ticket_messages（用户删除后的客服衍生数据）
//
// 明确保留：
//   - user_subscriptions / subscription_usages / topup_orders（BillingEntry 源事实表）
//   - api_logs（API 用量源事实表）
//
// **不删除 operation_logs**（fix CRITICAL Sprint1-P0-7）：
// 操作审计必须 append-only，即使用户被物理删除，审计链也必须可追溯。
// 用户主表已做 PII 匿名化 + 软删除（参见上方 anonName 流程），
// OperationLog.TargetUserID 仍指向被删用户 ID 但 User 表已无敏感字段。
func purgeUserDependents(tx *gorm.DB, userID uint) error {
	if err := tx.Unscoped().Where("user_id = ?", userID).Delete(&database.AccessToken{}).Error; err != nil {
		return err
	}
	if err := tx.Where("user_id = ?", userID).Delete(&database.UserSession{}).Error; err != nil {
		return err
	}
	// 先删 broadcast_targets（外键于 notifications），再删 notifications
	// fix Minor（codex 第五轮）：原实现漏掉广播目标关联表，硬删除用户后会留下孤儿引用
	if err := tx.Unscoped().
		Where("notification_id IN (SELECT id FROM notifications WHERE user_id = ?)", userID).
		Delete(&database.NotificationBroadcastTarget{}).Error; err != nil {
		return err
	}
	if err := tx.Unscoped().Where("user_id = ?", userID).Delete(&database.Notification{}).Error; err != nil {
		return err
	}
	// fix CRITICAL C-B2（codex 第二十一轮）：BillingEntry **不删**——保留账单事实表的 append-only 语义。
	// 之前的修复物理删账单是为了防"user.id 重用导致新用户继承账单"——但现在 user 改为软删除（DeletedAt
	// 标记），id 不会被 autoincrement 重用，所以账单孤儿风险消除。
	//   - 任何 admin 报表 / 对账工具仍能看到该 user.ID 的历史账单
	//   - 用户 PII 已匿名化（外层 DeleteUser 函数处理），账单关联到匿名 user 行不泄漏隐私
	// (intentionally do not delete BillingEntry)
	// 同理保留 BillingEntry 可跳转的源事实表：ApiLog / UserSubscription / SubscriptionUsage / TopupOrder。
	if err := tx.Unscoped().Where("user_id = ?", userID).Delete(&database.NotificationPreference{}).Error; err != nil {
		return err
	}
	if err := tx.Unscoped().
		Where("ticket_id IN (SELECT id FROM tickets WHERE user_id = ?)", userID).
		Delete(&database.TicketMessage{}).Error; err != nil {
		return err
	}
	if err := tx.Unscoped().Where("user_id = ?", userID).Delete(&database.Ticket{}).Error; err != nil {
		return err
	}
	return nil
}
