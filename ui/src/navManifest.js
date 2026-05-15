/**
 * navManifest — 单一导航源（Phase 5 从 routes.jsx 拆出）
 *
 * 拆分原因（codex Phase 5 审查 #4）：routes.jsx 同时导出 React 组件 (router)
 * 和非组件 manifest 数据，触发 react-refresh/only-export-components 大量 lint
 * error，破坏 Vite Fast Refresh。把 manifest 移到纯数据文件后这类 error 消失。
 *
 * Sidebar / AdminSidebar / MobileBottomNav 都从这里读，避免菜单分散在多处。
 */
import {
  Home, KeySquare, BarChart2, CreditCard, Package, Wallet,
  Receipt, MessageSquare, Settings as SettingsIcon,
  Network, Activity, Layers, Package as PackageIcon,
  ShieldAlert, Users, BarChart3, Bell, Globe, Key, Shield, ShieldCheck,
} from 'lucide-react';

// ─── User-side ───────────────────────────────────────────────────
// Phase 8：删"产品中心"入口（套餐购买已合并到仪表盘首页）。/upgrade 路由
// 保留兼容深链接（NotificationCenter action_url 等可能跳过来）。
export const userNav = [
  { id: 'dashboard', path: '/',         icon: Home,         labelKey: 'MENU.DASHBOARD', labelFallback: '仪表盘' },
  { id: 'tokens',    path: '/tokens',   icon: KeySquare,    labelKey: 'MENU.TOKENS',    labelFallback: 'API 令牌' },
  { id: 'stats',     path: '/stats',    icon: BarChart2,    labelKey: 'MENU.STATS',     labelFallback: '数据看板' },
  { id: 'pricing',   path: '/pricing',  icon: CreditCard,   labelKey: 'MENU.PRICING',   labelFallback: '定价' },
  { id: 'topup',     path: '/topup',    icon: Wallet,       labelKey: 'MENU.TOPUP',     labelFallback: '充值' },
  { id: 'bills',     path: '/bills',    icon: Receipt,      labelKey: 'MENU.BILLS',     labelFallback: '账单' },
  { id: 'tickets',   path: '/tickets',  icon: MessageSquare,labelKey: 'MENU.TICKETS',   labelFallback: '工单' },
];

export const userBottomNav = [
  { id: 'settings',  path: '/settings', icon: SettingsIcon, labelKey: 'MENU.SETTINGS',  labelFallback: '系统设置' },
];

// ─── Mobile bottom nav (5 grid + more panel) ────────────────────
// Phase 8：移动端底栏同步删"产品中心"
export const mobileBottomNav = [
  { id: 'dashboard', path: '/',        icon: Home,         labelKey: 'MENU.DASHBOARD', labelFallback: '仪表盘' },
  { id: 'tokens',    path: '/tokens',  icon: KeySquare,    labelKey: 'MENU.TOKENS',    labelFallback: 'API 令牌' },
  { id: 'topup',     path: '/topup',   icon: CreditCard,   labelKey: 'MENU.TOPUP',     labelFallback: '充值' },
  { id: 'tickets',   path: '/tickets', icon: MessageSquare,labelKey: 'MENU.TICKETS',   labelFallback: '工单' },
];

export const mobileMoreNav = [
  { id: 'pricing',  path: '/pricing',  icon: CreditCard,    labelKey: 'MENU.PRICING',  labelFallback: '费率与模型' },
  { id: 'stats',    path: '/stats',    icon: BarChart2,     labelKey: 'MENU.STATS',    labelFallback: '数据看板' },
  { id: 'bills',    path: '/bills',    icon: Receipt,       labelKey: 'MENU.BILLS',    labelFallback: '账单' },
  { id: 'settings', path: '/settings', icon: SettingsIcon,  labelKey: 'MENU.SETTINGS', labelFallback: '系统设置' },
];

