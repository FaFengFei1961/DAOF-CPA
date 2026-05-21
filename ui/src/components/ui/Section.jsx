import React from 'react';

/**
 * Section — 页面内分块容器（Phase 0.5）
 *
 * 用于把大页面分成清晰的视觉块。每个 Section 自带：
 *   - title (subtitle1, 16-20px) + optional sub
 *   - actions (右上角)
 *   - children 内容区
 *   - 默认套 card（带 hover reveal），用 `flat` prop 关掉
 *
 * 取代每页自己写 `<div className="bg-surface-container border border-outline-variant rounded-overlay p-4 md:p-6 mb-8">`。
 */
const Section = ({
  title,
  sub,
  actions,
  icon: Icon,
  children,
  flat = false,
  noPadding = false,
  className = '',
}) => {
  const surfaceClass = flat ? '' : 'card';
  const padClass = noPadding ? '' : 'p-4 md:p-6';
  return (
    <section className={`${surfaceClass} ${padClass} ${className}`}>
      {(title || actions) && (
        <header className={`flex items-start justify-between gap-3 ${children ? 'mb-4' : ''}`}>
          <div className="min-w-0">
            {title && (
              <h2 className="text-base font-semibold text-on-surface flex items-center gap-2">
                {Icon && <Icon size={16} className="text-primary shrink-0" strokeWidth={1.75} />}
                <span className="truncate">{title}</span>
              </h2>
            )}
            {sub && (
              <p className="text-xs text-on-surface-variant mt-1 max-w-3xl">{sub}</p>
            )}
          </div>
          {actions && (
            <div className="flex items-center gap-2 shrink-0">{actions}</div>
          )}
        </header>
      )}
      {children}
    </section>
  );
};

export default Section;
