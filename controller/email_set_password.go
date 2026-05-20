// Package controller / email_set_password.go
//
// OAuth 用户启用 email-login 流程 = "首次设置密码"。Phase G-2.5（2026-05-20）。
//
// 这与 G-2.4 reset-password 的差别：
//   - reset-password：用户已有 PasswordHash，因忘记而重置
//   - set-password：用户从未设过密码（OAuth-only），现在想加一种 email-login 通道
//
// 路由：
//   - POST /api/user/email/request-set-password  挂 UserGuard + CSRFGuard + 邮件限流
//                                                 logged-in OAuth 用户申请发邮件
//   - POST /api/auth/email/set-password           不挂 UserGuard，凭 token 完成设置
//
// 安全设计：
//   - 请求端：必须 logged-in；user.Email 已 verified；PasswordHash 必须为空
//     （已设密码的用户走 forgot-password 流程，不在这里）
//   - token 消费端：Purpose=set_password 严格 filter；race-check 确保 PasswordHash 仍为空
//     （并发情况：用户在 set-password tx 提交前手动从另一渠道设过密码）
//   - 设置成功后自动 EmailLoginEnabled=true —— 用户明确请求启用 email-login，
//     无需再让他登录后去 settings 里 opt-in（避免与 G-2.3 死锁等价问题）
package controller

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"daof-cpa/database"
	"daof-cpa/proxy"
	"daof-cpa/utils"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

type setPasswordRequest struct {
	Token       string `json:"token"`
	NewPassword string `json:"new_password"`
}

// RequestSetPassword POST /api/user/email/request-set-password
//
// OAuth 用户首次申请设置密码以启用 email-login。前提：
//   - master email_enabled
//   - user.Email != "" 且 EmailVerifiedAt != nil
//   - user.PasswordHash == ""（已设密码的用户应走 forgot-password 流程）
func RequestSetPassword(c *fiber.Ctx) error {
	if !proxy.IsEmailEnabled() {
		return c.Status(503).JSON(fiber.Map{"success": false, "message_code": "ERR_EMAIL_FEATURE_DISABLED"})
	}
	user, err := getCurrentUser(c)
	if err != nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}
	if user.Email == "" || user.EmailVerifiedAt == nil {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_EMAIL_NOT_VERIFIED",
			"message":      "请先绑定并验证邮箱后再申请设置密码",
		})
	}
	if user.PasswordHash != "" {
		// 已有密码：拒绝（用户应走 forgot-password 而非 set-password）
		return c.Status(409).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_PASSWORD_ALREADY_SET",
			"message":      "您已设置密码。如要修改请使用'忘记密码'功能。",
		})
	}

	clientIP := c.IP()
	// 原子 check + 占用版（fix HIGH H-3）
	if err := proxy.CheckAndConsumeEmailRateLimit(user.Email, clientIP); err != nil {
		return c.Status(429).JSON(fiber.Map{"success": false, "message_code": "ERR_EMAIL_RATE_LIMIT"})
	}

	rawToken, tokenHash, err := generateEmailToken()
	if err != nil {
		log.Printf("[SET-PWD-REQ] token gen failed user=%d: %v", user.ID, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_INTERNAL"})
	}
	// 与 reset 用同样的 TTL（短，敏感）—— 不引入独立 SysConfig 简化运维
	ttl := loadEmailResetTTL()
	now := time.Now()
	verification := database.EmailVerification{
		UserID:    user.ID,
		Email:     user.Email,
		TokenHash: tokenHash,
		Purpose:   database.EmailVerificationPurposeSetPassword,
		ExpiresAt: now.Add(ttl),
		ClientIP:  clientIP,
		UserAgent: truncateUserAgent(c.Get("User-Agent")),
		CreatedAt: now,
	}
	txErr := database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&database.EmailVerification{}).
			Where("user_id = ? AND purpose = ? AND consumed_at IS NULL AND expires_at > ?",
				user.ID, database.EmailVerificationPurposeSetPassword, now).
			Update("consumed_at", now).Error; err != nil {
			return fmt.Errorf("invalidate prior set-password tokens: %w", err)
		}
		if err := tx.Create(&verification).Error; err != nil {
			return fmt.Errorf("insert set-password verification: %w", err)
		}
		return nil
	})
	if txErr != nil {
		log.Printf("[SET-PWD-REQ] tx failed user=%d: %v", user.ID, txErr)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_TRANSACTION"})
	}

	// set 与 reset 用不同 URL 路径让前端分别处理两个 endpoint；token 也独立 purpose 防混用
	setURL, err := buildPasswordSetURL(rawToken)
	if err != nil {
		log.Printf("[SET-PWD-REQ] build URL failed: %v", err)
		return c.Status(503).JSON(fiber.Map{"success": false, "message_code": "ERR_SERVER_ADDRESS_NOT_CONFIGURED"})
	}
	locale := emailLocaleFromCtx(c)
	msg, err := proxy.RenderEmail(proxy.EmailTplSetPassword, locale, proxy.EmailVars{
		UserName:  user.Username,
		UserEmail: user.Email,
		ResetURL:  setURL,
		ExpiresIn: ttlDisplay(ttl, locale),
		AppName:   proxy.AppNameFromConfig(),
	})
	if err != nil {
		log.Printf("[SET-PWD-REQ] render failed user=%d: %v", user.ID, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_EMAIL_SEND_FAIL"})
	}
	dedupKey := fmt.Sprintf("set-password:%d:%s", user.ID, user.Email)
	if err := proxy.SendEmailDeduped(proxy.EmailTask{
		To:       user.Email,
		Message:  msg,
		DedupKey: dedupKey,
		Label:    "set_password",
	}); err != nil && !errors.Is(err, proxy.ErrEmailDedup) {
		log.Printf("[SET-PWD-REQ] enqueue failed user=%d: %v", user.ID, err)
		return c.Status(503).JSON(fiber.Map{"success": false, "message_code": "ERR_EMAIL_SEND_FAIL"})
	}
	// rate-limit 已在入口 CheckAndConsume 时原子占用，无需再 Register
	LogOperationBy(0, user.ID, "user", "SET_PASSWORD_REQUEST", clientIP,
		fmt.Sprintf(`[{"type":"SET_PASSWORD_REQUEST","email":%q}]`, maskEmailForAdmin(user.Email)))
	return c.JSON(fiber.Map{"success": true, "message_code": "SUCCESS_SET_PASSWORD_EMAIL_SENT"})
}

