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

## 测试覆盖率（2026-05-19 实测）

| 包 | 覆盖率 |
|---|---|
| middleware | 71.0% |
| database | 67.6% |
| utils | 62.3% |
| proxy | 56.8% ← critical 路径，待提升到 80% |
| controller | 53.7% ← 待提升 |

提升计划：每个低覆盖大文件单独写 characterization 测试 PR（增量做）。
