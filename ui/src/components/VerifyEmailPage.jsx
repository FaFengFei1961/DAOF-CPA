// Phase G-1.8（2026-05-20）：邮箱验证落地页。
//
// 用户从邮件点链接 https://app/verify-email?token=<rawToken> 落到这里，
// 自动 POST /api/user/email/verify 完成验证，结果用文案 + 跳转引导。
//
// 安全：token 只在 URL query 里，不写 localStorage / cookie，验证成功后立即弃用。
import React, { useEffect, useState } from 'react';
import { useSearchParams, useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { CheckCircle, AlertTriangle, Loader2 } from 'lucide-react';
import { authFetch, isLoggedIn } from '../utils/authFetch';
import { useAuth } from '../context/AuthContext';

const STATUS_IDLE = 'idle';
const STATUS_LOADING = 'loading';
const STATUS_SUCCESS = 'success';
const STATUS_FAIL = 'fail';

const VerifyEmailPage = () => {
  const { t } = useTranslation();
  const [params] = useSearchParams();
  const navigate = useNavigate();
  // IA audit M-J2 fix: 未登录用户 ERR_NO_AUTH 之前的 CTA 是"回到设置重发"
  // (/settings?tab=account)，但 RouteGuard 会立即把未登录用户挡回登录。
  // 接入 openLogin 让失败页能直接弹登录 modal。
  const { openLogin } = useAuth();
  const token = params.get('token') || '';
  const [status, setStatus] = useState(STATUS_IDLE);
  const [errorCode, setErrorCode] = useState('');
  const [verifiedEmail, setVerifiedEmail] = useState('');

  useEffect(() => {
    if (!token) {
      setStatus(STATUS_FAIL);
      setErrorCode('ERR_EMAIL_TOKEN_INVALID');
      return;
    }
    if (!isLoggedIn()) {
      // 未登录：把 token 暂存 query，登录后页面会重新挂载并自动调用
      setStatus(STATUS_FAIL);
      setErrorCode('ERR_NO_AUTH');
      return;
    }
    let alive = true;
    setStatus(STATUS_LOADING);
    authFetch('/api/user/email/verify', { method: 'POST', body: { token } })
      .then((json) => {
        if (!alive) return;
        if (json.success) {
          setStatus(STATUS_SUCCESS);
          setVerifiedEmail(json.email || '');
        } else {
          setStatus(STATUS_FAIL);
          setErrorCode(json.message_code || '');
        }
      })
      .catch(() => {
        if (!alive) return;
        setStatus(STATUS_FAIL);
        setErrorCode('ERR_NETWORK');
      });
    return () => { alive = false; };
  }, [token]);

  return (
    <div className="min-h-[60vh] flex items-center justify-center px-4">
      <div className="max-w-md w-full bg-surface-container border border-outline-variant rounded-overlay p-8 text-center">
        {status === STATUS_LOADING && (
          <>
            <Loader2 size={36} className="mx-auto text-primary animate-spin mb-4" />
            <h2 className="text-lg font-bold text-on-surface">{t('EMAIL.VERIFY.LOADING', '正在验证邮箱…')}</h2>
          </>
        )}
        {status === STATUS_SUCCESS && (
          <>
            <CheckCircle size={36} className="mx-auto text-success mb-4" />
            <h2 className="text-lg font-bold text-on-surface">{t('EMAIL.VERIFY.OK', '邮箱验证成功')}</h2>
            {verifiedEmail && (
              <p className="text-sm font-mono text-on-surface-variant mt-2">{verifiedEmail}</p>
            )}
            <p className="text-sm text-on-surface-variant mt-3">
              {t('EMAIL.VERIFY.OK_HINT', '您现在可以在通知偏好里启用邮件 channel，并接收来自平台的邮件通知。')}
            </p>
            <div className="flex justify-center gap-2 mt-6">
              <button
                onClick={() => navigate('/settings?tab=notification_prefs')}
                className="h-9 px-4 bg-primary text-on-primary rounded-control text-sm font-medium"
              >
                {t('EMAIL.VERIFY.GOTO_PREFS', '前往通知偏好')}
              </button>
              <button
                onClick={() => navigate('/')}
                className="h-9 px-4 bg-surface-container-high border border-outline text-on-surface rounded-control text-sm"
              >
                {t('COMMON.BACK_HOME', '返回首页')}
              </button>
            </div>
          </>
        )}
        {status === STATUS_FAIL && (
          <>
            <AlertTriangle size={36} className="mx-auto text-warning mb-4" />
            <h2 className="text-lg font-bold text-on-surface">{t('EMAIL.VERIFY.FAIL', '邮箱验证失败')}</h2>
            <p className="text-sm text-on-surface-variant mt-3">
              {errorCode ? t(`API.${errorCode}`, t('EMAIL.VERIFY.FAIL_GENERIC', '链接无效或已过期，请重新申请。')) :
                t('EMAIL.VERIFY.FAIL_GENERIC', '链接无效或已过期，请重新申请。')}
            </p>
            <div className="flex justify-center gap-2 mt-6">
              {errorCode === 'ERR_NO_AUTH' ? (
                <button
                  onClick={() => openLogin({ step: 'email-login' })}
                  className="h-9 px-4 bg-primary text-on-primary rounded-control text-sm font-medium"
                >
                  {t('EMAIL.VERIFY.GOTO_LOGIN', '请先登录')}
                </button>
              ) : (
                <button
                  onClick={() => navigate('/settings?tab=account')}
                  className="h-9 px-4 bg-primary text-on-primary rounded-control text-sm font-medium"
                >
                  {t('EMAIL.VERIFY.GOTO_REBIND', '回到设置重发')}
                </button>
              )}
              <button
                onClick={() => navigate('/')}
                className="h-9 px-4 bg-surface-container-high border border-outline text-on-surface rounded-control text-sm"
              >
                {t('COMMON.BACK_HOME', '返回首页')}
              </button>
            </div>
          </>
        )}
      </div>
    </div>
  );
};

export default VerifyEmailPage;
