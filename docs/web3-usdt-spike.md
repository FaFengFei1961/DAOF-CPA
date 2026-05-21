# Web3 USDT 支付集成 Spike

**状态：已立项**（2026-05-21）。架构方案选定 epusdt sidecar，W-1 PaymentProvider 抽象进入实施。

> 决策落地见 §6。本文档不含代码；具体实现进度跟踪在 task #145+（W-1 ~ W-5）。

## 1. 背景与目标

DAOF-CPA 当前充值通道**仅一家**：易付通 (yifut) RSA V2，收 CNY 转入账 micro_usd。

随着海外用户增长，需要支持 **Web3 稳定币** 充值：
- 用户群：海外开发者 / 跨境 / 加密原生用户
- 主要币种：**USDT**（绝对主流），其次 USDC
- 主要链：**TRC20**（手续费 ~$1，亚洲市场主流） / **BSC BEP20**（~$0.3） / **ERC20**（$5–20，溢出场景）
- 次要：Polygon / Solana / Arbitrum

约束：
1. **不持币**（non-custodial）—— 不做 KYT/AML 资质，钱包私钥本地化
2. 与 DAOF 现有 `TopupOrder` / `BillingEntry` / `PaymentWebhookReceipt` 审计链一致
3. 货币内部仍 int64 micro_usd（USDT/USDC ≈ 1:1 USD，折价单独处理）
4. 公测期无后向兼容包袱（按项目准则）

## 2. DAOF 现有支付链路画像

详见 `code-explorer` 报告，要点：

| 维度 | 现状 |
|------|------|
| 支付网关 | 只 yifut 一家，hardcoded |
| `PaymentProvider` 抽象 | **没有**（OAuthProvider 模式没复制到支付侧） |
| `TopupOrder.Provider` 字段 | **没有**（只有 yifut-internal `pay_type`） |
| `PaymentWebhookReceipt` | 已有，`(provider, nonce)` 联合 unique，append-only — **可直接复用** |
| 货币换算 | `topup_money.go` 内 fen↔micro_usd 整数算 + 汇率 SysConfig — **可扩展** |
| 异步入账 | webhook → 单事务（status CAS + User.Quota/PaidQuota += + BillingEntry）— **可复用** |
| 安全防御 | CIDR allowlist + RSA sig + timestamp ±300s + nonce 去重 — **可复用大部分** |
| 离线/补单 | 已有两个入口 `AdminCreateOfflineTopup` / `AdminMarkTopupPaid` |

**关键缺口**：缺一个 `PaymentProvider` interface。Web3 集成必须先做这一步抽象。

## 3. 方案对比

### A. epusdt sidecar（推荐）

