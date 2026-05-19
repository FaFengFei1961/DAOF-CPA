// Package proxy / credits_pool_google.go
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
	"strconv"
	"strings"
	"time"

)

// normalizeGoogleTierBadge 把 Google Cloud Code Assist 返回的 raw tier id
// 归一为 PRO / ULTRA / FREE / UNKNOWN，对齐 cockpit-tools 显示风格。
// 参考 src/types/gemini.ts:resolveGeminiPlanBucket
func normalizeGoogleTierBadge(raw string) string {
	lower := strings.ToLower(strings.TrimSpace(raw))
	if lower == "" {
		return "UNKNOWN"
	}
	if strings.Contains(lower, "ultra") {
		return "ULTRA"
	}
	if lower == "standard-tier" {
		return "FREE"
	}
	if strings.Contains(lower, "pro") || strings.Contains(lower, "premium") {
		return "PRO"
	}
	if lower == "free-tier" || strings.Contains(lower, "free") {
		return "FREE"
	}
	return "UNKNOWN"
}

// pickGoogleCodeAssistTier 从 loadCodeAssist 响应中按优先级抽取套餐：
// 1) paidTier.id（已付费）
// 2) currentTier.id（当前激活）
// 3) allowedTiers 中 isDefault=true 的 id（账号被授权使用的默认 tier）
// 返回经过 normalizeGoogleTierBadge 标准化后的字符串。
func pickGoogleCodeAssistTier(body []byte) string {
	var resp struct {
		PaidTier *struct {
			ID string `json:"id"`
		} `json:"paidTier"`
		CurrentTier *struct {
			ID string `json:"id"`
		} `json:"currentTier"`
		AllowedTiers []struct {
			ID        string `json:"id"`
			IsDefault bool   `json:"isDefault"`
		} `json:"allowedTiers"`
	}
	if json.Unmarshal(body, &resp) != nil {
		return ""
	}
	var raw string
	switch {
	case resp.PaidTier != nil && resp.PaidTier.ID != "":
		raw = resp.PaidTier.ID
	case resp.CurrentTier != nil && resp.CurrentTier.ID != "":
		raw = resp.CurrentTier.ID
	default:
		for _, t := range resp.AllowedTiers {
			if t.IsDefault && t.ID != "" {
				raw = t.ID
				break
			}
		}
	}
	if raw == "" {
		return ""
	}
	return normalizeGoogleTierBadge(raw)
}

// ─── Provider Fetcher: Antigravity ───────────────────────────────────────

var antigravityURLs = []string{
	"https://daily-cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels",
	"https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal:fetchAvailableModels",
	"https://cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels",
}

type antigravityGroup struct {
	ID          string
	Label       string
	Identifiers []string
}

var antigravityGroups = []antigravityGroup{
	{"claude-gpt", "Claude/GPT", []string{"claude-sonnet-4-6", "claude-opus-4-6-thinking", "gpt-oss-120b-medium"}},
	{"gemini-3-pro", "Gemini 3 Pro", []string{"gemini-3-pro-high", "gemini-3-pro-low"}},
	{"gemini-3-1-pro-series", "Gemini 3.1 Pro Series", []string{"gemini-3.1-pro-high", "gemini-3.1-pro-low"}},
	{"gemini-2-5-flash", "Gemini 2.5 Flash", []string{"gemini-2.5-flash", "gemini-2.5-flash-thinking"}},
	{"gemini-2-5-flash-lite", "Gemini 2.5 Flash Lite", []string{"gemini-2.5-flash-lite"}},
	{"gemini-2-5-cu", "Gemini 2.5 CU", []string{"rev19-uic3-1p"}},
	{"gemini-3-flash", "Gemini 3 Flash", []string{"gemini-3-flash"}},
	{"gemini-image", "Gemini 3.1 Flash Image", []string{"gemini-3.1-flash-image"}},
}

