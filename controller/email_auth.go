// Package controller / email_auth.go
//
// 邮箱+密码认证的共享 helper + 用户 EmailLoginEnabled toggle。Phase G-2.1（2026-05-20）。
//
// 这里只放共享 helper：
//   - validatePasswordStrength：密码强度校验（注册/重置/改密都用）
//   - PutMyEmailLoginEnabled：用户级开关，控制是否允许邮箱登录（在 admin master 之外的第二道闸）
//
// 邮箱+密码 登录/注册/重置 各自的 handler 在 G-2.2~G-2.5 子项里分别实现。
//
// 安全约定：
//   - 密码长度下限 8（NIST SP 800-63B 最低）/ 上限 72（bcrypt 限制）
//   - 不做"必须含大小写+数字"的复杂度强制（NIST 反对，鼓励长 passphrase）
//   - 但禁止全空白 / 全相同字符 / 与 username 完全一致（最常见的弱密码模式）
//   - 切换 EmailLoginEnabled 前要求当前密码：防 session hijack / CSRF 攻击者一键开启登录
package controller

import (
	"log"
	"strings"
	"unicode"
	"unicode/utf8"

	"daof-cpa/database"
	"daof-cpa/proxy"
	"daof-cpa/utils"

	"github.com/gofiber/fiber/v2"
)

// passwordMinLength / passwordMaxLength 限制。bcrypt 算法本身上限 72 字节。
// 我们用 rune 计数避免中文密码被字节截断。
const (
	passwordMinLength = 8
	passwordMaxLength = 72
)

// validatePasswordStrength 校验密码是否满足最低强度要求。
//
// 通过：返回 ("", true)
// 不通过：返回 (message_code, false)；调用方据此返回 400 + i18n。
//
// 注意：不做"必须含大小写+数字+符号"强制 —— NIST SP 800-63B 已废弃此类规则，
// 鼓励长 passphrase。但仍然拦截最常见的脆弱模式：全空白、全相同字符、与用户名重复。
func validatePasswordStrength(password, username string) (string, bool) {
	if password == "" {
		return "ERR_PASSWORD_EMPTY", false
	}
	// rune 计数：中文 6 个字符也算 6（避免按字节算出 18 误超 72）
	runeLen := utf8.RuneCountInString(password)
	if runeLen < passwordMinLength {
		return "ERR_PASSWORD_TOO_SHORT", false
	}
	// bcrypt 限制 72 字节。中文密码字节数 = rune × 3，留余地用 rune × 3 估算
	if runeLen > passwordMaxLength || len(password) > 72 {
		return "ERR_PASSWORD_TOO_LONG", false
	}
	// 全空白：明显输入错误（即使长度够也拒）
	if strings.TrimSpace(password) == "" {
		return "ERR_PASSWORD_WHITESPACE", false
	}
	// 全相同字符：'aaaaaaaa' / '11111111' / '        '
	if isAllSameRune(password) {
		return "ERR_PASSWORD_WEAK", false
	}
	// 与 username 完全相同（大小写敏感比较已经足够阻止显式攻击；
	// case-insensitive 比较过于严格会误伤）
	if username != "" && password == username {
		return "ERR_PASSWORD_SAME_AS_USERNAME", false
	}
	// 控制字符（NUL/Tab/CR/LF）：通常是输入错误
	for _, r := range password {
		if unicode.IsControl(r) {
			return "ERR_PASSWORD_CTRL_CHAR", false
		}
	}
	return "", true
}

// isAllSameRune 检查字符串是否全部是同一字符。
// 例：'aaaa', '1111', '    ' 都返回 true；'a1a1' 返回 false。
func isAllSameRune(s string) bool {
	if s == "" {
		return false
	}
	first, _ := utf8.DecodeRuneInString(s)
	for _, r := range s {
		if r != first {
			return false
		}
	}
	return true
}

// emailLoginToggleRequest PUT /api/user/email-login-enabled 请求体。
type emailLoginToggleRequest struct {
	Enabled         bool   `json:"enabled"`
	CurrentPassword string `json:"current_password"` // 必填：防 session hijack / CSRF 攻击者一键开关
}

// PutMyEmailLoginEnabled PUT /api/user/email-login-enabled
//
// 用户切换 EmailLoginEnabled（开/关邮箱登录方式）。前提：
//  1. 用户已绑定邮箱且已验证（User.Email + EmailVerifiedAt 非 nil）
//  2. 用户已设密码（User.PasswordHash 非空；OAuth 用户需先走 G-2.5 set-password）
//  3. 提交当前密码并校验通过
//
// 注意：admin email_login_enabled SysConfig 是另一个 gate，由后端登录端点检查；
// 用户在自己设置里开启了 EmailLoginEnabled 但 admin 关了 master，登录仍会失败。
// 设置这里仍允许 opt-in（提示 "管理员未启用 master 时不会生效"），不强制阻止。
func PutMyEmailLoginEnabled(c *fiber.Ctx) error {
	user, err := getCurrentUser(c)
	if err != nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}
	var req emailLoginToggleRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_BAD_REQUEST"})
	}

	// 必须先绑定 + 验证邮箱
	if user.Email == "" || user.EmailVerifiedAt == nil {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_EMAIL_NOT_VERIFIED",
			"message":      "请先在账号设置里绑定并验证邮箱后再启用邮箱登录",
		})
	}
	// 必须先设密码（OAuth 用户需走 set-password 流程）
	if user.PasswordHash == "" {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_PASSWORD_NOT_SET",
			"message":      "您还未设置密码。请先在账号设置里设置密码后再启用邮箱登录",
		})
	}

	// 校验当前密码：开启或关闭都要求 —— 防 session hijack 一键改设置
	if req.CurrentPassword == "" {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_CURRENT_PASSWORD_REQUIRED",
			"message":      "请输入当前密码以确认操作",
		})
	}
	if !utils.CheckHash(req.CurrentPassword, user.PasswordHash) {
		return c.Status(401).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_PASSWORD_INCORRECT",
			"message":      "密码错误",
		})
	}

	// 已经是目标状态：no-op
	if user.EmailLoginEnabled == req.Enabled {
		return c.JSON(fiber.Map{"success": true, "message_code": "SUCCESS_NO_CHANGE"})
	}

	if err := database.DB.Model(&database.User{}).
		Where("id = ?", user.ID).
		Update("email_login_enabled", req.Enabled).Error; err != nil {
		log.Printf("[EMAIL-LOGIN-TOGGLE] update user=%d failed: %v", user.ID, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_UPDATE"})
	}
	proxy.RefreshUserAuth(user.ID)
	return c.JSON(fiber.Map{
		"success":      true,
		"message_code": "SUCCESS_EMAIL_LOGIN_TOGGLED",
		"enabled":      req.Enabled,
	})
}

// requireEmailFeatureEnabled 检查 master + 指定子开关都打开。
// childKey 例如 "email_login_enabled" / "email_signup_enabled"；空字符串只检查 master。
// 返回 (childOK, masterOK)；任一为 false 调用方应回 503。
func requireEmailFeatureEnabled(childKey string) (childOK, masterOK bool) {
	masterOK = proxy.IsEmailEnabled()
	if !masterOK {
		return false, false
	}
	if childKey == "" {
		return true, true
	}
	return readBoolConfig(childKey, false), true
}
