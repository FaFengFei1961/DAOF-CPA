package controller

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/big"
	"net/http"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"

	"daof-cpa/database"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type upstreamAccountCostPayload struct {
	ID                          uint    `json:"id"`
	Provider                    string  `json:"provider"`
	AuthIndex                   string  `json:"auth_index"`
	AuthType                    string  `json:"auth_type"`
	Label                       string  `json:"label"`
	PlanName                    string  `json:"plan_name"`
	MonthlyCostUSD              float64 `json:"monthly_cost_usd"`
	EstimatedMonthlyCapacityUSD float64 `json:"estimated_monthly_capacity_usd"`
	Active                      *bool   `json:"active"`
	Notes                       string  `json:"notes"`

	monthlyCostMicroUSD              int64
	estimatedMonthlyCapacityMicroUSD int64
}

type upstreamAccountCostBulkPayload struct {
	Accounts                    []upstreamAccountCostBulkAccount `json:"accounts"`
	PlanName                    string                           `json:"plan_name"`
	MonthlyCostUSD              float64                          `json:"monthly_cost_usd"`
	EstimatedMonthlyCapacityUSD float64                          `json:"estimated_monthly_capacity_usd"`
	Active                      *bool                            `json:"active"`
	Notes                       string                           `json:"notes"`
}

type upstreamAccountCostBulkAccount struct {
	Provider  string `json:"provider"`
	AuthIndex string `json:"auth_index"`
	AuthType  string `json:"auth_type"`
	Label     string `json:"label"`
}

type upstreamAccountCostOut struct {
	ID                               uint      `json:"id"`
	Provider                         string    `json:"provider"`
	AuthIndex                        string    `json:"auth_index"`
	AuthType                         string    `json:"auth_type"`
	Label                            string    `json:"label"`
	PlanName                         string    `json:"plan_name"`
	MonthlyCostMicroUSD              int64     `json:"monthly_cost_micro_usd"`
	EstimatedMonthlyCapacityMicroUSD int64     `json:"estimated_monthly_capacity_micro_usd"`
	Active                           bool      `json:"active"`
	Notes                            string    `json:"notes"`
	CreatedAt                        time.Time `json:"created_at"`
	UpdatedAt                        time.Time `json:"updated_at"`
}

type upstreamAccountCandidateRow struct {
	Provider                    string     `json:"provider"`
	AuthIndex                   string     `json:"auth_index"`
	AuthType                    string     `json:"auth_type"`
	FileName                    string     `json:"file_name"`
	Email                       string     `json:"email"`
	CredentialStatus            string     `json:"credential_status"`
	CredentialDisabled          bool       `json:"credential_disabled"`
	LastSeenAt                  *time.Time `json:"last_seen_at,omitempty"`
	LastDownloadedAt            *time.Time `json:"last_downloaded_at,omitempty"`
	AccountID                   uint       `json:"account_id"`
	AccountConfigured           bool       `json:"account_configured"`
	AccountActive               bool       `json:"account_active"`
	Label                       string     `json:"label"`
	PlanName                    string     `json:"plan_name"`
	MonthlyCostUSD              float64    `json:"monthly_cost_usd"`
	EstimatedMonthlyCapacityUSD float64    `json:"estimated_monthly_capacity_usd"`
	Notes                       string     `json:"notes"`
}

type upstreamAccountCostPreset struct {
	ID                          string  `json:"id"`
	Label                       string  `json:"label"`
	Provider                    string  `json:"provider"`
	PlanName                    string  `json:"plan_name"`
	MonthlyCostUSD              float64 `json:"monthly_cost_usd"`
	EstimatedMonthlyCapacityUSD float64 `json:"estimated_monthly_capacity_usd"`
	Notes                       string  `json:"notes"`
}

func ListUpstreamAccountCosts(c *fiber.Ctx) error {
	q := database.DB.Model(&database.UpstreamAccountCost{})
	if provider := strings.TrimSpace(c.Query("provider")); provider != "" {
		q = q.Where("provider = ?", normalizeCostProvider(provider))
	}
	if authIndex := strings.TrimSpace(c.Query("auth_index")); authIndex != "" {
		q = q.Where("auth_index = ?", authIndex)
	}
	var rows []database.UpstreamAccountCost
	if err := q.Order("provider ASC, auth_index ASC").Find(&rows).Error; err != nil {
		log.Printf("[UPSTREAM-COST] list failed: %v", err)
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}
	out := make([]upstreamAccountCostOut, 0, len(rows))
	for _, row := range rows {
		out = append(out, upstreamAccountCostToOut(row))
	}
	return c.JSON(fiber.Map{"success": true, "data": out})
}

