// Package controller / oauth_provider_github_emails_test.go
//
// Phase H-Audit-3（2026-05-21）单元测试：GitHub Exchange 通过 user:email scope
// 调 /user/emails 拿 verified primary email，让 EmailVerified=true 能可靠判定。
//
// 验证：
//   1. /user/emails 返 verified primary → identity.Email 用 primary，EmailVerified=true
//   2. /user/emails 返 unverified primary → fail-soft，EmailVerified=false
//   3. /user/emails 返 verified 但非 primary → fail-soft（必须 primary && verified）
//   4. /user/emails 返 404（用户没授权 user:email scope）→ fail-soft
//   5. /user/emails 返 5xx → fail-soft
//   6. /user.email 与 /user/emails 的 primary 不一致 → 用 primary（更权威）
package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// installMockGitHubWithEmails 类似 installMockGitHub，但允许指定 /user/emails 响应。
// emailsJSON 是 /user/emails 端点返回的 JSON 字符串（emails 数组）；空字符串 = 返 404。
// userEmail 是 /user.email 字段的值（profile public email）。
func installMockGitHubWithEmails(t *testing.T, userEmail string, emailsStatus int, emailsJSON string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login/oauth/access_token":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"gh-test-token"}`))
		case "/user":
			w.Header().Set("Content-Type", "application/json")
			body := `{"id":99001,"login":"octocat"`
			if userEmail != "" {
				body += `,"email":"` + userEmail + `"`
			}
			body += `}`
			_, _ = w.Write([]byte(body))
		case "/user/emails":
			if emailsJSON == "" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(emailsStatus)
			_, _ = w.Write([]byte(emailsJSON))
		default:
			t.Errorf("unexpected GitHub mock path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	oldToken := githubTokenEndpoint
	oldUser := githubUserEndpoint
	oldEmails := githubEmailsEndpoint
	oldClient := oauthHTTPClient
	githubTokenEndpoint = server.URL + "/login/oauth/access_token"
	githubUserEndpoint = server.URL + "/user"
	githubEmailsEndpoint = server.URL + "/user/emails"
	oauthHTTPClient = server.Client()
	t.Cleanup(func() {
		githubTokenEndpoint = oldToken
		githubUserEndpoint = oldUser
		githubEmailsEndpoint = oldEmails
		oauthHTTPClient = oldClient
	})
}

func TestGitHubExchange_VerifiedPrimaryEmail(t *testing.T) {
	setOAuthSysConfigForTest(t)
	installMockGitHubWithEmails(t, "public-secondary@example.com", http.StatusOK,
		`[{"email":"primary@example.com","primary":true,"verified":true},
		  {"email":"secondary@example.com","primary":false,"verified":true},
		  {"email":"unverified-secondary@example.com","primary":false,"verified":false}]`)

	provider := NewGitHubProvider()
	id, err := provider.Exchange(context.Background(), "code-ok", "verifier-not-used")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	// /user/emails 返 verified primary → 用 primary 覆盖 /user.email 的 public secondary
	if id.Email != "primary@example.com" {
		t.Errorf("Email=%q, want primary@example.com (must override public secondary)", id.Email)
	}
	if !id.EmailVerified {
		t.Errorf("EmailVerified=false, want true (verified primary found)")
	}
}

func TestGitHubExchange_UnverifiedPrimary(t *testing.T) {
	// primary 但没 verify → fail-soft，保留 /user.email + EmailVerified=false
	setOAuthSysConfigForTest(t)
	installMockGitHubWithEmails(t, "public@example.com", http.StatusOK,
		`[{"email":"primary@example.com","primary":true,"verified":false}]`)

	provider := NewGitHubProvider()
	id, err := provider.Exchange(context.Background(), "code-ok", "verifier")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	// 没 verified primary → 保留 /user.email 作展示，EmailVerified=false
	if id.Email != "public@example.com" {
		t.Errorf("Email=%q, want public@example.com (fallback to /user.email)", id.Email)
	}
	if id.EmailVerified {
		t.Errorf("EmailVerified=true, want false (primary not verified)")
	}
}

func TestGitHubExchange_NoPrimaryButVerifiedSecondary(t *testing.T) {
	// 没 primary，只有 verified secondary → 拒（必须 primary && verified）
	setOAuthSysConfigForTest(t)
	installMockGitHubWithEmails(t, "public@example.com", http.StatusOK,
		`[{"email":"sec1@example.com","primary":false,"verified":true},
		  {"email":"sec2@example.com","primary":false,"verified":true}]`)

	provider := NewGitHubProvider()
	id, err := provider.Exchange(context.Background(), "code-ok", "verifier")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if id.EmailVerified {
		t.Errorf("EmailVerified=true, want false (no primary email)")
	}
}

func TestGitHubExchange_EmailsScopeNotGranted(t *testing.T) {
	// /user/emails 返 404 → 用户没授 user:email scope → fail-soft
	setOAuthSysConfigForTest(t)
	installMockGitHubWithEmails(t, "public@example.com", http.StatusNotFound, "")

	provider := NewGitHubProvider()
	id, err := provider.Exchange(context.Background(), "code-ok", "verifier")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if id.Email != "public@example.com" {
		t.Errorf("Email=%q, want public@example.com (fallback)", id.Email)
	}
	if id.EmailVerified {
		t.Errorf("EmailVerified=true, want false (scope not granted)")
	}
}

func TestGitHubExchange_EmailsAPI5xx(t *testing.T) {
	// GitHub /user/emails 间歇 5xx → fail-soft，不让 OAuth 整体失败
	setOAuthSysConfigForTest(t)
	installMockGitHubWithEmails(t, "public@example.com", http.StatusServiceUnavailable, "ignored")

	provider := NewGitHubProvider()
	id, err := provider.Exchange(context.Background(), "code-ok", "verifier")
	if err != nil {
		t.Fatalf("Exchange should not fail when /user/emails is 5xx: %v", err)
	}
	if id.EmailVerified {
		t.Errorf("EmailVerified=true, want false (emails API 5xx → fail-soft)")
	}
}

func TestGitHubExchange_EmailsAPIMalformedJSON(t *testing.T) {
	setOAuthSysConfigForTest(t)
	installMockGitHubWithEmails(t, "public@example.com", http.StatusOK, "{not-an-array}")

	provider := NewGitHubProvider()
	id, err := provider.Exchange(context.Background(), "code-ok", "verifier")
	if err != nil {
		t.Fatalf("Exchange should not fail on malformed /user/emails: %v", err)
	}
	if id.EmailVerified {
		t.Errorf("EmailVerified=true, want false (malformed JSON → fail-soft)")
	}
}
