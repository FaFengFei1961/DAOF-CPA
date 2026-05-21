package utils

import (
	"net"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/valyala/fasthttp"
)

// newCtxWithRemoteIP 用 fasthttp.RequestCtx 直接构造一个 fiber.Ctx，能精确控制
// socket peer IP（fiber app.Test() 默认 RemoteAddr 是 0.0.0.0，不能 mock loopback）。
func newCtxWithRemoteIP(t *testing.T, remoteIP string, headers map[string]string) (*fiber.Ctx, func()) {
	t.Helper()
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	fctx := &fasthttp.RequestCtx{}
	// 设 socket peer addr（fasthttp 内部读 *net.TCPAddr）
	addr, err := net.ResolveTCPAddr("tcp", net.JoinHostPort(remoteIP, "12345"))
	if err != nil {
		t.Fatalf("ResolveTCPAddr(%q): %v", remoteIP, err)
	}
	fctx.SetRemoteAddr(addr)
	for k, v := range headers {
		fctx.Request.Header.Set(k, v)
	}
	c := app.AcquireCtx(fctx)
	cleanup := func() { app.ReleaseCtx(c) }
	return c, cleanup
}

func TestRealClientIP_TrustsCFHeaderFromLoopback(t *testing.T) {
	// 本机 cloudflared 转发：TCP 对端是 127.0.0.1，CF-Connecting-IP 是真实客户端
	c, cleanup := newCtxWithRemoteIP(t, "127.0.0.1",
		map[string]string{"CF-Connecting-IP": "203.0.113.42"})
	defer cleanup()
	if got := RealClientIP(c); got != "203.0.113.42" {
		t.Errorf("got=%q, want 203.0.113.42 (loopback hop should trust CF header)", got)
	}
}

func TestRealClientIP_IgnoresCFHeaderFromPublicIP(t *testing.T) {
	// 关键安全测试：攻击者直连未防火墙的服务端口，伪造 CF-Connecting-IP 试图冒充内网
	c, cleanup := newCtxWithRemoteIP(t, "203.0.113.99",
		map[string]string{"CF-Connecting-IP": "127.0.0.1"})
	defer cleanup()
	got := RealClientIP(c)
	if got == "127.0.0.1" {
		t.Errorf("got=127.0.0.1 from spoofed CF header on public hop — LanGuard bypass!")
	}
	if got != "203.0.113.99" {
		t.Errorf("got=%q, want 203.0.113.99 (socket peer)", got)
	}
}

func TestRealClientIP_IgnoresInvalidCFHeader(t *testing.T) {
	// loopback 来源但 CF 头是非法 IP → 回退到 socket peer
	c, cleanup := newCtxWithRemoteIP(t, "127.0.0.1",
		map[string]string{"CF-Connecting-IP": "not-an-ip"})
	defer cleanup()
	if got := RealClientIP(c); got != "127.0.0.1" {
		t.Errorf("got=%q, want 127.0.0.1 (invalid CF header should fall back to socket)", got)
	}
}

func TestRealClientIP_IgnoresXForwardedFor(t *testing.T) {
	// fix Codex-CRITICAL 关键测试：X-Forwarded-For 完全不应被信任（即使来自 loopback）
	c, cleanup := newCtxWithRemoteIP(t, "127.0.0.1",
		map[string]string{"X-Forwarded-For": "10.0.0.1, 203.0.113.50"})
	defer cleanup()
	got := RealClientIP(c)
	if got == "10.0.0.1" || got == "203.0.113.50" {
		t.Errorf("got=%q — X-Forwarded-For must be ignored even from loopback", got)
	}
	if got != "127.0.0.1" {
		t.Errorf("got=%q, want 127.0.0.1 (socket peer)", got)
	}
}

func TestRealClientIP_IgnoresXRealIP(t *testing.T) {
	c, cleanup := newCtxWithRemoteIP(t, "127.0.0.1",
		map[string]string{"X-Real-IP": "10.0.0.1"})
	defer cleanup()
	if got := RealClientIP(c); got == "10.0.0.1" {
		t.Errorf("got=10.0.0.1 from X-Real-IP — should be ignored")
	}
}

func TestRealClientIP_IPv6LoopbackTrustsCF(t *testing.T) {
	c, cleanup := newCtxWithRemoteIP(t, "::1",
		map[string]string{"CF-Connecting-IP": "2001:db8::1"})
	defer cleanup()
	if got := RealClientIP(c); got != "2001:db8::1" {
		t.Errorf("got=%q, want 2001:db8::1 (IPv6 loopback should also trust CF)", got)
	}
}

func TestRealClientIP_NoHeadersReturnsSocketPeer(t *testing.T) {
	c, cleanup := newCtxWithRemoteIP(t, "192.0.2.1", nil)
	defer cleanup()
	if got := RealClientIP(c); got != "192.0.2.1" {
		t.Errorf("got=%q, want 192.0.2.1", got)
	}
}

func TestRealClientIP_PublicIPWithoutHeader(t *testing.T) {
	// 直连无任何头 → 返 socket peer
	c, cleanup := newCtxWithRemoteIP(t, "203.0.113.50", nil)
	defer cleanup()
	if got := RealClientIP(c); got != "203.0.113.50" {
		t.Errorf("got=%q, want 203.0.113.50", got)
	}
}
