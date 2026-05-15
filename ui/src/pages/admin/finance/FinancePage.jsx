import React, { Suspense, lazy } from 'react';
import { useTranslation } from 'react-i18next';
import { Outlet, NavLink, useLocation, Navigate } from 'react-router-dom';
import { ShieldAlert, Wallet, Receipt, Package as PackageIcon } from 'lucide-react';
import { PageContainer, PageHeader } from '../../../components/ui';

const FinanceSettingsPage = lazy(() => import('./FinanceSettingsPage'));
const AdminPaymentChannels = lazy(() => import('../../../components/AdminPaymentChannels'));
const AdminTopupOrders = lazy(() => import('../../../components/AdminTopupOrders'));
const AdminSubscriptions = lazy(() => import('../../../components/AdminSubscriptions'));

/**
 * FinancePage — 财务工作区（Phase 4 抽出）
 *
 * 替换 Settings.jsx 内 activeTab === 'finance'，并把原内部 financeTab state 改成
 * URL nested route：
 *   /admin/finance              → 基础设置（汇率 / server_address / 余额消费默认）
 *   /admin/finance/payment      → 支付通道（AdminPaymentChannels）
 *   /admin/finance/topups       → 充值订单（AdminTopupOrders）
 *   /admin/finance/subscriptions→ 订阅总览（AdminSubscriptions）
 *
 * 浏览器后退能在 finance sub-tab 间切换，比原内部 state 更可深链。
 */
const FINANCE_TABS = [
  { id: 'settings',      path: '/admin/finance',               icon: ShieldAlert, label: '基础设置' },
  { id: 'payment',       path: '/admin/finance/payment',       icon: Wallet,      label: '支付通道' },
  { id: 'topups',        path: '/admin/finance/topups',        icon: Receipt,     label: '充值订单' },
  { id: 'subscriptions', path: '/admin/finance/subscriptions', icon: PackageIcon, label: '订阅总览' },
];

const FinanceShell = () => {
  const { t } = useTranslation();
  const location = useLocation();

  return (
    <PageContainer>
      <PageHeader
        title={t('SETTINGS.FINANCE_TITLE', '财务工作区')}
        sub={t('SETTINGS.FINANCE_DESC', '汇率 / 服务地址 / 支付通道 / 充值订单 / 订阅总览，admin 全局财务运维入口')}
        icon={ShieldAlert}
      />

      <nav role="tablist" aria-label="财务工作区分区" className="flex flex-wrap gap-2">
        {FINANCE_TABS.map(tab => {
          const Icon = tab.icon;
          // index path '/admin/finance' 用 end 严格匹配
          const end = tab.path === '/admin/finance';
          return (
            <NavLink
              key={tab.id}
              to={tab.path}
              end={end}
              role="tab"
              className={({ isActive }) =>
                `h-10 px-4 rounded-control border text-sm font-medium transition-colors flex items-center gap-2 ${
                  isActive
                    ? 'bg-primary text-on-primary border-primary'
                    : 'bg-surface-container border-outline-variant text-on-surface-variant hover:text-on-surface hover:border-primary/50'
                }`
              }
            >
              <Icon size={15} />
              {tab.label}
            </NavLink>
          );
        })}
      </nav>

      <Suspense fallback={<div className="py-12 text-center text-sm text-on-surface-variant">{t('APP.LOADING', '加载中...')}</div>}>
        {/* Outlet 由 child route 渲染对应 page */}
        <Outlet key={location.pathname} />
      </Suspense>
    </PageContainer>
  );
};

export default FinanceShell;
export {
  FinanceSettingsPage,
  AdminPaymentChannels as FinancePaymentPage,
  AdminTopupOrders as FinanceTopupsPage,
  AdminSubscriptions as FinanceSubscriptionsPage,
};
