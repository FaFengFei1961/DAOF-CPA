// Package database / topup_seeds.go
//
// 易付通对接相关 SysConfig 默认值。
// admin 改过的 key 不会被启动时覆盖（参照 SeedSubscriptionDefaults 模式）。
package database

import (
	"fmt"
	"log"

	"daof-ai-hub/utils"

	"gorm.io/gorm"
)

var TopupSysConfigDefaults = map[string]string{
	// ── 易付通 V2 RSA 凭证 ──
	"yifut_pid":                  "", // admin 后台填，例如 "1161"
	"yifut_gateway":              "https://www.yifut.com",
	"yifut_merchant_private_key": "", // admin 后台填：易付通后台生成 RSA 密钥对后的"商户私钥"PEM 内容
	"yifut_platform_public_key":  "", // admin 后台填：易付通后台 API 信息页"平台公钥"PEM 内容

	// ── 通道开关与默认（V2 只支持 alipay/wxpay）──
	"yifut_enabled_methods": "alipay,wxpay",
	// 充值金额范围（RMB 元）
	"yifut_min_amount_rmb": "1.00",
	"yifut_max_amount_rmb": "10000.00",
	// 默认预设档位（RMB CSV）
	"yifut_preset_amounts_rmb": "10,30,50,100,300,500",

	// ── 商品名 / param 模板 ──
	"yifut_product_name": "DAOF-CPA 余额充值",

	// ── 回调与同步页 ──
	// 同步跳转后用户落地页（前端是 hash 路由：/#topup_result?status=...）
	"yifut_return_path": "/#topup_result",
	// 异步通知端点（后端固定路径，记录这里只是给 admin 看）
	"yifut_notify_path": "/api/payment/notify/yifut",

	// ── 行为开关 ──
	// 充值通知文案
	"notif_topup_title": "充值成功",
	"notif_topup_body":  "您充值的 ¥{amount_rmb} 已到账，等额 {amount_usd} USD 已加入余额。",
}

// SeedTopupDefaults 启动时调用一次，缺失 key 才写入（不覆盖）。
func SeedTopupDefaults() {
	if DB == nil {
		return
	}
	created := 0
	err := DB.Transaction(func(tx *gorm.DB) error {
		for k, v := range TopupSysConfigDefaults {
			var existing SysConfig
			res := tx.Where("key = ?", k).First(&existing)
			if res.RowsAffected > 0 {
				continue // 已存在不覆盖（admin 已配置）
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
		log.Printf("[TOPUP-SEED] transaction failed: %v", err)
		return
	}
	if created > 0 {
		log.Printf("🌱 易付通充值系统：写入 %d 条默认配置（已存在的未覆盖）", created)
	}
}
