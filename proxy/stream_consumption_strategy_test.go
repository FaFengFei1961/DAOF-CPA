package proxy

import (
	"bytes"
	"fmt"
	"io"
	"net/http/httptest"
	"testing"
	"time"

	"daof-cpa/database"

	"github.com/gofiber/fiber/v2"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupConsumptionStrategyDB(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:consumption_strategy_%d?mode=memory&cache=shared", time.Now().UnixNano())), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if sqlDB, dbErr := db.DB(); dbErr == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	if err := db.AutoMigrate(
		&database.User{}, &database.ApiLog{}, &database.BillingEntry{},
		&database.ApiLogRevenue{},
		&database.UserSubscription{}, &database.SubscriptionUsage{},
		&database.Channel{}, &database.ChannelModel{},
		&database.QuotaPlan{}, &database.Package{}, &database.PackagePlan{},
		&database.SysConfig{}, &database.NotificationPreference{}, &database.Notification{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	database.DB = db
	AuthCache = map[string]*database.User{}
	AuthTokenCache = map[string]*database.AccessToken{}
	RouteCache = map[string][]*database.ChannelModel{}
	ChannelMapCache = map[uint]*database.Channel{}
	FlushAllSubscriptionCache()

	oldConfig := replaceSysConfigForTest(map[string]string{
		"subscription_engine_fallback_to_quota": "true",
		BillingModelWeightsConfigKey:            `[{"pattern":"claude-opus-*","weight":3.5},{"pattern":"*","weight":1}]`,
	})
	t.Cleanup(func() {
		replaceSysConfigForTest(oldConfig)
		FlushAllSubscriptionCache()
	})
}

func seedConsumptionStrategyUser(t *testing.T, id uint, token string, quotaMicroUSD, balanceLimitMicroUSD int64) database.User {
	t.Helper()
	windowStart := time.Now()
	user := database.User{
		ID: id, Username: fmt.Sprintf("strategy-user-%d", id), Token: token,
		Quota:                       quotaMicroUSD,
		Status:                      1,
		Role:                        "user",
		BalanceConsumeEnabled:       true,
		BalanceConsumeLimitUSD:      balanceLimitMicroUSD,
		BalanceConsumeWindowSeconds: 3600,
		BalanceConsumeWindowStartAt: &windowStart,
	}
	if err := database.DB.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	AuthCache[token] = &user
	return user
}

func seedConsumptionStrategyRoute(t *testing.T, modelName string, promptTokens, completionTokens int) {
	t.Helper()
	backend := fakeChatBackend(t, promptTokens, completionTokens)
	t.Cleanup(backend.Close)
	ChannelMapCache[1] = &database.Channel{ID: 1, Type: ChannelTypeOpenAI, BaseURL: backend.URL, Key: "upstream-key"}
	RouteCache[modelName] = []*database.ChannelModel{{
		ChannelID:               1,
		Weight:                  1,
		InputPricePicoPerToken:  pricePicoForTest(1),
		OutputPricePicoPerToken: pricePicoForTest(1),
		ModerationLevel:         "off",
		ModerationFailMode:      "open",
	}}
}

func invokeConsumptionStrategyPayload(t *testing.T, token, payload string) (int, string) {
	t.Helper()
	app := fiber.New()
	app.Post("/v1/chat/completions", ChatCompletionProxyHandler)

	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	time.Sleep(50 * time.Millisecond)
	return resp.StatusCode, string(body)
}

func TestBalanceConsume_UsesRawCostNotCharged(t *testing.T) {
	setupConsumptionStrategyDB(t)
	const (
		modelName   = "claude-opus-balance-raw"
		rawCost     = int64(1 * database.MicroPerUSD)
		chargedCost = int64(3_500_000)
	)
	user := seedConsumptionStrategyUser(t, 201, "sk-balance-raw", 10*database.MicroPerUSD, 0)
	seedConsumptionStrategyRoute(t, modelName, 1_000_000, 0)

	resp := invokeChatCompletion(t, modelName, user.Token)
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var fresh database.User
	if err := database.DB.First(&fresh, user.ID).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	if fresh.Quota != 9*database.MicroPerUSD {
		t.Fatalf("quota=%d, want %d", fresh.Quota, 9*database.MicroPerUSD)
	}
	if fresh.BalanceConsumedInWindow != rawCost {
		t.Fatalf("balance window consumed=%d, want raw %d", fresh.BalanceConsumedInWindow, rawCost)
	}

	var entry database.BillingEntry
	if err := database.DB.Where("user_id = ? AND entry_type = ?", user.ID, database.BillingTypeApiConsumeBalance).First(&entry).Error; err != nil {
		t.Fatalf("api_consume_balance entry not found: %v", err)
	}
	if entry.AmountUSD != -rawCost || entry.BalanceAfterUSD != 9*database.MicroPerUSD {
		t.Fatalf("billing amount/balance=%d/%d, want %d/%d", entry.AmountUSD, entry.BalanceAfterUSD, -rawCost, 9*database.MicroPerUSD)
	}

	var apiLog database.ApiLog
	if err := database.DB.Where("user_id = ? AND model_name = ?", user.ID, modelName).First(&apiLog).Error; err != nil {
		t.Fatalf("api log not found: %v", err)
	}
	if apiLog.Cost != rawCost || apiLog.ChargedCost != chargedCost {
		t.Fatalf("api log raw/charged=%d/%d, want %d/%d", apiLog.Cost, apiLog.ChargedCost, rawCost, chargedCost)
	}
}

func TestSubscriptionConsume_StillUsesChargedCost(t *testing.T) {
	setupConsumptionStrategyDB(t)
	const (
		modelName   = "claude-opus-sub-charged"
		rawCost     = int64(1 * database.MicroPerUSD)
		chargedCost = int64(3_500_000)
		planID      = uint(502)
	)
	user := seedConsumptionStrategyUser(t, 202, "sk-sub-charged", 10*database.MicroPerUSD, 0)
	sub := seedSub(t, user.ID, makeSnapshot([]map[string]any{{
		"id": planID, "model_match": `["*opus*"]`, "limit_unit": "api_cost_usd", "limit_value": 10.0,
		"window_seconds": 0, "priority": 1,
	}}), 1)
	seedConsumptionStrategyRoute(t, modelName, 1_000_000, 0)

	resp := invokeChatCompletion(t, modelName, user.Token)
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var usage database.SubscriptionUsage
	if err := database.DB.Where("subscription_id = ? AND quota_plan_id = ?", sub.ID, planID).First(&usage).Error; err != nil {
		t.Fatalf("subscription usage not found: %v", err)
	}
	if usage.ConsumedValueMicroUSD != chargedCost {
		t.Fatalf("subscription consumed=%d, want charged %d", usage.ConsumedValueMicroUSD, chargedCost)
	}

	var fresh database.User
	if err := database.DB.First(&fresh, user.ID).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	if fresh.Quota != user.Quota {
		t.Fatalf("subscription hit should not deduct balance, quota=%d want %d", fresh.Quota, user.Quota)
	}

	var entry database.BillingEntry
	if err := database.DB.Where("user_id = ? AND entry_type = ?", user.ID, database.BillingTypeApiUsageSub).First(&entry).Error; err != nil {
		t.Fatalf("api_usage_sub entry not found: %v", err)
	}
	if entry.AmountUSD != 0 {
		t.Fatalf("api_usage_sub amount=%d, want 0", entry.AmountUSD)
	}

	var apiLog database.ApiLog
	if err := database.DB.Where("user_id = ? AND model_name = ?", user.ID, modelName).First(&apiLog).Error; err != nil {
		t.Fatalf("api log not found: %v", err)
	}
	if apiLog.Cost != rawCost || apiLog.ChargedCost != chargedCost {
		t.Fatalf("api log raw/charged=%d/%d, want %d/%d", apiLog.Cost, apiLog.ChargedCost, rawCost, chargedCost)
	}
}

func TestBalanceWindow_TracksRawCost(t *testing.T) {
	setupConsumptionStrategyDB(t)
	const (
		rawCost     = int64(1 * database.MicroPerUSD)
		chargedCost = int64(3_500_000)
	)
	user := seedConsumptionStrategyUser(t, 203, "sk-window-raw", 10*database.MicroPerUSD, 10*database.MicroPerUSD)

	if err := database.DB.Transaction(func(tx *gorm.DB) error {
		if !TryConsumeBalanceTx(tx, user.ID, rawCost, true) {
			return fmt.Errorf("TryConsumeBalanceTx returned false")
		}
		return nil
	}); err != nil {
		t.Fatalf("consume window: %v", err)
	}

	var fresh database.User
	if err := database.DB.First(&fresh, user.ID).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	if fresh.BalanceConsumedInWindow != rawCost || fresh.BalanceConsumedInWindow == chargedCost {
		t.Fatalf("balance window consumed=%d, want raw %d and not charged %d", fresh.BalanceConsumedInWindow, rawCost, chargedCost)
	}
}

func TestBalancePrecheck_UsesRawCost(t *testing.T) {
	setupConsumptionStrategyDB(t)
	const modelName = "claude-opus-precheck-raw"
	user := seedConsumptionStrategyUser(t, 204, "sk-precheck-raw", database.MicroPerUSD, 200)
	seedConsumptionStrategyRoute(t, modelName, 1, 1)

	payload := `{"model":"` + modelName + `","messages":[{"role":"user","content":"h"}],"max_tokens":1}`
	status, body := invokeConsumptionStrategyPayload(t, user.Token, payload)
	if status != 200 {
		t.Fatalf("precheck should allow raw-cost estimate under limit; status=%d body=%s", status, body)
	}
}

func TestPendingReconcile_EstimatedCostUSD_IsRaw(t *testing.T) {
	setupConsumptionStrategyDB(t)
	const (
		modelName   = "claude-opus-pending-raw"
		rawCost     = int64(1 * database.MicroPerUSD)
		chargedCost = int64(3_500_000)
	)
	user := seedConsumptionStrategyUser(t, 205, "sk-pending-raw", 1, 0)
	seedConsumptionStrategyRoute(t, modelName, 1_000_000, 0)

	resp := invokeChatCompletion(t, modelName, user.Token)
	if resp.StatusCode != 200 {
		t.Fatalf("expected upstream success to remain 200, got %d", resp.StatusCode)
	}

	var entry database.BillingEntry
	if err := database.DB.Where("user_id = ? AND entry_type = ? AND billing_state = ?",
		user.ID, database.BillingTypeApiUsagePendingReconcile, database.BillingStatePendingReconcile).First(&entry).Error; err != nil {
		t.Fatalf("pending reconcile entry not found: %v", err)
	}
	if entry.AmountUSD != 0 || entry.EstimatedCostUSD != rawCost {
		t.Fatalf("pending amount/estimated=%d/%d, want 0/raw %d", entry.AmountUSD, entry.EstimatedCostUSD, rawCost)
	}

	var apiLog database.ApiLog
	if err := database.DB.Where("user_id = ? AND model_name = ?", user.ID, modelName).First(&apiLog).Error; err != nil {
		t.Fatalf("api log not found: %v", err)
	}
	if apiLog.Cost != rawCost || apiLog.ChargedCost != chargedCost {
		t.Fatalf("api log raw/charged=%d/%d, want %d/%d", apiLog.Cost, apiLog.ChargedCost, rawCost, chargedCost)
	}

	var fresh database.User
	if err := database.DB.First(&fresh, user.ID).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	if fresh.Quota != user.Quota {
		t.Fatalf("pending reconcile should not make balance negative, quota=%d want %d", fresh.Quota, user.Quota)
	}
}
