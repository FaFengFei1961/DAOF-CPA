// Package controller / user_email.go
//
// 用户视角的邮箱绑定 / 验证 / 解绑 API。Phase G-1.5（2026-05-20）。
//
// 路由（均挂 UserGuard，写动作挂 CSRFGuard + 邮件限流）：
//   - POST   /api/user/email/bind                  发起绑定，发验证邮件
//   - POST   /api/user/email/verify                提交 token 完成验证
//   - POST   /api/user/email/resend-verification   重发验证邮件（dedup TTL 内跳过）
//   - DELETE /api/user/email                       解绑（清除 Email + EmailVerifiedAt）
//   - GET    /api/user/email                       查询当前绑定状态
//
// 安全约定：
//   - email 大小写 + 空白规范化（小写 + Trim）后入库；唯一性靠 DB partial unique index
//   - token：32 字节 crypto/rand → base64url；DB 仅存 SHA-256 hex
//   - TTL：默认 1 小时（SysConfig email_verify_ttl_seconds）
//   - 限流：复用 proxy.CheckEmailRateLimit（per-email + per-IP，默认 5/20/hour）
//   - master kill：proxy.IsEmailEnabled=false 时所有写入路径直接 503
//   - 邮箱已被他人占用 → 409；已绑同邮箱未验证 → 重发（dedup）；已绑不同邮箱 → 409 要求先解绑
package controller

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"daof-cpa/database"
	"daof-cpa/proxy"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

type emailBindRequest struct {
	Email string `json:"email"`
}

type emailVerifyRequest struct {
	Token string `json:"token"`
}

