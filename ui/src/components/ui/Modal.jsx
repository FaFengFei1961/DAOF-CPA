import React, { useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { X } from 'lucide-react';
import { useModalA11y } from '../../hooks/useModalA11y';

/**
 * Modal — 标准对话框（Phase 1）
 *
 * 取代各页自己写 `<div className="fixed inset-0 z-[60] flex items-center justify-center p-4 bg-black/70 backdrop-blur-md">`
 * 复用现有 useModalA11y hook（focus trap + ESC 关闭 + 背景点击）。
 *
 * size: sm (max-w-md) / md (max-w-2xl) / lg (max-w-4xl) / xl (max-w-6xl)
 */
const SIZE_MAX = {
  sm: 'max-w-md',
  md: 'max-w-2xl',
  lg: 'max-w-4xl',
  xl: 'max-w-6xl',
};

const Modal = ({
  open,
  onClose,
  title,
  description,
  size = 'md',
  footer,
  children,
  initialFocusRef,
  closeOnBackdrop = true,
}) => {
  const { t } = useTranslation();
  const closeBtnRef = useRef(null);
  const modalRef = useRef(null);
  const focusRef = initialFocusRef || closeBtnRef;
  const { onBackdropClick } = useModalA11y(open, onClose, focusRef, modalRef);

  if (!open) return null;
  const widthCls = SIZE_MAX[size] || SIZE_MAX.md;

  return (
    <div
      ref={modalRef}
      role="dialog"
      aria-modal="true"
      aria-labelledby={title ? 'modal-title' : undefined}
      aria-describedby={description ? 'modal-desc' : undefined}
      onClick={closeOnBackdrop ? onBackdropClick : undefined}
      className="fixed inset-0 z-[60] flex items-center justify-center p-4 bg-black/60 backdrop-blur-sm animate-in fade-in duration-200"
    >
      <div
        className={`bg-surface ${widthCls} w-full max-h-[90vh] rounded-overlay shadow-2xl border border-outline-variant flex flex-col overflow-hidden animate-in zoom-in-95 fade-in slide-in-from-bottom-2 duration-200`}
      >
        {(title || onClose) && (
          <header className="flex items-start justify-between gap-3 px-5 py-4 border-b border-outline-variant/40">
            <div className="min-w-0">
              {title && (
                <h2 id="modal-title" className="text-base font-semibold text-on-surface truncate">
                  {title}
                </h2>
              )}
              {description && (
                <p id="modal-desc" className="text-xs text-on-surface-variant mt-1">{description}</p>
              )}
            </div>
            <button
              ref={closeBtnRef}
              type="button"
              onClick={onClose}
              aria-label={t('COMMON.CLOSE', '关闭')}
              className="w-8 h-8 rounded-lg flex items-center justify-center text-on-surface-variant hover:text-on-surface hover:bg-on-surface/[0.06] shrink-0 fl-spring"
            >
              <X size={18} />
            </button>
          </header>
        )}
        <div className="flex-1 overflow-y-auto px-5 py-4">
          {children}
        </div>
        {footer && (
          <footer className="px-5 py-3 border-t border-outline-variant/40 bg-surface-container/40 flex items-center justify-end gap-2">
            {footer}
          </footer>
        )}
      </div>
    </div>
  );
};

export default Modal;
