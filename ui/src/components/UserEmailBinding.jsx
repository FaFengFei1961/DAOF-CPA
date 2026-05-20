// Phase G-1.8（2026-05-20）：用户邮箱绑定 UI（账号设置页内嵌）。
//
// 状态机：
//   - 未启用 (feature_enabled=false)：显示"管理员未开启邮箱功能"
//   - 已启用但未绑定：表单输入邮箱 → 提交后变"已发送验证邮件"状态
//   - 已绑定但未验证：显示当前邮箱 + 重发 / 解绑按钮 + 验证链接说明
//   - 已绑定且已验证：显示完整邮箱 + 解绑按钮 + 通知 channel 提示
//
// 不在 URL 里显式接收 token —— 用户从邮件点链接 → 前端 /verify-email 页面读 query
// 后调用 /verify 接口（VerifyEmailPage.jsx 单独实现）。
import React, { useState, useEffect, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { Mail, Send, AlertCircle, CheckCircle, Trash2, RefreshCw } from 'lucide-react';
import toast from 'react-hot-toast';
import { authFetch, isLoggedIn } from '../utils/authFetch';
import { useConfirm } from '../context/ConfirmContext';

const UserEmailBinding = () => {
  const { t } = useTranslation();
  const confirm = useConfirm();
  const [status, setStatus] = useState(null); // { email, email_verified_at, email_login_enabled, feature_enabled }
  const [loading, setLoading] = useState(true);
  const [emailInput, setEmailInput] = useState('');
  const [submitting, setSubmitting] = useState(false);

  const load = useCallback(async () => {
    if (!isLoggedIn()) {
      setLoading(false);
      return;
    }
    try {
      const json = await authFetch('/api/user/email');
      if (json.success && json.data) {
        setStatus(json.data);
        if (json.data.email) setEmailInput(json.data.email);
      }
    } catch {
      // soft fail; UI will show "not loaded"
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { load(); }, [load]);

  const handleBind = async (e) => {
    e?.preventDefault?.();
    const email = String(emailInput || '').trim().toLowerCase();
    if (!email || !email.includes('@')) {
      toast.error(t('EMAIL.BIND.INVALID_FORMAT', '请输入有效的邮箱地址'));
      return;
    }
    setSubmitting(true);
    try {
      const json = await authFetch('/api/user/email/bind', {
        method: 'POST',
        body: { email },
      });
      if (json.success) {
        toast.success(t(`API.${json.message_code}`, t('EMAIL.BIND.SENT', '验证邮件已发送，请前往邮箱完成验证')));
        await load();
      } else {
        toast.error(json.message_code ? t(`API.${json.message_code}`, json.message) : (json.message || t('EMAIL.BIND.FAIL', '绑定失败')));
      }
    } catch {
      toast.error(t('EMAIL.BIND.FAIL', '绑定失败'));
    } finally {
      setSubmitting(false);
    }
  };

  const handleResend = async () => {
    setSubmitting(true);
    try {
      const json = await authFetch('/api/user/email/resend-verification', { method: 'POST' });
      if (json.success) {
        toast.success(t('EMAIL.BIND.RESENT', '验证邮件已重新发送'));
      } else {
        toast.error(json.message_code ? t(`API.${json.message_code}`, json.message) : (json.message || t('EMAIL.BIND.RESEND_FAIL', '重发失败')));
      }
    } catch {
      toast.error(t('EMAIL.BIND.RESEND_FAIL', '重发失败'));
    } finally {
      setSubmitting(false);
    }
  };

  const handleUnbind = async () => {
    const ok = await confirm(t('EMAIL.BIND.UNBIND_CONFIRM', '解绑后将无法收到邮件通知，确定继续？'));
    if (!ok) return;
    setSubmitting(true);
    try {
      const json = await authFetch('/api/user/email', { method: 'DELETE' });
      if (json.success) {
        toast.success(t('EMAIL.BIND.UNBOUND', '邮箱已解绑'));
        setEmailInput('');
        await load();
      } else {
        toast.error(json.message_code ? t(`API.${json.message_code}`, json.message) : (json.message || t('EMAIL.BIND.UNBIND_FAIL', '解绑失败')));
      }
    } catch {
      toast.error(t('EMAIL.BIND.UNBIND_FAIL', '解绑失败'));
    } finally {
      setSubmitting(false);
    }
  };

  if (loading) {
    return <div className="text-sm text-on-surface-variant py-4">{t('COMMON.LOADING', '加载中…')}</div>;
  }

  const featureEnabled = !!status?.feature_enabled;
  const hasEmail = !!status?.email;
  const verified = !!status?.email_verified_at;

  if (!featureEnabled) {
    return (
      <section className="rounded-overlay border border-outline-variant bg-surface-container p-6">
        <div className="flex items-start gap-3">
          <AlertCircle className="text-on-surface-variant shrink-0 mt-0.5" size={20} />
          <div>
            <h3 className="text-base font-semibold text-on-surface">{t('EMAIL.BIND.DISABLED_TITLE', '邮箱功能未开启')}</h3>
            <p className="text-sm text-on-surface-variant mt-1">
              {t('EMAIL.BIND.DISABLED_DESC', '管理员尚未启用邮箱功能。启用后您将可以绑定邮箱并接收邮件通知。')}
            </p>
          </div>
        </div>
      </section>
    );
  }

  return (
    <section className="rounded-overlay border border-outline-variant bg-surface-container p-6 space-y-4">
      <header className="flex items-center gap-3">
        <div className="w-9 h-9 rounded-control bg-primary/15 text-primary flex items-center justify-center">
          <Mail size={18} />
        </div>
        <div>
          <h3 className="text-base font-semibold text-on-surface">{t('EMAIL.BIND.TITLE', '邮箱绑定')}</h3>
          <p className="text-xs text-on-surface-variant mt-0.5">
            {t('EMAIL.BIND.DESC', '绑定邮箱后可接收邮件通知，并启用邮箱登录方式（在登录方式面板里再单独开启）')}
          </p>
        </div>
      </header>

      {!hasEmail && (
        <form onSubmit={handleBind} className="space-y-3">
          <label className="block">
            <span className="text-xs font-semibold text-on-surface-variant uppercase tracking-wide">
              {t('EMAIL.BIND.INPUT_LABEL', '邮箱地址')}
            </span>
            <input
              type="email"
              autoComplete="email"
              value={emailInput}
              onChange={(e) => setEmailInput(e.target.value)}
              placeholder="user@example.com"
              className="mt-1.5 w-full h-11 bg-black/40 border border-outline rounded-control px-3 text-sm text-on-surface focus:border-primary outline-none"
              required
            />
          </label>
          <button
            type="submit"
            disabled={submitting}
            className="h-10 px-4 bg-primary hover:opacity-90 disabled:opacity-50 text-on-primary rounded-control text-sm font-medium inline-flex items-center gap-2"
          >
            <Send size={16} />
            {submitting ? t('COMMON.SAVING', '提交中...') : t('EMAIL.BIND.SUBMIT', '发送验证邮件')}
          </button>
          <p className="text-[11px] text-on-surface-variant">
            {t('EMAIL.BIND.HINT', '验证邮件 1 小时内有效。点击邮件中的链接即可完成绑定。')}
          </p>
        </form>
      )}

      {hasEmail && (
        <div className="space-y-3">
          <div className="flex items-start justify-between gap-3 bg-black/20 rounded-control border border-outline-variant px-4 py-3">
            <div className="min-w-0">
              <div className="text-xs text-on-surface-variant">{t('EMAIL.BIND.BOUND_LABEL', '当前邮箱')}</div>
              <div className="text-sm font-mono text-on-surface truncate mt-0.5">{status.email}</div>
              <div className="text-[11px] mt-1">
                {verified ? (
                  <span className="inline-flex items-center gap-1 text-success">
                    <CheckCircle size={12} /> {t('EMAIL.BIND.STATUS_VERIFIED', '已验证 · 邮件通知 channel 可用')}
                  </span>
                ) : (
                  <span className="inline-flex items-center gap-1 text-warning">
                    <AlertCircle size={12} /> {t('EMAIL.BIND.STATUS_PENDING', '待验证 · 请前往邮箱点击验证链接')}
                  </span>
                )}
              </div>
            </div>
          </div>

          <div className="flex flex-wrap gap-2">
            {!verified && (
              <button
                type="button"
                onClick={handleResend}
                disabled={submitting}
                className="h-9 px-3 bg-surface-container-high border border-outline hover:border-primary text-on-surface text-xs rounded-control inline-flex items-center gap-1.5 disabled:opacity-50"
              >
                <RefreshCw size={14} />
                {t('EMAIL.BIND.RESEND', '重发验证邮件')}
              </button>
            )}
            <button
              type="button"
              onClick={handleUnbind}
              disabled={submitting}
              className="h-9 px-3 bg-surface-container-high border border-error/40 hover:border-error text-error text-xs rounded-control inline-flex items-center gap-1.5 disabled:opacity-50"
            >
              <Trash2 size={14} />
              {t('EMAIL.BIND.UNBIND', '解绑邮箱')}
            </button>
          </div>
        </div>
      )}
    </section>
  );
};

export default UserEmailBinding;