// ListUpstreamAccountCandidates returns all CPA credentials with their optional
// local cost config. This is intentionally driven by cpa_credentials rather
// than api_logs, so admins can configure account cost before the first request.
func ListUpstreamAccountCandidates(c *fiber.Ctx) error {
	type joinedRow struct {
		Provider                  string
		AuthIndex                 string
		AuthType                  string
		FileName                  string
		Email                     string
		CredentialStatus          string
		CredentialDisabled        bool
		LastSeenAt                time.Time
		LastDownloadedAt          time.Time
		AccountID                 uint
		AccountActive             bool
		Label                     string
		PlanName                  string
		MonthlyCostMicroUSD       int64
		EstimatedCapacityMicroUSD int64
		Notes                     string
	}

	var rows []joinedRow
	if err := database.DB.Table("cpa_credentials AS cc").
		Select(`LOWER(TRIM(cc.provider)) AS provider,
			cc.auth_id AS auth_index,
			COALESCE(uac.auth_type, '') AS auth_type,
			cc.file_name,
			cc.email,
			cc.status AS credential_status,
			cc.disabled AS credential_disabled,
			cc.last_seen_at,
			cc.last_downloaded_at,
			COALESCE(uac.id, 0) AS account_id,
			COALESCE(uac.active, false) AS account_active,
			COALESCE(uac.label, '') AS label,
			COALESCE(uac.plan_name, '') AS plan_name,
			COALESCE(uac.monthly_cost_usd, 0) AS monthly_cost_micro_usd,
			COALESCE(uac.estimated_monthly_capacity_usd, 0) AS estimated_capacity_micro_usd,
			COALESCE(uac.notes, '') AS notes`).
		Joins(`LEFT JOIN upstream_account_costs AS uac
			ON uac.provider = LOWER(TRIM(cc.provider))
			AND uac.auth_index = cc.auth_id`).
		Order("cc.disabled ASC, LOWER(TRIM(cc.provider)) ASC, cc.email ASC, cc.file_name ASC, cc.auth_id ASC").
		Scan(&rows).Error; err != nil {
		log.Printf("[UPSTREAM-COST] list candidates failed: %v", err)
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}

	out := make([]upstreamAccountCandidateRow, 0, len(rows))
	for _, r := range rows {
		lastSeenAt := r.LastSeenAt
		lastDownloadedAt := r.LastDownloadedAt
		out = append(out, upstreamAccountCandidateRow{
			Provider:                    normalizeCostProvider(r.Provider),
			AuthIndex:                   strings.TrimSpace(r.AuthIndex),
			AuthType:                    strings.TrimSpace(r.AuthType),
			FileName:                    strings.TrimSpace(r.FileName),
			Email:                       strings.TrimSpace(r.Email),
			CredentialStatus:            strings.TrimSpace(r.CredentialStatus),
			CredentialDisabled:          r.CredentialDisabled,
			LastSeenAt:                  &lastSeenAt,
			LastDownloadedAt:            &lastDownloadedAt,
			AccountID:                   r.AccountID,
			AccountConfigured:           r.AccountID > 0,
			AccountActive:               r.AccountID > 0 && r.AccountActive,
			Label:                       strings.TrimSpace(r.Label),
			PlanName:                    strings.TrimSpace(r.PlanName),
			MonthlyCostUSD:              database.MicroToUSD(r.MonthlyCostMicroUSD),
			EstimatedMonthlyCapacityUSD: database.MicroToUSD(r.EstimatedCapacityMicroUSD),
			Notes:                       strings.TrimSpace(r.Notes),
		})
	}
	return c.JSON(fiber.Map{"success": true, "data": out})
}

func ListUpstreamAccountCostPresets(c *fiber.Ctx) error {
	raw := readSysConfigCached("upstream_account_cost_presets_json", database.SubscriptionSysConfigDefaults["upstream_account_cost_presets_json"])
	presets, err := parseUpstreamAccountCostPresets(raw)
	if err != nil {
		log.Printf("[UPSTREAM-COST] invalid presets config: %v", err)
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_INVALID_JSON",
			"message":      "upstream_account_cost_presets_json 格式不合法: " + err.Error(),
		})
	}
	return c.JSON(fiber.Map{"success": true, "data": presets})
}

func CreateUpstreamAccountCost(c *fiber.Ctx) error {
	payload, ok := parseUpstreamAccountCostPayload(c)
	if !ok {
		return nil
	}
	row := upstreamAccountCostFromPayload(payload, database.UpstreamAccountCost{})
	if err := database.DB.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "provider"}, {Name: "auth_index"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"auth_type",
			"label",
			"plan_name",
			"monthly_cost_usd",
			"estimated_monthly_capacity_usd",
			"active",
			"notes",
			"updated_at",
		}),
	}).Create(&row).Error; err != nil {
		log.Printf("[UPSTREAM-COST] upsert failed provider=%s auth=%s: %v", row.Provider, row.AuthIndex, err)
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_WRITE"})
	}
	if err := database.DB.Where("provider = ? AND auth_index = ?", row.Provider, row.AuthIndex).First(&row).Error; err != nil {
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}
	if err := refreshPlatformCostEstimateForAccount(row); err != nil {
		log.Printf("[UPSTREAM-COST] refresh platform estimate failed id=%d: %v", row.ID, err)
	}
	return c.JSON(fiber.Map{"success": true, "data": upstreamAccountCostToOut(row)})
}

