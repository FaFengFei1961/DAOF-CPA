import React, { Suspense, lazy } from 'react';
import { useTranslation } from 'react-i18next';
import { Shield } from 'lucide-react';
import { PageContainer, PageHeader } from '../../../components/ui';
import { useAdminConfigs } from '../../../hooks/useAdminConfigs';
import { SaveBar } from './_AdminFormPrimitives';

const ContentModerationGlobals = lazy(() => import('../../../components/ContentModerationGlobals'));

/**
 * ModerationPage — 内容审核全局配置（Phase 3）
 *
 * 包装 ContentModerationGlobals 组件（它本身依赖 props 注入 configs / handleChange）。
 * 修复 Phase 2 的路由直挂 bug：之前 routes.jsx 直接 <ContentModerationGlobals />
 * 没传 props，访问 configs.* 会崩。
 */
const ModerationPage = () => {
  const { t } = useTranslation();
  const { configs, loading, handleChange, handleSave } = useAdminConfigs();

  return (
    <PageContainer>
      <PageHeader
        title={t('SETTINGS.TAB_MODERATION', '内容审核')}
        sub={t('SETTINGS.MODERATION_DESC', '上游模型池审核 / 关键字 / 缓存 / 长 prompt 限制 / 多模态策略 / 拒绝文案的全局配置')}
        icon={Shield}
      />
      <Suspense fallback={<div className="py-12 text-center text-sm text-on-surface-variant">{t('APP.LOADING', '加载中...')}</div>}>
        <ContentModerationGlobals configs={configs} handleChange={handleChange} />
      </Suspense>
      <SaveBar loading={loading} onSave={handleSave} />
    </PageContainer>
  );
};

export default ModerationPage;
