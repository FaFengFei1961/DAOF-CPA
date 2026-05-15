import React from 'react';
import { useTranslation } from 'react-i18next';
import { Link, NavLink } from 'react-router-dom';
import { Settings as SettingsIcon } from 'lucide-react';
import { userNav } from '../navManifest';

/**
 * Expanded user sidebar with full labels and active-route affordance.
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