func fetchAntigravityQuota(ctx context.Context, af authFileLite, entry *CreditEntry) error {
	// project_id 从 cpa_credentials 表注入到 af.ProjectID（每个凭证独立、自动同步）
	// 替代旧的 SysConfig 全局值——支持多账号、admin 零配置
	projectID := strings.TrimSpace(af.ProjectID)
	if projectID == "" {
		return fmt.Errorf("Antigravity 凭证 %s 的 project_id 缺失（CPA 凭证文件未含 cloudaicompanionProject 字段；尝试在 CLIProxyAPI 重新登录该凭证或检查文件完整性）", af.FileName)
	}

	headers := map[string]string{
		"Authorization": "Bearer $TOKEN$",
		"Content-Type":  "application/json",
		"User-Agent":    "antigravity/1.11.5 windows/amd64",
	}
	body, err := json.Marshal(map[string]string{"project": projectID})
	if err != nil {
		return fmt.Errorf("Antigravity marshal payload: %w", err)
	}

	var lastErr error
	var models map[string]any
	for _, url := range antigravityURLs {
		r, err := cpaAPICall(ctx, af.AuthIndex, "POST", url, headers, string(body))
		if err != nil {
			lastErr = err
			continue
		}
		if r.StatusCode < 200 || r.StatusCode >= 300 {
			lastErr = fmt.Errorf("Antigravity %s HTTP %d", url, r.StatusCode)
			continue
		}
		var payload struct {
			Models map[string]any `json:"models"`
		}
		if err := json.Unmarshal(r.Body, &payload); err != nil {
			lastErr = err
			continue
		}
		if len(payload.Models) > 0 {
			models = payload.Models
			lastErr = nil
			break
		}
		lastErr = fmt.Errorf("Antigravity %s 返回空 models", url)
	}
	if models == nil {
		if lastErr != nil {
			return lastErr
		}
		return fmt.Errorf("Antigravity 所有 URL 都失败")
	}

	entry.Windows = nil
	allModelNames := make([]string, 0, 16)
	for _, g := range antigravityGroups {
		minRem := 200.0
		var resetsAt time.Time
		hit := false
		for _, ident := range g.Identifiers {
			m, ok := models[ident].(map[string]any)
			if !ok {
				continue
			}
			hit = true
			allModelNames = append(allModelNames, ident)
			used := antigravityModelUsedPct(m)
			rem := 100 - used
			if rem < minRem {
				minRem = rem
			}
			if r := antigravityModelReset(m); !r.IsZero() && (resetsAt.IsZero() || r.Before(resetsAt)) {
				resetsAt = r
			}
		}
		if !hit {
			continue
		}
		entry.Windows = append(entry.Windows, CreditWindow{
			ID:               g.ID,
			Label:            g.Label,
			UsedPercent:      clampPct(100 - minRem),
			RemainingPercent: clampPct(minRem),
			ResetsAt:         resetsAt,
		})
	}
	entry.Models = allModelNames

	// 拉 paidTier.id / currentTier.id 作为套餐级别（PRO / ULTRA / FREE / UNKNOWN）。
	// 同步 jlcodes99/cockpit-tools quota.rs:fetch_project_id_with_context 的实现。
	// 失败不影响 windows 主数据，仅 log。
	caHeaders := map[string]string{
		"Authorization":     "Bearer $TOKEN$",
		"Content-Type":      "application/json",
		"User-Agent":        "antigravity/1.11.5 windows/amd64",
		"x-goog-api-client": "gl-node/22.10.0",
	}
	caPayload, _ := json.Marshal(map[string]any{
		"metadata": map[string]any{
			"ideName":       "antigravity",
			"ideType":       "ANTIGRAVITY",
			"ideVersion":    "1.11.5",
			"pluginVersion": "1.0.0",
			"platform":      "WINDOWS_AMD64",
			"duetProject":   projectID,
		},
		"mode":                    "FULL_ELIGIBILITY_CHECK",
		"cloudaicompanionProject": projectID,
	})
	r, err := cpaAPICall(ctx, af.AuthIndex, "POST",
		"https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist",
		caHeaders, string(caPayload))
	if err == nil && r != nil && r.StatusCode == 200 {
		if tier := pickGoogleCodeAssistTier(r.Body); tier != "" {
			entry.PlanType = tier
		}
	} else if err != nil {
		log.Printf("[CREDITS] Antigravity loadCodeAssist auth=%s 失败: %s", af.AuthIndex, sanitizeError(err.Error(), 200))
	}

	return nil
}

func antigravityModelUsedPct(m map[string]any) float64 {
	if q, ok := m["quota"].(map[string]any); ok {
		if v, ok := q["utilization"]; ok {
			return parseUsedPercent(v)
		}
		if cons, ok := q["consumed"].(float64); ok {
			if lim, ok := q["limit"].(float64); ok && lim > 0 {
				return clampPct(cons / lim * 100)
			}
		}
	}
	if v, ok := m["utilization"]; ok {
		return parseUsedPercent(v)
	}
	return 0
}

func antigravityModelReset(m map[string]any) time.Time {
	candidates := []string{"resets_at", "resetAt", "reset_at"}
	if q, ok := m["quota"].(map[string]any); ok {
		for _, k := range candidates {
			if v, ok := q[k].(string); ok {
				if t, err := time.Parse(time.RFC3339, v); err == nil {
					return t
				}
			}
		}
	}
	for _, k := range candidates {
		if v, ok := m[k].(string); ok {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				return t
			}
		}
	}
	return time.Time{}
}

// ─── Provider Fetcher: Gemini CLI ────────────────────────────────────────

const geminiCliQuotaURL = "https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuota"

