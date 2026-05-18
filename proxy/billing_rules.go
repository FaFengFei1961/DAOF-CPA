package proxy

import (
	"encoding/json"
	"log"
	"math"
	"math/big"
	"path"
	"strings"
	"time"

	"daof-cpa/database"

	"github.com/tidwall/gjson"
)

const (
	BillingModelWeightsConfigKey      = "billing_model_weights_json"
	BillingHealthMultipliersConfigKey = "billing_health_multipliers_json"
	BillingRulesVersionConfigKey      = "billing_rules_version"
)

type BillingWeightRule struct {
	Pattern        string  `json:"pattern"`
	Weight         float64 `json:"weight"`
	ThinkingWeight float64 `json:"thinking_weight,omitempty"`
	Label          string  `json:"label,omitempty"`
	Reason         string  `json:"reason,omitempty"`
}

// BillingRuleResolution carries everything billing/audit downstream needs.
//
// 字段含义：
//   - RawCostMicroUSD     = 上游真实 API 等值美元（公开模型单价折算）
//   - ChargedCostMicroUSD = 订阅扣减口径（raw × ModelWeight × HealthMultiplier）
//
// 余额扣减一律按 RawCostMicroUSD（1:1），不再应用 ModelWeight / HealthMultiplier。
type BillingRuleResolution struct {
	RequestedModel      string  `json:"requested_model"`
	ServedModel         string  `json:"served_model"`
	ModelWeight         float64 `json:"model_weight"`
	HealthMultiplier    float64 `json:"health_multiplier"`
	BillingRulesVersion string  `json:"billing_rules_version"`
	FallbackUserOptIn   bool    `json:"fallback_user_opt_in"`
	FallbackReason      string  `json:"fallback_reason,omitempty"`
	RawCostMicroUSD     int64   `json:"-"`
	ChargedCostMicroUSD int64   `json:"-"`
}

// BillingBalanceStrategy 告诉 /api/billing/rules 的消费方"余额扣减口径"。
//
// 当前固定为 rawCost 1:1。如果未来要变（如折扣套餐配额），就改这个对象的字段，
// 让前端展示 + admin 审计 + 用户公示一致同步。
type BillingBalanceStrategy struct {
	Mode string `json:"mode"`
	Note string `json:"note"`
}

// PublicBillingRules is the auditable contract returned by /api/billing/rules.
type PublicBillingRules struct {
	Version           string                 `json:"version"`
	EffectiveSince    string                 `json:"effective_since"`
	ModelWeights      []BillingWeightRule    `json:"model_weights"` // 仅对订阅扣减生效
	HealthMultipliers []BillingWeightRule    `json:"health_multipliers"`
	Subscription      map[string]string      `json:"subscription"`
	Balance           BillingBalanceStrategy `json:"balance"`
	Fallback          map[string]string      `json:"fallback"`
	Notes             []string               `json:"notes"`
}

var defaultBillingModelWeights = []BillingWeightRule{
	{Pattern: "claude-haiku-*", Weight: 0.3, Label: "Claude Haiku", Reason: "当前启用的 Claude 轻量系列"},
	{Pattern: "claude-sonnet-*", Weight: 1.0, ThinkingWeight: 1.5, Label: "Claude Sonnet", Reason: "当前启用的 Claude 基准系列；thinking 启用时加权"},
	{Pattern: "claude-opus-*", Weight: 3.5, ThinkingWeight: 5.0, Label: "Claude Opus", Reason: "当前启用的 Claude 高消耗系列"},
	{Pattern: "gemini-*-flash-lite*", Weight: 0.2, Label: "Gemini Flash Lite", Reason: "当前启用的 Gemini 超轻量系列"},
	{Pattern: "gemini-*-flash*", Weight: 0.4, Label: "Gemini Flash", Reason: "当前启用的 Gemini 快速系列"},
	{Pattern: "gemini-*-pro*", Weight: 0.9, Label: "Gemini Pro", Reason: "当前启用的 Gemini 主力系列"},
	{Pattern: "gpt-*-mini*", Weight: 0.5, Label: "GPT mini", Reason: "当前启用的 GPT 轻量系列"},
	{Pattern: "gpt-*", Weight: 1.0, Label: "GPT", Reason: "当前启用的 GPT 主力系列"},
	{Pattern: "grok-*", Weight: 1.0, Label: "Grok", Reason: "当前启用的 xAI Grok 系列"},
}

var defaultBillingHealthMultipliers = []BillingWeightRule{
	{Pattern: "*", Weight: 1.0, Label: "Normal", Reason: "默认无高峰加权"},
}

