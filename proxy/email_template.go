// Package proxy / email_template.go
//
// 邮件模板渲染 + i18n。Phase G-1.3（2026-05-20）。
//
// 设计要点：
//  1. **i18n**：每个模板有 zh-CN / en-US 两套 subject / text / html，按 Accept-Language
//     选择（zh* → zh，其他 → en）。复用 moderation_response.PickLocalizedMessage 模式。
//  2. **占位符**：模板里写 {var_name}，业务层填 EmailVars 后由 renderTemplate 替换。
//     untrusted 字段（user_name / user_email 等）在写入 HTML body 之前**必须**
//     先 html.EscapeString —— 防 admin 文案占位符填的是用户输入。
//     URL / 应用生成的 string（app_name / year）来源可信，不做转义。
//  3. **admin 可改文案**：模板字符串从 SysConfig 读取，admin 改 zh/en/subject/text/html
//     即时生效；缺失时回退到 SeedEmailDefaults 里的硬编码默认值。
//  4. **HTML 安全**：默认模板使用极简结构（H1 + 段落 + 唯一一个 a 标签）。admin 若想
//     塞 <script>/iframe，自伤而已（admin 信任域）。本模块不做 sanitize。
//  5. **kill switch**：master flag email_enabled=false 时 RenderEmail 返回 ErrEmailDisabled。
package proxy

import (
	"errors"
	"fmt"
	"html"
	"strings"
	"time"
)

// EmailTemplateName 是已注册模板的标识。新增模板必须在 emailTemplateRegistry 注册。
type EmailTemplateName string

const (
	EmailTplVerify        EmailTemplateName = "verify_email"
	EmailTplResetPassword EmailTemplateName = "reset_password"
	EmailTplSetPassword   EmailTemplateName = "set_password"
	EmailTplNotification  EmailTemplateName = "notification" // 通用通知 channel
)

// EmailVars 是模板可填的变量。所有字段都是 optional —— 没填的占位符保留原样
// （便于排查 "占位符没替换" 的情况，而不是静默丢字段）。
type EmailVars struct {
	UserName  string // untrusted：HTML body 必须先 escape
	UserEmail string // untrusted：HTML body 必须先 escape
	VerifyURL string // 可信：app 生成的完整 https:// URL
	ResetURL  string // 可信：同上
	ExpiresIn string // 可信：如 "1 小时" / "1 hour"
	AppName   string // 可信：来自 SysConfig site_name
	// 自由字段：业务侧再传 key-value 字段；HTML body 也按 untrusted 处理（escape）。
	Extra map[string]string
}

// ErrEmailDisabled 是 master kill-switch 未开启时 RenderEmail 返回的 sentinel。
var ErrEmailDisabled = errors.New("email feature disabled (SysConfig email_enabled != true)")

// ErrEmailTemplateUnknown 是请求未注册模板时的 sentinel。
var ErrEmailTemplateUnknown = errors.New("unknown email template name")

// SysConfig key constants（admin 可改的文案 key）
const (
	emailConfigKeyEnabled = "email_enabled"
	emailConfigKeySiteName = "site_name"
)

// RenderEmail 根据 name + locale + vars 渲染出可发送的 EmailMessage（不含 To，由 caller 填）。
//
// locale: "zh" / "en" / "" （空串默认 zh）。建议从 Accept-Language 头或 user.Locale 派生。
func RenderEmail(name EmailTemplateName, locale string, vars EmailVars) (EmailMessage, error) {
	if !IsEmailEnabled() {
		return EmailMessage{}, ErrEmailDisabled
	}
	def, ok := lookupEmailTemplate(name)
	if !ok {
		return EmailMessage{}, fmt.Errorf("%w: %q", ErrEmailTemplateUnknown, name)
	}

	wantZh := isZhLocale(locale)
	subject := pickEmailString(def.SubjectKey, def.DefaultSubjectZH, def.DefaultSubjectEN, wantZh)
	text := pickEmailString(def.TextKey, def.DefaultTextZH, def.DefaultTextEN, wantZh)
	htmlBody := pickEmailString(def.HTMLKey, def.DefaultHTMLZH, def.DefaultHTMLEN, wantZh)

	subject = substituteTextVars(subject, vars)
	text = substituteTextVars(text, vars)
	htmlBody = substituteHTMLVars(htmlBody, vars)

	return EmailMessage{
		Subject:  subject,
		TextBody: text,
		HTMLBody: htmlBody,
		// MessageID 由 caller（队列层）填，便于去重；这里不生成
	}, nil
}

