package middleware

// body_limit.go
//
// fix C-L1 (2026-05-19)：Fiber 全局 BodyLimit 32MB 对所有端点统一，文本 LLM 接口
// 实际需求 << 32MB（即便 1M context 也只 ~2MB），32MB 给攻击者 8× 的 DoS 放大面
// （fasthttp buffer copy ~3× 系数，96MB 临时内存 × N 并发请求）。
//
// 这里提供 per-route BodyLimit middleware，文本接口设 4MB，媒体接口保留 32MB。
// 用法：
//
//	app.All("/v1/chat/completions",
//	    middleware.BodyLimit(4 * 1024 * 1024),
//	    llmProxyLimiter, proxy.ChatCompletionProxyHandler)
//
// 注意：Fiber 全局 BodyLimit 仍兜底（防止小于全局限的攻击在更高层被丢弃）。
// 这里只能 narrower 不能 wider。

import (
	"github.com/gofiber/fiber/v2"
)

// BodyLimit 返回一个 Fiber middleware，要求 c.Body() 长度 ≤ limitBytes。
// 超限直接 413 + ERR_REQUEST_TOO_LARGE message_code，避免 handler 才发现已经
// 读到大 buffer。
func BodyLimit(limitBytes int) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if len(c.Body()) > limitBytes {
			return c.Status(413).JSON(fiber.Map{
				"success":      false,
				"message_code": "ERR_REQUEST_TOO_LARGE",
				"message":      "请求体超过当前端点上限",
			})
		}
		return c.Next()
	}
}
