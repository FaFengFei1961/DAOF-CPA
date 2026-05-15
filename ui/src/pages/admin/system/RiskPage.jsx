import React from 'react';
import { useTranslation } from 'react-i18next';
import { ShieldCheck, Activity } from 'lucide-react';
import { PageContainer, PageHeader, Section } from '../../../components/ui';
import TextInput from '../../../components/ui/TextInput';
import Select from '../../../components/ui/Select';
import { useAdminConfigs } from '../../../hooks/useAdminConfigs';
import { SaveBar } from './_AdminFormPrimitives';

/**
 * Registration risk controls, referral incentives, and credits collector tuning.
 */
const RiskPage = () => {
  const { t } = useTranslation();
  const { configs, loading, handleChange, handleSave } = useAdminConfigs();
  const incentiveFields = [
    {
      key: 'signup_bonus',
      label: t('ADMIN_SYS.RISK.SIGNUP_BONUS_LABEL'),
      hint: t('ADMIN_SYS.RISK.SIGNUP_BONUS_HINT'),
      placeholder: '1.00',
      defaultVal: '1',
    },
    {
      key: 'referrer_bonus',
      label: t('ADMIN_SYS.RISK.REFERRER_BONUS_LABEL'),
      hint: t('ADMIN_SYS.RISK.REFERRER_BONUS_HINT'),
      placeholder: '0.50',
      defaultVal: '0',
    },
    {
      key: 'referee_bonus',
      label: t('ADMIN_SYS.RISK.REFEREE_BONUS_LABEL'),
      hint: t('ADMIN_SYS.RISK.REFEREE_BONUS_HINT'),
      placeholder: '0.30',
      defaultVal: '0',
    },
  ];
  const collectorFields = [
    {
      key: 'credits_refresh_interval',
      label: t('ADMIN_SYS.RISK.CREDITS_REFRESH_LABEL'),
      hint: t('ADMIN_SYS.RISK.CREDITS_REFRESH_HINT'),
      unit: t('ADMIN_SYS.UNIT.MINUTES'),
      placeholder: '15',
      defaultVal: '15',
      min: 1,
      max: 1440,
    },
    {
      key: 'credits_max_retries',
      label: t('ADMIN_SYS.RISK.CREDITS_RETRIES_LABEL'),
      hint: t('ADMIN_SYS.RISK.CREDITS_RETRIES_HINT'),
      unit: t('ADMIN_SYS.UNIT.TIMES'),
      placeholder: '3',
      defaultVal: '3',
      min: 0,
      max: 100,
    },
    {
      key: 'credits_retry_interval',
      label: t('ADMIN_SYS.RISK.CREDITS_RETRY_INTERVAL_LABEL'),
      hint: t('ADMIN_SYS.RISK.CREDITS_RETRY_INTERVAL_HINT'),
      unit: t('ADMIN_SYS.UNIT.MINUTES'),
      placeholder: '5',
      defaultVal: '5',
      min: 1,
      max: 1440,
    },
  ];

  return (
    <PageContainer>
      <PageHeader
        title={t('ADMIN_SYS.RISK.TITLE')}
        sub={t('ADMIN_SYS.RISK.DESC')}
        icon={ShieldCheck}
      />

      <Section title={t('ADMIN_SYS.RISK.STRATEGY_SECTION_TITLE')}>
        <div className="flex flex-col md:flex-row md:items-center justify-between py-3 border-b border-outline-variant/30 gap-4">
          <div className="flex flex-col gap-1 w-full md:w-2/3">
            <span className="text-on-surface-variant font-medium">{t('ADMIN_SYS.RISK.STRATEGY_LABEL')}</span>
            <span className="text-xs text-on-surface-variant">{t('ADMIN_SYS.RISK.STRATEGY_HINT')}</span>
          </div>
          <div className="w-full md:w-64">
            <Select
              value={configs.reg_strategy || 'dynamic'}
              onChange={(e) => handleChange('reg_strategy', e.target.value)}
              options={[
                { value: 'trust', label: t('ADMIN_SYS.RISK.STRATEGY_TRUST') },
                { value: 'dynamic', label: t('ADMIN_SYS.RISK.STRATEGY_DYNAMIC') },
                { value: 'strict', label: t('ADMIN_SYS.RISK.STRATEGY_STRICT') }
              ]}
            />
          </div>
        </div>

        <div className="flex flex-col md:flex-row md:items-center justify-between py-3 border-b border-outline-variant/30 gap-4">
          <div className="flex flex-col gap-1 w-full md:w-2/3">
            <span className="text-on-surface-variant font-medium">{t('ADMIN_SYS.RISK.IP_LIMIT_LABEL')}</span>
            <span className="text-xs text-on-surface-variant">{t('ADMIN_SYS.RISK.IP_LIMIT_HINT')}</span>
          </div>
          <div className="relative w-full md:w-32">
            <TextInput
              type="number"
              value={configs.reg_ip_limit || '3'}
              onChange={(e) => handleChange('reg_ip_limit', e.target.value)}
              className="text-right"
              style={{ paddingRight: '2.5rem' }}
            />
            <span className="absolute right-4 top-2.5 text-on-surface-variant text-sm pointer-events-none">{t('ADMIN_SYS.UNIT.TIMES')}</span>
          </div>
        </div>

        <div className="flex flex-col md:flex-row md:items-center justify-between py-3 gap-4">
          <div className="flex flex-col gap-1 w-full md:w-2/3">
            <span className="text-on-surface-variant font-medium">{t('ADMIN_SYS.RISK.MAX_USERS_LABEL')}</span>
            <span className="text-xs text-on-surface-variant">{t('ADMIN_SYS.RISK.MAX_USERS_HINT')}</span>
          </div>
          <div className="relative w-full md:w-32">
            <TextInput
              type="number" min="0"
              value={configs.max_users ?? '0'}
              onChange={(e) => handleChange('max_users', e.target.value)}
              placeholder="0"
              className="text-right"
              style={{ paddingRight: '2.5rem' }}
            />
            <span className="absolute right-4 top-2.5 text-on-surface-variant text-sm pointer-events-none">{t('ADMIN_SYS.UNIT.USERS')}</span>
          </div>
        </div>
      </Section>

      <Section
        title={t('ADMIN_SYS.RISK.INCENTIVE_TITLE')}
        sub={<>{t('ADMIN_SYS.RISK.INCENTIVE_DESC')} <span className="font-mono text-primary">https://your-domain/?ref=&lt;username&gt;</span></>}
        icon={ShieldCheck}
      >
        {incentiveFields.map((item, idx, arr) => (
          <div key={item.key} className={`flex flex-col md:flex-row md:items-center justify-between py-3 ${idx === arr.length - 1 ? '' : 'border-b border-outline-variant/20'} gap-3`}>
            <div className="flex flex-col gap-1 w-full md:w-2/3">
              <span className="text-on-surface-variant font-medium text-sm">{item.label}</span>
              <span className="text-xs text-outline">{item.hint}</span>
            </div>
            <div className="relative w-full md:w-32">
              <span className="absolute left-3 top-2.5 text-on-surface-variant text-sm pointer-events-none z-10">$</span>
              <TextInput
                type="number" step="0.01" min="0"
                value={configs[item.key] ?? item.defaultVal}
                onChange={(e) => handleChange(item.key, e.target.value)}
                placeholder={item.placeholder}
                className="pl-7 text-right"
              />
            </div>
          </div>
        ))}
      </Section>

      <Section
        title={t('ADMIN_SYS.RISK.COLLECTOR_TITLE')}
        sub={t('ADMIN_SYS.RISK.COLLECTOR_DESC')}
        icon={Activity}
      >
        {collectorFields.map((item, idx, arr) => (
          <div key={item.key} className={`flex flex-col md:flex-row md:items-center justify-between py-3 ${idx === arr.length - 1 ? '' : 'border-b border-outline-variant/20'} gap-3`}>
            <div className="flex flex-col gap-1 w-full md:w-2/3">
              <span className="text-on-surface-variant font-medium text-sm">{item.label}</span>
              <span className="text-xs text-outline">{item.hint}</span>
            </div>
            <div className="relative w-full md:w-32">
              <TextInput
                type="number" min={item.min} max={item.max}
                value={configs[item.key] ?? item.defaultVal}
                onChange={(e) => handleChange(item.key, e.target.value)}
                placeholder={item.placeholder}
                className="text-right"
                style={{ paddingRight: '3rem' }}
              />
              <span className="absolute right-3 top-2.5 text-on-surface-variant text-sm pointer-events-none">{item.unit}</span>
            </div>
          </div>
        ))}
      </Section>

      <SaveBar loading={loading} onSave={handleSave} />
    </PageContainer>
  );
};

export default RiskPage;
