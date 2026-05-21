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
// fix H-Audit-3（2026-05-21）：scope=user:email 让 Exchange 能调 /user/emails 拿
// verified primary，进而把 EmailVerified=true 设给真正已验证的邮箱（让 H-6 跨 provider
// 邮箱冲突防御 + H-Audit-2 自动 sync user.email 对 GitHub 也生效）。
func (p *GitHubProvider) PublicMetadata() OAuthProviderPublicMetadata {
	proxy.SysConfigMutex.RLock()
	clientID := proxy.SysConfigCache["github_client_id"]
	proxy.SysConfigMutex.RUnlock()
	return OAuthProviderPublicMetadata{
		Key:               database.OAuthProviderGitHub,
		Label:             "GitHub",
		ClientID:          clientID,
		AuthorizeEndpoint: "https://github.com/login/oauth/authorize",
		DefaultParams: map[string]string{
			// H-Audit-3：申请 user:email scope，调 /user/emails 拿 verified primary
			"scope": "user:email",
		},
		IconKey: "github",
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

	// 3. /user/emails 取 verified primary
	//
	// Phase H-Audit-3（2026-05-21）：申请 user:email scope 后调 /user/emails，按
	// `primary=true && verified=true` 找权威邮箱。这一步是必要的：
	//   - GitHub /user.email 字段是用户公开设置（profile email），可能是 secondary
	//     未验证邮箱（攻击者可在 GitHub 加 secondary 邮箱、设为 public，让它在
	//     /user.email 露出。H-Audit H-1 因此把 EmailVerified 保守置 false）
	//   - /user/emails 列表带 verified 标识，能可靠区分。primary+verified 才是
	//     真正属于用户的邮箱
	//
	// fail-soft 策略：/user/emails 失败（用户未授权 user:email scope / GitHub API
	// 间歇 5xx）→ 退回 EmailVerified=false（fail-closed 防御性默认）。不让 OAuth
	// 流程因为 emails 查询失败而整体失败。
	primaryEmail, primaryVerified := fetchGitHubVerifiedPrimaryEmail(ctx, accessToken)
	if primaryVerified {
		// 找到 verified primary：覆盖 /user 拿到的 ghEmail（防 user 的 public email 是
		// secondary 未验证邮箱的场景）
		return &OAuthIdentityData{
			Provider:      database.OAuthProviderGitHub,
			ExternalID:    ghID,
			Email:         primaryEmail,
			Username:      ghLogin,
			AvatarURL:     avatar,
			EmailVerified: true,
		}, nil
	}
	// /user/emails 失败 / 未授权 / 无 verified primary：保留 /user.email 作展示值，
	// EmailVerified=false（H-6 跨 provider 冲突预检会跳过；uniq_users_email_nonempty
	// partial unique index 是最终兜底）。
	return &OAuthIdentityData{
		Provider:      database.OAuthProviderGitHub,
		ExternalID:    ghID,
		Email:         ghEmail,
		Username:      ghLogin,
		AvatarURL:     avatar,
		EmailVerified: false,
	}, nil
}

// fetchGitHubVerifiedPrimaryEmail 调 GitHub /user/emails 端点找 verified primary email。
// 需要 user:email scope（PublicMetadata.DefaultParams 已申请）。
//
// 返回 (email, true) 表示找到 verified primary；(<任意>, false) 表示失败 / 未授权 /
// 无 verified primary。caller 应在 false 时退回 EmailVerified=false 路径。
func fetchGitHubVerifiedPrimaryEmail(ctx context.Context, accessToken string) (string, bool) {
	req, err := http.NewRequestWithContext(ctx, "GET", githubEmailsEndpoint, nil)
	if err != nil {
		log.Printf("[OAUTH-GITHUB-EMAILS] build req failed: %v", err)
		return "", false
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := oauthHTTPClient.Do(req)
	if err != nil {
		log.Printf("[OAUTH-GITHUB-EMAILS] fetch failed: %v", err)
		return "", false
	}
	defer resp.Body.Close()
	// 用户没授 user:email scope → 404；GitHub 间歇 5xx → 也走这条路径
	if resp.StatusCode != http.StatusOK {
		log.Printf("[OAUTH-GITHUB-EMAILS] status=%d (user:email scope possibly missing)", resp.StatusCode)
		return "", false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, oauthUpstreamResponseLimit))
	if err != nil {
		log.Printf("[OAUTH-GITHUB-EMAILS] read body failed: %v", err)
		return "", false
	}
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := json.Unmarshal(body, &emails); err != nil {
		log.Printf("[OAUTH-GITHUB-EMAILS] decode body failed: %v", err)
		return "", false
	}
	for _, e := range emails {
		if e.Primary && e.Verified && e.Email != "" {
			return e.Email, true
		}
	}
	// primary 不 verified 也回 false：宁可漏检也不冒充验证
	return "", false
}

// fix H-Audit L6（2026-05-20）：mapOAuthProviderErrorGitHub 已删，所有 provider
// 共用 controller/oauth.go 的 mapOAuthProviderErrorGeneric。错误码 ERR_OAUTH_*。
