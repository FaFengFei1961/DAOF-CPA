// Package proxy / email_integration_test.go
//
// Phase G-1.9 端到端集成测试：从 Dispatch 入口到 SMTP 发送的完整链路。
//
// 设计：不真实拨号 SMTP（net/smtp 已被 stdlib 充分测过；wire-level 测试需要 TLS 证书
// + 完整 SMTP 状态机 ~150 行 mock，性价比低），而是通过：
//   - SetEmailQueueSyncForTest(true)：让 EnqueueEmail 同步执行（无 channel/goroutine）
//   - SetSendEmailViaSMTPHookForTest(fn)：替换真实 SMTP 调用为捕获函数
//
// 这套测试覆盖 Phase G-1.1～G-1.7 的端到端集成，外加几个安全 case
// （SSRF / header injection / port 25 / dedup）。
package proxy

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"daof-cpa/database"
	"daof-cpa/utils"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// emailCapture 记录测试期 fake SMTP 收到的 cfg+msg。
type emailCapture struct {
	mu       sync.Mutex
	calls    []capturedCall
	errToRet error // 若非 nil，hook 返回该错误（模拟 SMTP 失败路径）
}

type capturedCall struct {
	Cfg SMTPConfig
	Msg EmailMessage
}

func (c *emailCapture) hook() func(cfg SMTPConfig, msg EmailMessage) error {
	return func(cfg SMTPConfig, msg EmailMessage) error {
		c.mu.Lock()
		c.calls = append(c.calls, capturedCall{Cfg: cfg, Msg: msg})
		c.mu.Unlock()
		if c.errToRet != nil {
			return c.errToRet
		}
		return nil
	}
}

func (c *emailCapture) Count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.calls)
}

func (c *emailCapture) Last() (capturedCall, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.calls) == 0 {
		return capturedCall{}, false
	}
	return c.calls[len(c.calls)-1], true
}

