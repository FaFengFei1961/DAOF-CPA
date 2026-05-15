// Package middleware / admin_guard_security_test.go
//
// 覆盖 AdminGuard 的 CRITICAL/Major 修复：
//  1. R4 Major: status=1 强制——封禁 admin（status=2）即使持旧 cookie 也必须 403
//  2. R8 Major: CSRF Origin/Referer 校验——cookie 写请求必须同源；Bearer 写请求免校验
package middleware

import (
	"net/http/httptest"
	"testing"

	"daof-ai-hub/database"

	"github.com/gofiber/fiber/v2"
)

// setupAdminGuardTestDB 重置 DB 并塞 active + banned 两个 admin。
func setupAdminGuardTestDB(t *testing.T) (active, banned *database.User) {
	t.Helper()
	setupTestDB() // 已存在的 helper（in-memory + admin "admin-secret-123" status=1 默认）

	// 把现有 admin 显式标记 status=1（防 schema 默认变化）
	database.DB.Model(&database.User{}).
		Where("token = ?", "admin-secret-123").
		Update("status", 1)
	var a database.User
	database.DB.Where("token = ?", "admin-secret-123").First(&a)
	active = &a

	// 创建一个被封禁的 admin
	b := database.User{
		Username: "banned-admin",
		Role:     "admin",
		Token:    "banned-secret-789",
		Status:   2, // 封禁
	}
	database.DB.Create(&b)
	banned = &b
	return
}

// ─── R4 Major: status=1 强制 ──────────────────────────────────────

// TestSecurity_AdminGuard_NonActiveStatusRejected 验证：
// 非 status=1 的 admin（status=0/2/3）一律 403——白名单严格匹配。
//
// 自审第十三轮加强：原仅测 status=2，现覆盖 status 取值空间外的 0 和 3，
// 防御未来引入新 status 值时静默放行。
func TestSecurity_AdminGuard_NonActiveStatusRejected(t *testing.T) {
	setupAdminGuardTestDB(t) // ensures isolation

	// 创建三个不同 status 的 admin
	// 注：User.Status 也带 `gorm:"default:1"`，Go int 零值（0）会被 GORM 替换成 1。
	// 用 Update("status", X) 显式落值（map/expr 路径绕过 default 替换）。
	statuses := []struct {
		name   string
		status int
		token  string
	}{
		{"status=0 (uninit)", 0, "uninit-token"},
		{"status=2 (banned)", 2, "banned-token-2"},
		{"status=3 (suspended)", 3, "suspended-token"},
	}
	for _, s := range statuses {
		u := database.User{
			Username: "admin-" + s.name, Role: "admin",
			Token:  s.token,
			Status: 1, // 占位，下面 UPDATE 落实际值
		}
		database.DB.Create(&u)
		database.DB.Model(&u).Update("status", s.status)
	}

	app := fiber.New()
	app.Use(AdminGuard)
	app.Get("/admin/test", func(c *fiber.Ctx) error { return c.SendString("OK") })

	for _, s := range statuses {
		t.Run(s.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/admin/test", nil)
			req.Header.Set("Authorization", "Bearer "+s.token)
			resp, _ := app.Test(req)
			defer resp.Body.Close()
			if resp.StatusCode != 403 {
				t.Errorf("status=%d admin must be rejected, got %d", s.status, resp.StatusCode)
			}
		})
	}
}

// TestSecurity_AdminGuard_BannedAdminRejected 验证：
// 已封禁（status=2）的 admin，即使持有有效 token 也必须 403。
func TestSecurity_AdminGuard_BannedAdminRejected(t *testing.T) {
	_, banned := setupAdminGuardTestDB(t)

	app := fiber.New()
	app.Use(AdminGuard)
	app.Get("/admin/test", func(c *fiber.Ctx) error { return c.SendString("OK") })

	req := httptest.NewRequest("GET", "/admin/test", nil)
	req.Header.Set("Authorization", "Bearer "+banned.Token)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 403 {
		t.Errorf("banned admin (status=2) should get 403, got %d", resp.StatusCode)
	}
}

