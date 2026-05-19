// Package controller / billing.go
//
// 账单 API：用户查自己 / admin 查任意用户。
//
// 设计：
//   - 列表 + 汇总 + CSV 导出 三类端点共享同一查询构建逻辑（buildBillingQuery）
//   - 类型筛选（types=topup,purchase_sub,...）+ 时间范围（from/to ISO 8601）+ 分页
//   - 汇总按类型分组聚合，给前端"月度卡片"用
//   - admin 端点比用户端点多一个 user_id 路径参数；其余共享
package controller

import (
	"daof-cpa/database"
	"encoding/csv"
	"fmt"
	"io"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// 允许的 EntryType 白名单（防 admin 注入任意字符串污染日志）。Phase 8 后只保留订阅与余额路径。
//
// fix MAJOR（codex 第十七轮）：补齐 admin_grant_* 与 api_usage_pending_reconcile，
// 否则 admin 账单页过滤会把"赠送账单"和"待对账记录"排除，财务对账出现 hole。
var allowedBillingTypes = map[string]bool{
	database.BillingTypeTopup:                    true,
	database.BillingTypePurchaseSub:              true,
	database.BillingTypeBonusCredit:              true,
	database.BillingTypeRefundSub:                true,
	database.BillingTypeRefundTopup:              true,
	database.BillingTypeAdminAdjust:              true,
	database.BillingTypeAdminGrantSub:            true,
	database.BillingTypeAdminRevokeGrant:         true,
	database.BillingTypeApiConsumeBalance:        true,
	database.BillingTypeApiUsageSub:              true,
	database.BillingTypeApiUsagePendingReconcile: true,
}

// billingFilters 列表/汇总/CSV 三类端点共享的查询条件
type billingFilters struct {
	UserID uint
	Types  []string  // 空 = 不限制
	From   time.Time // 零值 = 不限制
	To     time.Time // 零值 = 不限制
	// ToExclusive=true 时使用 `< To` 比较（YYYY-MM-DD 输入下 To 已加 24h）；
	// false 时使用 `<= To`（RFC3339 输入：精确包含 to 时刻）。
	// fix Minor 第二十轮（codex）：YYYY-MM-DD 转 23:59:59 漏亚秒账单的修复
	ToExclusive bool
}

func parseBillingFilters(c *fiber.Ctx, userID uint) (billingFilters, error) {
	f := billingFilters{UserID: userID}
	if t := strings.TrimSpace(c.Query("types")); t != "" {
		raw := strings.Split(t, ",")
		for _, x := range raw {
			x = strings.TrimSpace(x)
			if x == "" {
				continue
			}
			if !allowedBillingTypes[x] {
				return f, fmt.Errorf("unknown entry type: %s", x)
			}
			f.Types = append(f.Types, x)
		}
	}
	if from := strings.TrimSpace(c.Query("from")); from != "" {
		t, err := time.Parse(time.RFC3339, from)
		if err != nil {
			// 兼容 yyyy-mm-dd
			t, err = time.Parse("2006-01-02", from)
			if err != nil {
				return f, fmt.Errorf("invalid 'from' date: %w", err)
			}
		}
		f.From = t
	}
	if to := strings.TrimSpace(c.Query("to")); to != "" {
		t, err := time.Parse(time.RFC3339, to)
		if err != nil {
			t, err = time.Parse("2006-01-02", to)
			if err != nil {
				return f, fmt.Errorf("invalid 'to' date: %w", err)
			}
			// fix Minor 第二十轮（codex）：原 23:59:59 漏掉亚秒级账单。
			// 改为 nextDay 边界 —— f.ToExclusive=true 让查询走严格 `<` 比较。
			t = t.Add(24 * time.Hour)
			f.ToExclusive = true
		}
		f.To = t
	}
	return f, nil
}

// applyBillingFilters 应用过滤条件到 *gorm.DB query builder
func applyBillingFilters(q *gorm.DB, f billingFilters) *gorm.DB {
	return applyBillingFiltersWithAlias(q, f, "")
}

func applyBillingFiltersWithAlias(q *gorm.DB, f billingFilters, alias string) *gorm.DB {
	col := func(name string) string {
		if alias == "" {
			return name
		}
		return alias + "." + name
	}
	q = q.Where(col("user_id")+" = ?", f.UserID)
	if len(f.Types) > 0 {
		q = q.Where(col("entry_type")+" IN ?", f.Types)
	}
	if !f.From.IsZero() {
		q = q.Where(col("occurred_at")+" >= ?", f.From)
	}
	if !f.To.IsZero() {
		// fix Minor 第二十轮（codex）：
		//   - YYYY-MM-DD 形式 → ToExclusive=true，f.To 已加 nextDay → 严格 `< nextDay`
		//   - RFC3339 形式 → ToExclusive=false，保持 `<= to` 包含 to 时刻精确
		// 这样既不漏 YYYY-MM-DD 当天亚秒级账单，又不破坏精确时间戳的包含语义。
		if f.ToExclusive {
			q = q.Where(col("occurred_at")+" < ?", f.To)
		} else {
			q = q.Where(col("occurred_at")+" <= ?", f.To)
		}
	}
	return q
}

type billingEntryWithReconcile struct {
	database.BillingEntry
	IsReconciled    bool   `gorm:"column:is_reconciled"`
	ReconcileResult string `gorm:"column:reconcile_result"`
}

type adminBillingEntryDTO struct {
	BillingEntryView
	IsReconciled    bool   `json:"is_reconciled"`
	ReconcileResult string `json:"reconcile_result,omitempty"`
}

type billingApiLogEstimate struct {
	RawCostMicroUSD     int64
	ChargedCostMicroUSD int64
}

func adminBillingEntryDTOsFrom(rows []billingEntryWithReconcile, estimates map[uint]billingApiLogEstimate) []adminBillingEntryDTO {
	out := make([]adminBillingEntryDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, adminBillingEntryDTO{
			BillingEntryView: billingEntryViewFromWithEstimate(row.BillingEntry, estimates),
			IsReconciled:     row.IsReconciled,
			ReconcileResult:  row.ReconcileResult,
		})
	}
	return out
}

