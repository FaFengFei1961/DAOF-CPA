package controller

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"daof-ai-hub/database"
	"daof-ai-hub/proxy"

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
	if err := db.AutoMigrate(&database.ApiLog{}, &database.UpstreamAccountCost{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	database.DB = db
	app := fiber.New()
	app.Get("/api/admin/upstream-account-cost-presets", ListUpstreamAccountCostPresets)
	app.Post("/api/admin/upstream-accounts", CreateUpstreamAccountCost)
	app.Post("/api/admin/upstream-accounts/bulk", BulkUpsertUpstreamAccountCosts)
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
	if err := database.DB.Create(&database.ApiLog{
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
	}).Error; err != nil {
		t.Fatalf("seed api log: %v", err)
	}

	payload := map[string]any{
		"provider":                       "codex",
		"auth_index":                     "acct-1",
		"auth_type":                      "oauth",
		"label":                          "Codex Pro 1",
		"plan_name":                      "ChatGPT Pro",
		"monthly_cost_usd":               20,
		"estimated_monthly_capacity_usd": 200,
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
				ChargedCostUSD          float64 `json:"charged_cost_usd"`
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
	if row.RawCostUSD != 100 || row.ChargedCostUSD != 150 || row.PlatformCostEstimateUSD != 10 || row.GrossMarginUSD != 140 {
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
		"monthly_cost_usd":               100,
		"estimated_monthly_capacity_usd": 500,
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
	var updatedLogs []database.ApiLog
	if err := database.DB.Order("upstream_auth_index").Find(&updatedLogs).Error; err != nil {
		t.Fatalf("read logs: %v", err)
	}
	for _, row := range updatedLogs {
		if row.PlatformCostEstimate != 10*database.MicroPerUSD {
			t.Fatalf("platform estimate for %s=%d want 10 USD", row.UpstreamAuthIndex, row.PlatformCostEstimate)
		}
	}
}