func ResolveBillingRules(modelName string, body []byte, reasoningTokens int, channelType string, fallbackOptIn bool) BillingRuleResolution {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		modelName = "unknown"
	}
	// Thinking 倍率严格判定：必须同时满足
	//   1) 用户在请求中显式启用 thinking/reasoning（requestIndicatesThinking）
	//   2) 上游真实消耗了 reasoning tokens（reasoningTokens > 0）
	// 两个条件都满足才走 Thinking 倍率（×5）。这样避免两类不公平：
	//   - 用户没启用 thinking，但模型自己 reason 了（rare）→ 不能加价
	//   - 用户启用了 thinking，但模型 reasoning_tokens=0（没真思考）→ 不能加价
	// 注意：precheck 时 reasoningTokens=0，所以 precheck 永远不会按 ×5 估算；
	// 实际扣费在 commit 时按真实 usage 计算，对用户更公平（不会因 thinking 字段被预先拦截）。
	thinking := reasoningTokens > 0 && requestIndicatesThinking(body)
	modelWeight := matchBillingWeight(modelName, thinking, loadBillingWeightRules(BillingModelWeightsConfigKey, defaultBillingModelWeights))
	healthMultiplier := matchBillingWeight(modelName, false, loadBillingWeightRules(BillingHealthMultipliersConfigKey, defaultBillingHealthMultipliers))
	_ = channelType // reserved for future per-channel adjustments
	return BillingRuleResolution{
		RequestedModel:      modelName,
		ServedModel:         modelName,
		ModelWeight:         modelWeight,
		HealthMultiplier:    healthMultiplier,
		BillingRulesVersion: billingRulesVersion(),
		FallbackUserOptIn:   fallbackOptIn,
	}
}

// WithCosts 把 raw 成本注入并算出订阅扣减口径 (charged)。余额扣减永远等于 raw。
func (r BillingRuleResolution) WithCosts(rawCostMicroUSD int64) BillingRuleResolution {
	r.RawCostMicroUSD = rawCostMicroUSD
	r.ChargedCostMicroUSD = applyBillingMultiplier(rawCostMicroUSD, r.ModelWeight*r.HealthMultiplier)
	return r
}

func GetPublicBillingRules() PublicBillingRules {
	version := billingRulesVersion()
	return PublicBillingRules{
		Version:           version,
		EffectiveSince:    extractEffectiveSinceFromVersion(version),
		ModelWeights:      loadBillingWeightRules(BillingModelWeightsConfigKey, defaultBillingModelWeights),
		HealthMultipliers: loadBillingWeightRules(BillingHealthMultipliersConfigKey, defaultBillingHealthMultipliers),
		Subscription: map[string]string{
			"formula":     "charged_cost = raw_cost × model_weight × health_multiplier",
			"applies_to":  "subscription_quota",
			"description": "命中订阅时按下表系数扣减套餐额度",
		},
		Balance: BillingBalanceStrategy{
			Mode: "raw_cost_1x",
			Note: "余额按上游真实成本 1:1 扣减，不应用模型权重或繁忙时段系数",
		},
		Fallback: map[string]string{
			"default":            "off",
			"per_request_header": "X-Allow-Fallback: true",
			"rule":               "Only user opt-in fallback may change served_model; otherwise requested_model must equal served_model.",
		},
		Notes: []string{
			"raw_cost 是上游官方 API 等值美元成本（公开模型单价折算）。",
			"订阅扣减：charged_cost = raw_cost × model_weight × health_multiplier。",
			"余额扣减：始终按 raw_cost 1:1，不应用下表系数。",
			"账单与请求事件审计同时记录两套口径，方便对账。",
		},
	}
}

// RecordApiLogRevenue 把一次请求真实从用户那里拿到的钱（订阅扣 charged / 余额扣 raw）
// 写入 ApiLogRevenue side table，供毛利报表和审计还原真实营收。
//
// fix HIGH（codex audit-integrity）：原实现失败仅 log.Printf，无重试无 reconcile，
// 在 SQLite WAL busy_timeout 期间瞬时失败会让"用户已扣费但 revenue side table 缺行"，
// 后续毛利报表永久低估真实营收。修复：1 次初次 + 3 次指数退避重试（50ms/100ms/200ms），
// 全部失败时用专用 [BILLING-REVENUE-LOST] 前缀打 log，便于 admin grep + 手工对账。
// 这是 best-effort 写入，不阻塞主请求；调用方已成功扣费的请求不会因 revenue 写失败而回滚。
//
// 指数退避基数 50ms × 2^(retry-1)：retry1=50ms / retry2=100ms / retry3=200ms。
// 总最坏延迟 ~350ms，远短于 SSE 流关闭的网络 RTT 阈值。
func RecordApiLogRevenue(apiLogID uint, source string, effectiveMicroUSD int64, subscriptionID uint) {
	if apiLogID == 0 {
		return
	}
	revenue := database.ApiLogRevenue{
		ApiLogID:                 apiLogID,
		RevenueSource:            source,
		EffectiveRevenueMicroUSD: effectiveMicroUSD,
		SubscriptionID:           subscriptionID,
		RecordedAt:               time.Now(),
	}
	// 初次写入
	err := database.DB.Create(&revenue).Error
	if err == nil {
		return
	}
	// 3 次重试，指数退避：50ms × 2^(retry-1) = 50 / 100 / 200ms
	for retry := 1; retry <= 3; retry++ {
		time.Sleep(time.Duration(50<<(retry-1)) * time.Millisecond)
		err = database.DB.Create(&revenue).Error
		if err == nil {
			return
		}
	}
	log.Printf("[BILLING-REVENUE-LOST] log_id=%d source=%s effective=%d sub_id=%d write failed after 1+3 retries: %v — manual reconcile required",
		apiLogID, source, effectiveMicroUSD, subscriptionID, err)
}

