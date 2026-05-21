import React, { useState } from 'react';
import { useTranslation } from 'react-i18next';
// IA audit M-IA2 fix: theme-mode + seed-color controls deleted from this
// page; Settings.jsx is now the single source. useTheme + SEED_COLORS no
// longer imported. Monitor stays — it's the PageHeader icon.
import { Monitor, Server, Save, Eye, EyeOff, ShieldCheck } from 'lucide-react';
import { PageContainer, PageHeader, Section } from '../../../components/ui';
import TextInput from '../../../components/ui/TextInput';
import { useAdminConfigs } from '../../../hooks/useAdminConfigs';
import { useAuth } from '../../../context/AuthContext';
import { authFetch } from '../../../utils/authFetch';
import toast from 'react-hot-toast';

/**
 * Admin general settings for CLIProxyAPI connection details and admin
 * credentials. Appearance moved out — see Settings.jsx for the single
 * source of theme + seed color controls.
 */
const GeneralAdminPage = () => {
  const { t } = useTranslation();
  const { configs, loading, handleChange } = useAdminConfigs();
  const { signOut } = useAuth();
  const [showClipKey, setShowClipKey] = useState(false);
  const [savingClip, setSavingClip] = useState(false);

  // 管理员账号修改：username + password。提交后 backend 会 RevokeSessionsForUser，
  // 当前 admin cookie 立即失效，前端登出回登录页用新凭证重登。
  const [cred, setCred] = useState({ username: '', password: '', confirm: '' });
  const [showPwd, setShowPwd] = useState(false);
  const [savingCred, setSavingCred] = useState(false);

  const saveCredentials = async () => {
    const username = cred.username.trim();
    const password = cred.password;
    if (!username || !password) {
      toast.error(t('ADMIN_SYS.GENERAL.ACCOUNT_FIELDS_REQUIRED', '用户名和新密码均不能为空'));
      return;
    }
    if (username.toLowerCase() === 'root') {
      toast.error(t('ADMIN_SYS.GENERAL.ACCOUNT_ROOT_FORBIDDEN', '不能再使用 root，请设新的管理员代号'));
      return;
    }
    if (password.length < 8) {
      toast.error(t('ADMIN_SYS.GENERAL.ACCOUNT_PASSWORD_TOO_SHORT', '密码至少 8 位'));
      return;
    }
    if (password !== cred.confirm) {
      toast.error(t('ADMIN_SYS.GENERAL.ACCOUNT_PASSWORD_MISMATCH', '两次输入的密码不一致'));
      return;
    }
    setSavingCred(true);
    try {
      const data = await authFetch('/api/admin/credentials', {
        method: 'PUT',
        body: { username, password },
      });
      if (data.success) {
        toast.success(data.message || t('ADMIN_SYS.GENERAL.ACCOUNT_SAVE_OK', '账号已更新，请用新凭证重新登录'));
        // 后端已 RevokeSessionsForUser，本地 cookie/session 已失效。
        // 给 toast 1.5s 露脸时间，然后强制登出回登录页。
        setTimeout(() => { signOut(); }, 1500);
      } else {
        toast.error(data.message || t('ADMIN_SYS.GENERAL.ACCOUNT_SAVE_FAIL', '账号更新失败'));
      }
    } catch (e) {
      toast.error(t('ADMIN_SYS.GENERAL.ACCOUNT_SAVE_FAIL', '账号更新失败'));
    } finally {
      setSavingCred(false);
    }
  };

  const saveClipProxy = async () => {
    if (!(configs.cliproxy_url || '').trim()) {
      toast.error(t('ADMIN_SYS.GENERAL.CLIPROXY_URL_REQUIRED'));
      return;
    }
    setSavingClip(true);
    try {
      const data = await authFetch('/api/admin/config', {
        method: 'POST',
        body: {
          cliproxy_url: (configs.cliproxy_url || '').trim(),
          cliproxy_key: (configs.cliproxy_key || '').trim(),
        },
      });
      if (data.success) {
        toast.success(t('ADMIN_SYS.GENERAL.CLIPROXY_SAVE_OK'));
      } else {
        toast.error(data.message || t('SETTINGS.SAVE_FAILED'));
      }
    } catch {
      toast.error(t('SETTINGS.SAVE_FAILED'));
    } finally {
      setSavingClip(false);
    }
  };

  return (
    <PageContainer>
      <PageHeader
        title={t('ADMIN_SYS.GENERAL.TITLE')}
        sub={t('ADMIN_SYS.GENERAL.DESC')}
        icon={Monitor}
      />

      <Section
        title={t('ADMIN_SYS.GENERAL.ACCOUNT_TITLE', '管理员账号')}
        sub={t('ADMIN_SYS.GENERAL.ACCOUNT_DESC', '修改后所有 admin 会话立即失效，需用新凭证重新登录。')}
        icon={ShieldCheck}
        actions={
          <button
            type="button"
            onClick={saveCredentials}
            disabled={savingCred}
            className="flex items-center gap-2 px-5 py-2 bg-primary text-on-primary rounded-full text-sm font-medium hover:opacity-90 disabled:opacity-50"
          >
            <Save size={14} />
            {savingCred
              ? t('COMMON.SAVING', '保存中…')
              : t('ADMIN_SYS.GENERAL.ACCOUNT_SAVE', '更新凭证并登出')}
          </button>
        }
      >
        <div className="flex flex-col md:flex-row md:items-center justify-between py-3 border-b border-outline-variant/20 gap-3">
          <div className="flex flex-col gap-1 w-full md:w-2/5">
            <span className="text-on-surface-variant font-medium text-sm">
              {t('ADMIN_SYS.GENERAL.ACCOUNT_USERNAME_LABEL', '新用户名')}
            </span>
            <span className="text-xs text-outline">
              {t('ADMIN_SYS.GENERAL.ACCOUNT_USERNAME_HINT', '不允许使用 root；至少 1 个字符')}
            </span>
          </div>
          <div className="w-full md:w-72">
            <TextInput
              type="text"
              value={cred.username}
              onChange={(e) => setCred((p) => ({ ...p, username: e.target.value }))}
              placeholder={t('ADMIN_SYS.GENERAL.ACCOUNT_USERNAME_PH', '例如 admin')}
              autoComplete="username"
            />
          </div>
        </div>
        <div className="flex flex-col md:flex-row md:items-center justify-between py-3 border-b border-outline-variant/20 gap-3">
          <div className="flex flex-col gap-1 w-full md:w-2/5">
            <span className="text-on-surface-variant font-medium text-sm">
              {t('ADMIN_SYS.GENERAL.ACCOUNT_PASSWORD_LABEL', '新密码')}
            </span>
            <span className="text-xs text-outline">
              {t('ADMIN_SYS.GENERAL.ACCOUNT_PASSWORD_HINT', '至少 8 位，建议混合大小写字母 + 数字 + 符号')}
            </span>
          </div>
          <div className="w-full md:w-72">
            <TextInput
              type={showPwd ? 'text' : 'password'}
              value={cred.password}
              onChange={(e) => setCred((p) => ({ ...p, password: e.target.value }))}
              placeholder="••••••••"
              autoComplete="new-password"
              suffix={showPwd ? EyeOff : Eye}
              onSuffixClick={() => setShowPwd((v) => !v)}
            />
          </div>
        </div>
        <div className="flex flex-col md:flex-row md:items-center justify-between py-3 gap-3">
          <div className="flex flex-col gap-1 w-full md:w-2/5">
            <span className="text-on-surface-variant font-medium text-sm">
              {t('ADMIN_SYS.GENERAL.ACCOUNT_CONFIRM_LABEL', '确认新密码')}
            </span>
            <span className="text-xs text-outline">
              {t('ADMIN_SYS.GENERAL.ACCOUNT_CONFIRM_HINT', '再输入一次防止误打')}
            </span>
          </div>
          <div className="w-full md:w-72">
            <TextInput
              type={showPwd ? 'text' : 'password'}
              value={cred.confirm}
              onChange={(e) => setCred((p) => ({ ...p, confirm: e.target.value }))}
              placeholder="••••••••"
              autoComplete="new-password"
            />
          </div>
        </div>
      </Section>

      {/*
        IA audit M-IA2 fix: dropped the duplicate theme-mode + seed-color
        controls that mirrored what Settings.jsx already exposes (and the
        same useTheme() context already drives globally from the TopBar
        currency/language toggle area). Two control surfaces for one piece
        of state was confusing — admin should change appearance from the
        same place every user does.
      */}

      <Section
        title={t('ADMIN_SYS.GENERAL.CLIPROXY_TITLE')}
        sub={t('ADMIN_SYS.GENERAL.CLIPROXY_DESC')}
        icon={Server}
        actions={
          <button
            type="button"
            onClick={saveClipProxy}
            disabled={loading || savingClip}
            className="flex items-center gap-2 px-5 py-2 bg-primary text-on-primary rounded-full text-sm font-medium hover:opacity-90 disabled:opacity-50"
          >
            <Save size={14} />
            {savingClip ? t('COMMON.SAVING') : t('ADMIN_SYS.GENERAL.CLIPROXY_SAVE')}
          </button>
        }
      >
        <div className="flex flex-col md:flex-row md:items-center justify-between py-3 border-b border-outline-variant/20 gap-3">
          <div className="flex flex-col gap-1 w-full md:w-2/5">
            <span className="text-on-surface-variant font-medium text-sm">{t('ADMIN_SYS.GENERAL.CLIPROXY_URL_LABEL')}</span>
            <span className="text-xs text-outline">{t('ADMIN_SYS.GENERAL.CLIPROXY_URL_HINT')}</span>
          </div>
          <div className="w-full md:w-72">
            <TextInput
              type="text"
              value={configs.cliproxy_url || ''}
              onChange={e => handleChange('cliproxy_url', e.target.value)}
              placeholder="http://127.0.0.1:8080"
            />
          </div>
        </div>

        <div className="flex flex-col md:flex-row md:items-center justify-between py-3 gap-3">
          <div className="flex flex-col gap-1 w-full md:w-2/5">
            <span className="text-on-surface-variant font-medium text-sm">Management Key</span>
            <span className="text-xs text-outline">{t('ADMIN_SYS.GENERAL.CLIPROXY_KEY_HINT')}</span>
          </div>
          <div className="w-full md:w-72">
            <TextInput
              type={showClipKey ? 'text' : 'password'}
              value={configs.cliproxy_key || ''}
              onChange={e => handleChange('cliproxy_key', e.target.value)}
              placeholder={t('ADMIN_SYS.GENERAL.CLIPROXY_KEY_PLACEHOLDER')}
              suffix={showClipKey ? EyeOff : Eye}
              onSuffixClick={() => setShowClipKey(v => !v)}
            />
          </div>
        </div>
      </Section>
    </PageContainer>
  );
};

export default GeneralAdminPage;
