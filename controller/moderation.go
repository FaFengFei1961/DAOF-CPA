package controller

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"daof-ai-hub/database"
	"daof-ai-hub/proxy"

	"github.com/gofiber/fiber/v2"
)

const moderationConfigTestTimeout = 10 * time.Second
const moderationKeywordGenerateTimeout = 25 * time.Second

// TestModerationConfig tests the saved moderation provider configuration.
//
// The endpoint intentionally does not accept API keys or config overrides from
// the request body. Admins must save the config first, then test the exact
// server-side state that production requests will use.
func TestModerationConfig(c *fiber.Ctx) error {
	proxy.SyncCacheConfig()
	cfg := proxy.LoadModerationConfig()
	endpoint := cfg.DiagnosticEndpoint()
	model := strings.TrimSpace(cfg.Model)
	base := fiber.Map{
		"configured": cfg.IsConfigured(),
		"provider":   cfg.Provider,
		"endpoint":   maskEndpointForDisplay(endpoint),
		"model":      model,
	}

	if !cfg.IsConfigured() {
		base["success"] = false
		base["status"] = "not_configured"
		base["message_code"] = "ERR_MODERATION_NOT_CONFIGURED"
		base["message"] = "请先保存 CPA 地址和审核模型后再测试"
		return c.Status(http.StatusBadRequest).JSON(base)
	}
	if strings.TrimSpace(model) == "" {
		base["success"] = false
		base["status"] = "config_invalid"
		base["message_code"] = "ERR_MODERATION_MODEL_EMPTY"
		base["message"] = "审核模型不能为空"
		return c.Status(http.StatusBadRequest).JSON(base)
	}

	ctx, cancel := context.WithTimeout(c.UserContext(), moderationConfigTestTimeout)
	defer cancel()

	// Add a nonce so the connectivity test cannot be satisfied by the local
	// moderation cache. The text remains harmless and short.
	prompt := fmt.Sprintf("DAOF moderation connectivity test %d. This is harmless text.", time.Now().UnixNano())
	start := time.Now()
	result := proxy.CheckContent(ctx, prompt, nil, cfg)
	latencyMs := time.Since(start).Milliseconds()

	base["latency_ms"] = latencyMs
	base["from_cache"] = result.FromCache
	base["flagged"] = result.Flagged
	if result.AuthIndex != "" {
		base["auth_index"] = result.AuthIndex
	}

	if result.Err != nil {
		tag := proxy.ClassifyModerationAPIError(result.Err)
		if apiErr, ok := proxy.ExtractModerationAPIError(result.Err); ok {
			base["upstream_status"] = apiErr.StatusCode
			if apiErr.ErrorType != "" {
				base["upstream_error_type"] = apiErr.ErrorType
			}
			if apiErr.ErrorCode != "" {
				base["upstream_error_code"] = apiErr.ErrorCode
			}
			if apiErr.ErrorMessage != "" {
				base["upstream_message"] = sanitizeModerationDiagnostic(apiErr.ErrorMessage, 240)
			}
			if apiErr.RequestID != "" {
				base["upstream_request_id"] = sanitizeModerationDiagnostic(apiErr.RequestID, 120)
			}
			if apiErr.RetryAfter != "" {
				base["retry_after"] = sanitizeModerationDiagnostic(apiErr.RetryAfter, 60)
			}
			if apiErr.RateLimitHeaders != nil {
				base["rate_limit"] = apiErr.RateLimitHeaders
			}
		}
		status, code, msg, httpStatus := moderationTestErrorResponse(tag)
		base["success"] = false
		base["status"] = status
		base["message_code"] = code
		base["message"] = msg
		base["error_tag"] = tag
		return c.Status(httpStatus).JSON(base)
	}

	if result.Flagged {
		base["success"] = true
		base["status"] = "flagged"
		base["message_code"] = "WARN_MODERATION_TEST_FLAGGED"
		base["message"] = "审核 provider 已连通，但无害测试文本被判定命中，请检查模型、阈值或提示词策略"
		return c.JSON(base)
	}

	base["success"] = true
	base["status"] = "ok"
	base["message_code"] = "SUCCESS_MODERATION_TEST_OK"
	base["message"] = "审核 provider 已连通，测试文本通过审核"
	return c.JSON(base)
}

