package proxy

import (
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
	"sync"
)

const (
	RiskRuleActionBlock       = "block"
	RiskRuleActionModelReview = "model_review"
	RiskRuleActionScoreOnly   = "score_only"

	moderationAnyGroupWindowChars = 420
)

type ModerationRiskRule struct {
	ID        string     `json:"id"`
	Category  string     `json:"category"`
	Severity  string     `json:"severity"`
	Action    string     `json:"action"`
	Score     int        `json:"score"`
	Contains  []string   `json:"contains,omitempty"`
	Any       []string   `json:"any,omitempty"`
	AnyGroups [][]string `json:"any_groups,omitempty"`
	Regex     []string   `json:"regex,omitempty"`
	Reason    string     `json:"reason,omitempty"`
}

type compiledModerationRiskRule struct {
	rule       ModerationRiskRule
	contains   []string
	any        []string
	anyGroups  [][]string
	regexps    []*regexp.Regexp
	regexRaw   []string
	hasMatcher bool
}

type ModerationRiskMatch struct {
	ID       string   `json:"id"`
	Category string   `json:"category"`
	Severity string   `json:"severity"`
	Action   string   `json:"action"`
	Score    int      `json:"score"`
	Reason   string   `json:"reason,omitempty"`
	Regex    []string `json:"regex,omitempty"`
}

type ModerationRiskResult struct {
	Matches      []ModerationRiskMatch `json:"matches"`
	TotalScore   int                   `json:"total_score"`
	HighestScore int                   `json:"highest_score"`
}

type moderationRiskRuleEngine struct {
	mu    sync.RWMutex
	raw   string
	rules []compiledModerationRiskRule
}

var globalModerationRiskRules = &moderationRiskRuleEngine{}

func ParseModerationRiskRules(raw string) ([]ModerationRiskRule, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var rules []ModerationRiskRule
	if err := json.Unmarshal([]byte(raw), &rules); err != nil {
		return nil, err
	}
	if len(rules) > 300 {
		return nil, fmt.Errorf("too many risk rules: %d > 300", len(rules))
	}
	clean := make([]ModerationRiskRule, 0, len(rules))
	seen := map[string]struct{}{}
	for i, r := range rules {
		compiled, err := compileModerationRiskRule(r, i)
		if err != nil {
			return nil, err
		}
		idKey := strings.ToLower(compiled.rule.ID)
		if _, ok := seen[idKey]; ok {
			return nil, fmt.Errorf("duplicate risk rule id %q", compiled.rule.ID)
		}
		seen[idKey] = struct{}{}
		clean = append(clean, compiled.rule)
	}
	return clean, nil
}

func EvaluateModerationRiskRules(prompt string) ModerationRiskResult {
	if strings.TrimSpace(prompt) == "" {
		return ModerationRiskResult{}
	}
	SysConfigMutex.RLock()
	raw := strings.TrimSpace(SysConfigCache["moderation_risk_rules"])
	SysConfigMutex.RUnlock()
	rules := globalModerationRiskRules.ensure(raw)
	if len(rules) == 0 {
		return ModerationRiskResult{}
	}
	lower := strings.ToLower(prompt)
	result := ModerationRiskResult{}
	for _, r := range rules {
		if !r.matches(lower) {
			continue
		}
		m := ModerationRiskMatch{
			ID:       r.rule.ID,
			Category: r.rule.Category,
			Severity: r.rule.Severity,
			Action:   r.rule.Action,
			Score:    r.rule.Score,
			Reason:   r.rule.Reason,
			Regex:    append([]string(nil), r.regexRaw...),
		}
		result.Matches = append(result.Matches, m)
		result.TotalScore += m.Score
		if m.Score > result.HighestScore {
			result.HighestScore = m.Score
		}
	}
	return result
}

func InvalidateRiskRuleCache() {
	globalModerationRiskRules.mu.Lock()
	globalModerationRiskRules.raw = ""
	globalModerationRiskRules.rules = nil
	globalModerationRiskRules.mu.Unlock()
}

func (e *moderationRiskRuleEngine) ensure(raw string) []compiledModerationRiskRule {
	raw = strings.TrimSpace(raw)
	e.mu.RLock()
	if raw == e.raw {
		rules := append([]compiledModerationRiskRule(nil), e.rules...)
		e.mu.RUnlock()
		return rules
	}
	e.mu.RUnlock()

	compiled, err := compileModerationRiskRules(raw)
	if err != nil {
		log.Printf("[MODERATION-RISK] invalid moderation_risk_rules, disabling risk rules: %v", err)
		compiled = nil
	}
	e.mu.Lock()
	e.raw = raw
	e.rules = compiled
	rules := append([]compiledModerationRiskRule(nil), e.rules...)
	e.mu.Unlock()
	return rules
}

