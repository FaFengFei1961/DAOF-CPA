// Package proxy / credits_pool_other.go
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
	"time"

)

// ─── Provider Fetcher: Codex / OpenAI ────────────────────────────────────

const codexUsageURL = "https://chatgpt.com/backend-api/wham/usage"

// Codex usage 响应结构（解析逻辑参照 CPA management center 前端代码）：
//
//	{
//	  "rate_limit": {                         // 主限额（普通对话）
//	    "primary_window":   { "limit_window_seconds": 18000,  "used_percent": ?, "resets_at": ... },
//	    "secondary_window": { "limit_window_seconds": 604800, "used_percent": ?, "resets_at": ... },
//	    "limit_reached": bool,
//	    "allowed": bool
//	  },
//	  "code_review_rate_limit": { ... 同上结构 }
//	}
//
// 关键发现：
//   - free 用户响应里 used_percent 字段缺失 → 但 limit_reached=true 时 UI 显示"100% 已耗尽"
//   - primary/secondary 的语义由 limit_window_seconds 决定（18000=5h, 604800=7d），不是字段名
func fetchCodexQuota(ctx context.Context, af authFileLite, entry *CreditEntry) error {
	if af.IDToken != nil {
		if pt, ok := af.IDToken["plan_type"].(string); ok {
			entry.PlanType = pt
		}
	}

	headers := map[string]string{
		"Authorization": "Bearer $TOKEN$",
		"Content-Type":  "application/json",
		"User-Agent":    "codex_cli_rs/0.76.0 (Debian 13.0.0; x86_64) WindowsTerminal",
	}
	r, err := cpaAPICall(ctx, af.AuthIndex, "GET", codexUsageURL, headers, "")
	if err != nil {
		return err
	}
	if r.StatusCode != 200 {
		return fmt.Errorf("Codex usage HTTP %d: %s", r.StatusCode, sanitizeError(string(r.Body), errorBodyMaxBytes))
	}
	var p struct {
		RateLimit           map[string]any `json:"rate_limit"`
		CodeReviewRateLimit map[string]any `json:"code_review_rate_limit"`
	}
	if err := json.Unmarshal(r.Body, &p); err != nil {
		return err
	}
	entry.Windows = nil
	// 主限额（编程对话）
	if win := codexPickWindow(p.RateLimit, 18000); win != nil {
		entry.Windows = append(entry.Windows, codexBuildWindow("five-hour", "5 小时限额", win, p.RateLimit))
	}
	if win := codexPickWindow(p.RateLimit, 604800); win != nil {
		entry.Windows = append(entry.Windows, codexBuildWindow("weekly", "周限额", win, p.RateLimit))
	}
	// Code Review 副限额（如果存在）
	if win := codexPickWindow(p.CodeReviewRateLimit, 18000); win != nil {
		entry.Windows = append(entry.Windows, codexBuildWindow("code-review-five-hour", "5 小时限额（Code Review）", win, p.CodeReviewRateLimit))
	}
	if win := codexPickWindow(p.CodeReviewRateLimit, 604800); win != nil {
		entry.Windows = append(entry.Windows, codexBuildWindow("code-review-weekly", "周限额（Code Review）", win, p.CodeReviewRateLimit))
	}
	entry.Models = []string{"gpt-5", "gpt-5-mini", "codex-mini-latest"}
	return nil
}

// codexPickWindow 从 rate_limit 容器里**严格按 limit_window_seconds** 挑窗口。
// 没匹配到 expectedSec 直接返回 nil——参照 CPA management UI 的实现（`if (t===18e3 && !o) o=w`），
// 不再做"primary→5h / secondary→weekly"的位置兜底，否则 Free 用户（只有 weekly 在 primary_window）
// 会被误识别为"5 小时限额"。
func codexPickWindow(rl map[string]any, expectedSec float64) map[string]any {
	if rl == nil {
		return nil
	}
	primary, _ := rl["primary_window"].(map[string]any)
	if primary == nil {
		primary, _ = rl["primaryWindow"].(map[string]any)
	}
	secondary, _ := rl["secondary_window"].(map[string]any)
	if secondary == nil {
		secondary, _ = rl["secondaryWindow"].(map[string]any)
	}
	for _, w := range []map[string]any{primary, secondary} {
		if w == nil {
			continue
		}
		if sec, ok := codexWindowSeconds(w); ok && sec == expectedSec {
			return w
		}
	}
	return nil
}

