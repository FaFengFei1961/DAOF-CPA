// Package proxy / channel_circuit_test.go
//
// Sprint5-M2：channel circuit breaker 回归测试。
//
// 测试矩阵：
//   1. 默认 closed → 不阻拦
//   2. 连续失败到阈值 → open，跳过该 channel
//   3. cooldown 过期 → half-open，允许 1 个 probe
//   4. probe 成功 → closed + 失败计数清零
//   5. probe 失败 → 重新 open（cooldown 翻倍）
//   6. ForceCloseChannelCircuit → 立即重置
//   7. 多请求并发：half-open 仅允许 1 个 probe（CAS 占位）
//   8. computeRetryBackoff 指数退避 + jitter 边界
package proxy

import (
	"sync"
	"testing"
	"time"
)

// resetCircuitForTest 清理 channelCircuits sync.Map，确保各测试间不污染
func resetCircuitForTest(channelID uint) {
	channelCircuits.Delete(channelID)
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
	h.openUntilNano.Store(time.Now().UnixNano() - int64(time.Second))
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
	initialCooldown := h.currentCooldownSec.Load()
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
	newCooldown := h.currentCooldownSec.Load()
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
