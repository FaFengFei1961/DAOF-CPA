// Package database / notification_seeds.go
//
// 通知偏好系统的 SysConfig 默认值。
//
// 用户首次保存偏好前，业务层（LoadPreference）从这里读取系统默认值。
// admin 改过的 SysConfig 不会被启动时覆盖。
package database

import (
	"log"

	"daof-ai-hub/utils"

	"gorm.io/gorm"
)

var NotificationSysConfigDefaults = map[string]string{
	// ── 用户偏好默认值 ──
	// JSON 对象，缺失的 key 视为启用；显式 false 才屏蔽。security/system/broadcast 强制送达，不走偏好。
	"notif_default_categories": `{"subscription_expiring":true,"subscription_usage_warn":true,"refund":true,"ticket_message":true}`,
	// CSV 形式的阈值数组，业务层解析；空字符串=全关
	"notif_default_thresholds_csv": "80,100",

	// ── 触发器文案（admin 可改）──
	"notif_usage_warn_title": "套餐用量提醒",
	"notif_usage_warn_body":  "您的「{package_name}」当前用量已达 {percent}%（{plan_name} / {bucket}）。",
	// 订阅取消的退款（钱进 USD 余额，可立即继续消费）—— 沿用旧文案
	"notif_refund_title": "退款已到账",
	"notif_refund_body":  "「{package_name}」已退款 {amount} {currency}，到账您的余额。",
	// 充值订单的退款（钱原路退回支付宝/微信，不在站内余额，存在网关延迟）
	// 与订阅退款分开两套 key，admin 可独立配置
	"notif_topup_refund_title": "退款已发起",
	"notif_topup_refund_body":  "「{package_name}」 {amount} {currency}，已原路退回，若 24 小时内未收到退款，请提交工单处理。",
	"notif_security_ban_title": "您的账户已被限制",
	"notif_security_ban_body":  "原因：{reason}。如有疑问请联系客服。",

	// ── 偏好缓存 TTL（秒）──
	"notif_pref_cache_ttl_seconds": "600",
}

// SeedNotificationDefaults 在每次启动时调用，仅 INSERT 不存在的 key。
// 与 SeedSubscriptionDefaults 同模式：单事务、并发冲突忽略、admin 已配置不覆盖。
func SeedNotificationDefaults() {
	if DB == nil {
		return
	}
	created := 0
	err := DB.Transaction(func(tx *gorm.DB) error {
		for k, v := range NotificationSysConfigDefaults {
			var existing SysConfig
			res := tx.Where("key = ?", k).First(&existing)
			if res.RowsAffected > 0 {
				continue
			}
			encrypted, err := utils.Encrypt(v)
			if err != nil {
				log.Printf("[NOTIF-SEED] encrypt %s failed: %v", k, err)
				continue
			}
			if err := tx.Create(&SysConfig{Key: k, Value: encrypted}).Error; err != nil {
				log.Printf("[NOTIF-SEED] create %s skipped: %v", k, err)
				continue
			}
			created++
		}
		return nil
	})
	if err != nil {
		log.Printf("[NOTIF-SEED] transaction failed: %v", err)
		return
	}
	if created > 0 {
		log.Printf("🌱 通知偏好系统：写入 %d 条默认配置（已存在的未覆盖）", created)
	}
}
