// Package proxy / channel_circuit_test.go
//
// Sprint5-M2：channel circuit breaker 回归测试。
//
// 测试矩阵：
//  1. 默认 closed → 不阻拦
//  2. 连续失败到阈值 → open，跳过该 channel
//  3. cooldown 过期 → half-open，允许 1 个 probe
//  4. probe 成功 → closed + 失败计数清零
//  5. probe 失败 → 重新 open（cooldown 翻倍）
//  6. ForceCloseChannelCircuit → 立即重置
//  7. 多请求并发：half-open 仅允许 1 个 probe（CAS 占位）
//  8. computeRetryBackoff 指数退避 + jitter 边界
package proxy

import (
	"math"
	"sync"
	"testing"
	"time"
)

// resetCircuitForTest 清理 channelCircuits sync.Map，确保各测试间不污染
func resetCircuitForTest(channelID uint) {
	channelCircuits.Delete(channelID)
	channelRateLimitCooldowns.Delete(channelID)
}

func resetChannelModelHealthForTest(channelID uint, modelName string) {
	channelModelUnhealthyUntilNs.Delete(channelModelHealthKey(channelID, modelName))
}

func TestChannelCircuit_DefaultClosed(t *testing.T) {
	resetCircuitForTest(101)
	if IsChannelCircuitOpen(101) {
		t.Errorf("default state should be closed (no failure history)")
	}
}

func TestChannelCircuit_OpensAfterThreshold(t *testing.T) {
	resetCircuitForTest(102)
	// 默认 openThreshold=5；连续 4 次失败仍 closed
	for i := 0; i < 4; i++ {
		MarkChannelFailure(102, 500)
		if IsChannelCircuitOpen(102) {
			t.Fatalf("after %d failures (< threshold), should still be closed", i+1)
		}
	}
	// 第 5 次失败触发 open
	MarkChannelFailure(102, 500)
	if !IsChannelCircuitOpen(102) {
		t.Errorf("after 5 consecutive failures, circuit should be OPEN")
	}
}

func TestChannelCircuit_SuccessResetsFailureCount(t *testing.T) {
	resetCircuitForTest(103)
	for i := 0; i < 4; i++ {
		MarkChannelFailure(103, 500)
	}
	// 一次成功重置
	MarkChannelSuccess(103)
	// 再来 4 次失败应仍 closed
	for i := 0; i < 4; i++ {
		MarkChannelFailure(103, 500)
		if IsChannelCircuitOpen(103) {
			t.Fatalf("after success-reset + %d failures, should still be closed (counter reset)", i+1)
		}
	}
}

func TestChannelCircuit_ForceCloseRecovers(t *testing.T) {
	resetCircuitForTest(104)
	for i := 0; i < 5; i++ {
		MarkChannelFailure(104, 500)
	}
	if !IsChannelCircuitOpen(104) {
		t.Fatalf("setup: should be open after 5 failures")
	}
	ForceCloseChannelCircuit(104)
	if IsChannelCircuitOpen(104) {
		t.Errorf("after ForceClose, circuit should be closed immediately")
	}
}

// 直接操作 channelHealth.openUntilNano 模拟 cooldown 过期，避免真实 sleep
func setCooldownExpired(channelID uint) {
	h := getChannelHealth(channelID)
	// 把 openUntilNano 设为过去时间
	cooldownSec := int64(30)
	if state := h.state.Load(); state != nil && state.cooldownSec > 0 {
		cooldownSec = state.cooldownSec
	}
	h.state.Store(&circuitState{
		cooldownSec:   cooldownSec,
		openUntilNano: time.Now().UnixNano() - int64(time.Second),
	})
	h.halfOpenInflight.Store(false)
}

func TestChannelCircuit_HalfOpenAllowsOneProbe(t *testing.T) {
	resetCircuitForTest(105)
	for i := 0; i < 5; i++ {
		MarkChannelFailure(105, 500)
	}
	// 模拟 cooldown 过期
	setCooldownExpired(105)
	// 第一次查询：half-open，CAS 占位 inflight → 允许通过（返回 false=not open）
	if IsChannelCircuitOpen(105) {
		t.Errorf("half-open first probe should be allowed (returned not-open)")
	}
	// 第二次同时查询：已有 inflight probe → 返回 true=open
	if !IsChannelCircuitOpen(105) {
		t.Errorf("half-open second concurrent probe should be rejected (returned open)")
	}
}

