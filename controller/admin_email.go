// Package controller / admin_email.go
//
// Admin 视角邮箱功能配置 API。Phase G-1.6（2026-05-20）。
//
// 路由（adminApi 组，已挂 LanGuard + AdminGuard）：
//   - GET  /api/admin/email/config       获取当前 SMTP 配置 + master toggle 状态（password 脱敏）
//   - PUT  /api/admin/email/config       更新 SMTP 配置 + toggle（password 加密前落库）
//   - POST /api/admin/email/test-send    发测试邮件验证 SMTP 可用
//
// 设计要点：
//   - **password 永远不回显**：GET 返回 has_password bool 而不是密码值，避免共享
//     admin 终端时旁观者拷贝密钥。前端 UI 用 "*****" 占位 + "Change password" 按钮。
//   - **不传 password 不改 password**：PUT body 若 smtp_password 字段为 nil 指针（JSON 缺失）
//     则保留现有加密值；显式空字符串 "" 才视为"清空密码"。
//   - **写入前 utils.Encrypt**：所有密码列加密后存 SysConfig，复用 BatchUpdateSysConfigs 同模式。
//   - **test-send 限流**：复用 G-1.4 CheckEmailRateLimit（per-email + per-IP），admin 也受限
//     防一个手抖把 SMTP 服务商触发风控。
//   - **CSRF**：写动作 PUT/POST 在 main.go 注册时挂 CSRFGuard。
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

// adminEmailConfigResponse 是 GET /api/admin/email/config 的响应结构。
// password 字段不出现 —— 只用 has_password 标记是否已配置。
type adminEmailConfigResponse struct {
	// Master toggle（admin 必须显式打开三个 flag 邮件功能才生效 + 注册/登录才放开）
	EmailEnabled        bool `json:"email_enabled"`
	EmailSignupEnabled  bool `json:"email_signup_enabled"`
	EmailLoginEnabled   bool `json:"email_login_enabled"`

	// SMTP 服务器配置
	SMTPHost           string `json:"smtp_host"`
	SMTPPort           int    `json:"smtp_port"`
	SMTPUsername       string `json:"smtp_username"`
	SMTPFrom           string `json:"smtp_from"`
	SMTPReplyTo        string `json:"smtp_reply_to"`
	SMTPUseImplicitTLS bool   `json:"smtp_use_implicit_tls"`
	HasPassword        bool   `json:"has_password"` // true 表示 DB 里已有加密密码（不回显原文）

	// 限流与 TTL（admin 通常用默认值）
	RateLimitPerEmailHourly int `json:"rate_limit_per_email_hourly"`
	RateLimitPerIPHourly    int `json:"rate_limit_per_ip_hourly"`
	VerifyTTLSeconds        int `json:"verify_ttl_seconds"`
	ResetTTLSeconds         int `json:"reset_ttl_seconds"`

	// 派生状态：所有 SMTP 字段齐全且 master 开启
	IsReady bool `json:"is_ready"`
}

// GetAdminEmailConfig GET /api/admin/email/config
//
// 返回当前邮件功能配置 + master toggle 状态。password 不回显。
func GetAdminEmailConfig(c *fiber.Ctx) error {
	if loadAdminUser(c) == nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}

	resp := adminEmailConfigResponse{
		EmailEnabled:       readBoolConfig("email_enabled", false),
		EmailSignupEnabled: readBoolConfig("email_signup_enabled", false),
		EmailLoginEnabled:  readBoolConfig("email_login_enabled", false),

		SMTPHost:           readSysConfigCached("smtp_host", ""),
		SMTPUsername:       readSysConfigCached("smtp_username", ""),
		SMTPFrom:           readSysConfigCached("smtp_from", ""),
		SMTPReplyTo:        readSysConfigCached("smtp_reply_to", ""),
		SMTPUseImplicitTLS: readBoolConfig("smtp_use_implicit_tls", false),

		RateLimitPerEmailHourly: int(readInt64Config("email_rate_limit_per_email_hourly", 5)),
		RateLimitPerIPHourly:    int(readInt64Config("email_rate_limit_per_ip_hourly", 20)),
		VerifyTTLSeconds:        int(readInt64Config("email_verify_ttl_seconds", 3600)),
		ResetTTLSeconds:         int(readInt64Config("email_reset_ttl_seconds", 900)),
	}
	if portRaw := strings.TrimSpace(readSysConfigCached("smtp_port", "")); portRaw != "" {
		if p, err := strconv.Atoi(portRaw); err == nil {
			resp.SMTPPort = p
		}
	}
	resp.HasPassword = strings.TrimSpace(readSysConfigCached("smtp_password", "")) != ""

	// IsReady：master 打开 + SMTP 5 字段齐 + has_password
	resp.IsReady = resp.EmailEnabled &&
		resp.SMTPHost != "" && resp.SMTPPort > 0 &&
		resp.SMTPUsername != "" && resp.SMTPFrom != "" && resp.HasPassword

	return c.JSON(fiber.Map{"success": true, "data": resp})
}

