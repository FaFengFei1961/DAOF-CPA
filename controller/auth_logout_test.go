package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"daof-ai-hub/database"
	"daof-ai-hub/middleware"
	"daof-ai-hub/proxy"

	"github.com/gofiber/fiber/v2"
)

func TestAuthLogout_EvictsAuthCache(t *testing.T) {
	setupOAuthControllerTestDB(t)
	user := database.User{Username: "logout_cache_user", Role: "user", Token: "sk-daof-logout-cache", Status: 1}
	if err := database.DB.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	proxy.EvictUserToken(user.Token)
	t.Cleanup(func() { proxy.EvictUserToken(user.Token) })
	proxy.AddUserToAuthCache(&user)
	if got := proxy.LookupUserByToken(user.Token); got == nil {
		t.Fatal("expected user in AuthCache before logout")
	}

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Post("/logout", middleware.UserGuard, AuthLogout)

	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	req.Header.Set("Authorization", "Bearer "+user.Token)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("logout request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("logout status=%d", resp.StatusCode)
	}
	if got := proxy.LookupUserByToken(user.Token); got != nil {
		t.Fatalf("expected AuthCache eviction after logout, got user id=%d", got.ID)
	}
}