func moderationTestErrorResponse(tag string) (status, code, message string, httpStatus int) {
	switch tag {
	case "api_auth_failed":
		return "auth_failed", "ERR_MODERATION_AUTH_FAILED", "CPA 模型池鉴权失败，请检查同地址 cliproxy 渠道 API key、moderation_cliproxy_api_key 或模型调用权限", http.StatusBadGateway
	case "api_rate_limited":
		return "rate_limited", "ERR_MODERATION_RATE_LIMITED", "CPA 模型池返回限流，请稍后重试，或切换更充足的审核模型", http.StatusTooManyRequests
	case "api_quota_or_billing":
		return "billing_or_quota", "ERR_MODERATION_BILLING_OR_QUOTA", "CPA 模型池 quota 或计费异常，请检查该模型的可用额度", http.StatusBadGateway
	case "api_timeout":
		return "timeout", "ERR_MODERATION_TIMEOUT", "审核请求超时，请检查 CPA、网络或模型响应时间", http.StatusGatewayTimeout
	case "api_network_error":
		return "network_error", "ERR_MODERATION_NETWORK", "无法连接 CPA 模型池，请检查 CPA 地址、网络或 DNS", http.StatusBadGateway
	case "api_5xx":
		return "api_5xx", "ERR_MODERATION_UPSTREAM_5XX", "CPA 模型池上游暂时异常，请稍后重试", http.StatusBadGateway
	case "input_too_long":
		return "input_too_long", "ERR_MODERATION_TEST_INPUT_TOO_LONG", "测试文本被当前长度限制拒绝，请检查长 Prompt 限制配置", http.StatusBadRequest
	default:
		return "api_error", "ERR_MODERATION_API_ERROR", "CPA 模型池审核调用失败，请检查 CPA 地址、模型名和调用权限", http.StatusBadGateway
	}
}

func sanitizeModerationDiagnostic(s string, maxRunes int) string {
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	if maxRunes <= 0 || len([]rune(s)) <= maxRunes {
		return s
	}
	r := []rune(s)
	return string(r[:maxRunes]) + "..."
}

type moderationKeywordGenerateRequest struct {
	Focus         string `json:"focus"`
	MaxCandidates int    `json:"max_candidates"`
}

// GenerateModerationKeywords asks the saved moderation provider for
// defensive keyword candidates. It never writes SysConfig directly; admins
// must review candidates and save the merged dictionary.
func GenerateModerationKeywords(c *fiber.Ctx) error {
	var req moderationKeywordGenerateRequest
	if len(c.Body()) > 0 {
		if err := c.BodyParser(&req); err != nil {
			return c.Status(http.StatusBadRequest).JSON(fiber.Map{
				"success":      false,
				"message_code": "ERR_PARSE_PAYLOAD",
			})
		}
	}
	if req.MaxCandidates < 0 || req.MaxCandidates > 200 {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_INVALID_PARAMS",
			"message":      "max_candidates 必须是 1-200 之间的整数",
		})
	}

	proxy.SyncCacheConfig()
	ctx, cancel := context.WithTimeout(c.UserContext(), moderationKeywordGenerateTimeout)
	defer cancel()

	start := time.Now()
	result, err := proxy.GenerateModerationKeywordCandidates(ctx, req.Focus, req.MaxCandidates)
	if err != nil {
		tag := proxy.ClassifyModerationAPIError(err)
		if tag == "" {
			tag = "api_error"
		}
		status, code, msg, httpStatus := moderationTestErrorResponse(tag)
		return c.Status(httpStatus).JSON(fiber.Map{
			"success":      false,
			"status":       status,
			"message_code": code,
			"message":      msg,
			"error_tag":    tag,
		})
	}
	return c.JSON(fiber.Map{
		"success":      true,
		"message_code": "SUCCESS_MODERATION_KEYWORDS_GENERATED",
		"data":         result.Candidates,
		"provider":     result.Provider,
		"endpoint":     maskEndpointForDisplay(result.Endpoint),
		"model":        result.Model,
		"auth_index":   result.AuthIndex,
		"latency_ms":   time.Since(start).Milliseconds(),
	})
}

