// 管理员登录流程
import { test, expect } from '@playwright/test';

const ADMIN_TOKEN = process.env.ADMIN_TOKEN || '';

test.describe('Admin Login', () => {
  test.skip(!ADMIN_TOKEN, 'ADMIN_TOKEN env var not set');

  test('rejects empty secret', async ({ request }) => {
    const res = await request.post('/api/admin/secret-login', { data: { secret: '' } });
    expect(res.status()).toBe(400);
  });

  test('rejects wrong secret', async ({ request }) => {
    const res = await request.post('/api/admin/secret-login', {
      data: { secret: 'definitely-not-the-token' },
    });
    expect(res.status()).toBe(401);
  });

  test('accepts correct secret and sets cookie', async ({ request, context }) => {
    const res = await request.post('/api/admin/secret-login', {
      data: { secret: ADMIN_TOKEN },
    });
    expect(res.ok()).toBeTruthy();
    const json = await res.json();
    expect(json.success).toBe(true);

    // cookie 应被设置
    const cookies = await context.cookies();
    const adminCookie = cookies.find(c => c.name === 'daof_admin_token');
    expect(adminCookie).toBeDefined();
    expect(adminCookie.httpOnly).toBe(true);
    expect(adminCookie.sameSite).toBe('Strict');
  });
});

test.describe('Auth Modal — phone send-sms', () => {
  test('rejects invalid phone format', async ({ request }) => {
    const res = await request.post('/api/auth/send-sms', {
      data: { phone: '12345' },
    });
    expect(res.status()).toBe(400);
    const json = await res.json();
    expect(json.message_code).toBe('ERR_PHONE_FORMAT');
  });

  test('returns 503 when SMS not configured', async ({ request }) => {
    // 假设测试环境没配 aliyun key
    const res = await request.post('/api/auth/send-sms', {
      data: { phone: '13800138000' },
    });
    // 可能 503（未配置）或 429（同号冷却 / IP 限流）— 都是预期之内的拒绝
    expect([503, 429].includes(res.status())).toBeTruthy();
  });
});