func BulkUpsertUpstreamAccountCosts(c *fiber.Ctx) error {
	var payload upstreamAccountCostBulkPayload
	if err := c.BodyParser(&payload); err != nil {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"success": false, "message_code": "ERR_PARSE_PAYLOAD"})
	}
	if len(payload.Accounts) == 0 || len(payload.Accounts) > 500 {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_INVALID_PARAMS",
			"message":      "accounts 必须包含 1-500 个账号",
		})
	}
	monthlyCost, ok := database.USDToMicro(payload.MonthlyCostUSD)
	if !ok || payload.MonthlyCostUSD < 0 {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_AMOUNT", "message": "monthly_cost_usd 必须是非负 USD 数值"})
	}
	capacity, ok := database.USDToMicro(payload.EstimatedMonthlyCapacityUSD)
	if !ok || payload.EstimatedMonthlyCapacityUSD < 0 {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_AMOUNT", "message": "estimated_monthly_capacity_usd 必须是非负 USD 数值"})
	}
	active := true
	if payload.Active != nil {
		active = *payload.Active
	}
	planName := strings.TrimSpace(payload.PlanName)
	notes := strings.TrimSpace(payload.Notes)

	seen := map[string]struct{}{}
	rows := make([]database.UpstreamAccountCost, 0, len(payload.Accounts))
	for _, account := range payload.Accounts {
		provider := normalizeCostProvider(account.Provider)
		authIndex := strings.TrimSpace(account.AuthIndex)
		if provider == "" || authIndex == "" {
			return c.Status(http.StatusBadRequest).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_PARAMS", "message": "每个 account 都必须包含 provider 和 auth_index"})
		}
		key := accountCostKey(provider, authIndex)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		rows = append(rows, database.UpstreamAccountCost{
			Provider:                    provider,
			AuthIndex:                   authIndex,
			AuthType:                    strings.TrimSpace(account.AuthType),
			Label:                       strings.TrimSpace(account.Label),
			PlanName:                    planName,
			MonthlyCostUSD:              monthlyCost,
			EstimatedMonthlyCapacityUSD: capacity,
			Active:                      active,
			Notes:                       notes,
		})
	}
	if len(rows) == 0 {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_PARAMS", "message": "没有可写入的账号"})
	}

	if err := database.DB.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "provider"}, {Name: "auth_index"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"auth_type",
			"label",
			"plan_name",
			"monthly_cost_usd",
			"estimated_monthly_capacity_usd",
			"active",
			"notes",
			"updated_at",
		}),
	}).Create(&rows).Error; err != nil {
		log.Printf("[UPSTREAM-COST] bulk upsert failed count=%d: %v", len(rows), err)
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_WRITE"})
	}
	for i := range rows {
		var fresh database.UpstreamAccountCost
		if err := database.DB.Where("provider = ? AND auth_index = ?", rows[i].Provider, rows[i].AuthIndex).First(&fresh).Error; err != nil {
			log.Printf("[UPSTREAM-COST] bulk refetch failed provider=%s auth=%s: %v", rows[i].Provider, rows[i].AuthIndex, err)
			continue
		}
		rows[i] = fresh
	}
	for _, row := range rows {
		if err := refreshPlatformCostEstimateForAccount(row); err != nil {
			log.Printf("[UPSTREAM-COST] bulk refresh platform estimate failed provider=%s auth=%s: %v", row.Provider, row.AuthIndex, err)
		}
	}
	return c.JSON(fiber.Map{"success": true, "data": fiber.Map{"count": len(rows)}})
}

func parseUpstreamAccountCostPresets(raw string) ([]upstreamAccountCostPreset, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []upstreamAccountCostPreset{}, nil
	}
	var presets []upstreamAccountCostPreset
	if err := json.Unmarshal([]byte(raw), &presets); err != nil {
		return nil, err
	}
	if len(presets) > 100 {
		return nil, fmt.Errorf("最多允许 100 个预设")
	}
	seen := map[string]struct{}{}
	out := make([]upstreamAccountCostPreset, 0, len(presets))
	for i, preset := range presets {
		preset.ID = strings.TrimSpace(preset.ID)
		preset.Label = strings.TrimSpace(preset.Label)
		preset.Provider = normalizeCostProvider(preset.Provider)
		preset.PlanName = strings.TrimSpace(preset.PlanName)
		preset.Notes = strings.TrimSpace(preset.Notes)
		if preset.ID == "" {
			return nil, fmt.Errorf("第 %d 项缺少 id", i+1)
		}
		if preset.Label == "" {
			return nil, fmt.Errorf("第 %d 项缺少 label", i+1)
		}
		if preset.Provider == "" {
			return nil, fmt.Errorf("第 %d 项缺少 provider", i+1)
		}
		if _, exists := seen[preset.ID]; exists {
			return nil, fmt.Errorf("重复的 preset id: %s", preset.ID)
		}
		seen[preset.ID] = struct{}{}
		if _, ok := database.USDToMicro(preset.MonthlyCostUSD); !ok || preset.MonthlyCostUSD < 0 {
			return nil, fmt.Errorf("%s monthly_cost_usd 必须是非负 USD 数值", preset.ID)
		}
		if _, ok := database.USDToMicro(preset.EstimatedMonthlyCapacityUSD); !ok || preset.EstimatedMonthlyCapacityUSD < 0 {
			return nil, fmt.Errorf("%s estimated_monthly_capacity_usd 必须是非负 USD 数值", preset.ID)
		}
		out = append(out, preset)
	}
	return out, nil
}

