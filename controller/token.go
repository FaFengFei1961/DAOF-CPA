package controller

import (
	"errors"
	"fmt"
	"log"
	"math"
	"time"

	"daof-ai-hub/database"
	"daof-ai-hub/proxy"
	"daof-ai-hub/utils"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// errTokenLimitReached 事务内业务级 sentinel
var errTokenLimitReached = errors.New("user token quota reached")

// isValidQuotaLimit 业务约定：QuotaLimit 必须 ≥ 0 且为有限数（不能 NaN/+Inf/-Inf）。
//   - 0 = 无限制（最常见）
//   - 正数 = 具体上限
//   - 负数 / Inf / NaN = 非法（拒绝写入）
//
// 校验前端输入（USD float），通过后转 micro_usd 入业务。
func isValidQuotaLimit(v float64) bool {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return false
	}
	return v >= 0
}

// getCurrentUser 从 UserGuard / AdminGuard 注入到 Locals 的 user 取出。
// 路由必须挂 UserGuard / AdminGuard，否则视为编程错误（panic 比静默 401 更好定位）。
func getCurrentUser(c *fiber.Ctx) (*database.User, error) {
	v := c.Locals("user")
	if v == nil {
		return nil, fmt.Errorf("ERR_MISSING_AUTH_TOKEN")
	}
	u, ok := v.(*database.User)
	if !ok || u == nil {
		return nil, fmt.Errorf("ERR_IDENTITY_UNTRACEABLE")
	}
	if u.Status == 2 {
		return nil, fmt.Errorf("ERR_BANNED")
	}
	return u, nil
}

// GetTokens 拉取当前名下所有衍生 API 通道
func GetTokens(c *fiber.Ctx) error {
	user, err := getCurrentUser(c)
	if err != nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message": "鉴权失败", "message_code": err.Error()})
	}

	// fix Major M1（codex 第十五轮）：原 Find 未检 .Error → DB 故障返回空数组让用户以为
	// "没有 token"。fail-closed：错误时 500，前端可重试。
	var tokens []database.AccessToken
	if err := database.DB.Where("user_id = ?", user.ID).Order("id desc").Find(&tokens).Error; err != nil {
		log.Printf("[TOKEN-LIST] failed user=%d: %v", user.ID, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}

	return c.JSON(fiber.Map{"success": true, "data": tokens})
}

type CreateTokenPayload struct {
	Name       string     `json:"name"`
	QuotaLimit float64    `json:"quota_limit"` // 0 for unlimited
	ExpiredAt  *time.Time `json:"expired_at"`  // null for unlimited
}

func CreateToken(c *fiber.Ctx) error {
	user, err := getCurrentUser(c)
	if err != nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message": "鉴权失败", "message_code": err.Error()})
	}

	var req CreateTokenPayload
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "请求参数解析失败", "message_code": "ERR_PARSE_PAYLOAD"})
	}

	if req.Name == "" {
		req.Name = "Unnamed Token"
	}

	// fix Major（codex 第五轮）：拒绝负数/Inf/NaN 限额。
	// 业务约定：0 表示无限制；正数表示具体上限；其他形态都视为非法（防伪装"伪限额"绕过）。
	if !isValidQuotaLimit(req.QuotaLimit) {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "quota_limit 不合法（必须 ≥ 0 且为有限数）", "message_code": "ERR_INVALID_QUOTA_LIMIT"})
	}
	quotaLimitMicro, ok := database.USDToMicro(req.QuotaLimit)
	if !ok {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "quota_limit 数值非法", "message_code": "ERR_INVALID_QUOTA_LIMIT"})
	}
	// fix MEDIUM M19-1（codex 第十九轮）：拒绝过期时间设在过去 → 否则创建即"立即失效"，前端列表里显示
	// 但实际任何请求都被 Auth 路径拒绝；用户体感是"刚建就坏掉"。预留 60s 容差对抗时钟漂移。
	if req.ExpiredAt != nil && req.ExpiredAt.Before(time.Now().Add(-60*time.Second)) {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "过期时间不能早于当前", "message_code": "ERR_EXPIRED_AT_PAST"})
	}

	// fix Minor（codex 第八轮）：原 count-then-create 是 TOCTOU；并发请求都通过 count<50 检查后
	// 全部 INSERT，可绕过 50 个上限。改用事务 + 锁 user 行串行化（与 purchase 同一锁路径）。
	newKey := utils.GenerateRandomToken("sk-daof")
	newToken := database.AccessToken{
		UserID:     user.ID,
		Name:       req.Name,
		Key:        newKey,
		QuotaLimit: quotaLimitMicro,
		ExpiredAt:  req.ExpiredAt,
		Status:     1,
	}
	txErr := database.DB.Transaction(func(tx *gorm.DB) error {
		// 锁定 user 行（跨方言）：PostgreSQL 用 FOR UPDATE，SQLite 用 no-op UPDATE 触发 RESERVED 锁。
		// 复用 lockUserForUpdate（subscription.go），保证两条需要 user 串行化的链路用同一锁路径。
		if err := lockUserForUpdate(tx, user.ID); err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&database.AccessToken{}).Where("user_id = ?", user.ID).Count(&count).Error; err != nil {
			return err
		}
		if count >= 50 {
			return errTokenLimitReached
		}
		return tx.Create(&newToken).Error
	})
	if txErr != nil {
		if errors.Is(txErr, errTokenLimitReached) {
			return c.Status(400).JSON(fiber.Map{"success": false, "message": "该账户关联令牌配额已满", "message_code": "ERR_TOKEN_LIMIT_REACHED"})
		}
		log.Printf("[TOKEN-CREATE] user=%d failed: %v", user.ID, txErr)
		return c.Status(500).JSON(fiber.Map{"success": false, "message": "系统处理内部异常，请稍后重试", "message_code": "ERR_DB_INSERT_FAILED"})
	}

	proxy.SyncCacheConfig()

	LogOperationBy(user.ID, user.ID, "user", "CREATE_TOKEN", c.IP(),
		fmt.Sprintf(`[{"type":"CREATE_TOKEN","token_id":%d,"name":%q,"quota_limit":%g}]`, newToken.ID, req.Name, req.QuotaLimit))

	return c.JSON(fiber.Map{"success": true, "message": "API 凭证创建成功", "message_code": "SUCCESS_TOKEN_CREATED", "data": newToken})
}

