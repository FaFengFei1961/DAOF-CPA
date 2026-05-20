// Package proxy / email_smtp.go
//
// SMTP 客户端封装。Phase G-1.2（2026-05-19）。
//
// 设计要点：
//  1. 凭据：所有 SMTP 配置项从 SysConfigCache 读取；password 列加密存储（utils.Encrypt），
//     使用时解密。在 admin 修改配置时不回显密码明文。
//  2. SSRF 防护：拨号走 ssrfSafeSMTPDialContext —— 复用 url_safety.go 里的 isForbiddenDestIP
//     拒绝内网 / 元数据 / 链路本地 / 回环。即使 admin 误把 SMTP host 配成
//     169.254.169.254 也不会泄漏 STARTTLS 中的 password。
//  3. 强制 TLS：SMTP 配置必须启用 TLS（implicit 465 或 STARTTLS 587）。明文 25 端口
//     永远禁止，避免 password / 邮件正文 plain-text 经过任何 hop。
//  4. 邮件内容：通过 EmailMessage 结构传入；本模块只负责"传输"，不负责模板渲染（在
//     proxy/email_template.go 中处理）。
//
// 不做的事（避免重蹈 SMTP 库的复杂性轮子）：
//  - 不支持 OAuth2 SMTP（gmail 等可用 app password）
//  - 不支持 SMTP relay chain / DKIM 签名（由 SMTP 服务商负责）
//  - 不支持本地 outbox / 失败重试（在 G-1.4 异步队列里处理）
//
// 测试：通过 net/smtp 接口 mock（参见 proxy/email_smtp_test.go）。
package proxy

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"daof-cpa/utils"
)

// EmailMessage 是一封待发送邮件的内存表示。模板渲染（subject / body）由 caller 完成。
//
// 安全约定：
//   - Subject / TextBody / HTMLBody 都应已经 HTML/header-injection 转义
//   - To 单收件人；批量发送（系统公告等）由 caller 循环单发，便于审计每封状态
type EmailMessage struct {
	To        string // 收件人邮箱（已小写规范化）
	Subject   string // 主题
	TextBody  string // 纯文本正文（必填，作为 HTML 不可达时的 fallback）
	HTMLBody  string // HTML 正文（可选）；空字符串表示纯文本邮件
	ReplyTo   string // 可选 Reply-To 头（admin 设置；空时不发送 Reply-To）
	MessageID string // 全局唯一 Message-ID（用于去重 / dedup；空时由 SMTP server 生成）
}

// SMTPConfig 是 SMTP 配置的内存对象。LoadSMTPConfig 从 SysConfigCache 解析后返回。
type SMTPConfig struct {
	Host         string // smtp.gmail.com 等
	Port         int    // 465 (SMTPS) / 587 (STARTTLS)
	Username     string // SMTP 登录名（通常 = From 地址，但不强制）
	Password     string // SMTP 登录密码（已解密，使用后清零）
	From         string // 邮件 From 头（"DAOF-CPA <noreply@example.com>" 或纯邮箱）
	UseImplicitTLS bool // true=直接 TLS（465 端口典型）；false=明文连接后 STARTTLS（587 端口典型）
	Timeout      time.Duration // 单次 SMTP 操作超时
}

// IsConfigured 判断 SMTP 配置是否齐备到可以发邮件。
func (c SMTPConfig) IsConfigured() bool {
	return c.Host != "" && c.Port > 0 &&
		c.Username != "" && c.Password != "" && c.From != ""
}

const (
	// SysConfig keys
	smtpConfigKeyHost         = "smtp_host"
	smtpConfigKeyPort         = "smtp_port"
	smtpConfigKeyUsername     = "smtp_username"
	smtpConfigKeyPassword     = "smtp_password" // 加密存储
	smtpConfigKeyFrom         = "smtp_from"
	smtpConfigKeyUseImplicitTLS = "smtp_use_implicit_tls"
	smtpConfigKeyReplyTo      = "smtp_reply_to"

	smtpDefaultTimeout = 15 * time.Second
)

