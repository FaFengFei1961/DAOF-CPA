package controller

import (
	"daof-ai-hub/database"
	"encoding/json"
	"log"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"
)

// StatDataPoint Cost 内部存 int64 micro_usd（SUM(cost) DB 列单位），
// JSON 输出经 MarshalJSON 转 USD float（前端友好）。
//
// fix MAJOR M22-A1 Phase 4-fix（自审）：原实现 Cost float64 直接 scan SUM(cost) 的 int64 micro，
// 导致 50_000_000 micro_usd 被当成 $50M 输出给前端，金额放大 1e6 倍。
type StatDataPoint struct {
	Date             string `json:"date"`
	ModelName        string `json:"model_name"`
	Reqs             int    `json:"reqs"`
	SuccessReqs      int    `json:"success_reqs"`
	FailedReqs       int    `json:"failed_reqs"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	Tokens           int    `json:"tokens"`
	CachedTokens     int    `json:"cached_tokens"`
	CacheWriteTokens int    `json:"cache_write_tokens"`
	ReasoningTokens  int    `json:"reasoning_tokens"`
	Cost             int64  `json:"-"` // micro_usd; JSON 输出由 MarshalJSON 转 USD float
	LatencyTotal     int64  `json:"-"`
}

func (p StatDataPoint) MarshalJSON() ([]byte, error) {
	type alias StatDataPoint
	return json.Marshal(&struct {
		*alias
		Cost float64 `json:"cost"`
	}{alias: (*alias)(&p), Cost: database.MicroToUSD(p.Cost)})
}

type TokenStatRow struct {
	TokenName string `json:"token_name"`
	Reqs      int    `json:"reqs"`
	Tokens    int    `json:"tokens"`
	Cost      int64  `json:"-"` // micro_usd
}

func (r TokenStatRow) MarshalJSON() ([]byte, error) {
	type alias TokenStatRow
	return json.Marshal(&struct {
		*alias
		Cost float64 `json:"cost"`
	}{alias: (*alias)(&r), Cost: database.MicroToUSD(r.Cost)})
}

type ModelStatRow struct {
	ModelName string `json:"model_name"`
	Reqs      int    `json:"reqs"`
	Tokens    int    `json:"tokens"`
	Cost      int64  `json:"-"` // micro_usd
}

func (r ModelStatRow) MarshalJSON() ([]byte, error) {
	type alias ModelStatRow
	return json.Marshal(&struct {
		*alias
		Cost float64 `json:"cost"`
	}{alias: (*alias)(&r), Cost: database.MicroToUSD(r.Cost)})
}

func GetStats(c *fiber.Ctx) error {
	user, err := getCurrentUser(c)
	if err != nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message": "Unauthorized"})
	}

	period := c.Query("period", "7d") // 24h, 7d, 30d

	var startDate time.Time
	var dateFormat string

	now := time.Now()
	switch period {
	case "24h":
		startDate = now.Add(-24 * time.Hour)
		dateFormat = "%Y-%m-%d %H:00"
	case "7d":
		startDate = now.AddDate(0, 0, -7)
		dateFormat = "%Y-%m-%d %H:00"
	case "30d":
		startDate = now.AddDate(0, 0, -30)
		dateFormat = "%Y-%m-%d"
	default:
		startDate = now.AddDate(0, 0, -7)
		dateFormat = "%Y-%m-%d"
	}

	// fix MAJOR M-B10（codex 第二十一轮）：4 个聚合查询原本不检 .Error，DB 故障会返回假空数据
	// 让用户/admin 误判"没有用量"。改为 fail-closed：任一查询失败立即 500。
	// 1. Chart data: grouped by (date, model_name)
	var chartData []StatDataPoint
	if err := database.DB.Model(&database.ApiLog{}).
		Select(`strftime(?, created_at) as date,
			model_name,
			COUNT(id) as reqs,
			SUM(CASE WHEN status >= 200 AND status < 300 THEN 1 ELSE 0 END) as success_reqs,
			SUM(CASE WHEN status < 200 OR status >= 300 THEN 1 ELSE 0 END) as failed_reqs,
			COALESCE(SUM(prompt_tokens), 0) as prompt_tokens,
			COALESCE(SUM(completion_tokens), 0) as completion_tokens,
			COALESCE(SUM(prompt_tokens + completion_tokens), 0) as tokens,
			COALESCE(SUM(cached_tokens), 0) as cached_tokens,
			COALESCE(SUM(cache_write_tokens), 0) as cache_write_tokens,
			COALESCE(SUM(reasoning_tokens), 0) as reasoning_tokens,
			COALESCE(SUM(cost), 0) as cost,
			COALESCE(SUM(latency), 0) as latency_total`, dateFormat).
		Where("user_id = ? AND created_at >= ?", user.ID, startDate).
		Group("date, model_name").
		Order("date ASC, model_name ASC").
		Scan(&chartData).Error; err != nil {
		log.Printf("[STATS] chartData query failed user=%d: %v", user.ID, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}

	// 2. Token stats: grouped by token_name (令牌来源)
	var tokenStats []TokenStatRow
	if err := database.DB.Model(&database.ApiLog{}).
		Select("token_name, COUNT(id) as reqs, SUM(prompt_tokens + completion_tokens) as tokens, SUM(cost) as cost").
		Where("user_id = ? AND created_at >= ?", user.ID, startDate).
		Group("token_name").
		Order("reqs DESC").
		Scan(&tokenStats).Error; err != nil {
		log.Printf("[STATS] tokenStats query failed user=%d: %v", user.ID, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}

	// 3. Model stats: grouped by model_name
	var modelStats []ModelStatRow
	if err := database.DB.Model(&database.ApiLog{}).
		Select("model_name, COUNT(id) as reqs, SUM(prompt_tokens + completion_tokens) as tokens, SUM(cost) as cost").
		Where("user_id = ? AND created_at >= ?", user.ID, startDate).
		Group("model_name").
		Order("reqs DESC").
		Scan(&modelStats).Error; err != nil {
		log.Printf("[STATS] modelStats query failed user=%d: %v", user.ID, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}

	// 4. Recent logs (paginated)
	page, _ := strconv.Atoi(c.Query("page", "1"))
	if page < 1 {
		page = 1
	}
	limit := 20

	var recentLogs []database.ApiLog
	var logsTotal int64
	logsQuery := database.DB.Model(&database.ApiLog{}).Where("user_id = ? AND created_at >= ?", user.ID, startDate)
	if err := logsQuery.Count(&logsTotal).Error; err != nil {
		log.Printf("[STATS] logs count failed user=%d: %v", user.ID, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}
	if err := logsQuery.Order("created_at DESC").Offset((page - 1) * limit).Limit(limit).Find(&recentLogs).Error; err != nil {
		log.Printf("[STATS] recentLogs query failed user=%d: %v", user.ID, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}

	totalReqs := 0
	successReqs := 0
	failedReqs := 0
	totalTokens := 0
	totalCached := 0
	totalCacheWrite := 0
	totalReasoning := 0
	var totalCostMicro int64 // 累加 int64 micro_usd 避免浮点误差
	var totalLatencyMs int64

	for _, p := range chartData {
		totalReqs += p.Reqs
		successReqs += p.SuccessReqs
		failedReqs += p.FailedReqs
		totalTokens += p.Tokens
		totalCached += p.CachedTokens
		totalCacheWrite += p.CacheWriteTokens
		totalReasoning += p.ReasoningTokens
		totalCostMicro += p.Cost
		totalLatencyMs += p.LatencyTotal
	}
	avgLatencySeconds := 0.0
	if totalReqs > 0 {
		avgLatencySeconds = float64(totalLatencyMs) / float64(totalReqs) / 1000
	}

	// RPM/TPM: rolling 30-minute window (matching CPAMC)
	windowMinutes := 30.0
	windowStart := now.Add(-30 * time.Minute)

	// fix MAJOR M22-6（codex 第二十二轮）：rpm/tpm 窗口聚合也加 .Error 检查。fail-closed。
	var windowReqs int64
	var windowTokens int64
	if err := database.DB.Model(&database.ApiLog{}).
		Where("user_id = ? AND created_at >= ?", user.ID, windowStart).
		Select("COUNT(id)").Scan(&windowReqs).Error; err != nil {
		log.Printf("[STATS] window reqs scan failed user=%d: %v", user.ID, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}
	if err := database.DB.Model(&database.ApiLog{}).
		Where("user_id = ? AND created_at >= ?", user.ID, windowStart).
		Select("COALESCE(SUM(prompt_tokens + completion_tokens), 0)").Scan(&windowTokens).Error; err != nil {
		log.Printf("[STATS] window tokens scan failed user=%d: %v", user.ID, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}

	rpm := float64(windowReqs) / windowMinutes
	tpm := float64(windowTokens) / windowMinutes

	return c.JSON(fiber.Map{
		"success": true,
		"data": map[string]interface{}{
			"summary": map[string]interface{}{
				"totalReqs":       totalReqs,
				"successReqs":     successReqs,
				"failedReqs":      failedReqs,
				"totalTokens":     totalTokens,
				"totalCached":     totalCached,
				"totalCacheWrite": totalCacheWrite,
				"totalReasoning":  totalReasoning,
				"totalCost":       database.MicroToUSD(totalCostMicro),
				"avgLatency":      avgLatencySeconds,
				"rpm":             rpm,
				"tpm":             tpm,
			},
			"chart_data":  chartData,
			"token_stats": tokenStats,
			"model_stats": modelStats,
			"recent_logs": map[string]interface{}{
				// fix CRITICAL（多模型审计第二十五轮）：用户侧 recent_logs 也走 PublicApiLog
				// 白名单，不能直接序列化 []database.ApiLog（避免 platform_cost_estimate /
				// upstream_* 字段泄漏到普通用户）。
				"logs":  database.ApiLogsToPublic(recentLogs),
				"total": logsTotal,
				"page":  page,
				"limit": limit,
			},
		},
	})
}
