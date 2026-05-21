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
)

// TestSSEBillingWritesApiLogUsageLines 固化 P8.4 起 SSE 路径也写 ApiLogUsageLine
// 的行为。原 deductQuota 仅写 ApiLog，不写 usage line；P8 统一后 SSE/WS 两端
// 行为对齐。
func TestSSEBillingWritesApiLogUsageLines(t *testing.T) {
	setupStreamConservationDB(t)
	if err := database.DB.AutoMigrate(&database.ApiLogUsageLine{}); err != nil {
		t.Fatalf("migrate usage line: %v", err)
	}

	user := database.User{
		ID: 1, Username: "bill-usageline", Token: "sk-usageline",
		Quota:                 1_000_000,
		PaidQuota:             1_000_000,
		Status:                1,
		BalanceConsumeEnabled: true,
		Role:                  "user",
	}
	if err := database.DB.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	AuthCache[user.Token] = &user

	backend := fakeChatBackend(t, 10, 20)
	defer backend.Close()
	ChannelMapCache[1] = &database.Channel{ID: 1, Type: "openai", BaseURL: backend.URL, Key: "k"}
	RouteCache["gpt-usageline-test"] = []*database.ChannelModel{
		{ChannelID: 1, Weight: 1,
			InputPricePicoPerToken:  pricePicoForTest(2),
			OutputPricePicoPerToken: pricePicoForTest(5),
		},
	}

	resp := invokeChatCompletion(t, "gpt-usageline-test", user.Token)
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}

	var apiLog database.ApiLog
	if err := database.DB.Where("user_id = ?", user.ID).First(&apiLog).Error; err != nil {
		t.Fatalf("load api log: %v", err)
	}
	var lines []database.ApiLogUsageLine
	if err := database.DB.Where("api_log_id = ?", apiLog.ID).Find(&lines).Error; err != nil {
		t.Fatalf("load usage lines: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("usage lines len=%d want 2 (input + output); lines=%+v", len(lines), lines)
	}
	var sawInput, sawOutput bool
	for _, ln := range lines {
		if ln.Unit != "token" {
			t.Errorf("unit=%q want token", ln.Unit)
		}
		if ln.CostSource != "upstream_usage" {
			t.Errorf("cost_source=%q want upstream_usage", ln.CostSource)
		}
		switch ln.Direction {
		case "input":
			sawInput = true
			if ln.Quantity != 10 {
				t.Errorf("input quantity=%d want 10", ln.Quantity)
			}
		case "output":
			sawOutput = true
			if ln.Quantity != 20 {
				t.Errorf("output quantity=%d want 20", ln.Quantity)
			}
		}
	}
	if !sawInput || !sawOutput {
		t.Errorf("missing direction; sawInput=%v sawOutput=%v", sawInput, sawOutput)
	}
}

