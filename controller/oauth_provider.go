// Package controller / oauth_provider.go
//
// OAuth provider 抽象。Phase H-2（2026-05-20）。
//
// 目标：把 OAuth callback 里"换 code + 拉用户 profile"这段 provider-specific 的逻辑
// 抽到 OAuthProvider interface 后面，让上层 handler 与具体 provider 解耦。
//
// 调用方流程：
//   1. Frontend POST /api/auth/oauth/:provider/prepare → 拿 state + code_challenge
//   2. Frontend 跳转到 provider 授权页（前端用 SysConfig 暴露的 client_id 自己拼 URL）
//   3. Provider redirect 回 /oauth/github?code=XXX&state=YYY，前端 POST 到 /api/auth/github
//   4. handler 调 provider := GetOAuthProvider("github") → provider.Exchange(ctx, code, verifier)
//      → 返回 OAuthIdentityData{ExternalID, Email, Username}
//   5. handler 用 ExternalID 查 / 建 user
//
// 注：本接口不存 access_token / refresh_token。OAuth 仅作"证明 external_id 真实"用，
// DAOF 不调 provider API（不需要爬 GitHub repo 等场景）。
package controller

import (
	"context"
	"errors"
	"sync"
)

// OAuthProvider 是一个 OAuth 2.0 + PKCE 第三方身份验证适配器。
//
// 每个 provider 实现独立 .go 文件（oauth_provider_<key>.go）。
// 启动时通过 RegisterOAuthProvider 注册到全局 registry。
type OAuthProvider interface {
	// Key 唯一 provider 标识，与 database.OAuthProvider* 常量对应（"github" / "google" / ...）。
	Key() string

	// IsConfigured 判定 admin 是否已在 SysConfig 配齐 client_id / secret。
	// 未配齐时，PrepareOAuthState / Callback 应返回 ERR_*_NOT_CONFIGURED 业务错误。
	IsConfigured() bool

	// Exchange 用 OAuth authorization code 换取 external identity。
	// 流程：
	//   1. POST provider 的 token endpoint 用 code + code_verifier (PKCE) 换 access_token
	//   2. GET provider 的 userinfo endpoint 拿 user 基础信息
	//   3. 返回 OAuthIdentityData；access_token 在函数返回前丢弃
	// 错误：用 ErrOAuth* sentinel 让上层 handler 映射成对应 HTTP status code + message_code。
	Exchange(ctx context.Context, code, codeVerifier string) (*OAuthIdentityData, error)
}

// OAuthIdentityData 是 provider Exchange 后返回的外部身份信息。
// 字段是"提示性"的——只 ExternalID 是必须的、稳定的、唯一的；
// Email / Username 可能为空（如 Microsoft Work Account 不返 email）。
type OAuthIdentityData struct {
	// Provider 必须等于 OAuthProvider.Key()。
	Provider string

	// ExternalID provider-internal user ID。**永远以字符串形式存**（防长 ID 整数溢出）。
	// GitHub: 数字串如 "123456789"
	// Google: openid sub 如 "1234567890abcdef..."
	ExternalID string

	// Email 可能为空。Verified 状态由 provider 自带，调用方不应假定 email 已验证。
	Email string

	// Username provider 的建议用户名（GitHub: login，Google: given_name / name）。
	// 用作 suggestUsernameFromOAuthName 的输入。
	Username string

	// AvatarURL 头像，仅用于 UI 显示，未来可能扔进 user.AvatarURL。
	AvatarURL string

	// EmailVerified 当 provider 明确标记 email_verified=true 才填 true。
	// 用于"跨 provider email 冲突检测"——unverified email 不算"已验证"。
	EmailVerified bool

	// LinkMethod（H-Audit L7）：caller 在写 oauth_identities 行时填入；
	// 用于审计追溯。取值见 database.LinkMethod* 常量。
	// 为空时 linkOAuthIdentityTx 会默认填 "oauth_flow"。
	LinkMethod string
}

// ErrOAuth* 是 Provider.Exchange 错误的标准 sentinel。
// 上层 handler 用 errors.Is 映射到 HTTP status + i18n message_code。
var (
	// ErrOAuthCodeExpired provider 拒掉 code（过期 / 已用 / 重放）。401。
	ErrOAuthCodeExpired = errors.New("oauth: code expired or already used")
	// ErrOAuthUpstreamUnavailable provider 网络故障 / 5xx / 超时。502。
	ErrOAuthUpstreamUnavailable = errors.New("oauth: provider unavailable")
	// ErrOAuthUpstreamMalformed provider 响应解析失败。502。
	ErrOAuthUpstreamMalformed = errors.New("oauth: provider response malformed")
	// ErrOAuthProviderInternal provider adapter 自己出错（如 marshal 失败）。500。
	ErrOAuthProviderInternal = errors.New("oauth: provider internal error")
	// ErrOAuthProviderNotConfigured admin 未配 client_id/secret。503。
	ErrOAuthProviderNotConfigured = errors.New("oauth: provider not configured by admin")
)

// 全局 provider registry。
// 启动时由各 oauth_provider_<key>.go 用 init() 或 main.go 注册。
var (
	oauthProvidersMu sync.RWMutex
	oauthProviders   = map[string]OAuthProvider{}
)

// RegisterOAuthProvider 把一个 provider 加入全局 registry。
// 重复注册同一 Key 会覆盖（用于测试 stub）。
func RegisterOAuthProvider(p OAuthProvider) {
	if p == nil || p.Key() == "" {
		return
	}
	oauthProvidersMu.Lock()
	defer oauthProvidersMu.Unlock()
	oauthProviders[p.Key()] = p
}

// GetOAuthProvider 按 key 取 provider。第二个返回值表示是否注册过。
func GetOAuthProvider(key string) (OAuthProvider, bool) {
	oauthProvidersMu.RLock()
	defer oauthProvidersMu.RUnlock()
	p, ok := oauthProviders[key]
	return p, ok
}

// ListConfiguredOAuthProviders 返回当前 admin 已配齐凭据的 provider key 列表。
// 用于 GET /api/public-config 暴露给前端"用户当前可选哪些登录方式"。
func ListConfiguredOAuthProviders() []string {
	oauthProvidersMu.RLock()
	defer oauthProvidersMu.RUnlock()
	keys := make([]string, 0, len(oauthProviders))
	for k, p := range oauthProviders {
		if p.IsConfigured() {
			keys = append(keys, k)
		}
	}
	return keys
}

// ResetOAuthProvidersForTest 测试 hook：清空 registry。仅测试使用。
func ResetOAuthProvidersForTest() {
	oauthProvidersMu.Lock()
	defer oauthProvidersMu.Unlock()
	oauthProviders = map[string]OAuthProvider{}
}
