package utils

import (
	"strings"
	"testing"
)

func TestGenerateRandomToken(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
	}{
		{"sk-daof prefix", "sk-daof"},
		{"sk-daof-root prefix", "sk-daof-root"},
		{"empty prefix", ""},
		{"unicode prefix", "sk-密钥"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tok := GenerateRandomToken(tc.prefix)
			if !strings.HasPrefix(tok, tc.prefix+"-") {
				t.Errorf("token %q should start with %q-", tok, tc.prefix)
			}
			// 32 hex chars after prefix
			payload := strings.TrimPrefix(tok, tc.prefix+"-")
			if len(payload) != 32 {
				t.Errorf("token payload length=%d, want 32 hex chars", len(payload))
			}
		})
	}
}

func TestGenerateRandomToken_Uniqueness(t *testing.T) {
	// 1000 次生成不应有重复（128 位熵）
	seen := make(map[string]bool, 1000)
	for i := 0; i < 1000; i++ {
		tok := GenerateRandomToken("test")
		if seen[tok] {
			t.Fatalf("duplicate token generated: %s", tok)
		}
		seen[tok] = true
	}
}

func TestCheckHash_Bcrypt(t *testing.T) {
	hash := GenerateHash("password123")
	if !CheckHash("password123", hash) {
		t.Error("CheckHash should accept correct password")
	}
	if CheckHash("wrong_password", hash) {
		t.Error("CheckHash should reject wrong password")
	}
	if CheckHash("", hash) {
		t.Error("CheckHash should reject empty password against valid hash")
	}
}

func TestCheckHash_EmptyHash(t *testing.T) {
	if CheckHash("anything", "") {
		t.Error("CheckHash with empty hash should always reject")
	}
}
