// Package proxy / moderation_audit.go
//
// 内容审核命中事件的**异步**审计写入。独立队列 + 1 个 worker（SQLite 单写者约束）。
//
// 为何独立队列（不直接在请求路径里 DB.Create）：
//   - 审核命中频率可能突发（攻击者在脚本里打 jailbreak 词库）→ 同步写 DB 会让请求路径阻塞
//   - SQLite 写串行（busy_timeout 5s）→ 多个 goroutine 同时 Create 会排队
//   - 请求路径已经返回拒绝响应给客户端，审计日志写库延迟无所谓——异步即可
//
// 故意不做：
//   - 多 worker 并发：SQLite 单写者；写多 worker 等于自己排自己
//   - 持久化队列：纯内存 channel；进程崩溃丢若干审计日志可接受（业务路径不依赖审计成功）
//   - 失败重试：写失败 stdlog.Printf 落进程日志，运维人工查（与 controller/LogOperationByTx 同模式）
//
// 队列容量：1024 条。突发超出 → 等待 100ms 后 drop newest + 计数器（promql 暴露用）。
// fix MINOR R23-m1（codex 审查）：注释原写"drop oldest"与实现不符，更正为 drop newest。
// drop newest 的好处：业务路径不会因为审计落库慢被阻塞超过 100ms。
package proxy

import (
	"log"
	"sync"
	"sync/atomic"
	"time"

	"daof-cpa/database"
)

// ModerationAuditEvent 单条审核审计事件。
type ModerationAuditEvent struct {
	UserID       uint   // 命中用户
	ModelName    string // 模型名（如 gpt-4o）
	ChannelID    uint   // 选中的渠道（0 = 还未选/审核前置）
	ActionType   string // "MODERATION_BLOCK_KEYWORD" / "MODERATION_BLOCK_POLICY" / "MODERATION_UNAVAILABLE_CLOSED"
	Reason       string // 内部原因 code（reject_reason）
	Keyword      string // 命中的关键字（仅 keyword 路径填）
	HighestCat   string // 智能审核命中分类（仅 moderation/strict 路径填）
	HighestScore float64
	IPAddress    string
	Details      string // JSON-encoded extra context（不强制；由调用方序列化好传入）
	OccurredAt   time.Time
}

const (
	moderationAuditQueueSize = 1024
	moderationAuditDropMsg   = 100 * time.Millisecond // 入队等待最多 100ms，避免拖慢请求路径
)

var (
	moderationAuditQueue chan ModerationAuditEvent
	moderationAuditOnce  sync.Once
	moderationAuditWG    sync.WaitGroup
	// 监控指标（atomic 读写，无需锁）
	moderationAuditEnqueuedTotal atomic.Uint64
	moderationAuditDroppedTotal  atomic.Uint64
	moderationAuditWrittenTotal  atomic.Uint64
	moderationAuditFailedTotal   atomic.Uint64
)

// StartModerationAuditWorker 启动唯一一个 worker goroutine。在 main.go init 阶段调一次。
//
// 幂等：sync.Once 防重复启动（测试场景里 main.go 可能被复用调用）。
func StartModerationAuditWorker() {
	moderationAuditOnce.Do(func() {
		moderationAuditQueue = make(chan ModerationAuditEvent, moderationAuditQueueSize)
		moderationAuditWG.Add(1)
		go moderationAuditLoop()
	})
}

// StopModerationAuditWorker 优雅关闭 worker：close 队列，等待 drain。
//
// fix MAJOR M23-A5（codex 第二十三轮）：原实现进程退出时 channel 直接被 GC 回收，
// 队列里未消费的审计事件全部丢失。SIGTERM 路径必须 close + WaitGroup.Wait()
// 等队列 drain 后再退出，确保审计完整写入 DB。
//
// 调用约束：必须在所有 EnqueueModerationAudit 调用方都已停止后调用，否则
// EnqueueModerationAudit 向 closed channel 写入会 panic（select default 兜底）。
func StopModerationAuditWorker() {
	if moderationAuditQueue == nil {
		return
	}
	close(moderationAuditQueue)
	moderationAuditWG.Wait()
}

