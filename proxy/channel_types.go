package proxy

import "strings"

const (
	ChannelTypeOpenAI    = "openai"
	ChannelTypeAnthropic = "anthropic"
	ChannelTypeGemini    = "gemini"
	ChannelTypeGoogleCLI = "google-cli"
	ChannelTypeCodex     = "codex"
	ChannelTypeCLIProxy  = "cliproxy"
)

var allowedChannelTypes = map[string]bool{
	ChannelTypeOpenAI:    true,
	ChannelTypeAnthropic: true,
	ChannelTypeGemini:    true,
	ChannelTypeGoogleCLI: true,
	ChannelTypeCodex:     true,
	ChannelTypeCLIProxy:  true,
}

func NormalizeChannelType(channelType string) string {
	return strings.ToLower(strings.TrimSpace(channelType))
}

func IsAllowedChannelType(channelType string) bool {
	return allowedChannelTypes[NormalizeChannelType(channelType)]
}

func normalizeCLIProxyPath(path string) string {
	p := strings.TrimSpace(path)
	switch {
	case strings.EqualFold(p, "/v1/v1/messages"):
		return "/v1/messages"
	case strings.EqualFold(p, "/v1/v1/messages/count_tokens"):
		return "/v1/messages/count_tokens"
	}
	return p
}
