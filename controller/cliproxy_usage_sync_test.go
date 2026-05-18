package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	"daof-cpa/database"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestApiLog_AttributionViaSideTable(t *testing.T) {
	var err error
	database.DB, err = gorm.Open(sqlite.Open("file:cliproxy_usage_sync_exact?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.DB.AutoMigrate(&database.ApiLog{}, &database.ApiLogAttribution{}, &database.ApiLogCostEstimate{}, &database.UpstreamUsageRecord{}); err != nil {
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

	var got database.ApiLogAttribution
	if err := database.DB.Where("api_log_id = ?", apiLog.ID).First(&got).Error; err != nil {
		t.Fatalf("reload api log attribution: %v", err)
	}
	if got.UpstreamProvider != "openai" || got.UpstreamAccountAuthIndex != "auth-index-1" || got.MatchReason != "exact_tokens" {
		t.Fatalf("unexpected upstream attribution: provider=%q auth=%q match=%q", got.UpstreamProvider, got.UpstreamAccountAuthIndex, got.MatchReason)
	}

	var usage database.UpstreamUsageRecord
	if err := database.DB.First(&usage).Error; err != nil {
		t.Fatalf("read usage record: %v", err)
	}
	if usage.MatchedApiLogID != apiLog.ID || usage.MatchStatus != "matched" {
		t.Fatalf("usage match = id:%d status:%q", usage.MatchedApiLogID, usage.MatchStatus)
	}
}

func TestCLIProxyUsageSync_OnlyMatchesPlatformChannelAPIKey(t *testing.T) {
	var err error
	database.DB, err = gorm.Open(sqlite.Open("file:cliproxy_usage_sync_platform_key?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.DB.AutoMigrate(&database.Channel{}, &database.ApiLog{}, &database.ApiLogAttribution{}, &database.ApiLogCostEstimate{}, &database.UpstreamUsageRecord{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := database.DB.Create(&database.Channel{
		Type:   "cliproxy",
		Name:   "platform-cpa",
		Key:    "platform-key",
		Status: 1,
	}).Error; err != nil {
		t.Fatalf("create platform channel: %v", err)
	}

	start := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)
	apiLog := database.ApiLog{
		UserID:           1,
		ModelName:        "gpt-5.5",
		RequestedModel:   "gpt-5.5",
		ServedModel:      "gpt-5.5",
		PromptTokens:     100,
		CompletionTokens: 20,
		Status:           200,
		RequestPath:      "/v1/responses",
		CreatedAt:        start.Add(1500 * time.Millisecond),
	}
	if err := database.DB.Create(&apiLog).Error; err != nil {
		t.Fatalf("create api log: %v", err)
	}

	baseUsage := cpaUsageQueueRecord{
		Provider:  "openai",
		Model:     "gpt-5.5",
		Alias:     "gpt-5.5",
		Endpoint:  "POST /v1/responses",
		AuthType:  "oauth",
		AuthIndex: "auth-index-1",
		Timestamp: start,
		LatencyMs: 1500,
		Tokens: cpaUsageTokens{
			InputTokens:  100,
			OutputTokens: 20,
			TotalTokens:  120,
		},
	}
	personalUsage := baseUsage
	personalUsage.APIKey = "personal-key"
	personalUsage.RequestID = "req-personal"
	platformUsage := baseUsage
	platformUsage.APIKey = "platform-key"
	platformUsage.RequestID = "req-platform"

	result, err := storeAndMatchCLIProxyUsageRecords([]cpaUsageQueueRecord{personalUsage, platformUsage})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if result.Fetched != 2 || result.Stored != 2 || result.Matched != 1 || result.Unmatched != 0 || result.Ignored != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}

	var ignored database.UpstreamUsageRecord
	if err := database.DB.Where("request_id = ?", "req-personal").First(&ignored).Error; err != nil {
		t.Fatalf("read personal usage: %v", err)
	}
	if ignored.MatchStatus != "ignored" || ignored.MatchReason != "ignored_non_platform_key" || ignored.MatchedApiLogID != 0 {
		t.Fatalf("personal usage status=%q reason=%q matched=%d", ignored.MatchStatus, ignored.MatchReason, ignored.MatchedApiLogID)
	}

	var matched database.UpstreamUsageRecord
	if err := database.DB.Where("request_id = ?", "req-platform").First(&matched).Error; err != nil {
		t.Fatalf("read platform usage: %v", err)
	}
	if matched.MatchStatus != "matched" || matched.MatchedApiLogID != apiLog.ID {
		t.Fatalf("platform usage status=%q matched=%d want %d", matched.MatchStatus, matched.MatchedApiLogID, apiLog.ID)
	}
}

