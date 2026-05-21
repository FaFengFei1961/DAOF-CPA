# DAOF-CPA 架构走读

DAOF-CPA 是一个**前台分发层 + 计费层**，把多家 LLM 厂商（OpenAI / xAI / Anthropic / Google）的能力统一暴露给客户端，认证、配额、计费、审计、内容审核全在此层完成。实际协议处理（OAuth refresh / Anthropic 兼容 / Codex WS session 状态）由本地 CPA（CLIProxyAPI）后台层负责。

```
客户端 → DAOF-CPA（鉴权 + 计费 + 审计 + 限流）→ CPA（协议层）→ 各模型厂商
```

## 路由总览（main.go）

| 协议 | 路径 | 处理器 |
|---|---|---|
| 文本 SSE/非流 | `POST /v1/chat/completions` `POST /v1/responses` `POST /v1/responses/compact` `POST /v1/completions` | `ChatCompletionProxyHandler` |
| 文本 WebSocket（Codex CLI/桌面端） | `GET /v1/responses` `GET /backend-api/codex/responses` | `ResponsesWebsocketProxyHandler` |
| Anthropic Messages | `ALL /v1/messages` `ALL /v1/messages/count_tokens`（+ `/v1/v1/...` 容错） | `ChatCompletionProxyHandler` |
| Gemini 原生 | `GET /v1beta/models` `GET/POST /v1beta/models/:modelAction` | `GeminiNativeProxyHandler` |
| 图像 | `POST /v1/images/generations` `POST /v1/images/edits` | `ImageGenerationProxyHandler` / `ImageEditProxyHandler` |
| 视频 | `POST /v1/videos/{generations,edits,extensions}` `GET /v1/videos/:request_id` | `VideoGenerationProxyHandler` / `VideoEditProxyHandler` / `VideoExtensionProxyHandler` / `VideoRetrieveProxyHandler` |
| Public model list | `GET /v1/models` | `controller.GetPublicModels` |

所有 LLM 入口经过 `llmIPCoarseLimiter`（IP 维度 5000/min）+ `llmProxyLimiter`（token 维度 600/min）双层限流。WebSocket 升级后另有 `wsClientFramesPerMinute=60` 单连接帧限流。

## 计费 pipeline（proxy/text_billing.go）

**核心入口**：`CommitTextTurn(ctx CommitTextContext, usage TextUsage, status int, deliveredBytes int64, errType, errMsg string) bool`

SSE 与 WebSocket **共用同一个入口**（P8 重构合并）。CommitTextContext 把原 ChatCompletionProxyHandler 闭包捕获的 15 个变量提到入参：

```go
type CommitTextContext struct {
    User, Token, SubToken, IsSubToken                 // 身份
    ModelName, Body, Path, ClientIP, StartTime, IsStream, FallbackUserOptIn
    SelectedPath, SelectedChan                         // 路由
    EngineDecision                                      // 预检
    UpstreamHeaders                                     // WS 路径传 nil
}
```

责任链（与原 `deductQuota` 闭包行为等价）：
1. token clamp 防御（cached ≤ prompt / cacheWrite 5m+1h 守恒 / reasoning ≤ completion）
2. `failedRequest`（status<200 || ≥400）跳过扣费，仅写 ApiLog.Cost=0
3. 计算 raw cost：ContextPriceThreshold 高低档切换 + claude 1.25× cacheWrite fallback
4. `ResolveBillingRules` 算 charged cost（modelWeight × healthMultiplier）
5. 写 ApiLog 主表 + ApiLogUsageLine（input/output token 各一行）
6. `Decide(IsPrecheck=false)` 决定订阅命中 vs fallback 余额
7. 订阅命中：写 `BillingTypeApiUsageSub` + `RecordApiLogRevenue(subscription)`
8. 订阅未命中且 BalanceConsumeEnabled：`commitTextBalanceTurn` 原子 CAS 扣余额
9. 订阅未命中且 BalanceConsumeEnabled=false：UNAUTHORIZED-FALLBACK pending_reconcile
10. 子 token UsedQuota 累加（balanceConsumed 守卫，CAS 失败不累加）

任何写失败 → pending_reconcile 兜底审计（永不让"已交付服务未结算"沉默）。

## 媒体生成（图像 / 视频 / Gemini）

**计费模式 3 选 1**（database.BillingMode*）：

| Mode | 来源 | 适用 |
|---|---|---|
| `token` | `usage.prompt_tokens` + `completion_tokens` | 文本 + gpt-image-2 + Gemini image（image-modality token via usageMetadata.candidatesTokenCount）|
| `image` | `len(candidates[].inlineData)` × per-image flat | Imagen 系列（无 usage 元数据）|
| `video_second` | xAI `usage.cost_in_usd_ticks` 优先 / 兜底按 duration × per-sec | xAI Grok Imagine 视频 |

