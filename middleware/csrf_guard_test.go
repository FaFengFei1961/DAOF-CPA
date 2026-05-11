// Package middleware / csrf_guard_test.go
//
// 覆盖 CSRFGuard 的关键不变量：
//   - GET/HEAD/OPTIONS 任何来源都放行
//   - POST/PUT/DELETE/PATCH 同源 cookie 放行
//   - POST/PUT/DELETE/PATCH 跨源 cookie 必须 403
//   - POST/PUT/DELETE/PATCH 任意来源的 Bearer 都放行（SDK/CI 免疫）
//
// fix CRITICAL C22-A1（codex 第二十二轮）：双角色 ticket 路由必须有此防护。
package middleware

import (
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
)

// newCSRFGuardTestApp 挂载一个最小的 dual-role 写路由 + CSRFGuard。
func newCSRFGuardTestApp() *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Post("/dual-write", CSRFGuard, func(c *fiber.Ctx) error { return c.SendString("OK") })
	app.Get("/dual-read", CSRFGuard, func(c *fiber.Ctx) error { return c.SendString("OK") })
	return app
}

func TestCSRFGuard_GetAlwaysAllowed(t *testing.T) {
	app := newCSRFGuardTestApp()
	req := httptest.NewRequest("GET", "/dual-read", nil)
	req.Host = "ourapp.example.com"
	req.Header.Set("Origin", "http://evil.example.org")
	resp, _ := app.Test(req)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("GET should always pass even with cross-origin Origin, got %d", resp.StatusCode)
	}
}

func TestCSRFGuard_PostSameOriginPasses(t *testing.T) {
	app := newCSRFGuardTestApp()
	req := httptest.NewRequest("POST", "/dual-write", nil)
	req.Host = "ourapp.example.com"
	req.Header.Set("Origin", "http://ourapp.example.com")
	req.Header.Set("Cookie", "daof_admin_token=anything")
	resp, _ := app.Test(req)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("same-origin POST should pass, got %d", resp.StatusCode)
	}
}

func TestCSRFGuard_PostCrossOriginRejected(t *testing.T) {
	app := newCSRFGuardTestApp()
	req := httptest.NewRequest("POST", "/dual-write", nil)
	req.Host = "ourapp.example.com"
	req.Header.Set("Origin", "http://evil.example.org")
	req.Header.Set("Cookie", "daof_admin_token=stolen-via-csrf")
	resp, _ := app.Test(req)
	defer resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Errorf("cross-origin cookie POST should be 403, got %d", resp.StatusCode)
	}
}

func TestCSRFGuard_PostNoOriginRejected(t *testing.T) {
	app := newCSRFGuardTestApp()
	req := httptest.NewRequest("POST", "/dual-write", nil)
	req.Host = "ourapp.example.com"
	req.Header.Set("Cookie", "daof_admin_token=anything")
	resp, _ := app.Test(req)
	defer resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Errorf("missing Origin POST should be 403, got %d", resp.StatusCode)
	}
}

func TestCSRFGuard_BearerExempt(t *testing.T) {
	app := newCSRFGuardTestApp()
	req := httptest.NewRequest("POST", "/dual-write", nil)
	req.Host = "ourapp.example.com"
	req.Header.Set("Authorization", "Bearer some-sdk-token")
	// no Origin: SDK / CI 永远不发 Origin，但 Bearer 自带 CSRF 免疫
	resp, _ := app.Test(req)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("Bearer POST without Origin should pass (SDK/CI), got %d", resp.StatusCode)
	}
}
