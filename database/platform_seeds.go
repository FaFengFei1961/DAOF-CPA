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
	// Phase H-4：Google OAuth。admin 在 oauth admin 面板填入。
	"google_client_id":     "",
	"google_client_secret": "",
	"aliyun_access_key":    "",
	"aliyun_access_secret": "",
	"aliyun_sms_sign":      "",
	"aliyun_sms_template":  "",

	// ── Registration risk controls ──
	"reg_strategy": "dynamic",
	"reg_ip_limit": "3",
	// fix C-H1 (2026-05-19)：默认改为 10000（之前 0 = unlimited 是攻击面：
	// 攻击者可任意注册 → AuthCache 全量装载 → 内存 OOM）。每用户 50 子 token
	// 上限已在 controller/token.go 强制；上限 10K user × 50 token = 500K cache entries
	// ~= 几百 MB，安全包络。admin 可改 0 显式 opt-in 无限注册（接受风险）。
	"max_users":      "10000",
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

	// ── Email feature (Phase G) ──
	// Master kill switch + 子 toggle。所有 toggle 默认 false：admin 必须显式开启邮箱功能。
	"email_enabled":              "false", // master kill: 全关
	"email_signup_enabled":       "false", // 允许邮箱+密码注册（G-2 用）
	"email_login_enabled":        "false", // 允许邮箱+密码登录（G-2 用）
	"email_verify_url_path":      "/verify-email",       // 前端验证邮箱页路径，verify_url = server_address + 这个 + ?token=
	"email_reset_url_path":       "/reset-password",     // 前端重置密码页路径
	"email_verify_ttl_seconds":   "3600",                // 验证 token TTL 默认 1 小时
	"email_reset_ttl_seconds":    "900",                 // 密码重置 token TTL 默认 15 分钟
	"email_rate_limit_per_email_hourly": "5",
	"email_rate_limit_per_ip_hourly":    "20",
	// SMTP 配置（admin 必须填，否则 IsConfigured=false → 邮件不发）
	"smtp_host":              "",
	"smtp_port":              "",     // 465 或 587
	"smtp_username":          "",
	"smtp_password":          "",     // utils.Encrypt 加密存
	"smtp_from":              "",     // "Site <noreply@example.com>"
	"smtp_use_implicit_tls":  "false", // 465 端口设 true；587 STARTTLS 设 false
	"smtp_reply_to":          "",     // 可选

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