// TestWriteTextUsageLines_SplitsByTokenCategory 锁定 2026-05-21 重构：
// 一次 prompt 同时带 standard / cache_read / cache_write_5m / cache_write_1h
// 时，writeTextUsageLines 必须写 5 条 line（4 input 类 + 1 output），
// 每条 quantity 与 amount_micro_usd 都跟主路径 checkedCostMicroUSD 的口径一致，
// 不再像旧实现那样把整段 promptTokens 全按 inputPricePico 算成一个"伪 input"。
//
// 数学验证：5 个 line 的 AmountMicroUSD 加总应严格 == 用 checkedCostMicroUSD
// 算出的 ApiLog.Cost（这才是 ApiLog.Cost vs usage_lines 总额闭环的保证）。
func TestWriteTextUsageLines_SplitsByTokenCategory(t *testing.T) {
	setupStreamConservationDB(t)
	if err := database.DB.AutoMigrate(&database.ApiLogUsageLine{}); err != nil {
		t.Fatalf("migrate usage line: %v", err)
	}

	// 一条已落地的 ApiLog 占位（usage_lines 通过 ApiLogID 关联）
	apiLog := database.ApiLog{UserID: 1, ModelName: "claude-opus-4-7", Status: 200}
	if err := database.DB.Create(&apiLog).Error; err != nil {
		t.Fatalf("create api log: %v", err)
	}

	// 价格矩阵：input=$5/MTok, output=$25/MTok, cache_read=$0.5/MTok（×0.1），
	// cache_write_5m=$6.25/MTok（×1.25），cache_write_1h=$10/MTok（×2）。
	// 这是 Anthropic Opus 公开口径，便于读者验证。
	in := TextUsageLineInput{
		APILogID:                   apiLog.ID,
		ModelName:                  "claude-opus-4-7",
		RequestPath:                "/v1/messages",
		StandardInputTokens:        6,        // 6 × 5 / 1e6 = 30 micro_usd
		CachedTokens:               1000,     // 1000 × 0.5 / 1e6 = 500 micro_usd
		CacheWrite5mTokens:         100_000,  // 100k × 6.25 / 1e6 = 625_000 micro_usd
		CacheWrite1hTokens:         2_000,    // 2k × 10 / 1e6 = 20_000 micro_usd
		CompletionTokens:           73,       // 73 × 25 / 1e6 = 1_825 micro_usd
		InputPricePico:             pricePicoForTest(5),
		CachedInputPricePico:       pricePicoForTest(0.5),
		CacheWriteInputPricePico:   pricePicoForTest(6.25),
		CacheWrite1hInputPricePico: pricePicoForTest(10),
		OutputPricePico:            pricePicoForTest(25),
	}
	writeTextUsageLines(in)

	var lines []database.ApiLogUsageLine
	if err := database.DB.Where("api_log_id = ?", apiLog.ID).Order("id asc").Find(&lines).Error; err != nil {
		t.Fatalf("load usage lines: %v", err)
	}
	if len(lines) != 5 {
		t.Fatalf("usage lines len=%d want 5 (standard input + cache_read + cache_write_5m + cache_write_1h + output); lines=%+v", len(lines), lines)
	}

	want := map[string]struct {
		quantity int64
		amount   int64
	}{
		"input":          {6, 30},
		"cache_read":     {1000, 500},
		"cache_write_5m": {100_000, 625_000},
		"cache_write_1h": {2_000, 20_000},
		"output":         {73, 1_825},
	}
	var totalAmount int64
	for _, ln := range lines {
		exp, ok := want[ln.Direction]
		if !ok {
			t.Errorf("unexpected direction %q in usage lines", ln.Direction)
			continue
		}
		if ln.Quantity != exp.quantity {
			t.Errorf("direction=%s quantity=%d want %d", ln.Direction, ln.Quantity, exp.quantity)
		}
		if ln.AmountMicroUSD != exp.amount {
			t.Errorf("direction=%s amount_micro_usd=%d want %d", ln.Direction, ln.AmountMicroUSD, exp.amount)
		}
		if ln.Unit != "token" {
			t.Errorf("direction=%s unit=%q want token", ln.Direction, ln.Unit)
		}
		if ln.CostSource != "upstream_usage" {
			t.Errorf("direction=%s cost_source=%q want upstream_usage", ln.Direction, ln.CostSource)
		}
		totalAmount += ln.AmountMicroUSD
	}
	// 总额应等于 ApiLog.Cost 在同样口径下的算法（checkedCostMicroUSD ceil-div 各段加和）
	wantTotal := int64(30 + 500 + 625_000 + 20_000 + 1_825)
	if totalAmount != wantTotal {
		t.Errorf("sum of usage_lines.amount_micro_usd = %d want %d (== ApiLog.Cost)", totalAmount, wantTotal)
	}
}

