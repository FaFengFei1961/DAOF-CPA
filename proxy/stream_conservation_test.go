// Package proxy / stream_conservation_test.go
//
// 财务守恒测试覆盖到生产最高频路径：proxy/stream.go 的 cost 扣费链路。
//
// 守恒约束（fix MAJOR M22-A1 Phase 4）：
//
//	对每次 API 调用 [t0, t1]，对调用用户：
//	  ΔQuota(user) == Σ AmountUSD(billing entries[t0..t1])
//
// 路径分类（每个都必须满足上述约束）：
//
//	路径 A（命中订阅）：
//	  ΔQuota = 0
//	  billing 写 api_usage_sub, AmountUSD = 0（仅审计）
//
//	路径 B（fallback 余额扣费）：
//	  ΔQuota = -cost
//	  billing 写 api_consume_balance, AmountUSD = -cost
//
//	路径 C（请求失败 / 上游异常）：
//	  ΔQuota = 0
//	  无 billing 写入
//
// 失败任意一条都会让对账查询（按 billing 重建用户余额）漂移。
package proxy

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"daof-cpa/database"

	"github.com/gofiber/fiber/v2"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupStreamConservationDB 用 in-memory + cache=shared 让 stream.go 异步 goroutine
// 能看到主测试 commit 的数据；migrate 完整表集（含 BillingEntry/SubscriptionUsage）
func setupStreamConservationDB(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if sqlDB, dbErr := db.DB(); dbErr == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	if err := db.AutoMigrate(
		&database.User{}, &database.ApiLog{}, &database.BillingEntry{},
		&database.UserSubscription{}, &database.SubscriptionUsage{},
		&database.Channel{}, &database.ChannelModel{},
		&database.QuotaPlan{}, &database.Package{}, &database.PackagePlan{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	database.DB = db
	// 清空可能由前面 stream test 残留的数据
	db.Exec("DELETE FROM users")
	db.Exec("DELETE FROM billing_entries")
	db.Exec("DELETE FROM api_logs")
	db.Exec("DELETE FROM user_subscriptions")
	db.Exec("DELETE FROM subscription_usages")

	// 重置 proxy 缓存
	AuthCache = map[string]*database.User{}
	AuthTokenCache = map[string]*database.AccessToken{}
	RouteCache = map[string][]*database.ChannelModel{}
	ChannelMapCache = map[uint]*database.Channel{}
	SysConfigMutex.Lock()
	SysConfigCache = map[string]string{}
	SysConfigMutex.Unlock()
}

// fakeChatBackend 返回一个固定 token 数 + 内容的 OpenAI-style 后端
func fakeChatBackend(t *testing.T, prompt, completion int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		// 必须返回 usage.prompt_tokens / completion_tokens，proxy 用这俩算 cost
		body := `{"choices":[{"message":{"content":"hi"}}],"usage":{"prompt_tokens":` +
			itoaInt(prompt) + `,"completion_tokens":` + itoaInt(completion) + `}}`
		w.Write([]byte(body))
	}))
}

func itoaInt(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	buf := make([]byte, 0, 12)
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	if neg {
		buf = append([]byte{'-'}, buf...)
	}
	return string(buf)
}