// BindEmail POST /api/user/email/bind
//
// 用户提交想绑定的邮箱地址。流程：
//   1. 校验 master switch + email 格式
//   2. 限流（per-email + per-IP）
//   3. 检查邮箱是否被他人占用 / 当前用户是否已绑不同邮箱
//   4. 作废用户之前所有 verify purpose 的未消费 token
//   5. 生成新 token + INSERT EmailVerification
//   6. 渲染邮件 + enqueue
//   7. 返回 SUCCESS_EMAIL_BIND_SENT（不在响应里回显 token）
func BindEmail(c *fiber.Ctx) error {
	if !proxy.IsEmailEnabled() {
		return c.Status(503).JSON(fiber.Map{"success": false, "message_code": "ERR_EMAIL_FEATURE_DISABLED"})
	}
	user, err := getCurrentUser(c)
	if err != nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}
	var req emailBindRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_BAD_REQUEST"})
	}
	email, ok := normalizeEmail(req.Email)
	if !ok {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_EMAIL_INVALID_FORMAT"})
	}

	// 已绑同邮箱且已验证 → 友好响应（不返回错误）
	if strings.EqualFold(user.Email, email) && user.EmailVerifiedAt != nil {
		return c.JSON(fiber.Map{"success": true, "message_code": "SUCCESS_EMAIL_ALREADY_VERIFIED"})
	}
	// 已绑不同邮箱（无论是否验证）→ 要求先解绑，避免一个账号同时挂两个 pending 邮箱
	if user.Email != "" && !strings.EqualFold(user.Email, email) {
		return c.Status(409).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_EMAIL_BIND_BLOCKED",
			"message":      "您已绑定其他邮箱，请先解绑再切换",
		})
	}

	// 限流（per-email + per-IP）
	clientIP := c.IP()
	if err := proxy.CheckEmailRateLimit(email, clientIP); err != nil {
		return c.Status(429).JSON(fiber.Map{"success": false, "message_code": "ERR_EMAIL_RATE_LIMIT"})
	}

	// 邮箱是否被他人占用？查 users 表（partial unique index 也会兜底，但这里给出友好错误）
	if takenByOther, queryErr := isEmailTakenByOther(user.ID, email); queryErr != nil {
		log.Printf("[EMAIL-BIND] DB query failed user=%d: %v", user.ID, queryErr)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	} else if takenByOther {
		return c.Status(409).JSON(fiber.Map{"success": false, "message_code": "ERR_EMAIL_TAKEN"})
	}

	rawToken, tokenHash, err := generateEmailToken()
	if err != nil {
		log.Printf("[EMAIL-BIND] token gen failed user=%d: %v", user.ID, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_INTERNAL"})
	}

	ttl := loadEmailVerifyTTL()
	verification := database.EmailVerification{
		UserID:    user.ID,
		Email:     email,
		TokenHash: tokenHash,
		Purpose:   database.EmailVerificationPurposeVerify,
		ExpiresAt: time.Now().Add(ttl),
		ClientIP:  clientIP,
		UserAgent: truncateUserAgent(c.Get("User-Agent")),
		CreatedAt: time.Now(),
	}
	// 事务内：作废旧 pending token（仅 update consumed_at）+ 创建新 token
	txErr := database.DB.Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		if err := tx.Model(&database.EmailVerification{}).
			Where("user_id = ? AND purpose = ? AND consumed_at IS NULL AND expires_at > ?",
				user.ID, database.EmailVerificationPurposeVerify, now).
			Update("consumed_at", now).Error; err != nil {
			return fmt.Errorf("invalidate prior tokens: %w", err)
		}
		if err := tx.Create(&verification).Error; err != nil {
			return fmt.Errorf("insert verification: %w", err)
		}
		return nil
	})
	if txErr != nil {
		log.Printf("[EMAIL-BIND] tx failed user=%d: %v", user.ID, txErr)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_TRANSACTION"})
	}

	verifyURL, err := buildEmailVerifyURL(rawToken)
	if err != nil {
		log.Printf("[EMAIL-BIND] build URL failed: %v", err)
		return c.Status(503).JSON(fiber.Map{"success": false, "message_code": "ERR_SERVER_ADDRESS_NOT_CONFIGURED"})
	}

	locale := emailLocaleFromCtx(c)
	msg, err := proxy.RenderEmail(proxy.EmailTplVerify, locale, proxy.EmailVars{
		UserName:  user.Username,
		UserEmail: email,
		VerifyURL: verifyURL,
		ExpiresIn: ttlDisplay(ttl, locale),
		AppName:   proxy.AppNameFromConfig(),
	})
	if err != nil {
		log.Printf("[EMAIL-BIND] render failed user=%d: %v", user.ID, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_EMAIL_SEND_FAIL"})
	}
	dedupKey := fmt.Sprintf("verify:%d:%s", user.ID, email)
	if err := proxy.SendEmailDeduped(proxy.EmailTask{
		To:       email,
		Message:  msg,
		DedupKey: dedupKey,
		Label:    "verify_bind",
	}); err != nil && !errors.Is(err, proxy.ErrEmailDedup) {
		log.Printf("[EMAIL-BIND] enqueue failed user=%d: %v", user.ID, err)
		return c.Status(503).JSON(fiber.Map{"success": false, "message_code": "ERR_EMAIL_SEND_FAIL"})
	}
	proxy.RegisterEmailSent(email, clientIP)
	return c.JSON(fiber.Map{"success": true, "message_code": "SUCCESS_EMAIL_BIND_SENT"})
}

