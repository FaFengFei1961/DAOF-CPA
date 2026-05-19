package proxy

// coverage_fillers_test.go
//
// 这个文件是 M-R3 的产物——把 proxy 包里一批小而关键、覆盖率 0% 的纯函数（balance
// consume status 视图 / cache 增删 / channel type 白名单 / channel circuit 健康标记）
// 通过 characterization 测试钉住，把 proxy 包覆盖率从 56.8% 推到 ≥60%。
//
// 不动业务逻辑：每个 case 只验证既有行为，作为后续重构（如 P10+ credits_pool 拆分）
// 的回归网。

import (
	"sync"
	"testing"
	"time"

	"daof-cpa/database"
)

// ─── balance_consume.go::GetBalanceConsumeStatus ──────────────────────────────

func TestGetBalanceConsumeStatus_FreshUser(t *testing.T) {
	user := &database.User{
		BalanceConsumeEnabled:       true,
		BalanceConsumeLimitUSD:      1_000_000, // $1
		BalanceConsumeWindowSeconds: 2_592_000, // 30 天
		BalanceConsumeWindowStartAt: nil,       // 未开始
		BalanceConsumedInWindow:     0,
	}
	st := GetBalanceConsumeStatus(user)
	if !st.Enabled {
		t.Errorf("Enabled=%v want true", st.Enabled)
	}
	if st.LimitMicroUSD != 1_000_000 || st.WindowSeconds != 2_592_000 || st.ConsumedInWindowMicroUSD != 0 {
		t.Errorf("aggregated fields wrong: %+v", st)
	}
	// 未开始窗口：StartAt 应回退到 now，ResetsAt 应在 30 天后
	if st.WindowStartAt.IsZero() {
		t.Error("WindowStartAt should default to now when user.BalanceConsumeWindowStartAt is nil")
	}
	gap := st.ResetsAt.Sub(st.WindowStartAt)
	if gap < 29*24*time.Hour || gap > 31*24*time.Hour {
		t.Errorf("ResetsAt-StartAt=%v want ~30d", gap)
	}
}

func TestGetBalanceConsumeStatus_ActiveWindow(t *testing.T) {
	start := time.Now().Add(-5 * 24 * time.Hour) // 5 天前开始
	user := &database.User{
		BalanceConsumeEnabled:       true,
		BalanceConsumeLimitUSD:      5_000_000,
		BalanceConsumeWindowSeconds: 2_592_000,
		BalanceConsumeWindowStartAt: &start,
		BalanceConsumedInWindow:     1_234_567,
	}
	st := GetBalanceConsumeStatus(user)
	if !st.WindowStartAt.Equal(start) {
		t.Errorf("WindowStartAt=%v want %v", st.WindowStartAt, start)
	}
	expectedReset := start.Add(30 * 24 * time.Hour)
	if !st.ResetsAt.Equal(expectedReset) {
		t.Errorf("ResetsAt=%v want %v", st.ResetsAt, expectedReset)
	}
	if st.ConsumedInWindowMicroUSD != 1_234_567 {
		t.Errorf("ConsumedInWindow=%d want 1_234_567", st.ConsumedInWindowMicroUSD)
	}
}

func TestGetBalanceConsumeStatus_ExpiredWindowShowsNowAsReset(t *testing.T) {
	// 窗口已过期：ResetsAt 应回退到 now（"下次首次消费时重置"）
	start := time.Now().Add(-60 * 24 * time.Hour) // 60 天前
	user := &database.User{
		BalanceConsumeEnabled:       true,
		BalanceConsumeLimitUSD:      1_000_000,
		BalanceConsumeWindowSeconds: 2_592_000, // 30 天，已过期
		BalanceConsumeWindowStartAt: &start,
		BalanceConsumedInWindow:     999_999, // 即将清零
	}
	st := GetBalanceConsumeStatus(user)
	// ResetsAt 应被钳制到 now（实测允许 5s 抖动）
	if diff := time.Since(st.ResetsAt); diff > 5*time.Second || diff < -5*time.Second {
		t.Errorf("expired window ResetsAt=%v should be ~now; diff=%v", st.ResetsAt, diff)
	}
}

