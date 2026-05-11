// Package proxy / moderation_runner.go
//
// 审核流水线编排：把 keyword_filter / content_moderation / prompt_extract / response 串起来。
//
// 关键决策（codex 第二十三轮反馈，吸收 v2 Critical 反馈）：
//
//  1. **执行时机**：必须在 Decide(IsPrecheck=true) 之后、上游路由发起之前。
//     - 在 Decide 之前会"成本放大"——攻击者用没余额的账号刷请求，每条都打 OpenAI
//       Moderation API → 我方账单暴涨。Decide 先把没余额的卡掉。
//     - 在路由之后才有真实 srcFormat，但路由仅做模型查找，不发上游请求，所以
//       moderation 在 routes 解析后但路由循环之前是黄金时机。
//
//  2. **fail-mode 双轨**：
//     - 直连官方上游（openai 直连）→ ChannelModel.ModerationFailMode="closed"，
//       Moderation API 不可达时拒绝，不能让 jailbreak 透传到官方导致封号
//     - cloaked 路径（CLIProxyAPI 兜底）→ "open"，CLIProxyAPI 自带 cloaking + Anthropic
//       自我拒答，审核服务挂掉时不阻塞业务
//
//  3. **超长 prompt**：超过 max_chars 直接拒绝（不"截断"）——
//     截断会留下"前 N 字符过审，后面违规内容透传"的绕过缝，必须 fail-closed。
//
//  4. **图片策略**：cfg.ImagePolicy 控制
//     - "skip"   → 跳过图片（默认 cloaked）
//     - "submit" → 把 image_url 也送 omni-moderation-latest（多模态官方支持）
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

	"daof-ai-hub/database"

	"github.com/gofiber/fiber/v2"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

// ModerationGate 单次请求的审核执行单元。
type ModerationGate struct {
	Ctx       *fiber.Ctx
	UserID    uint
	TokenHash string // 已 hash 的 token，写 ApiLog 用
	Body      []byte
	ModelName string
	SrcFormat sdktranslator.Format
	Policy    ModerationPolicy
	ClientIP  string
	StartTime time.Time
}

// moderationAPITimeout 单次 OpenAI Moderation API 调用的硬上限（含 chunk 分片）。
//
// 取值依据：OpenAI Moderation 单次 P99 ~250ms；分块最多 8 块 → 8 × 250 = 2s
// 留 1s buffer 应对网络抖动。超时 → fail-mode 决定 open/closed。
const moderationAPITimeout = 3 * time.Second

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

	// 1. 抽 prompt 文本 + 图片（按客户端协议）
	srcStr := translateFormatForExtract(g.SrcFormat)
	extracted, err := ExtractPromptText(srcStr, g.Body)
	if err != nil || !extracted.HasContent {
		// 没内容 → 没法审；让 upstream 自己处理（业务路径不阻塞）
		return false, nil
	}

	// 2. 加载 Moderation 配置
	cfg := LoadModerationConfig()

	// 3. max_chars 上限——超过直接拒绝（fail-closed），不截断
	if cfg.MaxChars > 0 && utf8.RuneCountInString(extracted.Text) > cfg.MaxChars {
		return g.rejectOversize(cfg)
	}

	// 4. fix MAJOR R23-M1（codex 审查）：图片策略**前置**且独立于 NeedsModeration ——
	//    cfg.ImagePolicy="reject" 时不论审核等级（off/keyword/moderation/strict），
	//    带图请求都应被独立 reason 拒绝；之前只在 NeedsModeration 时触发，且伪装成 keyword
	//    命中（审计/响应被错标）。
	if cfg.ImagePolicy == "reject" && len(extracted.ImageURLs) > 0 {
		return g.rejectImagePolicy(cfg, len(extracted.ImageURLs))
	}

	// 5. 关键字快扫（毫秒级；strict 和 keyword 级别都过）
	if g.Policy.NeedsKeyword() {
		if kw := MatchKeyword(extracted.Text); kw != "" {
			return g.rejectKeyword(kw, cfg)
		}
	}

	// 6. 图片策略 submit/skip 决定是否把 image_url 也送 OpenAI Moderation
	imageURLs := []string(nil)
	if g.Policy.NeedsModeration() && len(extracted.ImageURLs) > 0 && cfg.ImagePolicy == "submit" {
		imageURLs = extracted.ImageURLs
	}
	// "skip" 或未知值 → 不送图（imageURLs 保持 nil）

	// 7. OpenAI Moderation 智能审核（仅 moderation/strict 级别）
	if g.Policy.NeedsModeration() {
		// API 未配置 → fail-mode 决定
		if !cfg.IsConfigured() {
			if g.Policy.FailClosed() {
				return g.rejectUnavailable(cfg)
			}
			// fail-open：admin 还没配 key 时业务正常跑
			return false, nil
		}
		// 单调用硬超时；不继承 fiber ctx 超时（fiber 默认无超时）
		ctx, cancel := context.WithTimeout(g.Ctx.UserContext(), moderationAPITimeout)
		defer cancel()
		result := CheckContent(ctx, extracted.Text, imageURLs, cfg)
		if result.Err != nil {
			// API 不可达
			if g.Policy.FailClosed() {
				return g.rejectUnavailableWithErr(cfg, result.Err)
			}
			// fix MAJOR R23-M6（codex 审查）：审计只记 tag，不写 raw err.Error() ——
			// 远端响应 body / URL / 分块边界可能落审计表泄漏供应商细节。
			// 原始 err 仅进进程日志（已限长）。
			tag := classifyAPIError(result.Err)
			log.Printf("[MODERATION] fail-open user=%d model=%s err_tag=%s err=%s",
				g.UserID, g.ModelName, tag, sanitizeErrText(result.Err.Error(), 256))
			g.audit("MODERATION_FAIL_OPEN", tag, "", 0, fmt.Sprintf(`{"err_tag":%q}`, tag))
			return false, nil
		}
		if result.Flagged {
			return g.rejectPolicy(result, cfg)
		}
	}

	return false, nil
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

