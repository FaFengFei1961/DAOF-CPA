// Package database / subscription_seeds.go
//
// 套餐订阅系统的 SysConfig 默认值。
// 启动时不存在的 key 才会写入默认值，admin 改过的不会被覆盖。
package database

import (
	"errors"
	"fmt"
	"log"

	"daof-cpa/utils"

	"gorm.io/gorm"
)

var SubscriptionSysConfigDefaults = map[string]string{
	// ── 订阅缓存 ──
	"subscription_cache_ttl_seconds": "30",

	// ── 扣费引擎 ──
	"subscription_engine_fallback_to_quota": "true",
	"subscription_engine_402_message":       "您的订阅额度已用尽，请购买套餐或充值余额",

	// ── 风控 / 防刷 ──
	"subscription_stack_alert_threshold":  "5",
	"subscription_stack_freeze_threshold": "10",
	"refund_count_per_month_alert":        "3",

	// ── 通知文案（admin 可改）──
	"notif_subscription_expiring_title": "订阅即将到期",
	"notif_subscription_expiring_body":  "您的「{package_name}」将在 {days} 天后到期。",
	"notif_subscription_expired_title":  "订阅已到期",
	"notif_subscription_expired_body":   "您的「{package_name}」已到期。",

	// ── 超时倒计时窗口 ──
	"subscription_expiring_warn_days":    "3",
	"subscription_expired_grace_seconds": "60",

	// ── ApiLog 自动清理 ──
	"apilog_retention_days":     "90",   // 保留最近 N 天，0=不清理
	"apilog_cleanup_batch_size": "5000", // 单次清理的最大行数（避免一次锁表过久）

	// ── 三段消费模型默认值（admin 全局配置，影响新用户初始化）──
	"balance_consume_default_enabled":         "false",   // 余额消费默认关闭（最严策略）
	"balance_consume_default_limit_micro_usd": "0",       // 默认无限额（micro_usd 整数；旧 *_usd USD float key 已废弃）
	"balance_consume_default_window_secs":     "2592000", // 默认 30 天重置窗口
	"subscription_default_period_seconds":     "2592000", // 订阅默认周期 30 天

	// ── 公开透明计费规则（订阅 charged_cost 口径 + 余额 raw_cost 1:1 口径）──
	"billing_rules_version":              "default-active-series-2026-05-17",
	"billing_model_weights_json":         `[{"pattern":"claude-haiku-*","weight":0.3,"label":"Claude Haiku","reason":"当前启用的 Claude 轻量系列"},{"pattern":"claude-sonnet-*","weight":1,"thinking_weight":1.5,"label":"Claude Sonnet","reason":"当前启用的 Claude 基准系列；thinking 启用时加权"},{"pattern":"claude-opus-*","weight":3.5,"thinking_weight":5,"label":"Claude Opus","reason":"当前启用的 Claude 高消耗系列"},{"pattern":"gemini-*-flash-lite*","weight":0.2,"label":"Gemini Flash Lite","reason":"当前启用的 Gemini 超轻量系列"},{"pattern":"gemini-*-flash*","weight":0.4,"label":"Gemini Flash","reason":"当前启用的 Gemini 快速系列"},{"pattern":"gemini-*-pro*","weight":0.9,"label":"Gemini Pro","reason":"当前启用的 Gemini 主力系列"},{"pattern":"gpt-*-mini*","weight":0.5,"label":"GPT mini","reason":"当前启用的 GPT 轻量系列"},{"pattern":"gpt-*","weight":1,"label":"GPT","reason":"当前启用的 GPT 主力系列"},{"pattern":"grok-*","weight":1,"label":"Grok","reason":"当前启用的 xAI Grok 系列"}]`,
	"billing_health_multipliers_json":    `[{"pattern":"*","weight":1,"label":"Normal","reason":"默认无高峰加权"}]`,
	"upstream_account_cost_presets_json": `[{"id":"claude-pro","label":"Claude Pro","provider":"anthropic","plan_name":"Claude Pro","monthly_cost_usd":20,"estimated_monthly_capacity_usd":0,"notes":"仅填官方月费；容量需按本平台实测填写。"},{"id":"claude-max-5x","label":"Claude Max 5x","provider":"anthropic","plan_name":"Claude Max 5x","monthly_cost_usd":100,"estimated_monthly_capacity_usd":0,"notes":"仅填官方月费；容量需按本平台实测填写。"},{"id":"claude-max-20x","label":"Claude Max 20x","provider":"anthropic","plan_name":"Claude Max 20x","monthly_cost_usd":200,"estimated_monthly_capacity_usd":0,"notes":"仅填官方月费；容量需按本平台实测填写。"},{"id":"chatgpt-plus","label":"ChatGPT Plus / Codex","provider":"codex","plan_name":"ChatGPT Plus","monthly_cost_usd":20,"estimated_monthly_capacity_usd":0,"notes":"仅填官方月费；容量需按本平台实测填写。"},{"id":"chatgpt-pro-100","label":"ChatGPT Pro 100 / Codex","provider":"codex","plan_name":"ChatGPT Pro 100","monthly_cost_usd":100,"estimated_monthly_capacity_usd":0,"notes":"仅填官方月费；容量需按本平台实测填写。"},{"id":"chatgpt-pro-200","label":"ChatGPT Pro 200 / Codex","provider":"codex","plan_name":"ChatGPT Pro 200","monthly_cost_usd":200,"estimated_monthly_capacity_usd":0,"notes":"仅填官方月费；容量需按本平台实测填写。"},{"id":"google-ai-pro","label":"Google AI Pro","provider":"gemini","plan_name":"Google AI Pro","monthly_cost_usd":20,"estimated_monthly_capacity_usd":0,"notes":"仅填官方月费；容量需按本平台实测填写。"},{"id":"google-ai-ultra","label":"Google AI Ultra","provider":"gemini","plan_name":"Google AI Ultra","monthly_cost_usd":250,"estimated_monthly_capacity_usd":0,"notes":"仅填官方月费；容量需按本平台实测填写。"}]`,

	// ── CLIProxyAPI usage queue 同步（上游账号归因 / 毛利核算基础）──
	"cliproxy_usage_sync_enabled":          "true",
	"cliproxy_usage_sync_interval_seconds": "60",
	"cliproxy_usage_sync_batch_size":       "100",

	// ── 注册自动发券 ──
	"signup_coupon_template_id": "0", // 0/空 = 不自动发券
}

