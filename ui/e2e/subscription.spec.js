// 订阅页与 API
import { test, expect } from '@playwright/test';
import { loginAsAdmin } from './helpers.js';

const ADMIN_TOKEN = process.env.ADMIN_TOKEN || '';

test.describe('Subscription system', () => {
  test('list public packages without auth', async ({ request }) => {
    // ListPublicPackages 通常允许匿名
    const res = await request.get('/api/packages/public');
    // 可能 200 (有套餐) 或 200 (空数组)
    expect(res.ok()).toBeTruthy();
    const json = await res.json();
    expect(json.success).toBe(true);
    expect(Array.isArray(json.data)).toBe(true);
  });

  test('purchase requires auth', async ({ request }) => {
    const res = await request.post('/api/subscriptions/purchase', {
      data: { package_id: 1, quantity: 1 },
    });
    expect(res.status()).toBe(401);
  });

  test.describe('Admin operations', () => {
    test.skip(!ADMIN_TOKEN, 'ADMIN_TOKEN env var not set');

    test('list all subscriptions paginated', async ({ page }) => {
      await loginAsAdmin(page);
      const res = await page.request.get('/api/admin/subscriptions?page=1&page_size=10');
      expect(res.ok()).toBeTruthy();
      const json = await res.json();
      expect(json.success).toBe(true);
      expect(json.data).toHaveProperty('items');
      expect(json.data).toHaveProperty('total');
    });

    test('list quota plans', async ({ page }) => {
      await loginAsAdmin(page);
      const res = await page.request.get('/api/admin/quota-plans');
      expect(res.ok()).toBeTruthy();
    });

    test('list packages', async ({ page }) => {
      await loginAsAdmin(page);
      const res = await page.request.get('/api/admin/packages');
      expect(res.ok()).toBeTruthy();
    });
  });
});

test.describe('Negative net price guard (C-3)', () => {
  test.skip(!ADMIN_TOKEN, 'ADMIN_TOKEN env var not set');

  test('cannot create package with bonus > price', async ({ page }) => {
    await loginAsAdmin(page);
    const res = await page.request.post('/api/admin/packages', {
      data: {
        name: 'NegativeTrap',
        price_amount: 1.0,
        bonus_balance_usd: 2.0, // 故意大于价格
        billing_period_seconds: 3600,
        public: false,
        enabled: true,
      },
    });
    expect(res.status()).toBe(400);
    const json = await res.json();
    expect(json.message_code).toBe('ERR_NEGATIVE_NET_PRICE');
  });
});
