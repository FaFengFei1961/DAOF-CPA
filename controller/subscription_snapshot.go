// Package controller / subscription_snapshot.go
//
// 订阅快照辅助：把购买时的 Package + 关联 plans 序列化为 JSON 存到
// UserSubscription.PackageSnapshot，以及读取 stack_index。
//
// 从 subscription.go 抽出（Phase D-5，2026-05-19）：只是物理拆分，无语义改动。
package controller

import (
	"encoding/json"
	"fmt"

	"daof-cpa/database"

	"gorm.io/gorm"
)

// buildPackageSnapshot 把当前 Package + 关联 plans 序列化为 JSON 字符串。
//
// fix MAJOR M22-4 follow-up：原实现固定用 database.DB 查 plans，事务内调用会因 SQLite
// 单连接配置（MaxOpenConns=1）等待自己持有的连接而死锁。改成 buildPackageSnapshotTx
// 接受 db *gorm.DB；事务路径传 tx，事务外路径传 database.DB。
func buildPackageSnapshot(pkg *database.Package) (string, error) {
	return buildPackageSnapshotTx(database.DB, pkg)
}

func buildPackageSnapshotTx(db *gorm.DB, pkg *database.Package) (string, error) {
	type planSnap struct {
		ID                 uint    `json:"id"`
		Name               string  `json:"name"`
		ModelMatch         string  `json:"model_match"`
		LimitUnit          string  `json:"limit_unit"`
		LimitValue         float64 `json:"limit_value"`
		LimitValueMicroUSD int64   `json:"limit_value_micro_usd"`
		WindowSeconds      int     `json:"window_seconds"`
		WeightFactor       string  `json:"weight_factor"`
		Priority           int     `json:"priority"`
		OverflowStrategy   string  `json:"overflow_strategy"`
		ExtraConfig        string  `json:"extra_config"`
		QuantityMultiplier float64 `json:"quantity_multiplier"`
	}
	type snap struct {
		// schema_version 标记当前快照语义；QuantityMultiplier 放大限额。
		// fix MAJOR M22-A1 Phase 1：PriceAmount 单位 micro_usd（int64）。
		SchemaVersion        int        `json:"schema_version"`
		PackageID            uint       `json:"package_id"`
		PackageName          string     `json:"package_name"`
		ProductType          string     `json:"product_type"` // 始终是 subscription
		PriceAmount          int64      `json:"price_amount"`
		PriceCurrency        string     `json:"price_currency"`
		BillingPeriodSeconds int        `json:"billing_period_seconds"`
		Plans                []planSnap `json:"plans"`
	}
	productType := pkg.ProductType
	if productType == "" {
		productType = "subscription" // 防御式默认值
	}
	s := snap{
		SchemaVersion:        database.PackageSnapshotCurrentVersion,
		PackageID:            pkg.ID,
		PackageName:          pkg.Name,
		ProductType:          productType,
		PriceAmount:          pkg.PriceAmount,
		PriceCurrency:        pkg.PriceCurrency,
		BillingPeriodSeconds: pkg.BillingPeriodSeconds,
	}
	var pps []database.PackagePlan
	if err := db.Where("package_id = ?", pkg.ID).Order("sort_order asc").Find(&pps).Error; err != nil {
		return "", fmt.Errorf("load package_plans pkg=%d: %w", pkg.ID, err)
	}
	if len(pps) == 0 {
		b, err := json.Marshal(s)
		return string(b), err
	}
	planIDs := make([]uint, 0, len(pps))
	for _, pp := range pps {
		planIDs = append(planIDs, pp.QuotaPlanID)
	}
	var plans []database.QuotaPlan
	if err := db.Where("id IN ?", planIDs).Find(&plans).Error; err != nil {
		return "", fmt.Errorf("load quota_plans pkg=%d: %w", pkg.ID, err)
	}
	planMap := make(map[uint]database.QuotaPlan, len(plans))
	for _, p := range plans {
		planMap[p.ID] = p
	}
	// fix MAJOR R23+3-B6（codex 第四轮）：所有绑定的 plan 必须 enabled，否则 fail-closed
	// 防 admin 绑了 disabled plan → 用户购买后引擎走 no_plans → fallback 余额扣费的灰色路径
	missing := make([]uint, 0)
	for _, pp := range pps {
		plan, ok := planMap[pp.QuotaPlanID]
		if !ok {
			missing = append(missing, pp.QuotaPlanID)
			continue
		}
		if !plan.IsEnabled() {
			missing = append(missing, pp.QuotaPlanID)
			continue
		}
		if plan.LimitUnit == "api_cost_usd" && plan.LimitValue > 0 && plan.LimitValueMicroUSD <= 0 {
			return "", fmt.Errorf("package %d plan_id %d missing limit_value_micro_usd", pkg.ID, plan.ID)
		}
	}
	if len(missing) > 0 {
		return "", fmt.Errorf("package %d has invalid plan_ids %v (missing or disabled)", pkg.ID, missing)
	}
	for _, pp := range pps {
		plan, ok := planMap[pp.QuotaPlanID]
		if !ok {
			continue // 已在上面阻止；防御性
		}
		s.Plans = append(s.Plans, planSnap{
			ID: plan.ID, Name: plan.Name, ModelMatch: plan.ModelMatch,
			LimitUnit: plan.LimitUnit, LimitValue: plan.LimitValue, LimitValueMicroUSD: plan.LimitValueMicroUSD,
			WindowSeconds: plan.WindowSeconds, WeightFactor: plan.WeightFactor,
			Priority: plan.Priority, OverflowStrategy: plan.OverflowStrategy,
			ExtraConfig:        plan.ExtraConfig,
			QuantityMultiplier: pp.QuantityMultiplier,
		})
	}
	b, err := json.Marshal(s)
	return string(b), err
}

// readPackageNameFromSnapshot 从订阅快照里读购买时套餐名（用于通知正文）。
//
// fix Minor（codex 第四轮）：原代码读字段 "name"，但 buildPackageSnapshot 写的是 "package_name"，
// 导致取消订阅退款通知拿到的永远是空字符串，fallback 当前 pkg.Name（套餐改名后会丢历史名）。
func readPackageNameFromSnapshot(snapJSON string) string {
	if snapJSON == "" {
		return ""
	}
	var s struct {
		PackageName string `json:"package_name"`
	}
	if err := json.Unmarshal([]byte(snapJSON), &s); err != nil {
		return ""
	}
	return s.PackageName
}

// getNextStackIndex 返回该用户该套餐下一个可用的 stack_index。
// 必须在事务内调用，scan 错误显式传播让外层 rollback 整笔购买，避免 stack_index 静默落到 1 破坏单调性。
func getNextStackIndex(tx *gorm.DB, userID, packageID uint) (int, error) {
	var maxIdx int
	if err := tx.Model(&database.UserSubscription{}).
		Where("user_id = ? AND package_id = ?", userID, packageID).
		Select("COALESCE(MAX(stack_index), 0)").
		Scan(&maxIdx).Error; err != nil {
		return 0, fmt.Errorf("scan max stack_index: %w", err)
	}
	return maxIdx + 1, nil
}
