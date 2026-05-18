package database

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestCacheBillingColumnNames(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&ChannelModel{}, &ApiLog{}, &ApiLogAttribution{}, &ApiLogCostEstimate{}, &UpstreamUsageRecord{}, &UpstreamAccountCost{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}

	channelModelColumns := sqliteColumnSet(t, db, "channel_models")
	if !channelModelColumns["cache_write_1h_input_price_pico_per_token"] {
		t.Fatalf("channel_models missing cache_write_1h_input_price_pico_per_token: %#v", channelModelColumns)
	}
	for _, name := range []string{
		"input_price",
		"output_price",
		"cached_input_price",
		"cache_write_input_price",
		"cache_write_1h_input_price",
		"cache_write1h_input_price",
		"high_input_price",
		"high_cached_input_price",
		"high_output_price",
	} {
		if channelModelColumns[name] {
			t.Fatalf("channel_models should not create legacy %s", name)
		}
	}

	apiLogColumns := sqliteColumnSet(t, db, "api_logs")
	for _, name := range []string{
		"cache_write_5m_tokens",
		"cache_write_1h_tokens",
		"requested_model",
		"served_model",
		"charged_cost",
		"model_weight",
		"health_multiplier",
		"billing_rules_version",
		"precheck_input_tokens",
		"precheck_output_tokens",
		"precheck_raw_cost",
		"precheck_charged_cost",
		"precheck_quota_plan_id",
		"precheck_quota_limit",
		"precheck_quota_used",
		"precheck_quota_remaining",
		"precheck_window_end_at",
		"block_reason",
		"fallback_user_opt_in",
		"fallback_reason",
		"upstream_provider",
		"upstream_auth_index",
		"upstream_auth_type",
		"upstream_source",
		"upstream_request_id",
		"upstream_usage_record_id",
		"upstream_usage_match",
		"upstream_usage_synced_at",
	} {
		if !apiLogColumns[name] {
			t.Fatalf("api_logs missing %s: %#v", name, apiLogColumns)
		}
	}
	upstreamUsageColumns := sqliteColumnSet(t, db, "upstream_usage_records")
	for _, name := range []string{
		"provider",
		"model",
		"alias",
		"auth_index",
		"request_id",
		"matched_api_log_id",
		"match_status",
	} {
		if !upstreamUsageColumns[name] {
			t.Fatalf("upstream_usage_records missing %s: %#v", name, upstreamUsageColumns)
		}
	}
	attributionColumns := sqliteColumnSet(t, db, "api_log_attributions")
	for _, name := range []string{
		"api_log_id",
		"upstream_usage_record_id",
		"upstream_provider",
		"upstream_account_auth_index",
		"upstream_auth_type",
		"upstream_source",
		"upstream_request_id",
		"match_reason",
		"matched_at",
	} {
		if !attributionColumns[name] {
			t.Fatalf("api_log_attributions missing %s: %#v", name, attributionColumns)
		}
	}
	costEstimateColumns := sqliteColumnSet(t, db, "api_log_cost_estimates")
	for _, name := range []string{
		"api_log_id",
		"platform_cost_micro_usd",
		"computed_at",
		"method",
	} {
		if !costEstimateColumns[name] {
			t.Fatalf("api_log_cost_estimates missing %s: %#v", name, costEstimateColumns)
		}
	}
	accountCostColumns := sqliteColumnSet(t, db, "upstream_account_costs")
	for _, name := range []string{
		"provider",
		"auth_index",
		"auth_type",
		"label",
		"plan_name",
		"monthly_cost_usd",
		"estimated_monthly_capacity_usd",
		"active",
	} {
		if !accountCostColumns[name] {
			t.Fatalf("upstream_account_costs missing %s: %#v", name, accountCostColumns)
		}
	}
	for _, name := range []string{"cache_write5m_tokens", "cache_write1h_tokens"} {
		if apiLogColumns[name] {
			t.Fatalf("api_logs should not create legacy %s", name)
		}
	}
}

func sqliteColumnSet(t *testing.T, db *gorm.DB, table string) map[string]bool {
	t.Helper()
	var rows []struct {
		Name string
	}
	if err := db.Raw("PRAGMA table_info(" + table + ")").Scan(&rows).Error; err != nil {
		t.Fatalf("table_info %s: %v", table, err)
	}
	out := make(map[string]bool, len(rows))
	for _, row := range rows {
		out[row.Name] = true
	}
	return out
}
