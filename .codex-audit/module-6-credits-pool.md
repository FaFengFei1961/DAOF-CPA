# Codex 审计任务：模块 6 — 号池监控 & 凭证 & quota 拉取

## 角色
你是 daof-ai-hub 项目的资深运维/并发审计员，使用 codex 进行 0 偏差精审。重点关注 goroutine 安全、上游集成、缓存一致性。

## 强制约束
- 项目未上线 → 禁止任何向后兼容 / alias
- 旧逻辑残留 = P0 必删
- 上游 API 不可信，必须健壮处理 4xx/5xx
- 凭证（token / cookie）必须脱敏

## 审查文件范围
- D:/project/one-api/daof-ai-hub/proxy/credits_pool.go
- D:/project/one-api/daof-ai-hub/controller/credits_pool.go
- D:/project/one-api/daof-ai-hub/controller/cliproxy_usage_sync.go

## 审查维度（10 项，0-10 分）
1. **Goroutine 并发安全** - sync.RWMutex / atomic 使用是否正确；号池增删改不与读冲突
2. **上游 API 重试策略** - 401/403/429/5xx 分类处理；指数退避；不雪崩
3. **缓存失效一致性** - quota 数据 TTL 合理；写入后失效；不读到 stale 数据
4. **tier 解析鲁棒性** - 上游 JSON 结构变化时不 panic；fallback 到 normalizeGoogleTierBadge
5. **Token 刷新机制** - 过期前主动刷新；并发场景下不重复刷新（singleflight）
6. **401/403 处理** - 不立即标记账号失效；区分临时 vs 永久；通知管理员
7. **同步任务幂等性** - 重复触发 sync 不导致数据重复 / 状态错误
8. **内存泄漏** - 账号数据量随时间增长时不爆内存；定期清理离线账号
9. **错误降级策略** - 单账号失败不影响整体；上游不可达时降级到 cache；记录可观测
10. **监控指标完整性** - 成功率 / latency / 失败原因分类暴露；不丢异常

## 输出格式（同模块 1）

## 特别关注
- credits_pool.go 中 Claude/Antigravity/Gemini-CLI tier 拉取是否都有 timeout？
- normalizeGoogleTierBadge / pickGoogleCodeAssistTier 边界情况覆盖
- WINDOWS_AMD64 platform enum 是否硬编码（应支持其他平台）
- 上游账号增长时 map 是否会无限增长
- 凭证（cookie/refresh_token）是否在日志中泄漏？
- cliproxy_usage_sync 启动多个 goroutine 是否有 leader election / lock？

请按上述格式输出，中文。
