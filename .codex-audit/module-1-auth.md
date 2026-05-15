# Codex 审计任务：模块 1 — 认证 & 用户 & 权限

## 角色
你是 daof-ai-hub 项目的资深安全审计员，使用 codex 进行 0 偏差精审。任务严格、目标是发现真实风险。

## 强制约束（最重要）
- 项目未上线，处于重构期 → **禁止任何向后兼容代码 / alias / deprecated shim / 兼容 mapping**
- 旧逻辑残留 = **P0 必删**（直接覆盖、删除，不留 alias）
- 收紧攻击面 > 兼容旧调用方
- 用户身份与会话接口必须达到金融级安全标准

## 审查文件范围（精确扫描）
- D:/project/one-api/daof-ai-hub/controller/admin_auth.go
- D:/project/one-api/daof-ai-hub/controller/oauth.go
- D:/project/one-api/daof-ai-hub/controller/sms.go
- D:/project/one-api/daof-ai-hub/controller/user.go
- D:/project/one-api/daof-ai-hub/middleware/admin_guard.go
- D:/project/one-api/daof-ai-hub/middleware/csrf_guard.go
- D:/project/one-api/daof-ai-hub/middleware/lan_guard.go
- D:/project/one-api/daof-ai-hub/middleware/setup_guard.go
- D:/project/one-api/daof-ai-hub/middleware/user_guard.go

## 审查维度（10 项，每项 0-10 分）
1. **Session/Cookie 安全** - SameSite(Lax/Strict) / HttpOnly / Secure / Domain / Path / 过期合理
2. **CSRF 防护完整性** - 所有状态变更接口（POST/PUT/DELETE/PATCH）必须验证 token，包括管理端
3. **XSS 输入过滤** - 用户输入存储/回显前是否净化（nickname/avatar_url/about/email/phone）
4. **SMS 速率限制** - 单 IP / 单手机号 / 全局，防滥发；冷却时间合理；过期 OTP 清理
5. **OAuth state CSRF & PKCE** - state 一次性、绑定会话；code_challenge/verifier 流程完整
6. **密码哈希强度 & 凭证存储** - bcrypt cost ≥ 10 / Argon2id；不明文记录；不日志输出
7. **权限越权检测** - 横向（同级用户互访）/ 纵向（普通用户调管理端）；admin guard 是否真正校验
8. **暴力破解防护** - 登录/SMS/支付等敏感口失败次数限制 + 渐进延迟 + 账号锁定
9. **会话固定 & 劫持** - 登录后是否重新生成 token；登出是否真正失效；并发会话策略
10. **敏感信息泄漏** - 错误响应不含 stack trace / SQL / token；日志不含密码/手机号明文

## 输出格式（结构化，便于 review）

### 对每个文件
- **摘要**（1 段，说明文件职责 + 总体安全状态）
- **10 维评分**（每项格式：`维度名: X/10 — 1 句话理由`）
- **P0 issue**（必须立即修复）：行号 + 当前代码片段 + 修复方案 + 风险描述
- **P1 issue**（高优先级）：同上格式
- **P2 issue**（中优先级）：同上格式
- **GO / NO-GO 判定**：GO（可上线）/ NO-GO（必须修复后才能上线）

### 模块总评
- **模块总分**：X/100（10 维相加）
- **高优先级修复清单**（按风险/影响排序）
- **推荐 next action**（如：阻塞上线 / 修完 P0 即可 / 仅观察）

## 特别关注（不要遗漏）
- LAN guard 是否仅靠 IP 判定即可绕过？（X-Forwarded-For 注入）
- Setup guard 是否能被未授权用户触发重新初始化？
- admin 接口是否存在 IDOR（id 直接传参）？
- SMS 验证码长度/熵是否足够？是否短时间内可枚举？
- OAuth 回调 redirect_uri 是否严格校验白名单？
- user_guard 中 user_id 是否从 session 取，而非 query/body？

请按上述格式输出，使用中文。不要省略任何 P0/P1/P2 issue。