// SeedSubscriptionDefaults 在每次启动时调用，仅 INSERT 不存在的 key。
// 用单一事务包裹整个 seed 流程，避免并发启动时部分写入导致状态不一致。
func SeedSubscriptionDefaults() {
	if DB == nil {
		return
	}
	created := 0
	err := DB.Transaction(func(tx *gorm.DB) error {
		for k, v := range SubscriptionSysConfigDefaults {
			var existing SysConfig
			res := tx.Where("key = ?", k).First(&existing)
			if res.RowsAffected > 0 {
				continue
			}
			encrypted, err := utils.Encrypt(v)
			if err != nil {
				return fmt.Errorf("encrypt %s: %w", k, err)
			}
			if err := tx.Create(&SysConfig{Key: k, Value: encrypted}).Error; err != nil {
				return fmt.Errorf("create %s: %w", k, err)
			}
			created++
		}
		return nil
	})
	if err != nil {
		log.Printf("[SUBSCRIPTION-SEED] transaction failed: %v", err)
		return
	}
	if created > 0 {
		log.Printf("🌱 套餐订阅系统：写入 %d 条默认配置（已存在的未覆盖）", created)
	}
	SeedDefaultSubscriptionProducts()
}

type defaultSubscriptionTier struct {
	ProviderKey   string
	ProviderLabel string
	TierKey       string
	TierLabel     string
	HighlightTag  string
	PackageName   string
	Description   string
	PriceUSD      float64
	FiveHourUSD   float64
	SevenDayUSD   float64
	ModelMatch    string
	Bucket        string
	SortOrder     int
}

