// Package proxy / moderation_response.go
//
// 审核命中后向客户端返回**协议感知**的拒绝响应。
//
// 设计动机（codex 第二十三轮反馈）：
//   - **绝不**透传 OpenAI Moderation 的 category / score 给客户端 ——
//     防"反向工程"：测试者把 score 当 fitness function 微调 prompt 反复试，最后绕过审核
//   - 不同协议的 SDK 对错误结构 schema 严格解析，结构错了客户端会 throw "malformed response"
//     而非展示给用户友好的拒绝消息（最坏情况：客户端 retry → 把上游配额打爆）
//
// 三协议错误信封（参考各家官方 SDK 实测）：
//   OpenAI:    {error: {message, type, code}}                       (compatible with chat/responses SDK)
//   Anthropic: {type:"error", error:{type:"permission_error", ...}} (Claude SDK 严格按 type 分发)
//   Gemini:    {error:{code, message, status:"PERMISSION_DENIED"}}  (Google API 标准 status enum)
//
// 故意不做：
//   - 流式（SSE）拒绝：moderation 是请求**前**置审核，还没建立 SSE 连接，
//     直接 HTTP 4xx 即可，不需要在 stream 里发 done event
//   - 多语言自动协商：用 SysConfig 双套消息（zh/en），调用方根据 Accept-Language 选
package proxy

import (
	"strings"

	"github.com/gofiber/fiber/v2"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

// ModerationRejectReason 拒绝原因 code（不暴露 OpenAI 内部 category）。
type ModerationRejectReason string

const (
	// ModerationReasonKeyword 命中本地关键字快扫
	ModerationReasonKeyword ModerationRejectReason = "keyword_match"
	// ModerationReasonPolicy 命中 OpenAI Moderation 智能审核
	ModerationReasonPolicy ModerationRejectReason = "policy_violation"
	// ModerationReasonUnavailable Moderation API 不可达且 fail-mode=closed
	ModerationReasonUnavailable ModerationRejectReason = "moderation_unavailable"
	// ModerationReasonOversize prompt 超过 max_chars
	ModerationReasonOversize ModerationRejectReason = "input_too_long"
	// ModerationReasonImagePolicy fix MAJOR R23-M1：image_policy=reject 命中带图请求
	ModerationReasonImagePolicy ModerationRejectReason = "image_policy_reject"
)

// rejectBySourceFormat 按客户端协议返回审核拒绝响应。
//
// 参数：
//   c          - fiber 请求上下文
//   srcFormat  - 客户端请求格式（OpenAI / Claude / Gemini）
//   reason     - 拒绝原因（log/审计用，不直接展示给客户端文案，但 SDK 可读）
//   message    - 给用户看的本地化文案（zh 或 en，已由调用方根据 Accept-Language 选好）
//   httpStatus - HTTP 状态码（403=违规拒绝；503=审核服务不可用）
//
// 注意：不在响应里透传 category / score 数组（防反向工程）。
func rejectBySourceFormat(
	c *fiber.Ctx,
	srcFormat sdktranslator.Format,
	reason ModerationRejectReason,
	message string,
	httpStatus int,
) error {
	if message == "" {
		message = "您的请求被内容审核拦截。"
	}
	switch srcFormat {
	case sdktranslator.FormatClaude:
		return c.Status(httpStatus).JSON(fiber.Map{
			"type": "error",
			"error": fiber.Map{
				"type":    anthropicErrorType(reason, httpStatus),
				"message": message,
			},
		})
	case sdktranslator.FormatGemini, sdktranslator.FormatGeminiCLI:
		return c.Status(httpStatus).JSON(fiber.Map{
			"error": fiber.Map{
				"code":    httpStatus,
				"message": message,
				"status":  geminiStatus(httpStatus),
			},
		})
	default: // OpenAI 兼容（FormatOpenAI / FormatCodex / 未识别格式）
		return c.Status(httpStatus).JSON(fiber.Map{
			"error": fiber.Map{
				"message": message,
				"type":    "content_policy_violation",
				"code":    string(reason),
			},
		})
	}
}

// anthropicErrorType 把 ModerationRejectReason 映射到 Anthropic 错误 type 枚举。
//
// Anthropic SDK 解析的合法 type 集合：
//   invalid_request_error / authentication_error / permission_error / not_found_error /
//   rate_limit_error / api_error / overloaded_error
//
// 审核拒绝最贴近 permission_error；不可用映射到 overloaded_error 让客户端走 backoff retry。
func anthropicErrorType(reason ModerationRejectReason, httpStatus int) string {
	if reason == ModerationReasonUnavailable || httpStatus >= 500 {
		return "overloaded_error"
	}
	if reason == ModerationReasonOversize {
		return "invalid_request_error"
	}
	return "permission_error"
}

// geminiStatus 把 HTTP 状态码映射到 Google API canonical status enum。
//
// 官方枚举：https://cloud.google.com/apis/design/errors#error_model
func geminiStatus(httpStatus int) string {
	switch {
	case httpStatus == 403:
		return "PERMISSION_DENIED"
	case httpStatus == 400:
		return "INVALID_ARGUMENT"
	case httpStatus == 503:
		return "UNAVAILABLE"
	case httpStatus >= 500:
		return "INTERNAL"
	default:
		return "FAILED_PRECONDITION"
	}
}

// inferSourceFormat 根据请求路径推断客户端协议。
//
// stream.go 中已有同等逻辑（line 347-350），抽出来便于审核分支复用 +
// 后续 main.go 注册 Gemini 路径时只需扩展这里一处。
func inferSourceFormat(path string) sdktranslator.Format {
	p := strings.ToLower(path)
	if strings.Contains(p, "/messages") {
		return sdktranslator.FormatClaude
	}
	if strings.Contains(p, "/v1beta/") || strings.Contains(p, ":generatecontent") || strings.Contains(p, ":streamgeneratecontent") {
		return sdktranslator.FormatGemini
	}
	return sdktranslator.FormatOpenAI
}

// PickLocalizedMessage 根据客户端 Accept-Language 从 SysConfig 选 zh/en 拒绝文案。
//
// 逻辑：Accept-Language 含 "zh" → 返回 zh；否则 en。
// SysConfig key 形如 moderation_block_message_zh / moderation_unavailable_message_en。
func PickLocalizedMessage(acceptLang string, zhKey, enKey string) string {
	wantZh := strings.Contains(strings.ToLower(acceptLang), "zh")
	SysConfigMutex.RLock()
	defer SysConfigMutex.RUnlock()
	if wantZh {
		if msg := strings.TrimSpace(SysConfigCache[zhKey]); msg != "" {
			return msg
		}
		// 兜底：zh 缺则降级 en
		return strings.TrimSpace(SysConfigCache[enKey])
	}
	if msg := strings.TrimSpace(SysConfigCache[enKey]); msg != "" {
		return msg
	}
	return strings.TrimSpace(SysConfigCache[zhKey])
}
