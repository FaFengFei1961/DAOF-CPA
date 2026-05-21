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

// IA audit Mi-3 + Mi-5 cleanup:
//   - 删除 UpgradeRedirect / `/upgrade` 路由：后端 notification_links.go 现在
//     直接生成 "/" 和 "/?openBrowse=store"，不再走 compat shim
//   - 删除 adminTabItems import + spread：filter 永远空，是占位代码
//   - useSearchParams 不再需要（UpgradeRedirect 移除后唯一调用点也没了）

// Shells
const UserShell = lazy(() => import('./shells/UserShell'));
const AdminShell = lazy(() => import('./shells/AdminShell'));
import RouteGuard from './shells/RouteGuard';

// IA audit C1 + C2 fix: dedicated 404 + route-level error boundary so
// mistyped URLs, lazy chunk failures, and runtime throws no longer silently
// teleport to "/" (which was masking real bugs and stranding users).
// Both are imported eagerly because they're tiny and act as fallbacks —
// we don't want a Suspense flash on the very screen meant to recover the user.
import NotFound from './components/NotFound';
import RouteErrorBoundary from './components/RouteErrorBoundary';

// OAuth 回调（GitHub / Google / ...）— 浏览器从授权页 redirect 回 /oauth/:provider
// 时由它接住，发 POST 给后端 callback 端点。原 App.jsx 里 OAuthCallbackHandler
// 挂在 RouterProvider 外面，但 RouterProvider 的 NotFound fallback 抢先渲染 404
// 给新用户造成"页面不存在"假象。
const OAuthCallbackPage = lazy(() => import('./pages/OAuthCallbackPage'));

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
// Sprint J-2 IA: GitHub OAuth / Email SMTP / SMS / Risk 四个旧入口合并为
// /admin/auth 一站页面 + 内部 tab。OAuthPage / EmailPage / SmsPage / RiskPage
// 仍 import 但只在 AuthAdminPage 内部用 embedded 模式渲染，旧的独立路由删除。
const AuthAdminPage = lazy(() => import('./pages/admin/system/AuthAdminPage'));
const ModerationPage = lazy(() => import('./pages/admin/system/ModerationPage'));
const CouponsPage = lazy(() => import('./pages/admin/system/CouponsPage'));
const SyncPage = lazy(() => import('./pages/admin/system/SyncPage'));

// Remaining admin forms and finance workspace.
const GeneralAdminPage = lazy(() => import('./pages/admin/system/GeneralAdminPage'));
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
    // errorElement catches throws / loader errors anywhere under this route subtree.
    // The boundary itself routes 404 responses to <NotFound /> for consistent UX.
    errorElement: <RouteErrorBoundary />,
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
    errorElement: <RouteErrorBoundary />,
    children: [
      { index: true, element: <Navigate to="/admin/channels" replace /> },

      { path: 'users/usage',     element: <UsersUsageOverviewPage /> },
      { path: 'upstream/margin', element: <UpstreamMarginPage /> },
      { path: 'audit/events',    element: <AuditEventsPage /> },

      { path: 'channels',        element: <ChannelManagement /> },
      { path: 'credits',         element: <CreditsMonitor /> },
      { path: 'quota-plans',     element: <QuotaPlanManagement /> },
      { path: 'packages',        element: <PackageManagement /> },
      { path: 'users',           element: <UserManagement /> },
      { path: 'notifications',   element: <AdminNotificationManagement /> },
      { path: 'tickets',         element: <AdminCustomerMessages /> },
      { path: 'i18n',            element: <I18nManagement /> },

      // J-2: 旧 /admin/oauth /admin/sms /admin/email /admin/risk 4 入口
      // 合并为 /admin/auth?tab=oauth|sms|email|risk 单页面。
      { path: 'auth',            element: <AuthAdminPage /> },
      { path: 'moderation',      element: <ModerationPage /> },
      { path: 'coupons',         element: <CouponsPage /> },
      { path: 'sync',            element: <SyncPage /> },

      { path: 'general',         element: <GeneralAdminPage /> },
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
    ],
  },
  // OAuth callback — 放在顶层(无 UserShell 包裹)，避免 sidebar + topbar
  // 在跳转动画期间闪一下。组件自己跳转 / 拉起 AuthModal。
  { path: '/oauth/:provider', element: <OAuthCallbackPage /> },

  // Global 404 fallback — dedicated page so users see what they asked for
  // and have explicit escape hatches (back / home / pricing).
  { path: '*', element: <NotFound /> },
]);

export default router;
