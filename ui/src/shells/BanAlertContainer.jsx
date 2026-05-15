import React from 'react';
import { useTranslation } from 'react-i18next';
import { useAuth } from '../context/AuthContext';

/**
 * BanAlertContainer — 全局封禁拦截全屏弹窗（Phase 0）
 *
 * 从 App.jsx 抽出来。封禁触发后，所有路由都被这个全屏 modal 覆盖。
 */
const BanAlertContainer = () => {
  const { t } = useTranslation();
  const { banAlert, closeBan } = useAuth();
  if (!banAlert.isOpen) return null;

  const handleAck = () => {
    closeBan();
    window.location.href = '/';
  };

  return (
    <div className="fixed inset-0 z-[9999] flex items-center justify-center bg-black/90 backdrop-blur-md animate-in fade-in zoom-in duration-300">
      <div className="bg-surface-container-high border border-error/50 rounded-overlay w-full max-w-md p-8 text-center relative overflow-hidden">
        <div className="absolute top-0 right-0 w-48 h-48 bg-error/10 rounded-control-full blur-3xl -mr-20 -mt-20 pointer-events-none" />
        <div className="w-20 h-20 bg-error/30 rounded-control-full flex items-center justify-center mx-auto mb-6 relative z-10">
          <div className="w-12 h-12 bg-error rounded-control-full flex items-center justify-center /30 text-on-surface font-bold text-3xl">!</div>
        </div>
        <h2 className="text-2xl font-bold text-on-surface tracking-tight mb-2 relative z-10">
          {t('APP.BANNED.TITLE', '账户已被限制')}
        </h2>
        {banAlert.reason && (
          <div className="mt-4 p-4 rounded-overlay bg-error/40 border border-error/30 text-error text-sm italic">
            {banAlert.reason}
          </div>
        )}
        <button
          type="button"
          onClick={handleAck}
          className="w-full h-12 mt-6 bg-surface-variant hover:bg-white hover:text-black font-semibold text-on-surface-variant rounded-overlay transition-all border border-outline relative z-10"
        >
          {t('APP.BANNED.ACCEPT_BTN', '我知道了')}
        </button>
      </div>
    </div>
  );
};

export default BanAlertContainer;