func TestChannelCircuit_HalfOpenProbeSuccess_ClosesCircuit(t *testing.T) {
	resetCircuitForTest(106)
	for i := 0; i < 5; i++ {
		MarkChannelFailure(106, 500)
	}
	setCooldownExpired(106)
	// 拿到 probe slot
	if IsChannelCircuitOpen(106) {
		t.Fatal("expected probe slot")
	}
	// Probe 成功
	MarkChannelSuccess(106)
	// 应回到 closed
	if IsChannelCircuitOpen(106) {
		t.Errorf("after successful probe, circuit should be closed")
	}
	h := getChannelHealth(106)
	if h.consecutiveFailures.Load() != 0 {
		t.Errorf("failure count should be 0 after probe success")
	}
}

func TestChannelCircuit_HalfOpenProbeFailure_ReopensWithBackoff(t *testing.T) {
	resetCircuitForTest(107)
	for i := 0; i < 5; i++ {
		MarkChannelFailure(107, 500)
	}
	h := getChannelHealth(107)
	initialState := h.state.Load()
	if initialState == nil {
		t.Fatalf("expected initial open state")
	}
	initialCooldown := initialState.cooldownSec
	if initialCooldown != 30 {
		t.Fatalf("expected initial cooldown 30s, got %d", initialCooldown)
	}

	setCooldownExpired(107)
	// 拿到 probe slot
	if IsChannelCircuitOpen(107) {
		t.Fatal("expected probe slot")
	}
	// Probe 失败 → cooldown 翻倍
	MarkChannelFailure(107, 500)
	if !IsChannelCircuitOpen(107) {
		t.Errorf("after probe failure, should be open again")
	}
	newState := h.state.Load()
	if newState == nil {
		t.Fatalf("expected reopened state")
	}
	newCooldown := newState.cooldownSec
	if newCooldown != 60 {
		t.Errorf("expected doubled cooldown 60s, got %d", newCooldown)
	}
}

func TestChannelCircuit_ConcurrentSafeProbing(t *testing.T) {
	resetCircuitForTest(108)
	for i := 0; i < 5; i++ {
		MarkChannelFailure(108, 500)
	}
	setCooldownExpired(108)

	// 50 并发查询 half-open，仅 1 个应拿到 probe slot
	var wg sync.WaitGroup
	var probeAllowed int32
	mu := sync.Mutex{}
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !IsChannelCircuitOpen(108) {
				mu.Lock()
				probeAllowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if probeAllowed != 1 {
		t.Errorf("concurrent half-open should allow exactly 1 probe, got %d", probeAllowed)
	}
}

func TestComputeRetryBackoff_ExponentialWithJitter(t *testing.T) {
	// attempt=0 → 0
	if d := computeRetryBackoff(0); d != 0 {
		t.Errorf("attempt=0 should be 0, got %v", d)
	}
	// attempt=1 → 100ms~150ms
	d1 := computeRetryBackoff(1)
	if d1 < 100*time.Millisecond || d1 > 150*time.Millisecond {
		t.Errorf("attempt=1 should be 100~150ms, got %v", d1)
	}
	// attempt=2 → 200ms~300ms
	d2 := computeRetryBackoff(2)
	if d2 < 200*time.Millisecond || d2 > 300*time.Millisecond {
		t.Errorf("attempt=2 should be 200~300ms, got %v", d2)
	}
	// attempt=10 → clamp to maxMs=2000ms (+ jitter 0~1000ms) → 2000~3000ms
	d10 := computeRetryBackoff(10)
	if d10 < 2000*time.Millisecond || d10 > 3000*time.Millisecond {
		t.Errorf("attempt=10 should clamp to 2000~3000ms, got %v", d10)
	}
}

func TestComputeRetryBackoff_BitShift(t *testing.T) {
	if got := retryBackoffBaseDelayMS(0); got != 0 {
		t.Fatalf("attempt=0 base delay=%d want 0", got)
	}
	for attempt := 1; attempt <= 10; attempt++ {
		want := int64(100) * int64(math.Pow(2, float64(attempt-1)))
		if want > 2000 {
			want = 2000
		}
		if got := retryBackoffBaseDelayMS(attempt); got != want {
			t.Fatalf("attempt=%d base delay=%d want %d", attempt, got, want)
		}
	}
}

func TestComputeRetryBackoff_JitterDistribution(t *testing.T) {
	seen := make(map[time.Duration]int)
	for i := 0; i < 1000; i++ {
		d := computeRetryBackoff(3)
		if d < 400*time.Millisecond || d > 600*time.Millisecond {
			t.Fatalf("attempt=3 should be 400~600ms, got %v", d)
		}
		seen[d]++
	}
	if len(seen) < 50 {
		t.Fatalf("jitter distribution too narrow: got %d unique durations", len(seen))
	}
}

func TestCircuitState_AtomicPointer(t *testing.T) {
	const channelID uint = 301
	resetCircuitForTest(channelID)
	base := time.Unix(2_000_000_000, 0)
	h := getChannelHealth(channelID)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		secs := []int64{30, 60}
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
				sec := secs[i%len(secs)]
				h.state.Store(&circuitState{
					cooldownSec:   sec,
					openUntilNano: base.Add(time.Duration(sec) * time.Second).UnixNano(),
				})
				i++
			}
		}
	}()

	for i := 0; i < 1000; i++ {
		snaps := GetChannelCircuitSnapshot()
		for _, snap := range snaps {
			if snap.ChannelID != channelID || snap.OpenUntil == nil {
				continue
			}
			gotSec := int64(snap.OpenUntil.Sub(base) / time.Second)
			if gotSec != snap.CurrentCooldownSec {
				close(stop)
				wg.Wait()
				t.Fatalf("inconsistent snapshot: cooldown=%d openUntilDelta=%d", snap.CurrentCooldownSec, gotSec)
			}
		}
	}
	close(stop)
	wg.Wait()
}