func requestIndicatesThinking(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	if indicatesEnabledThinking(gjson.GetBytes(body, "thinking")) {
		return true
	}
	if indicatesEnabledThinking(gjson.GetBytes(body, "reasoning")) {
		return true
	}
	if indicatesEnabledThinking(gjson.GetBytes(body, "generationConfig.thinkingConfig")) {
		return true
	}
	reasoningEffort := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "reasoning_effort").String()))
	return reasoningEffort != "" && reasoningEffort != "none" && reasoningEffort != "off" && reasoningEffort != "disabled"
}

func indicatesEnabledThinking(v gjson.Result) bool {
	if !v.Exists() {
		return false
	}
	switch v.Type {
	case gjson.True:
		return true
	case gjson.False, gjson.Null:
		return false
	case gjson.String:
		return isEnabledThinkingValue(v.String())
	case gjson.Number:
		return v.Float() > 0
	}
	for _, key := range []string{"type", "effort", "mode"} {
		if raw := strings.TrimSpace(v.Get(key).String()); raw != "" {
			return isEnabledThinkingValue(raw)
		}
	}
	for _, key := range []string{"budget_tokens", "budgetTokens", "thinkingBudget", "thinking_budget", "max_tokens"} {
		if budget := v.Get(key); budget.Exists() {
			return budget.Float() > 0
		}
	}
	return false
}

func isEnabledThinkingValue(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "none", "off", "disabled", "disable", "false", "0":
		return false
	default:
		return true
	}
}

func loadBillingWeightRules(configKey string, fallback []BillingWeightRule) []BillingWeightRule {
	raw := sysConfigValue(configKey)
	if strings.TrimSpace(raw) == "" {
		return cloneBillingRules(fallback)
	}
	var rules []BillingWeightRule
	if err := json.Unmarshal([]byte(raw), &rules); err != nil {
		log.Printf("[BILLING-RULES] invalid %s json, using defaults: %v", configKey, err)
		return cloneBillingRules(fallback)
	}
	clean := make([]BillingWeightRule, 0, len(rules))
	for _, r := range rules {
		r.Pattern = strings.TrimSpace(r.Pattern)
		if r.Pattern == "" || !validPositiveMultiplier(r.Weight) {
			continue
		}
		if r.ThinkingWeight != 0 && !validPositiveMultiplier(r.ThinkingWeight) {
			r.ThinkingWeight = 0
		}
		clean = append(clean, r)
	}
	if len(clean) == 0 {
		return cloneBillingRules(fallback)
	}
	return clean
}

func matchBillingWeight(modelName string, thinking bool, rules []BillingWeightRule) float64 {
	lowerModel := strings.ToLower(strings.TrimSpace(modelName))
	for _, r := range rules {
		pattern := strings.ToLower(strings.TrimSpace(r.Pattern))
		matched := false
		if pattern == lowerModel {
			matched = true
		} else if ok, err := path.Match(pattern, lowerModel); err == nil && ok {
			matched = true
		}
		if !matched {
			continue
		}
		if thinking && validPositiveMultiplier(r.ThinkingWeight) {
			return r.ThinkingWeight
		}
		return r.Weight
	}
	return 1.0
}

