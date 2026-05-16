// Package proxy / notification_pref_cache.go
//
// 用户通知偏好的进程内缓存：sync.Map + 时间戳 TTL。
//
// 为什么需要缓存：subscription_engine.atomicConsume 是热路径，每次 LLM 调用都会
// 跑阈值检查；阈值检查需要读用户偏好；如果每次都查 DB 会成为性能瓶颈。
// TTL 默认 600s（可由 SysConfig 'notif_pref_cache_ttl_seconds' 调整）。
//
// 一致性保障：UpdateMyNotificationPreference 必须在写库后调 InvalidatePrefCache(uid)，
// 否则用户改了偏好但 10 分钟内不生效。
package proxy

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"daof-cpa/database"
)

type prefCacheEntry struct {
	view     *database.PreferenceView
	expireAt time.Time
}

var prefCache sync.Map // map[uint]*prefCacheEntry

// 只有 Janitor 起一次的同步原语
var (
	prefCacheJanitorOnce sync.Once
	prefCacheJanitorDone chan struct{}
)

// GetPrefCached 取（或加载）用户偏好。永不返回 nil（DB 失败也用系统默认填）。
func GetPrefCached(userID uint) *database.PreferenceView {
	if v, ok := prefCache.Load(userID); ok {
		entry := v.(*prefCacheEntry)
		if time.Now().Before(entry.expireAt) {
			return entry.view
		}
	}
	view := database.LoadPreference(userID)
	prefCache.Store(userID, &prefCacheEntry{
		view:     view,
		expireAt: time.Now().Add(prefCacheTTL()),
	})
	return view
}

// InvalidatePrefCache 偏好更新后调用，强制下次读取重新从 DB 加载。
func InvalidatePrefCache(userID uint) {
	prefCache.Delete(userID)
}

// FlushPrefCache 清空所有用户偏好缓存。
//
// fix Minor Mi22-2（codex 第二十二轮）：admin 改 notif_default_categories /
// notif_default_thresholds_csv / notif_pref_cache_ttl_seconds 等全局默认值后，
// 已缓存的用户视图仍按旧默认计算，必须批量失效让下次 GetPrefCached 重读。
// 由 controller/sysconfig.go 在保存 notif_default_* 类 SysConfig 时调用。
func FlushPrefCache() {
	prefCache.Range(func(key, _ any) bool {
		prefCache.Delete(key)
		return true
	})
}

// StartPrefCacheJanitor 启动后台清理 goroutine，每 5 分钟扫描过期条目。
// 由 main.go 启动时调用一次（与 StartSubscriptionCron 同位置）。
func StartPrefCacheJanitor() {
	prefCacheJanitorOnce.Do(func() {
		prefCacheJanitorDone = make(chan struct{})
		go func() {
			ticker := time.NewTicker(5 * time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					sweepPrefCache()
				case <-prefCacheJanitorDone:
					return
				}
			}
		}()
	})
}

// StopPrefCacheJanitor 停止后台清理（测试/优雅关闭）
func StopPrefCacheJanitor() {
	if prefCacheJanitorDone != nil {
		select {
		case <-prefCacheJanitorDone:
			// already closed
		default:
			close(prefCacheJanitorDone)
		}
	}
}

// sweepPrefCache 扫描并删除已过期的条目。
// sync.Map 没有大小上限保护——长期运行时活跃用户多会膨胀，定期清理回收内存。
func sweepPrefCache() {
	now := time.Now()
	prefCache.Range(func(key, value any) bool {
		entry := value.(*prefCacheEntry)
		if now.After(entry.expireAt) {
			prefCache.Delete(key)
		}
		return true
	})
}

func prefCacheTTL() time.Duration {
	SysConfigMutex.RLock()
	v := strings.TrimSpace(SysConfigCache["notif_pref_cache_ttl_seconds"])
	SysConfigMutex.RUnlock()
	if v == "" {
		return 600 * time.Second
	}
	sec, err := strconv.Atoi(v)
	if err != nil || sec < 30 {
		return 600 * time.Second
	}
	return time.Duration(sec) * time.Second
}
