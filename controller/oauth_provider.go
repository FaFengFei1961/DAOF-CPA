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
//   3. Provider redirect 回浏览器路由 /oauth/:provider?code=XXX&state=YYY，
//      前端解析后 POST 到 /api/auth/oauth/:provider/callback
//      （注：旧的 per-provider 路径 /api/auth/github 已在 H-3 删除）
//   4. handler 调 provider := GetOAuthProvider(":provider") → provider.Exchange(ctx, code, verifier)
//      → 返回 OAuthIdentityData{ExternalID, Email, Username}
//   5. handler 用 ExternalID 查 / 建 user
//
// 注：本接口不存 access_token / refresh_token。OAuth 仅作"证明 external_id 真实"用，
// DAOF 不调 provider API（不需要爬 GitHub repo 等场景）。
package controller

import (
	"context"
	"errors"
	"sort"
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

	// PublicMetadata 给前端用，返回展示和拼跳转 URL 所需的元数据。
	// 该值经 GetPublicConfig 进入 wire，前端按字段渲染按钮 + 拼 authorize URL。
	//
	// fix H-Audit L8（2026-05-21）：原前端 PROVIDER_META 是 hardcoded map（github + google
	// 各一行 metadata + authorize URL builder），添加新 provider 必须前端发版。现在 metadata
	// 全在 provider 实现侧定义，前端只渲染从 server 拿到的字段。
	//
	// ClientID 字段由该方法实现返回（典型用法是读 proxy.SysConfigCache[provider+"_client_id"]）。
	// 该字段已经通过 GetPublicConfig 暴露给前端（github_client_id / google_client_id 等），属公开值。
	PublicMetadata() OAuthProviderPublicMetadata
}

// OAuthProviderPublicMetadata 是 provider 自描述的"前端渲染所需"元数据。
// 通过 GET /api/public-config 暴露，字段都属于"公开值"（client_id 本就公开，scope / icon 等无敏感性）。
type OAuthProviderPublicMetadata struct {
	// Key 与 OAuthProvider.Key() 一致。前端用此识别 provider。
	Key string `json:"key"`

	// Label 用户可见的展示名（"GitHub" / "Google"），i18n 由前端按 key 兜底。
	Label string `json:"label"`

	// ClientID 当前 admin 配置的 client_id，前端拼 authorize URL 用。
	// 这是 oauth_provider_metadata[].client_id 的唯一权威字段（旧的顶层
	// github_client_id / google_client_id 在 Phase H 清理时已删）。
	ClientID string `json:"client_id"`

	// AuthorizeEndpoint provider 的 OAuth authorize URL（不含 query），如
	// "https://github.com/login/oauth/authorize"、"https://accounts.google.com/o/oauth2/v2/auth"。
	AuthorizeEndpoint string `json:"authorize_endpoint"`

	// DefaultParams provider-specific 的 query 参数（不含 client_id / redirect_uri / state /
	// code_challenge / code_challenge_method —— 这五个由前端运行时填）。
	// 例如 Google 的 {"response_type":"code","scope":"openid email profile","access_type":"online","prompt":"select_account"}。
	// GitHub 仅默认 scope 无需 response_type 等。
	DefaultParams map[string]string `json:"default_params"`

	// IconKey 前端按此 key 选内置 brand SVG（"github" / "google" / "fallback"）。
	IconKey string `json:"icon_key"`
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

// ListConfiguredOAuthProviderMetadata 返回所有 admin 已配齐凭据的 provider 完整元数据。
// 前端用 metadata 字段直接渲染按钮 + 拼 authorize URL，不再 hardcode provider map。
//
// Phase H cleanup：删除 ListConfiguredOAuthProviders ([]string) — 零调用方，
// metadata 数组已是唯一公共接口。
//
// 返回顺序：按 Key 字典序排序，保证前端渲染稳定（map 遍历顺序不固定）。
func ListConfiguredOAuthProviderMetadata() []OAuthProviderPublicMetadata {
	oauthProvidersMu.RLock()
	defer oauthProvidersMu.RUnlock()
	out := make([]OAuthProviderPublicMetadata, 0, len(oauthProviders))
	for _, p := range oauthProviders {
		if p.IsConfigured() {
			out = append(out, p.PublicMetadata())
		}
	}
	// 按 Key 字典序排序，让前端按钮顺序稳定
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// ResetOAuthProvidersForTest 测试 hook：清空 registry。仅测试使用。
func ResetOAuthProvidersForTest() {
	oauthProvidersMu.Lock()
	defer oauthProvidersMu.Unlock()
	oauthProviders = map[string]OAuthProvider{}
}
