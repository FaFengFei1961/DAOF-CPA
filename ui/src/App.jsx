
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
const BANNED_MARKER = '\u5c01\u7981';
const BANNED_PREFIX = '\u8d26\u6237\u88ab\u5c01\u7981';
const BANNED_REASON_PREFIX = '\u7406\u7531\uff1a';

// OAuthCallbackHandler 通用 OAuth 回调处理器（H-4 多 provider）。
// 检测当前 URL path 是否为 /oauth/{provider}，是则用对应 provider 调 backend callback。
// 支持的 path: /oauth/github, /oauth/google（H-4 新增）。
const OAuthCallbackHandler = () => {
  const { t } = useTranslation();
  const { openLogin, onLoginSuccess } = useAuth();

  useEffect(() => {
    // 从 URL path 拆 provider key（/oauth/github → "github"; /oauth/google → "google"）
    const pathMatch = window.location.pathname.match(/^\/oauth\/([a-z0-9_]+)$/i);
    if (!pathMatch) return;
    const provider = pathMatch[1].toLowerCase();

    const params = new URLSearchParams(window.location.search);
    const code = params.get('code');
    const state = params.get('state') || '';
    if (!code) return;

    const ref = sessionStorage.getItem('daof_ref') || '';
    window.history.replaceState({}, document.title, '/');
    queueMicrotask(() => openLogin({ step: 'github', loading: true }));

    fetch(`/api/auth/oauth/${encodeURIComponent(provider)}/callback?code=${encodeURIComponent(code)}&state=${encodeURIComponent(state)}`, {
      method: 'POST',
      credentials: 'include',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ ref }),
    })
      .then(async (res) => {
        const ct = res.headers.get('content-type') || '';
        if (!ct.includes('application/json')) {
          throw new Error(t('APP.NON_JSON_RESPONSE', {
            status: res.status,
            defaultValue: 'HTTP {{status}}: 服务端返回非 JSON 响应',
          }));
        }
        return res.json();
      })
      .then(data => {
        if (data.success) {
          // H-5：已登录用户主动 link 新 provider 时，后端返 SUCCESS_OAUTH_LINKED 但无 session_id
          // （用户原 session 仍有效）。检测这种情况：直接弹成功 toast + 跳设置页，不重 openLogin。
          if (data.message_code === 'SUCCESS_OAUTH_LINKED') {
            toast.success(t('API.SUCCESS_OAUTH_LINKED', '第三方账号绑定成功'));
            if (window.location.pathname !== '/settings') {
              window.location.replace('/settings?tab=account');
            }
            return;
          }
          if (!data.session_id) throw new Error('missing session_id');
          localStorage.setItem('daof_token', data.session_id);
          onLoginSuccess();
        } else if (data.action === 'require_sms_bind') {
          openLogin({ step: 'bind', tmpToken: data.tmp_token });
        } else if (data.action === 'require_profile_setup') {
          openLogin({ step: 'profile', tmpToken: data.tmp_token, defaultName: data.default_name || '' });
        } else if (data.message_code === 'ERR_BANNED' || (data.message && data.message.includes(BANNED_MARKER))) {

          window.dispatchEvent(new CustomEvent('daof_banned', {
            detail: data.ban_reason || (data.message ? data.message.replace(BANNED_PREFIX, '').replace(BANNED_REASON_PREFIX, '').trim() : ''),
          }));
        } else {
          toast.error((data.message_code ? t('API.' + data.message_code) : data.message) || t('APP.OAUTH_FAILED', '第三方登录失败'));
          openLogin({ step: 'github' });
        }
      })
      .catch((err) => {
        logger.warn('[oauth-callback] failed', err);
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
    <OAuthCallbackHandler />
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
