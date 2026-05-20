// Package controller / oauth_provider_google.go
//
// Google OAuth provider 适配器。Phase H-4（2026-05-20）。
//
// 实现 OAuthProvider interface。和 GitHubProvider 同范式，只是 endpoints +
// 字段名不同：
//
//   Token endpoint    https://oauth2.googleapis.com/token
//   UserInfo endpoint https://openidconnect.googleapis.com/v1/userinfo
//   Scope             "openid email profile"
//
// 必需的 SysConfig：
//   - google_client_id
//   - google_client_secret
//   - server_address（用于 redirect_uri = {server_address}/oauth/google）
//
// 前端流程对应：
//   1. 用户点"用 Google 登录"
//   2. POST /api/auth/oauth/google/prepare → 拿 state + code_challenge
//   3. 跳转 https://accounts.google.com/o/oauth2/v2/auth?client_id=...&redirect_uri={server_address}/oauth/google&...
//   4. Google 回跳到 {server_address}/oauth/google?code=...&state=...
//   5. 前端读 code+state 后 POST /api/auth/oauth/google/callback → 服务端 OAuthCallback
//
// 安全：
//   - 用 proxy.SafeTransport + RedirectGuard，复用 githubHTTPClient（同 SSRF 防护）
//   - email_verified=false 的 Google 账号在 OAuthIdentityData 里如实标 false，
//     让 H-3 跨 provider 冲突检测可以拒绝
//   - access_token 不持久化（同 GitHub adapter）
package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"

	"daof-cpa/database"
	"daof-cpa/proxy"
)

// Google OAuth endpoint 常量。声明为 var 让测试可以 mock（同 githubTokenEndpoint pattern）。
var (
	googleTokenEndpoint    = "https://oauth2.googleapis.com/token"
	googleUserInfoEndpoint = "https://openidconnect.googleapis.com/v1/userinfo"
	googleAuthorizeBase    = "https://accounts.google.com/o/oauth2/v2/auth"
)

// GoogleProvider OAuth adapter。
type GoogleProvider struct{}

// NewGoogleProvider 构造 default Google adapter。
func NewGoogleProvider() *GoogleProvider { return &GoogleProvider{} }

// Key 返回 "google"。
func (p *GoogleProvider) Key() string { return database.OAuthProviderGoogle }

// IsConfigured admin 是否在 SysConfig 配齐 client_id + secret。
func (p *GoogleProvider) IsConfigured() bool {
	proxy.SysConfigMutex.RLock()
	clientID := proxy.SysConfigCache["google_client_id"]
	clientSecret := proxy.SysConfigCache["google_client_secret"]
	proxy.SysConfigMutex.RUnlock()
	return clientID != "" && clientSecret != ""
}

