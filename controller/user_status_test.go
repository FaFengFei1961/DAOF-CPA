package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"daof-cpa/database"
	"daof-cpa/middleware"
	"daof-cpa/proxy"

	"github.com/gofiber/fiber/v2"
)

func TestUserStatus_OnlyRejectsStatusTwo(t *testing.T) {
	t.Run("session path rejects status two", func(t *testing.T) {
		setupUserControllerTestDB(t)
		user := seedUpdateUserTarget(t, database.MicroPerUSD, 1)
		if err := database.DB.Model(&user).Update("status", 2).Error; err != nil {
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

	t.Run("session path allows status zero", func(t *testing.T) {
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
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("status=%d, want 204", resp.StatusCode)
		}
	})

	t.Run("token cache path allows status three", func(t *testing.T) {
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
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("status=%d, want 204", resp.StatusCode)
		}
	})

	t.Run("controller local user allows status zero", func(t *testing.T) {
		user := setupTokenControllerTestDB(t)
		user.Status = 0
		app := fiber.New(fiber.Config{DisableStartupMessage: true})
		app.Use(func(c *fiber.Ctx) error {
			c.Locals("user", user)
			return c.Next()
		})
		app.Get("/tokens", GetTokens)
		code, resp := doJSON(t, app, "GET", "/tokens", nil)
		if code != http.StatusOK || resp["success"] != true {
			t.Fatalf("got %d/%v, want 200/success", code, resp)
		}
	})

	t.Run("oauth existing user status two gets appeal session", func(t *testing.T) {
		setupOAuthControllerTestDB(t)
		setOAuthSysConfigForTest(t)
		user := database.User{Username: "oauth_status2", Role: "user", Token: "sk-oauth-status2", Status: 1}
		if err := database.DB.Create(&user).Error; err != nil {
			t.Fatalf("create user: %v", err)
		}
		// H-3：oauth_identities 是 lookup 真相
		if err := database.DB.Create(&database.OAuthIdentity{
			UserID: user.ID, Provider: database.OAuthProviderGitHub, ExternalID: "12345", LinkedAt: time.Now(),
		}).Error; err != nil {
			t.Fatalf("create oauth identity: %v", err)
		}
		if err := database.DB.Model(&user).Update("status", 2).Error; err != nil {
			t.Fatalf("set status: %v", err)
		}
		state, verifier := prepareOAuthStateForTest(t)
		installMockGitHub(t, verifier)

		app := fiber.New(fiber.Config{DisableStartupMessage: true})
		app.Post("/callback/:provider", OAuthCallback)
		resp := postGithubCallback(t, app, "code-ok", state)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("callback status=%d, want 200", resp.StatusCode)
		}
		var got map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if got["success"] != true {
			t.Fatalf("success=%v, want true", got["success"])
		}
		if got["account_status"] != float64(2) {
			t.Fatalf("account_status=%v, want 2", got["account_status"])
		}
		sessionID, ok := got["session_id"].(string)
		if !ok || !database.IsSessionID(sessionID) {
			t.Fatalf("session_id=%v, want browser session", got["session_id"])
		}
		var sessions int64
		database.DB.Model(&database.UserSession{}).Count(&sessions)
		if sessions != 1 {
			t.Fatalf("sessions=%d, want 1", sessions)
		}

		guardApp := fiber.New(fiber.Config{DisableStartupMessage: true})
		guardApp.Use(middleware.UserGuardAllowBanned)
		guardApp.Get("/appeal", func(c *fiber.Ctx) error {
			if c.Locals("user_banned") != true {
				t.Fatalf("user_banned local=%v, want true", c.Locals("user_banned"))
			}
			return c.SendStatus(http.StatusNoContent)
		})
		req := httptest.NewRequest(http.MethodGet, "/appeal", nil)
		req.Header.Set("Authorization", "Bearer "+sessionID)
		appealResp, err := guardApp.Test(req)
		if err != nil {
			t.Fatalf("appeal request: %v", err)
		}
		if appealResp.StatusCode != http.StatusNoContent {
			t.Fatalf("appeal status=%d, want 204", appealResp.StatusCode)
		}
	})

	t.Run("oauth existing user allows status three", func(t *testing.T) {
		setupOAuthControllerTestDB(t)
		setOAuthSysConfigForTest(t)
		user := database.User{Username: "oauth_status3", Role: "user", Token: "sk-oauth-status3", Status: 1}
		if err := database.DB.Create(&user).Error; err != nil {
			t.Fatalf("create user: %v", err)
		}
		// H-3：oauth_identities 是 lookup 真相
		if err := database.DB.Create(&database.OAuthIdentity{
			UserID: user.ID, Provider: database.OAuthProviderGitHub, ExternalID: "12345", LinkedAt: time.Now(),
		}).Error; err != nil {
			t.Fatalf("create oauth identity: %v", err)
		}
		if err := database.DB.Model(&user).Update("status", 3).Error; err != nil {
			t.Fatalf("set status: %v", err)
		}
		state, verifier := prepareOAuthStateForTest(t)
		installMockGitHub(t, verifier)

		app := fiber.New(fiber.Config{DisableStartupMessage: true})
		app.Post("/callback/:provider", OAuthCallback)
		resp := postGithubCallback(t, app, "code-ok", state)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("callback status=%d, want 200", resp.StatusCode)
		}
		var sessions int64
		database.DB.Model(&database.UserSession{}).Count(&sessions)
		if sessions != 1 {
			t.Fatalf("sessions=%d, want 1", sessions)
		}
	})
}
