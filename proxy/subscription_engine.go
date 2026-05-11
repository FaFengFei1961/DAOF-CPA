// Package proxy / subscription_engine.go
//
// 订阅扣费引擎。
//
// 核心算法：
//  1. 取用户活跃订阅，按 ConsumptionOrder ASC（FIFO）
//  2. 对每个订阅，按 plan.priority ASC 匹配
//  3. 原子 upsert SubscriptionUsage 累加扣费
//  4. 全部失败 → 回 fallback 信号给上层（fallback_balance / 402）
//
// 所有阈值 / 默认值 / 文案均通过 SysConfig 配置，不写死。
package proxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"path"
	"sort"
	"strings"
	"time"

	"daof-ai-hub/database"

	"gorm.io/gorm"
)

// errSubInactive 哨兵：在 atomicConsume 事务结束前发现订阅已被取消/退款/过期，
// 用 GORM 事务 return-non-nil-rolls-back 语义把已写入的 usage 行回滚。
//
// fix CRITICAL C19-3（codex 第十九轮）：原 atomicConsume 顶部一次性 SELECT 订阅状态后，
// 后续 INSERT/UPDATE usage 行之间订阅可能被另一事务（cancel/refund）改成 cancelled。
// 此处 return errSubInactive → tx 回滚 → 不写入"过期订阅的脏 usage 行"。
var errSubInactive = errors.New("subscription became inactive during consume")

// EngineDecision 是引擎对一次请求的决策结果
type EngineDecision struct {
	Allowed           bool
	SubscriptionID    uint
	QuotaPlanID       uint
	ConsumedUnit      string
	ConsumedDelta     float64
	FallbackToBalance bool
	BlockReason       string
	BlockMessage      string
	// ProductType 命中订阅时填 "subscription" 或 "addon"，便于账单区分扣自哪个产品类型。
	// 未命中（FallbackToBalance / 拒绝）时为空字符串。
	ProductType string
	// fix CRITICAL R23+2-C3（codex 全方面审查）：DB 加载订阅失败时不能 fallback 到余额，
	// 否则有有效订阅的用户在 DB 抖动期间被错误扣美元。NeedsRetry=true 让调用方返回 503
	// 让客户端走 backoff 重试，等 DB 恢复。
	NeedsRetry bool
}

// EngineRequest 单次扣费请求的输入
type EngineRequest struct {
	UserID       uint
	ModelName    string
	InputTokens  int
	OutputTokens int
	IsPrecheck   bool
}

// Decide 决策一次请求该走哪条路径（三段消费模型）：
//
//  1. 订阅 (subscription)  ─ 按 ConsumptionOrder FIFO
//  2. 增量包 (addon)        ─ 订阅用尽后扣，组内 FIFO
//  3. 余额 (user.Quota)     ─ 由用户 BalanceConsumeEnabled 控制 + 窗口限额
//
// 注：fallback 到余额的实际限额检查在 relay/billing 扣 quota 路径里调
// proxy.CheckBalanceConsumeAllowed；这里只决定方向（FallbackToBalance=true）。
func Decide(req EngineRequest) EngineDecision {
	subs, err := GetUserActiveSubscriptions(req.UserID)
	if err != nil {
		// fix CRITICAL R23+2-C3：DB 加载失败 fail-closed，让 stream.go 返回 503
		return EngineDecision{
			Allowed:      false,
			NeedsRetry:   true,
			BlockReason:  "subscription_load_failed",
			BlockMessage: "订阅状态暂时不可用，请稍后重试",
		}
	}
	// 按 product_type 优先级排序，组内 FIFO。优先级数值越小越先扣。
	sort.SliceStable(subs, func(i, j int) bool {
		pi := productPriority(productTypeOfCached(subs[i]))
		pj := productPriority(productTypeOfCached(subs[j]))
		if pi != pj {
			return pi < pj
		}
		return subs[i].Subscription.ConsumptionOrder < subs[j].Subscription.ConsumptionOrder
	})

	for _, cs := range subs {
		d := trySharedQuota(cs, req)
		if d.Allowed {
			d.ProductType = productTypeOfCached(cs)
			return d
		}
		// fix CRITICAL C2：trySharedQuota 触达 DB 故障 → 直接返回不允许 + NeedsRetry，
		// 严禁继续尝试下一订阅或 fallback 到余额（防止 DB 故障期间被错误扣 USD）。
		if d.NeedsRetry {
			return d
		}
	}

	// 所有订阅 + 增量包都没命中 → fallback 到余额
	if engineFallbackToQuota() {
		return EngineDecision{
			Allowed:           true,
			FallbackToBalance: true,
		}
	}
	return EngineDecision{
		Allowed:      false,
		BlockReason:  "no_subscription_match",
		BlockMessage: get402Message(),
	}
}

