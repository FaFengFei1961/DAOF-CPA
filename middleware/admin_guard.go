package middleware

import (
	"net"
	"net/url"
	"strings"

	"daof-cpa/database"

	"github.com/gofiber/fiber/v2"
)

// AdminGuard 是 admin 路由的鉴权中间件。
// Token 来源：HttpOnly Cookie 优先 → Bearer header 回退（统一走 ExtractAdminToken）。
//
// fix Major（codex 第八轮）：CSRF 纵深防御。
// SameSite=Strict cookie 已能挡住绝大多数跨站攻击，但**同站子域**和**本地恶意页面**
// 仍可能发起带 cookie 的写请求。对所有写方法（POST/PUT/DELETE/PATCH）追加 Origin/Referer
// 校验：必须等于本站 Host，否则 403。GET 请求不校验（无副作用）。
func AdminGuard(c *fiber.Ctx) error {
	token := ExtractAdminToken(c)
	if token == "" {
		return c.Status(401).JSON(fiber.Map{"success": false, "message": "未识别到授权凭证，操作终止", "message_code": "ERR_MISSING_SUPREME_TOKEN"})
	}

	// CSRF Origin/Referer 校验：仅对**基于 cookie**的写请求生效。
	// 显式带 `Authorization: Bearer xxx` 的客户端（curl / CI / admin CLI / SDK）跳过该检查——
	// CSRF 攻击只能利用浏览器自动附加的 cookie；浏览器不会跨站发 Bearer header。
	// 这同时保留了 admin 工具的可用性，又对纯 cookie 的浏览器场景做严格 origin 校验。
	method := c.Method()
	if method == fiber.MethodPost || method == fiber.MethodPut || method == fiber.MethodDelete || method == fiber.MethodPatch {
		if !hasBearerAuth(c) && !sameOriginRequest(c) {
			return c.Status(403).JSON(fiber.Map{
				"success":      false,
				"message":      "CSRF 防护：跨域写请求被拒绝（cookie 鉴权请确保从同源页面发起）",
				"message_code": "ERR_CSRF_ORIGIN_MISMATCH",
			})
		}
	}

	var admin database.User
	// fix Major（codex 第四轮）：必须 status=1 才放行——封禁 admin 仍能凭旧 cookie/token 调用所有 admin 接口。
	if err := database.DB.Where("token = ? AND role = ? AND status = ?", token, "admin", 1).First(&admin).Error; err != nil {
		return c.Status(403).JSON(fiber.Map{"success": false, "message": "越权访问行为已被阻止", "message_code": "ERR_FORGED_INSTRUCTION"})
	}

	// fix MAJOR R23+3-B9（codex 第四轮）：注入 admin 用户实例 + ID 到 fiber locals，
	// 让下游 handler（coupon / channel_model / refund 等）能直接拿 operator ID 写审计，
	// 不必再手动调 loadAdminUser。
	c.Locals("admin_user", &admin)
	c.Locals("admin_user_id", admin.ID)

	return c.Next()
}

// hasBearerAuth 判断请求是否显式带了 `Authorization: Bearer <token>` 头。
// 这类请求来自 SDK / curl / CI 等非浏览器客户端，免疫 CSRF（浏览器不会跨站自动附加 Bearer）。
func hasBearerAuth(c *fiber.Ctx) bool {
	auth := strings.TrimSpace(c.Get("Authorization"))
	return strings.HasPrefix(strings.ToLower(auth), "bearer ")
}

