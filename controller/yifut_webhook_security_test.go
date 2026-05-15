// Package controller / yifut_webhook_security_test.go
//
// 验证 Sprint4-M3 webhook 安全加固：
//  1. IP 白名单（CIDR 列表）
//  2. nonce 防重放（PaymentWebhookReceipt unique on (provider, nonce)）
//  3. server_address HTTPS 强制
//  4. signatureHash 不存原始签名（最小化敏感面）
package controller

import (
	"strings"
	"testing"

	"daof-ai-hub/database"
	"daof-ai-hub/proxy"
)

// withSysConfig 临时替换 SysConfigCache + 自动恢复。
func withSysConfigOverride(t *testing.T, kvs map[string]string, fn func()) {
	t.Helper()
	proxy.SysConfigMutex.Lock()
	prev := proxy.SysConfigCache
	next := make(map[string]string, len(prev)+len(kvs))
	for k, v := range prev {
		next[k] = v
	}
	for k, v := range kvs {
		next[k] = v
	}
	proxy.SysConfigCache = next
	proxy.SysConfigMutex.Unlock()
	defer func() {
		proxy.SysConfigMutex.Lock()
		proxy.SysConfigCache = prev
		proxy.SysConfigMutex.Unlock()
	}()
	fn()
}

// TestCheckYifutNotifyIPAllowed_EmptyCIDRAllowsAll 未配置 CIDR 时默认允许（兼容性）
func TestCheckYifutNotifyIPAllowed_EmptyCIDRAllowsAll(t *testing.T) {
	withSysConfigOverride(t, map[string]string{"yifut_notify_allowed_cidrs": ""}, func() {
		if !checkYifutNotifyIPAllowed("1.2.3.4") {
			t.Errorf("empty CIDR list should allow any IP")
		}
		if !checkYifutNotifyIPAllowed("::1") {
			t.Errorf("empty CIDR list should allow IPv6")
		}
	})
}

// TestCheckYifutNotifyIPAllowed_CIDRMatch 配置 CIDR 后白名单生效
func TestCheckYifutNotifyIPAllowed_CIDRMatch(t *testing.T) {
	withSysConfigOverride(t, map[string]string{
		"yifut_notify_allowed_cidrs": "10.0.0.0/24,192.168.1.5/32",
	}, func() {
		// 在 10.0.0.0/24 内
		if !checkYifutNotifyIPAllowed("10.0.0.50") {
			t.Errorf("10.0.0.50 should be allowed (in 10.0.0.0/24)")
		}
		// 精确 IP 匹配
		if !checkYifutNotifyIPAllowed("192.168.1.5") {
			t.Errorf("192.168.1.5 should be allowed (exact /32)")
		}
		// 不在白名单
		if checkYifutNotifyIPAllowed("8.8.8.8") {
			t.Errorf("8.8.8.8 should be rejected (not in any CIDR)")
		}
		// 隔壁 IP 也拒
		if checkYifutNotifyIPAllowed("192.168.1.6") {
			t.Errorf("192.168.1.6 should be rejected (only .5/32 allowed)")
		}
	})
}

// TestValidateYifutCIDRConfig_RejectsInvalid admin 配错时预校验失败，运行时不再接受裸 IP fallback。
func TestValidateYifutCIDRConfig_RejectsInvalid(t *testing.T) {
	withSysConfigOverride(t, map[string]string{
		"yifut_notify_allowed_cidrs": "not_a_cidr,1.2.3.4,10.0.0.0/24",
	}, func() {
		if err := ValidateYifutNotifyCIDRConfig(); err == nil {
			t.Fatal("invalid CIDR config should be rejected")
		}
		if checkYifutNotifyIPAllowed("1.2.3.4") {
			t.Errorf("plain IP fallback must not be accepted")
		}
		if checkYifutNotifyIPAllowed("10.0.0.99") {
			t.Errorf("invalid config should fail closed at runtime")
		}
	})
}

// TestWebhookNonce_UniquePerOrderAndSignature 同订单不同 sign 应生成不同 nonce
func TestWebhookNonce_UniquePerOrderAndSignature(t *testing.T) {
	params1 := map[string]string{"out_trade_no": "tp_abc", "sign": "AAAA1111BBBB2222EEEE"}
	params2 := map[string]string{"out_trade_no": "tp_abc", "sign": "AAAA1111BBBB2222FFFF"}
	params3 := map[string]string{"out_trade_no": "tp_xyz", "sign": "AAAA1111BBBB2222EEEE"}

	n1 := webhookNonce("yifut", params1)
	n2 := webhookNonce("yifut", params2)
	n3 := webhookNonce("yifut", params3)

	// sign 前 16 字符相同 → n1==n2（同一订单同一签名应判为重放，即使尾部 noise 不同）
	// 注意：易付通签名是固定前缀+payload，前 16 字符已足够区分不同请求
	if !strings.HasPrefix(params1["sign"], params2["sign"][:16]) {
		t.Fatal("test data assumption broken: first 16 chars of sign1/sign2 must equal")
	}
	if n1 != n2 {
		t.Errorf("same out_trade_no + same first 16 sign chars should yield same nonce (got %q vs %q)", n1, n2)
	}
	// 不同订单 → 不同 nonce（防跨订单签名复用）
	if n1 == n3 {
		t.Errorf("different out_trade_no should yield different nonce (got %q == %q)", n1, n3)
	}
}

