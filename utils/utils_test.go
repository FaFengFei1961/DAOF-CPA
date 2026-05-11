package utils

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCryptoEdgeCases(t *testing.T) {
	// fix Major（codex 第八轮）：原测试在 cwd 直接 os.Remove("daof.key") + InitCrypto 创建
	// 会污染/重置生产 / 调试环境的根密钥，导致已加密 SysConfig 全部解密失败。
	// 改用 t.TempDir + DAOF_KEY_PATH 环境变量，测试结束自动清理。
	tmpDir := t.TempDir()
	t.Setenv("DAOF_KEY_PATH", filepath.Join(tmpDir, "test-daof.key"))
	InitCrypto()

	// encrypt empty
	enc, err := Encrypt("")
	if err != nil || enc != "" {
		t.Errorf("Encrypt empty should return empty string")
	}

	enc, err = Encrypt("Hello World! 1234")
	if err != nil {
		t.Errorf("Encryption failed: %v", err)
	}

	// decrypt empty
	dec, err := Decrypt("")
	if err != nil || dec != "" {
		t.Errorf("Decrypt empty should return empty string")
	}

	dec, err = Decrypt(enc)
	if err != nil {
		t.Errorf("Decryption failed: %v", err)
	}
	if dec != "Hello World! 1234" {
		t.Errorf("Decryption mismatch, got %v", dec)
	}

	// hash
	hash := GenerateHash("admin123")
	if len(hash) < 10 {
		t.Errorf("Hash failed")
	}
	if !CheckHash("admin123", hash) {
		t.Errorf("CheckHash failed")
	}
}

func TestCryptoFailures(t *testing.T) {
	// Break the AES key globally
	oldKey := aesKey
	aesKey = make([]byte, 10) // Invalid size
	_, err := Encrypt("test")
	if err == nil {
		t.Errorf("Expected encrypt to fail with wrong key size")
	}
	_, err = Decrypt("YmFzZTY0")
	if err == nil {
		t.Errorf("Expected decrypt to fail with wrong key size")
	}
	aesKey = oldKey

	// Test malformed base64 block
	_, err = Decrypt("!!!not_base64!!!")
	if err == nil {
		t.Errorf("Expected fail on invalid base64")
	}

	// Test too short decoded ciphertext block
	_, err = Decrypt("YWE=") // base64 "aa", smaller than 12
	if err == nil {
		t.Errorf("Expected fail on too short payload")
	}

	// Run InitCrypto when key file exists（依赖上面已 setenv 到 t.TempDir 的 DAOF_KEY_PATH）
	keyPath := os.Getenv("DAOF_KEY_PATH")
	if keyPath == "" {
		// 上一个 t.Setenv 是 TestCryptoEdgeCases 内的；本测试独立时单独构造一份临时 key
		tmpDir := t.TempDir()
		keyPath = filepath.Join(tmpDir, "test-daof.key")
		t.Setenv("DAOF_KEY_PATH", keyPath)
	}
	os.WriteFile(keyPath, []byte("12345678901234567890123456789012"), 0644)
	InitCrypto()
	// t.TempDir 自动清理
}
