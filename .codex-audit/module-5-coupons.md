# Codex 审计任务：模块 5 — 优惠券 & 发券 & 应用

## 角色
你是 daof-ai-hub 项目的资深业务安全审计员，使用 codex 进行 0 偏差精审。重点关注套利、并发、事务一致性。

## 强制约束
- 项目未上线 → 禁止任何向后兼容 / alias
- 旧逻辑残留 = P0 必删
- 金融级精度：优惠券面额 int64 micro_usd
- 批量发券必须事务原子

## 审查文件范围
- D:/project/one-api/daof-ai-hub/controller/coupon.go
- D:/project/one-api/daof-ai-hub/database/coupon_schema.go

## 审查维度（10 项，0-10 分）
1. **fixed_price 防套利** - 折扣后金额不能 < 上游真实成本；不能让用户无限套利
2. **批量发券事务原子性** - 1000 张券要么全发要么全失败；中途失败回滚干净
3. **并发使用竞态** - 同一张券两个请求同时核销；CAS / SELECT FOR UPDATE
4. **优惠券码生成安全** - 不可预测（crypto/rand）；熵 ≥ 64 bit；不能被枚举
5. **过期时间检查** - 服务器时钟为准；客户端时间不可信；时区一致
6. **使用次数限制** - per-user / per-coupon / global 三重限制
7. **用户绑定校验** - 私券必须校验 user_id 与 cookie 一致；不能跨用户使用
8. **重复使用防护** - 已使用券不能再次核销；状态机 unused → used 不可逆
9. **SQL 注入** - 所有查询使用参数化；prefix LIKE 查询转义
10. **审计日志** - 发券/核销/作废都有日志；包含 admin_id / user_id / coupon_code / before_after

## 输出格式（同模块 1）

## 特别关注
- 批量发券接口（前端 BulkGrant）是否有上限保护？防止管理员误操作发 100 万张
- coupon code 重复时数据库唯一约束是否真的生效？
- 优惠券应用顺序：折扣前 vs 折扣后扣余额？
- 与订阅/充值组合时金额计算是否 0 偏差？
- 过期券是否会被定时清理？数据膨胀风险
- 是否存在"复活已用券"的接口？

请按上述格式输出，中文。
