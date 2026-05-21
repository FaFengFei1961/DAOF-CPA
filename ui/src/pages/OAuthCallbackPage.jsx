/**
 * OAuthCallbackPage — 第三方登录回调落地页。
 *
 * 路由：/oauth/:provider?code=...&state=...
 *
 * GitHub / Google 完成授权后会 302 回这个 URL。组件 mount 时：
 *   1. 从 URL 取 code + state
 *   2. POST /api/auth/oauth/:provider/callback 给后端
 *   3. 按 response 走 3 个分支：
 *      - success + session_id → 写 token + 跳首页
 *      - require_sms_bind / require_profile_setup → 打开 AuthModal 续流程
 *      - 其他 → 红色 toast + 回登录弹窗
 *
 * Audit fix（2026-05-21）：之前 OAuthCallbackHandler 在 App.jsx 里挂在
 * RouterProvider 外面，但 RouterProvider 的 NotFound fallback 会先抢先
 * 渲染 404 → 用户看到大大的"页面不存在"。改成正经 router child route
 * 是 React Router 范式，无 404 闪烁。
 */
import React, { useEffect } from 'react';
import { useNavigate, useParams, useSearchParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import toast from 'react-hot-toast';
import { useAuth } from '../context/AuthContext';
import { logger } from '../utils/logger';

const BANNED_MARKER = '封禁';
const BANNED_PREFIX = '账户被封禁';
const BANNED_REASON_PREFIX = '理由：';

const OAuthCallbackPage = () => {
  const { t } = useTranslation();
  const { provider: rawProvider } = useParams();
  const [searchParams] = useSearchParams();
  const navigate = useNavigate();
  const { openLogin, onLoginSuccess } = useAuth();

  useEffect(() => {
    const provider = String(rawProvider || '').toLowerCase().trim();
    const code = searchParams.get('code') || '';
    const state = searchParams.get('state') || '';
    const ref = sessionStorage.getItem('daof_ref') || '';

    // 缺 provider 或缺 code：把用户带回登录页，不静默卡死
    if (!provider || !code) {
      navigate('/', { replace: true });
      queueMicrotask(() => openLogin({ step: 'github' }));
      return;
    }

    // 立刻把 URL 改成 /，让用户即便手动刷新也不会重提交同一个 code
    // （OAuth code 一次性，replay 会被后端拒）
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
      .then((data) => {
        if (data.success) {
          // H-5：已登录用户主动 link 新 provider 时，后端返 SUCCESS_OAUTH_LINKED 但无 session_id。
          if (data.message_code === 'SUCCESS_OAUTH_LINKED') {
            toast.success(t('API.SUCCESS_OAUTH_LINKED', '第三方账号绑定成功'));
            navigate('/settings?tab=account', { replace: true });
            return;
          }
          if (!data.session_id) throw new Error('missing session_id');
          localStorage.setItem('daof_token', data.session_id);
          onLoginSuccess();
          navigate('/', { replace: true });
        } else if (data.action === 'require_sms_bind') {
          navigate('/', { replace: true });
          openLogin({ step: 'bind', tmpToken: data.tmp_token });
        } else if (data.action === 'require_profile_setup') {
          navigate('/', { replace: true });
          openLogin({ step: 'profile', tmpToken: data.tmp_token, defaultName: data.default_name || '' });
        } else if (data.message_code === 'ERR_BANNED' || (data.message && data.message.includes(BANNED_MARKER))) {
          navigate('/', { replace: true });
          window.dispatchEvent(new CustomEvent('daof_banned', {
            detail: data.ban_reason || (data.message ? data.message.replace(BANNED_PREFIX, '').replace(BANNED_REASON_PREFIX, '').trim() : ''),
          }));
        } else if (data.message_code === 'ERR_OAUTH_EMAIL_TAKEN_LINK_REQUIRED') {
          // H-6：跨 provider 邮箱冲突
          toast.error(
            t('API.ERR_OAUTH_EMAIL_TAKEN_LINK_REQUIRED', '该第三方邮箱已被另一个账号占用，请先登录原账号后在「设置 → 第三方账号」中绑定。'),
            { duration: 8000 },
          );
          navigate('/', { replace: true });
          openLogin({ step: 'github' });
        } else {
          toast.error(
            (data.message_code ? t('API.' + data.message_code) : data.message)
              || t('APP.OAUTH_FAILED', '第三方登录失败'),
          );
          navigate('/', { replace: true });
          openLogin({ step: 'github' });
        }
      })
      .catch((err) => {
        logger.warn('[oauth-callback] failed', err);
        toast.error(t('APP.LOGIN_NET_ERROR', '登录网络异常'));
        navigate('/', { replace: true });
        openLogin({ step: 'github' });
      });
  // 仅 mount 时执行一次；deps 全用 ref / param 第一渲染快照
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // 渲染一个极简 loading 占位 —— AuthModal 会立刻覆盖上来
  // （openLogin 是 queueMicrotask 调的，肉眼几乎看不到这层）
  return (
    <div className="min-h-screen flex items-center justify-center bg-surface text-on-surface-variant">
      <div className="text-sm">{t('APP.OAUTH_PROCESSING', '正在完成第三方登录…')}</div>
    </div>
  );
};

export default OAuthCallbackPage;