// moderationAuditLoop worker 主循环。channel close 后 drain 完队列退出。
func moderationAuditLoop() {
	defer moderationAuditWG.Done()
	for evt := range moderationAuditQueue {
		writeModerationAuditEvent(evt)
	}
}

// writeModerationAuditEvent 实际写库。失败仅 log，不重试（与 controller/LogOperationByTx 同模式）。
func writeModerationAuditEvent(evt ModerationAuditEvent) {
	if database.DB == nil {
		return
	}
	row := database.OperationLog{
		TargetUserID: evt.UserID,
		OperatorID:   0, // 系统自动审计
		OperatorRole: "system",
		ActionType:   evt.ActionType,
		IPAddress:    evt.IPAddress,
		Details:      evt.Details,
	}
	if !evt.OccurredAt.IsZero() {
		row.CreatedAt = evt.OccurredAt
	}
	if err := database.DB.Create(&row).Error; err != nil {
		// 失败不阻塞业务；写进程日志便于排查
		log.Printf("[MODERATION-AUDIT] write failed user=%d model=%s action=%s err=%v",
			evt.UserID, evt.ModelName, evt.ActionType, err)
		moderationAuditFailedTotal.Add(1)
		return
	}
	moderationAuditWrittenTotal.Add(1)
	handleModerationRiskAfterAudit(evt, row.ID)
}

// EnqueueModerationAudit 业务层入队入口。
//
// 行为：
//   - 队列未满 → 立即入队，返回 true
//   - 队列满 + 100ms 等待超时 → drop（计数 +1），返回 false
//   - worker 未启动 → drop，返回 false（防 nil channel panic）
//   - 队列已 close（StopModerationAuditWorker 后）→ drop，返回 false（fix M23-A5：避免 panic）
//
// 阻塞策略选择：100ms 等待让突发尖峰能被吸收（一个 SQLite Create ~1-3ms，100ms 能消化 30+ 条）；
// 严格 100ms 上限确保请求路径不会被审计写库阻塞。
func EnqueueModerationAudit(evt ModerationAuditEvent) bool {
	if moderationAuditQueue == nil {
		moderationAuditDroppedTotal.Add(1)
		return false
	}
	if evt.OccurredAt.IsZero() {
		evt.OccurredAt = time.Now()
	}
	// fix MAJOR M23-A5（codex 第二十三轮）：worker 已 stop（channel close）后业务路径仍可能
	// 调 Enqueue。defer recover 兜底防 panic："send on closed channel" → 改记 dropped。
	defer func() {
		if r := recover(); r != nil {
			moderationAuditDroppedTotal.Add(1)
		}
	}()
	select {
	case moderationAuditQueue <- evt:
		moderationAuditEnqueuedTotal.Add(1)
		return true
	default:
		// 满了 → 短等待（最多 100ms）
		select {
		case moderationAuditQueue <- evt:
			moderationAuditEnqueuedTotal.Add(1)
			return true
		case <-time.After(moderationAuditDropMsg):
			moderationAuditDroppedTotal.Add(1)
			return false
		}
	}
}

// ModerationAuditMetrics 给 admin /metrics 接口暴露内部指标。
type ModerationAuditMetrics struct {
	QueueDepth    int    `json:"queue_depth"`
	QueueCapacity int    `json:"queue_capacity"`
	Enqueued      uint64 `json:"enqueued_total"`
	Dropped       uint64 `json:"dropped_total"`
	Written       uint64 `json:"written_total"`
	Failed        uint64 `json:"failed_total"`
}

// GetModerationAuditMetrics 读监控指标快照。线程安全。
func GetModerationAuditMetrics() ModerationAuditMetrics {
	depth := 0
	cap := 0
	if moderationAuditQueue != nil {
		depth = len(moderationAuditQueue)
		cap = moderationAuditQueueSize
	}
	return ModerationAuditMetrics{
		QueueDepth:    depth,
		QueueCapacity: cap,
		Enqueued:      moderationAuditEnqueuedTotal.Load(),
		Dropped:       moderationAuditDroppedTotal.Load(),
		Written:       moderationAuditWrittenTotal.Load(),
		Failed:        moderationAuditFailedTotal.Load(),
	}
}