func SeedDefaultSubscriptionProducts() {
	if DB == nil {
		return
	}
	enabled := true
	stackable := false
	// 仅保留 Combo 组合套餐（产品决策：不再单独售卖 Claude / Codex / Gemini / Grok 单品套餐）
	specs := []defaultSubscriptionTier{
		{"combo", "Combo", "pro", "Pro", "轻量", "Combo Pro", "Claude + Codex + Gemini + Grok 全部模型共享 API 等值额度。", 49, 25, 125, `["claude-*","gpt-*","gemini-*","grok-*"]`, "combo:all", 310},
		{"combo", "Combo", "max_5x", "Max 5x", "中等", "Combo Max 5x", "Claude + Codex + Gemini + Grok 全部模型共享更高额度。", 199, 125, 625, `["claude-*","gpt-*","gemini-*","grok-*"]`, "combo:all", 320},
		{"combo", "Combo", "max_20x", "Max 20x", "重度", "Combo Max 20x", "Claude + Codex + Gemini + Grok 全部模型共享旗舰额度。", 499, 400, 2000, `["claude-*","gpt-*","gemini-*","grok-*"]`, "combo:all", 330},
	}

	createdPlans := 0
	createdPackages := 0
	err := DB.Transaction(func(tx *gorm.DB) error {
		for _, spec := range specs {
			fiveHourPlan, madePlan, err := firstOrCreateDefaultQuotaPlan(tx, spec, "5h", "5 小时滚动额度", spec.FiveHourUSD, 5*3600)
			if err != nil {
				return err
			}
			if madePlan {
				createdPlans++
			}
			sevenDayPlan, madePlan, err := firstOrCreateDefaultQuotaPlan(tx, spec, "7d", "7 天滚动额度", spec.SevenDayUSD, 7*86400)
			if err != nil {
				return err
			}
			if madePlan {
				createdPlans++
			}

			priceMicro, ok := USDToMicro(spec.PriceUSD)
			if !ok {
				return fmt.Errorf("default package price overflow: %s", spec.PackageName)
			}
			pkg := Package{}
			res := tx.Where("name = ?", spec.PackageName).First(&pkg)
			if res.Error != nil && !errors.Is(res.Error, gorm.ErrRecordNotFound) {
				return fmt.Errorf("load package %s: %w", spec.PackageName, res.Error)
			}
			if res.RowsAffected == 0 {
				pkg = Package{
					Name:                 spec.PackageName,
					Description:          spec.Description,
					ProductType:          "subscription",
					IconKey:              spec.ProviderKey,
					BadgeColor:           "primary",
					HighlightTag:         spec.HighlightTag,
					PriceAmount:          priceMicro,
					PriceCurrency:        "USD",
					BillingPeriodSeconds: 30 * 86400,
					Stackable:            &stackable,
					MaxActivePerUser:     1,
					PurchaseWhenOwned:    "stack",
					Public:               true,
					SortOrder:            spec.SortOrder,
					Enabled:              &enabled,
					ExtraConfig:          fmt.Sprintf(`{"seed":"subscription_v1","provider":"%s","tier":"%s","api_equivalent":true}`, spec.ProviderKey, spec.TierKey),
				}
				if err := tx.Create(&pkg).Error; err != nil {
					return fmt.Errorf("create package %s: %w", spec.PackageName, err)
				}
				createdPackages++
			}
			if err := ensureDefaultPackagePlan(tx, pkg.ID, fiveHourPlan.ID, 0); err != nil {
				return err
			}
			if err := ensureDefaultPackagePlan(tx, pkg.ID, sevenDayPlan.ID, 1); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		log.Printf("[SUBSCRIPTION-SEED] default products failed: %v", err)
		return
	}
	if createdPlans > 0 || createdPackages > 0 {
		log.Printf("🌱 默认订阅产品：新增 %d 个配额计划、%d 个套餐", createdPlans, createdPackages)
	}
}

func firstOrCreateDefaultQuotaPlan(tx *gorm.DB, spec defaultSubscriptionTier, windowKey, windowLabel string, limitUSD float64, windowSeconds int) (QuotaPlan, bool, error) {
	name := fmt.Sprintf("sub_%s_%s_%s_api_cost", spec.ProviderKey, spec.TierKey, windowKey)
	plan := QuotaPlan{}
	res := tx.Where("name = ?", name).First(&plan)
	if res.Error != nil && !errors.Is(res.Error, gorm.ErrRecordNotFound) {
		return plan, false, fmt.Errorf("load quota plan %s: %w", name, res.Error)
	}
	if res.RowsAffected > 0 {
		return plan, false, nil
	}
	enabled := true
	limitMicro, ok := USDToMicro(limitUSD)
	if !ok {
		return plan, false, fmt.Errorf("quota plan %s limit overflow", name)
	}
	plan = QuotaPlan{
		Name:               name,
		DisplayName:        fmt.Sprintf("%s %s · %s", spec.ProviderLabel, spec.TierLabel, windowLabel),
		Description:        fmt.Sprintf("%s。额度单位为 API 等值美元，不是现金余额。", windowLabel),
		ModelMatch:         spec.ModelMatch,
		LimitUnit:          "api_cost_usd",
		LimitValue:         limitUSD,
		LimitValueMicroUSD: limitMicro,
		WindowSeconds:      windowSeconds,
		WeightFactor:       "{}",
		Priority:           100,
		OverflowStrategy:   "block",
		ExtraConfig: fmt.Sprintf(`{"seed":"subscription_v1","bucket":"%s","bucket_label":"%s","window":"%s"}`,
			spec.Bucket, spec.ProviderLabel, windowKey),
		Enabled: &enabled,
	}
	if err := tx.Create(&plan).Error; err != nil {
		return plan, false, fmt.Errorf("create quota plan %s: %w", name, err)
	}
	return plan, true, nil
}

func ensureDefaultPackagePlan(tx *gorm.DB, packageID, planID uint, sortOrder int) error {
	var pp PackagePlan
	res := tx.Where("package_id = ? AND quota_plan_id = ?", packageID, planID).First(&pp)
	if res.Error != nil && !errors.Is(res.Error, gorm.ErrRecordNotFound) {
		return fmt.Errorf("load package_plan pkg=%d plan=%d: %w", packageID, planID, res.Error)
	}
	if res.RowsAffected > 0 {
		return nil
	}
	pp = PackagePlan{
		PackageID:          packageID,
		QuotaPlanID:        planID,
		QuantityMultiplier: 1,
		SortOrder:          sortOrder,
	}
	if err := tx.Create(&pp).Error; err != nil {
		return fmt.Errorf("create package_plan pkg=%d plan=%d: %w", packageID, planID, err)
	}
	return nil
}