// TestSecurity_AdminGuard_ActiveAdminPasses 验证：status=1 的 admin 持 Bearer 可正常通过。
// 这是 baseline 测试，确认上面"封禁拒绝"不是因为别的原因失败。
func TestSecurity_AdminGuard_ActiveAdminPasses(t *testing.T) {
	active, _ := setupAdminGuardTestDB(t)

	app := fiber.New()
	app.Use(AdminGuard)
	app.Get("/admin/test", func(c *fiber.Ctx) error { return c.SendString("OK") })

	req := httptest.NewRequest("GET", "/admin/test", nil)
	req.Header.Set("Authorization", "Bearer "+active.Token)
	resp, _ := app.Test(req)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("active admin should pass, got %d", resp.StatusCode)
	}
}

// ─── R8 Major: CSRF Origin/Referer 校验 ───────────────────────────

// TestSecurity_AdminGuard_CSRF_CookieWriteWithoutOriginRejected 验证：
// 写方法（POST/PUT/DELETE/PATCH）+ cookie 鉴权 + 无 Origin/Referer → 403 ERR_CSRF_ORIGIN_MISMATCH。
//
// 攻击场景：恶意子站点 / 本地恶意页面诱导用户浏览器自动附加 admin cookie 发 POST。
// 防御：CSRF Origin/Referer 必须等于本站 Host。
//
// 注意：仅 cookie 鉴权时校验；Bearer header 因浏览器不会跨站附加，免校验。
func TestSecurity_AdminGuard_CSRF_CookieWriteWithoutOriginRejected(t *testing.T) {
	active, _ := setupAdminGuardTestDB(t)

	app := fiber.New()
	app.Use(AdminGuard)
	app.Post("/admin/dangerous", func(c *fiber.Ctx) error { return c.SendString("OK") })

	req := httptest.NewRequest("POST", "/admin/dangerous", nil)
	// 关键：用 cookie 鉴权（非 Bearer），且 NOT 设 Origin/Referer
	req.Header.Set("Cookie", "daof_admin_token="+active.Token)

	resp, _ := app.Test(req)
	defer resp.Body.Close()

	if resp.StatusCode != 403 {
		t.Errorf("cookie POST without Origin/Referer should be 403 (CSRF), got %d", resp.StatusCode)
	}
}

// TestSecurity_AdminGuard_CSRF_BearerWriteWithoutOriginAllowed 验证：
// Bearer 鉴权（curl/CI/SDK 等非浏览器客户端）即使无 Origin 也允许写——
// 浏览器不会跨站自动附加 Authorization 头，CSRF 攻击无法利用 Bearer。
func TestSecurity_AdminGuard_CSRF_BearerWriteWithoutOriginAllowed(t *testing.T) {
	active, _ := setupAdminGuardTestDB(t)

	app := fiber.New()
	app.Use(AdminGuard)
	app.Post("/admin/dangerous", func(c *fiber.Ctx) error { return c.SendString("OK") })

	req := httptest.NewRequest("POST", "/admin/dangerous", nil)
	req.Header.Set("Authorization", "Bearer "+active.Token)
	// 故意不设 Origin/Referer

	resp, _ := app.Test(req)
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("Bearer POST without Origin should pass (CSRF immunity), got %d", resp.StatusCode)
	}
}

// TestSecurity_AdminGuard_CSRF_CookieWriteWithCrossOriginRejected 验证：
// cookie 写请求 + Origin 头是别人站点 → 拒绝。
func TestSecurity_AdminGuard_CSRF_CookieWriteWithCrossOriginRejected(t *testing.T) {
	active, _ := setupAdminGuardTestDB(t)

	app := fiber.New()
	app.Use(AdminGuard)
	app.Post("/admin/dangerous", func(c *fiber.Ctx) error { return c.SendString("OK") })

	req := httptest.NewRequest("POST", "/admin/dangerous", nil)
	req.Host = "ourapp.example.com"
	req.Header.Set("Cookie", "daof_admin_token="+active.Token)
	req.Header.Set("Origin", "https://evil.example.org") // 跨域

	resp, _ := app.Test(req)
	defer resp.Body.Close()

	if resp.StatusCode != 403 {
		t.Errorf("cookie POST with cross-origin Origin should be 403, got %d", resp.StatusCode)
	}
}

