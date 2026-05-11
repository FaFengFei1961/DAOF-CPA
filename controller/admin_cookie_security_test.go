package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
)

func TestAdminCookieSecurityAttributes(t *testing.T) {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Get("/set", func(c *fiber.Ctx) error {
		setAdminCookie(c, "sk-test-admin")
		return c.SendStatus(204)
	})

	resp, err := app.Test(httptest.NewRequest("GET", "/set", nil))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	cookies := resp.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies=%d want 1", len(cookies))
	}
	assertAdminCookieSecurity(t, cookies[0], "sk-test-admin")
	if cookies[0].MaxAge != 0 {
		t.Fatalf("login cookie MaxAge=%d, want 0 because Expires carries the 30-day lifetime", cookies[0].MaxAge)
	}
	if cookies[0].Expires.IsZero() {
		t.Fatal("login cookie must set Expires")
	}
}

func TestAdminLogoutClearsStrictCookie(t *testing.T) {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Post("/logout", AdminLogout)

	resp, err := app.Test(httptest.NewRequest("POST", "/logout", nil))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	cookies := resp.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies=%d want 1", len(cookies))
	}
	assertAdminCookieSecurity(t, cookies[0], "")
	if cookies[0].MaxAge >= 0 {
		t.Fatalf("logout cookie MaxAge=%d, want negative clear marker", cookies[0].MaxAge)
	}
}

func assertAdminCookieSecurity(t *testing.T, cookie *http.Cookie, wantValue string) {
	t.Helper()
	if cookie.Name != "daof_admin_token" {
		t.Fatalf("cookie name=%q want daof_admin_token", cookie.Name)
	}
	if cookie.Value != wantValue {
		t.Fatalf("cookie value=%q want %q", cookie.Value, wantValue)
	}
	if cookie.Path != "/" {
		t.Fatalf("cookie path=%q want /", cookie.Path)
	}
	if !cookie.HttpOnly {
		t.Fatal("admin cookie must be HttpOnly")
	}
	if !cookie.Secure {
		t.Fatal("admin cookie must be Secure")
	}
	if cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("admin cookie SameSite=%v want Strict", cookie.SameSite)
	}
}
