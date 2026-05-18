// Package database / topup_seeds.go
//
// 易付通对接相关 SysConfig 默认值。
// admin 改过的 key 不会被启动时覆盖（参照 SeedSubscriptionDefaults 模式）。
package database

import (
	"fmt"
	"log"

	"daof-cpa/utils"

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
	// 充值金额范围（fen 分，int64）。Sprint4-M3：彻底改为 fen int 输入，杜绝 float64。
	"yifut_min_amount_fen": "100",     // ¥1.00
	"yifut_max_amount_fen": "1000000", // ¥10,000.00
	// 默认预设档位（fen CSV）
	"yifut_preset_amounts_fen": "1000,3000,5000,10000,30000,50000",
	// 汇率（RMB per USD × 1e6）。Sprint4-M3：从 float 7.2 改为 int64 定点 7_200_000。
	"exchange_rate_rmb_per_usd_micros": "7200000",

	// ── Webhook 安全 (Sprint4-M3) ──
	// 易付通回调来源 IP CIDR 白名单（CSV）。空表示禁用 IP 检查（仅依赖签名 + nonce 防重放）。
	// 例："1.2.3.0/24,5.6.7.8/32"。admin 在易付通商户后台查到固定回调 IP 段后建议启用。
	"yifut_notify_allowed_cidrs": "",
	// server_address 是否强制 https（生产 true；开发 false 可用 http）。
	// 强制开启可防 admin 误配 http://... 导致 notify_url 在网关侧被劫持。
	"server_address_require_https": "true",

	// ── 商品名 / param 模板 ──
	"yifut_product_name": "DAOF-CPA 余额充值",

	// ── 回调与同步页 ──
	// 同步跳转后用户落地页（前端 React Router 路径路由：/topup-result?status=...）
	// fix P2（codex review verify-1）：前端 hashRedirect.js 已删除，hash 路由不再被改写。
	"yifut_return_path": "/topup-result",
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
