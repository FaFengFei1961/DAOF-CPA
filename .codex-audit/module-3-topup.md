# Codex 审计任务：模块 3 — 充值 & 支付 & 退款

## 角色
你是 daof-ai-hub 项目的资深支付安全审计员，使用 codex 进行 0 偏差精审。重点关注金额一致性、防重放、CAS 状态机。

## 强制约束
- 项目未上线 → 禁止任何向后兼容 / alias / deprecated
- 旧逻辑残留 = P0 必删
- 金融级精度：所有金额 int64 micro_usd；禁止 float64 出现在金额计算
- 支付回调必须签名验签 + 幂等

## 审查文件范围
- D:/project/one-api/daof-ai-hub/controller/topup.go
- D:/project/one-api/daof-ai-hub/database/topup_schema.go

## 审查维度（10 项，0-10 分）
1. **签名验签完整性** - 支付回调必须验签；签名算法（HMAC-SHA256+）；签名字段不可遗漏
2. **重复支付防护** - idempotency key；同一 order_id 不重复入账；事务级唯一约束
3. **CAS 状态机正确性** - pending → success/failed 单向迁移；不能从 success 回退；状态字段 + version
4. **金额精度** - 全链路 int64 micro_usd；上游回调 float64 必须立即转 int64 with rounding policy
5. **回调验证完整性** - 来源 IP 白名单 + 签名 + 时间戳防重放
6. **退款流程** - 退款幂等；不超过原订单金额；退款不能多次累加；冲账记录完整
7. **余额扣减事务一致性** - 充值入账 + 余额 UPDATE 必须在同一 DB tx；崩溃恢复
8. **并发支付竞态** - 同时发起两个充值不会双倍入账；SELECT FOR UPDATE 或 CAS
9. **审计日志完整** - 每笔充值/退款都有 OperationLog；包含 before/after 余额
10. **Webhook 防伪** - HTTPS only；签名时效（< 5 min）；nonce 不重用

## 输出格式（同模块 1）
对每个文件：摘要 + 10 维评分 + P0/P1/P2 issue（行号+代码+修复+风险）+ GO/NO-GO
模块总评：总分 X/100 + 修复清单 + next action

## 特别关注
- 回调接口是否暴露在 LAN guard 之外？是否依赖签名作为唯一防线？
- 充值金额 < 0 / overflow 校验
- 取消订单是否会让 pending 状态长期挂起？是否有 sweeper？
- 优惠券应用于充值时金额计算是否 0 偏差？
- 充值后立即扣费的并发场景（充值刚到账被高并发扣完）
- 退款是否影响订阅窗口/优惠券使用记录？

请按上述格式输出，中文。