func UpdateUpstreamAccountCost(c *fiber.Ctx) error {
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil || id <= 0 {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_PARAMS"})
	}
	var existing database.UpstreamAccountCost
	if err := database.DB.First(&existing, id).Error; err != nil {
		return c.Status(http.StatusNotFound).JSON(fiber.Map{"success": false, "message_code": "ERR_ACCOUNT_COST_NOT_FOUND"})
	}
	payload, ok := parseUpstreamAccountCostPayload(c)
	if !ok {
		return nil
	}
	row := upstreamAccountCostFromPayload(payload, existing)
	row.ID = existing.ID
	if err := database.DB.Save(&row).Error; err != nil {
		log.Printf("[UPSTREAM-COST] update failed id=%d: %v", row.ID, err)
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_WRITE"})
	}
	if err := refreshPlatformCostEstimateForAccount(row); err != nil {
		log.Printf("[UPSTREAM-COST] refresh platform estimate failed id=%d: %v", row.ID, err)
	}
	return c.JSON(fiber.Map{"success": true, "data": upstreamAccountCostToOut(row)})
}

func DeleteUpstreamAccountCost(c *fiber.Ctx) error {
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil || id <= 0 {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_PARAMS"})
	}
	if err := database.DB.Delete(&database.UpstreamAccountCost{}, id).Error; err != nil {
		log.Printf("[UPSTREAM-COST] delete failed id=%d: %v", id, err)
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_WRITE"})
	}
	return c.JSON(fiber.Map{"success": true})
}

type upstreamMarginAggRow struct {
	Provider            string
	AuthIndex           string
	AuthType            string
	Requests            int64
	FailedRequests      int64
	InputTokens         int64
	OutputTokens        int64
	RawCost             int64
	SubscriptionRevenue int64 // ApiLogRevenue.effective_revenue WHERE revenue_source='subscription'
	BalanceRevenue      int64 // ApiLogRevenue.effective_revenue WHERE revenue_source='balance'
	TotalRevenue        int64 // = SubscriptionRevenue + BalanceRevenue
	StoredPlatformCost  int64 // ApiLogCostEstimate.platform_cost_micro_usd
}

type upstreamMarginRow struct {
	Provider                    string  `json:"provider"`
	AuthIndex                   string  `json:"auth_index"`
	AuthType                    string  `json:"auth_type"`
	Label                       string  `json:"label"`
	PlanName                    string  `json:"plan_name"`
	AccountID                   uint    `json:"account_id"` // 0 = 未配置；> 0 = UpstreamAccountCost.ID，前端用于 delete 操作
	AccountConfigured           bool    `json:"account_configured"`
	AccountActive               bool    `json:"account_active"`
	Requests                    int64   `json:"requests"`
	FailedRequests              int64   `json:"failed_requests"`
	InputTokens                 int64   `json:"input_tokens"`
	OutputTokens                int64   `json:"output_tokens"`
	RawCostUSD                  float64 `json:"raw_cost_usd"`
	SubscriptionRevenueUSD      float64 `json:"subscription_revenue_usd"` // 订阅扣减口径
	BalanceRevenueUSD           float64 `json:"balance_revenue_usd"`      // 余额扣减口径（= raw 1:1）
	TotalRevenueUSD             float64 `json:"total_revenue_usd"`        // 真实从用户拿到的钱（毛利分子）
	PlatformCostEstimateUSD     float64 `json:"platform_cost_estimate_usd"`
	GrossMarginUSD              float64 `json:"gross_margin_usd"`
	GrossMarginRate             float64 `json:"gross_margin_rate"`
	MonthlyCostUSD              float64 `json:"monthly_cost_usd"`
	EstimatedMonthlyCapacityUSD float64 `json:"estimated_monthly_capacity_usd"`
	ProratedCapacityUSD         float64 `json:"prorated_capacity_usd"`
	CapacityUtilization         float64 `json:"capacity_utilization"`
	MissingCostConfig           bool    `json:"missing_cost_config"`
	CostBasis                   string  `json:"cost_basis"`
}

