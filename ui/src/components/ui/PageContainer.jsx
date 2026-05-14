import React from 'react';

/**
 * PageContainer — 全站页面外框（Phase 0.5 quick win #1）
 *
 * 取代每页自己写 `mb-6` / `mb-8` / `p-4` / `p-5` / `p-6` 的混乱状态。
 * 强制呼吸感：模块间 gap-8 (32px)，符合 Fluent v9 spacing。
 *
 * 用法：
 *   <PageContainer>
 *     <PageHeader title="..." sub="..." />
 *     <Section>...</Section>
 *     <Section>...</Section>
 *   </PageContainer>
 *
 * 不强制设宽度上限 — 由外层 main 容器控制（App 层的 max-w-1880px）。
 */
const PageContainer = ({ children, className = '' }) => (
  <div className={`flex flex-col gap-6 md:gap-8 pb-12 ${className}`}>
    {children}
  </div>
);

export default PageContainer;
