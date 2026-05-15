import React, { useEffect, useState, useCallback, Suspense, lazy } from 'react';
import { useTranslation } from 'react-i18next';
import toast from 'react-hot-toast';
import { Package as PackageIcon, RefreshCw, Save } from 'lucide-react';
import { PageContainer, PageHeader, Section } from '../../../components/ui';
import Select from '../../../components/ui/Select';
import { useAdminConfigs } from '../../../hooks/useAdminConfigs';

const CouponManagement = lazy(() => import('../../../components/CouponManagement'));

/**
 * CouponsPage — admin 优惠券模板（Phase 3 修 P2 路由 bug）
 *
 * 包含两块：
 *   1. 新用户自动发券配置（signup_coupon_template_id）— 用 useAdminConfigs 共享 fetch/save
 *   2. CouponManagement 组件 — 自己管理 templates CRUD
 *
 * 修复 Phase 2 的路由直挂 bug：之前 /admin/coupons → CouponManagement 而失去了 signup
 * 配置入口（旧 Settings 内是 SignupCoupon 配置 + CouponManagement 上下叠加）。
 */
const CouponsPage = () => {
  const { t } = useTranslation();
  const { configs, loading: configsLoading, handleChange, handleSave } = useAdminConfigs();

  const [templates, setTemplates] = useState([]);
  const [templatesLoading, setTemplatesLoading] = useState(false);
  const [signupSaving, setSignupSaving] = useState(false);

  const fetchTemplates = useCallback(async () => {
    setTemplatesLoading(true);
    try {
      const res = await fetch('/api/admin/coupon-templates', { credentials: 'include' });
      const data = await res.json();
      if (data.success) {
        setTemplates(data.data || []);
      } else {
        toast.error((data.message_code ? t('API.' + data.message_code) : data.message) || t('COUPON.LOAD_FAIL', '加载失败'));
      }
    } catch {
      toast.error(t('API.ERR_NETWORK', '网络异常'));
    } finally {
      setTemplatesLoading(false);
    }
  }, [t]);

  useEffect(() => { fetchTemplates(); }, [fetchTemplates]);

  const saveSignupCoupon = async () => {
    setSignupSaving(true);
    await handleSave(
      { signup_coupon_template_id: configs.signup_coupon_template_id || '0' },
      t('SETTINGS.SIGNUP_COUPON_SAVE_OK', '新人券配置已保存'),
    );
    setSignupSaving(false);
  };

  return (
    <PageContainer>
      <PageHeader
        title={t('SETTINGS.TAB_COUPONS', '优惠券模板')}
        sub={t('COUPON.PAGE_DESC', '管理优惠券模板，配置新用户自动发券规则')}
        icon={PackageIcon}
      />

      <Section
        title={t('SETTINGS.SIGNUP_COUPON_TITLE', '新用户自动发券')}
        sub={t('SETTINGS.SIGNUP_COUPON_DESC', '选择一个已启用的优惠券模板。新用户完成注册时会自动获得一张该模板的券；选择"不自动发放"则关闭此流程。')}
        icon={PackageIcon}
        actions={
          <button
            type="button"
            onClick={fetchTemplates}
            disabled={templatesLoading}
            className="h-9 px-3 bg-surface-container-high border border-outline rounded-control text-xs text-on-surface hover:border-primary disabled:opacity-50 flex items-center gap-2"
          >
            <RefreshCw size={14} className={templatesLoading ? 'animate-spin' : ''} />
            {t('SYSTEM.REFRESH', '刷新')}
          </button>
        }
      >
        <div className="flex flex-col md:flex-row md:items-center gap-3">
          <div className="flex-1">
            <Select
              id="signup_coupon_template_id"
              value={configs.signup_coupon_template_id || '0'}
              onChange={(e) => handleChange('signup_coupon_template_id', e.target.value)}
              options={[
                { value: '0', label: t('SETTINGS.SIGNUP_COUPON_NONE', '不自动发放') },
                ...templates.map(tpl => ({
                  value: String(tpl.id),
                  label: `#${tpl.id} · ${tpl.name}${tpl.enabled === false ? `（${t('COUPON.NO', '禁用')}）` : ''}`,
                  disabled: tpl.enabled === false
                }))
              ]}
            />
          </div>
          <button
            type="button"
            onClick={saveSignupCoupon}
            disabled={configsLoading || templatesLoading || signupSaving}
            className="h-10 px-5 bg-primary text-on-primary rounded-control text-sm font-medium hover:opacity-90 disabled:opacity-50 flex items-center justify-center gap-2"
          >
            <Save size={16} />
            {(configsLoading || signupSaving) ? t('SETTINGS.BTN_SAVING', '保存中…') : t('SETTINGS.SIGNUP_COUPON_SAVE', '保存新人券')}
          </button>
        </div>
      </Section>

      <Suspense fallback={<div className="py-12 text-center text-sm text-on-surface-variant">{t('APP.LOADING', '加载中...')}</div>}>
        <CouponManagement />
      </Suspense>
    </PageContainer>
  );
};

export default CouponsPage;
