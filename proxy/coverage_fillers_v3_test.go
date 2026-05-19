package proxy

// coverage_fillers_v3_test.go
//
// M-R3 增量 3：把第三批 0% 纯函数（SafeTruncateRunes / readKeywordAIMaxCandidates /
// LoadKeywordsFromConfig + InvalidateKeywordFilterCache / RedirectGuard）通过
// characterization 测试钉住。

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// ─── prompt_extract.go::SafeTruncateRunes ─────────────────────────────────────

func TestSafeTruncateRunes(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		maxRunes int
		want     string
	}{
		{"short ASCII no truncate", "hello", 10, "hello"},
		{"exact length", "hello", 5, "hello"},
		{"truncate ASCII", "hello world", 5, "hello"},
		{"empty input", "", 10, ""},
		{"zero max → empty", "anything", 0, ""},
		{"negative max → empty", "anything", -3, ""},
		{"CJK rune boundary safe", "你好世界天下", 3, "你好世"},
		{"CJK truncate exact", "你好", 2, "你好"},
		{"mixed CJK + ASCII", "ab你好cd", 4, "ab你好"},
		{"emoji handling", "🙂🙃🙂🙃", 2, "🙂🙃"},
	}
	for _, c := range cases {
		got := SafeTruncateRunes(c.in, c.maxRunes)
		if got != c.want {
			t.Errorf("%s: SafeTruncateRunes(%q,%d)=%q want %q", c.name, c.in, c.maxRunes, got, c.want)
		}
	}
}

// ─── moderation_keyword_ai.go::readKeywordAIMaxCandidates ─────────────────────

func TestReadKeywordAIMaxCandidates(t *testing.T) {
	original := replaceSysConfigForTest(map[string]string{})
	t.Cleanup(func() { replaceSysConfigForTest(original) })

	// 空 → 默认 80
	replaceSysConfigForTest(map[string]string{})
	if n := readKeywordAIMaxCandidates(); n != 80 {
		t.Errorf("empty config → got %d want 80", n)
	}

	// 合法数字
	replaceSysConfigForTest(map[string]string{"moderation_keyword_ai_max_candidates": "200"})
	if n := readKeywordAIMaxCandidates(); n != 200 {
		t.Errorf("config=200 → got %d want 200", n)
	}

	// 非数字字符 → 默认 80（fail-closed 不让管理员误配把它打爆）
	replaceSysConfigForTest(map[string]string{"moderation_keyword_ai_max_candidates": "junk"})
	if n := readKeywordAIMaxCandidates(); n != 80 {
		t.Errorf("non-numeric config → got %d want 80 (fail-closed)", n)
	}

	// 数字混字符
	replaceSysConfigForTest(map[string]string{"moderation_keyword_ai_max_candidates": "12abc34"})
	if n := readKeywordAIMaxCandidates(); n != 80 {
		t.Errorf("mixed config → got %d want 80 (fail-closed)", n)
	}

	// 前导空格 trim
	replaceSysConfigForTest(map[string]string{"moderation_keyword_ai_max_candidates": "  150  "})
	if n := readKeywordAIMaxCandidates(); n != 150 {
		t.Errorf("padded config → got %d want 150", n)
	}
}

// ─── keyword_filter.go::LoadKeywordsFromConfig + InvalidateKeywordFilterCache ─

