package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

type ModerationKeywordCandidate struct {
	Category string `json:"category"`
	Keyword  string `json:"keyword"`
	Severity string `json:"severity"`
	Reason   string `json:"reason"`
}

type ModerationKeywordGenerationResult struct {
	Candidates []ModerationKeywordCandidate `json:"candidates"`
	Provider   string                       `json:"provider"`
	Endpoint   string                       `json:"endpoint"`
	Model      string                       `json:"model"`
	AuthIndex  string                       `json:"auth_index"`
}

type keywordAIResponse struct {
	Candidates []ModerationKeywordCandidate `json:"candidates"`
}

func GenerateModerationKeywordCandidates(ctx context.Context, focus string, maxCandidates int) (ModerationKeywordGenerationResult, error) {
	cfg := LoadModerationConfig()
	out := ModerationKeywordGenerationResult{
		Provider: cfg.Provider,
		Endpoint: cfg.DiagnosticEndpoint(),
		Model:    cfg.Model,
	}
	if !cfg.IsConfigured() {
		return out, fmt.Errorf("moderation not configured")
	}
	if maxCandidates <= 0 {
		maxCandidates = readKeywordAIMaxCandidates()
	}
	if maxCandidates <= 0 {
		maxCandidates = 80
	}
	if maxCandidates > 200 {
		maxCandidates = 200
	}

	existing := currentModerationKeywordsForPrompt(240)
	return generateCLIProxyKeywordCandidates(ctx, cfg, out, focus, existing, maxCandidates)
}

