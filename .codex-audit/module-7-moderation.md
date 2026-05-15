# Codex 审计任务：模块 7 — 内容审核 moderation

## 角色
你是 daof-ai-hub 项目的资深内容安全审计员，使用 codex 进行 0 偏差精审。重点关注误判、绕过、性能、可追溯。

## 强制约束
- 项目未上线 → 禁止任何向后兼容 / alias
- 旧逻辑残留 = P0 必删
- 审核结果必须可追溯（user/timestamp/snapshot）
- 自动封禁必须有撤销机制

## 审查文件范围
- D:/project/one-api/daof-ai-hub/proxy/content_moderation.go
- D:/project/one-api/daof-ai-hub/proxy/moderation_risk.go
- D:/project/one-api/daof-ai-hub/controller/moderation.go

## 审查维度（10 项，0-10 分）
1. **关键词扫描准确性** - 词库覆盖；中文分词；语境识别；不命中 substring 假阳性
2. **HMAC cache 安全** - cache key 用 HMAC 防猜测；secret 不泄漏；TTL 合理
3. **autoban 阈值合理性** - 不会误封正常用户；累计窗口；冷却期
4. **误判 (false positive) 控制** - 测试样本覆盖；申诉路径；管理员人工复核接口
5. **绕过攻击防护** - Unicode 同形字 / 全角半角 / Base64 / URL encode / 零宽空格
6. **并发扫描性能** - 大文本不阻塞；goroutine pool；超时退出
7. **风险评分一致性** - 多维度加权；分数稳定（同输入同分）；阈值可配
8. **用户申诉机制** - 申诉接口存在；管理员审批流；撤销封禁
9. **审计可追溯性** - 每次审核命中都有日志（user_id / content_hash / matched_rule / score / action）
10. **大文本性能** - 长 prompt / 流式响应分块扫描；不超时；不耗尽内存

## 输出格式（同模块 1）

## 特别关注
- 是否有 admin override 能跳过审核？是否被滥用？
- 审核失败时是否 fail-open（放过）还是 fail-closed（拒绝）？金融级应 fail-closed
- 流式响应是否在生成中扫描？还是仅最终内容？
- 用户被封禁时是否清晰告知原因？还是只 403 不解释
- 词库更新是否需要重启？热加载安全性
- HMAC secret 是否从环境变量读取？不硬编码

请按上述格式输出，中文。