func billingEntryViewsFromWithEstimates(rows []database.BillingEntry, estimates map[uint]billingApiLogEstimate) []BillingEntryView {
	out := make([]BillingEntryView, 0, len(rows))
	for _, row := range rows {
		out = append(out, billingEntryViewFromWithEstimate(row, estimates))
	}
	return out
}

func billingEntryViewFromWithEstimate(row database.BillingEntry, estimates map[uint]billingApiLogEstimate) BillingEntryView {
	view := billingEntryViewFrom(row)
	view.EstimatedReconcileCostUSD = database.MicroToUSD(row.EstimatedCostUSD)
	if estimate, ok := estimates[row.ID]; ok {
		view.EstimatedRawCostUSD = database.MicroToUSD(estimate.RawCostMicroUSD)
		view.EstimatedChargedCostUSD = database.MicroToUSD(estimate.ChargedCostMicroUSD)
	}
	if row.EntryType == database.BillingTypeApiUsagePendingReconcile &&
		row.EstimatedCostUSD > 0 &&
		view.EstimatedRawCostUSD == 0 &&
		view.EstimatedChargedCostUSD == 0 {
		view.EstimatedRawCostUSD = database.MicroToUSD(row.EstimatedCostUSD)
		view.EstimatedChargedCostUSD = database.MicroToUSD(row.EstimatedCostUSD)
	}
	return view
}

func billingCostEstimatesForEntries(rows []database.BillingEntry) map[uint]billingApiLogEstimate {
	apiLogByEntryID := make(map[uint]uint)
	apiLogIDs := make([]uint, 0, len(rows))
	for _, row := range rows {
		if row.RelatedID == 0 || row.RelatedType != "api_log" {
			continue
		}
		apiLogByEntryID[row.ID] = row.RelatedID
		apiLogIDs = append(apiLogIDs, row.RelatedID)
	}
	if len(apiLogIDs) == 0 {
		return nil
	}

	type apiLogCostRow struct {
		ID                  uint
		Cost                int64
		ChargedCost         int64
		PrecheckRawCost     int64
		PrecheckChargedCost int64
	}
	var logs []apiLogCostRow
	if err := database.DB.Model(&database.ApiLog{}).
		Select("id, cost, charged_cost, precheck_raw_cost, precheck_charged_cost").
		Where("id IN ?", apiLogIDs).
		Scan(&logs).Error; err != nil {
		log.Printf("[BILLING-LIST] api_log estimate lookup failed: %v", err)
		return nil
	}
	byApiLogID := make(map[uint]billingApiLogEstimate, len(logs))
	for _, row := range logs {
		rawCost := row.PrecheckRawCost
		chargedCost := row.PrecheckChargedCost
		if rawCost == 0 && chargedCost == 0 {
			rawCost = row.Cost
			chargedCost = row.ChargedCost
		}
		if rawCost > 0 && chargedCost == 0 {
			chargedCost = rawCost
		}
		if chargedCost > 0 && rawCost == 0 {
			rawCost = chargedCost
		}
		if rawCost > 0 || chargedCost > 0 {
			byApiLogID[row.ID] = billingApiLogEstimate{
				RawCostMicroUSD:     rawCost,
				ChargedCostMicroUSD: chargedCost,
			}
		}
	}
	out := make(map[uint]billingApiLogEstimate, len(apiLogByEntryID))
	for entryID, apiLogID := range apiLogByEntryID {
		if estimate, ok := byApiLogID[apiLogID]; ok {
			out[entryID] = estimate
		}
	}
	return out
}