// VerifyEmail POST /api/user/email/verify
//
// 用户从邮件链接进入前端 → 前端 POST { token } 到本端点。
// 服务端 SHA-256 hash 后查 EmailVerification，状态机：
//   - 未找到 → ERR_EMAIL_TOKEN_INVALID
//   - 已 consumed → ERR_EMAIL_TOKEN_CONSUMED
//   - 已过期 → ERR_EMAIL_TOKEN_EXPIRED
//   - 都通过 → SET user.Email + EmailVerifiedAt + token.ConsumedAt
func VerifyEmail(c *fiber.Ctx) error {
	user, err := getCurrentUser(c)
	if err != nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}
	var req emailVerifyRequest
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
		hash, database.EmailVerificationPurposeVerify).First(&verification).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_EMAIL_TOKEN_INVALID"})
		}
		log.Printf("[EMAIL-VERIFY] DB query failed: %v", err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}

	// fix CRITICAL：token 必须属于当前登录用户（防 A 用 B 的 token 偷绑邮箱）
	if verification.UserID != user.ID {
		return c.Status(403).JSON(fiber.Map{"success": false, "message_code": "ERR_EMAIL_TOKEN_INVALID"})
	}
	now := time.Now()
	if verification.IsConsumed() {
		return c.Status(409).JSON(fiber.Map{"success": false, "message_code": "ERR_EMAIL_TOKEN_CONSUMED"})
	}
	if verification.IsExpired(now) {
		return c.Status(410).JSON(fiber.Map{"success": false, "message_code": "ERR_EMAIL_TOKEN_EXPIRED"})
	}

	// 事务内：消费 token + 更新 user.Email/EmailVerifiedAt
	txErr := database.DB.Transaction(func(tx *gorm.DB) error {
		// 再 race 检查邮箱是否被他人占走
		taken, qerr := isEmailTakenByOtherTx(tx, user.ID, verification.Email)
		if qerr != nil {
			return fmt.Errorf("re-check email taken: %w", qerr)
		}
		if taken {
			return errEmailTakenInTx
		}
		// 消费 token（only update consumed_at；append-only invariant 允许）
		res := tx.Model(&database.EmailVerification{}).
			Where("id = ? AND consumed_at IS NULL", verification.ID).
			Update("consumed_at", now)
		if res.Error != nil {
			return fmt.Errorf("consume token: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return errEmailTokenRacedInTx // 并发已被另一请求消费
		}
		// 更新 user
		if err := tx.Model(&database.User{}).
			Where("id = ?", user.ID).
			Updates(map[string]any{
				"email":             verification.Email,
				"email_verified_at": now,
			}).Error; err != nil {
			return fmt.Errorf("update user email: %w", err)
		}
		return nil
	})
	if txErr != nil {
		if errors.Is(txErr, errEmailTakenInTx) {
			return c.Status(409).JSON(fiber.Map{"success": false, "message_code": "ERR_EMAIL_TAKEN"})
		}
		if errors.Is(txErr, errEmailTokenRacedInTx) {
			return c.Status(409).JSON(fiber.Map{"success": false, "message_code": "ERR_EMAIL_TOKEN_CONSUMED"})
		}
		log.Printf("[EMAIL-VERIFY] tx failed user=%d: %v", user.ID, txErr)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_TRANSACTION"})
	}

	// 刷新 AuthCache 里的 user 对象，让后续 /api/user/me 看到最新 email/verified
	proxy.RefreshUserAuth(user.ID)
	return c.JSON(fiber.Map{
		"success":      true,
		"message_code": "SUCCESS_EMAIL_VERIFIED",
		"email":        verification.Email,
	})
}

// ResendVerificationEmail POST /api/user/email/resend-verification
//
// 与 BindEmail 流程相同，但要求 user.Email 已经有值（之前 bind 过），
// 且未验证（已验证就走 SUCCESS_EMAIL_ALREADY_VERIFIED 不重发）。
// 复用 dedup（5min 内重复点击不会刷邮件给 SMTP）。
func ResendVerificationEmail(c *fiber.Ctx) error {
	if !proxy.IsEmailEnabled() {
		return c.Status(503).JSON(fiber.Map{"success": false, "message_code": "ERR_EMAIL_FEATURE_DISABLED"})
	}
	user, err := getCurrentUser(c)
	if err != nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}
	if user.Email == "" {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_EMAIL_NOT_BOUND"})
	}
	if user.EmailVerifiedAt != nil {
		return c.JSON(fiber.Map{"success": true, "message_code": "SUCCESS_EMAIL_ALREADY_VERIFIED"})
	}
	email := user.Email
	clientIP := c.IP()
	if err := proxy.CheckEmailRateLimit(email, clientIP); err != nil {
		return c.Status(429).JSON(fiber.Map{"success": false, "message_code": "ERR_EMAIL_RATE_LIMIT"})
	}

	rawToken, tokenHash, err := generateEmailToken()
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_INTERNAL"})
	}
	ttl := loadEmailVerifyTTL()
	verification := database.EmailVerification{
		UserID:    user.ID,
		Email:     email,
		TokenHash: tokenHash,
		Purpose:   database.EmailVerificationPurposeVerify,
		ExpiresAt: time.Now().Add(ttl),
		ClientIP:  clientIP,
		UserAgent: truncateUserAgent(c.Get("User-Agent")),
		CreatedAt: time.Now(),
	}
	txErr := database.DB.Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		if err := tx.Model(&database.EmailVerification{}).
			Where("user_id = ? AND purpose = ? AND consumed_at IS NULL AND expires_at > ?",
				user.ID, database.EmailVerificationPurposeVerify, now).
			Update("consumed_at", now).Error; err != nil {
			return err
		}
		return tx.Create(&verification).Error
	})
	if txErr != nil {
		log.Printf("[EMAIL-RESEND] tx failed user=%d: %v", user.ID, txErr)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_TRANSACTION"})
	}

	verifyURL, err := buildEmailVerifyURL(rawToken)
	if err != nil {
		return c.Status(503).JSON(fiber.Map{"success": false, "message_code": "ERR_SERVER_ADDRESS_NOT_CONFIGURED"})
	}
	locale := emailLocaleFromCtx(c)
	msg, err := proxy.RenderEmail(proxy.EmailTplVerify, locale, proxy.EmailVars{
		UserName:  user.Username,
		UserEmail: email,
		VerifyURL: verifyURL,
		ExpiresIn: ttlDisplay(ttl, locale),
		AppName:   proxy.AppNameFromConfig(),
	})
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_EMAIL_SEND_FAIL"})
	}
	dedupKey := fmt.Sprintf("verify:%d:%s", user.ID, email)
	if err := proxy.SendEmailDeduped(proxy.EmailTask{
		To:       email,
		Message:  msg,
		DedupKey: dedupKey,
		Label:    "verify_resend",
	}); err != nil && !errors.Is(err, proxy.ErrEmailDedup) {
		log.Printf("[EMAIL-RESEND] enqueue failed user=%d: %v", user.ID, err)
		return c.Status(503).JSON(fiber.Map{"success": false, "message_code": "ERR_EMAIL_SEND_FAIL"})
	}
	proxy.RegisterEmailSent(email, clientIP)
	return c.JSON(fiber.Map{"success": true, "message_code": "SUCCESS_EMAIL_BIND_SENT"})
}

