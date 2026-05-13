// Package middleware / auth_extract.go
//
// 统一的 admin token 提取逻辑：优先 HttpOnly cookie，回退 Bearer header。
// 替代 admin_auth.go / admin_guard.go 等处重复实现。
package middleware

import (
	"strings"

	"github.com/gofiber/fiber/v2"
)

// ExtractAdminToken 从 Fiber 上下文按优先级取 admin token：
//
//  1. HttpOnly Cookie "daof_admin_token"
//  2. Authorization: Bearer <token>
//
// 返回空串表示未携带。
func ExtractAdminToken(c *fiber.Ctx) string {
	if cookie := strings.TrimSpace(c.Cookies("daof_admin_token")); cookie != "" {
		return cookie
	}
	authHeader := c.Get("Authorization")
	if authHeader == "" {
		return ""
	}
	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}
