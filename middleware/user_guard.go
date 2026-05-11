// Package middleware / user_guard.go
//
// UserGuard 普通用户级鉴权。验证 Bearer token 或 admin cookie（admin 也是 User），
// 命中 AuthCache 后把 user 注入 c.Locals("user")，否则 401。
package middleware

import (
	"strings"

	"daof-ai-hub/proxy"

	"github.com/gofiber/fiber/v2"
)

func UserGuard(c *fiber.Ctx) error {
	// 1) Bearer token（普通用户主路径）
	authHeader := c.Get("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") || strings.HasPrefix(authHeader, "bearer ") {
		if token := strings.TrimSpace(authHeader[7:]); token != "" {
			if u := proxy.LookupUserByToken(token); u != nil {
				if u.Status == 2 {
					return c.Status(403).JSON(fiber.Map{
						"success":      false,
						"message":      "账户被封禁",
						"message_code": "ERR_BANNED",
						"ban_reason":   u.BanReason,
					})
				}
				c.Locals("user", u)
				return c.Next()
			}
		}
	}

	// 2) admin cookie（admin 浏览用户视图时复用同一中间件）
	// fix Major M4（claude security 第十五轮）：原仅查 Role == "admin"，未查 Status == 1
	// → 被封禁 admin 仍能在 c.Locals("user") 注入；防御纵深漏洞与 AdminGuard 不一致。
	//
	// fix MAJOR Phase 4-codex（第二十四轮）：admin cookie fallback 让此中间件接受 cookie 鉴权，
	// 这本身不安全（CSRF 风险）。所有挂 UserGuard 的写路由（POST/PUT/DELETE）必须 **同时挂 CSRFGuard**，
	// 否则 admin 浏览器会被跨源诱导写令牌/扣费/退款。Bearer 鉴权（SDK/CI）天然不受影响。
	// 路由侧已全量挂 CSRFGuard 在 main.go（购买/取消/充值/令牌/通知/balance preference 等）。
	if cookieToken := c.Cookies("daof_admin_token"); cookieToken != "" {
		if u := proxy.LookupUserByToken(cookieToken); u != nil && u.Role == "admin" && u.Status == 1 {
			c.Locals("user", u)
			return c.Next()
		}
	}

	return c.Status(401).JSON(fiber.Map{
		"success":      false,
		"message":      "鉴权失败",
		"message_code": "ERR_NO_AUTH",
	})
}
