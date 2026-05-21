import React from 'react';

/**
 * StatCard — 统一 KPI 卡片（Phase 1）
 *
 * 替换原各 admin 页面自己写的 5 张总览卡（密度/对比/对齐都不一致）。
 * 视觉规则：
 *  - Fluent card（带 hover reveal，符合 gemini ccg "卡片微交互"建议）
 *  - 左上 icon（圆形 token 容器） + 右上 trend 小徽章
 *  - 主数值 title3 (24px) tabular-nums，副标题 caption1 (12px) muted
 *  - 可选 sub2 行（如"近 24h" 时间窗）
 */
const StatCard = ({
  icon: Icon,
  iconColor = 'text-primary',
  iconBg = 'bg-primary-container/40',
  label,
  value,
  sub,
  sub2,
  trend,
  trendTone = 'neutral',
  onClick,
  className = '',
}) => {
  const trendBg = {
    up:      'bg-success/15 text-success border-success/30',
    down:    'bg-error/15 text-error border-error/30',
    neutral: 'bg-on-surface/[0.06] text-on-surface-variant border-outline-variant',
  }[trendTone];

  const baseClass = `card p-4 md:p-5 flex flex-col gap-3 min-h-[112px] ${className}`;
  const Wrapper = onClick ? 'button' : 'div';
  const wrapperProps = onClick
    ? { type: 'button', onClick, className: `${baseClass} text-left focus-visible:ring-2 focus-visible:ring-primary outline-none` }
    : { className: baseClass };

  return (
    <Wrapper {...wrapperProps}>
      <header className="flex items-start justify-between gap-2">
        {Icon && (
          <span className={`w-9 h-9 rounded-control ${iconBg} ${iconColor} flex items-center justify-center shrink-0`}>
            <Icon size={18} strokeWidth={1.75} />
          </span>
        )}
        {trend && (
          <span className={`inline-flex items-center gap-0.5 px-2 h-6 rounded-full text-[11px] font-medium border ${trendBg}`}>
            {trend}
          </span>
        )}
      </header>
      <div className="min-w-0">
        <div className="text-2xl font-semibold text-on-surface tracking-tight tabular-nums truncate">
          {value}
        </div>
        {label && (
          <div className="text-xs text-on-surface-variant mt-0.5 truncate">{label}</div>
        )}
        {sub && (
          <div className="text-[11px] text-on-surface-variant/80 mt-1 truncate">{sub}</div>
        )}
        {sub2 && (
          <div className="text-[10px] text-outline-variant mt-0.5 truncate">{sub2}</div>
        )}
      </div>
    </Wrapper>
  );
};

export default StatCard;
