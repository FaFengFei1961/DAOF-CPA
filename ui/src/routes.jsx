/**
 * Single route source plus navigation manifest.
 *
 * Replaces the old App.jsx hash-router switch with React Router v7 nested routes:
 *   - /         -> UserShell
 *   - /admin/*  -> AdminShell
 *
 * The nav manifest remains the single source of truth for user, admin,
 * and mobile navigation.
 */
import React, { lazy } from 'react';
import { createBrowserRouter, Navigate, useSearchParams } from 'react-router-dom';
// navManifest.js owns menu data; routes only consume adminTabItems for legacy tab routes.
import { adminTabItems } from './navManifest';

// fix P2（codex review verify-r5）：旧 /upgrade?pane=mine|store 通知链接 compat redirect。
// pane=mine（已订阅用户看续费 / 退款）→ Dashboard 不弹 modal；
// pane=store（看新套餐营销）→ Dashboard + ?openBrowse=store 让 MySubscriptions 自动弹 modal。
const UpgradeRedirect = () => {
  const [searchParams] = useSearchParams();
  const pane = searchParams.get('pane');
  const target = pane === 'store' ? '/?openBrowse=store' : '/';
  return <Navigate to={target} replace />;
};

// Shells
const UserShell = lazy(() => import('./shells/UserShell'));
const AdminShell = lazy(() => import('./shells/AdminShell'));
import RouteGuard from './shells/RouteGuard';

// User-side pages
const Dashboard = lazy(() => import('./components/Dashboard'));
const TokenManager = lazy(() => import('./components/TokenManager'));
const StatisticsDash = lazy(() => import('./components/StatisticsDash'));
const PricingDash = lazy(() => import('./components/PricingDash'));
const Topup = lazy(() => import('./components/Topup'));
const TopupResult = lazy(() => import('./components/TopupResult'));
const VerifyEmailPage = lazy(() => import('./components/VerifyEmailPage'));
// Phase G-2.6：邮箱+密码 reset / set 落地页
const ResetPasswordPage = lazy(() => import('./components/ResetPasswordPage'));
const SetPasswordPage = lazy(() => import('./components/SetPasswordPage'));
const BillsPage = lazy(() => import('./components/BillsPage'));
const Tickets = lazy(() => import('./components/Tickets'));

// Legacy Settings wrapper for remaining user settings routes.
const Settings = lazy(() => import('./components/Settings'));

// User usage pages split out from the old dashboard.
const UsersUsageOverviewPage = lazy(() => import('./pages/admin/UsersUsageOverviewPage'));
const UpstreamMarginPage = lazy(() => import('./pages/admin/UpstreamMarginPage'));
const AuditEventsPage = lazy(() => import('./pages/admin/AuditEventsPage'));

// Standalone admin modules.
const ChannelManagement = lazy(() => import('./components/ChannelManagement'));
const CreditsMonitor = lazy(() => import('./components/CreditsMonitor'));
const QuotaPlanManagement = lazy(() => import('./components/QuotaPlanManagement'));
const PackageManagement = lazy(() => import('./components/PackageManagement'));
const UserManagement = lazy(() => import('./components/UserManagement'));
const AdminNotificationManagement = lazy(() => import('./components/AdminNotificationManagement'));
const AdminCustomerMessages = lazy(() => import('./components/AdminCustomerMessages'));
const I18nManagement = lazy(() => import('./components/I18nManagement'));

// Admin system form pages.
const OAuthPage = lazy(() => import('./pages/admin/system/OAuthPage'));
const SmsPage = lazy(() => import('./pages/admin/system/SmsPage'));
const EmailPage = lazy(() => import('./pages/admin/system/EmailPage'));
const ModerationPage = lazy(() => import('./pages/admin/system/ModerationPage'));
const CouponsPage = lazy(() => import('./pages/admin/system/CouponsPage'));
const SyncPage = lazy(() => import('./pages/admin/system/SyncPage'));

// Remaining admin forms and finance workspace.
const GeneralAdminPage = lazy(() => import('./pages/admin/system/GeneralAdminPage'));
const RiskPage = lazy(() => import('./pages/admin/system/RiskPage'));
const FinanceShell = lazy(() => import('./pages/admin/finance/FinancePage'));
const FinanceSettingsPage = lazy(() => import('./pages/admin/finance/FinanceSettingsPage'));
const BillingRulesAdminPage = lazy(() => import('./pages/admin/finance/BillingRulesPage'));
const AdminPaymentChannels = lazy(() => import('./components/AdminPaymentChannels'));
const AdminPaymentChannelsEpusdt = lazy(() => import('./components/AdminPaymentChannelsEpusdt'));
const AdminTopupOrders = lazy(() => import('./components/AdminTopupOrders'));
const AdminSubscriptions = lazy(() => import('./components/AdminSubscriptions'));