// TestWriteTextUsageLines_SkipsZeroQuantityCategories 确保 quantity=0 的档不会
// 凭空生成一条 0-quantity / 0-amount 噪音 line，让 admin 详情页保持清爽。
func TestWriteTextUsageLines_SkipsZeroQuantityCategories(t *testing.T) {
	setupStreamConservationDB(t)
	if err := database.DB.AutoMigrate(&database.ApiLogUsageLine{}); err != nil {
		t.Fatalf("migrate usage line: %v", err)
	}
	apiLog := database.ApiLog{UserID: 1, ModelName: "gpt-5.5", Status: 200}
	if err := database.DB.Create(&apiLog).Error; err != nil {
		t.Fatalf("create api log: %v", err)
	}

	// 无任何 cache 命中也没 cache write，只走最朴素的 input/output —— 应只写 2 条。
	in := TextUsageLineInput{
		APILogID:            apiLog.ID,
		ModelName:           "gpt-5.5",
		RequestPath:         "/v1/chat/completions",
		StandardInputTokens: 100,
		CompletionTokens:    50,
		InputPricePico:      pricePicoForTest(1),
		OutputPricePico:     pricePicoForTest(2),
	}
	writeTextUsageLines(in)

	var lines []database.ApiLogUsageLine
	if err := database.DB.Where("api_log_id = ?", apiLog.ID).Find(&lines).Error; err != nil {
		t.Fatalf("load usage lines: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("usage lines len=%d want 2 (no cache → only input + output); lines=%+v", len(lines), lines)
	}
	for _, ln := range lines {
		if ln.Direction != "input" && ln.Direction != "output" {
			t.Errorf("unexpected direction %q in no-cache scenario", ln.Direction)
		}
		if ln.Quantity <= 0 {
			t.Errorf("direction=%s should not write quantity=%d line", ln.Direction, ln.Quantity)
		}
	}
}

// TestSSEBillingSubTokenUsedQuotaAccumulates 固化子 token UsedQuota 累加：
//  1. 走 balance fallback（无订阅）的请求会按 rawCost 累加 sub-token.UsedQuota
//  2. AuthTokenCache 同步更新（clone-on-write）
//  3. CAS 成功才累加；CAS 失败（余额不足）不应累加
func TestSSEBillingSubTokenUsedQuotaAccumulates(t *testing.T) {
	setupStreamConservationDB(t)
	if err := database.DB.AutoMigrate(&database.AccessToken{}); err != nil {
		t.Fatalf("migrate access token: %v", err)
	}

	user := database.User{
		ID: 1, Username: "subtoken-bill", Token: "sk-main",
		Quota: 1_000_000, PaidQuota: 1_000_000, Status: 1,
		BalanceConsumeEnabled: true, Role: "user",
	}
	if err := database.DB.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	subToken := database.AccessToken{
		ID: 7, UserID: user.ID, Status: 1,
		Name:       "test-sub",
		Key:        "sk-sub-7",
		QuotaLimit: 10_000_000,
		UsedQuota:  0,
	}
	if err := database.DB.Create(&subToken).Error; err != nil {
		t.Fatalf("seed sub token: %v", err)
	}
	AuthCache[subToken.Key] = &user
	AuthTokenCache[subToken.Key] = &subToken

	backend := fakeChatBackend(t, 5, 15)
	defer backend.Close()
	ChannelMapCache[1] = &database.Channel{ID: 1, Type: "openai", BaseURL: backend.URL, Key: "k"}
	RouteCache["gpt-subtoken-test"] = []*database.ChannelModel{
		{ChannelID: 1, Weight: 1,
			InputPricePicoPerToken:  pricePicoForTest(1),
			OutputPricePicoPerToken: pricePicoForTest(1),
		},
	}

	resp := invokeChatCompletion(t, "gpt-subtoken-test", subToken.Key)
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}

	// cost = 5*1 + 15*1 = 20 micro_usd
	wantUsed := int64(20)

	var fresh database.AccessToken
	if err := database.DB.First(&fresh, subToken.ID).Error; err != nil {
		t.Fatalf("load sub token: %v", err)
	}
	if fresh.UsedQuota != wantUsed {
		t.Errorf("DB UsedQuota=%d want %d", fresh.UsedQuota, wantUsed)
	}

	authSnapshotMutex.RLock()
	cached, ok := AuthTokenCache[subToken.Key]
	authSnapshotMutex.RUnlock()
	if !ok {
		t.Fatalf("sub token missing from AuthTokenCache")
	}
	if cached.UsedQuota != wantUsed {
		t.Errorf("AuthTokenCache UsedQuota=%d want %d", cached.UsedQuota, wantUsed)
	}
}

// TestSSEBillingFailedRequestNoDeduct 固化 failedRequest=true（status>=400）
// 不走 commit pipeline：cost=0、Quota 不变、无 BillingEntry。
// 这条路径 P8.4 抽取后必须保留——commitTextTurn 内 failedRequest 直接 0 cost
// 跳过订阅/余额 commit + 子 token 累加。
func TestSSEBillingFailedRequestNoDeduct(t *testing.T) {
	setupStreamConservationDB(t)

	user := database.User{
		ID: 1, Username: "fail-no-deduct", Token: "sk-fail",
		Quota: 500_000, PaidQuota: 500_000, Status: 1,
		BalanceConsumeEnabled: true, Role: "user",
	}
	if err := database.DB.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	AuthCache[user.Token] = &user

	// 上游返 500 → handler 不走 deductQuota，直接走错误路径
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(`{"error":"upstream broken"}`))
	}))
	defer backend.Close()

	ChannelMapCache[1] = &database.Channel{ID: 1, Type: "openai", BaseURL: backend.URL, Key: "k"}
	RouteCache["gpt-fail-test"] = []*database.ChannelModel{
		{ChannelID: 1, Weight: 1,
			InputPricePicoPerToken:  pricePicoForTest(1),
			OutputPricePicoPerToken: pricePicoForTest(1),
		},
	}

	app := fiber.New()
	app.Post("/v1/chat/completions", ChatCompletionProxyHandler)
	payload := `{"model":"gpt-fail-test","messages":[{"role":"user","content":"Hi"}],"stream":false}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+user.Token)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	time.Sleep(50 * time.Millisecond)

	// 余额不应变化
	var fresh database.User
	if err := database.DB.First(&fresh, user.ID).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	if fresh.Quota != 500_000 {
		t.Errorf("quota=%d want 500_000 (upstream 5xx must not deduct)", fresh.Quota)
	}

	// 无 BillingEntry（500 后 handler 直接返回 5xx，连 ApiLog 也是错误路径）
	var entries []database.BillingEntry
	_ = database.DB.Where("user_id = ?", user.ID).Find(&entries).Error
	for _, e := range entries {
		if e.EntryType == database.BillingTypeApiUsageSub || e.EntryType == database.BillingTypeApiConsumeBalance {
			t.Errorf("unexpected billing entry on 5xx: %+v", e)
		}
	}
}
