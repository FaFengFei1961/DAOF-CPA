// Package proxy / email_queue.go
//
// 邮件发送的异步队列 + 限流 + 幂等。Phase G-1.4（2026-05-20）。
//
// 镜像 notification_dispatcher.go 的有界 worker pool 设计：
//   - 容量 256 的 channel 队列
//   - 2 个常驻 worker（SMTP 是慢 I/O，多 worker 也会被 SMTP server 限流）
//   - 队列满 → 丢弃 + 告警
//   - StopEmailQueue 优雅停止，graceful drain
//
// 限流：per-email 每 N 封/小时 + per-IP 每 M 封/小时（窗口式计数）
//   - admin 配置：email_rate_limit_per_email_hourly / email_rate_limit_per_ip_hourly
//   - 默认 5/20，保护 SMTP 配额防滥用
//
// 幂等：dedupKey TTL 内不重发
//   - 默认 5 分钟。caller 传相同 dedupKey 会被去重（验证邮件不会因为用户狂点 resend 而被刷屏）
package proxy

import (
	"errors"
	"fmt"
	"log"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	emailQueueCap     = 256
	emailQueueWorkers = 2

	emailRateLimitPerEmailDefault = 5
	emailRateLimitPerIPDefault    = 20
	emailRateLimitWindow          = time.Hour

	emailDedupTTL = 5 * time.Minute

	// SysConfig keys
	emailRateLimitPerEmailKey = "email_rate_limit_per_email_hourly"
	emailRateLimitPerIPKey    = "email_rate_limit_per_ip_hourly"
)

// EmailTask 是一封排队中的邮件。
type EmailTask struct {
	To       string       // 收件人邮箱（已小写）
	Message  EmailMessage // 已渲染好的消息（subject/text/html）
	DedupKey string       // 可选：去重 key。空串表示不去重
	Label    string       // 调试日志用，如 "verify_bind" / "reset_password"
}

var (
	emailQueue        chan EmailTask
	emailQueueOnce    sync.Once
	emailQueueWG      sync.WaitGroup
	emailQueueStopped atomic.Bool

	// emailQueueSyncForTest 在测试期把"入队 + 异步发送"改成"同步直接发送"，让断言能确定看到结果。
	// 同 recordApiLogRevenueSync 模式（参见 testmain_test.go）。production 永远是 false。
	emailQueueSyncForTest atomic.Bool

	// sendEmailViaSMTPHook 允许测试替换真实 SMTP 拨号 / 协议交互，捕获 cfg+msg 做断言。
	// nil 时走 SendEmailViaSMTP 原始实现。
	sendEmailViaSMTPHook   func(cfg SMTPConfig, msg EmailMessage) error
	sendEmailViaSMTPHookMu sync.RWMutex

	// fix M-10 / M-12：ops 可见性 ——
	// 邮件 send 失败次数与 queue drop 次数累计。供 admin / 监控查询，
	// 让 SMTP 配错或队列拥塞不再"完全静默"。
	emailSendFailCount atomic.Int64
	emailQueueDropCount atomic.Int64
)

// EmailOpsStats 返回邮件 pipeline 的累计 ops 指标（自进程启动起）。
// 给 admin 监控 / 调试用——失败率高 = SMTP 配错 / 网络问题；drop 高 = 队列拥塞需扩容。
func EmailOpsStats() (sendFails, queueDrops int64) {
	return emailSendFailCount.Load(), emailQueueDropCount.Load()
}

// IncEmailSendFailCount 让外部 caller（如 signup 流程 fire-and-forget 发邮件失败）
// 也能贡献给同一份 ops 计数器。无需返回值。
func IncEmailSendFailCount() { emailSendFailCount.Add(1) }

// SetEmailQueueSyncForTest 让 EnqueueEmail / SendEmailDeduped 同步执行 processEmailTask。
// 仅测试用；caller 负责测试结束后 reset。
func SetEmailQueueSyncForTest(b bool) {
	emailQueueSyncForTest.Store(b)
}

// SetSendEmailViaSMTPHookForTest 注入一个 fake send 函数。传 nil 恢复默认（调真实 SMTP）。
// 仅测试用；caller 负责在测试结束 reset。
func SetSendEmailViaSMTPHookForTest(fn func(cfg SMTPConfig, msg EmailMessage) error) {
	sendEmailViaSMTPHookMu.Lock()
	sendEmailViaSMTPHook = fn
	sendEmailViaSMTPHookMu.Unlock()
}