// TestSecurity_AdminGuard_CSRF_CookieWriteWithSameOriginAllowed 验证：
// cookie 写请求 + Origin 与请求 Host 同源 → 允许。
func TestSecurity_AdminGuard_CSRF_CookieWriteWithSameOriginAllowed(t *testing.T) {
	active, _ := setupAdminGuardTestDB(t)

	app := fiber.New()
	app.Use(AdminGuard)
	app.Post("/admin/dangerous", func(c *fiber.Ctx) error { return c.SendString("OK") })

	req := httptest.NewRequest("POST", "/admin/dangerous", nil)
	req.Host = "ourapp.example.com"
	req.Header.Set("Cookie", "daof_admin_token="+active.Token)
	// fix MAJOR M-B1（codex 第二十一轮）：sameOriginRequest 现在校验 scheme 一致，
	// httptest 默认协议是 http，所以 Origin 也得是 http 才匹配。
	req.Header.Set("Origin", "http://ourapp.example.com") // 同 Host + 同 scheme

	resp, _ := app.Test(req)
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("cookie POST with same-origin should pass, got %d", resp.StatusCode)
	}
}

// TestSecurity_AdminGuard_CSRF_GETNotChecked 验证：GET 请求不做 CSRF 校验（无副作用）。
func TestSecurity_AdminGuard_CSRF_GETNotChecked(t *testing.T) {
	active, _ := setupAdminGuardTestDB(t)

	app := fiber.New()
	app.Use(AdminGuard)
	app.Get("/admin/list", func(c *fiber.Ctx) error { return c.SendString("OK") })

	req := httptest.NewRequest("GET", "/admin/list", nil)
	req.Header.Set("Cookie", "daof_admin_token="+active.Token)
	// 不设 Origin/Referer

	resp, _ := app.Test(req)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("GET without Origin should pass (CSRF only checks writes), got %d", resp.StatusCode)
	}
}

// TestSecurity_AdminGuard_CSRF_NonStandardPortSameOrigin 验证（自审第十三轮 M1 修复）：
// 部署在非标准端口（dev :3000、内网 :8443）时，浏览器 Origin 头含端口，
// 旧实现用 `c.Hostname()`（剥端口）vs `u.Host`（含端口）比较 → 永远不匹配 → 全部 CSRF 拒绝。
// 修复后两侧都用 `Hostname()` 方法（不含端口），仅比较主机名。
func TestSecurity_AdminGuard_CSRF_NonStandardPortSameOrigin(t *testing.T) {
	active, _ := setupAdminGuardTestDB(t)

	app := fiber.New()
	app.Use(AdminGuard)
	app.Post("/admin/dangerous", func(c *fiber.Ctx) error { return c.SendString("OK") })

	// 模拟 dev 部署：app 在 :3000，浏览器 Origin 也是 :3000
	req := httptest.NewRequest("POST", "/admin/dangerous", nil)
	req.Host = "ourapp.example.com:3000" // 含端口
	req.Header.Set("Cookie", "daof_admin_token="+active.Token)
	req.Header.Set("Origin", "http://ourapp.example.com:3000")
	resp, _ := app.Test(req)
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("non-standard-port same-origin should pass after M1 fix, got %d", resp.StatusCode)
	}

	// fix CRITICAL C1（codex 第二十轮）：原测试断言 "Host:3000 + Origin:3001 → 200" 是错误期望。
	// 同主机不同端口的页面应被拒（防同主机邻居站借 cookie 发写请求）。
	// 新语义：scheme + hostname + port 全等才算同源。
	req2 := httptest.NewRequest("POST", "/admin/dangerous", nil)
	req2.Host = "ourapp.example.com:3000"
	req2.Header.Set("Cookie", "daof_admin_token="+active.Token)
	req2.Header.Set("Origin", "http://ourapp.example.com:3001")
	resp2, _ := app.Test(req2)
	defer resp2.Body.Close()
	if resp2.StatusCode != 403 {
		t.Errorf("same-host-different-port: should be 403 (port mismatch is cross-origin), got %d", resp2.StatusCode)
	}

	// 真跨主机仍被拦
	req3 := httptest.NewRequest("POST", "/admin/dangerous", nil)
	req3.Host = "ourapp.example.com:3000"
	req3.Header.Set("Cookie", "daof_admin_token="+active.Token)
	req3.Header.Set("Origin", "http://evil.example.org:3000")
	resp3, _ := app.Test(req3)
	defer resp3.Body.Close()
	if resp3.StatusCode != 403 {
		t.Errorf("different host (even same port) should be 403, got %d", resp3.StatusCode)
	}
}

