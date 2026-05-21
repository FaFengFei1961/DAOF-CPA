import React from 'react';
import { useTranslation } from 'react-i18next';
import { Link, NavLink } from 'react-router-dom';
import { Settings as SettingsIcon, Wallet, ArrowUpRight } from 'lucide-react';
import { userNav } from '../navManifest';
import { useAuth } from '../context/AuthContext';
import { useCurrency } from '../context/CurrencyContext';

/**
 * Expanded user sidebar (Sprint J-3 真重设计版本):
 *   - 顶部 brand 块（保持原始尺寸，hover 反馈）
 *   - 中段 nav 列表
 *   - 底部固定 "钱包卡 + 充值 CTA"（登录用户）或 settings 入口
 *
 * 余额来自 AuthContext.profile —— /api/user/me 已经在挂载 + 30s 轮询 +
 * user-profile-refresh 三处刷新。这里直接读，不再二次 fetch。
 */
const NavItem = ({ to, label, Icon }) => {
  const end = to === '/';
  return (
    <NavLink
      to={to}
      end={end}
      title={label}
      className={({ isActive }) =>
        `relative w-full flex items-center gap-3 h-9 pl-3 pr-2 rounded-control text-[13px] font-medium transition
         ${isActive
           ? 'bg-primary/10 text-primary'
           : 'text-on-surface-variant hover:bg-on-surface/[0.05] hover:text-on-surface'}`
      }
    >
      {({ isActive }) => (
        <>
          {isActive && (
            <span
              aria-hidden
              className="absolute left-0 top-1/2 -translate-y-1/2 w-[3px] h-5 bg-primary rounded-control-r-full"
            />
          )}
          <Icon size={16} strokeWidth={isActive ? 2.25 : 1.75} className="shrink-0" />
          <span className="truncate">{label}</span>
        </>
      )}
    </NavLink>
  );
};

const WalletCard = () => {
  const { t } = useTranslation();
  const { profile, isAuthenticated, isAdmin } = useAuth();
  const { formatCurrency } = useCurrency();

  // Admin 不显示钱包；未登录用户也不显示（避免 sidebar 出现登录入口与 topbar 重复）
  if (isAdmin || !isAuthenticated || !profile) return null;

  const balance = Number(profile.quota ?? 0);
  const isLow = balance <= 0;

  return (
    <div className="mx-2 mb-2 rounded-overlay border border-outline-variant/40 bg-surface-container/60 p-3">
      <div className="flex items-center gap-2 mb-2">
        <div className="w-7 h-7 rounded-control bg-primary/10 text-primary flex items-center justify-center shrink-0">
          <Wallet size={14} />
        </div>
        <div className="flex flex-col leading-tight min-w-0">
          <span className="text-[10px] uppercase tracking-wider text-on-surface-variant">
            {t('SHELL.USER.WALLET_LABEL', '账户余额')}
          </span>
          <span className="text-[15px] font-semibold text-on-surface num-tabular truncate">
            {formatCurrency(balance, 2)}
          </span>
        </div>
      </div>
      <Link
        to="/topup"
        className={`flex items-center justify-center gap-1.5 h-8 rounded-control text-xs font-semibold transition w-full
          ${isLow
            ? 'bg-primary text-on-primary hover:bg-primary/90'
            : 'bg-on-surface/[0.04] text-on-surface hover:bg-on-surface/[0.08] border border-outline-variant/40'}`}
      >
        {t('SHELL.USER.WALLET_TOPUP', '充值')}
        <ArrowUpRight size={12} strokeWidth={2.4} />
      </Link>
    </div>
  );
};

const UserSidebar = () => {
  const { t } = useTranslation();

  return (
    <nav
      aria-label={t('SHELL.USER.NAV_LABEL')}
      className="hidden lg:flex flex-col w-60 h-screen bg-surface-container/40 border-r border-outline-variant/40 fixed top-0 left-0 z-50"
    >
      <Link
        to="/"
        className="flex items-center gap-2.5 px-4 h-14 border-b border-outline-variant/30 hover:bg-on-surface/[0.03] transition shrink-0"
        aria-label="DAOF-CPA"
      >
        <img src="/daof_logo.png" alt="" className="w-8 h-8 rounded-control shrink-0" />
        <div className="min-w-0">
          <div className="text-sm font-bold tracking-tight text-on-surface leading-tight">
            DAOF-CPA
          </div>
          <div className="text-[10px] text-on-surface-variant leading-tight mt-0.5">
            {t('SHELL.USER.TAGLINE')}
          </div>
        </div>
      </Link>

      <div className="flex-1 overflow-y-auto px-2 py-3 space-y-0.5 no-scrollbar">
        {userNav.map(item => (
          <NavItem
            key={item.id}
            to={item.path}
            label={t(item.labelKey)}
            Icon={item.icon}
          />
        ))}
      </div>

      <WalletCard />

      <div className="border-t border-outline-variant/30 px-2 py-2 shrink-0">
        <NavItem
          to="/settings"
          label={t('MENU.SETTINGS')}
          Icon={SettingsIcon}
        />
      </div>
    </nav>
  );
};

export default UserSidebar;
