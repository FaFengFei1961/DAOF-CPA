# Sprint 5 后置深度审计：93 项发现 Baseline

> **审计日期**：2026-05-15
> **审计范围**：Sprint 4 + Sprint 5 累计 70+ commits 涉及的核心模块
> **来源**：5 个并行专业 agent（第 1 轮模块审计 = 65 项）+ 3 个深挖 codex（第 2 轮基于已知漏洞向外扩散 = 28 项去重后）
> **目的**：作为修复 baseline，每项都可被 grep/独立追踪。未来"全链修完"承诺必须能用 grep 严格证伪。

---

## 一、统计

| 严重度 | 数量 | 说明 |
|--------|------|------|
| CRITICAL | 2 | 必须立即修复 |
| HIGH | 21 | P0 范围 |
| MEDIUM | 39 | P1 范围 |
| LOW | 18 | P2 范围 |
| NOTE | 13 | 设计警示 |
| **总计** | **93** |  |

## 二、标签约定

| 标签 | 含义 | 处理 |
|------|------|------|
| `[BUG]` | 明确漏洞，直接修复 | 派 codex |
| `[DESIGN]` | 设计决策，需要拍板（多实例支持、SQLite 迁移等） | 用户决策 → 再派 codex |
| `[DEBT]` | 技术债，可延期但应记录 | 优先级低，时间允许时修 |

## 三、状态约定

| 状态 | 含义 |
|------|------|
| `pending` | 未开始 |
| `fixing` | 修复中（关联进行中的 codex 任务） |
| `done` | 已修复（关联 commit） |
| `wontfix` | 决策不修复（必须给理由） |
| `deferred` | 延期到下一个 sprint |

## 四、来源代号

| 代号 | 来源 | 范围 |
|------|------|------|
| `AGT1` | Agent 1 (security-reviewer) | Auth / Session / OAuth / UserGuard |
| `AGT2` | Agent 2 (security-reviewer) | Topup / Refund / Webhook |
| `AGT3` | Agent 3 (security-reviewer) | Billing / Reconcile / Coupon |
| `AGT4` | Agent 4 (go-reviewer) | Proxy / Cache / 并发 / 熔断 / 号池 |
| `AGT5` | Agent 5 (database-reviewer) | Schema / 索引 / 迁移 |
| `CX12` | Codex 12 | 深挖：金融精度 + append-only |
| `CX13` | Codex 13 | 深挖：业务校验 + 缓存一致性 |
| `CX14` | Codex 14 | 深挖：并发 + SSRF + 单点假设 |

---

# 主题 A：金融精度全链彻底修复

**批次目标**：消除所有 `float64` 出现在金额/价格/限额/汇率/cost 计算路径。承诺"金融级 0 偏差"前必须能用 `grep -rn "float64" controller/ database/ proxy/` 严格证伪。

**预计 codex 任务数**：2 个并行（DTO 入口 / 中间运算 + Schema 字段）

---

## A-1 `[BUG]` `[CRITICAL]` AdminRefundSubscription 入口 float64

- **来源**：AGT2 (C-1)
- **文件**：`controller/subscription.go:794-802` + `database/money.go:41-50`
- **问题**：DTO `AmountUSD float64` → `math.Round(req.AmountUSD*100)/100` → `USDToMicro(refundAmountUSD)` 完整 float 路径穿透到 `refundAmountMicro` 与 `purchasedPriceMicro` 比较
- **失效**：admin 输入 `$13.89` 经多次 float 运算，比较结果不可信；与 `AdminRefundTopup` 走 fen int64 → big.Int 整数路径不一致
- **修复**：DTO 改 int64 micro_usd 或 decimal string，big.Int 整数路径处理
- **状态**：`pending`

## A-2 `[BUG]` `[CRITICAL]` ChannelModel 价格矩阵 float64 污染全链路计费

