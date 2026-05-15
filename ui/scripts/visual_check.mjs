// 视觉自动化检查：访问关键页，截图 + DOM 探测视觉重叠/原语接入。
// 用法：node scripts/visual_check.mjs
// 前提：后端 :3000 在跑（go run main.go），前端 dist 已 build。
//
// 输出：
//   - scripts/visual_out/<page>-<viewport>.png  （截图）
//   - scripts/visual_out/report.json            （DOM 探测结果）
//   - stdout                                    （问题摘要）

import { chromium } from '@playwright/test';
import { mkdir } from 'node:fs/promises';
import { writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const __dirname = dirname(fileURLToPath(import.meta.url));
const outDir = join(__dirname, 'visual_out');
const BASE = process.env.BASE_URL || 'http://localhost:3000';

// 待访问的关键页（未登录可达）
const PAGES = [
  { name: 'dashboard',     path: '/' },
  { name: 'pricing',       path: '/pricing' },
  { name: 'upgrade',       path: '/upgrade' },
  { name: 'topup-result',  path: '/topup-result' },
];

// 视口（覆盖桌面 + 移动）
const VIEWPORTS = [
  { name: 'desktop', width: 1440, height: 900 },
  { name: 'mobile',  width: 375,  height: 800 },
];

await mkdir(outDir, { recursive: true });

const browser = await chromium.launch();
const ctx = await browser.newContext();
const report = { issues: [], pages: {} };

const consoleErrors = [];
ctx.on('weberror', e => consoleErrors.push(`weberror: ${e.error()?.message}`));

for (const v of VIEWPORTS) {
  for (const p of PAGES) {
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
      continue;
    }

    // 等 React 渲染
    await page.waitForTimeout(800);

    // DOM 探测
    const probe = await page.evaluate(() => {
      const result = {};

      // 1. 检查是否有 sidebar 与 main 区重叠
      const sidebar = document.querySelector('nav[aria-label="主导航"]');
      const main = document.querySelector('main#main-content');
      if (sidebar && main) {
        const sb = sidebar.getBoundingClientRect();
        const mb = main.getBoundingClientRect();
        result.sidebar = { left: sb.left, right: sb.right, width: sb.width };
        result.main = { left: mb.left, right: mb.right, width: mb.width };
        // sidebar 在桌面端应该在 main 左侧（main.left >= sidebar.right），允许 4px 误差
        if (sb.width > 0 && mb.left < sb.right - 4) {
          result.overlap_sidebar_main = true;
        }
      }

      // 2. Settings 内部 nav 是否存在并位于 main 区域内
      const settingsNav = document.querySelector('nav[aria-label="设置导航"]');
      if (settingsNav && main) {
        const sn = settingsNav.getBoundingClientRect();
        const mb = main.getBoundingClientRect();
        result.settingsNav = { left: sn.left, right: sn.right };
        // 应该在 main 区域内
        if (sn.left < mb.left - 4) {
          result.settings_nav_escapes_main = true;
        }
      }

      // 3. rounded-control-full 残留检测（class 名出现在 outerHTML 就报警）
      const html = document.documentElement.outerHTML;
      const m1 = (html.match(/rounded-control-full/g) || []).length;
      const m2 = (html.match(/rounded-overlay-full/g) || []).length;
      if (m1 + m2 > 0) result.bogus_rounded = m1 + m2;

      // 4. 圆形元素是否真圆？随机找几个标榜 rounded-full 的元素，看 borderRadius 计算值
      const roundedEls = Array.from(document.querySelectorAll('.rounded-full')).slice(0, 5);
      result.roundedSamples = roundedEls.map(el => {
        const cs = getComputedStyle(el);
        return {
          br: cs.borderRadius,
          w: el.offsetWidth,
          h: el.offsetHeight,
          // 真圆应该是 borderRadius = 50% 或者 >= max(w,h)
          isCircle: cs.borderRadius === '50%' ||
                    parseFloat(cs.borderRadius) >= Math.max(el.offsetWidth, el.offsetHeight) / 2,
        };
      });

      // 5. 是否有 .fl-card 元素带 box-shadow（应该是平面卡）
      const cards = Array.from(document.querySelectorAll('.fl-card')).slice(0, 5);
      result.cardsHaveShadow = cards.filter(el => {
        const sh = getComputedStyle(el).boxShadow;
        return sh && sh !== 'none' && !sh.includes('rgba(0, 0, 0, 0)');
      }).length;

      // 6. 找出可能的内容溢出（horizontal overflow）
      result.horizontalScroll = document.documentElement.scrollWidth > document.documentElement.clientWidth + 2;

      // 7. body 计算的 font-family（验证字体栈是否生效）
      result.bodyFont = getComputedStyle(document.body).fontFamily.split(',')[0].trim();

      // 8. 数据 token CSS var 是否注入
      const root = getComputedStyle(document.documentElement);
      result.tokens = {
        primary: root.getPropertyValue('--color-primary').trim(),
        surface: root.getPropertyValue('--color-surface').trim(),
        radiusControl: root.getPropertyValue('--radius-control').trim(),
        radiusOverlay: root.getPropertyValue('--radius-overlay').trim(),
      };

      return result;
    });

    // 截图
    const shotPath = join(outDir, `${pageId}.png`);
    await page.screenshot({ path: shotPath, fullPage: true });

    report.pages[pageId] = { probe, errs };

    // 提炼问题
    if (probe.overlap_sidebar_main) {
      report.issues.push({ pageId, kind: 'overlap', detail: `main.left=${probe.main.left}px < sidebar.right=${probe.sidebar.right}px` });
    }
    if (probe.settings_nav_escapes_main) {
      report.issues.push({ pageId, kind: 'settings_nav_escape', detail: `settingsNav.left=${probe.settingsNav.left} < main.left=${probe.main?.left}` });
    }
    if (probe.bogus_rounded) {
      report.issues.push({ pageId, kind: 'bogus_rounded', detail: `${probe.bogus_rounded} occurrences of rounded-control-full/rounded-overlay-full` });
    }
    if (probe.cardsHaveShadow > 0) {
      report.issues.push({ pageId, kind: 'fl-card-has-shadow', detail: `${probe.cardsHaveShadow} fl-card elements with box-shadow ≠ none` });
    }
    if (probe.horizontalScroll) {
      report.issues.push({ pageId, kind: 'horizontal_overflow', detail: 'body scrollWidth > clientWidth' });
    }
    if (errs.length) {
      report.issues.push({ pageId, kind: 'page_console_errs', detail: errs.slice(0, 3).join(' | ') });
    }

    const noCircle = (probe.roundedSamples || []).filter(s => !s.isCircle);
    if (noCircle.length) {
      report.issues.push({ pageId, kind: 'rounded_full_not_circle', detail: JSON.stringify(noCircle.slice(0, 2)) });
    }

    await page.close();
  }
}

await browser.close();

writeFileSync(join(outDir, 'report.json'), JSON.stringify(report, null, 2));

console.log('═══ 视觉自动化检查报告 ═══');
console.log(`Pages 截图：${Object.keys(report.pages).length}，问题数：${report.issues.length}`);
console.log('');

if (report.issues.length === 0) {
  console.log('✓ 无问题发现');
} else {
  for (const i of report.issues) {
    console.log(`  [${i.kind}] ${i.pageId}: ${i.detail}`);
  }
}

console.log('');
console.log(`截图目录: ${outDir}`);
console.log(`报告 JSON: ${join(outDir, 'report.json')}`);