[GMwalletApp/epusdt](https://github.com/GMwalletApp/epusdt) — 开源 Go 实现的自托管 Web3 收款网关。

**支持矩阵**：
- TRC20: USDT / TRX
- ERC20: USDT / USDC / ETH
- BEP20 (BSC): USDT / USDC / BNB
- Polygon: USDT / USDC
- Solana: USDT / USDC

**部署**：单二进制零依赖（低并发不用 MySQL/Redis），Docker compose 友好。3.5k stars / 180 commits，2026-05-20 刚发 v1.0.0，活跃维护。

**协议**：
- 出站 HTTP API 创建订单（admin 后台填 wallet 地址 + 密钥）
- 回调 webhook，"EPay 兼容"协议格式（与 OneAPI / NewAPI / V2Board / Dujiaoka 同一套）
- 入账验证机制：**地址 + 金额组合 unique** —— 同一钱包地址 + 同一金额在 10 分钟内只能锁给一个订单。若占用则金额自动 +0.0001 试下一个组合（最多 100 次）

**优点**：
- 协议成熟（生态 7+ 个 panel 用同一套）
- 不要写链上代码（epusdt 内置节点连接 / 余额轮询）
- 钱包私钥不进 DAOF 进程
- 多链统一接口
- 失败回滚有"超时未支付自动释放"机制

**缺点**：
- 多一个进程 / Docker 实例运维（运维成本中等）
- "地址 + 金额组合"机制有"金额精确性"限制（用户必须发指定金额到几位小数，不友好但行业标准）
- 单点（如果 epusdt 挂了，新订单建不了，但已建订单的入账不受影响）

### B. 自集成（直接接链上节点）

DAOF 自己接 TronGrid / Infura / Alchemy / Solana RPC，监听 USDT 合约 Transfer 事件，对账。

**优点**：架构上最干净，无第三方
**缺点**：
- 工作量 5-10 倍（每条链一份监听器 + 重组保护 + 漏块重扫）
- 钱包私钥进 DAOF 主进程，blast radius 极大
- Tron / Solana 等都有自己的 SDK 学习曲线
- 上线后维护成本持续高

**判定**：除非有专门加密资产团队，否则不建议。

### C. 第三方聚合（Coinbase Commerce / NowPayments / CryptoCloud）

**优点**：合规漂亮（拿到 KYC/KYT，对企业身份友好），费率清晰（1-2%）
**缺点**：
- **托管**（资金先经平台中转，有提现 KYC 要求）
- 对海外用户友好，但对国内中转商等灰色业务**不友好**
- 平台政策风险（账户被冻 / 提现门槛）
- 月费 / 提现费叠加可能比 epusdt 长期更贵

**判定**：若 DAOF 业务定位是合规海外平台可选 C；若是开发者 / 跨境工具偏好不持币方案选 A。

### 决策矩阵

| 维度 | A. epusdt | B. 自集成 | C. 第三方 |
|------|-----------|----------|----------|
| 工作量（首期） | **中** | 高 | 低 |
| 持续维护 | 中 | **高** | 低 |
| 资金托管 | 否 | 否 | **是** |
| 私钥风险 | sidecar 持有 | DAOF 持有 | 平台持有 |
| 费率 | **0%** | gas only | 1–2% + 平台费 |
| 多链覆盖 | 5+ 链 | 看实现 | 看平台 |
| 合规友好度 | 中 | 中 | **高** |
| 推荐 | ★★★★★ | ★★ | ★★★（特定场景） |

**结论：选 A（epusdt sidecar）**。理由：与 DAOF 当前"开发者工具 / 非托管"定位匹配，工作量可控，私钥隔离在独立进程。

## 4. 推荐方案：DAOF + epusdt 集成

### 架构图

```
┌─────────────────┐  ┌──────────────────┐
│    用户         │  │   epusdt sidecar │
│  (前端 React)   │  │   (独立 Docker)   │
└────────┬────────┘  └────────▲─────────┘
         │ POST /api/topup/create     │
         ▼                            │ HTTP API
┌─────────────────┐  HTTP POST 创建订单 │
│   DAOF-CPA      ├──────────────────►│
│   (Go / Fiber)  │                   │
│                 ◄──────────────────┤ 回调 webhook
│  PaymentProvider│  钱包地址 + 金额    │ "EPay-compat" 签名
│  abstraction    │                   │
└────────┬────────┘                   │
         │                            │
         │ ┌─────────────┐            │
         └►│ TRC20 链 ───┤            │
           │ ERC20 链 ───┤◄───────────┘
           │ BSC 链  ───┤  epusdt 内置
           │ ...        │  链上轮询 / 节点
           └────────────┘
```

### DAOF 侧改造（4 个 phase）

#### Phase W-1：`PaymentProvider` 抽象（重构 yifut 当 reference impl）

```go
// 设计草图，非最终代码
type PaymentProvider interface {
    Key() string                                          // "yifut" / "epusdt"
    IsConfigured() bool
    CreateOrder(ctx, order *PaymentOrderRequest) (*PaymentOrderResult, error)
    VerifyWebhook(headers, body []byte) (*WebhookEvent, error)
    PublicMetadata() PaymentProviderMetadata               // 前端渲染按钮用
}

// 注册表（仿 OAuthProvider）
RegisterPaymentProvider(yifut)   // 老 yifut adapter
RegisterPaymentProvider(epusdt)  // 新加
```

- `controller/topup.go` 改 `CreateTopup` 按 `provider` 参数路由到 registry
- `controller/topup_webhook.go` 拆出 yifut-specific 部分到 `payment_provider_yifut.go`
- `database/topup_schema.go` 新增 `TopupOrder.Provider` 字段 + migration（默认 "yifut" 兼容历史数据）

**工作量**：~600 行（含测试）。**不带功能新增**——纯重构，老 yifut 行为不变。

#### Phase W-2：epusdt sidecar 部署 + smoke test

- `docker-compose.yml` 加 `epusdt` 服务（公开 8000 端口仅给 DAOF 内网）
- 配置一个 TRC20 测试钱包（小额）
- 跑 1 个真实 $0.10 USDT 充值流程（不接 DAOF）

**这一步不动 DAOF 代码**，纯运维 + 验证 epusdt 自身能跑通。

#### Phase W-3：DAOF epusdt adapter

- `controller/payment_provider_epusdt.go` 实现 PaymentProvider 4 个方法
- `proxy/epusdt_client.go` HTTP client（带 `oauthHTTPClient` 同款 SSRF 防护）
- 复用 `PaymentWebhookReceipt` nonce 表（provider="epusdt"）
- USDT/USDC → USD 换算：直接 1:1（不引入 oracle 价格源；币价偏差由 admin 在 SysConfig `usdt_to_usd_micros` 静态配置，默认 1_000_000 = 1:1）
- 单事务入账（与 yifut 完全一致的链路）

**工作量**：~400 行（含测试）。

#### Phase W-4：admin UI + 用户充值 UI

- `controller/topup.go` `GetTopupOptions` 返回 provider list（前端按可选 provider 渲染按钮）
- 前端 `Topup.jsx` 增加"Web3 USDT 充值"卡片，提示用户：扫码 / 复制地址 + 精确金额发送
- admin `system/PaymentConfig.jsx` 增加 epusdt 配置区（webhook secret / endpoint URL / 启用开关 / 支持链勾选）

**工作量**：~300 行（前端 + i18n）。

#### Phase W-5：上线 + 监控

- Admin 后台先以"内测"标志开启（仅 admin 自己可见 Web3 入口）
- 监控指标：epusdt sidecar 健康 / 失败订单率 / 链上确认平均时长
- 完整真实金额（$10）走一次再开公开

**总工作量估算**：~1300 行 + 1 个 epusdt sidecar 部署，预计 2-3 周（含测试 + 真实小额 dry-run）。

## 5. 风险与未知

### 合规风险

- 中国境内 Web3 支付**法律灰色**。建议明确目标用户为海外，不在 admin UI 暴露给中国 IP（用 LanGuard 同款 IP 段判定屏蔽）
- 充值入账要求用户**自负其责**：DAOF 不做 KYC/KYT，不能保证收款合法性
- USDT 本身是 Tether 发行的稳定币，合规态度跟 BTC 不同——某些司法辖区按虚拟货币禁止，某些按证券规管

### 技术风险

- **USDT 折价**：99% 时间 ≈$1，但崩盘 / FUD 时可能跌到 $0.95。DAOF 当前 `usdt_to_usd_micros` 静态配置策略**接受折价风险**——admin 可在异常时段调低。oracle 接入是未来可选项。
- **用户错链**：用户把 ERC20 USDT 误发到 TRC20 地址 → 资金永久丢失。epusdt 标准做法是地址按链类型分配，前端必须**强制显示链类型 + 精确小数金额**，但教育成本必然存在。
- **钱包私钥**：epusdt 持有，DAOF 不持有。但 sidecar 被攻破 = 钱被取走。建议：钱包每日热提冷（保留 < 1 周收款总额在热钱包）。
- **链上确认延迟**：TRC20 ~1 min；ERC20 拥堵时 5-30 min。用户体验"扫码后等"成预期。
- **EPay 协议成熟度**：epusdt 本身实现的 EPay 协议变体可能与 yifut V2 RSA **完全不兼容**，所以无法复用 yifut 签名 helper，必须新写。

### 业务风险

- 单进程 SPOF（epusdt 挂了不能新建订单，但已建订单不受影响）
- 退款流程复杂（链上交易不可逆 → admin 必须手工链上发回 → DAOF 走 `TopupRefund` 表登记）
- 客服成本：用户错链 / 金额发错 / 链上拥堵等问题需要 admin 介入

## 6. 决策记录（2026-05-21 用户拍板）

| 问题 | 决策 | 备注 |
|------|------|------|
| 目标用户区域 | **海外用户 only** | 不做法律审查；admin UI 不暴露给中国 IP |
| 支持链 | **TRC20 + ERC20 + BEP20 + Polygon** 一期全开 | epusdt 默认支持 |
| USDT → USD 换算 | **1:1 直接入账** | DAOF 内部 micro_usd ≡ 1 USDT；平台承担脱锚风险 |
| 钱包模式 | **静态地址 + 金额尾数**（epusdt 默认） | **DAOF 进程内不存任何私钥** |

### 已收集

| 链 | 收款地址 |
|----|---------|
| TRC20 (TRON) | `TMBjEGgFAPMt6DxDPKqcxsAQvWMAua8gHk`（mainnet） |
| ERC20 | **待补** |
| BEP20 (BSC) | **待补** |
| Polygon | **待补** |

### USDT 脱锚风险声明（用户已知情）

DAOF 选择 1:1 入账策略，即：用户充 100 USDT → 平台余额 +100 USD（=100_000_000 micro_usd）。

- **正常时段**（99% 时间）：USDT ≈ $1，方案无损
- **极端事件**：USDT 历史最低 $0.92（2022 LUNA 崩盘，3 天回归 $0.99）；USDC 历史最低 $0.87（2023 SVB，1 周恢复）
- **风险承担**：脱锚期间充值进来的 USDT 由 DAOF 平台吃损失

**缓解措施**（W-3 实现）：
- SysConfig `usdt_topup_emergency_pause` 急停开关（admin 可手动暂停 USDT 充值入口）
- SysConfig `usdt_topup_min_peg_micros`（默认 970_000 = $0.97）— 未来接 oracle 后可自动急停（一期手动）

### 私钥管理（重要澄清）

epusdt 标准模式是**静态地址 + 金额尾数**（用户付款金额做 0.0001 USDT 级别的小数后缀避免冲突）。

**结论：DAOF 任何进程都不需要持有私钥**。
- epusdt sidecar 只配 **watch-only** 收款地址 → 链上 RPC 监听到对应金额 → 回调 DAOF webhook
- 用户的 USDT 直接进**主收款钱包**（用户自己掌控的硬件钱包 / 冷钱包）
- 不存在"归集"步骤（不归集就不需要私钥）

私钥风险面 = 0（前提是 epusdt 不接受归集 / 提现指令）。

### W-2 启动前阻塞项（需补齐）

1. ERC20 / BEP20 / Polygon 收款地址（3 个，可以是同一个 EVM 地址，3 链同一私钥派生）
2. epusdt sidecar 部署位置（同机 Docker / 独立小机器？）
3. epusdt admin 后台口令（首次部署设）

### W-1 不阻塞 — 可立即启动

PaymentProvider 抽象层是纯代码重构，不接触任何链上 / 钱包配置，与 W-2 完全解耦。本 spike 完成后**即刻进入 W-1**。

---

## 附录 A：参考资源

- [GMwalletApp/epusdt](https://github.com/GMwalletApp/epusdt) — 主仓库
- [epusdt-docs](https://github.com/GMwalletApp/epusdt-docs) — 集成文档
- 易支付（EPay）协议：OneAPI / NewAPI / V2Board / Dujiaoka 用同一套

## 附录 B：DAOF 当前 yifut 集成的关键文件

详见 code-explorer 报告。简版：
- `controller/topup.go` / `topup_webhook.go` / `topup_admin.go` / `topup_money.go`
- `proxy/yifut_client.go` / `yifut_signer.go`
- `database/topup_schema.go` / `payment_webhook_schema.go` / `billing_schema.go`