// ─── balance_consume.go::CheckBalanceConsumeAllowed extra branches ────────────

func TestCheckBalanceConsumeAllowed_DisabledUser(t *testing.T) {
	user := &database.User{BalanceConsumeEnabled: false}
	if CheckBalanceConsumeAllowed(user, 1000) {
		t.Error("disabled user should always be rejected")
	}
	if CheckBalanceConsumeAllowed(nil, 1000) {
		t.Error("nil user should be rejected")
	}
}

func TestCheckBalanceConsumeAllowed_UnlimitedWhenLimitZero(t *testing.T) {
	user := &database.User{
		BalanceConsumeEnabled:  true,
		BalanceConsumeLimitUSD: 0, // 不限
	}
	if !CheckBalanceConsumeAllowed(user, 1<<60) {
		t.Error("limit=0 should allow any amount")
	}
}

func TestCheckBalanceConsumeAllowed_OverflowProtection(t *testing.T) {
	start := time.Now()
	const big = int64(1) << 62
	user := &database.User{
		BalanceConsumeEnabled:       true,
		BalanceConsumeLimitUSD:      big,
		BalanceConsumeWindowSeconds: 2_592_000,
		BalanceConsumeWindowStartAt: &start,
		BalanceConsumedInWindow:     big, // 加 big 会溢出 int64
	}
	// CheckedAddInt64 应识别溢出并拒绝（fail-closed）
	if CheckBalanceConsumeAllowed(user, big) {
		t.Error("overflow should fail closed, not allow")
	}
}

func TestCheckBalanceConsumeAllowed_ExpiredWindowResetsCheck(t *testing.T) {
	// 30 天窗口已过期 60 天 → 应等于"新窗口未消费"，单次 delta 只要 ≤ limit 即放行
	start := time.Now().Add(-60 * 24 * time.Hour)
	user := &database.User{
		BalanceConsumeEnabled:       true,
		BalanceConsumeLimitUSD:      1_000_000,
		BalanceConsumeWindowSeconds: 2_592_000,
		BalanceConsumeWindowStartAt: &start,
		BalanceConsumedInWindow:     999_999, // 旧窗口积累的，应被忽略
	}
	if !CheckBalanceConsumeAllowed(user, 500_000) {
		t.Error("expired window with delta <= limit should be allowed (reset semantics)")
	}
	if CheckBalanceConsumeAllowed(user, 2_000_000) {
		t.Error("expired window but delta > limit should still be rejected")
	}
}

// ─── cache.go::AddUserToAuthCache / EvictUserToken ────────────────────────────

func TestAddUserToAuthCache_BasicAddAndOverwrite(t *testing.T) {
	origAuth := AuthCache
	authSnapshotMutex.Lock()
	AuthCache = map[string]*database.User{}
	authSnapshotMutex.Unlock()
	t.Cleanup(func() {
		authSnapshotMutex.Lock()
		AuthCache = origAuth
		authSnapshotMutex.Unlock()
	})

	u := &database.User{ID: 11, Token: "sk-add-cache", Status: 1}
	AddUserToAuthCache(u)
	got := LookupUserByToken("sk-add-cache")
	if got == nil || got.ID != 11 {
		t.Fatalf("AddUserToAuthCache failed; got=%+v", got)
	}

	// 重复 add 同 token：覆盖（quota / status 等会被新对象替换）
	u2 := &database.User{ID: 11, Token: "sk-add-cache", Status: 2, Quota: 999}
	AddUserToAuthCache(u2)
	got = LookupUserByToken("sk-add-cache")
	if got.Status != 2 || got.Quota != 999 {
		t.Errorf("overwrite didn't take effect: %+v", got)
	}
}

