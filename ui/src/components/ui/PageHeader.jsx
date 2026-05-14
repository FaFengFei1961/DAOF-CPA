import React from 'react';

/**
 * PageHeader — 全站页面标题（Phase 0.5 quick win #1）
 *
 * 取代各页自己写的 `<div className="mb-8 border-b border-outline-variant pb-6">
 * <h1 className="text-xl md:text-2xl font-bold ...">{...}</h1>
 * <p className="text-on-surface-variant mt-2 text-sm">{...}</p></div>` 模板。
 *
 * 视觉规则（Fluent 2 + Win11 Settings）：
 *   - 标题用 subtitle1 (20px) 600，hierarchy 跟 Win11 Settings 一致（不要做太大）
 *   - subtitle 用 caption1 (12px) muted
 *   - 不画底部 border（旧实现 border-b 把内容压扁，不符合 Fluent 留白）
 *   - actions 右侧（按钮 / 筛选触发器等）
 */
const PageHeader = ({ title, sub, actions, icon: Icon, children, className = '' }) => (
  <header className={`flex flex-col sm:flex-row sm:items-end sm:justify-between gap-3 ${className}`}>
    <div className="flex items-start gap-3 min-w-0">
      {Icon && (
        <span className="w-10 h-10 rounded-lg bg-primary-container/40 flex items-center justify-center shrink-0 text-primary mt-0.5">
          <Icon size={20} strokeWidth={1.75} />
        </span>
      )}
      <div className="min-w-0">
        <h1 className="text-xl font-semibold tracking-tight text-on-surface leading-tight">
          {title}
        </h1>
        {sub && (
          <p className="text-xs text-on-surface-variant mt-1 max-w-3xl">
            {sub}
          </p>
        )}
        {children && <div className="mt-2">{children}</div>}
      </div>
    </div>
    {actions && (
      <div className="flex items-center gap-2 shrink-0">
        {actions}
      </div>
    )}
  </header>
);

export default PageHeader;
