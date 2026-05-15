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
import { createBrowserRouter, Navigate } from 'react-router-dom';
// navManifest.js owns menu data; routes only consume adminTabItems for legacy tab routes.
import { adminTabItems } from './navManifest';

// Shells
const UserShell = lazy(() => import('./shells/UserShell'));
const AdminShell = lazy(() => import('./shells/AdminShell'));
import RouteGuard from './shells/RouteGuard';

// User-side pages
const Dashboard = lazy(() => import('./components/Dashboard'));
const TokenManager = lazy(() => import('./components/TokenManager'));
const StatisticsDash = lazy(() => import('./components/StatisticsDash'));
const PricingDash = lazy(() => import('./components/PricingDash'));
const UpgradePage = lazy(() => import('./components/UpgradePage'));
const Topup = lazy(() => import('./components/Topup'));
const TopupResult = lazy(() => import('./components/TopupResult'));
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
const ModerationPage = lazy(() => import('./pages/admin/system/ModerationPage'));
const CouponsPage = lazy(() => import('./pages/admin/system/CouponsPage'));
const SyncPage = lazy(() => import('./pages/admin/system/SyncPage'));

// Remaining admin forms and finance workspace.
const GeneralAdminPage = lazy(() => import('./pages/admin/system/GeneralAdminPage'));
const RiskPage = lazy(() => import('./pages/admin/system/RiskPage'));
const FinanceShell = lazy(() => import('./pages/admin/finance/FinancePage'));
const FinanceSettingsPage = lazy(() => import('./pages/admin/finance/FinanceSettingsPage'));
const AdminPaymentChannels = lazy(() => import('./components/AdminPaymentChannels'));
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
      { path: 'upgrade',element: <UpgradePage /> },
      { path: 'topup-result', element: <TopupResult /> },
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
          { path: 'payment',       element: <AdminPaymentChannels /> },
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
