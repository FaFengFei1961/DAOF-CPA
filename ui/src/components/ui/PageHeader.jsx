import React from 'react';

/**
 * PageHeader — 全站页面标题
 *
 * Phase 7.7：强化视觉权重，对齐 Linear / Vercel / Stripe 控制台 H1 标准
 * - H1 28px BOLD tracking-tight（旧 20px semibold 跟正文几乎一样大）
 * - sub 14px text-on-surface-variant（旧 12px 太小看不清）
 * - icon 容器 11×11 rounded-xl（更"app icon"感而非 chip）
 * - actions 与 title baseline 对齐
 */
const PageHeader = ({ title, sub, actions, icon: Icon, children, className = '' }) => (
  <header className={`flex flex-col sm:flex-row sm:items-start sm:justify-between gap-4 ${className}`}>
    <div className="flex items-start gap-3.5 min-w-0">
      {Icon && (
        <span className="w-11 h-11 rounded-xl bg-primary/10 border border-primary/20 flex items-center justify-center shrink-0 text-primary mt-0.5">
          <Icon size={22} strokeWidth={1.75} />
        </span>
      )}
      <div className="min-w-0">
        <h1 className="text-[26px] sm:text-[28px] font-bold tracking-tight text-on-surface leading-[1.15]">
          {title}
        </h1>
        {sub && (
          <p className="text-sm text-on-surface-variant mt-2 max-w-3xl leading-relaxed">
            {sub}
          </p>
        )}
        {children && <div className="mt-3">{children}</div>}
      </div>
    </div>
    {actions && (
      <div className="flex items-center gap-2 shrink-0 mt-1">
        {actions}
      </div>
    )}
  </header>
);

export default PageHeader;
