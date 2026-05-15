// Package proxy / subscription_cache.go
//
// 用户活跃订阅的内存缓存。FIFO 排序、TTL 由 SysConfig 配置。
package proxy

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"daof-ai-hub/database"
)

type CachedSubscription struct {
	Subscription *database.UserSubscription
	Snapshot     map[string]any // package_snapshot 反序列化结果（含 plans）
}

type userSubsBucket struct {
	subs       []*CachedSubscription // 已按 ConsumptionOrder ASC 排序
	expiresAt  time.Time
	lastUsedNS atomic.Int64 // 用于 LRU 驱逐，存 unix nano；atomic 避免 hit 时启动 goroutine
}

var (
	subCacheMu sync.RWMutex
	subCache   = map[uint]*userSubsBucket{}

	// G-M4 修复：合并并发同 userID 的 cache miss，避免对同一用户的 N 个并发请求各打一次 DB
	subLoadMu      sync.Mutex
	subLoadInFlght = map[uint]chan struct{}{}
)

// 缓存容量上限。超出时驱逐最近最少使用的 entry。可配 SysConfig.subscription_cache_max_users。
const defaultSubCacheMaxUsers = 50000

// InvalidateUserSubscriptionCache 用户购买/取消/订阅状态变化时调用
func InvalidateUserSubscriptionCache(userID uint) {
	subCacheMu.Lock()
	delete(subCache, userID)
	subCacheMu.Unlock()
}

// FlushAllSubscriptionCache 清空全部订阅缓存（factory reset 等场景）
func FlushAllSubscriptionCache() {
	subCacheMu.Lock()
	subCache = map[uint]*userSubsBucket{}
	subCacheMu.Unlock()
}

// GetUserActiveSubscriptions 返回用户的活跃订阅（FIFO 顺序）。带 TTL 缓存 + LRU 驱逐 + 单飞合并。
//
// fix HIGH NEW-H3（codex 第十八轮）：返回缓存内底层 slice 的**拷贝**，不是原引用。
// fix CRITICAL R23+2-C3（codex 全方面审查）：DB 失败必须返回 error 给调用方，
// 调用方（Decide）应 fail-closed 而不是当作"无订阅"让用户被错误降级到余额扣费。
//
// 返回 (subs, err)：err != nil 表示 DB 加载失败，调用方应按"无法决策"处理（如 503）。
func GetUserActiveSubscriptions(userID uint) ([]*CachedSubscription, error) {
	ttl := getSubscriptionCacheTTL()
	now := time.Now()

	subCacheMu.RLock()
	bucket, ok := subCache[userID]
	if ok && now.Before(bucket.expiresAt) {
		subs := append([]*CachedSubscription(nil), bucket.subs...)
		bucket.lastUsedNS.Store(now.UnixNano())
		subCacheMu.RUnlock()
		return subs, nil
	}
	subCacheMu.RUnlock()

	// G-M4 修复：单飞合并 — 同一 userID 的并发 miss 仅执行一次 DB 加载，其余等待
	subLoadMu.Lock()
	if ch, inflight := subLoadInFlght[userID]; inflight {
		subLoadMu.Unlock()
		<-ch // 等领头 goroutine 完成
		// 重新读缓存（一定已被领头 goroutine 写入）
		subCacheMu.RLock()
		if b, ok := subCache[userID]; ok {
			subs := append([]*CachedSubscription(nil), b.subs...)
			b.lastUsedNS.Store(time.Now().UnixNano())
			subCacheMu.RUnlock()
			return subs, nil
		}
		subCacheMu.RUnlock()
		// 领头 goroutine 失败 → 返回 error 让调用方 fail-closed
		return nil, errSubLoadFailedFollower
	}
	done := make(chan struct{})
	subLoadInFlght[userID] = done
	subLoadMu.Unlock()
	defer func() {
		subLoadMu.Lock()
		delete(subLoadInFlght, userID)
		subLoadMu.Unlock()
	}()
	defer close(done)

	// 重新加载
	if database.DB == nil {
		// 测试环境/启动期 DB 还没初始化 → 当作"无订阅"，不报错（与 engine disabled 等价）
		return []*CachedSubscription{}, nil
	}
	// fix MAJOR Sprint2-M4：加 start_at <= now 守卫，防未来生效订阅被提前激活。
	// 旧实现仅查 status='active' + end_at > now，admin 给用户开 7 天后才生效的订阅
	// 会立即被引擎拿来扣费（产品/审计语义错位）。
	// 注：StartAt 是 time.Time，零值（0001-01-01）总是 ≤ now，向后兼容历史数据。
	var rows []database.UserSubscription
	if err := database.DB.Where("user_id = ? AND status = ? AND start_at <= ? AND end_at > ?", userID, "active", now, now).
		Order("consumption_order ASC").Find(&rows).Error; err != nil {
		log.Printf("[SUB-CACHE] DB load failed user=%d: %v (fail-closed)", userID, err)
		return nil, fmt.Errorf("db load: %w", err)
	}

	cached := make([]*CachedSubscription, 0, len(rows))
	for i := range rows {
		entry := &CachedSubscription{Subscription: &rows[i]}
		if rows[i].PackageSnapshot != "" {
			parsed := map[string]any{}
			if err := jsonUnmarshalSafe(rows[i].PackageSnapshot, &parsed); err != nil {
				log.Printf("[SUB-CACHE] snapshot parse failed sub_id=%d user=%d err=%v",
					rows[i].ID, rows[i].UserID, err)
			}
			entry.Snapshot = parsed
		}
		cached = append(cached, entry)
	}

	newBucket := &userSubsBucket{
		subs:      cached,
		expiresAt: now.Add(ttl),
	}
	newBucket.lastUsedNS.Store(now.UnixNano())
	subCacheMu.Lock()
	subCache[userID] = newBucket
	maxUsers := getSubCacheMaxUsers()
	if len(subCache) > maxUsers {
		evictCacheLocked(maxUsers, now)
	}
	subCacheMu.Unlock()
	// 同 H3 修复：返回拷贝，与上面缓存命中路径一致
	return append([]*CachedSubscription(nil), cached...), nil
}

