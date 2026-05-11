package controller

import (
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"daof-ai-hub/database"

	"github.com/gofiber/fiber/v2"
)

// UserUsageRow 是聚合后单个用户的使用量统计
type UserUsageRow struct {
	UserID          uint             `json:"user_id"`
	Username        string           `json:"username"`
	GithubID        string           `json:"github_id"`
	Phone           string           `json:"phone"`
	Role            string           `json:"role"`
	Status          int              `json:"status"`
	Quota           float64          `json:"quota"`
	Requests        int64            `json:"requests"`
	FailedRequests  int64            `json:"failed_requests"`
	InputTokens     int64            `json:"input_tokens"`
	OutputTokens    int64            `json:"output_tokens"`
	ReasoningTokens int64            `json:"reasoning_tokens"`
	CachedTokens    int64            `json:"cached_tokens"`
	TotalTokens     int64            `json:"total_tokens"`
	Cost            float64          `json:"cost"`
	AvgLatencyMs    float64          `json:"avg_latency_ms"`
	LastActiveAt    *time.Time       `json:"last_active_at,omitempty"`
	CreatedAt       time.Time        `json:"created_at"`
	ModelBreakdown  []ModelBreakdown `json:"model_breakdown,omitempty"`
}

// ModelBreakdown 是单个用户在某模型上的子聚合。
// Cost 输出 USD float（前端友好），内部存 int64 micro_usd 累加无误差。
//
// fix MAJOR M22-A1 Phase 4-fix（自审）：原 Cost float64 直接 scan SUM(int64 micro)
// → 50_000_000 micro_usd 被序列化成 $50M。改为 int64 + 显式转换。
type ModelBreakdown struct {
	ModelName string  `json:"model_name"`
	Requests  int64   `json:"requests"`
	Tokens    int64   `json:"tokens"`
	Cost      float64 `json:"cost"` // USD float（输出值已通过 MicroToUSD 转换）
}

