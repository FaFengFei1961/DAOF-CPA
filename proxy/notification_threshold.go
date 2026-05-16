// Package proxy / notification_threshold.go
//
// 套餐用量阈值跨越触发器。
//
// 在扣费完成后判断 (before% → after%) 是否跨过用户偏好里的某些阈值，
// 跨过则发一条 subscription_usage_warn 通知。dedupKey 确保同一窗口同一阈值只发一次。
//
// 调用方约定：必须用 goroutine 调用，永不阻塞热路径。
package proxy

import (
	"fmt"
	"strings"
	"time"

	"daof-cpa/database"
)

// MaybeFireUsageWarn 检查用量是否跨过阈值并发通知。
//
// 参数：
//
//	subID, planID  ── 订阅 / 配额计划 ID（用于 dedupKey + 关联）
//	userID         ── 收件用户
//	bucket         ── 模型桶（usage 表 model_bucket）
//	before, after  ── UPDATE 前后的 consumed_value（绝对值）
//	limit          ── plan 的 LimitValue
//	windowStart    ── 当前用量窗口的起始时间（用作 dedupKey 一部分，新窗口=新 key）
//
// limit<=0 视为无限额，直接 return（api_cost_usd 等场景）。
func MaybeFireUsageWarn(subID, planID, userID uint, bucket string, before, after, limit float64, windowStart time.Time) {
	if userID == 0 || limit <= 0 || after <= before {
		return
	}
	beforePct := before / limit * 100.0
	afterPct := after / limit * 100.0

	view := GetPrefCached(userID)
	crossed := database.CrossedThresholds(view, beforePct, afterPct)
	if len(crossed) == 0 {
		return
	}

	// 取套餐名 / plan 名（缓存里有 snapshot；查不到也不阻塞主流程）
	pkgName, planName := lookupSubscriptionDisplayNames(userID, subID, planID)

	wsUnix := windowStart.Unix()
	for _, thr := range crossed {
		severity := "warning"
		if thr >= 100 {
			severity = "error"
		}
		dedupKey := fmt.Sprintf("usage_warn:%d:%d:%s:%d:%d", subID, planID, bucket, thr, wsUnix)
		title := getStrConfigStr("notif_usage_warn_title", "套餐用量提醒")
		bodyTpl := getStrConfigStr("notif_usage_warn_body",
			"您的「{package_name}」当前用量已达 {percent}%（{plan_name} / {bucket}）。")
		body := strings.ReplaceAll(bodyTpl, "{package_name}", pkgName)
		body = strings.ReplaceAll(body, "{plan_name}", planName)
		body = strings.ReplaceAll(body, "{bucket}", bucket)
		body = strings.ReplaceAll(body, "{percent}", fmt.Sprintf("%d", thr))

		dk := dedupKey
		Dispatch(
			userID, "subscription_usage_warn", severity,
			title, body,
			LinkUpgradeMine(), "查看",
			"subscription", subID, &dk,
		)
	}
}

// lookupSubscriptionDisplayNames 查找订阅快照里的 package 名 + plan 名。
// 通过用户活跃订阅缓存反查（活跃订阅一般 1-5 个，O(n) 即可）。
func lookupSubscriptionDisplayNames(userID, subID, planID uint) (pkgName, planName string) {
	pkgName = "（未知套餐）"
	planName = "（未知计划）"

	subs, err := GetUserActiveSubscriptions(userID)
	if err != nil {
		// notification 路径不阻塞业务，DB 失败时静默走"未知套餐"占位
		return pkgName, planName
	}
	for _, cs := range subs {
		if cs.Subscription == nil || cs.Subscription.ID != subID || cs.Snapshot == nil {
			continue
		}
		if name, ok := cs.Snapshot["name"].(string); ok && name != "" {
			pkgName = name
		}
		if plansRaw, ok := cs.Snapshot["plans"].([]any); ok {
			for _, p := range plansRaw {
				m, ok := p.(map[string]any)
				if !ok {
					continue
				}
				if uintFromAny(m["id"]) == planID {
					if dn, ok := m["display_name"].(string); ok && dn != "" {
						planName = dn
					} else if n, ok := m["name"].(string); ok && n != "" {
						planName = n
					}
					break
				}
			}
		}
		break
	}
	return
}
