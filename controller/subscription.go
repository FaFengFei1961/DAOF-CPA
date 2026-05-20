// Package controller / subscription.go
//
// 用户视角订阅查询 / 取消 + 共享 helper（lockUserForUpdate）。
// 购买流程在 subscription_purchase.go；admin 视角在 subscription_admin.go；
// 显示聚合在 subscription_view.go；快照构建在 subscription_snapshot.go。
package controller

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"daof-cpa/database"
	"daof-cpa/proxy"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// lockUserForUpdate 跨数据库方言提供 user 行级排他锁。
//
// fix Major（codex 第九轮）：GORM SQLite 驱动会**忽略** clause.Locking{Strength: "UPDATE"}
// 子句（FOR UPDATE 在 SQLite 不存在），所以 PostgreSQL 上有效的行锁在 SQLite 下完全失效，
// 同 user 并发购买/创建 token 不能被串行化（snapshot isolation 让两个事务都读到 count=0
// 后各自 INSERT，busy_timeout 仅延后 UPDATE 而非 SELECT）。
//
// 跨方言策略：
//   - PostgreSQL/MySQL: clause.Locking → 真正的行级排他锁
//   - SQLite: no-op UPDATE 触发 RESERVED 锁——立刻把事务从 reader 升级为 writer，
//     让其他并发事务的"写"操作在 PRAGMA busy_timeout=5000ms 内排队。
//     这等价于 BEGIN IMMEDIATE 的效果（GORM 不直接暴露事务模式）。
//
// 调用方必须在事务内（tx 必须是 *gorm.DB 的事务句柄）。
func lockUserForUpdate(tx *gorm.DB, userID uint) error {
	dialect := tx.Dialector.Name()
	if dialect == "sqlite" {
		// no-op UPDATE 触发 RESERVED → 升级 writer，与其他写事务串行化
		res := tx.Exec("UPDATE users SET updated_at = updated_at WHERE id = ?", userID)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return fmt.Errorf("user %d not found", userID)
		}
		return nil
	}
	// PostgreSQL / MySQL：FOR UPDATE 行锁
	var u database.User
	return tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", userID).First(&u).Error
}


