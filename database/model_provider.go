package database

import (
	"strings"
	"unicode"
)

// IsOpenAIModelID returns true for model IDs that belong to the OpenAI/Codex
// family exposed to customers, regardless of the underlying channel type.
func IsOpenAIModelID(modelID string) bool {
	id := strings.ToLower(strings.TrimSpace(modelID))
	if id == "" {
		return false
	}
	if strings.Contains(id, "openai") || hasOpenAIGPTSegment(id) {
		return true
	}
	if strings.HasPrefix(id, "chatgpt-") || strings.HasPrefix(id, "codex-") {
		return true
	}
	return isOpenAIOSeriesModelID(id)
}

func hasOpenAIGPTSegment(id string) bool {
	for _, part := range strings.FieldsFunc(id, func(r rune) bool {
		switch r {
		case '/', ':', ' ', '\t':
			return true
		default:
			return false
		}
	}) {
		if part == "gpt" || strings.HasPrefix(part, "gpt-") || strings.HasPrefix(part, "gpt_") {
			return true
		}
	}
	return false
}

func isOpenAIOSeriesModelID(id string) bool {
	if len(id) < 2 || id[0] != 'o' {
		return false
	}
	return unicode.IsDigit(rune(id[1]))
}
