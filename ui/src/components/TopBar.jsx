import React, { useState, useEffect, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate, Link } from 'react-router-dom';
import { User, LogOut, Globe, Search, ShieldAlert, ChevronDown, DollarSign, Check } from 'lucide-react';
import { useCurrency } from '../context/CurrencyContext';
import { useConfirm } from '../context/ConfirmContext';
import { logger } from '../utils/logger';
import NotificationCenter from './NotificationCenter';

/**
 * Microsoft Store / Windows 11 Settings style command bar.
 *
 * Visual rules:
 *  - 48px height
 *  - centered desktop search box
 *  - compact right-side controls
 *  - 32px controls with subtle hover states
 *  - mobile keeps only logo, app name, and account or sign-in controls
 */
const TopBar = ({ isAuthenticated, onOpenAuth, isAdmin, profile }) => {
  const confirm = useConfirm();
  const { t, i18n } = useTranslation();
  const navigate = useNavigate();
  const { displayCurrency, toggleCurrency, formatCurrency } = useCurrency();
  const [locales, setLocales] = useState([]);
  const [searchQ, setSearchQ] = useState('');
  const [menuOpen, setMenuOpen] = useState(false);
  const menuRef = useRef(null);
  const menuTriggerRef = useRef(null);

  // Account menu a11y:
  // - close on outside click
  // - close on Escape and restore focus to the trigger
  // - focus the first interactive menu item after opening
  useEffect(() => {
    if (!menuOpen) return;
    const onClick = (e) => {
      if (menuRef.current && !menuRef.current.contains(e.target)) setMenuOpen(false);
    };
    const onKey = (e) => {
      if (e.key === 'Escape') {
        e.preventDefault();
        setMenuOpen(false);
        menuTriggerRef.current?.focus();
      }
    };
    document.addEventListener('mousedown', onClick);
    document.addEventListener('keydown', onKey);
    const focusId = requestAnimationFrame(() => {
      const firstFocusable = menuRef.current?.querySelector(
        'button:not([disabled]), a[href]'
      );
      firstFocusable?.focus();
    });
    return () => {
      document.removeEventListener('mousedown', onClick);
      document.removeEventListener('keydown', onKey);
      cancelAnimationFrame(focusId);
    };
  }, [menuOpen]);

  useEffect(() => {
    fetch('/api/i18n/locales')
      .then((r) => (r.ok ? r.json() : null))
      .then((d) => {
        if (d?.success && d.data) setLocales(d.data);
      })
      .catch((e) => {
        logger.warn('[i18n] locale list fetch failed', e);
      });
  }, []);

  const handleLogout = async () => {
    if (!(await confirm(t('TOPBAR.LOGOUT_CONFIRM')))) return;
    try {
      localStorage.removeItem('daof_token');
      localStorage.removeItem('daof_admin_unlocked');
      await fetch('/api/root/logout', { method: 'POST', credentials: 'include' }).catch(() => {});
    } finally {
      window.location.reload();
    }
  };

  const onSearchSubmit = (e) => {
    e.preventDefault();
    const q = searchQ.trim();
    if (!q) return;
    navigate(`/pricing?q=${encodeURIComponent(q)}`);
  };

  // Focus search with Cmd+K / Ctrl+K.
  useEffect(() => {
    const onKey = (e) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault();
        document.getElementById('topbar-search')?.focus();
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);

  return (
    <header className="h-12 flex items-center gap-2 px-3 sm:px-4 bg-surface/85 backdrop-blur-md w-full shrink-0 sticky top-0 z-40 border-b border-outline-variant/40">
      {/* Mobile logo */}
      <div className="flex items-center gap-2 lg:hidden shrink-0">
        <img src="/daof_logo.png" alt="" className="w-7 h-7 rounded-control" />
        <span className="text-sm font-semibold text-on-surface">DAOF-CPA</span>
      </div>

      {/* Desktop search box */}
      <form onSubmit={onSearchSubmit} className="hidden lg:flex flex-1 max-w-2xl mx-auto relative">
        <label htmlFor="topbar-search" className="sr-only">
          {t('TOPBAR.SEARCH_PLACEHOLDER', '搜索模型、套餐')}
        </label>
        <Search
          size={14}
          className="absolute left-3.5 top-1/2 -translate-y-1/2 text-on-surface-variant pointer-events-none z-10"
        />
        <input
          id="topbar-search"
          type="text"
          value={searchQ}
          onChange={(e) => setSearchQ(e.target.value)}
          placeholder={t('TOPBAR.SEARCH_PLACEHOLDER', '搜索模型、套餐')}
          className="w-full h-9 bg-surface-container/60 border border-outline-variant/60 rounded-overlay pl-9 pr-14 text-sm text-on-surface placeholder:text-on-surface-variant outline-none focus:border-primary focus:bg-surface-container transition-colors"
        />
        <kbd className="hidden lg:flex absolute right-3 top-1/2 -translate-y-1/2 items-center gap-0.5 h-5 px-1.5 rounded-control text-[10px] font-mono text-on-surface-variant bg-on-surface/5 border border-outline-variant/60 pointer-events-none">
          <span>⌘</span><span>K</span>
        </kbd>
      </form>

      {/* Right-side control cluster */}
      <div className="flex items-center gap-1 ml-auto shrink-0">
        <NotificationCenter
          isAuthenticated={isAuthenticated || isAdmin}
          onSignIn={onOpenAuth}
        />

        {isAdmin && (
          <Link
            to="/admin"
            className="flex items-center gap-1.5 h-8 px-2.5 rounded-control border border-outline-variant text-on-surface-variant hover:bg-on-surface/[0.04] hover:text-on-surface transition"
            title={t('TOPBAR.ENTER_ADMIN', '进入管理后台')}
          >
            <ShieldAlert size={14} />
            <span className="hidden sm:inline text-sm font-medium">{t('TOPBAR.ADMIN')}</span>
          </Link>
        )}

        {isAuthenticated || isAdmin ? (
          <div ref={menuRef} className="relative ml-1">
            <button
              ref={menuTriggerRef}
              type="button"
              onClick={() => setMenuOpen(v => !v)}
              className="flex items-center gap-1.5 h-8 pl-1 pr-2 rounded-control hover:bg-on-surface/[0.04] transition"
              aria-haspopup="menu"
              aria-expanded={menuOpen}
              aria-label={t('TOPBAR.ACCOUNT_MENU', '账户菜单')}
            >
              <div className="w-7 h-7 rounded-full bg-primary text-on-primary flex items-center justify-center shrink-0">
                <User size={13} />
              </div>
              <ChevronDown size={12} className={`text-on-surface-variant transition-transform ${menuOpen ? 'rotate-180' : ''}`} />
            </button>

            {menuOpen && (
              <div
                role="menu"
                className="absolute right-0 top-full mt-2 w-72 bg-surface-container-high border border-outline-variant rounded-overlay shadow-black/40 z-[100] overflow-hidden"
              >
                {/* Header: username, role, and balance */}
                <div className="px-4 py-3 border-b border-outline-variant/40">
                  <div className="flex items-center gap-3">
                    <div className="w-10 h-10 rounded-full bg-primary text-on-primary flex items-center justify-center">
                      <User size={18} />
                    </div>
                    <div className="min-w-0 flex-1">
                      <div className="text-sm font-semibold text-on-surface truncate">
                        {isAdmin ? t('TOPBAR.ADMIN') : profile?.username || ''}
                      </div>
                      {profile && profile.role !== 'admin' && (
                        <div className="text-xs text-on-surface-variant tabular-nums mt-0.5">
                          {t('TOPBAR.BALANCE')}: <span className="font-semibold text-on-surface">{formatCurrency(profile.quota)}</span>
                        </div>
                      )}
                    </div>
                  </div>
                </div>

                {/* Preferences: currency and language */}
                <div className="py-1">
                  <button
                    type="button"
                    onClick={toggleCurrency}
                    role="menuitem"
                    className="w-full flex items-center gap-2.5 px-4 py-2 text-sm text-on-surface hover:bg-on-surface/[0.06] transition"
                  >
                    <DollarSign size={15} className="text-on-surface-variant" />
                    <span className="flex-1 text-left">{t('TOPBAR.TOGGLE_CURRENCY')}</span>
                    <span className="text-xs text-on-surface-variant font-mono">{displayCurrency}</span>
                  </button>

                  {locales.length > 1 && (
                    <div className="px-4 py-2">
                      <div className="flex items-center gap-2.5 mb-1.5">
                        <Globe size={15} className="text-on-surface-variant" />
                        <span className="text-sm text-on-surface">{t('TOPBAR.LANG', '语言')}</span>
                      </div>
                      <div className="grid grid-cols-2 gap-1 mt-1.5">
                        {locales.map((loc) => {
                          const active = i18n.language === loc.id;
                          return (
                            <button
                              key={loc.id}
                              type="button"
                              onClick={() => i18n.changeLanguage(loc.id)}
                              role="menuitemradio"
                              aria-checked={active}
                              className={`flex items-center gap-1.5 px-2 py-1.5 rounded-control text-xs transition ${
                                active
                                  ? 'bg-primary/15 text-primary font-semibold border border-primary/30'
                                  : 'text-on-surface-variant hover:bg-on-surface/[0.06] border border-transparent'
                              }`}
                            >
                              {active && <Check size={11} />}
                              <span className="truncate">{loc.name}</span>
                            </button>
                          );
                        })}
                      </div>
                    </div>
                  )}
                </div>

                {/* Logout */}
                <div className="border-t border-outline-variant/40 py-1">
                  <button
                    type="button"
                    onClick={() => { setMenuOpen(false); handleLogout(); }}
                    role="menuitem"
                    className="w-full flex items-center gap-2.5 px-4 py-2 text-sm text-on-surface hover:bg-error/10 hover:text-error transition"
                  >
                    <LogOut size={15} />
                    <span>{t('TOPBAR.LOGOUT_TOOLTIP')}</span>
                  </button>
                </div>
              </div>
            )}
          </div>
        ) : (
          <div className="flex items-center gap-1 ml-1 pl-2 border-l border-outline-variant/60">
            <button type="button" onClick={onOpenAuth} className="fl-btn fl-btn-subtle h-8">
              {t('TOPBAR.LOGIN')}
            </button>
            <button type="button" onClick={onOpenAuth} className="fl-btn fl-btn-prominent h-8">
              {t('TOPBAR.REGISTER')}
            </button>
          </div>
        )}
      </div>
    </header>
  );
};

export default TopBar;