// sameOriginRequest 校验 Origin 或 Referer 头与本站完全同源（scheme + host + port 全等）。
//
// fix CRITICAL C1（codex 第二十轮）：原实现 stripPort 比对仅主机名 →
// localhost:3001（恶意 dev 服务）的 Origin 与 localhost:3000（admin） 视同源，
// 攻击者在同主机不同端口可借 cookie 发写请求。修复：保留端口做完整比较。
//
// 比较口径：scheme + hostname + 显式端口（即使是默认端口也按字面比较）。
//   - Origin 优先（更严格，浏览器永远附端口）
//   - 若无 Origin，fallback 到 Referer
//   - 都没有 → 拒绝（admin 写操作必有浏览器标头）
func sameOriginRequest(c *fiber.Ctx) bool {
	myScheme := strings.ToLower(strings.TrimSpace(c.Protocol()))
	myHost := strings.ToLower(strings.TrimSpace(c.Hostname())) // fiber 的 Hostname() 含端口
	if myHost == "" || myScheme == "" {
		return false
	}
	myHostname, myPort := splitHostPort(myHost, myScheme)

	check := func(rawURL string) bool {
		u, err := url.Parse(rawURL)
		if err != nil {
			return false
		}
		// scheme 必须一致（防 HTTP↔HTTPS 跨协议）
		otherScheme := strings.ToLower(u.Scheme)
		if otherScheme == "" || otherScheme != myScheme {
			return false
		}
		// hostname 必须一致
		// fix MAJOR M23-A3（codex 第二十三轮）：Origin 端也要走 normalizeIPv6Zone，
		// 否则 Host: `[fe80::1%eth0]` (Mi22-1 已归一化为 fe80::1)
		// 与 Origin: http://[fe80::1%25eth0] (u.Hostname() = fe80::1%eth0) 仍会不匹配。
		otherHostname := normalizeIPv6Zone(strings.ToLower(u.Hostname()))
		if otherHostname == "" || otherHostname != myHostname {
			return false
		}
		// port 必须一致（按 scheme 默认端口归一化后比较）
		otherPort := u.Port()
		if otherPort == "" {
			otherPort = defaultPortFor(otherScheme)
		}
		return otherPort == myPort
	}
	if origin := strings.TrimSpace(c.Get("Origin")); origin != "" {
		return check(origin)
	}
	if referer := strings.TrimSpace(c.Get("Referer")); referer != "" {
		return check(referer)
	}
	return false
}

// splitHostPort 拆 host[:port]，缺端口时按 scheme 推断默认端口（http=80 / https=443）。
//
// fix MAJOR M-A1（codex 第二十一轮）：IPv6 边界处理 ——
// `net.SplitHostPort` 失败时（缺端口），原实现直接返回 `hostport`，但 IPv6 字面量带括号
// （如 Host: `[::1]`）。`url.Parse(...).Hostname()` 会去掉括号返回 `::1`，导致 host 比对
// `[::1] != ::1` 误拒同源。修复：fallback 路径同样剥离 IPv6 括号。
//
// fix Minor Mi22-1（codex 第二十二轮）：IPv6 zone-id 归一化。
// link-local 地址（fe80::1%eth0）在 Host 头里可能以原始形式或 percent-encoded（%25eth0）出现，
// 而 `url.Parse(...).Hostname()` 会做 PathUnescape。两侧不一致会让合法同源被误拒。
// 同时去掉 zone-id 前的 `%`/`%25` 后比较，让 host 部分一致。
func splitHostPort(hostport, scheme string) (string, string) {
	if h, p, err := net.SplitHostPort(hostport); err == nil {
		return normalizeIPv6Zone(strings.ToLower(h)), p
	}
	// fallback：缺端口或非法格式。剥离 IPv6 字面量的方括号让其与 url.Hostname() 一致。
	host := strings.ToLower(hostport)
	if len(host) >= 2 && host[0] == '[' && host[len(host)-1] == ']' {
		host = host[1 : len(host)-1]
	}
	return normalizeIPv6Zone(host), defaultPortFor(scheme)
}

// normalizeIPv6Zone 把 IPv6 zone-id 标准化（兼容 raw `%` 和 percent-encoded `%25`）。
// 输入：`fe80::1%eth0` 或 `fe80::1%25eth0` → 输出：`fe80::1` （去掉 zone-id）。
// 设计权衡：跨 zone 的同源比较通常无意义（zone-id 是本地链接限定符），统一去掉做名称匹配。
// 仅处理含 `:` 的字符串（IPv6），普通 IPv4 / 域名直接返回。
func normalizeIPv6Zone(h string) string {
	if !strings.Contains(h, ":") {
		return h
	}
	// 兼容 percent-encoded zone-id (%25eth0) 和 raw (%eth0)
	if i := strings.Index(h, "%25"); i != -1 {
		return h[:i]
	}
	if i := strings.Index(h, "%"); i != -1 {
		return h[:i]
	}
	return h
}

func defaultPortFor(scheme string) string {
	switch scheme {
	case "https":
		return "443"
	case "http":
		return "80"
	}
	return ""
}
