// Package proxy / credits_pool_anthropic.go
//
// H-R2 重构（2026-05-19）：原 credits_pool.go 2127 行单文件按职责拆为 5 个文件：
//   - credits_pool.go         核心：types / globals / lifecycle / refresh-all / 共享 helpers
//   - credits_pool_cpa.go     CPA management API 客户端 + auth files sync + refresh-one dispatch
//   - credits_pool_anthropic.go  Claude / Anthropic quota window fetcher
//   - credits_pool_google.go     Antigravity + Gemini CLI quota fetcher + Google 共享 helpers
//   - credits_pool_other.go      Codex + Kimi quota fetcher
//
// 业务逻辑零改动；仅按文件物理拆分。

package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

)

// ─── Provider Fetcher: Claude / Anthropic ────────────────────────────────

const (
	claudeProfileURL = "https://api.anthropic.com/api/oauth/profile"
	claudeUsageURL   = "https://api.anthropic.com/api/oauth/usage"
)

// claudeWindowOrder 严格定义 Claude 窗口的展示顺序：
// 5 小时窗口最贴近用户当下的使用体验，必须排第一；之后是各种 7 天周期窗口。
// （之前用 map 遍历导致顺序随机，5h 可能掉到末尾——参照 CPA UI 的固定顺序。）
type claudeWindowDef struct {
	Key   string
	ID    string
	Label string
}

var claudeWindowOrder = []claudeWindowDef{
	{"five_hour", "five-hour", "5 小时限额"},
	{"seven_day", "seven-day", "7 天限额"},
	{"seven_day_oauth_apps", "seven-day-oauth-apps", "7 天 OAuth 应用"},
	{"seven_day_opus", "seven-day-opus", "7 天 Opus"},
	{"seven_day_sonnet", "seven-day-sonnet", "7 天 Sonnet"},
	{"seven_day_cowork", "seven-day-cowork", "7 天 Cowork"},
	{"iguana_necktie", "iguana-necktie", "Iguana Necktie"},
}

func fetchClaudeQuota(ctx context.Context, af authFileLite, entry *CreditEntry) error {
	headers := map[string]string{
		"Authorization":  "Bearer $TOKEN$",
		"Content-Type":   "application/json",
		"anthropic-beta": "oauth-2025-04-20",
	}
	// 1. 拉 profile（plan_type）
	// profile 失败不影响主流程（usage 才是核心数据），但要打日志便于排查 token 权限降级
	if pr, err := cpaAPICall(ctx, af.AuthIndex, "GET", claudeProfileURL, headers, ""); err != nil {
		log.Printf("[CREDITS] Claude profile auth=%s 失败: %s", af.AuthIndex, sanitizeError(err.Error(), 200))
	} else if pr.StatusCode != 200 {
		log.Printf("[CREDITS] Claude profile auth=%s HTTP %d", af.AuthIndex, pr.StatusCode)
	} else {
		// 同步 Cli-Proxy-API-Management-Center quotaConfigs.ts:resolveClaudePlanType
		// 优先级：has_claude_max → Max；has_claude_pro → Pro；
		//        organization_type=claude_team && subscription_status=active → Team；
		//        else → Free。
		// 旧实现读 account.plan_type / organization.plan_type — 这两个字段在
		// Anthropic /api/oauth/profile 响应里实际不存在，永远拿到空字符串 →
		// 前端 CreditsMonitor plan_type 角标完全不显示。
		var profile struct {
			Account struct {
				HasClaudeMax bool   `json:"has_claude_max"`
				HasClaudePro bool   `json:"has_claude_pro"`
				EmailAddress string `json:"email_address"`
			} `json:"account"`
			Organization struct {
				OrganizationType   string `json:"organization_type"`
				SubscriptionStatus string `json:"subscription_status"`
			} `json:"organization"`
		}
		if json.Unmarshal(pr.Body, &profile) == nil {
			switch {
			case profile.Account.HasClaudeMax:
				entry.PlanType = "Max"
			case profile.Account.HasClaudePro:
				entry.PlanType = "Pro"
			case strings.EqualFold(profile.Organization.OrganizationType, "claude_team") &&
				strings.EqualFold(profile.Organization.SubscriptionStatus, "active"):
				entry.PlanType = "Team"
			default:
				entry.PlanType = "Free"
			}
			if entry.Email == "" && profile.Account.EmailAddress != "" {
				entry.Email = profile.Account.EmailAddress
			}
		}
	}

	// 2. 拉 usage
	r, err := cpaAPICall(ctx, af.AuthIndex, "GET", claudeUsageURL, headers, "")
	if err != nil {
		return err
	}
	if r.StatusCode != 200 {
		return fmt.Errorf("Claude usage HTTP %d: %s", r.StatusCode, sanitizeError(string(r.Body), errorBodyMaxBytes))
	}

	var usage map[string]any
	if err := json.Unmarshal(r.Body, &usage); err != nil {
		// fix MEDIUM M19-3（codex 第十九轮）：%v 丢失原始 error 类型 → 上层 errors.Is/As 判断失效。
		// 改 %w 让调用链可以根据 *json.SyntaxError 等具体类型做差异化处理。
		return fmt.Errorf("解析 Claude usage 失败: %w", err)
	}

	entry.Windows = nil
	// fix：按 claudeWindowOrder 固定顺序遍历，保证 5h 窗口永远排第一
	for _, def := range claudeWindowOrder {
		raw, ok := usage[def.Key].(map[string]any)
		if !ok {
			continue
		}
		entry.Windows = append(entry.Windows, claudeBuildWindow(def, raw))
	}
	entry.Models = []string{"claude-opus-4-5", "claude-sonnet-4-5", "claude-haiku-4-5"}
	return nil
}

func claudeBuildWindow(def claudeWindowDef, raw map[string]any) CreditWindow {
	w := CreditWindow{ID: def.ID, Label: def.Label}
	// Anthropic OAuth usage 的 utilization 表示已用百分比。管理页展示的是剩余，
	// 因此这里保留 used/remaining 两个字段，前端统一展示 remaining_percent。
	w.UsedPercent = parseUsedPercent(raw["utilization"])
	w.RemainingPercent = clampPct(100 - w.UsedPercent)
	if v, ok := raw["resets_at"].(string); ok {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			w.ResetsAt = t
		}
	}
	return w
}
