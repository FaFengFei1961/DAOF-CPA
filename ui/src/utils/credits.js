// Shared utilities for the credits pool monitoring UI.
// Used by admin monitoring and subscription usage views.

/**
 * Pick a remaining-percent color on a 4-stop gradient (red -> dark red).
 * @param {number} pct - 0..100
 * @returns {string} - CSS color string
 */
export const remainingColor = (pct) => {
  const safe = Number.isFinite(pct) ? pct : 0;
  if (safe >= 50) return '#10b981';
  if (safe >= 20) return '#f59e0b';
  if (safe >= 5) return '#ef4444';
  return '#7f1d1d';
};

/**
 * Format an ISO timestamp to local zh-CN string. Handles Go's zero time
 * (`0001-01-01T00:00:00Z`) and undefined/null gracefully.
 * @param {string|null|undefined} iso
 * @returns {string}
 */
export const fmtTime = (iso) => {
  if (!iso || iso === '0001-01-01T00:00:00Z') return '—';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '—';
  return d.toLocaleString('zh-CN', { hour12: false });
};

/**
 * Render an ISO timestamp as relative time from "now" — e.g. "5 分钟后", "2 小时后".
 * Returns null on invalid/zero values, "已重置" on past times.
 * @param {string|null|undefined} iso
 * @returns {string|null}
 */
export const fmtRelativeFromNow = (iso) => {
  if (!iso || iso === '0001-01-01T00:00:00Z') return null;
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return null;
  const diff = d.getTime() - Date.now();
  if (diff < 0) return '已重置';
  const mins = Math.round(diff / 60000);
  if (mins < 60) return `${mins} 分钟后`;
  const hrs = Math.round(mins / 60);
  if (hrs < 24) return `${hrs} 小时后`;
  return `${Math.round(hrs / 24)} 天后`;
};

/**
 * Clamp a percentage value into [0, 100], substituting NaN/null with 0.
 * @param {unknown} v
 * @returns {number}
 */
export const safePct = (v) => {
  const n = Number(v);
  if (!Number.isFinite(n)) return 0;
  if (n < 0) return 0;
  if (n > 100) return 100;
  return n;
};
