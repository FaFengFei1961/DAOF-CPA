import React from 'react';
import { useTranslation } from 'react-i18next';
import { Link, NavLink } from 'react-router-dom';
import { Home, ShieldAlert, ArrowLeft } from 'lucide-react';
import { adminNav } from '../navManifest';

/**
 * AdminSidebar — admin 独立左侧导航（Phase 0 重构）
 *
 * 替换原 Settings.jsx 内嵌 vertical nav。
 * 视觉规则：
 *   - 桌面：宽 224px（比 Win11 NavigationView Expanded 240px 略窄给主区让位）
 *   - acrylic 表面 + 圆角 0px（贴边）
 *   - 顶部 "管理控制台" 标识 + 返回用户视图入口
 *   - 分组用 caption 标题（GROUP_BUSINESS / GROUP_USERS / GROUP_SYSTEM）
 *   - active 项左侧 3px 主色指示条
 */
const AdminSidebar = () => {
  const { t } = useTranslation();

  return (
    <aside
      aria-label={t('SETTINGS.NAV_LABEL', '管理导航')}
      className="hidden lg:flex flex-col w-60 h-screen bg-surface-container/40 border-r border-outline-variant/40 fixed top-0 left-0 z-50"
    >
      {/* Phase 8：admin brand 区中性化（去 fuchsia "admin = 高权限要醒目"
          的 promo banner 设计），跟用户 sidebar 视觉一致；admin 身份用图标 +
          小字 caption 标识即可 */}
      <div className="border-b border-outline-variant/40">
        <div className="px-4 py-3 flex items-center gap-2.5">
          <img src="/daof_logo.png" alt="" className="w-8 h-8 rounded-control" />
          <div className="min-w-0 flex-1">
            <div className="text-sm font-semibold text-on-surface truncate leading-tight">
              DAOF-CPA
            </div>
            <div className="flex items-center gap-1 text-[11px] text-on-surface-variant mt-0.5">
              <ShieldAlert size={11} />
              {t('ADMIN.SHELL_TITLE', '管理控制台')}
            </div>
          </div>
        </div>
        <Link
          to="/"
          className="flex items-center gap-1.5 px-4 pb-2.5 text-[11px] text-on-surface-variant hover:text-on-surface transition group"
        >
          <ArrowLeft size={11} className="group-hover:-translate-x-0.5 transition-transform" />
          {t('ADMIN.BACK_TO_USER', '返回用户视图')}
        </Link>
      </div>

      {/* 导航分组 */}
      <nav className="flex-1 overflow-y-auto px-2 py-3 space-y-4 no-scrollbar">
        {adminNav.map(group => (
          <div key={group.groupKey}>
            <p className="px-2 mb-1 text-[10px] uppercase tracking-wider text-on-surface-variant/70 font-semibold">
              {t(group.groupKey, group.groupFallback)}
            </p>
            <ul className="space-y-0.5">
              {group.items.map(item => {
                const Icon = item.icon;
                return (
                  <li key={item.id}>
                    <NavLink
                      to={item.path}
                      end
                      className={({ isActive }) =>
                        `relative w-full h-8 flex items-center gap-2 px-2.5 rounded-control text-sm transition
                         ${isActive
                           ? 'bg-primary-container text-on-primary-container font-medium'
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
                          <Icon size={16} className={`shrink-0 ${isActive ? 'opacity-100' : 'opacity-70'}`} />
                          <span className="truncate">{t(item.labelKey, item.labelFallback)}</span>
                        </>
                      )}
                    </NavLink>
                  </li>
                );
              })}
            </ul>
          </div>
        ))}
      </nav>

      {/* 底部 — 快捷返回 user 仪表盘 */}
      <div className="border-t border-outline-variant/40 p-2">
        <NavLink
          to="/"
          className="w-full h-8 flex items-center gap-2 px-2.5 rounded-control text-sm text-on-surface-variant hover:bg-on-surface/[0.04] hover:text-on-surface transition"
        >
          <Home size={14} className="opacity-70" />
          <span>{t('ADMIN.GOTO_USER', '用户仪表盘')}</span>
        </NavLink>
      </div>
    </aside>
  );
};

export default AdminSidebar;
