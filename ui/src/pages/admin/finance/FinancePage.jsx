import React, { Suspense, lazy, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Outlet, NavLink, useLocation, Navigate } from 'react-router-dom';
import { ShieldAlert, Wallet, Receipt, Package as PackageIcon, Scale, Coins } from 'lucide-react';
import { PageContainer, PageHeader } from '../../../components/ui';
import { authFetch } from '../../../utils/authFetch';

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
  { id: 'settings',         path: '/admin/finance',                  icon: ShieldAlert, labelKey: 'ADMIN_FINANCE.TABS.SETTINGS' },
  { id: 'rules',            path: '/admin/finance/rules',            icon: Scale,       labelKey: 'ADMIN_FINANCE.TABS.RULES' },
  { id: 'payment',          path: '/admin/finance/payment',          icon: Wallet,      labelKey: 'ADMIN_FINANCE.TABS.PAYMENT' },
  { id: 'payment-epusdt',   path: '/admin/finance/payment-epusdt',   icon: Coins,       labelKey: 'ADMIN_FINANCE.TABS.PAYMENT_EPUSDT' },
  { id: 'topups',           path: '/admin/finance/topups',           icon: Receipt,     labelKey: 'ADMIN_FINANCE.TABS.TOPUPS' },
  { id: 'subscriptions',    path: '/admin/finance/subscriptions',    icon: PackageIcon, labelKey: 'ADMIN_FINANCE.TABS.SUBSCRIPTIONS' },
];

// W-4-Manual H-6（2026-05-21）：用户在 admin 后台 nav 上能直接看到"积压的 USDT 订单"红点
// 数字，不用打开邮箱就能感知。30s polling 一次，开销极小（< 5ms 的 COUNT 查询）。
// 鼠标悬停显示积压时间提示，帮 admin 决定是否立即处理。
const PENDING_POLL_INTERVAL_MS = 30 * 1000;

function usePendingManualEpusdtCount() {
  const [data, setData] = useState({ count: 0, oldestAgeSeconds: 0 });

  useEffect(() => {
    let cancelled = false;
    const fetchCount = async () => {
      try {
        const json = await authFetch('/api/admin/topup/pending-manual-count');
        if (cancelled) return;
        if (json?.success && json.data) {
          setData({
            count: Number(json.data.count) || 0,
            oldestAgeSeconds: Number(json.data.oldest_age_seconds) || 0,
          });
        }
      } catch {
        // 静默失败：badge 不显示比错误状态更友好
      }
    };
    fetchCount();
    const id = setInterval(fetchCount, PENDING_POLL_INTERVAL_MS);
    return () => { cancelled = true; clearInterval(id); };
  }, []);

  return data;
}

function formatAgeLabel(seconds, t) {
  if (seconds < 60) return t('ADMIN_FINANCE.AGE_JUST_NOW', '刚刚');
  if (seconds < 3600) return t('ADMIN_FINANCE.AGE_MINUTES', '{{m}} 分钟前', { m: Math.floor(seconds / 60) });
  if (seconds < 86400) return t('ADMIN_FINANCE.AGE_HOURS', '{{h}} 小时前', { h: Math.floor(seconds / 3600) });
  return t('ADMIN_FINANCE.AGE_DAYS', '{{d}} 天前', { d: Math.floor(seconds / 86400) });
}

const FinanceShell = () => {
  const { t } = useTranslation();
  const location = useLocation();
  const pending = usePendingManualEpusdtCount();

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
          // H-6：epusdt tab + topups tab 都显示积压 badge（admin 两条路径都可能去处理）
          const showBadge = (tab.id === 'payment-epusdt' || tab.id === 'topups') && pending.count > 0;
          const ageLabel = showBadge ? formatAgeLabel(pending.oldestAgeSeconds, t) : '';
          return (
            <NavLink
              key={tab.id}
              to={tab.path}
              end={end}
              role="tab"
              title={showBadge ? t('ADMIN_FINANCE.PENDING_USDT_TIP', '{{n}} 个待确认 USDT 订单，最早 {{age}}', { n: pending.count, age: ageLabel }) : undefined}
              className={({ isActive }) =>
                `relative h-10 px-4 rounded-control border text-sm font-medium transition-colors flex items-center gap-2 ${
                  isActive
                    ? 'bg-primary text-on-primary border-primary'
                    : 'bg-surface-container border-outline-variant text-on-surface-variant hover:text-on-surface hover:border-primary/50'
                }`
              }
            >
              <Icon size={15} />
              {t(tab.labelKey)}
              {showBadge && (
                <span className="ml-1 inline-flex items-center justify-center min-w-[18px] h-[18px] px-1.5 rounded-full bg-error text-on-error text-[10px] font-bold leading-none">
                  {pending.count > 99 ? '99+' : pending.count}
                </span>
              )}
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
