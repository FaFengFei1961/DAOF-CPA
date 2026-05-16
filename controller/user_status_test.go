package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"daof-ai-hub/database"
	"daof-ai-hub/middleware"
	"daof-ai-hub/proxy"

	"github.com/gofiber/fiber/v2"
)

func TestUserStatus_RejectsNonOne(t *testing.T) {
	t.Run("session path rejects status zero", func(t *testing.T) {
		setupUserControllerTestDB(t)
		user := seedUpdateUserTarget(t, database.MicroPerUSD, 1)
		if err := database.DB.Model(&user).Update("status", 0).Error; err != nil {
			t.Fatalf("set status: %v", err)
		}
		sessionID, err := database.CreateUserSession(user.ID, "ua", "127.0.0.1")
		if err != nil {
			t.Fatalf("create session: %v", err)
		}

		app := fiber.New(fiber.Config{DisableStartupMessage: true})
		app.Use(middleware.UserGuard)
		app.Get("/", func(c *fiber.Ctx) error { return c.SendStatus(204) })
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer "+sessionID)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status=%d, want 403", resp.StatusCode)
		}
	})

	t.Run("token cache path rejects status three", func(t *testing.T) {
		setupUserControllerTestDB(t)
		user := seedUpdateUserTarget(t, database.MicroPerUSD, 1)
		if err := database.DB.Model(&user).Update("status", 3).Error; err != nil {
			t.Fatalf("set status: %v", err)
		}
		user.Status = 3
		proxy.AddUserToAuthCache(&user)
		t.Cleanup(func() { proxy.EvictUserToken(user.Token) })

		app := fiber.New(fiber.Config{DisableStartupMessage: true})
		app.Use(middleware.UserGuard)
		app.Get("/", func(c *fiber.Ctx) error { return c.SendStatus(204) })
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer "+user.Token)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status=%d, want 403", resp.StatusCode)
		}
	})

	t.Run("controller local user rejects status zero", func(t *testing.T) {
		user := setupTokenControllerTestDB(t)
		user.Status = 0
		app := fiber.New(fiber.Config{DisableStartupMessage: true})
		app.Use(func(c *fiber.Ctx) error {
			c.Locals("user", user)
			return c.Next()
		})
		app.Get("/tokens", GetTokens)
		code, resp := doJSON(t, app, "GET", "/tokens", nil)
		if code != http.StatusUnauthorized || resp["message_code"] != "ERR_BANNED" {
			t.Fatalf("got %d/%v, want 401/ERR_BANNED", code, resp["message_code"])
		}
	})

	t.Run("oauth existing user rejects status three", func(t *testing.T) {
		setupOAuthControllerTestDB(t)
		setOAuthSysConfigForTest(t)
		user := database.User{Username: "oauth_status3", GithubID: "12345", Role: "user", Token: "sk-oauth-status3", Status: 1}
		if err := database.DB.Create(&user).Error; err != nil {
			t.Fatalf("create user: %v", err)
		}
		if err := database.DB.Model(&user).Update("status", 3).Error; err != nil {
			t.Fatalf("set status: %v", err)
		}
		state, verifier := prepareOAuthStateForTest(t)
		installMockGitHub(t, verifier)

		app := fiber.New(fiber.Config{DisableStartupMessage: true})
		app.Post("/callback", GithubCallback)
		resp := postGithubCallback(t, app, "code-ok", state)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("callback status=%d, want 403", resp.StatusCode)
		}
		var got map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if got["message_code"] != "ERR_BANNED" {
			t.Fatalf("message_code=%v, want ERR_BANNED", got["message_code"])
		}
		var sessions int64
		database.DB.Model(&database.UserSession{}).Count(&sessions)
		if sessions != 0 {
			t.Fatalf("sessions=%d, want 0", sessions)
		}
	})
}
