// Package controller / oauth_provider_github.go
//
// GitHub OAuth provider 适配器。Phase H-2（2026-05-20）。
//
// 实现 OAuthProvider interface。封装 GitHub token endpoint / userinfo endpoint /
// SafeTransport + RedirectGuard（防 SSRF / DNS rebinding / open redirect）。
//
// 行为兼容性：本文件提取自原 GithubCallback (controller/oauth.go) 的步骤 2-4，
// 不改变现有 SysConfig key（github_client_id / github_client_secret）、不改 HTTP
// 调用细节、不改错误码语义；只把 provider-specific 部分从 1073 行 handler 里抽出来。
package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"

	"daof-cpa/database"
	"daof-cpa/proxy"
)

// init 注册 GitHub provider 到全局 registry。
// 注：这里不检查 IsConfigured() —— 即使 admin 还没配 client_id 也注册 adapter，
// 实际调用 Exchange 才检查 SysConfig。这样 admin 后续配置生效不用重启进程。
func init() {
	RegisterOAuthProvider(NewGitHubProvider())
}

// GitHubProvider OAuth adapter。私有结构，通过 RegisterOAuthProvider 注册到全局 registry。
//
// 注：endpoints + httpClient 不存进 struct，每次调用 Exchange 时读全局变量。
// 这样测试可以通过 installMockGitHub 临时替换 globals 而不需要重新构造 provider。
type GitHubProvider struct{}

// NewGitHubProvider 构造 default GitHub adapter。无配置；endpoints + HTTP client
// 读 oauth.go 顶部的 globals（githubTokenEndpoint / githubUserEndpoint / oauthHTTPClient）。
func NewGitHubProvider() *GitHubProvider { return &GitHubProvider{} }

// Key 返回 "github"。
func (p *GitHubProvider) Key() string { return database.OAuthProviderGitHub }

// IsConfigured 判定 admin 是否在 SysConfig 配齐 github_client_id + github_client_secret。
func (p *GitHubProvider) IsConfigured() bool {
	proxy.SysConfigMutex.RLock()
	clientID := proxy.SysConfigCache["github_client_id"]
	clientSecret := proxy.SysConfigCache["github_client_secret"]
	proxy.SysConfigMutex.RUnlock()
	return clientID != "" && clientSecret != ""
}

// PublicMetadata 返回前端渲染 GitHub 登录按钮 + 拼 authorize URL 所需的元数据。
// fix H-Audit L8（2026-05-21）：让前端不再 hardcode GitHub-specific 参数。
func (p *GitHubProvider) PublicMetadata() OAuthProviderPublicMetadata {
	proxy.SysConfigMutex.RLock()
	clientID := proxy.SysConfigCache["github_client_id"]
	proxy.SysConfigMutex.RUnlock()
	return OAuthProviderPublicMetadata{
		Key:               database.OAuthProviderGitHub,
		Label:             "GitHub",
		ClientID:          clientID,
		AuthorizeEndpoint: "https://github.com/login/oauth/authorize",
		// GitHub OAuth 默认 scope 即可拿到 primary email；前端无需追加 query
		DefaultParams: map[string]string{},
		IconKey:       "github",
	}
}

