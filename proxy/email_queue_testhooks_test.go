// Test-only hooks for email queue. Living in *_test.go ensures these
// functions disappear from the production binary (Audit 2026-05-21 T1-4 fix).
//
// 把 SetEmailQueueSyncForTest / SetSendEmailViaSMTPHookForTest 从生产文件
// (email_queue.go) 移出来。两者 mutate global var → 留在 production 文件里
// 等于给攻击 / 误用一个 "改全局 SMTP hook" 的 surface。现在仅 _test 构建可见。
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