// ─── Admin-side ──────────────────────────────────────────────────
//
// standalone: true → 走独立 page；tab: 'xxx' → 复用 Settings hideNav initialTab
// Phase 4 后已无 tab 项；spread 仍保留以备未来增量 admin form
//
export const adminNav = [
  {
    groupKey: 'SETTINGS.GROUP_BUSINESS', groupFallback: '业务',
    items: [
      { id: 'channels',        standalone: true, path: '/admin/channels',     icon: Network,    labelKey: 'MENU.CHANNELS',          labelFallback: '渠道枢纽' },
      { id: 'credits_monitor', standalone: true, path: '/admin/credits',      icon: Activity,   labelKey: 'SETTINGS.TAB_CREDITS',    labelFallback: '号池监控' },
      { id: 'quota_plans',     standalone: true, path: '/admin/quota-plans',  icon: Layers,     labelKey: 'SETTINGS.TAB_QUOTA_PLANS',labelFallback: '配额计划库' },
      { id: 'packages',        standalone: true, path: '/admin/packages',     icon: PackageIcon,labelKey: 'SETTINGS.TAB_PACKAGES',   labelFallback: '销售套餐' },
      { id: 'coupons',         standalone: true, path: '/admin/coupons',      icon: PackageIcon,labelKey: 'SETTINGS.TAB_COUPONS',    labelFallback: '优惠券模板' },
      { id: 'finance',         standalone: true, path: '/admin/finance',      icon: ShieldAlert,labelKey: 'SETTINGS.TAB_FINANCE',    labelFallback: '财务工作区' },
    ],
  },
  {
    groupKey: 'SETTINGS.GROUP_USERS', groupFallback: '用户',
    items: [
      { id: 'users',           standalone: true, path: '/admin/users',           icon: Users,     labelKey: 'SETTINGS.TAB_USERS',         labelFallback: '用户管理' },
      { id: 'users_usage',     standalone: true, path: '/admin/users/usage',     icon: BarChart3, labelKey: 'ADMIN.NAV_USAGE_OVERVIEW',  labelFallback: '用户用量大盘' },
      { id: 'upstream_margin', standalone: true, path: '/admin/upstream/margin', icon: Activity,  labelKey: 'ADMIN.NAV_UPSTREAM_MARGIN', labelFallback: '上游成本毛利' },
      { id: 'audit_events',    standalone: true, path: '/admin/audit/events',    icon: BarChart3, labelKey: 'ADMIN.NAV_AUDIT_EVENTS',    labelFallback: '请求事件审计' },
    ],
  },
  {
    groupKey: 'SETTINGS.GROUP_SYSTEM', groupFallback: '系统',
    items: [
      { id: 'sync',           standalone: true, path: '/admin/sync',          icon: Activity,     labelKey: 'SETTINGS.TAB_SYNC',      labelFallback: '号池同步' },
      { id: 'general',        standalone: true, path: '/admin/general',       icon: SettingsIcon, labelKey: 'SETTINGS.TAB_GENERAL',   labelFallback: '常规设置' },
      { id: 'oauth',          standalone: true, path: '/admin/oauth',         icon: Key,          labelKey: 'SETTINGS.TAB_OAUTH',     labelFallback: 'OAuth' },
      { id: 'sms',            standalone: true, path: '/admin/sms',           icon: MessageSquare,labelKey: 'SETTINGS.TAB_SMS',       labelFallback: '短信' },
      { id: 'risk',           standalone: true, path: '/admin/risk',          icon: ShieldCheck,  labelKey: 'SETTINGS.TAB_RISK',      labelFallback: '风控' },
      { id: 'notifications',  standalone: true, path: '/admin/notifications', icon: Bell,         labelKey: 'NOTIF.ADMIN.TAB',        labelFallback: '通知管理' },
      { id: 'admin_tickets',  standalone: true, path: '/admin/tickets',       icon: MessageSquare,labelKey: 'TICKET.ADMIN.TAB',       labelFallback: '工单管理' },
      { id: 'i18n',           standalone: true, path: '/admin/i18n',          icon: Globe,        labelKey: 'SETTINGS.TAB_I18N',      labelFallback: '国际化' },
      { id: 'moderation',     standalone: true, path: '/admin/moderation',    icon: Shield,       labelKey: 'SETTINGS.TAB_MODERATION',labelFallback: '内容审核' },
    ],
  },
];

// 扁平化 admin 项。tab 项才走 Settings wrapper；standalone 项要在 routes children 单独写明
export const adminFlatItems = adminNav.flatMap(g => g.items);
export const adminTabItems = adminFlatItems.filter(item => item.tab && !item.standalone);
