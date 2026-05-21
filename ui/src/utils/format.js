// IA audit M-V5 fix: currency formatting now lives exclusively in
// CurrencyContext#formatCurrency. The old formatUSD / formatRMB helpers
// were not currency-toggle aware and skipped tiered decimals; removing
// them stops the codebase from drifting back into two parallel formatters.

export function formatNumber(n) {
  if (n == null) return '';
  return Number(n).toLocaleString();
}

export function formatCompactNumber(n) {
  if (n == null) return '';
  return Intl.NumberFormat('en-US', {
    notation: 'compact',
    maximumFractionDigits: 1,
  }).format(n);
}

// IA audit m5 fix: i18next resolvedLanguage can be `zh`, `zh-CN`, or
// `zh-Hant`; previous exact `=== 'zh-CN'` check showed English to Hant
// users mid-Chinese UI. Cover the entire Chinese family.
const isChineseLocale = (locale) => typeof locale === 'string' && locale.toLowerCase().startsWith('zh');

export function formatRelativeTime(ts, locale = 'en') {
  if (!ts) return '';
  const now = Date.now();
  const diff = now - ts * 1000;
  const minutes = Math.floor(diff / 60000);
  const hours = Math.floor(minutes / 60);
  const days = Math.floor(hours / 24);
  const zh = isChineseLocale(locale);

  if (days > 0) return zh ? `${days}天前` : `${days} days ago`;
  if (hours > 0) return zh ? `${hours}小时前` : `${hours} hours ago`;
  if (minutes > 0) return zh ? `${minutes}分钟前` : `${minutes} mins ago`;
  return zh ? '刚刚' : 'just now';
}

export function formatTokens(n) {
  if (n == null) return '';
  if (Math.abs(n) >= 10000) {
    return formatCompactNumber(n);
  }
  return formatNumber(n);
}