// adminEmailConfigUpdateRequest 是 PUT /api/admin/email/config 请求体。
// 所有指针字段为 nil 表示"不修改"；显式 "" / false / 0 才视为"清空/关闭"。
type adminEmailConfigUpdateRequest struct {
	// Master toggle
	EmailEnabled       *bool `json:"email_enabled"`
	EmailSignupEnabled *bool `json:"email_signup_enabled"`
	EmailLoginEnabled  *bool `json:"email_login_enabled"`

	// SMTP 服务器
	SMTPHost           *string `json:"smtp_host"`
	SMTPPort           *int    `json:"smtp_port"`
	SMTPUsername       *string `json:"smtp_username"`
	SMTPPassword       *string `json:"smtp_password"` // nil = 不改；"" = 显式清空；其他 = 替换
	SMTPFrom           *string `json:"smtp_from"`
	SMTPReplyTo        *string `json:"smtp_reply_to"`
	SMTPUseImplicitTLS *bool   `json:"smtp_use_implicit_tls"`

	// 限流 + TTL
	RateLimitPerEmailHourly *int `json:"rate_limit_per_email_hourly"`
	RateLimitPerIPHourly    *int `json:"rate_limit_per_ip_hourly"`
	VerifyTTLSeconds        *int `json:"verify_ttl_seconds"`
	ResetTTLSeconds         *int `json:"reset_ttl_seconds"`
}