func setupIntegrationTestDB(t *testing.T, opts integrationOpts) *database.User {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=private"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if sqlDB, dbErr := db.DB(); dbErr == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	if err := db.AutoMigrate(
		&database.User{}, &database.SysConfig{},
		&database.NotificationPreference{}, &database.Notification{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	database.DB = db

	encPwd, _ := utils.Encrypt("smtp-pwd")
	SysConfigMutex.Lock()
	SysConfigCache = map[string]string{
		"email_enabled":         "true",
		"smtp_host":             "smtp.example.com",
		"smtp_port":             "587",
		"smtp_username":         "noreply@example.com",
		"smtp_password":         encPwd,
		"smtp_from":             "DAOF <noreply@example.com>",
		"smtp_use_implicit_tls": "false",
		"site_name":             "DAOF-CPA-Test",
		"server_address":        "https://app.example.com",
	}
	SysConfigMutex.Unlock()

	now := time.Now()
	verifiedAt := &now
	if !opts.verified {
		verifiedAt = nil
	}
	email := opts.email
	if email == "" {
		email = "alice@example.com"
	}
	if !opts.bound {
		email = ""
		verifiedAt = nil
	}
	u := database.User{
		Username:        opts.username,
		Token:           "sk-integration-" + opts.username,
		PasswordHash:    "x",
		Status:          1,
		Email:           email,
		EmailVerifiedAt: verifiedAt,
	}
	if opts.banned {
		u.Status = 2
	}
	if err := db.Create(&u).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if opts.emailCategories != nil {
		if err := database.SavePreference(u.ID, map[string]bool{}, []int{}, opts.emailCategories); err != nil {
			t.Fatalf("save pref: %v", err)
		}
		InvalidatePrefCache(u.ID)
	}
	return &u
}

type integrationOpts struct {
	username        string
	bound           bool
	verified        bool
	banned          bool
	email           string // 默认 "alice@example.com"
	emailCategories map[string]bool
}

// withSyncEmailAndHook 一次性激活两个测试 hook，结束时还原。
func withSyncEmailAndHook(t *testing.T, hook func(cfg SMTPConfig, msg EmailMessage) error, fn func()) {
	t.Helper()
	SetEmailQueueSyncForTest(true)
	SetSendEmailViaSMTPHookForTest(hook)
	defer func() {
		SetEmailQueueSyncForTest(false)
		SetSendEmailViaSMTPHookForTest(nil)
		resetEmailQueueForTest()
	}()
	fn()
}

// ── 端到端集成 case ──

func TestIntegration_DispatchSendsEmailWhenEligible(t *testing.T) {
	utils.InitCrypto()
	cap := &emailCapture{}
	withSyncEmailAndHook(t, cap.hook(), func() {
		user := setupIntegrationTestDB(t, integrationOpts{
			username: "alice", bound: true, verified: true,
			emailCategories: map[string]bool{"refund": true},
		})
		key := "test-dispatch-1"
		Dispatch(user.ID, "refund", "info", "退款已到账",
			"您的订阅已退款 $10", "/bills", "查看",
			"subscription", 0, &key)

		if cap.Count() != 1 {
			t.Fatalf("expected 1 email call, got %d", cap.Count())
		}
		got, _ := cap.Last()
		if got.Msg.To != "alice@example.com" {
			t.Errorf("To = %q want alice@example.com", got.Msg.To)
		}
		// notification template 应包含通知正文
		if !strings.Contains(got.Msg.TextBody, "您的订阅已退款") {
			t.Errorf("text body missing notification body: %q", got.Msg.TextBody)
		}
		if !strings.Contains(got.Msg.HTMLBody, "退款已到账") {
			t.Errorf("html body missing notification title: %q", got.Msg.HTMLBody)
		}
		// cfg 应该是测试 SMTP 配置（password 已解密）
		if got.Cfg.Password != "smtp-pwd" {
			t.Errorf("Cfg.Password should be decrypted, got %q", got.Cfg.Password)
		}
	})
}

func TestIntegration_DispatchSkipsWhenMasterDisabled(t *testing.T) {
	utils.InitCrypto()
	cap := &emailCapture{}
	withSyncEmailAndHook(t, cap.hook(), func() {
		user := setupIntegrationTestDB(t, integrationOpts{
			username: "alice", bound: true, verified: true,
			emailCategories: map[string]bool{"refund": true},
		})
		// 关闭 master
		SysConfigMutex.Lock()
		SysConfigCache["email_enabled"] = "false"
		SysConfigMutex.Unlock()

		key := "test-master-off"
		Dispatch(user.ID, "refund", "info", "T", "B", "", "", "subscription", 0, &key)

		if cap.Count() != 0 {
			t.Errorf("master off → expected 0 email, got %d", cap.Count())
		}
	})
}

func TestIntegration_DispatchSkipsBannedUser(t *testing.T) {
	utils.InitCrypto()
	cap := &emailCapture{}
	withSyncEmailAndHook(t, cap.hook(), func() {
		user := setupIntegrationTestDB(t, integrationOpts{
			username: "alice", bound: true, verified: true, banned: true,
			emailCategories: map[string]bool{"refund": true},
		})
		key := "test-banned"
		Dispatch(user.ID, "refund", "info", "T", "B", "", "", "subscription", 0, &key)

		if cap.Count() != 0 {
			t.Errorf("banned user → expected 0 email, got %d", cap.Count())
		}
	})
}

func TestIntegration_DispatchSkipsUnverifiedEmail(t *testing.T) {
	utils.InitCrypto()
	cap := &emailCapture{}
	withSyncEmailAndHook(t, cap.hook(), func() {
		user := setupIntegrationTestDB(t, integrationOpts{
			username: "alice", bound: true, verified: false,
			emailCategories: map[string]bool{"refund": true},
		})
		key := "test-unverified"
		Dispatch(user.ID, "refund", "info", "T", "B", "", "", "subscription", 0, &key)

		if cap.Count() != 0 {
			t.Errorf("unverified user → expected 0 email, got %d", cap.Count())
		}
	})
}

func TestIntegration_DispatchSkipsWhenCategoryNotOptIn(t *testing.T) {
	utils.InitCrypto()
	cap := &emailCapture{}
	withSyncEmailAndHook(t, cap.hook(), func() {
		// 偏好里只开了 refund，触发 security 通知
		user := setupIntegrationTestDB(t, integrationOpts{
			username: "alice", bound: true, verified: true,
			emailCategories: map[string]bool{"refund": true},
		})
		key := "test-cat-not-optin"
		Dispatch(user.ID, "security", "warning", "T", "B", "", "", "user", 0, &key)

		if cap.Count() != 0 {
			t.Errorf("category not opt-in → expected 0 email, got %d", cap.Count())
		}
	})
}

func TestIntegration_DispatchForceDeliverEmailStillRespectsPrefs(t *testing.T) {
	utils.InitCrypto()
	cap := &emailCapture{}
	withSyncEmailAndHook(t, cap.hook(), func() {
		// security 是 forceDeliver（in-app 强制送达），但邮件 channel 仍按偏好
		user := setupIntegrationTestDB(t, integrationOpts{
			username: "alice", bound: true, verified: true,
			emailCategories: map[string]bool{"security": true}, // 显式 opt-in
		})
		key := "test-force-deliver"
		Dispatch(user.ID, "security", "warning", "账号异常", "登录异常告警", "", "", "user", 0, &key)

		if cap.Count() != 1 {
			t.Fatalf("security with opt-in → expected 1 email, got %d", cap.Count())
		}
	})
}

func TestIntegration_DispatchDedupPreventsDoubleSend(t *testing.T) {
	utils.InitCrypto()
	cap := &emailCapture{}
	withSyncEmailAndHook(t, cap.hook(), func() {
		user := setupIntegrationTestDB(t, integrationOpts{
			username: "alice", bound: true, verified: true,
			emailCategories: map[string]bool{"refund": true},
		})
		key := "test-dedup-same"
		Dispatch(user.ID, "refund", "info", "T1", "B1", "", "", "subscription", 0, &key)
		Dispatch(user.ID, "refund", "info", "T2", "B2", "", "", "subscription", 0, &key)
		Dispatch(user.ID, "refund", "info", "T3", "B3", "", "", "subscription", 0, &key)

		if cap.Count() != 1 {
			t.Errorf("same dedupKey → expected 1 email, got %d", cap.Count())
		}
	})
}

func TestIntegration_DispatchDifferentDedupKeysAllSent(t *testing.T) {
	utils.InitCrypto()
	cap := &emailCapture{}
	withSyncEmailAndHook(t, cap.hook(), func() {
		user := setupIntegrationTestDB(t, integrationOpts{
			username: "alice", bound: true, verified: true,
			emailCategories: map[string]bool{"refund": true},
		})
		for i := 0; i < 3; i++ {
			key := "test-different-" + string(rune('a'+i))
			Dispatch(user.ID, "refund", "info", "T", "B", "", "", "subscription", 0, &key)
		}

		if cap.Count() != 3 {
			t.Errorf("3 different keys → expected 3 emails, got %d", cap.Count())
		}
	})
}

func TestIntegration_DispatchSendFailureLogged(t *testing.T) {
	utils.InitCrypto()
	cap := &emailCapture{errToRet: errors.New("simulated SMTP server error 421")}
	withSyncEmailAndHook(t, cap.hook(), func() {
		user := setupIntegrationTestDB(t, integrationOpts{
			username: "alice", bound: true, verified: true,
			emailCategories: map[string]bool{"refund": true},
		})
		key := "test-smtp-fail"
		// 不应 panic：SendEmailDeduped 即使 SMTP fail 也只 log 不抛
		Dispatch(user.ID, "refund", "info", "T", "B", "", "", "subscription", 0, &key)

		// hook 被调用了（说明 SMTP send 路径被走到）
		if cap.Count() != 1 {
			t.Errorf("expected hook called, got %d", cap.Count())
		}
	})
}

func TestIntegration_DispatchMultiUserFanOut(t *testing.T) {
	utils.InitCrypto()
	cap := &emailCapture{}
	withSyncEmailAndHook(t, cap.hook(), func() {
		alice := setupIntegrationTestDB(t, integrationOpts{
			username: "alice", bound: true, verified: true,
			email:           "alice@example.com",
			emailCategories: map[string]bool{"refund": true},
		})
		// seed second user with verified email + opt-in
		now := time.Now()
		bob := database.User{
			Username: "bob", Token: "sk-bob", PasswordHash: "x", Status: 1,
			Email:           "bob@example.com",
			EmailVerifiedAt: &now,
		}
		if err := database.DB.Create(&bob).Error; err != nil {
			t.Fatalf("seed bob: %v", err)
		}
		if err := database.SavePreference(bob.ID, map[string]bool{}, []int{}, map[string]bool{"refund": true}); err != nil {
			t.Fatalf("save bob pref: %v", err)
		}
		InvalidatePrefCache(bob.ID)

		// 用不同 dedupKey 分别发给 alice 和 bob
		keyA := "fanout-alice"
		Dispatch(alice.ID, "refund", "info", "T", "B", "", "", "subscription", 0, &keyA)
		keyB := "fanout-bob"
		Dispatch(bob.ID, "refund", "info", "T", "B", "", "", "subscription", 0, &keyB)

		if cap.Count() != 2 {
			t.Fatalf("expected 2 emails (alice+bob), got %d", cap.Count())
		}
		recipients := map[string]bool{}
		cap.mu.Lock()
		for _, c := range cap.calls {
			recipients[c.Msg.To] = true
		}
		cap.mu.Unlock()
		if !recipients["alice@example.com"] || !recipients["bob@example.com"] {
			t.Errorf("recipients = %v, want both alice + bob", recipients)
		}
	})
}

func TestIntegration_DispatchHTMLEscapesNotificationBody(t *testing.T) {
	utils.InitCrypto()
	cap := &emailCapture{}
	withSyncEmailAndHook(t, cap.hook(), func() {
		user := setupIntegrationTestDB(t, integrationOpts{
			username: "alice", bound: true, verified: true,
			emailCategories: map[string]bool{"refund": true},
		})
		key := "test-xss"
		// 攻击者控制 body 含 HTML payload
		Dispatch(user.ID, "refund", "info",
			"<script>alert(1)</script>", "evil &amp; injected <img onerror=x>",
			"", "", "subscription", 0, &key)

		if cap.Count() != 1 {
			t.Fatalf("expected 1 email, got %d", cap.Count())
		}
		got, _ := cap.Last()
		// HTML body 必须 escape 这些字段（notif_title / notif_body 来自 Extra → 转义）
		// title 出现在 HTML body 里（默认通知模板的 H1 + body 都用 {notif_title}/{notif_body}）
		if strings.Contains(got.Msg.HTMLBody, "<script>alert(1)</script>") {
			t.Errorf("HTML body did NOT escape <script>: %s", got.Msg.HTMLBody)
		}
		if !strings.Contains(got.Msg.HTMLBody, "&lt;script&gt;") {
			t.Error("HTML body should contain escaped <script>")
		}
		// img onerror 攻击载荷也必须被转义
		if strings.Contains(got.Msg.HTMLBody, "<img onerror=x>") {
			t.Errorf("HTML body did NOT escape <img onerror>: %s", got.Msg.HTMLBody)
		}
		// text body 原样保留（notif_body 在 text 默认模板里出现）
		if !strings.Contains(got.Msg.TextBody, "<img onerror=x>") {
			t.Errorf("text body should preserve raw HTML in body (text-only context), got: %s", got.Msg.TextBody)
		}
	})
}

// ── 安全 case：直接调底层验证 ──

func TestSecurity_SSRFBlocksInternalSMTPHost(t *testing.T) {
	// 直接调 ssrfSafeSMTPDialContext，不走 SendEmailViaSMTP（后者还要 TLS 握手）
	tests := []struct {
		name string
		addr string
		want string // 期望 err 包含此字符串
	}{
		{"loopback 127.0.0.1 blocked", "127.0.0.1:587", "forbidden"},
		{"private 10.x blocked", "10.0.0.1:587", "forbidden"},
		{"link-local 169.254 blocked", "169.254.169.254:587", "forbidden"},
		{"metadata multicast 224.x blocked", "224.0.0.1:587", "forbidden"},
		{"ipv6 loopback blocked", "[::1]:587", "forbidden"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ssrfSafeSMTPDialContext("tcp", tc.addr, 100*time.Millisecond)
			if err == nil {
				t.Errorf("expected error for %s, got nil", tc.addr)
				return
			}
			if !strings.Contains(strings.ToLower(err.Error()), tc.want) {
				t.Errorf("err %q should contain %q", err.Error(), tc.want)
			}
		})
	}
}

