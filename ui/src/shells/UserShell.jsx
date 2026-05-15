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
        className="sr-only focus:not-sr-only focus:absolute focus:top-2 focus:left-2 focus:z-[200] focus:px-4 focus:py-2 focus:bg-primary focus:text-on-primary focus:rounded-control focus: focus:outline focus:outline-2 focus:outline-offset-2 focus:outline-primary"
      >
        {t('A11Y.SKIP_TO_MAIN', '跳至主要内容')}
      </a>

      <UserSidebar />

      <div className="flex-1 lg:ml-60 flex flex-col h-screen overflow-y-auto pb-20 lg:pb-8">
        <TopBar
          isAuthenticated={isAuthenticated}
          isAdmin={isAdmin}
          profile={profile}
          onOpenAuth={openLogin}
        />
        {/* Phase 7.7：max-w 1880 → 1440，避免在 1080p+ 屏上 fluid 铺满显得空旷
            page padding 从 px-3/sm:5/lg:6/2xl:8 → px-4/sm:8/lg:10/xl:12 显著加大
            mt-2 → mt-6 给 TopBar 与内容明确呼吸 */}
        <main
          id="main-content"
          tabIndex="-1"
          key={location.pathname}
          className="flex-1 w-full max-w-[1440px] mx-auto px-4 sm:px-8 lg:px-10 xl:px-12 mt-6 sm:mt-8 focus:outline-none animate-in fade-in slide-in-from-bottom-1 duration-300"
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