func TestAddUserToAuthCache_RejectsNilOrEmptyToken(t *testing.T) {
	origAuth := AuthCache
	authSnapshotMutex.Lock()
	AuthCache = map[string]*database.User{}
	authSnapshotMutex.Unlock()
	t.Cleanup(func() {
		authSnapshotMutex.Lock()
		AuthCache = origAuth
		authSnapshotMutex.Unlock()
	})

	AddUserToAuthCache(nil) // 不应 panic
	AddUserToAuthCache(&database.User{ID: 12, Token: ""})
	authSnapshotMutex.RLock()
	if len(AuthCache) != 0 {
		t.Errorf("nil / empty-token user should not be added; got %d entries", len(AuthCache))
	}
	authSnapshotMutex.RUnlock()
}

func TestEvictUserToken_RemovesEntry(t *testing.T) {
	origAuth := AuthCache
	authSnapshotMutex.Lock()
	AuthCache = map[string]*database.User{
		"sk-evict-me":   {ID: 21, Token: "sk-evict-me", Status: 1},
		"sk-keep-me":    {ID: 22, Token: "sk-keep-me", Status: 1},
	}
	authSnapshotMutex.Unlock()
	t.Cleanup(func() {
		authSnapshotMutex.Lock()
		AuthCache = origAuth
		authSnapshotMutex.Unlock()
	})

	EvictUserToken("sk-evict-me")
	if LookupUserByToken("sk-evict-me") != nil {
		t.Error("EvictUserToken did not remove entry")
	}
	if LookupUserByToken("sk-keep-me") == nil {
		t.Error("EvictUserToken evicted wrong entry")
	}

	// 重复 evict 不存在的 key 安全（no-op）
	EvictUserToken("sk-evict-me")
	EvictUserToken("")
}

// ─── channel_types.go::IsAllowedChannelType ───────────────────────────────────

func TestIsAllowedChannelType(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"openai", true},
		{"OpenAI", true}, // 大小写归一
		{"  openai  ", true},
		{"anthropic", true},
		{"gemini", true},
		{"google-cli", true},
		{"codex", true},
		{"cliproxy", true},
		{"", false},
		{"unknown", false},
		{"random-junk", false},
	}
	for _, c := range cases {
		got := IsAllowedChannelType(c.in)
		if got != c.want {
			t.Errorf("IsAllowedChannelType(%q)=%v want %v", c.in, got, c.want)
		}
	}
}

// ─── channel_circuit.go::markChannelModelUnhealthy ────────────────────────────

func TestMarkChannelModelUnhealthy_BlocksRoute(t *testing.T) {
	// markChannelModelUnhealthy 是 unexported helper（IsChannelModelUnhealthy 的 setter）
	// 由 stream.go 在 channel_misconfigured 路径触发。验证：mark 后 IsChannelModelUnhealthy
	// 返回 true，确保 stream.go 不再选 unhealthy 路由。
	markChannelModelUnhealthy(9999, "unhealthy-model-test")
	if !IsChannelModelUnhealthy(9999, "unhealthy-model-test") {
		t.Error("markChannelModelUnhealthy did not register; IsChannelModelUnhealthy returned false")
	}
	if IsChannelModelUnhealthy(9999, "different-model") {
		t.Error("unhealthy mark leaked to different model")
	}
	if IsChannelModelUnhealthy(8888, "unhealthy-model-test") {
		t.Error("unhealthy mark leaked to different channel")
	}
}

// 并发安全性 smoke test（race detector 会捕获问题）
func TestAuthCacheConcurrentReadWrite(t *testing.T) {
	origAuth := AuthCache
	authSnapshotMutex.Lock()
	AuthCache = map[string]*database.User{}
	authSnapshotMutex.Unlock()
	t.Cleanup(func() {
		authSnapshotMutex.Lock()
		AuthCache = origAuth
		authSnapshotMutex.Unlock()
	})

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func(idx int) {
			defer wg.Done()
			AddUserToAuthCache(&database.User{ID: uint(idx + 100), Token: tokenFromIdx(idx), Status: 1})
		}(i)
		go func(idx int) {
			defer wg.Done()
			_ = LookupUserByToken(tokenFromIdx(idx))
		}(i)
	}
	wg.Wait()
	// 无 panic / race 即视为通过；race detector 会失败若 mutex 用错
}

func tokenFromIdx(i int) string {
	return "sk-concurrent-" + string(rune('a'+i))
}
