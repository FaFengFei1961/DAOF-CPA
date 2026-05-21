// Package controller / payment_provider_epusdt_test.go
//
// Phase W-3-P2（2026-05-21）：epusdt PaymentProvider 单元测试。
//
// 覆盖：
//   1. 签名算法 SignEpusdtMD5 边界（与上游 sign.Get 一致）
//   2. 未配齐 → ErrPaymentProviderNotConfigured / ErrWebhookProviderNotConfigured
//   3. CreateOrder：mock epusdt sidecar 返成功响应
//   4. CreateOrder：mock 返 5xx → ErrPaymentUpstreamUnavailable
//   5. ParseAndVerifyWebhook：合法 paid 回调
//   6. ParseAndVerifyWebhook：签名错误 / pid 错误 / 状态非终态
//   7. PublicOptions：methods 跟随 SysConfig epusdt_enabled_chains
package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"daof-cpa/database"
	"daof-cpa/proxy"
)

// configureEpusdtForTest 注入 SysConfig 让 IsConfigured 返 true，并把 endpoint 指向 mock server。
func configureEpusdtForTest(t *testing.T, endpoint, pid, secret string) {
	t.Helper()
	proxy.SysConfigMutex.Lock()
	old := proxy.SysConfigCache
	cfg := map[string]string{
		"epusdt_endpoint":       endpoint,
		"epusdt_pid":            pid,
		"epusdt_secret_key":     secret,
		"epusdt_enabled_chains": "tron,ethereum,bsc,polygon",
	}
	proxy.SysConfigCache = cfg
	proxy.SysConfigMutex.Unlock()
	t.Cleanup(func() {
		proxy.SysConfigMutex.Lock()
		proxy.SysConfigCache = old
		proxy.SysConfigMutex.Unlock()
	})
}

func clearEpusdtConfigForTest(t *testing.T) {
	t.Helper()
	proxy.SysConfigMutex.Lock()
	old := proxy.SysConfigCache
	proxy.SysConfigCache = map[string]string{}
	proxy.SysConfigMutex.Unlock()
	t.Cleanup(func() {
		proxy.SysConfigMutex.Lock()
		proxy.SysConfigCache = old
		proxy.SysConfigMutex.Unlock()
	})
}

// ─── 签名算法 ──────────────────────────────────────────────────

func TestSignEpusdtMD5_StableOrdering(t *testing.T) {
	// 同样字段不同 map 插入顺序应得到相同签名（依赖 sort.Strings 字典序）
	a := proxy.SignEpusdtMD5(map[string]any{
		"order_id": "x",
		"amount":   10.0,
		"pid":      int64(1),
	}, "secret")
	b := proxy.SignEpusdtMD5(map[string]any{
		"pid":      int64(1),
		"amount":   10.0,
		"order_id": "x",
	}, "secret")
	if a != b {
		t.Errorf("sign not deterministic: a=%s b=%s", a, b)
	}
	if len(a) != 32 {
		t.Errorf("sign should be 32 hex chars (MD5), got %d", len(a))
	}
}

func TestSignEpusdtMD5_SkipsEmpty(t *testing.T) {
	// 空字符串 / nil 应被跳过；与上游 sign.Get 一致
	a := proxy.SignEpusdtMD5(map[string]any{
		"order_id": "x",
		"amount":   10.0,
		"name":     "",  // 跳过
		"note":     nil, // 跳过
		"pid":      int64(1),
	}, "secret")
	b := proxy.SignEpusdtMD5(map[string]any{
		"order_id": "x",
		"amount":   10.0,
		"pid":      int64(1),
	}, "secret")
	if a != b {
		t.Errorf("empty/nil should be skipped: a=%s b=%s", a, b)
	}
}

func TestSignEpusdtMD5_DependentOnSecretKey(t *testing.T) {
	a := proxy.SignEpusdtMD5(map[string]any{"x": "1"}, "secret-a")
	b := proxy.SignEpusdtMD5(map[string]any{"x": "1"}, "secret-b")
	if a == b {
		t.Errorf("sign should differ for different secrets: %s == %s", a, b)
	}
}

