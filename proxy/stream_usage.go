// Package proxy / stream_usage.go
//
// M-R2 重构（2026-05-19）：从 stream.go 抽出 usage 相关 helper，纯文件物理拆分。
// 业务逻辑零改动；handler ChatCompletionProxyHandler 仍在 stream.go。

package proxy

import (
	"math/big"

	"daof-cpa/database"

	"github.com/tidwall/gjson"
)

type usageTokenCounts struct {
	PromptTokens        int
	CompletionTokens    int
	CachedTokens        int
	CacheWriteTokens    int
	CacheWrite5mTokens  int
	CacheWrite1hTokens  int
	ReasoningTokens     int
	HasPromptTokens     bool
	HasCompletionTokens bool
	HasCachedTokens     bool
	HasCacheWriteTokens bool
	HasReasoningTokens  bool
}

func (u usageTokenCounts) HasAny() bool {
	return u.HasPromptTokens || u.HasCompletionTokens || u.HasCachedTokens || u.HasCacheWriteTokens || u.HasReasoningTokens
}

func (u usageTokenCounts) HasBillableTokens() bool {
	return u.PromptTokens+u.CompletionTokens > 0
}

func extractUsageTokenCounts(usage gjson.Result) usageTokenCounts {
	var out usageTokenCounts
	if !usage.Exists() {
		return out
	}

	promptTokens, hasPromptTokens := usageInt(usage, "prompt_tokens")
	inputTokens, hasInputTokens := usageInt(usage, "input_tokens")
	geminiPromptTokens, hasGeminiPromptTokens := usageInt(usage, "promptTokenCount", "prompt_token_count")
	if hasPromptTokens {
		out.PromptTokens = promptTokens
		out.HasPromptTokens = true
	} else if hasInputTokens {
		out.PromptTokens = inputTokens
		out.HasPromptTokens = true
	} else if hasGeminiPromptTokens {
		out.PromptTokens = geminiPromptTokens
		out.HasPromptTokens = true
	}

	if v, ok := usageInt(usage,
		"completion_tokens",
		"output_tokens",
		"candidatesTokenCount",
		"candidates_token_count",
	); ok {
		out.CompletionTokens = v
		out.HasCompletionTokens = true
	}
	if v, ok := usageInt(usage,
		"prompt_tokens_details.cached_tokens",
		"input_tokens_details.cached_tokens",
		"cache_read_input_tokens",
		"cachedContentTokenCount",
		"cached_content_token_count",
	); ok {
		out.CachedTokens = v
		out.HasCachedTokens = true
	}
	cacheWrite5mTokens, hasCacheWrite5mTokens := usageInt(usage,
		"cache_creation.ephemeral_5m_input_tokens",
		"cache_creation.ephemeral5m_input_tokens",
		"cache_creation_5m_input_tokens",
		"cache_write_5m_input_tokens",
	)
	cacheWrite1hTokens, hasCacheWrite1hTokens := usageInt(usage,
		"cache_creation.ephemeral_1h_input_tokens",
		"cache_creation.ephemeral1h_input_tokens",
		"cache_creation_1h_input_tokens",
		"cache_write_1h_input_tokens",
	)
	if hasCacheWrite5mTokens || hasCacheWrite1hTokens {
		out.CacheWrite5mTokens = cacheWrite5mTokens
		out.CacheWrite1hTokens = cacheWrite1hTokens
		out.CacheWriteTokens = cacheWrite5mTokens + cacheWrite1hTokens
		out.HasCacheWriteTokens = true
	} else if v, ok := usageInt(usage,
		"cache_creation_input_tokens",
		"cache_write_input_tokens",
		"prompt_tokens_details.cache_creation_tokens",
		"input_tokens_details.cache_creation_tokens",
	); ok {
		out.CacheWriteTokens = v
		out.CacheWrite5mTokens = v
		out.HasCacheWriteTokens = true
	}
	if v, ok := usageInt(usage,
		"completion_tokens_details.reasoning_tokens",
		"output_tokens_details.reasoning_tokens",
		"reasoning_tokens",
		"thoughtsTokenCount",
		"thoughts_token_count",
	); ok {
		out.ReasoningTokens = v
		out.HasReasoningTokens = true
	}
	// Gemini usageMetadata reports candidatesTokenCount and thoughtsTokenCount separately.
	// Treat thoughts as output-side reasoning so billing and charts include the full delivered output.
	if out.HasReasoningTokens && (usage.Get("thoughtsTokenCount").Exists() || usage.Get("thoughts_token_count").Exists()) {
		out.CompletionTokens += out.ReasoningTokens
		out.HasCompletionTokens = true
	}
	if !out.HasPromptTokens && (out.HasCachedTokens || out.HasCacheWriteTokens) {
		out.PromptTokens = out.CachedTokens + out.CacheWriteTokens
		out.HasPromptTokens = true
	}

	// OpenAI prompt/input token totals already include cached tokens when details are present.
	// Anthropic Messages reports cache read/write tokens as separate top-level counters, so
	// add them into the total prompt side for billing and observability.
	promptIncludesCache := hasPromptTokens ||
		hasGeminiPromptTokens ||
		usage.Get("prompt_tokens_details").Exists() ||
		usage.Get("input_tokens_details").Exists() ||
		usage.Get("promptTokenCount").Exists() ||
		usage.Get("prompt_token_count").Exists()
	if out.HasPromptTokens && !promptIncludesCache {
		out.PromptTokens += out.CachedTokens + out.CacheWriteTokens
	}

	return out
}

