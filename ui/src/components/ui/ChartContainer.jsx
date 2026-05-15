import React, { useMemo } from 'react';
import { useTheme } from '../../context/ThemeContext';

/**
 * ChartContainer: shared shell for Recharts and similar visualizations.
 *
 * Responsibilities:
 *   - card shell, title, actions, and hint
 *   - controlled height, h='sm' | 'md' | 'lg' | number
 *   - consumer-provided chart body
 *   - chart color hook driven by the theme seed
 *
 * Usage:
 *   <ChartContainer title="User Trend" h="md">
 *     <ResponsiveContainer>
 *       <LineChart ...>
 *         <Line stroke={chartColors[0]} ... />
 *       </LineChart>
 *     </ResponsiveContainer>
 *   </ChartContainer>
 */
const HEIGHT_MAP = {
  sm: 240,
  md: 320,
  lg: 420,
};

const ChartContainer = ({
  title,
  sub,
  actions,
  icon: Icon,
  h = 'md',
  children,
  noPadding = false,
  className = '',
}) => {
  const height = typeof h === 'number' ? h : (HEIGHT_MAP[h] || HEIGHT_MAP.md);
  return (
    <section className={`fl-card overflow-hidden ${className}`}>
      {(title || actions) && (
        <header className="px-4 md:px-5 py-3 border-b border-outline-variant/40 flex items-center justify-between gap-3">
          <div className="min-w-0">
            {title && (
              <h3 className="text-sm font-semibold text-on-surface flex items-center gap-2">
                {Icon && <Icon size={14} className="text-primary shrink-0" strokeWidth={1.75} />}
                <span className="truncate">{title}</span>
              </h3>
            )}
            {sub && <p className="text-xs text-on-surface-variant mt-0.5 truncate">{sub}</p>}
          </div>
          {actions && <div className="flex items-center gap-2 shrink-0">{actions}</div>}
        </header>
      )}
      <div
        className={noPadding ? '' : 'p-3 md:p-4'}
        style={{ height: `${height}px` }}
      >
        {children}
      </div>
    </section>
  );
};

// Color helpers (HSL <-> HEX).
//
// HSL hue rotation keeps the series palette simple, stable, and visually separated.

const hexToHsl = (hex) => {
  let h = (hex || '').replace('#', '');
  if (h.length === 3) h = h.split('').map(c => c + c).join('');
  if (h.length !== 6) return [240, 0.6, 0.55];
  const r = parseInt(h.slice(0, 2), 16) / 255;
  const g = parseInt(h.slice(2, 4), 16) / 255;
  const b = parseInt(h.slice(4, 6), 16) / 255;
  const max = Math.max(r, g, b);
  const min = Math.min(r, g, b);
  const l = (max + min) / 2;
  let s = 0; let hue = 0;
  if (max !== min) {
    const d = max - min;
    s = l > 0.5 ? d / (2 - max - min) : d / (max + min);
    if (max === r) hue = ((g - b) / d) + (g < b ? 6 : 0);
    else if (max === g) hue = ((b - r) / d) + 2;
    else hue = ((r - g) / d) + 4;
    hue *= 60;
  }
  return [hue, s, l];
};

const hslToHex = (h, s, l) => {
  h = ((h % 360) + 360) % 360;
  s = Math.max(0, Math.min(1, s));
  l = Math.max(0, Math.min(1, l));
  const c = (1 - Math.abs(2 * l - 1)) * s;
  const x = c * (1 - Math.abs(((h / 60) % 2) - 1));
  const m = l - c / 2;
  let r = 0; let g = 0; let b = 0;
  if (h < 60)       { r = c; g = x; b = 0; }
  else if (h < 120) { r = x; g = c; b = 0; }
  else if (h < 180) { r = 0; g = c; b = x; }
  else if (h < 240) { r = 0; g = x; b = c; }
  else if (h < 300) { r = x; g = 0; b = c; }
  else              { r = c; g = 0; b = x; }
  const toHex = v => Math.round((v + m) * 255).toString(16).padStart(2, '0');
  return '#' + toHex(r) + toHex(g) + toHex(b);
};

/**
 * useChartColors: derive a series palette from the current theme seed.
 *
 * HSL starts from the seed hue and rotates evenly across the requested count.
 */
export const useChartColors = (count = 8) => {
  const theme = useTheme?.() || {};
  const seed = theme.seedColor || '#7c5cff';
  const isDark = theme.isDarkMode ?? true;
  return useMemo(() => {
    const [baseH, baseS] = hexToHsl(seed);
    // Slightly raise saturation and tune lightness per theme mode for readable lines.
    const sat = Math.max(0.55, Math.min(0.85, baseS));
    const lit = isDark ? 0.62 : 0.50;
    const out = [];
    for (let i = 0; i < count; i++) {
      const h = baseH + (360 / count) * i;
      out.push(hslToHex(h, sat, lit));
    }
    return out;
  }, [seed, isDark, count]);
};

export default ChartContainer;
