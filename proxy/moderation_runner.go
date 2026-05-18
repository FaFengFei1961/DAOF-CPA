// Package proxy / moderation_runner.go
//
// 审核流水线编排：把 keyword_filter / content_moderation / prompt_extract / response 串起来。
//
// 关键决策（codex 第二十三轮反馈，吸收 v2 Critical 反馈）：
//
//  1. **执行时机**：必须在 Decide(IsPrecheck=true) 之后、上游路由发起之前。
//     - 在 Decide 之前会"成本放大"——攻击者用没余额的账号刷请求，每条都打智能审核
//     服务 → 我方账单暴涨。Decide 先把没余额的卡掉。
//     - 在路由之后才有真实 srcFormat，但路由仅做模型查找，不发上游请求，所以
//     moderation 在 routes 解析后但路由循环之前是黄金时机。
//
//  2. **fail-mode 双轨**：
//     - 直连官方上游（openai 直连）→ ChannelModel.ModerationFailMode="closed"，
//     智能审核不可达时拒绝，不能让 jailbreak 透传到官方导致封号
//     - cloaked 路径（CLIProxyAPI 兜底）→ "open"，CLIProxyAPI 自带 cloaking + Anthropic
//     自我拒答，审核服务挂掉时不阻塞业务
//
//  3. **超长 prompt**：超过 max_chars 直接拒绝（不"截断"）——
//     截断会留下"前 N 字符过审，后面违规内容透传"的绕过缝，必须 fail-closed。
//
//  4. **图片策略**：cfg.ImagePolicy 控制
//     - "skip"   → 跳过图片（默认 cloaked）
//     - "submit" → 预留给可审核图片的供应商；当前 CPA 分类器会按不可达处理
//     - "reject" → 直接拒绝带图请求（最严，直连官方时推荐）
package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"time"
	"unicode/utf8"

	"daof-cpa/database"

	"github.com/gofiber/fiber/v2"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

// ModerationGate 单次请求的审核执行单元。
type ModerationGate struct {
	Ctx        *fiber.Ctx
	UserID     uint
	TokenHash  string // 已 hash 的 token，写 ApiLog 用
	Body       []byte
	ModelName  string
	SrcFormat  sdktranslator.Format
	Policy     ModerationPolicy
	ClientIP   string
	StartTime  time.Time
	ReviewText string // 审核用文本；只用于生成脱敏审计预览，不原文落库
}

// defaultModerationAPITimeout 单次智能审核调用的默认硬上限（含 chunk 分片）。
//
// 取值依据：CPA 模型池会经过本地号池、上游模型和冷启动路径，实测轻量分类器也可能
// 到 4-6s。默认 15s 避免官方模型在 fail-closed 下因正常抖动大量 503；仍可通过
// moderation_api_timeout_seconds 下调。
const defaultModerationAPITimeout = 15 * time.Second