// UpdateAdminEmailConfig PUT /api/admin/email/config
//
// 只更新 body 里显式传入的字段（指针非 nil）。password 加密后落库。
func UpdateAdminEmailConfig(c *fiber.Ctx) error {
	op := loadAdminUser(c)
	if op == nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}
	var req adminEmailConfigUpdateRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_BAD_REQUEST"})
	}

	// 收集待写入的 plain values（写库前会 utils.Encrypt）
	updates := map[string]string{}
	if req.EmailEnabled != nil {
		updates["email_enabled"] = formatBoolConfig(*req.EmailEnabled)
	}
	if req.EmailSignupEnabled != nil {
		updates["email_signup_enabled"] = formatBoolConfig(*req.EmailSignupEnabled)
	}
	if req.EmailLoginEnabled != nil {
		updates["email_login_enabled"] = formatBoolConfig(*req.EmailLoginEnabled)
	}
	if req.SMTPHost != nil {
		h := strings.TrimSpace(*req.SMTPHost)
		// SMTP host 本身在拨号时会经 SSRF 检查（ssrfSafeSMTPDialContext），
		// 这里只做"非空 + 不含奇怪字符"的基本校验，详细 SSRF 在调用 SendEmailViaSMTP 时兜底
		if h != "" && (strings.ContainsAny(h, " \r\n\t") || len(h) > 253) {
			return c.Status(400).JSON(fiber.Map{
				"success":      false,
				"message_code": "ERR_SMTP_HOST_INVALID",
				"message":      "smtp_host 含非法字符或过长（>253 字符）",
			})
		}
		updates["smtp_host"] = h
	}
	if req.SMTPPort != nil {
		p := *req.SMTPPort
		if p < 0 || p > 65535 {
			return c.Status(400).JSON(fiber.Map{
				"success":      false,
				"message_code": "ERR_SMTP_PORT_INVALID",
				"message":      "smtp_port 必须在 0-65535 范围",
			})
		}
		if p == 25 {
			return c.Status(400).JSON(fiber.Map{
				"success":      false,
				"message_code": "ERR_SMTP_PORT_PLAINTEXT",
				"message":      "smtp_port=25 是明文 SMTP，禁止使用；请用 465 (implicit TLS) 或 587 (STARTTLS)",
			})
		}
		if p == 0 {
			updates["smtp_port"] = ""
		} else {
			updates["smtp_port"] = strconv.Itoa(p)
		}
	}
	if req.SMTPUsername != nil {
		updates["smtp_username"] = strings.TrimSpace(*req.SMTPUsername)
	}
	if req.SMTPPassword != nil {
		// 显式传入：替换。空字符串视为"清空密码"，admin 用此来 disable SMTP 不删 host
		updates["smtp_password"] = *req.SMTPPassword
	}
	if req.SMTPFrom != nil {
		updates["smtp_from"] = strings.TrimSpace(*req.SMTPFrom)
	}
	if req.SMTPReplyTo != nil {
		updates["smtp_reply_to"] = strings.TrimSpace(*req.SMTPReplyTo)
	}
	if req.SMTPUseImplicitTLS != nil {
		updates["smtp_use_implicit_tls"] = formatBoolConfig(*req.SMTPUseImplicitTLS)
	}
	if req.RateLimitPerEmailHourly != nil {
		v := *req.RateLimitPerEmailHourly
		if v <= 0 || v > 1000 {
			return c.Status(400).JSON(fiber.Map{
				"success":      false,
				"message_code": "ERR_EMAIL_RATE_LIMIT_INVALID",
				"message":      "rate_limit_per_email_hourly 必须在 1-1000 范围",
			})
		}
		updates["email_rate_limit_per_email_hourly"] = strconv.Itoa(v)
	}
	if req.RateLimitPerIPHourly != nil {
		v := *req.RateLimitPerIPHourly
		if v <= 0 || v > 10000 {
			return c.Status(400).JSON(fiber.Map{
				"success":      false,
				"message_code": "ERR_EMAIL_RATE_LIMIT_INVALID",
				"message":      "rate_limit_per_ip_hourly 必须在 1-10000 范围",
			})
		}
		updates["email_rate_limit_per_ip_hourly"] = strconv.Itoa(v)
	}
	if req.VerifyTTLSeconds != nil {
		v := *req.VerifyTTLSeconds
		if v < 60 || v > 86400 {
			return c.Status(400).JSON(fiber.Map{
				"success":      false,
				"message_code": "ERR_EMAIL_TTL_INVALID",
				"message":      "verify_ttl_seconds 必须在 60-86400 范围",
			})
		}
		updates["email_verify_ttl_seconds"] = strconv.Itoa(v)
	}
	if req.ResetTTLSeconds != nil {
		v := *req.ResetTTLSeconds
		if v < 60 || v > 86400 {
			return c.Status(400).JSON(fiber.Map{
				"success":      false,
				"message_code": "ERR_EMAIL_TTL_INVALID",
				"message":      "reset_ttl_seconds 必须在 60-86400 范围",
			})
		}
		updates["email_reset_ttl_seconds"] = strconv.Itoa(v)
	}

	if len(updates) == 0 {
		return c.JSON(fiber.Map{"success": true, "message_code": "SUCCESS_NO_CHANGE"})
	}

	// 事务内 upsert：utils.Encrypt 加密后写入 SysConfig
	if err := persistAdminEmailConfigUpdates(updates); err != nil {
		log.Printf("[ADMIN-EMAIL-CFG] persist failed admin=%d: %v", op.ID, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_CONFIG_BATCH_FAILED"})
	}
	// 热刷 SysConfigCache，让 RenderEmail / SMTP 立即看到新值
	proxy.SyncCacheConfig()

	// 审计
	auditDetails := fmt.Sprintf(`[{"type":"ADMIN_EMAIL_CONFIG_UPDATE","keys_changed":%d}]`, len(updates))
	LogOperationBy(op.ID, op.ID, "admin", "ADMIN_EMAIL_CONFIG_UPDATE", c.IP(), auditDetails)

	return c.JSON(fiber.Map{
		"success":      true,
		"message_code": "SUCCESS_CONFIG_SAVED",
		"updated":      len(updates),
	})
}