// errSubLoadFailedFollower 单飞合并的"跟随" goroutine 看到 leader 失败时返回此错误。
// 这是 sentinel，调用方按"DB 加载失败"处理即可。
var errSubLoadFailedFollower = fmt.Errorf("subscription leader load failed")

// evictCacheLocked 必须在持有 subCacheMu.Lock() 的前提下调用。
// 策略：先删所有已过期 entry；仍超容量则按 lastUsed 升序删除最旧的，直到 80% 容量。
func evictCacheLocked(maxUsers int, now time.Time) {
	for k, v := range subCache {
		if now.After(v.expiresAt) {
			delete(subCache, k)
		}
	}
	if len(subCache) <= maxUsers {
		return
	}
	target := maxUsers * 8 / 10
	type pair struct {
		uid    uint
		usedNS int64
	}
	all := make([]pair, 0, len(subCache))
	for k, v := range subCache {
		all = append(all, pair{k, v.lastUsedNS.Load()})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].usedNS < all[j].usedNS })
	toEvict := len(all) - target
	for i := 0; i < toEvict && i < len(all); i++ {
		delete(subCache, all[i].uid)
	}
}

func getSubCacheMaxUsers() int {
	SysConfigMutex.RLock()
	v := SysConfigCache["subscription_cache_max_users"]
	SysConfigMutex.RUnlock()
	if v == "" {
		return defaultSubCacheMaxUsers
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 100 {
		return defaultSubCacheMaxUsers
	}
	return n
}

func getSubscriptionCacheTTL() time.Duration {
	SysConfigMutex.RLock()
	v := strings.TrimSpace(SysConfigCache["subscription_cache_ttl_seconds"])
	SysConfigMutex.RUnlock()
	if v == "" {
		return 30 * time.Second
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 30 * time.Second
	}
	return time.Duration(n) * time.Second
}

// jsonUnmarshalSafe 简单包装，便于将来切换到更快的解析器（如 sonic）
func jsonUnmarshalSafe(s string, out any) error {
	return json.Unmarshal([]byte(s), out)
}