type UpdateTokenPayload struct {
	Name        *string    `json:"name"`
	Status      *int       `json:"status"` // 1 启用, 2 禁用
	QuotaLimit  *float64   `json:"quota_limit"`
	ExpiredAt   *time.Time `json:"expired_at"`   // null can technically mean infinite here if passed null in JSON (requires careful pointer handling)
	ClearExpiry bool       `json:"clear_expiry"` // Explicit flag to clear expiration
}

func UpdateTokenSettings(c *fiber.Ctx) error {
	user, err := getCurrentUser(c)
	if err != nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message": "鉴权失败", "message_code": err.Error()})
	}

	id := c.Params("id")
	var req UpdateTokenPayload
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "传入参数格式出现异常", "message_code": "ERR_DATA_EXCEPTION"})
	}

	var token database.AccessToken
	if err := database.DB.Where("id = ? AND user_id = ?", id, user.ID).First(&token).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"success": false, "message": "未能找到对应令牌凭证", "message_code": "ERR_TOKEN_LOST"})
	}

	updates := map[string]interface{}{}
	if req.Name != nil && *req.Name != "" {
		updates["name"] = *req.Name
	}
	if req.Status != nil && (*req.Status == 1 || *req.Status == 2) {
		updates["status"] = *req.Status
	}
	if req.QuotaLimit != nil {
		// fix Major（codex 第五轮）：UPDATE 路径同样拒绝负数/Inf/NaN
		if !isValidQuotaLimit(*req.QuotaLimit) {
			return c.Status(400).JSON(fiber.Map{"success": false, "message": "quota_limit 不合法（必须 ≥ 0 且为有限数）", "message_code": "ERR_INVALID_QUOTA_LIMIT"})
		}
		quotaLimitMicro, ok := database.USDToMicro(*req.QuotaLimit)
		if !ok {
			return c.Status(400).JSON(fiber.Map{"success": false, "message": "quota_limit 数值非法", "message_code": "ERR_INVALID_QUOTA_LIMIT"})
		}
		updates["quota_limit"] = quotaLimitMicro
	}
	if req.ClearExpiry {
		updates["expired_at"] = nil
	} else if req.ExpiredAt != nil {
		// fix MEDIUM M19-1（codex 第十九轮）：与 Create 同一守卫——拒绝把 expired_at 设到过去。
		// 容差 60s 对抗时钟漂移；想"立即失效"应该改 status=2 而不是把过期时间打到过去。
		if req.ExpiredAt.Before(time.Now().Add(-60 * time.Second)) {
			return c.Status(400).JSON(fiber.Map{"success": false, "message": "过期时间不能早于当前", "message_code": "ERR_EXPIRED_AT_PAST"})
		}
		updates["expired_at"] = req.ExpiredAt
	}

	// fix Major M1（codex 第十五轮）：原 Updates 不检 .Error → 失败时仍 SyncCacheConfig + 返回 success
	// 用户以为已禁用 token，实际仍可调用。
	if err := database.DB.Model(&token).Updates(updates).Error; err != nil {
		log.Printf("[TOKEN-UPDATE] user=%d token=%d failed: %v", user.ID, token.ID, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_UPDATE"})
	}
	proxy.SyncCacheConfig()

	LogOperationBy(user.ID, user.ID, "user", "UPDATE_TOKEN", c.IP(),
		fmt.Sprintf(`[{"type":"UPDATE_TOKEN","token_id":%d,"name":%q}]`, token.ID, token.Name))

	return c.JSON(fiber.Map{"success": true, "message": "状态已修改保存", "message_code": "SUCCESS_STATUS_LOCKED"})
}

func DeleteToken(c *fiber.Ctx) error {
	user, err := getCurrentUser(c)
	if err != nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message": "鉴权失败", "message_code": err.Error()})
	}

	id := c.Params("id")
	var token database.AccessToken
	if err := database.DB.Where("id = ? AND user_id = ?", id, user.ID).First(&token).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"success": false, "message": "权限受限（403 Forbidden）", "message_code": "ERR_ENTITY_NOT_OWNED"})
	}

	// 连同历史直接物理剥除
	// fix Major M1（codex 第十五轮）：Unscoped().Delete 失败时若不检 .Error 用户以为废了实际仍可用
	tokenName := token.Name
	tokenID := token.ID
	if err := database.DB.Unscoped().Delete(&token).Error; err != nil {
		log.Printf("[TOKEN-DELETE] user=%d token=%d failed: %v", user.ID, tokenID, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_DELETE"})
	}
	proxy.SyncCacheConfig()

	LogOperationBy(user.ID, user.ID, "user", "DELETE_TOKEN", c.IP(),
		fmt.Sprintf(`[{"type":"DELETE_TOKEN","token_id":%d,"token_name":%q}]`, tokenID, tokenName))

	return c.JSON(fiber.Map{"success": true, "message": "相关令牌已被彻底删除", "message_code": "SUCCESS_TOKEN_BURNED"})
}
