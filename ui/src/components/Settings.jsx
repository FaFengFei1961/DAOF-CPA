import React, { useState, useEffect, useCallback } from 'react';
import { useSearchParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Monitor, User, Bell, Package as PackageIcon, Wallet } from 'lucide-react';
import AccountProfile from './AccountProfile';
import UserCoupons from './UserCoupons';
import NotificationPreferences from './NotificationPreferences';
import BalanceConsumePreferences from './BalanceConsumePreferences';
import { useTheme } from '../context/ThemeContext';
import { SEED_COLORS } from '../utils/theme-seeds';

/**
 * Settings: user-facing personal preferences.
 *
 * Scope is intentionally limited to appearance, account, notification preferences, and coupons.
 * Admin settings live under the dedicated admin pages.
 *
 * Routing contract (IA audit C-1 fix):
 *   - `?tab=<id>` is the source of truth for the active tab.
 *   - OAuth-link, email-verify, and notification deep-links all set this query.
 *   - Without `?tab=`, `initialTab` prop (or 'general') is used.
 *   - Clicking a tab updates the URL so deep-links round-trip.
 */

// Whitelist of valid tab ids. Defined at module scope so the query
// validator is stable across renders.
const VALID_TABS = ['general', 'account', 'consume_prefs', 'notification_prefs', 'my_coupons'];

const seedColorName = (hex, fallbackName, t) => {
  switch (hex.toLowerCase()) {
    case '#7c5cff': return t('SETTINGS.SEED_COLOR_PURPLE', '紫');
    case '#2563eb': return t('SETTINGS.SEED_COLOR_BLUE', '蓝');
    case '#059669': return t('SETTINGS.SEED_COLOR_CYAN', '青');
    case '#ea580c': return t('SETTINGS.SEED_COLOR_ORANGE', '橙');
    case '#dc2626': return t('SETTINGS.SEED_COLOR_RED', '红');
    case '#0891b2': return t('SETTINGS.SEED_COLOR_TEAL', '湖');
    case '#a16207': return t('SETTINGS.SEED_COLOR_GOLD', '金');
    case '#475569': return t('SETTINGS.SEED_COLOR_GRAY', '灰');
    default: return fallbackName || hex;
  }
};

