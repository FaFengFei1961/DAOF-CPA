# daof-cpa 代码约定

## 1. 审计表 INSERT-only 强制规范

### 1.1 已确立的审计表清单

下列表是审计性质，**只允许 INSERT，禁止 UPDATE / DELETE**：

- `OperationLog`
- `TopupRefund`
- `PaymentWebhookReceipt`
- `BillingEntry`
- `BillingReconciliation`
- `ApiLog`（仅原始事实；可变信息走 `ApiLogAttribution` / `ApiLogCostEstimate` side table）

### 1.2 禁止模式

#### 禁止：GORM `Updates(map[string]any)` 用于审计表

GORM v2 已知行为：`<-:create` tag **只对 struct 型 Update 生效**，对 `Updates(map[string]any{...})` 完全无效。
任何对审计表的 `Updates(map{...})` 都能绕过保护字段。

**不允许：**
```go
// 错误！绕过 <-:create 保护
DB.Model(&OperationLog{}).Where("id = ?", id).Updates(map[string]any{
    "action": "modified",
})
```

#### 禁止：raw SQL UPDATE / DELETE 触及审计表

**不允许：**
```go
DB.Exec("UPDATE billing_entries SET amount_usd = ? WHERE id = ?", newAmount, id)
DB.Exec("DELETE FROM api_logs WHERE created_at < ?", cutoff)
```

#### 禁止：`Unscoped().Delete()` 触及审计表

**不允许：**
```go
DB.Unscoped().Delete(&database.ApiLog{}, "user_id = ?", userID)
```

### 1.3 允许模式

只允许：
```go
// 允许：INSERT
DB.Create(&entry)

// 允许：基于 struct 的 Save / Update（<-:create 字段会被自动跳过）
entry.Status = "new"
DB.Save(&entry)  // <-:create 字段不变

// 允许：基于 struct 的 Updates
DB.Model(&entry).Updates(database.BillingEntry{
    Status: "new",
})  // <-:create 字段不变
```

### 1.4 数据归档/清理

审计表数据增长时**不能用 DELETE 清理**。改用：
- 归档到独立表（如 `archived_api_logs`）
- 导出到 S3/文件 + DELETE（必须有专用归档工具）
- 永远保留（成本可接受时）

当前 `proxy/subscription_cron.go` 的 ApiLog 周期清理**已关闭**（Sprint 5 P0-β）。

### 1.5 CI 检查（待加）

未来 CI 应加 grep 检查：
```bash
grep -rn 'Updates(map\[string\]' controller/ database/ | grep -iE "OperationLog|TopupRefund|PaymentWebhookReceipt|BillingEntry|BillingReconciliation|ApiLog"
```
应该 0 命中。

## 2. 金额单位约定

### 2.1 统一单位

- **金额（USD）**：int64 micro_usd（USD × 1e6）
- **金额（RMB）**：int64 fen（RMB × 100）
- **价格（per token）**：int64 pico_per_token（USD × 1e15 / token）
- **汇率**：int64 RmbPerUsdMicros（RMB × 1e6 / USD）

### 2.2 禁止模式

- 禁止 `float64` 出现在金额计算路径
- 禁止 `math.Round/Floor/Ceil` 操作金额
- 禁止 SQL 层 `CAST(value AS REAL)` 做金额转换
- 禁止 `strconv.ParseFloat` 解析金额字符串

### 2.3 边界情况

- 比例计算用 `big.Int` 或 `big.Rat`（避免 int64 溢出）
- ceil-div 用 `(a + b - 1) / b` 整数公式
- 展示层若必须返回 float JSON，用 string 替代（前端解析）

## 3. message_code 规范

### 3.1 必须字面量字符串

`message_code` 值必须是**单个完整字面量字符串**。

**不允许：**
```go
return fiber.Map{"message_code": "ERR_" + "FOO"}  // 绕过 AST 扫描
return fiber.Map{"message_code": strings.Join([]string{"ERR", "FOO"}, "_")}
const Code = "ERR" + "_FOO"  // 也不允许
```

**允许：**
```go
return fiber.Map{"message_code": "ERR_FOO"}
const Code = "ERR_FOO"
```

### 3.2 双语 i18n 强制

任何新 message_code 必须在 `i18n/zh-CN.json` 和 `i18n/en-US.json` 都补齐。
`TestBackendMessageCodesCoveredByLocales` 通过 AST 扫描验证。

## 4. 并发约束

### 4.1 进程内状态（单实例假设）

以下状态依赖**单实例部署**：
- `proxy.AuthCache` / `proxy.AuthTokenCache`
- `proxy.ChannelMapCache` / `proxy.RouteCache` / `proxy.SysConfigCache`
- OAuth `oauthStateStore` (sync.Map)
- SMS `smsCodeCache` / `smsCooldown` / `smsIPRate`
- tmp_token `tmpTokenConsumedStore`
- CreditsPool 全局状态

若未来要支持多实例水平扩展，需要迁移到 Redis 共享状态。当前**永久单实例部署**是明确架构决策。

### 4.2 goroutine 生命周期

所有 `go func()` 必须有清晰退出路径，使用 `context.Context` + `sync.WaitGroup`。
后台 goroutine 在主程序退出/重启时应优雅终止。

## 5. SSRF 防御

### 5.1 出站 HTTP client 必须

- 使用 `proxy.SafeTransport()` 注入 `DialContext`
- 设置 `CheckRedirect: redirectGuard` 每跳重新校验

### 5.2 已确立的 denylist

- 云元数据：169.254.169.254 / 168.63.129.16 (Azure) / 100.100.100.200 (Aliyun) / 169.254.0.23 (Tencent)
- 内网保留：RFC1918 / Loopback / Link-local / IPv6 ULA / CGNAT / 6to4 / Teredo
- 详见 `proxy/url_safety.go` 的 `denyPrefixes`

新增云元数据 IP 时必须同步更新此清单。