- **来源**：CX12 (#4)
- **文件**：`controller/channel_model.go:61-72` + `database/schema.go:130-135`
- **问题**：价格 payload `float64` → `PricePicoPerTokenFromUSDPerMTok` + `math.Round(price * 1e9)` 落 pico
- **失效**：模型单价是**所有 API cost 的源头**，根上 float 整条链路都脏；高精度或 tie 值价格会先丢 decimal 语义
- **修复**：价格 API 改收 pico integer 或 decimal string；前端展示层不允许 float 进价格配置
- **状态**：`pending`

## A-3 `[BUG]` `[HIGH]` controller/user.go 多处 USD float 入口

- **来源**：CX12 (#3)
- **文件**：`controller/user.go:125,142,308,314,384,445`
- **问题**：用户额度更新、批量配额调整等多个 admin 接口接收 USD float → `USDToMicro` 转换
- **失效**：边界值受 IEEE-754 舍入；admin 设置额度时精度漂移
- **修复**：写接口改收 `*_micro_usd int64` 或 decimal string；服务端用 decimal/big.Rat 解析
- **状态**：`pending`

## A-4 `[BUG]` `[HIGH]` controller/token.go API Token 限额 float 入口

- **来源**：CX12 (#3)
- **文件**：`controller/token.go:71,95,150,184`
- **问题**：API token 创建/更新 quota_limit 接收 USD float
- **失效**：用户/admin 设置 token 限额时精度漂移
- **修复**：DTO 改 int64 micro_usd
- **状态**：`pending`

## A-5 `[BUG]` `[HIGH]` controller/balance_consume.go 余额消费限额 float 入口

- **来源**：CX12 (#3)
- **文件**：`controller/balance_consume.go:21,70`
- **问题**：用户余额消费偏好接口接收 USD float
- **失效**：用户自设余额消费上限精度漂移
- **修复**：DTO 改 int64 micro_usd
- **状态**：`pending`

## A-6 `[BUG]` `[HIGH]` controller/coupon.go 优惠券价格 float 入口

- **来源**：CX12 (#3)
- **文件**：`controller/coupon.go:235,250`
- **问题**：admin 创建优惠券模板 fixed_price 通过 USD float 输入
- **失效**：fixed_price 精度漂移可能导致 cost_floor 校验边界值错误
- **修复**：DTO 改 int64 micro_usd 或 decimal string
- **状态**：`pending`

## A-7 `[BUG]` `[HIGH]` controller/package_admin.go 套餐价格 float 入口

- **来源**：CX12 (#3)
- **文件**：`controller/package_admin.go:407,442`
- **问题**：admin 创建/更新套餐价格通过 USD float 输入
- **失效**：套餐价格精度漂移 → 套餐购买结算偏差
- **修复**：DTO 改 int64 micro_usd
- **状态**：`pending`

## A-8 `[BUG]` `[HIGH]` controller/upstream_cost.go 上游成本配置 float 入口

- **来源**：CX12 (#3, #5)
- **文件**：`controller/upstream_cost.go:28-29,152-156,544,623-627`
- **问题**：上游账号成本配置接收 float；平台成本/毛利分摊 `math.Round(float64(rawCost) * float64(monthlyCost) / float64(capacity))`
- **失效**：金额累计超 2^53 micro 丢精度；毛利报表偏差；估算结果回写历史 ApiLog（结合 C-2 双重风险）
- **修复**：用 big.Int/big.Rat 做比例分摊；估算结果写 append-only 估算表
- **状态**：`pending`

## A-9 `[BUG]` `[MEDIUM]` SQLite 迁移用 SQL REAL/CAST 做金额单位转换

- **来源**：CX12 (#6)
- **文件**：`database/sqlite.go:262,306`
- **问题**：`CAST(limit_value * 1000000 AS INTEGER)` + `CAST(ROUND(%s * 1000000000) AS INTEGER)` 在 SQL 层做 float 乘法 + 截断
- **失效**：历史 `quota_plans.limit_value` 回填 micro_usd 时直接截断，不是 decimal round；老数据可能少 1 micro
- **修复**：迁移逻辑用 Go decimal/big.Rat 逐行转换；对已迁移数据做校验/重算
- **状态**：`pending`

## A-10 `[BUG]` `[MEDIUM]` OAuth 默认余额限额走 SysConfig float string

- **来源**：CX12 (#7) + AGT2 (M-2)
- **文件**：`controller/sysconfig.go:127-134` + `controller/topup.go:1220, 1249-1258` + `controller/oauth.go:777,918`
- **问题**：`balance_consume_default_limit_usd` 用 `ParseFloat` 校验；`readMicroUSDConfig` / `readFloatConfig` 再 `ParseFloat` + `USDToMicro`；注册时写入 `BalanceConsumeLimitUSD`
- **失效**：新人注册默认消费限额受 float 解析影响
- **修复**：改为 `balance_consume_default_limit_micro_usd` 整数字段，一次性 decimal 迁移
- **状态**：`pending`

## A-11 `[DEBT]` `[MEDIUM]` SubscriptionUsage.ConsumedValue float64 字段

- **来源**：AGT5 (M-2)
- **文件**：`database/subscription_schema.go:217`
- **问题**：`ConsumedValue float64` 用于 request_count/token_count/weighted_tokens 累计
- **失效**：high-traffic 按 weighted_tokens 累加可能漂移（不直接影响金额，但影响配额精度）
- **修复**：改 int64（unit 是整数 token/request 计数，无浮点需求）
- **状态**：`pending`

## A-12 `[DEBT]` `[MEDIUM]` QuotaPlan.LimitValue float64 字段

- **来源**：AGT5 (M-2 关联)
- **文件**：`database/subscription_schema.go:34`
- **问题**：`LimitValue float64` 仅用于 admin 展示输入，引擎走 `LimitValueMicroUSD`
- **失效**：架构合理但代码层面 float 残留，违反"全面 int64"策略
- **修复**：保留 `LimitValue` 但加注释明确"仅 admin 展示输入，业务计算用 LimitValueMicroUSD"，或彻底去除 LimitValue
- **状态**：`pending`

## A-13 `[DEBT]` `[LOW]` PackagePlan.QuantityMultiplier float64

- **来源**：AGT5 (L-4)
- **文件**：`database/subscription_schema.go:141`
- **问题**：配额放大系数 float64 倍率 × int64 限额后截断 int64
- **失效**：若有非整数 multiplier（如 1.5x）会引入轻微漂移
- **修复**：若只用整数，改 int64；若分数，加注释或改 PPM int64
- **状态**：`pending`

## A-14 `[DEBT]` `[MEDIUM]` dto.go 展示层 float64 JSON wire 协议

- **来源**：AGT2 (M-4)
- **文件**：`controller/dto.go:43-44,53` `MoneyRMB / AmountUSD / RefundedAmountRMB float64`
- **问题**：只读展示字段返回 float（≤ $100000 不丢美分精度），但前端接收 float 后原样提交会引入精度传染
- **修复（可选）**：改返回 6 位小数字符串，让前端用 BigInt/Decimal 解析
- **状态**：`pending`

## A-15 `[DEBT]` `[LOW]` 测试 helper float 路径污染

- **来源**：CX12 (#8) + AGT2 (H-3)
- **文件**：`controller/subscription_integration_test.go:60-62` + `proxy/subscription_engine_integration_test.go:57-58` + `controller/yifut_security_test.go:92,157,200,251` + `controller/topup_security_test.go:36` + `controller/topup.go:1333` (round2 dead code)
- **问题**：测试 helper 收 float 参数 + Yifut 测试 `MoneyRMB: 10.0` 实际是 10 fen 但 callback 用 `"10.00"` 是 1000 fen（单位错误）+ round2 仅测试用 dead code
- **失效**：测试和生产同走 float，对称误差掩盖真精度 bug；Yifut 单位错误可能让真实金额错误回归挡不住
- **修复**：测试 helper 改收 micro/fen/pico int64；Yifut seed 改 `MoneyRMB: 1000` 并补 `AmountUSD/ExchangeRate`；删 round2
- **状态**：`pending`

---

# 主题 B：审计表 append-only 完整性

**批次目标**：所有审计性质的表必须 INSERT-only。可变字段拆到 side table。明确禁止 `Updates(map[string]any{})` 用于审计表。

**预计 codex 任务数**：1 个（ApiLog 改造是大动作，独立批次）

---

## B-1 `[BUG]` `[CRITICAL]` ApiLog 不是 append-only（核心审计漏洞）

- **来源**：CX12 (#1)
- **文件**：
  - Schema 缺保护：`database/schema.go:207,223,244,253`（ApiLog 关键字段无 `<-:create`）
  - UPDATE 路径：`controller/cliproxy_usage_sync.go:429-438` (`Updates(updates)` 回写上游归因)
  - UPDATE 路径：`controller/upstream_cost.go:572` (回写 `platform_cost_estimate`)
  - DELETE 路径：`proxy/subscription_cron.go:171` (cron 定期删 ApiLog)
  - DELETE 路径：`controller/user.go:758` (`Unscoped().Delete(&database.ApiLog{})` 删用户时硬删)
- **失效**：admin 误配 `apilog_retention_days` 或执行用户删除后，请求成本/上游账号归因/用量事实被抹掉；后续 margin/对账无法证明原始请求发生过
- **修复**：
  1. ApiLog 原始事实只 INSERT；
  2. 上游匹配、平台成本估算放到 append-only side table（如 `ApiLogUpstreamAttribution` / `ApiLogCostEstimate`）；
  3. 历史保留用归档/脱敏（不删除）；
  4. DB trigger 禁止 `api_logs` UPDATE/DELETE 兜底
- **状态**：`pending`

## B-2 `[BUG]` `[HIGH]` BillingEntry RelatedID 指向虚空（用户删除时硬删源表）

- **来源**：CX12 (#2)
- **文件**：`database/billing_schema.go:11,53-54` + `controller/user.go:774-780`
- **问题**：BillingEntry.RelatedType 指向 `topup_orders / user_subscriptions / api_logs`；删用户时硬删 `SubscriptionUsage / UserSubscription / TopupOrder`
- **失效**：退款/争议后 admin 硬删用户，BillingEntry 行留下但源证据消失 → 账务链断裂
- **修复**：支付订单/订阅快照/用量事实**不要随用户删除**，仅匿名化 PII 或软删除；BillingEntry 关联源表需可追溯
- **状态**：`pending`

## B-3 `[BUG]` `[HIGH]` GORM `Updates(map[string]any)` 绕过 `<-:create` tag

- **来源**：AGT5 (HD-1)
- **文件**：`database/distributed_lock_helper.go:52-57`（合法使用，但建立危险先例）
- **问题**：GORM v2 已知行为 — `<-:create` 只对 struct 型 Update 生效，对 map 型 Updates 完全无效
- **失效**：任何审计表若有人 `DB.Model(&AuditTable{}).Updates(map{...})` 都能改 `<-:create` 字段
- **修复**：
  1. 团队约定文档：审计表禁用 `Updates(map)` 模式；
  2. 在 `billing_helper.go` / 审计表 helper 文件头加注释强制 `tx.Create(&entry)`；
  3. CI 加 grep 检查：`Updates(map\[string\]any{}` 不能与审计表名共存
- **状态**：`pending`

## B-4 `[BUG]` `[MEDIUM]` BillingEntry.CreatedAt 缺 `<-:create`

- **来源**：AGT5 (M-1)
- **文件**：`database/billing_schema.go:81`
- **问题**：OperationLog/TopupRefund/PaymentWebhookReceipt 的 CreatedAt 都打了 `<-:create`，独 BillingEntry 漏掉
- **失效**：admin 可通过 GORM struct Update 修改 `billing_entries.created_at`，破坏时间戳审计链
- **修复**：`CreatedAt time.Time \`gorm:"<-:create" json:"created_at"\``
- **状态**：`pending`

## B-5 `[BUG]` `[MEDIUM]` BillingEntry 无 (related_type, related_id, entry_type) 唯一约束

- **来源**：AGT5 (M-3)
- **文件**：`database/billing_schema.go:52-54` + `database/sqlite.go:194-195`
- **问题**：现 `idx_billing_related` 是普通索引，DB 层无最终防线防同一来源重复入账
- **失效**：若状态 CAS 检查与 BillingEntry INSERT 不在同一事务，并发场景下可能双写
- **修复**：partial unique `WHERE related_id > 0`（排除 api_usage_* 大量 related_id=0 行）：
  ```sql
  CREATE UNIQUE INDEX IF NOT EXISTS idx_billing_related_unique
  ON billing_entries(related_type, related_id, entry_type)
  WHERE related_id > 0
  ```
- **状态**：`pending`

## B-6 `[DESIGN]` `[HIGH]` UserSession.ExpiresAt `<-:create` 与滑动续期冲突

- **来源**：AGT5 (HD-2)
- **文件**：`database/session_schema.go:18`
- **问题**：ExpiresAt 打了 `<-:create`，未来想做 sliding window 续期会被 tag 锁死
- **决策点**：是否支持 session 滑动续期？
  - 不支持：保留 `<-:create` + 文档明确
  - 支持：去掉 `<-:create` 或通过 `DB.Exec("UPDATE...")` 绕过
- **状态**：`pending`

## B-7 `[DEBT]` `[LOW]` TicketMessage 无 `<-:create` 保护

- **来源**：AGT5 (L-1)
- **文件**：`database/customer_message_schema.go`
- **问题**：TicketMessage 应该是消息流水（不可修改），但整张表无 `<-:create` 保护
- **失效**：admin 路径可能意外修改工单消息内容（无 DB 层保护）
- **修复**：Body/Sender/SenderID/TicketID/CreatedAt 加 `<-:create`
- **状态**：`pending`

---

# 主题 C：业务校验跨路径漏洞

**批次目标**：所有 validate 函数覆盖完整路径（create + update + use）；所有 snapshot 字段使用时重新校验当前业务规则；所有过滤条件不漏掉合法零值。

**预计 codex 任务数**：1 个

---

## C-1 `[BUG]` `[HIGH]` cost_floor=0 套餐被 coupon 校验完全跳过

- **来源**：AGT3 (HB-1)
- **文件**：`controller/coupon.go:97-103`
- **问题**：`validateTemplateFixedPriceCostFloor` 用 `WHERE cost_floor_micro_usd > 0` 排除 cost_floor=0 套餐
- **攻击场景**：admin 故意不设 cost_floor（保持 0）的套餐 → 可发 $0.01 券薅羊毛
- **修复**：cost_floor=0 时退而使用 `price_amount * 最低毛利率` 作为事实下限；或强制要求所有 public 套餐配 cost_floor
- **状态**：`pending`

## C-2 `[BUG]` `[HIGH]` coupon 兑换不重新校验 cost_floor

- **来源**：AGT3 (HB-2)
- **文件**：`database/coupon_schema.go:108-120` + `controller/subscription.go:287`
- **问题**：已发券的 SnapshotValue 不随 template/package 更新；`SnapshotEffectivePrice` 无 cost_floor 检查；购买路径 `lockAndApplyCoupon` 也不校验
- **攻击场景**：admin 先发 fixed_price=$8 券，后提价 cost_floor=$15，历史券仍按 $8 兑换 → 亏损
- **修复**：`lockAndApplyCoupon` 内重新校验 `coupon.SnapshotValue >= pkg.CostFloorMicroUSD`，不满足则拒绝
- **状态**：`pending`

## C-3 `[BUG]` `[HIGH]` User.Status 状态机跨路径不一致

- **来源**：CX13 (#1)
- **文件**：
  - 定义：`database/schema.go:32`（只 1=正常, 2=封禁）
  - UpdateUser 用非指针 `Status int`：`controller/user.go:126`
  - 不校验 status 白名单：`controller/user.go:153-158,210-214`
  - AuthCache 只加载 `status=1`：`proxy/cache.go:155`
  - **只拒绝 status==2**：`middleware/user_guard.go:25,44` + `controller/token.go:45` + `controller/oauth.go:597-608`
- **失效**：admin 漏传 status → Go 零值 0；API token 因 AuthCache 失效，但 SessionID + OAuth 老用户路径继续放行（只拦 2）
- **修复**：
  1. 所有入口统一要求 `user.Status == 1`；
  2. UpdateUser 用 `*int` 区分未传 vs 显式传值，白名单 `1/2`；
  3. OAuth 老用户登录也要 `status=1`
- **状态**：`pending`

## C-4 `[BUG]` `[HIGH]` dto.go percent 类型幽灵代码

- **来源**：AGT3 (HB-4)
- **文件**：`controller/dto.go:189`
- **问题**：`validateTemplate` 已锁死 `fixed_price`，但 `dto.go:189` 有 `if t.DiscountType != "percent"` 残留
- **失效**：admin 若绕过 validateTemplate 直接 raw SQL 改 DB，`SnapshotEffectivePrice` 走 default 返回原价（券无效但无报错）
- **修复**：删除 percent 特判，统一为 `microUSDToFloat(t.DiscountValue)`；`SnapshotEffectivePrice` default 分支加 log warning
- **状态**：`pending`

## C-5 `[BUG]` `[LOW]` lockAndApplyCoupon 条件 UPDATE 缺 expires_at 守卫（TOCTOU）

- **来源**：AGT3 (LOW)
- **文件**：`controller/coupon.go:200-211`
- **问题**：条件 UPDATE 仅 `WHERE id=? AND user_id=? AND status='available'`，无 `expires_at > now`
- **失效**：微秒级 TOCTOU 窗口允许刚过期券被使用
- **修复**：WHERE 加 `AND (expires_at IS NULL OR expires_at > ?)` 传入 now
- **状态**：`pending`

---

# 主题 D：鉴权 / 会话 / 管理员保护

**批次目标**：Logout 路径完整失效所有缓存；封禁立即撤 session；最后一个 admin 保护事务化；tmp_token 单次消费。

**预计 codex 任务数**：1 个

---

## D-1 `[BUG]` `[HIGH]` AuthLogout 不失效 AuthCache（Bearer token 路径）

- **来源**：AGT1 (H1)
- **文件**：`controller/auth_logout.go:14-35` + `proxy/cache.go:51-55` + `middleware/user_guard.go:43-54`
- **问题**：AuthLogout 仅撤 Session 表，AuthCache（Bearer sk-daof-xxx 路径）不失效，直到下次 SyncCacheConfig 全量刷新
- **攻击场景**：用户 logout 后老 API token 仍能通过 LookupUserByToken 鉴权
- **修复**：AuthLogout 内根据 sessionID 找 user，同步调 `proxy.EvictUserToken(user.Token)`
- **状态**：`pending`

## D-2 `[BUG]` `[HIGH]` tmp_token 无服务端单次消费（可重放）

- **来源**：AGT1 (H2)
- **文件**：`controller/oauth.go:55-83` (parseTmpToken) + `controller/oauth.go:711-816` (CompleteRisk) + `controller/oauth.go:864-955` (CompleteProfile)
- **问题**：tmp_token AES-GCM 无状态，只校验 TTL 不做撤销，15 min TTL 内可重放
- **失效**：攻击者截获 tmp_token 可反复打 CompleteRisk/CompleteProfile，被 DB unique 拦但消耗 registerMu 锁 + DB 资源
- **修复**：`sync.Map` 存已消费 jti（hash 后存），消费后立即标记，TTL 到期清理
- **状态**：`pending`

## D-3 `[BUG]` `[HIGH]` 最后一个可用管理员保护 TOCTOU + 计数条件错

- **来源**：CX14 (#3)
- **文件**：
  - AdminGuard 只允许 `status=1`：`middleware/admin_guard.go:41-44`
  - 封禁前事务外 count：`controller/user.go:152-159`（更新在 201-215）
  - 删除前 count **不带 status=1**：`controller/user.go:680-683`（删除事务从 704 开始）
- **失效**：1 active + 1 banned admin 时删 active admin，或两个 admin 并发封禁/删除 → 系统永久锁死无法登录
- **修复**：
  1. 同一事务内锁目标 admin + active admin 集合；
  2. 所有保护 count 用 `role='admin' AND status=1 AND deleted_at IS NULL`；
  3. 条件 UPDATE/DELETE 保证"变更后 active admin 数 > 0"
- **状态**：`pending`

## D-4 `[BUG]` `[HIGH]` GodSetup 内网横向移动接管路径

- **来源**：AGT1 (H3)
- **文件**：`main.go:232` + `controller/admin_auth.go:153-202`
- **问题**：GodSetup `isInitialSetup=true` 路径允许任何能访问 /api/root/（受 LanGuard）的人绕过旧密码改 admin 密码
- **场景**：内网横向移动者可无密码接管 admin
- **修复**：限定只有首次 `/api/root/check-sys` 返回 `setup_required=true` 时才开放该路径（复用 SetupGuard 类似逻辑）
- **状态**：`pending`

## D-5 `[BUG]` `[MEDIUM]` 封禁用户不撤销 UserSession

- **来源**：CX13 (#3)
- **文件**：`controller/user.go:248-253` + `database/session_helper.go:97-108`
- **问题**：admin 改用户后只 `SyncCacheConfig + EvictUserToken`，**漏调 `RevokeSessionsForUser`**
- **失效**：封禁后浏览器 session 行仍有效；解封后旧 session 立即恢复无需重新登录；叠加 C-3 (User.Status) 漏洞更严重
- **修复**：用户 status 从 active 变为非 active 时，同事务（或 tx 成功后）撤销该用户所有 session
- **状态**：`pending`

## D-6 `[BUG]` `[MEDIUM]` AdminLogout 旧 token 路径不撤 Session（架构空白）

- **来源**：AGT1 (M2)
- **文件**：`controller/admin_auth.go:43-62`
- **问题**：若 token 不是 sessionID（IsSessionID=false），跳过 session 撤销直接 token 旋转；Session 表残留
- **现状**：当前架构 admin cookie 存 sessionID，此路径基本不走，但有逻辑空白
- **修复**：非 sessionID 路径也加 `RevokeSessionsForUser` 兜底
- **状态**：`pending`

## D-7 `[BUG]` `[MEDIUM]` LookupUserBySession 把 last_used_at 写当鉴权硬依赖

- **来源**：AGT1 (M1) + CX13 (#9)
- **文件**：`database/session_helper.go:122-127, 130-132`
- **问题**：每次 UserGuard 验 session 都 UPDATE last_used_at，写失败直接返回 false
- **失效**：DB 可读但短暂写锁/写故障时合法 session 被判 401；高频请求加剧 SQLite 写锁压力
- **修复**：last_used_at 改 best-effort，失败仅 log；或节流刷新（每 N 分钟更新一次）
- **状态**：`pending`

---

# 主题 E：熔断器 + HTTP status 分类重写

**批次目标**：HTTP status 子类分类精细化；状态码驱动的业务/基础设施动作正确分类（retry/熔断/calc/扣费）。

**预计 codex 任务数**：1 个

---

## E-1 `[BUG]` `[HIGH]` 401/403 触发熔断（用户/key 问题误判为渠道故障）

- **来源**：AGT4 (HP-1)
- **文件**：`proxy/stream.go:895` + `proxy/channel_circuit.go:21-23`
- **问题**：MarkChannelFailure 在 401/403 时累加 `consecutiveFailures`，5 次后 channel 进入 open
- **失效**：用户 key 过期/Tier 限制问题被误判为 channel 不健康，全平台用户被拒
- **修复**：401/403 不触发跨请求 circuit breaker（可记 lastFailureNano 但不累加 consecutiveFailures）
- **状态**：`pending`

## E-2 `[BUG]` `[HIGH]` 400 响应误调 MarkChannelSuccess（错误重置 half-open 探针）

- **来源**：AGT4 (HP-2)
- **文件**：`proxy/stream.go:918-923`
- **问题**：400 不该 retry 但 `break` 前调 `MarkChannelSuccess` → 重置 consecutive_failures + open_until
- **失效**：channel 处于 half-open 时 probe 因请求格式问题收到 400 → 错误把 open 状态清零
- **修复**：400 既不调 success 也不调 failure（请求侧错误，保持 circuit 状态不变）
- **状态**：`pending`

## E-3 `[BUG]` `[HIGH]` extendCircuitOpen 多字段 Store 非原子（admin 快照不一致）

- **来源**：AGT4 (HP-3 → MEDIUM)
- **文件**：`proxy/channel_circuit.go:109-112`
- **问题**：`currentCooldownSec.Store(nextSec)` + `openUntilNano.Store(...)` 两次独立 atomic.Store
- **失效**：GetChannelCircuitSnapshot 读取时可能看到 currentCooldownSec 已更新但 openUntilNano 未更新的中间态
- **修复**：用 `atomic.Pointer[circuitState]` 整体替换或加 mutex
- **状态**：`pending`

## E-4 `[BUG]` `[MEDIUM]` 408 (Gateway Timeout) 当成"成功"处理

- **来源**：CX14 (#4)
- **文件**：`proxy/stream.go:895, 918-923, 1562-1578`
- **问题**：retry 条件只 `429/401/403/>=500`，408 不在其中；fallback 进 `MarkChannelSuccess`
- **失效**：408 不切换其他 channel，half-open 探针被错误关闭为成功，客户端直接收 408
- **修复**：408/504 归为 retryable transient，但不按长期故障熔断
- **状态**：`pending`

## E-5 `[BUG]` `[MEDIUM]` 404/410 已路由模型不 retry 且 MarkChannelSuccess

- **来源**：CX14 (#5)
- **文件**：`proxy/stream.go:895, 918-923`
- **问题**：404/410 是 channel/model 配置错误（路径错、route cache 陈旧、模型部署被删），但被当成功
- **失效**：有其他健康 channel 时仍直接给用户 404/410；该坏 channel 被记为成功
- **修复**：路由命中的模型返回 404/410 归为 channel/model 配置错误，可 retry 其他 route；标记 channel_model unhealthy
- **状态**：`pending`

## E-6 `[BUG]` `[MEDIUM]` 429 忽略 Retry-After，本地短退避累计熔断

- **来源**：CX14 (#6)
- **文件**：`proxy/stream.go:895-900` + `proxy/channel_circuit.go:225-245`
- **问题**：429 进故障分支，退避用本地指数 jitter（100ms-2s）；主代理未读 `Retry-After` header
- **失效**：上游 `Retry-After: 60` 时仍 100ms 重试，快速累计 channel failure，临时限流升级为熔断
- **修复**：解析 Retry-After，建立 per-channel rate-limit cooldown；429 不与 502/503 同等计入故障阈值
- **状态**：`pending`

## E-7 `[BUG]` `[MEDIUM]` jitter 用时间戳取模分布不均

- **来源**：AGT4 (M jitter)
- **文件**：`proxy/channel_circuit.go:243`
- **问题**：`int64(time.Now().UnixNano()/1000) % (delay/2)` 在 Windows 100ns 精度下高并发同微秒入循环 → jitter 完全相同
- **失效**：thundering herd 防御失效
- **修复**：用 `math/rand/v2` 的 `mrand.Int64N(delay/2)`（crypto 安全且无锁）
- **状态**：`pending`

## E-8 `[DEBT]` `[MEDIUM]` MarkChannelFailure 每次调 loadCircuitConfig 持 RLock

- **来源**：AGT4 (M loadCircuitConfig)
- **文件**：`proxy/channel_circuit.go:138, 160`
- **问题**：高错误率时每个失败请求竞争 SysConfigMutex.RLock；SyncCacheConfig 写锁期间所有阻塞
- **修复**：用 `atomic.Pointer[circuitConfig]` 缓存配置，SyncCacheConfig 原子替换
- **状态**：`pending`

---

# 主题 F：SSRF 加固

**批次目标**：所有出站 HTTP 走 SafeTransport；redirect 重新校验；SSRF 名单覆盖所有云元数据 + CGNAT + IPv6 子集。

**预计 codex 任务数**：1 个

---

## F-1 `[BUG]` `[HIGH]` CPA HTTP client 未走 SafeTransport（DNS rebinding）

- **来源**：CX14 (#2) + CX13 (#2)
- **文件**：`proxy/credits_pool.go:105-120` (cpaHTTPClient/cpaAuthFilesClient 裸 http.Transport) + `proxy/credits_pool.go:269` (PingCliproxy 新建默认 client) + `proxy/credits_pool.go:657-667,725-733,1077-1085` (使用点) + `proxy/content_moderation.go:581-595`
- **问题**：保存 `cliproxy_url` 时校验 (`controller/sysconfig.go:338-347`)，但使用时 client 无 `DialContext` SafeTransport
- **攻击场景**：`cliproxy_url` 校验时解析公网，DNS rebinding 后实际请求带 `Authorization` header 到 metadata/内网
- **修复**：所有 CPA client 统一用 `proxy.SafeTransport()`；`getCliproxyURL()` 取出后再做 use-time URL 校验
- **状态**：`pending`

## F-2 `[BUG]` `[MEDIUM]` HTTP 默认跟随 redirect 不重新校验 URL

- **来源**：CX14 (#7)
- **文件**：`proxy/stream.go:860-863` + `controller/channel_model.go:702-705` + `controller/cliproxy.go:48-51`（全无 `CheckRedirect`）
- **攻击场景**：合法公网 BaseURL 上游返回 302 到 `http://127.0.0.1` 或 RFC1918 → SafeTransport 在 dial 层拦但仍允许 loopback/RFC1918
- **修复**：设置 `CheckRedirect`，每跳重新跑 URL 策略；默认禁跨 host/scheme redirect
- **状态**：`pending`

## F-3 `[BUG]` `[MEDIUM]` 易付通 SSRF 名单遗漏 Azure/CGNAT/IPv6 6to4

- **来源**：CX14 (#8) + AGT4 (M Azure IMDS)
- **文件**：`proxy/yifut_client.go:85-87, 391-401` (ValidateGateway/isUnsafeIP) + `proxy/url_safety.go:130-132` (Azure IMDS)
- **遗漏 IP**：
  - Azure Wireserver `168.63.129.16/32`
  - CGNAT `100.64.0.0/10`
  - IPv6 6to4 `2002::/16`
  - Teredo / benchmark / documentation ranges
- **场景**：支付网关 client 连云控制面/保留网段
- **修复**：用 `netip.Prefix` 显式维护 denylist
- **状态**：`pending`

## F-4 `[BUG]` `[MEDIUM]` safeDialContext 失去 Happy-Eyeballs

- **来源**：AGT4 (M Happy-Eyeballs)
- **文件**：`proxy/url_safety.go:77`
- **问题**：所有 IP 安全验证后只用 `addrs[0]`（DNS 返回顺序），丢失 RFC 8305 并发尝试 v4/v6
- **失效**：v6 连通性差的环境下连接超时后才 fallback，增加延迟
- **修复**：安全验证通过后还原传入原始 `addr`（含域名）给 `d.DialContext`
- **状态**：`pending`

---

# 主题 G：充值/退款/Webhook 加固

**批次目标**：webhook 配置启动期 fail-fast；订阅退款幂等键；退款 BillingEntry 状态机闭环。

**预计 codex 任务数**：1 个（可合并到 主题 A 金融精度批次）

---

## G-1 `[BUG]` `[HIGH]` webhook bad CIDR 静默 continue（admin 配错不告警）

- **来源**：AGT2 (H-1)
- **文件**：`controller/topup.go:1113-1116`
- **问题**：`checkYifutNotifyIPAllowed` 解析失败时 fallback 裸 IP 比较 + log.Printf；运行时静默跳过，admin 配错全部 CIDR 时所有 webhook 被拒（fail-closed）但无告警
- **修复**：启动时验证 CIDR 配置，格式错误 fatal；删除运行时 fallback 裸比较
- **状态**：`pending`

## G-2 `[BUG]` `[MEDIUM]` AdminRefundSubscription 无幂等键

- **来源**：AGT2 (M-1)
- **文件**：`controller/subscription.go:862-929`
- **问题**：与 AdminRefundTopup 不一致（topup 有 ExternalRefundRef unique）；订阅退款仅靠 status CAS 防并发
- **失效**：admin 双击"确认退款"，409 告诉"状态已变化"不够明确；无后续操作审计
- **修复**：加幂等键或新增 `SubscriptionRefund` 事实表（参考 TopupRefund 范式）
- **状态**：`pending`

## G-3 `[DESIGN]` `[NOTE]` IP 白名单默认空 fail-open

- **来源**：AGT2 (NOTE-1)
- **决策点**：`yifut_notify_allowed_cidrs` 默认空 = 允许所有 IP，仅靠签名+nonce。是否启动时加 warning？或强制配置？
- **建议**：启动时若空，log.Warn "WARNING: no IP whitelist configured for yifut webhook"
- **状态**：`pending`

## G-4 `[DESIGN]` `[NOTE]` nonce 用 sign 前 16 字符（熵脆弱）

- **来源**：AGT2 (NOTE-2)
- **决策点**：当前 nonce = `provider + ":" + out_trade_no + ":" + sign[:16]`。改成 `signatureHash(sign)` 全 SHA-256 更稳？
- **建议**：用全量 sha256 hash，nonce 长度仍在限制内
- **状态**：`pending`

## G-5 `[DESIGN]` `[NOTE]` Yifut webhook 走 GET 不是 POST

- **来源**：AGT2 (NOTE-4)
- **决策点**：参数在 URL query string，sign 字段在 server log / proxy log 全记录。向易付通确认是否支持 POST 回调
- **状态**：`pending`

---

# 主题 H：缓存 / 状态机 / 失效完整性

**批次目标**：所有缓存对应业务事件触发失效；状态字段流转完整可表达；模型/路由/通知/审核口径一致。

**预计 codex 任务数**：1-2 个（部分项较琐碎可合并）

---

## H-1 `[BUG]` `[HIGH]` BillingEntry.BillingState 对账后状态永不变 → UI 混淆

- **来源**：AGT3 (HIGH)
- **文件**：`controller/billing.go:160-192` (listBillingEntries 不 join reconciliation 表)
- **问题**：`BillingEntry.BillingState` 是 `<-:create` append-only，对账后仍是 `pending_reconcile`；admin 列表 UI 持续显示"待对账"
- **失效**：admin 重复对账（被 unique constraint 拒绝，但 UX 差）
- **修复**：admin 列表 DTO 加 `is_reconciled` 字段，LEFT JOIN `billing_reconciliations`；或加 `BillingEntry.is_reconciled bool` 字段在 reconcile tx 内更新（不破坏 append-only 金额字段）
- **状态**：`pending`

## H-2 `[BUG]` `[MEDIUM]` CPA 号池冷启动盲区

- **来源**：AGT4 (M validateAuthFilesResponse)
- **文件**：`proxy/credits_pool.go:375-421`
- **问题**：`prevTotal == 0` 时透传空快照不拦截
- **失效**：首次启动 CPA 返回空列表 → 系统以空号池启动，所有依赖号池功能静默失效
- **修复**：
  ```go
  if prevTotal == 0 && validCount == 0 {
      log.Printf("[CREDITS-ANOMALY] ABORT: cold start but auth-files is empty")
      return false
  }
  ```
- **状态**：`pending`

## H-3 `[BUG]` `[MEDIUM]` moderation_cache_secret 旋转不清内存 secret

- **来源**：CX13 (#4)
- **文件**：`proxy/content_moderation.go:293-305, 391-395` + `controller/sysconfig.go:404-409`
- **问题**：`moderationCacheSecret` memoize，admin 改 SysConfig 只 flush 内容 LRU，secret 仍是旧值直到进程重启
- **修复**：增加 `ResetModerationCacheSecret()`，在 secret 变更后清空 memoized + flush 内容缓存
- **状态**：`pending`

## H-4 `[BUG]` `[MEDIUM]` DeleteQuotaPlan 非事务 + 忽略 Count error

- **来源**：CX13 (#5)
- **文件**：`controller/quota_plan.go:273, 282`
- **问题**：`Count(&refCount)` 未检查 `.Error`；DB count 临时失败 → refCount=0 → delete 放行
- **失效**：删除在用 QuotaPlan，PackagePlan 指向不存在 plan；用户买套餐时 `buildPackageSnapshotTx` fail-closed
- **修复**：事务包 count+delete，检查 Count error 并 fail-closed；锁定 quota_plan 行或加 FK 约束
- **状态**：`pending`

## H-5 `[BUG]` `[MEDIUM]` 公开模型列表与 RouteCache 口径不一致

- **来源**：CX13 (#6)
- **文件**：`controller/channel_model.go:312, 362-374`
- **问题**：`/v1/models` 只查 `channel_models.status=1`，不 join `channels.status=1`
- **失效**：admin 禁用 channel 但下属 model 仍 status=1 → 用户看见"可见但不可用"幽灵模型
- **修复**：公开模型与价格聚合 JOIN `channels` 要求 `channels.status=1`，或直接基于 RouteCache 输出
- **状态**：`pending`

## H-6 `[BUG]` `[MEDIUM]` AddChannelModelsBatch 忽略 Find 错误

- **来源**：CX13 (#7)
- **文件**：`controller/channel_model.go:828-829`
- **问题**：`Find(&existing)` 未检查 `.Error`，DB 读失败时 existing=空 → 批量插入含重复
- **修复**：检查 .Error 直接 500；用 `ON CONFLICT DO NOTHING` 并返回真实 inserted 数
- **状态**：`pending`

## H-7 `[BUG]` `[MEDIUM]` 批量发券单 tx 50,000 INSERT 风险

- **来源**：AGT3 (MEDIUM)
- **文件**：`controller/coupon.go:508-679`
- **问题**：单 tx 最多 500 用户 × 100 张 = 50,000 INSERT；SQLite 单写者串行；HTTP 客户端超时但 tx 仍可能提交
- **修复**：限制降至 50 用户 × 10 张/批；或多事务批次（牺牲原子性）；至少 log 总耗时
- **状态**：`pending`

## H-8 `[BUG]` `[MEDIUM]` billingSummary 缺 covering index

- **来源**：AGT3 (MEDIUM)
- **文件**：`controller/billing.go:236` + `database/sqlite.go`
- **问题**：聚合 SQL 仅单列索引可选，回表拿 amount_usd；10 万行用户每次 summary 全表用户过滤+行回查
- **修复**：`CREATE INDEX idx_billing_summary ON billing_entries(user_id, entry_type, occurred_at, amount_usd)` covering
- **状态**：`pending`

## H-9 `[BUG]` `[MEDIUM]` UpstreamUsageRecord.created_at 无索引

- **来源**：AGT5 (M-4)
- **文件**：`database/schema.go:286` + cron 路径
- **问题**：仅 Timestamp 有索引，cron 按 created_at 清理会全表扫
- **修复**：补 `idx_upusage_created_at` 或统一 cron 用 timestamp 字段
- **状态**：`pending`

## H-10 `[BUG]` `[LOW]` 未读通知 partial index 未排除 revoked

- **来源**：AGT5 (L-2)
- **文件**：`database/notification_schema.go:252-253` + `database/sqlite.go:150-151`
- **问题**：`idx_notif_user_unread` partial `WHERE read_at IS NULL`，未排除 `revoked_at IS NOT NULL`
- **失效**：用户铃铛显示已撤回广播未读条数
- **修复**：partial 改为 `WHERE read_at IS NULL AND revoked_at IS NULL`
- **状态**：`pending`

## H-11 `[BUG]` `[LOW]` NotificationPreference 首次保存 read-then-create unique 冲突

- **来源**：CX14 (#14)
- **文件**：`database/notification_preference.go:113-128`
- **问题**：先 First 再 Create/Save，user_id unique；双请求并发触发 500
- **修复**：用 `ON CONFLICT(user_id) DO UPDATE`，或事务内锁用户行
- **状态**：`pending`

## H-12 `[BUG]` `[LOW]` 重排接口不校验 RowsAffected

- **来源**：CX13 (#8)
- **文件**：`controller/package_admin.go:776-781` + `controller/quota_plan.go:301-305`
- **问题**：仅检查 .Error 不检查 RowsAffected；陈旧 ID 静默吞
- **修复**：每次 update 要求 RowsAffected==1，否则回滚返回 409/404
- **状态**：`pending`

## H-13 `[BUG]` `[MEDIUM]` 净收支不含待对账条目 UI 提示

- **来源**：AGT3 (MEDIUM)
- **文件**：`controller/billing.go:236`
- **问题**：billing summary 月度卡片不含 api_usage_pending_reconcile，用户对账后看到净额变化感到混乱
- **修复**：summary 响应加 `pending_reconcile_count`，UI 提示"另有 N 笔待对账，净额可能变化"
- **状态**：`pending`

## H-14 `[DEBT]` `[LOW]` UpstreamAccountCost.MonthlyCostUSD 字段名缺 micro 后缀

- **来源**：AGT5 (L-3)
- **文件**：`database/upstream_account_cost_schema.go:17`
- **问题**：注释说"micro_usd"但字段名无 `_micro` 后缀，前端/admin 易理解为原始 USD
- **修复**：字段名改 `monthly_cost_micro_usd` 与 `estimated_monthly_capacity_micro_usd`
- **状态**：`pending`

## H-15 `[DEBT]` `[LOW]` transportCache 双写竞态

- **来源**：AGT4 (L transportCache)
- **文件**：`proxy/stream.go:107-116`
- **问题**：Load miss 后双 goroutine 都 Store，先写入的 transport（含连接池）被丢弃
- **修复**：用 `LoadOrStore`，loaded=true 时返回 actual
- **状态**：`pending`

## H-16 `[DEBT]` `[LOW]` computeRetryBackoff 用 math.Pow

- **来源**：AGT4 (L computeRetryBackoff)
- **文件**：`proxy/channel_circuit.go:236`
- **问题**：`math.Pow(2, float64(attempt-1))`；当前 attempt 最大 4 无风险，未来 maxRetries>50 时退化
- **修复**：位移 `int64(baseMs) << uint(attempt-1)`
- **状态**：`pending`

## H-17 `[DEBT]` `[LOW]` http.Client 每请求 new

- **来源**：AGT4 (L http.Client)
- **文件**：`proxy/stream.go:860-866`
- **问题**：Transport 复用但 http.Client 实例频繁 new，GC 压力
- **修复**：以 proxyURL+timeout key 缓存 http.Client
- **状态**：`pending`

## H-18 `[DESIGN]` `[NOTE]` SyncCacheConfig 持写锁外做查询（正面发现）

- **来源**：AGT4 (NOTE)
- **说明**：所有 DB 查询在写锁**外**完成，写锁只保护最后 map swap（微秒级）。lock-free-read + atomic-publish 模式正确，**不是 finding，是工程亮点确认**

---

# 主题 I：单实例假设全面崩溃 `[DESIGN]`

**批次目标**：决定是否支持多实例水平扩展。当前所有进程内状态在多实例下崩溃。

**这是架构决策，不是 BUG。** 单实例部署完全 OK；多实例需要 Redis 共享缓存 + 集群 invalidation。**需要用户拍板。**

**预计 codex 任务数**：0（决策前不写代码）

---

## I-1 `[DESIGN]` `[HIGH]` AuthCache/AuthTokenCache 进程内（多实例不同步）

- **来源**：CX14 (#1)
- **文件**：`proxy/cache.go:31-38` + `middleware/user_guard.go:43-53` + `proxy/stream.go:452-471` + `controller/user.go:249, 732`
- **失效**：多实例下 A 封禁/删号/旋转 token，B 不知道直到 SyncCacheConfig 周期刷新
- **决策**：
  - 选项 A：永久单实例部署 + 文档明确
  - 选项 B：Redis pub/sub 集群 invalidation + token_version 校验

## I-2 `[DESIGN]` `[MEDIUM]` ChannelMapCache/RouteCache/SysConfigCache 多实例不一致

- **来源**：CX14 (#9)
- **文件**：`proxy/cache.go:26-38` + `main.go:33-34` + `controller/sysconfig.go:394`
- **决策**：与 I-1 一并决策

## I-3 `[DESIGN]` `[MEDIUM]` OAuth PKCE state sync.Map（LB 路由失败）

- **来源**：CX14 (#10)
- **文件**：`controller/oauth.go:40-46, 128-133, 136-149`
- **失效**：LB 把 callback 路由到不同实例 → 合法 OAuth 失败
- **决策**：
  - LB sticky session（按 cookie hash 路由）
  - 或 state/verifier 存 Redis/DB

## I-4 `[DESIGN]` `[MEDIUM]` SMS 验证码/冷却/IP 限流进程内

- **来源**：CX14 (#11)
- **文件**：`controller/sms.go:61-67, 219-228, 240-260, 167-181`
- **失效**：多实例下 A 发码 B 验证失败；冷却+5/h 限流按实例数线性放大 → 短信轰炸
- **决策**：Redis TTL key + INCR/SETNX/DEL

## I-5 `[DESIGN]` `[MEDIUM]` CreditsPool 每实例都启动（CPA 调用放大 N 倍）

- **来源**：CX14 (#12)
- **文件**：`main.go:43` + `proxy/credits_pool.go:158-197, 459-474, 507-515`
- **失效**：多实例下 CPA 调用×N，触发上游限流；last_seen_at 竞争刷新
- **决策**：DB/Redis 锁保护全量刷新；或只 leader 写 DB

## I-6 `[DESIGN]` `[MEDIUM]` DistributedLock SQLite 多机失效

- **来源**：AGT4 (M SQLite 多机)
- **文件**：`database/distributed_lock_helper.go` + `controller/cliproxy_usage_sync.go`
- **失效**：SQLite 文件锁仅对共享同一文件的进程有效；不同机不同 DB 文件 = 锁形同虚设
- **决策**：与 I-1 一并决策；若多实例则需迁移 PG

## I-7 `[DESIGN]` `[LOW]` 通知偏好缓存跨实例 10min TTL 不一致

- **来源**：CX14 (#13) + AGT4 关联
- **文件**：`proxy/notification_pref_cache.go:27, 51-53, 114-125` + `controller/notification_preference.go:58-64`
- **决策**：与 I-1 一并决策

## I-8 `[DESIGN]` `[MEDIUM]` DistributedLock TTL=60s zombie window

- **来源**：AGT4 (M TTL)
- **文件**：`controller/cliproxy_usage_sync.go:27-28`
- **问题**：单机部署基本无影响；多实例下持有者崩溃后最长 60s zombie
- **决策**：TTL 调小 + renew 调快（30s TTL + 10s renew）

## I-9 `[BUG]` `[MEDIUM]` keepCLIProxyUsageSyncLockAlive goroutine 未 wait

- **来源**：AGT4 (M goroutine wait)
- **文件**：`controller/cliproxy_usage_sync.go:210-246`
- **问题**：`close(done)` 不等待 renew goroutine 真正退出
- **失效**：日志噪声（不影响正确性）
- **修复**：done 关闭后加 `wg.Wait()`
- **状态**：`pending`（这条是 BUG 不是 DESIGN，可独立修）

---

# 主题 J：DB schema 小补漏

**批次目标**：索引补齐、字段命名规范、FK 约束。

**预计 codex 任务数**：1 个（小补漏，可与 主题 H 合并）

---

## J-1 `[BUG]` `[MEDIUM]` Session 表无 (user_id, revoked_at) 复合索引

- **来源**：AGT1 (NOTE-4 → MEDIUM)
- **文件**：`database/session_schema.go` + sqlite.go
- **问题**：RevokeSessionsForUser `WHERE user_id=? AND revoked_at IS NULL` 走 user_id 单列索引另条件全扫
- **修复**：补 partial index `WHERE revoked_at IS NULL`
- **状态**：`pending`

---

# 主题 K：隐私 / 日志 / 死代码 `[DEBT]`

**批次目标**：杂项收口。可累积到下个 sprint 一次性处理。

**预计 codex 任务数**：0-1 个

---

## K-1 `[DEBT]` `[MEDIUM]` `User_+phone[-4:]` 默认用户名泄露手机尾号

- **来源**：AGT1 (M4)
- **文件**：`controller/oauth.go:763`
- **修复**：随机后缀替代

## K-2 `[DEBT]` `[MEDIUM]` SMS 验证顺序问题（先消费 SMS 再验 tmp_token）

- **来源**：AGT1 (M5)
- **文件**：`controller/oauth.go:718-727`
- **修复**：交换顺序，先验 tmp_token 后验 SMS

## K-3 `[DEBT]` `[LOW]` SIGNUP-COUPON 日志打印 coupon code

- **来源**：AGT1 (L1)
- **文件**：`controller/oauth.go:303`
- **修复**：掩码或仅 log template_id+user_id

## K-4 `[DEBT]` `[LOW]` LocalhostOnly "localhost" 字符串死代码

- **来源**：AGT1 (L2)
- **文件**：`middleware/localhost.go:9-12`
- **修复**：删除或注释说明

## K-5 `[DEBT]` `[LOW]` GodLogin 日志带 username 长度不限

- **来源**：AGT1 (L3)
- **文件**：`controller/admin_auth.go:116-118`
- **修复**：req.Username/Password 长度截断（128 字节）

## K-6 `[DEBT]` `[MEDIUM]` CORS AllowOrigins 硬编码 localhost http + 生产域名

- **来源**：AGT1 (M3 + NOTE-3)
- **文件**：`main.go:96`
- **修复**：环境变量注入

## K-7 `[DESIGN]` `[NOTE]` tmp_token AES-GCM 无状态设计风险

- **来源**：AGT1 (NOTE-1)
- **决策**：上线前是否改为有状态 PASETO + nonce 注册

## K-8 `[DESIGN]` `[NOTE]` admin user.Token / sessionID 混用

- **来源**：AGT1 (NOTE-2)
- **决策**：单 admin 多设备策略是否需要 multi-session？当前是强制 single-session

## K-9 `[DESIGN]` `[NOTE]` api_cost_usd snapshot 展示用 float64

- **来源**：AGT3 (NOTE)
- **说明**：仅展示层，不在金融关键路径，可保留

## K-10 `[DESIGN]` `[NOTE]` json.Marshal 每错误一次（性能）

- **来源**：AGT4 (NOTE)
- **修复**：预定义 []byte 常量

---

# 五、修复路线图（推荐）

## P0 第 1 批（用户已认可的紧急修复）

| 主题 | 范围 | codex | 预估 |
|------|------|-------|------|
| A 金融精度全链 | A-1, A-2, A-3~A-10, A-15（15 项） | codex-A | 1 天 |
| B append-only | B-1（ApiLog 改造）+ B-4（CreatedAt） | codex-B | 1 天 |
| D 鉴权修正 | D-1, D-5（封禁撤 session）| codex-D1 | 0.5 天 |
| C 优惠券漏洞 | C-1, C-2（cost_floor 双修）| codex-C1 | 0.5 天 |

**并行**：codex-A 与 codex-B 文件不冲突可并行；codex-D1 与 codex-C1 同步并行；4 个并行 codex 1 天完成。

## P1 第 2 批

| 主题 | 范围 | 预估 |
|------|------|------|
| E 熔断器分类重写 | E-1, E-2, E-3, E-4, E-5, E-6 | 0.5 天 |
| D 鉴权剩余 | D-2, D-3, D-4, D-6, D-7 | 1 天 |
| C 业务校验剩余 | C-3, C-4, C-5 | 0.5 天 |
| F SSRF 加固 | F-1, F-2, F-3, F-4 | 1 天 |
| G Webhook 加固 | G-1, G-2 | 0.5 天 |

## P2 第 3 批

| 主题 | 范围 |
|------|------|
| H 缓存/状态机/失效 | H-1 ~ H-13 |
| J Schema 补漏 | J-1 + 其他 |
| 其他 LOW / DEBT | A-11~A-14, B-7, E-7~E-8, H-14~H-17, K-3~K-6 |

## 需要决策（不动手）

**主题 I 单实例假设**：用户拍板"永久单实例"还是"支持多实例（需要 Redis）"。决策结果决定 I-1 ~ I-8 是 `wontfix` 还是 `pending` 大改造。

**主题 K NOTE**：tmp_token PASETO 改造 / admin 单 vs 多 session / CORS 环境变量。

**主题 G NOTE**：webhook GET vs POST / IP 白名单默认空 fail-open / nonce 用 sha256 vs sign 前 16。

---

# 六、统计验证

```bash
# 验证本文档完整性
grep -c "^## [A-K]-[0-9]" .codex-audit/sprint5-post-audit-findings.md
# 应输出 93（93 个独立 finding）

# 验证状态分布
grep -c "^- \*\*状态\*\*：\`pending\`" .codex-audit/sprint5-post-audit-findings.md
# 当前 = ~70（剩余 ~23 是 NOTE 或 DESIGN 类无状态字段）
```

---

# 七、后续工作流

1. **现在（已完成）**：本文档入库 baseline
2. **下一步（待用户）**：用户 review 主题 I 单实例决策 + 主题 K NOTE 决策
3. **再下一步**：按 P0 第 1 批派 4 个并行 codex 修复
4. **修完后**：每个 finding 状态改 `done` 并附 commit hash
5. **下次审计**：用 baseline + done 列表做 diff，证伪/证实"全修完"承诺

---

**最后**：所有 finding 都附 file:line + 失效场景 + 修复建议。任何工程师（人或 codex）拿到本文档都能从 baseline 起步，不需要回放对话历史。这是项目长期资产。
