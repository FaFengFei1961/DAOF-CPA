# Codex 5：OAuth PKCE + Session 表 + Logout 服务端吊销（最大型）

你是 daof-ai-hub 项目的资深全栈安全工程师，使用 codex 实现具体代码。

## 项目硬约束
- 项目**未上线**，处于重构期 → **禁止任何向后兼容 / alias / shim**
- 旧逻辑残留 = P0 必删
- 收紧攻击面 > 兼容旧调用方
- 所有金额 int64 micro_usd

## 上下文
codex 模块 1 审计 P0/P1：
- OAuth state CSRF & PKCE: 3/10 — 无 PKCE / 无服务端一次性 state / 无后端 redirect_uri allowlist
- Session/Cookie 安全 / 会话固定 & 劫持 — admin login 轮转，**用户 login 不轮转**，logout **不吊销**
- AdminLogout 服务端**不**真正吊销 token

## 实施需求

### 1. Session 表 (database/session_schema.go 新建)
```go
// UserSession 用户会话。Bearer token 失效查这个表，不再依赖"持有 token 字符串"作为唯一凭证。
//
// fix CRITICAL Sprint5-M1：原 user.Token 是长期不变的 API key（设计为 SDK 凭证），
// 但用户浏览器 session 也复用同 token → 浏览器关闭后 token 仍可被滥用，logout 不能撤销。
// 新设计：浏览器 session 独立 token + 服务端 session 表，logout 即时撤销。
type UserSession struct {
    ID         uint      `gorm:"primaryKey"`
    UserID     uint      `gorm:"<-:create;index;not null"`
    SessionID  string    `gorm:"<-:create;uniqueIndex;not null;size:64"` // crypto/rand 32 bytes hex
    UserAgent  string    `gorm:"<-:create;size:255"`                     // 审计：何种客户端
    IPAddress  string    `gorm:"<-:create;size:64"`                      // 审计：登录 IP
    CreatedAt  time.Time `gorm:"<-:create;index"`
    LastUsedAt time.Time `gorm:"index"`                                  // 每次 auth 校验更新
    ExpiresAt  time.Time `gorm:"<-:create;index"`                        // 超过此时间视作失效
    RevokedAt  *time.Time `gorm:"index"`                                 // logout 时设置；非 nil = 已吊销
}
```

### 2. OAuth state + PKCE (controller/oauth.go)
- `PrepareOAuthState` (GET /api/auth/github/prepare) 改造：
  - 服务端生成 `code_verifier` (crypto/rand 64 bytes base64url) + 计算 `code_challenge=SHA256(verifier)` (base64url)
  - 服务端生成 `state` (32 bytes hex)
  - 把 `state → code_verifier` 存到内存 map（5min TTL，crypto-safe sync.Map + 自动过期）
  - 返回给前端：`state`, `code_challenge`, `code_challenge_method=S256`
  - 前端把这些拼到 GitHub OAuth URL 跳转
- `GithubCallback` 改造：
  - 从 query 拿 `code` + `state`
  - 查内存 map：用 state 找 verifier → 找到删除（一次性）+ 校验未过期
  - 找不到 → 403 ERR_OAUTH_STATE_INVALID
  - 调 GitHub token endpoint 带 `code_verifier` 完成 PKCE
- redirect_uri 严格白名单：服务端不再接受任意 redirect_uri，使用 server_address + 固定路径

### 3. Session token 生成 + 吊销
- `createUserSession(userID, ua, ip)` helper：
  - 生成新 SessionID (crypto/rand 32 bytes hex)
  - INSERT UserSession (ExpiresAt = now + 7 days)
  - 返回 SessionID（前端存 localStorage）
- `revokeSessionByID(sessionID)` helper：UPDATE RevokedAt=now
- `LookupUserBySession(sessionID) (*User, bool)`：
  - SELECT UserSession WHERE session_id=? AND revoked_at IS NULL AND expires_at > now
  - 如果存在 → SELECT User by user_id → 返回；同时 UPDATE LastUsedAt=now
  - 否则 返回 nil, false

