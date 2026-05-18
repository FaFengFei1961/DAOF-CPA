import React from 'react';
import { useTranslation } from 'react-i18next';
import { ShieldCheck, Info } from 'lucide-react';
import { PageContainer, PageHeader, Section } from '../../../components/ui';
import TextInput from '../../../components/ui/TextInput';
import Select from '../../../components/ui/Select';
import UsdAmountInput from '../../../components/ui/UsdAmountInput';
import { useAdminConfigs } from '../../../hooks/useAdminConfigs';
import { SaveBar } from './_AdminFormPrimitives';

const STRATEGY_DETAIL_TONE = {
  trust:   { tone: 'warning', icon: '⚠' },
  dynamic: { tone: 'primary', icon: '✓' },
  strict:  { tone: 'error',   icon: '🛡' },
};

const StrategyDetail = ({ strategy, ipLimit, t }) => {
  const { tone, icon } = STRATEGY_DETAIL_TONE[strategy] || STRATEGY_DETAIL_TONE.dynamic;
  const colorClass = {
    primary: 'border-primary/30 bg-primary/[0.06] text-on-surface',
    warning: 'border-warning/30 bg-warning/[0.06] text-on-surface',
    error:   'border-error/30   bg-error/[0.06]   text-on-surface',
  }[tone];
  const bulletKey = `ADMIN_SYS.RISK.STRATEGY_DETAIL_${strategy.toUpperCase()}`;
  const detailText = t(bulletKey, { ipLimit: ipLimit || '3' });
  const lines = detailText.split('\n').filter(Boolean);
  return (
    <div className={`mt-4 rounded-control border px-4 py-3 ${colorClass}`}>
      <div className="flex items-start gap-2">
        <Info size={14} className="mt-0.5 shrink-0 opacity-80" />
        <div className="text-xs leading-relaxed flex-1">
          <div className="font-semibold mb-1.5">
            {icon} {t(`ADMIN_SYS.RISK.STRATEGY_${strategy.toUpperCase()}`)}
          </div>
          <ul className="space-y-1 list-disc list-inside marker:text-on-surface-variant/60">
            {lines.map((line, idx) => (<li key={idx}>{line}</li>))}
          </ul>
        </div>
      </div>
    </div>
  );
};

/**
 * Registration risk controls + referral incentives.
 *
 * 号池额度采集器（credits_refresh_interval / credits_max_retries / credits_retry_interval）
 * 已挪到 /admin/sync —— 与 CLIProxy 用量同步同属"后台从 CPA 拉数据"语义。
 */
const RiskPage = () => {
  const { t } = useTranslation();
  const { configs, loading, handleChange, handleSave } = useAdminConfigs();
  // 持久化层是 micro_usd 整数字符串（后端 readMicroUSDConfig 用 strconv.ParseInt 解析）。
  // microDefault 是 SysConfig 缺失时的兜底，与后端 oauth.go resolveBonusConfig 默认一致。
  const incentiveFields = [
    {
      key: 'signup_bonus',
      label: t('ADMIN_SYS.RISK.SIGNUP_BONUS_LABEL'),
      hint: t('ADMIN_SYS.RISK.SIGNUP_BONUS_HINT'),
      placeholder: '1.00',
      microDefault: '1000000', // $1
    },
    {
      key: 'referrer_bonus',
      label: t('ADMIN_SYS.RISK.REFERRER_BONUS_LABEL'),
      hint: t('ADMIN_SYS.RISK.REFERRER_BONUS_HINT'),
      placeholder: '0.50',
      microDefault: '0',
    },
    {
      key: 'referee_bonus',
      label: t('ADMIN_SYS.RISK.REFEREE_BONUS_LABEL'),
      hint: t('ADMIN_SYS.RISK.REFEREE_BONUS_HINT'),
      placeholder: '0.30',
      microDefault: '0',
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
        <div className="py-3 border-b border-outline-variant/30">
          <div className="flex flex-col md:flex-row md:items-center justify-between gap-4">
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
          <StrategyDetail
            strategy={configs.reg_strategy || 'dynamic'}
            ipLimit={configs.reg_ip_limit || '3'}
            t={t}
          />
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
            <UsdAmountInput
              microValue={configs[item.key]}
              microDefault={item.microDefault}
              onMicroChange={(micro) => handleChange(item.key, micro)}
              placeholder={item.placeholder}
            />
          </div>
        ))}
      </Section>

      <SaveBar loading={loading} onSave={handleSave} />
    </PageContainer>
  );
};

export default RiskPage;