func TestKeywordFilterLoadAndInvalidate(t *testing.T) {
	original := replaceSysConfigForTest(map[string]string{})
	t.Cleanup(func() {
		replaceSysConfigForTest(original)
		LoadKeywordsFromConfig() // 恢复全局过滤器
	})

	// 空 config → 词库清空
	replaceSysConfigForTest(map[string]string{})
	LoadKeywordsFromConfig()
	if hit := MatchKeyword("anything goes here"); hit != "" {
		t.Errorf("empty config should clear filter, but matched %q", hit)
	}

	// 加载词库（含 lowercase / 去重 / 去空白校验）
	replaceSysConfigForTest(map[string]string{
		"moderation_keywords": `["bomb","WEAPON","bomb","  attack  ",""]`,
	})
	LoadKeywordsFromConfig()
	// 命中各词
	if hit := MatchKeyword("how to make a BOMB"); hit != "bomb" {
		t.Errorf("expected bomb match, got %q", hit)
	}
	if hit := MatchKeyword("WEAPONS are here"); hit != "weapon" {
		t.Errorf("expected weapon match (lower-cased), got %q", hit)
	}
	if hit := MatchKeyword("attack vector"); hit != "attack" {
		t.Errorf("expected attack (trimmed) match, got %q", hit)
	}
	if hit := MatchKeyword(""); hit != "" {
		t.Errorf("empty prompt should never match, got %q", hit)
	}
	if hit := MatchKeyword("benign text"); hit != "" {
		t.Errorf("benign prompt should not match, got %q", hit)
	}

	// JSON 损坏 → 保留旧词库（不清空）+ log warn
	replaceSysConfigForTest(map[string]string{
		"moderation_keywords": "not-valid-json[",
	})
	LoadKeywordsFromConfig()
	if hit := MatchKeyword("bomb threat"); hit != "bomb" {
		t.Errorf("JSON-parse failure should keep old keywords; got %q", hit)
	}

	// InvalidateKeywordFilterCache 是 LoadKeywordsFromConfig 的别名，行为相同
	replaceSysConfigForTest(map[string]string{
		"moderation_keywords": `["replacement-word"]`,
	})
	InvalidateKeywordFilterCache()
	if hit := MatchKeyword("contains replacement-word here"); hit != "replacement-word" {
		t.Errorf("InvalidateKeywordFilterCache should reload; got %q", hit)
	}
}

// ─── url_safety.go::RedirectGuard ─────────────────────────────────────────────

func TestRedirectGuardExported_AllowsSameOrigin(t *testing.T) {
	prevURL, _ := url.Parse("https://api.example.com/v1/x")
	nextURL, _ := url.Parse("https://api.example.com/v1/y")
	req := &http.Request{URL: nextURL}
	via := []*http.Request{{URL: prevURL}}
	if err := RedirectGuard(req, via); err != nil {
		t.Errorf("same-origin redirect should be allowed: %v", err)
	}
}

func TestRedirectGuardExported_BlocksCrossHost(t *testing.T) {
	prevURL, _ := url.Parse("https://api.example.com/v1/x")
	nextURL, _ := url.Parse("https://evil.com/v1/y")
	req := &http.Request{URL: nextURL}
	via := []*http.Request{{URL: prevURL}}
	err := RedirectGuard(req, via)
	if err == nil || !strings.Contains(err.Error(), "cross-host") {
		t.Errorf("cross-host redirect should be blocked; got err=%v", err)
	}
}

func TestRedirectGuardExported_BlocksCrossScheme(t *testing.T) {
	prevURL, _ := url.Parse("https://api.example.com/v1/x")
	nextURL, _ := url.Parse("http://api.example.com/v1/y") // 降级 scheme
	req := &http.Request{URL: nextURL}
	via := []*http.Request{{URL: prevURL}}
	err := RedirectGuard(req, via)
	if err == nil || !strings.Contains(err.Error(), "cross-host/scheme") {
		t.Errorf("HTTPS→HTTP downgrade should be blocked; got err=%v", err)
	}
}

func TestRedirectGuardExported_BlocksMetadataIP(t *testing.T) {
	// 上游试图 302 到 169.254.169.254 元数据服务 → ValidateChannelURL 拦截
	prevURL, _ := url.Parse("https://api.example.com/v1/x")
	nextURL, _ := url.Parse("http://169.254.169.254/latest/meta-data/iam/security-credentials")
	req := &http.Request{URL: nextURL}
	via := []*http.Request{{URL: prevURL}}
	err := RedirectGuard(req, via)
	if err == nil {
		t.Errorf("redirect to metadata IP should be blocked")
	}
}

func TestRedirectGuardExported_TooManyRedirects(t *testing.T) {
	nextURL, _ := url.Parse("https://api.example.com/v1/end")
	req := &http.Request{URL: nextURL}
	// 模拟 ≥10 跳
	via := make([]*http.Request, 10)
	for i := range via {
		u, _ := url.Parse("https://api.example.com/v1/hop")
		via[i] = &http.Request{URL: u}
	}
	err := RedirectGuard(req, via)
	if err != http.ErrUseLastResponse {
		t.Errorf("≥10 redirects should return http.ErrUseLastResponse; got %v", err)
	}
}

func TestRedirectGuardExported_MissingTargetURL(t *testing.T) {
	err := RedirectGuard(&http.Request{URL: nil}, nil)
	if err == nil || !strings.Contains(err.Error(), "missing target URL") {
		t.Errorf("nil URL should be blocked; got %v", err)
	}
	err = RedirectGuard(nil, nil)
	if err == nil {
		t.Errorf("nil request should be blocked")
	}
}