// productTypeOfCached 从 CachedSubscription.Snapshot 读 product_type，缺省返回 subscription
func productTypeOfCached(cs *CachedSubscription) string {
	if cs == nil || cs.Snapshot == nil {
		return "subscription"
	}
	if t, ok := cs.Snapshot["product_type"].(string); ok && t != "" {
		return t
	}
	return "subscription"
}

// productPriority 三段消费排序优先级（数字越小越先扣）。未来加新 type 在这里登记。
func productPriority(productType string) int {
	switch productType {
	case "subscription":
		return 1
	case "addon":
		return 2
	default:
		return 99 // 未知类型最后扣，并在日志里能看出来
	}
}

// ─── Shared Quota 路径 ────────────────────────────────────────────

func trySharedQuota(cs *CachedSubscription, req EngineRequest) EngineDecision {
	sub := cs.Subscription
	plans := extractPlansFromSnapshot(cs.Snapshot)
	if len(plans) == 0 {
		return EngineDecision{Allowed: false, BlockReason: "no_plans"}
	}
	for _, plan := range plans {
		if !matchModel(plan.ModelMatch, req.ModelName) {
			continue
		}
		delta, unit := computeDelta(plan, req)
		if delta < 0 {
			continue
		}
		bucket := normalizeModelBucket(plan, req.ModelName)
		// multiplier 只放大限额，不放大单次消费 delta。
		// fix MAJOR M5（codex 第二十轮）：multiplier 必须 finite + 0 < v ≤ 100。
		// admin CRUD 路径已校验，直改 DB 的异常值也在引擎侧兜底。
		mult := plan.QuantityMultiplier
		if math.IsNaN(mult) || math.IsInf(mult, 0) || mult <= 0 {
			mult = 1.0
		}
		const engineMaxMultiplier = 100.0
		if mult > engineMaxMultiplier {
			log.Printf("[ENGINE] plan %d multiplier %v exceeds cap, clamping to %v",
				plan.ID, mult, engineMaxMultiplier)
			mult = engineMaxMultiplier
		}
		effectiveDelta := delta
		effectiveLimit := plan.LimitValue * mult
		ok, dbErr := atomicConsume(sub.ID, plan.ID, sub.UserID, bucket, effectiveDelta, plan.WindowSeconds, effectiveLimit, req.IsPrecheck)
		if dbErr != nil {
			// fix CRITICAL C2（codex 第二十轮）：DB 故障必须 fail-closed，绝不能 fallback 到余额。
			// NeedsRetry=true + Allowed=false 让 stream.go 返回 503 让客户端重试，
			// 而不是把扣费默默路由到余额（导致双重计费）。
			return EngineDecision{
				Allowed:      false,
				NeedsRetry:   true,
				BlockReason:  "subscription_db_error",
				BlockMessage: "订阅扣费暂时不可用，请稍后重试",
			}
		}
		if ok {
			return EngineDecision{
				Allowed:        true,
				SubscriptionID: sub.ID,
				QuotaPlanID:    plan.ID,
				ConsumedUnit:   unit,
				ConsumedDelta:  effectiveDelta,
			}
		}
		if plan.OverflowStrategy == "next_subscription" {
			return EngineDecision{Allowed: false, BlockReason: "plan_full_skip_sub"}
		}
	}

	return EngineDecision{Allowed: false, BlockReason: "no_plan_in_sub_matched"}
}

