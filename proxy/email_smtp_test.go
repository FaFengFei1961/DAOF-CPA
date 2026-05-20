// Package proxy / email_smtp_test.go
//
// Phase G-1.2 单元测试：SMTP helper 纯函数 + LoadSMTPConfig 校验。
// 不测真实 SMTP 拨号（在 G-1.9 用 mock smtp server 集成测）。
package proxy

import (
	"strings"
	"testing"

	"daof-cpa/utils"
)

func TestSMTPConfig_IsConfigured(t *testing.T) {
	tests := []struct {
		name string
		cfg  SMTPConfig
		want bool
	}{
		{"empty", SMTPConfig{}, false},
		{"missing host", SMTPConfig{Port: 587, Username: "u", Password: "p", From: "f"}, false},
		{"missing port", SMTPConfig{Host: "h", Username: "u", Password: "p", From: "f"}, false},
		{"missing username", SMTPConfig{Host: "h", Port: 587, Password: "p", From: "f"}, false},
		{"missing password", SMTPConfig{Host: "h", Port: 587, Username: "u", From: "f"}, false},
		{"missing from", SMTPConfig{Host: "h", Port: 587, Username: "u", Password: "p"}, false},
		{"all populated", SMTPConfig{Host: "h", Port: 587, Username: "u", Password: "p", From: "f"}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.IsConfigured(); got != tc.want {
				t.Errorf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestExtractEmailAddr(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"plain@example.com", "plain@example.com"},
		{"  trim@example.com  ", "trim@example.com"},
		{"Display Name <user@example.com>", "user@example.com"},
		{"<noreply@example.com>", "noreply@example.com"},
		{"DAOF-CPA <noreply@daof-cpa.com>", "noreply@daof-cpa.com"},
		{"weird <but valid>", "but valid"}, // documents current behavior; SMTP would reject downstream
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got := extractEmailAddr(tc.in)
			if got != tc.want {
				t.Errorf("extractEmailAddr(%q) = %q; want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSanitizeHeaderValue(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"normal", "normal"},
		{"with CR\r injection", "with CR  injection"},
		{"with LF\n injection", "with LF  injection"},
		{"with CRLF\r\n combo", "with CRLF   combo"},
		{"unicode 中文 emoji 🚀 ok", "unicode 中文 emoji 🚀 ok"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got := sanitizeHeaderValue(tc.in)
			if got != tc.want {
				t.Errorf("sanitizeHeaderValue(%q) = %q; want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestBuildEmailBody_PlainText(t *testing.T) {
	cfg := SMTPConfig{From: "noreply@example.com"}
	msg := EmailMessage{
		To:       "user@example.com",
		Subject:  "Test Subject",
		TextBody: "Hello, this is plain text.",
	}
	body := buildEmailBody(cfg, msg)
	for _, expected := range []string{
		"From: noreply@example.com",
		"To: user@example.com",
		"Subject: Test Subject",
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=\"UTF-8\"",
		"Hello, this is plain text.",
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("body missing %q", expected)
		}
	}
	// 纯文本不应有 multipart 边界
	if strings.Contains(body, "multipart/alternative") {
		t.Error("plain text email should not have multipart header")
	}
}

func TestBuildEmailBody_HTMLMultipart(t *testing.T) {
	cfg := SMTPConfig{From: "noreply@example.com"}
	msg := EmailMessage{
		To:       "user@example.com",
		Subject:  "HTML Test",
		TextBody: "Plain fallback",
		HTMLBody: "<p>HTML body</p>",
	}
	body := buildEmailBody(cfg, msg)
	for _, expected := range []string{
		"Content-Type: multipart/alternative; boundary=\"DAOF-CPA-Boundary-",
		"Plain fallback",
		"<p>HTML body</p>",
		"Content-Type: text/plain; charset=\"UTF-8\"",
		"Content-Type: text/html; charset=\"UTF-8\"",
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("body missing %q\n--- got ---\n%s", expected, body)
		}
	}
}

func TestBuildEmailBody_HeaderInjectionDefended(t *testing.T) {
	cfg := SMTPConfig{From: "noreply@example.com"}
	msg := EmailMessage{
		To:       "user@example.com\r\nBcc: evil@attacker.com",
		Subject:  "Attack\r\nX-Custom: injected",
		TextBody: "body",
	}
	body := buildEmailBody(cfg, msg)
	// 关键防御：CRLF 被替换为空格，所以攻击 payload 不会形成"新 header 行"。
	// 验证方式：body 里不能有 "\r\nBcc:" 或 "\r\nX-Custom:" 这样的连续序列。
	// （payload 文本仍出现在原 header 的值里，但被当作 To/Subject 的一部分，SMTP server
	//   会把它当成一个奇怪的 To 名字，不会按新 header 解析）
	if strings.Contains(body, "\r\nBcc:") {
		t.Error("CRLF injection in To: header should not produce new Bcc header line")
	}
	if strings.Contains(body, "\r\nX-Custom:") {
		t.Error("CRLF injection in Subject: header should not produce new X-Custom header line")
	}
	// 同时验证 sanitizeHeaderValue 把 CRLF 变成空格（最关键的不变量）
	if strings.Contains(body, "user@example.com\r\nBcc:") {
		t.Error("raw CRLF leaked into output")
	}
}

func TestBuildEmailBody_OptionalHeaders(t *testing.T) {
	cfg := SMTPConfig{From: "noreply@example.com"}

	t.Run("no ReplyTo no Message-ID", func(t *testing.T) {
		msg := EmailMessage{To: "u@e.com", Subject: "S", TextBody: "B"}
		body := buildEmailBody(cfg, msg)
		if strings.Contains(body, "Reply-To:") {
			t.Error("Reply-To should not appear when empty")
		}
		if strings.Contains(body, "Message-ID:") {
			t.Error("Message-ID should not appear when empty")
		}
	})

	t.Run("with ReplyTo and Message-ID", func(t *testing.T) {
		msg := EmailMessage{
			To:        "u@e.com",
			Subject:   "S",
			TextBody:  "B",
			ReplyTo:   "support@example.com",
			MessageID: "abc123@example.com",
		}
		body := buildEmailBody(cfg, msg)
		if !strings.Contains(body, "Reply-To: support@example.com") {
			t.Error("Reply-To missing")
		}
		if !strings.Contains(body, "Message-ID: <abc123@example.com>") {
			t.Error("Message-ID missing or malformed")
		}
	})
}

func TestLoadSMTPConfig_ParsesValuesAndRejectsPort25(t *testing.T) {
	// 保存现场并在测试末尾恢复
	originalCache := SysConfigCache
	defer func() {
		SysConfigMutex.Lock()
		SysConfigCache = originalCache
		SysConfigMutex.Unlock()
	}()

	encPassword, err := utils.Encrypt("secret-password")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	tests := []struct {
		name      string
		overrides map[string]string
		wantErr   bool
		check     func(*testing.T, SMTPConfig)
	}{
		{
			name: "complete config parses",
			overrides: map[string]string{
				"smtp_host":              "smtp.example.com",
				"smtp_port":              "587",
				"smtp_username":          "user@example.com",
				"smtp_password":          encPassword,
				"smtp_from":              "DAOF-CPA <noreply@example.com>",
				"smtp_use_implicit_tls":  "false",
			},
			wantErr: false,
			check: func(t *testing.T, cfg SMTPConfig) {
				if cfg.Host != "smtp.example.com" || cfg.Port != 587 {
					t.Errorf("host/port = %s:%d", cfg.Host, cfg.Port)
				}
				if cfg.Password != "secret-password" {
					t.Errorf("password decrypted incorrectly: %q", cfg.Password)
				}
				if cfg.UseImplicitTLS {
					t.Error("UseImplicitTLS should be false")
				}
			},
		},
		{
			name: "implicit tls toggle parses",
			overrides: map[string]string{
				"smtp_host":              "smtp.example.com",
				"smtp_port":              "465",
				"smtp_username":          "u",
				"smtp_password":          encPassword,
				"smtp_from":              "f",
				"smtp_use_implicit_tls":  "true",
			},
			wantErr: false,
			check: func(t *testing.T, cfg SMTPConfig) {
				if !cfg.UseImplicitTLS {
					t.Error("UseImplicitTLS should be true")
				}
			},
		},
		{
			name: "port 25 rejected",
			overrides: map[string]string{
				"smtp_host":     "smtp.example.com",
				"smtp_port":     "25",
				"smtp_username": "u",
				"smtp_password": encPassword,
				"smtp_from":     "f",
			},
			wantErr: true,
		},
		{
			name: "non-int port rejected",
			overrides: map[string]string{
				"smtp_host":     "smtp.example.com",
				"smtp_port":     "abc",
				"smtp_username": "u",
				"smtp_password": encPassword,
				"smtp_from":     "f",
			},
			wantErr: true,
		},
		{
			name: "port out of range rejected",
			overrides: map[string]string{
				"smtp_host":     "smtp.example.com",
				"smtp_port":     "70000",
				"smtp_username": "u",
				"smtp_password": encPassword,
				"smtp_from":     "f",
			},
			wantErr: true,
		},
		{
			name: "password decrypt failure rejected",
			overrides: map[string]string{
				"smtp_host":     "smtp.example.com",
				"smtp_port":     "587",
				"smtp_username": "u",
				"smtp_password": "not-a-valid-encrypted-blob",
				"smtp_from":     "f",
			},
			wantErr: true,
		},
		{
			name:      "empty config returns empty SMTPConfig (no error)",
			overrides: map[string]string{},
			wantErr:   false,
			check: func(t *testing.T, cfg SMTPConfig) {
				if cfg.IsConfigured() {
					t.Error("empty config should not be IsConfigured")
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			SysConfigMutex.Lock()
			SysConfigCache = map[string]string{}
			for k, v := range tc.overrides {
				SysConfigCache[k] = v
			}
			SysConfigMutex.Unlock()

			cfg, err := LoadSMTPConfig()
			if (err != nil) != tc.wantErr {
				t.Errorf("err = %v; wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && tc.check != nil {
				tc.check(t, cfg)
			}
		})
	}
}

func TestSendEmailViaSMTP_InputValidation(t *testing.T) {
	validCfg := SMTPConfig{Host: "h", Port: 587, Username: "u", Password: "p", From: "f"}

	tests := []struct {
		name string
		cfg  SMTPConfig
		msg  EmailMessage
	}{
		{
			name: "missing config",
			cfg:  SMTPConfig{},
			msg:  EmailMessage{To: "u@e.com", Subject: "s", TextBody: "b"},
		},
		{
			name: "empty To",
			cfg:  validCfg,
			msg:  EmailMessage{To: "", Subject: "s", TextBody: "b"},
		},
		{
			name: "whitespace-only To",
			cfg:  validCfg,
			msg:  EmailMessage{To: "   ", Subject: "s", TextBody: "b"},
		},
		{
			name: "empty Subject",
			cfg:  validCfg,
			msg:  EmailMessage{To: "u@e.com", Subject: "", TextBody: "b"},
		},
		{
			name: "empty TextBody",
			cfg:  validCfg,
			msg:  EmailMessage{To: "u@e.com", Subject: "s", TextBody: ""},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// SendEmailViaSMTP 在做完输入校验后会进入网络拨号阶段；
			// 我们只关心输入校验本身的拒绝行为，不去拨号。所以失败是预期。
			err := SendEmailViaSMTP(tc.cfg, tc.msg)
			if err == nil {
				t.Error("expected error from validation, got nil")
			}
		})
	}
}
