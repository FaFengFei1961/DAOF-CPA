// Package controller / oauth_email_collision_test.go
//
// Phase H-6（2026-05-20）单元测试：跨 provider 邮箱冲突防御。
//
// 验证场景：
//  1. 命中：DAOF 内有 verified 邮箱用户，OAuth 同邮箱 + EmailVerified=true → 409
//  2. bypass：identity.Email == "" → 正常走新建账号路径
//  3. bypass：identity.EmailVerified == false → 正常走新建账号路径（fail-closed 设计）
//  4. bypass：DAOF user 邮箱未验证（email_verified_at IS NULL）→ 正常走新建账号路径
//  5. bypass：DAOF user 已封禁（status != 1）→ 正常走新建账号路径
//  6. 大小写不敏感：DAOF 存 lowercased，provider 给 mixed case 也应命中
//
// Phase H-Audit H-1（2026-05-20）：GitHub provider 收紧 EmailVerified=false（防
// secondary public email 占位攻击）。这些测试因此改用 stub provider，让测试可控
// EmailVerified 字段，不依赖任何真实 provider 的语义。
package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"daof-cpa/database"

	"github.com/gofiber/fiber/v2"
)

// stubOAuthProvider 是测试专用的 OAuth provider，让单测可控
// EmailVerified / Email / ExternalID 三个关键字段。
type stubOAuthProvider struct {
	key      string
	email    string
	verified bool
	extID    string
	username string
}

func (s *stubOAuthProvider) Key() string         { return s.key }
func (s *stubOAuthProvider) IsConfigured() bool  { return true }
func (s *stubOAuthProvider) Exchange(_ context.Context, _, _ string) (*OAuthIdentityData, error) {
	if s.extID == "" {
		return nil, errors.New("stub: extID required")
	}
	return &OAuthIdentityData{
		Provider:      s.key,
		ExternalID:    s.extID,
		Email:         s.email,
		Username:      s.username,
		EmailVerified: s.verified,
	}, nil
}

// registerStubProvider 注册 stub provider 到 OAuthProviderRegistry，
// 并在测试结束时还原（若 key 原本未注册则 delete）。
func registerStubProvider(t *testing.T, stub *stubOAuthProvider) {
	t.Helper()
	old, exists := GetOAuthProvider(stub.key)
	RegisterOAuthProvider(stub)
	t.Cleanup(func() {
		if exists {
			RegisterOAuthProvider(old)
		} else {
			oauthProvidersMu.Lock()
			delete(oauthProviders, stub.key)
			oauthProvidersMu.Unlock()
		}
	})
}

