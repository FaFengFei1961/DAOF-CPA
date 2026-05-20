// Package controller / email_password_reset.go
//
// 忘记密码 / 重置密码 endpoint。Phase G-2.4（2026-05-20）。
//
// 路由（均不需要 UserGuard；各挂自己的 IP 限流）：
//   - POST /api/auth/email/forgot-password   申请重置（user 提交 email；服务端发 reset 邮件）
//   - POST /api/auth/email/reset-password    凭 token + 新密码完成重置
//
// Gate：master email_enabled。这里**不**额外 gate email_login_enabled —— 用户可能在
// admin 关闭 login 期间预先重置密码以便 admin 开启后立刻可用。实际登录处仍会双重 gate。
//
// 安全设计 — **邮箱枚举防御**：
//   - forgot-password 永远返回 200 + 同一 message_code，无论邮箱是否存在 / 是否 verified /
//     是否设过密码。攻击者不能用此端点判断邮箱是否注册。
//   - 内部条件分支：
//       (a) 邮箱存在 + EmailVerifiedAt 非 nil + PasswordHash 非空 → 真正发邮件
//       (b) 邮箱存在但未验证 / OAuth-only 无密码 / 不存在 → 跳过发邮件，仍返回同一响应
//   - 用 dedup key 避免短时间重复点击产生多封邮件
//
// 安全设计 — **token 强度 + 一次性**：
//   - 32 字节 crypto/rand → base64url（43 字符），DB 仅存 SHA-256 hex
//   - 短 TTL：默认 15min（SysConfig email_reset_ttl_seconds，比 verify 的 1h 更敏感）
//   - 一次性消费：成功 ResetPassword 后立即 SET ConsumedAt
//   - 发新 token 前作废所有 prior reset_password pending token（防 token 泄漏后并存多份）
//   - lookup 时严格 filter Purpose='reset_password'，verify token 不能跨用
//
// 安全设计 — **限流**：
//   - forgot-password: 5/hour/IP + 3/hour/email（防滥发邮件骚扰）
//   - reset-password: 10/hour/IP（保险 — token 本身已不可暴破）
package controller

