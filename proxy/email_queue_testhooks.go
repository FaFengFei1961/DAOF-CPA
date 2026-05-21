// Build tag: 默认包含。生产构建可加 `-tags prod` 剥离这些 hook（不剥离也无害 ——
// 它们仅在被显式调用时才 mutate global state，生产代码路径不调）。
//
// 为什么用 build tag 而不是 _test.go：跨包测试（controller/email_password_reset_test.go
// 等）需要 import proxy.SetEmailQueueSyncForTest，但 Go 的 _test.go 文件**只在同包**
// 测试里可见 —— 跨包调不到。务实选择是放普通 .go + build tag。
//
// Audit 2026-05-21 T1-8 修正方案。原计划"移到 _test.go"被 reset 后 go vet 揪出
// 跨包不可见，回退到 build-tag 隔离。
//go:build !prod

package proxy

// SetEmailQueueSyncForTest 让 EnqueueEmail / SendEmailDeduped 同步执行 processEmailTask。
// caller 负责测试结束后 reset (defer SetEmailQueueSyncForTest(false))。
func SetEmailQueueSyncForTest(b bool) {
	emailQueueSyncForTest.Store(b)
}

// SetSendEmailViaSMTPHookForTest 注入一个 fake send 函数。
// 传 nil 恢复默认（调真实 SMTP）。caller 负责测试结束 reset。
func SetSendEmailViaSMTPHookForTest(fn func(cfg SMTPConfig, msg EmailMessage) error) {
	sendEmailViaSMTPHookMu.Lock()
	sendEmailViaSMTPHook = fn
	sendEmailViaSMTPHookMu.Unlock()
}
