// Package controller / oauth_provider_metadata_test.go
//
// Phase H-Audit L8（2026-05-21）单元测试：OAuthProvider.PublicMetadata() 返回值。
//
// 验证：
//   - GitHub / Google adapter 返回的元数据形态正确（key / label / authorize_endpoint）
//   - ClientID 跟随 SysConfig 动态变化
//   - default_params 与原 hardcoded 前端配置 1:1 等价
//   - ListConfiguredOAuthProviderMetadata 按字典序稳定排序
package controller

import (
	"sort"
	"testing"

	"daof-cpa/database"
	"daof-cpa/proxy"
)

func withSysConfig(t *testing.T, kv map[string]string, fn func()) {
	t.Helper()
	proxy.SysConfigMutex.Lock()
	old := proxy.SysConfigCache
	cfg := make(map[string]string, len(old)+len(kv))
	for k, v := range old {
		cfg[k] = v
	}
	for k, v := range kv {
		cfg[k] = v
	}
	proxy.SysConfigCache = cfg
	proxy.SysConfigMutex.Unlock()
	defer func() {
		proxy.SysConfigMutex.Lock()
		proxy.SysConfigCache = old
		proxy.SysConfigMutex.Unlock()
	}()
	fn()
}

func TestGitHubProvider_PublicMetadata(t *testing.T) {
	p := NewGitHubProvider()
	withSysConfig(t, map[string]string{"github_client_id": "Iv1.test-client-id"}, func() {
		meta := p.PublicMetadata()
		if meta.Key != database.OAuthProviderGitHub {
			t.Errorf("Key=%q, want %q", meta.Key, database.OAuthProviderGitHub)
		}
		if meta.Label != "GitHub" {
			t.Errorf("Label=%q, want GitHub", meta.Label)
		}
		if meta.ClientID != "Iv1.test-client-id" {
			t.Errorf("ClientID=%q, want from SysConfig", meta.ClientID)
		}
		if meta.AuthorizeEndpoint != "https://github.com/login/oauth/authorize" {
			t.Errorf("AuthorizeEndpoint=%q", meta.AuthorizeEndpoint)
		}
		if len(meta.DefaultParams) != 0 {
			t.Errorf("DefaultParams should be empty for GitHub (default scope is enough), got %v", meta.DefaultParams)
		}
		if meta.IconKey != "github" {
			t.Errorf("IconKey=%q, want github", meta.IconKey)
		}
	})
}

func TestGitHubProvider_PublicMetadataNoClientID(t *testing.T) {
	// admin 没配 client_id 时 ClientID 应为空（前端会校验空值显示"未配置"）
	p := NewGitHubProvider()
	withSysConfig(t, map[string]string{"github_client_id": ""}, func() {
		meta := p.PublicMetadata()
		if meta.ClientID != "" {
			t.Errorf("ClientID=%q, want empty when SysConfig key missing", meta.ClientID)
		}
		// 即使没配 client_id，其它字段（label / authorize_endpoint）仍应填好——前端
		// 可以决定是否渲染该按钮
		if meta.AuthorizeEndpoint == "" {
			t.Error("AuthorizeEndpoint should still be filled when client_id is missing")
		}
	})
}

func TestGoogleProvider_PublicMetadata(t *testing.T) {
	p := NewGoogleProvider()
	withSysConfig(t, map[string]string{"google_client_id": "test.apps.googleusercontent.com"}, func() {
		meta := p.PublicMetadata()
		if meta.Key != database.OAuthProviderGoogle {
			t.Errorf("Key=%q", meta.Key)
		}
		if meta.Label != "Google" {
			t.Errorf("Label=%q", meta.Label)
		}
		if meta.ClientID != "test.apps.googleusercontent.com" {
			t.Errorf("ClientID=%q", meta.ClientID)
		}
		if meta.AuthorizeEndpoint != "https://accounts.google.com/o/oauth2/v2/auth" {
			t.Errorf("AuthorizeEndpoint=%q", meta.AuthorizeEndpoint)
		}
		// Google OIDC 需要的 4 个固定参数
		expectedParams := map[string]string{
			"response_type": "code",
			"scope":         "openid email profile",
			"access_type":   "online",
			"prompt":        "select_account",
		}
		for k, v := range expectedParams {
			if got := meta.DefaultParams[k]; got != v {
				t.Errorf("DefaultParams[%q]=%q, want %q", k, got, v)
			}
		}
		if meta.IconKey != "google" {
			t.Errorf("IconKey=%q", meta.IconKey)
		}
	})
}

func TestListConfiguredOAuthProviderMetadata_SortedByKey(t *testing.T) {
	// 两个 provider 都配齐 → 应按字典序排（github < google）
	withSysConfig(t, map[string]string{
		"github_client_id":     "gh-id",
		"github_client_secret": "gh-secret",
		"google_client_id":     "go-id",
		"google_client_secret": "go-secret",
	}, func() {
		list := ListConfiguredOAuthProviderMetadata()
		if len(list) < 2 {
			t.Fatalf("expected at least 2 providers, got %d", len(list))
		}
		keys := make([]string, len(list))
		for i, m := range list {
			keys[i] = m.Key
		}
		sorted := append([]string{}, keys...)
		sort.Strings(sorted)
		for i := range keys {
			if keys[i] != sorted[i] {
				t.Errorf("ListConfiguredOAuthProviderMetadata not sorted by key: got %v, want %v", keys, sorted)
				return
			}
		}
	})
}

func TestListConfiguredOAuthProviderMetadata_ExcludesUnconfigured(t *testing.T) {
	// 只配 github，google 留空 → 列表只含 github
	withSysConfig(t, map[string]string{
		"github_client_id":     "gh-id",
		"github_client_secret": "gh-secret",
		"google_client_id":     "",
		"google_client_secret": "",
	}, func() {
		list := ListConfiguredOAuthProviderMetadata()
		for _, m := range list {
			if m.Key == database.OAuthProviderGoogle {
				t.Errorf("Google should be excluded (no client_id/secret), got: %+v", m)
			}
		}
	})
}
