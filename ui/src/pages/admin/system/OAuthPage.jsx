import React from 'react';
import { useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
// lucide-react 已删 Github / Google 商标 icon —— 用通用 Key/Globe 替代，保持 UI 一致。
import { Key, Globe, AlertCircle } from 'lucide-react';
import { useAdminConfigs } from '../../../hooks/useAdminConfigs';
import { SaveBar, SecretInputField, SectionCard } from './_AdminFormPrimitives';
import { useMaskState } from '../../../hooks/useMaskState';

/**
 * 第三方登录凭证配置 sub-form。
 *
 * 数据驱动渲染：每个 OAuth provider 在 PROVIDER_DEFS 里一行配置，按数组
 * 顺序生成"GitHub OAuth / Google OAuth / ..."独立 SectionCard。后端
 * (oauth_provider_*.go) 已实现 GitHub + Google，前端这里要把两边都暴露
 * 配置 UI ——之前只渲 GitHub，Google 凭证字段在后端但 admin UI 里看不见。
 *
 * Sprint J-2: 仅作为 AuthAdminPage 的内嵌 tab 渲染；父级负责
 * PageContainer + PageHeader，本组件只输出表单 body。
 */

// 添加新 provider 时只需加一行 PROVIDER_DEFS + 后端实现 OAuthProvider interface。
// labelKey 走 i18n（fallback 给英文是 provider 商标名，保持品牌一致）。
const PROVIDER_DEFS = [
  {
    key: 'github',
    label: 'GitHub',
    icon: Key,
    idField: 'github_client_id',
    secretField: 'github_client_secret',
    docsURL: 'https://github.com/settings/developers',
    docsHint: 'Settings → Developer settings → OAuth Apps',
  },
  {
    key: 'google',
    label: 'Google',
    icon: Globe,
    idField: 'google_client_id',
    secretField: 'google_client_secret',
    docsURL: 'https://console.cloud.google.com/apis/credentials',
    docsHint: 'Google Cloud Console → APIs & Services → Credentials',
  },
];

const OAuthPage = () => {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { configs, loading, handleChange, handleSave } = useAdminConfigs();
  const [mask, toggleMask] = useMaskState();

  const serverAddress = configs.server_address || '';

  return (
    <>
      {PROVIDER_DEFS.map((p) => {
        const idVal = configs[p.idField] || '';
        const secretVal = configs[p.secretField] || '';
        const fullyConfigured = idVal !== '' && secretVal !== '';
        const Icon = p.icon;
        return (
          <SectionCard
            key={p.key}
            title={
              <span className="flex items-center gap-2">
                <Icon size={16} className="opacity-80" aria-hidden="true" />
                <span>{p.label} OAuth</span>
                {fullyConfigured ? (
                  <span className="chip chip-success ml-2">
                    {t('ADMIN_SYS.OAUTH.STATUS_CONFIGURED', '已配置')}
                  </span>
                ) : (
                  <span className="chip chip-warning ml-2">
                    <AlertCircle size={10} strokeWidth={2.4} />
                    {t('ADMIN_SYS.OAUTH.STATUS_INCOMPLETE', '未配置')}
                  </span>
                )}
              </span>
            }
          >
            <div className="flex flex-col gap-6">
              <SecretInputField
                label="Client ID"
                id={p.idField}
                val={idVal}
                onChange={handleChange}
                show={mask[p.idField]}
                onToggle={() => toggleMask(p.idField)}
              />
              <SecretInputField
                label="Client Secret"
                id={p.secretField}
                val={secretVal}
                onChange={handleChange}
                show={mask[p.secretField]}
                onToggle={() => toggleMask(p.secretField)}
                isPassword
              />
              <div className="text-xs text-on-surface-variant pl-1">
                {t('ADMIN_SYS.OAUTH.DOCS_HINT_PREFIX', '从')}{' '}
                <a
                  href={p.docsURL}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="text-primary hover:underline font-mono"
                >
                  {p.docsHint}
                </a>{' '}
                {t('ADMIN_SYS.OAUTH.DOCS_HINT_SUFFIX', '获取凭证')}
                {serverAddress && (
                  <>
                    {' · '}
                    {t('ADMIN_SYS.OAUTH.CALLBACK_FOR_PROVIDER', '回调地址：')}
                    <code className="font-mono text-primary">{serverAddress.replace(/\/$/, '')}/api/auth/oauth/{p.key}/callback</code>
                  </>
                )}
              </div>
            </div>
          </SectionCard>
        );
      })}

      <SectionCard title={t('ADMIN_SYS.OAUTH.CALLBACK_TITLE')}>
        <div className="flex flex-col md:flex-row md:items-center justify-between py-4 gap-4">
          <div className="flex flex-col gap-1 w-full md:w-2/3">
            <span className="text-on-surface-variant font-medium text-sm">
              {t('ADMIN_SYS.OAUTH.SERVER_ADDR_LABEL')}
            </span>
            <span className="text-xs text-on-surface-variant">
              {serverAddress ? (
                <>
                  {t('ADMIN_SYS.OAUTH.CALLBACK_CURRENT_PREFIX')}<code className="font-mono text-primary">{serverAddress}</code>
                  {' · '}{t('ADMIN_SYS.OAUTH.CALLBACK_AUTO_SUFFIX_MULTI', '回调路径按 provider 自动拼接：/api/auth/oauth/<provider>/callback')}
                </>
              ) : (
                <>
                  {t('ADMIN_SYS.OAUTH.CALLBACK_EMPTY_PREFIX')}{' '}
                  <button type="button" onClick={() => navigate('/admin/finance')}
                    className="text-primary underline hover:opacity-80">
                    {t('ADMIN_SYS.OAUTH.FINANCE_SETTINGS_LINK')}
                  </button>
                  {' '}{t('ADMIN_SYS.OAUTH.CALLBACK_EMPTY_SUFFIX')}
                </>
              )}
            </span>
          </div>
          <button
            type="button"
            onClick={() => navigate('/admin/finance')}
            className="bg-surface-container-high border border-outline rounded-control px-4 py-2 text-sm text-on-surface hover:border-primary transition-colors"
          >
            {t('ADMIN_SYS.OAUTH.GOTO_FINANCE')}
          </button>
        </div>
      </SectionCard>

      <SaveBar loading={loading} onSave={handleSave} />
    </>
  );
};

export default OAuthPage;