func GetUpstreamMarginReport(c *fiber.Ctx) error {
	period := c.Query("period", "7d")
	cutoff := resolvePeriodCutoff(period)
	q := database.DB.Model(&database.ApiLog{})
	if !cutoff.IsZero() {
		q = q.Where("api_logs.created_at >= ?", cutoff)
	}

	var rows []upstreamMarginAggRow
	if err := q.
		Joins("LEFT JOIN api_log_attributions ala ON ala.api_log_id = api_logs.id").
		Joins("LEFT JOIN api_log_cost_estimates ace ON ace.api_log_id = api_logs.id").
		Joins("LEFT JOIN api_log_revenues alr ON alr.api_log_id = api_logs.id").
		Select(`CASE
			WHEN ala.upstream_provider IS NOT NULL AND ala.upstream_provider <> '' THEN ala.upstream_provider
			WHEN api_logs.upstream_provider IS NOT NULL AND api_logs.upstream_provider <> '' THEN api_logs.upstream_provider
			ELSE 'unknown'
		END AS provider,
		COALESCE(NULLIF(ala.upstream_account_auth_index, ''), NULLIF(api_logs.upstream_auth_index, ''), '') AS auth_index,
		COALESCE(NULLIF(ala.upstream_auth_type, ''), NULLIF(api_logs.upstream_auth_type, ''), '') AS auth_type,
		COUNT(*) AS requests,
		SUM(CASE WHEN api_logs.status < 200 OR api_logs.status >= 300 THEN 1 ELSE 0 END) AS failed_requests,
		COALESCE(SUM(api_logs.prompt_tokens), 0) AS input_tokens,
		COALESCE(SUM(api_logs.completion_tokens), 0) AS output_tokens,
		COALESCE(SUM(api_logs.cost), 0) AS raw_cost,
		COALESCE(SUM(CASE WHEN alr.revenue_source = 'subscription' THEN alr.effective_revenue_micro_usd ELSE 0 END), 0) AS subscription_revenue,
		COALESCE(SUM(CASE WHEN alr.revenue_source = 'balance'      THEN alr.effective_revenue_micro_usd ELSE 0 END), 0) AS balance_revenue,
		COALESCE(SUM(alr.effective_revenue_micro_usd), 0) AS total_revenue,
		COALESCE(SUM(CASE WHEN ace.platform_cost_micro_usd IS NULL THEN api_logs.cost ELSE ace.platform_cost_micro_usd END), 0) AS stored_platform_cost`).
		Group("provider, auth_index, auth_type").
		Order("raw_cost DESC").
		Scan(&rows).Error; err != nil {
		log.Printf("[UPSTREAM-MARGIN] aggregate failed: %v", err)
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}

	accounts, err := loadUpstreamAccountCostMap()
	if err != nil {
		log.Printf("[UPSTREAM-MARGIN] account map failed: %v", err)
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}
	windowDays := marginReportWindowDays(period, cutoff)
	out := make([]upstreamMarginRow, 0, len(rows))
	var totalRaw, totalSubRevenue, totalBalRevenue, totalRevenue, totalPlatform, totalRequests, totalFailed int64
	configuredRequests := int64(0)
	for _, r := range rows {
		provider := normalizeCostProvider(r.Provider)
		authIndex := strings.TrimSpace(r.AuthIndex)
		acct, configured := accounts[accountCostKey(provider, authIndex)]
		platformCost, costBasis := estimatePlatformCostForAggregate(r.RawCost, r.StoredPlatformCost, acct, configured)
		gross := r.TotalRevenue - platformCost
		proratedCapacity := proratedCapacityMicro(acct.EstimatedMonthlyCapacityUSD, windowDays)
		utilization := 0.0
		if proratedCapacity > 0 {
			utilization = float64(r.RawCost) / float64(proratedCapacity)
		}
		if configured {
			configuredRequests += r.Requests
		}
		totalRaw += r.RawCost
		totalSubRevenue += r.SubscriptionRevenue
		totalBalRevenue += r.BalanceRevenue
		totalRevenue += r.TotalRevenue
		totalPlatform += platformCost
		totalRequests += r.Requests
		totalFailed += r.FailedRequests
		out = append(out, upstreamMarginRow{
			Provider:                    provider,
			AuthIndex:                   authIndex,
			AuthType:                    firstNonEmpty(r.AuthType, acct.AuthType),
			Label:                       acct.Label,
			PlanName:                    acct.PlanName,
			AccountID:                   acct.ID,
			AccountConfigured:           configured,
			AccountActive:               configured && acct.Active,
			Requests:                    r.Requests,
			FailedRequests:              r.FailedRequests,
			InputTokens:                 r.InputTokens,
			OutputTokens:                r.OutputTokens,
			RawCostUSD:                  database.MicroToUSD(r.RawCost),
			SubscriptionRevenueUSD:      database.MicroToUSD(r.SubscriptionRevenue),
			BalanceRevenueUSD:           database.MicroToUSD(r.BalanceRevenue),
			TotalRevenueUSD:             database.MicroToUSD(r.TotalRevenue),
			PlatformCostEstimateUSD:     database.MicroToUSD(platformCost),
			GrossMarginUSD:              database.MicroToUSD(gross),
			GrossMarginRate:             marginRate(gross, r.TotalRevenue),
			MonthlyCostUSD:              database.MicroToUSD(acct.MonthlyCostUSD),
			EstimatedMonthlyCapacityUSD: database.MicroToUSD(acct.EstimatedMonthlyCapacityUSD),
			ProratedCapacityUSD:         database.MicroToUSD(proratedCapacity),
			CapacityUtilization:         utilization,
			MissingCostConfig:           !configured || acct.MonthlyCostUSD <= 0 || acct.EstimatedMonthlyCapacityUSD <= 0,
			CostBasis:                   costBasis,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].MissingCostConfig != out[j].MissingCostConfig {
			return out[i].MissingCostConfig
		}
		return out[i].RawCostUSD > out[j].RawCostUSD
	})

	return c.JSON(fiber.Map{
		"success": true,
		"data": fiber.Map{
			"period":      period,
			"window_days": windowDays,
			"summary": fiber.Map{
				"requests":                     totalRequests,
				"failed_requests":              totalFailed,
				"raw_cost_usd":                 database.MicroToUSD(totalRaw),
				"subscription_revenue_usd":     database.MicroToUSD(totalSubRevenue),
				"balance_revenue_usd":          database.MicroToUSD(totalBalRevenue),
				"total_revenue_usd":            database.MicroToUSD(totalRevenue),
				"platform_cost_estimate_usd":   database.MicroToUSD(totalPlatform),
				"gross_margin_usd":             database.MicroToUSD(totalRevenue - totalPlatform),
				"gross_margin_rate":            marginRate(totalRevenue-totalPlatform, totalRevenue),
				"configured_request_ratio":     ratio(configuredRequests, totalRequests),
				"unconfigured_request_count":   totalRequests - configuredRequests,
				"configured_account_row_count": configuredAccountRows(out),
			},
			"rows": out,
		},
	})
}

