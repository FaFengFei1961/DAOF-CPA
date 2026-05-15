import React, { Suspense } from 'react';
import { Outlet, useLocation } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import TopBar from '../components/TopBar';
import AdminSidebar from './AdminSidebar';
import AdminGuard from './AdminGuard';
import { useAuth } from '../context/AuthContext';

/**
 * AdminShell — 管理员独立布局（Phase 0）
 *
 * 跟 UserShell 视觉延续：同样 TopBar、同样 Mica 表面，只换 sidebar 内容。
 * AdminSidebar 含全部 admin 模块，admin 在不同模块切换不再需要进 Settings 内的 vertical nav。
 *
 * 路由保护由 AdminGuard 包裹（未解锁的 admin redirect /）
 */
const AdminShell = () => {
  const { t } = useTranslation();
  const { isAuthenticated, isAdmin, profile, openLogin } = useAuth();
  const location = useLocation();

  return (
    <AdminGuard>
      <div className="min-h-screen bg-surface text-on-surface flex font-sans animate-in fade-in duration-500">
        <a
          href="#main-content"
          className="sr-only focus:not-sr-only focus:absolute focus:top-2 focus:left-2 focus:z-[200] focus:px-4 focus:py-2 focus:bg-primary focus:text-on-primary focus:rounded-control focus: focus:outline focus:outline-2 focus:outline-offset-2 focus:outline-primary"
        >
          {t('A11Y.SKIP_TO_MAIN', '跳至主要内容')}
        </a>

        <AdminSidebar />

        <div className="flex-1 min-w-0 lg:ml-60 flex flex-col h-screen overflow-y-auto">
          <TopBar
            isAuthenticated={isAuthenticated}
            isAdmin={isAdmin}
            profile={profile}
            onOpenAuth={openLogin}
          />
          <main
            id="main-content"
            tabIndex="-1"
            key={location.pathname}
            className="flex-1 min-w-0 w-full max-w-[1880px] 2xl:max-w-none mx-auto px-3 sm:px-5 lg:px-6 2xl:px-8 mt-2 sm:mt-4 focus:outline-none animate-in fade-in slide-in-from-bottom-1 duration-300"
          >
            <Suspense fallback={<div className="py-12 text-center text-sm text-on-surface-variant">{t('APP.LOADING', '加载中...')}</div>}>
              <Outlet />
            </Suspense>
          </main>
        </div>
      </div>
    </AdminGuard>
  );
};

export default AdminShell;