// Exchange code → user identity。
//
// endpoints + http client 通过 oauth.go 顶部的 package-level globals 引用，
// 测试可临时替换以 mock。
func (p *GitHubProvider) Exchange(ctx context.Context, code, codeVerifier string) (*OAuthIdentityData, error) {
	proxy.SysConfigMutex.RLock()
	clientID := proxy.SysConfigCache["github_client_id"]
	clientSecret := proxy.SysConfigCache["github_client_secret"]
	proxy.SysConfigMutex.RUnlock()

	if clientID == "" || clientSecret == "" {
		return nil, ErrOAuthProviderNotConfigured
	}
	redirectURI, err := buildAbsoluteURL("/oauth/github")
	if err != nil {
		log.Printf("[OAUTH-GITHUB] invalid redirect_uri config: %v", err)
		return nil, ErrOAuthProviderNotConfigured
	}

	// 1. Token exchange
	reqBody := map[string]string{
		"client_id":     clientID,
		"client_secret": clientSecret,
		"code":          code,
		"redirect_uri":  redirectURI,
		"code_verifier": codeVerifier,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		log.Printf("[OAUTH-GITHUB] marshal token req failed: %v", err)
		return nil, ErrOAuthProviderInternal
	}
	req, err := http.NewRequestWithContext(ctx, "POST", githubTokenEndpoint, bytes.NewBuffer(bodyBytes))
	if err != nil {
		log.Printf("[OAUTH-GITHUB] build token req failed: %v", err)
		return nil, ErrOAuthProviderInternal
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := oauthHTTPClient.Do(req)
	if err != nil {
		log.Printf("[OAUTH-GITHUB] token exchange failed: %v", err)
		return nil, ErrOAuthUpstreamUnavailable
	}
	defer resp.Body.Close()

	tokenBody, err := io.ReadAll(io.LimitReader(resp.Body, oauthUpstreamResponseLimit))
	if err != nil {
		log.Printf("[OAUTH-GITHUB] read token resp failed (status=%d): %v", resp.StatusCode, err)
		return nil, ErrOAuthUpstreamMalformed
	}
	var tokenRes map[string]interface{}
	if err := json.Unmarshal(tokenBody, &tokenRes); err != nil {
		log.Printf("[OAUTH-GITHUB] decode token resp failed (status=%d): %v", resp.StatusCode, err)
		return nil, ErrOAuthUpstreamMalformed
	}
	accessToken, ok := tokenRes["access_token"].(string)
	if !ok {
		return nil, ErrOAuthCodeExpired
	}

	// 2. User info
	req2, err := http.NewRequestWithContext(ctx, "GET", githubUserEndpoint, nil)
	if err != nil {
		log.Printf("[OAUTH-GITHUB] build user req failed: %v", err)
		return nil, ErrOAuthProviderInternal
	}
	req2.Header.Set("Authorization", "Bearer "+accessToken)
	resp2, err := oauthHTTPClient.Do(req2)
	if err != nil {
		log.Printf("[OAUTH-GITHUB] fetch user failed: %v", err)
		return nil, ErrOAuthUpstreamUnavailable
	}
	defer resp2.Body.Close()

	userBody, err := io.ReadAll(io.LimitReader(resp2.Body, oauthUpstreamResponseLimit))
	if err != nil {
		log.Printf("[OAUTH-GITHUB] read user body failed: %v", err)
		return nil, ErrOAuthUpstreamMalformed
	}
	var ghUser map[string]interface{}
	if err := json.Unmarshal(userBody, &ghUser); err != nil {
		log.Printf("[OAUTH-GITHUB] unmarshal user body failed (status=%d, body=%.200q): %v",
			resp2.StatusCode, string(userBody), err)
		return nil, ErrOAuthUpstreamMalformed
	}

	// GitHub user.id 是数字。json default 解 float64，转回字符串。
	ghIDFloat, ok := ghUser["id"].(float64)
	if !ok {
		return nil, ErrOAuthUpstreamMalformed
	}
	ghID := fmt.Sprintf("%.0f", ghIDFloat)
	ghLogin, _ := ghUser["login"].(string)
	ghEmail, _ := ghUser["email"].(string) // 可能为空：用户未公开 primary email
	avatar, _ := ghUser["avatar_url"].(string)

	return &OAuthIdentityData{
		Provider:   database.OAuthProviderGitHub,
		ExternalID: ghID,
		Email:      ghEmail,
		Username:   ghLogin,
		AvatarURL:  avatar,
		// Phase H-Audit H-1（2026-05-20）：保守置 false。
		//
		// 历史：H-6 时把 EmailVerified 设为 `ghEmail != ""`，假设 GitHub /user.email
		// 一定是 verified primary。审查发现 GitHub 实际允许用户把 secondary public
		// email 设为公开邮箱，且 /user.email 仅返"primary 公开 OR 未设公开 secondary"，
		// 并非保证 verified——攻击者可在 GitHub 加未验证 secondary、设为 public，
		// 让其在 /user.email 出现并冒充受害者邮箱占位 DAOF 账号（DoS）。
		//
		// 修复：除非扩 `user:email` scope 调 /user/emails 显式拿 verified=true，
		// 否则一律按"未验证"处理。H-6 跨 provider 邮箱冲突预检会因此对 GitHub
		// bypass，但这是 fail-closed 的设计：宁可漏检（让 partial unique index
		// 兜底）也不要被 secondary email 占位骗过。
		//
		// 若未来需要让 GitHub 也参与冲突检测，应扩 scope 到 `user:email`，调
		// GET /user/emails 找 `primary=true && verified=true` 的条目，并仅在此
		// 字段标 EmailVerified=true。
		EmailVerified: false,
	}, nil
}

// fix H-Audit L6（2026-05-20）：mapOAuthProviderErrorGitHub 已删，所有 provider
// 共用 controller/oauth.go 的 mapOAuthProviderErrorGeneric。错误码 ERR_OAUTH_*。