// staleUpstreamAccountRow 表示一条"本地配置过但 CPA 那边已删/失效"的孤儿配置。
type staleUpstreamAccountRow struct {
	ID                          uint       `json:"id"`
	Provider                    string     `json:"provider"`
	AuthIndex                   string     `json:"auth_index"`
	Label                       string     `json:"label"`
	PlanName                    string     `json:"plan_name"`
	MonthlyCostUSD              float64    `json:"monthly_cost_usd"`
	EstimatedMonthlyCapacityUSD float64    `json:"estimated_monthly_capacity_usd"`
	Active                      bool       `json:"active"`
	Notes                       string     `json:"notes"`
	StaleReason                 string     `json:"stale_reason"` // not_in_cpa | cpa_disabled | cpa_unseen_7d
	CPALastSeenAt               *time.Time `json:"cpa_last_seen_at,omitempty"`
	CPADisabled                 bool       `json:"cpa_disabled"`
	CreatedAt                   time.Time  `json:"created_at"`
	UpdatedAt                   time.Time  `json:"updated_at"`
}

// ListStaleUpstreamAccountCosts 列出"本地 UpstreamAccountCost 配置过但 CPA 端已消失/失效"
// 的孤儿配置，便于 admin 清理。
//
// 对账 key：UpstreamAccountCost.auth_index == CPACredential.auth_id。
// 三类 stale：
//   - not_in_cpa     : CPACredential 中找不到匹配 auth_id（CPA 那边已彻底删除）
//   - cpa_disabled   : CPACredential 标记 disabled=true（CPA 那边软禁用）
//   - cpa_unseen_7d  : CPACredential 的 last_seen_at < now-7d（一周以上没出现在 CPA 清单）
func ListStaleUpstreamAccountCosts(c *fiber.Ctx) error {
	type joinedRow struct {
		ID                          uint
		Provider                    string
		AuthIndex                   string
		Label                       string
		PlanName                    string
		MonthlyCostUSD              int64
		EstimatedMonthlyCapacityUSD int64
		Active                      bool
		Notes                       string
		CreatedAt                   time.Time
		UpdatedAt                   time.Time
		CPAAuthID                   *string
		CPADisabled                 *bool
		CPALastSeenAt               *time.Time
	}
	var rows []joinedRow
	if err := database.DB.Table("upstream_account_costs AS uac").
		Select(`uac.id, uac.provider, uac.auth_index, uac.label, uac.plan_name,
			uac.monthly_cost_usd, uac.estimated_monthly_capacity_usd, uac.active, uac.notes,
			uac.created_at, uac.updated_at,
			cc.auth_id AS cpa_auth_id, cc.disabled AS cpa_disabled, cc.last_seen_at AS cpa_last_seen_at`).
		Joins("LEFT JOIN cpa_credentials cc ON cc.auth_id = uac.auth_index").
		Order("uac.provider ASC, uac.auth_index ASC").
		Scan(&rows).Error; err != nil {
		log.Printf("[UPSTREAM-STALE] query failed: %v", err)
		return c.Status(http.StatusInternalServerError).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}

	staleCutoff := time.Now().Add(-7 * 24 * time.Hour)
	out := make([]staleUpstreamAccountRow, 0)
	for _, r := range rows {
		reason := ""
		if r.CPAAuthID == nil {
			reason = "not_in_cpa"
		} else if r.CPADisabled != nil && *r.CPADisabled {
			reason = "cpa_disabled"
		} else if r.CPALastSeenAt != nil && r.CPALastSeenAt.Before(staleCutoff) {
			reason = "cpa_unseen_7d"
		}
		if reason == "" {
			continue
		}
		disabled := false
		if r.CPADisabled != nil {
			disabled = *r.CPADisabled
		}
		out = append(out, staleUpstreamAccountRow{
			ID:                          r.ID,
			Provider:                    r.Provider,
			AuthIndex:                   r.AuthIndex,
			Label:                       r.Label,
			PlanName:                    r.PlanName,
			MonthlyCostUSD:              database.MicroToUSD(r.MonthlyCostUSD),
			EstimatedMonthlyCapacityUSD: database.MicroToUSD(r.EstimatedMonthlyCapacityUSD),
			Active:                      r.Active,
			Notes:                       r.Notes,
			StaleReason:                 reason,
			CPALastSeenAt:               r.CPALastSeenAt,
			CPADisabled:                 disabled,
			CreatedAt:                   r.CreatedAt,
			UpdatedAt:                   r.UpdatedAt,
		})
	}
	return c.JSON(fiber.Map{"success": true, "data": out})
}

