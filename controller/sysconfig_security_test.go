// Package controller / sysconfig_security_test.go
//
// 覆盖 SysConfig 脱敏 / 防回写相关的 Major 修复：
//  1. R7 Minor: MaskSecret 用 rune 切，UTF-8 多字节字符不被截成 �
//  2. R6+R7 Minor: looksLikeMaskedSecret 精确匹配 14 rune 模式，避免误伤合法值
//  3. isSensitiveConfigKey 后缀匹配不误伤 "monkey" / "key_rotation_counter"
package controller

import (
	"strings"
	"testing"

	"daof-ai-hub/database"
)

// ─── R7 Minor: MaskSecret UTF-8 安全 ──────────────────────────────

// TestSecurity_MaskSecret_UTF8MultibyteSafe 验证：
// 包含中文 / emoji 等多字节字符的密钥，按 rune 切割后不会出现 �（U+FFFD）。
//
// 攻击/缺陷场景（codex r7）：原实现 s[:2] + ... + s[len(s)-4:] 是按 byte 切，
// 中文每字符 3 byte → s[:2] 切到字符中间字节 → UTF-8 解码出 �。
// 副作用：脱敏后字符串通过 looksLikeMaskedSecret 检测失败，admin GET → POST 把
// �****�... 回写到真实密钥位，破坏配置。
func TestSecurity_MaskSecret_UTF8MultibyteSafe(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"chinese", "我的秘密钥匙abcd1234"},
		{"emoji", "🔑secret_key_abcd1234"},
		{"mixed", "中abc🔑x_y_z_1234"},
		{"long_chinese", "极长的中文密码abcdefghij"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := MaskSecret(tc.in)
			// 关键 1：不含 U+FFFD（替换字符）
			if strings.ContainsRune(out, '�') {
				t.Errorf("MaskSecret(%q) produced replacement char: %q", tc.in, out)
			}
			// 关键 2：长度满足 14 rune（前 2 + 8 星 + 后 4）
			if len([]rune(out)) != 14 {
				t.Errorf("MaskSecret(%q) length=%d (rune), want 14: %q", tc.in, len([]rune(out)), out)
			}
			// 关键 3：自反检测——MaskSecret 的输出必须被 looksLikeMaskedSecret 识别
			if !looksLikeMaskedSecret(out) {
				t.Errorf("looksLikeMaskedSecret(%q) returned false; would let admin re-POST mask as real key", out)
			}
		})
	}
}

// TestSecurity_MaskSecret_ShortReturnsFixed 验证：长度 ≤ 6 rune 的输入，返回固定 "******"。
func TestSecurity_MaskSecret_ShortReturnsFixed(t *testing.T) {
	cases := []string{"", "a", "ab", "abcde", "abcdef", "中abc"}
	for _, in := range cases {
		out := MaskSecret(in)
		if out != "******" {
			t.Errorf("MaskSecret(%q)=%q, want \"******\"", in, out)
		}
	}
}

// ─── R6+R7 Minor: looksLikeMaskedSecret 不误伤合法值 ───────────────

// TestSecurity_LooksLikeMaskedSecret_NotOverbroadOnValidStrings 验证：
// 含 4 个连续 * 但不符合精确格式的字符串（如 webhook 模板）不被误判为 mask。
//
// 缺陷场景（自审 r6）：原 strings.Contains(v, "****") 会把
// "https://hooks.example.com/webhook?token=****PLACEHOLDER****" 当 mask，
// 静默丢弃 admin 的合法更新。
func TestSecurity_LooksLikeMaskedSecret_NotOverbroadOnValidStrings(t *testing.T) {
	notMasked := []string{
		"",
		"a",
		"********",                        // 8 个星，但前后无字符
		"webhook?token=****PLACEHOLDER",   // 含 4 星但格式不符
		"sk-real-secret-1234567890abcdef", // 真实密钥
		"我爱中国",                            // 4 rune 但无星号
		"ab********cdef0",                 // 15 rune（多 1 个）
		"ab*******cdef",                   // 7 星而非 8 星
		"abcdefghij",                      // 10 rune 无星号
	}
	for _, v := range notMasked {
		if looksLikeMaskedSecret(v) {
			t.Errorf("looksLikeMaskedSecret(%q)=true, should be false", v)
		}
	}
}

// TestSecurity_LooksLikeMaskedSecret_AcceptsExactFormat 验证：精确格式（14 rune，第 3-10 位是星号）必须识别。
func TestSecurity_LooksLikeMaskedSecret_AcceptsExactFormat(t *testing.T) {
	masked := []string{
		"******",         // 短输入兜底
		"sk********cdef", // 标准
		"我心********的爱啊我", // 中文混合（14 rune）
		"AB********1234",
	}
	for _, v := range masked {
		if !looksLikeMaskedSecret(v) {
			t.Errorf("looksLikeMaskedSecret(%q)=false, should be true", v)
		}
	}
}

// ─── isSensitiveConfigKey 后缀匹配 ───────────────────────────────

// TestSecurity_IsSensitiveConfigKey_NoFalsePositive 验证：
// "monkey" 不被当成 sensitive（旧 Contains 实现会误中 "monkey" 含 "key"）。
// "key_rotation_counter" 也不应误中。
func TestSecurity_IsSensitiveConfigKey_NoFalsePositive(t *testing.T) {
	notSensitive := []string{
		"monkey",
		"key_rotation_counter",
		"public_key_url", // 只有 _url 后缀，且不在 sensitiveExactKeys
		"username",
		"github_client_id", // _id 不在 suffixes
		"normal_setting",
	}
	for _, k := range notSensitive {
		if isSensitiveConfigKey(k) {
			t.Errorf("isSensitiveConfigKey(%q)=true, should be false", k)
		}
	}
}

