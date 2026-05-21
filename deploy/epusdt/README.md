# epusdt Sidecar 部署指南

DAOF-CPA Web3 USDT 充值通道的 sidecar 部署 sample。配合 PaymentProvider 抽象层
（W-1 / W-3 已完成），admin 配齐 SysConfig 后即激活 USDT 多链收款。

## 前提

- Docker + Docker Compose 已安装
- 至少 1 个 USDT 收款地址（建议每条链一个 watch-only 地址，私钥保管在你的硬件钱包 / 冷钱包，不进入 epusdt 服务器）
- 链上 RPC 节点（默认公开节点够用；高频场景建议买 Alchemy / Infura）

## 5 步部署

### 1. 准备配置

```bash
cd D:/project/DAOF-CPA/deploy/epusdt
cp .env.example .env
# 编辑 .env：设 install_token、链 RPC、（可选）MySQL 凭据
```

### 2. 启动 sidecar

```bash
docker compose up -d
docker compose logs -f epusdt   # 看初次启动日志
```

健康检查地址：`http://localhost:8000/health`

### 3. epusdt 自身 admin 引导

浏览器访问 `http://localhost:8000/install?token=<.env 里的 install_token>`：
- 设置 admin 用户名 + 强密码
- 配置收款钱包地址（4 条链，每条链一个 watch-only 地址）：
  - TRC20:   `T...`（你的 TRON 地址）
  - ERC20:   `0x...`（你的 Ethereum 地址）
  - BEP20:   `0x...`（你的 BSC 地址，可与 ERC20 同地址）
  - Polygon: `0x...`（你的 Polygon 地址，可与 ERC20 同地址）
- 启用对应链（admin → 链管理）
- 创建商户 API Key → 拿到 `pid` 和 `secret_key`
- 引导完成后立刻把 `.env` 里的 `install=false`（或删除 install_token）

### 4. DAOF SysConfig 配置

DAOF admin 后台 → SysConfig → 新增 4 个 key：

| Key | 值 |
|-----|----|
| `epusdt_endpoint` | `http://localhost:8000`（或容器互访地址） |
| `epusdt_pid` | epusdt admin 后台分配的 PID |
| `epusdt_secret_key` | 对应的 secret_key |
| `epusdt_enabled_chains` | `tron,ethereum,bsc,polygon`（按实际启用的链）|

配置即生效（无需重启 DAOF），下次 `/api/topup/options` 即返回 `epusdt` provider。

### 5. 真实充值 dry-run

- DAOF 前端 `/topup`（或直接 POST `/api/topup/create` 带 `{provider:"epusdt",amount_fen:1000,method:"trc20-usdt"}`）
- 前端按返回的 `pay_info`（含 `receive_address` + `actual_amount`）显示二维码 / 钱包地址
- 用真实 USDT 转账（金额必须精确到 0.0001 USDT，否则 epusdt 锁单失败）
- 链上确认（TRC20 ~1 分钟）→ epusdt 推 POST `/api/payment/notify/epusdt` 到 DAOF
- DAOF 验签 + 入账 → user.quota +=

## 故障排查

| 症状 | 排查 |
|------|------|
| `/api/payment/notify/epusdt` 返 `provider_unknown` | epusdt provider 未注册（不应发生，init 会强制注册）；查 DAOF 启动日志 |
| 返 `not_configured` | DAOF SysConfig 4 项未配齐；admin 后台补 |
| 返 `sign_invalid` | secret_key 不一致；DAOF / epusdt 两侧重新对齐 |
| 返 `pid_mismatch` | pid 不一致；同上 |
| 返 `amount_mismatch` | 用户转的金额 ≠ epusdt 返回的 actual_amount；不应发生，epusdt 内部锁单已防 |
| 链上确认到了但 DAOF 没入账 | 查 epusdt admin → 订单详情 → callback 状态；若 `callback failed`，看 DAOF webhook 日志 |
| DAOF 收到 callback 但没入账 | 查 PaymentWebhookReceipt 表（rejected_pid / rejected_timestamp 等）+ DAOF stdout 日志 |

## 安全清单

- [x] epusdt 8000 端口仅绑定 localhost，不暴露公网
- [x] epusdt admin 后台强密码 + 限 IP 访问
- [x] DAOF 配的 epusdt_endpoint 是私网/loopback（防 SSRF 泄漏到公网网关）
- [x] epusdt 钱包用 watch-only 地址（私钥在用户冷钱包，不进 epusdt 服务器）
- [x] DAOF 验签：所有 webhook 走 MD5(sorted_params + secret_key) + pid 双校验防跨商户重放
- [x] DAOF IP allowlist：默认仅允许 `127.0.0.1/32, ::1/128` —— SysConfig `epusdt_notify_allowed_cidrs` 可调
- [ ] 公测：admin 后台 USDT 入口对中国 IP 隐藏（DAOF 侧 IP geo 过滤）
- [ ] 定期审计 PaymentWebhookReceipt 表，异常 reject 模式预警

## 与 DAOF 通信架构

```
┌────────────────┐
│  用户钱包       │  转 USDT
│ (硬件 / 软件)   │ ──────────► [链上节点 (TRC20/ERC20/BSC/Polygon)]
└────────────────┘                          │
                                            ▼
                                  ┌────────────────────┐
                                  │   epusdt sidecar    │
                                  │   (本地 Docker)     │
                                  └──┬──────────────┬───┘
                  POST JSON (验签 MD5)│              │ 链上轮询监听
                                     ▼              │
                          ┌──────────────────┐      │
                          │   DAOF-CPA       │◄─────┘ 实时 Transfer 事件
                          │  /api/payment/   │
                          │  notify/epusdt   │
                          │  (W-3-P3 通用入账)│
                          └──────────────────┘
                                     │
                                     ▼
                          [TopupOrder.status: created → paid]
                          [User.quota += micro_usd]
                          [BillingEntry insert]
```