// LoadSMTPConfig 从 SysConfigCache 解析 SMTP 配置。password 列会被 utils.Decrypt
// 解密。若 password 解密失败，返回错误（fail-closed：宁可发不出邮件，也不要回退到
// 明文密码或 silently 用空密码登录）。
func LoadSMTPConfig() (SMTPConfig, error) {
	SysConfigMutex.RLock()
	host := strings.TrimSpace(SysConfigCache[smtpConfigKeyHost])
	portRaw := strings.TrimSpace(SysConfigCache[smtpConfigKeyPort])
	username := strings.TrimSpace(SysConfigCache[smtpConfigKeyUsername])
	passwordEnc := strings.TrimSpace(SysConfigCache[smtpConfigKeyPassword])
	from := strings.TrimSpace(SysConfigCache[smtpConfigKeyFrom])
	useImplicitTLSRaw := strings.ToLower(strings.TrimSpace(SysConfigCache[smtpConfigKeyUseImplicitTLS]))
	SysConfigMutex.RUnlock()

	cfg := SMTPConfig{
		Host:           host,
		Username:       username,
		From:           from,
		UseImplicitTLS: useImplicitTLSRaw == "true" || useImplicitTLSRaw == "1",
		Timeout:        smtpDefaultTimeout,
	}

	if portRaw != "" {
		p, err := strconv.Atoi(portRaw)
		if err != nil {
			return SMTPConfig{}, fmt.Errorf("smtp_port not int: %w", err)
		}
		if p <= 0 || p > 65535 {
			return SMTPConfig{}, fmt.Errorf("smtp_port out of range: %d", p)
		}
		// fix CRITICAL：禁止明文 25 端口（password 会明文传输）。同时禁止
		// admin 误配 0/特权端口（<1024 通常是测试用，生产 SMTP 是 465/587/2525）。
		if p == 25 {
			return SMTPConfig{}, errors.New("smtp_port=25 is plain SMTP and forbidden; use 465 (implicit TLS) or 587 (STARTTLS)")
		}
		cfg.Port = p
	}

	if passwordEnc != "" {
		pwd, err := utils.Decrypt(passwordEnc)
		if err != nil {
			// 密码解密失败 → 配置损坏 → 发不出邮件。不要 silently 用空密码登录上游。
			return SMTPConfig{}, fmt.Errorf("decrypt smtp password: %w", err)
		}
		cfg.Password = pwd
	}

	return cfg, nil
}

// ssrfSafeSMTPDialContext 包装 net.Dial，强制 SMTP 服务器拨号目标不是内网 IP。
//
// SMTP 的 SSRF 模型比 LLM proxy 严格：admin 配置的 smtp_host 应该是外部 SMTP 服务商
// （Gmail/SendGrid 等），永远不应该指向 127.0.0.1/10.x/192.168.x/169.254 等。
// 因此用 yifut_client.go 的 isUnsafeIP（覆盖 loopback + RFC1918 + 链路本地 + 元数据），
// 比 url_safety.go 里 cliproxy 上下文的 isForbiddenDestIP 更严格。
//
// 即使 admin 设置 smtp_host=169.254.169.254（试图让 SMTP password 泄漏给云元数据），
// 这里也会在拨号阶段就报错拒绝。
func ssrfSafeSMTPDialContext(network, addr string, timeout time.Duration) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("split host:port %q: %w", addr, err)
	}
	// 字面量 IP：直接用 isUnsafeIP（loopback + RFC1918 + link-local + metadata 都拒绝）
	if ip := net.ParseIP(host); ip != nil {
		if isUnsafeIP(ip) {
			return nil, fmt.Errorf("smtp dial blocked: dest IP %s is forbidden (private/loopback/link-local/metadata)", ip)
		}
	}
	// 域名：先 dial，拨号成功后校验对端真实 IP（防 DNS rebinding）
	d := net.Dialer{Timeout: timeout}
	conn, err := d.Dial(network, addr)
	if err != nil {
		return nil, fmt.Errorf("smtp dial %s: %w", addr, err)
	}
	tcpAddr, ok := conn.RemoteAddr().(*net.TCPAddr)
	if ok && isUnsafeIP(tcpAddr.IP) {
		_ = conn.Close()
		return nil, fmt.Errorf("smtp dial blocked: connected to forbidden IP %s", tcpAddr.IP)
	}
	return conn, nil
}