// UnbindEmail DELETE /api/user/email
//
// 清除 Email + EmailVerifiedAt + EmailLoginEnabled，并作废所有 pending token。
// 不需要二次确认（CSRFGuard 已防 CSRF；前端做"是否确定"弹窗即可）。
func UnbindEmail(c *fiber.Ctx) error {
	user, err := getCurrentUser(c)
	if err != nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}
	if user.Email == "" {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_EMAIL_NOT_BOUND"})
	}
	txErr := database.DB.Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		if err := tx.Model(&database.EmailVerification{}).
			Where("user_id = ? AND consumed_at IS NULL", user.ID).
			Update("consumed_at", now).Error; err != nil {
			return fmt.Errorf("invalidate pending tokens: %w", err)
		}
		if err := tx.Model(&database.User{}).
			Where("id = ?", user.ID).
			Updates(map[string]any{
				"email":               "",
				"email_verified_at":   nil,
				"email_login_enabled": false,
			}).Error; err != nil {
			return fmt.Errorf("clear user email: %w", err)
		}
		return nil
	})
	if txErr != nil {
		log.Printf("[EMAIL-UNBIND] tx failed user=%d: %v", user.ID, txErr)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_TRANSACTION"})
	}
	proxy.RefreshUserAuth(user.ID)
	return c.JSON(fiber.Map{"success": true, "message_code": "SUCCESS_EMAIL_UNBOUND"})
}

// GetMyEmailStatus GET /api/user/email
//
// 返回当前邮箱绑定状态。无需 CSRF，仅 UserGuard。
func GetMyEmailStatus(c *fiber.Ctx) error {
	user, err := getCurrentUser(c)
	if err != nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}
	return c.JSON(fiber.Map{
		"success": true,
		"data": fiber.Map{
			"email":               user.Email,
			"email_verified_at":   user.EmailVerifiedAt,
			"email_login_enabled": user.EmailLoginEnabled,
			"feature_enabled":     proxy.IsEmailEnabled(),
		},
	})
}

// ── 内部 helper ──

var (
	errEmailTakenInTx      = errors.New("email taken by another user (race)")
	errEmailTokenRacedInTx = errors.New("token already consumed by concurrent request")
)

