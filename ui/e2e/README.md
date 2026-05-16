# E2E Tests (Playwright)

端到端测试覆盖 daof-cpa 主干用户路径。

## 安装

```bash
cd ui
npm i -D @playwright/test
npx playwright install chromium
```

## 准备测试环境

1. 启动后端：`go run main.go` (默认 :8080)
2. （首次）记录默认 admin token：启动日志中 `🔑 默认管理员账户 [root] 创建成功` 后，从 `daofa-hub.db` 读取或经 `/api/admin/secret-login` 拿到。
3. 把 token 设为环境变量 `ADMIN_TOKEN=sk-daof-root-xxxx`

## 运行

```bash
# 全部
BASE_URL=http://localhost:8080 ADMIN_TOKEN=$TOKEN npx playwright test

# 单个
npx playwright test e2e/login.spec.js

# UI 调试模式
npx playwright test --ui
```

## 测试范围

| 文件 | 覆盖 |
|------|------|
| `login.spec.js` | 管理员秘钥登录、错误密钥拒绝 |
| `homepage.spec.js` | 首页加载、HeroBanner 轮播、模型网格渲染 |
| `subscription.spec.js` | 订阅页加载、列表渲染（依赖 admin 已建套餐） |
| `theme.spec.js` | 浅色/深色切换持久化、MD3 CSS 变量切换 |

## 注意

- 测试假设是干净 DB；不要在生产环境跑。
- `admin` 路由全部需要 `daof_admin_token` cookie；helpers 自动注入。
- 截图/trace 失败时存于 `playwright-report/`。
