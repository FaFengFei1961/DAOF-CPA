// Phase G-1.8（2026-05-20）：Admin SMTP 配置面板。
//
// 与其他 admin/system/*Page.jsx 不同，本页用 G-1.6 的专用端点（/api/admin/email/config）
// 而不是通用 BatchUpdateSysConfigs：
//   - GET 返回 has_password 而不是密码 blob（共享终端不暴露密钥）
//   - PUT 用指针字段（nil = 不修改）；password 留空不动，输入新值即替换
//   - 还多了一个 test-send 按钮，admin 验证 SMTP 拨号 + 模板渲染 + 服务商可达
import React, { useState, useEffect, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { Mail, Send, ShieldCheck, AlertTriangle } from 'lucide-react';
import toast from 'react-hot-toast';
import { authFetch } from '../../../utils/authFetch';
import { PageContainer, PageHeader } from '../../../components/ui';
import { SectionCard } from './_AdminFormPrimitives';

const EmailPage = () => {
  const { t } = useTranslation();
  const [config, setConfig] = useState(null);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [testing, setTesting] = useState(false);
  // 表单 staging：所有字段独立保留，password 默认空表示"不改"
  const [form, setForm] = useState({});
  const [testTo, setTestTo] = useState('');

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const json = await authFetch('/api/admin/email/config');
      if (json?.success && json.data) {
        setConfig(json.data);
        setForm({
          email_enabled: json.data.email_enabled,
          email_signup_enabled: json.data.email_signup_enabled,
          email_login_enabled: json.data.email_login_enabled,
          smtp_host: json.data.smtp_host || '',
          smtp_port: json.data.smtp_port || 0,
          smtp_username: json.data.smtp_username || '',
          smtp_password: '', // 永远空开始（不回显）
          smtp_from: json.data.smtp_from || '',
          smtp_reply_to: json.data.smtp_reply_to || '',
          smtp_use_implicit_tls: json.data.smtp_use_implicit_tls,
          rate_limit_per_email_hourly: json.data.rate_limit_per_email_hourly || 5,
          rate_limit_per_ip_hourly: json.data.rate_limit_per_ip_hourly || 20,
          verify_ttl_seconds: json.data.verify_ttl_seconds || 3600,
          reset_ttl_seconds: json.data.reset_ttl_seconds || 900,
        });
      }
    } catch {
      toast.error(t('ADMIN_SYS.EMAIL.LOAD_FAIL', '加载邮件配置失败'));
    } finally {
      setLoading(false);
    }
  }, [t]);

  useEffect(() => { load(); }, [load]);

  const handleField = (key, value) => setForm((prev) => ({ ...prev, [key]: value }));

  const handleSave = async () => {
    setSaving(true);
    // 只把"用户实际改过 / 主动填写"的字段发出去。
    // password 空字符串 → 不传（保留原密码）；非空 → 传新密码替换。
    const body = {
      email_enabled: !!form.email_enabled,
      email_signup_enabled: !!form.email_signup_enabled,
      email_login_enabled: !!form.email_login_enabled,
      smtp_host: form.smtp_host || '',
      smtp_port: Number(form.smtp_port) || 0,
      smtp_username: form.smtp_username || '',
      smtp_from: form.smtp_from || '',
      smtp_reply_to: form.smtp_reply_to || '',
      smtp_use_implicit_tls: !!form.smtp_use_implicit_tls,
      rate_limit_per_email_hourly: Number(form.rate_limit_per_email_hourly) || 5,
      rate_limit_per_ip_hourly: Number(form.rate_limit_per_ip_hourly) || 20,
      verify_ttl_seconds: Number(form.verify_ttl_seconds) || 3600,
      reset_ttl_seconds: Number(form.reset_ttl_seconds) || 900,
    };
    if (form.smtp_password && form.smtp_password.length > 0) {
      body.smtp_password = form.smtp_password;
    }
    try {
      const json = await authFetch('/api/admin/email/config', { method: 'PUT', body });
      if (json?.success) {
        toast.success(t('ADMIN_SYS.EMAIL.SAVED', '邮件配置已保存'));
        setForm((prev) => ({ ...prev, smtp_password: '' })); // 清空 password input
        await load();
      } else {
        toast.error(json?.message_code ? t(`API.${json.message_code}`, json.message) : (json?.message || t('ADMIN_SYS.EMAIL.SAVE_FAIL', '保存失败')));
      }
    } catch {
      toast.error(t('ADMIN_SYS.EMAIL.SAVE_FAIL', '保存失败'));
    } finally {
      setSaving(false);
    }
  };

  const handleTest = async () => {
    if (!testTo || !testTo.includes('@')) {
      toast.error(t('ADMIN_SYS.EMAIL.TEST_INVALID', '请输入有效的收件邮箱'));
      return;
    }
    setTesting(true);
    try {
      const json = await authFetch('/api/admin/email/test-send', { method: 'POST', body: { to: testTo } });
      if (json?.success) {
        toast.success(t('ADMIN_SYS.EMAIL.TEST_SENT', '测试邮件已发送，请查收'));
      } else {
        const detail = json?.detail ? ` (${json.detail})` : '';
        toast.error((json?.message_code ? t(`API.${json.message_code}`, json.message) : (json?.message || t('ADMIN_SYS.EMAIL.TEST_FAIL', '测试失败'))) + detail);
      }
    } catch {
      toast.error(t('ADMIN_SYS.EMAIL.TEST_FAIL', '测试失败'));
    } finally {
      setTesting(false);
    }
  };

  if (loading) {
    return <div className="text-on-surface-variant p-8 text-center">{t('COMMON.LOADING', '加载中…')}</div>;
  }

  const isReady = !!config?.is_ready;
  const hasPwd = !!config?.has_password;

  return (
    <PageContainer>
      <PageHeader
        title={t('ADMIN_SYS.EMAIL.TITLE', '邮件 / SMTP 配置')}
        sub={t('ADMIN_SYS.EMAIL.DESC', '配置 SMTP 服务器并控制邮件功能的启用范围（绑定 / 注册 / 登录）')}
        icon={Mail}
      />

      <SectionCard title={t('ADMIN_SYS.EMAIL.MASTER_TITLE', '功能开关')} accent="bg-primary">
        <div className="space-y-3 text-sm">
          <label className="flex items-center gap-3 cursor-pointer">
            <input type="checkbox" className="w-4 h-4 accent-primary"
              checked={!!form.email_enabled}
              onChange={(e) => handleField('email_enabled', e.target.checked)} />
            <div>
              <div className="text-on-surface">{t('ADMIN_SYS.EMAIL.MASTER_ENABLED', '启用邮箱功能（master switch）')}</div>
              <div className="text-[11px] text-on-surface-variant">
                {t('ADMIN_SYS.EMAIL.MASTER_ENABLED_HINT', '关闭后所有邮件相关接口（绑定、验证、注册、登录、通知）一律 503。')}
              </div>
            </div>
          </label>
          <label className="flex items-center gap-3 cursor-pointer">
            <input type="checkbox" className="w-4 h-4 accent-primary"
              disabled={!form.email_enabled}
              checked={!!form.email_signup_enabled}
              onChange={(e) => handleField('email_signup_enabled', e.target.checked)} />
            <div>
              <div className={`${form.email_enabled ? 'text-on-surface' : 'text-on-surface-variant'}`}>{t('ADMIN_SYS.EMAIL.SIGNUP_ENABLED', '允许邮箱+密码注册')}</div>
              <div className="text-[11px] text-on-surface-variant">
                {t('ADMIN_SYS.EMAIL.SIGNUP_ENABLED_HINT', '关闭时新用户只能用 GitHub 登录注册（Phase G-2 后生效）。')}
              </div>
            </div>
          </label>
          <label className="flex items-center gap-3 cursor-pointer">
            <input type="checkbox" className="w-4 h-4 accent-primary"
              disabled={!form.email_enabled}
              checked={!!form.email_login_enabled}
              onChange={(e) => handleField('email_login_enabled', e.target.checked)} />
            <div>
              <div className={`${form.email_enabled ? 'text-on-surface' : 'text-on-surface-variant'}`}>{t('ADMIN_SYS.EMAIL.LOGIN_ENABLED', '允许邮箱+密码登录')}</div>
              <div className="text-[11px] text-on-surface-variant">
                {t('ADMIN_SYS.EMAIL.LOGIN_ENABLED_HINT', '用户还需在自己的设置里 opt-in 才能用邮箱登录（双 gate；Phase G-2 后生效）。')}
              </div>
            </div>
          </label>
          <div className={`flex items-center gap-2 text-xs px-3 py-2 rounded-control border ${isReady ? 'border-green-500/30 bg-green-500/10 text-green-400' : 'border-amber-500/30 bg-amber-500/10 text-amber-400'}`}>
            {isReady ? <ShieldCheck size={14} /> : <AlertTriangle size={14} />}
            {isReady
              ? t('ADMIN_SYS.EMAIL.READY', 'SMTP 配置完整且功能已启用，可正常发送邮件')
              : t('ADMIN_SYS.EMAIL.NOT_READY', '尚未完成 SMTP 配置或 master 未打开 —— 邮件不会发出')}
          </div>
        </div>
      </SectionCard>

      <SectionCard title={t('ADMIN_SYS.EMAIL.SMTP_TITLE', 'SMTP 服务器')} accent="bg-secondary">
        <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
          <Field label={t('ADMIN_SYS.EMAIL.HOST', '服务器地址')} hint="smtp.gmail.com / smtp.qq.com"
            value={form.smtp_host} onChange={(v) => handleField('smtp_host', v)} />
          <Field label={t('ADMIN_SYS.EMAIL.PORT', '端口')} hint="465 (TLS) / 587 (STARTTLS) — 25 禁止"
            type="number" value={form.smtp_port} onChange={(v) => handleField('smtp_port', v)} />
          <Field label={t('ADMIN_SYS.EMAIL.USERNAME', '用户名')} hint=""
            value={form.smtp_username} onChange={(v) => handleField('smtp_username', v)} />
          <Field
            label={t('ADMIN_SYS.EMAIL.PASSWORD', '密码 / app-password')}
            hint={hasPwd
              ? t('ADMIN_SYS.EMAIL.PASSWORD_KEEP_HINT', '当前已配置 · 留空保留原密码 · 输入新密码即替换')
              : t('ADMIN_SYS.EMAIL.PASSWORD_NEW_HINT', '未配置 · 请输入 SMTP 密码或 app-password')}
            type="password"
            value={form.smtp_password}
            onChange={(v) => handleField('smtp_password', v)} />
          <Field label={t('ADMIN_SYS.EMAIL.FROM', 'From 地址')} hint='例：DAOF-CPA &lt;noreply@example.com&gt;'
            value={form.smtp_from} onChange={(v) => handleField('smtp_from', v)} />
          <Field label={t('ADMIN_SYS.EMAIL.REPLY_TO', 'Reply-To（可选）')} hint=""
            value={form.smtp_reply_to} onChange={(v) => handleField('smtp_reply_to', v)} />
          <label className="flex items-center gap-3 cursor-pointer md:col-span-2">
            <input type="checkbox" className="w-4 h-4 accent-primary"
              checked={!!form.smtp_use_implicit_tls}
              onChange={(e) => handleField('smtp_use_implicit_tls', e.target.checked)} />
            <div>
              <div className="text-sm text-on-surface">{t('ADMIN_SYS.EMAIL.IMPLICIT_TLS', 'Implicit TLS')}</div>
              <div className="text-[11px] text-on-surface-variant">
                {t('ADMIN_SYS.EMAIL.IMPLICIT_TLS_HINT', '465 端口选中；587 STARTTLS 不选中（自动协商）')}
              </div>
            </div>
          </label>
        </div>
      </SectionCard>

      <SectionCard title={t('ADMIN_SYS.EMAIL.QUOTA_TITLE', '限流与 TTL')} accent="bg-tertiary">
        <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
          <Field label={t('ADMIN_SYS.EMAIL.RATE_EMAIL', '每邮箱每小时上限')}
            hint="1-1000"
            type="number" value={form.rate_limit_per_email_hourly} onChange={(v) => handleField('rate_limit_per_email_hourly', v)} />
          <Field label={t('ADMIN_SYS.EMAIL.RATE_IP', '每 IP 每小时上限')}
            hint="1-10000"
            type="number" value={form.rate_limit_per_ip_hourly} onChange={(v) => handleField('rate_limit_per_ip_hourly', v)} />
          <Field label={t('ADMIN_SYS.EMAIL.VERIFY_TTL', '验证 token TTL（秒）')}
            hint="60-86400 · 默认 3600 (1 小时)"
            type="number" value={form.verify_ttl_seconds} onChange={(v) => handleField('verify_ttl_seconds', v)} />
          <Field label={t('ADMIN_SYS.EMAIL.RESET_TTL', '密码重置 TTL（秒）')}
            hint="60-86400 · 默认 900 (15 分钟)"
            type="number" value={form.reset_ttl_seconds} onChange={(v) => handleField('reset_ttl_seconds', v)} />
        </div>
      </SectionCard>

      <SectionCard title={t('ADMIN_SYS.EMAIL.TEST_TITLE', '测试发送')} accent="bg-warning">
        <div className="space-y-3 text-sm">
          <p className="text-on-surface-variant text-[12px]">
            {t('ADMIN_SYS.EMAIL.TEST_DESC', '保存配置后，可发送一封测试邮件到指定地址，验证 SMTP 服务器、TLS 与服务商风控是否正常。')}
          </p>
          <div className="flex gap-2">
            <input
              type="email"
              placeholder="admin@example.com"
              value={testTo}
              onChange={(e) => setTestTo(e.target.value)}
              className="flex-1 h-10 bg-black/40 border border-outline rounded-control px-3 text-sm text-on-surface focus:border-primary outline-none"
            />
            <button
              type="button"
              onClick={handleTest}
              disabled={testing}
              className="h-10 px-4 bg-primary text-on-primary rounded-control text-sm font-medium inline-flex items-center gap-2 disabled:opacity-50"
            >
              <Send size={16} />
              {testing ? t('COMMON.SAVING', '提交中...') : t('ADMIN_SYS.EMAIL.TEST_BUTTON', '发送测试邮件')}
            </button>
          </div>
        </div>
      </SectionCard>

      <div className="flex items-center justify-end gap-3 mb-12">
        <button
          type="button"
          onClick={handleSave}
          disabled={saving}
          className="fl-btn fl-btn-prominent disabled:opacity-50"
        >
          {saving ? t('COMMON.SAVING', '保存中...') : t('COMMON.SAVE', '保存')}
        </button>
      </div>
    </PageContainer>
  );
};

const Field = ({ label, hint, value, onChange, type = 'text' }) => {
  return (
    <div className="flex flex-col gap-1.5">
      <label className="text-xs font-semibold text-on-surface-variant ml-1">{label}</label>
      <input
        type={type}
        value={value ?? ''}
        onChange={(e) => onChange(type === 'number' ? Number(e.target.value) : e.target.value)}
        className="h-10 bg-black/40 border border-outline rounded-control px-3 text-sm text-on-surface focus:border-primary outline-none"
      />
      {hint && <span className="text-[10px] text-on-surface-variant ml-1">{hint}</span>}
    </div>
  );
};

export default EmailPage;
