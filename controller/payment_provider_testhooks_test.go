// Test-only hooks for payment provider registry.
// Living in *_test.go ensures this function disappears from the production binary
// (Audit 2026-05-21 T1-4 fix).
package controller

// ResetPaymentProvidersForTest 清空 PaymentProvider registry，供测试隔离用。
// 仅 _test 构建可见。
func ResetPaymentProvidersForTest() {
	paymentProvidersMu.Lock()
	defer paymentProvidersMu.Unlock()
	paymentProviders = map[string]PaymentProvider{}
}
