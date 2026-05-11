// Package proxy / balance_consume.go
//
// 余额消费控制（三段消费模型的第三段）。
//
// 流程：订阅(subscription) → 增量包(addon) → 余额(user.Quota)。
// 前两段都不可用时，subscription_engine 调 CheckBalanceConsumeAllowed 决定能否走余额。
//
// 设计要点：
//   - 默认关闭（user.BalanceConsumeEnabled=false），最严策略
//   - 限额可设（0 = 不限）
//   - 周期窗口（默认 30 天）到期自动重置 BalanceConsumedInWindow=0
//   - 重置用条件 UPDATE 防并发覆盖
//   - 累加消费用 gorm.Expr 防 lost update
//
// fix MAJOR M22-A1 Phase 1（codex 第二十三轮）：所有金额单位 micro_usd（int64）。
package proxy

import (
	"log"
	"time"

	"daof-ai-hub/database"

	"gorm.io/gorm"
)

// BalanceConsumeStatus 给前端展示用的当前状态视图。
// 单位均为 micro_usd（int64），前端除以 1e6 显示美元数。
type BalanceConsumeStatus struct {
	Enabled                  bool      `json:"enabled"`
	LimitMicroUSD            int64     `json:"limit_micro_usd"`
	WindowSeconds            int       `json:"window_seconds"`
	WindowStartAt            time.Time `json:"window_start_at"`
	ConsumedInWindowMicroUSD int64     `json:"consumed_in_window_micro_usd"`
	ResetsAt                 time.Time `json:"resets_at"`
}

// TryConsumeBalanceTx 在调用方事务内原子检查并扣减用户余额消费窗口配额。
//
// fix CRITICAL C-B5（codex 第二十一轮）：原 TryConsumeBalance 与 deductQuotaAtomic 不在
// 同一事务——若窗口累加成功但扣费 / 账单写入失败（DB 故障 / panic），
// BalanceConsumedInWindow 已增长但 user.Quota 没扣，资金账漂移。
// 调用方在扣费事务里直接调本函数，所有状态变更走同一 commit，要么一起成功要么一起回滚。
//
// fix CRITICAL C22-1（codex 第二十二轮）：上游已交付服务时调用方必扣 quota（契约不可破）。
// 增加 forceTrack 参数：
//   - forceTrack=false（precheck）：限额超返回 false，调用方应拒绝服务（不扣费）
//   - forceTrack=true（commit 已服务）：无条件累加（即使超限）。下次 precheck 会拦住。
func TryConsumeBalanceTx(tx *gorm.DB, userID uint, deltaMicroUSD int64, forceTrack bool) bool {
	if tx == nil || userID == 0 || deltaMicroUSD <= 0 {
		return false
	}

	// 取一份快照判断窗口状态（事务内读，加 FOR UPDATE 等价的串行化交给 lockUser 父级路径）
	var u database.User
	if err := tx.Select("id, balance_consume_enabled, balance_consume_limit_usd, balance_consume_window_seconds, balance_consume_window_start_at, balance_consumed_in_window").
		Where("id = ?", userID).First(&u).Error; err != nil {
		log.Printf("[BALANCE-CONSUME-TX] load user=%d err=%v", userID, err)
		return false
	}
	if !u.BalanceConsumeEnabled {
		return false
	}

	now := time.Now()
	expired := u.BalanceConsumeWindowStartAt == nil ||
		now.Sub(*u.BalanceConsumeWindowStartAt).Seconds() > float64(u.BalanceConsumeWindowSeconds)

	if expired {
		q := tx.Model(&database.User{}).Where("id = ?", userID)
		if u.BalanceConsumeWindowStartAt == nil {
			q = q.Where("balance_consume_window_start_at IS NULL")
		} else {
			q = q.Where("balance_consume_window_start_at = ?", *u.BalanceConsumeWindowStartAt)
		}
		res := q.Updates(map[string]any{
			"balance_consume_window_start_at": now,
			"balance_consumed_in_window":      int64(0),
		})
		if res.Error != nil {
			log.Printf("[BALANCE-CONSUME-TX] reset window user=%d err=%v", userID, res.Error)
			return false
		}
		// res.RowsAffected==0 表示并发已重置；后续 UPDATE 会用最新状态判定
	}

	// forceTrack=true：上游已交付服务，无论窗口是否超限都必须累加，保证窗口记账完整
	if forceTrack {
		res := tx.Model(&database.User{}).
			Where("id = ? AND balance_consume_enabled = ?", userID, true).
			Update("balance_consumed_in_window", gorm.Expr("balance_consumed_in_window + ?", deltaMicroUSD))
		if res.Error != nil {
			log.Printf("[BALANCE-CONSUME-TX] forceTrack accumulate user=%d err=%v", userID, res.Error)
			return false
		}
		return res.RowsAffected > 0
	}

	// forceTrack=false（precheck 路径）：检查限额，超限拒绝
	q := tx.Model(&database.User{}).Where("id = ? AND balance_consume_enabled = ?", userID, true)
	if u.BalanceConsumeLimitUSD > 0 {
		q = q.Where("balance_consume_limit_usd = 0 OR balance_consumed_in_window + ? <= balance_consume_limit_usd", deltaMicroUSD)
	}
	res := q.Update("balance_consumed_in_window", gorm.Expr("balance_consumed_in_window + ?", deltaMicroUSD))
	if res.Error != nil {
		log.Printf("[BALANCE-CONSUME-TX] atomic consume user=%d err=%v", userID, res.Error)
		return false
	}
	return res.RowsAffected > 0
}

