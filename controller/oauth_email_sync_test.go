// Package controller / oauth_email_sync_test.go
//
// Phase H-Audit-2（2026-05-21）单元测试：OAuth 注册时 sync email → user 表。
//
// 验证场景（H-6 反向防御补强）：
//  1. Profile-setup 路径：identity.EmailVerified=true → user.email + EmailVerifiedAt 自动填充
//  2. Profile-setup 路径：identity.EmailVerified=false（GitHub 默认）→ user.email 仍为空
//  3. Profile-setup 路径：identity.Email="" → user.email 仍为空
//  4. applyVerifiedEmailFromIdentity 单元逻辑：normalize lowercase + trim
//  5. tmp_token 8 段格式 round-trip：build → encrypt → decrypt → parse 还原全部字段
//
// 注意：完整 e2e（OAuthCallback → tmp_token → CompleteProfile → user.email）的场景
// 涉及多个 mock，太重；这里聚焦"applyVerifiedEmailFromIdentity 在注册路径被调用，
// 结果落到 user.email"。OAuthCallback → tmp_token 那一段已被 H-6 collision 套件覆盖。
package controller

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"daof-cpa/database"
	"daof-cpa/proxy"
	"daof-cpa/utils"

	"github.com/gofiber/fiber/v2"
)

// newJSONCompleteProfileApp 建一个 fiber app 仅挂 CompleteProfile，方便整 e2e。
func newJSONCompleteProfileApp() *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Post("/complete-profile", CompleteProfile)
	return app
}

// newJSONPostBytes 构造一个 POST JSON 请求。
func newJSONPostBytes(t *testing.T, path, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

// disableSignupBonusForTest 把 signup_bonus 设为 0，避免测试 DB 缺 billing_entries 表
// 时 CompleteProfile 写 billing 失败。
func disableSignupBonusForTest(t *testing.T) {
	t.Helper()
	proxy.SysConfigMutex.Lock()
	old := proxy.SysConfigCache
	cfg := make(map[string]string, len(old)+1)
	for k, v := range old {
		cfg[k] = v
	}
	cfg["signup_bonus"] = "0"
	proxy.SysConfigCache = cfg
	proxy.SysConfigMutex.Unlock()
	t.Cleanup(func() {
		proxy.SysConfigMutex.Lock()
		proxy.SysConfigCache = old
		proxy.SysConfigMutex.Unlock()
	})
}

// TestCompleteProfile_SyncsVerifiedEmailToUserTable e2e：模拟 OAuth 回调返
// identity{Email=alice@example.com, EmailVerified=true} → CompleteProfile 取名 →
// 验证 user.email + email_verified_at 自动填充（H-Audit-2 反向 H-6 漏洞补强）。
func TestCompleteProfile_SyncsVerifiedEmailToUserTable(t *testing.T) {
	setupOAuthControllerTestDB(t)
	setOAuthSysConfigForTest(t)
	disableSignupBonusForTest(t)
	utils.InitCrypto()

	// 构造一个新 8 段 tmp_token：Google 风格 verified email
	payload := buildOAuthTmpTokenPayload("clean", database.OAuthProviderGoogle, "google-sub-aaa", "alice", "",
		"alice@example.com", true)
	tmpToken, err := utils.Encrypt(payload)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	app := newJSONCompleteProfileApp()
	body := `{"tmp_token":"` + tmpToken + `","username":"alice"}`
	req := newJSONPostBytes(t, "/complete-profile", body)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}

	// 校验 user.email 已写入 + email_verified_at 非 nil
	var u database.User
	if err := database.DB.Where("username = ?", "alice").First(&u).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	if u.Email != "alice@example.com" {
		t.Errorf("user.Email=%q, want 'alice@example.com'", u.Email)
	}
	if u.EmailVerifiedAt == nil {
		t.Errorf("user.EmailVerifiedAt should be set (verified email from identity)")
	}
}

// TestCompleteProfile_UnverifiedEmailNotSynced 验证 unverified identity 不污染 user.email
// （GitHub 当前 H-1 后默认 EmailVerified=false，所以 GitHub OAuth 注册不应触发 sync）。
func TestCompleteProfile_UnverifiedEmailNotSynced(t *testing.T) {
	setupOAuthControllerTestDB(t)
	setOAuthSysConfigForTest(t)
	disableSignupBonusForTest(t)
	utils.InitCrypto()

	payload := buildOAuthTmpTokenPayload("clean", database.OAuthProviderGitHub, "gh-bbb", "bob", "",
		"bob@example.com", false) // verified=false
	tmpToken, err := utils.Encrypt(payload)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	app := newJSONCompleteProfileApp()
	body := `{"tmp_token":"` + tmpToken + `","username":"bob"}`
	req := newJSONPostBytes(t, "/complete-profile", body)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}

	var u database.User
	if err := database.DB.Where("username = ?", "bob").First(&u).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	if u.Email != "" {
		t.Errorf("user.Email=%q, want empty (unverified should skip sync)", u.Email)
	}
	if u.EmailVerifiedAt != nil {
		t.Errorf("user.EmailVerifiedAt should remain nil for unverified identity")
	}
}

