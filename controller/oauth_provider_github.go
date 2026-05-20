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
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"

	"daof-cpa/database"
	"daof-cpa/proxy"

	"github.com/gofiber/fiber/v2"
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
// 读 oauth.go 顶部的 globals（githubTokenEndpoint / githubUserEndpoint / githubHTTPClient）。
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

	resp, err := githubHTTPClient.Do(req)
	if err != nil {
		log.Printf("[OAUTH-GITHUB] token exchange failed: %v", err)
		return nil, ErrOAuthUpstreamUnavailable
	}
	defer resp.Body.Close()

	tokenBody, err := io.ReadAll(io.LimitReader(resp.Body, githubResponseLimit))
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
	resp2, err := githubHTTPClient.Do(req2)
	if err != nil {
		log.Printf("[OAUTH-GITHUB] fetch user failed: %v", err)
		return nil, ErrOAuthUpstreamUnavailable
	}
	defer resp2.Body.Close()

	userBody, err := io.ReadAll(io.LimitReader(resp2.Body, githubResponseLimit))
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
		Provider:      database.OAuthProviderGitHub,
		ExternalID:    ghID,
		Email:         ghEmail,
		Username:      ghLogin,
		AvatarURL:     avatar,
		EmailVerified: false, // GitHub /user 端点不返 email_verified；保守视为未验证
	}, nil
}

// mapOAuthProviderErrorGitHub 把 Provider.Exchange 错误映射成 GitHub-flavored HTTP 响应。
// 保留原 GithubCallback 的 status code + message_code 语义不变，兼容前端现有逻辑。
//
// 这是 H-2 过渡阶段——H-3 路由换 :provider 后，所有 provider 共用一个 generic mapper。
func mapOAuthProviderErrorGitHub(c *fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, ErrOAuthProviderNotConfigured):
		return c.Status(500).JSON(fiber.Map{
			"success":      false,
			"message":      "暂时无法提供该授权模式，请使用其他方式登录",
			"message_code": "ERR_GITHUB_NOT_CONFIGURED",
		})
	case errors.Is(err, ErrOAuthCodeExpired):
		return c.Status(401).JSON(fiber.Map{
			"success":      false,
			"message":      "第三方颁发的客户端授权码已过期失效",
			"message_code": "ERR_GITHUB_CODE_EXPIRED",
		})
	case errors.Is(err, ErrOAuthUpstreamUnavailable):
		return c.Status(502).JSON(fiber.Map{
			"success":      false,
			"message":      "第三方服务响应超时(502)",
			"message_code": "ERR_GITHUB_CONN",
		})
	case errors.Is(err, ErrOAuthUpstreamMalformed):
		return c.Status(502).JSON(fiber.Map{
			"success":      false,
			"message":      "第三方接口同步异常",
			"message_code": "ERR_GITHUB_PROFILE_EXCEPTION",
		})
	default:
		return c.Status(500).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_GITHUB_INTERNAL",
		})
	}
}
