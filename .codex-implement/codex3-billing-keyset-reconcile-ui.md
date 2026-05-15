# Codex 3：M8 keyset pagination + admin reconcile UI（合并 BillsPage 一次性改完）

你是 daof-ai-hub 项目的资深全栈工程师，使用 codex 实现具体代码。

## 项目硬约束
- 项目**未上线**，处于重构期 → **禁止任何向后兼容 / alias / shim**
- 旧逻辑残留 = P0 必删
- 所有金额 int64 micro_usd；杜绝 float64 出现在金额计算

## 上下文

### 任务 A（M8 后端性能）
audit 模块 8 P1：账单列表使用 COUNT + OFFSET（controller/billing.go:156），CSV 一次性加载 10000 行到内存（controller/billing.go:289）。
- OFFSET 在大数据集下 O(N) 扫描；keyset 用 `WHERE id < last_id ORDER BY id DESC LIMIT N` 是 O(log N)
- CSV 应该流式 io.Pipe 写入响应而不是先全量收集再 Write

### 任务 B（M8 前端 reconcile）
Sprint 5 M8 已实现 `AdminReconcileBillingEntry` endpoint `POST /api/admin/billing/:id/reconcile`，
但前端没有调用入口。需要：
- BillsPage 列表中对 BillingState='pending_reconcile' 或 'upstream_unmetered' 行显示"对账"按钮
- 点击弹出 Modal，让 admin 选 result (absorbed / charged / voided) + 填 note
- 提交后调 endpoint，成功后刷新列表

## 实施需求

### 1. Keyset pagination 后端 (controller/billing.go)
- 给现有列表 endpoint 加入参 `cursor int64`（上一页最后一行的 id）
- 改为 keyset 查询：`WHERE user_id = ? AND id < cursor ORDER BY id DESC LIMIT N`
- 响应增加 `next_cursor int64`（本页最后一行 id；如果没有更多页则 0）
- 旧的 `page + offset` 参数：**直接删除**（pre-launch，无向后兼容）
- 同步 admin 端点 `AdminListUserBilling`（如果存在）改造

### 2. CSV 流式导出 (controller/billing.go)
- 把现有 `*BillingExport` endpoint（用户和 admin 各一个）改为流式：
  - 不再先 SELECT 全量加载，改用 `db.FindInBatches(batchSize=500)` 分批拉取
  - 每批拉到后立即 `csv.Writer.Write` 到 `c.Response().BodyWriter()`
  - 内存占用与总数据无关
- 删除任何"max 10000 行"限制（流式后无上限）

### 3. 前端 BillsPage.jsx
- 列表分页改为"加载更多"模式：维护 `cursor` state，每次 fetch 用上次的 `next_cursor`
- 删除现有 page number / total count 显示（keyset 没有 total）
- 对每行 BillingEntry：
  - 如果 `billing_state` ∈ {'pending_reconcile', 'upstream_unmetered'}：显示"对账"按钮
  - 否则显示状态徽章
- 新建 ReconcileBillingModal 组件（同文件或单独 jsx 都行）：
  - select: 对账结果 (absorbed / charged / voided)
  - textarea: note（required, max 500 chars）
  - 提交 POST `/api/admin/billing/:id/reconcile`
  - 成功 toast + 关闭 modal + 刷新列表
  - 错误处理：ERR_RECONCILE_RESULT_INVALID / ERR_RECONCILE_NOTE_REQUIRED / ERR_RECONCILE_NOTE_TOO_LONG / ERR_RECONCILE_NOT_PENDING / ERR_RECONCILE_ALREADY_DONE / ERR_RECONCILE_RACED

### 4. 测试 (controller/billing_test.go 扩展或新文件)
- TestMyBillingEntries_KeysetPagination
  - seed 30 条账单
  - 不带 cursor 拿 10 条 → next_cursor 应该是第 11 条 id（第一页最后一行）
  - 用 next_cursor 拿下 10 条 → 第二页
  - 验证无重复、无跳过
- TestMyBillingEntries_NoMorePagesReturnsZeroCursor
  - 最后一页 next_cursor=0
- TestMyBillingExport_StreamsLargeDataset
  - seed 1500 条（超过 10000 cap，但用 1500 节省时间）
  - 调 export，验证响应包含全部 1500 行
- 删除可能存在的"超 10000 行"测试

## 文件白名单
- `controller/billing.go` (keyset + CSV stream 重构)
- `controller/billing_test.go` (新测试) 或新建文件
- `ui/src/components/BillsPage.jsx` (keyset + reconcile modal)
- `ui/src/components/ReconcileBillingModal.jsx` (新建，可选；也可放 BillsPage 内)

## 文件黑名单
- `main.go` — 不动（路由已注册）
- `database/sqlite.go` — 不动（无 schema 变更）
- `i18n/*.json` — 列出需要的 message keys 给主进程
  - 至少需要：USER_MGMT / BILLS 等 namespace 下的 reconcile_title / reconcile_result_label / reconcile_note_label / SUCCESS_RECONCILED 等
- `controller/billing_reconcile.go` — Sprint 5 已实现，不动

## 提交要求
- `git add` 白名单文件
- commit message 标题：`feat: Sprint 5 M8 keyset pagination + admin reconcile UI`
- commit body 列出：
  - 后端 keyset 查询改造
  - CSV 流式导出
  - 前端 BillsPage reconcile 入口 + Modal
  - 测试用例
  - 主进程需补的 i18n keys

## 测试
- `go test ./controller/ -run "TestMyBilling" -count=1 -v` 必须通过
- `go build ./...` 必须通过
- 不能破坏现有 billing 测试
- `ui` 不强制跑 build（但应保证语法 OK）

完成后输出简短中文报告：改文件 / 测试结果 / 主进程需补 i18n。
