// Package middleware / user_guard.go
//
// UserGuard 普通用户级鉴权。**仅接受 Bearer token**（用户主路径 / SDK / CI）。
// 命中 AuthCache 后把 user 注入 c.Locals("user")，否则 401。
//
// 安全约束：不接受 admin cookie 作为用户身份（避免横向越权 + CSRF 攻击面扩大）。
// admin 如需浏览用户视图，走 AdminGuard 专用 impersonation API，并显式审计。
package middleware

import (
	"strings"

	"daof-ai-hub/database"
	"daof-ai-hub/proxy"

	"github.com/gofiber/fiber/v2"
)

func UserGuard(c *fiber.Ctx) error {
	authHeader := c.Get("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") || strings.HasPrefix(authHeader, "bearer ") {
		if token := strings.TrimSpace(authHeader[7:]); token != "" {
			if database.IsSessionID(token) {
				if u, ok := database.LookupUserBySession(token); ok && u != nil {
					if u.Status != 1 {
						return c.Status(403).JSON(fiber.Map{
							"success":      false,
							"message":      "账户被封禁",
							"message_code": "ERR_BANNED",
							"ban_reason":   u.BanReason,
						})
					}
					c.Locals("user", u)
					c.Locals("session_id", token)
					return c.Next()
				}
				return c.Status(401).JSON(fiber.Map{
					"success":      false,
					"message":      "鉴权失败",
					"message_code": "ERR_NO_AUTH",
				})
			}
			if u := proxy.LookupUserByToken(token); u != nil {
				if u.Status != 1 {
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

	return c.Status(401).JSON(fiber.Map{
		"success":      false,
		"message":      "鉴权失败",
		"message_code": "ERR_NO_AUTH",
	})
}
