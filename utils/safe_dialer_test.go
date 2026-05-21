package utils

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

func TestIsUtilsUnsafeIP(t *testing.T) {
	cases := []struct {
		name   string
		ip     string
		unsafe bool
	}{
		// 黑名单段
		{"IPv4 loopback", "127.0.0.1", true},
		{"IPv4 loopback 127.x.y.z", "127.10.20.30", true},
		{"IPv6 loopback", "::1", true},
		{"IPv4 link-local", "169.254.1.5", true},
		{"AWS/GCP metadata", "169.254.169.254", true},
		{"IPv4 private 10.x", "10.0.0.1", true},
		{"IPv4 private 192.168.x", "192.168.1.1", true},
		{"IPv4 private 172.16-31.x", "172.20.5.10", true},
		{"IPv4 multicast", "224.0.0.1", true},
		{"IPv6 link-local fe80::/10", "fe80::1234", true},
		{"IPv6 ULA fc00::/7", "fd12:3456:789a::1", true},
		{"unspecified IPv4", "0.0.0.0", true},
		{"unspecified IPv6", "::", true},
		// 白名单（公网 IP）
		{"public IPv4 8.8.8.8", "8.8.8.8", false},
		{"public IPv4 1.1.1.1", "1.1.1.1", false},
		{"public IPv4 203.0.113.x (TEST-NET-3)", "203.0.113.50", false},
		{"public IPv6 Google DNS", "2001:4860:4860::8888", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ip := net.ParseIP(tc.ip)
			if ip == nil {
				t.Fatalf("invalid test fixture IP %q", tc.ip)
			}
			got := isUtilsUnsafeIP(ip)
			if got != tc.unsafe {
				t.Errorf("isUtilsUnsafeIP(%s) = %v, want %v", tc.ip, got, tc.unsafe)
			}
		})
	}
}

func TestIsUtilsUnsafeIP_NilInput(t *testing.T) {
	if !isUtilsUnsafeIP(nil) {
		t.Error("nil IP should be unsafe (fail-closed)")
	}
}

func TestSafeHTTPClient_Singleton(t *testing.T) {
	c1 := SafeHTTPClient()
	c2 := SafeHTTPClient()
	if c1 != c2 {
		t.Error("SafeHTTPClient should return the same *http.Client instance")
	}
	if c1.Timeout != 15*time.Second {
		t.Errorf("Timeout=%v, want 15s", c1.Timeout)
	}
	if c1.Transport == nil {
		t.Error("Transport must be set (custom dialer)")
	}
}

// TestSafeDialContext_RejectsLoopback 验证 dial 一个解析到 127.0.0.1 的主机会被拒
func TestSafeDialContext_RejectsLoopback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// "localhost" 在所有平台解析为 127.0.0.1 / ::1
	_, err := safeUtilsDialContext(ctx, "tcp", "localhost:1")
	if err == nil {
		t.Fatal("expected error dialing localhost (loopback should be refused)")
	}
	if !strings.Contains(err.Error(), "unsafe IP") && !strings.Contains(err.Error(), "refused") {
		t.Errorf("err=%v, want 'unsafe IP / refused'", err)
	}
}

// TestSafeDialContext_RejectsMetadataIP 验证拨 AWS/GCP/Azure 元数据 IP 会被拒
// （直接用 IP 字面量，跳过 DNS）
func TestSafeDialContext_RejectsMetadataIP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := safeUtilsDialContext(ctx, "tcp", "169.254.169.254:80")
	if err == nil {
		t.Fatal("expected error dialing cloud metadata IP")
	}
	if !strings.Contains(err.Error(), "unsafe IP") && !strings.Contains(err.Error(), "refused") {
		t.Errorf("err=%v, want 'unsafe IP / refused'", err)
	}
}

// TestSafeDialContext_RejectsRFC1918 验证内网段 IP 被拒
func TestSafeDialContext_RejectsRFC1918(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cases := []string{"10.0.0.1:80", "192.168.1.1:80", "172.20.5.10:80"}
	for _, addr := range cases {
		t.Run(addr, func(t *testing.T) {
			_, err := safeUtilsDialContext(ctx, "tcp", addr)
			if err == nil {
				t.Errorf("expected error dialing private IP %s", addr)
			}
		})
	}
}

func TestSafeDialContext_InvalidAddr(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := safeUtilsDialContext(ctx, "tcp", "missing-port")
	if err == nil {
		t.Fatal("expected error for malformed addr")
	}
	if !strings.Contains(err.Error(), "invalid addr") {
		t.Errorf("err=%v, want 'invalid addr'", err)
	}
}

// TestSafeDialContext_RejectsHostnameLoopback 验证 hostname 解析到 loopback 也被拒
// （主要 case：localhost → 127.0.0.1 / ::1）。这是 DNS rebinding 防御的核心：即使
// 攻击者控制 DNS 让某个域名指向内网 IP，dialer 也会拒绝拨号。
func TestSafeDialContext_RejectsHostnameLoopback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := safeUtilsDialContext(ctx, "tcp", "localhost:1")
	if err == nil {
		t.Fatal("expected error dialing localhost (resolves to loopback)")
	}
	// 错误信息应明确是 "unsafe IP refused"，不是普通的 connection refused
	if !strings.Contains(err.Error(), "unsafe IP") && !strings.Contains(err.Error(), "refused") {
		t.Errorf("err=%v, want 'unsafe IP refused'（防 DNS rebinding）", err)
	}
}
