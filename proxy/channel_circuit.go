// Package proxy / channel_circuit.go
//
// Per-channel circuit breaker (Sprint5-M2)。
//
// 解决 codex 模块 2 审计 P1：网关 retry 即时 + 无 backoff/jitter/circuit breaker，
// 高并发 429/5xx 会 thundering herd 把上游打挂。
//
// 状态机：
//
//   closed (健康)
//     │  连续失败 >= openThreshold (默认 5 次)
//     ↓
//   open  (拒绝所有请求 openDuration 秒，默认 30s 起阶；最大 5min)
//     │  cooldown 到期
//     ↓
//   half-open (允许 1 个 probe，原子占位 inflight 防多探针)
//     ├─ probe 成功 → closed + 失败计数清零
//     └─ probe 失败 → open（cooldown 翻倍，上限 5min）
//
// 失败定义：HTTP 4xx (401/403/429) 或 5xx 或 connect failure。
// 400 不视作 channel 故障（请求侧问题，不应触发熔断）。
//
// 内存状态：进程重启即清零（cold start 视所有 channel 健康）。
// admin force-reset 可通过 ForceCloseChannelCircuit(id) 调用。
package proxy

import (
	"math"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// channelHealth 单个 channel 的健康状态（in-memory，per-process）。
type channelHealth struct {
	consecutiveFailures atomic.Int32 // 连续失败次数（成功清零）
	openUntilNano       atomic.Int64 // unix nano；> now 表示 circuit open
	currentCooldownSec  atomic.Int64 // 当前 open 周期的秒数（指数退避）
	halfOpenInflight    atomic.Bool  // half-open 时只允许 1 个 probe
	lastFailureNano     atomic.Int64 // 最近一次失败时间（仅审计）
}

var (
	channelCircuits sync.Map // key=uint(channel_id) → *channelHealth
)

// circuitConfig 当前生效的配置（从 SysConfig 读取，带合理默认值）。
type circuitConfig struct {
	OpenThreshold   int32         // 连续失败多少次后 open（默认 5）
	InitialCooldown time.Duration // 首次 open 的冷却时间（默认 30s）
	MaxCooldown     time.Duration // open 最大持续时间（默认 5 min）
	BackoffFactor   int64         // 每次 open 翻倍因子（默认 2）
}

func loadCircuitConfig() circuitConfig {
	cfg := circuitConfig{
		OpenThreshold:   5,
		InitialCooldown: 30 * time.Second,
		MaxCooldown:     5 * time.Minute,
		BackoffFactor:   2,
	}
	SysConfigMutex.RLock()
	defer SysConfigMutex.RUnlock()
	if v := strings.TrimSpace(SysConfigCache["channel_circuit_open_threshold"]); v != "" {
		if n, err := strconv.ParseInt(v, 10, 32); err == nil && n >= 1 && n <= 1000 {
			cfg.OpenThreshold = int32(n)
		}
	}
	if v := strings.TrimSpace(SysConfigCache["channel_circuit_initial_cooldown_seconds"]); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 1 && n <= 3600 {
			cfg.InitialCooldown = time.Duration(n) * time.Second
		}
	}
	if v := strings.TrimSpace(SysConfigCache["channel_circuit_max_cooldown_seconds"]); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 1 && n <= 86400 {
			cfg.MaxCooldown = time.Duration(n) * time.Second
		}
	}
	return cfg
}

// getChannelHealth 返回（或创建）某 channel 的健康状态。
func getChannelHealth(channelID uint) *channelHealth {
	if v, ok := channelCircuits.Load(channelID); ok {
		return v.(*channelHealth)
	}
	h := &channelHealth{}
	actual, _ := channelCircuits.LoadOrStore(channelID, h)
	return actual.(*channelHealth)
}

// IsChannelCircuitOpen 返回 channel 是否处于 open 状态（应被路由跳过）。
//
// half-open 状态下：若已有 inflight probe 则返回 true（拒绝），否则原子占位 inflight 返回 false（允许探测）。
// 调用方应在请求结束后调用 MarkChannelSuccess / MarkChannelFailure。
func IsChannelCircuitOpen(channelID uint) bool {
	h := getChannelHealth(channelID)
	openUntil := h.openUntilNano.Load()
	if openUntil == 0 {
		return false // 从未失败，绝对 closed
	}
	nowNano := time.Now().UnixNano()
	if nowNano < openUntil {
		return true // 仍在 open 周期内
	}
	// cooldown 已过 → half-open：允许 1 个 probe
	if h.halfOpenInflight.CompareAndSwap(false, true) {
		return false // 占位成功，本请求作为 probe
	}
	return true // 已有 probe 在跑，其他请求继续被拒
}

// MarkChannelSuccess 标记 channel 请求成功，重置失败计数与 circuit 状态。
func MarkChannelSuccess(channelID uint) {
	h := getChannelHealth(channelID)
	h.consecutiveFailures.Store(0)
	h.openUntilNano.Store(0)
	h.currentCooldownSec.Store(0)
	h.halfOpenInflight.Store(false)
}

