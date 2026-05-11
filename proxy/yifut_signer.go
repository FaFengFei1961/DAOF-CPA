// Package proxy / yifut_signer.go
//
// 易付通 V2 RSA 签名/验签（SHA256WithRSA）。
// 协议规则：https://www.yifut.com/doc/sign_note.html
//
// 签名步骤：
//  1. 收集所有非空请求参数（剔除 sign / sign_type）
//  2. 按参数名 ASCII 升序排序
//  3. 拼成 a=val1&b=val2 形式（值不 URL 编码）
//  4. 用商户私钥对待签名串做 SHA256WithRSA 签名
//  5. base64 编码作为 sign 字段提交
//
// 验签步骤（异步通知 / 同步跳转 / 接口返回）：
//  1. 同样规则构造待签名字符串
//  2. 用平台公钥验证 sign 是否合法
package proxy

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"sort"
	"strings"
)

// ─── 签名串构造 ────────────────────────────────────────────────

// buildSignString 收集非空参数（排除 sign/sign_type），按 key 升序拼接。
// 易付通 V2 RSA 签名输入：过滤空值和 sign 字段后按 key 字典序拼接 query string。
func buildSignString(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k, v := range params {
		if k == "sign" || k == "sign_type" {
			continue
		}
		if v == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(params[k])
	}
	return b.String()
}

// ─── PEM 密钥解析 ──────────────────────────────────────────────

// ParseRSAPrivateKey 解析 PEM 格式的商户私钥。支持 PKCS1 和 PKCS8 两种 PEM 头。
//
// 易付通后台导出的私钥可能是这些常见形式：
//   - "-----BEGIN PRIVATE KEY-----"          (PKCS8)
//   - "-----BEGIN RSA PRIVATE KEY-----"      (PKCS1)
//   - 或仅包含 base64 内容（无头尾，需自动包装）
func ParseRSAPrivateKey(pemStr string) (*rsa.PrivateKey, error) {
	pemStr = strings.TrimSpace(pemStr)
	if pemStr == "" {
		return nil, fmt.Errorf("empty private key")
	}
	// 兼容用户只粘贴 base64 内容、缺少 BEGIN/END 的场景
	if !strings.Contains(pemStr, "BEGIN") {
		pemStr = "-----BEGIN PRIVATE KEY-----\n" + pemStr + "\n-----END PRIVATE KEY-----"
	}

	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, fmt.Errorf("not a valid PEM block")
	}
	// 先按 PKCS8 试
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		rsaKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("PKCS8 key is not RSA")
		}
		return rsaKey, nil
	}
	// 退回 PKCS1
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	return key, nil
}

// ParseRSAPublicKey 解析 PEM 格式的平台公钥。支持 PKIX (X.509 SubjectPublicKeyInfo) 和 PKCS1 两种。
//
// 易付通"平台公钥"通常是 PKIX 格式：
//   - "-----BEGIN PUBLIC KEY-----"
//   - 或仅 base64 内容
func ParseRSAPublicKey(pemStr string) (*rsa.PublicKey, error) {
	pemStr = strings.TrimSpace(pemStr)
	if pemStr == "" {
		return nil, fmt.Errorf("empty public key")
	}
	if !strings.Contains(pemStr, "BEGIN") {
		pemStr = "-----BEGIN PUBLIC KEY-----\n" + pemStr + "\n-----END PUBLIC KEY-----"
	}

	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, fmt.Errorf("not a valid PEM block")
	}
	// PKIX
	if pub, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
		rsaPub, ok := pub.(*rsa.PublicKey)
		if !ok {
			return nil, fmt.Errorf("PKIX key is not RSA")
		}
		return rsaPub, nil
	}
	// PKCS1
	rsaPub, err := x509.ParsePKCS1PublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse public key: %w", err)
	}
	return rsaPub, nil
}

// ─── 签名 / 验签 ──────────────────────────────────────────────

// SignYifutRSA 用商户私钥对参数 map 做 SHA256WithRSA 签名，返回 base64 编码的签名串。
func SignYifutRSA(params map[string]string, privKey *rsa.PrivateKey) (string, error) {
	if privKey == nil {
		return "", fmt.Errorf("private key not loaded")
	}
	signStr := buildSignString(params)
	hashed := sha256.Sum256([]byte(signStr))
	sig, err := rsa.SignPKCS1v15(rand.Reader, privKey, crypto.SHA256, hashed[:])
	if err != nil {
		return "", fmt.Errorf("rsa sign: %w", err)
	}
	return base64.StdEncoding.EncodeToString(sig), nil
}

// VerifyYifutRSA 用平台公钥校验 params 中 sign 字段。
//   - 提取 params["sign"]（base64）
//   - 重建待签名字符串（排除 sign / sign_type / 空值）
//   - VerifyPKCS1v15 内部已是常量时间，无需 ConstantTimeCompare
func VerifyYifutRSA(params map[string]string, pubKey *rsa.PublicKey) bool {
	if pubKey == nil {
		return false
	}
	sigB64 := strings.TrimSpace(params["sign"])
	if sigB64 == "" {
		return false
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return false
	}
	signStr := buildSignString(params)
	hashed := sha256.Sum256([]byte(signStr))
	if err := rsa.VerifyPKCS1v15(pubKey, crypto.SHA256, hashed[:], sig); err != nil {
		return false
	}
	return true
}

// ─── 金额格式化（V2 下单/回调校验共用） ─────────────────────────────

// FormatMoneyRMB 格式化金额："1" → "1.00"，最多 2 位小数。
func FormatMoneyRMB(rmb float64) string {
	return fmt.Sprintf("%.2f", rmb)
}
