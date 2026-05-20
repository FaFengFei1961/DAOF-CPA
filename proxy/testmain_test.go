package proxy

import (
	"os"
	"testing"

	"daof-cpa/utils"
)

// TestMain 在 proxy 包级别强制 RecordApiLogRevenue 走同步模式 + 初始化 crypto 根密钥。
//
// fix Phase B (2026-05-19) — SF-H6 把 RecordApiLogRevenue 改异步 goroutine 写
// 后，测试期 goroutine 容易跨测试边界（DB swap、cache reset）写到错误的 DB，
// 导致 transient FAIL。production 仍走 goroutine（不阻塞主请求路径），测试期
// 直接同步执行避免污染 + 让断言确定可见。
//
// fix Phase G-1.2 (2026-05-19) — SMTP 测试要解密 SysConfig 里加密存的 password，
// 需要 utils.aesKey 已就绪。把 daof.key 指到 tmp 目录避免污染 repo 根。
func TestMain(m *testing.M) {
	recordApiLogRevenueSync.Store(true)
	// 在临时目录里初始化 AES 根密钥（utils.Encrypt 依赖；密钥用完即弃）
	keyDir, err := os.MkdirTemp("", "daof-proxy-test-crypto-*")
	if err != nil {
		os.Exit(1)
	}
	defer os.RemoveAll(keyDir)
	os.Setenv("DAOF_KEY_PATH", keyDir+"/daof.key")
	utils.InitCrypto()
	os.Exit(m.Run())
}
