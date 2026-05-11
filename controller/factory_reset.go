package controller

import (
	"log"
	"time"

	"daof-ai-hub/database"
	"daof-ai-hub/middleware"
	"daof-ai-hub/proxy"
	"daof-ai-hub/utils"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// FactoryResetPayload 必须显式带固定确认字符串和当前 admin 密码，避免误调和 session 劫持。
type FactoryResetPayload struct {
	Confirm  string `json:"confirm"`
	Password string `json:"password"` // 操作者必须重新输入当前 admin 密码
}

// FactoryReset 把整个平台抹回初始安装态：
//   - 清空所有 User / AccessToken / ApiLog / OperationLog / Channel / ChannelModel / SysConfig
//   - 重新插入默认 admin (root / 123456)
//   - 清除当前 admin HttpOnly cookie，强制下次访问从 setup 入口重新引导
//
// 安全要求：调用方必须在 body 里传 confirm="FACTORY_RESET" 才执行。
// 即使如此，admin 误点照样有可能 — 前端必须再做一次手输确认。
//
// 使用场景：内部测试/演示环境清空、首次正式部署前抹掉历史脏数据、紧急安全事件后全平台重置凭证。
func FactoryReset(c *fiber.Ctx) error {
	var req FactoryResetPayload
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "请求体解析失败", "message_code": "ERR_PARSE_PAYLOAD"})
	}
	if req.Confirm != "FACTORY_RESET" {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "确认字符串不匹配，操作中止", "message_code": "ERR_RESET_CONFIRM_MISMATCH"})
	}

	// 二次鉴权：从 cookie/header 取当前 admin token，验证操作者身份 + 重新校验密码。
	token := middleware.ExtractAdminToken(c)
	var operator database.User
	if err := database.DB.Where("token = ? AND role = ?", token, "admin").First(&operator).Error; err != nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message": "无法识别操作者身份", "message_code": "ERR_UNAUTHORIZED"})
	}
	if !utils.CheckHash(req.Password, operator.PasswordHash) {
		return c.Status(401).JSON(fiber.Map{"success": false, "message": "管理员密码错误，出厂重置已中止", "message_code": "ERR_RESET_PASSWORD_INVALID"})
	}

	// 注册临界区互斥：避免 cap_check 期间有用户正在 CompleteProfile，导致清表后还插一条孤儿 user。
	registerMu.Lock()
	defer registerMu.Unlock()

	// 写一条系统级审计到 stdout（OperationLog 表会被自己清掉，无法依赖）
	log.Printf("[FACTORY-RESET] triggered by admin id=%d username=%s ip=%s @ %s",
		operator.ID, operator.Username, c.IP(), time.Now().Format(time.RFC3339))

	// 用事务包裹：任何一步失败全回滚，避免半个表清了半个表没清的不一致状态。
	// 删除顺序：先清子表（含外键引用），再清主表，避免约束冲突。
	err := database.DB.Transaction(func(tx *gorm.DB) error {
		// fix Major（codex 第四轮）：清表清单必须与 database/sqlite.go::AutoMigrate 同步，
		// 漏表会导致出厂重置后留有 PII / 业务数据残留（充值流水、工单内容、广播目标、CPA 元数据等）。
		tables := []any{
			// 订阅系统（依赖 User / Package / QuotaPlan）
			&database.SubscriptionUsage{},
			&database.UserSubscription{},
			&database.PackagePlan{},
			&database.Package{},
			&database.QuotaPlan{},
			// 通知系统（站内信 + 广播 + 用户偏好）
			&database.Notification{},
			&database.NotificationBroadcastTarget{},
			&database.NotificationBroadcast{},
			&database.NotificationPreference{},
			// 工单系统（用户↔admin 对话）
			&database.TicketMessage{},
			&database.Ticket{},
			// 充值订单（易付通对接）
			&database.TopupOrder{},
			// CPA 凭证元数据缓存（GCP project_id / refresh token 等）
			&database.CPACredential{},
			// 审计 / 日志
			&database.ApiLog{},
			&database.OperationLog{},
			// 用户子表
			&database.AccessToken{},
			// 渠道
			&database.ChannelModel{},
			&database.Channel{},
			// 主表 + 配置
			&database.User{},
			&database.SysConfig{},
		}
		for _, m := range tables {
			// GORM 默认禁止无 WHERE 的 Delete，用 "1=1" 显式表达"全表"
			if err := tx.Unscoped().Where("1=1").Delete(m).Error; err != nil {
				return err
			}
		}

		// 重建默认 root admin（与 sqlite.go InitDB seed 完全一致）
		seed := database.User{
			Username:     "root",
			PasswordHash: utils.GenerateHash("123456"),
			Role:         "admin",
			Token:        utils.GenerateRandomToken("sk-daof-root"),
			Quota:        99999.0,
			Status:       1,
		}
		return tx.Create(&seed).Error
	})

	if err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "message": "出厂重置过程中发生错误，已回滚", "message_code": "ERR_FACTORY_RESET_FAILED"})
	}

	// 清当前 admin cookie（之前的 admin token 已在 user 表里被清，cookie 保留也是无效，但显式清更干净）
	c.Cookie(&fiber.Cookie{
		Name:     "daof_admin_token",
		Value:    "",
		Path:     "/",
		HTTPOnly: true,
		Secure:   true,
		SameSite: "Strict",
		Expires:  time.Now().Add(-1 * time.Hour),
		MaxAge:   -1,
	})

	// 同步所有缓存：让流量代理立刻拒绝旧 token；让 SetupGuard 立即重新评估（root 默认密码=true）
	proxy.SyncCacheConfig()
	proxy.FlushAllSubscriptionCache()
	middleware.InvalidateSetupGuardCache()

	return c.JSON(fiber.Map{
		"success":      true,
		"message":      "平台已恢复出厂设置：所有用户、渠道、令牌、配置、日志已清空。请使用 ?sys=root 入口（默认密码 123456）重新引导。",
		"message_code": "SUCCESS_FACTORY_RESET",
	})
}
