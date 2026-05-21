import React from 'react';
import { useTranslation } from 'react-i18next';
import { Link, NavLink } from 'react-router-dom';
import { Settings as SettingsIcon, Wallet, ArrowUpRight, Sparkles } from 'lucide-react';
import { userNav } from '../navManifest';
import { useAuth } from '../context/AuthContext';
import { useCurrency } from '../context/CurrencyContext';

/**
 * UserSidebar — Sprint J-3 batch 3：宽度加到 272，brand 区放大，
 * 钱包卡作为底部主角卡而不是小条。整体目标：让 sidebar 看起来像
 * "产品的左半"，不是导航条。
 */
const NavItem = ({ to, label, Icon }) => {
  const end = to === '/';
  return (
    <NavLink
      to={to}
      end={end}
      title={label}
      className={({ isActive }) =>
        `relative w-full flex items-center gap-3 h-10 pl-3 pr-2 rounded-control text-[13.5px] font-medium transition
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
              className="absolute left-0 top-1/2 -translate-y-1/2 w-[3px] h-6 bg-primary rounded-control-r-full"
            />
          )}
          <Icon size={17} strokeWidth={isActive ? 2.25 : 1.75} className="shrink-0" />
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
    <div className="mx-3 mb-3 rounded-overlay border border-outline-variant/40 bg-surface-container/70 p-4 relative overflow-hidden">
      {/* 顶部细高光，提升纵深感（dark 模式才显） */}
      <div
        aria-hidden
        className="absolute inset-x-0 top-0 h-px bg-gradient-to-r from-transparent via-primary/20 to-transparent pointer-events-none"
      />
      <div className="flex items-center gap-2 mb-3">
        <span className="text-[10px] uppercase tracking-[0.12em] font-medium text-on-surface-variant">
          {t('SHELL.USER.WALLET_LABEL', '账户余额')}
        </span>
        {!isLow && (
          <span className="ml-auto inline-flex items-center gap-1 text-[10px] font-medium text-success">
            <Sparkles size={10} strokeWidth={2.5} />
          </span>
        )}
      </div>
      <div className="flex items-baseline gap-1 mb-3">
        <span
          className="text-[26px] font-mono font-semibold tracking-tight text-on-surface tabular-nums leading-none truncate"
          title={formatCurrency(balance, 2)}
        >
          {formatCurrency(balance, 2)}
        </span>
      </div>
      <Link
        to="/topup"
        className={`flex items-center justify-center gap-1.5 h-9 rounded-control text-[13px] font-semibold transition w-full
          ${isLow
            ? 'bg-primary text-on-primary hover:bg-primary/90 shadow-sm'
            : 'bg-on-surface/[0.05] text-on-surface hover:bg-on-surface/[0.08] border border-outline-variant/50'}`}
      >
        <Wallet size={13} strokeWidth={2.2} />
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
      className="hidden lg:flex flex-col w-[272px] h-screen bg-surface-container/30 border-r border-outline-variant/40 fixed top-0 left-0 z-50"
    >
      <Link
        to="/"
        className="flex items-center gap-3 px-5 h-16 border-b border-outline-variant/30 hover:bg-on-surface/[0.03] transition shrink-0"
        aria-label="DAOF-CPA"
      >
        <img src="/daof_logo.png" alt="" className="w-9 h-9 rounded-control shrink-0" />
        <div className="min-w-0">
          <div className="text-[15px] font-bold tracking-tight text-on-surface leading-tight">
            DAOF-CPA
          </div>
          <div className="text-[11px] text-on-surface-variant leading-tight mt-0.5 tracking-wide">
            {t('SHELL.USER.TAGLINE')}
          </div>
        </div>
      </Link>

      <div className="flex-1 overflow-y-auto px-3 py-4 space-y-0.5 no-scrollbar">
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

      <div className="border-t border-outline-variant/30 px-3 py-2 shrink-0">
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