// Gemini-CLI 模型 series（参照 CPA management center 前端代码）
// 同一 series 里取 minRemaining 作为该 series 的剩余额度，与 Antigravity 同款"短板原理"
type geminiCliSeries struct {
	ID, Label string
	ModelIDs  []string
}

var geminiCliSeriesList = []geminiCliSeries{
	{"gemini-flash-lite-series", "Gemini Flash Lite", []string{"gemini-2.5-flash-lite"}},
	{"gemini-flash-series", "Gemini Flash", []string{"gemini-3-flash-preview", "gemini-2.5-flash"}},
	{"gemini-pro-series", "Gemini Pro", []string{"gemini-3.1-pro-preview", "gemini-3-pro-preview", "gemini-2.5-pro"}},
}

// retrieveUserQuota 真实协议（参照 CPA management center 的 Pb fetcher）：
//
//	请求体：POST {"project": "<gcp-project-id>"}     ← 必须传 project_id（与 Antigravity 同源）
//	响应：
//	  {
//	    "buckets": [
//	      {
//	        "modelId": "gemini-2.5-pro",
//	        "tokenType": "input",                    // 可空
//	        "remainingFraction": 0.83,               // 剩余比例 0~1
//	        "remainingAmount": 1234,                 // 剩余原始数量
//	        "resetTime": "2025-..."                  // ISO8601
//	      },
//	      ...
//	    ],
//	    "tier": {...}                                // 套餐等元数据
//	  }
//
// 关键 corner case（CPA UI 同步实现）：
//   - remainingAmount==0 → 视为已耗尽（remainingFraction=0）
//   - remainingAmount==null && resetTime!=null → 视为已耗尽（remainingFraction=0）
//   - 同 series 多 model 取 min(remainingFraction)（短板原理）
func fetchGeminiCliQuota(ctx context.Context, af authFileLite, entry *CreditEntry) error {
	projectID := strings.TrimSpace(af.ProjectID)
	if projectID == "" {
		return fmt.Errorf("Gemini-CLI 凭证 %s 的 project_id 缺失（请确保该凭证在 CLIProxyAPI 已完成 OAuth 登录）", af.FileName)
	}
	headers := map[string]string{
		"Authorization": "Bearer $TOKEN$",
		"Content-Type":  "application/json",
	}
	body, err := json.Marshal(map[string]string{"project": projectID})
	if err != nil {
		return fmt.Errorf("Gemini-CLI marshal payload: %w", err)
	}
	r, err := cpaAPICall(ctx, af.AuthIndex, "POST", geminiCliQuotaURL, headers, string(body))
	if err != nil {
		return err
	}
	if r.StatusCode != 200 {
		return fmt.Errorf("Gemini-CLI quota HTTP %d: %s", r.StatusCode, sanitizeError(string(r.Body), errorBodyMaxBytes))
	}
	var p struct {
		Buckets []map[string]any `json:"buckets"`
	}
	if err := json.Unmarshal(r.Body, &p); err != nil {
		return fmt.Errorf("Gemini-CLI quota 解析失败: %w", err)
	}

	// modelId → series 的反向索引（参照 CPA UI 的 iy map）
	modelToSeries := make(map[string]int, 8)
	for idx, s := range geminiCliSeriesList {
		for _, mid := range s.ModelIDs {
			modelToSeries[mid] = idx
		}
	}

	// 按 series 聚合：取每个 series 内所有 bucket 的 min(remaining)
	type seriesAgg struct {
		minRem   float64
		resetsAt time.Time
		hit      bool
	}
	aggs := make([]seriesAgg, len(geminiCliSeriesList))
	for i := range aggs {
		aggs[i].minRem = 2.0 // 哨兵：大于 1 表示未命中
	}
	allModelIDs := make([]string, 0, 8)
	seenModel := make(map[string]bool, 8)

	for _, b := range p.Buckets {
		modelID := geminiCliStripVertex(geminiCliStr(b["modelId"], b["model_id"]))
		if modelID == "" {
			continue
		}
		idx, ok := modelToSeries[modelID]
		if !ok {
			continue // 未识别的 model 跳过
		}
		// remainingFraction 优先，否则按 remainingAmount==0 / resetTime 启发式补 0
		rem := geminiCliFraction(b)
		if rem < 0 {
			continue
		}

		if !seenModel[modelID] {
			seenModel[modelID] = true
			allModelIDs = append(allModelIDs, modelID)
		}
		a := &aggs[idx]
		a.hit = true
		if rem < a.minRem {
			a.minRem = rem
		}
		if rs := geminiCliResetTime(b); !rs.IsZero() && (a.resetsAt.IsZero() || rs.Before(a.resetsAt)) {
			a.resetsAt = rs
		}
	}

	entry.Windows = nil
	for i, s := range geminiCliSeriesList {
		a := aggs[i]
		if !a.hit {
			continue
		}
		used := clampPct((1 - a.minRem) * 100)
		entry.Windows = append(entry.Windows, CreditWindow{
			ID:               s.ID,
			Label:            s.Label,
			UsedPercent:      used,
			RemainingPercent: clampPct(100 - used),
			ResetsAt:         a.resetsAt,
		})
	}
	if len(allModelIDs) > 0 {
		entry.Models = allModelIDs
	} else {
		entry.Models = []string{"gemini-2.5-pro", "gemini-2.5-flash", "gemini-2.5-flash-lite"}
	}

	// 拉 paidTier.id / currentTier.id 作为 Gemini CLI 套餐级别（FREE / LEGACY / STANDARD /
	// PRO / ULTRA 等，来自 Google Cloud Code Assist）。逻辑与 Antigravity 一致，但 metadata
	// 标 ideName=gemini-cli 让上游区分。
	caHeaders := map[string]string{
		"Authorization":     "Bearer $TOKEN$",
		"Content-Type":      "application/json",
		"User-Agent":        "GeminiCLI/0.5.0 (linux; x64)",
		"x-goog-api-client": "gl-node/22.10.0",
	}
	caPayload, _ := json.Marshal(map[string]any{
		"metadata": map[string]any{
			"ideName":       "gemini-cli",
			"ideType":       "IDE_UNSPECIFIED",
			"ideVersion":    "0.5.0",
			"pluginVersion": "0.5.0",
			"platform":      "LINUX_AMD64",
			"duetProject":   projectID,
		},
		"mode":                    "FULL_ELIGIBILITY_CHECK",
		"cloudaicompanionProject": projectID,
	})
	r2, err2 := cpaAPICall(ctx, af.AuthIndex, "POST",
		"https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist",
		caHeaders, string(caPayload))
	if err2 == nil && r2 != nil && r2.StatusCode == 200 {
		if tier := pickGoogleCodeAssistTier(r2.Body); tier != "" {
			entry.PlanType = tier
		}
	} else if err2 != nil {
		log.Printf("[CREDITS] Gemini-CLI loadCodeAssist auth=%s 失败: %s", af.AuthIndex, sanitizeError(err2.Error(), 200))
	}

	return nil
}

