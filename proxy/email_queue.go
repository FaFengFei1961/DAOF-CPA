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
)

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
	if err := SendEmailViaSMTP(cfg, msg); err != nil {
		log.Printf("[EMAIL-QUEUE-SEND-FAIL] label=%s to=%s err=%v",
			task.Label, maskEmail(task.To), err)
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
func EnqueueEmail(task EmailTask) error {
	if emailQueueStopped.Load() {
		return ErrEmailQueueFull
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
		log.Printf("[EMAIL-QUEUE-DROP] queue full (cap=%d) label=%s to=%s",
			emailQueueCap, task.Label, maskEmail(task.To))
		return ErrEmailQueueFull
	}
}

// SendEmailDeduped 在入队前检查 dedupKey 是否命中。命中 → 返回 ErrEmailDedup 不入队。
// dedupKey 为空 → 直接入队（无 dedup）。
func SendEmailDeduped(task EmailTask) error {
	if task.DedupKey != "" {
		if dedupHit(task.DedupKey) {
			return ErrEmailDedup
		}
	}
	if err := EnqueueEmail(task); err != nil {
		return err
	}
	if task.DedupKey != "" {
		recordDedup(task.DedupKey, time.Now())
	}
	return nil
}

// dedupHit 检查 key 是否在 TTL 内已记录。同时顺便清理过期项（lazy GC）。
func dedupHit(key string) bool {
	now := time.Now()
	emailDedupMu.Lock()
	defer emailDedupMu.Unlock()
	if t, ok := emailDedupMap[key]; ok {
		if now.Sub(t) < emailDedupTTL {
			return true
		}
		// expired：顺手清掉
		delete(emailDedupMap, key)
	}
	// lazy GC：发现 map 太大时整体清一遍
	if len(emailDedupMap) > 1024 {
		for k, v := range emailDedupMap {
			if now.Sub(v) > emailDedupTTL {
				delete(emailDedupMap, k)
			}
		}
	}
	return false
}

func recordDedup(key string, at time.Time) {
	emailDedupMu.Lock()
	emailDedupMap[key] = at
	emailDedupMu.Unlock()
}

// CheckEmailRateLimit 校验 email + IP 都没超限，超了返回 ErrEmailRateLimitExceeded。
// 调用方在执行 SMTP send 前调本函数；成功 enqueue 后调 RegisterEmailSent 计数。
//
// email 应已小写规范化。clientIP 是 c.IP()。
//
// 校验通过 != 已发送：caller 可能在拿到许可后才发现配置缺失等，所以本函数不"占用"配额。
// 配额只在 RegisterEmailSent 时被消费，这是为了让"check-then-send"逻辑可重入。
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

// RegisterEmailSent 在邮件成功入队后调用，给桶 +1。
func RegisterEmailSent(email, clientIP string) {
	emailRateLimitMu.Lock()
	defer emailRateLimitMu.Unlock()
	now := time.Now()
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
	// lazy GC：当 map 过大时清一遍
	if len(emailSentByEmail) > 4096 {
		for k, v := range emailSentByEmail {
			if rateBucketExpired(v, now) {
				delete(emailSentByEmail, k)
			}
		}
	}
	if len(emailSentByIP) > 4096 {
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
