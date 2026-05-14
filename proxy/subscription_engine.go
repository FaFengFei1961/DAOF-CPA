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

// errSubInactive 哨兵：在 atomicConsumeMany 事务结束前发现订阅已被取消/退款/过期，
// 用 GORM 事务 return-non-nil-rolls-back 语义把已写入的 usage 行回滚。
//
// fix CRITICAL C19-3（codex 第十九轮）：原单窗口扣费顶部一次性 SELECT 订阅状态后，
// 后续 INSERT/UPDATE usage 行之间订阅可能被另一事务（cancel/refund）改成 cancelled。
// 此处 return errSubInactive → tx 回滚 → 不写入"过期订阅的脏 usage 行"。
var errSubInactive = errors.New("subscription became inactive during consume")
var errPlanLimitExceeded = errors.New("subscription quota plan limit exceeded")

// EngineDecision 是引擎对一次请求的决策结果
type EngineDecision struct {
	Allowed            bool
	SubscriptionID     uint
	QuotaPlanID        uint
	QuotaPlanIDs       []uint
	ConsumedUnit       string
	ConsumedDelta      float64
	FallbackToBalance  bool
	BlockReason        string
	BlockMessage       string
	BlockQuotaPlanID   uint
	BlockConsumedValue float64
	BlockDelta         float64
	BlockLimitValue    float64
	BlockRemaining     float64
	BlockWindowEndAt   *time.Time
	BlockUnit          string
	// ProductType 命中订阅时填 "subscription"（addon 已在 Phase 8 移除）。
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
	// CostMicroUSD 是本次请求按当前路由官方/配置价格折算出的 API 等值成本。
	// api_cost_usd 订阅计划直接使用它；precheck 传悲观估算，commit 传真实成本。
	CostMicroUSD int64
	IsPrecheck   bool
}

