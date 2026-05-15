package proxy

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"testing"
	"time"
)

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
		{"Azure wireserver IP", "http://168.63.129.16/", true},
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

func TestRedirectGuard_BlocksMetadata(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "http://169.254.169.254/latest/meta-data/", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if err := redirectGuard(req, nil); err == nil {
		t.Fatal("expected metadata redirect target to be blocked")
	}
}

func TestRedirectGuard_BlocksCrossHost(t *testing.T) {
	prev, err := http.NewRequest(http.MethodGet, "https://api.example.com/v1/models", nil)
	if err != nil {
		t.Fatalf("build previous request: %v", err)
	}
	req, err := http.NewRequest(http.MethodGet, "https://other.example.com/v1/models", nil)
	if err != nil {
		t.Fatalf("build redirect request: %v", err)
	}
	if err := redirectGuard(req, []*http.Request{prev}); err == nil {
		t.Fatal("expected cross-host redirect to be blocked")
	}
}

func TestYifutSSRF_AzureWireserver(t *testing.T) {
	if !isUnsafeYifutIP(netip.MustParseAddr("168.63.129.16")) {
		t.Fatalf("Azure Wireserver should be denied")
	}
	if err := ValidateGateway("https://168.63.129.16"); err == nil {
		t.Fatalf("ValidateGateway should reject Azure Wireserver")
	}
}

func TestYifutSSRF_CGNAT(t *testing.T) {
	if !isUnsafeYifutIP(netip.MustParseAddr("100.64.0.1")) {
		t.Fatalf("CGNAT should be denied")
	}
	if err := ValidateGateway("https://100.64.0.1"); err == nil {
		t.Fatalf("ValidateGateway should reject CGNAT")
	}
}

func TestYifutSSRF_6to4(t *testing.T) {
	if !isUnsafeYifutIP(netip.MustParseAddr("2002::1")) {
		t.Fatalf("IPv6 6to4 should be denied")
	}
	if err := ValidateGateway("https://[2002::1]"); err == nil {
		t.Fatalf("ValidateGateway should reject IPv6 6to4")
	}
}

func TestSafeDialContext_HappyEyeballs(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	accepted := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			conn.Close()
			close(accepted)
		}
	}()

	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("split listener addr: %v", err)
	}
	oldLookup := lookupIPAddr
	lookupIPAddr = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		return []net.IPAddr{
			{IP: net.ParseIP("127.0.0.2")},
			{IP: net.ParseIP("127.0.0.1")},
		}, nil
	}
	defer func() { lookupIPAddr = oldLookup }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := safeDialContext(ctx, "tcp", net.JoinHostPort("happy.test", port))
	if err != nil {
		t.Fatalf("safeDialContext should fall through to second prevalidated IP: %v", err)
	}
	conn.Close()

	select {
	case <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatalf("listener did not accept connection")
	}
}
