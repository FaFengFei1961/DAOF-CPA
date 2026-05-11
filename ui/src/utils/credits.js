// Shared utilities for the credits pool monitoring UI.
// Used by both CreditsMonitor (admin dashboard) and CreditsPoolCard (user homepage card).

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

/**
 * Map a model name to its series for dashboard grouping.
 * Returns one of: 'claude' | 'openai' | 'gemini' | 'kimi' | 'other'
 * @param {string} modelName
 * @returns {string}
 */
// OpenAI o-series 识别正则：单词边界 + 任意位数（o1 / o3 / o10 / o123）
// 抽出常量给 `Dashboard.jsx::inferProvider` 共享，避免两处不一致漂移
export const OPENAI_O_SERIES_RE = /\bo\d+\b/;

export const classifyModelSeries = (modelName) => {
  const m = (modelName || '').toLowerCase();
  if (m.includes('claude') || m.includes('anthropic')) return 'claude';
  // OpenAI 包含：gpt-* / codex-* / openai/* / azure/o1-mini / 单独的 o1/o2/...o10... 系列
  if (m.includes('gpt') || m.includes('codex') || m.includes('openai') || OPENAI_O_SERIES_RE.test(m)) return 'openai';
  if (m.includes('gemini')) return 'gemini';
  if (m.includes('kimi') || m.includes('moonshot')) return 'kimi';
  return 'other';
};

/**
 * Series presentation metadata：固定 Claude / OpenAI / Gemini / Kimi 顺序。
 * 仪表盘按这个顺序固定展示，避免 4 块格子位置抖动。
 */
export const SERIES_META = [
  { id: 'claude', label: 'Claude',  hue: '#d97706' },
  { id: 'openai', label: 'OpenAI',  hue: '#10b981' },
  { id: 'gemini', label: 'Gemini',  hue: '#0ea5e9' },
  { id: 'kimi',   label: 'Kimi',    hue: '#ef4444' },
];

/**
 * 把 ModelSummary 数组按系列聚合，返回 4 个 (固定顺序) 系列条目。
 * 单个系列：取该系列下"在线模型"的平均剩余%；只要有 1 个在线即视为系列在线。
 *
 * @param {Array<{model_name: string, avg_remaining_pct: number, online: boolean}>} models
 * @returns {Array<{
 *   id: string, label: string, hue: string,
 *   avgRemaining: number,         // 仅在线模型的平均
 *   online: boolean,              // 至少 1 个模型在线
 *   modelCount: number,           // 系列内总模型数（含离线）
 *   onlineCount: number           // 系列内在线模型数（用于 UI "m/n" 标签）
 * }>}
 */
export const aggregateBySeries = (models) => {
  const groups = new Map(SERIES_META.map(s => [s.id, { ...s, sum: 0, cnt: 0, online: false, modelCount: 0 }]));
  for (const m of models || []) {
    const sid = classifyModelSeries(m.model_name);
    const g = groups.get(sid);
    // sid === 'other' 时 g 为 undefined → 故意丢弃。
    // SERIES_META 只有 4 个固定 series（Claude/OpenAI/Gemini/Kimi），
    // 设计上 Dashboard 的 4 块 grid 不容纳第 5 类（DeepSeek / Qwen / xAI 等）。
    // 后续如需扩展到 5+ series，先扩 SERIES_META 再调 grid 列数。
    if (!g) continue;
    g.modelCount += 1;
    if (m.online) {
      g.online = true;
      g.sum += safePct(m.avg_remaining_pct);
      g.cnt += 1;
    }
  }
  return SERIES_META.map(s => {
    const g = groups.get(s.id);
    return {
      id: s.id,
      label: s.label,
      hue: s.hue,
      avgRemaining: g.cnt > 0 ? g.sum / g.cnt : 0,
      online: g.online,
      modelCount: g.modelCount,
      onlineCount: g.cnt,
    };
  });
};