// Decide 决策一次请求该走哪条路径（Phase 8 后两段消费模型）：
//
//  1. 订阅 (subscription)  ─ 按 ConsumptionOrder FIFO
//  2. 余额 (user.Quota)     ─ 由用户 BalanceConsumeEnabled 控制 + 窗口限额
//
// 注：addon（增量包）已在 Phase 8 移除；productPriority 仅保留 subscription
// 分支，未知类型走 default fallback（兼容历史 snapshot 数据）。
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

	var lastLimitDecision EngineDecision
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
		// fix MINOR（多模型审计第二十五轮 P3）：lastLimitDecision 优先保留 BlockQuotaPlanID != 0 的
		// snapshot decision（precheck 路径才有详细 ConsumedValue/Remaining/WindowEndAt）。
		// 否则后续 sub 的普通 plan_full_skip_sub 会覆盖前一个有 snapshot 的，前端损失精准提示。
		if d.BlockQuotaPlanID != 0 {
			lastLimitDecision = d
		} else if lastLimitDecision.BlockQuotaPlanID == 0 && d.BlockReason == "plan_full_skip_sub" {
			lastLimitDecision = d
		}
	}

	// 所有订阅 + 增量包都没命中 → fallback 到余额
	if engineFallbackToQuota() {
		d := EngineDecision{
			Allowed:           true,
			FallbackToBalance: true,
		}
		if lastLimitDecision.BlockReason != "" {
			d.BlockReason = lastLimitDecision.BlockReason
			d.BlockMessage = lastLimitDecision.BlockMessage
			d.BlockQuotaPlanID = lastLimitDecision.BlockQuotaPlanID
			d.BlockConsumedValue = lastLimitDecision.BlockConsumedValue
			d.BlockDelta = lastLimitDecision.BlockDelta
			d.BlockLimitValue = lastLimitDecision.BlockLimitValue
			d.BlockRemaining = lastLimitDecision.BlockRemaining
			d.BlockWindowEndAt = lastLimitDecision.BlockWindowEndAt
			d.BlockUnit = lastLimitDecision.BlockUnit
		}
		return d
	}
	// fix MINOR（多模型审计第二十五轮 P2）：precheck 命中窗口超额时（BlockQuotaPlanID != 0
	// 表示 trySharedQuota 已捕获 snapshot），即使 fallback=false 也要透出 snapshot 给前端
	// 构建精准提示（"本次预估超过当前窗口剩余 X"），与 P1-6 TOCTOU 修复一脉相承。
	// 非 precheck commit 路径（BlockQuotaPlanID=0）保持现有 generic 行为，不破坏既有契约。
	if lastLimitDecision.BlockQuotaPlanID != 0 {
		return EngineDecision{
			Allowed:            false,
			BlockReason:        lastLimitDecision.BlockReason,
			BlockMessage:       lastLimitDecision.BlockMessage,
			BlockQuotaPlanID:   lastLimitDecision.BlockQuotaPlanID,
			BlockConsumedValue: lastLimitDecision.BlockConsumedValue,
			BlockDelta:         lastLimitDecision.BlockDelta,
			BlockLimitValue:    lastLimitDecision.BlockLimitValue,
			BlockRemaining:     lastLimitDecision.BlockRemaining,
			BlockWindowEndAt:   lastLimitDecision.BlockWindowEndAt,
			BlockUnit:          lastLimitDecision.BlockUnit,
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

// productPriority 消费排序优先级（数字越小越先扣）。
// Phase 8：addon 移除后只剩 subscription 一种正常类型。
func productPriority(productType string) int {
	switch productType {
	case "subscription":
		return 1
	default:
		return 99 // 未知类型（如历史 addon 残留）最后扣，日志可见
	}
}

// ─── Shared Quota 路径 ────────────────────────────────────────────

func trySharedQuota(cs *CachedSubscription, req EngineRequest) EngineDecision {
	sub := cs.Subscription
	plans := extractPlansFromSnapshot(cs.Snapshot)
	if len(plans) == 0 {
		return EngineDecision{Allowed: false, BlockReason: "no_plans"}
	}
	specs := make([]consumeSpec, 0, len(plans))
	for _, plan := range plans {
		if !matchModel(plan.ModelMatch, req.ModelName) {
			continue
		}
		delta, unit := computeDelta(plan, req)
		if delta < 0 {
			return EngineDecision{Allowed: false, BlockReason: "invalid_plan_delta", BlockMessage: "订阅额度配置异常，请联系管理员"}
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
		specs = append(specs, consumeSpec{
			PlanID:        plan.ID,
			Bucket:        bucket,
			Delta:         delta,
			Unit:          unit,
			WindowSeconds: plan.WindowSeconds,
			LimitValue:    plan.LimitValue * mult,
		})
	}

	if len(specs) == 0 {
		return EngineDecision{Allowed: false, BlockReason: "no_plan_in_sub_matched"}
	}
	ok, failSnap, dbErr := atomicConsumeMany(sub.ID, sub.UserID, specs, req.IsPrecheck)
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
	if !ok {
		d := EngineDecision{Allowed: false, BlockReason: "plan_full_skip_sub"}
		// fix CRITICAL（多模型审计第二十五轮）：snapshot 由 consumePlanInTx 在事务内捕获，
		// 不再事务外重新 SELECT — 杜绝并发写造成的"剩余额度"展示错乱（"明明没用尽却提示用尽"）。
		if req.IsPrecheck && failSnap != nil {
			d.BlockQuotaPlanID = failSnap.PlanID
			d.BlockConsumedValue = failSnap.ConsumedValue
			d.BlockDelta = failSnap.Delta
			d.BlockLimitValue = failSnap.LimitValue
			d.BlockRemaining = failSnap.Remaining
			d.BlockWindowEndAt = failSnap.WindowEndAt
			d.BlockUnit = failSnap.Unit
			d.BlockMessage = buildPrecheckLimitMessage(*failSnap)
		}
		return d
	}
	planIDs := make([]uint, 0, len(specs))
	for _, spec := range specs {
		planIDs = append(planIDs, spec.PlanID)
	}
	return EngineDecision{
		Allowed:        true,
		SubscriptionID: sub.ID,
		QuotaPlanID:    specs[0].PlanID,
		QuotaPlanIDs:   planIDs,
		ConsumedUnit:   specs[0].Unit,
		ConsumedDelta:  specs[0].Delta,
	}
}

type consumeSpec struct {
	PlanID        uint
	Bucket        string
	Delta         float64
	Unit          string
	WindowSeconds int
	LimitValue    float64
}

type precheckLimitDetail struct {
	PlanID        uint
	ConsumedValue float64
	Delta         float64
	LimitValue    float64
	Remaining     float64
	WindowEndAt   *time.Time
	Unit          string
}

// fix CRITICAL（多模型审计第二十五轮）：原 diagnosePrecheckLimit 在事务外重新 SELECT，
// 与 atomicConsumeMany 事务内的写存在 TOCTOU 竞态，会让用户看到"剩余额度"展示数值与
// 实际拒绝原因不符（已确认的用户痛点："明明没用尽却提示用尽"）。
//
// 现在 snapshot 在 consumePlanInTx 触发 errPlanLimitExceeded 时同事务内捕获，
// 由 atomicConsumeMany 的第二个返回值传出，caller 直接消费。彻底消除事务外重查路径。

func buildPrecheckLimitMessage(detail precheckLimitDetail) string {
	if detail.Unit == "api_cost_usd" {
		return fmt.Sprintf("本次请求预估消耗 %.6f credits，超过当前窗口剩余额度 %.6f credits", detail.Delta, math.Max(0, detail.Remaining))
	}
	return fmt.Sprintf("本次请求预估消耗 %.0f %s，超过当前窗口剩余额度 %.0f %s", detail.Delta, detail.Unit, math.Max(0, detail.Remaining), detail.Unit)
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
	ExtraConfig        string  `json:"extra_config"`
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
			ExtraConfig:        stringFromAny(m["extra_config"]),
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
	if plan.ExtraConfig != "" && plan.ExtraConfig != "{}" {
		var cfg map[string]any
		if err := json.Unmarshal([]byte(plan.ExtraConfig), &cfg); err != nil {
			log.Printf("[ENGINE] normalizeModelBucket: invalid plan.ExtraConfig json plan_id=%d err=%v", plan.ID, err)
		} else {
			for _, key := range []string{"bucket", "model_bucket"} {
				if v, ok := cfg[key].(string); ok {
					v = strings.TrimSpace(v)
					if v != "" {
						return v
					}
				}
			}
		}
	}
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
// 这里只算 raw delta；caller 在 atomicConsumeMany 调用前用 effectiveLimit = LimitValue * multiplier。
// 之前做反向 → 倍数套餐反而更快耗尽，业务语义错误。
func computeDelta(plan snapshotPlan, req EngineRequest) (float64, string) {
	weightSingle, weightInOut := parseWeightFactor(plan.WeightFactor, req.ModelName)

	switch plan.LimitUnit {
	case "request_count":
		return 1.0 * weightSingle, "request_count"
	case "api_cost_usd":
		if req.CostMicroUSD < 0 {
			log.Printf("[ENGINE] plan %d uses api_cost_usd but request cost is negative", plan.ID)
			return -1, "api_cost_usd"
		}
		return database.MicroToUSD(req.CostMicroUSD), "api_cost_usd"
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
	}
	log.Printf("[ENGINE] unsupported limit_unit=%q plan_id=%d", plan.LimitUnit, plan.ID)
	return -1, plan.LimitUnit
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

type planConsumeWarn struct {
	PlanID      uint
	Bucket      string
	Before      float64
	After       float64
	LimitValue  float64
	WindowStart time.Time
}

// atomicConsumeMany 将同一订阅内所有匹配的 quota plans 作为 AND 条件处理：
// 任何一个窗口/单位超额，本次请求整体拒绝且不写入任何 usage。
//
// 这正是订阅产品当前需要的"5 小时爆发额度 + 7 天总额度"模型；不再保留旧的
// "命中第一个 plan 即成功"语义。
//
// fix CRITICAL（多模型审计第二十五轮）：返回值新增 *precheckLimitDetail snapshot，
// 在 errPlanLimitExceeded 触发时由 consumePlanInTx 在 tx 内捕获真实 ConsumedValue/Remaining
// 并冒泡到此处。caller 应直接消费此 snapshot，避免事务外重新 SELECT 造成 TOCTOU
// （旧 diagnosePrecheckLimit 在 tx 提交后用 database.DB 重查，并发写会让"剩余额度"展示数字与
// 实际拒绝原因不符，引发"明明没用尽却提示用尽"用户投诉）。
func atomicConsumeMany(subID, userID uint, specs []consumeSpec, isPrecheck bool) (bool, *precheckLimitDetail, error) {
	if len(specs) == 0 {
		return false, nil, nil
	}
	warns := make([]planConsumeWarn, 0, len(specs))
	var failedSnap *precheckLimitDetail
	txErr := database.DB.Transaction(func(tx *gorm.DB) error {
		var sub database.UserSubscription
		if err := tx.Select("id, status, end_at").
			Where("id = ? AND status = ? AND end_at > ?", subID, "active", time.Now()).
			First(&sub).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errSubInactive
			}
			return fmt.Errorf("verify sub at consume entry: %w", err)
		}

		for _, spec := range specs {
			warn, snap, err := consumePlanInTx(tx, subID, spec, isPrecheck)
			if err != nil {
				if snap != nil {
					failedSnap = snap
				}
				return err
			}
			if warn != nil {
				warns = append(warns, *warn)
			}
		}
		if !isPrecheck {
			if err := verifySubStillActive(tx, subID); err != nil {
				return err
			}
		}
		return nil
	})

	if txErr != nil {
		if errors.Is(txErr, errSubInactive) {
			return false, nil, nil
		}
		if errors.Is(txErr, errPlanLimitExceeded) {
			return false, failedSnap, nil
		}
		log.Printf("[ENGINE] atomicConsumeMany tx failed sub=%d err=%v", subID, txErr)
		return false, nil, txErr
	}

	for _, w := range warns {
		warn := w
		safeAsync("USAGE-WARN", func() {
			MaybeFireUsageWarn(subID, warn.PlanID, userID, warn.Bucket, warn.Before, warn.After, warn.LimitValue, warn.WindowStart)
		})
	}
	return true, nil, nil
}

func consumePlanInTx(tx *gorm.DB, subID uint, spec consumeSpec, isPrecheck bool) (*planConsumeWarn, *precheckLimitDetail, error) {
	now := time.Now()
	// fix CRITICAL（多模型审计第二十五轮）：snapshotForPlanLimit 在 tx 内捕获真实 usage 状态，
	// 给 caller 用于构建用户侧错误消息（"本次预估超过当前窗口剩余"），不再事务外重查避免 TOCTOU。
	snapshotForPlanLimit := func(consumedValue float64, windowEndAt time.Time) *precheckLimitDetail {
		snap := &precheckLimitDetail{
			PlanID:        spec.PlanID,
			ConsumedValue: consumedValue,
			Delta:         spec.Delta,
			LimitValue:    spec.LimitValue,
			Remaining:     math.Max(0, spec.LimitValue-consumedValue),
			Unit:          spec.Unit,
		}
		if !windowEndAt.IsZero() {
			end := windowEndAt
			snap.WindowEndAt = &end
		}
		return snap
	}
	var usage database.SubscriptionUsage
	err := tx.Where("subscription_id = ? AND quota_plan_id = ? AND model_bucket = ?",
		subID, spec.PlanID, spec.Bucket).First(&usage).Error

	if errors.Is(err, gorm.ErrRecordNotFound) {
		if spec.LimitValue > 0 && spec.Delta > spec.LimitValue {
			// 无 usage 行 → consumed=0；window 用预期开窗时间
			windowEnd := time.Time{}
			if spec.WindowSeconds > 0 {
				windowEnd = now.Add(time.Duration(spec.WindowSeconds) * time.Second)
			}
			return nil, snapshotForPlanLimit(0, windowEnd), errPlanLimitExceeded
		}
		if isPrecheck {
			return nil, nil, nil
		}
		windowEnd := now
		if spec.WindowSeconds > 0 {
			windowEnd = now.Add(time.Duration(spec.WindowSeconds) * time.Second)
		} else {
			windowEnd = now.Add(365 * 24 * time.Hour)
		}
		newRow := database.SubscriptionUsage{
			SubscriptionID: subID,
			QuotaPlanID:    spec.PlanID,
			ModelBucket:    spec.Bucket,
			WindowStartAt:  now,
			WindowEndAt:    windowEnd,
			ConsumedValue:  spec.Delta,
			RequestCount:   1,
		}
		if cerr := tx.Create(&newRow).Error; cerr != nil {
			// 并发首插撞唯一索引时，重读后走累加路径。
			if existing := (database.SubscriptionUsage{}); tx.
				Where("subscription_id = ? AND quota_plan_id = ? AND model_bucket = ?", subID, spec.PlanID, spec.Bucket).
				First(&existing).Error == nil {
				usage = existing
				err = nil
			} else {
				return nil, nil, fmt.Errorf("usage create: %w", cerr)
			}
		} else {
			return &planConsumeWarn{
				PlanID:      spec.PlanID,
				Bucket:      spec.Bucket,
				Before:      0,
				After:       spec.Delta,
				LimitValue:  spec.LimitValue,
				WindowStart: now,
			}, nil, nil
		}
	} else if err != nil {
		return nil, nil, fmt.Errorf("usage query: %w", err)
	}

	if spec.WindowSeconds > 0 && now.After(usage.WindowEndAt) {
		if spec.LimitValue > 0 && spec.Delta > spec.LimitValue {
			// window 已过期 → 视为新窗口起点，consumed=0
			newEnd := now.Add(time.Duration(spec.WindowSeconds) * time.Second)
			return nil, snapshotForPlanLimit(0, newEnd), errPlanLimitExceeded
		}
		if isPrecheck {
			return nil, nil, nil
		}
		newEnd := now.Add(time.Duration(spec.WindowSeconds) * time.Second)
		res := tx.Model(&database.SubscriptionUsage{}).
			Where("id = ? AND window_end_at = ?", usage.ID, usage.WindowEndAt).
			Updates(map[string]any{
				"window_start_at": now,
				"window_end_at":   newEnd,
				"consumed_value":  spec.Delta,
				"request_count":   1,
			})
		if res.Error != nil {
			return nil, nil, fmt.Errorf("usage reset: %w", res.Error)
		}
		if res.RowsAffected > 0 {
			return &planConsumeWarn{
				PlanID:      spec.PlanID,
				Bucket:      spec.Bucket,
				Before:      0,
				After:       spec.Delta,
				LimitValue:  spec.LimitValue,
				WindowStart: now,
			}, nil, nil
		}
		if rErr := tx.First(&usage, usage.ID).Error; rErr != nil {
			return nil, nil, fmt.Errorf("re-read usage: %w", rErr)
		}
	}

	if spec.LimitValue > 0 && usage.ConsumedValue+spec.Delta > spec.LimitValue {
		return nil, snapshotForPlanLimit(usage.ConsumedValue, usage.WindowEndAt), errPlanLimitExceeded
	}
	if isPrecheck {
		return nil, nil, nil
	}
	q := tx.Model(&database.SubscriptionUsage{}).Where("id = ?", usage.ID)
	if spec.LimitValue > 0 {
		q = q.Where("consumed_value + ? <= ?", spec.Delta, spec.LimitValue)
	}
	res := q.Updates(map[string]any{
		"consumed_value": gorm.Expr("consumed_value + ?", spec.Delta),
		"request_count":  gorm.Expr("request_count + 1"),
	})
	if res.Error != nil {
		return nil, nil, fmt.Errorf("usage accumulate: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		// CAS 失败 → 并发请求已让 consumed_value 进一步上涨到无法容纳本次 delta。
		// 重读当前事务可见的 usage（同 tx 内其他 SELECT 是 consistent read），用真实 consumed
		// 构 snapshot，避免 caller 拿过时的 usage.ConsumedValue 给用户错误"剩余"展示。
		var fresh database.SubscriptionUsage
		if rerr := tx.First(&fresh, usage.ID).Error; rerr == nil {
			return nil, snapshotForPlanLimit(fresh.ConsumedValue, fresh.WindowEndAt), errPlanLimitExceeded
		}
		return nil, snapshotForPlanLimit(usage.ConsumedValue, usage.WindowEndAt), errPlanLimitExceeded
	}
	return &planConsumeWarn{
		PlanID:      spec.PlanID,
		Bucket:      spec.Bucket,
		Before:      usage.ConsumedValue,
		After:       usage.ConsumedValue + spec.Delta,
		LimitValue:  spec.LimitValue,
		WindowStart: usage.WindowStartAt,
	}, nil, nil
}

// verifySubStillActive 在 atomicConsumeMany 事务即将提交前再校验订阅状态。
//
// fix MAJOR M-A2（codex 第二十一轮）：原实现用 `database.DB`（非 tx）发起独立连接 SELECT，
// 在 SQLite `MaxOpenConns=1`（部分测试 / 资源受限环境）下会与当前事务争抢同一连接 → 自死锁：
//   - 当前 atomicConsumeMany 事务持锁
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