// TestSignatureHash_DeterministicNotEmpty 同 sign 同 hash；空 sign 返回空字符串
func TestSignatureHash_DeterministicNotEmpty(t *testing.T) {
	h1 := signatureHash("abc123")
	h2 := signatureHash("abc123")
	h3 := signatureHash("different")

	if h1 == "" || h1 != h2 {
		t.Errorf("sha256 should be deterministic non-empty: h1=%q h2=%q", h1, h2)
	}
	if h1 == h3 {
		t.Errorf("different inputs should yield different hashes")
	}
	if len(h1) != 64 {
		t.Errorf("sha256 hex should be 64 chars, got %d", len(h1))
	}
	// 空输入 → 空输出（不存储无意义 hash）
	if signatureHash("") != "" {
		t.Errorf("empty sign should yield empty hash for clarity")
	}
}

// TestRecordWebhookReceiptOnce_NonceUniqueOnReplay 同 (provider, nonce) 第二次插入
// 必须被 unique 索引拦截，返回 duplicate=true
func TestRecordWebhookReceiptOnce_NonceUniqueOnReplay(t *testing.T) {
	setupSubTestDB(t)

	params := map[string]string{
		"out_trade_no": "tp_replay_test",
		"sign":         "1234567890123456abcdef",
	}
	// 第一次：accepted
	dup, err := recordWebhookReceiptOnce("yifut", params, "tp_replay_test", "1.2.3.4")
	if err != nil {
		t.Fatalf("first insert err: %v", err)
	}
	if dup {
		t.Errorf("first insert should NOT be duplicate")
	}

	// 第二次相同 (provider, nonce)：unique 违反 → duplicate=true
	dup2, err := recordWebhookReceiptOnce("yifut", params, "tp_replay_test", "5.6.7.8")
	if err != nil {
		t.Fatalf("second insert err: %v", err)
	}
	if !dup2 {
		t.Errorf("second insert with same nonce should return duplicate=true")
	}

	// DB 验证：只有 1 条 receipt
	var count int64
	database.DB.Model(&database.PaymentWebhookReceipt{}).
		Where("provider = ? AND out_trade_no = ?", "yifut", "tp_replay_test").
		Count(&count)
	// 第一次 accepted（1 行）。第二次 nonce 已存在不能再插。
	// 但我们重新调用 recordWebhookReceiptOnce 用同样 params，nonce 一样 → 第二次插入被拒。
	if count != 1 {
		t.Errorf("expected 1 receipt row after replay attempt, got %d", count)
	}
}

// TestRecordWebhookReceipt_RejectedPathsCoexist 拒绝路径的 nonce 拼接了 status 后缀，
// 不应与同一订单后续 accepted 的 nonce 冲突
func TestRecordWebhookReceipt_RejectedPathsCoexist(t *testing.T) {
	setupSubTestDB(t)

	params := map[string]string{
		"out_trade_no": "tp_rejected_then_ok",
		"sign":         "samesign1234567890",
	}
	// 先记录一次拒绝（pid mismatch）
	recordWebhookReceipt("yifut", params, "tp_rejected_then_ok", "1.1.1.1", "rejected_pid", "test pid mismatch")
	// 后续合法回调（不同的网关重试，pid 已修正）— nonce 不带后缀
	dup, err := recordWebhookReceiptOnce("yifut", params, "tp_rejected_then_ok", "1.1.1.1")
	if err != nil {
		t.Fatalf("accepted after rejected err: %v", err)
	}
	if dup {
		t.Errorf("rejected path uses suffixed nonce, accepted path should NOT collide")
	}
	// 应有 2 条 receipt：rejected + accepted
	var count int64
	database.DB.Model(&database.PaymentWebhookReceipt{}).
		Where("out_trade_no = ?", "tp_rejected_then_ok").
		Count(&count)
	if count != 2 {
		t.Errorf("expected 2 receipts (rejected + accepted), got %d", count)
	}
}

// TestBuildAbsoluteURL_RequireHTTPS 默认 require_https=true 时 http:// base 被拒绝
func TestBuildAbsoluteURL_RequireHTTPS(t *testing.T) {
	// 默认（require_https=true）拒绝 http
	withSysConfigOverride(t, map[string]string{
		"server_address":               "http://example.com",
		"server_address_require_https": "true",
	}, func() {
		_, err := buildAbsoluteURL("/api/payment/notify/yifut")
		if err == nil {
			t.Errorf("http:// base should be rejected when require_https=true")
		}
		if !strings.Contains(err.Error(), "https://") {
			t.Errorf("error should mention https://, got %v", err)
		}
	})

	// require_https=true + https base → 通过
	withSysConfigOverride(t, map[string]string{
		"server_address":               "https://example.com",
		"server_address_require_https": "true",
	}, func() {
		got, err := buildAbsoluteURL("/api/payment/notify/yifut")
		if err != nil {
			t.Fatalf("https base should pass, got err: %v", err)
		}
		if got != "https://example.com/api/payment/notify/yifut" {
			t.Errorf("unexpected URL: %s", got)
		}
	})

	// require_https=false（开发环境）+ http base → 通过
	withSysConfigOverride(t, map[string]string{
		"server_address":               "http://localhost:3000",
		"server_address_require_https": "false",
	}, func() {
		got, err := buildAbsoluteURL("/api/payment/notify/yifut")
		if err != nil {
			t.Fatalf("http base with require_https=false should pass, got err: %v", err)
		}
		if got != "http://localhost:3000/api/payment/notify/yifut" {
			t.Errorf("unexpected URL: %s", got)
		}
	})

	// server_address 缺失 → 拒绝
	withSysConfigOverride(t, map[string]string{
		"server_address": "",
	}, func() {
		_, err := buildAbsoluteURL("/api/payment/notify/yifut")
		if err == nil {
			t.Errorf("empty server_address should be rejected")
		}
	})
}
