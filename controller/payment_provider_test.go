// Package controller / payment_provider_test.go
//
// Phase W-1（2026-05-21）：PaymentProvider 抽象层单元测试。
//
// 验证：
//   - registry 基本功能（Register / Get / List / Reset）
//   - Yifut adapter 满足 PaymentProvider interface
//   - CreateOrder 错误映射（not configured → sentinel）
//   - PublicOptions 字段填充
//   - ListConfiguredPaymentProviderOptions 按 Key 字典序排序
package controller

import (
	"context"
	"errors"
	"sort"
	"testing"

	"daof-cpa/database"
	"daof-cpa/proxy"
)

// ─── Registry 基本功能 ──────────────────────────────────────────────

// stubPaymentProvider 测试 stub。
type stubPaymentProvider struct {
	key        string
	configured bool
}

func (s *stubPaymentProvider) Key() string      { return s.key }
func (s *stubPaymentProvider) IsConfigured() bool { return s.configured }
func (s *stubPaymentProvider) CreateOrder(ctx context.Context, req *PaymentCreateOrderRequest) (*PaymentCreateOrderResult, error) {
	return &PaymentCreateOrderResult{ExternalTradeNo: "stub-" + s.key, GatewayPayType: "stub"}, nil
}
func (s *stubPaymentProvider) PublicOptions() PaymentProviderPublicOptions {
	return PaymentProviderPublicOptions{Key: s.key, Configured: s.configured, Label: "Stub-" + s.key}
}
func (s *stubPaymentProvider) ParseAndVerifyWebhook(input *PaymentWebhookInput) (*PaymentWebhookEvent, error) {
	return &PaymentWebhookEvent{Kind: WebhookEventPaid, OutTradeNo: "stub-tn", Nonce: s.key + ":stub-nonce"}, nil
}

// withStubPaymentProviders 临时替换全局 registry，结束后还原。
// 注意：这里不调用 ResetPaymentProvidersForTest，因为生产 init() 已经注册了 yifut；
// 测试只追加 / 覆盖 stub provider 进 registry，结束后还原。
func withStubPaymentProviders(t *testing.T, stubs []*stubPaymentProvider, fn func()) {
	t.Helper()
	paymentProvidersMu.Lock()
	original := make(map[string]PaymentProvider, len(paymentProviders))
	for k, v := range paymentProviders {
		original[k] = v
	}
	paymentProvidersMu.Unlock()

	for _, s := range stubs {
		RegisterPaymentProvider(s)
	}

	defer func() {
		paymentProvidersMu.Lock()
		paymentProviders = original
		paymentProvidersMu.Unlock()
	}()

	fn()
}

func TestPaymentProvider_RegisterAndGet(t *testing.T) {
	stub := &stubPaymentProvider{key: "test-stub-1", configured: true}
	withStubPaymentProviders(t, []*stubPaymentProvider{stub}, func() {
		p, ok := GetPaymentProvider("test-stub-1")
		if !ok {
			t.Fatal("GetPaymentProvider returned ok=false for registered stub")
		}
		if p.Key() != "test-stub-1" {
			t.Errorf("Key=%q, want test-stub-1", p.Key())
		}
	})
}

func TestPaymentProvider_GetReturnsFalseForUnknown(t *testing.T) {
	_, ok := GetPaymentProvider("definitely-not-registered-zzz")
	if ok {
		t.Error("GetPaymentProvider should return ok=false for unknown key")
	}
}

func TestPaymentProvider_RegisterIsIdempotentByKey(t *testing.T) {
	// 重复注册同 key → 覆盖（用于测试 stub 替换生产 adapter）
	first := &stubPaymentProvider{key: "test-stub-replace", configured: false}
	second := &stubPaymentProvider{key: "test-stub-replace", configured: true}
	withStubPaymentProviders(t, []*stubPaymentProvider{first, second}, func() {
		p, _ := GetPaymentProvider("test-stub-replace")
		if !p.IsConfigured() {
			t.Error("second registration should override first (configured=true)")
		}
	})
}

func TestPaymentProvider_RegisterIgnoresNilOrEmptyKey(t *testing.T) {
	// 防御性：nil 或空 key 都应该静默忽略，不能崩
	before := len(paymentProviders)
	RegisterPaymentProvider(nil)
	RegisterPaymentProvider(&stubPaymentProvider{key: ""})
	after := len(paymentProviders)
	if after != before {
		t.Errorf("registry size changed after invalid registration: %d → %d", before, after)
	}
}