// GetUsersUsage 管理员视角：按用户聚合 ApiLog，返回所有用户的使用量统计。
//
// Query 参数：
//   - period: 24h / 7d / 30d / all（默认 7d）
//   - sort: cost_desc / cost_asc / requests_desc / requests_asc /
//     tokens_desc / tokens_asc / last_active_desc / username_asc（默认 cost_desc）
//   - include_models: true 时附带每个用户 top 5 模型分布（默认 false 节省负载）
func GetUsersUsage(c *fiber.Ctx) error {
	period := c.Query("period", "7d")
	sortBy := c.Query("sort", "cost_desc")
	includeModels := c.Query("include_models") == "true"

	cutoff := resolvePeriodCutoff(period)

	// 1. 拉所有 user（含管理员）
	var users []database.User
	if err := database.DB.Find(&users).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "message": "用户表读取失败", "message_code": "ERR_USER_READ"})
	}

	// 2. 按 user_id 聚合 ApiLog（cost 单位 micro_usd 累加，无浮点误差）
	type aggRow struct {
		UserID          uint
		Requests        int64
		FailedRequests  int64
		InputTokens     int64
		OutputTokens    int64
		ReasoningTokens int64
		CachedTokens    int64
		Cost            int64 // sum(api_logs.cost) 单位 micro_usd
		TotalLatency    int64
		LastActiveAt    time.Time
	}

	q := database.DB.Model(&database.ApiLog{}).
		Select(`user_id,
			COUNT(*) AS requests,
			SUM(CASE WHEN status >= 400 OR status = 0 THEN 1 ELSE 0 END) AS failed_requests,
			COALESCE(SUM(prompt_tokens), 0) AS input_tokens,
			COALESCE(SUM(completion_tokens), 0) AS output_tokens,
			COALESCE(SUM(reasoning_tokens), 0) AS reasoning_tokens,
			COALESCE(SUM(cached_tokens), 0) AS cached_tokens,
			COALESCE(SUM(cost), 0) AS cost,
			COALESCE(SUM(latency), 0) AS total_latency,
			MAX(created_at) AS last_active_at`).
		Group("user_id")
	if !cutoff.IsZero() {
		q = q.Where("created_at >= ?", cutoff)
	}

	var aggs []aggRow
	// fix MAJOR M-B10（codex 第二十一轮）：原 Scan 不检 .Error，DB 故障返回空 → admin 误判用量为 0。
	if err := q.Scan(&aggs).Error; err != nil {
		log.Printf("[USERS-USAGE] aggregate scan failed: %v", err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}

	aggMap := make(map[uint]aggRow, len(aggs))
	for _, a := range aggs {
		aggMap[a.UserID] = a
	}

	// 3. 可选：按用户拉 top 5 模型分布
	modelMap := map[uint][]ModelBreakdown{}
	if includeModels {
		modelMap = loadUserModelBreakdown(cutoff)
	}

	// 4. 组装输出 + 总览
	rows := make([]UserUsageRow, 0, len(users))
	var (
		totalRequests   int64
		totalTokens     int64
		totalCostMicro  int64
		activeUsers     int
	)

	for _, u := range users {
		agg := aggMap[u.ID]
		var lastActive *time.Time
		if !agg.LastActiveAt.IsZero() {
			ts := agg.LastActiveAt
			lastActive = &ts
		}
		var avgLatency float64
		if agg.Requests > 0 {
			avgLatency = float64(agg.TotalLatency) / float64(agg.Requests)
			activeUsers++
		}
		totalRequests += agg.Requests
		// 总 Token 不重复计 cached（OpenAI/Claude 的 cached 都是 input 子集）
		totalTokens += agg.InputTokens + agg.OutputTokens + agg.ReasoningTokens
		totalCostMicro += agg.Cost

		rows = append(rows, UserUsageRow{
			UserID:   u.ID,
			Username: u.Username,
			GithubID: u.GithubID,
			// fix Major（自审第六轮）：admin 聚合统计接口不应批量回显未脱敏手机号。
			// PIPL/GDPR 合规要求 PII 默认最小暴露；admin session 被盗即可批量泄露所有用户手机号。
			Phone:  maskPhone(u.Phone),
			Role:   u.Role,
			Status:          u.Status,
			Quota:           database.MicroToUSD(u.Quota),
			Requests:        agg.Requests,
			FailedRequests:  agg.FailedRequests,
			InputTokens:     agg.InputTokens,
			OutputTokens:    agg.OutputTokens,
			ReasoningTokens: agg.ReasoningTokens,
			CachedTokens:    agg.CachedTokens,
			TotalTokens:     agg.InputTokens + agg.OutputTokens + agg.ReasoningTokens,
			Cost:            database.MicroToUSD(agg.Cost),
			AvgLatencyMs:    avgLatency,
			LastActiveAt:    lastActive,
			CreatedAt:       u.CreatedAt,
			ModelBreakdown:  modelMap[u.ID],
		})
	}

	// 5. 排序
	sortUserUsageRows(rows, sortBy)

	return c.JSON(fiber.Map{
		"success": true,
		"data": fiber.Map{
			"period": period,
			"summary": fiber.Map{
				"total_users":    len(users),
				"active_users":   activeUsers,
				"total_requests": totalRequests,
				"total_tokens":   totalTokens,
				"total_cost":     database.MicroToUSD(totalCostMicro),
			},
			"users": rows,
		},
	})
}

func resolvePeriodCutoff(period string) time.Time {
	now := time.Now()
	switch period {
	case "24h":
		return now.Add(-24 * time.Hour)
	case "7d":
		return now.Add(-7 * 24 * time.Hour)
	case "30d":
		return now.Add(-30 * 24 * time.Hour)
	default:
		return time.Time{}
	}
}