// SendEmailViaSMTP 把 EmailMessage 发出去。同步阻塞（caller 应放进 goroutine）。
//
// 失败原因可能是：SMTP 配置缺失、拨号被 SSRF 防护拒、TLS 握手失败、AUTH 失败、
// 收件人被服务器拒绝等。所有错误都包装 cfg.Host:cfg.Port 上下文便于排查。
//
// 安全：错误信息中**不要**回显 cfg.Password 或邮件正文（避免日志/响应泄漏）。
func SendEmailViaSMTP(cfg SMTPConfig, msg EmailMessage) error {
	if !cfg.IsConfigured() {
		return errors.New("smtp not configured (host/port/username/password/from required)")
	}
	if strings.TrimSpace(msg.To) == "" {
		return errors.New("email message: recipient (To) is empty")
	}
	if strings.TrimSpace(msg.Subject) == "" {
		return errors.New("email message: subject is empty")
	}
	if strings.TrimSpace(msg.TextBody) == "" {
		return errors.New("email message: TextBody is required (HTML-only emails not allowed)")
	}

	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = smtpDefaultTimeout
	}

	// 拨号：implicit TLS 直接走 tls.Dial；STARTTLS 走 plain dial 后再升级
	var conn net.Conn
	var err error
	if cfg.UseImplicitTLS {
		rawConn, dialErr := ssrfSafeSMTPDialContext("tcp", addr, timeout)
		if dialErr != nil {
			return fmt.Errorf("smtp tls dial %s: %w", addr, dialErr)
		}
		tlsConn := tls.Client(rawConn, &tls.Config{
			ServerName: cfg.Host,
			MinVersion: tls.VersionTLS12,
		})
		if err := tlsConn.Handshake(); err != nil {
			_ = rawConn.Close()
			return fmt.Errorf("smtp tls handshake %s: %w", addr, err)
		}
		conn = tlsConn
	} else {
		conn, err = ssrfSafeSMTPDialContext("tcp", addr, timeout)
		if err != nil {
			return fmt.Errorf("smtp dial %s: %w", addr, err)
		}
	}
	defer func() { _ = conn.Close() }()

	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		return fmt.Errorf("smtp client init %s: %w", addr, err)
	}
	defer func() { _ = client.Close() }()

	// EHLO（smtp.NewClient 已调用一次；下面 STARTTLS 后还得再 EHLO）
	if !cfg.UseImplicitTLS {
		// STARTTLS：先看 server 是否支持
		ok, _ := client.Extension("STARTTLS")
		if !ok {
			return fmt.Errorf("smtp server %s does not advertise STARTTLS; refuse to send password in plain", addr)
		}
		if err := client.StartTLS(&tls.Config{
			ServerName: cfg.Host,
			MinVersion: tls.VersionTLS12,
		}); err != nil {
			return fmt.Errorf("smtp STARTTLS %s: %w", addr, err)
		}
	}

	auth := smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
	if err := client.Auth(auth); err != nil {
		// fix: 不要把 cfg.Password 写进 err 信息。Go SMTP lib 不会，但 wrap 时要小心。
		return fmt.Errorf("smtp auth %s username=%s: %w", addr, cfg.Username, err)
	}

	if err := client.Mail(extractEmailAddr(cfg.From)); err != nil {
		return fmt.Errorf("smtp MAIL FROM: %w", err)
	}
	if err := client.Rcpt(msg.To); err != nil {
		return fmt.Errorf("smtp RCPT TO: %w", err)
	}

	wc, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}
	if _, err := wc.Write([]byte(buildEmailBody(cfg, msg))); err != nil {
		_ = wc.Close()
		return fmt.Errorf("smtp body write: %w", err)
	}
	if err := wc.Close(); err != nil {
		return fmt.Errorf("smtp body close: %w", err)
	}

	if err := client.Quit(); err != nil {
		// QUIT 失败不影响"邮件已送到上游"的事实，但记日志
		return fmt.Errorf("smtp quit: %w", err)
	}
	return nil
}

