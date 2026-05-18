import React from 'react';
import { useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { AlertOctagon, MessageSquare } from 'lucide-react';
import { useAuth } from '../context/AuthContext';

/**
 * 顶部红色封禁横幅。仅当 profile.status === 2（封禁）时渲染。
 *
 * 与 BanAlertContainer 的关系：
 *   - BanAlertContainer 是全屏一次性提示，用户 ack 后关闭并 sessionStorage 记 flag。
 *   - BannedBanner 是 session 持续可见的提醒条，关掉 modal 后用户依然知道账户状态。
 *   - 两者协作让封禁路径不再死循环——modal 提醒过一次 + 横幅常驻 + 工单端点可达。
 */
const BannedBanner = () => {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { profile } = useAuth();
  if (!profile || profile.status !== 2) return null;

  return (
    <div className="bg-error/10 border-b border-error/40 text-on-surface px-4 py-2.5 flex items-center gap-3 flex-wrap">
      <AlertOctagon size={18} className="text-error shrink-0" />
      <div className="flex-1 min-w-0 text-sm">
        <span className="font-semibold text-error">
          {t('SHELL.BANNED_BANNER.TITLE', '账户已被封禁')}
        </span>
        {profile.ban_reason && (
          <span className="ml-2 text-on-surface-variant text-xs">
            {t('SHELL.BANNED_BANNER.REASON_PREFIX', '理由：')}{profile.ban_reason}
          </span>
        )}
        <span className="ml-2 text-on-surface-variant text-xs">
          {t('SHELL.BANNED_BANNER.HINT', '业务功能已禁用；如有疑义可提交工单。')}
        </span>
      </div>
      <button
        type="button"
        onClick={() => navigate('/tickets')}
        className="inline-flex items-center gap-1.5 h-8 px-3 rounded-control bg-error text-on-error text-xs font-medium hover:opacity-90 shrink-0"
      >
        <MessageSquare size={14} />
        {t('SHELL.BANNED_BANNER.CONTACT_BTN', '提交工单')}
      </button>
    </div>
  );
};

export default BannedBanner;
