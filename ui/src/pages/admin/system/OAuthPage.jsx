import React from 'react';
import { useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { useAdminConfigs } from '../../../hooks/useAdminConfigs';
import { SaveBar, SecretInputField, SectionCard } from './_AdminFormPrimitives';
import { useMaskState } from '../../../hooks/useMaskState';

/**
 * GitHub OAuth credential sub-form.
 *
 * 注意：这是"第三方登录"分组里 GitHub 这一行的配置面板，不是整个 OAuth
 * 配置入口。Google 等其他 provider 的凭证目前由同一 tab 下不同的 SectionCard
 * 渲染（Google client_id/secret 字段也在 SysConfig 里，由前端按
 * oauth_provider_metadata 数组分别渲染配置区）。
 *
 * Sprint J-2: 仅作为 AuthAdminPage 的内嵌 tab 渲染。父级负责
 * PageContainer + PageHeader，本组件只输出表单 body。
 */
const OAuthPage = () => {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { configs, loading, handleChange, handleSave } = useAdminConfigs();
  const [mask, toggleMask] = useMaskState();

  return (
    <>
      <SectionCard title={t('ADMIN_SYS.OAUTH.APP_PARAMS_TITLE')}>
        <div className="flex flex-col gap-6">
          <SecretInputField
            label="Client ID" id="github_client_id"
            val={configs.github_client_id} onChange={handleChange}
            show={mask.github_client_id} onToggle={() => toggleMask('github_client_id')}
          />
          <SecretInputField
            label="Client Secret" id="github_client_secret"
            val={configs.github_client_secret} onChange={handleChange}
            show={mask.github_client_secret} onToggle={() => toggleMask('github_client_secret')}
            isPassword
          />
        </div>
      </SectionCard>

      <SectionCard title={t('ADMIN_SYS.OAUTH.CALLBACK_TITLE')}>
        <div className="flex flex-col md:flex-row md:items-center justify-between py-4 gap-4">
          <div className="flex flex-col gap-1 w-full md:w-2/3">
            <span className="text-on-surface-variant font-medium text-sm">
              {t('ADMIN_SYS.OAUTH.SERVER_ADDR_LABEL')}
            </span>
            <span className="text-xs text-on-surface-variant">
              {configs.server_address ? (
                <>
                  {t('ADMIN_SYS.OAUTH.CALLBACK_CURRENT_PREFIX')}<code className="font-mono text-primary">{configs.server_address}</code>
                  {' · '}{t('ADMIN_SYS.OAUTH.CALLBACK_AUTO_SUFFIX')} <code className="font-mono">/api/auth/oauth/github/callback</code>
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