// geminiCliStr 多候选键取第一个非空字符串。
func geminiCliStr(vs ...any) string {
	for _, v := range vs {
		if s, ok := v.(string); ok {
			s = strings.TrimSpace(s)
			if s != "" {
				return s
			}
		}
	}
	return ""
}

// geminiCliStripVertex 兼容 CPA UI 的 _y 函数：去掉 "_vertex" 后缀
func geminiCliStripVertex(s string) string {
	const sfx = "_vertex"
	if strings.HasSuffix(s, sfx) {
		return s[:len(s)-len(sfx)]
	}
	return s
}

// geminiCliFraction 从一个 bucket 算出剩余比例 [0,1]，按 CPA UI 启发式：
//
//	remainingFraction 有值 → 用它（clamp [0,1]）
//	否则 remainingAmount==0 → 0
//	否则 remainingAmount<=0 但 null && resetTime → 视为 0
//	否则返回 -1（不参与聚合）
func geminiCliFraction(b map[string]any) float64 {
	if rem, ok := geminiCliFloat(b, "remainingFraction", "remaining_fraction"); ok {
		if rem < 0 {
			rem = 0
		}
		if rem > 1 {
			rem = 1
		}
		return rem
	}
	amt, hasAmt := geminiCliFloat(b, "remainingAmount", "remaining_amount")
	if hasAmt && amt <= 0 {
		return 0
	}
	if !hasAmt {
		// 没数量但有重置时间 → 已耗尽
		if t := geminiCliResetTime(b); !t.IsZero() {
			return 0
		}
	}
	return -1
}

func geminiCliFloat(b map[string]any, keys ...string) (float64, bool) {
	for _, k := range keys {
		switch v := b[k].(type) {
		case float64:
			return v, true
		case int:
			return float64(v), true
		case int64:
			return float64(v), true
		case string:
			s := strings.TrimSpace(v)
			if s == "" {
				continue
			}
			if strings.HasSuffix(s, "%") {
				if x, err := strconv.ParseFloat(strings.TrimSuffix(s, "%"), 64); err == nil {
					return x / 100, true
				}
			} else if x, err := strconv.ParseFloat(s, 64); err == nil {
				return x, true
			}
		}
	}
	return 0, false
}

func geminiCliResetTime(b map[string]any) time.Time {
	for _, k := range []string{"resetTime", "reset_time", "resetsAt", "resets_at"} {
		if s, ok := b[k].(string); ok && strings.TrimSpace(s) != "" {
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				return t
			}
		}
	}
	return time.Time{}
}
