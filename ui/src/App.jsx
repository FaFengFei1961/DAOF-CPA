/**
 * App — Phase 0 重构后的根组件
 *
 * 旧版 App.jsx 587 行混合了：路由、auth、ban 拦截、global profile fetch、mobile bottom nav。
 * 重构后：
 *   - 路由 → routes.jsx + RouterProvider
 *   - auth/profile/ban → context/AuthContext.jsx
 *   - mobile bottom nav → shells/MobileBottomNav.jsx
 *   - sys/setup wizard → 这里仍保留（pre-app flow，不进路由）
 *
 * App.jsx 现在只做：
 *   1. setup needed / sys param 前置 wizard（admin 首次安装 / admin secret login）
 *   2. AuthProvider 包裹
 *   3. Toaster + AuthModalContainer + BanAlertContainer 全局 portal
 *   4. RouterProvider 挂载
 *   5. github oauth callback 拦截（一次性，URL 含 ?code= 时）
 */
import React, { useEffect, useMemo, useState, lazy, Suspense } from 'react';
import { useTranslation } from 'react-i18next';
import { RouterProvider } from 'react-router-dom';
import toast, { Toaster } from 'react-hot-toast';
import { ConfirmProvider } from './context/ConfirmContext';
import { AuthProvider, useAuth } from './context/AuthContext';
import AuthModalContainer from './shells/AuthModalContainer';
import BanAlertContainer from './shells/BanAlertContainer';
import router from './routes';
import { logger } from './utils/logger';

const AdminSecretLogin = lazy(() => import('./components/AdminSecretLogin'));

// ─── 内部组件：处理 github oauth callback ────────────────────────
//
// 必须在 AuthProvider 内（用 useAuth 触发 modal）。挂载点在 RootShell。
//
const GithubCallbackHandler = () => {
  const { t } = useTranslation();
  const { openLogin, onLoginSuccess } = useAuth();

  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    const code = params.get('code');
    const state = params.get('state') || '';
    if (!code) return;

    const ref = sessionStorage.getItem('daof_ref') || '';
    window.history.replaceState({}, document.title, '/');
    queueMicrotask(() => openLogin({ step: 'github', loading: true }));

    fetch(`/api/auth/github?code=${encodeURIComponent(code)}&state=${encodeURIComponent(state)}`, {
      method: 'POST',
      credentials: 'include',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ ref }),
    })
      .then(async (res) => {
        const ct = res.headers.get('content-type') || '';
        if (!ct.includes('application/json')) {
          throw new Error(`HTTP ${res.status}：服务端返回非 JSON 响应`);
        }
        return res.json();
      })
      .then(data => {
        if (data.success) {
          if (!data.session_id) throw new Error('missing session_id');
          localStorage.setItem('daof_token', data.session_id);
          onLoginSuccess();
        } else if (data.action === 'require_sms_bind') {
          openLogin({ step: 'bind', tmpToken: data.tmp_token });
        } else if (data.action === 'require_profile_setup') {
          openLogin({ step: 'profile', tmpToken: data.tmp_token, defaultName: data.default_name || '' });
        } else if (data.message_code === 'ERR_BANNED' || (data.message && data.message.includes('封禁'))) {
          // ban 通过 daof_banned 事件抛出，AuthContext 监听
          window.dispatchEvent(new CustomEvent('daof_banned', {
            detail: data.ban_reason || (data.message ? data.message.replace('账户被封禁', '').replace('理由：', '').trim() : ''),
          }));
        } else {
          toast.error((data.message_code ? t('API.' + data.message_code) : data.message) || t('APP.GITHUB_OAUTH_FAILED', 'GitHub 登录失败'));
          openLogin({ step: 'github' });
        }
      })
      .catch((err) => {
        logger.warn('[github-oauth] failed', err);
        toast.error(t('APP.LOGIN_NET_ERROR', '登录网络异常'));
        openLogin({ step: 'github' });
      });
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return null;
};

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
    <GithubCallbackHandler />
    <AuthModalContainer />
    <BanAlertContainer />
    <RouterProvider router={router} />
  </>
);

function App() {
  const { t } = useTranslation();

  // sys/setup 前置 wizard 仍然依赖 URL ?sys= 参数 + /api/root/check-sys
  const sysParam = useMemo(() => new URLSearchParams(window.location.search).get('sys'), []);
  const isAdminUnlocked = localStorage.getItem('daof_admin_unlocked') === '1';
  const [sysCheckStatus, setSysCheckStatus] = useState(() => ({
    loading: !!sysParam,
    setupNeeded: false,
  }));

  // 仅当带 sysParam 才探测系统首次安装态（避免外网 LanGuard 403 噪音）
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

  // setup 流程：必须带 ?sys=... 才能进入 setup 引导，否则锁站
  if (sysCheckStatus.setupNeeded) {
    if (sysParam && !isAdminUnlocked) {
      return (
        <Suspense fallback={<div className="min-h-screen bg-surface" />}>
          <AdminSecretLogin
            sysParam={sysParam}
            setupMode
            onSuccess={() => {
              // setup 完成 → 强制刷新到 /admin
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

  // 正常态：admin 通过 ?sys= URL 走 AdminSecretLogin
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