### 4. UserGuard 改造 (middleware/user_guard.go)
旧实现：直接 LookupUserByToken (`AuthCache[token]`)
新实现：
- 优先尝试 SessionID 路径（前端浏览器走这个）
- 如果 token 看起来是 SessionID 格式（64 hex chars，无前缀）→ 调 LookupUserBySession
- 如果看起来是 API key (sk-daof-* 前缀)→ 走老的 LookupUserByToken
- 两种都失败 → 401

### 5. Login / Logout endpoint
- `GithubCallback` 成功后 createUserSession → 返回 SessionID 给前端
- 新增 `POST /api/auth/logout` (UserGuard, 拿到当前 sessionID):
  - 解析 Authorization header
  - 如果是 SessionID 格式 → revokeSessionByID
  - 写 OperationLog (action_type="USER_LOGOUT")
  - 返回 200

### 6. AdminLogout 真正吊销 (controller/admin_auth.go)
- 旧实现：只清前端 cookie
- 新实现：admin token 也用 UserSession 模式（或者新增 admin_sessions 表 — 你决定）
- 关键：DB 标记 RevokedAt，下次请求即时拒

### 7. 前端 (ui/src/components/AuthModal.jsx + AuthContext.jsx + App.jsx)
- OAuth flow 走新的 prepare endpoint，拿 code_challenge 后跳转 GitHub
- 登录回调拿到 sessionID 后 localStorage 存（而不是 user.token）
- 加 logout 按钮调 POST /api/auth/logout

### 8. 测试
- TestPrepareOAuthState_ReturnsChallenge + state 一次性
- TestGithubCallback_RejectsReusedState
- TestGithubCallback_RejectsMismatchedVerifier (PKCE 攻击)
- TestUserSession_LookupAfterRevokeFails
- TestUserSession_ExpiredSessionFails
- TestLogout_RevokesSession (登录 → 调 endpoint → 验证下次请求 401)

## 文件白名单
- `database/session_schema.go` (新建)
- `database/session_helper.go` (新建：CRUD + helper)
- `database/session_test.go` (新建)
- `controller/oauth.go` (PKCE + state + session 集成)
- `controller/oauth_test.go` (扩展)
- `controller/auth_logout.go` (新建 - logout endpoint)
- `controller/admin_auth.go` (logout 真吊销)
- `middleware/user_guard.go` (session lookup 路径)
- `ui/src/components/AuthModal.jsx` (PKCE prepare + 存 sessionID)
- `ui/src/context/AuthContext.jsx` (logout 调 endpoint)
- `ui/src/App.jsx` (如果需要)

## 文件黑名单
- `main.go` — 不动；列出需补的路由 (`/api/auth/logout`)
- `database/sqlite.go` — 不动；列出需补的 AutoMigrate (`&UserSession{}`)
- `i18n/*.json` — 列出需补的 message keys 给主进程

## 提交要求
- `git add` 白名单文件
- commit message 标题：`feat: Sprint 5 M1 OAuth PKCE + Session 表 + Logout 真吊销`
- commit body 列出：
  - schema 新增
  - PKCE 流程
  - Session lifecycle
  - 测试覆盖
  - 主进程需补的：route + AutoMigrate + i18n

## 测试
- `go test ./controller/ -run "TestPrepareOAuth|TestGithubCallback|TestUserSession|TestLogout" -count=1 -v` 必须通过
- `go test ./database/ -run "Session" -count=1 -v` 必须通过
- `go build ./...` 必须通过
- 不能破坏现有测试
- 老的 AdminGuard / 老的 Bearer API key 路径 **仍要工作**（仅浏览器 session 走新逻辑）

完成后输出简短中文报告：改文件 / 测试结果 / 主进程需补 route + migrate + i18n。
