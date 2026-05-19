package proxy

import (
	"math/big"
	"strconv"
	"testing"
	"time"

	"daof-cpa/database"
	"daof-cpa/utils"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestResolveBillingRulesDefaults(t *testing.T) {
	old := replaceSysConfigForTest(map[string]string{})
	defer replaceSysConfigForTest(old)

	opus := ResolveBillingRules("claude-opus-4-7", nil, 0, ChannelTypeAnthropic, false).WithCosts(100)
	if opus.ModelWeight != 1 {
		t.Fatalf("opus weight = %v, want 1", opus.ModelWeight)
	}
	if opus.ChargedCostMicroUSD != 100 {
		t.Fatalf("opus charged = %d, want 100", opus.ChargedCostMicroUSD)
	}
	if opus.RequestedModel != opus.ServedModel {
		t.Fatalf("default path must not change model: %+v", opus)
	}

	// Claude extended thinking 不稳定返回独立 reasoning_tokens；显式 thinking 请求即按
	// Claude thinking_weight 预检/扣减。
	thinkingPrecheck := ResolveBillingRules("claude-opus-4-7", []byte(`{"thinking":{"type":"enabled","budget_tokens":1024}}`), 0, ChannelTypeAnthropic, true).WithCosts(100)
	if thinkingPrecheck.ModelWeight != 1.5 {
		t.Fatalf("claude thinking precheck weight = %v, want 1.5", thinkingPrecheck.ModelWeight)
	}

	// commit 时也保持同一 Claude thinking_weight。
	thinkingCommit := ResolveBillingRules("claude-opus-4-7", []byte(`{"thinking":{"type":"enabled","budget_tokens":1024}}`), 800, ChannelTypeAnthropic, true).WithCosts(100)
	if thinkingCommit.ModelWeight != 1.5 {
		t.Fatalf("commit with explicit Claude thinking must trigger ×1.5; got %v, want 1.5", thinkingCommit.ModelWeight)
	}
	adaptiveThinking := ResolveBillingRules("claude-opus-4-7", []byte(`{"thinking":{"type":"adaptive","budget_tokens":1024}}`), 0, ChannelTypeAnthropic, false).WithCosts(100)
	if adaptiveThinking.ModelWeight != 1.5 {
		t.Fatalf("claude adaptive thinking must trigger ×1.5 even without reasoning_tokens; got %v, want 1.5", adaptiveThinking.ModelWeight)
	}
	thinkingAlias := ResolveBillingRules("claude-opus-4-6-thinking", []byte(`{}`), 0, ChannelTypeAnthropic, false).WithCosts(100)
	if thinkingAlias.ModelWeight != 1.5 {
		t.Fatalf("claude *-thinking alias must trigger ×1.5 even without reasoning_tokens; got %v, want 1.5", thinkingAlias.ModelWeight)
	}
	if !thinkingCommit.FallbackUserOptIn {
		t.Fatalf("fallback opt-in should be recorded")
	}
}

func TestResolveBillingRulesThinkingDetection(t *testing.T) {
	old := replaceSysConfigForTest(map[string]string{})
	defer replaceSysConfigForTest(old)

	cases := []struct {
		name      string
		body      string
		reasoning int
		want      float64
	}{
		// === explicit thinking + reasoning tokens → thinking weight ===
		{name: "anthropic enabled + reasoning tokens", body: `{"thinking":{"type":"enabled","budget_tokens":1024}}`, reasoning: 500, want: 1.5},
		{name: "budget enables + reasoning tokens", body: `{"thinking":{"budget_tokens":1024}}`, reasoning: 500, want: 1.5},
		{name: "openai effort medium + reasoning tokens", body: `{"reasoning":{"effort":"medium"}}`, reasoning: 100, want: 1.5},
		{name: "top-level reasoning effort low + reasoning tokens", body: `{"reasoning_effort":"low"}`, reasoning: 100, want: 1.5},

		// === Claude explicit thinking without separate reasoning tokens still uses thinking weight ===
		{name: "claude request has thinking but reasoning=0", body: `{"thinking":{"type":"enabled","budget_tokens":1024}}`, reasoning: 0, want: 1.5},
		{name: "claude adaptive thinking but reasoning=0", body: `{"thinking":{"type":"adaptive","budget_tokens":1024}}`, reasoning: 0, want: 1.5},
		{name: "claude openai chat reasoning_effort maps to thinking", body: `{"reasoning_effort":"high"}`, reasoning: 0, want: 1.5},
		{name: "claude openai responses reasoning effort maps to thinking", body: `{"reasoning":{"effort":"high"}}`, reasoning: 0, want: 1.5},

		// === no explicit thinking → base weight ===
		{name: "claude openai chat reasoning_effort none disables thinking", body: `{"reasoning_effort":"none"}`, reasoning: 0, want: 1},
		{name: "claude openai responses reasoning none disables thinking", body: `{"reasoning":{"effort":"none"}}`, reasoning: 0, want: 1},
		{name: "reasoning tokens but request has no thinking field", body: `{}`, reasoning: 1, want: 1},
		{name: "reasoning tokens but thinking explicitly disabled", body: `{"thinking":{"type":"disabled"}}`, reasoning: 1, want: 1},

		// === neither holds → base weight ===
		{name: "no thinking field, no reasoning tokens", body: `{}`, want: 1},
		{name: "anthropic thinking disabled", body: `{"thinking":{"type":"disabled"}}`, want: 1},
		{name: "empty thinking object", body: `{"thinking":{}}`, want: 1},
		{name: "zero budget", body: `{"thinking":{"budget_tokens":0}}`, want: 1},
		{name: "openai reasoning effort none", body: `{"reasoning":{"effort":"none"}}`, want: 1},
		{name: "top-level reasoning effort none", body: `{"reasoning_effort":"none"}`, want: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveBillingRules("claude-opus-4-7", []byte(tc.body), tc.reasoning, ChannelTypeAnthropic, false).ModelWeight
			if got != tc.want {
				t.Fatalf("model weight = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestResolveBillingRulesNonClaudeThinkingStillRequiresReasoningTokens(t *testing.T) {
	old := replaceSysConfigForTest(map[string]string{
		BillingModelWeightsConfigKey: `[{"pattern":"gpt-*","weight":1,"thinking_weight":1.5}]`,
	})
	defer replaceSysConfigForTest(old)

	precheck := ResolveBillingRules("gpt-5.5", []byte(`{"reasoning":{"effort":"high"}}`), 0, ChannelTypeOpenAI, false)
	if precheck.ModelWeight != 1 {
		t.Fatalf("non-Claude precheck must stay base weight, got %v want 1", precheck.ModelWeight)
	}
	commit := ResolveBillingRules("gpt-5.5", []byte(`{"reasoning":{"effort":"high"}}`), 100, ChannelTypeOpenAI, false)
	if commit.ModelWeight != 1.5 {
		t.Fatalf("non-Claude commit with reasoning tokens should use thinking weight, got %v want 1.5", commit.ModelWeight)
	}
}

func TestResolveBillingRulesFromConfig(t *testing.T) {
	old := replaceSysConfigForTest(map[string]string{
		BillingModelWeightsConfigKey:      `[{"pattern":"special-*","weight":2.25}]`,
		BillingHealthMultipliersConfigKey: `[{"pattern":"special-*","weight":1.2}]`,
		BillingRulesVersionConfigKey:      "test-v1",
	})
	defer replaceSysConfigForTest(old)

	r := ResolveBillingRules("special-model", nil, 0, ChannelTypeOpenAI, false).WithCosts(100)
	if r.BillingRulesVersion != "test-v1" {
		t.Fatalf("version = %q", r.BillingRulesVersion)
	}
	// 订阅扣减：raw=100 × weight=2.25 × health=1.2 = 270
	if r.ChargedCostMicroUSD != 270 {
		t.Fatalf("charged = %d, want 270", r.ChargedCostMicroUSD)
	}
	// 余额扣减：永远 = raw（rawCost 1:1）
	if r.RawCostMicroUSD != 100 {
		t.Fatalf("raw = %d, want 100", r.RawCostMicroUSD)
	}
}

func TestGetPublicBillingRulesExposesBalanceStrategy(t *testing.T) {
	old := replaceSysConfigForTest(map[string]string{
		BillingRulesVersionConfigKey: "v1-2026-05-13",
	})
	defer replaceSysConfigForTest(old)

	rules := GetPublicBillingRules()
	if rules.Version != "v1-2026-05-13" {
		t.Fatalf("version = %q", rules.Version)
	}
	if rules.EffectiveSince != "2026-05-13" {
		t.Fatalf("effective_since = %q, want 2026-05-13", rules.EffectiveSince)
	}
	if rules.Balance.Mode != "raw_cost_1x" {
		t.Fatalf("balance.mode = %q, want raw_cost_1x", rules.Balance.Mode)
	}
	if rules.Balance.Note == "" {
		t.Fatalf("balance.note must not be empty")
	}
	if rules.Subscription["formula"] == "" {
		t.Fatalf("subscription.formula must not be empty")
	}
}

func TestSubscriptionRevenueMicroUSD_GrantedSubscriptionIsZero(t *testing.T) {
	if got := subscriptionRevenueMicroUSD(12345, true); got != 0 {
		t.Fatalf("granted subscription revenue = %d, want 0", got)
	}
	if got := subscriptionRevenueMicroUSD(12345, false); got != 12345 {
		t.Fatalf("paid subscription revenue = %d, want charged cost", got)
	}
	if got := subscriptionRevenueMicroUSD(-1, false); got != 0 {
		t.Fatalf("negative charged cost should clamp to 0, got %d", got)
	}
}

// TestMultiplierFixedPoint 验证 applyBillingMultiplier 使用 ceil-div（Sprint4-M2 fix）。
// 余数 > 0 时向上进位，保证正数成本不被截断到 0；与 checkedCostMicroUSD 同款 ceil 语义。
func TestMultiplierFixedPoint(t *testing.T) {
	cases := []struct {
		cost       int64
		multiplier float64
	}{
		{cost: 101, multiplier: 0.5},
		{cost: 101, multiplier: 0.333},
		{cost: 101, multiplier: 3.14},
		{cost: 999_999_937, multiplier: 3.14},
	}
	for _, tc := range cases {
		ppm, ok := multiplierPPMFromFloat(tc.multiplier)
		if !ok {
			t.Fatalf("multiplierPPMFromFloat(%v) failed", tc.multiplier)
		}
		// 期望 ceil-div: ⌈cost × ppm / base⌉
		product := new(big.Int).Mul(big.NewInt(tc.cost), big.NewInt(ppm))
		divisor := big.NewInt(database.MultiplierPPMBase)
		adjusted := new(big.Int).Add(product, new(big.Int).Sub(divisor, big.NewInt(1)))
		expected := new(big.Int).Quo(adjusted, divisor)
		if !expected.IsInt64() {
			t.Fatalf("test expected overflowed: %s", expected.String())
		}
		if got := applyBillingMultiplier(tc.cost, tc.multiplier); got != expected.Int64() {
			t.Fatalf("cost=%d multiplier=%v got=%d want=%d", tc.cost, tc.multiplier, got, expected.Int64())
		}
	}
}

// TestApplyBillingMultiplier_CeilPreventsSubMicroLoss 验证 ceil-div 防 sub-1-micro 免费消耗：
// cost × multiplier 落在 (0, 1) micro 范围 → 必须进位到 1，旧 floor 会截断到 0（免费）。
func TestApplyBillingMultiplier_CeilPreventsSubMicroLoss(t *testing.T) {
	// 2 micro × 0.3 = 0.6 micro → ceil = 1（旧 floor = 0 即免费消耗）
	if got := applyBillingMultiplier(2, 0.3); got != 1 {
		t.Errorf("cost=2 mult=0.3 expect ceil to 1 micro, got %d (was 0 before Sprint4-M2 fix)", got)
	}
	// 1 micro × 0.5 = 0.5 micro → ceil = 1
	if got := applyBillingMultiplier(1, 0.5); got != 1 {
		t.Errorf("cost=1 mult=0.5 expect ceil to 1, got %d", got)
	}
	// 边界：1 micro × 1.0 = 1 micro，整除不应误进位
	if got := applyBillingMultiplier(1, 1.0); got != 1 {
		t.Errorf("cost=1 mult=1.0 expect exact 1, got %d", got)
	}
	// 0 成本仍为 0
	if got := applyBillingMultiplier(0, 0.5); got != 0 {
		t.Errorf("cost=0 expect 0, got %d", got)
	}
	// 负成本返回 0
	if got := applyBillingMultiplier(-5, 0.5); got != 0 {
		t.Errorf("cost=-5 expect 0, got %d", got)
	}
}

func TestActivateDueBillingRuleRevisionsSkipsCanceledAndAppliesLatestDue(t *testing.T) {
	utils.InitCrypto()
	db, err := gorm.Open(sqlite.Open("file:billing_rule_activation?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(
		&database.SysConfig{}, &database.BillingRuleRevision{}, &database.BillingRuleRevisionCancellation{},
		&database.Channel{}, &database.ChannelModel{}, &database.User{}, &database.AccessToken{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	oldDB := database.DB
	database.DB = db
	t.Cleanup(func() { database.DB = oldDB })

	oldCache := replaceSysConfigForTest(map[string]string{})
	defer replaceSysConfigForTest(oldCache)

	now := time.Now().UTC()
	canceledAt := now.Add(-2 * time.Minute)
	dueAt := now.Add(-1 * time.Minute)
	canceled := database.BillingRuleRevision{
		Version:               "scheduled-canceled",
		EffectiveSince:        canceledAt.Format("2006-01-02"),
		PublishedAt:           &now,
		EffectiveAt:           &canceledAt,
		ModelWeightsJSON:      `[{"pattern":"canceled-*","weight":9}]`,
		HealthMultipliersJSON: `[{"pattern":"*","weight":1}]`,
		ModelCount:            1,
		HealthCount:           1,
	}
	active := database.BillingRuleRevision{
		Version:               "scheduled-active",
		EffectiveSince:        dueAt.Format("2006-01-02"),
		PublishedAt:           &now,
		EffectiveAt:           &dueAt,
		ModelWeightsJSON:      `[{"pattern":"active-*","weight":2}]`,
		HealthMultipliersJSON: `[{"pattern":"*","weight":1}]`,
		ModelCount:            1,
		HealthCount:           1,
	}
	if err := db.Create(&canceled).Error; err != nil {
		t.Fatalf("create canceled revision: %v", err)
	}
	if err := db.Create(&active).Error; err != nil {
		t.Fatalf("create active revision: %v", err)
	}
	if err := db.Create(&database.BillingRuleRevisionCancellation{RevisionID: canceled.ID}).Error; err != nil {
		t.Fatalf("create cancellation: %v", err)
	}

	activateDueBillingRuleRevisions(now)

	if got := readPlainSysConfigForBillingRulesTest(t, BillingRulesVersionConfigKey); got != active.Version {
		t.Fatalf("active version = %q, want %q", got, active.Version)
	}
	if got := readPlainSysConfigForBillingRulesTest(t, BillingRulesRevisionIDConfigKey); got != strconv.Itoa(int(active.ID)) {
		t.Fatalf("active revision id = %q, want %d", got, active.ID)
	}
	if got := readPlainSysConfigForBillingRulesTest(t, BillingModelWeightsConfigKey); got != active.ModelWeightsJSON {
		t.Fatalf("model weights = %q, want %q", got, active.ModelWeightsJSON)
	}
}

func replaceSysConfigForTest(next map[string]string) map[string]string {
	SysConfigMutex.Lock()
	defer SysConfigMutex.Unlock()
	old := SysConfigCache
	SysConfigCache = next
	return old
}

func readPlainSysConfigForBillingRulesTest(t *testing.T, key string) string {
	t.Helper()
	var row database.SysConfig
	if err := database.DB.Where("key = ?", key).First(&row).Error; err != nil {
		t.Fatalf("read sysconfig %s: %v", key, err)
	}
	plain, err := utils.Decrypt(row.Value)
	if err != nil {
		t.Fatalf("decrypt sysconfig %s: %v", key, err)
	}
	return plain
}
