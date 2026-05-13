package controller

import "testing"

func TestSuggestUsernameFromOAuthName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "github hyphen", in: "354685856-sn", want: "354685856_sn"},
		{name: "trim invalid edges", in: "--alice--", want: "alice"},
		{name: "fallback empty", in: "---", want: "user"},
		{name: "keep han", in: "测试-user", want: "测试_user"},
		{name: "limit runes", in: "abcdefghijklmnopqrstuvwxyz", want: "abcdefghijklmnopqrst"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := suggestUsernameFromOAuthName(tt.in); got != tt.want {
				t.Fatalf("suggestUsernameFromOAuthName(%q)=%q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
