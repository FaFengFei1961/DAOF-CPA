import React from 'react';
import { useTranslation } from 'react-i18next';
import { Wallet } from 'lucide-react';
import { Section } from '../../../components/ui';
import UsdAmountInput from '../../../components/ui/UsdAmountInput';
import { useAdminConfigs } from '../../../hooks/useAdminConfigs';
import { SaveBar } from '../system/_AdminFormPrimitives';

/**
 * Base finance settings: exchange rate, public server address, and default
 * balance-spending behavior for newly registered users.
 */
const FinanceSettingsPage = () => {
  const { t } = useTranslation();
  const { configs, loading, handleChange, handleSave } = useAdminConfigs();
  const balanceEnabled = String(configs.balance_consume_default_enabled).toLowerCase() === 'true';

  return (
    <>
      <Section title={t('ADMIN_FINANCE.SETTINGS.EXCHANGE_RATE_TITLE')} sub={t('ADMIN_FINANCE.SETTINGS.EXCHANGE_RATE_DESC')}>
        <div className="flex flex-col md:flex-row md:items-center justify-between py-2 gap-4">
          <span className="text-sm text-on-surface">{t('ADMIN_FINANCE.SETTINGS.EXCHANGE_RATE_LABEL')}</span>
          <div className="relative w-full md:w-auto">
            <input
              type="number" step="1" min="1000000" max="1000000000"
              value={configs.exchange_rate_rmb_per_usd_micros || ''}
              onChange={(e) => handleChange('exchange_rate_rmb_per_usd_micros', e.target.value)}
              placeholder="7200000"
              className="w-full md:w-48 bg-surface-container-high border border-outline rounded-control px-4 py-2 text-on-surface outline-none text-right focus:border-primary font-mono"
            />
          </div>
        </div>
      </Section>

      <Section title={t('ADMIN_FINANCE.SETTINGS.SERVER_ADDR_TITLE')} sub={t('ADMIN_FINANCE.SETTINGS.SERVER_ADDR_DESC')}>
        <div className="flex flex-col md:flex-row md:items-center justify-between py-2 gap-4">
          <span className="text-sm text-on-surface">{t('ADMIN_FINANCE.SETTINGS.SERVER_ADDR_LABEL')}</span>
          <input
            type="text"
            value={configs.server_address || ''}
            onChange={(e) => handleChange('server_address', e.target.value)}
            placeholder="https://your-domain/"
            className="bg-surface-container-high border border-outline text-on-surface-variant rounded-control px-4 py-2 outline-none text-sm w-full md:w-64 hover:border-primary/50 focus:border-primary"
          />
        </div>
      </Section>

      <Section
        title={t('ADMIN_FINANCE.SETTINGS.BALANCE_DEFAULT_TITLE')}
        sub={t('ADMIN_FINANCE.SETTINGS.BALANCE_DEFAULT_DESC')}
        icon={Wallet}
      >
        <div className="flex flex-col md:flex-row md:items-center justify-between py-3 border-b border-outline-variant/20 gap-3">
          <div className="flex flex-col gap-1 w-full md:w-2/3">
            <span id="balance-default-enabled-label" className="text-on-surface-variant font-medium text-sm">
              {t('ADMIN_FINANCE.SETTINGS.BALANCE_DEFAULT_ENABLED')}
            </span>
            <span className="text-xs text-outline">
              {t('ADMIN_FINANCE.SETTINGS.BALANCE_DEFAULT_ENABLED_HINT')}
            </span>
          </div>
          <button
            type="button"
            role="switch"
            aria-checked={balanceEnabled}
            aria-labelledby="balance-default-enabled-label"
            onClick={() => handleChange('balance_consume_default_enabled', balanceEnabled ? 'false' : 'true')}
            className={`relative shrink-0 w-12 h-6 rounded-full transition ${balanceEnabled ? 'bg-primary' : 'bg-on-surface/20'}`}
          >
            <span className={`absolute top-0.5 w-5 h-5 rounded-full bg-white transition-all ${balanceEnabled ? 'left-6' : 'left-0.5'}`} />
          </button>
        </div>

        <div className="grid grid-cols-1 md:grid-cols-2 gap-4 pt-4">
          <label className="flex flex-col gap-2">
            <span className="text-xs font-medium text-on-surface-variant">
              {t('ADMIN_FINANCE.SETTINGS.BALANCE_DEFAULT_LIMIT')}
            </span>
            {/* key 用后端权威的 micro_usd 版本——旧 *_usd key 已被 sysconfig.go 显式忽略 */}
            <UsdAmountInput
              microValue={configs.balance_consume_default_limit_micro_usd}
              microDefault="0"
              onMicroChange={(micro) => handleChange('balance_consume_default_limit_micro_usd', micro)}
              placeholder="0"
              widthClass="w-full"
            />
            <span className="text-[11px] text-on-surface-variant">
              {t('ADMIN_FINANCE.SETTINGS.BALANCE_DEFAULT_LIMIT_HINT')}
            </span>
          </label>

          <label className="flex flex-col gap-2">
            <span className="text-xs font-medium text-on-surface-variant">
              {t('ADMIN_FINANCE.SETTINGS.BALANCE_DEFAULT_WINDOW')}
            </span>
            <input
              type="number" min="60" max={365 * 24 * 60 * 60} step="60"
              value={configs.balance_consume_default_window_secs ?? '2592000'}
              onChange={(e) => handleChange('balance_consume_default_window_secs', e.target.value)}
              placeholder="2592000"
              className="w-full bg-surface-container-high border border-outline rounded-control px-3 py-2 text-on-surface outline-none text-right focus:border-primary"
            />
            <span className="text-[11px] text-on-surface-variant">
              {t('ADMIN_FINANCE.SETTINGS.BALANCE_DEFAULT_WINDOW_HINT')}
            </span>
          </label>
        </div>
      </Section>

      <SaveBar loading={loading} onSave={handleSave} />
    </>
  );
};

export default FinanceSettingsPage;
