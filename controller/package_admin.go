// Package controller / package_admin.go
//
// 销售套餐 (Package) 的 admin CRUD + 套餐 ↔ 配额计划 关联管理。
package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
	"time"

	"daof-cpa/database"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// MaxQuantityMultiplier PlanMultipliers 上限：100x。
//
// fix MAJOR M5（codex 第二十轮）：原 PlanMultipliers 只过滤 ≤0 静默 fallback 到 1.0，
// NaN/Inf/超大值（1e308）落库后让订阅引擎计算 effectiveLimit/effectiveDelta 时溢出，
// 破坏额度守恒。修复：必须 finite + (0, 100] 范围内，非法直接 400 而非静默 fallback。
const MaxQuantityMultiplier = 100.0

var errDeprecatedRequestField = errors.New("deprecated request field")
var errInvalidProductType = errors.New("product_type only supports subscription")
var errPackageCostFloorInvalid = errors.New("package cost_floor invalid")
var errReorderStaleID = errors.New("reorder contains stale id")

// 直接使用常量字面量，i18n 覆盖测试可通过 AST 扫描捕获，避免遗漏翻译。
const MessageCodePackageCostFloorInvalid = "ERR_PACKAGE_COST_FLOOR_INVALID"

// validatePlanMultipliers 校验 plan_multipliers 与 plan_ids 一一对应的合法性。
// 缺失（len 不足）视为 1.0 默认；显式传值必须 finite + 0 < v ≤ 100。
func validatePlanMultipliers(planIDs []uint, multipliers []float64) error {
	for i, m := range multipliers {
		if i >= len(planIDs) {
			break // 多余值忽略（与原行为一致）
		}
		if math.IsNaN(m) || math.IsInf(m, 0) {
			return fmt.Errorf("plan_multipliers[%d] 必须为有限数（NaN/Inf 非法）", i)
		}
		if m <= 0 {
			return fmt.Errorf("plan_multipliers[%d] 必须 > 0（当前 %v）", i, m)
		}
		if m > MaxQuantityMultiplier {
			return fmt.Errorf("plan_multipliers[%d] 超过上限 %v（当前 %v）", i, MaxQuantityMultiplier, m)
		}
	}
	return nil
}

// MaxBillingPeriodSeconds billing 周期上限：5 年（含润年安全余量）。
//
// fix CRITICAL C4（codex 第二十轮）：原仅校验 >0，无上限 →
// admin 误填 9223372036（int64 上限附近）+ time.Duration(seconds)*time.Second 整数溢出，
// 生成已过期/异常订阅，攻击者可构造任意时间戳的订阅打破续费/退款公式。
// 上限选 5 年 = 5 * 366 * 86400 = 158,112,000 秒，覆盖所有合理订阅周期。
const MaxBillingPeriodSeconds = 5 * 366 * 86400

// validatePackagePayload 套餐字段边界校验。任何字段非法即拒绝写入。
//
// fix Minor（codex 第五轮）：原 CreatePackage 未挡住
// 负数价格、零/负 billing 周期、负叠加上限等。这些值落库后会被购买路径放行，
// 形成 "免费套餐 / 立即过期 / 无叠加上限" 的非法状态。
//
// fix MAJOR M22-A1 Phase 1：PriceAmount 已是 int64 micro_usd，
// NaN/Inf 在反序列化前由前端 JSON 解析挡掉，这里只做范围校验。
func validatePackagePayload(p *database.Package) error {
	if p.PriceAmount < 0 {
		return fmt.Errorf("price_amount 不能为负数")
	}
	if p.CostFloorMicroUSD < 0 {
		return fmt.Errorf("%w: cost_floor_micro_usd 不能为负数", errPackageCostFloorInvalid)
	}
	if p.CostFloorMicroUSD > p.PriceAmount {
		return fmt.Errorf("%w: cost_floor_micro_usd 不能高于 price_amount", errPackageCostFloorInvalid)
	}
	if p.BillingPeriodSeconds <= 0 {
		return fmt.Errorf("billing_period_seconds 必须为正整数")
	}
	if p.BillingPeriodSeconds > MaxBillingPeriodSeconds {
		// fix CRITICAL C4：防 time.Duration 整数溢出 + 防异常订阅周期
		return fmt.Errorf("billing_period_seconds 超过上限 %d（约 5 年），请检查输入",
			MaxBillingPeriodSeconds)
	}
	if p.MaxActivePerUser < 0 {
		return fmt.Errorf("max_active_per_user 不能为负数（0 = 不限）")
	}
	if p.SortOrder < 0 {
		return fmt.Errorf("sort_order 不能为负数")
	}
	return nil
}

