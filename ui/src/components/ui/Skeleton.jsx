import React from 'react';

/**
 * Skeleton — 占位骨架（Phase 1）
 *
 * 替换"加载中..." 满天飞文案。
 * 用法：
 *   <Skeleton w={120} h={16} />               单条
 *   <Skeleton.Line lines={3} />               多行文本
 *   <Skeleton.Card />                         卡片占位
 *   <Skeleton.Row cols={5} />                 表格行
 */
const base = 'inline-block bg-on-surface/[0.06] rounded animate-pulse';

const Skeleton = ({ w = '100%', h = 14, rounded = 'rounded', className = '' }) => (
  <span
    aria-hidden
    style={{ width: typeof w === 'number' ? `${w}px` : w, height: typeof h === 'number' ? `${h}px` : h }}
    className={`${base.replace('rounded', rounded)} ${className}`}
  />
);

Skeleton.Line = ({ lines = 3, className = '' }) => (
  <div className={`flex flex-col gap-2 ${className}`}>
    {Array.from({ length: lines }).map((_, i) => (
      <Skeleton key={i} h={12} w={i === lines - 1 ? '60%' : '100%'} />
    ))}
  </div>
);

Skeleton.Card = ({ className = '' }) => (
  <div className={`fl-card p-4 md:p-5 flex flex-col gap-3 min-h-[112px] ${className}`}>
    <Skeleton w={36} h={36} rounded="rounded-lg" />
    <div className="flex flex-col gap-2">
      <Skeleton w="60%" h={20} />
      <Skeleton w="40%" h={12} />
    </div>
  </div>
);

Skeleton.Row = ({ cols = 5 }) => (
  <tr aria-hidden>
    {Array.from({ length: cols }).map((_, i) => (
      <td key={i} className="px-3 py-3">
        <Skeleton w={i === 0 ? '60%' : '80%'} h={12} />
      </td>
    ))}
  </tr>
);

export default Skeleton;