// IsEmailEnabled 是 master kill switch。admin 把 email_enabled 设为 "true"/"1" 才放行
// 任何邮件发送 / 渲染 / 绑定流程。其他值（""/"false"/"0"/缺失）一律视为关闭。
func IsEmailEnabled() bool {
	SysConfigMutex.RLock()
	v := strings.ToLower(strings.TrimSpace(SysConfigCache[emailConfigKeyEnabled]))
	SysConfigMutex.RUnlock()
	return v == "true" || v == "1"
}

// isZhLocale 判断 locale 字符串是否表示中文。
// 接受 "zh", "zh-CN", "zh_TW", "ZH" 等大小写、地区变体。
func isZhLocale(locale string) bool {
	l := strings.ToLower(strings.TrimSpace(locale))
	if l == "" {
		return true // 默认 zh
	}
	return strings.HasPrefix(l, "zh")
}

// pickEmailString 按 locale 优先读 SysConfig 覆盖；缺失/空白时回退到硬编码默认。
func pickEmailString(baseKey, defaultZH, defaultEN string, wantZh bool) string {
	if baseKey == "" {
		if wantZh {
			return defaultZH
		}
		return defaultEN
	}
	zhKey := baseKey + "_zh"
	enKey := baseKey + "_en"
	SysConfigMutex.RLock()
	zhVal := strings.TrimSpace(SysConfigCache[zhKey])
	enVal := strings.TrimSpace(SysConfigCache[enKey])
	SysConfigMutex.RUnlock()
	if wantZh {
		if zhVal != "" {
			return zhVal
		}
		// zh 缺失：退回 en SysConfig；en 也缺再回硬编码 zh 默认（防丢失文案）
		if enVal != "" {
			return enVal
		}
		return defaultZH
	}
	if enVal != "" {
		return enVal
	}
	if zhVal != "" {
		return zhVal
	}
	return defaultEN
}

// substituteTextVars 把 {var} 占位替换为 vars 对应字段（纯文本上下文，不转义）。
// text body / subject 都走这里。
func substituteTextVars(tmpl string, vars EmailVars) string {
	tmpl = strings.ReplaceAll(tmpl, "{user_name}", vars.UserName)
	tmpl = strings.ReplaceAll(tmpl, "{user_email}", vars.UserEmail)
	tmpl = strings.ReplaceAll(tmpl, "{verify_url}", vars.VerifyURL)
	tmpl = strings.ReplaceAll(tmpl, "{reset_url}", vars.ResetURL)
	tmpl = strings.ReplaceAll(tmpl, "{expires_in}", vars.ExpiresIn)
	tmpl = strings.ReplaceAll(tmpl, "{app_name}", vars.AppName)
	tmpl = strings.ReplaceAll(tmpl, "{year}", currentYear())
	for k, v := range vars.Extra {
		tmpl = strings.ReplaceAll(tmpl, "{"+k+"}", v)
	}
	return tmpl
}

