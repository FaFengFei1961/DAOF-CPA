import React from 'react';
import { ChevronRight, ArrowUpRight } from 'lucide-react';

/*
 * Store-style primitive components inspired by Microsoft Store surfaces.
 *
 * Centralizes page headers, sections, cards, heroes, and grids so feature pages
 * keep consistent spacing, radius, and typography.
 *
 * Usage:
 *   <StorePage title="..." subtitle="..." actions={<...>}>
 *     <StoreSection title="..." onSeeAll={...}>
 *       <StoreCard ...>
 *     </StoreSection>
 *   </StorePage>
 */

// 1. StorePage: page-level container.
//
// Provides a unified header and optional mica-like shell.
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

// 2. StoreSection: section header plus content.
//
// Title can be clickable, with an optional right-side control slot.
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

// 3. StoreCard: reusable app-style card.
//
// Density modes:
//   - 'app': 64px icon plus title, subtitle, and CTA
//   - 'compact': 40px icon for smaller list rows
//   - 'feature': 96px icon for featured headers
//
// Accent color is used to create a lightweight logo tile.
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
      className={`card group flex items-center gap-4 ${isCompact ? 'p-3' : 'p-4'} text-left w-full ${onClick ? 'cursor-pointer' : ''} ${className}`}
    >
      {/* Logo tile derived from the accent hue. */}
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

      {/* Main text */}
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

      {/* Trailing content */}
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

// 4. StoreHero: shared page-level hero card.
//
// Used by feature entry pages that need one compact headline surface.
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
        <div className="w-16 h-16 sm:w-20 sm:h-20 rounded-overlay flex items-center justify-center bg-white/15 backdrop-blur-md shrink-0">
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
          className="shrink-0 inline-flex items-center justify-center gap-1.5 h-10 px-5 rounded-control text-sm font-semibold bg-white text-on-surface-variant hover:bg-white/90 active:scale-[0.98] transition"
        >
          {ctaLabel}
          <ArrowUpRight size={14} />
        </button>
      )}
    </div>
  );
}

// 5. StoreGrid: responsive card grid.
export function StoreGrid({ children, columns = 'auto' }) {
  const cls =
    columns === 2 ? 'grid grid-cols-1 md:grid-cols-2 gap-3' :
    columns === 3 ? 'grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-3' :
    columns === 4 ? 'grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4 gap-3' :
    'grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4 gap-3';
  return <div className={cls}>{children}</div>;
}

// hex to rgba helper; kept local to avoid a circular import.
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
