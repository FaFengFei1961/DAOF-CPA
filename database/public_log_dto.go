// Package database / public_log_dto.go
//
// PublicApiLog 是 ApiLog 的用户侧白名单 DTO，专门用于面向普通用户的 HTTP 响应。
//
// 背景（多模型审计第二十五轮）：
//   - 平台原则要求 CPA 上游账号池细节不能暴露给普通用户
//   - 平台原则要求 platform_cost_estimate（毛利估算）不能展示为用户扣费依据
//   - 但 ApiLog 模型字段直接暴露在 /api/logs 与 /api/logs/stats.recent_logs 等用户接口
//     会通过 ApiLog.MarshalJSON 把 platform_cost_estimate / upstream_* 一起吐出
//
// 解决：
//   - 用户接口在序列化前显式转 PublicApiLog；金额按 USD float 输出（与 MarshalJSON 一致）
//   - admin 接口仍可使用 ApiLog（或其专用 admin DTO，如 GetUsersUsageEvents.eventOut）
//   - 任何新增的用户接口若要返回 ApiLog，必须经过本 DTO
package database

import (
	"encoding/json"
	"time"
)

// PublicApiLogUsageLine 是 ApiLogUsageLine 的用户/管理员共同可见白名单 DTO。
// 它只暴露可对账的用量事实，不暴露上游账号、内部成本归因或任何凭证信息。
type PublicApiLogUsageLine struct {
	ID           uint           `json:"id"`
	ApiLogID     uint           `json:"api_log_id"`
	ModelName    string         `json:"model_name"`
	RequestPath  string         `json:"request_path"`
	Unit         string         `json:"unit"`
	Direction    string         `json:"direction"`
	Quantity     int64          `json:"quantity"`
	UnitPrice    float64        `json:"unit_price"`
	Amount       float64        `json:"amount"`
	CostSource   string         `json:"cost_source"`
	Quality      string         `json:"quality"`
	Size         string         `json:"size"`
	Resolution   string         `json:"resolution"`
	AspectRatio  string         `json:"aspect_ratio"`
	Metadata     map[string]any `json:"metadata,omitempty"`
	MetadataJSON string         `json:"metadata_json,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
}

// PublicApiLog 仅暴露用户视角可审计的字段。
// 故意 *不* 包含 PlatformCostEstimate、UpstreamProvider/AuthIndex/AuthType/Source/RequestID/UsageRecordID/UsageMatch/UsageSyncedAt
// 这些是平台内部账户成本归因 + CPA 池信息，不属于用户视角。
type PublicApiLog struct {
	ID                     uint                    `json:"id"`
	UserID                 uint                    `json:"user_id"`
	TokenName              string                  `json:"token_name"`
	ModelName              string                  `json:"model_name"`
	RequestedModel         string                  `json:"requested_model"`
	ServedModel            string                  `json:"served_model"`
	PromptTokens           int                     `json:"prompt_tokens"`
	CompletionTokens       int                     `json:"completion_tokens"`
	CachedTokens           int                     `json:"cached_tokens"`
	CacheWriteTokens       int                     `json:"cache_write_tokens"`
	CacheWrite5mTokens     int                     `json:"cache_write_5m_tokens"`
	CacheWrite1hTokens     int                     `json:"cache_write_1h_tokens"`
	ReasoningTokens        int                     `json:"reasoning_tokens"`
	Cost                   float64                 `json:"cost"`         // USD（=raw_cost；保留旧字段名兼容前端）
	RawCost                float64                 `json:"raw_cost"`     // USD，官方 API 等值原始成本
	ChargedCost            float64                 `json:"charged_cost"` // USD，套餐/credits 实际扣减成本
	ModelWeight            float64                 `json:"model_weight"`
	HealthMultiplier       float64                 `json:"health_multiplier"`
	BillingRulesVersion    string                  `json:"billing_rules_version"`
	PrecheckInputTokens    int                     `json:"precheck_input_tokens"`
	PrecheckOutputTokens   int                     `json:"precheck_output_tokens"`
	PrecheckRawCost        float64                 `json:"precheck_raw_cost"`
	PrecheckChargedCost    float64                 `json:"precheck_charged_cost"`
	PrecheckQuotaPlanID    uint                    `json:"precheck_quota_plan_id"`
	PrecheckQuotaLimit     float64                 `json:"precheck_quota_limit"`
	PrecheckQuotaUsed      float64                 `json:"precheck_quota_used"`
	PrecheckQuotaRemaining float64                 `json:"precheck_quota_remaining"`
	PrecheckWindowEndAt    *time.Time              `json:"precheck_window_end_at"`
	BlockReason            string                  `json:"block_reason"`
	FallbackUserOptIn      bool                    `json:"fallback_user_opt_in"`
	FallbackReason         string                  `json:"fallback_reason"`
	Latency                int64                   `json:"latency"`
	Status                 int                     `json:"status"`
	IPAddress              string                  `json:"ip_address"`
	RequestPath            string                  `json:"request_path"`
	ErrorType              string                  `json:"error_type"`
	ErrorMessage           string                  `json:"error_message"`
	UsageLines             []PublicApiLogUsageLine `json:"usage_lines,omitempty"`
	CreatedAt              time.Time               `json:"created_at"`
}

// ToPublic 把 ApiLog 转为用户侧 DTO。
// 字段语义保持与 ApiLog.MarshalJSON 一致：
//   - 金额：micro_usd → USD float
//   - charged_cost 缺省回退为 cost
//   - model_weight / health_multiplier 缺省回退为 1
//   - requested_model / served_model 缺省回退为 model_name
func (l ApiLog) ToPublic() PublicApiLog {
	chargedCost := l.ChargedCost
	if chargedCost == 0 && l.Cost > 0 {
		chargedCost = l.Cost
	}
	modelWeight := l.ModelWeight
	if modelWeight == 0 {
		modelWeight = 1
	}
	healthMultiplier := l.HealthMultiplier
	if healthMultiplier == 0 {
		healthMultiplier = 1
	}
	requestedModel := l.RequestedModel
	if requestedModel == "" {
		requestedModel = l.ModelName
	}
	servedModel := l.ServedModel
	if servedModel == "" {
		servedModel = l.ModelName
	}
	return PublicApiLog{
		ID:                     l.ID,
		UserID:                 l.UserID,
		TokenName:              l.TokenName,
		ModelName:              l.ModelName,
		RequestedModel:         requestedModel,
		ServedModel:            servedModel,
		PromptTokens:           l.PromptTokens,
		CompletionTokens:       l.CompletionTokens,
		CachedTokens:           l.CachedTokens,
		CacheWriteTokens:       l.CacheWriteTokens,
		CacheWrite5mTokens:     l.CacheWrite5mTokens,
		CacheWrite1hTokens:     l.CacheWrite1hTokens,
		ReasoningTokens:        l.ReasoningTokens,
		Cost:                   MicroToUSD(l.Cost),
		RawCost:                MicroToUSD(l.Cost),
		ChargedCost:            MicroToUSD(chargedCost),
		ModelWeight:            modelWeight,
		HealthMultiplier:       healthMultiplier,
		BillingRulesVersion:    l.BillingRulesVersion,
		PrecheckInputTokens:    l.PrecheckInputTokens,
		PrecheckOutputTokens:   l.PrecheckOutputTokens,
		PrecheckRawCost:        MicroToUSD(l.PrecheckRawCost),
		PrecheckChargedCost:    MicroToUSD(l.PrecheckChargedCost),
		PrecheckQuotaPlanID:    l.PrecheckQuotaPlanID,
		PrecheckQuotaLimit:     MicroToUSD(l.PrecheckQuotaLimit),
		PrecheckQuotaUsed:      MicroToUSD(l.PrecheckQuotaUsed),
		PrecheckQuotaRemaining: MicroToUSD(l.PrecheckQuotaRemaining),
		PrecheckWindowEndAt:    l.PrecheckWindowEndAt,
		BlockReason:            l.BlockReason,
		FallbackUserOptIn:      l.FallbackUserOptIn,
		FallbackReason:         l.FallbackReason,
		Latency:                l.Latency,
		Status:                 l.Status,
		IPAddress:              l.IPAddress,
		RequestPath:            l.RequestPath,
		ErrorType:              l.ErrorType,
		ErrorMessage:           l.ErrorMessage,
		CreatedAt:              l.CreatedAt,
	}
}

// ApiLogsToPublic 批量转换。返回长度与输入一致。
func ApiLogsToPublic(logs []ApiLog) []PublicApiLog {
	out := make([]PublicApiLog, len(logs))
	for i := range logs {
		out[i] = logs[i].ToPublic()
	}
	return out
}

func (l ApiLogUsageLine) ToPublic() PublicApiLogUsageLine {
	out := PublicApiLogUsageLine{
		ID:           l.ID,
		ApiLogID:     l.ApiLogID,
		ModelName:    l.ModelName,
		RequestPath:  l.RequestPath,
		Unit:         l.Unit,
		Direction:    l.Direction,
		Quantity:     l.Quantity,
		UnitPrice:    MicroToUSD(l.UnitPriceMicro),
		Amount:       MicroToUSD(l.AmountMicroUSD),
		CostSource:   l.CostSource,
		Quality:      l.Quality,
		Size:         l.Size,
		Resolution:   l.Resolution,
		AspectRatio:  l.AspectRatio,
		MetadataJSON: l.MetadataJSON,
		CreatedAt:    l.CreatedAt,
	}
	if l.MetadataJSON != "" {
		var metadata map[string]any
		if err := json.Unmarshal([]byte(l.MetadataJSON), &metadata); err == nil {
			out.Metadata = metadata
			out.MetadataJSON = ""
		}
	}
	return out
}

func ApiLogsToPublicWithUsageLines(logs []ApiLog) []PublicApiLog {
	out := ApiLogsToPublic(logs)
	AttachUsageLinesToPublicLogs(out)
	return out
}

func AttachUsageLinesToPublicLogs(logs []PublicApiLog) {
	if len(logs) == 0 || DB == nil || !DB.Migrator().HasTable(&ApiLogUsageLine{}) {
		return
	}
	ids := make([]uint, 0, len(logs))
	indexByID := make(map[uint]int, len(logs))
	for i := range logs {
		if logs[i].ID == 0 {
			continue
		}
		ids = append(ids, logs[i].ID)
		indexByID[logs[i].ID] = i
	}
	if len(ids) == 0 {
		return
	}
	var lines []ApiLogUsageLine
	if err := DB.Where("api_log_id IN ?", ids).Order("api_log_id ASC, id ASC").Find(&lines).Error; err != nil {
		return
	}
	for _, line := range lines {
		if idx, ok := indexByID[line.ApiLogID]; ok {
			logs[idx].UsageLines = append(logs[idx].UsageLines, line.ToPublic())
		}
	}
}