import (
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"daof-cpa/database"
	"daof-cpa/proxy"
	"daof-cpa/utils"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

type forgotPasswordRequest struct {
	Email string `json:"email"`
}

type resetPasswordRequest struct {
	Token       string `json:"token"`
	NewPassword string `json:"new_password"`
}

// ForgotPassword POST /api/auth/email/forgot-password
//
// 用户输入邮箱，服务端发重置链接邮件。
// **永远返回相同的 SUCCESS_PASSWORD_RESET_EMAIL_SENT** —— 不泄漏邮箱存在性。
func ForgotPassword(c *fiber.Ctx) error {
	// Gate：master only
	if !proxy.IsEmailEnabled() {
		return c.Status(503).JSON(fiber.Map{"success": false, "message_code": "ERR_EMAIL_FEATURE_DISABLED"})
	}

	var req forgotPasswordRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_BAD_REQUEST"})
	}
	email, ok := normalizeEmail(req.Email)
	if !ok {
		// 格式都错就直接报错——不算枚举（任何合法邮箱用户都能通过格式校验）
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_EMAIL_INVALID_FORMAT"})
	}

	clientIP := c.IP()
	// 邮件级限流（额外一道防滥发）
	if err := proxy.CheckEmailRateLimit(email, clientIP); err != nil {
		// 注意：这里仍走"枚举防御"返回路径——攻击者从响应看不出邮箱是否存在。
		// 但我们仍消费了限流配额，下次同 IP/邮箱会更快碰到顶。
		log.Printf("[FORGOT-PWD] rate-limited email=%s ip=%s: %v", maskEmailForAdmin(email), clientIP, err)
		return successPasswordResetEmailSent(c)
	}

	// 查用户。lookup by lower(email)。复用 email_login 的策略：用户不存在/未验证/无密码
	// 都不真正发邮件，但响应保持一致。
	var user database.User
	lookupErr := database.DB.Where("email = ?", email).First(&user).Error
	switch {
	case lookupErr == nil:
		// 用户存在；继续判断是否真正发邮件
	case errors.Is(lookupErr, gorm.ErrRecordNotFound):
		log.Printf("[FORGOT-PWD] user not found email=%s — silently no-op", maskEmailForAdmin(email))
		return successPasswordResetEmailSent(c)
	default:
		log.Printf("[FORGOT-PWD] DB lookup failed: %v", lookupErr)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}

	// 必须已验证邮箱（否则攻击者可往任意邮箱发垃圾）
	if user.EmailVerifiedAt == nil {
		log.Printf("[FORGOT-PWD] email not verified user=%d — silently no-op", user.ID)
		return successPasswordResetEmailSent(c)
	}
	// 必须已设密码（OAuth-only 用户走 G-2.5 set-password 流程，不在这里）
	if user.PasswordHash == "" {
		log.Printf("[FORGOT-PWD] user has no password (OAuth-only) user=%d — silently no-op", user.ID)
		return successPasswordResetEmailSent(c)
	}

	// 真正发邮件
	rawToken, tokenHash, err := generateEmailToken()
	if err != nil {
		log.Printf("[FORGOT-PWD] token gen failed user=%d: %v", user.ID, err)
		// 内部错误仍走 generic 响应——避免泄漏哪些用户能触发
		return successPasswordResetEmailSent(c)
	}

	ttl := loadEmailResetTTL()
	now := time.Now()
	verification := database.EmailVerification{
		UserID:    user.ID,
		Email:     user.Email,
		TokenHash: tokenHash,
		Purpose:   database.EmailVerificationPurposeResetPassword,
		ExpiresAt: now.Add(ttl),
		ClientIP:  clientIP,
		UserAgent: truncateUserAgent(c.Get("User-Agent")),
		CreatedAt: now,
	}
	txErr := database.DB.Transaction(func(tx *gorm.DB) error {
		// 作废 prior 未消费 reset token（同 BindEmail/ResendVerification 模式）
		if err := tx.Model(&database.EmailVerification{}).
			Where("user_id = ? AND purpose = ? AND consumed_at IS NULL AND expires_at > ?",
				user.ID, database.EmailVerificationPurposeResetPassword, now).
			Update("consumed_at", now).Error; err != nil {
			return fmt.Errorf("invalidate prior reset tokens: %w", err)
		}
		if err := tx.Create(&verification).Error; err != nil {
			return fmt.Errorf("insert reset verification: %w", err)
		}
		return nil
	})
	if txErr != nil {
		log.Printf("[FORGOT-PWD] tx failed user=%d: %v", user.ID, txErr)
		return successPasswordResetEmailSent(c)
	}

	resetURL, err := buildPasswordResetURL(rawToken)
	if err != nil {
		log.Printf("[FORGOT-PWD] build URL failed: %v", err)
		return successPasswordResetEmailSent(c)
	}

	locale := emailLocaleFromCtx(c)
	msg, err := proxy.RenderEmail(proxy.EmailTplResetPassword, locale, proxy.EmailVars{
		UserName:  user.Username,
		UserEmail: user.Email,
		ResetURL:  resetURL,
		ExpiresIn: ttlDisplay(ttl, locale),
		AppName:   proxy.AppNameFromConfig(),
	})
	if err != nil {
		log.Printf("[FORGOT-PWD] render failed user=%d: %v", user.ID, err)
		return successPasswordResetEmailSent(c)
	}
	dedupKey := fmt.Sprintf("reset:%d:%s", user.ID, user.Email)
	if err := proxy.SendEmailDeduped(proxy.EmailTask{
		To:       user.Email,
		Message:  msg,
		DedupKey: dedupKey,
		Label:    "password_reset",
	}); err != nil && !errors.Is(err, proxy.ErrEmailDedup) {
		log.Printf("[FORGOT-PWD] enqueue failed user=%d: %v", user.ID, err)
		// 仍走 generic 响应
		return successPasswordResetEmailSent(c)
	}
	proxy.RegisterEmailSent(user.Email, clientIP)
	LogOperationBy(0, user.ID, "user", "PASSWORD_RESET_REQUEST", clientIP,
		fmt.Sprintf(`[{"type":"PASSWORD_RESET_REQUEST","email":%q}]`, maskEmailForAdmin(user.Email)))
	return successPasswordResetEmailSent(c)
}

