import React from 'react';
import { useTranslation } from 'react-i18next';
import { MessageSquare } from 'lucide-react';
import { PageContainer, PageHeader } from '../../../components/ui';
import { useAdminConfigs } from '../../../hooks/useAdminConfigs';
import { SaveBar, SecretInputField, SectionCard } from './_AdminFormPrimitives';
import { useMaskState } from '../../../hooks/useMaskState';

/**
 * Aliyun SMS credential and template configuration.
 */
const SmsPage = () => {
  const { t } = useTranslation();
  const { configs, loading, handleChange, handleSave } = useAdminConfigs();
  const [mask, toggleMask] = useMaskState();

  return (
    <PageContainer>
      <PageHeader
        title={t('ADMIN_SYS.SMS.TITLE')}
        sub={t('ADMIN_SYS.SMS.DESC')}
        icon={MessageSquare}
      />

      <SectionCard
        title={t('ADMIN_SYS.SMS.RAM_TITLE')}
        accent="bg-warning"
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
        title={t('ADMIN_SYS.SMS.TEMPLATE_TITLE')}
        accent="bg-warning"
      >
        <div className="flex flex-col gap-6">
          <SecretInputField
            label={t('ADMIN_SYS.SMS.SIGN_LABEL')} id="aliyun_sms_sign"
            val={configs.aliyun_sms_sign} onChange={handleChange}
            show={mask.aliyun_sms_sign} onToggle={() => toggleMask('aliyun_sms_sign')}
          />
          <SecretInputField
            label={t('ADMIN_SYS.SMS.TEMPLATE_CODE_LABEL')} id="aliyun_sms_template"
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
