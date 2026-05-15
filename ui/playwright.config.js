// @ts-check
/**
 * Playwright 配置
 *
 * 运行：
 *   npm i -D @playwright/test
 *   npx playwright install chromium
 *   BASE_URL=http://localhost:8080 npx playwright test
 *
 * 默认假设后端 (Go) 在 :8080，前端 dist 已被后端 static 托管。
 */
import { defineConfig, devices } from '@playwright/test';

export default defineConfig({
  testDir: './e2e',
  timeout: 30 * 1000,
  expect: { timeout: 5000 },
  fullyParallel: false, // 共享数据库 → 串行
  retries: process.env.CI ? 1 : 0,
  workers: 1,
  reporter: [['list'], ['html', { open: 'never' }]],
  use: {
    baseURL: process.env.BASE_URL || 'http://localhost:3000',
    headless: true,
    screenshot: 'only-on-failure',
    trace: 'retain-on-failure',
    video: 'retain-on-failure',
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
});