// Run 执行审核流水线。返回 (rejected, err)。
//
//	rejected=true  → 已写响应，调用方应直接 return err（fiber 风格）
//	rejected=false → 通过审核，调用方继续路由到上游
func (g *ModerationGate) Run() (bool, error) {
	// fix MAJOR R23-M3：DB 加载策略失败 → 不能裸奔放行，按 fail-closed 兜底
	if g.Policy.LoadFailed() {
		return g.rejectUnavailable(LoadModerationConfig())
	}
	// Policy.IsActive 已在调用方判过；这里再防御性检查一次。
	if !g.Policy.IsActive() {
		return false, nil
	}

	// 1. 抽 prompt 文本 + 图片（按客户端协议）。审核使用 source-aware segments：
	//    用户消息进入主审；工具结果只做低风险审计；系统/工具 schema 不直接送审。
	srcStr := translateFormatForExtract(g.SrcFormat)
	review, err := ExtractModerationSegments(srcStr, g.Body)
	if err != nil || !review.HasContent {
		// 没内容 → 没法审；让 upstream 自己处理（业务路径不阻塞）
		return false, nil
	}
	primaryReview := review.ResultForKinds(SegmentUserMessage)
	contextReview := review.ResultForKinds(SegmentToolResult, SegmentFunctionOutput, SegmentClientContext, SegmentToolCall)
	g.ReviewText = primaryReview.Text
	if g.ReviewText == "" {
		g.ReviewText = contextReview.Text
	}
	if isCodexAmbientSuggestionsPrompt(primaryReview.Text) || isCodexAmbientSuggestionsPrompt(primaryReview.Text+"\n"+contextReview.Text) {
		return false, nil
	}

	// 2. 加载 Moderation 配置。1M 上下文模型按模型能力放宽 max_chars，
	// 同时保持普通模型的低成本审核预算。
	cfg := LoadModerationConfig().ForRequestModel(g.ModelName)

	// 3. max_chars 上限——超过直接拒绝（fail-closed），不截断
	extractedTextLen := utf8.RuneCountInString(primaryReview.Text)
	if cfg.MaxChars > 0 && extractedTextLen > cfg.MaxChars {
		return g.rejectOversize(cfg, extractedTextLen)
	}

	// 4. fix MAJOR R23-M1（codex 审查）：图片策略**前置**且独立于 NeedsModeration ——
	//    cfg.ImagePolicy="reject" 时不论审核等级（off/keyword/moderation/strict），
	//    带图请求都应被独立 reason 拒绝；之前只在 NeedsModeration 时触发，且伪装成 keyword
	//    命中（审计/响应被错标）。
	if cfg.ImagePolicy == "reject" && len(review.ImageURLs) > 0 {
		return g.rejectImagePolicy(cfg, len(review.ImageURLs))
	}

	// 5. 关键字快扫（毫秒级）。
	//
	// keyword 档：命中后直接拦截，适合少量高置信词条。
	// strict 档：关键字/规则只作为风险信号，最终交给智能审核二判，减少正常安全研究、
	// API key 配置咨询等场景被词库误杀。
	imageURLs := []string(nil)
	if len(review.ImageURLs) > 0 && cfg.ImagePolicy == "submit" {
		imageURLs = review.ImageURLs
	}
	needsSmartReviewFromSignal := false
	smartReviewRan := false
	var smartReviewResult ModerationResult
	runSmartReviewOnce := func() (ModerationResult, bool, error) {
		if smartReviewRan {
			return smartReviewResult, false, nil
		}
		result, rejected, err := g.runSmartModeration(primaryReview.Text, imageURLs, cfg)
		if rejected || err != nil {
			return result, rejected, err
		}
		smartReviewRan = true
		smartReviewResult = result
		return result, false, nil
	}
	applySmartReview := func() (bool, error) {
		result, rejected, err := runSmartReviewOnce()
		if rejected || err != nil {
			return rejected, err
		}
		if result.Flagged {
			if shouldAllowCodingAgentWorkflowPromptInjection(primaryReview.Text, result) {
				g.audit("MODERATION_POLICY_ALLOW_CODING_AGENT_WORKFLOW", "policy_allow", "prompt_injection_coding_agent_workflow", result.HighestScore, g.policyAllowDetails(result, "coding_agent_workflow"))
				return false, nil
			}
			return g.rejectPolicy(result, cfg)
		}
		return false, nil
	}
	if g.Policy.NeedsKeyword() && primaryReview.Text != "" {
		if kw := MatchKeyword(primaryReview.Text); kw != "" {
			if g.Policy.NeedsModeration() {
				needsSmartReviewFromSignal = true
				g.auditKeywordSignal(kw, "keyword_model_review")
			} else {
				return g.rejectKeyword(kw, cfg)
			}
		}
		riskResult := EvaluateModerationRiskRules(primaryReview.Text)
		if riskResult.HasMatches() {
			if riskResult.ShouldBlock() {
				if g.Policy.NeedsModeration() {
					needsSmartReviewFromSignal = true
					g.auditRiskScoreForSource(riskResult, "block_model_review", "user_message")
				} else {
					return g.rejectRiskRule(riskResult, cfg)
				}
			}
			if riskResult.NeedsModelReview() {
				needsSmartReviewFromSignal = true
				g.auditRiskScoreForSource(riskResult, "model_review", "user_message")
			}
			if !riskResult.ShouldBlock() && !riskResult.NeedsModelReview() {
				g.auditRiskScoreForSource(riskResult, "score", "user_message")
			}
		}
	}
	if g.Policy.NeedsKeyword() && contextReview.Text != "" {
		// 外部工具/函数返回可能包含 “ignore previous instructions” 等网页注入文本。
		// 这类内容只做审计打分，不直接阻断真实用户请求，也不送通用智能审核模型。
		riskResult := EvaluateModerationRiskRules(contextReview.Text)
		if riskResult.HasMatches() {
			saved := g.ReviewText
			g.ReviewText = contextReview.Text
			g.auditRiskScoreForSource(riskResult, "context_score", "non_user_context")
			g.ReviewText = saved
		}
	}

	// 6. 图片策略 submit/skip 决定是否把 image_url 也送智能审核服务
	// "skip" 或未知值 → 不送图（imageURLs 保持 nil）

	if needsSmartReviewFromSignal && !g.Policy.NeedsModeration() && primaryReview.HasContent {
		if rejected, err := applySmartReview(); rejected || err != nil {
			return rejected, err
		}
	}

	// 7. 智能审核（仅 moderation/strict 级别）
	if g.Policy.NeedsModeration() && primaryReview.HasContent {
		if rejected, err := applySmartReview(); rejected || err != nil {
			return rejected, err
		}
	}

	return false, nil
}

