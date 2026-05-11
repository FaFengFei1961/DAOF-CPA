import React from 'react';
import { useTranslation } from 'react-i18next';
import { Lock, ArrowRight } from 'lucide-react';

/**
 * 鉴权"软"守卫：
 *  - 已登录：直接渲染 children
 *  - 未登录：在 children 上方放一条 banner 提示"登录后查看完整数据"，
 *           children 自身保持渲染（顶部标题、骨架/空表格），用户能看到原貌。
 *
 * 注意：children 内部需要自己感知 isAuthenticated 来决定是否调用 fetch /
 * 显示空态，避免未登录时弹"鉴权失败"toast。本组件只负责视觉提示，不阻断渲染。
 */
const RequireAuth = ({ isAuthenticated, onSignIn, children }) => {
  const { t } = useTranslation();
  if (isAuthenticated) return children;

  return (
    <div className="space-y-4">
      <div className="fl-card flex items-center justify-between gap-3 px-4 py-3 border-primary/40 bg-primary-container/30">
        <div className="flex items-center gap-3 min-w-0">
          <div className="w-8 h-8 rounded bg-primary text-on-primary flex items-center justify-center shrink-0">
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
      <div inert="" className="opacity-70 pointer-events-none select-none">
        {children}
      </div>
    </div>
  );
};

export default RequireAuth;
