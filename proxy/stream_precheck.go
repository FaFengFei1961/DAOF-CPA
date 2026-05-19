// Package proxy / stream_precheck.go
//
// M-R2 重构（2026-05-19）：从 stream.go 抽出 precheck 相关 helper，纯文件物理拆分。
// 业务逻辑零改动；handler ChatCompletionProxyHandler 仍在 stream.go。

package proxy

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"

	"daof-cpa/database"

	"github.com/gofiber/fiber/v2"
	"github.com/tidwall/gjson"
)

func estimateTextPrecheckTokens(s string) int {
	cjkRunes := 0
	asciiLikeRunes := 0
	otherRunes := 0
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Han, r), unicode.Is(unicode.Hiragana, r), unicode.Is(unicode.Katakana, r), unicode.Is(unicode.Hangul, r):
			cjkRunes++
		case r <= 0x7f:
			asciiLikeRunes++
		default:
			otherRunes++
		}
	}
	ceilDiv := func(n, d int) int {
		if n <= 0 {
			return 0
		}
		return (n + d - 1) / d
	}
	return cjkRunes + ceilDiv(asciiLikeRunes, 2) + ceilDiv(otherRunes, 2)
}

// estimatePrecheckTokens 给 Decide(IsPrecheck=true) 用的粗粒度 token 估算。
//
// 真实 token 数要等上游 tokenizer 跑过才能拿到；precheck 不能等。
//
// 累加范围：messages/prompt/input + Anthropic 顶层 system + tools/functions schema 字符数。
// 多模态非文本部分（image/audio/video/file）按固定常数加 token。
func estimatePrecheckTokens(body []byte) int {
	totalTokens := 0
	addText := func(s string) {
		totalTokens += estimateTextPrecheckTokens(s)
	}
	// 多模态非文本占位（image/audio/video）— 每个 part 加保守常数
	nonTextParts := 0
	addContentPart := func(p gjson.Result) {
		if p.Type == gjson.String {
			addText(p.String())
			return
		}
		t := strings.ToLower(strings.TrimSpace(p.Get("type").String()))
		switch t {
		case "text", "input_text", "output_text", "":
			if text := p.Get("text"); text.Exists() {
				addText(text.String())
				return
			}
			if p.Get("image_url").Exists() || p.Get("source").Exists() || p.Get("inline_data").Exists() || p.Get("file_data").Exists() {
				nonTextParts++
			}
		case "image", "image_url", "input_image", "input_audio", "audio", "video", "file", "input_file":
			nonTextParts++
		default:
			if text := p.Get("text"); text.Exists() {
				addText(text.String())
			} else {
				nonTextParts++
			}
		}
	}
	addContent := func(content gjson.Result) {
		if !content.Exists() {
			return
		}
		if content.IsArray() {
			content.ForEach(func(_, p gjson.Result) bool {
				addContentPart(p)
				return true
			})
			return
		}
		addContentPart(content)
	}

	// messages: [{role, content}] 数组（OpenAI/Anthropic 兼容）
	if msgs := gjson.GetBytes(body, "messages"); msgs.IsArray() {
		msgs.ForEach(func(_, m gjson.Result) bool {
			addText(m.Get("role").String())
			addContent(m.Get("content"))
			return true
		})
	}

	// Anthropic 顶层 system（与 messages 平级）
	if sys := gjson.GetBytes(body, "system"); sys.Exists() {
		if sys.IsArray() {
			sys.ForEach(func(_, p gjson.Result) bool {
				addText(p.Get("text").String())
				return true
			})
		} else {
			addText(sys.String())
		}
	}

	// prompt: 字符串或字符串数组（completions API）
	if prompt := gjson.GetBytes(body, "prompt"); prompt.Exists() {
		if prompt.IsArray() {
			prompt.ForEach(func(_, p gjson.Result) bool {
				addText(p.String())
				return true
			})
		} else {
			addText(prompt.String())
		}
	}
	// input: OpenAI Responses API / embeddings API
	if input := gjson.GetBytes(body, "input"); input.Exists() {
		if input.IsArray() {
			input.ForEach(func(_, item gjson.Result) bool {
				if item.Type == gjson.String {
					addText(item.String())
					return true
				}
				addText(item.Get("role").String())
				addContent(item.Get("content"))
				if text := item.Get("text"); text.Exists() {
					addText(text.String())
				}
				if output := item.Get("output"); output.Exists() {
					addContent(output)
				}
				if arguments := item.Get("arguments"); arguments.Exists() {
					addText(arguments.String())
				}
				return true
			})
		} else {
			addText(input.String())
		}
	}
	if ins := gjson.GetBytes(body, "instructions"); ins.Exists() {
		addText(ins.String())
	}
	// Gemini contents / systemInstruction
	if contents := gjson.GetBytes(body, "contents"); contents.IsArray() {
		contents.ForEach(func(_, c gjson.Result) bool {
			addText(c.Get("role").String())
			c.Get("parts").ForEach(func(_, p gjson.Result) bool {
				if text := p.Get("text"); text.Exists() {
					addText(text.String())
				} else {
					nonTextParts++
				}
				return true
			})
			return true
		})
	}
	if sys := gjson.GetBytes(body, "systemInstruction.parts"); sys.IsArray() {
		sys.ForEach(func(_, p gjson.Result) bool {
			if text := p.Get("text"); text.Exists() {
				addText(text.String())
			}
			return true
		})
	}
	// tools / functions schema（OpenAI tool calling）— description + parameters JSON 都计入
	if tools := gjson.GetBytes(body, "tools"); tools.IsArray() {
		tools.ForEach(func(_, p gjson.Result) bool {
			addText(p.Raw) // 整个 tool 定义当文本估算
			return true
		})
	}
	if functions := gjson.GetBytes(body, "functions"); functions.IsArray() {
		functions.ForEach(func(_, p gjson.Result) bool {
			addText(p.Raw)
			return true
		})
	}

	estimated := totalTokens + nonTextParts*200
	if estimated < 1 && totalTokens > 0 {
		estimated = 1
	}
	return estimated
}

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}