const router = createBrowserRouter([
  {
    path: '/',
    element: <UserShell />,
    children: [
      // Public routes.
      { index: true,    element: <Dashboard /> },
      { path: 'pricing',element: <PricingDash /> },
      { path: 'topup-result', element: <TopupResult /> },
      // Phase G-1.8：邮箱验证落地页（用户从邮件链接进入）
      { path: 'verify-email', element: <VerifyEmailPage /> },
      // Phase G-2.6：邮箱+密码 重置 / 首次设置 落地页（从邮件链接进入，public）
      { path: 'reset-password', element: <ResetPasswordPage /> },
      { path: 'set-password', element: <SetPasswordPage /> },
      // fix P2（codex review verify-1 + verify-r4 + verify-r5）：后端 notification_links.go
      // 仍生成 `/upgrade?pane=mine|store` 用于通知 action_url。/upgrade 路由删除后这些通知按钮
      // silent 跳全局 404。
      //
      // 不能用 static <Navigate>：pane=mine 和 pane=store 行为不同：
      //   - pane=mine（订阅到期 / 退款 / 看自己订阅）→ 落到 Dashboard，**不弹** modal
      //   - pane=store（营销 / 看新套餐）→ Dashboard + ?openBrowse=store 自动弹 modal
      // 用 UpgradeRedirect 动态组件读 search.pane 决定 Navigate 目标。
      { path: 'upgrade', element: <UpgradeRedirect /> },
      // User routes guarded by the RequireAuth banner.
      { path: 'tokens',  element: <RouteGuard><TokenManager /></RouteGuard> },
      { path: 'stats',   element: <RouteGuard><StatisticsDash /></RouteGuard> },
      { path: 'topup',   element: <RouteGuard><Topup /></RouteGuard> },
      { path: 'bills',   element: <RouteGuard><BillsPage /></RouteGuard> },
      { path: 'tickets', element: <RouteGuard><Tickets /></RouteGuard> },
      { path: 'settings',element: <RouteGuard><Settings initialTab="general" /></RouteGuard> },
    ],
  },
  {
    path: '/admin',
    element: <AdminShell />,
    children: [
      { index: true, element: <Navigate to="/admin/channels" replace /> },

      { path: 'users/usage',     element: <UsersUsageOverviewPage /> },
      { path: 'upstream/margin', element: <UpstreamMarginPage /> },
      { path: 'audit/events',    element: <AuditEventsPage /> },
      // Legacy /admin/users-usage compatibility.
      { path: 'users-usage',     element: <Navigate to="/admin/users/usage" replace /> },

      { path: 'channels',        element: <ChannelManagement /> },
      { path: 'credits',         element: <CreditsMonitor /> },
      { path: 'quota-plans',     element: <QuotaPlanManagement /> },
      { path: 'packages',        element: <PackageManagement /> },
      { path: 'users',           element: <UserManagement /> },
      { path: 'notifications',   element: <AdminNotificationManagement /> },
      { path: 'tickets',         element: <AdminCustomerMessages /> },
      { path: 'i18n',            element: <I18nManagement /> },

      { path: 'oauth',           element: <OAuthPage /> },
      { path: 'sms',             element: <SmsPage /> },
      { path: 'email',           element: <EmailPage /> },
      { path: 'moderation',      element: <ModerationPage /> },
      { path: 'coupons',         element: <CouponsPage /> },
      { path: 'sync',            element: <SyncPage /> },

      { path: 'general',         element: <GeneralAdminPage /> },
      { path: 'risk',            element: <RiskPage /> },
      // Finance workspace nested routes keep each sub-tab deep-linkable.
      {
        path: 'finance',
        element: <FinanceShell />,
        children: [
          { index: true,           element: <FinanceSettingsPage /> },
          { path: 'rules',         element: <BillingRulesAdminPage /> },
          { path: 'payment',       element: <AdminPaymentChannels /> },
          { path: 'payment-epusdt', element: <AdminPaymentChannelsEpusdt /> },
          { path: 'topups',        element: <AdminTopupOrders /> },
          { path: 'subscriptions', element: <AdminSubscriptions /> },
        ],
      },

      // Legacy settings-tab route support. Empty today, kept for future tab-style items.
      ...adminTabItems.map(item => ({
        path: item.path.replace('/admin/', ''),
        element: <Settings initialTab={item.tab} hideNav />,
      })),
    ],
  },
  // Global 404 fallback.
  { path: '*', element: <Navigate to="/" replace /> },
]);

export default router;
