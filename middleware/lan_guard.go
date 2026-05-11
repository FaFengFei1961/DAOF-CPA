package middleware

import (
	"net"

	"daof-ai-hub/utils"

	"github.com/gofiber/fiber/v2"
)

// LanGuard 物理网络层防御墙：仅放行 loopback / RFC1918 私有段，公网拒绝。
//
// 真实客户端 IP 来源（按优先级，且**全部不可被攻击者伪造**）：
//  1. CF-Connecting-IP — Cloudflare 注入并清洗，前提是请求经过 CF Tunnel/Proxy
//  2. TCP socket 对端 IP（c.Context().RemoteIP()）— 网络层数据，攻击者无法伪造
//
// 注意：**故意不读** X-Real-IP / X-Forwarded-For。这两个头是客户端可控的，
// 攻击者直连未防火墙的服务端口可发 `X-Real-IP: 192.168.1.1` 让 IsPrivate() 返回 true 绕过。
// 若部署在 nginx/traefik 后面需要走 XFF，请通过 Fiber 的 TrustedProxies/ProxyHeader 让框架
// 自身验证，并显式确认来源代理在白名单内（当前 main.go 仅信任 127.0.0.1, ::1）。
func LanGuard(c *fiber.Ctx) error {
	parsed := net.ParseIP(utils.RealClientIP(c))
	if parsed == nil {
		return c.Status(403).JSON(fiber.Map{
			"success":      false,
			"message":      "无法识别访问来源，已拒绝",
			"message_code": "ERR_LAN_IP_UNRECOGNIZED",
		})
	}

	if !parsed.IsLoopback() && !parsed.IsPrivate() {
		return c.Status(403).JSON(fiber.Map{
			"success":      false,
			"message":      "管理面板仅允许从本机或内网访问",
			"message_code": "ERR_LAN_ONLY",
		})
	}

	return c.Next()
}
