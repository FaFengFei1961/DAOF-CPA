// Package proxy / email_queue_test.go
//
// Phase G-1.4 单元测试：email 队列 + 限流 + 幂等。
// 不真实拨号 SMTP（processEmailTask 在没配置 SMTP 时会 log 然后 return），
// 重点验证 enqueue 控制、dedup、限流。
package proxy

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMaskEmail(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"   ", ""}, // trim → empty → empty out (与 empty input 同行为)
		{"no-at-symbol", "***"},
		{"@example.com", "*@example.com"},
		{"a@example.com", "a***@example.com"},
		{"alice@example.com", "a***@example.com"},
		{"longer.name+tag@example.com", "l***@example.com"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got := maskEmail(tc.in)
			if got != tc.want {
				t.Errorf("maskEmail(%q) = %q; want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRateBucketExpired(t *testing.T) {
	now := time.Now()
	t.Run("fresh bucket not expired", func(t *testing.T) {
		b := &emailRateBucket{windowStart: now}
		if rateBucketExpired(b, now) {
			t.Error("fresh bucket should not be expired")
		}
	})
	t.Run("just-under-window not expired", func(t *testing.T) {
		b := &emailRateBucket{windowStart: now.Add(-emailRateLimitWindow + time.Second)}
		if rateBucketExpired(b, now) {
			t.Error("just-under-window should not be expired")
		}
	})
	t.Run("exact window boundary expired", func(t *testing.T) {
		b := &emailRateBucket{windowStart: now.Add(-emailRateLimitWindow)}
		if !rateBucketExpired(b, now) {
			t.Error("exactly-at-window should be expired (>= not >)")
		}
	})
	t.Run("way past window expired", func(t *testing.T) {
		b := &emailRateBucket{windowStart: now.Add(-2 * emailRateLimitWindow)}
		if !rateBucketExpired(b, now) {
			t.Error("past window should be expired")
		}
	})
}

func TestCheckEmailRateLimit_PerEmail(t *testing.T) {
	resetEmailQueueForTest()
	defer resetEmailQueueForTest()

	email := "alice@example.com"
	ip := "1.2.3.4"
	limit, _ := loadEmailRateLimits()

	// 前 N-1 次都该通过 + 计数
	for i := 0; i < limit; i++ {
		if err := CheckEmailRateLimit(email, ip); err != nil {
			t.Fatalf("call %d should pass: %v", i+1, err)
		}
		RegisterEmailSent(email, ip)
	}
	// 第 N+1 次该被拒
	err := CheckEmailRateLimit(email, ip)
	if !errors.Is(err, ErrEmailRateLimitExceeded) {
		t.Errorf("expected ErrEmailRateLimitExceeded, got %v", err)
	}
}

func TestCheckEmailRateLimit_PerIP(t *testing.T) {
	resetEmailQueueForTest()
	defer resetEmailQueueForTest()

	_, ipLimit := loadEmailRateLimits()
	ip := "9.9.9.9"

	// 用不同 email 但同一 IP 灌满 ip-bucket
	for i := 0; i < ipLimit; i++ {
		email := fmt.Sprintf("user%d@example.com", i)
		if err := CheckEmailRateLimit(email, ip); err != nil {
			t.Fatalf("call %d should pass: %v", i+1, err)
		}
		RegisterEmailSent(email, ip)
	}
	// 新 email 但同 IP → 仍被拒（per-IP 限流）
	err := CheckEmailRateLimit("brand-new@example.com", ip)
	if !errors.Is(err, ErrEmailRateLimitExceeded) {
		t.Errorf("per-IP limit not enforced, got %v", err)
	}
	if !strings.Contains(err.Error(), "per-IP") {
		t.Errorf("error should mention per-IP, got %v", err)
	}
}

func TestCheckEmailRateLimit_EmptyValuesSkipped(t *testing.T) {
	resetEmailQueueForTest()
	defer resetEmailQueueForTest()

	// 空 email + 空 IP → 永远通过（不上限制）
	for i := 0; i < 100; i++ {
		if err := CheckEmailRateLimit("", ""); err != nil {
			t.Fatalf("empty values should never be rate-limited: %v", err)
		}
		RegisterEmailSent("", "")
	}
}

func TestCheckEmailRateLimit_ResetsAfterWindow(t *testing.T) {
	resetEmailQueueForTest()
	defer resetEmailQueueForTest()

	email := "reset@example.com"
	ip := "5.5.5.5"
	limit, _ := loadEmailRateLimits()

	for i := 0; i < limit; i++ {
		_ = CheckEmailRateLimit(email, ip)
		RegisterEmailSent(email, ip)
	}
	if err := CheckEmailRateLimit(email, ip); err == nil {
		t.Fatal("expected limit hit")
	}

	// 强制把 bucket 的 windowStart 推到过去 → 限流应自动重置
	emailRateLimitMu.Lock()
	if b := emailSentByEmail[email]; b != nil {
		b.windowStart = time.Now().Add(-2 * emailRateLimitWindow)
	}
	if b := emailSentByIP[ip]; b != nil {
		b.windowStart = time.Now().Add(-2 * emailRateLimitWindow)
	}
	emailRateLimitMu.Unlock()

	if err := CheckEmailRateLimit(email, ip); err != nil {
		t.Errorf("after window expiry, limit should reset: %v", err)
	}
}

func TestDedup_HitWithinTTL(t *testing.T) {
	resetEmailQueueForTest()
	defer resetEmailQueueForTest()

	key := "verify:user-1:2026-05-20"
	now := time.Now()

	if dedupHit(key) {
		t.Error("first check should miss")
	}
	recordDedup(key, now)
	if !dedupHit(key) {
		t.Error("immediate re-check should hit")
	}

	// 超过 TTL 后应 miss + map 自动清理
	emailDedupMu.Lock()
	emailDedupMap[key] = now.Add(-emailDedupTTL - time.Minute)
	emailDedupMu.Unlock()
	if dedupHit(key) {
		t.Error("after TTL expiry should miss")
	}
	emailDedupMu.Lock()
	_, stillThere := emailDedupMap[key]
	emailDedupMu.Unlock()
	if stillThere {
		t.Error("expired entry should be GCed")
	}
}

func TestSendEmailDeduped_FirstCallEnqueuesSecondDedups(t *testing.T) {
	resetEmailQueueForTest()
	defer resetEmailQueueForTest()

	// 第一次：dedup miss → 尝试 enqueue。若 enqueue 失败 (smtp 没启动) 也算入队不算 dedup
	task := EmailTask{
		To:       "a@example.com",
		Message:  EmailMessage{Subject: "S", TextBody: "B"},
		DedupKey: "test-dedup-1",
		Label:    "test",
	}
	err := SendEmailDeduped(task)
	// 这里可能返回 nil（成功 enqueue）或 ErrEmailQueueFull（队列满），但绝不应是 ErrEmailDedup
	if errors.Is(err, ErrEmailDedup) {
		t.Fatalf("first call should not dedup, got %v", err)
	}

	// 第二次：dedup 命中
	err = SendEmailDeduped(task)
	if !errors.Is(err, ErrEmailDedup) {
		t.Errorf("second call should dedup, got %v", err)
	}
}

func TestSendEmailDeduped_EmptyDedupKeyNeverDedup(t *testing.T) {
	resetEmailQueueForTest()
	defer resetEmailQueueForTest()

	task := EmailTask{
		To:      "a@example.com",
		Message: EmailMessage{Subject: "S", TextBody: "B"},
		// DedupKey 留空
		Label: "no-dedup",
	}
	for i := 0; i < 3; i++ {
		err := SendEmailDeduped(task)
		if errors.Is(err, ErrEmailDedup) {
			t.Errorf("call %d should not dedup (empty key), got %v", i+1, err)
		}
	}
}

func TestLoadEmailRateLimits_DefaultsAndOverrides(t *testing.T) {
	SysConfigMutex.Lock()
	prev := SysConfigCache
	SysConfigCache = map[string]string{}
	SysConfigMutex.Unlock()
	defer func() {
		SysConfigMutex.Lock()
		SysConfigCache = prev
		SysConfigMutex.Unlock()
	}()

	t.Run("defaults when SysConfig empty", func(t *testing.T) {
		e, i := loadEmailRateLimits()
		if e != emailRateLimitPerEmailDefault || i != emailRateLimitPerIPDefault {
			t.Errorf("defaults got (%d,%d) want (%d,%d)", e, i, emailRateLimitPerEmailDefault, emailRateLimitPerIPDefault)
		}
	})

	t.Run("valid overrides applied", func(t *testing.T) {
		SysConfigMutex.Lock()
		SysConfigCache[emailRateLimitPerEmailKey] = "10"
		SysConfigCache[emailRateLimitPerIPKey] = "100"
		SysConfigMutex.Unlock()
		e, i := loadEmailRateLimits()
		if e != 10 || i != 100 {
			t.Errorf("overrides got (%d,%d) want (10,100)", e, i)
		}
	})

	t.Run("non-int falls to defaults", func(t *testing.T) {
		SysConfigMutex.Lock()
		SysConfigCache[emailRateLimitPerEmailKey] = "abc"
		SysConfigCache[emailRateLimitPerIPKey] = ""
		SysConfigMutex.Unlock()
		e, i := loadEmailRateLimits()
		if e != emailRateLimitPerEmailDefault {
			t.Errorf("non-int should fall to default, got %d", e)
		}
		if i != emailRateLimitPerIPDefault {
			t.Errorf("empty should fall to default, got %d", i)
		}
	})

	t.Run("negative or huge values rejected", func(t *testing.T) {
		SysConfigMutex.Lock()
		SysConfigCache[emailRateLimitPerEmailKey] = "-5"
		SysConfigCache[emailRateLimitPerIPKey] = "100000"
		SysConfigMutex.Unlock()
		e, i := loadEmailRateLimits()
		if e != emailRateLimitPerEmailDefault || i != emailRateLimitPerIPDefault {
			t.Errorf("out-of-range should fall to defaults, got (%d,%d)", e, i)
		}
	})
}

func TestRateLimitConcurrentSafety(t *testing.T) {
	resetEmailQueueForTest()
	defer resetEmailQueueForTest()

	// 50 个 goroutine 并发 Check + Register 同一 (email, ip) 不能 race
	// （Check 和 Register 都拿同一把 mu）
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			email := "concurrent@example.com"
			ip := "10.0.0.1"
			_ = CheckEmailRateLimit(email, ip)
			RegisterEmailSent(email, ip)
		}(i)
	}
	wg.Wait()
	// 不 panic 即通过；用 -race 跑能捕获 data race
}