// snapshotPlan 是从 package_snapshot 提取的简化 plan 结构
type snapshotPlan struct {
	ID                 uint    `json:"id"`
	ModelMatch         string  `json:"model_match"`
	LimitUnit          string  `json:"limit_unit"`
	LimitValue         float64 `json:"limit_value"`
	WindowSeconds      int     `json:"window_seconds"`
	WeightFactor       string  `json:"weight_factor"`
	Priority           int     `json:"priority"`
	OverflowStrategy   string  `json:"overflow_strategy"`
	QuantityMultiplier float64 `json:"quantity_multiplier"`
}

func extractPlansFromSnapshot(snap map[string]any) []snapshotPlan {
	if snap == nil {
		return nil
	}
	rawPlans, ok := snap["plans"].([]any)
	if !ok {
		return nil
	}
	out := make([]snapshotPlan, 0, len(rawPlans))
	for _, rp := range rawPlans {
		m, ok := rp.(map[string]any)
		if !ok {
			continue
		}
		plan := snapshotPlan{
			ID:                 uintFromAny(m["id"]),
			ModelMatch:         stringFromAny(m["model_match"]),
			LimitUnit:          stringFromAny(m["limit_unit"]),
			LimitValue:         floatFromAny(m["limit_value"]),
			WindowSeconds:      intFromAny(m["window_seconds"]),
			WeightFactor:       stringFromAny(m["weight_factor"]),
			Priority:           intFromAny(m["priority"]),
			OverflowStrategy:   stringFromAny(m["overflow_strategy"]),
			QuantityMultiplier: floatFromAny(m["quantity_multiplier"]),
		}
		if plan.QuantityMultiplier <= 0 {
			plan.QuantityMultiplier = 1.0
		}
		out = append(out, plan)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Priority < out[j].Priority })
	return out
}

// matchModel 用 glob 匹配规则。空数组 = 匹配所有。
//
// fix CRITICAL C-B4（codex 第二十一轮）：原 JSON 解析失败 fallback 到 true，
// 任何配置错误（typo / 空字符 / 超长字符串）都让低价 plan 匹配所有模型 —— 资金风险。
// 改为 fail-closed：解析失败 log + return false，运维必须看日志修复配置。
func matchModel(modelMatchJSON, model string) bool {
	if modelMatchJSON == "" || modelMatchJSON == "[]" {
		return true
	}
	var patterns []string
	if err := json.Unmarshal([]byte(modelMatchJSON), &patterns); err != nil {
		log.Printf("[ENGINE] matchModel: invalid model_match json (rejecting all matches as defense): %q err=%v", modelMatchJSON, err)
		return false // fail-closed：解析失败不允许通配
	}
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		if matched, _ := path.Match(p, model); matched {
			return true
		}
	}
	return false
}

func normalizeModelBucket(plan snapshotPlan, model string) string {
	var patterns []string
	// fix LOW（codex 第十九轮）：原 _ = json.Unmarshal 静默失败 → 配置漂移（plan.ModelMatch 损坏）
	// 时按"无匹配"返回原 model 字符串，规则引擎得不到诊断信息。改为 log 异常，patterns 仍是空切片
	// 让逻辑保持原行为（fallback to raw model）。
	if err := json.Unmarshal([]byte(plan.ModelMatch), &patterns); err != nil {
		log.Printf("[ENGINE] normalizeModelBucket: invalid plan.ModelMatch json plan_id=%d err=%v", plan.ID, err)
	}
	for _, p := range patterns {
		if matched, _ := path.Match(p, model); matched {
			return p
		}
	}
	return model
}

