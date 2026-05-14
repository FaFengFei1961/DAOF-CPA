package proxy

import (
	"testing"

	"daof-ai-hub/database"
)

func TestEndpointPolicyAllows(t *testing.T) {
	tests := []struct {
		name     string
		policy   string
		path     string
		stream   bool
		expected bool
	}{
		{"all allows chat", database.EndpointPolicyAll, "/v1/chat/completions", false, true},
		{"no_chat_non_stream blocks non-stream chat", database.EndpointPolicyNoChatNonStream, "/v1/chat/completions", false, false},
		{"no_chat_non_stream allows stream chat", database.EndpointPolicyNoChatNonStream, "/v1/chat/completions", true, true},
		{"no_chat_non_stream allows responses", database.EndpointPolicyNoChatNonStream, "/v1/responses", false, true},
		{"responses_only blocks stream chat", database.EndpointPolicyResponsesOnly, "/v1/chat/completions", true, false},
		{"responses_only allows responses", database.EndpointPolicyResponsesOnly, "/v1/responses", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := endpointPolicyAllows(tt.policy, tt.path, tt.stream); got != tt.expected {
				t.Fatalf("endpointPolicyAllows()=%v want %v", got, tt.expected)
			}
		})
	}
}

func TestFilterRoutesByEndpointPolicyKeepsCompatibleRoute(t *testing.T) {
	routes := []*database.ChannelModel{
		{ChannelID: 1, EndpointPolicy: database.EndpointPolicyNoChatNonStream},
		{ChannelID: 2, EndpointPolicy: database.EndpointPolicyAll},
	}
	filtered, blocked := filterRoutesByEndpointPolicy(routes, "/v1/chat/completions", false)
	if blocked != 1 {
		t.Fatalf("blocked=%d want 1", blocked)
	}
	if len(filtered) != 1 || filtered[0].ChannelID != 2 {
		t.Fatalf("filtered=%+v want only channel 2", filtered)
	}
}
