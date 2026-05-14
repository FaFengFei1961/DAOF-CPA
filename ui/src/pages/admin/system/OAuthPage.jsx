import React from 'react';
import { useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Key } from 'lucide-react';
import { PageContainer, PageHeader } from '../../../components/ui';
import { useAdminConfigs } from '../../../hooks/useAdminConfigs';
import { SaveBar, SecretInputField, SectionCard } from './_AdminFormPrimitives';
import { useMaskState } from '../../../hooks/useMaskState';

/**
 * OAuthPage — admin GitHub OAuth 配置（Phase 3 抽出）
 *
 * 替换 Settings.jsx 内 activeTab === 'oauth' 区块。
 * 使用 useAdminConfigs hook 共享 fetch/save 逻辑。
 */
const OAuthPage = () => {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { configs, loading, handleChange, handleSave } = useAdminConfigs();
  const [mask, toggleMask] = useMaskState();

  return (
    <PageContainer>
      <PageHeader
        title={t('SETTINGS.OAUTH_TITLE', 'GitHub OAuth')}
        sub={t('SETTINGS.SECURE_ZONE_DESC', '安全区 — 涉及第三方鉴权凭证')}
        icon={Key}
      />

      <SectionCard title={t('SETTINGS.OAUTH_APP_PARAMS', 'OAuth 应用参数')}>
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

      <SectionCard title={t('SETTINGS.OAUTH_CALLBACK_TITLE', 'OAuth 回调地址')}>
        <div className="flex flex-col md:flex-row md:items-center justify-between py-4 gap-4">
          <div className="flex flex-col gap-1 w-full md:w-2/3">
            <span className="text-on-surface-variant font-medium text-sm">
              {t('SETTINGS.SERVER_ADDR_LABEL', '服务地址')}
            </span>
            <span className="text-xs text-on-surface-variant">
              {configs.server_address ? (
                <>
                  当前值：<code className="font-mono text-primary">{configs.server_address}</code>
                  {' · '}OAuth 回调地址将自动拼接 <code className="font-mono">/api/auth/github</code>
                </>
              ) : (
                <>
                  尚未配置。请在{' '}
                  <button type="button" onClick={() => navigate('/admin/finance')}
                    className="text-primary underline hover:opacity-80">
                    财务工作区 → 基础设置
                  </button>
                  {' '}中填入 server_address。
                </>
              )}
            </span>
          </div>
          <button
            type="button"
            onClick={() => navigate('/admin/finance')}
            className="bg-surface-container-high border border-outline rounded-lg px-4 py-2 text-sm text-on-surface hover:border-primary transition-colors"
          >
            去财务工作区
          </button>
        </div>
      </SectionCard>

      <SaveBar loading={loading} onSave={handleSave} />
    </PageContainer>
  );
};

export default OAuthPage;