// computeDelta 计算单次请求的"原始消费 delta"（不含 QuantityMultiplier）。
//
// fix CRITICAL C-B3（codex 第二十一轮）：multiplier 作用于"限额"而非"消费"，
// 这里只算 raw delta；caller 在 atomicConsume 调用前用 effectiveLimit = LimitValue * multiplier。
// 之前做反向 → 倍数套餐反而更快耗尽，业务语义错误。
func computeDelta(plan snapshotPlan, req EngineRequest) (float64, string) {
	weightSingle, weightInOut := parseWeightFactor(plan.WeightFactor, req.ModelName)

	switch plan.LimitUnit {
	case "messages":
		return 1.0 * weightSingle, "messages"
	case "input_tokens":
		return float64(req.InputTokens) * weightSingle, "input_tokens"
	case "output_tokens":
		return float64(req.OutputTokens) * weightSingle, "output_tokens"
	case "total_tokens":
		return float64(req.InputTokens+req.OutputTokens) * weightSingle, "total_tokens"
	case "weighted_tokens":
		if weightInOut.HasInOut {
			d := float64(req.InputTokens)*weightInOut.Input + float64(req.OutputTokens)*weightInOut.Output
			return d, "weighted_tokens"
		}
		return float64(req.InputTokens+req.OutputTokens) * weightSingle, "weighted_tokens"
	case "usd_equivalent":
		if weightInOut.HasInOut {
			d := (float64(req.InputTokens)*weightInOut.Input + float64(req.OutputTokens)*weightInOut.Output) / 1_000_000.0
			return d, "usd_equivalent"
		}
		log.Printf("[ENGINE] plan %d 用 usd_equivalent 但 weight_factor 未配置 input/output 分别价格，跳过", plan.ID)
		return -1, "usd_equivalent"
	}
	return 1.0, plan.LimitUnit
}

type weightInOut struct {
	HasInOut bool
	Input    float64
	Output   float64
}

func parseWeightFactor(weightJSON, model string) (single float64, inout weightInOut) {
	single = 1.0
	if weightJSON == "" || weightJSON == "{}" {
		return
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(weightJSON), &raw); err != nil {
		return
	}
	for pattern, val := range raw {
		matched := false
		if pattern == model {
			matched = true
		} else if m, _ := path.Match(pattern, model); m {
			matched = true
		}
		if !matched {
			continue
		}
		switch v := val.(type) {
		case float64:
			single = v
			return
		case map[string]any:
			inout.HasInOut = true
			inout.Input = floatFromAny(v["input"])
			inout.Output = floatFromAny(v["output"])
			return
		}
	}
	return
}

