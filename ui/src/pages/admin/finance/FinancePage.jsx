import React, { Suspense, lazy } from 'react';
import { useTranslation } from 'react-i18next';
import { Outlet, NavLink, useLocation, Navigate } from 'react-router-dom';
import { ShieldAlert, Wallet, Receipt, Package as PackageIcon, Scale } from 'lucide-react';
import { PageContainer, PageHeader } from '../../../components/ui';

const FinanceSettingsPage = lazy(() => import('./FinanceSettingsPage'));
const BillingRulesPage = lazy(() => import('./BillingRulesPage'));
const AdminPaymentChannels = lazy(() => import('../../../components/AdminPaymentChannels'));
const AdminTopupOrders = lazy(() => import('../../../components/AdminTopupOrders'));
const AdminSubscriptions = lazy(() => import('../../../components/AdminSubscriptions'));

/**
 * Finance workspace shell. Each sub-tab is a nested URL so browser history
 * and deep links work across finance operations.
 */
const FINANCE_TABS = [
  { id: 'settings',      path: '/admin/finance',               icon: ShieldAlert, labelKey: 'ADMIN_FINANCE.TABS.SETTINGS' },
  { id: 'rules',         path: '/admin/finance/rules',         icon: Scale,       labelKey: 'ADMIN_FINANCE.TABS.RULES' },
  { id: 'payment',       path: '/admin/finance/payment',       icon: Wallet,      labelKey: 'ADMIN_FINANCE.TABS.PAYMENT' },
  { id: 'topups',        path: '/admin/finance/topups',        icon: Receipt,     labelKey: 'ADMIN_FINANCE.TABS.TOPUPS' },
  { id: 'subscriptions', path: '/admin/finance/subscriptions', icon: PackageIcon, labelKey: 'ADMIN_FINANCE.TABS.SUBSCRIPTIONS' },
];

const FinanceShell = () => {
  const { t } = useTranslation();
  const location = useLocation();

  return (
    <PageContainer>
      <PageHeader
        title={t('ADMIN_FINANCE.TITLE')}
        sub={t('ADMIN_FINANCE.DESC')}
        icon={ShieldAlert}
      />

      <nav role="tablist" aria-label={t('ADMIN_FINANCE.TABS.ARIA')} className="flex flex-wrap gap-2">
        {FINANCE_TABS.map(tab => {
          const Icon = tab.icon;
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
              {t(tab.labelKey)}
            </NavLink>
          );
        })}
      </nav>

      <Suspense fallback={<div className="py-12 text-center text-sm text-on-surface-variant">{t('COMMON.LOADING')}</div>}>
        <Outlet key={location.pathname} />
      </Suspense>
    </PageContainer>
  );
};

export default FinanceShell;
export {
  FinanceSettingsPage,
  BillingRulesPage,
  AdminPaymentChannels as FinancePaymentPage,
  AdminTopupOrders as FinanceTopupsPage,
  AdminSubscriptions as FinanceSubscriptionsPage,
};