// CheckBalanceConsumeAllowed 仅做"是否允许"快速预检（用于 Decide 阶段决定路径）。
// **不**修改任何状态。真正的扣减在 TryConsumeBalance 里以原子方式完成。
//
// 注意：这里返回 true 不保证后续 TryConsumeBalance 一定成功——并发请求可能在你检查后扣光余额。
// 调用方必须接受这一点：Decide 用此函数粗筛"用户允许走余额"，最终扣费用 TryConsumeBalance。
func CheckBalanceConsumeAllowed(user *database.User, deltaMicroUSD int64) bool {
	if user == nil || !user.BalanceConsumeEnabled {
		return false
	}
	if user.BalanceConsumeLimitUSD <= 0 {
		return true // 不限额直接允许
	}
	now := time.Now()
	// 窗口过期视为重置后未消费，必然允许
	if user.BalanceConsumeWindowStartAt == nil ||
		now.Sub(*user.BalanceConsumeWindowStartAt).Seconds() > float64(user.BalanceConsumeWindowSeconds) {
		return deltaMicroUSD <= user.BalanceConsumeLimitUSD
	}
	return user.BalanceConsumedInWindow+deltaMicroUSD <= user.BalanceConsumeLimitUSD
}

// GetBalanceConsumeStatus 给前端展示用的状态聚合（含预测的下次重置时间）
func GetBalanceConsumeStatus(user *database.User) BalanceConsumeStatus {
	now := time.Now()
	startAt := now
	if user.BalanceConsumeWindowStartAt != nil {
		startAt = *user.BalanceConsumeWindowStartAt
	}
	resets := startAt.Add(time.Duration(user.BalanceConsumeWindowSeconds) * time.Second)
	// 如果当前窗口已过期，下次重置就是"下次首次消费时"——这里展示为现在
	if now.After(resets) {
		resets = now
	}
	return BalanceConsumeStatus{
		Enabled:                  user.BalanceConsumeEnabled,
		LimitMicroUSD:            user.BalanceConsumeLimitUSD,
		WindowSeconds:            user.BalanceConsumeWindowSeconds,
		WindowStartAt:            startAt,
		ConsumedInWindowMicroUSD: user.BalanceConsumedInWindow,
		ResetsAt:                 resets,
	}
}