func TestVerifyEpusdtSignature_RejectsMismatch(t *testing.T) {
	payload := map[string]any{"x": "1", "y": int64(2)}
	sig := proxy.SignEpusdtMD5(payload, "secret")
	if !proxy.VerifyEpusdtSignature(payload, sig, "secret") {
		t.Error("VerifyEpusdtSignature should accept its own output")
	}
	if proxy.VerifyEpusdtSignature(payload, sig, "wrong-secret") {
		t.Error("VerifyEpusdtSignature should reject wrong secret")
	}
	if proxy.VerifyEpusdtSignature(payload, "deadbeef-not-the-sig", "secret") {
		t.Error("VerifyEpusdtSignature should reject wrong signature")
	}
}

// ─── adapter 接口 ──────────────────────────────────────────────

func TestEpusdtPaymentProvider_Key(t *testing.T) {
	p := NewEpusdtPaymentProvider()
	if p.Key() != database.TopupProviderEpusdt {
		t.Errorf("Key=%q, want epusdt", p.Key())
	}
}

func TestEpusdtPaymentProvider_IsConfigured_FalseWhenMissing(t *testing.T) {
	clearEpusdtConfigForTest(t)
	p := NewEpusdtPaymentProvider()
	if p.IsConfigured() {
		t.Error("IsConfigured should be false when SysConfig empty")
	}
}

func TestEpusdtPaymentProvider_CreateOrder_RejectsWhenNotConfigured(t *testing.T) {
	clearEpusdtConfigForTest(t)
	p := NewEpusdtPaymentProvider()
	_, err := p.CreateOrder(context.Background(), &PaymentCreateOrderRequest{
		OutTradeNo:     "tp_epusdt_test",
		AmountUSDMicro: 10_000_000, // 10 USDT
		RawExtras:      map[string]string{"method": "trc20-usdt"},
	})
	if !errors.Is(err, ErrPaymentProviderNotConfigured) {
		t.Errorf("err=%v, want ErrPaymentProviderNotConfigured", err)
	}
}

func TestEpusdtPaymentProvider_CreateOrder_RejectsNilRequest(t *testing.T) {
	p := NewEpusdtPaymentProvider()
	_, err := p.CreateOrder(context.Background(), nil)
	if !errors.Is(err, ErrPaymentProviderInternal) {
		t.Errorf("err=%v, want ErrPaymentProviderInternal", err)
	}
}

func TestEpusdtPaymentProvider_CreateOrder_RejectsInvalidMethod(t *testing.T) {
	configureEpusdtForTest(t, "http://localhost:65535", "1", "secret")
	p := NewEpusdtPaymentProvider()
	_, err := p.CreateOrder(context.Background(), &PaymentCreateOrderRequest{
		OutTradeNo:     "tp_epusdt_bad_method",
		AmountUSDMicro: 10_000_000,
		RawExtras:      map[string]string{"method": "doge-usdt-fake"},
	})
	if !errors.Is(err, ErrPaymentProviderInternal) {
		t.Errorf("err=%v, want ErrPaymentProviderInternal for invalid method", err)
	}
}