// postStubProviderCallback 走 /callback/:provider 路由触发 OAuthCallback。
func postStubProviderCallback(t *testing.T, app *fiber.App, providerKey, code, state string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost,
		"/callback/"+providerKey+"?code="+code+"&state="+state,
		bytes.NewBufferString(`{"ref":""}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("callback request: %v", err)
	}
	return resp
}

func newStubCollisionApp() *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Post("/callback/:provider", OAuthCallback)
	return app
}

func TestOAuthCallback_EmailCollision_Rejects(t *testing.T) {
	setupOAuthControllerTestDB(t)
	setOAuthSysConfigForTest(t)
	registerStubProvider(t, &stubOAuthProvider{
		key: "stubverif", email: "alice@example.com", verified: true,
		extID: "stub-99001", username: "alice_stub",
	})

	// DAOF 内已有一个 verified 邮箱用户 alice@example.com
	now := time.Now()
	if err := database.DB.Create(&database.User{
		Username: "alice", Role: "user", Token: "sk-alice-existing", Status: 1,
		Email: "alice@example.com", EmailVerifiedAt: &now,
	}).Error; err != nil {
		t.Fatalf("seed existing: %v", err)
	}

	state, _ := prepareOAuthStateForTest(t)
	resp := postStubProviderCallback(t, newStubCollisionApp(), "stubverif", "code-ok", state)
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
	if body["provider"] != "stubverif" {
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
	registerStubProvider(t, &stubOAuthProvider{
		key: "stubnoemail", email: "", verified: false,
		extID: "stub-empty", username: "noemail",
	})

	now := time.Now()
	if err := database.DB.Create(&database.User{
		Username: "alice", Role: "user", Token: "sk-alice", Status: 1,
		Email: "alice@example.com", EmailVerifiedAt: &now,
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	state, _ := prepareOAuthStateForTest(t)
	resp := postStubProviderCallback(t, newStubCollisionApp(), "stubnoemail", "code-ok", state)
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

func TestOAuthCallback_EmailCollision_BypassWhenIdentityUnverified(t *testing.T) {
	// 验证 H-Audit H-1：provider 返 email 但 EmailVerified=false（GitHub 当前默认行为）
	// → 不参与冲突检测，正常走新注册路径
	setupOAuthControllerTestDB(t)
	setOAuthSysConfigForTest(t)
	registerStubProvider(t, &stubOAuthProvider{
		key: "stubunverif", email: "alice@example.com", verified: false,
		extID: "stub-unverif", username: "unverif",
	})

	now := time.Now()
	if err := database.DB.Create(&database.User{
		Username: "alice", Role: "user", Token: "sk-alice", Status: 1,
		Email: "alice@example.com", EmailVerifiedAt: &now,
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	state, _ := prepareOAuthStateForTest(t)
	resp := postStubProviderCallback(t, newStubCollisionApp(), "stubunverif", "code-ok", state)
	if resp.StatusCode == http.StatusConflict {
		t.Fatalf("status=409 (unverified identity should not trigger collision)")
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["action"] != "require_profile_setup" {
		t.Errorf("action=%v, want require_profile_setup", body["action"])
	}
}

func TestOAuthCallback_EmailCollision_BypassWhenDAOFEmailUnverified(t *testing.T) {
	setupOAuthControllerTestDB(t)
	setOAuthSysConfigForTest(t)
	registerStubProvider(t, &stubOAuthProvider{
		key: "stubverif2", email: "alice@example.com", verified: true,
		extID: "stub-99002", username: "alice2",
	})

	// DAOF 内 alice@example.com 但 email_verified_at == NULL
	if err := database.DB.Create(&database.User{
		Username: "alice", Role: "user", Token: "sk-alice", Status: 1,
		Email: "alice@example.com",
		// EmailVerifiedAt: nil
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	state, _ := prepareOAuthStateForTest(t)
	resp := postStubProviderCallback(t, newStubCollisionApp(), "stubverif2", "code-ok", state)
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
	registerStubProvider(t, &stubOAuthProvider{
		key: "stubverif3", email: "alice@example.com", verified: true,
		extID: "stub-99003", username: "alice3",
	})

	now := time.Now()
	if err := database.DB.Create(&database.User{
		Username: "alice_banned", Role: "user", Token: "sk-alice-banned",
		Status: 2, // banned
		Email:  "alice@example.com", EmailVerifiedAt: &now,
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	state, _ := prepareOAuthStateForTest(t)
	resp := postStubProviderCallback(t, newStubCollisionApp(), "stubverif3", "code-ok", state)
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
	registerStubProvider(t, &stubOAuthProvider{
		key: "stubverif4", email: "Alice@Example.COM", verified: true,
		extID: "stub-99004", username: "alice4",
	})

	now := time.Now()
	if err := database.DB.Create(&database.User{
		Username: "alice", Role: "user", Token: "sk-alice", Status: 1,
		Email: "alice@example.com", EmailVerifiedAt: &now,
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	state, _ := prepareOAuthStateForTest(t)
	resp := postStubProviderCallback(t, newStubCollisionApp(), "stubverif4", "code-ok", state)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status=%d, want 409 (case-insensitive match)", resp.StatusCode)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["message_code"] != "ERR_OAUTH_EMAIL_TAKEN_LINK_REQUIRED" {
		t.Errorf("message_code=%v", body["message_code"])
	}
}