// invokeChatCompletion 跑一个完整的 ChatCompletion 请求并等待 stream.go 异步 commit
func invokeChatCompletion(t *testing.T, model, token string) *http.Response {
	t.Helper()
	app := fiber.New()
	app.Post("/v1/chat/completions", ChatCompletionProxyHandler)

	payload := `{"model":"` + model + `","messages":[{"role":"user","content":"Hi"}],"stream":false}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	// drain body so any chunked / async commit completes
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	// 给 deductQuotaAtomic 异步事务一个 settle 窗口（goroutine 写入 commit）
	time.Sleep(50 * time.Millisecond)
	return resp
}

// assertStreamConservation 通用守恒断言：对单个 user，跑 API 后 ΔQuota == Σbilling
func assertStreamConservation(t *testing.T, userID uint, quotaBeforeMicro int64, sinceTime time.Time, label string) (deltaMicro int64, billingMicro int64) {
	t.Helper()
	var fresh database.User
	if err := database.DB.Select("id, quota").First(&fresh, userID).Error; err != nil {
		t.Fatalf("[%s] re-read user: %v", label, err)
	}
	deltaMicro = fresh.Quota - quotaBeforeMicro

	if err := database.DB.Model(&database.BillingEntry{}).
		Where("user_id = ? AND occurred_at >= ?", userID, sinceTime).
		Select("COALESCE(SUM(amount_usd), 0)").
		Scan(&billingMicro).Error; err != nil {
		t.Fatalf("[%s] sum billing: %v", label, err)
	}
	if deltaMicro != billingMicro {
		t.Errorf("[%s] CONSERVATION VIOLATED: ΔQuota=%d but Σbilling=%d (diff=%d micro_usd)",
			label, deltaMicro, billingMicro, deltaMicro-billingMicro)
	}
	return
}

// TestStreamConservation_BalanceFallback 路径 B：无订阅 → fallback 余额扣费。
// 价格 $1/M tokens，10 input + 20 output → cost = 30 micro_usd ($0.00003)。
// 要求：ΔQuota == -cost，且 billing(api_consume_balance).AmountUSD == -cost。
func TestStreamConservation_BalanceFallback(t *testing.T) {
	setupStreamConservationDB(t)

	referredAt := time.Now().Add(-time.Hour)
	referrer := database.User{
		ID: 99, Username: "balance-referrer", Token: "sk-balance-referrer",
		Quota:  0,
		Status: 1,
		Role:   "user",
	}
	if err := database.DB.Create(&referrer).Error; err != nil {
		t.Fatalf("create referrer: %v", err)
	}

	// 用户启用余额消费 + 余额 $1（足够付 cost）
	user := database.User{
		ID: 1, Username: "bal-fallback-tester", Token: "sk-bal-test",
		Quota:                 1 * database.MicroPerUSD,
		PaidQuota:             1 * database.MicroPerUSD,
		Status:                1,
		ReferredByUserID:      referrer.ID,
		ReferredAt:            &referredAt,
		BalanceConsumeEnabled: true,
		Role:                  "user",
	}
	database.DB.Create(&user)
	AuthCache[user.Token] = &user
	SysConfigMutex.Lock()
	SysConfigCache[database.ReferralPaidSpendRewardBPSConfigKey] = "1000" // 10%
	SysConfigCache[database.ReferralPaidSpendRewardWindowSecondsConfigKey] = "2592000"
	SysConfigMutex.Unlock()

	backend := fakeChatBackend(t, 10, 20)
	defer backend.Close()
	ChannelMapCache[1] = &database.Channel{ID: 1, Type: "openai", BaseURL: backend.URL, Key: "sk-A"}
	RouteCache["gpt-conserve-bal"] = []*database.ChannelModel{
		{ChannelID: 1, Weight: 1, InputPricePicoPerToken: pricePicoForTest(1), OutputPricePicoPerToken: pricePicoForTest(1)},
	}

	beforeMicro := user.Quota
	startTime := time.Now().Add(-10 * time.Millisecond)

	resp := invokeChatCompletion(t, "gpt-conserve-bal", user.Token)
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// 守恒断言
	delta, billing := assertStreamConservation(t, user.ID, beforeMicro, startTime, "balance-fallback")

	// cost = 10*1 + 20*1 = 30 micro_usd（$0.00003）
	wantDelta := int64(-30)
	if delta != wantDelta {
		t.Errorf("ΔQuota=%d, want %d", delta, wantDelta)
	}
	if billing != wantDelta {
		t.Errorf("billing total=%d, want %d", billing, wantDelta)
	}

	// 验证写入的是 api_consume_balance 类型
	var entry database.BillingEntry
	if err := database.DB.Where("user_id = ? AND entry_type = ?",
		user.ID, database.BillingTypeApiConsumeBalance).First(&entry).Error; err != nil {
		t.Fatalf("api_consume_balance entry not found: %v", err)
	}
	if entry.AmountUSD != -30 {
		t.Errorf("entry.AmountUSD=%d, want -30", entry.AmountUSD)
	}
	var freshUser, freshReferrer database.User
	if err := database.DB.First(&freshUser, user.ID).Error; err != nil {
		t.Fatalf("load fresh user: %v", err)
	}
	if freshUser.PaidQuota != database.MicroPerUSD-30 {
		t.Errorf("paid_quota=%d, want %d", freshUser.PaidQuota, database.MicroPerUSD-30)
	}
	if err := database.DB.First(&freshReferrer, referrer.ID).Error; err != nil {
		t.Fatalf("load fresh referrer: %v", err)
	}
	if freshReferrer.Quota != 3 {
		t.Errorf("referrer quota=%d, want 3", freshReferrer.Quota)
	}
	var reward database.BillingEntry
	if err := database.DB.Where("user_id = ? AND entry_type = ?", referrer.ID, database.BillingTypeBonusCredit).First(&reward).Error; err != nil {
		t.Fatalf("referral spend reward billing missing: %v", err)
	}
	if reward.AmountUSD != 3 || reward.RelatedType != "api_log" || reward.RelatedID == 0 {
		t.Errorf("unexpected reward billing: %+v", reward)
	}
}

// TestStreamConservation_FailedUpstreamNoDeduct 路径 C：上游全部失败 → ΔQuota=0、无 billing。
//
// 因为没有成功的扣费链路，billing 不应有任何条目，user.Quota 不应变化。
func TestStreamConservation_FailedUpstreamNoDeduct(t *testing.T) {
	setupStreamConservationDB(t)

	user := database.User{
		ID: 2, Username: "fail-tester", Token: "sk-fail-test",
		Quota:                 5 * database.MicroPerUSD,
		Status:                1,
		BalanceConsumeEnabled: true,
		Role:                  "user",
	}
	database.DB.Create(&user)
	AuthCache[user.Token] = &user

	// 上游永远返回 500 → proxy 应耗尽所有重试后给 502
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(`{"error":"upstream broke"}`))
	}))
	defer backend.Close()
	ChannelMapCache[2] = &database.Channel{ID: 2, Type: "openai", BaseURL: backend.URL, Key: "sk-B"}
	RouteCache["gpt-conserve-fail"] = []*database.ChannelModel{
		{ChannelID: 2, Weight: 1, InputPricePicoPerToken: pricePicoForTest(1), OutputPricePicoPerToken: pricePicoForTest(1)},
	}

	beforeMicro := user.Quota
	startTime := time.Now().Add(-10 * time.Millisecond)

	resp := invokeChatCompletion(t, "gpt-conserve-fail", user.Token)
	if resp.StatusCode == 200 {
		t.Fatalf("expected non-200 (upstream all failed), got %d", resp.StatusCode)
	}

	// 关键：ΔQuota = 0 + 没有 billing 写入
	delta, billing := assertStreamConservation(t, user.ID, beforeMicro, startTime, "upstream-fail")
	if delta != 0 {
		t.Errorf("ΔQuota=%d, want 0 (upstream failed should NOT deduct)", delta)
	}
	if billing != 0 {
		t.Errorf("billing total=%d, want 0", billing)
	}

	// 防御：billing 表对此 user 不应有任何条目
	var count int64
	database.DB.Model(&database.BillingEntry{}).Where("user_id = ?", user.ID).Count(&count)
	if count != 0 {
		t.Errorf("billing entries=%d, want 0 (failed request creates no billing)", count)
	}
}

// TestStreamConservation_BalanceConsumeDisabledNoFallback 验证余额消费禁用时，
// 无订阅请求必须被拒绝，不能绕过用户开关直接扣 quota。
func TestStreamConservation_BalanceConsumeDisabledNoFallback(t *testing.T) {
	setupStreamConservationDB(t)

	user := database.User{
		ID: 3, Username: "no-fallback-tester", Token: "sk-no-fallback",
		Quota:                 100 * database.MicroPerUSD, // 余额充足
		Status:                1,
		BalanceConsumeEnabled: false, // 但禁用余额消费
		Role:                  "user",
	}
	database.DB.Create(&user)
	AuthCache[user.Token] = &user

	backend := fakeChatBackend(t, 10, 20)
	defer backend.Close()
	ChannelMapCache[3] = &database.Channel{ID: 3, Type: "openai", BaseURL: backend.URL, Key: "sk-C"}
	RouteCache["gpt-conserve-disabled"] = []*database.ChannelModel{
		{ChannelID: 3, Weight: 1, InputPricePicoPerToken: pricePicoForTest(1), OutputPricePicoPerToken: pricePicoForTest(1)},
	}

	beforeMicro := user.Quota
	startTime := time.Now().Add(-10 * time.Millisecond)

	resp := invokeChatCompletion(t, "gpt-conserve-disabled", user.Token)

	if resp.StatusCode != 402 {
		t.Fatalf("balance disabled should return 402, got %d", resp.StatusCode)
	}

	delta, billing := assertStreamConservation(t, user.ID, beforeMicro, startTime, "balance-disabled-402")
	if delta != 0 {
		t.Errorf("402 should not change quota, got delta=%d", delta)
	}
	if billing != 0 {
		t.Errorf("402 should not write billing, got %d", billing)
	}
}
