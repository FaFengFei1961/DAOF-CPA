/**
 * 旧 hash 路由 → 新 React Router 路径兼容（Phase 0 重构）
 *
 * 旧实现（App.jsx 第二十四轮以前）用 `#dashboard`、`#topup_result?status=success` 等
 * hash 路由。Phase 0 切到 React Router BrowserRouter 后，旧链接（书签 / 邮件深链 /
 * github callback /?return_to=#bills）必须能 redirect。
 *
 * 启动时（main.jsx import 顺序最早）调用一次 redirectLegacyHash()，把
 * `window.location.hash` 解析后用 history.replaceState 改写为标准 path + query。
 */

// 旧 hash view → 新 path 映射
const HASH_TO_PATH = {
  dashboard:     '/',
  tokens:        '/tokens',
  stats:         '/stats',
  pricing:       '/pricing',
  upgrade:       '/upgrade',
  topup:         '/topup',
  topup_result:  '/topup-result',
  tickets:       '/tickets',
  bills:         '/bills',
  // settings 在新架构有两个去处：用户 → /settings；admin → /admin
  // 这里先 redirect 到 /settings，AdminShell 内部判定后会再 redirect 到 /admin
  settings:      '/settings',
};

/**
 * 兼容旧 hash。返回 true 表示已 redirect，false 表示无需处理。
 * 必须在 RouterProvider 挂载之前调用，否则 BrowserRouter 解析时还是基于
 * 没改写的 location。
 */
export function redirectLegacyHash() {
  const rawHash = (window.location.hash || '').replace(/^#/, '').trim();
  if (!rawHash) return false;

  // 形如 "topup_result?status=success" 或 "upgrade?pane=mine"
  const [view, query] = rawHash.split('?');
  const target = HASH_TO_PATH[view];
  if (!target) return false;

  const newUrl = target + (query ? `?${query}` : '');
  // 用 replaceState 不留旧 url 在历史里
  window.history.replaceState({}, '', newUrl);
  return true;
}