// TestApplyVerifiedEmailFromIdentity_VerifiedEmail 等单元测试见下。
func TestApplyVerifiedEmailFromIdentity_VerifiedEmail(t *testing.T) {
	utils.InitCrypto()
	user := database.User{Username: "alice"}
	applyVerifiedEmailFromIdentity(&user, "Alice@Example.COM", true)
	if user.Email != "alice@example.com" {
		t.Errorf("Email = %q, want normalized lowercase 'alice@example.com'", user.Email)
	}
	if user.EmailVerifiedAt == nil {
		t.Errorf("EmailVerifiedAt should be set to now, got nil")
	}
}

func TestApplyVerifiedEmailFromIdentity_UnverifiedSkipsWrite(t *testing.T) {
	user := database.User{Username: "alice"}
	applyVerifiedEmailFromIdentity(&user, "alice@example.com", false)
	if user.Email != "" {
		t.Errorf("Email = %q, want empty (unverified should skip write)", user.Email)
	}
	if user.EmailVerifiedAt != nil {
		t.Errorf("EmailVerifiedAt should remain nil for unverified identity")
	}
}

func TestApplyVerifiedEmailFromIdentity_EmptyEmailSkipsWrite(t *testing.T) {
	user := database.User{Username: "alice"}
	applyVerifiedEmailFromIdentity(&user, "", true)
	if user.Email != "" {
		t.Errorf("Email = %q, want empty (no email to write)", user.Email)
	}
	if user.EmailVerifiedAt != nil {
		t.Errorf("EmailVerifiedAt should remain nil when email is empty")
	}
}

func TestApplyVerifiedEmailFromIdentity_NilUserNoOp(t *testing.T) {
	// 不应 panic
	applyVerifiedEmailFromIdentity(nil, "alice@example.com", true)
}

func TestTmpToken8SegmentRoundTrip(t *testing.T) {
	utils.InitCrypto()
	cases := []struct {
		name          string
		tokenType     string
		provider      string
		externalID    string
		username      string
		ref           string
		email         string
		emailVerified bool
	}{
		{
			name:          "clean google verified",
			tokenType:     "clean",
			provider:      "google",
			externalID:    "google-sub-123",
			username:      "alice",
			ref:           "",
			email:         "alice@example.com",
			emailVerified: true,
		},
		{
			name:          "sms github unverified empty email",
			tokenType:     "sms",
			provider:      "github",
			externalID:    "12345",
			username:      "octo",
			ref:           "promoter",
			email:         "",
			emailVerified: false,
		},
		{
			name:          "clean with ref",
			tokenType:     "clean",
			provider:      "google",
			externalID:    "g-abc",
			username:      "bob",
			ref:           "alice",
			email:         "bob@example.com",
			emailVerified: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := buildOAuthTmpTokenPayload(tc.tokenType, tc.provider, tc.externalID, tc.username, tc.ref, tc.email, tc.emailVerified)
			encrypted, err := utils.Encrypt(payload)
			if err != nil {
				t.Fatalf("encrypt: %v", err)
			}
			gotType, gotRef, decryptedStr, err := parseTmpToken(encrypted)
			if err != nil {
				t.Fatalf("parseTmpToken: %v", err)
			}
			if gotType != tc.tokenType {
				t.Errorf("tokenType=%q, want %q", gotType, tc.tokenType)
			}
			if gotRef != tc.ref {
				t.Errorf("refUser=%q, want %q", gotRef, tc.ref)
			}
			provider, extID, username, email, verified := parseOAuthTmpTokenParts(decryptedStr)
			if provider != tc.provider {
				t.Errorf("provider=%q, want %q", provider, tc.provider)
			}
			if extID != tc.externalID {
				t.Errorf("externalID=%q, want %q", extID, tc.externalID)
			}
			if username != tc.username {
				t.Errorf("username=%q, want %q", username, tc.username)
			}
			if email != tc.email {
				t.Errorf("email=%q, want %q", email, tc.email)
			}
			if verified != tc.emailVerified {
				t.Errorf("emailVerified=%v, want %v", verified, tc.emailVerified)
			}
		})
	}
}

func TestTmpToken6SegmentBackwardCompat(t *testing.T) {
	// 老 6 段格式不再被新 build 函数生成，但 parseTmpToken 仍接受（如有遗留 token TTL 内的）。
	// 用当前时间戳模拟"刚发出来的旧格式 token"。
	utils.InitCrypto()
	old6Seg := buildOldFormat6Segment("clean", "google", "ext-123", "alice", "")
	enc, err := utils.Encrypt(old6Seg)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	gotType, gotRef, decryptedStr, err := parseTmpToken(enc)
	if err != nil {
		t.Fatalf("parseTmpToken 6-seg: %v", err)
	}
	if gotType != "clean" {
		t.Errorf("tokenType=%q", gotType)
	}
	if gotRef != "" {
		t.Errorf("ref=%q, want empty", gotRef)
	}
	// 6 段时 email + verified 应为空 / false（向后兼容默认）
	_, _, _, email, verified := parseOAuthTmpTokenParts(decryptedStr)
	if email != "" {
		t.Errorf("email=%q, want empty for 6-segment token", email)
	}
	if verified {
		t.Errorf("verified=true, want false for 6-segment token (default)")
	}
}

// buildOldFormat6Segment 重现旧 6 段格式（仅测试用，模拟 H-3 老 token）。
func buildOldFormat6Segment(tokenType, provider, externalID, username, ref string) string {
	return strings.Join([]string{
		tokenType,
		provider,
		externalID,
		username,
		ref,
		fmt.Sprintf("%d", time.Now().Unix()),
	}, "|")
}
