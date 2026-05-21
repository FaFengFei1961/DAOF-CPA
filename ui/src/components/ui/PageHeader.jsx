import React from 'react';

/**
 * PageHeader — 全站页面标题
 *
 * Sprint J-3 batch 3：用 .page-title 原语（32px / mobile 24px）替代硬编码的
 * text-[26px]/text-[28px]。同时 hero CTA / icon block 都缩放到与新字号匹配
 * 的尺寸，让页面头部不再看着像正文 +1。
 */
const PageHeader = ({ title, sub, actions, icon: Icon, children, className = '' }) => (
  <header className={`flex flex-col sm:flex-row sm:items-start sm:justify-between gap-4 mb-8 ${className}`}>
    <div className="flex items-start gap-4 min-w-0">
      {Icon && (
        <span className="w-12 h-12 rounded-control bg-primary/[0.08] border border-primary/20 flex items-center justify-center shrink-0 text-primary mt-0.5">
          <Icon size={24} strokeWidth={1.75} />
        </span>
      )}
      <div className="min-w-0">
        <h1 className="page-title">{title}</h1>
        {sub && <p className="page-subtitle">{sub}</p>}
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