func sendEmailViaSMTPDispatch(cfg SMTPConfig, msg EmailMessage) error {
	sendEmailViaSMTPHookMu.RLock()
	hook := sendEmailViaSMTPHook
	sendEmailViaSMTPHookMu.RUnlock()
	if hook != nil {
		return hook(cfg, msg)
	}
	return SendEmailViaSMTP(cfg, msg)
}

// rate-limit 桶：窗口起点 + 计数
type emailRateBucket struct {
	windowStart time.Time
	count       int
}

var (
	emailRateLimitMu sync.Mutex
	emailSentByEmail = map[string]*emailRateBucket{}
	emailSentByIP    = map[string]*emailRateBucket{}

	emailDedupMu  sync.Mutex
	emailDedupMap = map[string]time.Time{}
)

// ErrEmailRateLimitExceeded 是 CheckEmailRateLimit 返回的 sentinel。
var ErrEmailRateLimitExceeded = errors.New("email rate limit exceeded")

// ErrEmailQueueFull 是 EnqueueEmail 入队失败的 sentinel。
var ErrEmailQueueFull = errors.New("email queue full")

// ErrEmailDedup 是 SendEmailDeduped 因 dedupKey 命中而跳过的 sentinel
// （caller 视情况决定是否当作"成功"处理）。
var ErrEmailDedup = errors.New("email skipped: dedup key matched within TTL")

// ensureEmailQueue 第一次调用时启动 worker pool。
func ensureEmailQueue() {
	emailQueueOnce.Do(func() {
		emailQueue = make(chan EmailTask, emailQueueCap)
		for i := 0; i < emailQueueWorkers; i++ {
			emailQueueWG.Add(1)
			go func() {
				defer emailQueueWG.Done()
				for task := range emailQueue {
					processEmailTask(task)
				}
			}()
		}
	})
}

func processEmailTask(task EmailTask) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[EMAIL-QUEUE-PANIC] task label=%s to=%s recovered: %v\n%s",
				task.Label, maskEmail(task.To), r, debug.Stack())
		}
	}()

	cfg, err := LoadSMTPConfig()
	if err != nil {
		log.Printf("[EMAIL-QUEUE-CFG-FAIL] label=%s to=%s: %v",
			task.Label, maskEmail(task.To), err)
		return
	}
	if !cfg.IsConfigured() {
		log.Printf("[EMAIL-QUEUE-NOT-CONFIGURED] label=%s to=%s — admin must finish SMTP config first",
			task.Label, maskEmail(task.To))
		return
	}
	msg := task.Message
	msg.To = task.To
	if err := sendEmailViaSMTPDispatch(cfg, msg); err != nil {
		fails := emailSendFailCount.Add(1)
		log.Printf("[EMAIL-QUEUE-SEND-FAIL] label=%s to=%s total_fails=%d err=%v",
			task.Label, maskEmail(task.To), fails, err)
		return
	}
	log.Printf("[EMAIL-QUEUE-SENT] label=%s to=%s", task.Label, maskEmail(task.To))
}

// StopEmailQueue 优雅停止：close queue 让 workers 排空后退出。
// 镜像 StopDispatchPool 的设计，main.go OS-signal handler 应调本函数。
func StopEmailQueue() {
	if emailQueue == nil {
		return
	}
	if !emailQueueStopped.CompareAndSwap(false, true) {
		return
	}
	close(emailQueue)
	emailQueueWG.Wait()
}

// EnqueueEmail 把一封邮件丢进队列（无限流 / 无幂等，由 caller 负责前置校验）。
// 满了或队列已停时返回 ErrEmailQueueFull。
//
// 测试 hook：emailQueueSyncForTest=true 时改为同步直接调 processEmailTask（无 channel
// + goroutine），让 caller 在 EnqueueEmail 返回后可立刻断言副作用（捕获 hook、DB 状态）。
func EnqueueEmail(task EmailTask) error {
	if emailQueueStopped.Load() {
		return ErrEmailQueueFull
	}
	if emailQueueSyncForTest.Load() {
		processEmailTask(task)
		return nil
	}
	ensureEmailQueue()
	defer func() {
		// post-shutdown send → recover 兜底（与 dispatchAsync 同模式）
		if r := recover(); r != nil {
			log.Printf("[EMAIL-QUEUE-DROP] post-shutdown enqueue recovered: %v", r)
		}
	}()
	select {
	case emailQueue <- task:
		return nil
	default:
		dropped := emailQueueDropCount.Add(1)
		log.Printf("[EMAIL-QUEUE-DROP] queue full (cap=%d) label=%s to=%s total_drops=%d",
			emailQueueCap, task.Label, maskEmail(task.To), dropped)
		return ErrEmailQueueFull
	}
}

