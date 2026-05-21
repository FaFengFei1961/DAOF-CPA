// Package controller / email_login.go
//
// 邮箱+密码登录 endpoint。Phase G-2.2（2026-05-20）。
//
// 路由：POST /api/auth/email/login（rootApi 组，挂 emailLoginLimiter；不需要 UserGuard）
//
// 双 gate（任一不满足都拒）：
//   - SysConfig email_enabled = true（master）
//   - SysConfig email_login_enabled = true（admin 允许邮箱登录）
//
// 用户级 4 道校验（都过才发 session）：
//   1. User 存在 + 未封禁 + Email 匹配
//   2. PasswordHash 校验通过
//   3. EmailVerifiedAt 非 nil
//   4. EmailLoginEnabled = true（用户自己 opt-in 过）
//
// 安全设计 — **邮箱枚举防御**：
//   - 1-4 任一失败都返回 401 + ERR_LOGIN_FAILED 统一错误信息
//   - 即使 SMTP / DB 真实错误也不向客户端泄漏；只 log
//   - 时间侧信道：始终调一次 utils.CheckHash（哪怕 user 不存在也用假 hash 走一遍 bcrypt）
//     bcrypt cost=12 ~250ms，没 user 时直接返回 = 时间差异立刻能猜出 user 存在性
//   - rate limit 在 main.go 路由层（emailLoginLimiter，per-IP）
package controller

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"daof-cpa/database"
	"daof-cpa/utils"

	"github.com/gofiber/fiber/v2"
)

// dummyPasswordHashForTiming 是一个合法 bcrypt hash 字符串（cost=12 / "never-match"），
// 用于"user 不存在"时仍然走一遍 bcrypt 验证，消除时间侧信道枚举。
// **lazy init**：避免 package-level var 在 import 期就跑 bcrypt（cost=12 约 250ms），
// 导致 test 启动慢 + main() 启动延迟。每次启动随机生成保持时间一致。
var (
	dummyHashOnce sync.Once
	dummyHash     string
)

// Audit 2026-05-21 T1-3 fix：per-email brute-force lockout。
// emailLoginLimiter 是 per-IP 5次/5min —— 防不了分布式撞库（攻击者用 1000 个 IP
// × 5 次/5min × bcrypt 250ms = 5000 次/5min 打一个邮箱）。这里加 per-email 计数：
// 10 次失败 / 1 小时窗口 → 锁 1 小时（拒绝走 bcrypt，直接 429）。
const (
	emailLoginFailWindow   = time.Hour
	emailLoginFailThreshold = 10
	emailLoginLockDuration  = time.Hour
)

type emailLoginFailEntry struct {
	count       int
	firstAt     time.Time
	lockedUntil time.Time
}

var (
	emailLoginFailMu    sync.Mutex
	emailLoginFailStore = make(map[string]*emailLoginFailEntry)
)

// checkEmailLoginLockout 在 bcrypt 之前调用：若该 email 处于锁定窗口，
// 返回 (locked=true, retryAfterSec)。
func checkEmailLoginLockout(email string) (bool, int) {
	emailLoginFailMu.Lock()
	defer emailLoginFailMu.Unlock()
	entry, ok := emailLoginFailStore[email]
	if !ok {
		return false, 0
	}
	now := time.Now()
	if entry.lockedUntil.After(now) {
		return true, int(entry.lockedUntil.Sub(now).Seconds()) + 1
	}
	// 锁定到期 + 计数窗口过期 → 清掉
	if now.Sub(entry.firstAt) > emailLoginFailWindow {
		delete(emailLoginFailStore, email)
	}
	return false, 0
}

// recordEmailLoginFail 在每次失败后调用：累加计数，过阈值则锁定。
func recordEmailLoginFail(email string) {
	if email == "" {
		return
	}
	emailLoginFailMu.Lock()
	defer emailLoginFailMu.Unlock()
	now := time.Now()
	entry, ok := emailLoginFailStore[email]
	if !ok || now.Sub(entry.firstAt) > emailLoginFailWindow {
		emailLoginFailStore[email] = &emailLoginFailEntry{count: 1, firstAt: now}
		return
	}
	entry.count++
	if entry.count >= emailLoginFailThreshold {
		entry.lockedUntil = now.Add(emailLoginLockDuration)
	}
}

// clearEmailLoginFail 在登录成功后调用：清掉失败计数。
func clearEmailLoginFail(email string) {
	if email == "" {
		return
	}
	emailLoginFailMu.Lock()
	delete(emailLoginFailStore, email)
	emailLoginFailMu.Unlock()
}

// getDummyPasswordHashForTiming 懒加载并 cache 一个 bcrypt hash 用于失败路径的时间侧信道防御。
// 若 bcrypt 异常返回空串，调用方需 fallback（loginFailedResponse 里有处理）。
func getDummyPasswordHashForTiming() string {
	dummyHashOnce.Do(func() {
		dummyHash = utils.GenerateHash("dummy-no-match-" + utils.GenerateRandomToken("seed"))
	})
	return dummyHash
}

type emailLoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// EmailLogin POST /api/auth/email/login
//
// 流程：双 gate → user 查找 → bcrypt → verified/login_enabled 检查 → 发 session。
// 全程不向客户端泄漏 user 存在性或失败原因；仅 log 内部细节。
func EmailLogin(c *fiber.Ctx) error {
	// Gate 1: master switch
	childOK, masterOK := requireEmailFeatureEnabled("email_login_enabled")
	if !masterOK {
		return c.Status(503).JSON(fiber.Map{"success": false, "message_code": "ERR_EMAIL_FEATURE_DISABLED"})
	}
	if !childOK {
		return c.Status(503).JSON(fiber.Map{"success": false, "message_code": "ERR_EMAIL_LOGIN_DISABLED"})
	}

	var req emailLoginRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_BAD_REQUEST"})
	}
	email, ok := normalizeEmail(req.Email)
	if !ok {
		// 邮箱格式无效：直接拒，但用统一 LOGIN_FAILED 不让枚举
		return loginFailedResponse(c, "invalid email format input", "", req.Password)
	}
	if strings.TrimSpace(req.Password) == "" {
		return loginFailedResponse(c, "empty password input", email, "")
	}

	// Audit T1-3：per-email lockout 在 bcrypt 之前先拦截
	if locked, retryAfter := checkEmailLoginLockout(email); locked {
		log.Printf("[EMAIL-LOGIN] account locked email=%s ip=%s retry_after=%ds",
			maskEmailForAdmin(email), c.IP(), retryAfter)
		c.Set("Retry-After", fmt.Sprintf("%d", retryAfter))
		return c.Status(429).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_LOGIN_FAILED", // 不暴露锁定状态，避免攻击者通过 429 判定账户存在
			"message":      "邮箱或密码错误",
		})
	}

	// 查用户：partial unique index 保证 (email, status=1) 至多一行
	var user database.User
	err := database.DB.Where("email = ? AND status = ?", email, 1).First(&user).Error
	if err != nil {
		// user 不存在：仍然走一次 bcrypt（消除时间侧信道）+ 返回统一 LOGIN_FAILED
		return loginFailedResponse(c, "user not found", email, req.Password)
	}

	// bcrypt 校验：utils.CheckHash 内部是 bcrypt.CompareHashAndPassword，恒定时间
	if !utils.CheckHash(req.Password, user.PasswordHash) {
		LogOperationBy(0, user.ID, "user", "EMAIL_LOGIN_FAIL", c.IP(),
			fmt.Sprintf(`[{"type":"EMAIL_LOGIN_FAIL","reason":"bad_password","email":%q}]`, email))
		return loginFailedResponse(c, "bad password", email, "")
	}

	// PasswordHash 校验过了，但还要检查：
	//   - 邮箱已验证（未验证不能登录）
	//   - 用户自己开启了 EmailLoginEnabled
	// 任一失败仍走统一 LOGIN_FAILED，避免泄漏"邮箱存在 + 密码对"+ 仅状态问题
	if user.EmailVerifiedAt == nil {
		LogOperationBy(0, user.ID, "user", "EMAIL_LOGIN_FAIL", c.IP(),
			fmt.Sprintf(`[{"type":"EMAIL_LOGIN_FAIL","reason":"email_not_verified","email":%q}]`, email))
		return loginFailedResponse(c, "email not verified", email, "")
	}
	if !user.EmailLoginEnabled {
		LogOperationBy(0, user.ID, "user", "EMAIL_LOGIN_FAIL", c.IP(),
			fmt.Sprintf(`[{"type":"EMAIL_LOGIN_FAIL","reason":"user_disabled","email":%q}]`, email))
		return loginFailedResponse(c, "user disabled email login", email, "")
	}

	// 全部 gate 通过 → 创建 session
	sessionID, err := database.CreateUserSession(user.ID, c.Get("User-Agent"), c.IP())
	if err != nil {
		log.Printf("[EMAIL-LOGIN] create session failed user=%d: %v", user.ID, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_INSERT_FAILED"})
	}

	// 成功登录 → 清掉失败计数，避免下一次合法登录还在锁定窗口里
	clearEmailLoginFail(email)

	LogOperationBy(user.ID, user.ID, "user", "EMAIL_LOGIN", c.IP(),
		fmt.Sprintf(`[{"type":"EMAIL_LOGIN","email":%q,"username":%q}]`, email, user.Username))
	return c.JSON(fiber.Map{
		"success":      true,
		"message_code": "SUCCESS_LOGIN",
		"session_id":   sessionID,
		"username":     user.Username,
	})
}

// loginFailedResponse 统一登录失败响应。所有内部原因都包装成同一对外 message_code，
// 防邮箱枚举 + 时间侧信道。reason / email 仅进服务端 log。
//
// password 参数：若非空，对其调一次 bcrypt（与 dummy hash 比对，恒返 false），消除"是否走过 bcrypt"的时间差。
// 若 dummy hash 因 bcrypt 异常为空（极少见），fallback 用固定 sleep 维持时间常量。
func loginFailedResponse(c *fiber.Ctx, internalReason, email, password string) error {
	if password != "" {
		dummy := getDummyPasswordHashForTiming()
		if dummy != "" {
			// 时间侧信道防御：故意走一遍 bcrypt 比较（结果丢弃）
			_ = utils.CheckHash(password, dummy)
		} else {
			// fallback：bcrypt 初始化失败 → 用 sleep 模拟 bcrypt cost=12 时间
			time.Sleep(250 * time.Millisecond)
		}
	}
	if email != "" {
		// Audit T1-3：记录失败计数（locked 检查在 EmailLogin 入口已做）
		recordEmailLoginFail(email)
		log.Printf("[EMAIL-LOGIN] failed reason=%s email=%s ip=%s",
			internalReason, maskEmailForAdmin(email), c.IP())
	}
	return c.Status(401).JSON(fiber.Map{
		"success":      false,
		"message_code": "ERR_LOGIN_FAILED",
		"message":      "邮箱或密码错误",
	})
}
