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
