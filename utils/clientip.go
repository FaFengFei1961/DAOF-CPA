package utils

import (
	"net"
	"strings"

	"github.com/gofiber/fiber/v2"
)

// RealClientIP 提取无法被攻击者伪造的真实客户端 IP。
//
// 优先级：
//  1. CF-Connecting-IP — **仅当 TCP 对端是受信任代理（loopback）时采信**。
//     cloudflared 跑在本机，CF 请求都经 127.0.0.1 进入；这是唯一安全的信任模型。
//  2. TCP socket 对端 IP（c.Context().RemoteIP()，网络层数据攻击者无法伪造）
//
// fix Codex-CRITICAL：原实现无条件信任 CF-Connecting-IP，攻击者直连服务端口（非经 cloudflared）
// 发送 `CF-Connecting-IP: 127.0.0.1` 即可让 RealClientIP 返回 127.0.0.1，绕过 LanGuard
// 直接访问 /api/admin/* 和 /api/root/* 端点，影响整个 admin 控制面。
//
// **故意忽略** X-Real-IP / X-Forwarded-For：直连未防火墙的服务端口可任意伪造，
// 同样的原因 CF-Connecting-IP 也必须验证 hop。
func RealClientIP(c *fiber.Ctx) string {
	remote := c.Context().RemoteIP()
	// 仅当 TCP 对端是受信代理（loopback）时，才采信 CF-Connecting-IP 头
	if remote != nil && (remote.IsLoopback() || remote.String() == "::1") {
		if ip := strings.TrimSpace(c.Get("CF-Connecting-IP")); ip != "" && net.ParseIP(ip) != nil {
			return ip
		}
	}
	if remote != nil {
		return remote.String()
	}
	return ""
}