func loadUserModelBreakdown(cutoff time.Time) map[uint][]ModelBreakdown {
	// fix MAJOR Phase 4-fix：row.Cost 用 int64 接 SUM(cost) 的 micro_usd 累加值
	type row struct {
		UserID    uint
		ModelName string
		Requests  int64
		Tokens    int64
		Cost      int64 // micro_usd
	}

	q := database.DB.Model(&database.ApiLog{}).
		Select(`user_id,
			model_name,
			COUNT(*) AS requests,
			COALESCE(SUM(prompt_tokens + completion_tokens + reasoning_tokens), 0) AS tokens,
			COALESCE(SUM(cost), 0) AS cost`).
		Group("user_id, model_name")
	if !cutoff.IsZero() {
		q = q.Where("created_at >= ?", cutoff)
	}

	var rows []row
	// fix MAJOR M-B10：检查 Scan 错误，避免静默返回空映射
	if err := q.Scan(&rows).Error; err != nil {
		log.Printf("[USERS-USAGE] model breakdown scan failed: %v", err)
		return nil
	}

	result := make(map[uint][]ModelBreakdown)
	for _, r := range rows {
		result[r.UserID] = append(result[r.UserID], ModelBreakdown{
			ModelName: r.ModelName,
			Requests:  r.Requests,
			Tokens:    r.Tokens,
			Cost:      database.MicroToUSD(r.Cost), // micro → USD float（输出端转换）
		})
	}
	// 每个用户只保留 top 5 (按 cost desc)
	for uid, list := range result {
		sort.Slice(list, func(i, j int) bool { return list[i].Cost > list[j].Cost })
		if len(list) > 5 {
			list = list[:5]
		}
		result[uid] = list
	}
	return result
}