func (g *ModerationGate) runSmartModeration(text string, imageURLs []string, cfg ModerationConfig) (ModerationResult, bool, error) {
	// API 未配置 → fail-mode 决定
	if !cfg.IsConfigured() {
		if g.Policy.FailClosed() {
			rejected, err := g.rejectUnavailable(cfg)
			return ModerationResult{}, rejected, err
		}
		// fail-open：admin 还没配 key 时业务正常跑
		return ModerationResult{}, false, nil
	}
	// 单调用硬超时；不继承 fiber ctx 超时（fiber 默认无超时）
	ctx, cancel := context.WithTimeout(g.Ctx.UserContext(), moderationTimeoutForConfig(cfg))
	defer cancel()
	result := CheckContent(ctx, text, imageURLs, cfg)
	if result.Err != nil {
		// API 不可达
		if g.Policy.FailClosed() {
			rejected, err := g.rejectUnavailableWithErr(cfg, result.Err)
			return result, rejected, err
		}
		// fix MAJOR R23-M6（codex 审查）：审计只记 tag，不写 raw err.Error() ——
		// 远端响应 body / URL / 分块边界可能落审计表泄漏供应商细节。
		// 原始 err 仅进进程日志（已限长）。
		tag := classifyAPIError(result.Err)
		log.Printf("[MODERATION] fail-open user=%d model=%s err_tag=%s err=%s",
			g.UserID, g.ModelName, tag, sanitizeErrText(result.Err.Error(), 256))
		g.audit("MODERATION_FAIL_OPEN", tag, "", 0, fmt.Sprintf(`{"err_tag":%q}`, tag))
		return result, false, nil
	}
	return result, false, nil
}

// translateFormatForExtract sdktranslator.Format → prompt_extract.go 接受的字符串。
//
// prompt_extract switch 接受："openai/anthropic/gemini/codex" 等多种别名；
// sdktranslator 常量值是 "openai/claude/gemini/gemini-cli/codex"。
// "claude" 在 prompt_extract switch 里没列，所以要映射成 "anthropic"。
func translateFormatForExtract(f sdktranslator.Format) string {
	switch f {
	case sdktranslator.FormatClaude:
		return "anthropic"
	case sdktranslator.FormatGemini, sdktranslator.FormatGeminiCLI:
		return "gemini"
	case sdktranslator.FormatCodex:
		return "codex"
	case sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse:
		return "openai"
	default:
		return "openai" // 兜底
	}
}

func isCodexAmbientSuggestionsPrompt(text string) bool {
	normalized := strings.ToLower(strings.TrimSpace(strings.Join(strings.Fields(text), " ")))
	markers := []string{
		"generate 0 to 3 ambient suggestions for this local project:",
		"use recent codex threads from this project",
		"for local project suggestions, make sure suggestions are truly relevant to this project itself",
		"suggest actionable tasks that they would actually act on",
	}
	hits := 0
	for _, marker := range markers {
		if strings.Contains(normalized, marker) {
			hits++
		}
	}
	if strings.Contains(normalized, "<environment_context>") {
		return hits >= 3
	}
	return hits >= len(markers)
}

