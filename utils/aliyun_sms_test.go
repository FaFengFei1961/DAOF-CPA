package utils

import (
	"strings"
	"testing"
)

func TestPercentEncode(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain ascii", "hello", "hello"},
		{"space → %20 not +", "hello world", "hello%20world"},
		{"asterisk → %2A", "a*b", "a%2Ab"},
		{"tilde stays ~", "a~b", "a~b"},
		{"slash escaped", "/", "%2F"},
		{"equals escaped", "a=b", "a%3Db"},
		{"chinese", "签名", "%E7%AD%BE%E5%90%8D"},
		{"empty", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := percentEncode(tc.in)
			if got != tc.want {
				t.Errorf("percentEncode(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// 阿里云签名算法是确定性的：同样的入参必出同样的签名
// 测试一个固定 fixture，避免回归
func TestSignAliyunRPC_Deterministic(t *testing.T) {
	params := map[string]string{
		"AccessKeyId":      "testkey",
		"Action":           "SendSms",
		"Format":           "JSON",
		"PhoneNumbers":     "13800138000",
		"SignName":         "TestSign",
		"SignatureMethod":  "HMAC-SHA1",
		"SignatureNonce":   "fixed-nonce-123",
		"SignatureVersion": "1.0",
		"TemplateCode":     "SMS_TEST_001",
		"TemplateParam":    `{"code":"123456"}`,
		"Timestamp":        "2026-04-30T00:00:00Z",
		"Version":          "2017-05-25",
	}
	secret := "test-secret-abc"

	sig1 := signAliyunRPC("POST", params, secret)
	sig2 := signAliyunRPC("POST", params, secret)
	if sig1 != sig2 {
		t.Fatalf("signature must be deterministic, got %q vs %q", sig1, sig2)
	}
	if sig1 == "" {
		t.Fatal("signature must not be empty")
	}
	// base64 不应有非法字符
	if strings.ContainsAny(sig1, " \t\n") {
		t.Errorf("signature should be clean base64, got %q", sig1)
	}
}

func TestSignAliyunRPC_DifferentSecretChangesSig(t *testing.T) {
	params := map[string]string{
		"Action":    "SendSms",
		"Timestamp": "2026-04-30T00:00:00Z",
	}
	a := signAliyunRPC("POST", params, "secret-A")
	b := signAliyunRPC("POST", params, "secret-B")
	if a == b {
		t.Errorf("different secrets must produce different signatures, both got %q", a)
	}
}

func TestSignAliyunRPC_DifferentParamsChangeSig(t *testing.T) {
	base := map[string]string{"Action": "SendSms", "Timestamp": "2026-04-30T00:00:00Z"}
	mod := map[string]string{"Action": "SendSms", "Timestamp": "2026-04-30T00:00:01Z"}
	if signAliyunRPC("POST", base, "secret") == signAliyunRPC("POST", mod, "secret") {
		t.Error("changing Timestamp must change signature")
	}
}

func TestSendAliyunSMS_ConfigValidation(t *testing.T) {
	tests := []struct {
		name        string
		ak, sk      string
		sign, tpl   string
		phone       string
		wantErrFrag string
	}{
		{"missing access key", "", "sk", "sign", "tpl", "138", "access key/secret"},
		{"missing secret", "ak", "", "sign", "tpl", "138", "access key/secret"},
		{"missing sign", "ak", "sk", "", "tpl", "138", "sign/template"},
		{"missing template", "ak", "sk", "sign", "", "138", "sign/template"},
		{"missing phone", "ak", "sk", "sign", "tpl", "", "phone empty"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := SendAliyunSMS(tc.ak, tc.sk, tc.sign, tc.tpl, tc.phone, map[string]string{"code": "123456"})
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErrFrag) {
				t.Errorf("error %q should contain %q", err.Error(), tc.wantErrFrag)
			}
		})
	}
}
