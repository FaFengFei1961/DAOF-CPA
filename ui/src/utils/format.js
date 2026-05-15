export function formatUSD(n, decimals = 2) {
  if (n == null) return '';
  return '$' + Number(n).toLocaleString('en-US', {
    minimumFractionDigits: decimals,
    maximumFractionDigits: decimals,
  });
}

export function formatRMB(n, decimals = 2) {
  if (n == null) return '';
  return '¥' + Number(n).toLocaleString('zh-CN', {
    minimumFractionDigits: decimals,
    maximumFractionDigits: decimals,
  });
}

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

export function formatRelativeTime(ts, locale = 'en') {
  if (!ts) return '';
  const now = Date.now();
  const diff = now - ts * 1000;
  const minutes = Math.floor(diff / 60000);
  const hours = Math.floor(minutes / 60);
  const days = Math.floor(hours / 24);

  if (days > 0) return locale === 'zh-CN' ? `${days}天前` : `${days} days ago`;
  if (hours > 0) return locale === 'zh-CN' ? `${hours}小时前` : `${hours} hours ago`;
  if (minutes > 0) return locale === 'zh-CN' ? `${minutes}分钟前` : `${minutes} mins ago`;
  return locale === 'zh-CN' ? '刚刚' : 'just now';
}

export function formatTokens(n) {
  if (n == null) return '';
  if (Math.abs(n) >= 10000) {
    return formatCompactNumber(n);
  }
  return formatNumber(n);
}