func packageValidationMessageCode(err error) string {
	if errors.Is(err, errPackageCostFloorInvalid) {
		return MessageCodePackageCostFloorInvalid
	}
	return "ERR_INVALID_PACKAGE"
}

func validatePackageProductType(productType string) error {
	if productType != "subscription" {
		return errInvalidProductType
	}
	return nil
}

// packageWithPlans 是返回给前端的扩展结构，含 plans 列表
type packageWithPlans struct {
	packageResponse
	Plans []packagePlanItem `json:"plans"`
}

type packagePlanItem struct {
	database.PackagePlan
	Plan database.QuotaPlan `json:"plan"`
}

type publicPackagePlanItem struct {
	ID        uint            `json:"id"`
	SortOrder int             `json:"sort_order"`
	Plan      publicPlanBrief `json:"plan"`
}

type publicPackageResponse struct {
	ID                   uint      `json:"id"`
	Name                 string    `json:"name"`
	Description          string    `json:"description"`
	ProductType          string    `json:"product_type"`
	IconKey              string    `json:"icon_key"`
	BadgeColor           string    `json:"badge_color"`
	Gradient             string    `json:"gradient"`
	HighlightTag         string    `json:"highlight_tag"`
	PriceAmount          float64   `json:"price_amount"`
	PriceCurrency        string    `json:"price_currency"`
	BillingPeriodSeconds int       `json:"billing_period_seconds"`
	Stackable            *bool     `json:"stackable"`
	MaxActivePerUser     int       `json:"max_active_per_user"`
	PurchaseWhenOwned    string    `json:"purchase_when_owned"`
	Public               bool      `json:"public"`
	SortOrder            int       `json:"sort_order"`
	Enabled              *bool     `json:"enabled"`
	ExtraConfig          string    `json:"extra_config"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

type publicPlanBrief struct {
	Name          string  `json:"name"`
	DisplayName   string  `json:"display_name"`
	Description   string  `json:"description"`
	LimitValue    float64 `json:"limit_value"`
	LimitUnit     string  `json:"limit_unit"`
	LimitLabel    string  `json:"limit_label"`
	WindowSeconds int     `json:"window_seconds"`
}

func publicPlanBriefFrom(plan database.QuotaPlan) publicPlanBrief {
	label := publicQuotaUnitLabel(plan.LimitUnit)
	return publicPlanBrief{
		Name:          plan.Name,
		DisplayName:   plan.DisplayName,
		Description:   plan.Description,
		LimitValue:    plan.LimitValue,
		LimitUnit:     label,
		LimitLabel:    label,
		WindowSeconds: plan.WindowSeconds,
	}
}

func publicQuotaUnitLabel(unit string) string {
	switch strings.ToLower(strings.TrimSpace(unit)) {
	case "api_cost_usd":
		return "API 等值额度"
	case "request_count":
		return "次调用"
	case "input_tokens", "output_tokens", "total_tokens", "weighted_tokens":
		return "Tokens"
	default:
		return "额度"
	}
}

type packageResponse struct {
	ID                   uint      `json:"id"`
	Name                 string    `json:"name"`
	Description          string    `json:"description"`
	ProductType          string    `json:"product_type"`
	IconKey              string    `json:"icon_key"`
	BadgeColor           string    `json:"badge_color"`
	Gradient             string    `json:"gradient"`
	HighlightTag         string    `json:"highlight_tag"`
	PriceAmount          float64   `json:"price_amount"`
	CostFloorMicroUSD    int64     `json:"cost_floor_micro_usd"`
	PriceCurrency        string    `json:"price_currency"`
	BillingPeriodSeconds int       `json:"billing_period_seconds"`
	Stackable            *bool     `json:"stackable"`
	MaxActivePerUser     int       `json:"max_active_per_user"`
	PurchaseWhenOwned    string    `json:"purchase_when_owned"`
	Public               bool      `json:"public"`
	SortOrder            int       `json:"sort_order"`
	Enabled              *bool     `json:"enabled"`
	ExtraConfig          string    `json:"extra_config"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

func packageResponseFrom(p database.Package) packageResponse {
	return packageResponse{
		ID:                   p.ID,
		Name:                 p.Name,
		Description:          p.Description,
		ProductType:          p.ProductType,
		IconKey:              p.IconKey,
		BadgeColor:           p.BadgeColor,
		Gradient:             p.Gradient,
		HighlightTag:         p.HighlightTag,
		PriceAmount:          database.MicroToUSD(p.PriceAmount),
		CostFloorMicroUSD:    p.CostFloorMicroUSD,
		PriceCurrency:        p.PriceCurrency,
		BillingPeriodSeconds: p.BillingPeriodSeconds,
		Stackable:            p.Stackable,
		MaxActivePerUser:     p.MaxActivePerUser,
		PurchaseWhenOwned:    p.PurchaseWhenOwned,
		Public:               p.Public,
		SortOrder:            p.SortOrder,
		Enabled:              p.Enabled,
		ExtraConfig:          p.ExtraConfig,
		CreatedAt:            p.CreatedAt,
		UpdatedAt:            p.UpdatedAt,
	}
}

func publicPackageResponseFrom(p database.Package) publicPackageResponse {
	r := packageResponseFrom(p)
	return publicPackageResponse{
		ID:                   r.ID,
		Name:                 r.Name,
		Description:          r.Description,
		ProductType:          r.ProductType,
		IconKey:              r.IconKey,
		BadgeColor:           r.BadgeColor,
		Gradient:             r.Gradient,
		HighlightTag:         r.HighlightTag,
		PriceAmount:          r.PriceAmount,
		PriceCurrency:        r.PriceCurrency,
		BillingPeriodSeconds: r.BillingPeriodSeconds,
		Stackable:            r.Stackable,
		MaxActivePerUser:     r.MaxActivePerUser,
		PurchaseWhenOwned:    r.PurchaseWhenOwned,
		Public:               r.Public,
		SortOrder:            r.SortOrder,
		Enabled:              r.Enabled,
		ExtraConfig:          r.ExtraConfig,
		CreatedAt:            r.CreatedAt,
		UpdatedAt:            r.UpdatedAt,
	}
}

// loadPackageWithPlans 加载套餐 + 关联 plans。一次性 IN 查询避免 N+1。
//
// fix MAJOR（codex 第十七轮）：所有 DB 错误必须冒泡。原实现 PackagePlan/QuotaPlan
// 查询失败被静默吞掉，admin 详情页可能展示空 plans 让人误以为套餐没绑定 plan。
func loadPackageWithPlans(pkgID uint) (*packageWithPlans, error) {
	var pkg database.Package
	if err := database.DB.First(&pkg, pkgID).Error; err != nil {
		return nil, fmt.Errorf("load package: %w", err)
	}
	out := &packageWithPlans{packageResponse: packageResponseFrom(pkg), Plans: []packagePlanItem{}}
	var pps []database.PackagePlan
	if err := database.DB.Where("package_id = ?", pkgID).Order("sort_order asc, id asc").Find(&pps).Error; err != nil {
		return nil, fmt.Errorf("load package plans: %w", err)
	}
	if len(pps) == 0 {
		return out, nil
	}
	planIDs := make([]uint, 0, len(pps))
	for _, pp := range pps {
		planIDs = append(planIDs, pp.QuotaPlanID)
	}
	var plans []database.QuotaPlan
	if err := database.DB.Where("id IN ?", planIDs).Find(&plans).Error; err != nil {
		return nil, fmt.Errorf("load quota plans: %w", err)
	}
	planMap := make(map[uint]database.QuotaPlan, len(plans))
	for _, p := range plans {
		planMap[p.ID] = p
	}
	for _, pp := range pps {
		if plan, ok := planMap[pp.QuotaPlanID]; ok {
			out.Plans = append(out.Plans, packagePlanItem{PackagePlan: pp, Plan: plan})
		}
	}
	return out, nil
}

// ListPackagesAdmin admin 全量套餐列表
func ListPackagesAdmin(c *fiber.Ctx) error {
	q := database.DB.Model(&database.Package{}).Order("sort_order asc, id desc")
	if v := c.Query("enabled"); v == "1" {
		q = q.Where("enabled = ?", true)
	}
	var pkgs []database.Package
	if err := q.Find(&pkgs).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}
	type item struct {
		packageResponse
		PlanCount       int   `json:"plan_count"`
		ActiveSubsCount int64 `json:"active_subs_count"`
	}
	if len(pkgs) == 0 {
		return c.JSON(fiber.Map{"success": true, "data": []item{}})
	}
	pkgIDs := make([]uint, 0, len(pkgs))
	for _, p := range pkgs {
		pkgIDs = append(pkgIDs, p.ID)
	}
	// 一次聚合查询替代 N 次 Count
	type planCountRow struct {
		PackageID uint
		Cnt       int64
	}
	// fix Minor（codex 第十六轮）：Scan 错误必须冒泡，DB 故障时不能让 admin 看到 plan/sub count=0
	// 误以为可以删套餐
	var planCounts []planCountRow
	if err := database.DB.Model(&database.PackagePlan{}).
		Select("package_id, COUNT(*) as cnt").
		Where("package_id IN ?", pkgIDs).
		Group("package_id").Scan(&planCounts).Error; err != nil {
		log.Printf("[PKG-LIST] aggregate plan counts failed: %v", err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_AGGREGATE"})
	}
	planCountMap := make(map[uint]int64, len(planCounts))
	for _, r := range planCounts {
		planCountMap[r.PackageID] = r.Cnt
	}
	// fix Minor（codex 第十五轮）：active count 必须排除已过期但未结算的行（end_at <= now）
	// 否则 admin 列表"占用份数"虚高，影响"可禁用"判断。
	var subCounts []planCountRow
	if err := database.DB.Model(&database.UserSubscription{}).
		Select("package_id, COUNT(*) as cnt").
		Where("package_id IN ? AND status = ? AND end_at > ?", pkgIDs, "active", time.Now()).
		Group("package_id").Scan(&subCounts).Error; err != nil {
		log.Printf("[PKG-LIST] aggregate sub counts failed: %v", err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_AGGREGATE"})
	}
	subCountMap := make(map[uint]int64, len(subCounts))
	for _, r := range subCounts {
		subCountMap[r.PackageID] = r.Cnt
	}

	out := make([]item, 0, len(pkgs))
	for _, p := range pkgs {
		out = append(out, item{
			packageResponse: packageResponseFrom(p),
			PlanCount:       int(planCountMap[p.ID]),
			ActiveSubsCount: subCountMap[p.ID],
		})
	}
	return c.JSON(fiber.Map{"success": true, "data": out})
}

// GetPackageAdmin 详情含 plans
func GetPackageAdmin(c *fiber.Ctx) error {
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_PARAMS"})
	}
	pkg, err := loadPackageWithPlans(uint(id))
	if err != nil {
		// fix Minor（codex 第十七轮）：区分 NotFound 与 DB 故障，避免 fail-open 假象
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return c.Status(404).JSON(fiber.Map{"success": false, "message_code": "ERR_NOT_FOUND"})
		}
		log.Printf("[PKG-GET] load with plans pkg=%d failed: %v", id, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}
	return c.JSON(fiber.Map{"success": true, "data": pkg})
}

type createPackagePayload struct {
	database.Package
	PlanIDs         []uint    `json:"plan_ids"`
	PlanMultipliers []float64 `json:"plan_multipliers"` // 与 PlanIDs 同序，缺省按 1.0
}

// packagePayloadJSON 是 admin 端 JSON 表示，金额字段使用 USD float，handler 内转 micro_usd。
type packagePayloadJSON struct {
	Name                 string           `json:"name"`
	Description          string           `json:"description"`
	ProductType          string           `json:"product_type"`
	IconKey              string           `json:"icon_key"`
	BadgeColor           string           `json:"badge_color"`
	Gradient             string           `json:"gradient"`
	HighlightTag         string           `json:"highlight_tag"`
	PriceAmount          float64          `json:"price_amount"`
	CostFloorMicroUSD    int64            `json:"cost_floor_micro_usd"`
	PriceCurrency        string           `json:"price_currency"`
	BillingPeriodSeconds int              `json:"billing_period_seconds"`
	Stackable            *bool            `json:"stackable"`
	MaxActivePerUser     int              `json:"max_active_per_user"`
	PurchaseWhenOwned    string           `json:"purchase_when_owned"`
	Public               bool             `json:"public"`
	SortOrder            int              `json:"sort_order"`
	Enabled              *bool            `json:"enabled"`
	ExtraConfig          string           `json:"extra_config"`
	PlanIDs              []uint           `json:"plan_ids"`
	PlanMultipliers      []float64        `json:"plan_multipliers"`
	DeprecatedBonus      *json.RawMessage `json:"bonus_balance_usd"`
}

// parsePackagePayload 解析 admin POST/PUT 套餐 body。
func parsePackagePayload(c *fiber.Ctx) (createPackagePayload, error) {
	var raw packagePayloadJSON
	if err := c.BodyParser(&raw); err != nil {
		return createPackagePayload{}, err
	}
	if raw.DeprecatedBonus != nil {
		return createPackagePayload{}, fmt.Errorf("%w: bonus_balance_usd", errDeprecatedRequestField)
	}
	productType := strings.TrimSpace(raw.ProductType)
	if productType == "" {
		productType = "subscription"
	}
	if err := validateAdminQuotaInput(raw.PriceAmount); err != nil {
		return createPackagePayload{}, fmt.Errorf("price_amount: %w", err)
	}
	priceMicro, ok := database.USDToMicro(raw.PriceAmount)
	if !ok {
		return createPackagePayload{}, fmt.Errorf("price_amount overflow")
	}
	out := createPackagePayload{
		Package: database.Package{
			Name:                 raw.Name,
			Description:          raw.Description,
			ProductType:          productType,
			IconKey:              raw.IconKey,
			BadgeColor:           raw.BadgeColor,
			Gradient:             raw.Gradient,
			HighlightTag:         raw.HighlightTag,
			PriceAmount:          priceMicro,
			CostFloorMicroUSD:    raw.CostFloorMicroUSD,
			PriceCurrency:        raw.PriceCurrency,
			BillingPeriodSeconds: raw.BillingPeriodSeconds,
			Stackable:            raw.Stackable,
			MaxActivePerUser:     raw.MaxActivePerUser,
			PurchaseWhenOwned:    raw.PurchaseWhenOwned,
			Public:               raw.Public,
			SortOrder:            raw.SortOrder,
			Enabled:              raw.Enabled,
			ExtraConfig:          raw.ExtraConfig,
		},
		PlanIDs:         raw.PlanIDs,
		PlanMultipliers: raw.PlanMultipliers,
	}
	return out, nil
}

// CreatePackage admin 创建套餐
func CreatePackage(c *fiber.Ctx) error {
	payload, perr := parsePackagePayload(c)
	if perr != nil {
		if errors.Is(perr, errDeprecatedRequestField) {
			return c.Status(400).JSON(fiber.Map{
				"success":      false,
				"message":      perr.Error(),
				"message_code": "ERR_DEPRECATED_FIELD",
			})
		}
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_PARSE_PAYLOAD"})
	}
	if payload.Name == "" {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "name 必填", "message_code": "ERR_REQUIRED"})
	}
	if err := validatePackageProductType(payload.ProductType); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "产品类型仅支持订阅", "message_code": MessageCodeInvalidProductType})
	}
	if err := validatePackagePayload(&payload.Package); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": err.Error(), "message_code": packageValidationMessageCode(err)})
	}
	// fix MAJOR M5（codex 第二十轮）：plan_multipliers 必须 finite + 上限校验，非法 400
	if err := validatePlanMultipliers(payload.PlanIDs, payload.PlanMultipliers); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": err.Error(), "message_code": "ERR_INVALID_MULTIPLIER"})
	}
	if payload.ExtraConfig == "" {
		payload.ExtraConfig = "{}"
	}

	err := database.DB.Transaction(func(tx *gorm.DB) error {
		// 注：Package.Stackable / Enabled 已改为 `*bool`（自审第十三轮），
		// admin 显式传 `false` 时 payload.Stackable=&false → Create 写入 false；
		// 不传时 payload.Stackable=nil → DB default true 生效。无需 Select("*")。
		if err := tx.Create(&payload.Package).Error; err != nil {
			return err
		}
		for i, pid := range payload.PlanIDs {
			mult := 1.0
			if i < len(payload.PlanMultipliers) && payload.PlanMultipliers[i] > 0 {
				mult = payload.PlanMultipliers[i]
			}
			pp := database.PackagePlan{
				PackageID:          payload.Package.ID,
				QuotaPlanID:        pid,
				QuantityMultiplier: mult,
				SortOrder:          i,
			}
			if err := tx.Create(&pp).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "message": err.Error(), "message_code": "ERR_DB_CREATE"})
	}
	// fix MAJOR（codex 第十七轮）：helper 错误必须冒泡，否则 admin 看到 success 但 data 为 nil
	out, loadErr := loadPackageWithPlans(payload.Package.ID)
	if loadErr != nil {
		log.Printf("[PKG-CREATE] load with plans pkg=%d failed: %v", payload.Package.ID, loadErr)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}
	return c.JSON(fiber.Map{"success": true, "data": out, "message_code": "SUCCESS_CREATED"})
}

