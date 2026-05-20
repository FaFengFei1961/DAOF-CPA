import React from 'react';
import { useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Key } from 'lucide-react';
import { PageContainer, PageHeader } from '../../../components/ui';
import { useAdminConfigs } from '../../../hooks/useAdminConfigs';
import { SaveBar, SecretInputField, SectionCard } from './_AdminFormPrimitives';
import { useMaskState } from '../../../hooks/useMaskState';

/**
 * GitHub OAuth configuration page.
 */
const OAuthPage = () => {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { configs, loading, handleChange, handleSave } = useAdminConfigs();
  const [mask, toggleMask] = useMaskState();

  return (
    <PageContainer>
      <PageHeader
        title={t('ADMIN_SYS.OAUTH.TITLE')}
        sub={t('ADMIN_SYS.OAUTH.DESC')}
        icon={Key}
      />

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
    </PageContainer>
  );
};

export default OAuthPage;
