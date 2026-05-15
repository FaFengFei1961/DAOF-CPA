package middleware

import (
	"net"

	"github.com/gofiber/fiber/v2"
)

// LocalhostOnly 是极度森严的安全防御墙，断绝外界公网访问机要数据的可能性
func LocalhostOnly(c *fiber.Ctx) error {
	ip := c.IP()
	if !isLoopbackIP(ip) {
		return c.Status(403).JSON(fiber.Map{
			"success": false,
			"message": "被拒绝(403)：访问该资源受限",
		})
	}
	return c.Next()
}

func isLoopbackIP(ip string) bool {
	parsedIP := net.ParseIP(ip)
	return parsedIP != nil && parsedIP.IsLoopback()
}