// SetPassword POST /api/auth/email/set-password
//
// 凭 set_password token + 新密码完成"首次设置密码"。
//
//   - 校验 token：Purpose=set_password，未消费 / 未过期
//   - 校验密码强度
//   - race-check：tx 内 lock user 再次确认 PasswordHash 仍为空
//     （并发情况：用户从其他渠道已设过密码 → 拒绝，让用户走 forgot-password 流程）
//   - 事务内：消费 token + 设 PasswordHash + EmailLoginEnabled=true
//     （用户明确请求 enable email-login，自动 opt-in 避免 G-2.3 死锁）
func SetPassword(c *fiber.Ctx) error {
	if !proxy.IsEmailEnabled() {
		return c.Status(503).JSON(fiber.Map{"success": false, "message_code": "ERR_EMAIL_FEATURE_DISABLED"})
	}
	var req setPasswordRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_BAD_REQUEST"})
	}
	token := strings.TrimSpace(req.Token)
	if token == "" {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_EMAIL_TOKEN_INVALID"})
	}
	hash := hashEmailToken(token)

	var verification database.EmailVerification
	if err := database.DB.Where("token_hash = ? AND purpose = ?",
		hash, database.EmailVerificationPurposeSetPassword).First(&verification).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_EMAIL_TOKEN_INVALID"})
		}
		log.Printf("[SET-PWD] DB query failed: %v", err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}
	now := time.Now()
	if verification.IsConsumed() {
		return c.Status(409).JSON(fiber.Map{"success": false, "message_code": "ERR_EMAIL_TOKEN_CONSUMED"})
	}
	if verification.IsExpired(now) {
		return c.Status(410).JSON(fiber.Map{"success": false, "message_code": "ERR_EMAIL_TOKEN_EXPIRED"})
	}

	var user database.User
	if err := database.DB.Where("id = ?", verification.UserID).First(&user).Error; err != nil {
		log.Printf("[SET-PWD] user not found uid=%d: %v", verification.UserID, err)
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_EMAIL_TOKEN_INVALID"})
	}
	if code, ok := validatePasswordStrength(req.NewPassword, user.Username); !ok {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": code})
	}

	newHash := utils.GenerateHash(req.NewPassword)
	if newHash == "" {
		log.Printf("[SET-PWD] bcrypt hash empty user=%d", user.ID)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_INTERNAL"})
	}

	// race-check + tx：set-password 仅当 PasswordHash 仍为空时执行
	txErr := database.DB.Transaction(func(tx *gorm.DB) error {
		// 消费 token
		res := tx.Model(&database.EmailVerification{}).
			Where("id = ? AND consumed_at IS NULL", verification.ID).
			Update("consumed_at", now)
		if res.Error != nil {
			return fmt.Errorf("consume token: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return errEmailTokenRacedInTx
		}
		// race-check：tx 内重新读 user，确认 PasswordHash 仍为空
		var fresh database.User
		if err := tx.Where("id = ?", user.ID).First(&fresh).Error; err != nil {
			return fmt.Errorf("reload user: %w", err)
		}
		if fresh.PasswordHash != "" {
			return errPasswordAlreadySetInTx
		}
		if err := tx.Model(&database.User{}).
			Where("id = ?", user.ID).
			Updates(map[string]any{
				"password_hash":       newHash,
				"email_login_enabled": true,
			}).Error; err != nil {
			return fmt.Errorf("update password: %w", err)
		}
		return nil
	})
	if txErr != nil {
		if errors.Is(txErr, errEmailTokenRacedInTx) {
			return c.Status(409).JSON(fiber.Map{"success": false, "message_code": "ERR_EMAIL_TOKEN_CONSUMED"})
		}
		if errors.Is(txErr, errPasswordAlreadySetInTx) {
			return c.Status(409).JSON(fiber.Map{
				"success":      false,
				"message_code": "ERR_PASSWORD_ALREADY_SET",
				"message":      "密码已设置过。如要修改请使用'忘记密码'功能。",
			})
		}
		log.Printf("[SET-PWD] tx failed user=%d: %v", user.ID, txErr)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_TRANSACTION"})
	}

	// 安全：密码已设 → 作废所有 browser session。
	// 普通 OAuth 用户的 session 不一定基于 PasswordHash，但既然密码已成为登录凭据，
	// 同样要求重新认证（防 stolen-session 持续有效）。
	if err := database.RevokeSessionsForUser(user.ID); err != nil {
		log.Printf("[SET-PWD] revoke sessions failed user=%d: %v", user.ID, err)
	}
	proxy.RefreshUserAuth(user.ID)
	LogOperationBy(0, user.ID, "user", "SET_PASSWORD_DONE", c.IP(),
		fmt.Sprintf(`[{"type":"SET_PASSWORD_DONE","email":%q}]`, maskEmailForAdmin(user.Email)))
	return c.JSON(fiber.Map{"success": true, "message_code": "SUCCESS_PASSWORD_SET"})
}

// errPasswordAlreadySetInTx 是 SetPassword tx 内的并发竞态 sentinel：
// 用户在 token 生成与消费之间，从其他渠道已设过密码，本次 SetPassword 必须拒绝。
var errPasswordAlreadySetInTx = errors.New("password already set by concurrent path")

// buildPasswordSetURL 拼装前端"首次设置密码"页面 URL。
// 共用 buildFrontendTokenURL，仅差路径 config key。
func buildPasswordSetURL(rawToken string) (string, error) {
	return buildFrontendTokenURL("email_set_url_path", "/set-password", rawToken)
}
