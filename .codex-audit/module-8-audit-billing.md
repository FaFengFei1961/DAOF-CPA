# Codex 审计任务：模块 8 — 审计 & 账单事实表 & 操作日志

## 角色
你是 daof-ai-hub 项目的资深数据/审计架构师，使用 codex 进行 0 偏差精审。重点关注审计完整性、防篡改、查询性能。

## 强制约束
- 项目未上线 → 禁止任何向后兼容 / alias
- 旧逻辑残留 = P0 必删
- 所有金额 int64 micro_usd；所有价格 int64 pico_per_token
- OperationLog 一旦写入不可修改

## 审查文件范围
- D:/project/one-api/daof-ai-hub/database/billing_helper.go
- D:/project/one-api/daof-ai-hub/database/billing_schema.go
- D:/project/one-api/daof-ai-hub/controller/operation.go

## 审查维度（10 项，0-10 分）
1. **OperationLog 完整性** - 所有关键操作（充值/退款/订阅/扣费/封禁）必须落日志；不漏字段
2. **审计字段防篡改** - created_at 不可写；updated_at 由触发器/ORM 控制；admin 不能修改 historical
3. **查询性能** - 索引覆盖常用 query（user_id + created_at / operation_type）；不全表扫描
4. **时间戳精度一致** - UTC 存储；millisecond 精度；不同表风格一致
5. **金额字段类型一致** - 全 int64；不混用 float64；CHECK 约束防负数（除退款）
6. **关联完整性** - FK 约束；删除 user 时审计日志是否保留（应保留，软删除）
7. **软删除处理** - 软删除字段统一；查询默认过滤；管理端可见 deleted
8. **数据保留策略** - 审计日志保留多久？归档？合规要求（金融至少 5 年）
9. **分页/导出性能** - keyset pagination 而非 OFFSET；大量数据导出不内存爆炸
10. **敏感字段脱敏** - 日志中手机号 / 邮箱 / token 是否脱敏；不明文存储

## 输出格式（同模块 1）

## 特别关注
- billing_helper.go 中 ChargeUserMicroUSD 是否真的原子？事务边界
- billing_schema.go 中表结构：BillingEvent 字段 type、metadata、reconcile_state
- pending_reconcile / upstream_unmetered / settled 三态转换是否完整
- OperationLog 是否被 admin 接口可修改？应禁止
- 大量 BillingEvent 查询是否拖慢主库？需读写分离 / 分区
- 历史数据迁移：旧表（float 字段）是否已 truncate / rename / drop？

请按上述格式输出，中文。
