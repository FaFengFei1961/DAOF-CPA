package controller

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"daof-ai-hub/database"
	"daof-ai-hub/utils"

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

func TestGodSetup_BlockedWhenAdminExists(t *testing.T) {
	setupOAuthControllerTestDB(t)
	admin := database.User{
		Username:     "configured_admin",
		Role:         "admin",
		Token:        "sk-configured-admin",
		Status:       1,
		PasswordHash: utils.GenerateHash("old-password"),
	}
	if err := database.DB.Create(&admin).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Post("/setup", GodSetup)
	body, _ := json.Marshal(GodSetupRequest{
		CurrentUsername: admin.Username,
		OldPassword:     "old-password",
		NewUsername:     "new_admin",
		NewPassword:     "new-password",
	})
	req := httptest.NewRequest(http.MethodPost, "/setup", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status=%d, want 403", resp.StatusCode)
	}
	var got map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if got["message_code"] != "ERR_SETUP_NOT_ALLOWED" {
		t.Fatalf("message_code=%v, want ERR_SETUP_NOT_ALLOWED", got["message_code"])
	}
}

func TestAdminLogout_RevokesAllSessions(t *testing.T) {
	setupOAuthControllerTestDB(t)
	admin := database.User{
		Username: "legacy_admin",
		Role:     "admin",
		Token:    "sk-legacy-admin",
		Status:   1,
	}
	if err := database.DB.Create(&admin).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}
	session1, err := database.CreateUserSession(admin.ID, "ua1", "127.0.0.1")
	if err != nil {
		t.Fatalf("create session1: %v", err)
	}
	session2, err := database.CreateUserSession(admin.ID, "ua2", "127.0.0.1")
	if err != nil {
		t.Fatalf("create session2: %v", err)
	}

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Post("/logout", AdminLogout)
	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	req.Header.Set("Authorization", "Bearer "+admin.Token)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}

	for _, sid := range []string{session1, session2} {
		var session database.UserSession
		if err := database.DB.Where("session_id = ?", sid).First(&session).Error; err != nil {
			t.Fatalf("load session %s: %v", sid, err)
		}
		if session.RevokedAt == nil {
			t.Fatalf("session %s not revoked", sid)
		}
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
