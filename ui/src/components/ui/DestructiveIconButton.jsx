import React from 'react';
import { Trash2 } from 'lucide-react';

/**
 * DestructiveIconButton — 统一的 icon-only 删除按钮（IA audit M-V1）。
 *
 * 旧代码在 6+ 个 admin 页面有 3 种不同样式：
 *   - hover:bg-error/20 text-error
 *   - hover:bg-error/20 + p-2
 *   - text-on-surface-variant hover:text-error（仅 hover 红）
 *   - text-on-surface-variant hover:text-error hover:bg-error/10
 *   - text-on-surface-variant hover:text-error 无 bg
 *
 * 现在统一为：默认中性灰，hover 红字 + 浅红 bg，符合 Linear/Vercel
 * "destructive action 默认收敛，hover 才显眼"的约定。
 *
 * 默认渲染 <Trash2 size={14}>，调用方可传 `icon` 覆盖。
 */
const DestructiveIconButton = ({
  onClick,
  title,
  icon: Icon = Trash2,
  size = 14,
  disabled = false,
  className = '',
  ...rest
}) => (
  <button
    type="button"
    onClick={onClick}
    disabled={disabled}
    title={title}
    aria-label={title}
    className={[
      'inline-flex items-center justify-center',
      'w-8 h-8 rounded-control',
      'text-on-surface-variant',
      'hover:text-error hover:bg-error/10',
      'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-error/40',
      'disabled:opacity-40 disabled:cursor-not-allowed',
      'transition-colors',
      className,
    ].filter(Boolean).join(' ')}
    {...rest}
  >
    <Icon size={size} aria-hidden="true" />
  </button>
);

export default DestructiveIconButton;