// ─── 用户：GET /api/billing/mine ─────────────────────────────────

// MyBillingEntries keyset 分页列表。最多 200 条/页，按 id 倒序。
func MyBillingEntries(c *fiber.Ctx) error {
	user, err := getCurrentUser(c)
	if err != nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}
	return listBillingEntries(c, user.ID, false)
}

// AdminListUserBilling admin 看任意用户。路径参数 :id。
func AdminListUserBilling(c *fiber.Ctx) error {
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil || id <= 0 {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_PARAMS"})
	}
	return listBillingEntries(c, uint(id), true)
}

func listBillingEntries(c *fiber.Ctx, userID uint, includeInternal bool) error {
	f, err := parseBillingFilters(c, userID)
	if err != nil {
		// fix C-M4 (2026-05-19)：面向用户接口不回显 err.Error()（可能含 SQL 片段 / 内部
		// 路径），改成固定文案 + 服务端日志。
		log.Printf("[BILLING-FILTER] parse failed: %v", err)
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "查询参数非法", "message_code": "ERR_INVALID_FILTER"})
	}
	size, _ := strconv.Atoi(c.Query("page_size", "30"))
	if size < 1 || size > 200 {
		size = 30
	}
	var cursor int64
	if rawCursor := strings.TrimSpace(c.Query("cursor")); rawCursor != "" {
		cursor, err = strconv.ParseInt(rawCursor, 10, 64)
		if err != nil || cursor < 0 {
			return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_PARAMS"})
		}
	}

	if includeInternal {
		q := applyBillingFiltersWithAlias(
			database.DB.Table("billing_entries AS be").
				Select("be.*, br.id IS NOT NULL AS is_reconciled, COALESCE(br.result, '') AS reconcile_result").
				Joins("LEFT JOIN billing_reconciliations br ON br.billing_entry_id = be.id"),
			f,
			"be",
		)
		if cursor > 0 {
			q = q.Where("be.id < ?", cursor)
		}
		var rows []billingEntryWithReconcile
		if err := q.Order("be.id DESC").
			Limit(size + 1).
			Scan(&rows).Error; err != nil {
			log.Printf("[BILLING-LIST] find failed user=%d: %v", userID, err)
			return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
		}
		var nextCursor int64
		if len(rows) > size {
			rows = rows[:size]
			nextCursor = int64(rows[len(rows)-1].ID)
		}
		entries := make([]database.BillingEntry, 0, len(rows))
		for _, row := range rows {
			entries = append(entries, row.BillingEntry)
		}
		estimates := billingCostEstimatesForEntries(entries)
		return c.JSON(fiber.Map{
			"success":     true,
			"data":        adminBillingEntryDTOsFrom(rows, estimates),
			"next_cursor": nextCursor,
		})
	}

	q := applyBillingFilters(database.DB.Model(&database.BillingEntry{}), f)
	if cursor > 0 {
		q = q.Where("id < ?", cursor)
	}

	var rows []database.BillingEntry
	if err := q.Order("id DESC").
		Limit(size + 1).
		Find(&rows).Error; err != nil {
		log.Printf("[BILLING-LIST] find failed user=%d: %v", userID, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}
	var nextCursor int64
	if len(rows) > size {
		rows = rows[:size]
		nextCursor = int64(rows[len(rows)-1].ID)
	}
	estimates := billingCostEstimatesForEntries(rows)
	for i := range rows {
		rows[i].UserID = 0
		rows[i].Description = publicBillingDescription(rows[i])
		rows[i].RelatedType = ""
		rows[i].RelatedID = 0
		rows[i].SourceSubscriptionID = nil
	}

	return c.JSON(fiber.Map{
		"success":     true,
		"data":        billingEntryViewsFromWithEstimates(rows, estimates),
		"next_cursor": nextCursor,
	})
}

