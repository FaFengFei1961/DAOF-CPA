// Package controller / oauth_email_collision_test.go
//
// Phase H-6（2026-05-20）单元测试：跨 provider 邮箱冲突防御。
//
// 验证场景：
//   1. 命中：DAOF 内有 verified 邮箱用户，OAuth 同邮箱 → 409 + ERR_OAUTH_EMAIL_TAKEN_LINK_REQUIRED
//   2. bypass：identity.Email == "" → 正常走新建账号路径
//   3. bypass：identity.EmailVerified == false → 正常走新建账号路径
//   4. bypass：DAOF user 邮箱未验证（email_verified_at IS NULL）→ 正常走新建账号路径
//   5. bypass：DAOF user 已封禁（status != 1）→ 正常走新建账号路径
//   6. 大小写不敏感：DAOF 存 lowercased，provider 给 mixed case 也应命中
package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"daof-cpa/database"

	"github.com/gofiber/fiber/v2"
)

// installMockGitHubWithEmail 与 installMockGitHub 一样，但允许 mock /user 返回指定的 email。
// 用于验证邮箱冲突路径，因为基础 installMockGitHub 不返 email 字段。
func installMockGitHubWithEmail(t *testing.T, expectedVerifier, mockEmail string) *atomic.Int64 {
	t.Helper()
	var tokenHits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login/oauth/access_token":
			tokenHits.Add(1)
			var payload map[string]string
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("decode token request: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if payload["code_verifier"] != expectedVerifier {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"bad_verifier"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"github-access"}`))
		case "/user":
			w.Header().Set("Content-Type", "application/json")
			// 真实 GitHub /user 在用户公开了 primary email 时会返回 email 字段
			body := map[string]any{
				"id":    int64(99001),
				"login": "newcomer",
				"email": mockEmail,
			}
			out, _ := json.Marshal(body)
			_, _ = w.Write(out)
		default:
			t.Errorf("unexpected GitHub mock path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	oldTokenEndpoint := githubTokenEndpoint
	oldUserEndpoint := githubUserEndpoint
	oldClient := githubHTTPClient
	githubTokenEndpoint = server.URL + "/login/oauth/access_token"
	githubUserEndpoint = server.URL + "/user"
	githubHTTPClient = server.Client()
	t.Cleanup(func() {
		githubTokenEndpoint = oldTokenEndpoint
		githubUserEndpoint = oldUserEndpoint
		githubHTTPClient = oldClient
	})
	return &tokenHits
}

func TestOAuthCallback_EmailCollision_Rejects(t *testing.T) {
	setupOAuthControllerTestDB(t)
	setOAuthSysConfigForTest(t)

	// 场景：DAOF 内已经有一个 verified 邮箱用户 alice@example.com
	now := time.Now()
	existing := database.User{
		Username:        "alice",
		Role:            "user",
		Token:           "sk-alice-existing",
		Status:          1,
		Email:           "alice@example.com",
		EmailVerifiedAt: &now,
	}
	if err := database.DB.Create(&existing).Error; err != nil {
		t.Fatalf("seed existing: %v", err)
	}

	// GitHub OAuth 现在返同邮箱 → 应拒
	state, verifier := prepareOAuthStateForTest(t)
	installMockGitHubWithEmail(t, verifier, "alice@example.com")

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Post("/callback", GithubCallback)
	resp := postGithubCallback(t, app, "code-ok", state)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status=%d, want 409", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["message_code"] != "ERR_OAUTH_EMAIL_TAKEN_LINK_REQUIRED" {
		t.Errorf("message_code=%v, want ERR_OAUTH_EMAIL_TAKEN_LINK_REQUIRED", body["message_code"])
	}
	if body["provider"] != "github" {
		t.Errorf("provider=%v", body["provider"])
	}
	hint, _ := body["email_hint"].(string)
	if !strings.Contains(hint, "***") || !strings.Contains(hint, "@example.com") {
		t.Errorf("email_hint=%q, want masked form", hint)
	}
	// 不应建新用户
	var n int64
	database.DB.Model(&database.User{}).Count(&n)
	if n != 1 {
		t.Errorf("user count=%d, want 1 (no new user)", n)
	}
	// 不应建 oauth_identities 行
	var ids int64
	database.DB.Model(&database.OAuthIdentity{}).Count(&ids)
	if ids != 0 {
		t.Errorf("oauth_identities count=%d, want 0", ids)
	}
	// 应写一条 OAUTH_EMAIL_COLLISION_BLOCKED 操作日志
	var logs int64
	database.DB.Model(&database.OperationLog{}).
		Where("action_type = ?", "OAUTH_EMAIL_COLLISION_BLOCKED").Count(&logs)
	if logs != 1 {
		t.Errorf("collision audit log count=%d, want 1", logs)
	}
}

func TestOAuthCallback_EmailCollision_BypassWhenIdentityEmailEmpty(t *testing.T) {
	setupOAuthControllerTestDB(t)
	setOAuthSysConfigForTest(t)

	// DAOF 内已有 alice@example.com 但 provider 返空 email → 不构成冲突
	now := time.Now()
	if err := database.DB.Create(&database.User{
		Username:        "alice",
		Role:            "user",
		Token:           "sk-alice",
		Status:          1,
		Email:           "alice@example.com",
		EmailVerifiedAt: &now,
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	state, verifier := prepareOAuthStateForTest(t)
	installMockGitHub(t, verifier) // 这个 mock 不返 email

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Post("/callback", GithubCallback)
	resp := postGithubCallback(t, app, "code-ok", state)
	// 应正常走新用户分支 → 返回 require_profile_setup（非 409）
	if resp.StatusCode == http.StatusConflict {
		t.Fatalf("status=409 (collision falsely triggered)")
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["action"] != "require_profile_setup" {
		t.Errorf("action=%v, want require_profile_setup", body["action"])
	}
}

func TestOAuthCallback_EmailCollision_BypassWhenDAOFEmailUnverified(t *testing.T) {
	setupOAuthControllerTestDB(t)
	setOAuthSysConfigForTest(t)

	// DAOF 内 alice@example.com 但 email_verified_at == NULL（绑了邮箱但没点验证链接）
	// → 不构成冲突（恶意 attacker 可绑别人邮箱占位，必须 verified 才算 ground truth）
	if err := database.DB.Create(&database.User{
		Username: "alice",
		Role:     "user",
		Token:    "sk-alice",
		Status:   1,
		Email:    "alice@example.com",
		// EmailVerifiedAt: nil
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	state, verifier := prepareOAuthStateForTest(t)
	installMockGitHubWithEmail(t, verifier, "alice@example.com")

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Post("/callback", GithubCallback)
	resp := postGithubCallback(t, app, "code-ok", state)
	if resp.StatusCode == http.StatusConflict {
		t.Fatalf("status=409 (unverified DAOF email triggered collision)")
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["action"] != "require_profile_setup" {
		t.Errorf("action=%v, want require_profile_setup", body["action"])
	}
}

func TestOAuthCallback_EmailCollision_BypassWhenDAOFUserBanned(t *testing.T) {
	setupOAuthControllerTestDB(t)
	setOAuthSysConfigForTest(t)

	// DAOF 内 alice@example.com 已 verified 但用户被封禁（status=2）
	// → 不算"有效占用"，新用户可以走正常路径建账号
	now := time.Now()
	if err := database.DB.Create(&database.User{
		Username:        "alice_banned",
		Role:            "user",
		Token:           "sk-alice-banned",
		Status:          2, // banned
		Email:           "alice@example.com",
		EmailVerifiedAt: &now,
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	state, verifier := prepareOAuthStateForTest(t)
	installMockGitHubWithEmail(t, verifier, "alice@example.com")

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Post("/callback", GithubCallback)
	resp := postGithubCallback(t, app, "code-ok", state)
	if resp.StatusCode == http.StatusConflict {
		t.Fatalf("status=409 (banned user falsely blocked new register)")
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["action"] != "require_profile_setup" {
		t.Errorf("action=%v, want require_profile_setup", body["action"])
	}
}

func TestOAuthCallback_EmailCollision_CaseInsensitive(t *testing.T) {
	setupOAuthControllerTestDB(t)
	setOAuthSysConfigForTest(t)

	// DAOF 存的是 lowercase（G-1 规范化），provider 给 mixed case
	now := time.Now()
	if err := database.DB.Create(&database.User{
		Username:        "alice",
		Role:            "user",
		Token:           "sk-alice",
		Status:          1,
		Email:           "alice@example.com",
		EmailVerifiedAt: &now,
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	state, verifier := prepareOAuthStateForTest(t)
	installMockGitHubWithEmail(t, verifier, "Alice@Example.COM")

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Post("/callback", GithubCallback)
	resp := postGithubCallback(t, app, "code-ok", state)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status=%d, want 409 (case-insensitive match)", resp.StatusCode)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["message_code"] != "ERR_OAUTH_EMAIL_TAKEN_LINK_REQUIRED" {
		t.Errorf("message_code=%v", body["message_code"])
	}
}
