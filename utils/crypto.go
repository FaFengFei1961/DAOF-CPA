package utils

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"os"
)

var aesKey []byte

// InitCrypto 尝试读取或生成根密钥。
//
// 路径可通过环境变量 DAOF_KEY_PATH 覆盖；未设置时默认 ./daof.key（当前工作目录）。
// 推荐生产部署：把 key 放到 data/ 子目录或独立挂载卷，避免与源码一起被 zip/打包。
//
//	DAOF_KEY_PATH=./data/daof.key go run main.go
func InitCrypto() {
	keyFile := os.Getenv("DAOF_KEY_PATH")
	if keyFile == "" {
		keyFile = "daof.key"
	}
	data, err := os.ReadFile(keyFile)
	if err == nil && len(data) == 32 {
		aesKey = data
		log.Println("🔐 成功装载本地 AES 根密钥。")
		return
	}

	// 如果不存在或不对，生成一把全新的 256 位密钥
	aesKey = make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, aesKey); err != nil {
		log.Fatalf("无法生成强随机根密钥: %v", err)
	}

	if err := os.WriteFile(keyFile, aesKey, 0600); err != nil {
		log.Fatalf("无法写入根密钥文件: %v", err)
	}
	log.Println("⚡️ 首次启动：成功创建并写入 AES 根密钥。")
}

// Encrypt 负责将核心敏感数据通过 AES-GCM 进行全息搅拌加密
func Encrypt(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	if len(aesKey) != 32 {
		return "", fmt.Errorf("ERR_CRYPTO_NOT_INIT")
	}

	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}

	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt 负责解密数据库中提出的混淆数据
func Decrypt(cryptoText string) (string, error) {
	if cryptoText == "" {
		return "", nil
	}
	if len(aesKey) != 32 {
		return "", fmt.Errorf("ERR_DECRYPT_NOT_INIT")
	}

	ciphertext, err := base64.StdEncoding.DecodeString(cryptoText)
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return "", fmt.Errorf("ERR_CIPHERTEXT_TOO_SHORT")
	}

	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}

	return string(plaintext), nil
}