func maskEndpointForDisplay(endpoint string) string {
	if endpoint == "" {
		return ""
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

type moderationEvaluateRequest struct {
	Text         string `json:"text"`
	Model        string `json:"model"`
	IncludeSmart bool   `json:"include_smart"`
}

// EvaluateModerationDryRun runs the same local moderation gates used by the
// production request path, but is admin-only and does not call the CPA review
// model unless include_smart=true. This keeps regression tests cheap while
// still exercising the live keyword/risk/length configuration.
func EvaluateModerationDryRun(c *fiber.Ctx) error {
	var req moderationEvaluateRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_PARSE_PAYLOAD",
		})
	}
	text := strings.TrimSpace(req.Text)
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = "gpt-5.5"
	}
	if text == "" {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_INVALID_PARAMS",
			"message":      "text 不能为空",
		})
	}
	if utf8.RuneCountInString(text) > 1_000_000 {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_INVALID_PARAMS",
			"message":      "dry-run text 不能超过 1000000 字符",
		})
	}

	proxy.SyncCacheConfig()
	policy := proxy.LookupModerationPolicy(model)
	cfg := proxy.LoadModerationConfig().ForRequestModel(model)
	textLen := utf8.RuneCountInString(text)
	base := fiber.Map{
		"success":          true,
		"model":            model,
		"policy_level":     policy.Level,
		"policy_fail_mode": policy.FailMode,
		"include_smart":    req.IncludeSmart,
		"text_runes":       textLen,
		"max_chars":        cfg.MaxChars,
	}

	if policy.LoadFailed() {
		base["decision"] = "block"
		base["action"] = "MODERATION_UNAVAILABLE_CLOSED"
		base["reason"] = "policy_load_failed"
		return c.JSON(base)
	}
	if !policy.IsActive() {
		base["decision"] = "allow"
		base["reason"] = "moderation_off"
		return c.JSON(base)
	}
	if cfg.MaxChars > 0 && textLen > cfg.MaxChars {
		base["decision"] = "block"
		base["action"] = proxy.ActionModerationBlockOversize
		base["reason"] = "oversize"
		return c.JSON(base)
	}

	if policy.NeedsKeyword() {
		if kw := proxy.MatchKeyword(text); kw != "" {
			base["decision"] = "block"
			base["action"] = proxy.ActionModerationBlockKeyword
			base["reason"] = "keyword_match"
			base["keyword"] = kw
			return c.JSON(base)
		}
		riskResult := proxy.EvaluateModerationRiskRules(text)
		if riskResult.HasMatches() {
			base["risk"] = riskResult
			if riskResult.ShouldBlock() {
				base["decision"] = "block"
				base["action"] = proxy.ActionModerationBlockRiskRule
				base["reason"] = "risk_rule_match"
				return c.JSON(base)
			}
			if riskResult.NeedsModelReview() {
				if !req.IncludeSmart {
					base["decision"] = "model_review"
					base["action"] = proxy.ActionModerationRiskScore
					base["reason"] = "risk_rule_model_review"
					return c.JSON(base)
				}
				return evaluateModerationSmart(c, text, cfg, base)
			}
			base["decision"] = "score_only"
			base["action"] = proxy.ActionModerationRiskScore
			base["reason"] = "risk_rule_score"
			return c.JSON(base)
		}
	}

	if policy.NeedsModeration() {
		if !req.IncludeSmart {
			base["decision"] = "model_review"
			base["reason"] = "policy_requires_smart_review"
			return c.JSON(base)
		}
		return evaluateModerationSmart(c, text, cfg, base)
	}

	base["decision"] = "allow"
	base["reason"] = "local_rules_clean"
	return c.JSON(base)
}

