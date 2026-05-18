// Package middleware / user_guard.go
//
// UserGuard 普通用户级鉴权。**仅接受 Bearer token**（用户主路径 / SDK / CI）。
// 命中 AuthCache 后把 user 注入 c.Locals("user")，否则 401。
//
// 安全约束：不接受 admin cookie 作为用户身份（避免横向越权 + CSRF 攻击面扩大）。
// admin 如需浏览用户视图，走 AdminGuard 专用 impersonation API，并显式审计。
//
// UserGuardAllowBanned 是 UserGuard 的"软门"变体——封禁用户也能进入，但会被
// 标记 c.Locals("user_banned", true)，让 controller 自行决定是否允许操作。
// 仅用在明确允许 banned 用户访问的端点：/api/user/me、/api/tickets/* 等申诉路径。
package middleware

import (
	"strings"

	"daof-cpa/database"
	"daof-cpa/proxy"

	"github.com/gofiber/fiber/v2"
)

// userGuardCore 是 UserGuard / UserGuardAllowBanned 的共享解析逻辑。
// allowBanned=false：Status==2 直接 403 ERR_BANNED；
// allowBanned=true：放行但注入 c.Locals("user_banned", true)，controller 自行决定。
// 返回 (handled, err)：handled=true 表示已经发送响应或调用 Next，调用方不应继续。
func userGuardCore(c *fiber.Ctx, allowBanned bool) error {
	authHeader := c.Get("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") || strings.HasPrefix(authHeader, "bearer ") {
		if token := strings.TrimSpace(authHeader[7:]); token != "" {
			if database.IsSessionID(token) {
				if u, ok := database.LookupUserBySession(token); ok && u != nil {
					if u.Status == 2 && !allowBanned {
						return c.Status(403).JSON(fiber.Map{
							"success":      false,
							"message":      "账户被封禁",
							"message_code": "ERR_BANNED",
							"ban_reason":   u.BanReason,
						})
					}
					c.Locals("user", u)
					c.Locals("session_id", token)
					if u.Status == 2 {
						c.Locals("user_banned", true)
					}
					return c.Next()
				}
				return c.Status(401).JSON(fiber.Map{
					"success":      false,
					"message":      "鉴权失败",
					"message_code": "ERR_NO_AUTH",
				})
			}
			if u := proxy.LookupUserByToken(token); u != nil {
				if u.Status == 2 && !allowBanned {
					return c.Status(403).JSON(fiber.Map{
						"success":      false,
						"message":      "账户被封禁",
						"message_code": "ERR_BANNED",
						"ban_reason":   u.BanReason,
					})
				}
				c.Locals("user", u)
				if u.Status == 2 {
					c.Locals("user_banned", true)
				}
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

// UserGuard 标准用户鉴权——封禁用户拒之门外。
func UserGuard(c *fiber.Ctx) error {
	return userGuardCore(c, false)
}

// UserGuardAllowBanned 仅用于"申诉路径"，让封禁用户也能进入。controller 应通过
// c.Locals("user_banned") 自行判断是否限制行为（例如禁止充值但允许提工单）。
func UserGuardAllowBanned(c *fiber.Ctx) error {
	return userGuardCore(c, true)
}
