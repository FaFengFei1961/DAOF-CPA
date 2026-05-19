// Package proxy / stream_audit.go
//
// M-R2 重构（2026-05-19）：从 stream.go 抽出 audit 相关 helper，纯文件物理拆分。
// 业务逻辑零改动；handler ChatCompletionProxyHandler 仍在 stream.go。

package proxy

import (
	"log"
	"strings"
	"sync"
	"time"

	"daof-cpa/database"
)

type invalidAuthLogBucket struct {
	windowStart time.Time
	count       int
	suppressed  int
}

var (
	invalidAuthLogMu          sync.Mutex
	invalidAuthLogBuckets     = map[string]*invalidAuthLogBucket{}
	invalidAuthLogLastCleanup time.Time
)


func truncForLog(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "...(truncated)"
}


func recordProxyApiLog(userID uint, token, modelName string, status int, clientIP string, startTime time.Time, requestPath, errorType, errorMessage string) {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		modelName = "unknown"
	}
	if status >= 200 && status < 400 {
		errorType = ""
		errorMessage = ""
	}
	database.DB.Create(&database.ApiLog{
		UserID:           userID,
		TokenName:        HashTokenForLog(token),
		ModelName:        modelName,
		RequestedModel:   modelName,
		ServedModel:      modelName,
		ModelWeight:      1,
		HealthMultiplier: 1,
		Status:           status,
		IPAddress:        clientIP,
		Latency:          time.Since(startTime).Milliseconds(),
		Cost:             0,
		RequestPath:      sanitizeError(requestPath, 160),
		ErrorType:        sanitizeError(errorType, 64),
		ErrorMessage:     sanitizeError(errorMessage, 512),
		CreatedAt:        time.Now(),
	})
}

func shouldRecordInvalidAuthApiLog(clientIP string) bool {
	return shouldRecordInvalidAuthApiLogAt(clientIP, time.Now())
}

func shouldRecordInvalidAuthApiLogAt(clientIP string, now time.Time) bool {
	key := strings.TrimSpace(clientIP)
	if key == "" {
		key = "<unknown>"
	}
	windowStart := now.Truncate(time.Minute)

	invalidAuthLogMu.Lock()
	if invalidAuthLogLastCleanup.IsZero() || windowStart.Sub(invalidAuthLogLastCleanup) >= time.Minute {
		cutoff := windowStart.Add(-2 * time.Minute)
		for ip, bucket := range invalidAuthLogBuckets {
			if bucket.windowStart.Before(cutoff) {
				delete(invalidAuthLogBuckets, ip)
			}
		}
		invalidAuthLogLastCleanup = windowStart
	}

	bucket := invalidAuthLogBuckets[key]
	if bucket == nil || !bucket.windowStart.Equal(windowStart) {
		bucket = &invalidAuthLogBucket{windowStart: windowStart}
		invalidAuthLogBuckets[key] = bucket
	}
	if bucket.count < invalidAuthLogLimitPerIPPerMinute {
		bucket.count++
		invalidAuthLogMu.Unlock()
		return true
	}

	bucket.suppressed++
	suppressed := bucket.suppressed
	shouldLog := suppressed == 1 || suppressed%1000 == 0
	invalidAuthLogMu.Unlock()

	if shouldLog {
		log.Printf("[AUTH-INVALID-SUPPRESSED] ip=%s window=%s suppressed=%d", key, windowStart.Format(time.RFC3339), suppressed)
	}
	return false
}


func recordProxyApiLogWithPrecheck(userID uint, token, modelName string, status int, clientIP string, startTime time.Time, requestPath, errorType, errorMessage string, inputTokens, outputTokens int, billing BillingRuleResolution, decision EngineDecision) {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		modelName = "unknown"
	}
	if status >= 200 && status < 400 {
		errorType = ""
		errorMessage = ""
	}
	quotaLimit, quotaUsed, quotaRemaining := precheckQuotaMicroValues(decision)
	database.DB.Create(&database.ApiLog{
		UserID:                 userID,
		TokenName:              HashTokenForLog(token),
		ModelName:              modelName,
		RequestedModel:         modelName,
		ServedModel:            modelName,
		ModelWeight:            billing.ModelWeight,
		HealthMultiplier:       billing.HealthMultiplier,
		BillingRulesVersion:    billing.BillingRulesVersion,
		FallbackUserOptIn:      billing.FallbackUserOptIn,
		Status:                 status,
		IPAddress:              clientIP,
		Latency:                time.Since(startTime).Milliseconds(),
		Cost:                   0,
		ChargedCost:            0,
		PrecheckInputTokens:    inputTokens,
		PrecheckOutputTokens:   outputTokens,
		PrecheckRawCost:        billing.RawCostMicroUSD,
		PrecheckChargedCost:    billing.ChargedCostMicroUSD,
		PrecheckQuotaPlanID:    decision.BlockQuotaPlanID,
		PrecheckQuotaLimit:     quotaLimit,
		PrecheckQuotaUsed:      quotaUsed,
		PrecheckQuotaRemaining: quotaRemaining,
		PrecheckWindowEndAt:    decision.BlockWindowEndAt,
		BlockReason:            sanitizeError(firstNonEmptyString(decision.BlockReason, errorType), 96),
		RequestPath:            sanitizeError(requestPath, 160),
		ErrorType:              sanitizeError(errorType, 64),
		ErrorMessage:           sanitizeError(errorMessage, 512),
		CreatedAt:              time.Now(),
	})
}
