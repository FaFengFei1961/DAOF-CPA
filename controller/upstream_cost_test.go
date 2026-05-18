package controller

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"daof-cpa/database"
	"daof-cpa/proxy"

	"github.com/gofiber/fiber/v2"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupUpstreamCostTestDB(t *testing.T) *fiber.App {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=private"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&database.ApiLog{}, &database.ApiLogAttribution{}, &database.ApiLogCostEstimate{}, &database.ApiLogRevenue{}, &database.UpstreamAccountCost{}, &database.CPACredential{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	database.DB = db
	app := fiber.New()
	app.Get("/api/admin/upstream-account-cost-presets", ListUpstreamAccountCostPresets)
	app.Get("/api/admin/upstream-accounts/candidates", ListUpstreamAccountCandidates)
	app.Post("/api/admin/upstream-accounts", CreateUpstreamAccountCost)
	app.Post("/api/admin/upstream-accounts/bulk", BulkUpsertUpstreamAccountCosts)
	app.Put("/api/admin/upstream-accounts/:id", UpdateUpstreamAccountCost)
	app.Get("/api/admin/upstream-margin", GetUpstreamMarginReport)
	return app
}

func TestListUpstreamAccountCostPresetsFromSysConfig(t *testing.T) {
	app := setupUpstreamCostTestDB(t)
	old := replaceSysConfigCacheForUpstreamCostTest(map[string]string{
		"upstream_account_cost_presets_json": `[{"id":"claude-max-5x","label":"Claude Max 5x","provider":" Anthropic ","plan_name":"Claude Max 5x","monthly_cost_usd":100,"estimated_monthly_capacity_usd":0,"notes":"capacity is measured locally"}]`,
	})
	defer replaceSysConfigCacheForUpstreamCostTest(old)

	resp, err := app.Test(httptest.NewRequest("GET", "/api/admin/upstream-account-cost-presets", nil))
	if err != nil {
		t.Fatalf("list presets: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("preset status=%d", resp.StatusCode)
	}
	var decoded struct {
		Success bool `json:"success"`
		Data    []struct {
			ID                          string  `json:"id"`
			Label                       string  `json:"label"`
			Provider                    string  `json:"provider"`
			PlanName                    string  `json:"plan_name"`
			MonthlyCostUSD              float64 `json:"monthly_cost_usd"`
			EstimatedMonthlyCapacityUSD float64 `json:"estimated_monthly_capacity_usd"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode presets: %v", err)
	}
	if !decoded.Success || len(decoded.Data) != 1 {
		t.Fatalf("unexpected presets response: %+v", decoded)
	}
	preset := decoded.Data[0]
	if preset.ID != "claude-max-5x" || preset.Provider != "anthropic" || preset.MonthlyCostUSD != 100 || preset.EstimatedMonthlyCapacityUSD != 0 {
		t.Fatalf("bad preset normalization: %+v", preset)
	}
}

func TestUpstreamCost_WriteMonthlyCostUSD(t *testing.T) {
	app := setupUpstreamCostTestDB(t)
	payload := map[string]any{
		"provider":                       "codex",
		"auth_index":                     "acct-usd",
		"auth_type":                      "oauth",
		"label":                          "Codex USD",
		"plan_name":                      "ChatGPT Plus",
		"monthly_cost_usd":               20.0,
		"estimated_monthly_capacity_usd": 200.0,
		"active":                         true,
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest("POST", "/api/admin/upstream-accounts", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("create account status=%d", resp.StatusCode)
	}

	var row database.UpstreamAccountCost
	if err := database.DB.Where("provider = ? AND auth_index = ?", "codex", "acct-usd").First(&row).Error; err != nil {
		t.Fatalf("read account cost: %v", err)
	}
	if row.MonthlyCostUSD != 20*database.MicroPerUSD {
		t.Fatalf("monthly cost micro=%d want %d", row.MonthlyCostUSD, 20*database.MicroPerUSD)
	}
	if row.EstimatedMonthlyCapacityUSD != 200*database.MicroPerUSD {
		t.Fatalf("capacity micro=%d want %d", row.EstimatedMonthlyCapacityUSD, 200*database.MicroPerUSD)
	}
}

func TestListUpstreamAccountCandidatesIncludesNoRequestCredentials(t *testing.T) {
	app := setupUpstreamCostTestDB(t)
	now := time.Now()
	creds := []database.CPACredential{
		{
			AuthID:           "auth-no-requests",
			FileName:         "claude-a.json",
			Provider:         "claude",
			Email:            "claude-a@example.com",
			Status:           "active",
			LastSeenAt:       now,
			LastDownloadedAt: now,
		},
		{
			AuthID:           "auth-configured",
			FileName:         "codex-pro.json",
			Provider:         "codex",
			Email:            "codex@example.com",
			Status:           "active",
			LastSeenAt:       now,
			LastDownloadedAt: now,
		},
	}
	if err := database.DB.Create(&creds).Error; err != nil {
		t.Fatalf("seed cpa credentials: %v", err)
	}
	if err := database.DB.Create(&database.UpstreamAccountCost{
		Provider:                    "codex",
		AuthIndex:                   "auth-configured",
		Label:                       "Codex Pro",
		PlanName:                    "ChatGPT Pro",
		MonthlyCostUSD:              200 * database.MicroPerUSD,
		EstimatedMonthlyCapacityUSD: 1000 * database.MicroPerUSD,
		Active:                      true,
	}).Error; err != nil {
		t.Fatalf("seed account cost: %v", err)
	}

	resp, err := app.Test(httptest.NewRequest("GET", "/api/admin/upstream-accounts/candidates", nil))
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("candidate status=%d", resp.StatusCode)
	}
	var decoded struct {
		Success bool `json:"success"`
		Data    []struct {
			Provider                    string  `json:"provider"`
			AuthIndex                   string  `json:"auth_index"`
			Email                       string  `json:"email"`
			AccountConfigured           bool    `json:"account_configured"`
			AccountActive               bool    `json:"account_active"`
			MonthlyCostUSD              float64 `json:"monthly_cost_usd"`
			EstimatedMonthlyCapacityUSD float64 `json:"estimated_monthly_capacity_usd"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode candidates: %v", err)
	}
	if !decoded.Success || len(decoded.Data) != 2 {
		t.Fatalf("unexpected candidates response: %+v", decoded)
	}
	byAuth := map[string]struct {
		Provider                    string
		Email                       string
		AccountConfigured           bool
		AccountActive               bool
		MonthlyCostUSD              float64
		EstimatedMonthlyCapacityUSD float64
	}{}
	for _, row := range decoded.Data {
		byAuth[row.AuthIndex] = struct {
			Provider                    string
			Email                       string
			AccountConfigured           bool
			AccountActive               bool
			MonthlyCostUSD              float64
			EstimatedMonthlyCapacityUSD float64
		}{
			Provider:                    row.Provider,
			Email:                       row.Email,
			AccountConfigured:           row.AccountConfigured,
			AccountActive:               row.AccountActive,
			MonthlyCostUSD:              row.MonthlyCostUSD,
			EstimatedMonthlyCapacityUSD: row.EstimatedMonthlyCapacityUSD,
		}
	}
	if row, ok := byAuth["auth-no-requests"]; !ok || row.Provider != "claude" || row.Email != "claude-a@example.com" || row.AccountConfigured {
		t.Fatalf("no-request credential missing or unexpectedly configured: %+v ok=%v", row, ok)
	}
	if row, ok := byAuth["auth-configured"]; !ok || row.Provider != "codex" || !row.AccountConfigured || !row.AccountActive || row.MonthlyCostUSD != 200 || row.EstimatedMonthlyCapacityUSD != 1000 {
		t.Fatalf("configured credential missing cost config: %+v ok=%v", row, ok)
	}
}

func replaceSysConfigCacheForUpstreamCostTest(next map[string]string) map[string]string {
	proxy.SysConfigMutex.Lock()
	defer proxy.SysConfigMutex.Unlock()
	old := proxy.SysConfigCache
	proxy.SysConfigCache = next
	return old
}

func TestUpstreamMarginReportUsesAccountCostCapacity(t *testing.T) {
	app := setupUpstreamCostTestDB(t)
	now := time.Now()
	apiLog := database.ApiLog{
		UserID:            1,
		ModelName:         "gpt-5.5",
		UpstreamProvider:  "codex",
		UpstreamAuthIndex: "acct-1",
		UpstreamAuthType:  "oauth",
		PromptTokens:      1000,
		CompletionTokens:  500,
		Cost:              100 * database.MicroPerUSD,
		ChargedCost:       150 * database.MicroPerUSD,
		Status:            200,
		CreatedAt:         now,
	}
	if err := database.DB.Create(&apiLog).Error; err != nil {
		t.Fatalf("seed api log: %v", err)
	}
	// 该请求由订阅扣减：营收 = chargedCost = 150 USD
	if err := database.DB.Create(&database.ApiLogRevenue{
		ApiLogID:                 apiLog.ID,
		RevenueSource:            database.RevenueSourceSubscription,
		EffectiveRevenueMicroUSD: 150 * database.MicroPerUSD,
		RecordedAt:               now,
	}).Error; err != nil {
		t.Fatalf("seed api log revenue: %v", err)
	}

	payload := map[string]any{
		"provider":                       "codex",
		"auth_index":                     "acct-1",
		"auth_type":                      "oauth",
		"label":                          "Codex Pro 1",
		"plan_name":                      "ChatGPT Pro",
		"monthly_cost_usd":               20.0,
		"estimated_monthly_capacity_usd": 200.0,
		"active":                         true,
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest("POST", "/api/admin/upstream-accounts", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("create account status=%d", resp.StatusCode)
	}

	resp, err = app.Test(httptest.NewRequest("GET", "/api/admin/upstream-margin?period=30d", nil))
	if err != nil {
		t.Fatalf("margin report: %v", err)
	}
	var decoded struct {
		Success bool `json:"success"`
		Data    struct {
			Summary map[string]any `json:"summary"`
			Rows    []struct {
				Provider                string  `json:"provider"`
				AuthIndex               string  `json:"auth_index"`
				AccountConfigured       bool    `json:"account_configured"`
				RawCostUSD              float64 `json:"raw_cost_usd"`
				SubscriptionRevenueUSD  float64 `json:"subscription_revenue_usd"`
				BalanceRevenueUSD       float64 `json:"balance_revenue_usd"`
				TotalRevenueUSD         float64 `json:"total_revenue_usd"`
				PlatformCostEstimateUSD float64 `json:"platform_cost_estimate_usd"`
				GrossMarginUSD          float64 `json:"gross_margin_usd"`
				CostBasis               string  `json:"cost_basis"`
			} `json:"rows"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !decoded.Success || len(decoded.Data.Rows) != 1 {
		t.Fatalf("unexpected report: %+v", decoded)
	}
	row := decoded.Data.Rows[0]
	if row.Provider != "codex" || row.AuthIndex != "acct-1" || !row.AccountConfigured {
		t.Fatalf("bad row identity/config: %+v", row)
	}
	// 订阅收入 150、余额收入 0、平台成本 = 100 × 20 / 200 = 10、毛利 = 150 - 10 = 140
	if row.RawCostUSD != 100 || row.SubscriptionRevenueUSD != 150 || row.BalanceRevenueUSD != 0 || row.TotalRevenueUSD != 150 {
		t.Fatalf("bad revenue split: %+v", row)
	}
	if row.PlatformCostEstimateUSD != 10 || row.GrossMarginUSD != 140 {
		t.Fatalf("bad margin math: %+v", row)
	}
	if row.CostBasis != "account_capacity" {
		t.Fatalf("cost basis=%q want account_capacity", row.CostBasis)
	}
}

func TestBulkUpsertUpstreamAccountCosts(t *testing.T) {
	app := setupUpstreamCostTestDB(t)
	now := time.Now()
	for _, authIndex := range []string{"acct-a", "acct-b"} {
		if err := database.DB.Create(&database.ApiLog{
			UserID:            1,
			ModelName:         "claude-sonnet-4-6",
			UpstreamProvider:  "anthropic",
			UpstreamAuthIndex: authIndex,
			Cost:              50 * database.MicroPerUSD,
			ChargedCost:       75 * database.MicroPerUSD,
			Status:            200,
			CreatedAt:         now,
		}).Error; err != nil {
			t.Fatalf("seed api log %s: %v", authIndex, err)
		}
	}
	payload := map[string]any{
		"accounts": []map[string]any{
			{"provider": "anthropic", "auth_index": "acct-a", "auth_type": "oauth", "label": "Claude A"},
			{"provider": "anthropic", "auth_index": "acct-b", "auth_type": "oauth", "label": "Claude B"},
		},
		"plan_name":                      "Claude Max 5x",
		"monthly_cost_usd":               100.0,
		"estimated_monthly_capacity_usd": 500.0,
		"active":                         true,
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest("POST", "/api/admin/upstream-accounts/bulk", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("bulk upsert: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("bulk status=%d", resp.StatusCode)
	}
	var count int64
	if err := database.DB.Model(&database.UpstreamAccountCost{}).Count(&count).Error; err != nil {
		t.Fatalf("count account costs: %v", err)
	}
	if count != 2 {
		t.Fatalf("account cost rows=%d want 2", count)
	}
	var estimates []database.ApiLogCostEstimate
	if err := database.DB.Order("api_log_id").Find(&estimates).Error; err != nil {
		t.Fatalf("read estimates: %v", err)
	}
	if len(estimates) != 2 {
		t.Fatalf("estimate rows=%d want 2", len(estimates))
	}
	for _, row := range estimates {
		if row.PlatformCostMicroUSD != 10*database.MicroPerUSD {
			t.Fatalf("platform estimate=%d want 10 USD", row.PlatformCostMicroUSD)
		}
	}
}

func TestRefreshPlatformCost_UpdatesExisting(t *testing.T) {
	setupUpstreamCostTestDB(t)
	now := time.Now()
	if err := database.DB.Create(&database.ApiLog{
		UserID:            1,
		ModelName:         "gpt-5.5",
		UpstreamProvider:  "codex",
		UpstreamAuthIndex: "acct-refresh",
		Cost:              100 * database.MicroPerUSD,
		Status:            200,
		CreatedAt:         now,
	}).Error; err != nil {
		t.Fatalf("seed api log: %v", err)
	}
	var apiLog database.ApiLog
	if err := database.DB.Where("upstream_auth_index = ?", "acct-refresh").First(&apiLog).Error; err != nil {
		t.Fatalf("read api log: %v", err)
	}
	oldComputedAt := now.Add(-24 * time.Hour)
	if _, err := insertApiLogCostEstimateTx(database.DB, apiLog.ID, 10*database.MicroPerUSD, "capacity_share", oldComputedAt); err != nil {
		t.Fatalf("seed estimate: %v", err)
	}

	acct := database.UpstreamAccountCost{
		Provider:                    "codex",
		AuthIndex:                   "acct-refresh",
		MonthlyCostUSD:              50 * database.MicroPerUSD,
		EstimatedMonthlyCapacityUSD: 100 * database.MicroPerUSD,
		Active:                      true,
	}
	if err := refreshPlatformCostEstimateForAccount(acct); err != nil {
		t.Fatalf("refresh estimates: %v", err)
	}
	var estimate database.ApiLogCostEstimate
	if err := database.DB.Where("api_log_id = ?", apiLog.ID).First(&estimate).Error; err != nil {
		t.Fatalf("read estimate: %v", err)
	}
	if estimate.PlatformCostMicroUSD != 50*database.MicroPerUSD {
		t.Fatalf("platform estimate=%d want %d", estimate.PlatformCostMicroUSD, 50*database.MicroPerUSD)
	}
	if !estimate.ComputedAt.After(oldComputedAt) {
		t.Fatalf("computed_at=%s should be after old %s", estimate.ComputedAt, oldComputedAt)
	}
}

func TestBulkUpsertUpstreamCost_RefetchesRows(t *testing.T) {
	app := setupUpstreamCostTestDB(t)
	if err := database.DB.Create(&database.ApiLog{
		UserID:            1,
		ModelName:         "gpt-5.5",
		UpstreamProvider:  "codex",
		UpstreamAuthIndex: "acct-trigger",
		Cost:              100 * database.MicroPerUSD,
		Status:            200,
		CreatedAt:         time.Now(),
	}).Error; err != nil {
		t.Fatalf("seed api log: %v", err)
	}
	if err := database.DB.Create(&database.UpstreamAccountCost{
		Provider:                    "codex",
		AuthIndex:                   "acct-trigger",
		MonthlyCostUSD:              1 * database.MicroPerUSD,
		EstimatedMonthlyCapacityUSD: 100 * database.MicroPerUSD,
		Active:                      true,
	}).Error; err != nil {
		t.Fatalf("seed account cost: %v", err)
	}
	if err := database.DB.Exec(`
CREATE TRIGGER upstream_cost_test_adjust
AFTER UPDATE ON upstream_account_costs
BEGIN
	UPDATE upstream_account_costs
	SET monthly_cost_usd = 50000000,
		estimated_monthly_capacity_usd = 100000000
	WHERE id = NEW.id;
END;`).Error; err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	payload := map[string]any{
		"accounts": []map[string]any{
			{"provider": "codex", "auth_index": "acct-trigger", "auth_type": "oauth", "label": "Codex Trigger"},
		},
		"plan_name":                      "Trigger Plan",
		"monthly_cost_usd":               20.0,
		"estimated_monthly_capacity_usd": 100.0,
		"active":                         true,
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest("POST", "/api/admin/upstream-accounts/bulk", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("bulk upsert: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("bulk status=%d", resp.StatusCode)
	}

	var estimate database.ApiLogCostEstimate
	if err := database.DB.First(&estimate).Error; err != nil {
		t.Fatalf("read estimate: %v", err)
	}
	if estimate.PlatformCostMicroUSD != 50*database.MicroPerUSD {
		t.Fatalf("platform estimate=%d want trigger-adjusted 50 USD", estimate.PlatformCostMicroUSD)
	}
}

func TestUpstreamCost_BigIntCapacityShare(t *testing.T) {
	acct := database.UpstreamAccountCost{
		MonthlyCostUSD:              9_000_000_000_000_000_000,
		EstimatedMonthlyCapacityUSD: 9_000_000_000_000_000_000,
		Active:                      true,
	}
	got := estimatePlatformCostFromAccount(9_000_000_000_000_000_000, acct)
	if got != 9_000_000_000_000_000_000 {
		t.Fatalf("estimate=%d want exact large int64 ratio", got)
	}

	acct.MonthlyCostUSD = 1
	acct.EstimatedMonthlyCapacityUSD = 2
	if got := estimatePlatformCostFromAccount(5, acct); got != 3 {
		t.Fatalf("rounded estimate=%d want 3", got)
	}
}