type adminEmailTestSendRequest struct {
	To string `json:"to"` // 收件人邮箱（admin 自己的或团队的）
}

// SendAdminEmailTest POST /api/admin/email/test-send
//
// 让 admin 验证 SMTP 配置可用。流程：
//   1. 校验 SMTP 已配置 + master enable
//   2. 校验收件邮箱格式
//   3. 限流（per-email + per-IP）
//   4. 渲染测试邮件模板 + 异步入队
//   5. 立即返回；admin 自己查收
//
// 不要求 master switch email_enabled = true：admin 想在打开 master 前先测一下 SMTP。
// 但 SMTP 5 字段必须齐（IsConfigured()=true），否则告诉 admin 还没配完。
func SendAdminEmailTest(c *fiber.Ctx) error {
	op := loadAdminUser(c)
	if op == nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}
	var req adminEmailTestSendRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_BAD_REQUEST"})
	}
	to, ok := normalizeEmail(req.To)
	if !ok {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_EMAIL_INVALID_FORMAT"})
	}

	cfg, err := proxy.LoadSMTPConfig()
	if err != nil {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_SMTP_NOT_CONFIGURED",
			"message":      "SMTP 配置无法加载（可能密码解密失败或 port 非法），请先在邮件设置面板完成配置",
		})
	}
	if !cfg.IsConfigured() {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_SMTP_NOT_CONFIGURED",
			"message":      "SMTP host / port / username / password / from 必须全部填写",
		})
	}

	// admin 测试也走限流（防一个面板手抖几十次）— 原子 check + 占用（fix HIGH H-3）
	clientIP := c.IP()
	if err := proxy.CheckAndConsumeEmailRateLimit(to, clientIP); err != nil {
		return c.Status(429).JSON(fiber.Map{"success": false, "message_code": "ERR_EMAIL_RATE_LIMIT"})
	}

	// 测试邮件用 notification 模板（通用结构）+ 固定文案
	locale := emailLocaleFromCtx(c)
	zh := strings.HasPrefix(strings.ToLower(strings.TrimSpace(locale)), "zh") || locale == ""
	subject := "[DAOF-CPA] SMTP test email"
	title := "SMTP test email"
	body := "This is a test email sent from admin to verify SMTP configuration. Sent at " + time.Now().UTC().Format(time.RFC3339)
	if zh {
		subject = "【DAOF-CPA】SMTP 测试邮件"
		title = "SMTP 测试邮件"
		body = "这是一封 admin 发送的测试邮件，用于验证 SMTP 配置。发送时间：" + time.Now().UTC().Format(time.RFC3339)
	}

	msg, err := proxyRenderTestEmail(locale, to, op.Username, subject, title, body)
	if err != nil {
		log.Printf("[ADMIN-EMAIL-TEST] render failed admin=%d: %v", op.ID, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_EMAIL_SEND_FAIL"})
	}

	// 同步发送（不进队列）——admin 想立即看到成功 / 失败原因
	if err := proxy.SendEmailViaSMTP(cfg, proxy.EmailMessage{
		To:       to,
		Subject:  msg.Subject,
		TextBody: msg.TextBody,
		HTMLBody: msg.HTMLBody,
	}); err != nil {
		log.Printf("[ADMIN-EMAIL-TEST] SMTP send failed admin=%d to=%s: %v", op.ID, maskEmailForAdmin(to), err)
		return c.Status(502).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_EMAIL_SEND_FAIL",
			// 安全：err 经过 proxy.SanitizeErrorMessage 抹掉 Bearer/api_key 等
			"detail": proxy.SanitizeErrorMessage(err.Error(), 240),
		})
	}
	// rate-limit 已在入口 CheckAndConsume 时原子占用
	LogOperationBy(op.ID, op.ID, "admin", "ADMIN_EMAIL_TEST_SEND", c.IP(),
		fmt.Sprintf(`[{"type":"ADMIN_EMAIL_TEST_SEND","to":%q}]`, maskEmailForAdmin(to)))

	// fix M-11：SMTP 接受 ≠ 收件人收到。SMTP server 可能稍后异步 bounce / spam-filter。
	// 改成"已提交 SMTP，请查收"的提示，避免 admin 以为 SMTP 全链路 ok 但用户没收到。
	return c.JSON(fiber.Map{
		"success":      true,
		"message_code": "SUCCESS_EMAIL_TEST_SENT",
		"note":         "邮件已被 SMTP 服务器接受。请检查收件人邮箱（含垃圾箱）确认实际送达。",
	})
}

