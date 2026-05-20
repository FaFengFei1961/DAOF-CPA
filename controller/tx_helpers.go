// Package controller / tx_helpers.go
//
// 跨流程的事务级 helper 与 sentinel —— 这些符号被多个无关链路（购买 / 退款 /
// 充值 / 用户管理 / coupon / 积分对账等）共享，所以从原文件抽出独立放在这里，
// 避免命名空间被某个具体流程的文件名占用造成误导。
//
// 抽出于 Phase E-3（2026-05-19，架构复审反馈）。
package controller

import (
	"errors"
	"fmt"

	"daof-cpa/database"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// errPriceOverflow 是购买/调额路径金额累加溢出 sentinel。
//
// fix CRITICAL Phase 4-codex（第二十四轮）：防 admin 设极端金额 + 大 qty 导致 int64 溢出
// 回绕成负值穿透余额检查。被 subscription_purchase.go / user.go 多个流程使用。
var errPriceOverflow = errors.New("price * qty overflow int64")

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
//
// 调用流程：subscription_purchase / subscription_admin / subscription_grant /
// topup_admin / coupon / token / billing_reconcile / user.go 等都走这一把锁，
// 保证同一用户的并发写路径全部串行化。
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
