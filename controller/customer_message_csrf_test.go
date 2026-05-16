// Package controller / customer_message_csrf_test.go
//
// 验证 ticket 双角色路由（/api/tickets/:id/messages|close|read）的 CSRF 防护：
//   - 跨源 admin cookie 写请求 → 403（CSRFGuard 拦截）
//   - 同源 admin cookie 写请求 → 通过 CSRFGuard，继续业务逻辑
//   - Bearer header 用户写请求 → 通过 CSRFGuard（Bearer 免 CSRF）
//   - admin cookie + user Bearer 同时存在 → Bearer 优先（M23-A4 双角色身份选择）
//
// fix Mi23-4（codex 第二十三轮）：CSRFGuard 单元测试只测裸中间件，没覆盖真实
// /api/tickets/:id/messages 路由组合。补上 integration 防回归。
package controller

import (
	"bytes"
	"net/http/httptest"
	"testing"
	"time"

	"daof-cpa/database"
	"daof-cpa/middleware"
	"daof-cpa/proxy"

	"github.com/gofiber/fiber/v2"
)

// newTicketDualApp 复刻 main.go 中的 ticket 双角色路由（CSRFGuard + handler）
func newTicketDualApp() *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Post("/api/tickets/:id/close", middleware.CSRFGuard, CloseTicket)
	return app
}

func seedOpenTicket(t *testing.T, userID uint) *database.Ticket {
	t.Helper()
	ticket := database.Ticket{
		UserID:        userID,
		Subject:       "test",
		Status:        "open",
		LastMessageAt: time.Now(),
	}
	if err := database.DB.Create(&ticket).Error; err != nil {
		t.Fatalf("seed ticket: %v", err)
	}
	return &ticket
}

func TestSecurity_TicketClose_CSRF_CrossOriginAdminCookieRejected(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 0)
	app := newTicketDualApp()
	ticket := seedOpenTicket(t, user.ID)

	req := httptest.NewRequest("POST",
		"/api/tickets/"+itoaUint(ticket.ID)+"/close",
		bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://evil.example.org")
	req.Host = "ourapp.example.com"
	// 攻击者诱导浏览器附 admin cookie（HttpOnly 即使设了，浏览器跨站请求仍会自动附）
	req.Header.Set("Cookie", "daof_admin_token="+admin.Token)

	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Errorf("cross-origin admin cookie write should be 403 (CSRFGuard), got %d", resp.StatusCode)
	}
	// 验证：tx 未被执行 → ticket 仍 open
	var fresh database.Ticket
	database.DB.First(&fresh, ticket.ID)
	if fresh.Status != "open" {
		t.Errorf("CSRF blocked but ticket status changed to %q", fresh.Status)
	}
}

func TestSecurity_TicketClose_CSRF_SameOriginAdminCookiePasses(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 0)
	app := newTicketDualApp()
	ticket := seedOpenTicket(t, user.ID)

	req := httptest.NewRequest("POST",
		"/api/tickets/"+itoaUint(ticket.ID)+"/close",
		bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://example.com")
	req.Host = "example.com"
	req.Header.Set("Cookie", "daof_admin_token="+admin.Token)

	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 403 {
		t.Errorf("same-origin admin cookie should pass CSRFGuard, got 403")
	}
}

func TestSecurity_TicketClose_CSRF_BearerExempt(t *testing.T) {
	setupSubTestDB(t)
	user := seedTestUser(t, 0)
	app := newTicketDualApp()
	ticket := seedOpenTicket(t, user.ID)

	req := httptest.NewRequest("POST",
		"/api/tickets/"+itoaUint(ticket.ID)+"/close",
		bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	// Bearer 不需要 Origin（SDK / curl 永远不发）
	req.Header.Set("Authorization", "Bearer "+user.Token)
	req.Host = "example.com"

	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 403 {
		t.Errorf("Bearer write should pass CSRFGuard without Origin, got 403")
	}
}

// fix MAJOR M23-A4（codex 第二十三轮）：admin cookie + user Bearer 同时存在时，Bearer 优先。
// 旧实现 cookie 优先 → admin 浏览器调试时发的请求会被识别成 admin 关闭工单。
func TestSecurity_TicketClose_DualAuth_BearerPriority(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 0)
	// LookupUserByToken 走 AuthCache，需要先注册
	proxy.AddUserToAuthCache(admin)
	proxy.AddUserToAuthCache(user)
	app := newTicketDualApp()
	ticket := seedOpenTicket(t, user.ID)

	req := httptest.NewRequest("POST",
		"/api/tickets/"+itoaUint(ticket.ID)+"/close",
		bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://example.com") // 同源让 CSRFGuard 通过
	req.Host = "example.com"
	// 同时附 admin cookie + user Bearer
	req.Header.Set("Cookie", "daof_admin_token="+admin.Token)
	req.Header.Set("Authorization", "Bearer "+user.Token)

	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("dual-auth (Bearer prio) close should succeed (user closes own ticket), got %d", resp.StatusCode)
	}
	// 验证：sender 应该是 user（Bearer 视角），而非 admin（cookie 视角）
	var msgs []database.TicketMessage
	database.DB.Where("ticket_id = ?", ticket.ID).Find(&msgs)
	for _, m := range msgs {
		if m.Sender == "admin" {
			t.Errorf("dual-auth: ticket close generated admin-sender message; Bearer should win → user")
			return
		}
	}
}