func parseUpstreamAccountCostPayload(c *fiber.Ctx) (upstreamAccountCostPayload, bool) {
	var payload upstreamAccountCostPayload
	if err := c.BodyParser(&payload); err != nil {
		_ = c.Status(http.StatusBadRequest).JSON(fiber.Map{"success": false, "message_code": "ERR_PARSE_PAYLOAD"})
		return payload, false
	}
	payload.Provider = normalizeCostProvider(payload.Provider)
	payload.AuthIndex = strings.TrimSpace(payload.AuthIndex)
	payload.AuthType = strings.TrimSpace(payload.AuthType)
	payload.Label = strings.TrimSpace(payload.Label)
	payload.PlanName = strings.TrimSpace(payload.PlanName)
	payload.Notes = strings.TrimSpace(payload.Notes)
	if payload.Provider == "" || payload.AuthIndex == "" {
		_ = c.Status(http.StatusBadRequest).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_PARAMS", "message": "provider 和 auth_index 必填"})
		return payload, false
	}
	monthlyCost, ok := database.USDToMicro(payload.MonthlyCostUSD)
	if !ok || payload.MonthlyCostUSD < 0 {
		_ = c.Status(http.StatusBadRequest).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_AMOUNT", "message": "monthly_cost_usd 必须是非负 USD 数值"})
		return payload, false
	}
	capacity, ok := database.USDToMicro(payload.EstimatedMonthlyCapacityUSD)
	if !ok || payload.EstimatedMonthlyCapacityUSD < 0 {
		_ = c.Status(http.StatusBadRequest).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_AMOUNT", "message": "estimated_monthly_capacity_usd 必须是非负 USD 数值"})
		return payload, false
	}
	payload.monthlyCostMicroUSD = monthlyCost
	payload.estimatedMonthlyCapacityMicroUSD = capacity
	return payload, true
}

func upstreamAccountCostFromPayload(payload upstreamAccountCostPayload, existing database.UpstreamAccountCost) database.UpstreamAccountCost {
	active := existing.Active
	if payload.Active != nil {
		active = *payload.Active
	} else if existing.ID == 0 {
		active = true
	}
	existing.Provider = payload.Provider
	existing.AuthIndex = payload.AuthIndex
	existing.AuthType = payload.AuthType
	existing.Label = payload.Label
	existing.PlanName = payload.PlanName
	existing.MonthlyCostUSD = payload.monthlyCostMicroUSD
	existing.EstimatedMonthlyCapacityUSD = payload.estimatedMonthlyCapacityMicroUSD
	existing.Active = active
	existing.Notes = payload.Notes
	return existing
}

func upstreamAccountCostToOut(row database.UpstreamAccountCost) upstreamAccountCostOut {
	return upstreamAccountCostOut{
		ID:                               row.ID,
		Provider:                         row.Provider,
		AuthIndex:                        row.AuthIndex,
		AuthType:                         row.AuthType,
		Label:                            row.Label,
		PlanName:                         row.PlanName,
		MonthlyCostMicroUSD:              row.MonthlyCostUSD,
		EstimatedMonthlyCapacityMicroUSD: row.EstimatedMonthlyCapacityUSD,
		Active:                           row.Active,
		Notes:                            row.Notes,
		CreatedAt:                        row.CreatedAt,
		UpdatedAt:                        row.UpdatedAt,
	}
}

func loadUpstreamAccountCostMap() (map[string]database.UpstreamAccountCost, error) {
	var accounts []database.UpstreamAccountCost
	if err := database.DB.Find(&accounts).Error; err != nil {
		return nil, err
	}
	out := make(map[string]database.UpstreamAccountCost, len(accounts))
	for _, acct := range accounts {
		out[accountCostKey(acct.Provider, acct.AuthIndex)] = acct
	}
	return out, nil
}

func estimatePlatformCostForAggregate(rawCost, storedPlatformCost int64, acct database.UpstreamAccountCost, configured bool) (int64, string) {
	if configured && acct.Active && rawCost > 0 && acct.MonthlyCostUSD > 0 && acct.EstimatedMonthlyCapacityUSD > 0 {
		return estimatePlatformCostFromAccount(rawCost, acct), "account_capacity"
	}
	if storedPlatformCost > 0 {
		return storedPlatformCost, "stored_estimate"
	}
	if rawCost > 0 {
		return rawCost, "raw_cost_conservative"
	}
	return 0, "zero"
}

func estimatePlatformCostFromAccount(rawCost int64, acct database.UpstreamAccountCost) int64 {
	if rawCost <= 0 || acct.MonthlyCostUSD <= 0 || acct.EstimatedMonthlyCapacityUSD <= 0 {
		return 0
	}
	value := roundedPositiveProductRatio(rawCost, acct.MonthlyCostUSD, acct.EstimatedMonthlyCapacityUSD)
	if value < 1 {
		return 1
	}
	return value
}