**xAI cost_in_usd_ticks 协议**：单位 10⁻¹⁰ USD（1 USD = 10¹⁰ ticks），是 xAI 已应用所有折扣后的权威账单。DAOF 在 `costTicksFromImageResponse` / `costTicksFromMediaResponse` 中直接读取，向上取整除以 10000 转 micro_usd。

**默认 disabled 策略**：所有非传统媒体端点（images/edits / videos/edits / videos/extensions / /v1beta/models / /v1/responses/ws）seed 默认 `Supported=false` / `DefaultEnabled=false`，admin 必须在 ChannelModel.AllowedEndpoints 显式加端点 + 切 Supported=true 才能启用。

## 邮件系统（Phase G-1）

**核心**：SMTP 客户端 + 模板引擎 + 异步发送队列 + 用户邮箱绑定 API。

**架构**：
```
Admin 配置 SMTP（加密存储）→ EmailTemplate（模板 + i18n）
  ↓
User 触发（注册 / 验证邮箱 / 忘记密码）→ EmailQueue → 限流+幂等 → SMTP 发送
  ↓
审计：SMTP 配置 / Token 消费 / 发送失败记录
```

**关键组件**：

- `proxy/email_smtp.go` — SMTP 客户端（TLS 强制 465/587，SSRF 防护 via safeDialContext，凭据 AES-CBC 加密）
- `proxy/email_template.go` — 模板系统（bind/verify/reset/welcome 预定义，i18n 多语言）
- `proxy/email_queue.go` — 异步队列（per-target 限流、幂等通过 SourceRef hash）
- `database/email_verification_schema.go` — EmailVerification 表（append-only，token 单次消费）
- `database/user_schema.go` — User 新增 Email / EmailVerifiedAt / PasswordHash / EmailLoginEnabled 字段（bcrypt cost=12 prod/cost=4 test）
- `controller/admin_email.go` — Admin 侧 SMTP 配置 + 凭据加密解密 + feature toggle（SysConfig email_enabled）
- `controller/email_auth.go` — 邮箱注册 / 登录 / 忘记密码 / 重置密码（含 signup_bonus + partial unique index 兜底）

**关键不变量**：
- EmailVerification 仅 INSERT + 消费时 UPDATE ConsumedAt；其他修改拒绝（BeforeUpdate hook）
- SMTP password 绝不回显；admin 修改配置时 UI 展示密码掩码
- 发送失败 → 入队列重试，不轮询数据库（由 email_queue 定期扫 SendErrorCount < 5 的行）

## 邮箱+密码登录（Phase G-2）

**扩展 G-1**：在 User 表基础上加登录、注册、忘记密码全流程。

**API**（实际路由见 `main.go`）：

匿名（认证前）：
- `POST /api/auth/email/login` — 邮箱+密码登录
- `POST /api/auth/email/signup` — 邮箱+密码注册（发 verify 邮件 → 用户点链接 → `/api/user/email/verify` 消费 token）
- `POST /api/auth/email/forgot-password` — 忘记密码申请（发 reset_password token）
- `POST /api/auth/email/reset-password` — 新密码设置（token 消费 + bcrypt 更新）
- `POST /api/auth/email/set-password` — OAuth 用户首次启用 email-login（复用 reset 流程语义）

登录后（UserGuard + CSRFGuard）：
- `GET    /api/user/email` — 查当前邮箱绑定 / 验证状态
- `POST   /api/user/email/bind` — 绑定邮箱（发 verify 邮件）
- `POST   /api/user/email/verify` — 消费 verify token
- `POST   /api/user/email/resend-verification` — 重发验证邮件
- `DELETE /api/user/email` — 解绑邮箱
- `POST   /api/user/email/request-set-password` — OAuth 用户请求设密链接
- `PUT    /api/user/email-login-enabled` — 用户级开关 email-login

**关键文件**：
- `controller/email_signup.go`
- `controller/email_login.go`
- `controller/email_password_reset.go`
- `controller/email_set_password.go`

**关键不变量**：
- 注册路径（createUserWithSignupBonus）事务化：user + EmailVerification + signup_bonus + signup_coupon 四步原子（H-Audit C-1）
- 邮箱唯一约束：`users(email)` 有 partial unique index 限制已验证账号同邮箱唯一（兜底 409 + ERR_EMAIL_TAKEN）

## OAuth 多 provider 抽象（Phase H-1 ~ H-4）