func evaluateModerationSmart(c *fiber.Ctx, text string, cfg proxy.ModerationConfig, base fiber.Map) error {
	if !cfg.IsConfigured() {
		base["decision"] = "unavailable"
		base["reason"] = "smart_not_configured"
		return c.JSON(base)
	}
	ctx, cancel := context.WithTimeout(c.UserContext(), time.Duration(cfg.APITimeoutSec)*time.Second)
	defer cancel()
	start := time.Now()
	result := proxy.CheckContent(ctx, text, nil, cfg)
	base["smart_latency_ms"] = time.Since(start).Milliseconds()
	base["smart_from_cache"] = result.FromCache
	base["smart_highest_cat"] = result.HighestCat
	base["smart_highest_score"] = result.HighestScore
	if result.Err != nil {
		base["decision"] = "unavailable"
		base["reason"] = proxy.ClassifyModerationAPIError(result.Err)
		return c.JSON(base)
	}
	if result.Flagged {
		base["decision"] = "block"
		base["action"] = proxy.ActionModerationBlockPolicy
		base["reason"] = "policy_violation"
		return c.JSON(base)
	}
	base["decision"] = "allow"
	base["reason"] = "smart_review_clean"
	return c.JSON(base)
}

var moderationRiskActions = []string{
	proxy.ActionModerationBlockKeyword,
	proxy.ActionModerationBlockRiskRule,
	proxy.ActionModerationRiskScore,
	proxy.ActionModerationBlockPolicy,
	proxy.ActionModerationBlockImagePolicy,
	proxy.ActionModerationBlockOversize,
	"MODERATION_UNAVAILABLE_CLOSED",
	"MODERATION_FAIL_OPEN",
	proxy.ActionSecurityAutoban,
}

type moderationRiskEventRow struct {
	ID           uint      `json:"id"`
	TargetUserID uint      `json:"target_user_id"`
	Username     string    `json:"username"`
	OperatorRole string    `json:"operator_role"`
	ActionType   string    `json:"action_type"`
	IPAddress    string    `json:"ip_address"`
	Details      string    `json:"details"`
	CreatedAt    time.Time `json:"created_at"`
}

func ListModerationRiskEvents(c *fiber.Ctx) error {
	limit, _ := strconv.Atoi(c.Query("limit", "80"))
	if limit < 1 {
		limit = 80
	}
	if limit > 200 {
		limit = 200
	}
	action := strings.TrimSpace(c.Query("action"))
	userID, _ := strconv.ParseUint(strings.TrimSpace(c.Query("user_id")), 10, 32)

	db := database.DB.Table("operation_logs").
		Select("operation_logs.id, operation_logs.target_user_id, users.username, operation_logs.operator_role, operation_logs.action_type, operation_logs.ip_address, operation_logs.details, operation_logs.created_at").
		Joins("LEFT JOIN users ON users.id = operation_logs.target_user_id").
		Where("operation_logs.action_type IN ?", moderationRiskActions)
	if action != "" {
		db = db.Where("operation_logs.action_type = ?", action)
	}
	if userID > 0 {
		db = db.Where("operation_logs.target_user_id = ?", uint(userID))
	}

	var rows []moderationRiskEventRow
	if err := db.Order("operation_logs.id DESC").Limit(limit).Scan(&rows).Error; err != nil {
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_READ_AUDIT_LOGS",
		})
	}
	return c.JSON(fiber.Map{
		"success": true,
		"data":    rows,
		"meta": fiber.Map{
			"limit": limit,
		},
	})
}