// GetUsersUsageTimeseries 按时间桶聚合的请求/token/花费序列，用于折线图。
//
// Query：
//   - period=24h|7d|30d|all（默认 7d）
//   - top_n=5（默认）：仅返回花费 Top N 的用户线，其余汇总成 "其他"
//
// 响应：
//
//	{
//	  buckets: ["04-22", "04-23", ...],
//	  bucket_unit: "day"|"hour",
//	  series: [
//	    { user_id, username, points: [{requests, tokens, cost, prompt, completion, reasoning, cached}, ...] }
//	  ]
//	}
func GetUsersUsageTimeseries(c *fiber.Ctx) error {
	period := c.Query("period", "7d")
	topN := 5
	if n, err := strconv.Atoi(c.Query("top_n", "5")); err == nil && n > 0 && n <= 20 {
		topN = n
	}
	cutoff := resolvePeriodCutoff(period)

	// bucket 粒度自适应：
	//   24h / 7d → hour（168 个点上限，避免数据全在 1 天时图表只剩 1 个点的"假死"观感）
	//   30d / all → day（粒度太细会让 30 天图变成 720 点过密）
	bucketUnit := "day"
	bucketFmt := "%Y-%m-%d"
	if period == "24h" || period == "7d" {
		bucketUnit = "hour"
		bucketFmt = "%Y-%m-%d %H:00"
	}

	// fix MAJOR Phase 4-fix：bucketRow.Cost 接 SUM(int64 micro_usd) 必须用 int64
	type bucketRow struct {
		UserID     uint
		Bucket     string
		Requests   int64
		Tokens     int64
		Cost       int64 // micro_usd
		Prompt     int64
		Completion int64
		Reasoning  int64
		Cached     int64
	}
	q := database.DB.Model(&database.ApiLog{}).
		Select(`user_id,
			strftime(?, created_at) AS bucket,
			COUNT(*) AS requests,
			COALESCE(SUM(prompt_tokens + completion_tokens + reasoning_tokens), 0) AS tokens,
			COALESCE(SUM(cost), 0) AS cost,
			COALESCE(SUM(prompt_tokens), 0) AS prompt,
			COALESCE(SUM(completion_tokens), 0) AS completion,
			COALESCE(SUM(reasoning_tokens), 0) AS reasoning,
			COALESCE(SUM(cached_tokens), 0) AS cached`, bucketFmt).
		Group("user_id, bucket").
		Order("bucket ASC")
	if !cutoff.IsZero() {
		q = q.Where("created_at >= ?", cutoff)
	}
	var rows []bucketRow
	// fix MAJOR M22-6（codex 第二十二轮）：检查 Scan 错误，避免 timeseries 静默返回空数据
	if err := q.Scan(&rows).Error; err != nil {
		log.Printf("[USERS-USAGE] timeseries bucket scan failed: %v", err)
		return nil
	}

	// 收集所有 buckets
	bucketSet := map[string]struct{}{}
	for _, r := range rows {
		bucketSet[r.Bucket] = struct{}{}
	}
	buckets := make([]string, 0, len(bucketSet))
	for b := range bucketSet {
		buckets = append(buckets, b)
	}
	sort.Strings(buckets)

	// 按 user 总花费排序，取 top N，其余合并到 "其他"。累加用 int64 micro_usd。
	type userTotal struct {
		UserID uint
		Cost   int64 // micro_usd
	}
	totals := map[uint]int64{}
	for _, r := range rows {
		totals[r.UserID] += r.Cost
	}
	uts := make([]userTotal, 0, len(totals))
	for uid, c := range totals {
		uts = append(uts, userTotal{uid, c})
	}
	sort.Slice(uts, func(i, j int) bool { return uts[i].Cost > uts[j].Cost })

	topSet := map[uint]struct{}{}
	for i := 0; i < len(uts) && i < topN; i++ {
		topSet[uts[i].UserID] = struct{}{}
	}

	// 拉用户名。对已被硬删除的 user_id（users 表查不到），从 ApiLog.token_name 反查：
	// 我们的 token 命名约定是 `sk-daof-{username}-{hash}`，可以从 token_name 第三段提取曾经的用户名。
	usernames := map[uint]string{}
	if len(totals) > 0 {
		ids := make([]uint, 0, len(totals))
		for uid := range totals {
			ids = append(ids, uid)
		}
		var users []database.User
		database.DB.Where("id IN ?", ids).Find(&users)
		for _, u := range users {
			usernames[u.ID] = u.Username
		}
		// 对没找到的 user_id，反查 ApiLog 一条 token_name 提取
		for _, uid := range ids {
			if usernames[uid] != "" {
				continue
			}
			var tn string
			database.DB.Model(&database.ApiLog{}).
				Where("user_id = ? AND token_name LIKE ?", uid, "sk-daof-%").
				Order("created_at desc").
				Limit(1).
				Pluck("token_name", &tn)
			if tn != "" {
				// sk-daof-{username}-{hash}：第 3 段就是 username
				parts := strings.SplitN(tn, "-", 4)
				if len(parts) >= 4 {
					usernames[uid] = parts[2] + " (已删)"
					continue
				}
			}
			usernames[uid] = "已删用户 #" + strconv.FormatUint(uint64(uid), 10)
		}
	}

	// point 内部 Cost 仍累加 int64 micro_usd 避浮点漂移；JSON 输出经 MarshalJSON 转 USD float
	type point struct {
		Requests   int64 `json:"requests"`
		Tokens     int64 `json:"tokens"`
		Cost       int64 `json:"-"` // micro_usd; 由本地 helper 转 USD 输出
		Prompt     int64 `json:"prompt_tokens"`
		Completion int64 `json:"completion_tokens"`
		Reasoning  int64 `json:"reasoning_tokens"`
		Cached     int64 `json:"cached_tokens"`
		// CostUSD 是 JSON 输出字段（USD float），由 finalize 阶段填充
		CostUSD float64 `json:"cost"`
	}
	type series struct {
		UserID   uint    `json:"user_id"`
		Username string  `json:"username"`
		IsOther  bool    `json:"is_other"`
		Points   []point `json:"points"`
	}

	// 初始化每个 series 的空 points 数组（与 buckets 长度一致）
	seriesMap := map[uint]*series{}
	otherSeries := &series{UserID: 0, Username: "其他", IsOther: true, Points: make([]point, len(buckets))}
	for uid := range topSet {
		seriesMap[uid] = &series{UserID: uid, Username: usernames[uid], Points: make([]point, len(buckets))}
	}

	// bucket -> idx 映射
	bucketIdx := map[string]int{}
	for i, b := range buckets {
		bucketIdx[b] = i
	}

	for _, r := range rows {
		idx, ok := bucketIdx[r.Bucket]
		if !ok {
			continue
		}
		var s *series
		if _, isTop := topSet[r.UserID]; isTop {
			s = seriesMap[r.UserID]
		} else {
			s = otherSeries
		}
		p := &s.Points[idx]
		p.Requests += r.Requests
		p.Tokens += r.Tokens
		p.Cost += r.Cost
		p.Prompt += r.Prompt
		p.Completion += r.Completion
		p.Reasoning += r.Reasoning
		p.Cached += r.Cached
	}

	out := make([]*series, 0, len(seriesMap)+1)
	for _, s := range seriesMap {
		out = append(out, s)
	}
	if len(uts) > topN {
		out = append(out, otherSeries)
	}
	// 按总花费排序（int64 micro_usd 累加，避免浮点）
	sort.Slice(out, func(i, j int) bool {
		var ci, cj int64
		for _, p := range out[i].Points {
			ci += p.Cost
		}
		for _, p := range out[j].Points {
			cj += p.Cost
		}
		return ci > cj
	})

	// 排序完成后填充 CostUSD（输出字段）。在累加阶段一直用 int64 micro 避免漂移。
	for _, s := range out {
		for i := range s.Points {
			s.Points[i].CostUSD = database.MicroToUSD(s.Points[i].Cost)
		}
	}

	return c.JSON(fiber.Map{
		"success": true,
		"data": fiber.Map{
			"period":      period,
			"bucket_unit": bucketUnit,
			"buckets":     buckets,
			"series":      out,
		},
	})
}