**目标**：把第三方身份验证从 hardcoded GitHub 扩展到任意 provider（GitHub + Google + 预留扩展点）。

**架构**：
```
Provider Registry（init 时注册 GitHub / Google adapter）
  ↓ OAuthProvider interface
  ├─ oauth_provider_github.go（OIDC via github.com/apps）
  ├─ oauth_provider_google.go（OIDC via accounts.google.com）
  └─ [扩展点] oauth_provider_*.go
  ↓
controller/oauth.go（handler）→ tmp_token 生成 → 前端消费
  ↓
database/oauth_identities 表（append-only，soft-delete via unlinked_at）
```

**关键表**：
- `oauth_identities` — (provider, external_id, user_id, email, email_verified, unlinked_at)
  - Partial unique index：`uniq_oauth_identity_active(provider, external_id) WHERE unlinked_at IS NULL`
  - 审计：append-only with soft-delete（BeforeUpdate/BeforeDelete hook）

**关键 API**：
- `POST /api/auth/oauth/:provider/prepare` — 获 state + code_challenge（前端自行跳转 provider）
- `POST /api/auth/oauth/:provider/callback` — 回调处理（code → external_id → lookup/create user）

**关键文件**：
- `controller/oauth_provider.go` — Provider 接口 + 注册表 + sentinel errors（ErrOAuthCodeExpired / ErrOAuthProviderNotConfigured 等）
- `controller/oauth_provider_github.go` — GitHub 实现（H-Audit-3：申请 user:email scope，调 /user/emails 找 verified primary；找不到时 fail-soft 退回 EmailVerified=false）
- `controller/oauth_provider_google.go` — Google 实现（OIDC userinfo 格式 + scope: openid email profile）
- `database/oauth_identity_schema.go` — OAuthIdentity 模型 + append-only 约束

**tmp_token 格式**（8 段，H-Audit-2 扩展）：
```
(clean|sms)|provider|externalID|username|ref|email|verifiedFlag|timestamp
```

**关键不变量**：
- GitHub email 通过 /user/emails endpoint（user:email scope）拿 `primary=true && verified=true`；fail-soft：scope 未授 / API 5xx / 无 verified primary 时退回 EmailVerified=false（让 partial unique index 兜底；防 secondary public email 占位攻击）
- tmp_token 一次性消费（CompleteRisk / CompleteProfile 时消费，存 state 防 CSRF）
- Provider credential 轮换：ClientID / ClientSecret 从 SysConfig 读取（无硬编码）

## OAuth 账号 link-unlink（Phase H-5）

**场景**：已登录用户绑定 / 解绑第三方账号，跨 provider 邮箱防冲突。

**API**：
- `GET  /api/user/oauth/identities` — 列当前已绑定的 active providers（仅 provider + external_id + linked_at；H-Audit M4 起隐藏 email/username 减少 PII 泄漏）
- `POST /api/user/oauth/:provider/link/prepare` — 启动 link 流程（返 state + code_challenge；state 内嵌 `LinkUserID` 标识"已登录用户加新 provider"）
- `POST /api/user/oauth/:provider/unlink` — 软删（事务内 check + `SET unlinked_at = now`；TOCTOU 防御 H-Audit H-2）

**关键设计**：link 回调**复用同一个 `/api/auth/oauth/:provider/callback`**端点。`oauthStateRecord.LinkUserID != 0` 时 `OAuthCallback` 走 `finishOAuthLinkToExistingUser` 分支（绑到当前 user），否则走匿名 login/signup 分支。state 一次性消费保证不可混淆。

**关键文件**：
- `controller/oauth_identity_helpers.go` — 用户视角的 identity 查询 + 安全检查（至少保留一种 auth method）
- `controller/oauth.go` — 回调处理分支（linkMode vs loginMode）

**关键不变量**：
- 安全：至少保留一个 auth method（phone OR email+verified+password OR active identity）
  - unlink 时 check via `userHasOtherAuthMethodTx`（TOCTOU 事务化，H-Audit H-2）
- Email 冲突防御（H-6）：跨 provider 邮箱相同且都 verified 时，返 409 + ERR_OAUTH_EMAIL_TAKEN_LINK_REQUIRED，write 审计日志 OAUTH_EMAIL_COLLISION_BLOCKED

## 支付通道：PaymentProvider 抽象（Phase W 系列）

充值收款的多 provider 抽象。yifut（CNY 实时回调）+ epusdt（USDT 链上）双通道，
后者支持 **auto sidecar** 和 **manual 邮件确认**双模式（零部署也能上线 USDT）。