func precheckQuotaMicroValues(decision EngineDecision) (limit, used, remaining int64) {
	if decision.BlockUnit != "api_cost_usd" {
		return 0, 0, 0
	}
	if decision.BlockLimitMicroUSD > 0 || decision.BlockConsumedMicroUSD > 0 || decision.BlockRemainingMicroUSD > 0 {
		return decision.BlockLimitMicroUSD, decision.BlockConsumedMicroUSD, decision.BlockRemainingMicroUSD
	}
	limit, _ = database.USDToMicro(decision.BlockLimitValue)
	used, _ = database.USDToMicro(decision.BlockConsumedValue)
	remaining, _ = database.USDToMicro(math.Max(0, decision.BlockRemaining))
	return
}

func precheckLimitMessage(decision EngineDecision, billing BillingRuleResolution) string {
	remaining := math.Max(0, decision.BlockRemaining)
	if decision.BlockUnit == "api_cost_usd" {
		return fmt.Sprintf("本次请求预估消耗 %.6f credits，超过当前窗口剩余额度 %.6f credits。请减少上下文、等待窗口恢复，或开启余额兜底。", database.MicroToUSD(billing.ChargedCostMicroUSD), remaining)
	}
	if decision.BlockUnit != "" {
		return fmt.Sprintf("本次请求预估消耗 %.0f %s，超过当前窗口剩余额度 %.0f %s。请减少上下文或等待窗口恢复。", decision.BlockDelta, decision.BlockUnit, remaining, decision.BlockUnit)
	}
	return "本次请求预估消耗超过当前窗口剩余额度。请减少上下文、等待窗口恢复，或开启余额兜底。"
}