// fix MAJOR M-A1（codex 第二十一轮）：IPv6 字面量同源边界 ——
// `Host: [::1]`（Hostname 含括号）vs `Origin: http://[::1]`（url.Hostname() 去括号）
// 必须被识别为同源。
func TestSecurity_AdminGuard_CSRF_IPv6SameOrigin(t *testing.T) {
	active, _ := setupAdminGuardTestDB(t)

	app := fiber.New()
	app.Use(AdminGuard)
	app.Post("/admin/dangerous", func(c *fiber.Ctx) error { return c.SendString("OK") })

	cases := []struct {
		name       string
		hostHeader string
		origin     string
		wantStatus int
	}{
		{"ipv6 default port same origin", "[::1]", "http://[::1]", 200},
		{"ipv6 explicit port same origin", "[::1]:3000", "http://[::1]:3000", 200},
		{"ipv6 different port", "[::1]:3000", "http://[::1]:3001", 403},
		{"ipv6 vs ipv4 different host", "[::1]", "http://127.0.0.1", 403},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/admin/dangerous", nil)
			req.Host = tc.hostHeader
			req.Header.Set("Cookie", "daof_admin_token="+active.Token)
			req.Header.Set("Origin", tc.origin)
			resp, _ := app.Test(req)
			defer resp.Body.Close()
			if resp.StatusCode != tc.wantStatus {
				t.Errorf("got %d want %d", resp.StatusCode, tc.wantStatus)
			}
		})
	}
}

// TestSecurity_AdminGuard_CSRF_RefererFallback 验证：
// 无 Origin 但有 Referer 时，应使用 Referer 做同源校验（fallback 路径）。
func TestSecurity_AdminGuard_CSRF_RefererFallback(t *testing.T) {
	active, _ := setupAdminGuardTestDB(t)

	app := fiber.New()
	app.Use(AdminGuard)
	app.Post("/admin/dangerous", func(c *fiber.Ctx) error { return c.SendString("OK") })

	// 同源 Referer
	req := httptest.NewRequest("POST", "/admin/dangerous", nil)
	req.Host = "ourapp.example.com"
	req.Header.Set("Cookie", "daof_admin_token="+active.Token)
	req.Header.Set("Referer", "http://ourapp.example.com/dashboard")
	resp, _ := app.Test(req)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("same-origin Referer should pass, got %d", resp.StatusCode)
	}

	// 跨域 Referer
	req2 := httptest.NewRequest("POST", "/admin/dangerous", nil)
	req2.Host = "ourapp.example.com"
	req2.Header.Set("Cookie", "daof_admin_token="+active.Token)
	req2.Header.Set("Referer", "https://evil.example.org/csrf")
	resp2, _ := app.Test(req2)
	defer resp2.Body.Close()
	if resp2.StatusCode != 403 {
		t.Errorf("cross-origin Referer should be 403, got %d", resp2.StatusCode)
	}
}