```
PaymentProvider interface (controller/payment_provider.go)
├── Key() string                                    // "yifut" / "epusdt"
├── IsConfigured() bool                              // admin 配齐凭据
├── CreateOrder(ctx, req) → (result, error)          // 下单
├── PublicOptions() PaymentProviderPublicOptions     // 给 /api/topup/options
└── ParseAndVerifyWebhook(input) → (event, error)    // 验签 + 解析（不查 DB）

可选 IPAllowlistedProvider interface（W-3 review M-2/M-7）
└── AllowedRemoteIPCIDRs() string                   // yifut + epusdt 实现
```

**通用 webhook 入口**：`ProcessPaymentWebhook(c, providerKey)`（controller/payment_webhook.go）
1. IP allowlist（按 provider 调 type assert）
2. provider.ParseAndVerifyWebhook → event
3. (provider, event.Nonce) 唯一约束防重放
4. SELECT order → 金额比对（fen vs micro_usd 双口径，按 event.AmountKind 分支）
5. 单事务：status 'created'→'paid' + quota+= + paid_quota+= + WriteBillingEntry

**Provider 一致性防御**：order.Provider != providerKey → 403，防攻击者拿 epusdt
合法 callback 投递到 /yifut 路由。

### Web3 USDT 双模式（W-4-Manual）

epusdt 内部按 `SysConfig epusdt_mode` 切换：

| 模式 | 工作流 | 部署 |
|---|---|---|
| **auto** | epusdt sidecar 监听链上 → POST webhook → DAOF 入账 | docker compose up，配 endpoint/pid/secret |
| **manual**（默认）| 订单创建时邮件通知 admin → admin 区块链浏览器验真 → admin 后台点"标记到账" | 0 部署，只配 admin 邮箱 + 钱包地址 |

manual 模式核心机制：
- **金额尾数**：`actual_amount_micro = AmountUSDMicro + (OrderID % 10000) * 100`（0.0001 USDT 步长，10000 个未决订单内不冲突）
- **邮件通知**：异步入 G-1 EmailQueue，DedupKey=`epusdt-manual:<order_id>`
- **入账复用**：admin 走 `POST /api/admin/topup/orders/:id/mark-paid`（provider-agnostic，原 yifut 应急补单入口）
- **不变量**：
  - SMTP 未配齐时 createOrderManual 直接 fail-closed（C-2：避免用户付款但 admin 永不知）
  - manual 模式 ParseAndVerifyWebhook 拒所有 webhook（H-2：返 405 ErrWebhookUnsupported 避免误判）
  - mark-paid 三层串行化（lockUserForUpdate → freshOrder 重读 → status CAS UPDATE）

**Admin in-product badge**（H-6）：FinancePage nav 30s polling `/api/admin/topup/pending-manual-count`，
积压订单 > 0 时显示红点 + 数字 + 悬停显示最早订单时长。不看邮箱也能感知积压。

**关键文件**：
- `controller/payment_provider.go` — 接口 + 错误 sentinel
- `controller/payment_provider_yifut.go` — yifut adapter
- `controller/payment_provider_epusdt.go` — epusdt adapter（auto + manual 分支）
- `controller/payment_webhook.go` — 通用 ProcessPaymentWebhook
- `controller/topup_admin.go` — AdminMarkTopupPaid（manual 入账复用）+ AdminGetPendingManualEpusdtCount
- `proxy/epusdt_client.go` — HTTP client + MD5 签名 + 钱包地址正则校验（ValidateEpusdtAddress）
- `deploy/epusdt/` — Docker 模板 + README + .env.example（auto 模式部署用）
- `docs/web3-usdt-spike.md` — 选型决策 + 实施进度记录

## WebSocket 桥接（proxy/responses_websocket.go）

Codex Responses WebSocket v2 协议透明代理：

```
client WS → DAOF (handshake: auth + channel pick + 余额检查)
         → gorilla/websocket Dial(wsUpstreamDialer with safeDialContext)
         → CPA upstream WS
client ⇄ DAOF: 双 goroutine pump，sniff upstream.response.completed → CommitTextTurn
```

防御：
- `safeDialContext`（防 DNS rebinding 到云元数据服务）
- `wsMaxSessionDuration=1h`（pumpCtx 超时）
- `wsReadIdleTimeout=5min`（每次 ReadMessage 前 SetReadDeadline）
- `wsClientFramesPerMinute=60`（sliding-window，超限发 rate_limit_exceeded 错误帧 + 关连接）
- `wsClientReadLimit=16MB`（单帧上限）

## Gemini 原生 API（proxy/gemini_native.go）

