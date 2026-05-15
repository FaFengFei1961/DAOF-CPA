# Codex 1：Channel Circuit Breaker Admin 监控（后端 endpoint + 前端面板）

你是 daof-ai-hub 项目的资深全栈工程师，使用 codex 实现具体代码。

## 项目硬约束
- 项目**未上线**，处于重构期 → **禁止任何向后兼容代码 / alias / shim**
- 旧逻辑残留 = P0 必删
- 收紧攻击面 > 兼容旧调用方
- 所有金额 int64 micro_usd；价格 int64 pico_per_token；杜绝 float64 出现在金额计算

## 上下文
Sprint 5 M2 已实现 `proxy/channel_circuit.go` 跨请求 circuit breaker：
- per-channel 状态机：closed → open → half-open → closed
- 已有 helper `GetChannelCircuitSnapshot()` 返回 `[]ChannelCircuitSnapshot{ChannelID, ConsecutiveFailures, OpenUntil, CurrentCooldownSec, State}`
- 已有 helper `ForceCloseChannelCircuit(channelID)` admin 强制重置

需要把这俩 helper 暴露给 admin UI。

## 实施需求

### 1. 后端 endpoint（新文件 controller/channel_circuit_admin.go）
- `func AdminListChannelCircuits(c *fiber.Ctx) error` GET 返回所有 channel circuit 状态快照
  - 用 `proxy.GetChannelCircuitSnapshot()`
  - 把 ChannelID 关联到 `proxy.ChannelMapCache[id]` 拿 Channel.Name/Type/BaseURL 一起返回（如果 channel 不在 cache，标记为 "unknown_channel"）
  - JSON 格式：`{ success, data: [{channel_id, channel_name, channel_type, base_url, state, consecutive_failures, current_cooldown_sec, open_until}] }`
- `func AdminForceResetChannelCircuit(c *fiber.Ctx) error` POST `/admin/channels/:id/circuit-reset`
  - 解析 :id 为 uint
  - 调 `proxy.ForceCloseChannelCircuit(id)`
  - LogOperationByTx（ActionType="CIRCUIT_FORCE_RESET"）
  - 返回 `{ success: true, message_code: "SUCCESS_CIRCUIT_RESET" }`

### 2. 前端（新文件 ui/src/components/ChannelCircuitMonitor.jsx）
- 在 ChannelManagement 页加一个 tab 或卡片，展示所有 channel circuit 状态
- 表格列：channel name / state badge (closed=green / open=red / half_open=yellow) / failures / cooldown_remaining
- 每行右侧"强制重置"按钮 → 调 POST endpoint
- 自动 30s 刷新（或手动刷新按钮）
- 状态变化即时更新

### 3. 测试（controller/channel_circuit_admin_test.go）
- TestAdminListChannelCircuits_ReturnsSnapshot 用 seed 几个 channel + 触发 circuit failures，验证响应包含正确状态
- TestAdminForceResetChannelCircuit_ResetsState 触发 open，调 endpoint 后验证 state=closed
- 验证 OperationLog 写入 CIRCUIT_FORCE_RESET

## 文件白名单（仅动这些文件）
- `controller/channel_circuit_admin.go` (新建)
- `controller/channel_circuit_admin_test.go` (新建)
- `ui/src/components/ChannelCircuitMonitor.jsx` (新建)
- `ui/src/components/ChannelManagement.jsx` (修改：添加 monitor tab 或卡片入口)

## 文件黑名单（禁止动这些文件 — 集成点由主进程统一处理）
- `main.go` — 不要加路由，列出需要加的路由 + 位置给主进程
- `database/sqlite.go` — 没有 schema 变更，不要动
- `i18n/zh-CN.json` / `i18n/en-US.json` — 列出需要的 message_code 给主进程

## 提交要求
- 完成后 `git add` 白名单文件 + 写 commit message
- commit message 标题：`feat: Sprint 5 M2 admin Channel Circuit Monitor endpoint + UI`
- commit body 列出：
  - 改了哪些文件
  - 新增哪些测试（带名字）
  - 需要主进程后续加的：路由 + i18n keys

## 测试
- `go test ./controller/ -run "TestAdminListChannelCircuits|TestAdminForceResetChannelCircuit" -count=1 -v` 必须通过
- `go build ./...` 必须通过
- 不能破坏现有测试（`go test ./... -count=1` 全过）

完成所有任务后，输出简短报告（中文）：
- 改了哪些文件
- 测试结果
- 主进程需补的路由 + i18n keys
