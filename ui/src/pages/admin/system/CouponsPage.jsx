import React, { useEffect, useState, useCallback, Suspense, lazy } from 'react';
import { useTranslation } from 'react-i18next';
import toast from 'react-hot-toast';
import { Package as PackageIcon, RefreshCw, Save } from 'lucide-react';
import { PageContainer, PageHeader, Section } from '../../../components/ui';
import Select from '../../../components/ui/Select';
import { useAdminConfigs } from '../../../hooks/useAdminConfigs';

const CouponManagement = lazy(() => import('../../../components/CouponManagement'));

/**
 * Coupon template page plus signup-coupon configuration.
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
        toast.error((data.message_code ? t(`API.${data.message_code}`) : data.message) || t('ADMIN_SYS.COUPONS.LOAD_FAIL'));
      }
    } catch {
      toast.error(t('API.ERR_NETWORK'));
    } finally {
      setTemplatesLoading(false);
    }
  }, [t]);

  useEffect(() => { fetchTemplates(); }, [fetchTemplates]);

  const saveSignupCoupon = async () => {
    setSignupSaving(true);
    await handleSave(
      { signup_coupon_template_id: configs.signup_coupon_template_id || '0' },
      t('ADMIN_SYS.COUPONS.SIGNUP_SAVE_OK'),
    );
    setSignupSaving(false);
  };

  return (
    <PageContainer>
      <PageHeader
        title={t('ADMIN_SYS.COUPONS.TITLE')}
        sub={t('ADMIN_SYS.COUPONS.DESC')}
        icon={PackageIcon}
      />

      <Section
        title={t('ADMIN_SYS.COUPONS.SIGNUP_TITLE')}
        sub={t('ADMIN_SYS.COUPONS.SIGNUP_DESC')}
        icon={PackageIcon}
        actions={
          <button
            type="button"
            onClick={fetchTemplates}
            disabled={templatesLoading}
            className="h-9 px-3 bg-surface-container-high border border-outline rounded-control text-xs text-on-surface hover:border-primary disabled:opacity-50 flex items-center gap-2"
          >
            <RefreshCw size={14} className={templatesLoading ? 'animate-spin' : ''} />
            {t('COMMON.REFRESH')}
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
                { value: '0', label: t('ADMIN_SYS.COUPONS.SIGNUP_NONE') },
                ...templates.map(tpl => ({
                  value: String(tpl.id),
                  label: `#${tpl.id} · ${tpl.name}${tpl.enabled === false ? t('ADMIN_SYS.COUPONS.DISABLED_SUFFIX') : ''}`,
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
            {(configsLoading || signupSaving) ? t('COMMON.SAVING') : t('ADMIN_SYS.COUPONS.SIGNUP_SAVE')}
          </button>
        </div>
      </Section>

      <Suspense fallback={<div className="py-12 text-center text-sm text-on-surface-variant">{t('COMMON.LOADING')}</div>}>
        <CouponManagement />
      </Suspense>
    </PageContainer>
  );
};

export default CouponsPage;
