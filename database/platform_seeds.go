// Package database / platform_seeds.go
//
// Platform-level SysConfig defaults that are not owned by a narrower domain
// seed (subscription, topup, notification, moderation, or model runtime).
// These are factory-state defaults only: startup inserts missing keys but never
// overwrites operator-configured values.
package database

import (
	"fmt"
	"log"

	"daof-cpa/utils"

	"gorm.io/gorm"
)

var PlatformSysConfigDefaults = map[string]string{
	// ── Public site / integrations ──
	"server_address":       "", // deployment-specific public origin; admin must set before payment callbacks
	"github_client_id":     "",
	"github_client_secret": "",
	"aliyun_access_key":    "",
	"aliyun_access_secret": "",
	"aliyun_sms_sign":      "",
	"aliyun_sms_template":  "",

	// ── Registration risk controls ──
	"reg_strategy":   "dynamic",
	"reg_ip_limit":   "3",
	"max_users":      "0",       // 0 = unlimited
	"signup_bonus":   "1000000", // micro_usd; $1
	"referrer_bonus": "0",
	"referee_bonus":  "0",

	// ── Local CLIProxyAPI management / usage-sync base ──
	"cliproxy_url":                       "http://127.0.0.1:8317",
	"cliproxy_key":                       "",
	"moderation_cliproxy_api_key":        "",
	"credits_refresh_interval":           "15", // minutes
	"credits_max_retries":                "3",
	"credits_retry_interval":             "5", // minutes
	"cpa_project_id_refresh_seconds":     "86400",
	"credits_shrink_abort_threshold_pct": "50",

	// ── Runtime hardening knobs ──
	"channel_circuit_open_threshold":           "5",
	"channel_circuit_initial_cooldown_seconds": "30",
	"channel_circuit_max_cooldown_seconds":     "300",
	"proxy_tls_skip_verify":                    "false",
	"stream_scanner_buffer_bytes":              "4194304", // 4 MiB
	"subscription_cache_max_users":             "50000",
}

// SeedPlatformDefaults inserts platform-level SysConfig defaults.
func SeedPlatformDefaults() {
	if DB == nil {
		return
	}
	created := 0
	err := DB.Transaction(func(tx *gorm.DB) error {
		for k, v := range PlatformSysConfigDefaults {
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
		log.Printf("[PLATFORM-SEED] transaction failed: %v", err)
		return
	}
	if created > 0 {
		log.Printf("🌱 平台运行默认值：写入 %d 条默认配置（已存在的未覆盖）", created)
	}
}
