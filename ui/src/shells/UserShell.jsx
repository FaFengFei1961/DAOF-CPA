import React, { Suspense } from 'react';
import { Outlet, useLocation } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import TopBar from '../components/TopBar';
import UserSidebar from './UserSidebar';
import MobileBottomNav from './MobileBottomNav';
import { useAuth } from '../context/AuthContext';

/**
 * User-facing layout with sidebar, top bar, mobile navigation, and outlet.
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
        {t('SHELL.USER.SKIP_TO_MAIN')}
      </a>

      <UserSidebar />

      <div className="flex-1 min-w-0 lg:ml-60 flex flex-col h-screen overflow-y-auto pb-20 lg:pb-8">
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
          className="flex-1 min-w-0 w-full max-w-[1440px] mx-auto px-4 sm:px-8 lg:px-10 xl:px-12 mt-6 sm:mt-8 focus:outline-none animate-in fade-in slide-in-from-bottom-1 duration-300"
        >
          <Suspense fallback={<div className="py-12 text-center text-sm text-on-surface-variant">{t('COMMON.LOADING')}</div>}>
            <Outlet />
          </Suspense>
        </main>
      </div>

      <MobileBottomNav />
    </div>
  );
};

export default UserShell;