// buildEmailBody 拼装符合 RFC 5322 的邮件原文（header + body）。
// 同时支持纯文本和 HTML（HTMLBody 非空时 multipart/alternative）。
func buildEmailBody(cfg SMTPConfig, msg EmailMessage) string {
	var b strings.Builder
	b.WriteString("From: " + sanitizeHeaderValue(cfg.From) + "\r\n")
	b.WriteString("To: " + sanitizeHeaderValue(msg.To) + "\r\n")
	b.WriteString("Subject: " + sanitizeHeaderValue(msg.Subject) + "\r\n")
	if msg.MessageID != "" {
		b.WriteString("Message-ID: <" + sanitizeHeaderValue(msg.MessageID) + ">\r\n")
	}
	if msg.ReplyTo != "" {
		b.WriteString("Reply-To: " + sanitizeHeaderValue(msg.ReplyTo) + "\r\n")
	}
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Date: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\n")

	if strings.TrimSpace(msg.HTMLBody) == "" {
		// 纯文本
		b.WriteString("Content-Type: text/plain; charset=\"UTF-8\"\r\n")
		b.WriteString("Content-Transfer-Encoding: 8bit\r\n")
		b.WriteString("\r\n")
		b.WriteString(msg.TextBody)
	} else {
		// multipart/alternative：客户端选 HTML，否则退到 text
		boundary := "DAOF-CPA-Boundary-" + strconv.FormatInt(time.Now().UnixNano(), 36)
		b.WriteString("Content-Type: multipart/alternative; boundary=\"" + boundary + "\"\r\n")
		b.WriteString("\r\n")

		b.WriteString("--" + boundary + "\r\n")
		b.WriteString("Content-Type: text/plain; charset=\"UTF-8\"\r\n")
		b.WriteString("Content-Transfer-Encoding: 8bit\r\n")
		b.WriteString("\r\n")
		b.WriteString(msg.TextBody)
		b.WriteString("\r\n")

		b.WriteString("--" + boundary + "\r\n")
		b.WriteString("Content-Type: text/html; charset=\"UTF-8\"\r\n")
		b.WriteString("Content-Transfer-Encoding: 8bit\r\n")
		b.WriteString("\r\n")
		b.WriteString(msg.HTMLBody)
		b.WriteString("\r\n")

		b.WriteString("--" + boundary + "--\r\n")
	}
	return b.String()
}

// sanitizeHeaderValue 抹掉 \r \n 防止 SMTP header injection。
// 真实 RFC 编码（如非 ASCII subject 应用 =?UTF-8?B?…?= encoding）目前简化为
// 直接保留 UTF-8 字节；大多数现代 SMTP server 接受 8-bit clean header。
func sanitizeHeaderValue(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}

// extractEmailAddr 从 "Name <addr@host>" 里提取 addr 部分。
// SMTP MAIL FROM 命令只接受 <addr>，不接受 "Name <addr>"。
func extractEmailAddr(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '<'); i >= 0 {
		if j := strings.IndexByte(s[i+1:], '>'); j > 0 {
			return s[i+1 : i+1+j]
		}
	}
	return s
}
