
import React, { useEffect, useMemo, useState, lazy, Suspense } from 'react';
import { useTranslation } from 'react-i18next';
import { RouterProvider } from 'react-router-dom';
import { Toaster } from 'react-hot-toast';
import { ConfirmProvider } from './context/ConfirmContext';
import { AuthProvider } from './context/AuthContext';
import AuthModalContainer from './shells/AuthModalContainer';
import BanAlertContainer from './shells/BanAlertContainer';
import router from './routes';

const AdminSecretLogin = lazy(() => import('./components/AdminSecretLogin'));

// OAuth 回调已迁到 pages/OAuthCallbackPage.jsx 作为正经 router route，
// 避免 RouterProvider 内的 NotFound fallback 抢先把回调 URL 渲成 404
// （新用户从 GitHub 回跳过来会看到大大的"页面不存在"，体验崩溃）。
// 原 OAuthCallbackHandler 函数体已迁移到 OAuthCallbackPage.jsx，App.jsx
// 不再持有此 handler，避免在 RouterProvider 外层渲染时被 NotFound 抢先击中。

const RootShell = () => (
  <>
    <Toaster
      position="top-center"
      containerStyle={{ top: 16 }}
      toastOptions={{
        style: {
          background: 'var(--color-surface-container-high)',
          color: 'var(--color-on-surface)',
          border: '1px solid var(--color-outline-variant)',
        },
      }}
    />
    <AuthModalContainer />
    <BanAlertContainer />
    <RouterProvider router={router} />
  </>
);

function App() {
  const { t } = useTranslation();

  const sysParam = useMemo(() => new URLSearchParams(window.location.search).get('sys'), []);
  const isAdminUnlocked = localStorage.getItem('daof_admin_unlocked') === '1';
  const [sysCheckStatus, setSysCheckStatus] = useState(() => ({
    loading: !!sysParam,
    setupNeeded: false,
  }));

  useEffect(() => {
    if (!sysParam) return;
    let cancelled = false;
    fetch('/api/root/check-sys', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
    })
      .then(r => r.json())
      .then(data => {
        if (cancelled) return;
        setSysCheckStatus({ loading: false, setupNeeded: !!data.setup_required });
      })
      .catch(() => {
        if (cancelled) return;
        setSysCheckStatus({ loading: false, setupNeeded: false });
      });
    return () => { cancelled = true; };
  }, [sysParam]);

  if (sysCheckStatus.loading) {
    return (
      <div className="min-h-screen bg-surface flex items-center justify-center text-outline">
        {t('APP.INITIALIZING', '初始化…')}
      </div>
    );
  }

  if (sysCheckStatus.setupNeeded) {
    if (sysParam && !isAdminUnlocked) {
      return (
        <Suspense fallback={<div className="min-h-screen bg-surface" />}>
          <AdminSecretLogin
            sysParam={sysParam}
            setupMode
            onSuccess={() => {
              window.location.href = '/admin';
            }}
          />
        </Suspense>
      );
    }
    return (
      <div className="min-h-screen bg-surface flex flex-col items-center justify-center text-center p-6">
        <h1 className="text-2xl font-semibold text-on-surface-variant mb-2">
          {t('APP.SERVICE_UNAVAILABLE.TITLE', '服务暂不可用')}
        </h1>
        <p className="text-outline-variant">
          {t('APP.SERVICE_UNAVAILABLE.DESC', '系统正在初始化或维护中，请稍后访问。')}
        </p>
      </div>
    );
  }

  if (sysParam && !isAdminUnlocked) {
    return (
      <Suspense fallback={<div className="min-h-screen bg-surface" />}>
        <AdminSecretLogin
          sysParam={sysParam}
          setupMode={false}
          onSuccess={() => {
            window.location.href = '/admin';
          }}
        />
      </Suspense>
    );
  }

  return (
    <ConfirmProvider>
      <AuthProvider>
        <RootShell />
      </AuthProvider>
    </ConfirmProvider>
  );
}

export default App;