// substituteHTMLVars 同 substituteTextVars，但对 untrusted 字段做 HTML 转义。
//
// 安全要点：
//   - UserName / UserEmail / Extra[*] → html.EscapeString（防 XSS）
//   - VerifyURL / ResetURL → 不转义（URL 本身可信，转义会破坏 query string 里的 &）
//     调用方必须保证传入的 URL 已经 url-encode 过任何 query 参数（substituteHTMLVars
//     不二次 url-encode）。如果 URL 来源是用户输入（不该是这种情况！）必须 caller 在传入前
//     验证 scheme=https + host 在白名单内。
//   - AppName / ExpiresIn / Year → 来自 SysConfig 或 Go time.Format，可信
func substituteHTMLVars(tmpl string, vars EmailVars) string {
	tmpl = strings.ReplaceAll(tmpl, "{user_name}", html.EscapeString(vars.UserName))
	tmpl = strings.ReplaceAll(tmpl, "{user_email}", html.EscapeString(vars.UserEmail))
	tmpl = strings.ReplaceAll(tmpl, "{verify_url}", vars.VerifyURL)
	tmpl = strings.ReplaceAll(tmpl, "{reset_url}", vars.ResetURL)
	tmpl = strings.ReplaceAll(tmpl, "{expires_in}", html.EscapeString(vars.ExpiresIn))
	tmpl = strings.ReplaceAll(tmpl, "{app_name}", html.EscapeString(vars.AppName))
	tmpl = strings.ReplaceAll(tmpl, "{year}", currentYear())
	for k, v := range vars.Extra {
		tmpl = strings.ReplaceAll(tmpl, "{"+k+"}", html.EscapeString(v))
	}
	return tmpl
}

func currentYear() string {
	return fmt.Sprintf("%d", time.Now().UTC().Year())
}

// emailTemplateDef 是单个模板的元数据 + 默认文案。
//
// key 命名约定：
//   - SubjectKey: "email_<name>_subject"，最终读 "_zh" 或 "_en" 后缀
//   - TextKey:    "email_<name>_text"
//   - HTMLKey:    "email_<name>_html"
//
// SysConfig 里 admin 只要 SET key="email_verify_subject_zh" value="新主题" 即可覆盖。
type emailTemplateDef struct {
	SubjectKey       string
	TextKey          string
	HTMLKey          string
	DefaultSubjectZH string
	DefaultSubjectEN string
	DefaultTextZH    string
	DefaultTextEN    string
	DefaultHTMLZH    string
	DefaultHTMLEN    string
}

func lookupEmailTemplate(name EmailTemplateName) (emailTemplateDef, bool) {
	def, ok := emailTemplateRegistry[name]
	return def, ok
}

// 默认 HTML 框架：极简、内联 style、单 CTA 按钮。客户端兼容性优先。
const emailHTMLShellZH = `<!DOCTYPE html>
<html lang="zh-CN"><head><meta charset="UTF-8"><title>{app_name}</title></head>
<body style="font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif;background:#f6f8fa;margin:0;padding:24px;color:#24292f;">
<div style="max-width:560px;margin:0 auto;background:#ffffff;border:1px solid #d0d7de;border-radius:6px;padding:32px;">
<h1 style="margin:0 0 16px;font-size:20px;font-weight:600;">{HEADING}</h1>
<p style="margin:0 0 16px;line-height:1.6;">{BODY}</p>
{CTA}
<p style="margin:24px 0 0;font-size:12px;color:#656d76;">{FOOTER}</p>
</div>
<p style="text-align:center;font-size:12px;color:#656d76;margin-top:24px;">&copy; {year} {app_name}</p>
</body></html>`

const emailHTMLShellEN = `<!DOCTYPE html>
<html lang="en"><head><meta charset="UTF-8"><title>{app_name}</title></head>
<body style="font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif;background:#f6f8fa;margin:0;padding:24px;color:#24292f;">
<div style="max-width:560px;margin:0 auto;background:#ffffff;border:1px solid #d0d7de;border-radius:6px;padding:32px;">
<h1 style="margin:0 0 16px;font-size:20px;font-weight:600;">{HEADING}</h1>
<p style="margin:0 0 16px;line-height:1.6;">{BODY}</p>
{CTA}
<p style="margin:24px 0 0;font-size:12px;color:#656d76;">{FOOTER}</p>
</div>
<p style="text-align:center;font-size:12px;color:#656d76;margin-top:24px;">&copy; {year} {app_name}</p>
</body></html>`