func shouldAllowCodingAgentWorkflowPromptInjection(text string, result ModerationResult) bool {
	if !result.Flagged || result.HighestCat != "prompt_injection" {
		return false
	}
	normalized := strings.ToLower(strings.TrimSpace(strings.Join(strings.Fields(text), " ")))
	if normalized == "" {
		return false
	}
	if containsAny(normalized, []string{
		"ignore previous instructions",
		"ignore all previous",
		"disregard previous instructions",
		"reveal system prompt",
		"show system prompt",
		"print system prompt",
		"developer message",
		"steal api key",
		"exfiltrate",
		"bypass safety",
		"jailbreak",
	}) {
		return false
	}
	hasAgentMarker := containsAny(normalized, []string{
		".codex",
		"skill.md",
		"harness",
		"another language model started",
		"<environment_context>",
		"codex",
		"claude code",
	})
	hasWorkspaceMarker := containsAny(normalized, []string{
		"workspace",
		"repo",
		"cwd",
		"powershell",
		"本地",
		"项目",
		"浏览器",
		"browser",
	})
	hasTaskMarker := containsAny(normalized, []string{
		"接手",
		"优化",
		"升级",
		"继续",
		"创建",
		"修复",
		"build on",
		"upgrade",
		"continue",
		"implement",
	})
	return hasAgentMarker && hasWorkspaceMarker && hasTaskMarker
}