func refreshPlatformCostEstimateForAccount(acct database.UpstreamAccountCost) error {
	if acct.MonthlyCostUSD <= 0 || acct.EstimatedMonthlyCapacityUSD <= 0 || !acct.Active {
		return nil
	}
	start := time.Now()
	type row struct {
		ID   uint
		Cost int64
	}
	var rows []row
	provider := normalizeCostProvider(acct.Provider)
	authIndex := strings.TrimSpace(acct.AuthIndex)
	if err := database.DB.Table("api_logs").
		Select("api_logs.id, api_logs.cost").
		Joins("LEFT JOIN api_log_attributions ala ON ala.api_log_id = api_logs.id").
		Where(`api_logs.cost > 0 AND (
			(api_logs.upstream_provider = ? AND api_logs.upstream_auth_index = ?)
			OR (ala.upstream_provider = ? AND ala.upstream_account_auth_index = ?)
		)`, provider, authIndex, provider, authIndex).
		Find(&rows).Error; err != nil {
		return err
	}
	err := database.DB.Transaction(func(tx *gorm.DB) error {
		computedAt := time.Now()
		for _, r := range rows {
			estimate := estimatePlatformCostFromAccount(r.Cost, acct)
			if _, err := insertApiLogCostEstimateTx(tx, r.ID, estimate, "capacity_share", computedAt); err != nil {
				return err
			}
		}
		return nil
	})
	elapsed := time.Since(start)
	if elapsed > 5*time.Second {
		log.Printf("[UPSTREAM-COST] refresh platform estimates slow provider=%s auth=%s rows=%d elapsed=%s stack=%s", provider, authIndex, len(rows), elapsed, debug.Stack())
	} else {
		log.Printf("[UPSTREAM-COST] refresh platform estimates provider=%s auth=%s rows=%d elapsed=%s", provider, authIndex, len(rows), elapsed)
	}
	return err
}

func insertApiLogCostEstimateTx(tx *gorm.DB, apiLogID uint, estimate int64, method string, computedAt time.Time) (bool, error) {
	if apiLogID == 0 || estimate <= 0 {
		return false, nil
	}
	row := database.ApiLogCostEstimate{
		ApiLogID:             apiLogID,
		PlatformCostMicroUSD: estimate,
		ComputedAt:           computedAt,
		Method:               strings.TrimSpace(method),
	}
	if row.Method == "" {
		row.Method = "capacity_share"
	}
	res := tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "api_log_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"platform_cost_micro_usd",
			"computed_at",
		}),
	}).Create(&row)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

func platformCostEstimateForMatchedLogTx(tx *gorm.DB, provider, authIndex string, rawCost int64) int64 {
	if rawCost <= 0 || strings.TrimSpace(authIndex) == "" {
		return 0
	}
	var acct database.UpstreamAccountCost
	err := tx.Where("provider = ? AND auth_index = ? AND active = ?", normalizeCostProvider(provider), strings.TrimSpace(authIndex), true).
		First(&acct).Error
	if err != nil {
		return 0
	}
	return estimatePlatformCostFromAccount(rawCost, acct)
}

func accountCostKey(provider, authIndex string) string {
	return normalizeCostProvider(provider) + "\x00" + strings.TrimSpace(authIndex)
}

func normalizeCostProvider(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}

func marginReportWindowDays(period string, cutoff time.Time) float64 {
	switch period {
	case "24h":
		return 1
	case "7d":
		return 7
	case "30d":
		return 30
	default:
		if cutoff.IsZero() {
			var minCreated time.Time
			if err := database.DB.Model(&database.ApiLog{}).Select("MIN(created_at)").Scan(&minCreated).Error; err == nil && !minCreated.IsZero() {
				days := time.Since(minCreated).Hours() / 24
				if days >= 1 {
					return days
				}
			}
		}
		return 30
	}
}

func proratedCapacityMicro(monthlyCapacity int64, windowDays float64) int64 {
	if monthlyCapacity <= 0 || windowDays <= 0 {
		return 0
	}
	if math.IsNaN(windowDays) || math.IsInf(windowDays, 0) {
		return 0
	}
	daysRat := new(big.Rat).SetFloat64(windowDays)
	if daysRat == nil || daysRat.Sign() <= 0 {
		return 0
	}
	value := new(big.Rat).Mul(new(big.Rat).SetInt64(monthlyCapacity), daysRat)
	value.Quo(value, big.NewRat(30, 1))
	out, ok := roundedPositiveRatToInt64(value)
	if !ok {
		return math.MaxInt64
	}
	return out
}

func roundedPositiveProductRatio(amount, multiplier, divisor int64) int64 {
	if amount <= 0 || multiplier <= 0 || divisor <= 0 {
		return 0
	}
	product := new(big.Int).Mul(big.NewInt(amount), big.NewInt(multiplier))
	q := new(big.Int)
	rem := new(big.Int)
	div := big.NewInt(divisor)
	q.QuoRem(product, div, rem)
	rem.Mul(rem, big.NewInt(2))
	if rem.Cmp(div) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	if !q.IsInt64() {
		return math.MaxInt64
	}
	return q.Int64()
}

func roundedPositiveRatToInt64(r *big.Rat) (int64, bool) {
	if r == nil || r.Sign() < 0 {
		return 0, false
	}
	q := new(big.Int)
	rem := new(big.Int)
	q.QuoRem(r.Num(), r.Denom(), rem)
	rem.Mul(rem, big.NewInt(2))
	if rem.Cmp(r.Denom()) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	if !q.IsInt64() {
		return 0, false
	}
	return q.Int64(), true
}

func marginRate(margin, revenue int64) float64 {
	if revenue <= 0 {
		return 0
	}
	return float64(margin) / float64(revenue)
}

func ratio(part, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(part) / float64(total)
}

func configuredAccountRows(rows []upstreamMarginRow) int {
	n := 0
	for _, row := range rows {
		if row.AccountConfigured {
			n++
		}
	}
	return n
}
