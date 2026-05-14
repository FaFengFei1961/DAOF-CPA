/**
 * admin/shared.js — 几个 admin page 公用的格式化 / 判定 helper
 *
 * 抽取自旧 UserUsageDash.jsx (1163 行) 重做时的共享部分，避免每个新 page
 * 重复定义 formatTokens / formatPercent 等。
 */
export const PERIODS = [
  { value: '24h', label: '24 小时' },
  { value: '7d',  label: '7 天' },
  { value: '30d', label: '30 天' },
  { value: 'all', label: '全部' },
];

export const formatTokens = (n) => {
  if (!n) return '0';
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M';
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'K';
  return n.toLocaleString();
};

export const formatLatency = (ms) => {
  if (!ms) return '-';
  if (ms < 1000) return Math.round(ms) + 'ms';
  return (ms / 1000).toFixed(2) + 's';
};

export const formatPercent = (v) => {
  const n = Number(v || 0);
  if (!Number.isFinite(n)) return '0.0%';
  return `${(n * 100).toFixed(1)}%`;
};

export const formatRelativeTime = (iso) => {
  if (!iso) return '从未活跃';
  const d = new Date(iso);
  const diffMs = Date.now() - d.getTime();
  const sec = Math.floor(diffMs / 1000);
  if (sec < 60) return `${sec}s 前`;
  if (sec < 3600) return `${Math.floor(sec / 60)}m 前`;
  if (sec < 86400) return `${Math.floor(sec / 3600)}h 前`;
  if (sec < 86400 * 30) return `${Math.floor(sec / 86400)}d 前`;
  return d.toLocaleDateString();
};

export const formatTime = (iso) => {
  if (!iso) return '-';
  return new Date(iso).toLocaleString('zh-CN', { hour12: false });
};

/**
 * formatMeterCost — 对极小金额自动加位（避免 0.000123 → 0.000）
 */
export const makeFormatMeterCost = (formatCurrencyFixed) => (value) => {
  const n = Number(value || 0);
  if (!Number.isFinite(n)) return value;
  return Math.abs(n) > 0 && Math.abs(n) < 0.001
    ? formatCurrencyFixed(n, 6)
    : formatCurrencyFixed(n, 3);
};

/**
 * isPrecheckLimitEvent — 区分"本次预估超窗口剩余"vs"总额度耗尽"
 * 与 ApiLog.error_type / block_reason 一致
 */
export const isPrecheckLimitEvent = (e) => (
  e?.error_type === 'request_estimate_exceeds_window_remaining'
  || e?.block_reason === 'plan_full_skip_sub'
  || e?.block_reason === 'request_estimate_exceeds_window_remaining'
);

/**
 * formatEventFailure — 把失败事件结构化成 { label, detail }
 */
export const makeFormatEventFailure = (formatMeterCost) => (e) => {
  if (!e?.error_type) return null;
  if (isPrecheckLimitEvent(e)) {
    const parts = [
      `预估扣减 ${formatMeterCost(e.precheck_charged_cost || 0)}`,
      `剩余 ${formatMeterCost(e.precheck_quota_remaining || 0)}`,
      `预估输入 ${(e.precheck_input_tokens || 0).toLocaleString()}`,
      `预估输出 ${(e.precheck_output_tokens || 0).toLocaleString()}`,
    ];
    if (e.precheck_window_end_at) {
      parts.push(`窗口结束 ${formatTime(e.precheck_window_end_at)}`);
    }
    return {
      label: '单次请求超过剩余额度',
      detail: parts.join(' · '),
    };
  }
  return {
    label: e.error_type,
    detail: e.error_message || '',
  };
};