支持 `generateContent` / `streamGenerateContent` / `countTokens` / `:predict` 翻译（Imagen 通过 CPA `:predict → generateContent` 内部翻译后透明暴露）。

路由（S7 后参数化收紧攻击面）：
```
GET  /v1beta/models                              → 透传 CPA models list
GET  /v1beta/models/:modelAction                 → modelAction = "<model>:<method>"
POST /v1beta/models/:modelAction
```

防御：
- `parseGeminiNativeAction` 白名单 method（generateContent / streamGenerateContent / countTokens）
- `rejectGeminiNativeFileURIRefs` 拒绝 `fileData.fileUri`（防 fetch oracle）
- `url.PathEscape(modelName)` + `url.QueryEscape(alt)` 防上游 URL 注入
- 接通 `CanonicalRuntimeGeminiModel` DB 白名单（admin 必须先 Supported=true）

## Calibration（scripts/calibration/）

实际调用 vendor API 校准 seed 价格的工具：

- `run.py` — 跑 7+ 个 test case，比对 actual vs seed expect
- `README.md` — 用法 + DRIFT 处理流程 + 新 provider 接入步骤
- `00_summary.json` — 2026-05-19 首次 calibration 留档

**真实调用 > 文档**——xAI doc 字面理解被 calibration 推翻过（commit `8dd2712`：edits input 实际是 output 的 10-20%，不是同价）。新 vendor 上线前必须跑一次。

## 关键不变量

- **审计表 INSERT-only**：ApiLog / ApiLogUsageLine / BillingEntry / ApiLogRevenue / MediaGenerationJob 等通过 GORM `BeforeUpdate/BeforeDelete` 拒绝改删
- **守恒断言**：ΔQuota == Σbilling（`assertStreamConservation` 在多个 conservation test 里 enforced）
- **SSRF 防御**：HTTP 上游全走 `getTransport → safeTransport → safeDialContext`；WS 走 `wsUpstreamDialer.NetDialContext = safeDialContext`；URL 全部 query/path escape
- **凭据 sanitize**：`sanitizeError` 抹 Bearer / api_key / JWT / Cookie / URL secrets，所有 ApiLog.ErrorMessage 写入前必经
- **公测期无兼容包袱**：不写 backward-compat shim；旧协议 / 旧字段直接删

## 测试覆盖率（2026-05-21 实测）

| 包 | 覆盖率 | 趋势 vs 05-19 | 备注 |
|---|---|---|---|
| daof-cpa (main) | 1.0% | n/a | `main.go` 几乎只是路由注册 + lifecycle hook；e2e 框架级测试覆盖（非单测） |
| controller | 57.7% | +4.0% ↑ | G/H 系列加了测试；剩余空白主要在 admin handler |
| database | 67.0% | -0.6% | 持平 |
| middleware | 67.6% | -3.4% | 旧值有未提交本地代码污染；当前实测更准 |
| proxy | 64.6% | +7.8% ↑ | Phase F batch 1/2 + 媒体路径测试效果 |
| utils | 69.9% | +7.6% ↑ | 2026-05-21 补 `safe_dialer` + `clientip` SSRF / 防伪 IP 关键测试 |

**为何尚未到 80% 准则**：

剩余未覆盖区主要是两类，都是高成本低 ROI：

1. **Admin HTTP handler**（controller 内 ~70% 空白量）：`AdminListSubscriptions` / `BulkAdjustQuota` / `CreateTicket` 等需要完整 GORM mock + fiber app setup + admin session 上下文。每个写 5-10 个边缘 case 才有意义，每包性价比 ~50 行实现/300 行测试。
2. **守护型 cron / 后台 goroutine**（proxy 内 `email_queue.go` / `cliproxy_usage_sync.go`）：依赖 time-based 调度 + 外部 HTTP 调用，集成测试方为合适方式。

**已覆盖的关键路径**：

- 计费 pipeline（`text_billing.go` / `image_billing.go` / `video_billing.go`）
- 守恒不变量（`assertStreamConservation` 多场景）
- SSRF 防御（`utils/safe_dialer.go` + `proxy/url_safety.go` + WS dialer）
- OAuth 全套（H-1 ~ H-6 + H-Audit Tier 1-4 + L8 + H-Audit-2 全部带 stub provider 测试套）
- 邮件认证（G-1/G-2 全套：bind/verify/resend/unbind + signup/login/reset/set-password）
- 防伪 client IP（`utils/clientip.go` 8 个场景含 CF spoofing / X-Forwarded-For 拒绝信任）

后续增量提升：每个新功能 PR 自带测试，不再批量回填老 admin handler（投入产出比太低，e2e 框架级测试覆盖更合算）。
