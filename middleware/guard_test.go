package middleware

import (
	"daof-cpa/database"
	"daof-cpa/utils"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupTestDB() {
	var err error
	// 唯一 DSN 防多次 count=N race 跑互相污染数据（"file::memory:?cache=shared" 共享全局，
	// 第二轮跑时 INSERT 撞 UNIQUE 约束 + admin 用户残留 setup 状态错乱）
	dsn := fmt.Sprintf("file:guard_test_%d?mode=memory&cache=shared", time.Now().UnixNano())
	database.DB, err = gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		panic("failed to connect test database")
	}
	database.DB.AutoMigrate(&database.User{})
	// Mock an admin user
	database.DB.Create(&database.User{Username: "admin", Role: "admin", Token: "admin-secret-123"})
	// Mock a normal user
	database.DB.Create(&database.User{Username: "normal", Role: "user", Token: "user-secret-456"})
}

func TestLanGuard(t *testing.T) {
	app := fiber.New(fiber.Config{
		ProxyHeader: "X-Test-IP",
	})
	app.Use(LanGuard)
	app.Get("/", func(c *fiber.Ctx) error {
		return c.SendString("OK")
	})

	tests := []struct {
		name       string
		headers    map[string]string
		ip         string // to mock RemoteAddr if possible, but c.IP relies on socket or trust
		wantStatus int
	}{
		// 故意不再支持 X-Real-IP / X-Forwarded-For 头判断（防伪造），
		// 这两个头存在时直接被忽略，IP 来源回退到 TCP 对端（在 app.Test 下是 0.0.0.0）→ 403。
		// CF-Connecting-IP 现在也仅在 TCP 对端是 loopback 时才采信（防 codex-CRITICAL：
		// 直连未防火墙端口可伪造 CF 头），app.Test 下对端是 0.0.0.0，所有 CF 场景一律拒绝。
		{"NoHeaders_DefaultLoopback", map[string]string{}, "8.8.8.8", 403},
		{"CFHeader_NonLoopbackPeer_Rejected_Public", map[string]string{"CF-Connecting-IP": "8.8.8.8"}, "127.0.0.1", 403},
		{"CFHeader_NonLoopbackPeer_Rejected_LAN", map[string]string{"CF-Connecting-IP": "192.168.1.10"}, "127.0.0.1", 403},
		// 攻击者伪造 X-Real-IP 私有段：必须仍被拒绝（不再被信任）
		{"RealIP_Spoofed_LAN_Rejected", map[string]string{"X-Real-IP": "10.0.0.5"}, "127.0.0.1", 403},
		{"XFF_Spoofed_LAN_Rejected", map[string]string{"X-Forwarded-For": "172.16.0.1, 192.168.1.1"}, "127.0.0.1", 403},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			req.Header.Set("X-Test-IP", tc.ip)
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}

			// If not providing a header that overrides, fiber will use 0.0.0.0 remote addr which is not LAN.
			// Let's explicitly test the headers here.
			resp, _ := app.Test(req)
			if resp.StatusCode != tc.wantStatus {
				t.Errorf("expected %d, got %d", tc.wantStatus, resp.StatusCode)
			}
		})
	}
}

func TestAdminGuard(t *testing.T) {
	setupTestDB()

	app := fiber.New()
	app.Use(AdminGuard)
	app.Get("/", func(c *fiber.Ctx) error {
		return c.SendString("ADMIN_ACCESS")
	})

	tests := []struct {
		name       string
		authHeader string
		wantStatus int
	}{
		{"No Auth Header", "", 401},
		{"Malformed Auth 1", "Bearer", 401},
		{"Malformed Auth 2", "admin-secret-123", 401},
		{"Invalid Token", "Bearer invalid-123", 403},
		{"Normal User Token", "Bearer user-secret-456", 403},
		{"Valid Admin Token", "Bearer admin-secret-123", 200},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			if tc.authHeader != "" {
				req.Header.Set("Authorization", tc.authHeader)
			}
			resp, _ := app.Test(req)
			if resp.StatusCode != tc.wantStatus {
				t.Errorf("expected %d, got %d", tc.wantStatus, resp.StatusCode)
			}
		})
	}
}

func TestLocalhostGuard(t *testing.T) {
	app := fiber.New(fiber.Config{
		ProxyHeader: "X-Test-IP",
	})
	app.Use(LocalhostOnly)
	app.Get("/", func(c *fiber.Ctx) error {
		return c.SendString("OK")
	})

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Test-IP", "8.8.8.8")
	resp, _ := app.Test(req)

	if resp.StatusCode != 403 {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

func TestLocalhostMiddleware_LoopbackParsedIP(t *testing.T) {
	app := fiber.New(fiber.Config{
		ProxyHeader: "X-Test-IP",
	})
	app.Use(LocalhostOnly)
	app.Get("/", func(c *fiber.Ctx) error {
		return c.SendString("OK")
	})

	tests := []struct {
		name       string
		ip         string
		wantStatus int
	}{
		{"IPv4Loopback", "127.0.0.1", 200},
		{"IPv6Loopback", "::1", 200},
		{"LocalhostHostnameRejected", "localhost", 403},
		{"PublicIPRejected", "8.8.8.8", 403},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			req.Header.Set("X-Test-IP", tc.ip)
			resp, _ := app.Test(req)
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status=%d want %d", resp.StatusCode, tc.wantStatus)
			}
		})
	}
}

func TestSetupGuard(t *testing.T) {
	setupTestDB() // Reinitialize memory DB
	InvalidateSetupGuardCache()

	app := fiber.New()
	app.Use(SetupGuard)
	app.Get("/", func(c *fiber.Ctx) error {
		return c.SendString("OK")
	})

	// bcrypt CheckHash 单次 ~70ms（race 模式更慢），给 app.Test 30s 缓冲
	const timeoutMs = 30000

	// 1. Valid admin (Role admin exists from setupTestDB, password not 123456 hash)
	resp, err := app.Test(httptest.NewRequest("GET", "/", nil), timeoutMs)
	if err != nil {
		t.Fatalf("test #1: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	// 2. Erase admin completely — 必须失效缓存，否则上一步缓存的"已 setup"状态会让此请求继续 200
	database.DB.Exec("DELETE FROM users WHERE role='admin'")
	InvalidateSetupGuardCache()
	resp2, err := app.Test(httptest.NewRequest("GET", "/", nil), timeoutMs)
	if err != nil {
		t.Fatalf("test #2: %v", err)
	}
	if resp2.StatusCode != 500 {
		t.Errorf("expected 500, got %d", resp2.StatusCode)
	}

	// 3. Admin is at default state — case2 SQL 错误未缓存（admin 不存在不写状态），需再次失效以重评估
	database.DB.Create(&database.User{Username: "root", Role: "admin", PasswordHash: utils.GenerateHash("123456")})
	InvalidateSetupGuardCache()
	resp3, err := app.Test(httptest.NewRequest("GET", "/", nil), timeoutMs)
	if err != nil {
		t.Fatalf("test #3: %v", err)
	}
	if resp3.StatusCode != 503 {
		t.Errorf("expected 503, got %d", resp3.StatusCode)
	}
}