// ─── 拒绝路径：写 ApiLog + 审计队列 + 协议感知响应 ───────────────────────────

func (g *ModerationGate) rejectOversize(cfg ModerationConfig) (bool, error) {
	g.writeApiLog(413)
	g.audit("MODERATION_BLOCK_OVERSIZE", "", "", 0, fmt.Sprintf("len=%d max=%d", utf8.RuneCountInString(string(g.Body)), cfg.MaxChars))
	msg := PickLocalizedMessage(g.Ctx.Get("Accept-Language"), "moderation_block_message_zh", "moderation_block_message_en")
	if msg == "" {
		msg = "请求内容过长，已被拦截。"
	}
	return true, rejectBySourceFormat(g.Ctx, g.SrcFormat, ModerationReasonOversize, msg, 413)
}

func (g *ModerationGate) rejectKeyword(keyword string, cfg ModerationConfig) (bool, error) {
	g.writeApiLog(403)
	details, _ := json.Marshal(map[string]any{
		"keyword":    keyword,
		"model":      g.ModelName,
		"src_format": string(g.SrcFormat),
	})
	g.audit("MODERATION_BLOCK_KEYWORD", "keyword_match", keyword, 0, string(details))
	msg := PickLocalizedMessage(g.Ctx.Get("Accept-Language"), "moderation_block_message_zh", "moderation_block_message_en")
	return true, rejectBySourceFormat(g.Ctx, g.SrcFormat, ModerationReasonKeyword, msg, 403)
}

// rejectImagePolicy fix MAJOR R23-M1：image_policy=reject 命中带图请求的独立路径。
// 审计与响应不混入 keyword_match，便于日志统计。
func (g *ModerationGate) rejectImagePolicy(cfg ModerationConfig, imageCount int) (bool, error) {
	g.writeApiLog(403)
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
	g.writeApiLog(403)
	details, _ := json.Marshal(map[string]any{
		"highest_cat":   r.HighestCat,
		"highest_score": r.HighestScore,
		"endpoint":      r.Endpoint,
		"from_cache":    r.FromCache,
		"model":         g.ModelName,
	})
	g.audit("MODERATION_BLOCK_POLICY", "policy_violation", "", r.HighestScore, string(details))
	msg := PickLocalizedMessage(g.Ctx.Get("Accept-Language"), "moderation_block_message_zh", "moderation_block_message_en")
	return true, rejectBySourceFormat(g.Ctx, g.SrcFormat, ModerationReasonPolicy, msg, 403)
}

func (g *ModerationGate) rejectUnavailable(cfg ModerationConfig) (bool, error) {
	g.writeApiLog(503)
	g.audit("MODERATION_UNAVAILABLE_CLOSED", "moderation_unavailable", "", 0, `{"reason":"api_not_configured"}`)
	msg := PickLocalizedMessage(g.Ctx.Get("Accept-Language"), "moderation_unavailable_message_zh", "moderation_unavailable_message_en")
	return true, rejectBySourceFormat(g.Ctx, g.SrcFormat, ModerationReasonUnavailable, msg, 503)
}

func (g *ModerationGate) rejectUnavailableWithErr(cfg ModerationConfig, err error) (bool, error) {
	g.writeApiLog(503)
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
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return "api_timeout"
		}
		return "api_network_error"
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "401"), strings.Contains(msg, "Unauthorized"):
		return "api_auth_failed"
	case strings.Contains(msg, "429"):
		return "api_rate_limited"
	case strings.Contains(msg, "5"):
		return "api_5xx"
	case strings.Contains(msg, "prompt too long"):
		return "input_too_long"
	default:
		return "api_error"
	}
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
func (g *ModerationGate) writeApiLog(status int) {
	if database.DB == nil {
		return
	}
	database.DB.Create(&database.ApiLog{
		UserID:    g.UserID,
		TokenName: g.TokenHash,
		ModelName: g.ModelName,
		Status:    status,
		IPAddress: g.ClientIP,
		Latency:   time.Since(g.StartTime).Milliseconds(),
		Cost: 0,
		CreatedAt: time.Now(),
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
		Details:      strings.TrimSpace(details),
		OccurredAt:   time.Now(),
	})
}
