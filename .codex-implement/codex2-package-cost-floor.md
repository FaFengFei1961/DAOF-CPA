# Codex 2：Package.cost_floor 字段 + fixed_price 真正与套餐成本绑定

你是 daof-ai-hub 项目的资深全栈工程师，使用 codex 实现具体代码。

## 项目硬约束
- 项目**未上线**，处于重构期 → **禁止任何向后兼容代码 / alias / shim**
- 旧逻辑残留 = P0 必删
- 收紧攻击面 > 兼容旧调用方
- 所有金额 int64 micro_usd；价格 int64 pico_per_token；杜绝 float64 出现在金额计算

## 上下文
Sprint 3 M5 P0-2 临时修复了 fixed_price 不能 < 10000 micro_usd（0.01 USD）。
但 audit 真正要求是：**fixed_price 必须 ≥ 套餐真实成本下限**，避免 admin 配 $5 fixed_price
卖出 $50 价值的服务，亏 90%。

本任务实施完整闭环：
1. Package 表加 `cost_floor_micro_usd int64` 字段（admin 估算的套餐成本下限）
2. validateTemplate 加新校验：fixed_price 必须 ≥ max(applicable_packages.cost_floor)
3. Admin UI 在套餐编辑表单加 cost_floor 输入

## 实施需求

### 1. Schema (database/subscription_schema.go)
- Package 结构体加字段：
```go
// CostFloorMicroUSD 套餐"上游成本下限"估算（micro_usd），admin 在套餐编辑时填。
// 用于 coupon fixed_price 校验：fixed_price 不得低于本字段，防 admin 配低价券亏损。
// 0 = 未配置（fixed_price 校验跳过该套餐）。
CostFloorMicroUSD int64 `gorm:"default:0" json:"cost_floor_micro_usd"`
```

### 2. 套餐 admin endpoint (controller/package_admin.go)
- createPackagePayload / 类似 update payload 加 `CostFloorMicroUSD int64 json:"cost_floor_micro_usd"`
- 校验：`>= 0` 且 `<= price_amount`（成本不能高于售价，否则永远亏损）
- 创建 / 更新时把字段写入 Package

### 3. Coupon validateTemplate (controller/coupon.go)
- 在现有 `validateTemplate` 函数加新逻辑：
  - 如果 fixed_price 且 PackageIDs 非空 → 解析 IDs → 查所有 Package.CostFloorMicroUSD → 取 max
  - 如果 fixed_price < maxCostFloor → 拒绝（返回明确错误说明哪个套餐成本下限是多少）
  - 如果 PackageIDs 为空（全套餐券）→ 取所有套餐 cost_floor 的 max
  - 如果套餐 cost_floor=0（未配置）→ 跳过该套餐校验（向前兼容 admin 还没填）
- 配套测试

### 4. 前端 (ui/src/components/PackageManagement.jsx)
- 编辑表单加 cost_floor 输入框（USD 元为单位显示，提交时 × 1e6 转 micro）
- hint 文案："admin 估算的套餐上游真实成本下限。配置后系统会防止 admin 创建低于此值的 fixed_price 优惠券，避免亏损。0 = 不限制（仅由全局 couponMinFixedPriceMicroUSD 兜底）"
- 显示已配置值（micro / 1e6 → "$X.XX"）

### 5. 测试（controller/coupon_test.go 扩展）
- TestValidateTemplate_FixedPriceBelowCostFloorRejected
  - seed 一个 Package CostFloorMicroUSD=2_000_000 ($2)
  - 试图创建 fixed_price=$1 + PackageIDs=[该套餐] → 应拒绝
- TestValidateTemplate_FixedPriceAboveCostFloorAccepted
  - fixed_price=$3 + 同套餐 → 应通过
- TestValidateTemplate_CostFloorZeroSkipsCheck
  - Package CostFloorMicroUSD=0 → 跳过该校验（其他限制如最低 10000 仍生效）
- TestValidateTemplate_MultiPackageTakesMaxCostFloor
  - 2 套餐 cost_floor=$2 和 $5 → fixed_price=$3 应拒绝（不到 $5），$5 应通过

## 文件白名单
- `database/subscription_schema.go` (Package 加字段)
- `controller/package_admin.go` (DTO + 校验 + 写入)
- `controller/package_admin_test.go` (新测试，如果文件存在则修改，不存在创建)
- `controller/coupon.go` (validateTemplate 加 cost floor 校验)
- `controller/coupon_test.go` (扩展测试)
- `ui/src/components/PackageManagement.jsx` (admin 表单加 cost_floor 输入)

## 文件黑名单
- `main.go` — 不动
- `database/sqlite.go` — 不动（Package 已在 AutoMigrate 内，加字段会自动 migrate）
- `i18n/zh-CN.json` / `i18n/en-US.json` — 列出需要的 message_code 给主进程
  - 至少需要：ERR_COUPON_FIXED_PRICE_BELOW_PACKAGE_COST_FLOOR
  - ERR_PACKAGE_COST_FLOOR_INVALID

## 提交要求
- `git add` 白名单文件
- commit message 标题：`feat: Sprint 5 M5 P0-2-extended Package.cost_floor 字段 + coupon 真实成本下限校验`
- commit body 列出：
  - schema 变更（auto-migrate）
  - validateTemplate 新逻辑
  - 4+ 个新测试
  - 主进程需补的 i18n keys

## 测试
- `go test ./controller/ -run "TestValidateTemplate" -count=1 -v` 必须通过
- `go build ./...` 必须通过
- 不能破坏现有测试

完成后输出简短中文报告：改文件 / 测试结果 / 主进程需补 i18n。