// TestSecurity_IsSensitiveConfigKey_TrueForActualSensitive 验证：精确 key + 标准后缀 触发。
func TestSecurity_IsSensitiveConfigKey_TrueForActualSensitive(t *testing.T) {
	sensitive := []string{
		"github_client_secret", // 精确匹配
		"aliyun_access_secret",
		"cliproxy_key",
		"some_password",
		"openai_token",
		"anthropic_apikey",
		"my_api_key",
		"merchant_private_key",
	}
	for _, k := range sensitive {
		if !isSensitiveConfigKey(k) {
			t.Errorf("isSensitiveConfigKey(%q)=false, should be true", k)
		}
	}
}

func TestSecurity_ClearableEmptyConfigKeys(t *testing.T) {
	if !isClearableEmptyConfigKey("moderation_cache_secret") {
		t.Fatal("moderation_cache_secret reset must be clearable from Settings")
	}
	if isClearableEmptyConfigKey("moderation_gemini_auth_index") {
		t.Fatal("removed Gemini moderation config must not remain clearable")
	}
	if isClearableEmptyConfigKey("cliproxy_key") {
		t.Fatal("real credentials must not become clearable through the Settings full-save path")
	}
}

func TestSecurity_ValidateSysConfigPayload_BalanceDefaults(t *testing.T) {
	setupSubTestDB(t)

	cases := []struct {
		name string
		in   map[string]string
		code string
		ok   bool
	}{
		{
			name: "valid",
			in: map[string]string{
				"balance_consume_default_enabled":     "true",
				"balance_consume_default_limit_usd":   "10.50",
				"balance_consume_default_window_secs": "60",
			},
			ok: true,
		},
		{name: "bad bool", in: map[string]string{"balance_consume_default_enabled": "maybe"}, code: "ERR_INVALID_PARAMS"},
		{name: "negative limit", in: map[string]string{"balance_consume_default_limit_usd": "-1"}, code: "ERR_LIMIT_INVALID"},
		{name: "short window", in: map[string]string{"balance_consume_default_window_secs": "59"}, code: "ERR_WINDOW_INVALID"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, ok := validateSysConfigPayload(tc.in)
			if ok != tc.ok || code != tc.code {
				t.Fatalf("validateSysConfigPayload() code=%q ok=%v, want code=%q ok=%v", code, ok, tc.code, tc.ok)
			}
		})
	}
}

func TestSecurity_ValidateSysConfigPayload_ModerationRiskRules(t *testing.T) {
	valid := `[{"id":"env_combo","action":"model_review","any_groups":[[".env"],["send"]]}]`
	code, _, ok := validateSysConfigPayload(map[string]string{"moderation_risk_rules": valid})
	if !ok || code != "" {
		t.Fatalf("valid moderation_risk_rules rejected: code=%q ok=%v", code, ok)
	}

	invalid := `[{"id":"bad","regex":["("]}]`
	code, _, ok = validateSysConfigPayload(map[string]string{"moderation_risk_rules": invalid})
	if ok || code != "ERR_INVALID_PARAMS" {
		t.Fatalf("invalid moderation_risk_rules accepted: code=%q ok=%v", code, ok)
	}
}

func TestSecurity_ValidateSysConfigPayload_ModerationProvider(t *testing.T) {
	for _, provider := range []string{"cliproxy_model", "cpa-model", "cliproxy", "cpa"} {
		code, _, ok := validateSysConfigPayload(map[string]string{"moderation_provider": provider})
		if !ok || code != "" {
			t.Fatalf("valid moderation_provider %q rejected: code=%q ok=%v", provider, code, ok)
		}
	}
	for _, provider := range []string{"gemini_cpa", "gemini-ai-studio", "openai"} {
		code, _, ok := validateSysConfigPayload(map[string]string{"moderation_provider": provider})
		if ok || code != "ERR_INVALID_PARAMS" {
			t.Fatalf("invalid moderation_provider %q accepted: code=%q ok=%v", provider, code, ok)
		}
	}
}

func TestSecurity_ValidateSysConfigPayload_SignupCouponTemplate(t *testing.T) {
	setupSubTestDB(t)

	enabled := true
	disabled := false
	activeTpl := database.CouponTemplate{Name: "welcome", DiscountType: "fixed_price", Enabled: &enabled}
	disabledTpl := database.CouponTemplate{Name: "disabled", DiscountType: "fixed_price", Enabled: &disabled}
	if err := database.DB.Create(&activeTpl).Error; err != nil {
		t.Fatalf("create active template: %v", err)
	}
	if err := database.DB.Create(&disabledTpl).Error; err != nil {
		t.Fatalf("create disabled template: %v", err)
	}

	cases := []struct {
		name string
		id   string
		code string
		ok   bool
	}{
		{name: "off", id: "0", ok: true},
		{name: "active", id: "1", ok: true},
		{name: "bad id", id: "abc", code: "ERR_INVALID_TEMPLATE"},
		{name: "missing", id: "999", code: "ERR_TEMPLATE_NOT_FOUND"},
		{name: "disabled", id: "2", code: "ERR_TEMPLATE_DISABLED"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, ok := validateSysConfigPayload(map[string]string{"signup_coupon_template_id": tc.id})
			if ok != tc.ok || code != tc.code {
				t.Fatalf("validateSysConfigPayload() code=%q ok=%v, want code=%q ok=%v", code, ok, tc.code, tc.ok)
			}
		})
	}
}