// ResetPassword POST /api/auth/email/reset-password
//
// 凭 token + 新密码完成重置。
//
//   - 校验 token：存在 / 未消费 / 未过期 / Purpose == reset_password
//   - 校验新密码强度（复用 validatePasswordStrength；username 由 token 关联的 user 决定）
//   - 事务：消费 token + 更新 user.PasswordHash
//   - 不自动登录 —— 用户重置完跳登录页输入新密码
func ResetPassword(c *fiber.Ctx) error {
	if !proxy.IsEmailEnabled() {
		return c.Status(503).JSON(fiber.Map{"success": false, "message_code": "ERR_EMAIL_FEATURE_DISABLED"})
	}
	var req resetPasswordRequest
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
		hash, database.EmailVerificationPurposeResetPassword).First(&verification).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_EMAIL_TOKEN_INVALID"})
		}
		log.Printf("[RESET-PWD] DB query failed: %v", err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}
	now := time.Now()
	if verification.IsConsumed() {
		return c.Status(409).JSON(fiber.Map{"success": false, "message_code": "ERR_EMAIL_TOKEN_CONSUMED"})
	}
	if verification.IsExpired(now) {
		return c.Status(410).JSON(fiber.Map{"success": false, "message_code": "ERR_EMAIL_TOKEN_EXPIRED"})
	}

	// 取 user（拿 username 做密码强度校验里的"password != username"判断）
	var user database.User
	if err := database.DB.Where("id = ?", verification.UserID).First(&user).Error; err != nil {
		log.Printf("[RESET-PWD] user not found uid=%d: %v", verification.UserID, err)
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_EMAIL_TOKEN_INVALID"})
	}

	if code, ok := validatePasswordStrength(req.NewPassword, user.Username); !ok {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": code})
	}

	newHash := utils.GenerateHash(req.NewPassword)
	if newHash == "" {
		log.Printf("[RESET-PWD] bcrypt hash empty user=%d", user.ID)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_INTERNAL"})
	}

	// 事务内：消费 token + 更新 PasswordHash
	txErr := database.DB.Transaction(func(tx *gorm.DB) error {
		// 一次性消费 token（race-safe：用 RowsAffected 判定）
		res := tx.Model(&database.EmailVerification{}).
			Where("id = ? AND consumed_at IS NULL", verification.ID).
			Update("consumed_at", now)
		if res.Error != nil {
			return fmt.Errorf("consume token: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return errEmailTokenRacedInTx
		}
		if err := tx.Model(&database.User{}).
			Where("id = ?", user.ID).
			Update("password_hash", newHash).Error; err != nil {
			return fmt.Errorf("update password: %w", err)
		}
		return nil
	})
	if txErr != nil {
		if errors.Is(txErr, errEmailTokenRacedInTx) {
			return c.Status(409).JSON(fiber.Map{"success": false, "message_code": "ERR_EMAIL_TOKEN_CONSUMED"})
		}
		log.Printf("[RESET-PWD] tx failed user=%d: %v", user.ID, txErr)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_TRANSACTION"})
	}

	// 刷新 AuthCache：让该用户的所有现存 session 失效（PasswordHash 变了 → 强烈建议作废 session）
	// 当前 AuthCache 只缓存 user 对象（不直接挂 session），所以 RefreshUserAuth 已够。
	proxy.RefreshUserAuth(user.ID)
	LogOperationBy(0, user.ID, "user", "PASSWORD_RESET_DONE", c.IP(),
		fmt.Sprintf(`[{"type":"PASSWORD_RESET_DONE","email":%q}]`, maskEmailForAdmin(user.Email)))
	return c.JSON(fiber.Map{"success": true, "message_code": "SUCCESS_PASSWORD_RESET"})
}

// successPasswordResetEmailSent —— 邮箱枚举防御统一响应。
func successPasswordResetEmailSent(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{
		"success":      true,
		"message_code": "SUCCESS_PASSWORD_RESET_EMAIL_SENT",
	})
}

// loadEmailResetTTL 读 SysConfig email_reset_ttl_seconds，缺失/非法用 900s（15 分钟）。
// 上限 1h 防 admin 配荒谬值（重置 token 越长越危险）。
func loadEmailResetTTL() time.Duration {
	v := readSysConfigCached("email_reset_ttl_seconds", "900")
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 || n > 3600 {
		return 15 * time.Minute
	}
	return time.Duration(n) * time.Second
}

// buildPasswordResetURL 拼装前端"设置新密码"页面 URL。
// 与 buildEmailVerifyURL 一样要求 server_address + https。
func buildPasswordResetURL(rawToken string) (string, error) {
	base := strings.TrimSpace(readSysConfigCached("server_address", ""))
	if base == "" {
		return "", fmt.Errorf("server_address SysConfig not configured")
	}
	if readBoolConfig("server_address_require_https", true) {
		if !strings.HasPrefix(strings.ToLower(base), "https://") {
			return "", fmt.Errorf("server_address must use https:// (got %q)", base)
		}
	}
	path := strings.TrimSpace(readSysConfigCached("email_reset_url_path", "/reset-password"))
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return strings.TrimRight(base, "/") + path + "?token=" + rawToken, nil
}
