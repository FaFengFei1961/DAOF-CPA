// Package proxy / moderation_policy.go
//
// 内容审核策略查询：根据 modelName 在所有候选 ChannelModel 中取**最严**策略。
//
// 设计动机（codex 第二十三轮反馈，per-channel-per-model 风控）：
//   - 同一 modelName（如 gpt-4o）可能在多个 Channel 上配置（直连官方 / 第三方中转 / 自部署）
//   - 路由层后续会按 weight 选具体渠道，但**审核必须先于路由**——否则可能"漏审"
//   - 取最严策略：任何一个候选写了 "strict"，整条请求按 strict 走；任何一个写了 "closed"，按 closed 走
//   - 这是防御性设计：admin 配置错某个渠道（漏写 ModerationLevel="off"）不会让违规 prompt 透传
//
// 故意不做：
//   - 路由命中后再审：路由层涉及 weight/health check，逻辑交叉太多；前置审核更安全
//   - 缓存到 sub-millisecond：30s TTL 已足够，admin 改完 ChannelModel 立即 InvalidateModerationPolicyCache
package proxy

import (
	"log"
	"sync"
	"time"

	"daof-cpa/database"
)

// ModerationPolicy 单个 ChannelModel 维度的风控策略。
type ModerationPolicy struct {
	Level    string // "off" / "keyword" / "moderation" / "strict"
	FailMode string // "open" / "closed"
	// loadFailed=true 表示 DB 查询失败，调用方应按 fail-closed 兜底，
	// 且**不要缓存**这条策略（会让 transient DB 错误锁住 30s）。
	loadFailed bool
}

// IsActive 是否需要启动审核流程（off → 跳过）。
func (p ModerationPolicy) IsActive() bool {
	return p.Level == "keyword" || p.Level == "moderation" || p.Level == "strict"
}

// NeedsKeyword 是否需要本地关键字快扫。
func (p ModerationPolicy) NeedsKeyword() bool {
	return p.Level == "keyword" || p.Level == "strict"
}

// NeedsModeration 是否需要智能审核服务。
func (p ModerationPolicy) NeedsModeration() bool {
	return p.Level == "moderation" || p.Level == "strict"
}

// FailClosed 智能审核服务不可达时是否拒绝（true=拒绝 / false=放行）。
func (p ModerationPolicy) FailClosed() bool {
	return p.FailMode == "closed"
}

// LoadFailed DB 加载失败导致的"无法决策"状态。
// 调用方应按 fail-closed 兜底（不能放行未审核的请求）。
func (p ModerationPolicy) LoadFailed() bool {
	return p.loadFailed
}

// 策略等级排序（越大越严）：off < keyword < moderation < strict
//
// "moderation" vs "strict"：strict = keyword + moderation 双层，等级最高；
// "moderation" 单跑智能审核但跳过本地词库，适合需要语义识别但不想启用模板拦截的场景。
//
// 注意：moderation 与 strict 严格度不能简单线性比较（一个是 API 智能，一个是双层）。
// 这里把 strict 排在最高，是因为它包含 moderation 的全部能力 + keyword 的快路径。
func levelRank(level string) int {
	switch level {
	case "strict":
		return 3
	case "moderation":
		return 2
	case "keyword":
		return 1
	default: // "off" 或未知值
		return 0
	}
}

// rankToLevel rank 反查 level 字符串。
func rankToLevel(rank int) string {
	switch rank {
	case 3:
		return "strict"
	case 2:
		return "moderation"
	case 1:
		return "keyword"
	default:
		return "off"
	}
}

// ─── 缓存（30s TTL；admin 改 ChannelModel 后立即失效）──────────────────────

type cachedModerationPolicy struct {
	policy    ModerationPolicy
	expiresAt time.Time
}

const moderationPolicyCacheTTL = 30 * time.Second

var (
	moderationPolicyCacheMu sync.RWMutex
	moderationPolicyCache   = map[string]cachedModerationPolicy{}
)