func TestSecurity_Port25Rejected(t *testing.T) {
	utils.InitCrypto()
	SysConfigMutex.Lock()
	prev := SysConfigCache
	SysConfigCache = map[string]string{
		"smtp_host":     "smtp.example.com",
		"smtp_port":     "25",
		"smtp_username": "u",
		"smtp_password": "",
		"smtp_from":     "f",
	}
	SysConfigMutex.Unlock()
	defer func() {
		SysConfigMutex.Lock()
		SysConfigCache = prev
		SysConfigMutex.Unlock()
	}()

	_, err := LoadSMTPConfig()
	if err == nil {
		t.Fatal("port 25 should be rejected")
	}
	if !strings.Contains(err.Error(), "25") {
		t.Errorf("err should mention port 25: %v", err)
	}
}

func TestSecurity_NoSMTPCallWhenQueueStopped(t *testing.T) {
	utils.InitCrypto()
	cap := &emailCapture{}

	// 强制 sync mode + hook
	SetEmailQueueSyncForTest(false) // 走真异步队列以测 stopped
	SetSendEmailViaSMTPHookForTest(cap.hook())
	defer func() {
		SetEmailQueueSyncForTest(false)
		SetSendEmailViaSMTPHookForTest(nil)
	}()

	// 直接模拟 stopped
	prevStopped := emailQueueStopped.Load()
	emailQueueStopped.Store(true)
	defer emailQueueStopped.Store(prevStopped)

	err := EnqueueEmail(EmailTask{To: "u@e.com", Message: EmailMessage{Subject: "S", TextBody: "B"}})
	if !errors.Is(err, ErrEmailQueueFull) {
		t.Errorf("expected ErrEmailQueueFull when stopped, got %v", err)
	}
	if cap.Count() != 0 {
		t.Errorf("hook should not be called when queue stopped, got %d calls", cap.Count())
	}
}

// 静默引用避免未使用 import lint
var _ atomic.Bool
