# Codex 审计任务：模块 2 — AI 网关代理 & 路由 & 重试

## 角色
你是 daof-ai-hub 项目的资深后端/并发审计员，使用 codex 进行 0 偏差精审。重点关注高并发、流式、热路径风险。

## 强制约束
- 项目未上线 → 禁止任何向后兼容 / alias / deprecated shim
- 旧逻辑残留 = P0 必删
- 金融级精度：金额必须 int64 micro_usd（USD × 1e6）；价格必须 int64 pico_per_token（USD × 1e15）
- big.Int 用于 0 偏差成本计算

## 审查文件范围
- D:/project/one-api/daof-ai-hub/proxy/stream.go
- D:/project/one-api/daof-ai-hub/proxy/cache.go
- D:/project/one-api/daof-ai-hub/proxy/channel_types.go
- D:/project/one-api/daof-ai-hub/proxy/billing_rules.go
- D:/project/one-api/daof-ai-hub/controller/channel.go
- D:/project/one-api/daof-ai-hub/controller/channel_model.go

## 审查维度（10 项，0-10 分）
1. **Channel failover 正确性** - 主备切换条件、不连环 retry、避免 thundering herd
2. **Retry 策略** - 指数退避 + jitter；幂等性保护；超过上限明确 fail
3. **热路径并发安全** - sync.Mutex/atomic 使用是否正确；channel cache 读写锁；map 并发访问
4. **SSE 流式断连处理** - 客户端断开后服务端是否清理 goroutine / 计费是否正确（部分消费 vs 全额）
5. **Channel 健康检查** - 主动探活 vs 被动失败计数；恢复策略；半开状态
6. **模型路由权重 & 灰度** - 多 channel 时权重分配；A/B 测试支持
7. **Context timeout 传递** - 上游 context.Context 是否一直传递到底层 HTTP/SSE；不被 ctx.Background 截断
8. **Goroutine 泄漏** - SSE / WebSocket / 长连接是否在所有路径 defer 关闭；select 是否处理 ctx.Done
9. **Cache 一致性** - 写入后失效策略；并发写入 race；TTL 与 channel 配置变更同步
10. **请求/响应头部安全** - 不泄漏内部头（X-Channel-ID / X-Internal-*）；不转发危险头（Host / X-Forwarded-*）

## 输出格式（同模块 1）
对每个文件：摘要 + 10 维评分 + P0/P1/P2 issue（行号+代码+修复+风险）+ GO/NO-GO
模块总评：总分 X/100 + 修复清单 + next action

## 特别关注
- proxy/stream.go 中 SSE 错误处理路径：客户端关闭、上游断流、超时三种情况是否都正确扣费
- billing_rules.go 中 checkedCostMicroUSD 是否真的 0 偏差（big.Int 使用规范）
- channel cache 重载时是否有 race window 让请求路由到失效 channel
- channel_model.go 是否仍有 float64 价格字段残留？应全部为 *_pico_per_token int64
- retry 是否会让一次失败请求扣多次费？
- ctx 是否在长 SSE 流中保持 alive？

请按上述格式输出，中文，覆盖所有发现。
