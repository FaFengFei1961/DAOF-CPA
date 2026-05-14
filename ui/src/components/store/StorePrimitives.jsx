import React from 'react';
import { ChevronRight, ArrowUpRight } from 'lucide-react';

/*
 * Store 风格基本组件三件套（与 Microsoft Store 主页/类目页一致）。
 *
 * 抽出动机：之前各页（Topup / Product / MySubscriptions / Channels）
 * 各自实现 H1 + p + 卡片网格，padding/圆角/typography 不统一。
 * 统一到这一组组件后，所有页面"开页就具备 Store 质感"。
 *
 * 用法：
 *   <StorePage title="..." subtitle="..." actions={<...>}>
 *     <StoreSection title="..." onSeeAll={...}>
 *       <StoreCard ...>
 *     </StoreSection>
 *   </StorePage>
 */

// ════════ 1. StorePage —— 页面级容器 ════════
//
// 提供：
//   - 统一的页面 padding（lg:px-10 py-8 与 Dashboard 一致）
//   - Hero 头部（title + subtitle + 右侧 actions slot）
//   - 与 fl-mica 配合的可选 mica 外壳
export function StorePage({ title, subtitle, icon: Icon, actions, children, mica = false }) {
  return (
    <div className={`w-full ${mica ? 'bg-surface-container border border-outline-variant rounded-overlay' : ''}`}>
      {(title || subtitle) && (
        <header className="mb-8 flex flex-col md:flex-row md:items-end md:justify-between gap-3">
          <div>
            <h1 className="text-3xl md:text-[40px] font-semibold tracking-tight text-on-surface flex items-center gap-3">
              {Icon && <Icon size={28} className="text-primary" />}
              {title}
            </h1>
            {subtitle && (
              <p className="text-on-surface-variant mt-2 text-sm md:text-base max-w-2xl">
                {subtitle}
              </p>
            )}
          </div>
          {actions && <div className="shrink-0">{actions}</div>}
        </header>
      )}
      <div className="space-y-8">{children}</div>
    </div>
  );
}

// ════════ 2. StoreSection —— 区块（标题 + chevron + content） ════════
//
// 严格按 Microsoft Store "热门游戏 / 新潮应用" 风格：
//   - 标题左侧 + chevron-right 在标题旁边（点击跳到详情）
//   - 右侧可放分页箭头或筛选 chip
//   - 内容区由 children 自由排版
export function StoreSection({ title, onSeeAll, right, children, dense = false }) {
  return (
    <section>
      <header className="fl-section-header">
        {onSeeAll ? (
          <button type="button" onClick={onSeeAll} className="fl-section-title">
            {title}
            <ChevronRight size={20} />
          </button>
        ) : (
          <h2 className="fl-section-title cursor-default" style={{ pointerEvents: 'none' }}>
            {title}
          </h2>
        )}
        {right && <div className="flex items-center gap-1">{right}</div>}
      </header>
      <div className={dense ? 'space-y-2' : 'space-y-3'}>{children}</div>
    </section>
  );
}

// ════════ 3. StoreCard —— 通用卡片（应用风格） ════════
//
// 三种密度：
//   - 'app' (默认)：图标 64×64 + 标题 + 副标 + CTA，对应 MS Store 应用列表行
//   - 'compact'：图标 40×40，整行小卡（用于历史列表 / 通知）
//   - 'feature'：图标 96×96，大尺寸特卡（专属页头部展示）
//
// 视觉锚点：
//   - 圆角矩形彩色背景容器作 logo（accent hue 渐变）
//   - hover 时整行轻微提亮（fl-card 的 hover behavior）
export function StoreCard({
  icon: Icon,
  iconColor,
  title,
  subtitle,
  meta,
  cta,
  onClick,
  badge,
  density = 'app',
  trailing,
  className = '',
}) {
  const isCompact = density === 'compact';
  const isFeature = density === 'feature';
  const iconBox = isFeature ? 'w-24 h-24' : isCompact ? 'w-10 h-10' : 'w-16 h-16';
  const iconSize = isFeature ? 40 : isCompact ? 18 : 28;

  const Wrapper = onClick ? 'button' : 'div';
  return (
    <Wrapper
      type={onClick ? 'button' : undefined}
      onClick={onClick}
      className={`fl-card group flex items-center gap-4 ${isCompact ? 'p-3' : 'p-4'} text-left w-full ${onClick ? 'cursor-pointer' : ''} ${className}`}
    >
      {/* Logo 容器：accent hue 衍生的浅渐变 + 边框（与 Dashboard ModelRow 同款） */}
      {Icon && (
        <div
          className={`${iconBox} rounded-overlay flex items-center justify-center shrink-0 border`}
          style={{
            background: `linear-gradient(135deg, ${hexA(iconColor || '#7c5cff', 0.30)} 0%, ${hexA(iconColor || '#7c5cff', 0.10)} 100%)`,
            borderColor: hexA(iconColor || '#7c5cff', 0.25),
          }}
        >
          <Icon size={iconSize} style={{ color: iconColor || '#7c5cff' }} />
        </div>
      )}

      {/* 中间：标题 + 副标 + meta */}
      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-2 flex-wrap">
          <span className={`font-semibold text-on-surface truncate ${isFeature ? 'text-xl' : isCompact ? 'text-sm' : 'text-base'}`}>
            {title}
          </span>
          {badge && (
            <span className="inline-flex items-center gap-1 text-[10px] font-semibold uppercase tracking-wider px-1.5 py-0.5 rounded-control bg-primary-container/60 text-on-primary-container">
              {badge}
            </span>
          )}
        </div>
        {subtitle && (
          <div className={`text-on-surface-variant mt-0.5 truncate ${isCompact ? 'text-[11px]' : 'text-xs'}`}>
            {subtitle}
          </div>
        )}
        {meta && <div className={`mt-1.5 ${isCompact ? 'text-[11px]' : 'text-xs'}`}>{meta}</div>}
      </div>

      {/* 右侧：CTA / trailing */}
      {trailing && <div className="shrink-0">{trailing}</div>}
      {cta && (
        <span className="shrink-0 inline-flex items-center justify-center h-8 px-3 rounded-control text-xs font-semibold bg-primary text-on-primary group-hover:brightness-110 transition">
          {cta}
        </span>
      )}
      {!cta && !trailing && onClick && (
        <ChevronRight size={16} className="shrink-0 text-on-surface-variant opacity-0 group-hover:opacity-100 transition" />
      )}
    </Wrapper>
  );
}

