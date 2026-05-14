import React from 'react';
import { useTranslation } from 'react-i18next';
import { Wallet } from 'lucide-react';
import { Section } from '../../../components/ui';
import { useAdminConfigs } from '../../../hooks/useAdminConfigs';
import { SaveBar } from '../system/_AdminFormPrimitives';

/**
 * FinanceSettingsPage — 财务工作区基础设置（Phase 4 抽出）
 *
 * 替换 Settings.jsx 内 financeTab === 'settings'。
 * 包含：
 *   1. 汇率（exchange_rate）
 *   2. 服务地址（server_address — 同时驱动 OAuth 回调 / 易付通 notify_url / return_url）
 *   3. 新用户余额消费默认值（balance_consume_default_*）
 */
const FinanceSettingsPage = () => {
  const { t } = useTranslation();
  const { configs, loading, handleChange, handleSave } = useAdminConfigs();
  const balanceEnabled = String(configs.balance_consume_default_enabled).toLowerCase() === 'true';

  return (
    <>
      <Section title={t('SETTINGS.EXCHANGE_RATE_TITLE', '汇率配置')} sub={t('SETTINGS.EXCHANGE_RATE_DESC', '人民币 → USD 汇率，影响充值入账金额计算')}>
        <div className="flex flex-col md:flex-row md:items-center justify-between py-2 gap-4">
          <span className="text-sm text-on-surface">{t('SETTINGS.EXCHANGE_RATE_TITLE', '汇率')}</span>
          <div className="relative w-full md:w-auto">
            <span className="absolute left-3 top-2.5 text-on-surface-variant text-sm pointer-events-none">￥</span>
            <input
              type="number" step="0.01"
              value={configs.exchange_rate || ''}
              onChange={(e) => handleChange('exchange_rate', e.target.value)}
              placeholder="7.25"
              className="w-full md:w-32 bg-surface-container-high border border-outline rounded-lg pl-8 pr-4 py-2 text-on-surface outline-none text-right focus:border-primary"
            />
          </div>
        </div>
      </Section>

      <Section title={t('SETTINGS.SERVER_ADDR_LABEL', '服务地址')} sub={t('SETTINGS.SERVER_ADDR_DESC', '系统全局对外服务地址，驱动 OAuth 回调 + 易付通 notify_url + return_url')}>
        <div className="flex flex-col md:flex-row md:items-center justify-between py-2 gap-4">
          <span className="text-sm text-on-surface">{t('SETTINGS.SERVER_ADDR_LABEL', 'server_address')}</span>
          <input
            type="text"
            value={configs.server_address || ''}
            onChange={(e) => handleChange('server_address', e.target.value)}
            placeholder="https://your-domain/"
            className="bg-surface-container-high border border-outline text-on-surface-variant rounded-lg px-4 py-2 outline-none text-sm w-full md:w-64 hover:border-primary/50 focus:border-primary"
          />
        </div>
      </Section>

      <Section
        title={t('SETTINGS.BALANCE_DEFAULT_TITLE', '新用户余额消费默认值')}
        sub={t('SETTINGS.BALANCE_DEFAULT_DESC', '只影响之后注册的新用户。余额消费仍排在订阅和增量包之后；默认关闭可保持最小攻击面。')}
        icon={Wallet}
      >
        <div className="flex flex-col md:flex-row md:items-center justify-between py-3 border-b border-outline-variant/20 gap-3">
          <div className="flex flex-col gap-1 w-full md:w-2/3">
            <span id="balance-default-enabled-label" className="text-on-surface-variant font-medium text-sm">
              {t('SETTINGS.BALANCE_DEFAULT_ENABLED', '默认允许余额消费')}
            </span>
            <span className="text-xs text-outline">
              {t('SETTINGS.BALANCE_DEFAULT_ENABLED_HINT', '开启后，新用户在订阅和增量包都耗尽时会自动扣余额。')}
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
              {t('SETTINGS.BALANCE_DEFAULT_LIMIT', '默认周期消费上限（USD）')}
            </span>
            <div className="relative">
              <span className="absolute left-3 top-2.5 text-on-surface-variant text-sm pointer-events-none">$</span>
              <input
                type="number" min="0" step="0.01"
                value={configs.balance_consume_default_limit_usd ?? '0'}
                onChange={(e) => handleChange('balance_consume_default_limit_usd', e.target.value)}
                placeholder="0"
                className="w-full bg-surface-container-high border border-outline rounded-lg pl-7 pr-3 py-2 text-on-surface outline-none text-right focus:border-primary"
              />
            </div>
            <span className="text-[11px] text-on-surface-variant">
              {t('SETTINGS.BALANCE_DEFAULT_LIMIT_HINT', '0 表示不限额。')}
            </span>
          </label>

          <label className="flex flex-col gap-2">
            <span className="text-xs font-medium text-on-surface-variant">
              {t('SETTINGS.BALANCE_DEFAULT_WINDOW', '默认统计窗口（秒）')}
            </span>
            <input
              type="number" min="60" max={365 * 24 * 60 * 60} step="60"
              value={configs.balance_consume_default_window_secs ?? '2592000'}
              onChange={(e) => handleChange('balance_consume_default_window_secs', e.target.value)}
              placeholder="2592000"
              className="w-full bg-surface-container-high border border-outline rounded-lg px-3 py-2 text-on-surface outline-none text-right focus:border-primary"
            />
            <span className="text-[11px] text-on-surface-variant">
              {t('SETTINGS.BALANCE_DEFAULT_WINDOW_HINT', '60 秒到 365 天；2592000 = 30 天。')}
            </span>
          </label>
        </div>
      </Section>

      <SaveBar loading={loading} onSave={handleSave} />
    </>
  );
};

export default FinanceSettingsPage;