func TestApiLog_NoUpdate(t *testing.T) {
	var err error
	database.DB, err = gorm.Open(sqlite.Open("file:api_log_no_update?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.DB.AutoMigrate(&database.ApiLog{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	row := database.ApiLog{
		UserID:    1,
		ModelName: "gpt-5.5",
		Cost:      100,
		Status:    200,
		CreatedAt: time.Now(),
	}
	if err := database.DB.Create(&row).Error; err != nil {
		t.Fatalf("create api log: %v", err)
	}

	res := database.DB.Model(&database.ApiLog{}).Where("id = ?", row.ID).Update("cost", int64(200))
	// fix HIGH（codex audit-integrity）：ApiLog.BeforeUpdate 改为 return ErrApiLogAppendOnly。
	// 期望 Update 报错（loud reject），与侧表（ApiLogAttribution/CostEstimate/Revenue）一致。
	if !errors.Is(res.Error, database.ErrApiLogAppendOnly) {
		t.Fatalf("update error=%v want ErrApiLogAppendOnly", res.Error)
	}
	if res.RowsAffected != 0 {
		t.Fatalf("update rows=%d want 0", res.RowsAffected)
	}

	var got database.ApiLog
	if err := database.DB.First(&got, row.ID).Error; err != nil {
		t.Fatalf("reload api log: %v", err)
	}
	if got.Cost != 100 {
		t.Fatalf("cost changed to %d", got.Cost)
	}
}

func TestApiLog_NoDelete(t *testing.T) {
	var err error
	database.DB, err = gorm.Open(sqlite.Open("file:api_log_no_delete?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.DB.AutoMigrate(&database.ApiLog{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	row := database.ApiLog{
		UserID:    1,
		ModelName: "gpt-5.5",
		Status:    200,
		CreatedAt: time.Now(),
	}
	if err := database.DB.Create(&row).Error; err != nil {
		t.Fatalf("create api log: %v", err)
	}

	res := database.DB.Unscoped().Where("id = ?", row.ID).Delete(&database.ApiLog{})
	if !errors.Is(res.Error, database.ErrApiLogAppendOnly) {
		t.Fatalf("delete error=%v want ErrApiLogAppendOnly", res.Error)
	}
	if res.RowsAffected != 0 {
		t.Fatalf("delete rows=%d want 0", res.RowsAffected)
	}

	var count int64
	if err := database.DB.Model(&database.ApiLog{}).Where("id = ?", row.ID).Count(&count).Error; err != nil {
		t.Fatalf("count api log: %v", err)
	}
	if count != 1 {
		t.Fatalf("api log count=%d want 1", count)
	}
}

func TestStoreAndMatchCLIProxyUsageRecordsKeepsUnmatched(t *testing.T) {
	var err error
	database.DB, err = gorm.Open(sqlite.Open("file:cliproxy_usage_sync_unmatched?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.DB.AutoMigrate(&database.ApiLog{}, &database.ApiLogAttribution{}, &database.ApiLogCostEstimate{}, &database.UpstreamUsageRecord{}); err != nil {
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

func TestKeepCLIProxyUsageSyncLockAlive_WaitsForGoroutine(t *testing.T) {
	var err error
	database.DB, err = gorm.Open(sqlite.Open("file:cliproxy_usage_sync_lock_alive_wait?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.DB.AutoMigrate(&database.DistributedLock{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	ownerID, acquired, err := database.AcquireLock(cliproxyUsageSyncLockKey, cliproxyUsageSyncLockTTL)
	if err != nil {
		t.Fatalf("acquire lock: %v", err)
	}
	if !acquired {
		t.Fatalf("expected to acquire test lock")
	}

	exited := make(chan struct{})
	cliproxyUsageSyncLockAliveExitHook = func() {
		close(exited)
	}
	defer func() {
		cliproxyUsageSyncLockAliveExitHook = nil
	}()

	release := keepCLIProxyUsageSyncLockAlive(context.Background(), ownerID)
	release()

	select {
	case <-exited:
	default:
		t.Fatalf("release returned before renew goroutine exited")
	}

	var count int64
	if err := database.DB.Model(&database.DistributedLock{}).Where("lock_key = ?", cliproxyUsageSyncLockKey).Count(&count).Error; err != nil {
		t.Fatalf("count lock: %v", err)
	}
	if count != 0 {
		t.Fatalf("lock row count=%d want 0", count)
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
