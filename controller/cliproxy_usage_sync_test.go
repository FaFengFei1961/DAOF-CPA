package controller

import (
	"testing"
	"time"

	"daof-ai-hub/database"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestStoreAndMatchCLIProxyUsageRecordsExactTokens(t *testing.T) {
	var err error
	database.DB, err = gorm.Open(sqlite.Open("file:cliproxy_usage_sync_exact?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.DB.AutoMigrate(&database.ApiLog{}, &database.UpstreamUsageRecord{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	start := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	apiLog := database.ApiLog{
		UserID:           1,
		ModelName:        "gpt-5.5",
		RequestedModel:   "gpt-5.5",
		ServedModel:      "gpt-5.5",
		PromptTokens:     100,
		CompletionTokens: 20,
		ReasoningTokens:  5,
		CachedTokens:     30,
		Status:           200,
		RequestPath:      "/v1/responses",
		CreatedAt:        start.Add(1500 * time.Millisecond),
	}
	if err := database.DB.Create(&apiLog).Error; err != nil {
		t.Fatalf("create api log: %v", err)
	}

	result, err := storeAndMatchCLIProxyUsageRecords([]cpaUsageQueueRecord{{
		Provider:  "openai",
		Model:     "gpt-5.5",
		Alias:     "gpt-5.5",
		Endpoint:  "POST /v1/responses",
		AuthType:  "oauth",
		Source:    "acct@example.com",
		AuthIndex: "auth-index-1",
		RequestID: "req-1",
		Timestamp: start,
		LatencyMs: 1500,
		Tokens: cpaUsageTokens{
			InputTokens:     100,
			OutputTokens:    20,
			ReasoningTokens: 5,
			CacheReadTokens: 30,
			TotalTokens:     125,
		},
	}})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if result.Fetched != 1 || result.Stored != 1 || result.Matched != 1 || result.Unmatched != 0 {
		t.Fatalf("unexpected result: %+v", result)
	}

	var got database.ApiLog
	if err := database.DB.First(&got, apiLog.ID).Error; err != nil {
		t.Fatalf("reload api log: %v", err)
	}
	if got.UpstreamProvider != "openai" || got.UpstreamAuthIndex != "auth-index-1" || got.UpstreamUsageMatch != "exact_tokens" {
		t.Fatalf("unexpected upstream attribution: provider=%q auth=%q match=%q", got.UpstreamProvider, got.UpstreamAuthIndex, got.UpstreamUsageMatch)
	}

	var usage database.UpstreamUsageRecord
	if err := database.DB.First(&usage).Error; err != nil {
		t.Fatalf("read usage record: %v", err)
	}
	if usage.MatchedApiLogID != apiLog.ID || usage.MatchStatus != "matched" {
		t.Fatalf("usage match = id:%d status:%q", usage.MatchedApiLogID, usage.MatchStatus)
	}
}

func TestStoreAndMatchCLIProxyUsageRecordsKeepsUnmatched(t *testing.T) {
	var err error
	database.DB, err = gorm.Open(sqlite.Open("file:cliproxy_usage_sync_unmatched?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.DB.AutoMigrate(&database.ApiLog{}, &database.UpstreamUsageRecord{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	result, err := storeAndMatchCLIProxyUsageRecords([]cpaUsageQueueRecord{{
		Provider:  "claude",
		Model:     "claude-opus-4-7",
		Alias:     "claude-opus-4-7",
		Endpoint:  "POST /v1/messages",
		AuthIndex: "auth-index-2",
		Timestamp: time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC),
		Tokens: cpaUsageTokens{
			InputTokens:  10,
			OutputTokens: 5,
			TotalTokens:  15,
		},
	}})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if result.Stored != 1 || result.Matched != 0 || result.Unmatched != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}

	var usage database.UpstreamUsageRecord
	if err := database.DB.First(&usage).Error; err != nil {
		t.Fatalf("read usage record: %v", err)
	}
	if usage.MatchStatus != "unmatched" || usage.MatchReason != "no_candidate" {
		t.Fatalf("usage status=%q reason=%q", usage.MatchStatus, usage.MatchReason)
	}
}