// SendEmailDeduped 在入队前检查 dedupKey 是否命中。命中 → 返回 ErrEmailDedup 不入队。
// dedupKey 为空 → 直接入队（无 dedup）。
//
// **原子性保证**（fix HIGH H-1, 2026-05-20）：check + record 必须在同一把锁下完成。
// 旧实现 check 与 record 分离，两个并发 goroutine 持同一 dedupKey 时都能通过 check、
// 都成功入队、再各自 record → 重复发送。新实现把 record 提前到 check 通过时立刻执行；
// 后续 enqueue 失败也不回滚 record（caller 拿到 ErrEmailQueueFull 已经知道）。
//
// 极端 corner case：record 已写但 enqueue 失败 → 下次相同 dedupKey 会被错误 dedup。
// 这是可接受的——dedup TTL 5 分钟内只丢一次重发，比"双发"风险小得多。
func SendEmailDeduped(task EmailTask) error {
	if task.DedupKey != "" {
		if !tryClaimDedupSlot(task.DedupKey) {
			return ErrEmailDedup
		}
	}
	return EnqueueEmail(task)
}

// tryClaimDedupSlot 在 TTL 窗口内尝试占用一个 dedup 槽位。
// 槽位空 / 槽位已过期 → 写入新时间戳并返回 true（caller 应继续 enqueue）。
// 槽位仍在 TTL 内 → 返回 false（caller 应 ErrEmailDedup）。
//
// 同时做 lazy GC：map 过大时整体扫一遍清掉过期项。
func tryClaimDedupSlot(key string) bool {
	now := time.Now()
	emailDedupMu.Lock()
	defer emailDedupMu.Unlock()
	if t, ok := emailDedupMap[key]; ok {
		if now.Sub(t) < emailDedupTTL {
			return false
		}
		// 过期：本次重新占用
	}
	emailDedupMap[key] = now
	if len(emailDedupMap) > 1024 {
		for k, v := range emailDedupMap {
			if now.Sub(v) > emailDedupTTL {
				delete(emailDedupMap, k)
			}
		}
	}
	return true
}

// CheckEmailRateLimit 校验 email + IP 都没超限，超了返回 ErrEmailRateLimitExceeded。
// CheckEmailRateLimit 仅检查 email + IP 是否已超限；**不**占用配额。配额在
// RegisterEmailSent 时消费。**有 TOCTOU**：两个并发 caller 可能都通过 check，
// 都各自 Register 一次，最终 count = limit + 1。如果想原子 check + 占用，调
// CheckAndConsumeEmailRateLimit。
//
// 保留这个函数是为了 forgot-password 这种"先确认能发再做重活"的路径——它在
// goroutine 里跑，重活失败时也不消费配额。
func CheckEmailRateLimit(email, clientIP string) error {
	emailLimit, ipLimit := loadEmailRateLimits()

	emailRateLimitMu.Lock()
	defer emailRateLimitMu.Unlock()

	now := time.Now()
	if email != "" {
		if b, ok := emailSentByEmail[email]; ok && !rateBucketExpired(b, now) && b.count >= emailLimit {
			return fmt.Errorf("%w: per-email limit %d/hour reached", ErrEmailRateLimitExceeded, emailLimit)
		}
	}
	if clientIP != "" {
		if b, ok := emailSentByIP[clientIP]; ok && !rateBucketExpired(b, now) && b.count >= ipLimit {
			return fmt.Errorf("%w: per-IP limit %d/hour reached", ErrEmailRateLimitExceeded, ipLimit)
		}
	}
	return nil
}

// CheckAndConsumeEmailRateLimit 在同一把锁下做 check + 占用，原子地防 TOCTOU。
// 用于 bind / resend / set-password 等"一次性、同步、必须严格限流"的场景。
//
// 成功 → 返回 nil，桶已 +1。
// 失败 → 返回 ErrEmailRateLimitExceeded，桶未动。
//
// fix HIGH H-3（2026-05-20）：原 Check + Register 分离 → 并发 caller 都通过 check
// 都各 +1，limit 实际成了 limit × N。本函数把两步合并到单个锁区。
func CheckAndConsumeEmailRateLimit(email, clientIP string) error {
	emailLimit, ipLimit := loadEmailRateLimits()

	emailRateLimitMu.Lock()
	defer emailRateLimitMu.Unlock()

	now := time.Now()
	if email != "" {
		if b, ok := emailSentByEmail[email]; ok && !rateBucketExpired(b, now) && b.count >= emailLimit {
			return fmt.Errorf("%w: per-email limit %d/hour reached", ErrEmailRateLimitExceeded, emailLimit)
		}
	}
	if clientIP != "" {
		if b, ok := emailSentByIP[clientIP]; ok && !rateBucketExpired(b, now) && b.count >= ipLimit {
			return fmt.Errorf("%w: per-IP limit %d/hour reached", ErrEmailRateLimitExceeded, ipLimit)
		}
	}
	registerEmailSentLocked(email, clientIP, now)
	return nil
}