func TestEpusdtPaymentProvider_CreateOrder_Success(t *testing.T) {
	// Mock epusdt sidecar
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/order/create-transaction" {
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		if r.Method != "POST" {
			t.Errorf("method=%s, want POST", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status_code": 200,
			"message": "ok",
			"data": {
				"trade_id": "EP123456",
				"order_id": "tp_epusdt_success",
				"amount": 10.0,
				"actual_amount": 10.0001,
				"receive_address": "TMBjEGgFAPMt6DxDPKqcxsAQvWMAua8gHk",
				"token": "usdt",
				"expiration_time": 9999999999,
				"payment_url": "http://localhost:8000/cashier/EP123456"
			}
		}`))
	}))
	defer server.Close()

	configureEpusdtForTest(t, server.URL, "1", "secret")
	p := NewEpusdtPaymentProvider()
	result, err := p.CreateOrder(context.Background(), &PaymentCreateOrderRequest{
		OutTradeNo:     "tp_epusdt_success",
		AmountUSDMicro: 10_000_000,
		ProductName:    "DAOF Test",
		NotifyURL:      "http://daof.test/api/payment/notify/epusdt",
		RawExtras:      map[string]string{"method": "trc20-usdt"},
	})
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if result.ExternalTradeNo != "EP123456" {
		t.Errorf("ExternalTradeNo=%q", result.ExternalTradeNo)
	}
	if result.GatewayPayType != "wallet_address" {
		t.Errorf("GatewayPayType=%q, want wallet_address", result.GatewayPayType)
	}
	// PayInfo 应是包含 receive_address 的 JSON
	var payInfo map[string]any
	if err := json.Unmarshal([]byte(result.PayInfo), &payInfo); err != nil {
		t.Fatalf("PayInfo not valid JSON: %v", err)
	}
	if payInfo["receive_address"] != "TMBjEGgFAPMt6DxDPKqcxsAQvWMAua8gHk" {
		t.Errorf("PayInfo.receive_address=%v", payInfo["receive_address"])
	}
}

func TestEpusdtPaymentProvider_CreateOrder_GatewayRejects(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"status_code":500,"message":"upstream error"}`))
	}))
	defer server.Close()

	configureEpusdtForTest(t, server.URL, "1", "secret")
	p := NewEpusdtPaymentProvider()
	_, err := p.CreateOrder(context.Background(), &PaymentCreateOrderRequest{
		OutTradeNo:     "tp_epusdt_5xx",
		AmountUSDMicro: 10_000_000,
		RawExtras:      map[string]string{"method": "trc20-usdt"},
	})
	if !errors.Is(err, ErrPaymentUpstreamUnavailable) {
		t.Errorf("err=%v, want ErrPaymentUpstreamUnavailable for 5xx", err)
	}
}

// ─── PublicOptions ──────────────────────────────────────────────

func TestEpusdtPaymentProvider_PublicOptions_FollowsEnabledChains(t *testing.T) {
	configureEpusdtForTest(t, "http://localhost:8000", "1", "secret")
	p := NewEpusdtPaymentProvider()
	opts := p.PublicOptions()

	if opts.Key != database.TopupProviderEpusdt {
		t.Errorf("Key=%q", opts.Key)
	}
	if opts.Currency != "USDT" {
		t.Errorf("Currency=%q, want USDT", opts.Currency)
	}
	if !opts.Configured {
		t.Error("Configured should be true with full SysConfig")
	}
	// 默认 epusdt_enabled_chains = "tron,ethereum,bsc,polygon" → 4 methods
	if len(opts.Methods) != 4 {
		t.Errorf("Methods=%v, want 4 methods (trc20/erc20/bep20/polygon)", opts.Methods)
	}
	expected := map[string]bool{
		"trc20-usdt": false, "erc20-usdt": false,
		"bep20-usdt": false, "polygon-usdt": false,
	}
	for _, m := range opts.Methods {
		if _, ok := expected[m]; ok {
			expected[m] = true
		}
	}
	for k, found := range expected {
		if !found {
			t.Errorf("Methods missing %q: got %v", k, opts.Methods)
		}
	}
}

func TestEpusdtPaymentProvider_PublicOptions_TronOnly(t *testing.T) {
	configureEpusdtForTest(t, "http://localhost:8000", "1", "secret")
	// 覆盖默认全开为仅 tron
	proxy.SysConfigMutex.Lock()
	proxy.SysConfigCache["epusdt_enabled_chains"] = "tron"
	proxy.SysConfigMutex.Unlock()

	p := NewEpusdtPaymentProvider()
	opts := p.PublicOptions()
	if len(opts.Methods) != 1 || opts.Methods[0] != "trc20-usdt" {
		t.Errorf("Methods=%v, want [trc20-usdt]", opts.Methods)
	}
}

