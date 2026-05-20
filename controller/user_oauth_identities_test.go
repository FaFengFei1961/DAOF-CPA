// Package controller / user_oauth_identities_test.go
//
// Phase H-5 单元测试：用户视角的 OAuth identity 管理 API。
package controller

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"daof-cpa/database"
	"daof-cpa/utils"

	"github.com/gofiber/fiber/v2"
)

func newOAuthIdsTestApp(user *database.User) *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error {
		if user != nil {
			c.Locals("user", user)
		}
		return c.Next()
	})
	app.Get("/oauth/identities", GetMyOAuthIdentities)
	app.Post("/oauth/:provider/link/prepare", PrepareOAuthLink)
	app.Post("/oauth/:provider/unlink", UnlinkMyOAuthIdentity)
	return app
}

func doJSONOAuth(t *testing.T, app *fiber.App, method, path string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	_ = json.Unmarshal(body, &out)
	return resp.StatusCode, out
}

func TestGetMyOAuthIdentities_Empty(t *testing.T) {
	setupOAuthControllerTestDB(t)
	user := database.User{Username: "u1", Role: "user", Token: "sk-u1", Status: 1}
	if err := database.DB.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	app := newOAuthIdsTestApp(&user)
	status, body := doJSONOAuth(t, app, http.MethodGet, "/oauth/identities")
	if status != 200 {
		t.Fatalf("status %d body=%v", status, body)
	}
	ids, ok := body["identities"].([]any)
	if !ok {
		t.Fatalf("identities field missing or wrong type: %v", body)
	}
	if len(ids) != 0 {
		t.Errorf("expected empty list, got %d", len(ids))
	}
}

func TestGetMyOAuthIdentities_ListsActive(t *testing.T) {
	setupOAuthControllerTestDB(t)
	user := database.User{Username: "u1", Role: "user", Token: "sk-u1", Status: 1}
	if err := database.DB.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	// 2 active + 1 unlinked
	now := time.Now()
	rows := []database.OAuthIdentity{
		{UserID: user.ID, Provider: database.OAuthProviderGitHub, ExternalID: "gh-1", LinkedAt: now.Add(-2 * time.Hour)},
		{UserID: user.ID, Provider: database.OAuthProviderGoogle, ExternalID: "go-1", LinkedAt: now.Add(-1 * time.Hour)},
	}
	if err := database.DB.Create(&rows).Error; err != nil {
		t.Fatalf("seed identities: %v", err)
	}
	// 一条已 unlinked 的不应出现在响应里
	unlinkedTime := now
	unlinkedRow := database.OAuthIdentity{
		UserID: user.ID, Provider: "discord", ExternalID: "ds-old", LinkedAt: now.Add(-3 * time.Hour), UnlinkedAt: &unlinkedTime,
	}
	if err := database.DB.Create(&unlinkedRow).Error; err != nil {
		t.Fatalf("seed unlinked: %v", err)
	}

	app := newOAuthIdsTestApp(&user)
	status, body := doJSONOAuth(t, app, http.MethodGet, "/oauth/identities")
	if status != 200 {
		t.Fatalf("status %d body=%v", status, body)
	}
	ids, _ := body["identities"].([]any)
	if len(ids) != 2 {
		t.Fatalf("expected 2 active identities, got %d (body=%v)", len(ids), body)
	}
}

func TestUnlinkMyOAuthIdentity_RejectsLast(t *testing.T) {
	setupOAuthControllerTestDB(t)
	// 用户只有 1 个 OAuth identity 且没有 email/phone → 解绑这唯一身份必须拒
	user := database.User{Username: "lonely", Role: "user", Token: "sk-lonely", Status: 1}
	if err := database.DB.Create(&user).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := database.DB.Create(&database.OAuthIdentity{
		UserID: user.ID, Provider: database.OAuthProviderGitHub, ExternalID: "gh-only", LinkedAt: time.Now(),
	}).Error; err != nil {
		t.Fatalf("seed identity: %v", err)
	}
	app := newOAuthIdsTestApp(&user)
	status, body := doJSONOAuth(t, app, http.MethodPost, "/oauth/github/unlink")
	if status != 409 {
		t.Fatalf("status %d (want 409), body=%v", status, body)
	}
	if body["message_code"] != "ERR_CANNOT_UNLINK_LAST_AUTH" {
		t.Errorf("msg = %v", body)
	}
	// DB 应仍有 active identity
	var n int64
	database.DB.Model(&database.OAuthIdentity{}).
		Where("user_id = ? AND unlinked_at IS NULL", user.ID).Count(&n)
	if n != 1 {
		t.Errorf("identity 不应被解绑，still active count=%d", n)
	}
}

