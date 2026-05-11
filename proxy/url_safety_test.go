package proxy

import "testing"

func TestValidateChannelURL(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{"empty", "", false},
		{"https public", "https://api.openai.com/v1", false},
		{"http loopback (CPA local)", "http://127.0.0.1:8317", false},
		{"http private (LAN gateway)", "http://192.168.1.10:8080/v1", false},
		{"https with port", "https://api.example.com:8443/v1", false},

		// 拒绝
		{"file scheme", "file:///etc/passwd", true},
		{"gopher scheme", "gopher://1.2.3.4:25/", true},
		{"data scheme", "data:text/plain,hi", true},
		{"javascript scheme", "javascript:alert(1)", true},
		{"jar scheme", "jar:file:/x.jar!/", true},
		{"empty host", "http://", true},
		{"with userinfo", "http://user:pass@evil.com/", true},
		{"control chars", "http://a.com/\nattack", true},
		{"AWS metadata IPv4", "http://169.254.169.254/latest/meta-data/", true},
		{"GCP metadata host", "http://metadata.google.internal/", true},
		{"link-local IP", "http://169.254.10.5/", true}, // also link-local
		{"multicast IP", "http://224.0.0.1/", true},
		{"malformed", "ht!tp://foo", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateChannelURL(tc.raw)
			if tc.wantErr && err == nil {
				t.Errorf("expected error for %q, got nil", tc.raw)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("expected no error for %q, got %v", tc.raw, err)
			}
		})
	}
}
