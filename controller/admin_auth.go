package controller

import (
	"fmt"
	"log"
	"time"

	"daof-ai-hub/database"
	"daof-ai-hub/middleware"
	"daof-ai-hub/proxy"
	"daof-ai-hub/utils"

	"github.com/gofiber/fiber/v2"
)

// setAdminCookie 把 admin token 写入 HttpOnly Secure 同站 cookie。
// 前端无法用 JS 读到该 cookie（防 XSS 偷 token），但浏览器会在同站请求里自动携带。
func setAdminCookie(c *fiber.Ctx, token string) {
	c.Cookie(&fiber.Cookie{
		Name:     "daof_admin_token",
		Value:    token,
		Path:     "/",
		HTTPOnly: true,
		Secure:   true,
		SameSite: "Strict",
		Expires:  time.Now().Add(database.UserSessionTTL),
	})
}

// AdminLogout 清除 admin cookie 并写一条登出审计。
func AdminLogout(c *fiber.Ctx) error {
	token := middleware.ExtractAdminToken(c)
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
	if token != "" && database.DB != nil {
		if database.IsSessionID(token) {
			if err := database.RevokeSessionByID(token); err != nil {
				log.Printf("[ADMIN-LOGOUT] revoke session failed token=%s: %v", token, err)
				return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_UPDATE"})
			}
		}
		var admin database.User
		if err := database.DB.Where("token = ? AND role = ?", token, "admin").First(&admin).Error; err == nil {
			newToken := utils.GenerateRandomToken("sk-daof-root")
			if err := database.DB.Model(&admin).Update("token", newToken).Error; err != nil {
				log.Printf("[ADMIN-LOGOUT] rotate token failed admin=%d: %v", admin.ID, err)
				return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_TOKEN_ROTATE_FAILED"})
			}
			proxy.SyncCacheConfig()
			LogOperationBy(admin.ID, admin.ID, "admin", "ADMIN_LOGOUT", c.IP(),
				fmt.Sprintf(`[{"type":"ADMIN_LOGOUT","session":%q}]`, token))
		}
	}
	return c.JSON(fiber.Map{"success": true, "message": "已登出"})
}

func createAdminSession(c *fiber.Ctx, admin *database.User) (string, error) {
	if admin == nil || admin.ID == 0 {
		return "", fmt.Errorf("admin is required")
	}
	if err := database.RevokeSessionsForUser(admin.ID); err != nil {
		return "", err
	}
	sessionID, err := database.CreateUserSession(admin.ID, c.Get("User-Agent"), c.IP())
	if err != nil {
		return "", err
	}
	if err := database.DB.Model(admin).Update("token", sessionID).Error; err != nil {
		_ = database.RevokeSessionByID(sessionID)
		return "", err
	}
	admin.Token = sessionID
	proxy.SyncCacheConfig()
	return sessionID, nil
}

// CheckSys 探测系统是否处于"首次安装态"（默认 root/123456 未改）。
// 始终只回 setup_required，绝不暴露用户名是否命中（防 admin 枚举）。
func CheckSys(c *fiber.Ctx) error {
	var admin database.User
	if err := database.DB.Where("role = ?", "admin").First(&admin).Error; err != nil {
		return c.JSON(fiber.Map{"success": true, "setup_required": true})
	}
	return c.JSON(fiber.Map{
		"success":        true,
		"setup_required": admin.Username == "root" && utils.CheckHash("123456", admin.PasswordHash),
	})
}

type GodLoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func GodLogin(c *fiber.Ctx) error {
	var req GodLoginRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "量子包数据解析异常", "message_code": "ERR_PARSE_PAYLOAD"})
	}
	var admin database.User
	// fix Major（codex 第四轮）：被封禁 admin 不能用密码重新登录获取新 token
	result := database.DB.Where("username = ? AND role = ? AND status = ?", req.Username, "admin", 1).First(&admin)
	if result.Error != nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message": "凭证校验失败", "message_code": "ERR_AUTH_FAILED"})
	}

	if !utils.CheckHash(req.Password, admin.PasswordHash) {
		LogOperationBy(admin.ID, admin.ID, "管理员", "ADMIN_LOGIN_FAIL", c.IP(),
			fmt.Sprintf(`[{"type":"ADMIN_LOGIN_FAIL","username":%q}]`, req.Username))
		return c.Status(401).JSON(fiber.Map{"success": false, "message": "凭证校验失败", "message_code": "ERR_AUTH_FAILED"})
	}
	// admin 浏览器凭证改为可吊销 session；同时旋到 users.token 让现有 AdminGuard 即时识别。
	adminSessionID, err := createAdminSession(c, &admin)
	if err != nil {
		log.Printf("[ADMIN-LOGIN] 创建 session 失败 user=%s: %v", admin.Username, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_INSERT_FAILED"})
	}

	LogOperationBy(admin.ID, admin.ID, "管理员", "ADMIN_LOGIN", c.IP(),
		fmt.Sprintf(`[{"type":"ADMIN_LOGIN","username":%q}]`, admin.Username))

	setAdminCookie(c, adminSessionID)
	// 任何运行在浏览器的脚本（XSS/扩展）都无法读到 token。
	return c.JSON(fiber.Map{
		"success":      true,
		"message":      "神级权限核准通过。",
		"message_code": "SUCCESS_GOD_MODE_LOGIN",
	})
}

type GodSetupRequest struct {
	CurrentUsername string `json:"current_username"`
	OldPassword     string `json:"old_password"`
	NewUsername     string `json:"new_username"`
	NewPassword     string `json:"new_password"`
}

