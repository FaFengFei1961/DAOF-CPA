// useModalA11y - 共享 hook 给模态框统一加 a11y 行为：
//   1. ESC 键关闭
//   2. 背景点击关闭（通过 onBackdropClick handler 暴露给调用方）
//   3. 模态打开时自动聚焦（initialFocusRef，可选）
//   4. Focus trap：Tab/Shift+Tab 在模态框内循环（modalRef）
//   5. 模态关闭后焦点恢复到打开前的元素
//
// 使用方式：
//   const closeBtnRef = useRef(null);
//   const modalRef = useRef(null);
//   const { onBackdropClick } = useModalA11y(isOpen, onClose, closeBtnRef, modalRef);
//   <div ref={modalRef} role="dialog" aria-modal="true" aria-labelledby="..."
//        onClick={onBackdropClick}>
//     <button ref={closeBtnRef}>×</button>
//   </div>
//
// 注：role="dialog" / aria-modal / aria-labelledby 仍由调用方 JSX 写明
// （避免 hook 注入 DOM 的复杂度）。
//
// fix CRITICAL C5（codex 第二十轮 + WCAG 2.1.2 + 2.4.3）：
//   原实现仅 ESC + initialFocus，缺：
//     - focus trap：Tab 键穿透到背景被遮罩元素
//     - 焦点恢复：关闭后焦点丢失，键盘用户失去定位
//   现补齐 modalRef Tab 循环 + previousFocus 自动恢复。
import { useEffect, useCallback, useRef } from 'react';

// 可获焦元素选择器（覆盖常见交互控件）
const FOCUSABLE_SELECTOR = [
  'a[href]',
  'button:not([disabled])',
  'input:not([disabled]):not([type="hidden"])',
  'select:not([disabled])',
  'textarea:not([disabled])',
  '[tabindex]:not([tabindex="-1"])',
  'audio[controls]',
  'video[controls]',
  '[contenteditable]:not([contenteditable="false"])',
].join(',');

function getFocusableElements(container) {
  if (!container) return [];
  return Array.from(container.querySelectorAll(FOCUSABLE_SELECTOR))
    .filter((el) => !el.hasAttribute('disabled') && el.offsetParent !== null);
}

export function useModalA11y(isOpen, onClose, initialFocusRef, modalRef) {
  // 记录打开前的焦点元素，关闭时恢复
  const previousFocusRef = useRef(null);

  // ESC 关闭
  useEffect(() => {
    if (!isOpen) return;
    const onKey = (e) => {
      if (e.key === 'Escape') onClose?.();
    };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, [isOpen, onClose]);

  // 焦点管理：保存上一焦点 → 移入模态 → 关闭时恢复。
  //
  // fix MAJOR M-F1（gemini 第二十一轮）：StrictMode 双调用兼容。
  // React StrictMode 在 dev 下双次 mount/unmount → 第一次保存触发按钮 → 卸载恢复 →
  // 第二次进入时 document.activeElement 已变成 body 或模态内部元素 → 覆盖正确的 ref。
  // 修复：仅当 ref 为空时才记录焦点；卸载（真关闭）后清空 ref 让下次打开重新捕获。
  useEffect(() => {
    if (!isOpen) return;
    // 仅在第一次（ref 为空）记录上一焦点，防 StrictMode 双调用覆盖
    if (!previousFocusRef.current) {
      previousFocusRef.current = document.activeElement;
    }

    // setTimeout(0) 让 DOM 完成渲染后再聚焦，避免被覆盖
    const timerId = setTimeout(() => {
      if (initialFocusRef?.current) {
        initialFocusRef.current.focus();
      } else if (modalRef?.current) {
        // fallback：聚焦模态内第一个可获焦元素
        const first = getFocusableElements(modalRef.current)[0];
        first?.focus();
      }
    }, 0);

    return () => {
      clearTimeout(timerId);
      // 关闭时恢复焦点（仅当上一元素仍在 DOM 内）
      const prev = previousFocusRef.current;
      if (prev && typeof prev.focus === 'function' && document.contains(prev)) {
        prev.focus();
      }
      // 真关闭后清空 ref，下次打开重新捕获正确的触发元素
      previousFocusRef.current = null;
    };
  }, [isOpen, initialFocusRef, modalRef]);

  // Focus trap：拦截 Tab / Shift+Tab，让焦点在模态内循环
  useEffect(() => {
    if (!isOpen || !modalRef?.current) return;
    const onKey = (e) => {
      if (e.key !== 'Tab') return;
      const focusables = getFocusableElements(modalRef.current);
      if (focusables.length === 0) {
        e.preventDefault();
        return;
      }
      const first = focusables[0];
      const last = focusables[focusables.length - 1];
      const active = document.activeElement;

      if (e.shiftKey) {
        // Shift+Tab：到第一个 → 跳到最后一个
        if (active === first || !modalRef.current.contains(active)) {
          e.preventDefault();
          last.focus();
        }
      } else {
        // Tab：到最后一个 → 跳到第一个
        if (active === last || !modalRef.current.contains(active)) {
          e.preventDefault();
          first.focus();
        }
      }
    };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, [isOpen, modalRef]);

  // 背景点击关闭：仅当点击发生在直接挂 onClick 的元素上（即背景层），
  // 不响应模态内部冒泡上来的事件。
  const onBackdropClick = useCallback(
    (e) => {
      if (e.target === e.currentTarget) onClose?.();
    },
    [onClose],
  );

  return { onBackdropClick };
}