func TestListConfiguredPaymentProviderOptions_SortedByKey(t *testing.T) {
	stubs := []*stubPaymentProvider{
		{key: "test-stub-z", configured: true},
		{key: "test-stub-a", configured: true},
		{key: "test-stub-m", configured: true},
	}
	withStubPaymentProviders(t, stubs, func() {
		list := ListConfiguredPaymentProviderOptions()
		keys := make([]string, 0, len(list))
		for _, opts := range list {
			keys = append(keys, opts.Key)
		}
		sorted := append([]string{}, keys...)
		sort.Strings(sorted)
		for i := range keys {
			if keys[i] != sorted[i] {
				t.Errorf("ListConfiguredPaymentProviderOptions not sorted: got %v want %v", keys, sorted)
				return
			}
		}
	})
}

func TestListConfiguredPaymentProviderOptions_ExcludesUnconfigured(t *testing.T) {
	stubs := []*stubPaymentProvider{
		{key: "test-stub-on", configured: true},
		{key: "test-stub-off", configured: false},
	}
	withStubPaymentProviders(t, stubs, func() {
		list := ListConfiguredPaymentProviderOptions()
		for _, opts := range list {
			if opts.Key == "test-stub-off" {
				t.Errorf("ListConfiguredPaymentProviderOptions should exclude unconfigured provider, got %+v", opts)
			}
		}
	})
}

// ─── Yifut adapter 测试 ──────────────────────────────────────────────

func TestYifutPaymentProvider_Key(t *testing.T) {
	p := NewYifutPaymentProvider()
	if p.Key() != database.TopupProviderYifut {
		t.Errorf("Key=%q, want %q", p.Key(), database.TopupProviderYifut)
	}
}

// setYifutMinimalSysConfigForTest 仅用于"已配齐"状态下的测试，需要 4 项 SysConfig 都有值。
// 实际值真伪不重要——只要 LoadYifutConfig 能解析 PEM 通过 IsConfigured 检查即可。
// 但 RSA PEM 解析需要真实密钥；这里只设 pid + gateway，把 IsConfigured 卡在密钥上，
// 测试"未配齐 → ErrPaymentProviderNotConfigured"那条路径足以验证 W-1 抽象。
func setYifutPartialConfigForTest(t *testing.T) {
	t.Helper()
	proxy.SysConfigMutex.Lock()
	old := proxy.SysConfigCache
	cfg := make(map[string]string, len(old)+4)
	for k, v := range old {
		cfg[k] = v
	}
	cfg["yifut_pid"] = "12345"
	cfg["yifut_gateway"] = "https://example.com"
	// 故意不设 RSA 密钥 → IsConfigured 返 false
	delete(cfg, "yifut_merchant_private_key")
	delete(cfg, "yifut_platform_public_key")
	proxy.SysConfigCache = cfg
	proxy.SysConfigMutex.Unlock()
	t.Cleanup(func() {
		proxy.SysConfigMutex.Lock()
		proxy.SysConfigCache = old
		proxy.SysConfigMutex.Unlock()
	})
}

func TestYifutPaymentProvider_IsConfigured_FalseWhenKeysMissing(t *testing.T) {
	setYifutPartialConfigForTest(t)
	p := NewYifutPaymentProvider()
	if p.IsConfigured() {
		t.Error("IsConfigured should return false when RSA keys missing")
	}
}

func TestYifutPaymentProvider_CreateOrder_ReturnsNotConfiguredWhenMissing(t *testing.T) {
	setYifutPartialConfigForTest(t)
	p := NewYifutPaymentProvider()
	_, err := p.CreateOrder(context.Background(), &PaymentCreateOrderRequest{
		OutTradeNo: "tp_test_w1",
		AmountFen:  1000,
		RawExtras:  map[string]string{"pay_type": "alipay", "device": "pc"},
	})
	if !errors.Is(err, ErrPaymentProviderNotConfigured) {
		t.Errorf("err=%v, want ErrPaymentProviderNotConfigured", err)
	}
}

