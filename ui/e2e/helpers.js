// E2E 共用 helpers
import { expect } from '@playwright/test';

const ADMIN_TOKEN = process.env.ADMIN_TOKEN || '';

/**
 * 通过 API 直接拿到 admin cookie 注入到浏览器，不走表单。
 * 适合非"测登录页"的测试。
 */
export async function loginAsAdmin(page) {
  if (!ADMIN_TOKEN) {
    throw new Error('ADMIN_TOKEN env var is required for admin tests');
  }
  // 直接调 secret-login，把 cookie 写进 BrowserContext
  const baseURL = page.context()._options?.baseURL || 'http://localhost:8080';
  const res = await page.request.post(`${baseURL}/api/admin/secret-login`, {
    data: { secret: ADMIN_TOKEN },
  });
  expect(res.ok()).toBeTruthy();
  // Server 写了 daof_admin_token httpOnly cookie，会自动跟随 page.goto
}

/**
 * 等待加载指示符消失（页面打字符 / spinner）
 */
export async function waitForReady(page) {
  await page.waitForLoadState('networkidle');
}
