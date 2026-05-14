import React from 'react';
import { Inbox } from 'lucide-react';

/**
 * EmptyState — 统一空状态（Phase 1）
 *
 * 取代各页自己写的"暂无数据 / 尚无 X" 不一致文案。
 * 视觉规则：
 *  - 居中圆形 icon 容器（subtle bg）
 *  - title body2 (16px) 600 + sub caption1 (12px) muted
 *  - 可选 action 按钮（跳到创建页等）
 *  - compact 版用于表格内嵌（无 padding）
 */
const EmptyState = ({
  icon: Icon = Inbox,
  title = '暂无数据',
  sub,
  action,
  compact = false,
  className = '',
}) => {
  const padCls = compact ? 'py-8' : 'py-16';
  return (
    <div className={`flex flex-col items-center justify-center text-center ${padCls} ${className}`}>
      <div className="w-12 h-12 rounded-full bg-on-surface/[0.04] border border-outline-variant flex items-center justify-center text-on-surface-variant/70 mb-3">
        <Icon size={20} strokeWidth={1.5} />
      </div>
      <div className="text-sm font-medium text-on-surface">{title}</div>
      {sub && (
        <div className="text-xs text-on-surface-variant mt-1 max-w-xs">{sub}</div>
      )}
      {action && <div className="mt-4">{action}</div>}
    </div>
  );
};

export default EmptyState;