func TestUnlinkMyOAuthIdentity_AllowsWhenOtherAuthMethodPresent(t *testing.T) {
	setupOAuthControllerTestDB(t)
	utils.InitCrypto()
	now := time.Now()
	// 用户有：GitHub + Google identity + 邮箱密码 → 解绑 GitHub 通过（剩 google + email/pwd 满足）
	user := database.User{
		Username:        "rich",
		Role:            "user",
		Token:           "sk-rich",
		Status:          1,
		Email:           "rich@example.com",
		EmailVerifiedAt: &now,
		PasswordHash:    utils.GenerateHashForTest("passw0rd"),
	}
	if err := database.DB.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := database.DB.Create(&database.OAuthIdentity{
		UserID: user.ID, Provider: database.OAuthProviderGitHub, ExternalID: "gh-A", LinkedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed gh: %v", err)
	}
	if err := database.DB.Create(&database.OAuthIdentity{
		UserID: user.ID, Provider: database.OAuthProviderGoogle, ExternalID: "go-A", LinkedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed go: %v", err)
	}
	app := newOAuthIdsTestApp(&user)
	status, body := doJSONOAuth(t, app, http.MethodPost, "/oauth/github/unlink")
	if status != 200 {
		t.Fatalf("status %d body=%v", status, body)
	}
	if body["message_code"] != "SUCCESS_OAUTH_UNLINKED" {
		t.Errorf("msg = %v", body)
	}
	// GitHub 行 unlinked_at != nil；Google 仍 active
	var ghRow database.OAuthIdentity
	if err := database.DB.Where("user_id = ? AND provider = ?", user.ID, database.OAuthProviderGitHub).First(&ghRow).Error; err != nil {
		t.Fatalf("query gh row: %v", err)
	}
	if ghRow.UnlinkedAt == nil {
		t.Error("GitHub identity should be unlinked")
	}
	var goN int64
	database.DB.Model(&database.OAuthIdentity{}).
		Where("user_id = ? AND provider = ? AND unlinked_at IS NULL", user.ID, database.OAuthProviderGoogle).Count(&goN)
	if goN != 1 {
		t.Errorf("Google identity should remain active, got %d", goN)
	}
}

func TestUnlinkMyOAuthIdentity_AllowsWhenPhoneBound(t *testing.T) {
	setupOAuthControllerTestDB(t)
	// 用户只有 GitHub identity，但绑了手机号 → 解绑 GitHub 通过（phone 兜底）
	user := database.User{Username: "p", Role: "user", Token: "sk-p", Status: 1, Phone: "13800000000"}
	if err := database.DB.Create(&user).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := database.DB.Create(&database.OAuthIdentity{
		UserID: user.ID, Provider: database.OAuthProviderGitHub, ExternalID: "gh-p", LinkedAt: time.Now(),
	}).Error; err != nil {
		t.Fatalf("seed identity: %v", err)
	}
	app := newOAuthIdsTestApp(&user)
	status, _ := doJSONOAuth(t, app, http.MethodPost, "/oauth/github/unlink")
	if status != 200 {
		t.Errorf("status %d (phone bound should allow unlink)", status)
	}
}

func TestUnlinkMyOAuthIdentity_NotFound(t *testing.T) {
	setupOAuthControllerTestDB(t)
	user := database.User{Username: "u", Role: "user", Token: "sk-u", Status: 1, Phone: "13800000000"}
	if err := database.DB.Create(&user).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	app := newOAuthIdsTestApp(&user)
	status, body := doJSONOAuth(t, app, http.MethodPost, "/oauth/github/unlink")
	if status != 404 {
		t.Fatalf("status %d body=%v", status, body)
	}
	if body["message_code"] != "ERR_OAUTH_IDENTITY_NOT_FOUND" {
		t.Errorf("msg = %v", body)
	}
}

func TestPrepareOAuthLink_AlreadyLinkedRejected(t *testing.T) {
	setupOAuthControllerTestDB(t)
	setOAuthSysConfigForTest(t)
	user := database.User{Username: "u", Role: "user", Token: "sk-u", Status: 1, Phone: "13800000000"}
	if err := database.DB.Create(&user).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	// 用户已绑 GitHub
	if err := database.DB.Create(&database.OAuthIdentity{
		UserID: user.ID, Provider: database.OAuthProviderGitHub, ExternalID: "gh-x", LinkedAt: time.Now(),
	}).Error; err != nil {
		t.Fatalf("seed identity: %v", err)
	}
	app := newOAuthIdsTestApp(&user)
	status, body := doJSONOAuth(t, app, http.MethodPost, "/oauth/github/link/prepare")
	if status != 409 {
		t.Fatalf("status %d body=%v", status, body)
	}
	if body["message_code"] != "ERR_OAUTH_PROVIDER_ALREADY_LINKED" {
		t.Errorf("msg = %v", body)
	}
}

func TestPrepareOAuthLink_ReturnsStateAndChallenge(t *testing.T) {
	setupOAuthControllerTestDB(t)
	setOAuthSysConfigForTest(t)
	user := database.User{Username: "u", Role: "user", Token: "sk-u", Status: 1, Phone: "13800000000"}
	if err := database.DB.Create(&user).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	app := newOAuthIdsTestApp(&user)
	status, body := doJSONOAuth(t, app, http.MethodPost, "/oauth/github/link/prepare")
	if status != 200 {
		t.Fatalf("status %d body=%v", status, body)
	}
	if body["state"] == "" || body["code_challenge"] == "" {
		t.Errorf("missing state/challenge: %v", body)
	}
	if body["code_challenge_method"] != "S256" {
		t.Errorf("challenge method = %v", body["code_challenge_method"])
	}
}

func TestPrepareOAuthLink_UnknownProvider(t *testing.T) {
	setupOAuthControllerTestDB(t)
	setOAuthSysConfigForTest(t)
	user := database.User{Username: "u", Role: "user", Token: "sk-u", Status: 1}
	if err := database.DB.Create(&user).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	app := newOAuthIdsTestApp(&user)
	status, body := doJSONOAuth(t, app, http.MethodPost, "/oauth/wechat/link/prepare")
	if status != 400 {
		t.Fatalf("status %d body=%v", status, body)
	}
	if body["message_code"] != "ERR_OAUTH_PROVIDER_UNKNOWN" {
		t.Errorf("msg = %v", body)
	}
}
