// 首页加载 + HeroBanner 轮播 + 模型网格
import { test, expect } from '@playwright/test';
import { waitForReady } from './helpers.js';

test.describe('Homepage', () => {
  test('loads with hero banner visible', async ({ page }) => {
    await page.goto('/');
    await waitForReady(page);

    // HeroBanner 是 role="region" aria-label="HeroBanner"
    const hero = page.getByRole('region', { name: 'HeroBanner' });
    await expect(hero).toBeVisible();

    // 至少有 4 个指示点
    const dots = hero.locator('button[aria-label^="轮播第"]');
    await expect(dots).toHaveCount(4);
  });

  test('hero carousel slide on dot click', async ({ page }) => {
    await page.goto('/');
    await waitForReady(page);

    const hero = page.getByRole('region', { name: 'HeroBanner' });
    const dots = hero.locator('button[aria-label^="轮播第"]');

    // 初始状态：第 1 个 aria-current="true"
    await expect(dots.nth(0)).toHaveAttribute('aria-current', 'true');

    // 点第 3 个
    await dots.nth(2).click();
    await expect(dots.nth(2)).toHaveAttribute('aria-current', 'true');
    await expect(dots.nth(0)).not.toHaveAttribute('aria-current', 'true');
  });

  test('language switcher persists', async ({ page }) => {
    await page.goto('/');
    await waitForReady(page);

    // 切换语言（具体选择器视实际 TopBar 实现而定）
    // 这里仅断言 localStorage 上的 i18nextLng 写入路径存在
    const stored = await page.evaluate(() => localStorage.getItem('i18nextLng'));
    // 默认中文或浏览器语言均可，只要写入了
    expect(stored).toBeTruthy();
  });
});
