package proxy

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"
)

// 在内存生成一对 RSA 密钥，导出 PEM 字符串供测试用
func genRSAKeyPair(t *testing.T) (privPEM, pubPEM string, priv *rsa.PrivateKey) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	privBytes, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal pkcs8: %v", err)
	}
	privPEM = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privBytes}))

	pubBytes, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("marshal pkix: %v", err)
	}
	pubPEM = string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubBytes}))
	return
}

// TestParseAndSign_RoundTrip 完整流程：生成密钥 → 解析 PEM → 签名 → 验签
func TestParseAndSign_RoundTrip(t *testing.T) {
	privPEM, pubPEM, _ := genRSAKeyPair(t)

	priv, err := ParseRSAPrivateKey(privPEM)
	if err != nil {
		t.Fatalf("parse private: %v", err)
	}
	pub, err := ParseRSAPublicKey(pubPEM)
	if err != nil {
		t.Fatalf("parse public: %v", err)
	}

	params := map[string]string{
		"pid":          "1161",
		"out_trade_no": "tp123abc",
		"money":        "1.00",
		"name":         "Token 充值",
		"timestamp":    "1700000000",
		"sign_type":    "RSA",
	}
	sig, err := SignYifutRSA(params, priv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if sig == "" {
		t.Fatal("signature empty")
	}

	params["sign"] = sig
	if !VerifyYifutRSA(params, pub) {
		t.Fatal("round-trip verification failed")
	}
}

// TestVerify_TamperedFails 篡改任意字段后必须验签失败
func TestVerify_TamperedFails(t *testing.T) {
	privPEM, pubPEM, _ := genRSAKeyPair(t)
	priv, _ := ParseRSAPrivateKey(privPEM)
	pub, _ := ParseRSAPublicKey(pubPEM)

	params := map[string]string{
		"pid":   "1161",
		"money": "0.01",
	}
	sig, _ := SignYifutRSA(params, priv)
	params["sign"] = sig

	// 篡改金额
	params["money"] = "100.00"
	if VerifyYifutRSA(params, pub) {
		t.Fatal("tampered money should fail")
	}
}

// TestVerify_RejectsInvalidInputs 空签名、错误 base64 等异常情况都应拒绝
func TestVerify_RejectsInvalidInputs(t *testing.T) {
	_, pubPEM, _ := genRSAKeyPair(t)
	pub, _ := ParseRSAPublicKey(pubPEM)

	cases := []map[string]string{
		{"a": "1"},                           // 无 sign
		{"a": "1", "sign": ""},               // 空 sign
		{"a": "1", "sign": "not-base64-!@#"}, // 非法 base64
		{"a": "1", "sign": "YWJjZA=="},       // 合法 base64 但不是有效签名
	}
	for _, p := range cases {
		if VerifyYifutRSA(p, pub) {
			t.Fatalf("should reject: %v", p)
		}
	}

	// nil 公钥必须拒绝
	if VerifyYifutRSA(map[string]string{"sign": "x"}, nil) {
		t.Fatal("nil pubkey should reject")
	}
}

// TestBuildSignString_Ordering 字段必须按 ASCII 升序排列
func TestBuildSignString_Ordering(t *testing.T) {
	got := buildSignString(map[string]string{
		"c": "3",
		"a": "1",
		"b": "2",
	})
	want := "a=1&b=2&c=3"
	if got != want {
		t.Errorf("ordering: got %q want %q", got, want)
	}
}

// TestBuildSignString_ExcludesSignAndEmpty 签名/类型/空值不参与
func TestBuildSignString_ExcludesSignAndEmpty(t *testing.T) {
	with := buildSignString(map[string]string{
		"a":         "1",
		"sign":      "deadbeef",
		"sign_type": "RSA",
		"empty":     "",
	})
	want := "a=1"
	if with != want {
		t.Errorf("got %q want %q", with, want)
	}
}

// TestParse_PEMWithoutHeaders 兼容用户只粘 base64 内容
func TestParse_PEMWithoutHeaders(t *testing.T) {
	privPEM, pubPEM, _ := genRSAKeyPair(t)

	// 剥掉 PEM 头尾
	stripPEM := func(s string) string {
		s = strings.ReplaceAll(s, "-----BEGIN PRIVATE KEY-----", "")
		s = strings.ReplaceAll(s, "-----END PRIVATE KEY-----", "")
		s = strings.ReplaceAll(s, "-----BEGIN PUBLIC KEY-----", "")
		s = strings.ReplaceAll(s, "-----END PUBLIC KEY-----", "")
		return strings.TrimSpace(s)
	}

	if _, err := ParseRSAPrivateKey(stripPEM(privPEM)); err != nil {
		t.Errorf("private without headers should parse: %v", err)
	}
	if _, err := ParseRSAPublicKey(stripPEM(pubPEM)); err != nil {
		t.Errorf("public without headers should parse: %v", err)
	}
}

// TestFormatMoneyRMB 金额格式不变（V1 V2 共用）
func TestFormatMoneyRMB(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{1, "1.00"},
		{1.5, "1.50"},
		{0.01, "0.01"},
		{100, "100.00"},
		{99.99, "99.99"},
	}
	for _, c := range cases {
		got := FormatMoneyRMB(c.in)
		if got != c.want {
			t.Errorf("FormatMoneyRMB(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
