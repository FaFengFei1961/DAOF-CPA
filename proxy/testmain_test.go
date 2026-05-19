package proxy

import (
	"os"
	"testing"
)

// TestMain 在 proxy 包级别强制 RecordApiLogRevenue 走同步模式。
//
// fix Phase B (2026-05-19) — SF-H6 把 RecordApiLogRevenue 改异步 goroutine 写
// 后，测试期 goroutine 容易跨测试边界（DB swap、cache reset）写到错误的 DB，
// 导致 transient FAIL。production 仍走 goroutine（不阻塞主请求路径），测试期
// 直接同步执行避免污染 + 让断言确定可见。
func TestMain(m *testing.M) {
	recordApiLogRevenueSync.Store(true)
	os.Exit(m.Run())
}
