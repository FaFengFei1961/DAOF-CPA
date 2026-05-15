import React, { Suspense, lazy } from 'react';
import { useTranslation } from 'react-i18next';
import { Shield } from 'lucide-react';
import { PageContainer, PageHeader } from '../../../components/ui';
import { useAdminConfigs } from '../../../hooks/useAdminConfigs';
import { SaveBar } from './_AdminFormPrimitives';

const ContentModerationGlobals = lazy(() => import('../../../components/ContentModerationGlobals'));

/**
 * Wrapper page for global content-moderation configuration.
 */
const ModerationPage = () => {
  const { t } = useTranslation();
  const { configs, loading, handleChange, handleSave } = useAdminConfigs();

  return (
    <PageContainer>
      <PageHeader
        title={t('ADMIN_SYS.MODERATION.TITLE')}
        sub={t('ADMIN_SYS.MODERATION.DESC')}
        icon={Shield}
      />
      <Suspense fallback={<div className="py-12 text-center text-sm text-on-surface-variant">{t('COMMON.LOADING')}</div>}>
        <ContentModerationGlobals configs={configs} handleChange={handleChange} />
      </Suspense>
      <SaveBar loading={loading} onSave={handleSave} />
    </PageContainer>
  );
};

export default ModerationPage;