// atomicConsume 用 SQL 原子操作完成"窗口边界判定 + 累加 + 上限检查"。
//
// fix CRITICAL C19-3（codex 第十九轮）：完整事务化 + 提交前再校验订阅活性。
//
// 之前结构：[读 sub] → [写 usage（数据库.DB）] → 无最终验证。
// 漏洞：cancel/refund 事务可能在「读 sub」与「写 usage」之间提交，导致 usage 行被写到已退款/取消订阅。
//
// 现在结构：
//   - 整体包在 database.DB.Transaction 里，所有 DB 操作走 tx
//   - precheck 路径不写库（只读 + 判 sub 活），事务空 commit
//   - 真扣费路径写完 usage 后用 verifySubStillActive(tx, ...) re-SELECT 订阅状态：
//     SQLite write tx 是 serializable（其他 writer 不能在我们持锁期间 commit）
//     PostgreSQL READ COMMITTED tx 内 SELECT 也能看到其他 tx 的 commit
//     —— tx 内 SELECT 跨方言一致 + 杜绝 SetMaxOpenConns(1) 下的自死锁（M-A2 第二十一轮）
//     若另一事务已 commit cancel → 返回 errSubInactive → tx 回滚已写的 usage 行
//   - safeAsync USAGE-WARN 移到事务 commit 之后才触发（避免回滚后还在错误地发"使用率超限"通知）
//
// userID 用于扣费成功后异步触发使用率阈值预警（MaybeFireUsageWarn）。
// precheck 路径不触发预警（只是预检未真扣费）。
//
// fix CRITICAL-3（旧）：原代码 precheck 路径跳过订阅状态检查 → 上游会基于"过期订阅"做错误决策。
// 现在不论 precheck 还是 commit，都先验证订阅 active+未过期。
// atomicConsume 在事务内尝试扣减额度。返回值语义：
//
//	(true, nil)      — 扣费成功
//	(false, nil)     — 业务拒绝（订阅过期 / 撞上限 / 并发抢占），上层应继续尝试下一条订阅
//	(false, non-nil) — DB 故障，上层必须 fail-closed，**不允许**fallback 到余额扣费
//
// fix CRITICAL C2（codex 第二十轮）：原签名只返回 bool，DB 写失败被折叠为 false →
// 上层 Decide 把"DB 写失败"和"业务额度不足"同等处理，最终错误 fallback 到余额扣费 ——
// 用户该减的订阅 quota 没减（事务回滚），却被扣了 USD（balance 路径），双重计费。
func atomicConsume(subID, planID, userID uint, bucket string, delta float64, windowSec int, limitValue float64, isPrecheck bool) (bool, error) {
	var (
		allowed         bool
		wantWarn        bool
		warnBefore      float64
		warnAfter       float64
		warnWindowStart time.Time
	)

	txErr := database.DB.Transaction(func(tx *gorm.DB) error {
		var sub database.UserSubscription
		if err := tx.Select("id, status, end_at").
			Where("id = ? AND status = ? AND end_at > ?", subID, "active", time.Now()).
			First(&sub).Error; err != nil {
			// fix CRITICAL C23-A1（codex 第二十三轮）：必须区分 NotFound 与真实 DB 故障。
			// 仅 ErrRecordNotFound = 业务"订阅不再 active" → errSubInactive，让上层尝试下一订阅。
			// 其他错误（DB 连接断 / 表损坏）= 真实故障 → 必须冒泡，让 trySharedQuota 设 NeedsRetry，
			// 严禁让缓存命中的 DB 故障静默 fallback 到余额扣费（双重计费风险）。
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errSubInactive
			}
			return fmt.Errorf("verify sub at consume entry: %w", err)
		}
		now := time.Now()

		var usage database.SubscriptionUsage
		err := tx.Where("subscription_id = ? AND quota_plan_id = ? AND model_bucket = ?",
			subID, planID, bucket).First(&usage).Error

		if errors.Is(err, gorm.ErrRecordNotFound) {
			if delta > limitValue && limitValue > 0 {
				// allowed=false 默认值，空 commit
				return nil
			}
			if isPrecheck {
				allowed = true
				return nil
			}
			windowEnd := now
			if windowSec > 0 {
				windowEnd = now.Add(time.Duration(windowSec) * time.Second)
			} else {
				windowEnd = now.Add(365 * 24 * time.Hour)
			}
			newRow := database.SubscriptionUsage{
				SubscriptionID: subID,
				QuotaPlanID:    planID,
				ModelBucket:    bucket,
				WindowStartAt:  now,
				WindowEndAt:    windowEnd,
				ConsumedValue:  delta,
				RequestCount:   1,
			}
			if cerr := tx.Create(&newRow).Error; cerr != nil {
				// G-M2 兜底：PostgreSQL 上并发首插可能撞 unique 约束 → 重读后走 accumulate 路径
				if existing := (database.SubscriptionUsage{}); tx.
					Where("subscription_id = ? AND quota_plan_id = ? AND model_bucket = ?", subID, planID, bucket).
					First(&existing).Error == nil {
					usage = existing
					err = nil
					// fall through to accumulate
				} else {
					log.Printf("[ENGINE] usage create failed sub=%d plan=%d bucket=%s err=%v", subID, planID, bucket, cerr)
					return fmt.Errorf("usage create: %w", cerr)
				}
			} else {
				// 新窗口首扣：before=0
				if vErr := verifySubStillActive(tx, subID); vErr != nil {
					return vErr
				}
				allowed = true
				wantWarn = true
				warnBefore = 0
				warnAfter = delta
				warnWindowStart = now
				return nil
			}
		} else if err != nil {
			log.Printf("[ENGINE] usage query failed sub=%d plan=%d bucket=%s err=%v", subID, planID, bucket, err)
			return fmt.Errorf("usage query: %w", err)
		}

		if windowSec > 0 && now.After(usage.WindowEndAt) {
			if delta > limitValue && limitValue > 0 {
				return nil
			}
			if isPrecheck {
				allowed = true
				return nil
			}
			newEnd := now.Add(time.Duration(windowSec) * time.Second)
			// 防止两个并发请求都判定"窗口过期"并各自重置导致计数丢失
			res := tx.Model(&database.SubscriptionUsage{}).
				Where("id = ? AND window_end_at = ?", usage.ID, usage.WindowEndAt).
				Updates(map[string]any{
					"window_start_at": now,
					"window_end_at":   newEnd,
					"consumed_value":  delta,
					"request_count":   1,
				})
			if res.Error != nil {
				return fmt.Errorf("usage reset: %w", res.Error)
			}
			if res.RowsAffected == 0 {
				// 别的并发请求已重置窗口，重新读取后走 accumulate 路径
				if rErr := tx.First(&usage, usage.ID).Error; rErr != nil {
					return fmt.Errorf("re-read usage: %w", rErr)
				}
				// fall through 到下面的 accumulate
			} else {
				// 窗口重置：before=0（新窗口起点）
				if vErr := verifySubStillActive(tx, subID); vErr != nil {
					return vErr
				}
				allowed = true
				wantWarn = true
				warnBefore = 0
				warnAfter = delta
				warnWindowStart = now
				return nil
			}
		}

		if usage.ConsumedValue+delta > limitValue && limitValue > 0 {
			return nil
		}
		if isPrecheck {
			allowed = true
			return nil
		}
		// fix Major：limitValue==0 表示不限额；原 WHERE consumed_value+delta<=0 在不限额时永远不成立，
		// 导致 RowsAffected=0 → 无限额套餐反而 100% 拒绝。仅在有限额时才追加上限断言。
		q := tx.Model(&database.SubscriptionUsage{}).Where("id = ?", usage.ID)
		if limitValue > 0 {
			q = q.Where("consumed_value + ? <= ?", delta, limitValue)
		}
		res := q.Updates(map[string]any{
			"consumed_value": gorm.Expr("consumed_value + ?", delta),
			"request_count":  gorm.Expr("request_count + 1"),
		})
		if res.Error != nil {
			return fmt.Errorf("usage accumulate: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			// 撞上限或并发已扣到上限；不算 tx 失败，allowed 保持 false 即可
			return nil
		}
		// 累加成功：before=usage.ConsumedValue, after=usage.ConsumedValue+delta
		if vErr := verifySubStillActive(tx, subID); vErr != nil {
			return vErr
		}
		allowed = true
		wantWarn = true
		warnBefore = usage.ConsumedValue
		warnAfter = usage.ConsumedValue + delta
		warnWindowStart = usage.WindowStartAt
		return nil
	})

	if txErr != nil {
		// errSubInactive 是预期的"竞态拒绝"——订阅已过期/取消/退款，业务语义上等价于"不命中此订阅"。
		// 上层应继续尝试下一条订阅，不算 DB 故障。
		if errors.Is(txErr, errSubInactive) {
			return false, nil
		}
		// 其他错误（DB 连接、写冲突、SQL 语法等）= 真实 DB 故障，必须冒泡。
		// 上层 Decide 据此 fail-closed，绝不允许 fallback 到余额扣费（防双重计费）。
		log.Printf("[ENGINE] atomicConsume tx failed sub=%d plan=%d bucket=%s err=%v",
			subID, planID, bucket, txErr)
		return false, txErr
	}

	// 事务已 commit，安全 fire 异步使用率预警（不会因为 rollback 误报）
	if wantWarn {
		before := warnBefore
		after := warnAfter
		ws := warnWindowStart
		safeAsync("USAGE-WARN", func() {
			MaybeFireUsageWarn(subID, planID, userID, bucket, before, after, limitValue, ws)
		})
	}

	return allowed, nil
}

