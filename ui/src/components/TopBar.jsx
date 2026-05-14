import React, { useState, useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate, Link } from 'react-router-dom';
import { User, LogOut, Globe, Search, ShieldAlert } from 'lucide-react';
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

      {/* 右：控件簇 */}
      <div className="flex items-center gap-1 ml-auto shrink-0">
        {/* 通知中心：所有人都看得到（未登录时点开会引导登录） */}
        <NotificationCenter
          isAuthenticated={isAuthenticated || isAdmin}
          onSignIn={onOpenAuth}
        />


        {locales.length > 1 && (
          <div className="relative group">
            <button
              type="button"
              className="flex items-center gap-1.5 text-on-surface-variant hover:text-on-surface px-2 h-8 rounded hover:bg-on-surface/[0.04] text-sm transition"
              aria-label={t('TOPBAR.LANG', '语言')}
            >
              <Globe size={14} />
              <span className="hidden sm:inline text-xs">
                {locales.find((l) => l.id === i18n.language)?.name || i18n.language.toUpperCase()}
              </span>
            </button>
            <div className="absolute top-full right-0 pt-1.5 w-32 opacity-0 invisible group-hover:opacity-100 group-hover:visible z-[100]">
              <div className="bg-surface-container-high border border-outline-variant rounded-lg shadow-xl overflow-hidden py-1">
                {locales.map((loc) => (
                  <button
                    key={loc.id}
                    type="button"
                    onClick={() => i18n.changeLanguage(loc.id)}
                    className={`w-full text-left px-3 py-1.5 text-sm transition ${
                      i18n.language === loc.id
                        ? 'bg-primary text-on-primary font-medium'
                        : 'text-on-surface-variant hover:bg-on-surface/[0.04] hover:text-on-surface'
                    }`}
                  >
                    {loc.name}
                  </button>
                ))}
              </div>
            </div>
          </div>
        )}

        <button
          type="button"
          onClick={toggleCurrency}
          className="flex items-center justify-center w-8 h-8 rounded text-on-surface-variant hover:text-on-surface hover:bg-on-surface/[0.04] font-medium text-sm transition"
          title={t('TOPBAR.TOGGLE_CURRENCY')}
          aria-label={t('TOPBAR.TOGGLE_CURRENCY')}
        >
          {displayCurrency === 'USD' ? '$' : '￥'}
        </button>

        {isAuthenticated || isAdmin ? (
          <div className="flex items-center gap-1 ml-1 pl-2 border-l border-outline-variant/60">
            {profile && profile.role !== 'admin' && (
              <div className="hidden lg:flex flex-col items-end leading-tight pr-2">
                <span className="text-[10px] text-on-surface-variant uppercase tracking-wider">
                  {t('TOPBAR.BALANCE')}
                </span>
                <span className="text-sm font-semibold text-on-surface tabular-nums">
                  {formatCurrency(profile.quota)}
                </span>
              </div>
            )}
            {isAdmin ? (
              // 重构后 admin 拆到 /admin/* 独立路由树。原"管理员"chip 仅文字无入口
              // 导致 admin 登录后陷在用户视图出不去；改成显式 Link + ShieldAlert
              // 强提示，与 AdminSidebar 顶部"返回用户视图"形成对称切换入口。
              <Link
                to="/admin"
                className="flex items-center gap-1.5 h-8 px-2.5 rounded bg-fuchsia-500/15 text-fuchsia-300 border border-fuchsia-500/30 hover:bg-fuchsia-500/25 transition"
                title={t('TOPBAR.ENTER_ADMIN', '进入管理后台')}
              >
                <ShieldAlert size={14} />
                <span className="hidden sm:inline text-sm font-medium">{t('TOPBAR.ADMIN')}</span>
              </Link>
            ) : (
              <div className="flex items-center gap-2 px-2 h-8 rounded hover:bg-on-surface/[0.04] transition">
                <span className="hidden sm:inline text-sm text-on-surface truncate max-w-[120px]">
                  {profile?.username || ''}
                </span>
                <div className="w-6 h-6 rounded-full bg-primary text-on-primary flex items-center justify-center shrink-0">
                  <User size={12} />
                </div>
              </div>
            )}
            <button
              type="button"
              onClick={handleLogout}
              className="text-on-surface-variant hover:text-error w-8 h-8 rounded hover:bg-error/10 flex items-center justify-center transition"
              title={t('TOPBAR.LOGOUT_TOOLTIP')}
              aria-label={t('TOPBAR.LOGOUT_TOOLTIP')}
            >
              <LogOut size={14} />
            </button>
          </div>
        ) : (
          <div className="flex items-center gap-1 ml-1 pl-2 border-l border-outline-variant/60">
            <button
              type="button"
              onClick={onOpenAuth}
              className="fl-btn fl-btn-subtle h-8"
            >
              {t('TOPBAR.LOGIN')}
            </button>
            <button
              type="button"
              onClick={onOpenAuth}
              className="fl-btn fl-btn-prominent h-8"
            >
              {t('TOPBAR.REGISTER')}
            </button>
          </div>
        )}
      </div>
    </header>
  );
};

export default TopBar;
