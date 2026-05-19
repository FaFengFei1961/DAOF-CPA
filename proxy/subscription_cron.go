// Package proxy / subscription_cron.go
//
// 套餐订阅系统的后台 cron 任务：
//  1. 订阅到期处理：扫描 status=active && end_at < now 的订阅，标记 expired
//  2. 即将到期通知：提前 N 天给用户发预警
//
// 所有阈值都从 SysConfig 读，不写死。
package proxy

import (
	"fmt"
	"log"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"daof-cpa/database"

	"gorm.io/gorm"
)

var (
	subCronDone chan struct{}
	subCronOnce sync.Once
	subCronStop sync.Once
)

// StartSubscriptionCron 启动订阅 cron。main.go 启动时调用一次。
func StartSubscriptionCron() {
	subCronOnce.Do(func() {
		subCronDone = make(chan struct{})
		go func() {
			select {
			case <-time.After(10 * time.Second):
			case <-subCronDone:
				return
			}
			runSubscriptionCronOnce()
			ticker := time.NewTicker(60 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					runSubscriptionCronOnce()
				case <-subCronDone:
					return
				}
			}
		}()
		log.Println("📅 订阅 cron 已启动（每 60s 巡检）")
	})
}

func StopSubscriptionCron() {
	subCronStop.Do(func() {
		if subCronDone != nil {
			close(subCronDone)
		}
	})
}

func runSubscriptionCronOnce() {
	defer func() {
		if r := recover(); r != nil {
			// fix LOW（silent-failure 第十八轮）：panic 必须带 stack trace 才能定位到具体文件:行
			log.Printf("[SUB-CRON] panic: %v\n%s", r, debug.Stack())
		}
	}()

	expireSubscriptions()
	ActivateDueBillingRuleRevisions()
	notifyExpiringSubscriptions()
	// 注：ApiLog 不做自动清理——核心审计事实表必须 append-only。
	// 如未来需 retention，先设计 archive 表 / 文件导出，再实现独立 archival cron。
	cleanupClosedTickets()
	cleanupStaleCPACredentials()
	monitorApiLogRevenueOrphans()
}

// monitorApiLogRevenueOrphans 周期检查"ApiLog 主表写成功但 ApiLogRevenue 侧表写失败"
// 的孤儿行。这是 codex audit-integrity 发现的 silent failure：RecordApiLogRevenue 失败
// 仅 log，没有 reconcile 机制——加 metric 监控让 admin 第一时间察觉。
//
// fix P3（codex review verify-r4）：原实现把所有"成功 + cost>0 但无 revenue"的 api_log 都当
// 孤儿，但 pending_reconcile / unmetered 路径**故意**不写 revenue（没收到钱）→ 误报噪声。
// JOIN billing_entries 只对真有 subscription/balance 计费的请求告警。
//
// 只扫最近 1 小时内的孤儿（避免百万行级历史 api_log 拖死 SQLite）。
func monitorApiLogRevenueOrphans() {
	if !database.DB.Migrator().HasTable(&database.ApiLog{}) || !database.DB.Migrator().HasTable(&database.ApiLogRevenue{}) || !database.DB.Migrator().HasTable(&database.BillingEntry{}) {
		return
	}
	cutoff := time.Now().Add(-1 * time.Hour)
	var orphanCount int64
	err := database.DB.Raw(`SELECT COUNT(*) FROM api_logs a
		INNER JOIN billing_entries be
			ON be.related_type = 'api_log' AND be.related_id = a.id
			AND be.entry_type IN (?, ?)
		LEFT JOIN api_log_revenues r ON r.api_log_id = a.id
		WHERE a.created_at >= ?
		  AND a.status >= 200 AND a.status < 400
		  AND r.id IS NULL`,
		database.BillingTypeApiUsageSub, database.BillingTypeApiConsumeBalance, cutoff,
	).Scan(&orphanCount).Error
	if err != nil {
		log.Printf("[REVENUE-ORPHAN-MONITOR] query failed: %v", err)
		return
	}
	if orphanCount > 0 {
		log.Printf("[REVENUE-ORPHAN-MONITOR] 最近 1 小时内 %d 个 api_logs（已写 BillingEntry 但无 revenue 侧表行）—— RecordApiLogRevenue 重试 4 次仍失败，需 admin 排查",
			orphanCount)
	}
}

