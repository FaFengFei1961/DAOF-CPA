package proxy

// coverage_fillers_v2_test.go
//
// M-R3 增量 2：把第二批 0% 纯函数（通知链接 builder / 默认 billing JSON /
// gpt-image-2 moderation 归一化 / imageErr 构造 / 风控 term 子串匹配 /
// incrementSubTokenUsedQuota 计数 / keyword AI focus sanitize / sanitize moderation
// 等）通过 characterization 测试钉住。

import (
	"encoding/json"
	"strings"
	"testing"

	"daof-cpa/database"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// ─── notification_links.go：5 个 link builder ─────────────────────────────────

func TestLinkBuilders(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		// Mi-3 cleanup: /upgrade compat shim 删除后，"我的订阅"通知直接落 Dashboard，
		// "看新套餐"营销通知带 ?openBrowse=store 由 MySubscriptions 自动弹 modal。
		{"LinkUpgradeMine", LinkUpgradeMine(), "/"},
		{"LinkUpgradeStore", LinkUpgradeStore(), "/?openBrowse=store"},
		{"LinkTopup", LinkTopup(), "/topup"},
		{"LinkBills no filter", LinkBills(""), "/bills"},
		{"LinkBills with filter", LinkBills("topup"), "/bills?filter=topup"},
		{"LinkTickets", LinkTickets(), "/tickets"},
		{"LinkSettingsTab no tab", LinkSettingsTab(""), "/settings"},
		{"LinkSettingsTab with tab", LinkSettingsTab("notifications"), "/settings?tab=notifications"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
}

// ─── billing_rules.go：默认 JSON serializer ───────────────────────────────────

func TestDefaultBillingJSONs(t *testing.T) {
	// 验证两个 default JSON 字符串都是合法 JSON
	weightsJSON := DefaultBillingModelWeightsJSON()
	multipliersJSON := DefaultBillingHealthMultipliersJSON()

	var weights any
	if err := json.Unmarshal([]byte(weightsJSON), &weights); err != nil {
		t.Errorf("DefaultBillingModelWeightsJSON invalid JSON: %v; raw=%s", err, weightsJSON)
	}
	var multipliers any
	if err := json.Unmarshal([]byte(multipliersJSON), &multipliers); err != nil {
		t.Errorf("DefaultBillingHealthMultipliersJSON invalid JSON: %v; raw=%s", err, multipliersJSON)
	}

	// 都应非空（默认值至少一条规则）
	if weightsJSON == "[]" || weightsJSON == "{}" || weightsJSON == "" {
		t.Errorf("DefaultBillingModelWeightsJSON should be non-empty default rules, got %q", weightsJSON)
	}
	if multipliersJSON == "[]" || multipliersJSON == "{}" || multipliersJSON == "" {
		t.Errorf("DefaultBillingHealthMultipliersJSON should be non-empty, got %q", multipliersJSON)
	}
}

func TestFormatChargedCostForDescription(t *testing.T) {
	// raw=100_000 (=$0.1), charged=300_000 (=$0.3) — 实际就是 micro_usd
	// 验证格式包含 raw= 和 charged= 两个 token
	got := FormatChargedCostForDescription(100_000, 300_000)
	if !strings.Contains(got, "raw=") || !strings.Contains(got, "charged=") {
		t.Errorf("FormatChargedCostForDescription = %q, must contain raw= and charged=", got)
	}
}

// ─── image_generation.go::normalizeGPTImageModeration ─────────────────────────

func TestNormalizeGPTImageModeration(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", "auto", false},
		{"auto", "auto", false},
		{"AUTO", "auto", false},
		{"  Auto  ", "auto", false},
		{"low", "low", false},
		{"LOW", "low", false},
		{"high", "", true}, // gpt-image-2 不接受 high，只接受 auto / low
		{"strict", "", true},
		{"medium", "", true},
	}
	for _, c := range cases {
		got, err := normalizeGPTImageModeration(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("normalizeGPTImageModeration(%q) expected error, got %q", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("normalizeGPTImageModeration(%q) unexpected error %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("normalizeGPTImageModeration(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

// ─── image_generation.go::imageErr ────────────────────────────────────────────

func TestImageErr(t *testing.T) {
	// 正常 status
	e := imageErr(429, "rate_limit", "too many requests")
	if e.status != 429 || e.errorType != "rate_limit" || e.message != "too many requests" {
		t.Errorf("imageErr fields wrong: %+v", e)
	}
	// body 应是合法 JSON，含 type+message
	var parsed struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(e.body, &parsed); err != nil {
		t.Fatalf("imageErr body not valid JSON: %v", err)
	}
	if parsed.Error.Type != "rate_limit" || parsed.Error.Message != "too many requests" {
		t.Errorf("imageErr body content mismatch: %+v", parsed)
	}

	// status <=0 应默认 502
	e2 := imageErr(0, "weird", "msg")
	if e2.status != 502 {
		t.Errorf("imageErr(0,...) status=%d want 502 default", e2.status)
	}
	e3 := imageErr(-1, "weird", "msg")
	if e3.status != 502 {
		t.Errorf("imageErr(-1,...) status=%d want 502 default", e3.status)
	}
}

// ─── image_generation.go::incrementSubTokenUsedQuota ──────────────────────────

func TestIncrementSubTokenUsedQuota_UpdatesDBAndCache(t *testing.T) {
	// 启用临时 DB + AccessToken 表
	db, err := gorm.Open(sqlite.Open("file:incsubquota?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&database.AccessToken{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	oldDB := database.DB
	database.DB = db
	t.Cleanup(func() { database.DB = oldDB })

	subToken := &database.AccessToken{
		ID:        7,
		UserID:    1,
		Name:      "test-sub",
		Key:       "sk-sub-7",
		Status:    1,
		UsedQuota: 100,
	}
	if err := db.Create(subToken).Error; err != nil {
		t.Fatalf("seed sub: %v", err)
	}
	origCache := AuthTokenCache
	authSnapshotMutex.Lock()
	AuthTokenCache = map[string]*database.AccessToken{
		"sk-sub-7": subToken,
	}
	authSnapshotMutex.Unlock()
	t.Cleanup(func() {
		authSnapshotMutex.Lock()
		AuthTokenCache = origCache
		authSnapshotMutex.Unlock()
	})

	// 正常累加
	incrementSubTokenUsedQuota("sk-sub-7", subToken, 250)

	// DB 应已累加
	var fresh database.AccessToken
	if err := db.First(&fresh, subToken.ID).Error; err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if fresh.UsedQuota != 350 {
		t.Errorf("DB UsedQuota=%d want 350 (100+250)", fresh.UsedQuota)
	}

	// AuthTokenCache 应同步更新（clone-on-write）
	authSnapshotMutex.RLock()
	cached := AuthTokenCache["sk-sub-7"]
	authSnapshotMutex.RUnlock()
	if cached.UsedQuota != 350 {
		t.Errorf("cache UsedQuota=%d want 350", cached.UsedQuota)
	}

	// nil sub-token / amount<=0 是 no-op，不应 panic 或写入
	incrementSubTokenUsedQuota("sk-sub-7", nil, 100) // nil sub
	incrementSubTokenUsedQuota("sk-sub-7", subToken, 0)
	incrementSubTokenUsedQuota("sk-sub-7", subToken, -10)
	if err := db.First(&fresh, subToken.ID).Error; err != nil {
		t.Fatalf("re-read 2: %v", err)
	}
	if fresh.UsedQuota != 350 {
		t.Errorf("after no-ops, DB UsedQuota=%d want 350", fresh.UsedQuota)
	}
}

// ─── moderation_risk_rules.go::containsAnyRiskTerm ────────────────────────────

func TestContainsAnyRiskTerm(t *testing.T) {
	terms := []string{"attack", "bomb", "weapon"}
	cases := []struct {
		s    string
		want bool
	}{
		{"how to make a bomb", true},
		{"weapons of mass", true},
		{"benign sentence", false},
		{"", false},
		{"BOMB", false}, // 区分大小写（无归一化）
		{"this is an attacker tutorial", true},
	}
	for _, c := range cases {
		got := containsAnyRiskTerm(c.s, terms)
		if got != c.want {
			t.Errorf("containsAnyRiskTerm(%q)=%v want %v", c.s, got, c.want)
		}
	}
	// 空 terms：always false
	if containsAnyRiskTerm("anything", nil) {
		t.Error("containsAnyRiskTerm with nil terms should return false")
	}
	if containsAnyRiskTerm("anything", []string{}) {
		t.Error("containsAnyRiskTerm with empty terms should return false")
	}
}

// ─── moderation_keyword_ai.go::sanitizeKeywordAIFocus ─────────────────────────

func TestSanitizeKeywordAIFocus(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"  multi  space  ", "multi space"},
		{"\n\ttabs\nand\nlines\n", "tabs and lines"},
		{"normal phrase", "normal phrase"},
	}
	for _, c := range cases {
		got := sanitizeKeywordAIFocus(c.in)
		if got != c.want {
			t.Errorf("sanitizeKeywordAIFocus(%q)=%q want %q", c.in, got, c.want)
		}
	}

	// >1000 rune 应被截断
	long := strings.Repeat("漢", 1500)
	out := sanitizeKeywordAIFocus(long)
	if runeCount(out) != 1000 {
		t.Errorf("sanitizeKeywordAIFocus(1500-rune)=%d rune, want 1000", runeCount(out))
	}
}

func runeCount(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}