func compileModerationRiskRules(raw string) ([]compiledModerationRiskRule, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var rules []ModerationRiskRule
	if err := json.Unmarshal([]byte(raw), &rules); err != nil {
		return nil, err
	}
	if len(rules) > 300 {
		return nil, fmt.Errorf("too many risk rules: %d > 300", len(rules))
	}
	out := make([]compiledModerationRiskRule, 0, len(rules))
	seen := map[string]struct{}{}
	for i, r := range rules {
		compiled, err := compileModerationRiskRule(r, i)
		if err != nil {
			return nil, err
		}
		idKey := strings.ToLower(compiled.rule.ID)
		if _, ok := seen[idKey]; ok {
			return nil, fmt.Errorf("duplicate risk rule id %q", compiled.rule.ID)
		}
		seen[idKey] = struct{}{}
		out = append(out, compiled)
	}
	return out, nil
}

func compileModerationRiskRule(r ModerationRiskRule, idx int) (compiledModerationRiskRule, error) {
	c := compiledModerationRiskRule{}
	r.ID = sanitizeRiskRuleID(r.ID)
	if r.ID == "" {
		r.ID = fmt.Sprintf("risk_rule_%03d", idx+1)
	}
	r.Category = sanitizeRiskRuleLabel(r.Category, "policy_evasion")
	r.Severity = sanitizeRiskRuleSeverity(r.Severity)
	r.Action = sanitizeRiskRuleAction(r.Action)
	if r.Score <= 0 {
		r.Score = defaultRiskRuleScore(r.Severity)
	}
	if r.Score > 1000 {
		r.Score = 1000
	}
	r.Reason = strings.TrimSpace(strings.Join(strings.Fields(r.Reason), " "))
	if len([]rune(r.Reason)) > 240 {
		r.Reason = string([]rune(r.Reason)[:240])
	}

	c.rule = r
	c.contains = normalizeRiskTerms(r.Contains, 80, "contains", r.ID)
	c.any = normalizeRiskTerms(r.Any, 80, "any", r.ID)
	c.anyGroups = make([][]string, 0, len(r.AnyGroups))
	for i, group := range r.AnyGroups {
		terms := normalizeRiskTerms(group, 80, fmt.Sprintf("any_groups[%d]", i), r.ID)
		if len(terms) > 0 {
			c.anyGroups = append(c.anyGroups, terms)
		}
	}
	if len(r.Regex) > 20 {
		return c, fmt.Errorf("risk rule %q has too many regex patterns", r.ID)
	}
	for _, raw := range r.Regex {
		pat := strings.TrimSpace(raw)
		if pat == "" {
			continue
		}
		if len([]rune(pat)) > 600 {
			return c, fmt.Errorf("risk rule %q regex too long", r.ID)
		}
		re, err := regexp.Compile(pat)
		if err != nil {
			return c, fmt.Errorf("risk rule %q invalid regex %q: %w", r.ID, pat, err)
		}
		c.regexps = append(c.regexps, re)
		c.regexRaw = append(c.regexRaw, pat)
	}
	c.hasMatcher = len(c.contains) > 0 || len(c.any) > 0 || len(c.anyGroups) > 0 || len(c.regexps) > 0
	if !c.hasMatcher {
		return c, fmt.Errorf("risk rule %q has no matcher", r.ID)
	}
	return c, nil
}