// ─── 汇总：GET /api/billing/mine/summary ────────────────────────

// BillingSummaryRow 按 entry_type 分组的聚合
//
// fix MAJOR M22-A1 Phase 1：金额字段统一 int64 micro_usd。前端展示时除 1e6 显示美元。
type BillingSummaryRow struct {
	EntryType     string `json:"entry_type"`
	Count         int64  `json:"count"`
	TotalMicroUSD int64  `json:"total_micro_usd"` // 该类型 sum(amount_usd) 的 micro_usd 累加
}

// MyBillingSummary 给前端"月度卡片"展示用：按类型分组的聚合 + 净收支 + 当前余额
func MyBillingSummary(c *fiber.Ctx) error {
	user, err := getCurrentUser(c)
	if err != nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}
	return billingSummary(c, user.ID, user.Quota)
}

// AdminUserBillingSummary admin 看任意用户的汇总
func AdminUserBillingSummary(c *fiber.Ctx) error {
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil || id <= 0 {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_PARAMS"})
	}
	var u database.User
	if err := database.DB.Select("id, quota").First(&u, id).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"success": false, "message_code": "ERR_USER_NOT_FOUND"})
	}
	return billingSummary(c, uint(id), u.Quota)
}

func billingSummary(c *fiber.Ctx, userID uint, currentBalanceMicroUSD int64) error {
	f, err := parseBillingFilters(c, userID)
	if err != nil {
		// fix C-M4 (2026-05-19)：面向用户接口不回显 err.Error()（可能含 SQL 片段 / 内部
		// 路径），改成固定文案 + 服务端日志。
		log.Printf("[BILLING-FILTER] parse failed: %v", err)
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "查询参数非法", "message_code": "ERR_INVALID_FILTER"})
	}
	q := applyBillingFilters(database.DB.Model(&database.BillingEntry{}), f)
	// fix D4 (2026-05-19)：pending_reconcile / upstream_unmetered 行的 amount_usd=0
	// 不影响金额汇总，但 count 列把它们计入消费笔数 → 前端"月度消费 N 次"虚高。
	// 默认只统计 settled 行；admin 看待对账队列走单独 endpoint。
	q = q.Where("billing_state = ?", database.BillingStateSettled)

	var rows []BillingSummaryRow
	if err := q.
		Select("entry_type, COUNT(*) AS count, COALESCE(SUM(amount_usd), 0) AS total_micro_usd").
		Group("entry_type").
		Find(&rows).Error; err != nil {
		log.Printf("[BILLING-SUMMARY] failed user=%d: %v", userID, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}
	// 计算合计（int64 micro_usd 累加，无浮点误差）
	var totalIn, totalOut int64
	byType := make(map[string]BillingSummaryRow, len(rows))
	for _, r := range rows {
		byType[r.EntryType] = r
		if r.TotalMicroUSD > 0 {
			totalIn += r.TotalMicroUSD
		} else {
			totalOut += -r.TotalMicroUSD // 转正数显示
		}
	}

	return c.JSON(fiber.Map{
		"success": true,
		"data": fiber.Map{
			"by_type":         byType,
			"total_in_usd":    database.MicroToUSD(totalIn),
			"total_out_usd":   database.MicroToUSD(totalOut),
			"net_change_usd":  database.MicroToUSD(totalIn - totalOut),
			"current_balance": database.MicroToUSD(currentBalanceMicroUSD),
		},
	})
}

// ─── CSV 导出：GET /api/billing/mine/export ──────────────────────

// MyBillingExport 用户导出自己的账单为 CSV
func MyBillingExport(c *fiber.Ctx) error {
	user, err := getCurrentUser(c)
	if err != nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}
	return exportBillingCSV(c, user.ID, false)
}

// AdminUserBillingExport admin 导出任意用户账单为 CSV
func AdminUserBillingExport(c *fiber.Ctx) error {
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil || id <= 0 {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_PARAMS"})
	}
	return exportBillingCSV(c, uint(id), true)
}