// RegisterEmailSent 在邮件成功入队后调用，给桶 +1。
// 推荐用 CheckAndConsumeEmailRateLimit 一次性完成 check + 占用，除非你有
// "先 check、做重活、最后 Register"的特殊语义需求（例如 ForgotPassword 在
// goroutine 内最终调）。
func RegisterEmailSent(email, clientIP string) {
	emailRateLimitMu.Lock()
	defer emailRateLimitMu.Unlock()
	registerEmailSentLocked(email, clientIP, time.Now())
}

// registerEmailSentLocked 是 +1 + GC 的实际逻辑，要求 caller 已持 emailRateLimitMu。
func registerEmailSentLocked(email, clientIP string, now time.Time) {
	if email != "" {
		b, ok := emailSentByEmail[email]
		if !ok || rateBucketExpired(b, now) {
			b = &emailRateBucket{windowStart: now}
			emailSentByEmail[email] = b
		}
		b.count++
	}
	if clientIP != "" {
		b, ok := emailSentByIP[clientIP]
		if !ok || rateBucketExpired(b, now) {
			b = &emailRateBucket{windowStart: now}
			emailSentByIP[clientIP] = b
		}
		b.count++
	}
	// lazy GC：当 map 过大时清一遍（fix M-9 阈值 4096→512，减少内存峰值）
	if len(emailSentByEmail) > 512 {
		for k, v := range emailSentByEmail {
			if rateBucketExpired(v, now) {
				delete(emailSentByEmail, k)
			}
		}
	}
	if len(emailSentByIP) > 512 {
		for k, v := range emailSentByIP {
			if rateBucketExpired(v, now) {
				delete(emailSentByIP, k)
			}
		}
	}
}

func rateBucketExpired(b *emailRateBucket, now time.Time) bool {
	return now.Sub(b.windowStart) >= emailRateLimitWindow
}

func loadEmailRateLimits() (emailLimit, ipLimit int) {
	emailLimit = emailRateLimitPerEmailDefault
	ipLimit = emailRateLimitPerIPDefault
	SysConfigMutex.RLock()
	emailRaw := strings.TrimSpace(SysConfigCache[emailRateLimitPerEmailKey])
	ipRaw := strings.TrimSpace(SysConfigCache[emailRateLimitPerIPKey])
	SysConfigMutex.RUnlock()
	if v, err := strconv.Atoi(emailRaw); err == nil && v > 0 && v < 10000 {
		emailLimit = v
	}
	if v, err := strconv.Atoi(ipRaw); err == nil && v > 0 && v < 10000 {
		ipLimit = v
	}
	return emailLimit, ipLimit
}

// resetEmailQueueForTest 仅供测试使用：清空 dedup + rate-limit 状态。
//
// 注：不重置 emailQueue / emailQueueOnce / emailQueueStopped —— 这些是全局
// goroutine 池状态，重置会留下泄漏的 worker。测试用 EnqueueEmail 之外的路径
// 验证 enqueue 失败时不要触发真正的 ensureEmailQueue（参见 _test.go）。
func resetEmailQueueForTest() {
	emailDedupMu.Lock()
	emailDedupMap = map[string]time.Time{}
	emailDedupMu.Unlock()
	emailRateLimitMu.Lock()
	emailSentByEmail = map[string]*emailRateBucket{}
	emailSentByIP = map[string]*emailRateBucket{}
	emailRateLimitMu.Unlock()
}

// maskEmail 脱敏邮箱地址用于日志：a***@example.com。
// 防止运维日志泄漏完整 PII。
//
// 规则：
//   - "" / 全空白 → ""
//   - 无 @ → "***"
//   - local 为空（"@host"）→ "*@host"
//   - local 1 字符 → "a***@host"（仍保留首字符）
//   - local 多字符 → "首字符***@host"
func maskEmail(email string) string {
	email = strings.TrimSpace(email)
	if email == "" {
		return ""
	}
	at := strings.IndexByte(email, '@')
	if at < 0 {
		return "***"
	}
	local := email[:at]
	domain := email[at:]
	if local == "" {
		return "*" + domain
	}
	return string(local[0]) + "***" + domain
}