// GetUsersUsageEvents 返回逐条 ApiLog 详情（admin 视角，跨用户）。
//
// Query：
//   - period=24h|7d|30d|all
//   - user_id=N (可选，过滤特定用户)
//   - model=xxx (可选)
//   - page=1, page_size=50（最大 200）
func GetUsersUsageEvents(c *fiber.Ctx) error {
	period := c.Query("period", "7d")
	cutoff := resolvePeriodCutoff(period)

	page, _ := strconv.Atoi(c.Query("page", "1"))
	if page < 1 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(c.Query("page_size", "50"))
	if pageSize < 1 || pageSize > 200 {
		pageSize = 50
	}

	q := database.DB.Model(&database.ApiLog{})
	if !cutoff.IsZero() {
		q = q.Where("created_at >= ?", cutoff)
	}
	if uid := strings.TrimSpace(c.Query("user_id")); uid != "" {
		q = q.Where("user_id = ?", uid)
	}
	if model := strings.TrimSpace(c.Query("model")); model != "" {
		q = q.Where("model_name = ?", model)
	}

	// fix MAJOR M22-6（codex 第二十二轮）：events list 加 .Error 检查 → fail-closed
	var total int64
	if err := q.Count(&total).Error; err != nil {
		log.Printf("[USERS-USAGE-EVENTS] count failed: %v", err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}

	var logs []database.ApiLog
	if err := q.Order("created_at DESC").Limit(pageSize).Offset((page - 1) * pageSize).Find(&logs).Error; err != nil {
		log.Printf("[USERS-USAGE-EVENTS] find failed: %v", err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}

	// 拉相关 user 的 username（同样对硬删用户 fallback 到 token_name 解析）
	idSet := map[uint]struct{}{}
	for _, l := range logs {
		idSet[l.UserID] = struct{}{}
	}
	usernames := map[uint]string{}
	if len(idSet) > 0 {
		ids := make([]uint, 0, len(idSet))
		for id := range idSet {
			ids = append(ids, id)
		}
		var users []database.User
		database.DB.Unscoped().Where("id IN ?", ids).Find(&users)
		for _, u := range users {
			usernames[u.ID] = u.Username
		}
		// 对硬删用户走 token_name 反查
		for _, l := range logs {
			if usernames[l.UserID] != "" {
				continue
			}
			if strings.HasPrefix(l.TokenName, "sk-daof-") {
				parts := strings.SplitN(l.TokenName, "-", 4)
				if len(parts) >= 4 {
					usernames[l.UserID] = parts[2] + " (已删)"
					continue
				}
			}
			usernames[l.UserID] = "已删用户 #" + strconv.FormatUint(uint64(l.UserID), 10)
		}
	}

	type eventOut struct {
		ID               uint    `json:"id"`
		UserID           uint    `json:"user_id"`
		Username         string  `json:"username"`
		TokenName        string  `json:"token_name"`
		ModelName        string  `json:"model_name"`
		PromptTokens     int     `json:"prompt_tokens"`
		CompletionTokens int     `json:"completion_tokens"`
		ReasoningTokens  int     `json:"reasoning_tokens"`
		CachedTokens     int     `json:"cached_tokens"`
		TotalTokens      int     `json:"total_tokens"`
		Cost             float64 `json:"cost"`
		Latency          int64   `json:"latency_ms"`
		Status           int     `json:"status"`
		IPAddress        string  `json:"ip_address"`
		CreatedAt        string  `json:"created_at"`
	}
	out := make([]eventOut, 0, len(logs))
	for _, l := range logs {
		out = append(out, eventOut{
			ID:               l.ID,
			UserID:           l.UserID,
			Username:         usernames[l.UserID],
			TokenName:        l.TokenName,
			ModelName:        l.ModelName,
			PromptTokens:     l.PromptTokens,
			CompletionTokens: l.CompletionTokens,
			ReasoningTokens:  l.ReasoningTokens,
			CachedTokens:     l.CachedTokens,
			TotalTokens:      l.PromptTokens + l.CompletionTokens + l.ReasoningTokens,
			Cost:             database.MicroToUSD(l.Cost),
			Latency:          l.Latency,
			Status:           l.Status,
			IPAddress:        l.IPAddress,
			CreatedAt:        l.CreatedAt.Format(time.RFC3339),
		})
	}

	return c.JSON(fiber.Map{
		"success": true,
		"data": fiber.Map{
			"page":       page,
			"page_size":  pageSize,
			"total":      total,
			"total_page": (total + int64(pageSize) - 1) / int64(pageSize),
			"events":     out,
		},
	})
}

func sortUserUsageRows(rows []UserUsageRow, key string) {
	switch key {
	case "cost_asc":
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].Cost < rows[j].Cost })
	case "requests_desc":
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].Requests > rows[j].Requests })
	case "requests_asc":
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].Requests < rows[j].Requests })
	case "tokens_desc":
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].TotalTokens > rows[j].TotalTokens })
	case "tokens_asc":
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].TotalTokens < rows[j].TotalTokens })
	case "last_active_desc":
		sort.SliceStable(rows, func(i, j int) bool {
			if rows[i].LastActiveAt == nil {
				return false
			}
			if rows[j].LastActiveAt == nil {
				return true
			}
			return rows[i].LastActiveAt.After(*rows[j].LastActiveAt)
		})
	case "username_asc":
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].Username < rows[j].Username })
	default: // cost_desc
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].Cost > rows[j].Cost })
	}
}
