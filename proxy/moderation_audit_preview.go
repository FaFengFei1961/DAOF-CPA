package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const moderationAuditPreviewRunes = 1200

type auditRedactionRule struct {
	re   *regexp.Regexp
	repl string
}

var moderationAuditRedactionRules = []auditRedactionRule{
	{regexp.MustCompile(`(?is)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`), `[REDACTED_PRIVATE_KEY]`},
	{regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]{12,}`), `Bearer [REDACTED]`},
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{12,}\b`), `[REDACTED_API_KEY]`},
	{regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`), `[REDACTED_AWS_KEY]`},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`), `[REDACTED_JWT]`},
	{regexp.MustCompile(`(?i)\b([A-Z0-9_]*(?:API[_-]?KEY|TOKEN|SECRET|PASSWORD|PASSWD|PRIVATE[_-]?KEY)[A-Z0-9_]*)\s*[:=]\s*("[^"]{4,}"|'[^']{4,}'|[^\s,;]{4,})`), `$1=[REDACTED]`},
	{regexp.MustCompile(`(?i)\b(api[_-]?key|access[_-]?token|refresh[_-]?token|id[_-]?token|secret|password|authorization)\s*["']?\s*[:=]\s*["']?[^"'\s,;]{4,}`), `$1=[REDACTED]`},
}

func enrichModerationAuditDetails(details string, reviewText string) string {
	base := map[string]any{}
	rawDetails := strings.TrimSpace(details)
	if rawDetails != "" {
		if err := json.Unmarshal([]byte(rawDetails), &base); err != nil {
			base["raw_details"] = rawDetails
		}
	}
	for k, v := range moderationAuditPreviewFields(reviewText, moderationAuditPreviewRunes) {
		base[k] = v
	}
	out, err := json.Marshal(base)
	if err != nil {
		return rawDetails
	}
	return string(out)
}

func moderationAuditPreviewFields(text string, maxRunes int) map[string]any {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	rawRunes := utf8.RuneCountInString(text)
	redacted, didRedact := redactModerationAuditText(text)
	preview := compactAuditPreviewWhitespace(redacted)
	previewRunes := utf8.RuneCountInString(preview)
	truncated := false
	if maxRunes > 0 && previewRunes > maxRunes {
		r := []rune(preview)
		preview = string(r[:maxRunes]) + "..."
		truncated = true
	}
	sum := sha256.Sum256([]byte(text))
	return map[string]any{
		"content_preview":        preview,
		"content_preview_runes":  utf8.RuneCountInString(preview),
		"content_runes":          rawRunes,
		"content_sha256":         hex.EncodeToString(sum[:]),
		"content_truncated":      truncated,
		"content_redacted":       didRedact,
		"content_preview_policy": "redacted_truncated",
	}
}

func redactModerationAuditText(text string) (string, bool) {
	out := text
	redacted := false
	for _, rule := range moderationAuditRedactionRules {
		next := rule.re.ReplaceAllString(out, rule.repl)
		if next != out {
			redacted = true
			out = next
		}
	}
	return out, redacted
}

func compactAuditPreviewWhitespace(text string) string {
	clean := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return ' '
		}
		return r
	}, text)
	return strings.Join(strings.Fields(clean), " ")
}
