package proxy

import (
	"encoding/json"
	"log"
	"math"
	"math/big"
	"path"
	"strings"

	"daof-ai-hub/database"

	"github.com/tidwall/gjson"
)

const (
	BillingModelWeightsConfigKey        = "billing_model_weights_json"
	BillingHealthMultipliersConfigKey   = "billing_health_multipliers_json"
	BillingProviderCostFactorsConfigKey = "billing_provider_cost_factors_json"
	BillingRulesVersionConfigKey        = "billing_rules_version"
)

type BillingWeightRule struct {
	Pattern        string  `json:"pattern"`
	Weight         float64 `json:"weight"`
	ThinkingWeight float64 `json:"thinking_weight,omitempty"`
	Label          string  `json:"label,omitempty"`
	Reason         string  `json:"reason,omitempty"`
}

type BillingRuleResolution struct {
	RequestedModel            string  `json:"requested_model"`
	ServedModel               string  `json:"served_model"`
	ModelWeight               float64 `json:"model_weight"`
	HealthMultiplier          float64 `json:"health_multiplier"`
	ProviderCostFactor        float64 `json:"provider_cost_factor"`
	BillingRulesVersion       string  `json:"billing_rules_version"`
	FallbackUserOptIn         bool    `json:"fallback_user_opt_in"`
	FallbackReason            string  `json:"fallback_reason,omitempty"`
	RawCostMicroUSD           int64   `json:"-"`
	ChargedCostMicroUSD       int64   `json:"-"`
	PlatformCostEstimateMicro int64   `json:"-"`
}

// PublicBillingRules 是 /api/billing/rules 对外公开的计费规则。
// 故意 *不* 暴露 ProviderCostFactors —— 那是平台内部毛利估算依据（platform_cost_estimate 计算因子），
// 非用户视角，admin 通过 SysConfig 直查。
type PublicBillingRules struct {
	Version           string              `json:"version"`
	ModelWeights      []BillingWeightRule `json:"model_weights"`
	HealthMultipliers []BillingWeightRule `json:"health_multipliers"`
	Fallback          map[string]string   `json:"fallback"`
	Notes             []string            `json:"notes"`
}

var defaultBillingModelWeights = []BillingWeightRule{
	{Pattern: "*haiku*", Weight: 0.3, Label: "Claude Haiku", Reason: "低成本/轻量模型"},
	{Pattern: "*sonnet*", Weight: 1.0, ThinkingWeight: 1.5, Label: "Claude Sonnet", Reason: "Claude 基准模型；thinking 启用时加权"},
	{Pattern: "*opus*", Weight: 3.5, ThinkingWeight: 5.0, Label: "Claude Opus", Reason: "高成本/高额度消耗模型"},
	{Pattern: "*gemini*flash*", Weight: 0.4, Label: "Gemini Flash", Reason: "低成本快速模型"},
	{Pattern: "*gemini*pro*", Weight: 0.9, Label: "Gemini Pro", Reason: "Gemini 主力模型"},
	{Pattern: "*gpt*mini*", Weight: 0.5, Label: "GPT mini", Reason: "低成本模型"},
	{Pattern: "*o1*", Weight: 2.5, Label: "OpenAI reasoning", Reason: "高推理成本模型"},
	{Pattern: "*o3*", Weight: 3.5, Label: "OpenAI reasoning", Reason: "高推理成本模型"},
	{Pattern: "*gpt*", Weight: 1.0, Label: "GPT", Reason: "OpenAI 基准模型"},
}

var defaultBillingHealthMultipliers = []BillingWeightRule{
	{Pattern: "*", Weight: 1.0, Label: "Normal", Reason: "默认无高峰加权"},
}

var defaultProviderCostFactors = map[string]float64{
	"anthropic":  1.0,
	"openai":     1.0,
	"gemini":     1.0,
	"google-cli": 1.0,
	"codex":      1.0,
	"cliproxy":   1.0,
	"unknown":    1.0,
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
	provider := inferBillingProvider(channelType, modelName)
	providerFactor := loadProviderCostFactors()[provider]
	if !validPositiveMultiplier(providerFactor) {
		providerFactor = 1
	}
	version := billingRulesVersion()
	return BillingRuleResolution{
		RequestedModel:      modelName,
		ServedModel:         modelName,
		ModelWeight:         modelWeight,
		HealthMultiplier:    healthMultiplier,
		ProviderCostFactor:  providerFactor,
		BillingRulesVersion: version,
		FallbackUserOptIn:   fallbackOptIn,
	}
}

