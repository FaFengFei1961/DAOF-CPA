import React from 'react';

/**
 * StatusBadge — 状态徽章
 * variant: success/warning/error/info/neutral，使用 var(--color-*) token
 */
const StatusBadge = ({
  variant = 'neutral',
  children,
  className = '',
}) => {
  const variantStyles = {
    success: 'bg-[var(--color-success)]/10 text-[var(--color-success)] border-[var(--color-success)]/20',
    warning: 'bg-[var(--color-warning)]/10 text-[var(--color-warning)] border-[var(--color-warning)]/20',
    error: 'bg-[var(--color-error)]/10 text-[var(--color-error)] border-[var(--color-error)]/20',
    info: 'bg-[var(--color-info)]/10 text-[var(--color-info)] border-[var(--color-info)]/20',
    neutral: 'bg-surface-container-highest text-on-surface-variant border-outline-variant/30',
  };

  const style = variantStyles[variant] || variantStyles.neutral;

  return (
    <span className={`inline-flex items-center px-2 py-0.5 rounded-control text-xs font-medium border ${style} ${className}`}>
      {children}
    </span>
  );
};

export default StatusBadge;