// GodSetup 用于重设管理员凭证。
//
// 安全模型：
//   - 首次安装态（用户名为 "root" 且密码为默认 "123456"）：允许无 OldPassword 直接 setup，
//     用于初始引导。前端 setupMode 会走这条路径。
//   - 已 setup 态：必须提供 OldPassword 并通过校验，否则任何外网/本机调用者都能接管账号。
//     此前的实现没有这层校验，是定时炸弹级漏洞。
func GodSetup(c *fiber.Ctx) error {
	var req GodSetupRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false})
	}

	var admin database.User
	result := database.DB.Where("username = ? AND role = ?", req.CurrentUsername, "admin").First(&admin)
	if result.Error != nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message": "权限异常", "message_code": "ERR_PERMISSION_DENIED"})
	}

	// 仅在"首次安装态"才允许免旧密码 setup。已配置过的实例必须验证旧密码。
	isInitialSetup := admin.Username == "root" && utils.CheckHash("123456", admin.PasswordHash)
	if !isInitialSetup {
		if !utils.CheckHash(req.OldPassword, admin.PasswordHash) {
			return c.Status(401).JSON(fiber.Map{"success": false, "message": "旧凭证校验失败", "message_code": "ERR_OLD_PASSWORD_INVALID"})
		}
	}

	// 强制要求修改，且不能再使用 root 或者空密码
	if req.NewUsername == "root" || req.NewUsername == "" || req.NewPassword == "" {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "新指令集存在违规漏洞", "message_code": "ERR_INVALID_PAYLOAD_VULN"})
	}

	oldUsername := admin.Username
	admin.Username = req.NewUsername
	admin.PasswordHash = utils.GenerateHash(req.NewPassword)
	if err := database.DB.Save(&admin).Error; err != nil {
		log.Printf("[ADMIN-SETUP] 保存失败: %v", err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_UPDATE"})
	}
	adminSessionID, err := createAdminSession(c, &admin)
	if err != nil {
		log.Printf("[ADMIN-SETUP] 创建 session 失败 admin=%d: %v", admin.ID, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_INSERT_FAILED"})
	}
	middleware.InvalidateSetupGuardCache() // root 密码已改，让 SetupGuard 立即重评估

	LogOperationBy(admin.ID, admin.ID, "管理员", "ADMIN_SETUP", c.IP(),
		fmt.Sprintf(`[{"type":"ADMIN_SETUP","old_username":%q,"new_username":%q,"initial_setup":%t}]`, oldUsername, req.NewUsername, isInitialSetup))

	setAdminCookie(c, adminSessionID)

	return c.JSON(fiber.Map{
		"success":      true,
		"message":      "协议刷新成功，全站解除锁定。",
		"message_code": "SUCCESS_SYSTEM_UNLOCKED",
	})
}

type AdminCredentialsPayload struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func UpdateAdminCredentials(c *fiber.Ctx) error {
	var req AdminCredentialsPayload
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "量子包数据解析异常", "message_code": "ERR_PARSE_PAYLOAD"})
	}

	if req.Username == "" || req.Password == "" {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "名称和密保不可为空！", "message_code": "ERR_EMPTY_CREDENTIALS"})
	}

	if req.Username == "root" {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "不能再使用原始代号 'root'，请建立独特的指挥官代号。", "message_code": "ERR_ROOT_ALIAS_FORBIDDEN"})
	}

	// 提取当前操作者 token（统一 helper）
	token := middleware.ExtractAdminToken(c)
	if token == "" {
		return c.Status(401).JSON(fiber.Map{"success": false, "message": "令牌残损", "message_code": "ERR_TOKEN_CORRUPTED"})
	}

	var admin database.User
	// fix Minor Mi22-5（codex 第二十二轮）：handler 自身要求 status=1，
	// 不依赖 AdminGuard 兜底（防 direct mount / 测试 helper 误放行封禁 admin）。
	if err := database.DB.Where("token = ? AND role = ? AND status = ?", token, "admin", 1).First(&admin).Error; err != nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message": "无可查证的高阶身份", "message_code": "ERR_NO_HIGH_LEVEL_IDENTITY"})
	}

	oldUsername := admin.Username
	newToken := utils.GenerateRandomToken("sk-daof-root")
	if err := database.DB.Model(&admin).Updates(map[string]interface{}{
		"username":      req.Username,
		"password_hash": utils.GenerateHash(req.Password),
		"token":         newToken,
	}).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "message": "底层权限库覆写失败，可能与其他神名发生冲突。", "message_code": "ERR_OVERRIDE_DB_FAILED"})
	}
	if err := database.RevokeSessionsForUser(admin.ID); err != nil {
		log.Printf("[ADMIN-CREDENTIALS] revoke sessions failed admin=%d: %v", admin.ID, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_UPDATE"})
	}
	proxy.SyncCacheConfig()

	LogOperationBy(admin.ID, admin.ID, "管理员", "ADMIN_CREDENTIALS_UPDATE", c.IP(),
		fmt.Sprintf(`[{"type":"ADMIN_CREDENTIALS_UPDATE","old_username":%q,"new_username":%q}]`, oldUsername, req.Username))

	return c.JSON(fiber.Map{
		"success":      true,
		"message":      "全息管理档案重构成功！注意：因为名称变动，您现在将会被注销，请使用新代号重新通过安全闸门进行认证！",
		"message_code": "SUCCESS_ADMIN_RECONFIGURED",
	})
}
