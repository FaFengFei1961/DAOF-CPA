// Package proxy / email_template_test.go
//
// Phase G-1.3 单元测试：邮件模板系统 + i18n + 占位替换 + HTML 转义。
package proxy

import (
	"strings"
	"testing"
)

// withEmailEnabled 暂时把 email_enabled=true 写进 SysConfigCache，结束时还原。
func withEmailEnabled(t *testing.T, enabled bool, fn func()) {
	t.Helper()
	SysConfigMutex.Lock()
	prev := SysConfigCache
	SysConfigCache = map[string]string{}
	for k, v := range prev {
		SysConfigCache[k] = v
	}
	if enabled {
		SysConfigCache[emailConfigKeyEnabled] = "true"
	} else {
		delete(SysConfigCache, emailConfigKeyEnabled)
	}
	SysConfigMutex.Unlock()
	defer func() {
		SysConfigMutex.Lock()
		SysConfigCache = prev
		SysConfigMutex.Unlock()
	}()
	fn()
}

func TestIsEmailEnabled(t *testing.T) {
	tests := []struct {
		name string
		v    string
		want bool
	}{
		{"true lowercase", "true", true},
		{"TRUE uppercase", "TRUE", true},
		{"1 numeric", "1", true},
		{"false", "false", false},
		{"0", "0", false},
		{"empty", "", false},
		{"whitespace", "   ", false},
		{"random text", "yes please", false}, // 严格匹配
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			SysConfigMutex.Lock()
			prev := SysConfigCache
			SysConfigCache = map[string]string{emailConfigKeyEnabled: tc.v}
			SysConfigMutex.Unlock()
			defer func() {
				SysConfigMutex.Lock()
				SysConfigCache = prev
				SysConfigMutex.Unlock()
			}()
			if got := IsEmailEnabled(); got != tc.want {
				t.Errorf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestIsZhLocale(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"", true}, // default zh
		{"zh", true},
		{"zh-CN", true},
		{"zh_TW", true},
		{"ZH-CN", true},
		{"en", false},
		{"en-US", false},
		{"ja", false},
		{"  zh  ", true}, // trim
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			if got := isZhLocale(tc.in); got != tc.want {
				t.Errorf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestRenderEmail_DisabledReturnsError(t *testing.T) {
	withEmailEnabled(t, false, func() {
		_, err := RenderEmail(EmailTplVerify, "zh", EmailVars{})
		if err != ErrEmailDisabled {
			t.Errorf("expected ErrEmailDisabled, got %v", err)
		}
	})
}

func TestRenderEmail_UnknownTemplateReturnsError(t *testing.T) {
	withEmailEnabled(t, true, func() {
		_, err := RenderEmail("nonexistent_template", "zh", EmailVars{})
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(err.Error(), "unknown email template") {
			t.Errorf("error message should mention unknown template, got %v", err)
		}
	})
}

func TestRenderEmail_VerifyZH(t *testing.T) {
	withEmailEnabled(t, true, func() {
		msg, err := RenderEmail(EmailTplVerify, "zh-CN", EmailVars{
			UserName:  "张三",
			UserEmail: "zhang@example.com",
			VerifyURL: "https://app.example.com/verify?token=abc123",
			ExpiresIn: "1 小时",
			AppName:   "DAOF-CPA",
		})
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if !strings.Contains(msg.Subject, "请验证您的邮箱") {
			t.Errorf("subject missing zh text: %q", msg.Subject)
		}
		if !strings.Contains(msg.Subject, "DAOF-CPA") {
			t.Errorf("subject missing app_name: %q", msg.Subject)
		}
		if !strings.Contains(msg.TextBody, "张三") {
			t.Errorf("text body missing user_name: %q", msg.TextBody)
		}
		if !strings.Contains(msg.TextBody, "zhang@example.com") {
			t.Errorf("text body missing user_email")
		}
		if !strings.Contains(msg.TextBody, "https://app.example.com/verify?token=abc123") {
			t.Errorf("text body missing verify_url")
		}
		if !strings.Contains(msg.TextBody, "1 小时") {
			t.Errorf("text body missing expires_in")
		}
		// HTML body 应该有按钮 + URL
		if !strings.Contains(msg.HTMLBody, "href=\"https://app.example.com/verify?token=abc123\"") {
			t.Errorf("html body missing verify_url in href")
		}
		// HTML escape：用户名 "张三" 在 HTML 里仍是 "张三"（非 ASCII 但不需要 escape）
		if !strings.Contains(msg.HTMLBody, "张三") {
			t.Errorf("html body missing escaped user_name")
		}
	})
}

func TestRenderEmail_VerifyEN(t *testing.T) {
	withEmailEnabled(t, true, func() {
		msg, err := RenderEmail(EmailTplVerify, "en-US", EmailVars{
			UserName:  "Alice",
			UserEmail: "alice@example.com",
			VerifyURL: "https://app.example.com/verify?token=xyz",
			ExpiresIn: "1 hour",
			AppName:   "DAOF-CPA",
		})
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if !strings.Contains(msg.Subject, "Verify your email") {
			t.Errorf("subject not in english: %q", msg.Subject)
		}
		if !strings.Contains(msg.TextBody, "Hi Alice") {
			t.Errorf("text body missing en greeting: %q", msg.TextBody)
		}
		if !strings.Contains(msg.TextBody, "alice@example.com") {
			t.Errorf("text body missing user_email")
		}
		if !strings.Contains(msg.HTMLBody, "Verify Email") {
			t.Errorf("html body missing en CTA button: %q", msg.HTMLBody)
		}
	})
}

func TestRenderEmail_HTMLEscapesUntrustedFields(t *testing.T) {
	withEmailEnabled(t, true, func() {
		msg, err := RenderEmail(EmailTplVerify, "zh", EmailVars{
			UserName:  `<script>alert('xss')</script>`,
			UserEmail: `evil"onclick="alert(1)"@example.com`,
			VerifyURL: "https://app.example.com/verify",
			ExpiresIn: "1h",
			AppName:   "DAOF-CPA",
		})
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		// HTML body 必须 escape：<script> → &lt;script&gt;
		if strings.Contains(msg.HTMLBody, "<script>alert('xss')</script>") {
			t.Error("HTML body did not escape <script> tag")
		}
		if !strings.Contains(msg.HTMLBody, "&lt;script&gt;") {
			t.Error("HTML body should contain escaped <script> entity")
		}
		// onclick injection 被转义（"→ &#34;）
		if strings.Contains(msg.HTMLBody, `evil"onclick="alert(1)"@example.com`) {
			t.Error("HTML body did not escape attribute injection attempt")
		}
		// text body 不转义（text 是 text）
		if !strings.Contains(msg.TextBody, "<script>alert('xss')</script>") {
			t.Error("text body should NOT escape (raw HTML stays as plaintext)")
		}
	})
}

func TestRenderEmail_URLsNotDoubleEscaped(t *testing.T) {
	withEmailEnabled(t, true, func() {
		// 含 query string & 的 URL，HTML body 里不应该把 & 转成 &amp;
		// （因为我们把 URL 当 trusted，不做 HTML escape；caller 已 url-encode 过）
		urlWithQuery := "https://app.example.com/verify?token=abc&user=1"
		msg, err := RenderEmail(EmailTplVerify, "zh", EmailVars{
			UserName:  "alice",
			VerifyURL: urlWithQuery,
			ExpiresIn: "1h",
		})
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if !strings.Contains(msg.HTMLBody, urlWithQuery) {
			t.Errorf("URL should pass through unmodified: %q", msg.HTMLBody)
		}
	})
}

func TestRenderEmail_SysConfigOverrides(t *testing.T) {
	withEmailEnabled(t, true, func() {
		// 把 zh + en subject 都覆盖
		SysConfigMutex.Lock()
		SysConfigCache["email_verify_subject_zh"] = "自定义主题：请验证"
		SysConfigMutex.Unlock()

		msg, err := RenderEmail(EmailTplVerify, "zh", EmailVars{UserName: "u"})
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if msg.Subject != "自定义主题：请验证" {
			t.Errorf("subject not overridden: %q", msg.Subject)
		}

		// en 当 SysConfig en 未配置时：会回退到 zh SysConfig（与 PickLocalizedMessage 一致行为）。
		// 这是 "admin only customized one locale" 时的预期：用户拿到的是 admin 配过的版本，
		// 而不是 fallback 到硬编码默认。
		msgEN, err := RenderEmail(EmailTplVerify, "en", EmailVars{UserName: "u"})
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if msgEN.Subject != "自定义主题：请验证" {
			t.Errorf("en should fall back to zh SysConfig when en not set, got %q", msgEN.Subject)
		}

		// 加上 en 覆盖后，en 应该独立用 en
		SysConfigMutex.Lock()
		SysConfigCache["email_verify_subject_en"] = "Custom: Please Verify"
		SysConfigMutex.Unlock()
		msgEN2, err := RenderEmail(EmailTplVerify, "en", EmailVars{UserName: "u"})
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if msgEN2.Subject != "Custom: Please Verify" {
			t.Errorf("en subject not overridden: %q", msgEN2.Subject)
		}
	})
}

func TestRenderEmail_ResetPassword(t *testing.T) {
	withEmailEnabled(t, true, func() {
		msg, err := RenderEmail(EmailTplResetPassword, "zh", EmailVars{
			UserName:  "用户",
			ResetURL:  "https://app.example.com/reset?token=abc",
			ExpiresIn: "15 分钟",
		})
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if !strings.Contains(msg.Subject, "重置您的密码") {
			t.Errorf("subject: %q", msg.Subject)
		}
		if !strings.Contains(msg.TextBody, "https://app.example.com/reset?token=abc") {
			t.Error("text body missing reset_url")
		}
		if !strings.Contains(msg.HTMLBody, "重置密码") {
			t.Error("html body missing reset CTA")
		}
	})
}

func TestRenderEmail_ExtraFieldsEscaped(t *testing.T) {
	withEmailEnabled(t, true, func() {
		msg, err := RenderEmail(EmailTplNotification, "zh", EmailVars{
			UserName: "u",
			AppName:  "DAOF-CPA",
			Extra: map[string]string{
				"notif_title": "<b>恶意标题</b>",
				"notif_body":  "<script>steal()</script> 普通正文",
			},
		})
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		// HTML body 必须 escape extra
		if strings.Contains(msg.HTMLBody, "<script>steal()</script>") {
			t.Error("Extra fields not escaped in HTML body")
		}
		if !strings.Contains(msg.HTMLBody, "&lt;script&gt;") {
			t.Error("Extra fields should be HTML-escaped")
		}
		// text body 不转义
		if !strings.Contains(msg.TextBody, "<script>steal()</script>") {
			t.Error("text body should NOT escape extra fields")
		}
	})
}

func TestRenderEmail_FallbackZHToEN(t *testing.T) {
	withEmailEnabled(t, true, func() {
		// admin 仅配置了 en，没有 zh → zh request 应回退到 en
		SysConfigMutex.Lock()
		// 强制清空可能存在的 _zh override
		delete(SysConfigCache, "email_verify_subject_zh")
		SysConfigCache["email_verify_subject_en"] = "EN Only Subject"
		SysConfigMutex.Unlock()

		msg, err := RenderEmail(EmailTplVerify, "zh", EmailVars{UserName: "u"})
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		// pickEmailString: zh 缺失 → 退 en SysConfig（不是默认 zh），所以应该是 "EN Only Subject"
		if msg.Subject != "EN Only Subject" {
			t.Errorf("expected zh-to-en fallback, got %q", msg.Subject)
		}
	})
}

func TestAppNameFromConfig(t *testing.T) {
	SysConfigMutex.Lock()
	prev := SysConfigCache
	SysConfigCache = map[string]string{}
	SysConfigMutex.Unlock()
	defer func() {
		SysConfigMutex.Lock()
		SysConfigCache = prev
		SysConfigMutex.Unlock()
	}()

	// 缺失时默认 DAOF-CPA
	if got := AppNameFromConfig(); got != "DAOF-CPA" {
		t.Errorf("empty config got %q want DAOF-CPA", got)
	}

	// 有值时返回 SysConfig 值
	SysConfigMutex.Lock()
	SysConfigCache["site_name"] = "My Site"
	SysConfigMutex.Unlock()
	if got := AppNameFromConfig(); got != "My Site" {
		t.Errorf("got %q want 'My Site'", got)
	}
}

func TestCurrentYear(t *testing.T) {
	y := currentYear()
	if len(y) != 4 {
		t.Errorf("year string should be 4 digits, got %q", y)
	}
	for _, ch := range y {
		if ch < '0' || ch > '9' {
			t.Errorf("year contains non-digit: %q", y)
		}
	}
}