func TestLoadCircuitConfig_Cached(t *testing.T) {
	SysConfigMutex.Lock()
	old := SysConfigCache
	SysConfigCache = map[string]string{"channel_circuit_open_threshold": "7"}
	SysConfigMutex.Unlock()
	ResetCircuitConfigCache()
	defer func() {
		SysConfigMutex.Lock()
		SysConfigCache = old
		SysConfigMutex.Unlock()
		ResetCircuitConfigCache()
	}()

	first := loadCircuitConfig()
	if first.OpenThreshold != 7 {
		t.Fatalf("first threshold=%d want 7", first.OpenThreshold)
	}

	SysConfigMutex.Lock()
	SysConfigCache["channel_circuit_open_threshold"] = "11"
	SysConfigMutex.Unlock()

	second := loadCircuitConfig()
	if second != first {
		t.Fatalf("second load should return cached pointer")
	}
	if second.OpenThreshold != 7 {
		t.Fatalf("cached threshold=%d want 7", second.OpenThreshold)
	}

	ResetCircuitConfigCache()
	third := loadCircuitConfig()
	if third.OpenThreshold != 11 {
		t.Fatalf("after reset threshold=%d want 11", third.OpenThreshold)
	}
}

// TestChannelCircuit_SnapshotMonitoring 验证 admin 监控快照能反映各状态
func TestChannelCircuit_SnapshotMonitoring(t *testing.T) {
	resetCircuitForTest(201)
	resetCircuitForTest(202)
	resetCircuitForTest(203)

	// 201: closed (一次失败但未到阈值)
	MarkChannelFailure(201, 500)
	// 202: open (失败 5 次)
	for i := 0; i < 5; i++ {
		MarkChannelFailure(202, 500)
	}
	// 203: healthy success
	MarkChannelSuccess(203)

	snaps := GetChannelCircuitSnapshot()
	got := make(map[uint]string)
	for _, s := range snaps {
		got[s.ChannelID] = s.State
	}
	if got[201] != "closed" {
		t.Errorf("ch 201 state=%q want closed (failures<threshold)", got[201])
	}
	if got[202] != "open" {
		t.Errorf("ch 202 state=%q want open (failures=threshold)", got[202])
	}
	// 203 调过 MarkChannelSuccess 但开始就是 closed/0；不在 sync.Map 里
	// （Snapshot 只列有过失败记录的 channel）— 这是正常行为
}
