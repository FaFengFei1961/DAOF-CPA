// Phase G-2.6（2026-05-20）：忘记密码 → 重置密码 落地页。
//
// 用户从邮件点链接 https://app/reset-password?token=<rawToken> 落到这里。
// 表单：填新密码 + 确认 → POST /api/auth/email/reset-password → 成功跳登录页。
//
// 安全：token 只在 URL query 里，不写 localStorage / cookie，校验+消费后立即弃用。
// 不需要登录态：reset-password 是 public endpoint。
import React, { useState, useMemo } from 'react';
import { useSearchParams, useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { CheckCircle, AlertTriangle, Loader2, Eye, EyeOff } from 'lucide-react';
import toast from 'react-hot-toast';

const STATUS_FORM = 'form';
const STATUS_SUBMITTING = 'submitting';
const STATUS_SUCCESS = 'success';
const STATUS_FAIL = 'fail';

const ResetPasswordPage = () => {
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
    if (password.length < 8) return t('EMAIL.RESET.ERR_TOO_SHORT', '密码至少 8 字符');
    if (password.length > 72) return t('EMAIL.RESET.ERR_TOO_LONG', '密码不能超过 72 字符');
    if (confirm && password !== confirm) return t('EMAIL.RESET.ERR_MISMATCH', '两次密码不一致');
    return '';
  }, [password, confirm, t]);

  const canSubmit = !!token && password.length >= 8 && password === confirm && status === STATUS_FORM;

  const handleSubmit = async (e) => {
    e.preventDefault();
    if (!canSubmit) return;
    setStatus(STATUS_SUBMITTING);
    try {
      const res = await fetch('/api/auth/email/reset-password', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ token, new_password: password }),
      });
      const data = await res.json();
      if (data.success) {
        setStatus(STATUS_SUCCESS);
        toast.success(t('API.SUCCESS_PASSWORD_RESET', '密码已重置，请使用新密码登录'));
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
              {t('EMAIL.RESET.TITLE', '设置新密码')}
            </h2>
            <p className="text-sm text-on-surface-variant text-center mb-6">
              {t('EMAIL.RESET.DESC', '请输入您的新密码。')}
            </p>
            <form onSubmit={handleSubmit} className="space-y-4">
              <div>
                <label className="block text-sm font-medium text-on-surface mb-1.5">
                  {t('EMAIL.RESET.NEW_PWD', '新密码')}
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
                    placeholder={t('EMAIL.RESET.PWD_PLACEHOLDER', '至少 8 个字符')}
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
                  {t('EMAIL.RESET.CONFIRM_PWD', '确认密码')}
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
                  placeholder={t('EMAIL.RESET.CONFIRM_PLACEHOLDER', '再次输入密码')}
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
                {t('EMAIL.RESET.SUBMIT', '提交')}
              </button>
            </form>
          </>
        )}
        {status === STATUS_SUBMITTING && (
          <div className="text-center">
            <Loader2 size={36} className="mx-auto text-primary animate-spin mb-4" />
            <h2 className="text-lg font-bold text-on-surface">{t('EMAIL.RESET.LOADING', '提交中…')}</h2>
          </div>
        )}
        {status === STATUS_SUCCESS && (
          <div className="text-center">
            <CheckCircle size={36} className="mx-auto text-success mb-4" />
            <h2 className="text-lg font-bold text-on-surface">{t('EMAIL.RESET.OK', '密码已重置')}</h2>
            <p className="text-sm text-on-surface-variant mt-3">
              {t('EMAIL.RESET.OK_HINT', '请使用新密码登录。')}
            </p>
            <div className="flex justify-center gap-2 mt-6">
              <button
                onClick={() => navigate('/')}
                className="h-9 px-4 bg-primary text-on-primary rounded-control text-sm font-medium"
              >
                {t('EMAIL.RESET.GOTO_LOGIN', '去登录')}
              </button>
            </div>
          </div>
        )}
        {status === STATUS_FAIL && (
          <div className="text-center">
            <AlertTriangle size={36} className="mx-auto text-warning mb-4" />
            <h2 className="text-lg font-bold text-on-surface">{t('EMAIL.RESET.FAIL', '重置失败')}</h2>
            <p className="text-sm text-on-surface-variant mt-3">
              {errorCode
                ? t(`API.${errorCode}`, t('EMAIL.RESET.FAIL_GENERIC', '链接无效或已过期，请重新申请。'))
                : t('EMAIL.RESET.FAIL_GENERIC', '链接无效或已过期，请重新申请。')}
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

export default ResetPasswordPage;