const billingCSVBatchSize = 500

func exportBillingCSV(c *fiber.Ctx, userID uint, includeInternal bool) error {
	f, err := parseBillingFilters(c, userID)
	if err != nil {
		// fix C-M4 (2026-05-19)：面向用户接口不回显 err.Error()（可能含 SQL 片段 / 内部
		// 路径），改成固定文案 + 服务端日志。
		log.Printf("[BILLING-FILTER] parse failed: %v", err)
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "查询参数非法", "message_code": "ERR_INVALID_FILTER"})
	}

	c.Set("Content-Type", "text/csv; charset=utf-8")
	c.Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="billing-user-%d-%s.csv"`,
			userID, time.Now().Format("20060102")))

	pr, pw := io.Pipe()
	go func() {
		if err := writeBillingCSVStream(pw, f, userID, includeInternal); err != nil {
			log.Printf("[BILLING-EXPORT] stream failed user=%d: %v", userID, err)
			_ = pw.CloseWithError(err)
			return
		}
		_ = pw.Close()
	}()

	return c.SendStream(pr)
}

func writeBillingCSVStream(w io.Writer, f billingFilters, userID uint, includeInternal bool) error {
	// 写 BOM 让 Excel 正确识别 UTF-8 中文
	if _, err := w.Write([]byte{0xEF, 0xBB, 0xBF}); err != nil {
		return err
	}

	csvw := csv.NewWriter(w)
	defer csvw.Flush()

	// 表头（中文友好；用户和 admin 共用）
	header := []string{
		"发生时间", "类型", "金额(USD)", "余额(USD)",
		"模型", "Tokens", "原币种", "原币金额", "描述", "关联类型", "关联ID",
	}
	if err := csvw.Write(header); err != nil {
		return err
	}
	csvw.Flush()
	if err := csvw.Error(); err != nil {
		return err
	}

	q := applyBillingFilters(database.DB.Model(&database.BillingEntry{}), f)
	var rows []database.BillingEntry
	return q.FindInBatches(&rows, billingCSVBatchSize, func(tx *gorm.DB, batch int) error {
		for _, r := range rows {
			record := billingCSVRecord(r, includeInternal)
			if err := csvw.Write(record); err != nil {
				// 流式写入失败时无法回传 4xx（headers 已发），至少日志记录受影响行。
				log.Printf("[BILLING-EXPORT] mid-stream write failed user=%d entry_id=%d batch=%d: %v (响应已截断，客户端 CSV 不完整)",
					userID, r.ID, batch, err)
				return err
			}
		}
		csvw.Flush()
		if err := csvw.Error(); err != nil {
			return err
		}
		return tx.Error
	}).Error
}

func billingCSVRecord(r database.BillingEntry, includeInternal bool) []string {
	description := r.Description
	relatedType := r.RelatedType
	relatedID := r.RelatedID
	if !includeInternal {
		description = publicBillingDescription(r)
		relatedType = ""
		relatedID = 0
	}
	// fix Major（codex+claude 第十四轮）：所有可能含用户/admin 输入的字符串字段必须经过 csvSanitize
	// 防 Excel 公式注入。数字/枚举字段不需要（来源受控）。
	// 金额 micro_usd → USD 字符串（6 位小数无损）；原币 RMB → fen → 元字符串（2 位小数）
	amountOriginalStr := ""
	if r.CurrencyOriginal == "USD" {
		amountOriginalStr = database.FormatMicroUSD(r.AmountOriginal)
	} else if r.CurrencyOriginal == "CNY" || r.CurrencyOriginal == "RMB" {
		amountOriginalStr = database.FormatFen(r.AmountOriginal)
	} else {
		amountOriginalStr = strconv.FormatInt(r.AmountOriginal, 10) // 未知币种用 raw 整数
	}
	return []string{
		r.OccurredAt.Format("2006-01-02 15:04:05"),
		localizeBillingType(r.EntryType),
		database.FormatMicroUSD(r.AmountUSD),
		database.FormatMicroUSD(r.BalanceAfterUSD),
		csvSanitize(r.ModelName),
		strconv.Itoa(r.TokensTotal),
		csvSanitize(r.CurrencyOriginal),
		amountOriginalStr,
		csvSanitize(description),
		csvSanitize(relatedType),
		strconv.Itoa(int(relatedID)),
	}
}

var adminMarkerRE = regexp.MustCompile(`(^| · )admin#\d+($| · )`)

