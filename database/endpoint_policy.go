package database

import "strings"

const (
	EndpointPolicyAll             = "all"
	EndpointPolicyNoChatNonStream = "no_chat_non_stream"
	EndpointPolicyResponsesOnly   = "responses_only"
)

// NormalizeEndpointPolicy returns the canonical enum value used in DB/API.
func NormalizeEndpointPolicy(policy string) string {
	p := strings.ToLower(strings.TrimSpace(policy))
	if p == "" {
		return EndpointPolicyAll
	}
	return p
}

// IsValidEndpointPolicy validates the persisted endpoint policy enum.
func IsValidEndpointPolicy(policy string) bool {
	switch NormalizeEndpointPolicy(policy) {
	case EndpointPolicyAll, EndpointPolicyNoChatNonStream, EndpointPolicyResponsesOnly:
		return true
	default:
		return false
	}
}

// DefaultEndpointPolicyForModel applies model-specific safety defaults without
// weakening a stricter admin choice such as responses_only.
func DefaultEndpointPolicyForModel(modelID, policy string) string {
	p := NormalizeEndpointPolicy(policy)
	if strings.EqualFold(strings.TrimSpace(modelID), "gpt-5.5") && p == EndpointPolicyAll {
		return EndpointPolicyNoChatNonStream
	}
	return p
}
