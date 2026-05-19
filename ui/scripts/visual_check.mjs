// 视觉自动化检查：访问关键页，截图 + DOM 探测视觉重叠/原语接入。
//
// 用法（PowerShell）：
//   # 不带 admin 凭证：只测公开页
//   node scripts/visual_check.mjs
//
//   # 带 admin 凭证（在你的 PS shell 临时设置，跑完关闭窗口即清）：
//   $env:DAOF_ADMIN_USER='你的admin用户名'
//   $env:DAOF_ADMIN_PASS='你的admin密码'
//   node scripts/visual_check.mjs
//
// 前提：后端 :3000 在跑，前端 dist 已 build。

import { chromium } from '@playwright/test';
import { mkdir } from 'node:fs/promises';
import { writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const __dirname = dirname(fileURLToPath(import.meta.url));
const outDir = join(__dirname, 'visual_out');
const BASE = process.env.BASE_URL || 'http://localhost:3000';
const ADMIN_USER = process.env.DAOF_ADMIN_USER || '';
const ADMIN_PASS = process.env.DAOF_ADMIN_PASS || '';

const PUBLIC_PAGES = [
  { name: 'dashboard',     path: '/' },
  { name: 'pricing',       path: '/pricing' },
  { name: 'topup-result',  path: '/topup-result' },
];

const ADMIN_PAGES = [
  { name: 'admin-channels',       path: '/admin/channels' },
  { name: 'admin-users',          path: '/admin/users' },
  { name: 'admin-sync',           path: '/admin/sync' },
  { name: 'admin-notifications',  path: '/admin/notifications' },
  { name: 'admin-finance-topups', path: '/admin/finance/topups' },
  { name: 'admin-moderation',     path: '/admin/moderation' },
  { name: 'admin-coupons',        path: '/admin/coupons' },
  { name: 'admin-packages',       path: '/admin/packages' },
  { name: 'admin-quota-plans',    path: '/admin/quota-plans' },
  { name: 'admin-credits',        path: '/admin/credits' },
  { name: 'admin-general',        path: '/admin/general' },
  { name: 'admin-oauth',          path: '/admin/oauth' },
  { name: 'admin-sms',            path: '/admin/sms' },
  { name: 'admin-risk',           path: '/admin/risk' },
  { name: 'admin-i18n',           path: '/admin/i18n' },
  { name: 'admin-tickets',        path: '/admin/tickets' },
  { name: 'admin-users-usage',    path: '/admin/users/usage' },
  { name: 'admin-upstream',       path: '/admin/upstream/margin' },
  { name: 'admin-audit',          path: '/admin/audit/events' },
];

const VIEWPORTS = [
  { name: 'desktop', width: 1440, height: 900 },
  { name: 'mobile',  width: 375,  height: 800 },
];

await mkdir(outDir, { recursive: true });

// 禁用 HTTP cache：Fiber Static 默认带 Last-Modified，
// Chromium 会用 heuristic cache 复用 dist 文件 → build 后看到的还是旧 hash。
const browser = await chromium.launch({
  args: ['--disable-cache', '--disk-cache-size=1', '--media-cache-size=1', '--disable-application-cache'],
});
const ctx = await browser.newContext({
  ignoreHTTPSErrors: true,
  extraHTTPHeaders: { 'Cache-Control': 'no-cache, no-store, must-revalidate', 'Pragma': 'no-cache' },
});
const report = { issues: [], pages: {}, adminLoggedIn: false };

// admin 登录（如有凭证）
let adminCookieAvailable = false;
if (ADMIN_USER && ADMIN_PASS) {
  const tmpPage = await ctx.newPage();
  try {
    const resp = await tmpPage.request.post(`${BASE}/api/root/god-login`, {
      data: { username: ADMIN_USER, password: ADMIN_PASS },
      headers: { 'Content-Type': 'application/json' },
    });
    const json = await resp.json().catch(() => ({}));
    if (resp.ok() && json.success) {
      adminCookieAvailable = true;
      report.adminLoggedIn = true;
      // 前端 isAdmin 检测 = localStorage.daof_admin_unlocked === '1'（AuthContext.jsx:30）
      // 不是只看 cookie。注入 localStorage flag 让 AdminGuard 通过。
      await ctx.addInitScript(() => {
        try { localStorage.setItem('daof_admin_unlocked', '1'); } catch (_) {}
      });
      console.log('✓ admin god-login 成功（已注入 localStorage flag）');
    } else {
      console.log(`✗ admin god-login 失败 status=${resp.status()} message=${json.message || ''}`);
    }
  } catch (e) {
    console.log(`✗ admin god-login 网络错: ${e.message}`);
  } finally {
    await tmpPage.close();
  }
} else {
  console.log('⚠ 未提供 DAOF_ADMIN_USER/DAOF_ADMIN_PASS，跳过 admin 页');
}

async function visitPage(p, v) {
  const pageId = `${p.name}-${v.name}`;
  const page = await ctx.newPage();
  await page.setViewportSize({ width: v.width, height: v.height });

  const errs = [];
  page.on('pageerror', e => errs.push(`pageerror: ${e.message}`));
  page.on('console', msg => {
    if (msg.type() === 'error') errs.push(`console.error: ${msg.text()}`);
  });

  try {
    await page.goto(BASE + p.path, { waitUntil: 'networkidle', timeout: 15000 });
  } catch (e) {
    report.issues.push({ pageId, kind: 'nav-fail', detail: e.message });
    await page.close();
    return;
  }
  await page.waitForTimeout(800);

  const probe = await page.evaluate(() => {
    const result = {};
    const sidebar = document.querySelector('nav[aria-label="主导航"], nav[aria-label="Admin 导航"], nav[aria-label="管理导航"]');
    const main = document.querySelector('main#main-content');
    if (sidebar && main) {
      const sb = sidebar.getBoundingClientRect();
      const mb = main.getBoundingClientRect();
      result.sidebar = { left: sb.left, right: sb.right, width: sb.width };
      result.main = { left: mb.left, right: mb.right, width: mb.width };
      if (sb.width > 0 && mb.left < sb.right - 4) result.overlap_sidebar_main = true;
    }

    const settingsNav = document.querySelector('nav[aria-label="设置导航"]');
    if (settingsNav && main) {
      const sn = settingsNav.getBoundingClientRect();
      const mb = main.getBoundingClientRect();
      result.settingsNav = { left: sn.left, right: sn.right };
      if (sn.left < mb.left - 4) result.settings_nav_escapes_main = true;
    }

    const html = document.documentElement.outerHTML;
    const m1 = (html.match(/rounded-control-full/g) || []).length;
    const m2 = (html.match(/rounded-overlay-full/g) || []).length;
    if (m1 + m2 > 0) result.bogus_rounded = m1 + m2;

    const roundedEls = Array.from(document.querySelectorAll('.rounded-full')).slice(0, 5);
    result.roundedSamples = roundedEls.map(el => {
      const cs = getComputedStyle(el);
      return {
        br: cs.borderRadius,
        isCircle: cs.borderRadius === '50%' || parseFloat(cs.borderRadius) >= Math.max(el.offsetWidth, el.offsetHeight) / 2,
      };
    });

    const cards = Array.from(document.querySelectorAll('.fl-card')).slice(0, 5);
    result.cardsHaveShadow = cards.filter(el => {
      const sh = getComputedStyle(el).boxShadow;
      return sh && sh !== 'none' && !sh.includes('rgba(0, 0, 0, 0)');
    }).length;

    result.horizontalScroll = document.documentElement.scrollWidth > document.documentElement.clientWidth + 2;

    const bareTables = document.querySelectorAll('table:not(.fl-table-shell table)').length;
    const shellTables = document.querySelectorAll('.fl-table-shell table').length;
    result.tables = { bare: bareTables, shell: shellTables };

    return result;
  });

  await page.screenshot({ path: join(outDir, `${pageId}.png`), fullPage: true });
  report.pages[pageId] = { probe, errs };

  if (probe.overlap_sidebar_main) report.issues.push({ pageId, kind: 'overlap', detail: `main.left=${probe.main.left} < sidebar.right=${probe.sidebar.right}` });
  if (probe.settings_nav_escapes_main) report.issues.push({ pageId, kind: 'settings_nav_escape', detail: 'escapes main bounds' });
  if (probe.bogus_rounded) report.issues.push({ pageId, kind: 'bogus_rounded', detail: `${probe.bogus_rounded} occurrences` });
  if (probe.cardsHaveShadow > 0) report.issues.push({ pageId, kind: 'fl-card-has-shadow', detail: `${probe.cardsHaveShadow} cards` });
  if (probe.horizontalScroll) report.issues.push({ pageId, kind: 'horizontal_overflow', detail: 'body overflows' });
  if (probe.tables && probe.tables.bare > 0) report.issues.push({ pageId, kind: 'bare_table', detail: `${probe.tables.bare} bare <table>(s) not using .fl-table-shell` });
  if (errs.length) report.issues.push({ pageId, kind: 'page_console_errs', detail: errs.slice(0, 3).join(' | ') });
  const noCircle = (probe.roundedSamples || []).filter(s => !s.isCircle);
  if (noCircle.length) report.issues.push({ pageId, kind: 'rounded_full_not_circle', detail: JSON.stringify(noCircle.slice(0, 2)) });

  await page.close();
}

for (const v of VIEWPORTS) {
  for (const p of PUBLIC_PAGES) await visitPage(p, v);
  if (adminCookieAvailable) {
    for (const p of ADMIN_PAGES) await visitPage(p, v);
  }
}

await browser.close();
writeFileSync(join(outDir, 'report.json'), JSON.stringify(report, null, 2));

console.log('═══ 视觉自动化检查报告 ═══');
console.log(`Admin 登录: ${report.adminLoggedIn ? '✓' : '✗ (skipped)'}`);
console.log(`Pages 截图: ${Object.keys(report.pages).length}，问题数: ${report.issues.length}`);
console.log('');

if (report.issues.length === 0) {
  console.log('✓ 无问题发现');
} else {
  for (const i of report.issues) console.log(`  [${i.kind}] ${i.pageId}: ${i.detail}`);
}
console.log('');
console.log(`截图目录: ${outDir}`);
console.log(`报告 JSON: ${join(outDir, 'report.json')}`);
