import React, { useState, useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { Monitor, User, Bell, Package as PackageIcon } from 'lucide-react';
import AccountProfile from './AccountProfile';
import UserCoupons from './UserCoupons';
import NotificationPreferences from './NotificationPreferences';
import { useTheme } from '../context/ThemeContext';

/**
 * Settings — 用户个人设置（Phase 4 完成瘦身）
 *
 * 责任只剩用户视角：外观（主题模式 + 主题色）/ 账号 / 通知偏好 / 我的优惠券。
 * admin 配置全部已迁到独立 page（pages/admin/system/* + pages/admin/finance/*）。
 *
 * 旧 props（initialTab / hideNav / isAdmin / isAuthenticated）现在仅 initialTab 还有用，
 * 其它已退役。保留 isAuthenticated 兼容旧调用。
 */
const SEED_COLORS = [
  { hex: '#7c5cff', name: '紫' },
  { hex: '#2563eb', name: '蓝' },
  { hex: '#059669', name: '青' },
  { hex: '#ea580c', name: '橙' },
  { hex: '#dc2626', name: '红' },
  { hex: '#0891b2', name: '湖' },
  { hex: '#a16207', name: '金' },
  { hex: '#475569', name: '灰' },
];

const Settings = ({ initialTab }) => {
  const { themePref, changeTheme, seedColor, changeSeedColor } = useTheme();
  const { t } = useTranslation();
  const [activeTab, setActiveTab] = useState(initialTab || 'general');

  useEffect(() => {
    if (initialTab) setActiveTab(initialTab);
  }, [initialTab]);

  const userTabs = [
    { id: 'general',            label: t('SETTINGS.TAB_GENERAL', '外观'), icon: Monitor },
    { id: 'account',            label: t('SETTINGS.TAB_ACCOUNT', '账号'), icon: User },
    { id: 'notification_prefs', label: t('SETTINGS.TAB_NOTIFICATION_PREFS', '通知偏好'), icon: Bell },
    { id: 'my_coupons',         label: t('SETTINGS.TAB_MY_COUPONS', '我的优惠券'), icon: PackageIcon },
  ];

  return (
    <div className="w-full min-h-full flex flex-col md:flex-row gap-4 animate-in fade-in slide-in-from-bottom-2">
      {/* 移动端：下拉切换 */}
      <div className="md:hidden -mx-4 px-4 py-3 sticky top-0 z-10 bg-surface/90 backdrop-blur-md border-b border-outline-variant">
        <select
          value={activeTab}
          onChange={(e) => setActiveTab(e.target.value)}
          className="w-full rounded-lg bg-surface-container border border-outline-variant text-on-surface text-sm px-3 py-2"
        >
          {userTabs.map(it => (
            <option key={it.id} value={it.id}>{it.label}</option>
          ))}
        </select>
      </div>

      {/* 桌面端：左侧菜单 */}
      <aside className="hidden md:block fixed left-4 lg:left-5 top-20 bottom-6 z-20 w-48">
        <nav
          aria-label={t('SETTINGS.NAV_LABEL', '设置导航')}
          className="h-full overflow-y-auto bg-surface-container/40 rounded-overlay p-2 space-y-0.5"
        >
          {userTabs.map(it => {
            const Icon = it.icon;
            const isActive = activeTab === it.id;
            return (
              <button
                key={it.id}
                type="button"
                aria-current={isActive ? 'page' : undefined}
                onClick={() => setActiveTab(it.id)}
                className={`w-full h-8 flex items-center gap-2 px-2.5 rounded-lg text-sm transition ${
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

      {/* 主面板 */}
      <div className="flex-1 min-w-0 pb-12 md:ml-52">
        {/* ─── 外观（主题模式 + 主题色）─────────── */}
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

            <div className="bg-surface-container border border-outline-variant rounded-2xl p-4 md:p-6 w-full">
              {/* 主题模式 */}
              <div className="flex flex-col md:flex-row md:items-center justify-between py-4 border-b border-outline-variant/30 gap-4">
                <div className="flex flex-col gap-1">
                  <span className="text-on-surface font-medium">{t('SETTINGS.THEME_LABEL', '外观模式')}</span>
                  <span className="text-xs text-on-surface-variant">{t('SETTINGS.THEME_HINT', '深色 / 浅色 / 跟随系统')}</span>
                </div>
                <div
                  role="radiogroup"
                  aria-label={t('SETTINGS.THEME_LABEL', '外观')}
                  className="inline-flex rounded-lg border border-outline-variant bg-surface p-0.5 self-start md:self-auto"
                >
                  {[
                    { v: 'light',  label: t('SETTINGS.THEME_LIGHT', '浅色') },
                    { v: 'dark',   label: t('SETTINGS.THEME_DARK',  '深色') },
                    { v: 'system', label: t('SETTINGS.THEME_SYSTEM', '跟随系统') },
                  ].map(({ v, label }) => (
                    <button
                      key={v} type="button" role="radio" aria-checked={themePref === v}
                      onClick={() => changeTheme(v)}
                      className={`px-3 py-1.5 text-sm rounded-md transition ${
                        themePref === v
                          ? 'bg-primary text-on-primary font-medium'
                          : 'text-on-surface-variant hover:text-on-surface'
                      }`}
                    >{label}</button>
                  ))}
                </div>
              </div>

              {/* 主题色 */}
              <div className="flex flex-col md:flex-row md:items-center justify-between py-4 gap-4">
                <div className="flex flex-col gap-1">
                  <span className="text-on-surface font-medium">{t('SETTINGS.SEED_COLOR_LABEL', '主题色')}</span>
                  <span className="text-xs text-on-surface-variant">{t('SETTINGS.SEED_COLOR_HINT', '选一个种子色，整套界面调色板自动生成')}</span>
                </div>
                <div className="flex items-center gap-2 flex-wrap">
                  {SEED_COLORS.map(({ hex, name }) => (
                    <button
                      key={hex} type="button" onClick={() => changeSeedColor(hex)}
                      title={name} aria-label={`主题色: ${name}`}
                      className={`w-7 h-7 rounded-full border-2 transition ${
                        seedColor.toLowerCase() === hex.toLowerCase()
                          ? 'border-on-surface scale-110'
                          : 'border-outline-variant hover:scale-110'
                      }`}
                      style={{ background: hex }}
                    />
                  ))}
                  <label
                    className="w-7 h-7 rounded-full border-2 border-dashed border-outline-variant flex items-center justify-center cursor-pointer hover:border-primary text-[10px] text-on-surface-variant"
                    title="自定义"
                  >
                    <input
                      type="color"
                      value={seedColor}
                      onChange={(e) => changeSeedColor(e.target.value)}
                      className="w-0 h-0 opacity-0"
                    />
                    ＋
                  </label>
                </div>
              </div>
            </div>
          </div>
        )}

        {/* ─── 账号 ───────────────────── */}
        {activeTab === 'account' && <AccountProfile />}

        {/* ─── 通知偏好 ─────────────────── */}
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
            <div className="bg-surface-container border border-outline-variant rounded-2xl p-6">
              <NotificationPreferences />
            </div>
          </div>
        )}

        {/* ─── 我的优惠券 ─────────────────── */}
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
