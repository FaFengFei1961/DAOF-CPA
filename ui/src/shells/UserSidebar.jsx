import React from 'react';
import { useTranslation } from 'react-i18next';
import { Link, NavLink } from 'react-router-dom';
import { Settings as SettingsIcon } from 'lucide-react';
import { userNav } from '../navManifest';

/**
 * UserSidebar — 用户侧 Fluent NavigationView (compact pane 64px)
 *
 * Phase 0 重构：
 *  - 替换原 components/Sidebar.jsx（含 isAdmin 分支）
 *  - 用 React Router NavLink，去掉 currentView prop
 *  - 菜单从 routes.jsx#userNav 读，单一来源
 *  - 视觉规则不变（Fluent NavigationView 4px 圆角 + 左侧 active 指示条）
 */
const NavItem = ({ to, label, Icon }) => {
  // index 路径 '/' 用 end 严格匹配，否则任何子路径都会显示 active
  const end = to === '/';
  return (
    <NavLink
      to={to}
      end={end}
      title={label}
      aria-label={label}
      className={({ isActive }) =>
        `relative w-full flex flex-col items-center gap-0.5 px-1 py-2 rounded transition
         ${isActive
           ? 'bg-primary-container/60 text-on-surface'
           : 'text-on-surface-variant hover:bg-on-surface/[0.04] hover:text-on-surface'}`
      }
    >
      {({ isActive }) => (
        <>
          {isActive && (
            <span
              aria-hidden
              className="absolute left-0 top-1/2 -translate-y-1/2 w-[3px] h-4 bg-primary rounded-full"
            />
          )}
          <Icon size={20} strokeWidth={isActive ? 2.25 : 1.75} />
          <span className={`text-[10px] leading-tight ${isActive ? 'font-semibold' : 'font-normal'}`}>
            {label}
          </span>
        </>
      )}
    </NavLink>
  );
};

const UserSidebar = () => {
  const { t } = useTranslation();

  return (
    <nav
      aria-label="主导航"
      className="hidden md:flex flex-col w-16 h-screen fl-mica items-center py-2 border-r border-outline-variant/40 fixed top-0 left-0 z-50"
    >
      {/* App Logo（同时是回 Dashboard 的快捷点） */}
      <Link
        to="/"
        className="w-10 h-10 rounded overflow-hidden mb-2 hover:bg-on-surface/[0.04] flex items-center justify-center transition"
        aria-label="DAOF-CPA"
      >
        <img src="/daof_logo.png" alt="DAOF-CPA" className="w-7 h-7 rounded" />
      </Link>

      <ul className="flex-1 w-full flex flex-col gap-1 px-1 overflow-y-auto no-scrollbar">
        {userNav.map(item => (
          <li key={item.id}>
            <NavItem to={item.path} label={t(item.labelKey, item.labelFallback)} Icon={item.icon} />
          </li>
        ))}
      </ul>

      {/* 设置永久固定底部 */}
      <ul className="w-full px-1 pt-2 border-t border-outline-variant/40">
        <li>
          <NavItem to="/settings" label={t('MENU.SETTINGS', '系统设置')} Icon={SettingsIcon} />
        </li>
      </ul>
    </nav>
  );
};

export default UserSidebar;
