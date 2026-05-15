package controller

import (
	"context"
	"testing"
	"time"

	"daof-ai-hub/database"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
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

func TestSyncCLIProxyUsage_SkippedIfLockHeld(t *testing.T) {
	var err error
	database.DB, err = gorm.Open(sqlite.Open("file:cliproxy_usage_sync_lock_held?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.DB.AutoMigrate(&database.DistributedLock{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	now := time.Now().UTC()
	lock := database.DistributedLock{
		LockKey:     cliproxyUsageSyncLockKey,
		OwnerID:     "other-owner",
		AcquiredAt:  now,
		HeartbeatAt: now,
		ExpiresAt:   now.Add(time.Minute),
	}
	if err := database.DB.Create(&lock).Error; err != nil {
		t.Fatalf("create held lock: %v", err)
	}

	result, err := SyncCLIProxyUsageQueue(context.Background(), 1)
	if err != nil {
		t.Fatalf("sync queue: %v", err)
	}
	if result != (CLIProxyUsageSyncResult{}) {
		t.Fatalf("expected skipped zero result, got %+v", result)
	}

	var got database.DistributedLock
	if err := database.DB.Where("lock_key = ?", cliproxyUsageSyncLockKey).First(&got).Error; err != nil {
		t.Fatalf("read lock: %v", err)
	}
	if got.OwnerID != "other-owner" {
		t.Fatalf("lock owner changed to %q", got.OwnerID)
	}
}

func TestSyncCLIProxyUsage_PersistsRawBeforeMatching(t *testing.T) {
	var err error
	database.DB, err = gorm.Open(sqlite.Open("file:cliproxy_usage_sync_raw_first?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.DB.AutoMigrate(&database.UpstreamUsageRecord{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	records := []cpaUsageQueueRecord{
		{
			Provider:  "openai",
			Model:     "gpt-5.5",
			Alias:     "gpt-5.5",
			Endpoint:  "POST /v1/responses",
			AuthIndex: "auth-index-1",
			RequestID: "req-raw-1",
			Timestamp: time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC),
			Tokens: cpaUsageTokens{
				InputTokens:  10,
				OutputTokens: 2,
				TotalTokens:  12,
			},
		},
		{
			Provider:  "claude",
			Model:     "claude-opus-4-7",
			Alias:     "claude-opus-4-7",
			Endpoint:  "POST /v1/messages",
			AuthIndex: "auth-index-2",
			RequestID: "req-raw-2",
			Timestamp: time.Date(2026, 5, 13, 10, 1, 0, 0, time.UTC),
			Tokens: cpaUsageTokens{
				InputTokens:  20,
				OutputTokens: 4,
				TotalTokens:  24,
			},
		},
	}
	result, err := storeAndMatchCLIProxyUsageRecords(records)
	if err != nil {
		t.Fatalf("store and match: %v", err)
	}
	if result.Fetched != 2 || result.Stored != 2 || result.Matched != 0 || result.Unmatched != 0 {
		t.Fatalf("unexpected result: %+v", result)
	}

	var usages []database.UpstreamUsageRecord
	if err := database.DB.Order("request_id ASC").Find(&usages).Error; err != nil {
		t.Fatalf("read usage records: %v", err)
	}
	if len(usages) != 2 {
		t.Fatalf("usage record count=%d want 2", len(usages))
	}
	for _, usage := range usages {
		if usage.MatchStatus != "pending" {
			t.Fatalf("usage %s match_status=%q want pending", usage.RequestID, usage.MatchStatus)
		}
	}
}
