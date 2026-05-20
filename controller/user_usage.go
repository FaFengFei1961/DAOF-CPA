package controller

import (
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"daof-cpa/database"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// UserUsageRow 是聚合后单个用户的使用量统计
type UserUsageRow struct {
	UserID uint   `json:"user_id"`
	Username string `json:"username"`
	// Phase H-3b（2026-05-20）：原 GithubID 单字段已删；改为活跃 OAuth 绑定列表，
	// 让 admin UI 能展示"该用户已绑 GitHub / Google / ..."徽章。
	OAuthIdentities  []AdminOAuthIdentitySummary `json:"oauth_identities"`
	Phone            string                      `json:"phone"`
	Role             string           `json:"role"`
	Status           int              `json:"status"`
	Quota            float64          `json:"quota"`
	Requests         int64            `json:"requests"`
	FailedRequests   int64            `json:"failed_requests"`
	InputTokens      int64            `json:"input_tokens"`
	OutputTokens     int64            `json:"output_tokens"`
	ReasoningTokens  int64            `json:"reasoning_tokens"`
	CachedTokens     int64            `json:"cached_tokens"`
	CacheWriteTokens int64            `json:"cache_write_tokens"`
	TotalTokens      int64            `json:"total_tokens"`
	Cost             float64          `json:"cost"`
	RawCost          float64          `json:"raw_cost"`
	ChargedCost      float64          `json:"charged_cost"`
	TotalCost        float64          `json:"total_cost"`
	TotalChargedCost float64          `json:"total_charged_cost"`
	AvgLatencyMs     float64          `json:"avg_latency_ms"`
	LastActiveAt     *time.Time       `json:"last_active_at,omitempty"`
	CreatedAt        time.Time        `json:"created_at"`
	ModelBreakdown   []ModelBreakdown `json:"model_breakdown,omitempty"`
}

// ModelBreakdown 是单个用户在某模型上的子聚合。
// Cost 输出 USD float（前端友好），内部存 int64 micro_usd 累加无误差。
//
// fix MAJOR M22-A1 Phase 4-fix（自审）：原 Cost float64 直接 scan SUM(int64 micro)
// → 50_000_000 micro_usd 被序列化成 $50M。改为 int64 + 显式转换。
type ModelBreakdown struct {
	ModelName   string  `json:"model_name"`
	Requests    int64   `json:"requests"`
	Tokens      int64   `json:"tokens"`
	Cost        float64 `json:"cost"` // USD float（输出值已通过 MicroToUSD 转换）
	RawCost     float64 `json:"raw_cost"`
	ChargedCost float64 `json:"charged_cost"`
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
		UserID           uint
		Requests         int64
		FailedRequests   int64
		InputTokens      int64
		OutputTokens     int64
		ReasoningTokens  int64
		CachedTokens     int64
		CacheWriteTokens int64
		Cost             int64 // sum(api_logs.cost) 单位 micro_usd
		ChargedCost      int64 // sum(api_logs charged_cost fallback cost) 单位 micro_usd
		TotalLatency     int64
		LastActiveAt     string
	}

	q := database.DB.Model(&database.ApiLog{}).
		Select(`user_id,
			COUNT(*) AS requests,
			SUM(CASE WHEN status < 200 OR status >= 300 THEN 1 ELSE 0 END) AS failed_requests,
			COALESCE(SUM(prompt_tokens), 0) AS input_tokens,
			COALESCE(SUM(completion_tokens), 0) AS output_tokens,
			COALESCE(SUM(reasoning_tokens), 0) AS reasoning_tokens,
			COALESCE(SUM(cached_tokens), 0) AS cached_tokens,
			COALESCE(SUM(cache_write_tokens), 0) AS cache_write_tokens,
			COALESCE(SUM(cost), 0) AS cost,
			COALESCE(SUM(CASE WHEN charged_cost > 0 THEN charged_cost ELSE cost END), 0) AS charged_cost,
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

	// 3b. 批量加载活跃 OAuth 绑定（Phase H-3b）
	identitiesByUser := loadActiveOAuthIdentitiesForUsers(users)

	// 4. 组装输出 + 总览
	rows := make([]UserUsageRow, 0, len(users))
	var (
		totalRequests         int64
		totalTokens           int64
		totalCostMicro        int64
		totalChargedCostMicro int64
		activeUsers           int
	)

	for _, u := range users {
		agg := aggMap[u.ID]
		var lastActive *time.Time
		if ts, ok := parseUsageTime(agg.LastActiveAt); ok {
			lastActive = &ts
		}
		var avgLatency float64
		if agg.Requests > 0 {
			avgLatency = float64(agg.TotalLatency) / float64(agg.Requests)
			activeUsers++
		}
		totalRequests += agg.Requests
		// 总 Token 不重复计 cached/cache_write/reasoning；它们分别是 input/output 子集。
		totalTokens += agg.InputTokens + agg.OutputTokens
		totalCostMicro += agg.Cost
		totalChargedCostMicro += effectiveAggregateChargedCost(agg.ChargedCost, agg.Cost)
		rowRawCost := database.MicroToUSD(agg.Cost)
		rowChargedCost := database.MicroToUSD(effectiveAggregateChargedCost(agg.ChargedCost, agg.Cost))

		rows = append(rows, UserUsageRow{
			UserID:          u.ID,
			Username:        u.Username,
			OAuthIdentities: identitiesByUser[u.ID],
			// fix Major（自审第六轮）：admin 聚合统计接口不应批量回显未脱敏手机号。
			// PIPL/GDPR 合规要求 PII 默认最小暴露；admin session 被盗即可批量泄露所有用户手机号。
			Phone:            maskPhone(u.Phone),
			Role:             u.Role,
			Status:           u.Status,
			Quota:            database.MicroToUSD(u.Quota),
			Requests:         agg.Requests,
			FailedRequests:   agg.FailedRequests,
			InputTokens:      agg.InputTokens,
			OutputTokens:     agg.OutputTokens,
			ReasoningTokens:  agg.ReasoningTokens,
			CachedTokens:     agg.CachedTokens,
			CacheWriteTokens: agg.CacheWriteTokens,
			TotalTokens:      agg.InputTokens + agg.OutputTokens,
			Cost:             rowRawCost,
			RawCost:          rowRawCost,
			ChargedCost:      rowChargedCost,
			TotalCost:        rowRawCost,
			TotalChargedCost: rowChargedCost,
			AvgLatencyMs:     avgLatency,
			LastActiveAt:     lastActive,
			CreatedAt:        u.CreatedAt,
			ModelBreakdown:   modelMap[u.ID],
		})
	}

	// 5. 排序
	sortUserUsageRows(rows, sortBy)

	return c.JSON(fiber.Map{
		"success": true,
		"data": fiber.Map{
			"period": period,
			"summary": fiber.Map{
				"total_users":        len(users),
				"active_users":       activeUsers,
				"total_requests":     totalRequests,
				"total_tokens":       totalTokens,
				"total_cost":         database.MicroToUSD(totalCostMicro),
				"total_charged_cost": database.MicroToUSD(totalChargedCostMicro),
			},
			"users": rows,
		},
	})
}

func parseUsageTime(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	formats := []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05-07:00",
		"2006-01-02 15:04:05Z07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
	}
	for _, f := range formats {
		if ts, err := time.Parse(f, raw); err == nil {
			return ts, true
		}
	}
	log.Printf("[USERS-USAGE] cannot parse last_active_at=%q", raw)
	return time.Time{}, false
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
	case "all":
		// fix C-M3 (2026-05-19)：原 default 返回 zero time → 触发无界 SUM 全表扫描
		// 独占 SQLite write-lock。强制最长回溯 365 天作为"all"的安全替代，避免单
		// admin 报表请求阻塞所有计费/日志写。需要更长时段查询请用归档脚本。
		return now.Add(-365 * 24 * time.Hour)
	default:
		// 未知 period 安全回退到 7 天而非"无限制"
		return now.Add(-7 * 24 * time.Hour)
	}
}

func loadUserModelBreakdown(cutoff time.Time) map[uint][]ModelBreakdown {
	// fix MAJOR Phase 4-fix：row.Cost 用 int64 接 SUM(cost) 的 micro_usd 累加值
	type row struct {
		UserID      uint
		ModelName   string
		Requests    int64
		Tokens      int64
		Cost        int64 // micro_usd
		ChargedCost int64 // micro_usd
	}

	q := database.DB.Model(&database.ApiLog{}).
		Select(`user_id,
			model_name,
			COUNT(*) AS requests,
			COALESCE(SUM(prompt_tokens + completion_tokens), 0) AS tokens,
			COALESCE(SUM(cost), 0) AS cost,
			COALESCE(SUM(CASE WHEN charged_cost > 0 THEN charged_cost ELSE cost END), 0) AS charged_cost`).
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
		rawCost := database.MicroToUSD(r.Cost)
		chargedCost := database.MicroToUSD(effectiveAggregateChargedCost(r.ChargedCost, r.Cost))
		result[r.UserID] = append(result[r.UserID], ModelBreakdown{
			ModelName:   r.ModelName,
			Requests:    r.Requests,
			Tokens:      r.Tokens,
			Cost:        rawCost,
			RawCost:     rawCost,
			ChargedCost: chargedCost,
		})
	}
	// 每个用户只保留 top 5 (按 cost desc)
	for uid, list := range result {
		sort.Slice(list, func(i, j int) bool {
			return usageRowCostForSort(list[i].ChargedCost, list[i].Cost) > usageRowCostForSort(list[j].ChargedCost, list[j].Cost)
		})
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
		UserID      uint
		Bucket      string
		Requests    int64
		Tokens      int64
		Cost        int64 // micro_usd
		ChargedCost int64 // micro_usd
		Prompt      int64
		Completion  int64
		Reasoning   int64
		Cached      int64
		CacheWrite  int64
	}
	q := database.DB.Model(&database.ApiLog{}).
		Select(`user_id,
			strftime(?, created_at) AS bucket,
			COUNT(*) AS requests,
			COALESCE(SUM(prompt_tokens + completion_tokens), 0) AS tokens,
			COALESCE(SUM(cost), 0) AS cost,
			COALESCE(SUM(CASE WHEN charged_cost > 0 THEN charged_cost ELSE cost END), 0) AS charged_cost,
			COALESCE(SUM(prompt_tokens), 0) AS prompt,
			COALESCE(SUM(completion_tokens), 0) AS completion,
			COALESCE(SUM(reasoning_tokens), 0) AS reasoning,
			COALESCE(SUM(cached_tokens), 0) AS cached,
			COALESCE(SUM(cache_write_tokens), 0) AS cache_write`, bucketFmt).
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

	// 生成完整 bucket 轴。不能只返回有数据的 bucket，否则 7 天窗口内只有一次调用时，
	// 前端图表会退化成单个 x 点，所有折线/堆叠点挤在一起。
	buckets := expectedUsageBuckets(period, time.Now())
	bucketSet := make(map[string]struct{}, len(buckets)+len(rows))
	for _, b := range buckets {
		bucketSet[b] = struct{}{}
	}
	for _, r := range rows {
		if _, ok := bucketSet[r.Bucket]; !ok {
			buckets = append(buckets, r.Bucket)
			bucketSet[r.Bucket] = struct{}{}
		}
	}
	sort.Strings(buckets)

	// 按 user 总花费排序，取 top N，其余合并到 "其他"。累加用 int64 micro_usd。
	type userTotal struct {
		UserID uint
		Cost   int64 // micro_usd
	}
	totals := map[uint]int64{}
	for _, r := range rows {
		totals[r.UserID] += effectiveAggregateChargedCost(r.ChargedCost, r.Cost)
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
		Requests    int64 `json:"requests"`
		Tokens      int64 `json:"tokens"`
		Cost        int64 `json:"-"` // raw micro_usd; 由本地 helper 转 USD 输出
		ChargedCost int64 `json:"-"` // charged micro_usd; 由本地 helper 转 USD 输出
		Prompt      int64 `json:"prompt_tokens"`
		Completion  int64 `json:"completion_tokens"`
		Reasoning   int64 `json:"reasoning_tokens"`
		Cached      int64 `json:"cached_tokens"`
		CacheWrite  int64 `json:"cache_write_tokens"`
		// CostUSD 是 JSON 输出字段（USD float），由 finalize 阶段填充
		CostUSD        float64 `json:"cost"`
		RawCostUSD     float64 `json:"raw_cost"`
		ChargedCostUSD float64 `json:"charged_cost"`
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
		p.ChargedCost += effectiveAggregateChargedCost(r.ChargedCost, r.Cost)
		p.Prompt += r.Prompt
		p.Completion += r.Completion
		p.Reasoning += r.Reasoning
		p.Cached += r.Cached
		p.CacheWrite += r.CacheWrite
	}

	out := make([]*series, 0, len(seriesMap)+1)
	for _, s := range seriesMap {
		out = append(out, s)
	}
	if len(uts) > topN {
		out = append(out, otherSeries)
	}
	// fix P-H3 (2026-05-19)：原 sort.Slice 比较函数每次 O(buckets) 重复遍历 Points，
	// 总开销 O(M log M × buckets)。改为预算一次 totals 缓存，比较 O(1)。
	// N=50 series × 168 buckets 时从 ~47K 加法降到 50 加法。
	seriesTotal := make(map[*series]int64, len(out))
	for _, s := range out {
		var total int64
		for _, p := range s.Points {
			total += effectiveAggregateChargedCost(p.ChargedCost, p.Cost)
		}
		seriesTotal[s] = total
	}
	sort.Slice(out, func(i, j int) bool {
		return seriesTotal[out[i]] > seriesTotal[out[j]]
	})

	// 排序完成后填充 USD 输出字段。在累加阶段一直用 int64 micro 避免漂移。
	for _, s := range out {
		for i := range s.Points {
			rawCost := s.Points[i].Cost
			chargedCost := effectiveAggregateChargedCost(s.Points[i].ChargedCost, rawCost)
			s.Points[i].CostUSD = database.MicroToUSD(rawCost)
			s.Points[i].RawCostUSD = database.MicroToUSD(rawCost)
			s.Points[i].ChargedCostUSD = database.MicroToUSD(chargedCost)
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

func expectedUsageBuckets(period string, now time.Time) []string {
	if period == "all" {
		return nil
	}
	format := "2006-01-02"
	step := 24 * time.Hour
	count := 30
	if period == "24h" || period == "7d" {
		format = "2006-01-02 15:00"
		step = time.Hour
		if period == "24h" {
			count = 24
		} else {
			count = 7 * 24
		}
	}
	buckets := make([]string, 0, count)
	base := now.UTC().Truncate(step)
	for i := count - 1; i >= 0; i-- {
		buckets = append(buckets, base.Add(-time.Duration(i)*step).Format(format))
	}
	return buckets
}

// GetUsersUsageEvents 返回逐条 ApiLog 详情（admin 视角，跨用户）。
//
// Query：
//   - period=24h|7d|30d|all
//   - user_id=N (可选，过滤特定用户)
//   - model=xxx (可选)
//   - status=failed|success|HTTP_STATUS (可选)
//   - error_type=xxx (可选)
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
	if statusFilter := strings.TrimSpace(c.Query("status")); statusFilter != "" {
		switch strings.ToLower(statusFilter) {
		case "failed", "error":
			q = q.Where("status < 200 OR status >= 300")
		case "success", "ok":
			q = q.Where("status >= 200 AND status < 300")
		default:
			if statusCode, err := strconv.Atoi(statusFilter); err == nil {
				q = q.Where("status = ?", statusCode)
			}
		}
	}
	if errorType := strings.TrimSpace(c.Query("error_type")); errorType != "" {
		q = q.Where("error_type = ?", errorType)
	}

	// fix MAJOR M22-6（codex 第二十二轮）：events list 加 .Error 检查 → fail-closed
	var total int64
	if err := q.Count(&total).Error; err != nil {
		log.Printf("[USERS-USAGE-EVENTS] count failed: %v", err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}

	type errorSummaryRow struct {
		ErrorType   string `json:"error_type"`
		Status      int    `json:"status"`
		RequestPath string `json:"request_path"`
		Count       int64  `json:"count"`
		LastSeenAt  string `json:"last_seen_at"`
	}
	var errorSummary []errorSummaryRow
	if err := q.Session(&gorm.Session{}).
		Where("status < 200 OR status >= 300").
		Select(`CASE
			WHEN error_type IS NOT NULL AND error_type <> '' THEN error_type
			ELSE 'http_' || status
		END AS error_type,
			status,
			request_path,
			COUNT(*) AS count,
			MAX(created_at) AS last_seen_at`).
		Group("error_type, status, request_path").
		Order("count DESC").
		Limit(10).
		Scan(&errorSummary).Error; err != nil {
		log.Printf("[USERS-USAGE-EVENTS] error summary failed: %v", err)
		errorSummary = nil
	}

	var logs []database.ApiLog
	if err := q.Order("created_at DESC").Limit(pageSize).Offset((page - 1) * pageSize).Find(&logs).Error; err != nil {
		log.Printf("[USERS-USAGE-EVENTS] find failed: %v", err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}

	// 拉本页 ApiLog 对应的 revenue 归因（订阅 / 余额）。事实化记录在 side table，
	// 避免依赖 ApiLog.ChargedCost 推断（订阅扣 charged、余额扣 raw 后两者口径不一致）。
	revenueByLog := map[uint]database.ApiLogRevenue{}
	usageLinesByLog := map[uint][]database.PublicApiLogUsageLine{}
	if len(logs) > 0 {
		logIDs := make([]uint, 0, len(logs))
		for _, l := range logs {
			logIDs = append(logIDs, l.ID)
		}
		var revenues []database.ApiLogRevenue
		if err := database.DB.Where("api_log_id IN ?", logIDs).Find(&revenues).Error; err != nil {
			log.Printf("[USERS-USAGE-EVENTS] revenue lookup failed: %v", err)
		}
		for _, r := range revenues {
			revenueByLog[r.ApiLogID] = r
		}
		if database.DB.Migrator().HasTable(&database.ApiLogUsageLine{}) {
			var usageLines []database.ApiLogUsageLine
			if err := database.DB.Where("api_log_id IN ?", logIDs).Order("api_log_id ASC, id ASC").Find(&usageLines).Error; err != nil {
				log.Printf("[USERS-USAGE-EVENTS] usage lines lookup failed: %v", err)
			}
			for _, line := range usageLines {
				usageLinesByLog[line.ApiLogID] = append(usageLinesByLog[line.ApiLogID], line.ToPublic())
			}
		}
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
		ID                     uint                             `json:"id"`
		UserID                 uint                             `json:"user_id"`
		Username               string                           `json:"username"`
		TokenName              string                           `json:"token_name"`
		ModelName              string                           `json:"model_name"`
		RequestedModel         string                           `json:"requested_model"`
		ServedModel            string                           `json:"served_model"`
		PromptTokens           int                              `json:"prompt_tokens"`
		CompletionTokens       int                              `json:"completion_tokens"`
		ReasoningTokens        int                              `json:"reasoning_tokens"`
		CachedTokens           int                              `json:"cached_tokens"`
		CacheWriteTokens       int                              `json:"cache_write_tokens"`
		CacheWrite5mTokens     int                              `json:"cache_write_5m_tokens"`
		CacheWrite1hTokens     int                              `json:"cache_write_1h_tokens"`
		TotalTokens            int                              `json:"total_tokens"`
		Cost                   float64                          `json:"cost"`
		RawCost                float64                          `json:"raw_cost"`
		ChargedCost            float64                          `json:"charged_cost"`
		RevenueSource          string                           `json:"revenue_source"`
		EffectiveRevenue       float64                          `json:"effective_revenue"`
		ModelWeight            float64                          `json:"model_weight"`
		HealthMultiplier       float64                          `json:"health_multiplier"`
		BillingRulesVersion    string                           `json:"billing_rules_version"`
		PrecheckInputTokens    int                              `json:"precheck_input_tokens"`
		PrecheckOutputTokens   int                              `json:"precheck_output_tokens"`
		PrecheckRawCost        float64                          `json:"precheck_raw_cost"`
		PrecheckChargedCost    float64                          `json:"precheck_charged_cost"`
		PrecheckQuotaPlanID    uint                             `json:"precheck_quota_plan_id"`
		PrecheckQuotaLimit     float64                          `json:"precheck_quota_limit"`
		PrecheckQuotaUsed      float64                          `json:"precheck_quota_used"`
		PrecheckQuotaRemaining float64                          `json:"precheck_quota_remaining"`
		PrecheckWindowEndAt    string                           `json:"precheck_window_end_at"`
		BlockReason            string                           `json:"block_reason"`
		FallbackUserOptIn      bool                             `json:"fallback_user_opt_in"`
		FallbackReason         string                           `json:"fallback_reason"`
		UpstreamProvider       string                           `json:"upstream_provider"`
		UpstreamAuthIndex      string                           `json:"upstream_auth_index"`
		UpstreamAuthType       string                           `json:"upstream_auth_type"`
		UpstreamSource         string                           `json:"upstream_source"`
		UpstreamRequestID      string                           `json:"upstream_request_id"`
		UpstreamUsageRecordID  uint                             `json:"upstream_usage_record_id"`
		UpstreamUsageMatch     string                           `json:"upstream_usage_match"`
		Latency                int64                            `json:"latency_ms"`
		Status                 int                              `json:"status"`
		IPAddress              string                           `json:"ip_address"`
		RequestPath            string                           `json:"request_path"`
		ErrorType              string                           `json:"error_type"`
		ErrorMessage           string                           `json:"error_message"`
		UsageLines             []database.PublicApiLogUsageLine `json:"usage_lines,omitempty"`
		CreatedAt              string                           `json:"created_at"`
	}
	out := make([]eventOut, 0, len(logs))
	for _, l := range logs {
		rev := revenueByLog[l.ID]
		out = append(out, eventOut{
			ID:                     l.ID,
			UserID:                 l.UserID,
			Username:               usernames[l.UserID],
			TokenName:              l.TokenName,
			ModelName:              l.ModelName,
			RequestedModel:         firstNonEmpty(l.RequestedModel, l.ModelName),
			ServedModel:            firstNonEmpty(l.ServedModel, l.ModelName),
			PromptTokens:           l.PromptTokens,
			CompletionTokens:       l.CompletionTokens,
			ReasoningTokens:        l.ReasoningTokens,
			CachedTokens:           l.CachedTokens,
			CacheWriteTokens:       l.CacheWriteTokens,
			CacheWrite5mTokens:     l.CacheWrite5mTokens,
			CacheWrite1hTokens:     l.CacheWrite1hTokens,
			TotalTokens:            l.PromptTokens + l.CompletionTokens,
			Cost:                   database.MicroToUSD(l.Cost),
			RawCost:                database.MicroToUSD(l.Cost),
			ChargedCost:            database.MicroToUSD(effectiveChargedCost(l)),
			RevenueSource:          rev.RevenueSource,
			EffectiveRevenue:       database.MicroToUSD(rev.EffectiveRevenueMicroUSD),
			ModelWeight:            effectivePositive(l.ModelWeight, 1),
			HealthMultiplier:       effectivePositive(l.HealthMultiplier, 1),
			BillingRulesVersion:    l.BillingRulesVersion,
			PrecheckInputTokens:    l.PrecheckInputTokens,
			PrecheckOutputTokens:   l.PrecheckOutputTokens,
			PrecheckRawCost:        database.MicroToUSD(l.PrecheckRawCost),
			PrecheckChargedCost:    database.MicroToUSD(l.PrecheckChargedCost),
			PrecheckQuotaPlanID:    l.PrecheckQuotaPlanID,
			PrecheckQuotaLimit:     database.MicroToUSD(l.PrecheckQuotaLimit),
			PrecheckQuotaUsed:      database.MicroToUSD(l.PrecheckQuotaUsed),
			PrecheckQuotaRemaining: database.MicroToUSD(l.PrecheckQuotaRemaining),
			PrecheckWindowEndAt:    formatOptionalTime(l.PrecheckWindowEndAt),
			BlockReason:            l.BlockReason,
			FallbackUserOptIn:      l.FallbackUserOptIn,
			FallbackReason:         l.FallbackReason,
			UpstreamProvider:       l.UpstreamProvider,
			UpstreamAuthIndex:      l.UpstreamAuthIndex,
			UpstreamAuthType:       l.UpstreamAuthType,
			UpstreamSource:         l.UpstreamSource,
			UpstreamRequestID:      l.UpstreamRequestID,
			UpstreamUsageRecordID:  l.UpstreamUsageRecordID,
			UpstreamUsageMatch:     l.UpstreamUsageMatch,
			Latency:                l.Latency,
			Status:                 l.Status,
			IPAddress:              l.IPAddress,
			RequestPath:            l.RequestPath,
			ErrorType:              l.ErrorType,
			ErrorMessage:           l.ErrorMessage,
			UsageLines:             usageLinesByLog[l.ID],
			CreatedAt:              l.CreatedAt.Format(time.RFC3339),
		})
	}

	return c.JSON(fiber.Map{
		"success": true,
		"data": fiber.Map{
			"page":          page,
			"page_size":     pageSize,
			"total":         total,
			"total_page":    (total + int64(pageSize) - 1) / int64(pageSize),
			"events":        out,
			"error_summary": errorSummary,
		},
	})
}

func sortUserUsageRows(rows []UserUsageRow, key string) {
	switch key {
	case "cost_asc":
		sort.SliceStable(rows, func(i, j int) bool {
			return usageRowCostForSort(rows[i].ChargedCost, rows[i].Cost) < usageRowCostForSort(rows[j].ChargedCost, rows[j].Cost)
		})
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
		sort.SliceStable(rows, func(i, j int) bool {
			return usageRowCostForSort(rows[i].ChargedCost, rows[i].Cost) > usageRowCostForSort(rows[j].ChargedCost, rows[j].Cost)
		})
	}
}

func usageRowCostForSort(chargedCost, rawCost float64) float64 {
	if chargedCost == 0 && rawCost > 0 {
		return rawCost
	}
	return chargedCost
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func formatOptionalTime(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

func effectiveChargedCost(l database.ApiLog) int64 {
	if l.ChargedCost == 0 && l.Cost > 0 {
		return l.Cost
	}
	return l.ChargedCost
}

func effectivePositive(v, fallback float64) float64 {
	if v > 0 {
		return v
	}
	return fallback
}