func usageInt(usage gjson.Result, paths ...string) (int, bool) {
	for _, path := range paths {
		v := usage.Get(path)
		if v.Exists() {
			return int(v.Int()), true
		}
	}
	return 0, false
}

// checkedCostMicroUSD 用 fixed-point int64 + big.Int 守护的整数化 cost 计算。
//
// 公式：sum(tokens_i × pico_usd_per_token_i) ÷ 1e9 → micro_usd。
//
// fix CRITICAL Phase 3：价格从 USD/M-token float 改为 pico_usd/token int64。
// 所有乘法在 big.Int 中完成，最后只做一次整数除法，杜绝 float round 累积偏差。
//
// 负 token、负价格、异常高价或 int64 溢出都会破坏财务守恒。本函数 fail-closed：
// 异常返回 (0, false)，调用方不扣不计。
//
// 参数采用 (token, pricePicoPerToken) 6 对，与 deductQuota 费用项对齐。
// 0 价格档位（如无 cached price）传 0/0 即可，对结果无贡献。
//
// fix CRITICAL Sprint1-P0-4：pico_usd → micro_usd 转换使用 **ceil-div**（正数向上取整）。
// 旧实现 `total.Div(total, ...)` 是 floor，低价 × 小 token 请求（pico cost < 1e9）会被
// 截断到 0 micro_usd，形成"免费消耗"。改为 ceil 后：
//   - 0 pico → 0 micro_usd（保持）
//   - 1..1e9 pico → 1 micro_usd（最小 1 micro 收费）
//   - 1e9+k pico → 2 micro_usd（向上进位 1）
//
// 平台侧永不少收。pico 是 1e-15 USD，1 micro_usd 进位上限约 1e-6 USD，单请求误差可忽略。
func checkedCostMicroUSD(t1 int, p1 int64, t2 int, p2 int64, t3 int, p3 int64, t4 int, p4 int64, t5 int, p5 int64, t6 int, p6 int64) (int64, bool) {
	total := new(big.Int)
	add := func(tokens int, pricePico int64) bool {
		if tokens < 0 || pricePico < 0 || pricePico > database.MaxChannelModelPricePicoPerToken {
			return false
		}
		if tokens == 0 || pricePico == 0 {
			return true
		}
		term := new(big.Int).Mul(big.NewInt(int64(tokens)), big.NewInt(pricePico))
		total.Add(total, term)
		return true
	}
	if !add(t1, p1) || !add(t2, p2) || !add(t3, p3) || !add(t4, p4) || !add(t5, p5) || !add(t6, p6) {
		return 0, false
	}
	// Ceil-div：(total + divisor - 1) / divisor 对 total ≥ 0 等价于 ⌈total/divisor⌉
	divisor := big.NewInt(database.PicoPerMicroUSD)
	if total.Sign() > 0 {
		adjustment := new(big.Int).Sub(divisor, big.NewInt(1))
		total.Add(total, adjustment)
	}
	total.Quo(total, divisor)
	if !total.IsInt64() {
		return 0, false
	}
	return total.Int64(), true
}