// cleanupStaleCPACredentials 物理删除已软删（disabled=true）超过 30 天且本地未再见到的 CPA 凭证缓存。
//
// 设计：CPA 端凭证被删/禁后，syncCPACredentials 第一时间把本地行 disabled 置 true（保留审计）。
// 但若 admin 在 CPA 上彻底删除该凭证，本地行永远不会再被 LastSeenAt 刷新——超过 30 天即可物理回收。
//
// 注意：这里 30 天判定用 LastSeenAt < cutoff（最近一次出现在 CPA 清单的时间），不是 UpdatedAt
// （UpdatedAt 会被任何 sync 触发的字段变更刷新）。
func cleanupStaleCPACredentials() {
	cutoff := time.Now().Add(-30 * 24 * time.Hour)
	res := database.DB.Where("disabled = ? AND last_seen_at < ?", true, cutoff).
		Delete(&database.CPACredential{})
	if res.Error != nil {
		log.Printf("[CPA-CRED-CRON] cleanup stale failed: %v", res.Error)
		return
	}
	if res.RowsAffected > 0 {
		log.Printf("[CPA-CRED-CRON] purged %d CPA credentials (disabled > 30d)", res.RowsAffected)
	}
}

// cleanupClosedTickets 物理删除关闭超 15 天的工单（含其全部消息）
const ticketCleanupBatchLimit = 500

func cleanupClosedTickets() {
	cutoff := time.Now().Add(-15 * 24 * time.Hour)
	// fix HIGH-1：Pluck 与 Delete 同事务，避免读取 id 列表后这些 ticket 被新消息污染
	// 但又被无条件删除（"已关闭+超 15d 后用户无法发消息"虽然在 PostTicketMessage 处校验过，
	// 但事务一致性是更基础的契约，比业务校验更可靠）
	var ids []uint
	err := database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&database.Ticket{}).
			Where("status = ? AND closed_at < ?", "closed", cutoff).
			Limit(ticketCleanupBatchLimit).
			Pluck("id", &ids).Error; err != nil {
			return fmt.Errorf("pluck closed ids: %w", err)
		}
		if len(ids) == 0 {
			return nil
		}
		if err := tx.Where("ticket_id IN ?", ids).Delete(&database.TicketMessage{}).Error; err != nil {
			return fmt.Errorf("delete messages: %w", err)
		}
		if err := tx.Where("id IN ?", ids).Delete(&database.Ticket{}).Error; err != nil {
			return fmt.Errorf("delete tickets: %w", err)
		}
		return nil
	})
	if len(ids) == 0 {
		return
	}
	if err != nil {
		log.Printf("[TICKET-CRON] cleanup tx failed: %v", err)
		return
	}
	log.Printf("[TICKET-CRON] purged %d closed tickets (>15 days)", len(ids))
	// 触达批次上限说明还有更多积压，下一轮会继续清；记录便于运维观察
	if len(ids) >= ticketCleanupBatchLimit {
		log.Printf("[TICKET-CRON] batch limit reached (%d), more eligible tickets remain — will continue next tick", ticketCleanupBatchLimit)
	}
}

// expireSubscriptions 处理已到期订阅
func expireSubscriptions() {
	now := time.Now()
	graceSec, _ := strconv.Atoi(getStrConfigStr("subscription_expired_grace_seconds", "60"))
	cutoff := now.Add(-time.Duration(graceSec) * time.Second)

	var subs []database.UserSubscription
	const expireBatchLimit = 200
	if err := database.DB.Where("status = ? AND end_at < ?", "active", cutoff).Limit(expireBatchLimit).Find(&subs).Error; err != nil {
		log.Printf("[SUB-CRON] expire query failed: %v — 跳过本轮", err)
		return
	}

	if len(subs) >= expireBatchLimit {
		log.Printf("[SUB-CRON] ⚠ 到期订阅积压：本轮已达 %d 上限，可能仍有未处理；下轮继续", expireBatchLimit)
	}

	for _, sub := range subs {
		// fix Major（codex 第四轮）：必须条件 UPDATE WHERE status='active'，
		// 否则在 cron 读 active 后、写 expired 前的窗口里，用户取消订阅写入 'canceled'
		// 会被 cron 这条无条件 UPDATE 覆盖回 'expired'，状态机被回滚污染。
		res := database.DB.Model(&database.UserSubscription{}).
			Where("id = ? AND status = ?", sub.ID, "active").
			Updates(map[string]any{"status": "expired"})
		if res.Error != nil {
			log.Printf("[SUB-CRON] expire sub %d failed: %v", sub.ID, res.Error)
			continue
		}
		if res.RowsAffected == 0 {
			// 并发被取消/退款 → 跳过通知，保留新状态
			log.Printf("[SUB-CRON] expire sub %d skipped: status raced (canceled/refunded)", sub.ID)
			continue
		}
		InvalidateUserSubscriptionCache(sub.UserID)

		// fix Minor（codex r11）：发通知前重读 status，避免 cron 标 expired 后 admin 立刻 refund，
		// 用户同时收到"到期"+"退款"两条通知造成困惑。重读到非 expired 时跳过到期通知。
		// fix Major（自审第十三轮）：原写法 `err == nil && status != expired` 让 err != nil
		// 落入"发通知"分支——若订阅在两次读之间被删除，会给已不存在的 sub 推送。
		// 改为 fail-safe：err != nil 也跳过通知，并日志。
		var fresh database.UserSubscription
		if err := database.DB.Select("id, status").First(&fresh, sub.ID).Error; err != nil {
			log.Printf("[SUB-CRON] expire sub %d notify skipped: fresh read failed: %v", sub.ID, err)
			continue
		}
		if fresh.Status != "expired" {
			log.Printf("[SUB-CRON] expire sub %d notify skipped: status changed to %s after cron set expired", sub.ID, fresh.Status)
			continue
		}

		var pkg database.Package
		pkgName := "（已删除套餐）"
		if err := database.DB.First(&pkg, sub.PackageID).Error; err == nil {
			pkgName = pkg.Name
		} else {
			log.Printf("[SUB-CRON] sub %d package %d not found: %v", sub.ID, sub.PackageID, err)
		}
		title := getStrConfigStr("notif_subscription_expired_title", "订阅已到期")
		body := getStrConfigStr("notif_subscription_expired_body", "您的「{package_name}」已到期")
		body = strings.ReplaceAll(body, "{package_name}", pkgName)
		// fix Minor（自审第十三轮）：原 dedupKey=nil → cron 重叠/重启窗口可能发两条 expired 通知。
		// 与 notifyExpiringSubscriptions 对齐，加 sub.ID 级 dedup（一个订阅 expired 一次即可）。
		dk := "expired:sub_" + strconv.Itoa(int(sub.ID))
		Dispatch(sub.UserID, "subscription_expired", "info", title, body, LinkUpgradeMine(), "查看", "subscription", sub.ID, &dk)
	}
	if len(subs) > 0 {
		log.Printf("[SUB-CRON] 处理了 %d 个到期订阅", len(subs))
	}
}