// MySubscriptions 查询我的活跃订阅。批量预加载 usage + package name 避免 N+1。
func MySubscriptions(c *fiber.Ctx) error {
	user, err := getCurrentUser(c)
	if err != nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}
	var subs []database.UserSubscription
	if err := database.DB.Where("user_id = ?", user.ID).
		Order("consumption_order ASC").Find(&subs).Error; err != nil {
		log.Printf("[SUB] list user=%d failed: %v", user.ID, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}

	type userSubscriptionWire database.UserSubscription
	type subItem struct {
		*userSubscriptionWire
		PurchasedUnitPriceUSD float64                      `json:"purchased_unit_price_usd"`
		Usage                 []database.SubscriptionUsage `json:"usage"`
		UsageSummary          []subscriptionUsageSummary   `json:"usage_summary"`
		PackageName           string                       `json:"package_name"`
	}
	if len(subs) == 0 {
		return c.JSON(fiber.Map{"success": true, "data": []subItem{}})
	}

	subIDs := make([]uint, 0, len(subs))
	pkgIDs := make([]uint, 0, len(subs))
	pkgIDSet := make(map[uint]bool)
	for _, s := range subs {
		subIDs = append(subIDs, s.ID)
		if !pkgIDSet[s.PackageID] {
			pkgIDs = append(pkgIDs, s.PackageID)
			pkgIDSet[s.PackageID] = true
		}
	}

	// fix Major（自审第十三轮）：原 usage 查询失败仅日志、继续返回空进度条，
	// 用户误判"用量为 0"重复购买。fail-closed：失败立即 500，让前端重试或显示"加载中"。
	var allUsages []database.SubscriptionUsage
	if err := database.DB.Where("subscription_id IN ?", subIDs).Find(&allUsages).Error; err != nil {
		log.Printf("[SUB] usage query failed: %v", err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}
	usageBySubID := make(map[uint][]database.SubscriptionUsage, len(subs))
	for _, u := range allUsages {
		usageBySubID[u.SubscriptionID] = append(usageBySubID[u.SubscriptionID], u)
	}

	// fix Major（自审第十三轮）：package 查询失败也 fail-closed。
	// 退化展示"未知套餐名"会让用户对自己的订阅产生疑问。
	var pkgs []database.Package
	if err := database.DB.Where("id IN ?", pkgIDs).Find(&pkgs).Error; err != nil {
		log.Printf("[SUB] package name query failed: %v", err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}
	pkgNameByID := make(map[uint]string, len(pkgs))
	for _, p := range pkgs {
		pkgNameByID[p.ID] = p.Name
	}

	out := make([]subItem, 0, len(subs))
	for _, s := range subs {
		packageName := strings.TrimSpace(pkgNameByID[s.PackageID])
		if packageName == "" {
			packageName = readPackageNameFromSnapshot(s.PackageSnapshot)
		}
		wireSub := userSubscriptionWire(s)
		out = append(out, subItem{
			userSubscriptionWire:  &wireSub,
			PurchasedUnitPriceUSD: database.MicroToUSD(s.PurchasedUnitPriceUSD),
			Usage:                 usageBySubID[s.ID],
			UsageSummary:          buildSubscriptionUsageSummary(s.PackageSnapshot, usageBySubID[s.ID]),
			PackageName:           packageName,
		})
	}
	return c.JSON(fiber.Map{"success": true, "data": out})
}

// CancelSubscription 用户**取消**订阅。仅标记 status=canceled，不发生任何资金移动。
//
// 业务模型（产品确认）：
//   - 用户端"取消"= 立即停止该订阅消费 quota（订阅引擎下次决策不再命中）
//   - **退款是独立流程**：用户走客服工单（CustomerMessage）提交退款申请，
//     admin 协商金额后调 AdminRefundSubscription 触发实际退款
//
// 历史 bug 修复（用户产品反馈第十轮）：原实现按"剩余时间比例"自动退款，存在两大问题：
//  1. 业务上不符合"协商退款"的运营模型——错把 cancel 等同于退款
//  2. 安全上有套利漏洞——攻击者买月包→1 小时耗尽 quota→取消近全额退款→重复
//
// 现在 cancel 只动状态机，不动钱。所有退款必须经管理员审核（AdminRefundSubscription）。
func CancelSubscription(c *fiber.Ctx) error {
	user, err := getCurrentUser(c)
	if err != nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_PARAMS"})
	}
	var sub database.UserSubscription
	if err := database.DB.First(&sub, id).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"success": false, "message_code": "ERR_NOT_FOUND"})
	}
	if sub.UserID != user.ID {
		return c.Status(403).JSON(fiber.Map{"success": false, "message_code": "ERR_FORBIDDEN"})
	}
	if sub.Status != "active" {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_NOT_ACTIVE"})
	}

	now := time.Now()
	// 条件 UPDATE 防并发双取消（虽不再退款，仍要保证状态机原子性）
	res := database.DB.Model(&database.UserSubscription{}).
		Where("id = ? AND status = ?", sub.ID, "active").
		Updates(map[string]any{"status": "canceled", "canceled_at": now})
	if res.Error != nil {
		log.Printf("[SUB] cancel update failed user=%d sub=%d err=%v", user.ID, sub.ID, res.Error)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_UPDATE"})
	}
	if res.RowsAffected == 0 {
		return c.Status(409).JSON(fiber.Map{"success": false, "message_code": "ERR_ALREADY_CANCELED"})
	}
	proxy.InvalidateUserSubscriptionCache(user.ID)

	// 通知（仅"已取消"，不提退款金额；用户若想退款应另开工单）
	pkgName := readPackageNameFromSnapshot(sub.PackageSnapshot)
	if pkgName == "" {
		var pkg database.Package
		if database.DB.Select("id, name").First(&pkg, sub.PackageID).Error == nil {
			pkgName = pkg.Name
		}
	}
	title := readSysConfigCached("notif_subscription_canceled_title", "您的订阅已取消")
	bodyTpl := readSysConfigCached("notif_subscription_canceled_body", "您的【{{plan_name}}】订阅已于 {{cancel_time}} 取消。如需恢复服务，请前往套餐中心重新订阅。")
	body := strings.ReplaceAll(bodyTpl, "{{plan_name}}", pkgName)
	body = strings.ReplaceAll(body, "{{cancel_time}}", now.Format("2006-01-02 15:04:05"))
	dedupKey := fmt.Sprintf("cancel:sub_%d", sub.ID)
	proxy.Dispatch(user.ID, "subscription", "info", title, body,
		proxy.LinkTickets(), "提交工单", "subscription", sub.ID, &dedupKey)

	return c.JSON(fiber.Map{
		"success":      true,
		"message_code": "SUCCESS_CANCELED",
		"message":      "订阅已取消。如需退款，请通过客服工单提交申请",
	})
}