func generateCLIProxyKeywordCandidates(ctx context.Context, cfg ModerationConfig, out ModerationKeywordGenerationResult, focus string, existing []string, maxCandidates int) (ModerationKeywordGenerationResult, error) {
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = defaultCLIProxyModerationModel
	}
	out.Model = model
	baseURL, err := getValidatedCliproxyURL()
	if err != nil {
		return out, fmt.Errorf("cliproxy_url safety validation failed: %w", err)
	}
	endpoint := baseURL + "/v1/chat/completions"
	out.Endpoint = endpoint
	body, err := json.Marshal(buildCLIProxyKeywordAIRequest(focus, existing, maxCandidates, model))
	if err != nil {
		return out, fmt.Errorf("CPA model keyword AI marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return out, fmt.Errorf("CPA model keyword AI NewRequest failed: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if k := getCliproxyKey(); k != "" {
		req.Header.Set("Authorization", "Bearer "+k)
	}
	resp, err := cpaHTTPClient.Do(req)
	if err != nil {
		return out, fmt.Errorf("CPA model keyword AI request failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := readLimited(resp.Body, responseBodyMaxBytes)
	if err != nil {
		return out, fmt.Errorf("read CPA model keyword AI response failed: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return out, parseOpenAICompatibleAPIError(resp.StatusCode, respBody, resp.Header)
	}
	text, err := extractOpenAICompatibleChoiceText(respBody, "CPA model keyword AI")
	if err != nil {
		return out, err
	}
	candidates, err := ParseModerationKeywordCandidates(text, maxCandidates, existing)
	if err != nil {
		return out, err
	}
	out.Candidates = candidates
	return out, nil
}

func readKeywordAIMaxCandidates() int {
	SysConfigMutex.RLock()
	defer SysConfigMutex.RUnlock()
	raw := strings.TrimSpace(SysConfigCache["moderation_keyword_ai_max_candidates"])
	if raw == "" {
		return 80
	}
	n := 0
	for _, ch := range raw {
		if ch < '0' || ch > '9' {
			return 80
		}
		n = n*10 + int(ch-'0')
	}
	if n < 1 {
		return 80
	}
	if n > 200 {
		return 200
	}
	return n
}

func currentModerationKeywordsForPrompt(limit int) []string {
	SysConfigMutex.RLock()
	raw := strings.TrimSpace(SysConfigCache["moderation_keywords"])
	SysConfigMutex.RUnlock()
	if raw == "" {
		return nil
	}
	var arr []string
	if err := json.Unmarshal([]byte(raw), &arr); err != nil {
		return nil
	}
	out := make([]string, 0, minInt(len(arr), limit))
	seen := map[string]struct{}{}
	for _, kw := range arr {
		kw = strings.TrimSpace(kw)
		if kw == "" {
			continue
		}
		key := strings.ToLower(kw)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, kw)
		if len(out) >= limit {
			break
		}
	}
	sort.Strings(out)
	return out
}

func buildCLIProxyKeywordAIRequest(focus string, existing []string, maxCandidates int, model string) map[string]any {
	focus = sanitizeKeywordAIFocus(focus)
	if focus == "" {
		focus = "jailbreak, prompt injection, system prompt leakage, tool fingerprint leakage, credential theft, moderation bypass, and abuse automation patterns for LLM gateway defense"
	}
	existingJSON, _ := json.Marshal(existing)
	return map[string]any{
		"model":       model,
		"temperature": 0.2,
		"stream":      false,
		"max_tokens":  8192,
		"messages": []map[string]string{
			{
				"role": "system",
				"content": strings.Join([]string{
					"You improve a defensive keyword dictionary for an LLM API gateway.",
					"Generate concise keyword or phrase candidates that help detect jailbreak, prompt-injection, system prompt extraction, tool fingerprint leakage, credential theft, moderation bypass, and abuse automation attempts.",
					"Do not write executable instructions, exploit steps, secrets, or long attack prompts.",
					"Return JSON only with shape {\"candidates\":[{\"category\":\"jailbreak\",\"keyword\":\"...\",\"severity\":\"high\",\"reason\":\"...\"}]}",
					"Allowed categories: jailbreak, prompt_leak, credential, tool_fingerprint, moderation_bypass, abuse_automation, policy_evasion.",
					"Allowed severities: low, medium, high, critical.",
					"Keep each keyword 2-80 characters where possible. Prefer stable substrings over full prompts.",
				}, " "),
			},
			{
				"role":    "user",
				"content": fmt.Sprintf("Existing keywords JSON:\n%s\n\nFocus:\n%s\n\nGenerate at most %d new candidates. Exclude duplicates and near-duplicates.", string(existingJSON), focus, maxCandidates),
			},
		},
	}
}

func ParseModerationKeywordCandidates(text string, maxCandidates int, existing []string) ([]ModerationKeywordCandidate, error) {
	text = stripJSONFence(strings.TrimSpace(text))
	if maxCandidates <= 0 || maxCandidates > 200 {
		maxCandidates = 200
	}
	var payload keywordAIResponse
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		var arr []ModerationKeywordCandidate
		if arrErr := json.Unmarshal([]byte(text), &arr); arrErr != nil {
			return nil, fmt.Errorf("invalid keyword candidates JSON: %w", err)
		}
		payload.Candidates = arr
	}

	seen := map[string]struct{}{}
	for _, kw := range existing {
		key := strings.ToLower(strings.TrimSpace(kw))
		if key != "" {
			seen[key] = struct{}{}
		}
	}

	out := make([]ModerationKeywordCandidate, 0, minInt(maxCandidates, len(payload.Candidates)))
	for _, c := range payload.Candidates {
		c.Keyword = sanitizeKeywordCandidate(c.Keyword)
		if c.Keyword == "" {
			continue
		}
		key := strings.ToLower(c.Keyword)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		c.Category = sanitizeKeywordLabel(c.Category, "jailbreak")
		c.Severity = sanitizeKeywordSeverity(c.Severity)
		c.Reason = sanitizeKeywordReason(c.Reason)
		out = append(out, c)
		if len(out) >= maxCandidates {
			break
		}
	}
	return out, nil
}

func sanitizeKeywordCandidate(s string) string {
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	s = strings.Trim(s, "\"'`")
	if s == "" || utf8.RuneCountInString(s) < 2 || utf8.RuneCountInString(s) > 120 {
		return ""
	}
	if isMostlyPunctuation(s) {
		return ""
	}
	if strings.ContainsAny(s, "\r\n\t") {
		return ""
	}
	if strings.Contains(s, "://") {
		if _, err := url.ParseRequestURI(s); err == nil {
			return ""
		}
	}
	return s
}

func sanitizeKeywordLabel(s, def string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "-", "_")
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		}
	}
	out := b.String()
	switch out {
	case "jailbreak", "prompt_leak", "credential", "tool_fingerprint", "moderation_bypass", "abuse_automation", "policy_evasion":
		return out
	default:
		return def
	}
}

func sanitizeKeywordSeverity(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "low", "medium", "high", "critical":
		return strings.ToLower(strings.TrimSpace(s))
	default:
		return "medium"
	}
}

func sanitizeKeywordReason(s string) string {
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	if utf8.RuneCountInString(s) > 180 {
		r := []rune(s)
		s = string(r[:180])
	}
	return s
}

func sanitizeKeywordAIFocus(s string) string {
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	if utf8.RuneCountInString(s) > 1000 {
		r := []rune(s)
		s = string(r[:1000])
	}
	return s
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func isMostlyPunctuation(s string) bool {
	total := 0
	punct := 0
	for _, r := range s {
		total++
		if unicode.IsPunct(r) || unicode.IsSymbol(r) {
			punct++
		}
	}
	return total > 0 && punct*2 >= total
}