// ─── webhook 验签 ───────────────────────────────────────────────

// buildEpusdtWebhookBody 构造一个合法 epusdt POST JSON 回调 body。
func buildEpusdtWebhookBody(t *testing.T, secret string, status int, orderID string) []byte {
	t.Helper()
	payload := map[string]any{
		"pid":                  int64(1),
		"trade_id":             "EP" + orderID,
		"order_id":             orderID,
		"amount":               10.0,
		"actual_amount":        10.0001,
		"receive_address":      "TMBjEGgFAPMt6DxDPKqcxsAQvWMAua8gHk",
		"token":                "usdt",
		"block_transaction_id": "0xabcdef1234567890",
		"status":               status,
	}
	payload["signature"] = proxy.SignEpusdtMD5(payload, secret)
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestEpusdtWebhook_ValidPaidCallback(t *testing.T) {
	configureEpusdtForTest(t, "http://localhost:8000", "1", "secret")
	body := buildEpusdtWebhookBody(t, "secret", 2, "tp_epusdt_paid") // status=2 = PaySuccess

	p := NewEpusdtPaymentProvider()
	event, err := p.ParseAndVerifyWebhook(&PaymentWebhookInput{
		Method: "POST",
		Body:   body,
	})
	if err != nil {
		t.Fatalf("ParseAndVerifyWebhook: %v", err)
	}
	if event.Kind != WebhookEventPaid {
		t.Errorf("Kind=%q, want paid", event.Kind)
	}
	if event.OutTradeNo != "tp_epusdt_paid" {
		t.Errorf("OutTradeNo=%q", event.OutTradeNo)
	}
	if event.ExternalTradeNo != "EPtp_epusdt_paid" {
		t.Errorf("ExternalTradeNo=%q", event.ExternalTradeNo)
	}
	if event.AmountKind != AmountKindMicroUSD {
		t.Errorf("AmountKind=%q, want micro_usd", event.AmountKind)
	}
	if event.AmountRaw != 10_000_000 {
		t.Errorf("AmountRaw=%d, want 10_000_000 (10 USDT = 10M micro_usd)", event.AmountRaw)
	}
	// W-3 review H-4 修复后：nonce 用 trade_id（签名覆盖，攻击者改不了）而非 block_tx_id
	if event.Nonce != "epusdt:tp_epusdt_paid:EPtp_epusdt_paid" {
		t.Errorf("Nonce=%q, want epusdt:<order_id>:<trade_id>", event.Nonce)
	}
}

// W-3 review H-4 测试：攻击者篡改 block_transaction_id 不影响 nonce
func TestEpusdtWebhook_BlockTxIDIgnoredInNonce(t *testing.T) {
	configureEpusdtForTest(t, "http://localhost:8000", "1", "secret")
	// 同一 trade_id，但不同 block_tx_id → nonce 必须相同（防绕过重放）
	payload1 := map[string]any{
		"pid":                  int64(1),
		"trade_id":             "EP-same",
		"order_id":             "tp_same_order",
		"amount":               10.0,
		"actual_amount":        10.0001,
		"receive_address":      "T...",
		"token":                "usdt",
		"block_transaction_id": "0x111",
		"status":               2,
	}
	payload1["signature"] = proxy.SignEpusdtMD5(payload1, "secret")
	body1, _ := json.Marshal(payload1)

	payload2 := map[string]any{
		"pid":                  int64(1),
		"trade_id":             "EP-same",
		"order_id":             "tp_same_order",
		"amount":               10.0,
		"actual_amount":        10.0001,
		"receive_address":      "T...",
		"token":                "usdt",
		"block_transaction_id": "0x222", // 不同！
		"status":               2,
	}
	payload2["signature"] = proxy.SignEpusdtMD5(payload2, "secret")
	body2, _ := json.Marshal(payload2)

	p := NewEpusdtPaymentProvider()
	event1, err := p.ParseAndVerifyWebhook(&PaymentWebhookInput{Body: body1})
	if err != nil {
		t.Fatalf("first event: %v", err)
	}
	event2, err := p.ParseAndVerifyWebhook(&PaymentWebhookInput{Body: body2})
	if err != nil {
		t.Fatalf("second event: %v", err)
	}
	if event1.Nonce != event2.Nonce {
		t.Errorf("nonce should not depend on block_tx_id: e1=%s e2=%s", event1.Nonce, event2.Nonce)
	}
}

func TestEpusdtWebhook_NonTerminalStatus(t *testing.T) {
	configureEpusdtForTest(t, "http://localhost:8000", "1", "secret")
	body := buildEpusdtWebhookBody(t, "secret", 1, "tp_epusdt_pending") // status=1 = pending

	p := NewEpusdtPaymentProvider()
	event, err := p.ParseAndVerifyWebhook(&PaymentWebhookInput{Body: body})
	if err != nil {
		t.Fatalf("ParseAndVerifyWebhook: %v", err)
	}
	if event.Kind != WebhookEventNonTerminal {
		t.Errorf("Kind=%q, want non_terminal", event.Kind)
	}
}

func TestEpusdtWebhook_ExpiredStatus(t *testing.T) {
	configureEpusdtForTest(t, "http://localhost:8000", "1", "secret")
	body := buildEpusdtWebhookBody(t, "secret", 3, "tp_epusdt_expired") // status=3 = PayExpired

	p := NewEpusdtPaymentProvider()
	event, err := p.ParseAndVerifyWebhook(&PaymentWebhookInput{Body: body})
	if err != nil {
		t.Fatalf("ParseAndVerifyWebhook: %v", err)
	}
	if event.Kind != WebhookEventFailed {
		t.Errorf("Kind=%q, want failed", event.Kind)
	}
}

func TestEpusdtWebhook_BadSignatureRejected(t *testing.T) {
	configureEpusdtForTest(t, "http://localhost:8000", "1", "secret")
	// 用错误的 secret 签出来的 body —— DAOF 验签应该拒
	body := buildEpusdtWebhookBody(t, "wrong-secret", 2, "tp_epusdt_bad_sig")

	p := NewEpusdtPaymentProvider()
	_, err := p.ParseAndVerifyWebhook(&PaymentWebhookInput{Body: body})
	if !errors.Is(err, ErrWebhookSignatureInvalid) {
		t.Errorf("err=%v, want ErrWebhookSignatureInvalid", err)
	}
}

func TestEpusdtWebhook_PIDMismatchRejected(t *testing.T) {
	configureEpusdtForTest(t, "http://localhost:8000", "1", "secret") // DAOF pid=1
	// 攻击者用自己的 epusdt 商户（pid=2）拿到合法签名的回调投递过来
	payload := map[string]any{
		"pid":                  int64(2), // 不是我们的 pid
		"trade_id":             "EPx",
		"order_id":             "tp_pid_mismatch",
		"amount":               10.0,
		"actual_amount":        10.0001,
		"receive_address":      "T...",
		"token":                "usdt",
		"block_transaction_id": "0x...",
		"status":               2,
	}
	payload["signature"] = proxy.SignEpusdtMD5(payload, "secret") // 签名合法
	body, _ := json.Marshal(payload)

	p := NewEpusdtPaymentProvider()
	_, err := p.ParseAndVerifyWebhook(&PaymentWebhookInput{Body: body})
	if !errors.Is(err, ErrWebhookPIDMismatch) {
		t.Errorf("err=%v, want ErrWebhookPIDMismatch", err)
	}
}

func TestEpusdtWebhook_NotConfigured(t *testing.T) {
	clearEpusdtConfigForTest(t)
	p := NewEpusdtPaymentProvider()
	_, err := p.ParseAndVerifyWebhook(&PaymentWebhookInput{Body: []byte(`{"x":"1"}`)})
	if !errors.Is(err, ErrWebhookProviderNotConfigured) {
		t.Errorf("err=%v, want ErrWebhookProviderNotConfigured", err)
	}
}

func TestEpusdtWebhook_EmptyBodyRejected(t *testing.T) {
	configureEpusdtForTest(t, "http://localhost:8000", "1", "secret")
	p := NewEpusdtPaymentProvider()
	_, err := p.ParseAndVerifyWebhook(&PaymentWebhookInput{Body: nil})
	if !errors.Is(err, ErrWebhookMalformed) {
		t.Errorf("err=%v, want ErrWebhookMalformed for nil body", err)
	}
}

func TestEpusdtWebhook_InvalidJSONRejected(t *testing.T) {
	configureEpusdtForTest(t, "http://localhost:8000", "1", "secret")
	p := NewEpusdtPaymentProvider()
	_, err := p.ParseAndVerifyWebhook(&PaymentWebhookInput{Body: []byte(`{not-json`)})
	if !errors.Is(err, ErrWebhookMalformed) {
		t.Errorf("err=%v, want ErrWebhookMalformed for invalid JSON", err)
	}
}

func TestEpusdtWebhook_MissingSignatureRejected(t *testing.T) {
	configureEpusdtForTest(t, "http://localhost:8000", "1", "secret")
	body := []byte(`{"pid":1,"order_id":"x","amount":10}`)
	p := NewEpusdtPaymentProvider()
	_, err := p.ParseAndVerifyWebhook(&PaymentWebhookInput{Body: body})
	if !errors.Is(err, ErrWebhookMalformed) {
		t.Errorf("err=%v, want ErrWebhookMalformed for missing signature", err)
	}
}

func TestEpusdtWebhook_NilInputRejected(t *testing.T) {
	p := NewEpusdtPaymentProvider()
	_, err := p.ParseAndVerifyWebhook(nil)
	if !errors.Is(err, ErrWebhookMalformed) {
		t.Errorf("err=%v, want ErrWebhookMalformed for nil input", err)
	}
}

// W-3 review H-2 / H-7 / H-8 测试：显式拒 NaN/Inf/缺字段/错类型
func TestEpusdtWebhook_MissingPID(t *testing.T) {
	configureEpusdtForTest(t, "http://localhost:8000", "1", "secret")
	payload := map[string]any{
		"order_id":      "tp_no_pid",
		"trade_id":      "EPno_pid",
		"amount":        10.0,
		"actual_amount": 10.0001,
		"token":         "usdt",
		"status":        2,
	}
	payload["signature"] = proxy.SignEpusdtMD5(payload, "secret")
	body, _ := json.Marshal(payload)

	p := NewEpusdtPaymentProvider()
	_, err := p.ParseAndVerifyWebhook(&PaymentWebhookInput{Body: body})
	if !errors.Is(err, ErrWebhookMalformed) {
		t.Errorf("err=%v, want ErrWebhookMalformed for missing pid", err)
	}
}

func TestEpusdtWebhook_PIDAsStringRejected(t *testing.T) {
	configureEpusdtForTest(t, "http://localhost:8000", "1", "secret")
	// epusdt 协议变种把 pid 当字符串发送 → 我方应该显式拒（malformed）而非误判 pid_mismatch
	payload := map[string]any{
		"pid":           "1", // 字符串而非数字
		"order_id":      "tp_str_pid",
		"trade_id":      "EPstr_pid",
		"amount":        10.0,
		"actual_amount": 10.0001,
		"token":         "usdt",
		"status":        2,
	}
	payload["signature"] = proxy.SignEpusdtMD5(payload, "secret")
	body, _ := json.Marshal(payload)

	p := NewEpusdtPaymentProvider()
	_, err := p.ParseAndVerifyWebhook(&PaymentWebhookInput{Body: body})
	if !errors.Is(err, ErrWebhookMalformed) {
		t.Errorf("err=%v, want ErrWebhookMalformed for pid as string", err)
	}
}

func TestEpusdtWebhook_MissingStatusRejected(t *testing.T) {
	configureEpusdtForTest(t, "http://localhost:8000", "1", "secret")
	// 缺 status 字段 → 原代码 silent 当 non_terminal（burn nonce）。修复后应显式拒。
	payload := map[string]any{
		"pid":           int64(1),
		"order_id":      "tp_no_status",
		"trade_id":      "EPno_status",
		"amount":        10.0,
		"actual_amount": 10.0001,
		"token":         "usdt",
	}
	payload["signature"] = proxy.SignEpusdtMD5(payload, "secret")
	body, _ := json.Marshal(payload)

	p := NewEpusdtPaymentProvider()
	_, err := p.ParseAndVerifyWebhook(&PaymentWebhookInput{Body: body})
	if !errors.Is(err, ErrWebhookMalformed) {
		t.Errorf("err=%v, want ErrWebhookMalformed for missing status", err)
	}
}

func TestEpusdtWebhook_MissingTradeID(t *testing.T) {
	configureEpusdtForTest(t, "http://localhost:8000", "1", "secret")
	payload := map[string]any{
		"pid":           int64(1),
		"order_id":      "tp_no_tradeid",
		"amount":        10.0,
		"actual_amount": 10.0001,
		"token":         "usdt",
		"status":        2,
	}
	payload["signature"] = proxy.SignEpusdtMD5(payload, "secret")
	body, _ := json.Marshal(payload)

	p := NewEpusdtPaymentProvider()
	_, err := p.ParseAndVerifyWebhook(&PaymentWebhookInput{Body: body})
	if !errors.Is(err, ErrWebhookMalformed) {
		t.Errorf("err=%v, want ErrWebhookMalformed for missing trade_id", err)
	}
}

// ─── helper 函数 ────────────────────────────────────────────────

func TestParseEpusdtMethod(t *testing.T) {
	cases := []struct {
		method       string
		wantToken    string
		wantNetwork  string
		wantOK       bool
	}{
		{"trc20-usdt", "usdt", "tron", true},
		{"erc20-usdt", "usdt", "ethereum", true},
		{"bep20-usdt", "usdt", "bsc", true},
		{"polygon-usdt", "usdt", "polygon", true},
		{"TRC20-USDT", "usdt", "tron", true}, // 大小写不敏感
		{"  trc20-usdt  ", "usdt", "tron", true},
		{"invalid", "", "", false},
		{"", "", "", false},
		{"sol-usdt", "", "", false}, // Solana 未在一期支持
	}
	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			token, network, ok := parseEpusdtMethod(tc.method)
			if ok != tc.wantOK {
				t.Errorf("ok=%v, want %v", ok, tc.wantOK)
			}
			if token != tc.wantToken {
				t.Errorf("token=%q, want %q", token, tc.wantToken)
			}
			if network != tc.wantNetwork {
				t.Errorf("network=%q, want %q", network, tc.wantNetwork)
			}
		})
	}
}

func TestChainToMethod(t *testing.T) {
	cases := []struct{ chain, want string }{
		{"tron", "trc20-usdt"},
		{"ethereum", "erc20-usdt"},
		{"bsc", "bep20-usdt"},
		{"polygon", "polygon-usdt"},
		{"TRON", "trc20-usdt"},
		{"  tron  ", "trc20-usdt"},
		{"unknown", ""},
		{"", ""},
	}
	for _, tc := range cases {
		t.Run(tc.chain, func(t *testing.T) {
			got := chainToMethod(tc.chain)
			if got != tc.want {
				t.Errorf("chainToMethod(%q)=%q, want %q", tc.chain, got, tc.want)
			}
		})
	}
}

// 编译期 assertion 已搬到 payment_provider_epusdt.go (L-4 修复)，此处去重。
