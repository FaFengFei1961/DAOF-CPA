// 首页加载 + 仪表盘
import { test, expect } from '@playwright/test';
import { waitForReady } from './helpers.js';

test.describe('Homepage', () => {
  test('loads with dashboard structure and login prompt', async ({ page }) => {
    await page.goto('/');
    await waitForReady(page);

    // 检查未登录态的横幅提示
    const loginPrompt = page.getByText(/登录后可查看/);
    await expect(loginPrompt).toBeVisible();

    // 检查是否有页面主体渲染了
    const mainContent = page.locator('main#main-content');
    await expect(mainContent).toBeVisible();
  });
});