const emailCTAButtonZH = `<p style="margin:24px 0;text-align:center;">
<a href="{verify_url}" style="display:inline-block;background:#0969da;color:#ffffff;text-decoration:none;padding:10px 24px;border-radius:6px;font-weight:500;">点击验证</a>
</p>`

const emailCTAButtonZHReset = `<p style="margin:24px 0;text-align:center;">
<a href="{reset_url}" style="display:inline-block;background:#0969da;color:#ffffff;text-decoration:none;padding:10px 24px;border-radius:6px;font-weight:500;">重置密码</a>
</p>`

const emailCTAButtonEN = `<p style="margin:24px 0;text-align:center;">
<a href="{verify_url}" style="display:inline-block;background:#0969da;color:#ffffff;text-decoration:none;padding:10px 24px;border-radius:6px;font-weight:500;">Verify Email</a>
</p>`

const emailCTAButtonENReset = `<p style="margin:24px 0;text-align:center;">
<a href="{reset_url}" style="display:inline-block;background:#0969da;color:#ffffff;text-decoration:none;padding:10px 24px;border-radius:6px;font-weight:500;">Reset Password</a>
</p>`

// buildShell 用 shell 模板（已 i18n）替换 HEADING/BODY/CTA/FOOTER 占位生成 HTML。
// 注：这里只是模板里的"二级占位"，跟用户变量占位（{user_name} 等）是两个层级。
func buildShell(shell, heading, body, cta, footer string) string {
	out := shell
	out = strings.ReplaceAll(out, "{HEADING}", heading)
	out = strings.ReplaceAll(out, "{BODY}", body)
	out = strings.ReplaceAll(out, "{CTA}", cta)
	out = strings.ReplaceAll(out, "{FOOTER}", footer)
	return out
}