func (r BillingRuleResolution) WithCosts(rawCostMicroUSD int64) BillingRuleResolution {
	r.RawCostMicroUSD = rawCostMicroUSD
	r.ChargedCostMicroUSD = applyBillingMultiplier(rawCostMicroUSD, r.ModelWeight*r.HealthMultiplier)
	r.PlatformCostEstimateMicro = applyBillingMultiplier(rawCostMicroUSD, r.ProviderCostFactor)
	return r
}

func GetPublicBillingRules() PublicBillingRules {
	return PublicBillingRules{
		Version:           billingRulesVersion(),
		ModelWeights:      loadBillingWeightRules(BillingModelWeightsConfigKey, defaultBillingModelWeights),
		HealthMultipliers: loadBillingWeightRules(BillingHealthMultipliersConfigKey, defaultBillingHealthMultipliers),
		Fallback: map[string]string{
			"default":            "off",
			"per_request_header": "X-Allow-Fallback: true",
			"rule":               "Only user opt-in fallback may change served_model; otherwise requested_model must equal served_model.",
		},
		Notes: []string{
			"cost/raw_cost 是官方 API 等值美元成本。",
			"charged_cost 是套餐/credits 扣减成本：raw_cost × model_weight × health_multiplier。",
		},
	}
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

func loadProviderCostFactors() map[string]float64 {
	out := make(map[string]float64, len(defaultProviderCostFactors))
	for k, v := range defaultProviderCostFactors {
		out[k] = v
	}
	raw := sysConfigValue(BillingProviderCostFactorsConfigKey)
	if strings.TrimSpace(raw) == "" {
		return out
	}
	var parsed map[string]float64
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		log.Printf("[BILLING-RULES] invalid %s json, using defaults: %v", BillingProviderCostFactorsConfigKey, err)
		return out
	}
	for k, v := range parsed {
		k = strings.ToLower(strings.TrimSpace(k))
		if k == "" || !validPositiveMultiplier(v) {
			continue
		}
		out[k] = v
	}
	return out
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

func applyBillingMultiplier(costMicroUSD int64, multiplier float64) int64 {
	if costMicroUSD <= 0 {
		return 0
	}
	multiplierPPM, ok := multiplierPPMFromFloat(multiplier)
	if !ok {
		multiplierPPM = database.MultiplierPPMBase
	}

	value := new(big.Int).Mul(big.NewInt(costMicroUSD), big.NewInt(multiplierPPM))
	value.Div(value, big.NewInt(database.MultiplierPPMBase))
	if !value.IsInt64() {
		return math.MaxInt64
	}
	return value.Int64()
}

func validPositiveMultiplier(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v > 0 && v <= 1000
}

func multiplierPPMFromFloat(v float64) (int64, bool) {
	if !validPositiveMultiplier(v) {
		return 0, false
	}
	ppm := math.Round(v * float64(database.MultiplierPPMBase))
	if ppm <= 0 || ppm > float64(database.MaxBillingMultiplierPPM) {
		return 0, false
	}
	return int64(ppm), true
}

func inferBillingProvider(channelType, modelName string) string {
	normalized := NormalizeChannelType(channelType)
	switch normalized {
	case ChannelTypeAnthropic:
		return "anthropic"
	case ChannelTypeOpenAI:
		return "openai"
	case ChannelTypeGemini:
		return "gemini"
	case ChannelTypeGoogleCLI:
		return "google-cli"
	case ChannelTypeCodex:
		return "codex"
	case ChannelTypeCLIProxy:
		lower := strings.ToLower(modelName)
		switch {
		case strings.Contains(lower, "claude") || strings.Contains(lower, "opus") || strings.Contains(lower, "sonnet") || strings.Contains(lower, "haiku"):
			return "anthropic"
		case strings.Contains(lower, "gemini"):
			return "gemini"
		case strings.Contains(lower, "codex"):
			return "codex"
		case strings.Contains(lower, "gpt") || strings.Contains(lower, "o1") || strings.Contains(lower, "o3"):
			return "openai"
		default:
			return "cliproxy"
		}
	default:
		return "unknown"
	}
}

func billingRulesVersion() string {
	if v := strings.TrimSpace(sysConfigValue(BillingRulesVersionConfigKey)); v != "" {
		return v
	}
	return "default-2026-05-13"
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

func DefaultBillingProviderCostFactorsJSON() string {
	return billingRulesJSON(defaultProviderCostFactors)
}

func FormatChargedCostForDescription(rawCost, chargedCost int64) string {
	return "raw=" + database.FormatMicroUSD(rawCost) + " charged=" + database.FormatMicroUSD(chargedCost)
}
