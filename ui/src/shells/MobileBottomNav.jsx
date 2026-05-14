import React, { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate, useLocation } from 'react-router-dom';
import { MoreHorizontal, X } from 'lucide-react';
import { mobileBottomNav, mobileMoreNav } from '../navManifest';

/**
 * MobileBottomNav — 移动端底部固定 6 格 + "更多"面板
 *
 * Phase 0 重构：从 App.jsx 内移出来，菜单从 routes.jsx 读。
 * 视觉规则保持原 App.jsx 实现（高 60px、bg-surface/95 + backdrop-blur）。
 */
const MobileBottomNav = () => {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const location = useLocation();
  const [moreOpen, setMoreOpen] = useState(false);

  // ESC 关闭"更多"面板
  useEffect(() => {
    if (!moreOpen) return undefined;
    const handler = (e) => { if (e.key === 'Escape') setMoreOpen(false); };
    window.addEventListener('keydown', handler);
    return () => window.removeEventListener('keydown', handler);
  }, [moreOpen]);

  const moreActive = mobileMoreNav.some(it => location.pathname === it.path);

  return (
    <>
      {/* 更多面板 */}
      {moreOpen && (
        <div className="lg:hidden fixed inset-0 z-[95]" role="presentation">
          <button
            type="button"
            aria-label={t('COMMON.CLOSE', '关闭')}
            onClick={() => setMoreOpen(false)}
            className="absolute inset-0 w-full h-full bg-black/45"
          />
          <section
            role="dialog"
            aria-modal="true"
            aria-labelledby="mobile-more-title"
            className="absolute left-3 right-3 bottom-[72px] rounded-2xl border border-outline-variant bg-surface-container shadow-2xl overflow-hidden animate-in fade-in slide-in-from-bottom-2"
          >
            <div className="flex items-center justify-between px-4 py-3 border-b border-outline-variant/60">
              <h2 id="mobile-more-title" className="text-sm font-semibold text-on-surface">
                {t('MENU.MORE', '更多')}
              </h2>
              <button
                type="button"
                onClick={() => setMoreOpen(false)}
                aria-label={t('COMMON.CLOSE', '关闭')}
                className="w-8 h-8 rounded-lg flex items-center justify-center text-on-surface-variant hover:bg-on-surface/[0.06] hover:text-on-surface"
              >
                <X size={18} />
              </button>
            </div>
            <div className="grid grid-cols-2 gap-2 p-3">
              {mobileMoreNav.map(item => {
                const Icon = item.icon;
                const active = location.pathname === item.path;
                return (
                  <button
                    key={item.id}
                    type="button"
                    onClick={() => {
                      navigate(item.path);
                      setMoreOpen(false);
                    }}
                    aria-current={active ? 'page' : undefined}
                    className={`min-h-16 rounded-xl border px-3 py-3 text-left flex items-center gap-3 transition active:scale-[0.98] focus-visible:ring-2 focus-visible:ring-primary ${
                      active
                        ? 'bg-primary-container border-primary/40 text-on-primary-container'
                        : 'bg-surface-container-high border-outline-variant text-on-surface hover:border-primary/60'
                    }`}
                  >
                    <Icon size={20} className={active ? 'text-primary' : 'text-on-surface-variant'} />
                    <span className="text-sm font-medium leading-tight">{t(item.labelKey, item.labelFallback)}</span>
                  </button>
                );
              })}
            </div>
          </section>
        </div>
      )}

      {/* 底部固定 6 格 */}
      <nav
        aria-label={t('MOBILE_NAV.BOTTOM_LABEL', '底部导航')}
        className="lg:hidden fixed bottom-0 left-0 right-0 h-[60px] bg-surface/95 backdrop-blur-md border-t border-outline-variant flex items-center justify-around z-[100] pb-1"
      >
        {mobileBottomNav.map(item => {
          const Icon = item.icon;
          const active = location.pathname === item.path;
          return (
            <button
              key={item.id}
              type="button"
              onClick={() => navigate(item.path)}
              aria-label={t(item.labelKey, item.labelFallback)}
              aria-current={active ? 'page' : undefined}
              className="flex flex-col items-center gap-1 p-2 cursor-pointer transition-transform active:scale-95 bg-transparent border-0 outline-none focus-visible:ring-2 focus-visible:ring-primary rounded-md"
            >
              <Icon size={22} className={active ? 'text-primary' : 'text-on-surface-variant'} />
              <span className={`text-[10px] font-medium leading-none ${active ? 'text-primary' : 'text-on-surface-variant'}`}>
                {t(item.labelKey, item.labelFallback)}
              </span>
            </button>
          );
        })}
        {/* 第 6 格：更多 */}
        <button
          type="button"
          onClick={() => setMoreOpen(o => !o)}
          aria-label={t('MENU.MORE', '更多')}
          aria-expanded={moreOpen}
          className="flex flex-col items-center gap-1 p-2 cursor-pointer transition-transform active:scale-95 bg-transparent border-0 outline-none focus-visible:ring-2 focus-visible:ring-primary rounded-md"
        >
          <MoreHorizontal size={22} className={moreActive || moreOpen ? 'text-primary' : 'text-on-surface-variant'} />
          <span className={`text-[10px] font-medium leading-none ${moreActive || moreOpen ? 'text-primary' : 'text-on-surface-variant'}`}>
            {t('MENU.MORE', '更多')}
          </span>
        </button>
      </nav>
    </>
  );
};

export default MobileBottomNav;