// UpdatePackage 更新套餐 + 关联 plans
func UpdatePackage(c *fiber.Ctx) error {
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_PARAMS"})
	}
	var pkg database.Package
	if err := database.DB.First(&pkg, id).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"success": false, "message_code": "ERR_NOT_FOUND"})
	}
	payload, perr := parsePackagePayload(c)
	if perr != nil {
		if errors.Is(perr, errDeprecatedRequestField) {
			return c.Status(400).JSON(fiber.Map{
				"success":      false,
				"message":      perr.Error(),
				"message_code": "ERR_DEPRECATED_FIELD",
			})
		}
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_PARSE_PAYLOAD"})
	}
	// fix Minor（codex 第五轮）：UpdatePackage 同样校验 price/period/active 上限的边界
	if err := validatePackageProductType(payload.ProductType); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "产品类型仅支持订阅", "message_code": MessageCodeInvalidProductType})
	}
	if err := validatePackagePayload(&payload.Package); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": err.Error(), "message_code": packageValidationMessageCode(err)})
	}
	// fix MAJOR M5（codex 第二十轮）：plan_multipliers 同样校验
	if err := validatePlanMultipliers(payload.PlanIDs, payload.PlanMultipliers); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": err.Error(), "message_code": "ERR_INVALID_MULTIPLIER"})
	}

	err = database.DB.Transaction(func(tx *gorm.DB) error {
		// 用 map 更新避免 zero 值覆盖 + 防误改 ID
		// payload.Stackable / payload.Enabled 是 *bool（schema 改造后），
		// nil → admin 没传该字段 → 跳过更新（保持原值）；非 nil → 解引用写入。
		updates := map[string]any{
			"name": payload.Name, "description": payload.Description,
			"product_type": payload.ProductType,
			"icon_key":     payload.IconKey, "badge_color": payload.BadgeColor,
			"gradient": payload.Gradient, "highlight_tag": payload.HighlightTag,
			"price_amount":           payload.PriceAmount,
			"cost_floor_micro_usd":   payload.CostFloorMicroUSD,
			"price_currency":         payload.PriceCurrency,
			"billing_period_seconds": payload.BillingPeriodSeconds,
			"max_active_per_user":    payload.MaxActivePerUser,
			"purchase_when_owned":    payload.PurchaseWhenOwned,
			"public":                 payload.Public, "sort_order": payload.SortOrder,
			"extra_config": payload.ExtraConfig,
		}
		if payload.Stackable != nil {
			updates["stackable"] = *payload.Stackable
		}
		if payload.Enabled != nil {
			updates["enabled"] = *payload.Enabled
		}
		if err := tx.Model(&pkg).Updates(updates).Error; err != nil {
			return err
		}
		// 重建 PackagePlan 关联（如果传了 plan_ids）
		if payload.PlanIDs != nil {
			if err := tx.Where("package_id = ?", pkg.ID).Delete(&database.PackagePlan{}).Error; err != nil {
				return err
			}
			for i, pid := range payload.PlanIDs {
				mult := 1.0
				if i < len(payload.PlanMultipliers) && payload.PlanMultipliers[i] > 0 {
					mult = payload.PlanMultipliers[i]
				}
				if err := tx.Create(&database.PackagePlan{
					PackageID: pkg.ID, QuotaPlanID: pid,
					QuantityMultiplier: mult, SortOrder: i,
				}).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "message": err.Error(), "message_code": "ERR_DB_UPDATE"})
	}
	// fix MAJOR（codex 第十七轮）：helper 错误必须冒泡
	out, loadErr := loadPackageWithPlans(pkg.ID)
	if loadErr != nil {
		log.Printf("[PKG-UPDATE] load with plans pkg=%d failed: %v", pkg.ID, loadErr)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}
	return c.JSON(fiber.Map{"success": true, "data": out, "message_code": "SUCCESS_UPDATED"})
}

