// Package middleware / csrf_guard.go
//
// 独立 CSRF 中间件：拦写请求里基于 cookie 的跨源写。
//
// fix CRITICAL C22-A1（codex 第二十二轮）：双角色路由（如 /api/tickets/:id/messages）
// 同时接受 admin cookie 与用户 Bearer token 鉴权。原 AdminGuard 的 CSRF 检查仅在 admin
// 路由生效，导致 admin 浏览器 cookie 可被跨源页面诱导写入工单（关闭/标已读/发消息）。
//
// 该中间件抽出 AdminGuard 的 CSRF 校验逻辑，让任何"双角色"或"用户写"路由都可独立挂载。
// 同源策略：scheme + host + port 全等（沿用 sameOriginRequest）。Bearer 请求免校验
// （SDK/curl/CI 不会被浏览器跨站发 Bearer）。
package middleware

import (
	"github.com/gofiber/fiber/v2"
)

// CSRFGuard 仅对写请求（POST/PUT/DELETE/PATCH）做 Origin/Referer 校验。
// GET/HEAD/OPTIONS 直接放行。
// Bearer 鉴权请求免校验。
// 仅 cookie / 无 Authorization 的写请求必须同源，否则返回 403 ERR_CSRF_ORIGIN_MISMATCH。
//
// 用法：
//
//	api.Post("/tickets/:id/messages", middleware.CSRFGuard, controller.PostTicketMessage)
//
// 或在 group 上挂载：
//
//	api.Use(middleware.CSRFGuard)
func CSRFGuard(c *fiber.Ctx) error {
	method := c.Method()
	if method != fiber.MethodPost && method != fiber.MethodPut && method != fiber.MethodDelete && method != fiber.MethodPatch {
		return c.Next()
	}
	if hasBearerAuth(c) {
		return c.Next()
	}
	if sameOriginRequest(c) {
		return c.Next()
	}
	return c.Status(403).JSON(fiber.Map{
		"success":      false,
		"message":      "CSRF 防护：跨域写请求被拒绝（cookie 鉴权请确保从同源页面发起）",
		"message_code": "ERR_CSRF_ORIGIN_MISMATCH",
	})
}