// ── 内部 helper ──

// formatBoolConfig 把 bool 转成 "true" / "false" 字符串落 SysConfig，
// 与 readBoolConfig 接受的 "true/1/yes/on" 互逆。
func formatBoolConfig(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

// persistAdminEmailConfigUpdates 把 plain key=value 加密后 upsert 进 SysConfig。
// 复用 BatchUpdateSysConfigs 的事务模式，但只针对 email-related 子集（避免误改无关 key）。
func persistAdminEmailConfigUpdates(updates map[string]string) error {
	if len(updates) == 0 {
		return nil
	}
	return database.DB.Transaction(func(tx *gorm.DB) error {
		for k, v := range updates {
			enc, err := utils.Encrypt(v)
			if err != nil {
				return fmt.Errorf("encrypt %s: %w", k, err)
			}
			var existing database.SysConfig
			res := tx.Where("key = ?", k).First(&existing)
			// fix HIGH SF-1：不能仅靠 RowsAffected 判定"是否存在"。
			// 只有 ErrRecordNotFound 才表示"该 key 不存在"；其它 DB 错误（lock timeout / disk full /
			// connection 断）也会让 RowsAffected = 0，旧代码 fallthrough 到 Create 会试图插入重复 key
			// → 要么静默成功导致重复行，要么 unique 冲突报错但掩盖了原因。
			if res.Error != nil && !errors.Is(res.Error, gorm.ErrRecordNotFound) {
				return fmt.Errorf("lookup %s: %w", k, res.Error)
			}
			if res.Error == nil {
				existing.Value = enc
				if err := tx.Save(&existing).Error; err != nil {
					return fmt.Errorf("save %s: %w", k, err)
				}
			} else {
				if err := tx.Create(&database.SysConfig{Key: k, Value: enc}).Error; err != nil {
					return fmt.Errorf("create %s: %w", k, err)
				}
			}
		}
		return nil
	})
}

// proxyRenderTestEmail 渲染一封测试邮件的简化结构（subject/title/body 由调用方传入）。
// 复用 proxy.RenderEmail 的 notification template，但绕过 email_enabled 检查
// （admin 测试时 master 可能还没开）。
func proxyRenderTestEmail(locale, to, adminUsername, subject, title, body string) (proxy.EmailMessage, error) {
	// 直接构造 EmailMessage（不经 RenderEmail，因 IsEmailEnabled 可能为 false）
	htmlBody := fmt.Sprintf(`<!DOCTYPE html><html><body style="font-family:sans-serif;padding:24px;">
<h2>%s</h2>
<p>%s</p>
<p style="color:#666;font-size:12px;">Test email from admin <strong>%s</strong> — DAOF-CPA SMTP verification</p>
</body></html>`, sanitizeAdminTestField(title), sanitizeAdminTestField(body), sanitizeAdminTestField(adminUsername))

	return proxy.EmailMessage{
		To:       to,
		Subject:  subject,
		TextBody: body + "\n\n---\nTest email from admin: " + adminUsername,
		HTMLBody: htmlBody,
	}, nil
}

// sanitizeAdminTestField 简单 HTML 转义防 admin username 含 < > 引入 XSS。
// 测试邮件只发给 admin 自己，但仍然防御。
func sanitizeAdminTestField(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		"\"", "&quot;",
		"'", "&#39;",
	)
	return r.Replace(s)
}

// maskEmailForAdmin 用于 admin 日志：a***@example.com 风格。
// 与 proxy.maskEmail 同语义，但 controller 包内独立实现避免 cross-package 依赖。
func maskEmailForAdmin(email string) string {
	email = strings.TrimSpace(email)
	if email == "" {
		return ""
	}
	at := strings.IndexByte(email, '@')
	if at < 0 {
		return "***"
	}
	local := email[:at]
	domain := email[at:]
	if local == "" {
		return "*" + domain
	}
	return string(local[0]) + "***" + domain
}

// 在编译时确认 errors.Is 不会被消除（防 dead-code elimination）。
var _ = errors.Is
