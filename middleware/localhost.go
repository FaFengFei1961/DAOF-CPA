package middleware

import (
	"github.com/gofiber/fiber/v2"
)

// LocalhostOnly 是极度森严的安全防御墙，断绝外界公网访问机要数据的可能性
func LocalhostOnly(c *fiber.Ctx) error {
	ip := c.IP()
	if ip != "127.0.0.1" && ip != "::1" && ip != "localhost" {
		return c.Status(403).JSON(fiber.Map{
			"success": false,
			"message": "被拒绝(403)：访问该资源受限",
		})
	}
	return c.Next()
}
