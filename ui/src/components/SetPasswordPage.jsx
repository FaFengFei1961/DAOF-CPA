// Phase G-2.6（2026-05-20）：OAuth 用户启用 email-login → 首次设置密码落地页。
//
// 用户从邮件点链接 https://app/set-password?token=<rawToken> 落到这里。
// 表单：填新密码 + 确认 → POST /api/auth/email/set-password → 成功跳登录页（已自动 EmailLoginEnabled=true）。
//
// 与 ResetPasswordPage 的差别：
//   - endpoint：/set-password vs /reset-password
//   - 成功后跳转默认到 / 而非 /login，因为用户可能仍在 OAuth session 里
//   - 文案侧重"首次设置"而非"重置"
import React, { useState, useMemo } from 'react';
import { useSearchParams, useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { CheckCircle, AlertTriangle, Loader2, Eye, EyeOff } from 'lucide-react';
import toast from 'react-hot-toast';

const STATUS_FORM = 'form';
const STATUS_SUBMITTING = 'submitting';
const STATUS_SUCCESS = 'success';
const STATUS_FAIL = 'fail';

const SetPasswordPage = () => {
  const { t } = useTranslation();
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const token = params.get('token') || '';
  const [status, setStatus] = useState(token ? STATUS_FORM : STATUS_FAIL);
  const [errorCode, setErrorCode] = useState(token ? '' : 'ERR_EMAIL_TOKEN_INVALID');
  const [password, setPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  const [showPwd, setShowPwd] = useState(false);

  const passwordError = useMemo(() => {
    if (!password) return '';
    if (password.length < 8) return t('EMAIL.SETPWD.ERR_TOO_SHORT', '密码至少 8 字符');
    if (password.length > 72) return t('EMAIL.SETPWD.ERR_TOO_LONG', '密码不能超过 72 字符');
    if (confirm && password !== confirm) return t('EMAIL.SETPWD.ERR_MISMATCH', '两次密码不一致');
    return '';
  }, [password, confirm, t]);

  const canSubmit = !!token && password.length >= 8 && password === confirm && status === STATUS_FORM;

  const handleSubmit = async (e) => {
    e.preventDefault();
    if (!canSubmit) return;
    setStatus(STATUS_SUBMITTING);
    try {
      const res = await fetch('/api/auth/email/set-password', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ token, new_password: password }),
      });
      const data = await res.json();
      if (data.success) {
        setStatus(STATUS_SUCCESS);
        toast.success(t('API.SUCCESS_PASSWORD_SET', '密码已设置，您现在可以使用邮箱+密码登录'));
      } else {
        setStatus(STATUS_FAIL);
        setErrorCode(data.message_code || '');
      }
    } catch {
      setStatus(STATUS_FAIL);
      setErrorCode('ERR_NETWORK');
    }
  };

  return (
    <div className="min-h-[60vh] flex items-center justify-center px-4">
      <div className="max-w-md w-full bg-surface-container border border-outline-variant rounded-overlay p-8">
        {status === STATUS_FORM && (
          <>
            <h2 className="text-lg font-bold text-on-surface text-center mb-2">
              {t('EMAIL.SETPWD.TITLE', '设置密码')}
            </h2>
            <p className="text-sm text-on-surface-variant text-center mb-6">
              {t('EMAIL.SETPWD.DESC', '为您的账号设置密码以启用邮箱登录。')}
            </p>
            <form onSubmit={handleSubmit} className="space-y-4">
              <div>
                <label className="block text-sm font-medium text-on-surface mb-1.5">
                  {t('EMAIL.SETPWD.NEW_PWD', '密码')}
                </label>
                <div className="relative">
                  <input
                    type={showPwd ? 'text' : 'password'}
                    value={password}
                    onChange={(e) => setPassword(e.target.value)}
                    autoComplete="new-password"
                    minLength={8}
                    maxLength={72}
                    required
                    className="w-full h-10 px-3 pr-10 rounded-control bg-surface-container-high border border-outline text-on-surface text-sm"
                    placeholder={t('EMAIL.SETPWD.PWD_PLACEHOLDER', '至少 8 个字符')}
                  />
                  <button
                    type="button"
                    aria-label={t('COMMON.TOGGLE_PASSWORD_VISIBILITY', '切换密码可见性')}
                    onClick={() => setShowPwd(s => !s)}
                    className="absolute right-2 top-1/2 -translate-y-1/2 text-on-surface-variant p-1"
                  >
                    {showPwd ? <EyeOff size={16} /> : <Eye size={16} />}
                  </button>
                </div>
              </div>
              <div>
                <label className="block text-sm font-medium text-on-surface mb-1.5">
                  {t('EMAIL.SETPWD.CONFIRM_PWD', '确认密码')}
                </label>
                <input
                  type={showPwd ? 'text' : 'password'}
                  value={confirm}
                  onChange={(e) => setConfirm(e.target.value)}
                  autoComplete="new-password"
                  minLength={8}
                  maxLength={72}
                  required
                  className="w-full h-10 px-3 rounded-control bg-surface-container-high border border-outline text-on-surface text-sm"
                  placeholder={t('EMAIL.SETPWD.CONFIRM_PLACEHOLDER', '再次输入密码')}
                />
              </div>
              {passwordError && (
                <p className="text-sm text-error">{passwordError}</p>
              )}
              <button
                type="submit"
                disabled={!canSubmit}
                className="w-full h-10 bg-primary text-on-primary rounded-control text-sm font-medium disabled:opacity-50 disabled:cursor-not-allowed"
              >
                {t('EMAIL.SETPWD.SUBMIT', '提交')}
              </button>
            </form>
          </>
        )}
        {status === STATUS_SUBMITTING && (
          <div className="text-center">
            <Loader2 size={36} className="mx-auto text-primary animate-spin mb-4" />
            <h2 className="text-lg font-bold text-on-surface">{t('EMAIL.SETPWD.LOADING', '提交中…')}</h2>
          </div>
        )}
        {status === STATUS_SUCCESS && (
          <div className="text-center">
            <CheckCircle size={36} className="mx-auto text-success mb-4" />
            <h2 className="text-lg font-bold text-on-surface">{t('EMAIL.SETPWD.OK', '密码设置成功')}</h2>
            <p className="text-sm text-on-surface-variant mt-3">
              {t('EMAIL.SETPWD.OK_HINT', '您现在可以使用邮箱+密码登录。')}
            </p>
            <div className="flex justify-center gap-2 mt-6">
              <button
                onClick={() => navigate('/')}
                className="h-9 px-4 bg-primary text-on-primary rounded-control text-sm font-medium"
              >
                {t('COMMON.BACK_HOME', '返回首页')}
              </button>
            </div>
          </div>
        )}
        {status === STATUS_FAIL && (
          <div className="text-center">
            <AlertTriangle size={36} className="mx-auto text-warning mb-4" />
            <h2 className="text-lg font-bold text-on-surface">{t('EMAIL.SETPWD.FAIL', '设置失败')}</h2>
            <p className="text-sm text-on-surface-variant mt-3">
              {errorCode
                ? t(`API.${errorCode}`, t('EMAIL.SETPWD.FAIL_GENERIC', '链接无效或已过期，请重新申请。'))
                : t('EMAIL.SETPWD.FAIL_GENERIC', '链接无效或已过期，请重新申请。')}
            </p>
            <div className="flex justify-center gap-2 mt-6">
              <button
                onClick={() => navigate('/')}
                className="h-9 px-4 bg-surface-container-high border border-outline text-on-surface rounded-control text-sm"
              >
                {t('COMMON.BACK_HOME', '返回首页')}
              </button>
            </div>
          </div>
        )}
      </div>
    </div>
  );
};

export default SetPasswordPage;
