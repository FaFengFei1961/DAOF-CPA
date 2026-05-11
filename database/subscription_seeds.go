// Package database / subscription_seeds.go
//
// 套餐订阅系统的 SysConfig 默认值。
// 启动时不存在的 key 才会写入默认值，admin 改过的不会被覆盖。
package database

import (
	"fmt"
	"log"

	"daof-ai-hub/utils"

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
	"balance_consume_default_enabled":     "false",   // 余额消费默认关闭（最严策略）
	"balance_consume_default_limit_usd":   "0",       // 默认无限额
	"balance_consume_default_window_secs": "2592000", // 默认 30 天重置窗口
	"addon_default_period_seconds":        "604800",  // 增量包默认有效期 7 天
	"subscription_default_period_seconds": "2592000", // 订阅默认周期 30 天

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
}