// MarkChannelFailure 标记 channel 请求失败：累加计数，必要时打开 circuit。
// statusCode 用于审计；400 等"请求侧问题"调用方不应调用本函数。
func MarkChannelFailure(channelID uint, statusCode int) {
	h := getChannelHealth(channelID)
	h.lastFailureNano.Store(time.Now().UnixNano())
	failures := h.consecutiveFailures.Add(1)

	// half-open probe 失败 → 立即重开 circuit（cooldown 翻倍）
	if h.halfOpenInflight.Swap(false) {
		extendCircuitOpen(h)
		return
	}

	// 普通失败累计到阈值 → 打开 circuit
	cfg := loadCircuitConfig()
	if failures >= cfg.OpenThreshold {
		// 首次 open 或从 closed 进 open
		if h.openUntilNano.Load() == 0 || h.currentCooldownSec.Load() == 0 {
			openCircuit(h, cfg.InitialCooldown.Nanoseconds(), int64(cfg.InitialCooldown.Seconds()), cfg.MaxCooldown)
		} else {
			// 已 open 状态下再次累加失败（不应该发生，因为 open 时请求会被跳过）
			extendCircuitOpen(h)
		}
	}
}

// openCircuit 设置 circuit 为 open 状态，cooldown = cooldownNano。
func openCircuit(h *channelHealth, cooldownNano int64, cooldownSec int64, maxCooldown time.Duration) {
	h.openUntilNano.Store(time.Now().UnixNano() + cooldownNano)
	h.currentCooldownSec.Store(cooldownSec)
	h.halfOpenInflight.Store(false)
	_ = maxCooldown // 仅供 caller 文档，本函数内不需要
}

// extendCircuitOpen 把当前 cooldown 翻倍后重新 open（probe 失败时调用）。
func extendCircuitOpen(h *channelHealth) {
	cfg := loadCircuitConfig()
	curSec := h.currentCooldownSec.Load()
	if curSec == 0 {
		curSec = int64(cfg.InitialCooldown.Seconds())
	}
	nextSec := curSec * cfg.BackoffFactor
	maxSec := int64(cfg.MaxCooldown.Seconds())
	if nextSec > maxSec {
		nextSec = maxSec
	}
	h.currentCooldownSec.Store(nextSec)
	h.openUntilNano.Store(time.Now().UnixNano() + nextSec*int64(time.Second))
	h.halfOpenInflight.Store(false)
}

// ForceCloseChannelCircuit admin 手动重置 channel circuit（force-close）。
// 用于 admin 修复 channel 配置后立即恢复路由，无需等 cooldown。
func ForceCloseChannelCircuit(channelID uint) {
	MarkChannelSuccess(channelID)
}

// ChannelCircuitSnapshot 返回某 channel 的当前状态（admin UI 用）。
type ChannelCircuitSnapshot struct {
	ChannelID           uint
	ConsecutiveFailures int32
	OpenUntil           *time.Time // nil 表示 closed
	CurrentCooldownSec  int64
	State               string // "closed" / "open" / "half_open"
}

// GetChannelCircuitSnapshot 返回所有 channel 的 circuit 状态快照（按 channel_id 排序）。
// 主要给 admin 监控面板用。
func GetChannelCircuitSnapshot() []ChannelCircuitSnapshot {
	var out []ChannelCircuitSnapshot
	now := time.Now()
	channelCircuits.Range(func(k, v any) bool {
		id, _ := k.(uint)
		h, _ := v.(*channelHealth)
		openUntilNano := h.openUntilNano.Load()
		snap := ChannelCircuitSnapshot{
			ChannelID:           id,
			ConsecutiveFailures: h.consecutiveFailures.Load(),
			CurrentCooldownSec:  h.currentCooldownSec.Load(),
		}
		if openUntilNano == 0 {
			snap.State = "closed"
		} else {
			openUntilT := time.Unix(0, openUntilNano)
			snap.OpenUntil = &openUntilT
			if now.UnixNano() < openUntilNano {
				snap.State = "open"
			} else {
				if h.halfOpenInflight.Load() {
					snap.State = "half_open"
				} else {
					snap.State = "half_open" // cooldown 过期但还没收到第一个 probe
				}
			}
		}
		out = append(out, snap)
		return true
	})
	return out
}

// computeRetryBackoff 单请求 retry 的指数退避 + jitter（第 attempt 次重试前的等待时间）。
//
// fix CRITICAL Sprint5-M2：从"即时下一跳"改为指数退避（100ms 起，2x，上限 2s）+ 0-50% jitter。
// 让"上游 thundering herd"获得喘息时间，同时单次重试总延迟 < 2s + 2s + 2s = 6s。
// attempt=0 返回 0（首次尝试不退避）。
func computeRetryBackoff(attempt int) time.Duration {
	if attempt <= 0 {
		return 0
	}
	const baseMs = 100
	const maxMs = 2000
	delay := int64(baseMs) * int64(math.Pow(2, float64(attempt-1)))
	if delay > maxMs {
		delay = maxMs
	}
	// jitter：0 ~ delay/2，避免多请求同时退避命中同一波
	jitter := int64(0)
	if delay > 0 {
		jitter = int64(time.Now().UnixNano()/1000) % (delay / 2)
	}
	return time.Duration(delay+jitter) * time.Millisecond
}
