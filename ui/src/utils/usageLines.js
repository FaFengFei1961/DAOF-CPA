export const usageLinesOf = (row) => (
  Array.isArray(row?.usage_lines)
    ? row.usage_lines.filter((line) => Number(line?.quantity || 0) > 0)
    : []
);

export const usageUnitLabel = (unit) => {
  switch (String(unit || '').toLowerCase()) {
    case 'image':
      return '张';
    case 'video_second':
      return '秒';
    case 'request':
      return '次';
    case 'token':
      return 'tokens';
    default:
      return unit || 'unit';
  }
};

export const usageCostSourceLabel = (source) => {
  switch (String(source || '').toLowerCase()) {
    case 'upstream_usage':
      return '上游回传';
    case 'official_matrix':
      return '官方矩阵';
    case 'pending_reconcile':
      return '待对账';
    default:
      return source || '';
  }
};

export const formatUsageLine = (line, formatCost) => {
  if (!line) return '';
  const quantity = Number(line.quantity || 0);
  if (!Number.isFinite(quantity) || quantity <= 0) return '';

  const unit = usageUnitLabel(line.unit);
  const dims = [line.resolution || line.size, line.aspect_ratio, line.quality]
    .map((v) => String(v || '').trim())
    .filter(Boolean)
    .join(' · ');
  const unitPrice = Number(line.unit_price || 0);
  const amount = Number(line.amount || 0);
  const source = usageCostSourceLabel(line.cost_source);
  const parts = [`${quantity.toLocaleString()} ${unit}`];

  if (dims) parts.push(dims);
  if (unitPrice > 0 && formatCost) parts.push(`${formatCost(unitPrice)}/${unit}`);
  if (amount > 0 && formatCost) parts.push(formatCost(amount));
  if (source) parts.push(source);

  return parts.join(' · ');
};

export const formatUsageLinesSummary = (row, formatCost) => (
  usageLinesOf(row).map((line) => formatUsageLine(line, formatCost)).filter(Boolean).join(' | ')
);
