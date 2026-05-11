package controller

import (
	"testing"
	"time"
)

// resetSMSCache 测试间清理全局缓存，避免互相影响
func resetSMSCache() {
	smsCodeMu.Lock()
	smsCodeCache = map[string]*smsCodeEntry{}
	smsCodeMu.Unlock()
	smsCooldownMu.Lock()
	smsCooldown = map[string]time.Time{}
	smsCooldownMu.Unlock()
	smsIPRateMu.Lock()
	smsIPRate = map[string]*smsRateEntry{}
	smsIPRateMu.Unlock()
}

func TestVerifySMSCode_HappyPath(t *testing.T) {
	resetSMSCache()
	phone := "13800138000"
	code := "654321"
	smsCodeMu.Lock()
	smsCodeCache[phone] = &smsCodeEntry{
		Code:      code,
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}
	smsCodeMu.Unlock()

	if !verifySMSCode(phone, code) {
		t.Error("verifySMSCode should accept valid code")
	}
	// 一次性消费：第二次必须 false
	if verifySMSCode(phone, code) {
		t.Error("verifySMSCode should reject reused code (one-time use)")
	}
}

func TestVerifySMSCode_WrongCode(t *testing.T) {
	resetSMSCache()
	phone := "13800138001"
	smsCodeMu.Lock()
	smsCodeCache[phone] = &smsCodeEntry{
		Code:      "111111",
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}
	smsCodeMu.Unlock()

	if verifySMSCode(phone, "999999") {
		t.Error("verifySMSCode should reject wrong code")
	}
	// 验证码仍在缓存中（错误尝试不消费）
	smsCodeMu.Lock()
	_, exists := smsCodeCache[phone]
	smsCodeMu.Unlock()
	if !exists {
		t.Error("wrong attempt should not delete the code")
	}
}

func TestVerifySMSCode_Expired(t *testing.T) {
	resetSMSCache()
	phone := "13800138002"
	smsCodeMu.Lock()
	smsCodeCache[phone] = &smsCodeEntry{
		Code:      "123456",
		ExpiresAt: time.Now().Add(-1 * time.Minute), // 已过期
	}
	smsCodeMu.Unlock()

	if verifySMSCode(phone, "123456") {
		t.Error("verifySMSCode should reject expired code")
	}
	// 过期码应被清理
	smsCodeMu.Lock()
	_, exists := smsCodeCache[phone]
	smsCodeMu.Unlock()
	if exists {
		t.Error("expired code should be cleaned up")
	}
}

func TestVerifySMSCode_PhoneNotFound(t *testing.T) {
	resetSMSCache()
	if verifySMSCode("13900000000", "123456") {
		t.Error("verifySMSCode should reject phone never sent code")
	}
}

func TestGenerate6DigitCode(t *testing.T) {
	for i := 0; i < 100; i++ {
		code, err := generate6DigitCode()
		if err != nil {
			t.Fatalf("generate6DigitCode error: %v", err)
		}
		if len(code) != 6 {
			t.Errorf("code %q length=%d, want 6", code, len(code))
		}
		for _, ch := range code {
			if ch < '0' || ch > '9' {
				t.Errorf("code %q contains non-digit %q", code, ch)
			}
		}
	}
}

func TestChinaPhoneRegex(t *testing.T) {
	tests := []struct {
		phone string
		valid bool
	}{
		{"13800138000", true},
		{"15912345678", true},
		{"19987654321", true},
		{"12345678901", false},  // 第二位 2 不在 3-9
		{"1380013800", false},   // 10 位
		{"138001380000", false}, // 12 位
		{"23800138000", false},  // 不以 1 开头
		{"", false},
		{"abcdefghijk", false},
		{"+8613800138000", false}, // 带国家码
	}
	for _, tc := range tests {
		got := chinaPhoneRegex.MatchString(tc.phone)
		if got != tc.valid {
			t.Errorf("phone %q: got %v, want %v", tc.phone, got, tc.valid)
		}
	}
}