const Settings = ({ initialTab }) => {
  const { themePref, changeTheme, seedColor, changeSeedColor } = useTheme();
  const { t } = useTranslation();
  const [searchParams, setSearchParams] = useSearchParams();

  // Source of truth: URL ?tab=<id>. Falls back to initialTab prop, then 'general'.
  // Validating against VALID_TABS guards against stale links pointing to deleted tabs.
  const queryTab = searchParams.get('tab');
  const resolveTab = useCallback((candidate) => {
    if (candidate && VALID_TABS.includes(candidate)) return candidate;
    return null;
  }, []);

  const [activeTab, setActiveTab] = useState(
    () => resolveTab(queryTab) || resolveTab(initialTab) || 'general'
  );

  // React to URL changes (browser back/forward, deep-link from notification, etc.).
  useEffect(() => {
    const fromQuery = resolveTab(queryTab);
    if (fromQuery && fromQuery !== activeTab) {
      setActiveTab(fromQuery);
    }
    // We intentionally exclude activeTab from the deps so this only reacts to URL changes
    // — the click handler below already writes to the URL, which feeds back into queryTab.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [queryTab, resolveTab]);

  useEffect(() => {
    const fromProp = resolveTab(initialTab);
    if (fromProp && fromProp !== activeTab && !queryTab) {
      setActiveTab(fromProp);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [initialTab, resolveTab]);

  // Tab click writes the new tab to the URL so deep links round-trip.
  // Keep other query params intact (e.g. token from notification action).
  const handleTabChange = useCallback((tabId) => {
    setActiveTab(tabId);
    setSearchParams((prev) => {
      const next = new URLSearchParams(prev);
      next.set('tab', tabId);
      return next;
    }, { replace: true });
  }, [setSearchParams]);

  const userTabs = [
    { id: 'general',            label: t('SETTINGS.TAB_GENERAL', '外观'), icon: Monitor },
    { id: 'account',            label: t('SETTINGS.TAB_ACCOUNT', '账号'), icon: User },
    { id: 'consume_prefs',      label: t('SETTINGS.TAB_CONSUME_PREFS', '消费偏好'), icon: Wallet },
    { id: 'notification_prefs', label: t('SETTINGS.TAB_NOTIFICATION_PREFS', '通知偏好'), icon: Bell },
    { id: 'my_coupons',         label: t('SETTINGS.TAB_MY_COUPONS', '我的优惠券'), icon: PackageIcon },
  ];

  return (
    <div className="w-full min-h-full flex flex-col md:flex-row gap-4 animate-in fade-in slide-in-from-bottom-2">
      {/* Mobile tab selector */}
      <div className="md:hidden -mx-4 px-4 py-3 sticky top-0 z-10 bg-surface/90 backdrop-blur-md border-b border-outline-variant">
        <select
          value={activeTab}
          onChange={(e) => handleTabChange(e.target.value)}
          className="w-full rounded-control bg-surface-container border border-outline-variant text-on-surface text-sm px-3 py-2"
        >
          {userTabs.map(it => (
            <option key={it.id} value={it.id}>{it.label}</option>
          ))}
        </select>
      </div>

      {/* Desktop sticky tab rail inside the Settings layout. */}
      <aside className="hidden md:block w-48 shrink-0">
        <nav
          aria-label={t('SETTINGS.NAV_LABEL', '设置导航')}
          className="sticky top-20 bg-surface-container/40 rounded-overlay p-2 space-y-0.5 max-h-[calc(100vh-6rem)] overflow-y-auto"
        >
          {userTabs.map(it => {
            const Icon = it.icon;
            const isActive = activeTab === it.id;
            return (
              <button
                key={it.id}
                type="button"
                aria-current={isActive ? 'page' : undefined}
                onClick={() => handleTabChange(it.id)}
                className={`w-full h-8 flex items-center gap-2 px-2.5 rounded-control text-sm transition ${
                  isActive
                    ? 'bg-primary-container text-on-primary-container font-medium'
                    : 'text-on-surface-variant hover:bg-surface-container'
                }`}
              >
                <Icon size={16} className={`shrink-0 ${isActive ? 'opacity-100' : 'opacity-70'}`} />
                <span className="truncate">{it.label}</span>
              </button>
            );
          })}
        </nav>
      </aside>

      {/* Main panel */}
      <div className="flex-1 min-w-0 pb-12">
        {/* Appearance */}
        {activeTab === 'general' && (
          <div className="w-full">
            <header className="mb-8 border-b border-outline-variant pb-6">
              <h1 className="text-xl md:text-2xl font-bold tracking-tight text-on-surface">
                {t('SETTINGS.TAB_GENERAL', '外观')}
              </h1>
              <p className="text-on-surface-variant mt-2 text-sm">
                {t('SETTINGS.GENERAL_DESC', '深色 / 浅色模式与主题色，应用到全站界面')}
              </p>
            </header>

            <div className="bg-surface-container border border-outline-variant rounded-overlay p-4 md:p-6 w-full">
              {/* Theme mode */}
              <div className="flex flex-col md:flex-row md:items-center justify-between py-4 border-b border-outline-variant/30 gap-4">
                <div className="flex flex-col gap-1">
                  <span className="text-on-surface font-medium">{t('SETTINGS.THEME_LABEL', '外观模式')}</span>
                  <span className="text-xs text-on-surface-variant">{t('SETTINGS.THEME_HINT', '深色 / 浅色 / 跟随系统')}</span>
                </div>
                <div
                  role="radiogroup"
                  aria-label={t('SETTINGS.THEME_LABEL', '外观')}
                  className="inline-flex rounded-control border border-outline-variant bg-surface p-0.5 self-start md:self-auto"
                >
                  {[
                    { v: 'light',  label: t('SETTINGS.THEME_LIGHT', '浅色') },
                    { v: 'dark',   label: t('SETTINGS.THEME_DARK',  '深色') },
                    { v: 'system', label: t('SETTINGS.THEME_SYSTEM', '跟随系统') },
                  ].map(({ v, label }) => (
                    <button
                      key={v} type="button" role="radio" aria-checked={themePref === v}
                      onClick={() => changeTheme(v)}
                      className={`px-3 py-1.5 text-sm rounded-control transition ${
                        themePref === v
                          ? 'bg-primary text-on-primary font-medium'
                          : 'text-on-surface-variant hover:text-on-surface'
                      }`}
                    >{label}</button>
                  ))}
                </div>
              </div>

              {/* Seed color */}
              <div className="flex flex-col md:flex-row md:items-center justify-between py-4 gap-4">
                <div className="flex flex-col gap-1">
                  <span className="text-on-surface font-medium">{t('SETTINGS.SEED_COLOR_LABEL', '主题色')}</span>
                  <span className="text-xs text-on-surface-variant">{t('SETTINGS.SEED_COLOR_HINT', '选一个种子色，整套界面调色板自动生成')}</span>
                </div>
                <div className="flex items-center gap-2 flex-wrap">
                  {SEED_COLORS.map(({ hex, name }) => {
                    const label = seedColorName(hex, name, t);
                    return (
                      <button
                        key={hex} type="button" onClick={() => changeSeedColor(hex)}
                        title={label}
                        aria-label={t('SETTINGS.SEED_COLOR_ARIA', { name: label, defaultValue: '主题色: {{name}}' })}
                        className={`w-7 h-7 rounded-full border-2 transition ${
                          seedColor.toLowerCase() === hex.toLowerCase()
                            ? 'border-on-surface scale-110'
                            : 'border-outline-variant hover:scale-110'
                        }`}
                        style={{ background: hex }}
                      />
                    );
                  })}
                  <label
                    className="w-7 h-7 rounded-full border-2 border-dashed border-outline-variant flex items-center justify-center cursor-pointer hover:border-primary text-[10px] text-on-surface-variant"
                    title={t('SETTINGS.SEED_COLOR_CUSTOM', '自定义')}
                  >
                    <input
                      type="color"
                      value={seedColor}
                      onChange={(e) => changeSeedColor(e.target.value)}
                      className="w-0 h-0 opacity-0"
                    />
                    +
                  </label>
                </div>
              </div>
            </div>
          </div>
        )}

        {/* Account */}
        {activeTab === 'account' && <AccountProfile />}

        {activeTab === 'consume_prefs' && (
          <div className="w-full">
            <header className="mb-8 border-b border-outline-variant pb-6">
              <h1 className="text-xl md:text-2xl font-bold tracking-tight text-on-surface flex items-center gap-3">
                <Wallet size={22} className="text-primary" />
                {t('SETTINGS.TAB_CONSUME_PREFS', '消费偏好')}
              </h1>
              <p className="text-on-surface-variant mt-2 text-sm">
                {t('SETTINGS.CONSUME_PREFS_DESC', '订阅用尽后是否允许从美元余额继续扣费 + 周期限额控制')}
              </p>
            </header>
            <BalanceConsumePreferences />
          </div>
        )}

        {/* Notification preferences */}
        {activeTab === 'notification_prefs' && (
          <div className="w-full">
            <header className="mb-8 border-b border-outline-variant pb-6">
              <h1 className="text-xl md:text-2xl font-bold tracking-tight text-on-surface flex items-center gap-3">
                <Bell size={22} className="text-primary" />
                {t('SETTINGS.TAB_NOTIFICATION_PREFS', '通知偏好')}
              </h1>
              <p className="text-on-surface-variant mt-2 text-sm">
                {t('SETTINGS.NOTIFICATION_PREFS_DESC', '配置站内铃铛、邮件、短信等通知渠道的接收偏好')}
              </p>
            </header>
            <div className="bg-surface-container border border-outline-variant rounded-overlay p-6">
              <NotificationPreferences />
            </div>
          </div>
        )}

        {/* User coupons */}
        {activeTab === 'my_coupons' && (
          <div className="w-full">
            <header className="mb-8 border-b border-outline-variant pb-6">
              <h1 className="text-xl md:text-2xl font-bold tracking-tight text-on-surface flex items-center gap-3">
                <PackageIcon size={22} className="text-primary" />
                {t('COUPON.MY_TITLE', '我的优惠券')}
              </h1>
              <p className="text-on-surface-variant mt-2 text-sm max-w-2xl">
                {t('COUPON.MY_DESC', '查看你账户下的所有优惠券。可用券会在购买套餐时自动出现在选择列表里。')}
              </p>
            </header>
            <UserCoupons />
          </div>
        )}
      </div>
    </div>
  );
};

export default Settings;
