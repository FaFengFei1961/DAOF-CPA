package proxy

import (
	"fmt"
	"strings"

	"daof-cpa/database"
)

func endpointPolicyAllows(policy, path string, isStream bool) bool {
	p := database.NormalizeEndpointPolicy(policy)
	lowerPath := strings.ToLower(path)
	isChatCompletions := strings.Contains(lowerPath, "/v1/chat/completions")
	isResponses := strings.Contains(lowerPath, "/v1/responses")

	switch p {
	case database.EndpointPolicyNoChatNonStream:
		return !(isChatCompletions && !isStream)
	case database.EndpointPolicyResponsesOnly:
		return isResponses
	default:
		return true
	}
}

func filterRoutesByEndpointPolicy(routes []*database.ChannelModel, path string, isStream bool) ([]*database.ChannelModel, int) {
	if len(routes) == 0 {
		return routes, 0
	}
	allowed := make([]*database.ChannelModel, 0, len(routes))
	blocked := 0
	for _, route := range routes {
		if route == nil {
			continue
		}
		if endpointPolicyAllows(route.EndpointPolicy, path, isStream) {
			allowed = append(allowed, route)
			continue
		}
		blocked++
	}
	return allowed, blocked
}

func unsupportedEndpointMessage(modelName, path string, isStream bool) string {
	mode := "non-streaming"
	if isStream {
		mode = "streaming"
	}
	if strings.Contains(strings.ToLower(path), "/v1/chat/completions") {
		return fmt.Sprintf("%s is not available on %s /v1/chat/completions for the configured upstream route. Use /v1/responses, enable a compatible route, or switch models.", modelName, mode)
	}
	return fmt.Sprintf("%s is not available on %s for the configured upstream route. Use a compatible endpoint or switch models.", modelName, path)
}
