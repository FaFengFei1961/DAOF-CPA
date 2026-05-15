package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"daof-ai-hub/database"
	"daof-ai-hub/proxy"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	defaultCLIProxyUsageSyncCount = 100
	maxCLIProxyUsageSyncCount     = 1000
	cliproxyUsageSyncLockKey      = "cliproxy_usage_sync"
	cliproxyUsageSyncLockTTL      = 60 * time.Second
	cliproxyUsageSyncRenewEvery   = 20 * time.Second
)

var (
	cliproxyUsageSyncDone              chan struct{}
	cliproxyUsageSyncOnce              sync.Once
	cliproxyUsageSyncStop              sync.Once
	cliproxyUsageSyncLockAliveExitHook func()
)

type cpaUsageQueueRecord struct {
	Provider  string         `json:"provider"`
	Model     string         `json:"model"`
	Alias     string         `json:"alias"`
	Endpoint  string         `json:"endpoint"`
	AuthType  string         `json:"auth_type"`
	APIKey    string         `json:"api_key"`
	RequestID string         `json:"request_id"`
	Timestamp time.Time      `json:"timestamp"`
	LatencyMs int64          `json:"latency_ms"`
	Source    string         `json:"source"`
	AuthIndex string         `json:"auth_index"`
	Tokens    cpaUsageTokens `json:"tokens"`
	Failed    bool           `json:"failed"`
	Fail      cpaUsageFail   `json:"fail"`
}