// emailTemplateRegistry 是模板注册表。新增模板在这里加一条 + 写好默认 zh/en 文案。
var emailTemplateRegistry = map[EmailTemplateName]emailTemplateDef{
	EmailTplVerify: {
		SubjectKey:       "email_verify_subject",
		TextKey:          "email_verify_text",
		HTMLKey:          "email_verify_html",
		DefaultSubjectZH: "【{app_name}】请验证您的邮箱",
		DefaultSubjectEN: "[{app_name}] Verify your email address",
		DefaultTextZH: "您好 {user_name}，\n\n" +
			"请点击以下链接验证您的邮箱（{user_email}）：\n\n" +
			"{verify_url}\n\n" +
			"链接 {expires_in} 内有效。如果不是您本人操作，请忽略此邮件。\n\n" +
			"—— {app_name}",
		DefaultTextEN: "Hi {user_name},\n\n" +
			"Please click the link below to verify your email ({user_email}):\n\n" +
			"{verify_url}\n\n" +
			"The link expires in {expires_in}. If you did not request this, ignore this email.\n\n" +
			"—— {app_name}",
		DefaultHTMLZH: buildShell(emailHTMLShellZH,
			"请验证您的邮箱",
			"您好 {user_name}，<br>请点击下方按钮验证您的邮箱（{user_email}）。",
			emailCTAButtonZH,
			"链接 {expires_in} 内有效。如果不是您本人操作，请忽略此邮件。"),
		DefaultHTMLEN: buildShell(emailHTMLShellEN,
			"Verify your email",
			"Hi {user_name},<br>Please click the button below to verify {user_email}.",
			emailCTAButtonEN,
			"The link expires in {expires_in}. If you did not request this, ignore this email."),
	},
	EmailTplResetPassword: {
		SubjectKey:       "email_reset_password_subject",
		TextKey:          "email_reset_password_text",
		HTMLKey:          "email_reset_password_html",
		DefaultSubjectZH: "【{app_name}】重置您的密码",
		DefaultSubjectEN: "[{app_name}] Reset your password",
		DefaultTextZH: "您好 {user_name}，\n\n" +
			"我们收到了重置密码的请求。请点击以下链接设置新密码：\n\n" +
			"{reset_url}\n\n" +
			"链接 {expires_in} 内有效。如果不是您本人操作，请忽略此邮件，您的密码不会被修改。\n\n" +
			"—— {app_name}",
		DefaultTextEN: "Hi {user_name},\n\n" +
			"We received a password reset request. Click below to set a new password:\n\n" +
			"{reset_url}\n\n" +
			"The link expires in {expires_in}. If you did not request this, ignore this email; your password will not change.\n\n" +
			"—— {app_name}",
		DefaultHTMLZH: buildShell(emailHTMLShellZH,
			"重置您的密码",
			"您好 {user_name}，<br>我们收到了重置密码请求。",
			emailCTAButtonZHReset,
			"链接 {expires_in} 内有效。如果不是您本人操作，请忽略此邮件，您的密码不会被修改。"),
		DefaultHTMLEN: buildShell(emailHTMLShellEN,
			"Reset your password",
			"Hi {user_name},<br>We received a password reset request.",
			emailCTAButtonENReset,
			"The link expires in {expires_in}. If you did not request this, ignore this email; your password will not change."),
	},
	EmailTplSetPassword: {
		SubjectKey:       "email_set_password_subject",
		TextKey:          "email_set_password_text",
		HTMLKey:          "email_set_password_html",
		DefaultSubjectZH: "【{app_name}】启用邮箱登录",
		DefaultSubjectEN: "[{app_name}] Enable email login",
		DefaultTextZH: "您好 {user_name}，\n\n" +
			"您请求启用邮箱+密码登录。请点击以下链接设置密码：\n\n" +
			"{reset_url}\n\n" +
			"链接 {expires_in} 内有效。设置完成后即可用邮箱 {user_email} 登录。\n\n" +
			"—— {app_name}",
		DefaultTextEN: "Hi {user_name},\n\n" +
			"You requested to enable email + password login. Click below to set your password:\n\n" +
			"{reset_url}\n\n" +
			"The link expires in {expires_in}. Once set, you can log in with {user_email}.\n\n" +
			"—— {app_name}",
		DefaultHTMLZH: buildShell(emailHTMLShellZH,
			"启用邮箱登录",
			"您好 {user_name}，<br>请点击下方按钮为账号 {user_email} 设置密码。",
			emailCTAButtonZHReset,
			"链接 {expires_in} 内有效。"),
		DefaultHTMLEN: buildShell(emailHTMLShellEN,
			"Enable email login",
			"Hi {user_name},<br>Click below to set a password for {user_email}.",
			emailCTAButtonENReset,
			"The link expires in {expires_in}."),
	},
	EmailTplNotification: {
		SubjectKey:       "email_notification_subject",
		TextKey:          "email_notification_text",
		HTMLKey:          "email_notification_html",
		// 通用通知模板：subject + body 由 caller 通过 Extra 字段填充
		DefaultSubjectZH: "【{app_name}】{notif_title}",
		DefaultSubjectEN: "[{app_name}] {notif_title}",
		DefaultTextZH: "{user_name} 您好，\n\n" +
			"{notif_body}\n\n" +
			"—— {app_name}",
		DefaultTextEN: "Hi {user_name},\n\n" +
			"{notif_body}\n\n" +
			"—— {app_name}",
		DefaultHTMLZH: buildShell(emailHTMLShellZH,
			"{notif_title}",
			"{user_name} 您好，<br>{notif_body}",
			"",
			"您可登录控制台调整邮件通知偏好。"),
		DefaultHTMLEN: buildShell(emailHTMLShellEN,
			"{notif_title}",
			"Hi {user_name},<br>{notif_body}",
			"",
			"Adjust your email notification preferences in your account settings."),
	},
}

// AppNameFromConfig 从 SysConfig 取 site_name，默认 "DAOF-CPA"。
// caller 应该把这个值传到 EmailVars.AppName。
func AppNameFromConfig() string {
	SysConfigMutex.RLock()
	v := strings.TrimSpace(SysConfigCache[emailConfigKeySiteName])
	SysConfigMutex.RUnlock()
	if v == "" {
		return "DAOF-CPA"
	}
	return v
}
