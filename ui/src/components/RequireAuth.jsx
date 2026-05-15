import React from 'react';
import { useTranslation } from 'react-i18next';
import { Lock, ArrowRight } from 'lucide-react';

/**
 * 鉴权守卫：
 *  - 已登录：直接渲染 children
 *  - 未登录：只渲染登录提示，不挂载受保护页面
 *
 * 之前的软守卫会把账单、充值、工单等页面继续挂载，导致未登录访问时仍然发起
 * 受保护 API 请求并刷出 401 噪音。项目未上线，直接收紧为硬阻断。
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
