package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"daof-cpa/controller"
	"daof-cpa/database"
	"daof-cpa/middleware"
	"daof-cpa/proxy"
	"daof-cpa/utils"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/limiter"
	"github.com/gofiber/fiber/v2/middleware/logger"
)

func getCORSOrigins() string {
	if v := os.Getenv("DAOF_CORS_ALLOWED_ORIGINS"); v != "" {
		return v
	}
	return "http://localhost:3000, http://127.0.0.1:3000"
}

func main() {
	// 1. 孵化底层 AES 军事级加密解密模组
	// 必须位于 InitDB 之前：SeedSubscriptionDefaults 会写入加密的 SysConfig，
	// 否则 14 个默认配置会全部 ERR_CRYPTO_NOT_INIT 并丢失。
	utils.InitCrypto()

	// 2. 初始化包含 CGO 的原生高极速 SQLite
	database.InitDB()

	// 3. 从底层物理硬盘中抽取全部配置存入极速幻影内存 RAM (SyncCacheConfig)
	proxy.SyncCacheConfig()

	// 3.1 加载内容审核关键字词库（依赖 SysConfigCache 已就绪）
	proxy.LoadKeywordsFromConfig()

	// 3.2 启动审核审计异步 worker（独立队列，1 worker，SQLite 单写者约束）
	proxy.StartModerationAuditWorker()

	// 1.9 启动号池额度采集器（后台 goroutine，按 SysConfig 周期刷新所有 CPA 凭证额度）
	proxy.StartCreditsPool()

	// 1.95 启动订阅 cron（订阅到期回收 + SLA 超时退款 + 凭证清理 + 即将到期通知）
	proxy.StartSubscriptionCron()

	// 1.96 启动通知偏好缓存清理（每 5 分钟扫描过期项）
	proxy.StartPrefCacheJanitor()

	// 1.97 启动 SMS 限流 map sweeper（每 5 分钟清理过期条目，防内存无界增长）
	controller.StartSMSSweeper()

	// 1.98 启动 CLIProxyAPI usage queue 同步器（账号归因 / 毛利核算基础）
	controller.StartCLIProxyUsageSyncCron()

	// 2. 创建极速 Fiber 实例
	app := fiber.New(fiber.Config{
		DisableStartupMessage: false,
		AppName:               "DAOF-CPA Fast Engine v1.0",
		// 并发极大化配置
		Concurrency:             256 * 1024,
		BodyLimit:               32 * 1024 * 1024,
		EnableTrustedProxyCheck: true,
		// 仅信任本机回环作为可信代理。cloudflared 跑在本机，所有外部请求经 loopback 进入。
		// 收窄信任面可避免攻击者伪造 X-Forwarded-For: 127.0.0.1 让 c.IP() 返回 127.0.0.1。
		TrustedProxies: []string{"127.0.0.1", "::1"},
		ProxyHeader:    fiber.HeaderXForwardedFor,
	})

	// 3. 挂载安全响应头 / 跨域 / 日志中间件
	// 安全头：防 XSS 升级、防点击劫持、防 MIME 嗅探、限制 referrer 泄漏。
	// CSP 较宽松（允许 inline style 因 Tailwind / dynamic seed color），但禁 inline script 关掉 XSS 主路径。
	app.Use(func(c *fiber.Ctx) error {
		c.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains; preload")
		c.Set("X-Frame-Options", "DENY")
		c.Set("X-Content-Type-Options", "nosniff")
		c.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		c.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		c.Set("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self'; "+
				"style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data: https:; "+
				"font-src 'self' data:; "+
				"connect-src 'self'; "+
				"frame-ancestors 'none'; "+
				"base-uri 'self'; "+
				"form-action 'self'")
		return c.Next()
	})

	// AllowCredentials=true 允许浏览器跨域携带 HttpOnly cookie（admin token 走 cookie 必需）。
	// AllowOrigins 不能是 wildcard *，必须显式列出受信源。
	app.Use(cors.New(cors.Config{
		AllowOrigins:     getCORSOrigins(),
		AllowHeaders:     "Origin, Content-Type, Accept, Authorization",
		AllowMethods:     "GET, POST, HEAD, PUT, DELETE, PATCH",
		AllowCredentials: true,
	}))
	// fix C-M1：注入 CORS 白名单到 proxy 包供 WS Origin 校验复用（避免反向 import）
	proxy.GetCORSAllowedOriginsFn = getCORSOrigins
	app.Use(logger.New())

	// ===========================
	// 路由树 (Routing)
	// ===========================

	// LLM 代理入口先按 IP 做粗限流，挡随机 Bearer token 扫描造成的 DB 写入放大。
	// 阈值故意很高（~83 RPS/IP），避免误伤正常多 agent / CLI 编排；真实用户仍由下方 token 限流约束。
	llmIPCoarseLimiter := limiter.New(limiter.Config{
		Max:        5000,
		Expiration: 1 * time.Minute,
		KeyGenerator: func(c *fiber.Ctx) string {
			return "llm-ip-coarse:" + utils.RealClientIP(c)
		},
		LimitReached: func(c *fiber.Ctx) error {
			return c.Status(429).JSON(fiber.Map{
				"error": fiber.Map{
					"type":    "rate_limit_exceeded",
					"message": "Too many requests from this IP. Limit: 5000/min. Please slow down.",
				},
			})
		},
	})

	// fix CRITICAL C-B1（codex 第二十一轮）：LLM 代理入口（/v1/*）原本无任何限流，
	// 公开后任何用户的 token 泄漏 / 恶意脚本扫单 / 死循环调用都会瞬间打爆上游网关 + 拖垮 SQLite
	// 写锁 + 烧账单链路。按 token 优先 + IP 兜底建限流：每个 token 每分钟最多 600 次（~10 RPS），
	// 远超合理用量（用户人手敲不到 10 RPS），但能抑制恶意脚本。
	llmProxyLimiter := limiter.New(limiter.Config{
		Max:        600,
		Expiration: 1 * time.Minute,
		KeyGenerator: func(c *fiber.Ctx) string {
			// LLM 代理用 Authorization Bearer token；优先按 token 限流
			auth := c.Get("Authorization")
			if strings.HasPrefix(auth, "Bearer ") {
				return "llm-tok:" + auth[7:]
			}
			// 兜底按 IP（无 token 的请求会被 ChatCompletionProxyHandler 自己拒绝，但限流先于 handler）
			return "llm-ip:" + utils.RealClientIP(c)
		},
		LimitReached: func(c *fiber.Ctx) error {
			return c.Status(429).JSON(fiber.Map{
				"error": fiber.Map{
					"type":    "rate_limit_exceeded",
					"message": "Too many requests. Limit: 600/min per token. Please slow down.",
				},
			})
		},
	})
	// fix C-L1 (2026-05-19)：文本 LLM 接口 body 上限 4MB（覆盖 1M context 仍绰绰
	// 有余），图像/视频上传保留全局 32MB。两个 middleware 共享，差异只在 limit。
	textBodyLimit := middleware.BodyLimit(4 * 1024 * 1024) // 4MB for text LLM endpoints

	// OpenAI 格式代理统一网关
	app.All("/v1/chat/completions", llmIPCoarseLimiter, llmProxyLimiter, textBodyLimit, proxy.ChatCompletionProxyHandler)
	// OpenAI legacy completions（GPT-3.5 之前的 prompt-based API；CPA 仍支持，
	// DAOF 透传给 ChatCompletionProxyHandler 走同一计费链路）
	app.Post("/v1/completions", llmIPCoarseLimiter, llmProxyLimiter, textBodyLimit, proxy.ChatCompletionProxyHandler)
	// /v1/responses 多协议路由：
	//   - GET + WebSocket Upgrade 头 → Codex Responses WebSocket v2 桥接（P7）
	//   - 其他方法 / 普通 GET → OpenAI Agentic Responses API（SSE / 非流）
	// fiber 不支持单 path 多 method 同时挂不同 handler，所以分两条注册：
	app.Get("/v1/responses", llmIPCoarseLimiter, llmProxyLimiter, proxy.ResponsesWebsocketProxyHandler)
	app.Post("/v1/responses", llmIPCoarseLimiter, llmProxyLimiter, textBodyLimit, proxy.ChatCompletionProxyHandler)
	app.Post("/v1/responses/compact", llmIPCoarseLimiter, llmProxyLimiter, textBodyLimit, proxy.ChatCompletionProxyHandler)
	// 媒体生成接口保留 32MB 全局上限（图像 / 视频 multipart 可能大）
	app.Post("/v1/images/generations", llmIPCoarseLimiter, llmProxyLimiter, proxy.ImageGenerationProxyHandler)
	app.Post("/v1/images/edits", llmIPCoarseLimiter, llmProxyLimiter, proxy.ImageEditProxyHandler)
	app.Post("/v1/videos/generations", llmIPCoarseLimiter, llmProxyLimiter, proxy.VideoGenerationProxyHandler)
	app.Post("/v1/videos/edits", llmIPCoarseLimiter, llmProxyLimiter, proxy.VideoEditProxyHandler)
	app.Post("/v1/videos/extensions", llmIPCoarseLimiter, llmProxyLimiter, proxy.VideoExtensionProxyHandler)
	app.Get("/v1/videos/:request_id", llmIPCoarseLimiter, llmProxyLimiter, proxy.VideoRetrieveProxyHandler)
	// Codex CLI 默认 base_url 兼容（与 /v1/responses 调用同一 handler，让 user 不必
	// 改 chatgpt_base_url 配置）
	codexDirect := app.Group("/backend-api/codex", llmIPCoarseLimiter, llmProxyLimiter)
	// 同 /v1/responses 拆法：GET → WebSocket，POST → SSE
	codexDirect.Get("/responses", proxy.ResponsesWebsocketProxyHandler)
	codexDirect.Post("/responses", textBodyLimit, proxy.ChatCompletionProxyHandler)
	codexDirect.Post("/responses/compact", textBodyLimit, proxy.ChatCompletionProxyHandler)
	// Google Gemini 兼容 API 代理（P6）：generateContent / streamGenerateContent /
	// countTokens（Imagen 内部走 :predict，CPA 自动翻译为 Gemini 格式）+ listModels
	// (GET /v1beta/models 无 action 透传 CPA 模型列表)。客户端用 Google AI SDK /
	// @google/generative-ai 直接调 DAOF；admin 必须在 ChannelModel.AllowedEndpoints
	// 中加 /v1beta/models 启用对应 model。
	// S7-1：把 catch-all `*` 改为单段 `:modelAction` 收紧攻击面。
	// Gemini API 标准路径 `/v1beta/models/<model>:<method>` 模型名不含 `/`，
	// `:modelAction` 单段 param 已足够，且阻止 `/v1beta/models/foo/bar/...` 多段
	// 注入。listModels 走第一条无后缀路由。
	app.Get("/v1beta/models", llmIPCoarseLimiter, llmProxyLimiter, proxy.GeminiNativeProxyHandler)
	app.Post("/v1beta/models/:modelAction", llmIPCoarseLimiter, llmProxyLimiter, textBodyLimit, proxy.GeminiNativeProxyHandler)
	app.Get("/v1beta/models/:modelAction", llmIPCoarseLimiter, llmProxyLimiter, proxy.GeminiNativeProxyHandler)
	// Anthropic 原生 Messages API（Claude Code / Anthropic SDK 默认调用此路径）
	app.All("/v1/messages", llmIPCoarseLimiter, llmProxyLimiter, textBodyLimit, proxy.ChatCompletionProxyHandler)
	// 容错：客户端 base URL 误填为 ".../v1" 时 SDK 会拼出 /v1/v1/messages，仍正确路由
	app.All("/v1/v1/messages", llmIPCoarseLimiter, llmProxyLimiter, textBodyLimit, proxy.ChatCompletionProxyHandler)
	app.All("/v1/messages/count_tokens", llmIPCoarseLimiter, llmProxyLimiter, textBodyLimit, proxy.ChatCompletionProxyHandler)
	app.All("/v1/v1/messages/count_tokens", llmIPCoarseLimiter, llmProxyLimiter, textBodyLimit, proxy.ChatCompletionProxyHandler)
	app.Get("/v1/models", controller.GetPublicModels)
	app.Get("/v1/v1/models", controller.GetPublicModels)

	// 前端控制台 API 占位
	api := app.Group("/api")
	api.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "ok", "message": "DAOF-CPA holds strong."})
	})

	// ==========================================
	// 绝对公开接口 (系统探测与通行核验)
	// SetupGuard 拒绝所有公共流量直到管理员把默认 root/123456 改掉。
	// 仅 /api/root/* 走 LanGuard 单独处理（admin 引导路径必须可达）。
	// ==========================================
	api.Get("/public-config", middleware.SetupGuard, controller.GetPublicConfig)
	api.Get("/auth/github/prepare", middleware.SetupGuard, controller.PrepareOAuthState)

	// fix Major M3（claude security 第十五轮）：OAuth/SMS 注册路径加 IP 级 rate limiter。
	// 对照 godLoginLimiter 5/5min 已有；此前这三条公开 POST 完全没限流，攻击者可以：
	//   - 用预生成的 GitHub code 批量探测 GithubCallback
	//   - 拿合法 tmp_token 批量回放 CompleteRisk/Profile（registerMu.Lock 让 goroutine 排队，做到 DoS）
	//   - SendSMS 已有应用层 5/小时 限制，外加 IP 级再加一道防 IP 切换
	authLimiter := limiter.New(limiter.Config{
		Max:        20, // 单 IP 每分钟 20 次（高于 godLogin 因 OAuth 第三方回调可能快速重试）
		Expiration: 1 * time.Minute,
		KeyGenerator: func(c *fiber.Ctx) string {
			return "auth:" + utils.RealClientIP(c)
		},
		LimitReached: func(c *fiber.Ctx) error {
			return c.Status(429).JSON(fiber.Map{
				"success":      false,
				"message":      "请求过于频繁，请稍后再试",
				"message_code": "ERR_TOO_MANY_REQUESTS",
			})
		},
	})

	api.Post("/auth/github", middleware.SetupGuard, authLimiter, controller.GithubCallback)
	api.Post("/auth/send-sms", middleware.SetupGuard, authLimiter, controller.SendSMS)
	api.Post("/auth/complete-risk", middleware.SetupGuard, authLimiter, controller.CompleteRisk)
	api.Post("/auth/complete-profile", middleware.SetupGuard, authLimiter, controller.CompleteProfile)
	// Sprint5-M1 浏览器登出：在 Session 表标记 revoked，下次 UserGuard 命中即 401
	// 登出对 banned 用户也必须可达——否则用户根本无法离开账号。
	api.Post("/auth/logout", middleware.UserGuardAllowBanned, controller.AuthLogout)
	api.Get("/models", middleware.SetupGuard, controller.GetPublicModels)
	api.Get("/pricing", middleware.SetupGuard, controller.GetPublicPricing)
	api.Get("/billing/rules", middleware.SetupGuard, controller.GetPublicBillingRules)
	api.Get("/billing/rules/history", middleware.SetupGuard, controller.GetPublicBillingRuleHistory)

	// ==========================================
	// 用户业务私域接口
	// ==========================================
	// 封禁策略：banned 用户只在"业务花钱动作"上被拒（购买套餐 / 创建充值订单 /
	// 调上游 LLM）；查看自己数据 + 管理自己资料/偏好/token 全部放行，让 banned
	// 用户能正常浏览、查账、提工单申诉。
	api.Get("/user/me", middleware.UserGuardAllowBanned, controller.GetSelfData)
	api.Get("/logs", middleware.UserGuardAllowBanned, controller.GetLogs)
	api.Get("/logs/stats", middleware.UserGuardAllowBanned, controller.GetStats)

	// i18n API (Public, client needs this to load translations)
	api.Get("/i18n/locales", controller.GetLocalesList)
	api.Get("/i18n/locales/:lang", controller.GetLocaleContent)

	// fix MAJOR Phase 4-codex（第二十四轮）：UserGuard 接受 admin cookie，所有写路由都必须挂 CSRFGuard，
	// 否则 admin 浏览器 cookie 可被跨源页面诱导写令牌/扣费/退款等。Bearer 鉴权（SDK/CI）免校验。
	// Token CRUD 对 banned 也放行——管理自己凭证是基础权，且 token 真要调上游时被 LLM 路径拒。
	api.Get("/tokens", middleware.UserGuardAllowBanned, controller.GetTokens)
	api.Post("/tokens", middleware.UserGuardAllowBanned, middleware.CSRFGuard, controller.CreateToken)
	api.Put("/tokens/:id", middleware.UserGuardAllowBanned, middleware.CSRFGuard, controller.UpdateTokenSettings)
	api.Delete("/tokens/:id", middleware.UserGuardAllowBanned, middleware.CSRFGuard, controller.DeleteToken)

	// ==========================================
	// Localhost 暗网级管理防线 (LanGuard强制封锁外网)
	// ==========================================
	// 限流器：godLoginLimiter 用于 /api/root/god-login，按真实客户端 IP（CF-Connecting-IP 或 c.IP()）
	// 限制单 IP 5 分钟内最多 5 次失败 + 成功合计尝试。防止暴力破解管理员密码。
	godLoginLimiter := limiter.New(limiter.Config{
		Max:        5,
		Expiration: 5 * time.Minute,
		KeyGenerator: func(c *fiber.Ctx) string {
			return "godlogin:" + utils.RealClientIP(c)
		},
		LimitReached: func(c *fiber.Ctx) error {
			return c.Status(429).JSON(fiber.Map{
				"success":      false,
				"message":      "登录尝试过于频繁，请 5 分钟后再试",
				"message_code": "ERR_RATE_LIMIT",
			})
		},
	})

	// 基础管理员探针接口（无需Token鉴权，但强制过网络物理墙）
	rootApi := app.Group("/api/root", middleware.LanGuard)
	rootApi.Post("/check-sys", controller.CheckSys)
	rootApi.Post("/god-login", godLoginLimiter, controller.GodLogin)
	rootApi.Post("/setup", godLoginLimiter, controller.GodSetup)
	rootApi.Post("/logout", controller.AdminLogout)

	// Phase G-2.2 邮箱+密码登录限流：与 godLoginLimiter 同强度（5次/5min/IP）
	// 不挂 LanGuard：普通用户从公网登录，必须开放公开
	emailLoginLimiter := limiter.New(limiter.Config{
		Max:        5,
		Expiration: 5 * time.Minute,
		KeyGenerator: func(c *fiber.Ctx) string {
			return "email-login:" + utils.RealClientIP(c)
		},
		LimitReached: func(c *fiber.Ctx) error {
			return c.Status(429).JSON(fiber.Map{
				"success":      false,
				"message_code": "ERR_RATE_LIMIT",
			})
		},
	})
	api.Post("/auth/email/login", emailLoginLimiter, controller.EmailLogin)

	// Phase G-2.3 邮箱+密码注册限流：稍宽（per-IP 10次/hour）—— 注册较少而 login 可能因输错频繁
	emailSignupLimiter := limiter.New(limiter.Config{
		Max:        10,
		Expiration: 1 * time.Hour,
		KeyGenerator: func(c *fiber.Ctx) string {
			return "email-signup:" + utils.RealClientIP(c)
		},
		LimitReached: func(c *fiber.Ctx) error {
			return c.Status(429).JSON(fiber.Map{
				"success":      false,
				"message_code": "ERR_RATE_LIMIT",
			})
		},
	})
	api.Post("/auth/email/signup", emailSignupLimiter, controller.EmailSignup)

	// Phase G-2.4 忘记密码限流：严格（per-IP 5次/hour）—— 防滥发重置邮件骚扰
	emailForgotPwdLimiter := limiter.New(limiter.Config{
		Max:        5,
		Expiration: 1 * time.Hour,
		KeyGenerator: func(c *fiber.Ctx) string {
			return "email-forgot-pwd:" + utils.RealClientIP(c)
		},
		LimitReached: func(c *fiber.Ctx) error {
			return c.Status(429).JSON(fiber.Map{
				"success":      false,
				"message_code": "ERR_RATE_LIMIT",
			})
		},
	})
	api.Post("/auth/email/forgot-password", emailForgotPwdLimiter, controller.ForgotPassword)
	// reset-password 限流：稍宽（per-IP 10次/hour）—— token 自身已是 256bit 随机，主要防恶意刷请求
	emailResetPwdLimiter := limiter.New(limiter.Config{
		Max:        10,
		Expiration: 1 * time.Hour,
		KeyGenerator: func(c *fiber.Ctx) string {
			return "email-reset-pwd:" + utils.RealClientIP(c)
		},
		LimitReached: func(c *fiber.Ctx) error {
			return c.Status(429).JSON(fiber.Map{
				"success":      false,
				"message_code": "ERR_RATE_LIMIT",
			})
		},
	})
	api.Post("/auth/email/reset-password", emailResetPwdLimiter, controller.ResetPassword)

	// Phase G-2.5 OAuth 用户凭 set_password token 完成首次设密码限流（同 reset：per-IP 10/hour）
	emailSetPwdLimiter := limiter.New(limiter.Config{
		Max:        10,
		Expiration: 1 * time.Hour,
		KeyGenerator: func(c *fiber.Ctx) string {
			return "email-set-pwd:" + utils.RealClientIP(c)
		},
		LimitReached: func(c *fiber.Ctx) error {
			return c.Status(429).JSON(fiber.Map{
				"success":      false,
				"message_code": "ERR_RATE_LIMIT",
			})
		},
	})
	api.Post("/auth/email/set-password", emailSetPwdLimiter, controller.SetPassword)

	// Admin 高权限隔离区 (换用 LanGuard + AdminGuard)
	adminApi := api.Group("/admin", middleware.LanGuard, middleware.AdminGuard)
	adminApi.Get("/config", controller.GetSysConfigs)
	adminApi.Post("/config", controller.BatchUpdateSysConfigs)
	// Phase G-1.6 邮件配置专用 API（password 不回显）
	adminApi.Get("/email/config", controller.GetAdminEmailConfig)
	adminApi.Put("/email/config", middleware.CSRFGuard, controller.UpdateAdminEmailConfig)
	adminApi.Post("/email/test-send", middleware.CSRFGuard, controller.SendAdminEmailTest)
	adminApi.Post("/moderation/test", controller.TestModerationConfig)
	adminApi.Post("/moderation/evaluate", controller.EvaluateModerationDryRun)
	adminApi.Post("/moderation/keywords/generate", controller.GenerateModerationKeywords)
	adminApi.Get("/moderation/events", controller.ListModerationRiskEvents)

	adminApi.Get("/users", controller.GetUsers)
	adminApi.Get("/users-usage", controller.GetUsersUsage)
	adminApi.Get("/users-usage/timeseries", controller.GetUsersUsageTimeseries)
	adminApi.Get("/users-usage/events", controller.GetUsersUsageEvents)
	adminApi.Get("/upstream-account-cost-presets", controller.ListUpstreamAccountCostPresets)
	adminApi.Get("/upstream-accounts", controller.ListUpstreamAccountCosts)
	adminApi.Get("/upstream-accounts/candidates", controller.ListUpstreamAccountCandidates)
	adminApi.Get("/upstream-accounts/stale", controller.ListStaleUpstreamAccountCosts)
	adminApi.Post("/upstream-accounts", controller.CreateUpstreamAccountCost)
	adminApi.Post("/upstream-accounts/bulk", controller.BulkUpsertUpstreamAccountCosts)
	adminApi.Put("/upstream-accounts/:id", controller.UpdateUpstreamAccountCost)
	adminApi.Delete("/upstream-accounts/:id", controller.DeleteUpstreamAccountCost)
	adminApi.Get("/upstream-margin", controller.GetUpstreamMarginReport)
	adminApi.Post("/billing/rules", controller.UpdateBillingRules)
	adminApi.Post("/billing/rules/revisions/:id/cancel", controller.CancelBillingRuleRevision)
	adminApi.Post("/users/bulk-quota/preview", controller.BulkAdjustQuotaPreview)
	adminApi.Post("/users/bulk-quota", controller.BulkAdjustQuota)
	adminApi.Post("/users/bulk-delete", controller.BulkDeleteUsers)
	adminApi.Post("/users/:id/offline-topup", controller.AdminCreateOfflineTopup)
	adminApi.Put("/users/:id", controller.UpdateUser)
	adminApi.Post("/users/:id/purge", controller.AdminPurgeUser)
	adminApi.Delete("/users/:id", controller.DeleteUser)
	adminApi.Get("/users/:id/operations", controller.GetUserOperations)

	adminApi.Put("/credentials", controller.UpdateAdminCredentials)

	adminApi.Get("/channels", controller.GetAdminChannels)
	adminApi.Post("/channels", controller.CreateChannel)
	adminApi.Put("/channels/:id", controller.UpdateChannel)
	adminApi.Put("/channels/:id/reset-key", controller.ResetChannelKey)
	adminApi.Delete("/channels/:id", controller.DeleteChannel)

	// Sprint5-M2 渠道熔断监控（admin 实时查看 closed/open/half-open + 强制重置）
	adminApi.Get("/channels/circuits", controller.AdminListChannelCircuits)
	adminApi.Post("/channels/:id/circuit-reset", controller.AdminForceResetChannelCircuit)

	adminApi.Get("/channels/:channelId/models", controller.GetModelsByChannel)
	adminApi.Get("/channels/:channelId/upstream-models", controller.FetchUpstreamModels)
	adminApi.Post("/channels/:channelId/models", controller.AddChannelModel)
	adminApi.Post("/channels/:channelId/models/batch", controller.AddChannelModelsBatch)
	adminApi.Put("/channel-models/:id", controller.UpdateChannelModel)
	adminApi.Delete("/channel-models/:id", controller.RemoveChannelModel)

	adminApi.Post("/i18n/:lang", controller.UploadLocale)
	adminApi.Delete("/i18n/:lang", controller.DeleteLocale)

	// CLIProxyAPI 安全代理（Management Key 仅在服务端持有，绝不下发前端）
	adminApi.Get("/cliproxy/usage", controller.ProxyCLIProxyUsage)
	adminApi.Post("/cliproxy/usage/sync", controller.SyncCLIProxyUsage)

	// 号池额度监控（admin 全量明细 + 立即刷新）
	adminApi.Get("/credits-pool", controller.GetAdminCreditsPool)
	adminApi.Post("/credits-pool/refresh", controller.RefreshAdminCreditsPool)

	// ==========================================
	// 套餐订阅系统 - Admin
	// ==========================================
	// 配额计划库
	adminApi.Get("/quota-plans", controller.ListQuotaPlans)
	adminApi.Get("/quota-plans/:id", controller.GetQuotaPlan)
	adminApi.Post("/quota-plans", controller.CreateQuotaPlan)
	adminApi.Post("/quota-plans/reorder", controller.ReorderQuotaPlans)
	adminApi.Put("/quota-plans/:id", controller.UpdateQuotaPlan)
	adminApi.Delete("/quota-plans/:id", controller.DeleteQuotaPlan)

	// 销售套餐
	adminApi.Get("/packages", controller.ListPackagesAdmin)
	adminApi.Get("/packages/:id", controller.GetPackageAdmin)
	adminApi.Post("/packages", controller.CreatePackage)
	adminApi.Post("/packages/reorder", controller.ReorderPackages)
	adminApi.Put("/packages/:id", controller.UpdatePackage)
	adminApi.Delete("/packages/:id", controller.DeletePackage)

	// 订阅总览
	adminApi.Get("/subscriptions", controller.AdminListSubscriptions)
	adminApi.Post("/subscriptions/reset-usage", controller.AdminResetSubscriptionUsage)
	// 注：订阅退款 admin endpoint 在 refundLimiter 声明后（约 line 408 附近）注册

	// 优惠券系统 admin（模板 CRUD + 发券 + 撤销 + 查用户券）
	adminApi.Get("/coupon-templates", controller.AdminListCouponTemplates)
	adminApi.Post("/coupon-templates", controller.AdminCreateCouponTemplate)
	adminApi.Put("/coupon-templates/:id", controller.AdminUpdateCouponTemplate)
	adminApi.Delete("/coupon-templates/:id", controller.AdminDeleteCouponTemplate)
	adminApi.Post("/coupons/grant", controller.AdminGrantCoupon)
	adminApi.Post("/users/bulk-grant-coupon", controller.AdminBulkGrantCoupon)
	adminApi.Delete("/coupons/:id", controller.AdminRevokeCoupon)
	adminApi.Get("/users/:userId/coupons", controller.AdminListUserCoupons)

	// ==========================================
	// 套餐订阅系统 - 用户
	// ==========================================
	// /packages 公开（用户购买页需要看套餐列表）
	api.Get("/packages", controller.ListPublicPackages)
	// 购买 / 取消是金融操作，限制每用户每分钟 6 次（防误点扣费 / 状态机滥用）
	purchaseLimiter := limiter.New(limiter.Config{
		Max:        6,
		Expiration: 1 * time.Minute,
		KeyGenerator: func(c *fiber.Ctx) string {
			if u, ok := c.Locals("user").(*database.User); ok && u != nil {
				return fmt.Sprintf("buy:%d", u.ID)
			}
			return "buy-ip:" + utils.RealClientIP(c)
		},
		LimitReached: func(c *fiber.Ctx) error {
			return c.Status(429).JSON(fiber.Map{"success": false, "message_code": "ERR_TOO_MANY_REQUESTS"})
		},
	})
	// fix MAJOR Phase 4-codex（第二十四轮）：购买/取消订阅是 cookie 写路径，必须挂 CSRFGuard
	// 购买套餐 = 业务花钱动作 → 严拒 banned。
	api.Post("/subscriptions/purchase", middleware.UserGuard, middleware.CSRFGuard, purchaseLimiter, controller.PurchasePackage)
	api.Get("/subscriptions/mine", middleware.UserGuardAllowBanned, controller.MySubscriptions)
	// 取消订阅对 banned 放行——停掉自己的业务关系是用户基本权利。
	api.Post("/subscriptions/:id/cancel", middleware.UserGuardAllowBanned, middleware.CSRFGuard, purchaseLimiter, controller.CancelSubscription)

	// 优惠券：用户查询自己的券
	api.Get("/coupons/my", middleware.UserGuardAllowBanned, controller.MyCoupons)

	// 账单流水（统一事实表，覆盖充值/购买/退款/API 扣费等所有金钱进出）
	// 账单查询/汇总/导出走 AllowBanned：封禁用户保留"查账"权（合规可追溯）。
	api.Get("/billing/mine", middleware.UserGuardAllowBanned, controller.MyBillingEntries)
	api.Get("/billing/mine/summary", middleware.UserGuardAllowBanned, controller.MyBillingSummary)
	api.Get("/billing/mine/export", middleware.UserGuardAllowBanned, controller.MyBillingExport)

	// 站内通知——查看 + 标已读对 banned 都放行（管理自己消息流，无业务动作）。
	api.Get("/notifications", middleware.UserGuardAllowBanned, controller.MyNotifications)
	api.Post("/notifications/:id/read", middleware.UserGuardAllowBanned, middleware.CSRFGuard, controller.MarkNotificationRead)
	api.Post("/notifications/read-all", middleware.UserGuardAllowBanned, middleware.CSRFGuard, controller.MarkAllNotificationsRead)

	// 用户通知偏好——查看 + 修改对 banned 放行（用户自身资料）。
	api.Get("/notifications/preference", middleware.UserGuardAllowBanned, controller.GetMyNotificationPreference)
	api.Put("/notifications/preference", middleware.UserGuardAllowBanned, middleware.CSRFGuard, controller.UpdateMyNotificationPreference)

	// 用户余额消费控制（三段消费模型第 3 段）——查看 + 修改对 banned 放行（自身偏好）。
	api.Get("/balance-consume/preference", middleware.UserGuardAllowBanned, controller.GetMyBalanceConsumePreference)
	api.Put("/balance-consume/preference", middleware.UserGuardAllowBanned, middleware.CSRFGuard, controller.UpdateMyBalanceConsumePreference)

	// 用户邮箱绑定（Phase G-1.5）——绑定/验证/重发/解绑/查询
	// 所有写动作挂 CSRFGuard；banned 用户不允许绑/改邮箱
	api.Get("/user/email", middleware.UserGuardAllowBanned, controller.GetMyEmailStatus)
	api.Post("/user/email/bind", middleware.UserGuard, middleware.CSRFGuard, controller.BindEmail)
	api.Post("/user/email/verify", middleware.UserGuard, middleware.CSRFGuard, controller.VerifyEmail)
	api.Post("/user/email/resend-verification", middleware.UserGuard, middleware.CSRFGuard, controller.ResendVerificationEmail)
	api.Delete("/user/email", middleware.UserGuard, middleware.CSRFGuard, controller.UnbindEmail)
	// Phase G-2.5：OAuth 用户申请发"设置密码"邮件（要求 logged-in 且 PasswordHash 为空）
	api.Post("/user/email/request-set-password",
		middleware.UserGuard, middleware.CSRFGuard, controller.RequestSetPassword)
	// Phase G-2.1：用户级开关（控制是否允许邮箱+密码登录；admin master 是另一道闸）
	api.Put("/user/email-login-enabled", middleware.UserGuard, middleware.CSRFGuard, controller.PutMyEmailLoginEnabled)

	// 工单系统（用户↔admin 多轮会话；关闭后 15 天 cron 清除）
	//
	// 限流策略修订（fix CRITICAL：双角色路由没 UserGuard 时 c.Locals("user") 永远是 nil
	// 导致 KeyGenerator 退化为 IP-based，NAT 后多用户共享同一桶 + 攻击者轮换 IP 即可绕过）：
	//   - 写动作（create/post/close）：从 token 直接抽 user，每用户每小时 30 次
	//   - 读动作（GET :id, mark-read）：每 IP 每分钟 60 次（防未鉴权 DB DoS + ID 枚举）
	resolveUserKey := func(c *fiber.Ctx) string {
		// 1) 已经被 UserGuard 填进 Locals 的（如 /tickets/mine）
		if u, ok := c.Locals("user").(*database.User); ok && u != nil {
			return fmt.Sprintf("u:%d", u.ID)
		}
		// 2) 双角色路径：从 Bearer/cookie 自查
		if tk := middleware.ExtractAdminToken(c); tk != "" {
			if u := proxy.LookupUserByToken(tk); u != nil {
				return fmt.Sprintf("u:%d", u.ID)
			}
		}
		auth := c.Get("Authorization")
		if strings.HasPrefix(strings.ToLower(auth), "bearer ") && len(auth) > 7 {
			tk := strings.TrimSpace(auth[7:])
			if tk != "" {
				if u := proxy.LookupUserByToken(tk); u != nil {
					return fmt.Sprintf("u:%d", u.ID)
				}
			}
		}
		// 3) 未鉴权落到 IP（仍是上限的兜底防滥用）
		return "ip:" + utils.RealClientIP(c)
	}
	ticketLimiter := limiter.New(limiter.Config{
		Max:          30,
		Expiration:   1 * time.Hour,
		KeyGenerator: func(c *fiber.Ctx) string { return "tk-w:" + resolveUserKey(c) },
		LimitReached: func(c *fiber.Ctx) error {
			return c.Status(429).JSON(fiber.Map{"success": false, "message_code": "ERR_TOO_MANY_MESSAGES"})
		},
	})
	// 读限流：未鉴权时按 IP 每分钟 60 次（GET :id 与 mark-read 都接，防 ID 枚举 DoS）
	ticketReadLimiter := limiter.New(limiter.Config{
		Max:          60,
		Expiration:   1 * time.Minute,
		KeyGenerator: func(c *fiber.Ctx) string { return "tk-r:" + resolveUserKey(c) },
		LimitReached: func(c *fiber.Ctx) error {
			return c.Status(429).JSON(fiber.Map{"success": false, "message_code": "ERR_TOO_MANY_REQUESTS"})
		},
	})
	// 工单全套用 AllowBanned 变体：封禁就是用工单申诉的唯一通道，必须可达。
	api.Post("/tickets", middleware.UserGuardAllowBanned, middleware.CSRFGuard, ticketLimiter, controller.CreateTicket)
	api.Get("/tickets/mine", middleware.UserGuardAllowBanned, controller.MyTickets)
	api.Get("/tickets/:id", ticketReadLimiter, controller.GetTicket) // 双角色（GET 无 CSRF 风险）
	// fix CRITICAL C22-A1（codex 第二十二轮）：双角色 POST 路由原本无任何 CSRF 防护，
	// admin cookie 可被跨源页面诱导写入工单。挂 CSRFGuard：Bearer 请求免校验（SDK/CI），
	// cookie 写请求强制同源。
	api.Post("/tickets/:id/messages", middleware.CSRFGuard, ticketLimiter, controller.PostTicketMessage)
	api.Post("/tickets/:id/close", middleware.CSRFGuard, ticketLimiter, controller.CloseTicket)
	api.Post("/tickets/:id/read", middleware.CSRFGuard, ticketReadLimiter, controller.MarkTicketRead)

	// Admin 工单列表（admin 视角的总览，单条工单走 /api/tickets/:id 双角色路径）
	adminApi.Get("/tickets", controller.AdminListTickets)

	// ==========================================
	// 余额充值（易付通 V2 RSA 协议）
	// ==========================================
	// 用户接口：查询 banned 放行（让用户看到充值页面 UI），创建订单严拒 banned。
	api.Get("/topup/options", middleware.UserGuardAllowBanned, controller.GetTopupOptions)
	// fix HIGH H19-1（codex 第十九轮）：充值下单与套餐购买同等敏感（生成订单 + 调易付通），
	// 复用 purchaseLimiter（每用户每分钟 6 次）防止失误点击/恶意刷单/误扣攻击。
	// 创建充值订单 = 业务花钱动作 → 严拒 banned。
	api.Post("/topup/create", middleware.UserGuard, middleware.CSRFGuard, purchaseLimiter, controller.CreateTopup)
	api.Get("/topup/mine", middleware.UserGuardAllowBanned, controller.MyTopupOrders)

	// 公开回调：易付通服务器从公网 GET 过来。**绝对不能加任何 guard**——
	// 否则用户付了钱但回调被拦，导致本地加额度失败、易付通持续重试堆积。
	// 安全靠 RSA 验签 + 金额双校验保证。
	//
	// 限流：单 IP 每分钟最多 60 次（合法重试节奏远低于此；超出几乎肯定是恶意枚举/DoS）
	yifutNotifyLimiter := limiter.New(limiter.Config{
		Max:        60,
		Expiration: 1 * time.Minute,
		KeyGenerator: func(c *fiber.Ctx) string {
			return "yifut-notify:" + utils.RealClientIP(c)
		},
		LimitReached: func(c *fiber.Ctx) error {
			return c.Status(429).SendString("rate_limit")
		},
	})
	yifutReturnLimiter := limiter.New(limiter.Config{
		Max:        30,
		Expiration: 1 * time.Minute,
		KeyGenerator: func(c *fiber.Ctx) string {
			return "yifut-return:" + utils.RealClientIP(c)
		},
		LimitReached: func(c *fiber.Ctx) error {
			return c.Status(429).SendString("rate_limit")
		},
	})
	api.Get("/payment/notify/yifut", yifutNotifyLimiter, controller.YifutNotify)
	api.Get("/payment/return/yifut", yifutReturnLimiter, controller.YifutReturn)

	// Admin 系统通知群发
	adminApi.Post("/notifications/broadcasts", controller.AdminCreateBroadcast)
	adminApi.Get("/notifications/broadcasts", controller.AdminListBroadcasts)
	adminApi.Get("/notifications/broadcasts/:id", controller.AdminGetBroadcast)
	adminApi.Post("/notifications/broadcasts/:id/revoke", controller.AdminRevokeBroadcast)
	adminApi.Get("/notifications/preview-targets", controller.AdminPreviewBroadcastTargets)

	// Admin 充值订单管理
	// fix Sec-H4 + Codex-MINOR：退款限流——admin session 被劫持或误操作脚本扫单可能批量打到上游网关。
	// 状态机原子性已防双花，但仍需限制对易付通的请求速率防 DoS。
	// AdminGuard 不会写 c.Locals("user")（只验证 admin cookie 不注入 user 实体），
	// 所以按 admin token 直接抽 user 做 key（多 admin 时各自独立计数；NAT 后多 admin 也准确）。
	refundLimiter := limiter.New(limiter.Config{
		Max:        10,
		Expiration: 1 * time.Minute,
		KeyGenerator: func(c *fiber.Ctx) string {
			if tk := middleware.ExtractAdminToken(c); tk != "" {
				if u := proxy.LookupUserByToken(tk); u != nil {
					return fmt.Sprintf("refund-admin:%d", u.ID)
				}
			}
			return "refund-ip:" + utils.RealClientIP(c)
		},
		LimitReached: func(c *fiber.Ctx) error {
			return c.Status(429).JSON(fiber.Map{"success": false, "message_code": "ERR_TOO_MANY_REQUESTS"})
		},
	})
	adminApi.Get("/topup/orders", controller.AdminListTopupOrders)
	adminApi.Post("/topup/orders/:id/mark-paid", refundLimiter, controller.AdminMarkTopupPaid)
	adminApi.Post("/topup/orders/:id/refund", refundLimiter, controller.AdminRefundTopup)

	// admin 账单：按用户查任意账单（嵌入 UserManagement 详情面板）
	adminApi.Get("/billing/users/:id", controller.AdminListUserBilling)
	adminApi.Get("/billing/users/:id/summary", controller.AdminUserBillingSummary)
	adminApi.Get("/billing/users/:id/export", controller.AdminUserBillingExport)
	// admin 对账（Sprint5-M8）：pending_reconcile / upstream_unmetered → 已对账
	adminApi.Post("/billing/:id/reconcile", controller.AdminReconcileBillingEntry)
	// 订阅退款（用户走客服工单提交申请；admin 协商金额后手动触发本接口）
	adminApi.Post("/subscriptions/:id/refund", refundLimiter, controller.AdminRefundSubscription)
	// 收回管理员赠送的订阅 / 增量包：只撤销权益，不退款、不改变 user.quota
	adminApi.Post("/subscriptions/:id/revoke-grant", refundLimiter, controller.AdminRevokeGrantedSubscription)
	// 赠送订阅 / 增量包：admin 给目标用户免费开通指定套餐（IsGranted=true → refund 路径拒绝）
	// 复用 refundLimiter（金融敏感操作；按 admin token 限速 10 次/分钟，与退款同等约束）
	// fix MINOR（codex 第二十轮）：原注释写"6 次/分钟"与实际 Max:10 不一致，已统一描述。
	adminApi.Post("/subscriptions/grant", refundLimiter, controller.AdminGrantSubscription)

	// ==========================================
	// 业务接口防爆盾
	// (在 Root 创世完成前，禁止散客访问)
	// ==========================================
	// ==========================================
	// 前后统一同源接管 (Static & SPA Fallback)
	// ==========================================
	app.Use("/api", func(c *fiber.Ctx) error {
		return c.Status(404).JSON(fiber.Map{
			"success":      false,
			"message":      "API endpoint not found",
			"message_code": "ERR_API_NOT_FOUND",
		})
	})
	app.Use("/v1", func(c *fiber.Ctx) error {
		return c.Status(404).JSON(fiber.Map{
			"error": fiber.Map{
				"message": "API endpoint not found",
				"type":    "invalid_request_error",
			},
		})
	})
	// fix Minor（codex 第六轮）：拒绝任何含 /. 的路径访问，防止误打包 .env / .git / source map
	// 等 dotfiles 走静态服务暴露。这一道在 Static 之前生效。
	app.Use("/", func(c *fiber.Ctx) error {
		p := c.Path()
		// 含 /. 的任意段都拒绝（覆盖 /.env、/api/.well-known 等都不该走静态）
		// API 路由前缀 (/api, /v1) 在上面已有显式 404，不会落到 SPA HTML。
		if strings.Contains(p, "/.") {
			return c.Status(404).SendString("not found")
		}
		return c.Next()
	})
	app.Static("/", "./ui/dist")

	app.Get("/*", func(c *fiber.Ctx) error {
		c.Set("Cache-Control", "no-store, no-cache, must-revalidate, proxy-revalidate")
		c.Set("Pragma", "no-cache")
		c.Set("Expires", "0")
		return c.SendFile("./ui/dist/index.html")
	})

	// 4. 注册 graceful shutdown：SIGINT/SIGTERM 触发后停 dispatch 队列 + Shutdown fiber
	// 让 in-flight 通知写完、in-flight 请求收尾，避免容器替换时丢数据。
	shutdownCh := make(chan os.Signal, 1)
	signal.Notify(shutdownCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-shutdownCh
		log.Println("[SHUTDOWN] received signal, draining...")
		// fix Major（codex 第七轮 + 自审第九轮 + M23-A5 第二十三轮）：完整 graceful shutdown 顺序：
		//   1) 先关 HTTP 入口：拒绝新连接 + 等已建立的 in-flight 请求收尾（最长 5s）
		//   2) 停所有后台 goroutine（cron / sweeper / pool / janitor / moderation_audit）
		//   3) 最后关 dispatch 队列让在飞的通知任务排空
		// 顺序保证：cron / sweeper 停了之后才会有最后一波 Dispatch（来自结尾收尾的 controller），
		// 它们会被有界队列吞下，然后 StopDispatchPool 排干净。
		// M23-A5：StopCreditsPool/StopModerationAuditWorker 现在 wait 直到 goroutine 真正退出。
		_ = app.ShutdownWithTimeout(5 * time.Second)
		proxy.StopSubscriptionCron()
		proxy.StopCreditsPool()
		proxy.StopPrefCacheJanitor()
		controller.StopSMSSweeper()
		controller.StopCLIProxyUsageSyncCron()
		proxy.StopModerationAuditWorker() // 关 moderation 审计队列 + 等 drain（防丢审计事件）
		proxy.StopDispatchPool()
		log.Println("[SHUTDOWN] complete")
	}()

	// 5. 起飞！
	log.Println("DAOF-CPA Engine Starting on :3000...")
	if err := app.Listen(":3000"); err != nil {
		log.Fatal(err)
	}
}