// applyBillingMultiplier 把 multiplier (PPM 整数) 应用到已 ceil-div 的 micro_usd 成本。
//
// fix CRITICAL Sprint4-M2：旧实现使用 floor div（big.Int.Div）会让"已 ceil 到 1 micro
// 的低成本请求"再经 multiplier<1 时被截断到 0 micro（"免费消耗"漏洞从 checkedCostMicroUSD
// 出口移动到这里）。
//
// 举例：
//   cost=2 micro × multiplier=0.3 → 2 × 300000 / 1e6 = 0.6
//   旧 floor: 0 micro  → 免费消耗 ❌
//   新 ceil:  1 micro  → 平台至少收 1 micro ✓
//
// 修复：对**正数结果**使用 ceil-div：(a + b - 1) / b（a, b > 0 时等价 ⌈a/b⌉）。
// 与 checkedCostMicroUSD 的 ceil 策略一致，保证全链路平台永不少收。
//
// fix HIGH（codex cross-cutting）：原实现在 multiplier 非法时 silent 退回 MultiplierPPMBase（=1x），
// 但调用方传入 `ModelWeight * HealthMultiplier`，两个 admin-合法值（≤1000 各自）相乘可达
// 1e6，触发 multiplierPPMFromFloat 的 ppm > MaxBillingMultiplierPPM 判 invalid → silent 1x
// → 严重少扣费。改为：乘积超出上限时 clamp 到 MaxBillingMultiplierPPM（≈1000x），不是退回 1x。
// 同时 log 告警让 admin 能看到自己设的规则被夹到上限。
func applyBillingMultiplier(costMicroUSD int64, multiplier float64) int64 {
	if costMicroUSD <= 0 {
		return 0
	}
	multiplierPPM, ok := multiplierPPMFromFloat(multiplier)
	if !ok {
		// 非法/溢出 → clamp 到上限，绝不 silent 退回 1x（旧实现的灾难性少扣费）。
		if !math.IsNaN(multiplier) && !math.IsInf(multiplier, 0) && multiplier > 0 {
			multiplierPPM = database.MaxBillingMultiplierPPM
			log.Printf("[BILLING-MULTIPLIER-CLAMP] product=%g exceeded limit, clamped to %d ppm (%.2fx)",
				multiplier, multiplierPPM, float64(multiplierPPM)/float64(database.MultiplierPPMBase))
		} else {
			// 真正非法（NaN / Inf / ≤0）→ 1x 兜底（保持原行为，但 log 告警）
			multiplierPPM = database.MultiplierPPMBase
			log.Printf("[BILLING-MULTIPLIER-INVALID] product=%g invalid, fell back to 1x", multiplier)
		}
	}

	value := new(big.Int).Mul(big.NewInt(costMicroUSD), big.NewInt(multiplierPPM))
	// Ceil-div：value > 0 时 (value + base - 1) / base = ⌈value/base⌉
	divisor := big.NewInt(database.MultiplierPPMBase)
	if value.Sign() > 0 {
		value.Add(value, new(big.Int).Sub(divisor, big.NewInt(1)))
	}
	value.Quo(value, divisor)
	if !value.IsInt64() {
		return math.MaxInt64
	}
	return value.Int64()
}

func validPositiveMultiplier(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v > 0 && v <= 1000
}

func multiplierPPMFromFloat(v float64) (int64, bool) {
	if math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
		return 0, false
	}
	ppm := math.Round(v * float64(database.MultiplierPPMBase))
	if ppm <= 0 || ppm > float64(database.MaxBillingMultiplierPPM) {
		return 0, false
	}
	return int64(ppm), true
}

func billingRulesVersion() string {
	if v := strings.TrimSpace(sysConfigValue(BillingRulesVersionConfigKey)); v != "" {
		return v
	}
	return "default-active-series-2026-05-17"
}

// extractEffectiveSinceFromVersion 从 "default-2026-05-13" / "v1-2026-05-13" 这类
// 版本号尾段提取日期 (YYYY-MM-DD) 作为生效日期。不识别则返回空串，前端按"-"渲染。
func extractEffectiveSinceFromVersion(version string) string {
	v := strings.TrimSpace(version)
	if len(v) < 10 {
		return ""
	}
	tail := v[len(v)-10:]
	if tail[4] != '-' || tail[7] != '-' {
		return ""
	}
	for i, c := range tail {
		if i == 4 || i == 7 {
			continue
		}
		if c < '0' || c > '9' {
			return ""
		}
	}
	return tail
}

func sysConfigValue(key string) string {
	SysConfigMutex.RLock()
	defer SysConfigMutex.RUnlock()
	if SysConfigCache == nil {
		return ""
	}
	return SysConfigCache[key]
}

func cloneBillingRules(in []BillingWeightRule) []BillingWeightRule {
	out := make([]BillingWeightRule, len(in))
	copy(out, in)
	return out
}

func billingRulesJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func DefaultBillingModelWeightsJSON() string {
	return billingRulesJSON(defaultBillingModelWeights)
}

func DefaultBillingHealthMultipliersJSON() string {
	return billingRulesJSON(defaultBillingHealthMultipliers)
}

func FormatChargedCostForDescription(rawCost, chargedCost int64) string {
	return "raw=" + database.FormatMicroUSD(rawCost) + " charged=" + database.FormatMicroUSD(chargedCost)
}