// Exchange code → user identity。
func (p *GoogleProvider) Exchange(ctx context.Context, code, codeVerifier string) (*OAuthIdentityData, error) {
	proxy.SysConfigMutex.RLock()
	clientID := proxy.SysConfigCache["google_client_id"]
	clientSecret := proxy.SysConfigCache["google_client_secret"]
	proxy.SysConfigMutex.RUnlock()

	if clientID == "" || clientSecret == "" {
		return nil, ErrOAuthProviderNotConfigured
	}
	redirectURI, err := buildAbsoluteURL("/oauth/google")
	if err != nil {
		log.Printf("[OAUTH-GOOGLE] invalid redirect_uri config: %v", err)
		return nil, ErrOAuthProviderNotConfigured
	}

	// 1. Token exchange — Google token endpoint 用 application/x-www-form-urlencoded
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("code_verifier", codeVerifier)
	form.Set("grant_type", "authorization_code")

	req, err := http.NewRequestWithContext(ctx, "POST", googleTokenEndpoint, bytes.NewBufferString(form.Encode()))
	if err != nil {
		log.Printf("[OAUTH-GOOGLE] build token req failed: %v", err)
		return nil, ErrOAuthProviderInternal
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := githubHTTPClient.Do(req)
	if err != nil {
		log.Printf("[OAUTH-GOOGLE] token exchange failed: %v", err)
		return nil, ErrOAuthUpstreamUnavailable
	}
	defer resp.Body.Close()

	tokenBody, err := io.ReadAll(io.LimitReader(resp.Body, githubResponseLimit))
	if err != nil {
		log.Printf("[OAUTH-GOOGLE] read token resp failed (status=%d): %v", resp.StatusCode, err)
		return nil, ErrOAuthUpstreamMalformed
	}
	var tokenRes struct {
		AccessToken string `json:"access_token"`
		IDToken     string `json:"id_token"`
		TokenType   string `json:"token_type"`
		Scope       string `json:"scope"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	if err := json.Unmarshal(tokenBody, &tokenRes); err != nil {
		log.Printf("[OAUTH-GOOGLE] decode token resp failed (status=%d): %v", resp.StatusCode, err)
		return nil, ErrOAuthUpstreamMalformed
	}
	if tokenRes.AccessToken == "" {
		// Google 在 code 过期 / 已用 / verifier 不匹配时返回 {"error":"invalid_grant", ...}
		log.Printf("[OAUTH-GOOGLE] token exchange rejected: error=%s desc=%s", tokenRes.Error, tokenRes.ErrorDesc)
		return nil, ErrOAuthCodeExpired
	}

	// 2. UserInfo (OpenID Connect)
	req2, err := http.NewRequestWithContext(ctx, "GET", googleUserInfoEndpoint, nil)
	if err != nil {
		log.Printf("[OAUTH-GOOGLE] build userinfo req failed: %v", err)
		return nil, ErrOAuthProviderInternal
	}
	req2.Header.Set("Authorization", "Bearer "+tokenRes.AccessToken)
	resp2, err := githubHTTPClient.Do(req2)
	if err != nil {
		log.Printf("[OAUTH-GOOGLE] fetch userinfo failed: %v", err)
		return nil, ErrOAuthUpstreamUnavailable
	}
	defer resp2.Body.Close()

	userBody, err := io.ReadAll(io.LimitReader(resp2.Body, githubResponseLimit))
	if err != nil {
		log.Printf("[OAUTH-GOOGLE] read userinfo body failed: %v", err)
		return nil, ErrOAuthUpstreamMalformed
	}
	// OIDC standard claims: https://accounts.google.com/.well-known/openid-configuration
	var userInfo struct {
		Sub           string `json:"sub"`            // Google account ID（数字串）
		Email         string `json:"email"`          // 用户邮箱
		EmailVerified bool   `json:"email_verified"` // Google 是否已验证
		Name          string `json:"name"`           // 全名
		GivenName     string `json:"given_name"`     // 名（First name）
		Picture       string `json:"picture"`        // 头像 URL
	}
	if err := json.Unmarshal(userBody, &userInfo); err != nil {
		log.Printf("[OAUTH-GOOGLE] unmarshal userinfo failed (status=%d, body=%.200q): %v",
			resp2.StatusCode, string(userBody), err)
		return nil, ErrOAuthUpstreamMalformed
	}
	if userInfo.Sub == "" {
		log.Printf("[OAUTH-GOOGLE] userinfo missing sub field (status=%d)", resp2.StatusCode)
		return nil, ErrOAuthUpstreamMalformed
	}

	// Username 建议：Google 没有 GitHub login 那种"机器友好用户名"。优先 given_name，否则 name，
	// 最后 email local-part。suggestUsernameFromOAuthName 会再清洗成合法 username。
	username := strings.TrimSpace(userInfo.GivenName)
	if username == "" {
		username = strings.TrimSpace(userInfo.Name)
	}
	if username == "" && userInfo.Email != "" {
		if at := strings.Index(userInfo.Email, "@"); at > 0 {
			username = userInfo.Email[:at]
		}
	}
	if username == "" {
		username = "google_user"
	}

	return &OAuthIdentityData{
		Provider:      database.OAuthProviderGoogle,
		ExternalID:    userInfo.Sub,
		Email:         userInfo.Email,
		Username:      username,
		AvatarURL:     userInfo.Picture,
		EmailVerified: userInfo.EmailVerified,
	}, nil
}

// GoogleAuthorizeURL 给前端用，返回完整的 Google authorize URL。
// 前端 click 之后 window.location.href = url 跳到 Google。
// scope 固定 "openid email profile"。
//
// 注：实际上前端可以自己拼这个 URL（client_id 走 /api/public-config 返回）。
// 提供 helper 主要让 backend tests + admin 看 URL 文档时方便。
func GoogleAuthorizeURL(state, codeChallenge string) (string, error) {
	proxy.SysConfigMutex.RLock()
	clientID := proxy.SysConfigCache["google_client_id"]
	proxy.SysConfigMutex.RUnlock()
	if clientID == "" {
		return "", ErrOAuthProviderNotConfigured
	}
	redirectURI, err := buildAbsoluteURL("/oauth/google")
	if err != nil {
		return "", err
	}
	v := url.Values{}
	v.Set("client_id", clientID)
	v.Set("redirect_uri", redirectURI)
	v.Set("response_type", "code")
	v.Set("scope", "openid email profile")
	v.Set("state", state)
	v.Set("code_challenge", codeChallenge)
	v.Set("code_challenge_method", "S256")
	v.Set("access_type", "online") // 不需要 refresh token
	v.Set("prompt", "select_account")
	return fmt.Sprintf("%s?%s", googleAuthorizeBase, v.Encode()), nil
}

// init 注册 Google provider 到全局 registry（与 GitHubProvider 同）。
func init() {
	RegisterOAuthProvider(NewGoogleProvider())
}