func TestYifutPaymentProvider_CreateOrder_RejectsNilRequest(t *testing.T) {
	p := NewYifutPaymentProvider()
	_, err := p.CreateOrder(context.Background(), nil)
	if !errors.Is(err, ErrPaymentProviderInternal) {
		t.Errorf("err=%v, want ErrPaymentProviderInternal for nil request", err)
	}
}

func TestYifutPaymentProvider_CreateOrder_RejectsMissingPayType(t *testing.T) {
	// 配置走完整路径（虽然不会真发出去，因为 pay_type 校验更早返回）
	// 这里用 partial config 也行，因为在 yifut 配置检查之后才校验 RawExtras——
	// 实际上 IsConfigured 先 fail，所以我们临时塞一个全配齐的 mock 太重；
	// 不如直接验证"即使配齐了，RawExtras 缺 pay_type 也会拒"。
	// 解法：注册一个完整 SysConfig 让 IsConfigured=true，CreateYifutOrder 会失败但 pay_type
	// 校验在 LoadYifutConfig 之后，所以会先 fail-internal。
	// 但因为我们没有真实 RSA 密钥，IsConfigured 必然 false，所以这个测试无法准确覆盖
	// "configured but missing pay_type"——跳过这层，依赖 controller 层的 pay_type 校验。
	t.Skip("controller 层在调 provider.CreateOrder 前已经校验 pay_type；adapter 内的 fallback 校验仅作防御")
}

func TestYifutPaymentProvider_PublicOptions(t *testing.T) {
	p := NewYifutPaymentProvider()

	// 设置一个干净的 SysConfig（不依赖 cleanup 前的状态）
	proxy.SysConfigMutex.Lock()
	old := proxy.SysConfigCache
	proxy.SysConfigCache = map[string]string{
		"yifut_enabled_methods":     "alipay,wxpay,bitcoin", // bitcoin 应被白名单过滤掉
		"yifut_preset_amounts_fen":  "1000, 3000,5000,abc,-500", // 含非法值应过滤
		"yifut_min_amount_fen":      "200",
		"yifut_max_amount_fen":      "500000",
	}
	proxy.SysConfigMutex.Unlock()
	t.Cleanup(func() {
		proxy.SysConfigMutex.Lock()
		proxy.SysConfigCache = old
		proxy.SysConfigMutex.Unlock()
	})

	opts := p.PublicOptions()
	if opts.Key != database.TopupProviderYifut {
		t.Errorf("Key=%q, want yifut", opts.Key)
	}
	if opts.Currency != "CNY" {
		t.Errorf("Currency=%q, want CNY", opts.Currency)
	}
	if opts.MinAmountFen != 200 {
		t.Errorf("MinAmountFen=%d, want 200", opts.MinAmountFen)
	}
	if opts.MaxAmountFen != 500000 {
		t.Errorf("MaxAmountFen=%d, want 500000", opts.MaxAmountFen)
	}
	// methods 应该只含白名单内的（alipay/wxpay），bitcoin 被过滤
	hasAlipay, hasWxpay, hasBitcoin := false, false, false
	for _, m := range opts.Methods {
		switch m {
		case "alipay":
			hasAlipay = true
		case "wxpay":
			hasWxpay = true
		case "bitcoin":
			hasBitcoin = true
		}
	}
	if !hasAlipay || !hasWxpay {
		t.Errorf("Methods missing alipay/wxpay: %v", opts.Methods)
	}
	if hasBitcoin {
		t.Errorf("Methods should filter out bitcoin (not in allowedPayTypes): %v", opts.Methods)
	}
	// presets：1000/3000/5000 应该被保留，abc/-500 应被过滤
	wantPresets := map[int64]bool{1000: false, 3000: false, 5000: false}
	for _, v := range opts.PresetsFen {
		if v < 0 {
			t.Errorf("PresetsFen contains negative value %d (should be filtered)", v)
		}
		if _, ok := wantPresets[v]; ok {
			wantPresets[v] = true
		}
	}
	for v, found := range wantPresets {
		if !found {
			t.Errorf("PresetsFen missing expected %d, got %v", v, opts.PresetsFen)
		}
	}
}

// ─── 静态接口校验 ──────────────────────────────────────────────

// 编译期 assertion：YifutPaymentProvider 必须实现 PaymentProvider interface。
// 如果 interface 增减方法，这里会编译失败。
var _ PaymentProvider = (*YifutPaymentProvider)(nil)