type cpaUsageTokens struct {
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	ReasoningTokens     int64 `json:"reasoning_tokens"`
	CachedTokens        int64 `json:"cached_tokens"`
	CacheReadTokens     int64 `json:"cache_read_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
}

type cpaUsageFail struct {
	StatusCode int    `json:"status_code"`
	Body       string `json:"body"`
}

type CLIProxyUsageSyncResult struct {
	Fetched   int `json:"fetched"`
	Stored    int `json:"stored"`
	Matched   int `json:"matched"`
	Unmatched int `json:"unmatched"`
}

// StartCLIProxyUsageSyncCron 定时拉取 CPA usage queue。
// 队列是 pop 语义，后台短周期同步能避免 CPA 侧队列过期或堆积。
func StartCLIProxyUsageSyncCron() {
	cliproxyUsageSyncOnce.Do(func() {
		cliproxyUsageSyncDone = make(chan struct{})
		go func() {
			select {
			case <-time.After(15 * time.Second):
			case <-cliproxyUsageSyncDone:
				return
			}
			for {
				runCLIProxyUsageSyncOnce()
				interval := cliproxyUsageSyncInterval()
				timer := time.NewTimer(interval)
				select {
				case <-timer.C:
				case <-cliproxyUsageSyncDone:
					timer.Stop()
					return
				}
			}
		}()
		log.Println("📊 CLIProxyAPI 用量队列同步器已启动")
	})
}

func StopCLIProxyUsageSyncCron() {
	cliproxyUsageSyncStop.Do(func() {
		if cliproxyUsageSyncDone != nil {
			close(cliproxyUsageSyncDone)
		}
	})
}

func runCLIProxyUsageSyncOnce() {
	defer func() {
		if r := recover(); r != nil {
			// fix CRITICAL（多模型审计第二十五轮）：panic 值可能含上游响应碎片（含 Bearer/JWT 等），
			// 必须经 sanitizeError 过滤后再写日志，避免凭据泄漏到系统日志。
			// 调用栈本身不含敏感数据，但 panic 值常携带原始字符串，必须脱敏。
			log.Printf("[CLIPROXY-USAGE-SYNC] panic: %s\n%s",
				proxy.SanitizeErrorMessage(fmt.Sprintf("%v", r), 500),
				debug.Stack())
		}
	}()

	if !cliproxyUsageSyncEnabled() || !proxy.IsCliproxyConfigured() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	result, err := SyncCLIProxyUsageQueue(ctx, cliproxyUsageSyncBatchSize())
	if err != nil {
		log.Printf("[CLIPROXY-USAGE-SYNC] failed: %v", err)
		return
	}
	if result.Fetched > 0 {
		log.Printf("[CLIPROXY-USAGE-SYNC] fetched=%d stored=%d matched=%d unmatched=%d",
			result.Fetched, result.Stored, result.Matched, result.Unmatched)
	}
}

func cliproxyUsageSyncEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(readSysConfigCached("cliproxy_usage_sync_enabled", "true")))
	return v == "" || v == "true" || v == "1" || v == "yes" || v == "on"
}

func cliproxyUsageSyncInterval() time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(readSysConfigCached("cliproxy_usage_sync_interval_seconds", "60")))
	if err != nil || n <= 0 {
		n = 60
	}
	if n < 10 {
		n = 10
	}
	if n > 3600 {
		n = 3600
	}
	return time.Duration(n) * time.Second
}

func cliproxyUsageSyncBatchSize() int {
	n, err := strconv.Atoi(strings.TrimSpace(readSysConfigCached("cliproxy_usage_sync_batch_size", strconv.Itoa(defaultCLIProxyUsageSyncCount))))
	if err != nil || n <= 0 {
		n = defaultCLIProxyUsageSyncCount
	}
	if n > maxCLIProxyUsageSyncCount {
		n = maxCLIProxyUsageSyncCount
	}
	return n
}

// SyncCLIProxyUsage 手动拉取 CLIProxyAPI usage queue，并归因到本地 ApiLog。
//
// 注意：CPA /usage-queue 是 pop 语义。实现必须先把记录落到 upstream_usage_records，
// 再做匹配；匹配失败也保留记录，避免对账事实丢失。
func SyncCLIProxyUsage(c *fiber.Ctx) error {
	count := parseUsageSyncCount(c.Query("count"))
	result, err := SyncCLIProxyUsageQueue(c.Context(), count)
	if err != nil {
		log.Printf("[CLIPROXY-USAGE-SYNC] failed: %v", err)
		return c.Status(502).JSON(fiber.Map{
			"success":      false,
			"message":      "同步 CLIProxyAPI 用量队列失败",
			"message_code": "ERR_CLIPROXY_USAGE_SYNC",
		})
	}
	return c.JSON(fiber.Map{
		"success": true,
		"data":    result,
	})
}

func SyncCLIProxyUsageQueue(ctx context.Context, count int) (CLIProxyUsageSyncResult, error) {
	ownerID, acquired, err := database.AcquireLock(cliproxyUsageSyncLockKey, cliproxyUsageSyncLockTTL)
	if err != nil {
		return CLIProxyUsageSyncResult{}, fmt.Errorf("acquire cliproxy usage sync lock: %w", err)
	}
	if !acquired {
		log.Printf("[CLIPROXY-USAGE-SYNC] skipped: lock held by another owner")
		return CLIProxyUsageSyncResult{}, nil
	}
	release := keepCLIProxyUsageSyncLockAlive(ctx, ownerID)
	defer release()

	records, err := fetchCLIProxyUsageQueue(ctx, count)
	if err != nil {
		return CLIProxyUsageSyncResult{}, err
	}
	return storeAndMatchCLIProxyUsageRecords(records)
}

func keepCLIProxyUsageSyncLockAlive(ctx context.Context, ownerID string) func() {
	done := make(chan struct{})
	var ctxDone <-chan struct{}
	if ctx != nil {
		ctxDone = ctx.Done()
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer func() {
			if cliproxyUsageSyncLockAliveExitHook != nil {
				cliproxyUsageSyncLockAliveExitHook()
			}
		}()
		ticker := time.NewTicker(cliproxyUsageSyncRenewEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				renewed, err := database.RenewLock(cliproxyUsageSyncLockKey, ownerID, cliproxyUsageSyncLockTTL)
				if err != nil {
					log.Printf("[CLIPROXY-USAGE-SYNC] renew lock failed: %v", err)
					continue
				}
				if !renewed {
					log.Printf("[CLIPROXY-USAGE-SYNC] lock lost before sync completed")
					return
				}
			case <-ctxDone:
				return
			case <-done:
				return
			}
		}
	}()

	return func() {
		close(done)
		wg.Wait()
		if err := database.ReleaseLock(cliproxyUsageSyncLockKey, ownerID); err != nil {
			log.Printf("[CLIPROXY-USAGE-SYNC] release lock failed: %v", err)
		}
	}
}

func parseUsageSyncCount(raw string) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n <= 0 {
		return defaultCLIProxyUsageSyncCount
	}
	if n > maxCLIProxyUsageSyncCount {
		return maxCLIProxyUsageSyncCount
	}
	return n
}

func fetchCLIProxyUsageQueue(ctx context.Context, count int) ([]cpaUsageQueueRecord, error) {
	if count <= 0 {
		count = defaultCLIProxyUsageSyncCount
	}
	if count > maxCLIProxyUsageSyncCount {
		count = maxCLIProxyUsageSyncCount
	}

	cliproxyURL := getDecryptedConfig("cliproxy_url")
	cliproxyKey := getDecryptedConfig("cliproxy_key")
	if cliproxyURL == "" {
		cliproxyURL = "http://127.0.0.1:8080"
	}
	if err := proxy.ValidateChannelURL(cliproxyURL); err != nil {
		return nil, fmt.Errorf("unsafe cliproxy_url: %w", err)
	}

	targetURL := strings.TrimRight(cliproxyURL, "/") + "/v0/management/usage-queue?count=" + strconv.Itoa(count)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if cliproxyKey != "" {
		req.Header.Set("Authorization", "Bearer "+cliproxyKey)
	}

	client := &http.Client{Timeout: 15 * time.Second, Transport: proxy.SafeTransport()}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request usage queue: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read usage queue: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("usage queue returned HTTP %d", resp.StatusCode)
	}

	var records []cpaUsageQueueRecord
	if err := json.Unmarshal(body, &records); err != nil {
		return nil, fmt.Errorf("decode usage queue: %w", err)
	}
	return records, nil
}

func storeAndMatchCLIProxyUsageRecords(records []cpaUsageQueueRecord) (CLIProxyUsageSyncResult, error) {
	result := CLIProxyUsageSyncResult{Fetched: len(records)}
	if len(records) == 0 {
		return result, nil
	}

	usageRecords := make([]database.UpstreamUsageRecord, 0, len(records))
	for _, raw := range records {
		usageRecords = append(usageRecords, convertCPAUsageRecord(raw))
	}

	if err := database.DB.Transaction(func(tx *gorm.DB) error {
		return tx.CreateInBatches(&usageRecords, 100).Error
	}); err != nil {
		return result, err
	}
	result.Stored = len(usageRecords)

	for i := range usageRecords {
		rec := usageRecords[i]
		err := database.DB.Transaction(func(tx *gorm.DB) error {
			matched, reason, err := matchUpstreamUsageRecordTx(tx, &rec)
			if err != nil {
				return err
			}
			if matched {
				result.Matched++
				return nil
			}
			result.Unmatched++
			return tx.Model(&rec).Updates(map[string]any{
				"match_status": "unmatched",
				"match_reason": trimForDB(reason, 255),
			}).Error
		})
		if err != nil {
			log.Printf("[CLIPROXY-USAGE-SYNC] match raw usage record id=%d failed: %v", rec.ID, err)
		}
	}
	return result, nil
}

func convertCPAUsageRecord(raw cpaUsageQueueRecord) database.UpstreamUsageRecord {
	status := raw.Fail.StatusCode
	if status == 0 && !raw.Failed {
		status = http.StatusOK
	}
	return database.UpstreamUsageRecord{
		Provider:            trimForDB(raw.Provider, 64),
		Model:               trimForDB(raw.Model, 160),
		Alias:               trimForDB(raw.Alias, 160),
		Endpoint:            trimForDB(raw.Endpoint, 160),
		AuthType:            trimForDB(raw.AuthType, 64),
		AuthIndex:           trimForDB(raw.AuthIndex, 64),
		Source:              trimForDB(raw.Source, 255),
		APIKeyHash:          proxy.HashTokenForLog(raw.APIKey),
		RequestID:           trimForDB(raw.RequestID, 64),
		Timestamp:           raw.Timestamp,
		Latency:             raw.LatencyMs,
		InputTokens:         safeInt(raw.Tokens.InputTokens),
		OutputTokens:        safeInt(raw.Tokens.OutputTokens),
		ReasoningTokens:     safeInt(raw.Tokens.ReasoningTokens),
		CachedTokens:        safeInt(raw.Tokens.CachedTokens),
		CacheReadTokens:     safeInt(raw.Tokens.CacheReadTokens),
		CacheCreationTokens: safeInt(raw.Tokens.CacheCreationTokens),
		TotalTokens:         safeInt(raw.Tokens.TotalTokens),
		Failed:              raw.Failed,
		Status:              status,
		// fix CRITICAL（多模型审计第二十五轮）：上游 4xx/5xx 响应体里常含 Bearer / api_key /
		// JWT 等凭据（如 401 unauthorized 通常会回显 token 前缀）。原实现仅截断不脱敏，
		// 直接持久化到 DB 形成泄漏面。必须先经 SanitizeErrorMessage 过滤再截断。
		FailBody:    trimForDB(proxy.SanitizeErrorMessage(raw.Fail.Body, 512), 512),
		MatchStatus: "pending",
	}
}

func matchUpstreamUsageRecordTx(tx *gorm.DB, rec *database.UpstreamUsageRecord) (bool, string, error) {
	candidates, err := findApiLogCandidatesTx(tx, rec)
	if err != nil {
		return false, "candidate_query_failed", err
	}
	if len(candidates) == 0 {
		return false, "no_candidate", nil
	}

	hasTokenFacts := rec.InputTokens > 0 ||
		rec.OutputTokens > 0 ||
		rec.ReasoningTokens > 0 ||
		rec.CachedTokens > 0 ||
		rec.CacheReadTokens > 0 ||
		rec.CacheCreationTokens > 0 ||
		rec.TotalTokens > 0

	var chosen *database.ApiLog
	matchReason := ""
	if hasTokenFacts {
		for i := range candidates {
			if apiLogTokensExact(candidates[i], rec) {
				if chosen == nil || usageTimeDistance(candidates[i], rec) < usageTimeDistance(*chosen, rec) {
					chosen = &candidates[i]
				}
			}
		}
		if chosen == nil {
			return false, "no_exact_token_match", nil
		}
		matchReason = "exact_tokens"
	} else {
		if len(candidates) != 1 {
			return false, "ambiguous_zero_usage", nil
		}
		chosen = &candidates[0]
		matchReason = "single_candidate_zero_usage"
	}

	now := time.Now()
	attribution := database.ApiLogAttribution{
		ApiLogID:                 chosen.ID,
		UpstreamUsageRecordID:    rec.ID,
		UpstreamProvider:         trimForDB(rec.Provider, 64),
		UpstreamAccountAuthIndex: trimForDB(rec.AuthIndex, 64),
		UpstreamAuthType:         trimForDB(rec.AuthType, 64),
		UpstreamSource:           trimForDB(rec.Source, 255),
		UpstreamRequestID:        trimForDB(rec.RequestID, 64),
		MatchReason:              matchReason,
		MatchedAt:                now,
	}
	res := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "api_log_id"}},
		DoNothing: true,
	}).Create(&attribution)
	if res.Error != nil {
		return false, "api_log_attribution_insert_failed", res.Error
	}
	if res.RowsAffected == 0 {
		return false, "api_log_already_matched", nil
	}
	if estimate := platformCostEstimateForMatchedLogTx(tx, rec.Provider, rec.AuthIndex, chosen.Cost); estimate > 0 {
		if _, err := insertApiLogCostEstimateTx(tx, chosen.ID, estimate, "capacity_share", now); err != nil {
			return false, "api_log_cost_estimate_insert_failed", err
		}
	}
	if err := tx.Model(rec).Updates(map[string]any{
		"matched_api_log_id": chosen.ID,
		"match_status":       "matched",
		"match_reason":       matchReason,
	}).Error; err != nil {
		return false, "usage_record_update_failed", err
	}
	return true, matchReason, nil
}

func findApiLogCandidatesTx(tx *gorm.DB, rec *database.UpstreamUsageRecord) ([]database.ApiLog, error) {
	names := compactStrings(rec.Alias, rec.Model)
	if len(names) == 0 {
		names = []string{"unknown"}
	}

	start, end := usageMatchWindow(rec)
	q := tx.Model(&database.ApiLog{}).
		Where("api_logs.upstream_usage_record_id = 0").
		Where("NOT EXISTS (SELECT 1 FROM api_log_attributions WHERE api_log_attributions.api_log_id = api_logs.id)").
		Where("created_at BETWEEN ? AND ?", start, end).
		Where("(model_name IN ? OR requested_model IN ? OR served_model IN ?)", names, names, names)

	if path := usageEndpointPath(rec.Endpoint); path != "" {
		q = q.Where("request_path = ?", path)
	}
	if rec.Failed || rec.Status >= http.StatusBadRequest {
		q = q.Where("status >= ?", http.StatusBadRequest)
	} else {
		q = q.Where("status >= ? AND status < ?", http.StatusOK, http.StatusBadRequest)
	}

	var out []database.ApiLog
	err := q.Order("created_at DESC").Limit(50).Find(&out).Error
	return out, err
}

func usageMatchWindow(rec *database.UpstreamUsageRecord) (time.Time, time.Time) {
	if rec.Timestamp.IsZero() {
		now := time.Now()
		return now.Add(-10 * time.Minute), now.Add(time.Minute)
	}
	end := rec.Timestamp.Add(time.Duration(rec.Latency)*time.Millisecond + 90*time.Second)
	return rec.Timestamp.Add(-20 * time.Second), end
}

func apiLogTokensExact(logRow database.ApiLog, rec *database.UpstreamUsageRecord) bool {
	if rec.InputTokens > 0 && logRow.PromptTokens != rec.InputTokens {
		return false
	}
	if rec.OutputTokens > 0 && logRow.CompletionTokens != rec.OutputTokens {
		return false
	}
	if rec.ReasoningTokens > 0 && logRow.ReasoningTokens != rec.ReasoningTokens {
		return false
	}
	cacheRead := rec.CacheReadTokens
	if cacheRead == 0 {
		cacheRead = rec.CachedTokens
	}
	if cacheRead > 0 && logRow.CachedTokens != cacheRead {
		return false
	}
	if rec.CacheCreationTokens > 0 && logRow.CacheWriteTokens != rec.CacheCreationTokens {
		return false
	}
	if rec.TotalTokens > 0 && logRow.PromptTokens+logRow.CompletionTokens != rec.TotalTokens {
		// 有些 provider 的 total_tokens 会单独包含 reasoning；如果基础输入输出能完全匹配，
		// 不能因为 total 口径差异错过精确归因。
		if rec.TotalTokens != logRow.PromptTokens+logRow.CompletionTokens+logRow.ReasoningTokens {
			return false
		}
	}
	return true
}

func usageTimeDistance(logRow database.ApiLog, rec *database.UpstreamUsageRecord) time.Duration {
	target := rec.Timestamp
	if !target.IsZero() && rec.Latency > 0 {
		target = target.Add(time.Duration(rec.Latency) * time.Millisecond)
	}
	if target.IsZero() {
		return 0
	}
	d := logRow.CreatedAt.Sub(target)
	if d < 0 {
		return -d
	}
	return d
}

func usageEndpointPath(endpoint string) string {
	fields := strings.Fields(strings.TrimSpace(endpoint))
	if len(fields) >= 2 {
		return strings.TrimSpace(fields[1])
	}
	if strings.HasPrefix(endpoint, "/") {
		return strings.TrimSpace(endpoint)
	}
	return ""
}

func compactStrings(values ...string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func trimForDB(value string, max int) string {
	value = strings.TrimSpace(value)
	if max > 0 && len(value) > max {
		return value[:max]
	}
	return value
}

func safeInt(v int64) int {
	if v <= 0 {
		return 0
	}
	maxInt := int64(^uint(0) >> 1)
	if v > maxInt {
		return int(maxInt)
	}
	return int(v)
}