func publicBillingDescription(r database.BillingEntry) string {
	switch r.EntryType {
	case database.BillingTypeAdminAdjust:
		return userFriendlyAdminAdjustDescription(r.AmountUSD)
	case database.BillingTypeAdminGrantSub, database.BillingTypeAdminRevokeGrant:
		return stripInternalBillingFragments(r.Description)
	default:
		return stripInternalBillingFragments(r.Description)
	}
}

func stripInternalBillingFragments(desc string) string {
	desc = strings.TrimSpace(desc)
	if desc == "" {
		return desc
	}
	if idx := strings.Index(desc, " · ["); idx >= 0 {
		desc = desc[:idx]
	}
	desc = adminMarkerRE.ReplaceAllString(desc, " · ")
	desc = strings.Trim(desc, " ·")
	return desc
}

func userFriendlyAdminAdjustDescription(deltaMicro int64) string {
	if deltaMicro > 0 {
		return "管理员调整额度 · 余额增加 $" + formatAbsMicroUSD(deltaMicro)
	}
	if deltaMicro < 0 {
		return "管理员调整额度 · 余额减少 $" + formatAbsMicroUSD(deltaMicro)
	}
	return "管理员调整额度 · 余额未变化"
}

func formatAbsMicroUSD(v int64) string {
	if v < 0 {
		v = -v
	}
	return fmt.Sprintf("%.2f", database.MicroToUSD(v))
}

// csvSanitize 防 CSV 注入：以 = + - @ \t \r 开头的单元格在 Excel/Sheets 中会被解析为公式。
// 攻击场景：admin 设置 req.Reason 为 `=HYPERLINK("https://evil","Click")`，落入 BillingEntry.Description，
// 其他 admin 导出 CSV → 在 Excel 里点开 → 公式执行 → 钓鱼或数据外泄。
// 修复：危险前缀加单引号（Excel 标准转义），完全保留原文便于审计。
//
// fix MAJOR M-B6（codex 第二十一轮）：原仅看 s[0]，漏掉以下情形：
//   - UTF-8 BOM (\uFEFF) 在前 → 实际有效字符是第二字节
//   - 前导空格 / 制表符 / 换行 → Excel 经常 trim 后再解析公式
//   - 字节级判断不能识别 BOM（多字节）
//
// 改为：先 strings.TrimLeft 掉 BOM + 空白控制字符，看剩余首字符是否危险。
// 注意：返回的转义结果用原始 s 加前缀单引号，**保留原文** 不做剥除（审计追溯）。
func csvSanitize(s string) string {
	if s == "" {
		return s
	}
	// 剔除 BOM (U+FEFF) + 空白 / 制表符 / 换行后再判断首字符
	// 用 \uFEFF 转义形式而非裸 BOM 字符（Go 编译器拒绝源码中段含 BOM）
	stripped := strings.TrimLeft(s, "\uFEFF \t\r\n")
	if stripped == "" {
		return s
	}
	switch stripped[0] {
	case '=', '+', '-', '@', '|', '%':
		return "'" + s
	}
	return s
}

// localizeBillingType 把 EntryType 常量翻成中文（CSV 里面用，避免英文 key 给非技术人员）
//
// fix MAJOR（codex 第十七轮）：补齐 admin_grant_* + api_usage_pending_reconcile
// 与 allowedBillingTypes 白名单对齐
func localizeBillingType(t string) string {
	switch t {
	case database.BillingTypeTopup:
		return "充值"
	case database.BillingTypePurchaseSub:
		return "购买套餐"
	case database.BillingTypeBonusCredit:
		return "奖励入账"
	case database.BillingTypeRefundSub:
		return "订阅退款"
	case database.BillingTypeRefundTopup:
		return "充值退款"
	case database.BillingTypeAdminAdjust:
		return "管理员调整"
	case database.BillingTypeAdminGrantSub:
		return "管理员赠送订阅"
	case database.BillingTypeAdminRevokeGrant:
		return "管理员收回赠送"
	case database.BillingTypeApiConsumeBalance:
		return "余额扣费"
	case database.BillingTypeApiUsageSub:
		return "套餐扣额度"
	case database.BillingTypeApiUsagePendingReconcile:
		return "待对账"
	default:
		return t
	}
}