func codexWindowSeconds(w map[string]any) (float64, bool) {
	for _, k := range []string{"limit_window_seconds", "limitWindowSeconds"} {
		switch v := w[k].(type) {
		case float64:
			return v, true
		case int:
			return float64(v), true
		case int64:
			return float64(v), true
		}
	}
	return 0, false
}

// codexBuildWindow 组装一个 CreditWindow。rl 是其所属的 rate_limit 容器，
// 用来读取 limit_reached / allowed 这种"limit-level"字段（兜底显示 100%）。
//
// CPA UI 完整条件（参照源码）：
//
//	used_percent ?? ((limit_reached || allowed===false) && resetLabel !== '-' ? 100 : null)
//
// 注意 resetLabel !== '-' 的守卫——必须有重置时间才显示 100%，
// 否则永久封禁 / 配置错误的凭证会被误识别为"已耗尽"。
func codexBuildWindow(id, label string, w map[string]any, rl map[string]any) CreditWindow {
	out := CreditWindow{ID: id, Label: label}
	// 先解析重置时间——后续 100% 兜底逻辑要用它做守卫
	for _, k := range []string{"resets_at", "resetAt", "reset_at"} {
		if v, ok := w[k].(string); ok && v != "" {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				out.ResetsAt = t
				break
			}
		}
	}

	usedSet := false
	if v, ok := w["used_percent"].(float64); ok {
		out.UsedPercent = clampPct(v)
		usedSet = true
	} else if v, ok := w["usedPercent"].(float64); ok {
		out.UsedPercent = clampPct(v)
		usedSet = true
	} else if v, ok := w["utilization"]; ok {
		out.UsedPercent = parseUsedPercent(v)
		usedSet = true
	}
	// fix Go-HIGH2：参照 CPA UI 完整条件——必须既"已耗尽信号"且"有重置时间"才能显示 100%。
	// 仅 limit_reached/allowed=false 但无重置时间 → 是永久封禁/配置错误，UsedPercent 留 0
	// 让上层判定为"无数据"而不是"已用完"，避免误导。
	if !usedSet && rl != nil && !out.ResetsAt.IsZero() {
		exhausted := false
		if v, ok := rl["limit_reached"].(bool); ok && v {
			exhausted = true
		} else if v, ok := rl["limitReached"].(bool); ok && v {
			exhausted = true
		}
		if v, ok := rl["allowed"].(bool); ok && !v {
			exhausted = true
		}
		if exhausted {
			out.UsedPercent = 100
		}
	}
	out.RemainingPercent = clampPct(100 - out.UsedPercent)
	return out
}

// ─── Provider Fetcher: Kimi ──────────────────────────────────────────────

const kimiUsageURL = "https://api.kimi.com/coding/v1/usages"

func fetchKimiQuota(ctx context.Context, af authFileLite, entry *CreditEntry) error {
	headers := map[string]string{
		"Authorization": "Bearer $TOKEN$",
	}
	r, err := cpaAPICall(ctx, af.AuthIndex, "GET", kimiUsageURL, headers, "")
	if err != nil {
		return err
	}
	if r.StatusCode != 200 {
		return fmt.Errorf("Kimi usage HTTP %d: %s", r.StatusCode, sanitizeError(string(r.Body), errorBodyMaxBytes))
	}
	var p struct {
		WeeklyLimit float64 `json:"weekly_limit"`
		WeeklyUsed  float64 `json:"weekly_used"`
		ResetsAt    string  `json:"resets_at"`
	}
	if err := json.Unmarshal(r.Body, &p); err != nil {
		return err
	}
	w := CreditWindow{ID: "weekly", Label: "周限额"}
	if p.WeeklyLimit > 0 {
		w.UsedPercent = clampPct(p.WeeklyUsed / p.WeeklyLimit * 100)
		w.RemainingPercent = clampPct(100 - w.UsedPercent)
		w.HasNumeric = true
		w.CreditAmount = p.WeeklyLimit - p.WeeklyUsed
	}
	if t, err := time.Parse(time.RFC3339, p.ResetsAt); err == nil {
		w.ResetsAt = t
	}
	entry.Windows = []CreditWindow{w}
	entry.Models = []string{"kimi-k2"}
	return nil
}