// normalizeEmail 规范化用户输入邮箱：trim + lower + RFC 5322 解析校验。
// 返回 (规范化后地址, 是否合法)。
func normalizeEmail(raw string) (string, bool) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return "", false
	}
	if len(s) > 254 {
		return "", false
	}
	// net/mail 解析；拒绝带 display name 的格式（用户应只填邮箱地址）
	addr, err := mail.ParseAddress(s)
	if err != nil {
		return "", false
	}
	// ParseAddress 接受 "name <email>" 形式；我们只允许纯邮箱（防 header injection 类输入）
	if addr.Name != "" {
		return "", false
	}
	return addr.Address, true
}

// generateEmailToken 生成 32 字节 random → base64url 字符串（43 字符）+ SHA-256 hex hash。
// 原始 token 仅在邮件链接里出现一次；DB 永远只存 hash。
func generateEmailToken() (raw, hashHex string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", fmt.Errorf("crypto/rand: %w", err)
	}
	raw = base64.RawURLEncoding.EncodeToString(b)
	hashHex = hashEmailToken(raw)
	return raw, hashHex, nil
}

// hashEmailToken 把原始 token 哈希成 64 字符 hex（DB 列 size:64 匹配）。
func hashEmailToken(rawToken string) string {
	sum := sha256.Sum256([]byte(rawToken))
	return hex.EncodeToString(sum[:])
}

// loadEmailVerifyTTL 读 SysConfig email_verify_ttl_seconds，缺失 / 非法用 3600s（1 小时）。
func loadEmailVerifyTTL() time.Duration {
	v := readSysConfigCached("email_verify_ttl_seconds", "3600")
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 || n > 86400 { // 上限 24h，防 admin 配荒谬值
		return time.Hour
	}
	return time.Duration(n) * time.Second
}

// buildEmailVerifyURL 拼装前端验证页 URL。server_address 未配置 → 报错。
// 强制 https://，与 buildAbsoluteURL（topup_webhook.go）逻辑一致。
func buildEmailVerifyURL(rawToken string) (string, error) {
	base := strings.TrimSpace(readSysConfigCached("server_address", ""))
	if base == "" {
		return "", fmt.Errorf("server_address SysConfig not configured")
	}
	if readBoolConfig("server_address_require_https", true) {
		if !strings.HasPrefix(strings.ToLower(base), "https://") {
			return "", fmt.Errorf("server_address must use https:// (got %q)", base)
		}
	}
	path := strings.TrimSpace(readSysConfigCached("email_verify_url_path", "/verify-email"))
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	// token 是 base64url，安全字符集；不需要进一步 url-encode
	return strings.TrimRight(base, "/") + path + "?token=" + rawToken, nil
}

// emailLocaleFromCtx 从 Accept-Language 派生用户偏好的语言（zh / en）。
func emailLocaleFromCtx(c *fiber.Ctx) string {
	return c.Get("Accept-Language")
}

// truncateUserAgent 把 UA 字符串截断到合理长度防 DB 列溢出。
func truncateUserAgent(ua string) string {
	if len(ua) > 255 {
		return ua[:255]
	}
	return ua
}

// ttlDisplay 用 zh / en 把 time.Duration 显示为 "1 小时" / "15 分钟" / "1 hour"。
func ttlDisplay(ttl time.Duration, locale string) string {
	zh := strings.HasPrefix(strings.ToLower(strings.TrimSpace(locale)), "zh") || locale == ""
	mins := int(ttl.Minutes())
	if zh {
		if mins >= 60 && mins%60 == 0 {
			return fmt.Sprintf("%d 小时", mins/60)
		}
		return fmt.Sprintf("%d 分钟", mins)
	}
	if mins >= 60 && mins%60 == 0 {
		if mins/60 == 1 {
			return "1 hour"
		}
		return fmt.Sprintf("%d hours", mins/60)
	}
	if mins == 1 {
		return "1 minute"
	}
	return fmt.Sprintf("%d minutes", mins)
}

// isEmailTakenByOther 用现有 DB 查另一个用户是否已绑此邮箱（partial unique 也兜底，
// 但这里给用户一个更友好的错误信息）。
func isEmailTakenByOther(currentUserID uint, email string) (bool, error) {
	return isEmailTakenByOtherTx(database.DB, currentUserID, email)
}

func isEmailTakenByOtherTx(tx *gorm.DB, currentUserID uint, email string) (bool, error) {
	var count int64
	err := tx.Model(&database.User{}).
		Where("email = ? AND id != ?", email, currentUserID).
		Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}
