import React from 'react';
import { useTranslation } from 'react-i18next';
import { Lock, ArrowRight } from 'lucide-react';

/**
 * Hard auth guard:
 *  - signed in: render children
 *  - signed out: render only the sign-in prompt
 *
 * Protected pages are not mounted for signed-out users, avoiding unnecessary 401 requests.
 */
const RequireAuth = ({ isAuthenticated, onSignIn, children }) => {
  const { t } = useTranslation();
  if (isAuthenticated) return children;

  return (
    <div className="space-y-4">
      <div className="fl-card flex items-center justify-between gap-3 px-4 py-3 border-primary/40 bg-primary-container/30">
        <div className="flex items-center gap-3 min-w-0">
          <div className="w-8 h-8 rounded-control bg-primary text-on-primary flex items-center justify-center shrink-0">
            <Lock size={16} />
          </div>
          <div className="min-w-0">
            <div className="text-sm font-semibold text-on-surface">
              {t('AUTH_GATE.TITLE', '需要登录')}
            </div>
            <div className="text-xs text-on-surface-variant">
              {t('AUTH_GATE.SUB', '此页面需要登录后才能查看完整数据')}
            </div>
          </div>
        </div>
        <button type="button" onClick={onSignIn} className="fl-btn fl-btn-prominent shrink-0">
          {t('AUTH_GATE.SIGN_IN', '登录')}
          <ArrowRight size={14} />
        </button>
      </div>
    </div>
  );
};

export default RequireAuth;
