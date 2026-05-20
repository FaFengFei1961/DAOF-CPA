// Package controller / email_signup.go
//
// 邮箱+密码注册 endpoint。Phase G-2.3（2026-05-20）。
//
// 路由：POST /api/auth/email/signup（不需要 UserGuard；挂 emailSignupLimiter）
//
// 双 gate（admin SysConfig）：
//   - email_enabled = true（master）
//   - email_signup_enabled = true（允许邮箱注册）
//
// 流程：
//   1. 双 gate → 入参 normalize（email + password 强度校验 + username 格式）
//   2. registerMu 临界区：cap 检查 → 邮箱占用 → username 占用 → 创建 user（PasswordHash 落库）
//   3. 邮箱**未**自动验证 —— 发验证邮件，用户必须点击链接才能登录
//   4. signup_bonus 与 GH OAuth 路径同（复用 createUserWithSignupBonus）
//   5. 推荐人奖励（query 参数 ?ref=username 或 body.ReferrerUsername）
//   6. 返回 username + 提示"请到邮箱完成验证后再登录"（不发 session_id）
//
// 安全设计：
//   - 邮箱枚举：注册路径接受了"邮箱已被占用"明确回错（与登录路径不同—注册时用户必须知道
//     才能改填别的邮箱）。攻击者可通过此判断邮箱是否注册过，是已知 trade-off。
//   - 密码：bcrypt cost=12（utils.GenerateHash）
//   - 拉新返佣：仅在 referrer 用户存在且非 self-referral 时生效
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

type emailSignupRequest struct {
	Email             string `json:"email"`
	Password          string `json:"password"`
	Username          string `json:"username"`           // 必填：邮箱注册没有 OAuth name 来源
	ReferrerUsername  string `json:"referrer_username"`  // 可选；空时不走拉新
}

// EmailSignup POST /api/auth/email/signup
func EmailSignup(c *fiber.Ctx) error {
	// Gate
	childOK, masterOK := requireEmailFeatureEnabled("email_signup_enabled")
	if !masterOK {
		return c.Status(503).JSON(fiber.Map{"success": false, "message_code": "ERR_EMAIL_FEATURE_DISABLED"})
	}
	if !childOK {
		return c.Status(503).JSON(fiber.Map{"success": false, "message_code": "ERR_EMAIL_SIGNUP_DISABLED"})
	}

	var req emailSignupRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_BAD_REQUEST"})
	}
	email, ok := normalizeEmail(req.Email)
	if !ok {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_EMAIL_INVALID_FORMAT"})
	}
	req.Username = strings.TrimSpace(req.Username)
	if !usernameRegex.MatchString(req.Username) {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_NICKNAME_FORMAT"})
	}
	if code, ok := validatePasswordStrength(req.Password, req.Username); !ok {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": code})
	}

	// 临界区：cap 检查 + 邮箱/用户名占用检查 + 创建 user，与 OAuth 路径同一锁
	registerMu.Lock()
	defer registerMu.Unlock()

	if rejectIfUserCapReached(c) {
		return nil
	}

	// 邮箱占用：partial unique index 也兜底，但这里给出友好错误
	var dup database.User
	if database.DB.Where("email = ?", email).First(&dup).Error == nil {
		return c.Status(409).JSON(fiber.Map{"success": false, "message_code": "ERR_EMAIL_TAKEN"})
	}
	// 用户名占用：username 是 GORM unique
	var dupName database.User
	if database.DB.Where("username = ?", req.Username).First(&dupName).Error == nil {
		return c.Status(409).JSON(fiber.Map{"success": false, "message_code": "ERR_USERNAME_TAKEN"})
	}

	pwdHash := utils.GenerateHash(req.Password)
	if pwdHash == "" {
		log.Printf("[EMAIL-SIGNUP] bcrypt hash empty (password too long?) email=%s", maskEmailForAdmin(email))
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_INTERNAL"})
	}

	signupBonusMicro, referrerBonusMicro, refereeBonusMicro := resolveBonusConfig()
	newSk := utils.GenerateRandomToken("sk-daof")
	newUser := database.User{
		Email:        email,         // 邮箱写入但 EmailVerifiedAt = nil（用户必须验证后才能登录）
		Username:     req.Username,
		PasswordHash: pwdHash,
		Role:         "user",
		Token:        newSk,
		Quota:        signupBonusMicro,
		Status:       1,
		RegIP:        c.IP(),
		RegRiskScore: 0,

		BalanceConsumeEnabled:       readBoolConfig("balance_consume_default_enabled", false),
		BalanceConsumeLimitUSD:      readDefaultBalanceConsumeLimitMicroUSD(),
		BalanceConsumeWindowSeconds: int(readInt64Config("balance_consume_default_window_secs", 2592000)),

		// EmailLoginEnabled = true：邮箱+密码注册路径的用户显然就是想用邮箱登录的；
		// "opt-in 在设置里"那套逻辑是给 OAuth 用户走 G-2.5 set-password 流程时用的，
		// 不适用于显式 signup。否则用户注册完→验邮箱→却无法登录，需先登录才能改设置，死循环。
		EmailLoginEnabled: true,
		// EmailVerifiedAt = nil：必须 verify 后才能登录（login handler 检查这个）
	}

	if err := createUserWithSignupBonus(&newUser, signupBonusMicro, "email"); err != nil {
		// unique 冲突的最后兜底：partial unique index 拦下并发竞态
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return c.Status(409).JSON(fiber.Map{"success": false, "message_code": "ERR_EMAIL_TAKEN"})
		}
		log.Printf("[EMAIL-SIGNUP] tx failed username=%s email=%s: %v", newUser.Username, maskEmailForAdmin(email), err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_CREATE_PASS_RECORD"})
	}
	proxy.AddUserToAuthCache(&newUser)

	// 推荐人奖励：仅当 referrer 存在且非自己
	refUsername := strings.TrimSpace(req.ReferrerUsername)
	if refUsername != "" && refUsername != newUser.Username {
		applyReferralBonuses(c, newUser.ID, newUser.Username, refUsername, referrerBonusMicro, refereeBonusMicro)
	}

	// 立即发验证邮件（与 G-1.5 BindEmail 逻辑相同，但 user 已存在所以略简）
	if err := sendInitialVerifyEmail(c, &newUser); err != nil {
		// 邮件发不出不阻塞注册成功 — 用户可在登录页用"重新发送验证邮件"重试
		log.Printf("[EMAIL-SIGNUP] send verify email failed user=%d: %v", newUser.ID, err)
	}

	LogOperationBy(0, newUser.ID, "system", "REGISTER", c.IP(),
		fmt.Sprintf(`[{"type":"REGISTER","via":"email","username":%q,"email":%q,"ref":%q,"signup_bonus":%g,"signup_bonus_micro":%d}]`,
			newUser.Username, maskEmailForAdmin(email), refUsername,
			database.MicroToUSD(signupBonusMicro), signupBonusMicro))

	var afterCount int64
	database.DB.Model(&database.User{}).Where("role = ?", "user").Count(&afterCount)
	log.Printf("[USER-CREATED] via=email id=%d username=%s email=%s ip=%s new_user_count=%d signup_bonus=%s",
		newUser.ID, newUser.Username, maskEmailForAdmin(email), c.IP(), afterCount, database.FormatMicroUSD(signupBonusMicro))

	return c.JSON(fiber.Map{
		"success":      true,
		"message_code": "SUCCESS_SIGNUP_PENDING_VERIFY",
		"username":     newUser.Username,
		// **不**返回 session_id：用户必须验证邮箱后才能登录
	})
}