// notifyExpiringSubscriptions 提前 N 天发到期预警。
// 使用 dedup_key 跨进程去重：每个订阅每天最多一条预警通知。
func notifyExpiringSubscriptions() {
	warnDays, _ := strconv.Atoi(getStrConfigStr("subscription_expiring_warn_days", "3"))
	if warnDays <= 0 {
		return
	}
	now := time.Now()
	cutoff := now.Add(time.Duration(warnDays) * 24 * time.Hour)
	dayKey := now.Format("2006-01-02")

	var lastID uint
	for {
		var subs []database.UserSubscription
		if err := database.DB.Where("status = ? AND end_at > ? AND end_at < ? AND id > ?", "active", now, cutoff, lastID).
			Order("id ASC").Limit(200).Find(&subs).Error; err != nil {
			log.Printf("[SUB-CRON] expiring-warn query failed: %v — 跳过本轮", err)
			return
		}
		if len(subs) == 0 {
			break
		}

		// fix Major（自审第十三轮）：原循环内对每条 sub 调一次 First(&pkg) →
		// 200 条订阅 = 200 次 DB roundtrip。批量预加载消除 N+1。
		// 同时给 unmarshal 失败加日志（之前完全静默）。
		pkgIDSet := make(map[uint]struct{}, len(subs))
		for _, sub := range subs {
			pkgIDSet[sub.PackageID] = struct{}{}
		}
		pkgIDs := make([]uint, 0, len(pkgIDSet))
		for id := range pkgIDSet {
			pkgIDs = append(pkgIDs, id)
		}
		var pkgs []database.Package
		if len(pkgIDs) > 0 {
			if err := database.DB.Select("id, name").Where("id IN ?", pkgIDs).Find(&pkgs).Error; err != nil {
				// 退化但不阻塞：所有 pkgName 走 fallback 文案
				log.Printf("[SUB-CRON] expiring-warn pkg batch load failed: %v — 用 fallback 文案继续", err)
			}
		}
		pkgByID := make(map[uint]string, len(pkgs))
		for _, p := range pkgs {
			pkgByID[p.ID] = p.Name
		}

		for _, sub := range subs {
			lastID = sub.ID
			dedupKey := "expire_warn:sub_" + strconv.Itoa(int(sub.ID)) + ":" + dayKey
			pkgName, ok := pkgByID[sub.PackageID]
			if !ok {
				pkgName = "（已删除套餐）"
			}
			title := getStrConfigStr("notif_subscription_expiring_title", "订阅即将到期")
			body := getStrConfigStr("notif_subscription_expiring_body", "您的订阅 {days} 天后到期")
			days := strconv.Itoa(int(time.Until(sub.EndAt).Hours() / 24))
			body = strings.ReplaceAll(body, "{package_name}", pkgName)
			body = strings.ReplaceAll(body, "{days}", days)
			dk := dedupKey
			Dispatch(sub.UserID, "subscription_expiring", "warning", title, body, LinkUpgradeMine(), "续费", "subscription", sub.ID, &dk)
		}
	}
}

func getStrConfigStr(key, def string) string {
	SysConfigMutex.RLock()
	v := strings.TrimSpace(SysConfigCache[key])
	SysConfigMutex.RUnlock()
	if v == "" {
		return def
	}
	return v
}
