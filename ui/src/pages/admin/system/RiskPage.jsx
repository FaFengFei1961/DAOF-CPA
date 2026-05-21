import React, { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { ShieldCheck, Info } from 'lucide-react';
import { Section } from '../../../components/ui';
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

const bpsToPercentDisplay = (bpsValue) => {
  const bps = parseInt(bpsValue || '0', 10);
  if (!Number.isFinite(bps) || bps <= 0) return '0';
  const whole = Math.floor(bps / 100);
  const frac = String(bps % 100).padStart(2, '0').replace(/0+$/, '');
  return frac ? `${whole}.${frac}` : `${whole}`;
};

const percentDisplayToBPS = (raw) => {
  const s = String(raw ?? '').trim();
  if (s === '') return '0';
  if (!/^\d+(\.\d{0,2})?$/.test(s)) return '';
  const [wholeRaw, fracRaw = ''] = s.split('.');
  const whole = parseInt(wholeRaw, 10);
  if (!Number.isFinite(whole)) return '';
  const frac = parseInt(fracRaw.padEnd(2, '0').slice(0, 2) || '0', 10);
  const bps = whole * 100 + frac;
  if (bps < 0 || bps > 10000) return '';
  return String(bps);
};

const PercentBPSInput = ({ value, onChange }) => {
  const [local, setLocal] = useState(() => bpsToPercentDisplay(value));
  useEffect(() => {
    setLocal(bpsToPercentDisplay(value));
  }, [value]);
  return (
    <div className="relative w-full md:w-32">
      <TextInput
        type="number"
        min="0"
        max="100"
        step="0.01"
        value={local}
        onChange={(e) => setLocal(e.target.value)}
        onBlur={(e) => {
          const bps = percentDisplayToBPS(e.target.value);
          onChange(bps || '0');
          setLocal(bpsToPercentDisplay(bps || '0'));
        }}
        className="text-right"
        style={{ paddingRight: '2rem' }}
      />
      <span className="absolute right-4 top-2.5 text-on-surface-variant text-sm pointer-events-none">%</span>
    </div>
  );
};

const secondsToDaysDisplay = (secondsValue) => {
  const seconds = parseInt(secondsValue || '2592000', 10);
  if (!Number.isFinite(seconds) || seconds <= 0) return '30';
  return String(Math.max(1, Math.round(seconds / 86400)));
};

const DaysSecondsInput = ({ value, onChange }) => {
  const { t } = useTranslation();
  const [local, setLocal] = useState(() => secondsToDaysDisplay(value));
  useEffect(() => {
    setLocal(secondsToDaysDisplay(value));
  }, [value]);
  return (
    <div className="relative w-full md:w-32">
      <TextInput
        type="number"
        min="1"
        max="365"
        step="1"
        value={local}
        onChange={(e) => setLocal(e.target.value)}
        onBlur={(e) => {
          const days = Math.min(365, Math.max(1, parseInt(e.target.value || '30', 10) || 30));
          onChange(String(days * 86400));
          setLocal(String(days));
        }}
        className="text-right"
        style={{ paddingRight: '2.5rem' }}
      />
      <span className="absolute right-4 top-2.5 text-on-surface-variant text-sm pointer-events-none">
        {t('ADMIN_SYS.UNIT.DAYS')}
      </span>
    </div>
  );
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
 * Registration risk controls + referral incentives sub-form.
 *
 * 号池额度采集器（credits_refresh_interval / credits_max_retries / credits_retry_interval）
 * 已挪到 /admin/sync —— 与 CLIProxy 用量同步同属"后台从 CPA 拉数据"语义。
 *
 * Sprint J-2: 仅作为 AuthAdminPage 的内嵌 tab 渲染；父级负责头部和分区切换。
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
    <>
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
        <div className="flex flex-col md:flex-row md:items-center justify-between py-3 border-t border-outline-variant/20 gap-3">
          <div className="flex flex-col gap-1 w-full md:w-2/3">
            <span className="text-on-surface-variant font-medium text-sm">
              {t('ADMIN_SYS.RISK.REFERRAL_PAID_SPEND_REWARD_LABEL')}
            </span>
            <span className="text-xs text-outline">
              {t('ADMIN_SYS.RISK.REFERRAL_PAID_SPEND_REWARD_HINT')}
            </span>
          </div>
          <PercentBPSInput
            value={configs.referral_paid_spend_reward_bps || '0'}
            onChange={(bps) => handleChange('referral_paid_spend_reward_bps', bps)}
          />
        </div>
        <div className="flex flex-col md:flex-row md:items-center justify-between py-3 border-t border-outline-variant/20 gap-3">
          <div className="flex flex-col gap-1 w-full md:w-2/3">
            <span className="text-on-surface-variant font-medium text-sm">
              {t('ADMIN_SYS.RISK.REFERRAL_REWARD_WINDOW_LABEL')}
            </span>
            <span className="text-xs text-outline">
              {t('ADMIN_SYS.RISK.REFERRAL_REWARD_WINDOW_HINT')}
            </span>
          </div>
          <DaysSecondsInput
            value={configs.referral_paid_spend_reward_window_seconds || '2592000'}
            onChange={(seconds) => handleChange('referral_paid_spend_reward_window_seconds', seconds)}
          />
        </div>
      </Section>

      <SaveBar loading={loading} onSave={handleSave} />
    </>
  );
};

export default RiskPage;