// verifySubStillActive 在 atomicConsume 事务即将提交前再校验订阅状态。
//
// fix MAJOR M-A2（codex 第二十一轮）：原实现用 `database.DB`（非 tx）发起独立连接 SELECT，
// 在 SQLite `MaxOpenConns=1`（部分测试 / 资源受限环境）下会与当前事务争抢同一连接 → 自死锁：
//   - 当前 atomicConsume 事务持锁
//   - verifySubStillActive 等同一连接释放
//   - 双方互锁，事务超时
//
// 旧注释说"独立连接读最新 commit"——但：
//   - SQLite write tx 已是排他写锁（serializable）：BEGIN 后不可能有其他 writer 提交，所以
//     tx 内 SELECT 与外部 SELECT 等价（甚至更安全：见到自己事务可能的中间写入）
//   - PostgreSQL 默认 READ COMMITTED：tx 内 SELECT 自动看到其他 tx 的 commit
//
// 修复：改用传入的 tx，跨方言一致 + 杜绝自死锁。
//
// 返回 errSubInactive 触发 GORM 事务回滚，已写入的 usage 行被撤销。
//
// fix CRITICAL C23-A1（codex 第二十三轮）：必须区分 NotFound 与真实 DB 故障。
// 仅 ErrRecordNotFound = 订阅被并发取消/退款（业务"竞态拒绝"）。其他错误必须冒泡，
// 让上层 fail-closed，绝不能在缓存命中后第一次 SELECT 失败时继续 fallback 余额扣费。
func verifySubStillActive(tx *gorm.DB, subID uint) error {
	var sub database.UserSubscription
	if err := tx.Select("id, status, end_at").
		Where("id = ? AND status = ? AND end_at > ?", subID, "active", time.Now()).
		First(&sub).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errSubInactive
		}
		return fmt.Errorf("verify sub still active: %w", err)
	}
	return nil
}

// ─── 工具函数 ────────────────────────────────────────────────────

func engineFallbackToQuota() bool {
	SysConfigMutex.RLock()
	v := strings.TrimSpace(SysConfigCache["subscription_engine_fallback_to_quota"])
	SysConfigMutex.RUnlock()
	return v != "false"
}

func get402Message() string {
	SysConfigMutex.RLock()
	v := strings.TrimSpace(SysConfigCache["subscription_engine_402_message"])
	SysConfigMutex.RUnlock()
	if v == "" {
		return "您的订阅额度已用尽，请购买套餐或充值余额"
	}
	return v
}

func uintFromAny(v any) uint {
	if f, ok := v.(float64); ok {
		return uint(f)
	}
	return 0
}
func intFromAny(v any) int {
	if f, ok := v.(float64); ok {
		return int(f)
	}
	return 0
}
func floatFromAny(v any) float64 {
	if f, ok := v.(float64); ok {
		return f
	}
	return 0
}
func stringFromAny(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	// 非字符串值不强转，避免数字/对象被当成 glob pattern 导致静默错配
	return ""
}
