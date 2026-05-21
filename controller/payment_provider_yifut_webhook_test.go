// Package controller / payment_provider_yifut_webhook_test.go
//
// Phase W-3-P1（2026-05-21）：YifutPaymentProvider.ParseAndVerifyWebhook 单元测试。
//
// 覆盖：
//   1. 合法回调 + TRADE_SUCCESS → WebhookEventPaid，字段填全
//   2. 合法回调 + 非 SUCCESS → WebhookEventNonTerminal
//   3. 验签失败 → ErrWebhookSignatureInvalid
//   4. pid 不一致 → ErrWebhookPIDMismatch
//   5. timestamp 过期 → ErrWebhookTimestampDrift
//   6. 缺 out_trade_no → ErrWebhookMalformed
//   7. 非法 money 字符串 → ErrWebhookMalformed
//   8. nil input → ErrWebhookMalformed
//   9. 空 QueryParams → ErrWebhookMalformed
//  10. provider 未配齐 → ErrWebhookProviderNotConfigured
//  11. trade_no 超长被截断到 128
package controller

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"daof-cpa/database"
	"daof-cpa/proxy"
)

// buildYifutWebhookParams 构造合法 yifut 回调 params（已签名）。
// 调用方可以再覆盖某个字段制造 negative test。
func buildYifutWebhookParams(t *testing.T, privPEM, pid, outTradeNo, moneyStr, tradeStatus string) map[string]string {
	t.Helper()
	params := map[string]string{
		"pid":          pid,
		"trade_no":     "YF" + outTradeNo,
		"out_trade_no": outTradeNo,
		"type":         "alipay",
		"name":         "test",
		"money":        moneyStr,
		"trade_status": tradeStatus,
		"timestamp":    strconv.FormatInt(time.Now().Unix(), 10),
		"sign_type":    "RSA",
	}
	params["sign"] = signWithPlatformKey(t, params, privPEM)
	return params
}