func precheckLimitErrorPayload(message string, decision EngineDecision, inputTokens, outputTokens int, billing BillingRuleResolution) fiber.Map {
	details := fiber.Map{
		"block_reason":           "request_estimate_exceeds_window_remaining",
		"precheck_input_tokens":  inputTokens,
		"precheck_output_tokens": outputTokens,
		"precheck_raw_cost":      database.MicroToUSD(billing.RawCostMicroUSD),
		"precheck_charged_cost":  database.MicroToUSD(billing.ChargedCostMicroUSD),
		"model_weight":           billing.ModelWeight,
		"health_multiplier":      billing.HealthMultiplier,
		"quota_plan_id":          decision.BlockQuotaPlanID,
		"quota_unit":             decision.BlockUnit,
		"quota_limit":            decision.BlockLimitValue,
		"quota_used":             decision.BlockConsumedValue,
		"quota_remaining":        math.Max(0, decision.BlockRemaining),
	}
	if decision.BlockWindowEndAt != nil {
		details["window_end_at"] = decision.BlockWindowEndAt.Format(time.RFC3339)
	}
	return fiber.Map{"error": fiber.Map{
		"message":      message,
		"type":         "subscription_required",
		"code":         "request_estimate_exceeds_window_remaining",
		"message_code": "ERR_REQUEST_ESTIMATE_EXCEEDS_WINDOW_REMAINING",
		"details":      details,
	}}
}

func parseAllowFallbackHeader(c *fiber.Ctx) bool {
	v := strings.ToLower(strings.TrimSpace(c.Get("X-Allow-Fallback")))
	return v == "true" || v == "1" || v == "yes" || v == "on"
}

func setModelAuditHeaders(c *fiber.Ctx, requestedModel, servedModel string, fallbackOptIn bool, fallbackReason string) {
	if strings.TrimSpace(requestedModel) != "" {
		c.Set("X-Requested-Model", requestedModel)
	}
	if strings.TrimSpace(servedModel) != "" {
		c.Set("X-Served-Model", servedModel)
	}
	c.Set("X-Fallback-Allowed", strconv.FormatBool(fallbackOptIn))
	c.Set("X-Fallback-Applied", strconv.FormatBool(fallbackReason != ""))
	if fallbackReason != "" {
		c.Set("X-Fallback-Reason", sanitizeError(fallbackReason, 160))
	}
}


func estimatePrecheckBalanceDelta(modelName string, inputTokens, outputTokens int) int64 {
	const fallbackPricePicoPerToken = 30 * database.PicoPerTokenPerUSDPerMTok // $30/M tokens 保守上界
	const minDeltaMicroUSD = int64(100)                                       // $0.0001 = 100 micro_usd 最低估算下限

	maxInputPico := int64(0)
	maxOutputPico := int64(0)

	gatewayMutex.RLock()
	routes := RouteCache[modelName]
	gatewayMutex.RUnlock()

	for _, r := range routes {
		if r == nil {
			continue
		}
		inP := r.InputPricePicoPerToken
		outP := r.OutputPricePicoPerToken
		// 与 commit 路径保持一致：只有估算输入 token 达到上下文阈值时，才启用长上下文高价档。
		if r.ContextPriceThreshold > 0 && inputTokens >= r.ContextPriceThreshold {
			if r.HighInputPricePicoPerToken > 0 {
				inP = r.HighInputPricePicoPerToken
			}
			if r.HighOutputPricePicoPerToken > 0 {
				outP = r.HighOutputPricePicoPerToken
			}
		}
		if inP > maxInputPico {
			maxInputPico = inP
		}
		if outP > maxOutputPico {
			maxOutputPico = outP
		}
	}
	if maxInputPico <= 0 {
		maxInputPico = fallbackPricePicoPerToken
	}
	if maxOutputPico <= 0 {
		maxOutputPico = fallbackPricePicoPerToken
	}

	// tokens × pico_usd/token ÷ 1e9 = micro_usd。
	// 用 checkedCostMicroUSD 加固以防负数/溢出 → fail-closed 时退到最低估算（避免免费透支）
	delta, ok := checkedCostMicroUSD(
		inputTokens, maxInputPico,
		0, 0,
		outputTokens, maxOutputPico,
		0, 0,
		0, 0,
		0, 0,
	)
	if !ok || delta < minDeltaMicroUSD {
		delta = minDeltaMicroUSD
	}
	return delta
}
