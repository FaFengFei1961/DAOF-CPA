import React from 'react';
import { useTranslation } from 'react-i18next';
import { Home, KeySquare, CreditCard, Settings as SettingsIcon, BarChart2, Package, Sparkles, Wallet, MessageSquare, Receipt } from 'lucide-react';

/**
 * Fluent NavigationView (Compact Pane) — 严格按 Microsoft Store / Win11 Settings 风格。
 *
 * 视觉规则（来自 Fluent 2 + Win11 Settings 实测）：
 *  - 宽度 64px（compact pane 标准）
 *  - 项高度 40px，图标 20px，标签 11px caption
 *  - active：主色矩形指示条 (3×16px) 在最左 + 项内填充 primary-container
 *  - hover：subtle 表面（rgba(255,255,255,0.04) 在 dark），无边框
 *  - 全部圆角 6px
 */
const NavItem = ({ id, currentView, onNav, label, Icon }) => {
  const isActive = currentView === id;
  return (
    <li className="relative">
      {/* 左侧 active 指示条 — Fluent 标准 3×16 圆头矩形 */}
      {isActive && (
        <span
          aria-hidden
          className="absolute left-0 top-1/2 -translate-y-1/2 w-[3px] h-4 bg-primary rounded-full"
        />
      )}
      <button
        type="button"
        onClick={() => onNav(id)}
        title={label}
        aria-label={label}
        aria-current={isActive ? 'page' : undefined}
        className={`group w-full flex flex-col items-center gap-0.5 px-1 py-2 rounded transition
          ${
            isActive
              ? 'bg-primary-container/60 text-on-surface'
              : 'text-on-surface-variant hover:bg-on-surface/[0.04] hover:text-on-surface'
          }`}
      >
        <Icon size={20} strokeWidth={isActive ? 2.25 : 1.75} />
        <span className={`text-[10px] leading-tight ${isActive ? 'font-semibold' : 'font-normal'}`}>
          {label}
        </span>
      </button>
    </li>
  );
};

const Sidebar = ({ currentView, onNav, isAdmin }) => {
  const { t } = useTranslation();

  // 注意：「我的产品」已并入「产品中心」（UpgradePage 内部 mine/store 一级 tab）
  // sidebar 不再独立展示，避免冗余。
  const userItems = [
    { id: 'dashboard',     icon: Home,       label: t('MENU.DASHBOARD') },
    { id: 'tokens',        icon: KeySquare,  label: t('MENU.TOKENS') },
    { id: 'stats',         icon: BarChart2,  label: t('MENU.STATS') },
    { id: 'pricing',       icon: CreditCard, label: t('MENU.PRICING', '定价') },
    { id: 'upgrade',       icon: Package,    label: t('MENU.PRODUCTS', '产品中心') },
    { id: 'topup',         icon: Wallet,     label: t('MENU.TOPUP', '充值') },
    { id: 'bills',         icon: Receipt,    label: t('MENU.BILLS', '账单') },
    { id: 'tickets',       icon: MessageSquare, label: t('MENU.TICKETS', '工单') },
  ];

  return (
    <nav
      aria-label="主导航"
      className="hidden md:flex flex-col w-16 h-screen fl-mica items-center py-2 border-r border-outline-variant/40 fixed top-0 left-0 z-50"
    >
      {/* App Logo（同时是回 Dashboard 的快捷点） */}
      <button
        type="button"
        onClick={() => onNav('dashboard')}
        className="w-10 h-10 rounded overflow-hidden mb-2 hover:bg-on-surface/[0.04] flex items-center justify-center transition"
        aria-label="DAOF-CPA"
      >
        <img src="/daof_logo.png" alt="DAOF-CPA" className="w-7 h-7 rounded" />
      </button>

      <ul className="flex-1 w-full flex flex-col gap-1 px-1 overflow-y-auto no-scrollbar">
        {!isAdmin && userItems.map((item) => (
          <NavItem
            key={item.id}
            id={item.id}
            currentView={currentView}
            onNav={onNav}
            label={item.label}
            Icon={item.icon}
          />
        ))}
      </ul>

      {/* 设置永久固定底部 */}
      <ul className="w-full px-1 pt-2 border-t border-outline-variant/40">
        <NavItem
          id="settings"
          currentView={currentView}
          onNav={onNav}
          label={t('MENU.SETTINGS')}
          Icon={SettingsIcon}
        />
      </ul>
    </nav>
  );
};

export default Sidebar;
