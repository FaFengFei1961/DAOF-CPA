import React from 'react';
import { useTranslation } from 'react-i18next';
import { MessageSquare } from 'lucide-react';
import { PageContainer, PageHeader } from '../../../components/ui';
import { useAdminConfigs } from '../../../hooks/useAdminConfigs';
import { SaveBar, SecretInputField, SectionCard } from './_AdminFormPrimitives';
import { useMaskState } from '../../../hooks/useMaskState';

/**
 * SmsPage — admin 阿里云短信配置（Phase 3 抽出）
 *
 * 替换 Settings.jsx 内 activeTab === 'sms' 区块。
 */
const SmsPage = () => {
  const { t } = useTranslation();
  const { configs, loading, handleChange, handleSave } = useAdminConfigs();
  const [mask, toggleMask] = useMaskState();

  return (
    <PageContainer>
      <PageHeader
        title={t('SETTINGS.SMS_TITLE', '阿里云短信配置')}
        sub={t('SETTINGS.SECURE_ZONE_DESC', '安全区 — 涉及阿里云 RAM 凭证')}
        icon={MessageSquare}
      />

      <SectionCard
        title={t('SETTINGS.SMS_RAM_TITLE', 'RAM 子账号 AccessKey')}
        accent="bg-orange-500"
      >
        <div className="flex flex-col gap-6">
          <SecretInputField
            label="AccessKey ID" id="aliyun_access_key"
            val={configs.aliyun_access_key} onChange={handleChange}
            show={mask.aliyun_access_key} onToggle={() => toggleMask('aliyun_access_key')}
          />
          <SecretInputField
            label="AccessKey Secret" id="aliyun_access_secret"
            val={configs.aliyun_access_secret} onChange={handleChange}
            show={mask.aliyun_access_secret} onToggle={() => toggleMask('aliyun_access_secret')}
            isPassword
          />
        </div>
      </SectionCard>

      <SectionCard
        title={t('SETTINGS.SMS_TPL_TITLE', '签名与模板')}
        accent="bg-orange-500"
      >
        <div className="flex flex-col gap-6">
          <SecretInputField
            label={t('SETTINGS.SMS_SIGN_LABEL', '短信签名')} id="aliyun_sms_sign"
            val={configs.aliyun_sms_sign} onChange={handleChange}
            show={mask.aliyun_sms_sign} onToggle={() => toggleMask('aliyun_sms_sign')}
          />
          <SecretInputField
            label={t('SETTINGS.SMS_TPL_LABEL', '短信模板 Code')} id="aliyun_sms_template"
            val={configs.aliyun_sms_template} onChange={handleChange}
            show={mask.aliyun_sms_template} onToggle={() => toggleMask('aliyun_sms_template')}
          />
        </div>
      </SectionCard>

      <SaveBar loading={loading} onSave={handleSave} />
    </PageContainer>
  );
};

export default SmsPage;