func containsAny(s string, needles []string) bool {
	for _, needle := range needles {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

// ─── 拒绝路径：写 ApiLog + 审计队列 + 协议感知响应 ───────────────────────────

func moderationTimeoutForConfig(cfg ModerationConfig) time.Duration {
	base := defaultModerationAPITimeout
	if cfg.APITimeoutSec > 0 {
		base = time.Duration(cfg.APITimeoutSec) * time.Second
	}
	if !cfg.SampleLongPrompts || cfg.MaxChunks <= 8 {
		return base
	}
	timeout := time.Duration(cfg.MaxChunks) * 500 * time.Millisecond
	if timeout < base {
		timeout = base
	}
	maxTimeout := 30 * time.Second
	if base > maxTimeout {
		maxTimeout = base
	}
	if timeout > maxTimeout {
		timeout = maxTimeout
	}
	return timeout
}

func (g *ModerationGate) rejectOversize(cfg ModerationConfig, textLen int) (bool, error) {
	g.writeApiLog(413, "moderation_oversize", fmt.Sprintf("input too long: %d > %d", textLen, cfg.MaxChars))
	g.audit("MODERATION_BLOCK_OVERSIZE", "", "", 0, fmt.Sprintf("len=%d max=%d model=%s", textLen, cfg.MaxChars, g.ModelName))
	msg := PickLocalizedMessage(g.Ctx.Get("Accept-Language"), "moderation_block_message_zh", "moderation_block_message_en")
	if msg == "" {
		msg = "请求内容过长，已被拦截。"
	}
	return true, rejectBySourceFormat(g.Ctx, g.SrcFormat, ModerationReasonOversize, msg, 413)
}

func (g *ModerationGate) rejectKeyword(keyword string, cfg ModerationConfig) (bool, error) {
	g.writeApiLog(403, "moderation_keyword", keyword)
	details, _ := json.Marshal(map[string]any{
		"keyword":       keyword,
		"model":         g.ModelName,
		"src_format":    string(g.SrcFormat),
		"segment_scope": "user_message",
	})
	g.audit("MODERATION_BLOCK_KEYWORD", "keyword_match", keyword, 0, string(details))
	msg := PickLocalizedMessage(g.Ctx.Get("Accept-Language"), "moderation_block_message_zh", "moderation_block_message_en")
	return true, rejectBySourceFormat(g.Ctx, g.SrcFormat, ModerationReasonKeyword, msg, 403)
}

func (g *ModerationGate) auditKeywordSignal(keyword, mode string) {
	details, _ := json.Marshal(map[string]any{
		"mode":            mode,
		"trigger_keyword": keyword,
		"model":           g.ModelName,
		"src_format":      string(g.SrcFormat),
		"segment_scope":   "user_message",
	})
	g.audit(ActionModerationRiskScore, "keyword_signal", keyword, 0, string(details))
}

func (g *ModerationGate) rejectRiskRule(result ModerationRiskResult, cfg ModerationConfig) (bool, error) {
	g.writeApiLog(403, "moderation_risk_rule", result.PrimaryMatchID())
	details := g.riskRuleDetailsForSource(result, "block", "user_message")
	g.audit(ActionModerationBlockRiskRule, "risk_rule_match", result.PrimaryMatchID(), float64(result.HighestScore), details)
	msg := PickLocalizedMessage(g.Ctx.Get("Accept-Language"), "moderation_block_message_zh", "moderation_block_message_en")
	return true, rejectBySourceFormat(g.Ctx, g.SrcFormat, ModerationReasonRiskRule, msg, 403)
}

func (g *ModerationGate) auditRiskScore(result ModerationRiskResult, mode string) {
	g.auditRiskScoreForSource(result, mode, "user_message")
}

func (g *ModerationGate) auditRiskScoreForSource(result ModerationRiskResult, mode, source string) {
	if !result.HasMatches() {
		return
	}
	g.audit(ActionModerationRiskScore, "risk_rule_score", result.PrimaryMatchID(), float64(result.HighestScore), g.riskRuleDetailsForSource(result, mode, source))
}

func (g *ModerationGate) riskRuleDetails(result ModerationRiskResult, mode string) string {
	return g.riskRuleDetailsForSource(result, mode, "user_message")
}

func (g *ModerationGate) riskRuleDetailsForSource(result ModerationRiskResult, mode, source string) string {
	details, _ := json.Marshal(map[string]any{
		"mode":          mode,
		"segment_scope": source,
		"model":         g.ModelName,
		"src_format":    string(g.SrcFormat),
		"total_score":   result.TotalScore,
		"highest_score": result.HighestScore,
		"matches":       result.Matches,
	})
	return string(details)
}

// rejectImagePolicy fix MAJOR R23-M1：image_policy=reject 命中带图请求的独立路径。
// 审计与响应不混入 keyword_match，便于日志统计。
func (g *ModerationGate) rejectImagePolicy(cfg ModerationConfig, imageCount int) (bool, error) {
	g.writeApiLog(403, "moderation_image_policy", fmt.Sprintf("image_count=%d policy=%s", imageCount, cfg.ImagePolicy))
	details, _ := json.Marshal(map[string]any{
		"image_count": imageCount,
		"policy":      cfg.ImagePolicy,
		"model":       g.ModelName,
		"src_format":  string(g.SrcFormat),
	})
	g.audit("MODERATION_BLOCK_IMAGE_POLICY", "image_policy_reject", "", 0, string(details))
	msg := PickLocalizedMessage(g.Ctx.Get("Accept-Language"), "moderation_block_message_zh", "moderation_block_message_en")
	return true, rejectBySourceFormat(g.Ctx, g.SrcFormat, ModerationReasonImagePolicy, msg, 403)
}

func (g *ModerationGate) rejectPolicy(r ModerationResult, cfg ModerationConfig) (bool, error) {
	g.writeApiLog(403, "moderation_policy", r.HighestCat)
	details, _ := json.Marshal(map[string]any{
		"highest_cat":   r.HighestCat,
		"highest_score": r.HighestScore,
		"endpoint":      r.Endpoint,
		"from_cache":    r.FromCache,
		"model":         g.ModelName,
		"segment_scope": "user_message",
	})
	g.audit("MODERATION_BLOCK_POLICY", "policy_violation", "", r.HighestScore, string(details))
	msg := PickLocalizedMessage(g.Ctx.Get("Accept-Language"), "moderation_block_message_zh", "moderation_block_message_en")
	return true, rejectBySourceFormat(g.Ctx, g.SrcFormat, ModerationReasonPolicy, msg, 403)
}

func (g *ModerationGate) policyAllowDetails(r ModerationResult, reason string) string {
	details, _ := json.Marshal(map[string]any{
		"highest_cat":   r.HighestCat,
		"highest_score": r.HighestScore,
		"from_cache":    r.FromCache,
		"model":         g.ModelName,
		"segment_scope": "user_message",
		"allow_reason":  reason,
	})
	return string(details)
}

func (g *ModerationGate) rejectUnavailable(cfg ModerationConfig) (bool, error) {
	g.writeApiLog(503, "moderation_unavailable", "api_not_configured")
	g.audit("MODERATION_UNAVAILABLE_CLOSED", "moderation_unavailable", "", 0, `{"reason":"api_not_configured"}`)
	msg := PickLocalizedMessage(g.Ctx.Get("Accept-Language"), "moderation_unavailable_message_zh", "moderation_unavailable_message_en")
	return true, rejectBySourceFormat(g.Ctx, g.SrcFormat, ModerationReasonUnavailable, msg, 503)
}

func (g *ModerationGate) rejectUnavailableWithErr(cfg ModerationConfig, err error) (bool, error) {
	g.writeApiLog(503, "moderation_unavailable", classifyAPIError(err))
	// fix MAJOR R23-M6：审计只记类型 tag，不写 raw err.Error()。
	// 真实 err 进进程日志（脱敏 + 限长 256 字符）便于运维排查。
	tag := classifyAPIError(err)
	log.Printf("[MODERATION] fail-closed user=%d model=%s err_tag=%s err=%s",
		g.UserID, g.ModelName, tag, sanitizeErrText(err.Error(), 256))
	details, _ := json.Marshal(map[string]any{"err_tag": tag})
	g.audit("MODERATION_UNAVAILABLE_CLOSED", "moderation_unavailable", "", 0, string(details))
	msg := PickLocalizedMessage(g.Ctx.Get("Accept-Language"), "moderation_unavailable_message_zh", "moderation_unavailable_message_en")
	return true, rejectBySourceFormat(g.Ctx, g.SrcFormat, ModerationReasonUnavailable, msg, 503)
}

// classifyAPIError 把 err 分类为粗粒度 tag，避免审计/响应泄漏 endpoint URL / 上游 body。
func classifyAPIError(err error) string {
	if err == nil {
		return ""
	}
	if apiErr, ok := ExtractModerationAPIError(err); ok {
		msg := strings.ToLower(strings.TrimSpace(apiErr.ErrorMessage + " " + apiErr.ErrorCode + " " + apiErr.ErrorType))
		switch {
		case strings.Contains(msg, "api key not valid"), strings.Contains(msg, "please pass a valid api key"), strings.Contains(msg, "invalid_api_key"):
			return "api_auth_failed"
		case strings.Contains(msg, "insufficient_quota"), strings.Contains(msg, "billing"), strings.Contains(msg, "quota"):
			return "api_quota_or_billing"
		case apiErr.StatusCode == 401 || apiErr.StatusCode == 403:
			return "api_auth_failed"
		case apiErr.StatusCode == 429:
			return "api_rate_limited"
		case apiErr.StatusCode >= 500:
			return "api_5xx"
		default:
			return "api_error"
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "api_timeout"
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return "api_timeout"
		}
		return "api_network_error"
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "401"), strings.Contains(msg, "unauthorized"), strings.Contains(msg, "invalid_api_key"), strings.Contains(msg, "api key not valid"), strings.Contains(msg, "please pass a valid api key"):
		return "api_auth_failed"
	case strings.Contains(msg, "429"), strings.Contains(msg, "too many requests"), strings.Contains(msg, "rate limit"):
		return "api_rate_limited"
	case strings.Contains(msg, "insufficient_quota"), strings.Contains(msg, "billing"), strings.Contains(msg, "quota"):
		return "api_quota_or_billing"
	case strings.Contains(msg, "api status 5"):
		return "api_5xx"
	case strings.Contains(msg, "prompt too long"):
		return "input_too_long"
	default:
		return "api_error"
	}
}

// ClassifyModerationAPIError exposes the same coarse, non-sensitive error tags
// used by the moderation runtime. Admin diagnostics should return these tags
// instead of raw upstream response bodies.
func ClassifyModerationAPIError(err error) string {
	return classifyAPIError(err)
}

// sanitizeErrText 把 err 文本截断到 maxLen rune，去除多余空白。
// 进程日志专用，不进审计/客户端响应。
func sanitizeErrText(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= maxLen {
		return s
	}
	r := []rune(s)
	return string(r[:maxLen]) + "..."
}

// writeApiLog 写一条 ApiLog 记录拒绝（与 stream.go 现有错误路径同模式）。
func (g *ModerationGate) writeApiLog(status int, errorType, errorMessage string) {
	if database.DB == nil {
		return
	}
	database.DB.Create(&database.ApiLog{
		UserID:       g.UserID,
		TokenName:    g.TokenHash,
		ModelName:    g.ModelName,
		Status:       status,
		IPAddress:    g.ClientIP,
		Latency:      time.Since(g.StartTime).Milliseconds(),
		Cost:         0,
		RequestPath:  sanitizeError(g.Ctx.Path(), 160),
		ErrorType:    sanitizeError(errorType, 64),
		ErrorMessage: sanitizeError(errorMessage, 512),
		CreatedAt:    time.Now(),
	})
}

// audit 入审计队列（异步，不阻塞）。details 是 JSON 字符串。
func (g *ModerationGate) audit(action, reason, keyword string, score float64, details string) {
	EnqueueModerationAudit(ModerationAuditEvent{
		UserID:       g.UserID,
		ModelName:    g.ModelName,
		ActionType:   action,
		Reason:       reason,
		Keyword:      keyword,
		HighestScore: score,
		IPAddress:    g.ClientIP,
		Details:      enrichModerationAuditDetails(details, g.ReviewText),
		OccurredAt:   time.Now(),
	})
}
