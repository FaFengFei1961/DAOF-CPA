import React, { useState, useEffect, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate, Link } from 'react-router-dom';
import { User, LogOut, Globe, Search, ShieldAlert, ChevronDown, DollarSign, Check } from 'lucide-react';
import { useCurrency } from '../context/CurrencyContext';
import { useConfirm } from '../context/ConfirmContext';
import { logger } from '../utils/logger';
import NotificationCenter from './NotificationCenter';

/**
 * Microsoft Store / Win11 Settings 风格 CommandBar。
 *
 * 视觉规则：
 *  - 高度 48px（Win11 Mica 顶栏标准）
 *  - 中央居中搜索框（占 max-w-2xl），仅桌面端显示
 *  - 右侧紧凑控件簇：通知 / 语言 / 货币 / 余额 / 头像 / 退出
 *  - 控件统一 32px 高，rounded (8px)，hover 时 subtle bg
 *  - 移动端：左侧露 logo + 应用名，右侧只保留头像 / 登录
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

  // 头像菜单点外部关闭（MS Store 行为）
  useEffect(() => {
    if (!menuOpen) return;
    const onClick = (e) => {
      if (menuRef.current && !menuRef.current.contains(e.target)) setMenuOpen(false);
    };
    document.addEventListener('mousedown', onClick);
    return () => document.removeEventListener('mousedown', onClick);
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

  // ⌘K / Ctrl+K：聚焦搜索框，对齐 Microsoft Store 的全局搜索快捷键
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
    <header className="h-12 flex items-center gap-2 px-3 sm:px-4 fl-mica w-full shrink-0 sticky top-0 z-40 border-b border-outline-variant/40">
      {/* 左：移动端 Logo */}
      <div className="flex items-center gap-2 md:hidden shrink-0">
        <img src="/daof_logo.png" alt="" className="w-7 h-7 rounded" />
        <span className="text-sm font-semibold text-on-surface">DAOF-CPA</span>
      </div>

      {/* 中：搜索框（MS Store 标志性元素） — acrylic 胶囊 + ⌘K 快捷键提示 */}
      <form onSubmit={onSearchSubmit} className="hidden md:flex flex-1 max-w-2xl mx-auto relative">
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
          className="w-full h-9 fl-acrylic rounded-overlay pl-9 pr-14 text-sm text-on-surface placeholder:text-on-surface-variant outline-none focus:border-primary transition-colors"
        />
        <kbd className="hidden lg:flex absolute right-3 top-1/2 -translate-y-1/2 items-center gap-0.5 h-5 px-1.5 rounded text-[10px] font-mono text-on-surface-variant bg-on-surface/5 border border-outline-variant/60 pointer-events-none">
          <span>⌘</span><span>K</span>
        </kbd>
      </form>

      {/* 右：控件簇 — Phase 7 极简化（MS Store 风格）
          原 6 个独立按钮（通知/语言/货币/余额/admin/退出）压缩为：
          通知 + Admin 入口（仅 admin）+ 头像菜单 + Login/Register（未登录）
          语言/货币/退出全收进头像 dropdown */}
      <div className="flex items-center gap-1 ml-auto shrink-0">
        <NotificationCenter
          isAuthenticated={isAuthenticated || isAdmin}
          onSignIn={onOpenAuth}
        />

        {isAdmin && (
          <Link
            to="/admin"
            className="flex items-center gap-1.5 h-8 px-2.5 rounded bg-fuchsia-500/15 text-fuchsia-300 border border-fuchsia-500/30 hover:bg-fuchsia-500/25 transition"
            title={t('TOPBAR.ENTER_ADMIN', '进入管理后台')}
          >
            <ShieldAlert size={14} />
            <span className="hidden sm:inline text-sm font-medium">{t('TOPBAR.ADMIN')}</span>
          </Link>
        )}

        {isAuthenticated || isAdmin ? (
          <div ref={menuRef} className="relative ml-1">
            <button
              type="button"
              onClick={() => setMenuOpen(v => !v)}
              className="flex items-center gap-1.5 h-8 pl-1 pr-2 rounded hover:bg-on-surface/[0.04] transition"
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
                className="absolute right-0 top-full mt-2 w-72 bg-surface-container-high border border-outline-variant rounded-overlay shadow-xl shadow-black/40 z-[100] overflow-hidden"
              >
                {/* Header: 用户名 + role + 余额 */}
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

                {/* 偏好：货币 + 语言 */}
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
                              className={`flex items-center gap-1.5 px-2 py-1.5 rounded text-xs transition ${
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

                {/* 退出 */}
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