// sendInitialVerifyEmail 注册时立即发一封验证邮件。
// 与 G-1.5 BindEmail 的发信逻辑一致，但跳过限流（注册流程已限流，不应在这里再次限流）。
func sendInitialVerifyEmail(c *fiber.Ctx, user *database.User) error {
	if !proxy.IsEmailEnabled() {
		return errors.New("email feature disabled")
	}
	rawToken, tokenHash, err := generateEmailToken()
	if err != nil {
		return fmt.Errorf("gen token: %w", err)
	}
	ttl := loadEmailVerifyTTL()

	txErr := database.DB.Transaction(func(tx *gorm.DB) error {
		// 注册路径下 user 才刚创建，理论上没有旧 token，但保险起见仍 invalidate（与 BindEmail 一致）
		// 跳过 "consume_at='replaced'" 步骤——这是首次发，肯定没旧 token
		row := database.EmailVerification{
			UserID:    user.ID,
			Email:     user.Email,
			TokenHash: tokenHash,
			Purpose:   database.EmailVerificationPurposeVerify,
			ExpiresAt: time.Now().Add(ttl),
			ClientIP:  c.IP(),
			UserAgent: truncateUserAgent(c.Get("User-Agent")),
			CreatedAt: time.Now(),
		}
		return tx.Create(&row).Error
	})
	if txErr != nil {
		return fmt.Errorf("insert verification: %w", txErr)
	}

	verifyURL, err := buildEmailVerifyURL(rawToken)
	if err != nil {
		return fmt.Errorf("build verify url: %w", err)
	}
	locale := emailLocaleFromCtx(c)
	msg, err := proxy.RenderEmail(proxy.EmailTplVerify, locale, proxy.EmailVars{
		UserName:  user.Username,
		UserEmail: user.Email,
		VerifyURL: verifyURL,
		ExpiresIn: ttlDisplay(ttl, locale),
		AppName:   proxy.AppNameFromConfig(),
	})
	if err != nil {
		return fmt.Errorf("render verify email: %w", err)
	}
	dedupKey := fmt.Sprintf("verify:%d:%s", user.ID, user.Email)
	task := proxy.EmailTask{
		To:       user.Email,
		Message:  msg,
		DedupKey: dedupKey,
		Label:    "signup_verify",
	}
	if err := proxy.SendEmailDeduped(task); err != nil && !errors.Is(err, proxy.ErrEmailDedup) {
		return fmt.Errorf("enqueue verify email: %w", err)
	}
	return nil
}
