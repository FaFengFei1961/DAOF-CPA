import React, { useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { ArrowLeft } from 'lucide-react';
import UpgradePage from './UpgradePage';

/**
 * BrowsePackagesModal — 主内容区覆盖层式套餐浏览弹窗。
 *
 * 设计意图：
 *   Dashboard 上的"暂无活跃订阅"卡片需要引导用户购套餐。用 Modal 而非独立路由：
 *     - 仅覆盖**主内容区**（桌面端 lg:left-60 让出 sidebar 宽度），sidebar 始终可见可点
 *     - 不改 URL（一键关闭原位返回，没有"返回上一页"困扰）
 *     - 只渲染商店内容（UpgradePage 用 embedded 跳过 tab 和 mine pane）
 *     - 购买成功后自动关闭，外层订阅列表自动刷新
 *
 * 键盘交互：ESC 关闭。锁定 body 滚动避免背景穿透。
 */
const BrowsePackagesModal = ({ isOpen, onClose }) => {
  const { t } = useTranslation();

  useEffect(() => {
    if (!isOpen) return undefined;
    const onKey = (e) => { if (e.key === 'Escape') onClose(); };
    const prevOverflow = document.body.style.overflow;
    document.addEventListener('keydown', onKey);
    document.body.style.overflow = 'hidden';
    return () => {
      document.removeEventListener('keydown', onKey);
      document.body.style.overflow = prevOverflow;
    };
  }, [isOpen, onClose]);

  if (!isOpen) return null;

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby="browse-packages-title"
      className="fixed top-0 right-0 bottom-0 left-0 lg:left-60 z-[120] flex flex-col bg-black/70 backdrop-blur-sm animate-in fade-in duration-200"
    >
      <div className="grid grid-cols-[2.25rem_1fr_2.25rem] items-center px-6 py-3 bg-surface border-b border-outline-variant shrink-0">
        <button
          type="button"
          onClick={onClose}
          aria-label={t('COMMON.BACK', '返回')}
          className="w-9 h-9 flex items-center justify-center rounded-control text-on-surface-variant hover:text-on-surface hover:bg-on-surface/[0.06] transition focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary"
        >
          <ArrowLeft size={18} />
        </button>
        <h2 id="browse-packages-title" className="text-base font-semibold text-on-surface text-center">
          {t('BROWSE_PACKAGES.TITLE', '浏览套餐')}
        </h2>
        <span aria-hidden="true" />
      </div>
      <div className="flex-1 overflow-y-auto bg-surface">
        <div className="max-w-[1280px] mx-auto px-4 sm:px-8 lg:px-10 py-6">
          <UpgradePage embedded onPurchaseSuccess={onClose} />
        </div>
      </div>
    </div>
  );
};

export default BrowsePackagesModal;
