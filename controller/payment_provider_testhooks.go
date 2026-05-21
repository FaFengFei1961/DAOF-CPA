// Build tag: 默认包含。生产构建可加 `-tags prod` 剥离。理由同
// proxy/email_queue_testhooks.go：跨包测试要 import controller.ResetPaymentProvidersForTest，
// _test.go 跨包不可见，回退到 build-tag 隔离。
//
// Audit 2026-05-21 T1-8 修正方案。
//go:build !prod

package controller

// ResetPaymentProvidersForTest 清空 PaymentProvider registry，供测试隔离用。
// 仅在测试代码里调用；生产代码路径不会触发。
func ResetPaymentProvidersForTest() {
	paymentProvidersMu.Lock()
	defer paymentProvidersMu.Unlock()
	paymentProviders = map[string]PaymentProvider{}
}
