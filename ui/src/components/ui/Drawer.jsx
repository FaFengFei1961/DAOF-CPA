import React, { useEffect, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { X } from 'lucide-react';

/**
 * Drawer — 右侧滑出抽屉（Phase 1，gemini ccg "滥用内联展开应该用 Drawer" #4）
 *
 * 主要用途：
 *   - DataTable 行点击 → 滑出详情，承载 17+ 字段的完整元数据
 *     （取代当前 UserUsageDash 强制 17 列 1960px 横滚的灾难）
 *   - admin 复杂表单（如上游账号成本编辑、工单回复）
 *
 * 视觉规则：
 *  - 桌面：右侧滑出，宽度 sm/md/lg/xl 可选；遮罩半透明 + blur
 *  - 移动：底部滑出占满（按 Win11 mobile pattern）
 *  - ESC + 背景点击 + 关闭按钮 三种关闭路径
 *  - focus trap（自动聚焦关闭按钮）
 */
const SIZE_W = {
  sm: 'md:max-w-md',
  md: 'md:max-w-lg',
  lg: 'md:max-w-2xl',
  xl: 'md:max-w-4xl',
};

const Drawer = ({
  open,
  onClose,
  title,
  description,
  size = 'md',
  footer,
  children,
}) => {
  const { t } = useTranslation();
  const closeRef = useRef(null);
  const panelRef = useRef(null);

  useEffect(() => {
    if (!open) return undefined;
    const handler = (e) => { if (e.key === 'Escape') onClose?.(); };
    window.addEventListener('keydown', handler);
    // 自动聚焦关闭按钮便于键盘 ESC
    queueMicrotask(() => closeRef.current?.focus());
    return () => window.removeEventListener('keydown', handler);
  }, [open, onClose]);

  if (!open) return null;
  const widthCls = SIZE_W[size] || SIZE_W.md;

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby={title ? 'drawer-title' : undefined}
      className="fixed inset-0 z-[60] animate-in fade-in duration-200"
      onClick={(e) => { if (e.target === e.currentTarget) onClose?.(); }}
    >
      {/* 遮罩 */}
      <div className="absolute inset-0 bg-black/50 backdrop-blur-sm" onClick={onClose} aria-hidden />

      {/* 桌面：右侧 / 移动：底部 */}
      <aside
        ref={panelRef}
        className={`absolute inset-x-0 bottom-0 md:inset-y-0 md:right-0 md:left-auto w-full ${widthCls} md:h-full max-h-[90vh] md:max-h-none bg-surface md:border-l border-t md:border-t-0 border-outline-variant shadow-2xl flex flex-col rounded-t-2xl md:rounded-none animate-in slide-in-from-bottom md:slide-in-from-right duration-250`}
      >
        <header className="flex items-start justify-between gap-3 px-5 py-4 border-b border-outline-variant/40 shrink-0">
          <div className="min-w-0">
            {title && (
              <h2 id="drawer-title" className="text-base font-semibold text-on-surface truncate">
                {title}
              </h2>
            )}
            {description && (
              <p className="text-xs text-on-surface-variant mt-1">{description}</p>
            )}
          </div>
          <button
            ref={closeRef}
            type="button"
            onClick={onClose}
            aria-label={t('COMMON.CLOSE', '关闭')}
            className="w-8 h-8 rounded-control flex items-center justify-center text-on-surface-variant hover:text-on-surface hover:bg-on-surface/[0.06] shrink-0"
          >
            <X size={18} />
          </button>
        </header>
        <div className="flex-1 overflow-y-auto px-5 py-4">
          {children}
        </div>
        {footer && (
          <footer className="px-5 py-3 border-t border-outline-variant/40 bg-surface-container/40 flex items-center justify-end gap-2 shrink-0">
            {footer}
          </footer>
        )}
      </aside>
    </div>
  );
};

export default Drawer;
