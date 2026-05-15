# Codex 4：cliproxy_usage_sync leader election + distributed lock

你是 daof-ai-hub 项目的资深 Go 后端工程师，使用 codex 实现具体代码。

## 项目硬约束
- 项目**未上线**，处于重构期 → **禁止任何向后兼容 / alias / shim**
- 旧逻辑残留 = P0 必删
- 所有金额 int64 micro_usd；杜绝 float64 出现在金额计算

## 上下文
codex 模块 6 审计 P0 #4：cliproxy usage sync 无 leader election / lock，pop 语义下存在并发消费和丢账风险。

当前实现 (controller/cliproxy_usage_sync.go)：
- `sync.Once` 只保证单进程启动一次
- 手动 sync (admin 触发) 可与 cron (定时) 并发
- 先 pop 再落库：pop 后单条写失败 → 后续记录丢失

威胁场景：
- 多副本部署时多个进程同时调上游 pop → 同一条 usage 被消费 N 次 → 重复扣费
- 单进程内手动 sync + cron 同时触发 → 同上
- pop 成功但落库失败 → 数据永久丢失

## 实施需求

### 1. 分布式锁表 (database/distributed_lock_schema.go 新建)
```go
// DistributedLock 进程间互斥锁（基于 DB 行级锁 + 心跳）。
type DistributedLock struct {
    ID        uint      `gorm:"primaryKey"`
    LockKey   string    `gorm:"<-:create;uniqueIndex;not null;size:128"` // 例 "cliproxy_usage_sync"
    OwnerID   string    `gorm:"not null;size:64"` // 进程唯一 ID（启动时生成）
    AcquiredAt time.Time `gorm:"not null;index"`
    HeartbeatAt time.Time `gorm:"not null"`        // 持锁方定期续约
    ExpiresAt time.Time `gorm:"not null;index"`    // 心跳超时后视为锁失效
    CreatedAt time.Time
    UpdatedAt time.Time
}
```

### 2. Lock helper (database/distributed_lock_helper.go 新建)
- `AcquireLock(key string, ttl time.Duration) (ownerID string, acquired bool, err error)`
  - 实现：用 INSERT OR UPDATE 模式（先尝试 update 已过期的锁 → 拿到；失败再尝试 INSERT）
  - SQLite/Postgres 兼容
- `RenewLock(key, ownerID string, ttl time.Duration) (renewed bool, err error)` 续约心跳
- `ReleaseLock(key, ownerID string) error` 主动释放（owner 校验防误释）
- 自动生成 `ownerID`：machineUUID + PID + nanoTimestamp（startup time）

### 3. cliproxy_usage_sync 集成 (controller/cliproxy_usage_sync.go)
- 在 `syncCLIProxyUsageCron` / `SyncCLIProxyUsage` 主体外加锁逻辑：
  - 尝试 AcquireLock("cliproxy_usage_sync", 60s)
  - 失败 → 直接返回（其他进程在做）
  - 成功 → 启动 goroutine 定期 RenewLock（每 20s）
  - sync 主流程完成后 defer ReleaseLock
- 添加单元测试：模拟两个 owner 同时尝试 acquire，只有一个能成功

### 4. 数据持久化改造 - 先持久化整批 raw record 再匹配
旧实现 (大约 line 188-279)：
```go
records := cpaPopUsage()  // 上游 pop
for _, rec := range records {
    tx.Begin()
    matched := matchUpstreamUsageRecordTx(tx, &rec)  // 匹配逻辑
    if err { return }                               // 失败中止，后续记录丢失
}
```

新实现：
- 先一次性 INSERT 所有 raw records 到 `upstream_usage_records` 表（accepted=false 默认）
- INSERT 失败时整批回滚 + 不 ACK 上游 pop → 下次再 pop
- INSERT 成功后再逐条匹配 + 更新 accepted 字段
- 单条匹配失败仅 log，不影响其他记录（pending 仍在 DB，admin 可后台修复）

### 5. 测试 (controller/cliproxy_usage_sync_test.go 扩展)
- TestAcquireLock_FirstSucceedsSecondFails
- TestAcquireLock_AfterExpiryNewOwnerSucceeds
- TestRenewLock_SameOwnerExtends
- TestReleaseLock_RequiresOwnerMatch
- TestSyncCLIProxyUsage_SkippedIfLockHeld
- TestSyncCLIProxyUsage_PersistsRawBeforeMatching

## 文件白名单
- `database/distributed_lock_schema.go` (新建)
- `database/distributed_lock_helper.go` (新建)
- `database/distributed_lock_test.go` (新建)
- `controller/cliproxy_usage_sync.go` (集成锁 + 改造 persistence 顺序)
- `controller/cliproxy_usage_sync_test.go` (扩展测试)

## 文件黑名单
- `main.go` — 不动（cron 启停已注册）
- `database/sqlite.go` — 不动 AutoMigrate；列出需补的：`&DistributedLock{}`
- `i18n/*.json` — 不动

## 提交要求
- `git add` 白名单文件
- commit message 标题：`feat: Sprint 5 M6 P0 cliproxy_usage_sync 分布式锁 + 持久化前置`
- commit body 列出：
  - 新增 DistributedLock 表
  - lock helper API
  - cliproxy sync 集成
  - 测试覆盖
  - 主进程需补的 AutoMigrate

## 测试
- `go test ./database/ -run "Lock" -count=1 -v` 必须通过
- `go test ./controller/ -run "CLIProxyUsage" -count=1 -v` 必须通过
- `go build ./...` 必须通过
- 不能破坏现有测试

完成后输出简短中文报告：改文件 / 测试结果 / 主进程需补 AutoMigrate。
