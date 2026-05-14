import React, { Suspense } from 'react';
import { Outlet, useLocation } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import TopBar from '../components/TopBar';
import UserSidebar from './UserSidebar';
import MobileBottomNav from './MobileBottomNav';
import { useAuth } from '../context/AuthContext';

/**
 * UserShell — 用户侧布局（Phase 0）
 *
 * 替换原 App.jsx 内 main + Sidebar + TopBar + bottom nav 杂糅代码。
 * 路由通过 <Outlet /> 渲染。
 *
 * 视觉规则：
 *  - 桌面：64px sidebar + 主区
 *  - 移动：sidebar 隐藏，底部固定 6 格 nav
 *  - main 容器统一 max-w-1880px + responsive padding
 */
const UserShell = () => {
  const { t } = useTranslation();
  const { isAuthenticated, isAdmin, profile, openLogin } = useAuth();
  const location = useLocation();

  return (
    <div className="min-h-screen bg-surface text-on-surface flex font-sans animate-in fade-in duration-500">
      {/* skip-to-main for keyboard / screen reader users */}
      <a
        href="#main-content"
        className="sr-only focus:not-sr-only focus:absolute focus:top-2 focus:left-2 focus:z-[200] focus:px-4 focus:py-2 focus:bg-primary focus:text-on-primary focus:rounded-lg focus:shadow-lg focus:outline focus:outline-2 focus:outline-offset-2 focus:outline-primary"
      >
        {t('A11Y.SKIP_TO_MAIN', '跳至主要内容')}
      </a>

      <UserSidebar />

      <div className="flex-1 md:ml-16 flex flex-col h-screen overflow-y-auto pb-20 md:pb-8">
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
          className="flex-1 w-full max-w-[1880px] 2xl:max-w-none mx-auto px-3 sm:px-5 lg:px-6 2xl:px-8 mt-2 sm:mt-4 focus:outline-none animate-in fade-in slide-in-from-bottom-1 duration-300"
        >
          <Suspense fallback={<div className="py-12 text-center text-sm text-on-surface-variant">{t('APP.LOADING', '加载中...')}</div>}>
            <Outlet />
          </Suspense>
        </main>
      </div>

      <MobileBottomNav />
    </div>
  );
};

export default UserShell;
