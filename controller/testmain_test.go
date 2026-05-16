// Package controller / testmain_test.go
//
// TestMain 兜底：把 DAOF_KEY_PATH 指向 OS 临时目录，避免任何测试调
// utils.InitCrypto() 时把 daof.key 写到 controller/ 包目录（即 cwd）污染源码树。
//
// 历史问题：之前测试直接 utils.InitCrypto() 没 setenv，导致每次跑 controller 测试
// 都在 controller/daof.key 留下一个 32B 密钥文件。reset.ps1 会清，但根因在测试代码。
package controller

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMain(m *testing.M) {
	tmpDir, err := os.MkdirTemp("", "daof-controller-test-*")
	if err != nil {
		panic("MkdirTemp for DAOF_KEY_PATH: " + err.Error())
	}
	_ = os.Setenv("DAOF_KEY_PATH", filepath.Join(tmpDir, "test-daof.key"))
	code := m.Run()
	_ = os.RemoveAll(tmpDir)
	os.Exit(code)
}
