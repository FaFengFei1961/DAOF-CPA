/**
 * 单一路由源 + 导航 manifest（Phase 0 重构）
 *
 * 替换原来 App.jsx 里手写的 hash + switch case 路由。
 * 用 React Router v7 nested routes：
 *   - /         → UserShell（用户布局，含 Sidebar + TopBar + Mobile Bottom Nav）
 *   - /admin/*  → AdminShell（admin 独立布局，独立 Sidebar 含全部 admin 功能）
 *
 * 设计原则（codex+gemini ccg 审查后）：
 *   - admin/user 视觉延续：两个 shell 都用 TopBar + Mica；只换 Sidebar 内容
 *   - 浏览器后退/前进/书签可用：所有 admin 子页都有 URL（/admin/channels、/admin/users 等）
 *   - 兼容旧 hash 链接：utils/hashRedirect.js 启动时统一 redirect 老 #path
 *   - 巨型 Settings.jsx 暂保留：admin 子路由复用 <Settings initialTab=... hideNav />
 *     等到 Phase 1 抽 DataTable / FormRow 时再分页
 *
 * Nav manifest 是导航菜单的单一来源。Sidebar / AdminSidebar / Mobile bottom nav 从这里读，
 * 避免现状的"4 处硬编码菜单"。
 */
import React, { lazy } from 'react';
import { createBrowserRouter, Navigate } from 'react-router-dom';
// Phase 5：nav manifest 已抽到 navManifest.js（避免 Fast Refresh 报错），routes.jsx
// 仅消费 adminTabItems 用于动态生成 settings-tab 路由
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

// Settings 大组件 — admin 子路由全部复用这个文件，通过 initialTab + hideNav 切换面板
// Phase 1 后会逐个抽出独立 page
const Settings = lazy(() => import('./components/Settings'));

// Phase 1 — UserUsageDash 拆出的 3 个独立 page
const UsersUsageOverviewPage = lazy(() => import('./pages/admin/UsersUsageOverviewPage'));
const UpstreamMarginPage = lazy(() => import('./pages/admin/UpstreamMarginPage'));
const AuditEventsPage = lazy(() => import('./pages/admin/AuditEventsPage'));

// Phase 2 — 现有 admin 独立组件直挂路由（不再走 Settings wrapper）
const ChannelManagement = lazy(() => import('./components/ChannelManagement'));
const CreditsMonitor = lazy(() => import('./components/CreditsMonitor'));
const QuotaPlanManagement = lazy(() => import('./components/QuotaPlanManagement'));
const PackageManagement = lazy(() => import('./components/PackageManagement'));
const UserManagement = lazy(() => import('./components/UserManagement'));
const AdminNotificationManagement = lazy(() => import('./components/AdminNotificationManagement'));
const AdminCustomerMessages = lazy(() => import('./components/AdminCustomerMessages'));
const I18nManagement = lazy(() => import('./components/I18nManagement'));

// Phase 3 — 新建的 admin form page（包装 + 自管 fetch/save，修 P2 路由 bug）
const OAuthPage = lazy(() => import('./pages/admin/system/OAuthPage'));
const SmsPage = lazy(() => import('./pages/admin/system/SmsPage'));
const ModerationPage = lazy(() => import('./pages/admin/system/ModerationPage'));
const CouponsPage = lazy(() => import('./pages/admin/system/CouponsPage'));
const SyncPage = lazy(() => import('./pages/admin/system/SyncPage'));

// Phase 4 — Settings.jsx 剩余 admin form 全部抽出独立 page
const GeneralAdminPage = lazy(() => import('./pages/admin/system/GeneralAdminPage'));
const RiskPage = lazy(() => import('./pages/admin/system/RiskPage'));
const FinanceShell = lazy(() => import('./pages/admin/finance/FinancePage'));
const FinanceSettingsPage = lazy(() => import('./pages/admin/finance/FinanceSettingsPage'));
const AdminPaymentChannels = lazy(() => import('./components/AdminPaymentChannels'));
const AdminTopupOrders = lazy(() => import('./components/AdminTopupOrders'));
const AdminSubscriptions = lazy(() => import('./components/AdminSubscriptions'));

// ─── Router 表 ────────────────────────────────────────────────────
// nav manifest 已迁到 ./navManifest.js，避免 Fast Refresh 报错

const router = createBrowserRouter([
  {
    path: '/',
    element: <UserShell />,
    children: [
      // 公开（未登录可见）
      { index: true,    element: <Dashboard /> },
      { path: 'pricing',element: <PricingDash /> },
      { path: 'upgrade',element: <UpgradePage /> },
      { path: 'topup-result', element: <TopupResult /> },
      // 受保护（未登录显示 RequireAuth banner）
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

      // ─── Phase 1：UserUsageDash 拆 3 page ──────────────
      { path: 'users/usage',     element: <UsersUsageOverviewPage /> },
      { path: 'upstream/margin', element: <UpstreamMarginPage /> },
      { path: 'audit/events',    element: <AuditEventsPage /> },
      // 旧 /admin/users-usage 兼容
      { path: 'users-usage',     element: <Navigate to="/admin/users/usage" replace /> },

      // ─── Phase 2：admin 独立组件直挂（绕过 Settings wrapper）──────────
      { path: 'channels',        element: <ChannelManagement /> },
      { path: 'credits',         element: <CreditsMonitor /> },
      { path: 'quota-plans',     element: <QuotaPlanManagement /> },
      { path: 'packages',        element: <PackageManagement /> },
      { path: 'users',           element: <UserManagement /> },
      { path: 'notifications',   element: <AdminNotificationManagement /> },
      { path: 'tickets',         element: <AdminCustomerMessages /> },
      { path: 'i18n',            element: <I18nManagement /> },

      // ─── Phase 3：admin form page（含 fetch/save，修 P2 路由 bug）────────
      { path: 'oauth',           element: <OAuthPage /> },
      { path: 'sms',             element: <SmsPage /> },
      { path: 'moderation',      element: <ModerationPage /> },
      { path: 'coupons',         element: <CouponsPage /> },
      { path: 'sync',            element: <SyncPage /> },

      // ─── Phase 4：Settings 剩余 admin form 全部抽出 ──────────
      { path: 'general',         element: <GeneralAdminPage /> },
      { path: 'risk',            element: <RiskPage /> },
      // 财务工作区 nested routes（每个 sub-tab 独立 URL，浏览器后退可用）
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

      // ─── 兼容已不存在的 Settings tab 路由（Phase 4 完成后所有 admin tab 都已独立）──
      // adminTabItems 现在应为空，保留 spread 以防未来再加 settings-tab 类型项
      ...adminTabItems.map(item => ({
        path: item.path.replace('/admin/', ''),
        element: <Settings initialTab={item.tab} hideNav />,
      })),
    ],
  },
  // 全局 404 → 回首页
  { path: '*', element: <Navigate to="/" replace /> },
]);

export default router;