// ════════ 4. StoreHero —— 页面顶部 hero 大卡（Dashboard 之外的页面共用） ════════
//
// 比 Dashboard HeroFeatured 简化：仅 1 块，纯文案 + CTA + 装饰图标
// 用于 Topup / Tickets / Products 这类"功能起点"页面的顶部
export function StoreHero({
  title,
  subtitle,
  ctaLabel,
  onCta,
  icon: Icon,
  hue = '#7c5cff',
  badge,
  children,
}) {
  return (
    <div
      className="fl-hero p-6 sm:p-8 flex flex-col md:flex-row md:items-center gap-6 min-h-[180px]"
      style={{
        '--hero-bg-1': darkenHex(hue, 0.45),
        '--hero-bg-2': hue,
        '--hero-bg-3': darkenHex(hue, 0.7),
      }}
    >
      {Icon && (
        <div className="w-16 h-16 sm:w-20 sm:h-20 rounded-2xl flex items-center justify-center bg-white/15 backdrop-blur-md shrink-0">
          <Icon size={36} className="text-white" />
        </div>
      )}
      <div className="flex-1 min-w-0">
        {badge && (
          <span className="inline-flex items-center px-2 py-0.5 rounded-control text-[11px] font-semibold uppercase tracking-wider bg-white/15 backdrop-blur-sm text-white mb-2">
            {badge}
          </span>
        )}
        <h2 className="text-2xl sm:text-3xl font-semibold tracking-tight text-white">{title}</h2>
        {subtitle && (
          <p className="text-sm sm:text-base fl-hero-text-secondary mt-1.5 max-w-2xl">{subtitle}</p>
        )}
        {children && <div className="mt-3">{children}</div>}
      </div>
      {ctaLabel && onCta && (
        <button
          type="button"
          onClick={onCta}
          className="shrink-0 inline-flex items-center justify-center gap-1.5 h-10 px-5 rounded-control text-sm font-semibold bg-white text-zinc-900 hover:bg-white/90 active:scale-[0.98] transition"
        >
          {ctaLabel}
          <ArrowUpRight size={14} />
        </button>
      )}
    </div>
  );
}

// ════════ 5. StoreGrid —— 卡片栅格（响应式 1/2/3/4 列） ════════
export function StoreGrid({ children, columns = 'auto' }) {
  const cls =
    columns === 2 ? 'grid grid-cols-1 md:grid-cols-2 gap-3' :
    columns === 3 ? 'grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-3' :
    columns === 4 ? 'grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4 gap-3' :
    'grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4 gap-3';
  return <div className={cls}>{children}</div>;
}

// hex → rgba（与 Dashboard 同款 helper，避免循环依赖）
function hexA(hex, alpha) {
  if (!hex || hex[0] !== '#') return `rgba(124, 92, 255, ${alpha})`;
  const m = hex.match(/^#([0-9a-f]{6})$/i);
  if (!m) return `rgba(124, 92, 255, ${alpha})`;
  const n = parseInt(m[1], 16);
  return `rgba(${(n >> 16) & 255}, ${(n >> 8) & 255}, ${n & 255}, ${alpha})`;
}

function darkenHex(hex, factor = 0.5) {
  if (!hex || hex[0] !== '#') return '#1e1b4b';
  const m = hex.match(/^#([0-9a-f]{6})$/i);
  if (!m) return '#1e1b4b';
  const n = parseInt(m[1], 16);
  const r = Math.round(((n >> 16) & 255) * (1 - factor));
  const g = Math.round(((n >> 8) & 255) * (1 - factor));
  const b = Math.round((n & 255) * (1 - factor));
  return '#' + [r, g, b].map((x) => x.toString(16).padStart(2, '0')).join('');
}