// LookupModerationPolicy 根据 modelName 查询所有候选 ChannelModel 的最严策略。
//
// 行为：
//   - 无候选（modelName 未配置任何渠道）→ {off, open}（无审核；后续路由会失败兜底）
//   - 多候选 → Level 取最高 rank；FailMode 任一为 closed → closed（防御性）
//   - DB 失败 → {off, open}（不阻塞业务；admin 看 OperationLog 排查）
//
// 缓存：30s TTL；admin CRUD ChannelModel 后调 InvalidateModerationPolicyCache(modelName)。
func LookupModerationPolicy(modelName string) ModerationPolicy {
	if modelName == "" {
		return ModerationPolicy{Level: "off", FailMode: "open"}
	}

	now := time.Now()
	moderationPolicyCacheMu.RLock()
	if entry, ok := moderationPolicyCache[modelName]; ok && now.Before(entry.expiresAt) {
		moderationPolicyCacheMu.RUnlock()
		return entry.policy
	}
	moderationPolicyCacheMu.RUnlock()

	policy := loadStrictestPolicyFromDB(modelName)

	// fix MAJOR R23-M3（codex 审查）：DB 失败不缓存 fail-open ——
	// transient DB 错误（连接抖动 / 锁等待）一旦缓存 off/open 30s，
	// 期间所有官方直连请求都裸奔。改为：失败则不缓存，调用方按 fail-closed 处理，
	// 30ms 后下一个请求重试 DB。
	if !policy.loadFailed {
		moderationPolicyCacheMu.Lock()
		moderationPolicyCache[modelName] = cachedModerationPolicy{
			policy:    policy,
			expiresAt: now.Add(moderationPolicyCacheTTL),
		}
		moderationPolicyCacheMu.Unlock()
	}

	return policy
}

// loadStrictestPolicyFromDB 真实 DB 查询：JOIN Channel ON status=1，取最严。
//
// 设计：单条 SQL 查所有候选；Go 层做 max-rank。比"先查 channel 再 IN 查 channel_model"快。
//
// fix MAJOR R23-M3：区分"DB 失败"和"查到 0 条"两种情况：
//   - DB 失败 → loadFailed=true，调用方 fail-closed
//   - 0 条 → 返回 off/open（合法的"未配置"状态）
func loadStrictestPolicyFromDB(modelName string) ModerationPolicy {
	if database.DB == nil {
		// 测试环境/启动期：当作"未配置"，让审核完全跳过（与 engine disabled 等价）
		return ModerationPolicy{Level: "off", FailMode: "open"}
	}

	type row struct {
		ModerationLevel    string
		ModerationFailMode string
	}
	var rows []row

	err := database.DB.Table("channel_models AS cm").
		Select("cm.moderation_level AS moderation_level, cm.moderation_fail_mode AS moderation_fail_mode").
		Joins("INNER JOIN channels AS c ON c.id = cm.channel_id AND c.deleted_at IS NULL").
		Where("cm.model_id = ? AND cm.status = 1 AND c.status = 1 AND cm.deleted_at IS NULL", modelName).
		Scan(&rows).Error

	if err != nil {
		log.Printf("[MODERATION-POLICY] DB load failed for model=%s: %v (fail-closed)", modelName, err)
		return ModerationPolicy{Level: "off", FailMode: "open", loadFailed: true}
	}
	if len(rows) == 0 {
		return ModerationPolicy{Level: "off", FailMode: "open"}
	}

	maxRank := 0
	failClosed := false
	for _, r := range rows {
		if rk := levelRank(r.ModerationLevel); rk > maxRank {
			maxRank = rk
		}
		if r.ModerationFailMode == "closed" {
			failClosed = true
		}
	}

	failMode := "open"
	if failClosed {
		failMode = "closed"
	}
	policy := ModerationPolicy{
		Level:    rankToLevel(maxRank),
		FailMode: failMode,
	}
	return policy
}

// InvalidateModerationPolicyCache admin 创建/更新/删除 ChannelModel 时调。
// modelName 为空 → 清空全表（factory reset 等场景）。
func InvalidateModerationPolicyCache(modelName string) {
	moderationPolicyCacheMu.Lock()
	defer moderationPolicyCacheMu.Unlock()
	if modelName == "" {
		moderationPolicyCache = map[string]cachedModerationPolicy{}
		return
	}
	delete(moderationPolicyCache, modelName)
}

// FlushAllModerationPolicyCache 同 InvalidateModerationPolicyCache("")，给业务层语义更清晰的入口。
func FlushAllModerationPolicyCache() {
	InvalidateModerationPolicyCache("")
}