// DeletePackage 删除套餐。已有 active 订阅的不允许直接删，建议 disable。
//
// fix MAJOR（codex 第十六轮）：active count 必须在事务内 + 检查 .Error，
// 否则 count(tx 外) → user 购买（中间窗口）→ delete(tx 内) 会成功通过校验，
// 留下用户持有"已删 package_id"的 active sub 孤儿（FK 兜底缺失下更脏）。
// 同时 .Error 被吞会让 DB 故障时静默放行删除。
var errPackageHasActiveSubs = errors.New("package has active subscriptions")

func DeletePackage(c *fiber.Ctx) error {
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_PARAMS"})
	}
	var activeCount int64
	txErr := database.DB.Transaction(func(tx *gorm.DB) error {
		// fix CRITICAL（codex 第十七轮）：必须先 SELECT ... FOR UPDATE 锁 package 行，
		// 与 purchase 路径"lockUser → lockPackage"锁顺序对齐——否则 admin 删除时
		// 用户购买 tx 仍可在 count=0 后插入 active sub，留下孤儿订阅。
		// SQLite 上 GORM 会无害降级（单写连接已串行化）；PG/MySQL 上锁定 package 行直至 commit。
		var freshPkg database.Package
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&freshPkg, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return gorm.ErrRecordNotFound
			}
			return fmt.Errorf("lock package: %w", err)
		}
		// tx 内重新 count active sub（排除已过期），必须检查 .Error，
		// DB 故障时绝不能"看到 0 就放行删除"。
		if err := tx.Model(&database.UserSubscription{}).
			Where("package_id = ? AND status = ? AND end_at > ?", id, "active", time.Now()).
			Count(&activeCount).Error; err != nil {
			return fmt.Errorf("count active subs: %w", err)
		}
		if activeCount > 0 {
			return errPackageHasActiveSubs
		}
		if err := tx.Where("package_id = ?", id).Delete(&database.PackagePlan{}).Error; err != nil {
			return fmt.Errorf("delete plans: %w", err)
		}
		return tx.Delete(&database.Package{}, id).Error
	})
	if txErr != nil {
		if errors.Is(txErr, errPackageHasActiveSubs) {
			return c.Status(409).JSON(fiber.Map{
				"success":      false,
				"message":      "存在活跃订阅，请先 disable 套餐",
				"message_code": "ERR_PACKAGE_HAS_ACTIVE_SUBS",
				"active_count": activeCount,
			})
		}
		if errors.Is(txErr, gorm.ErrRecordNotFound) {
			return c.Status(404).JSON(fiber.Map{"success": false, "message_code": "ERR_NOT_FOUND"})
		}
		log.Printf("[PKG-DELETE] tx failed pkg=%d: %v", id, txErr)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_DELETE"})
	}
	return c.JSON(fiber.Map{"success": true, "message_code": "SUCCESS_DELETED"})
}