// clearYifutConfigForWebhookTest 清空 yifut SysConfig 让 IsConfigured 返 false。
func clearYifutConfigForWebhookTest(t *testing.T) {
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

func TestYifutWebhook_ValidPaidCallback(t *testing.T) {
	privPEM, pubPEM := genTestRSAPair(t)
	configureYifutForTest(t, "12345", privPEM, pubPEM)
	params := buildYifutWebhookParams(t, privPEM, "12345", "tp_w3p1_paid", "10.00", "TRADE_SUCCESS")

	p := NewYifutPaymentProvider()
	event, err := p.ParseAndVerifyWebhook(&PaymentWebhookInput{
		Method:      "GET",
		QueryParams: params,
		RemoteIP:    "1.2.3.4",
	})
	if err != nil {
		t.Fatalf("ParseAndVerifyWebhook: %v", err)
	}
	if event.Kind != WebhookEventPaid {
		t.Errorf("Kind=%q, want paid", event.Kind)
	}
	if event.OutTradeNo != "tp_w3p1_paid" {
		t.Errorf("OutTradeNo=%q", event.OutTradeNo)
	}
	if event.ExternalTradeNo != "YFtp_w3p1_paid" {
		t.Errorf("ExternalTradeNo=%q", event.ExternalTradeNo)
	}
	if event.AmountKind != AmountKindFenCNY {
		t.Errorf("AmountKind=%q, want fen_cny", event.AmountKind)
	}
	if event.AmountRaw != 1000 {
		t.Errorf("AmountRaw=%d, want 1000 (10.00 RMB = 1000 fen)", event.AmountRaw)
	}
	if !strings.HasPrefix(event.Nonce, database.TopupProviderYifut+":") {
		t.Errorf("Nonce=%q, should start with provider key", event.Nonce)
	}
	if event.SignatureHash == "" {
		t.Error("SignatureHash empty, want non-empty SHA-256 hex")
	}
}

func TestYifutWebhook_NonSuccessIsNonTerminal(t *testing.T) {
	privPEM, pubPEM := genTestRSAPair(t)
	configureYifutForTest(t, "12345", privPEM, pubPEM)
	params := buildYifutWebhookParams(t, privPEM, "12345", "tp_w3p1_nt", "10.00", "WAIT_BUYER_PAY")

	p := NewYifutPaymentProvider()
	event, err := p.ParseAndVerifyWebhook(&PaymentWebhookInput{QueryParams: params})
	if err != nil {
		t.Fatalf("ParseAndVerifyWebhook: %v", err)
	}
	if event.Kind != WebhookEventNonTerminal {
		t.Errorf("Kind=%q, want non_terminal", event.Kind)
	}
}

func TestYifutWebhook_BadSignatureRejected(t *testing.T) {
	privPEM, pubPEM := genTestRSAPair(t)
	configureYifutForTest(t, "12345", privPEM, pubPEM)
	params := buildYifutWebhookParams(t, privPEM, "12345", "tp_w3p1_bad_sig", "10.00", "TRADE_SUCCESS")
	params["sign"] = "deadbeef-invalid-signature-replacement"

	p := NewYifutPaymentProvider()
	_, err := p.ParseAndVerifyWebhook(&PaymentWebhookInput{QueryParams: params})
	if !errors.Is(err, ErrWebhookSignatureInvalid) {
		t.Errorf("err=%v, want ErrWebhookSignatureInvalid", err)
	}
}

func TestYifutWebhook_PIDMismatchRejected(t *testing.T) {
	// 配置 pid=12345，但回调 pid=99999（攻击者用自家商户拿到合法签名的回调投递过来）
	privPEM, pubPEM := genTestRSAPair(t)
	configureYifutForTest(t, "12345", privPEM, pubPEM)
	params := buildYifutWebhookParams(t, privPEM, "99999", "tp_w3p1_pid_mismatch", "10.00", "TRADE_SUCCESS")

	p := NewYifutPaymentProvider()
	_, err := p.ParseAndVerifyWebhook(&PaymentWebhookInput{QueryParams: params})
	if !errors.Is(err, ErrWebhookPIDMismatch) {
		t.Errorf("err=%v, want ErrWebhookPIDMismatch", err)
	}
}

func TestYifutWebhook_StaleTimestampRejected(t *testing.T) {
	privPEM, pubPEM := genTestRSAPair(t)
	configureYifutForTest(t, "12345", privPEM, pubPEM)
	params := buildYifutWebhookParams(t, privPEM, "12345", "tp_w3p1_stale", "10.00", "TRADE_SUCCESS")
	// 重设 timestamp 到 1 小时前，重新签名
	params["timestamp"] = strconv.FormatInt(time.Now().Add(-1*time.Hour).Unix(), 10)
	delete(params, "sign")
	params["sign"] = signWithPlatformKey(t, params, privPEM)

	p := NewYifutPaymentProvider()
	_, err := p.ParseAndVerifyWebhook(&PaymentWebhookInput{QueryParams: params})
	if !errors.Is(err, ErrWebhookTimestampDrift) {
		t.Errorf("err=%v, want ErrWebhookTimestampDrift", err)
	}
}

func TestYifutWebhook_MissingOutTradeNo(t *testing.T) {
	privPEM, pubPEM := genTestRSAPair(t)
	configureYifutForTest(t, "12345", privPEM, pubPEM)
	params := buildYifutWebhookParams(t, privPEM, "12345", "tp_w3p1_no_otn", "10.00", "TRADE_SUCCESS")
	// 故意把 out_trade_no 抹掉，但保留其它字段 —— 重签名让 RSA 仍能通过
	delete(params, "out_trade_no")
	delete(params, "sign")
	params["sign"] = signWithPlatformKey(t, params, privPEM)

	p := NewYifutPaymentProvider()
	_, err := p.ParseAndVerifyWebhook(&PaymentWebhookInput{QueryParams: params})
	if !errors.Is(err, ErrWebhookMalformed) {
		t.Errorf("err=%v, want ErrWebhookMalformed for missing out_trade_no", err)
	}
}

func TestYifutWebhook_BadMoneyString(t *testing.T) {
	privPEM, pubPEM := genTestRSAPair(t)
	configureYifutForTest(t, "12345", privPEM, pubPEM)
	params := buildYifutWebhookParams(t, privPEM, "12345", "tp_w3p1_bad_money", "10.00", "TRADE_SUCCESS")
	// 改 money 为非法字符串 → 重签名让 RSA 通过 → adapter 解析金额失败
	params["money"] = "not-a-number"
	delete(params, "sign")
	params["sign"] = signWithPlatformKey(t, params, privPEM)

	p := NewYifutPaymentProvider()
	_, err := p.ParseAndVerifyWebhook(&PaymentWebhookInput{QueryParams: params})
	if !errors.Is(err, ErrWebhookMalformed) {
		t.Errorf("err=%v, want ErrWebhookMalformed for bad money string", err)
	}
}

func TestYifutWebhook_NilInputRejected(t *testing.T) {
	p := NewYifutPaymentProvider()
	_, err := p.ParseAndVerifyWebhook(nil)
	if !errors.Is(err, ErrWebhookMalformed) {
		t.Errorf("err=%v, want ErrWebhookMalformed for nil input", err)
	}
}

func TestYifutWebhook_EmptyQueryParamsRejected(t *testing.T) {
	privPEM, pubPEM := genTestRSAPair(t)
	configureYifutForTest(t, "12345", privPEM, pubPEM)

	p := NewYifutPaymentProvider()
	_, err := p.ParseAndVerifyWebhook(&PaymentWebhookInput{QueryParams: map[string]string{}})
	if !errors.Is(err, ErrWebhookMalformed) {
		t.Errorf("err=%v, want ErrWebhookMalformed for empty query params", err)
	}
}

func TestYifutWebhook_NotConfigured(t *testing.T) {
	clearYifutConfigForWebhookTest(t)
	// 即使有 params，IsConfigured=false 应该最先返回 ErrWebhookProviderNotConfigured
	p := NewYifutPaymentProvider()
	_, err := p.ParseAndVerifyWebhook(&PaymentWebhookInput{
		QueryParams: map[string]string{"pid": "x", "out_trade_no": "y", "sign": "z"},
	})
	if !errors.Is(err, ErrWebhookProviderNotConfigured) {
		t.Errorf("err=%v, want ErrWebhookProviderNotConfigured", err)
	}
}

func TestYifutWebhook_LongTradeNoTruncated(t *testing.T) {
	privPEM, pubPEM := genTestRSAPair(t)
	configureYifutForTest(t, "12345", privPEM, pubPEM)
	params := buildYifutWebhookParams(t, privPEM, "12345", "tp_w3p1_long_tn", "10.00", "TRADE_SUCCESS")
	// 网关返回一个超长 trade_no（攻击者可能构造，虽然签名校验过滤大部分场景）
	longTradeNo := strings.Repeat("A", 200)
	params["trade_no"] = longTradeNo
	delete(params, "sign")
	params["sign"] = signWithPlatformKey(t, params, privPEM)

	p := NewYifutPaymentProvider()
	event, err := p.ParseAndVerifyWebhook(&PaymentWebhookInput{QueryParams: params})
	if err != nil {
		t.Fatalf("ParseAndVerifyWebhook: %v", err)
	}
	if len(event.ExternalTradeNo) != 128 {
		t.Errorf("ExternalTradeNo length=%d, want 128 (truncated)", len(event.ExternalTradeNo))
	}
}

// 编译期 assertion 已在 payment_provider_test.go 里做了；这里跑动态测试覆盖接口
// PaymentWebhookEvent 字段的对齐。
func TestYifutWebhook_NonceIncludesProviderPrefix(t *testing.T) {
	privPEM, pubPEM := genTestRSAPair(t)
	configureYifutForTest(t, "12345", privPEM, pubPEM)
	params := buildYifutWebhookParams(t, privPEM, "12345", "tp_w3p1_nonce", "10.00", "TRADE_SUCCESS")

	p := NewYifutPaymentProvider()
	event, _ := p.ParseAndVerifyWebhook(&PaymentWebhookInput{QueryParams: params})
	// nonce = "yifut:out_trade_no:sign[:16]"
	wantPrefix := database.TopupProviderYifut + ":tp_w3p1_nonce:"
	if !strings.HasPrefix(event.Nonce, wantPrefix) {
		t.Errorf("Nonce=%q, want prefix %q", event.Nonce, wantPrefix)
	}
}