func (r compiledModerationRiskRule) matches(lowerPrompt string) bool {
	for _, term := range r.contains {
		if !strings.Contains(lowerPrompt, term) {
			return false
		}
	}
	if len(r.any) > 0 && !containsAnyRiskTerm(lowerPrompt, r.any) {
		return false
	}
	if len(r.anyGroups) > 0 && !containsRiskTermGroupsWithinWindow(lowerPrompt, r.anyGroups) {
		return false
	}
	if len(r.regexps) > 0 {
		matched := false
		for _, re := range r.regexps {
			if re.MatchString(lowerPrompt) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func (r ModerationRiskResult) HasMatches() bool {
	return len(r.Matches) > 0
}

func (r ModerationRiskResult) ShouldBlock() bool {
	for _, m := range r.Matches {
		if m.Action == RiskRuleActionBlock {
			return true
		}
	}
	return false
}

func (r ModerationRiskResult) NeedsModelReview() bool {
	for _, m := range r.Matches {
		if m.Action == RiskRuleActionModelReview {
			return true
		}
	}
	return false
}

func (r ModerationRiskResult) PrimaryMatchID() string {
	if len(r.Matches) == 0 {
		return ""
	}
	best := r.Matches[0]
	for _, m := range r.Matches[1:] {
		if m.Action == RiskRuleActionBlock && best.Action != RiskRuleActionBlock {
			best = m
			continue
		}
		if m.Action == best.Action && m.Score > best.Score {
			best = m
		}
	}
	return best.ID
}

func containsAnyRiskTerm(s string, terms []string) bool {
	for _, term := range terms {
		if strings.Contains(s, term) {
			return true
		}
	}
	return false
}

type riskTermOccurrence struct {
	pos   int
	group int
}

func containsRiskTermGroupsWithinWindow(s string, groups [][]string) bool {
	if len(groups) == 0 {
		return true
	}
	if len(groups) == 1 {
		return containsAnyRiskTerm(s, groups[0])
	}
	occurrences := make([]riskTermOccurrence, 0, len(groups)*4)
	for groupIdx, terms := range groups {
		positions := riskTermPositions(s, terms, 128)
		if len(positions) == 0 {
			return false
		}
		for _, pos := range positions {
			occurrences = append(occurrences, riskTermOccurrence{pos: pos, group: groupIdx})
		}
	}
	sort.Slice(occurrences, func(i, j int) bool {
		if occurrences[i].pos == occurrences[j].pos {
			return occurrences[i].group < occurrences[j].group
		}
		return occurrences[i].pos < occurrences[j].pos
	})

	counts := make([]int, len(groups))
	have := 0
	left := 0
	for right, occ := range occurrences {
		if counts[occ.group] == 0 {
			have++
		}
		counts[occ.group]++
		for left <= right && occurrences[right].pos-occurrences[left].pos > moderationAnyGroupWindowChars {
			leftOcc := occurrences[left]
			counts[leftOcc.group]--
			if counts[leftOcc.group] == 0 {
				have--
			}
			left++
		}
		if have == len(groups) {
			return true
		}
	}
	return false
}

func riskTermPositions(s string, terms []string, max int) []int {
	if max <= 0 {
		return nil
	}
	out := make([]int, 0, 4)
	for _, term := range terms {
		if term == "" {
			continue
		}
		offset := 0
		for {
			idx := strings.Index(s[offset:], term)
			if idx < 0 {
				break
			}
			out = append(out, offset+idx)
			if len(out) >= max {
				return out
			}
			offset += idx + len(term)
			if offset >= len(s) {
				break
			}
		}
	}
	return out
}

func normalizeRiskTerms(in []string, max int, field, ruleID string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]struct{}{}
	for _, term := range in {
		term = strings.ToLower(strings.TrimSpace(strings.Join(strings.Fields(term), " ")))
		if term == "" {
			continue
		}
		if len([]rune(term)) > 160 {
			log.Printf("[MODERATION-RISK] risk rule %s %s term too long, skipped", ruleID, field)
			continue
		}
		if _, ok := seen[term]; ok {
			continue
		}
		seen[term] = struct{}{}
		out = append(out, term)
		if len(out) >= max {
			break
		}
	}
	return out
}

func sanitizeRiskRuleID(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.ReplaceAll(s, " ", "_")
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' {
			b.WriteRune(r)
		}
	}
	return strings.Trim(b.String(), "._-")
}

func sanitizeRiskRuleLabel(s, def string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.ReplaceAll(s, "-", "_")
	switch s {
	case "jailbreak", "prompt_leak", "credential_exfiltration", "tool_forgery", "moderation_bypass", "abuse_automation", "ctf_pretext", "indirect_injection", "policy_evasion":
		return s
	default:
		return def
	}
}

func sanitizeRiskRuleSeverity(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "critical", "high", "medium", "low":
		return strings.ToLower(strings.TrimSpace(s))
	default:
		return "medium"
	}
}

func sanitizeRiskRuleAction(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case RiskRuleActionBlock, RiskRuleActionModelReview, "review", "model-review", RiskRuleActionScoreOnly, "score", "score-only":
		if s == "review" || s == "model-review" {
			return RiskRuleActionModelReview
		}
		if s == "score" || s == "score-only" {
			return RiskRuleActionScoreOnly
		}
		return s
	default:
		return RiskRuleActionScoreOnly
	}
}

func defaultRiskRuleScore(severity string) int {
	switch severity {
	case "critical":
		return 100
	case "high":
		return 70
	case "low":
		return 10
	default:
		return 30
	}
}
