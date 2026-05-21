import React from 'react';
import { useTranslation } from 'react-i18next';
import { Link, NavLink } from 'react-router-dom';
import { Settings as SettingsIcon, Wallet, ArrowUpRight, Sparkles } from 'lucide-react';
import { userNav } from '../navManifest';
import { useAuth } from '../context/AuthContext';
import { useCurrency } from '../context/CurrencyContext';

/**
 * UserSidebar — Sprint J-3 batch 4：宽度回退 240px。
 *
 * batch 3 加宽到 272 之后看起来反而更空（nav 才 7 项 4-5 个汉字，加宽
 * 没补任何信息，纯增加空白）。回到 240 + 紧凑行高，让 sidebar 看着是
 * "干练导航"而不是"虚胖空栏"。
 * Wallet 卡视觉提升保留（顶部 highlight + 大字余额 + 带 icon 的充值
 * 按钮），只是 padding / 数字尺寸往窄容器适配。
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
           ? 'bg-primary/[0.08] text-primary'
           : 'text-on-surface-variant hover:bg-on-surface/[0.04] hover:text-on-surface'}`
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

  if (isAdmin || !isAuthenticated || !profile) return null;

  const balance = Number(profile.quota ?? 0);
  const isLow = balance <= 0;

  return (
    <div className="mx-2 mb-2 rounded-overlay border border-outline-variant/40 bg-surface-container/70 p-3 relative overflow-hidden">
      {/* 顶部细高光，提升纵深感（dark 模式才显） */}
      <div
        aria-hidden
        className="absolute inset-x-0 top-0 h-px bg-gradient-to-r from-transparent via-primary/20 to-transparent pointer-events-none"
      />
      <div className="flex items-center gap-2 mb-2">
        <span className="text-[10px] uppercase tracking-[0.1em] font-medium text-on-surface-variant">
          {t('SHELL.USER.WALLET_LABEL', '账户余额')}
        </span>
        {!isLow && (
          <span className="ml-auto inline-flex items-center text-success">
            <Sparkles size={10} strokeWidth={2.5} />
          </span>
        )}
      </div>
      <div className="flex items-baseline gap-1 mb-2.5">
        <span
          className="text-[22px] font-mono font-semibold tracking-tight text-on-surface tabular-nums leading-none truncate"
          title={formatCurrency(balance, 2)}
        >
          {formatCurrency(balance, 2)}
        </span>
      </div>
      <Link
        to="/topup"
        className={`flex items-center justify-center gap-1.5 h-8 rounded-control text-xs font-semibold transition w-full
          ${isLow
            ? 'bg-primary text-on-primary hover:bg-primary/90'
            : 'bg-on-surface/[0.05] text-on-surface hover:bg-on-surface/[0.08] border border-outline-variant/50'}`}
      >
        <Wallet size={12} strokeWidth={2.2} />
        {t('SHELL.USER.WALLET_TOPUP', '充值')}
        <ArrowUpRight size={11} strokeWidth={2.4} />
      </Link>
    </div>
  );
};

const UserSidebar = () => {
  const { t } = useTranslation();

  return (
    <nav
      aria-label={t('SHELL.USER.NAV_LABEL')}
      className="hidden lg:flex flex-col w-60 h-screen bg-surface-container/30 border-r border-outline-variant/40 fixed top-0 left-0 z-50"
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