// ListPublicPackages 给用户购买页用，仅返回 public + enabled 的套餐。
// 价格永远是 PriceAmount；任何用户特定折扣由 UserCoupon 单独通过 /api/coupons/my 提供。
//
// fix MAJOR R23+2-B5（codex 全方面审查）：所有 DB 查询失败统一 fail-closed 返回 500。
func ListPublicPackages(c *fiber.Ctx) error {
	type pubItem struct {
		publicPackageResponse
		Plans []publicPackagePlanItem `json:"plans"`
	}

	var pkgs []database.Package
	if err := database.DB.Where("public = ? AND enabled = ?", true, true).
		Order("sort_order asc, id asc").Find(&pkgs).Error; err != nil {
		log.Printf("[PKG-LIST] load packages failed: %v", err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}
	if len(pkgs) == 0 {
		return c.JSON(fiber.Map{"success": true, "data": []pubItem{}})
	}

	pkgIDs := make([]uint, 0, len(pkgs))
	for _, p := range pkgs {
		pkgIDs = append(pkgIDs, p.ID)
	}

	var allPPs []database.PackagePlan
	if err := database.DB.Where("package_id IN ?", pkgIDs).Order("sort_order asc, id asc").Find(&allPPs).Error; err != nil {
		log.Printf("[PKG-LIST] load package_plans failed: %v", err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}

	planIDSet := make(map[uint]bool)
	for _, pp := range allPPs {
		planIDSet[pp.QuotaPlanID] = true
	}
	planIDs := make([]uint, 0, len(planIDSet))
	for id := range planIDSet {
		planIDs = append(planIDs, id)
	}
	var allPlans []database.QuotaPlan
	if len(planIDs) > 0 {
		if err := database.DB.Where("id IN ?", planIDs).Find(&allPlans).Error; err != nil {
			log.Printf("[PKG-LIST] load quota_plans failed: %v", err)
			return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
		}
	}
	planMap := make(map[uint]database.QuotaPlan, len(allPlans))
	for _, p := range allPlans {
		planMap[p.ID] = p
	}

	ppsByPkg := make(map[uint][]database.PackagePlan, len(pkgs))
	for _, pp := range allPPs {
		ppsByPkg[pp.PackageID] = append(ppsByPkg[pp.PackageID], pp)
	}

	out := make([]pubItem, 0, len(pkgs))
	for _, p := range pkgs {
		items := []publicPackagePlanItem{}
		for _, pp := range ppsByPkg[p.ID] {
			if plan, ok := planMap[pp.QuotaPlanID]; ok {
				items = append(items, publicPackagePlanItem{
					ID:        pp.ID,
					SortOrder: pp.SortOrder,
					Plan:      publicPlanBriefFrom(plan),
				})
			}
		}
		out = append(out, pubItem{publicPackageResponse: publicPackageResponseFrom(p), Plans: items})
	}
	return c.JSON(fiber.Map{"success": true, "data": out})
}

// ReorderPackages 批量重排 sort_order。
// 请求体：{ "ids": [10, 12, 11] } — 按此顺序 sort_order = 10, 20, 30 ...
// 拖拽 UI onDragEnd 调用，事务内批量 UPDATE。
func ReorderPackages(c *fiber.Ctx) error {
	var req struct {
		IDs []uint `json:"ids"`
	}
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_PARSE_PAYLOAD"})
	}
	if len(req.IDs) == 0 {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_PARAMS"})
	}
	if err := database.DB.Transaction(func(tx *gorm.DB) error {
		for i, id := range req.IDs {
			res := tx.Model(&database.Package{}).Where("id = ?", id).Update("sort_order", (i+1)*10)
			if res.Error != nil {
				return fmt.Errorf("reorder package %d: %w", id, res.Error)
			}
			if res.RowsAffected == 0 {
				return fmt.Errorf("%w: package id=%d", errReorderStaleID, id)
			}
		}
		return nil
	}); err != nil {
		if errors.Is(err, errReorderStaleID) {
			return c.Status(404).JSON(fiber.Map{"success": false, "message_code": "ERR_REORDER_STALE_ID"})
		}
		log.Printf("[PACKAGE-REORDER] failed: %v", err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}
	return c.JSON(fiber.Map{"success": true, "message_code": "SUCCESS_REORDERED"})
}
