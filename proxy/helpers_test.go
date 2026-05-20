// Package proxy / helpers_test.go
//
// 单元测试覆盖 yifut_client.go + responses_websocket.go 的纯函数 helper：
//   - isUnsafeIP (yifut_client.go)
//   - verifyYifutResponseTimestamp (yifut_client.go)
//   - truncate (yifut_client.go)
//   - YifutConfig.IsConfigured (yifut_client.go)
//   - isAllowedWSOrigin (responses_websocket.go)
//
// Phase F batch 2（2026-05-19）。
package proxy

import (
	"crypto/rsa"
	"net"
	"strings"
	"testing"
	"time"
)

func TestIsUnsafeIP(t *testing.T) {
	tests := []struct {
		name string
		ip   net.IP
		want bool
	}{
		{"nil IP unsafe", nil, true},
		{"loopback v4 unsafe", net.IPv4(127, 0, 0, 1), true},
		{"private 10.x unsafe", net.IPv4(10, 0, 0, 1), true},
		{"private 192.168 unsafe", net.IPv4(192, 168, 1, 1), true},
		{"private 172.16 unsafe", net.IPv4(172, 16, 0, 1), true},
		{"link-local 169.254 unsafe", net.IPv4(169, 254, 0, 1), true},
		{"metadata 169.254.169.254 unsafe", net.IPv4(169, 254, 169, 254), true},
		{"unspecified 0.0.0.0 unsafe", net.IPv4(0, 0, 0, 0), true},
		{"multicast 224.x unsafe", net.IPv4(224, 0, 0, 1), true},
		{"public 8.8.8.8 safe", net.IPv4(8, 8, 8, 8), false},
		{"public 1.1.1.1 safe", net.IPv4(1, 1, 1, 1), false},
		{"public IPv6 safe", net.ParseIP("2606:4700:4700::1111"), false},
		{"loopback v6 unsafe", net.ParseIP("::1"), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isUnsafeIP(tc.ip)
			if got != tc.want {
				t.Errorf("isUnsafeIP(%v) = %v; want %v", tc.ip, got, tc.want)
			}
		})
	}
}

func TestVerifyYifutResponseTimestamp(t *testing.T) {
	now := time.Now().Unix()

	tests := []struct {
		name      string
		ts        string
		wantError bool
	}{
		{"empty timestamp rejected", "", true},
		{"whitespace rejected", "   ", true},
		{"non-integer rejected", "abc", true},
		{"current time accepted", fmtInt(now), false},
		{"within +300s accepted", fmtInt(now + 200), false},
		{"within -300s accepted", fmtInt(now - 200), false},
		{"+301s rejected", fmtInt(now + 301), true},
		{"-301s rejected", fmtInt(now - 301), true},
		{"way in past rejected", fmtInt(now - 99999), true},
		{"way in future rejected", fmtInt(now + 99999), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := verifyYifutResponseTimestamp(tc.ts)
			if (err != nil) != tc.wantError {
				t.Errorf("verifyYifutResponseTimestamp(%q) err = %v; wantError %v", tc.ts, err, tc.wantError)
			}
		})
	}
}

func fmtInt(n int64) string {
	if n < 0 {
		return "-" + fmtInt(-n)
	}
	if n < 10 {
		return string(rune('0' + n))
	}
	var b strings.Builder
	var digits []byte
	for n > 0 {
		digits = append(digits, byte('0'+(n%10)))
		n /= 10
	}
	for i := len(digits) - 1; i >= 0; i-- {
		b.WriteByte(digits[i])
	}
	return b.String()
}

func TestTruncate_Proxy(t *testing.T) {
	tests := []struct {
		name string
		s    string
		n    int
		want string
	}{
		{"empty unchanged", "", 5, ""},
		{"under n unchanged", "hi", 10, "hi"},
		{"exactly n unchanged", "12345", 5, "12345"},
		{"over n truncates with count", "1234567890", 5, "12345...(5 more)"},
		{"way over n", "abcdefghijklmnop", 3, "abc...(13 more)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := truncate(tc.s, tc.n)
			if got != tc.want {
				t.Errorf("truncate(%q, %d) = %q; want %q", tc.s, tc.n, got, tc.want)
			}
		})
	}
}

func TestYifutConfig_IsConfigured(t *testing.T) {
	dummyPriv := &dummyRSAKey
	dummyPub := &dummyRSAKey.PublicKey

	tests := []struct {
		name string
		cfg  YifutConfig
		want bool
	}{
		{"all empty", YifutConfig{}, false},
		{"missing PID", YifutConfig{Gateway: "g", MerchantPrivateKey: dummyPriv, PlatformPublicKey: dummyPub}, false},
		{"missing Gateway", YifutConfig{PID: "p", MerchantPrivateKey: dummyPriv, PlatformPublicKey: dummyPub}, false},
		{"missing MerchantPrivateKey", YifutConfig{PID: "p", Gateway: "g", PlatformPublicKey: dummyPub}, false},
		{"missing PlatformPublicKey", YifutConfig{PID: "p", Gateway: "g", MerchantPrivateKey: dummyPriv}, false},
		{"all populated", YifutConfig{PID: "p", Gateway: "g", MerchantPrivateKey: dummyPriv, PlatformPublicKey: dummyPub}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.IsConfigured(); got != tc.want {
				t.Errorf("IsConfigured() = %v; want %v", got, tc.want)
			}
		})
	}
}

// dummyRSAKey is a placeholder RSA key for testing IsConfigured pointer checks.
// We never actually sign with it; only the non-nil pointer matters.
var dummyRSAKey = rsa.PrivateKey{}

func TestIsAllowedWSOrigin(t *testing.T) {
	originalFn := GetCORSAllowedOriginsFn
	defer func() { GetCORSAllowedOriginsFn = originalFn }()

	tests := []struct {
		name          string
		allowed       string
		origin        string
		want          bool
	}{
		{"empty allowlist rejects all", "", "http://localhost:3000", false},
		{"whitespace-only allowlist rejects all", "   ", "http://localhost:3000", false},
		{"single origin match", "http://localhost:3000", "http://localhost:3000", true},
		{"comma-separated match", "http://a.com,http://b.com", "http://b.com", true},
		{"whitespace tolerance in list", "http://a.com , http://b.com", "http://b.com", true},
		{"whitespace tolerance in input", "http://a.com", "  http://a.com  ", true},
		{"case insensitive match", "HTTP://A.COM", "http://a.com", true},
		{"different origin rejected", "http://a.com", "http://b.com", false},
		{"empty origin rejected when allowlist not empty", "http://a.com", "", false},
		{"port mismatch rejected", "http://a.com:3000", "http://a.com:4000", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			GetCORSAllowedOriginsFn = func() string { return tc.allowed }
			got := isAllowedWSOrigin(tc.origin)
			if got != tc.want {
				t.Errorf("isAllowedWSOrigin(%q) with allowlist=%q = %v; want %v", tc.origin, tc.allowed, got, tc.want)
			}
		})
	}
}
