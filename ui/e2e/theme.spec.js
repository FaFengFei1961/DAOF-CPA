// 浅色/深色切换 + MD3 CSS 变量
import { test, expect } from '@playwright/test';

test.describe('Theme switching', () => {
  test('default theme applies MD3 vars', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');

    // CSS 变量应被注入到 :root
    const primary = await page.evaluate(() =>
      getComputedStyle(document.documentElement).getPropertyValue('--color-primary').trim()
    );
    expect(primary).toMatch(/^#[0-9a-fA-F]{6,8}$/);
  });

  test('switching to light mode persists', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');

    // 直接通过 localStorage 切到 light（保险，独立于具体 UI 实现）
    await page.evaluate(() => {
      localStorage.setItem('daof_theme_preference', 'light');
    });
    await page.reload();
    await page.waitForLoadState('networkidle');

    const isDarkClass = await page.evaluate(() => document.documentElement.classList.contains('dark'));
    expect(isDarkClass).toBe(false);

    const stored = await page.evaluate(() => localStorage.getItem('daof_theme_preference'));
    expect(stored).toBe('light');
  });

  test('switching to dark mode adds dark class', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');

    await page.evaluate(() => {
      localStorage.setItem('daof_theme_preference', 'dark');
    });
    await page.reload();
    await page.waitForLoadState('networkidle');

    const isDarkClass = await page.evaluate(() => document.documentElement.classList.contains('dark'));
    expect(isDarkClass).toBe(true);
  });

  test('seed color change re-renders palette', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('networkidle');

    const before = await page.evaluate(() =>
      getComputedStyle(document.documentElement).getPropertyValue('--color-primary').trim()
    );

    // 改 seed → 红色
    await page.evaluate(() => {
      localStorage.setItem('daof_seed_color', '#dc2626');
    });
    await page.reload();
    await page.waitForLoadState('networkidle');

    const after = await page.evaluate(() =>
      getComputedStyle(document.documentElement).getPropertyValue('--color-primary').trim()
    );
    expect(before).not.toBe(after);
  });
});
