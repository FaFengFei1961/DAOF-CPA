import React from 'react';

/**
 * ProgressBar — 进度条
 * h-2 默认/h-1 thin，可选 label，使用 primary token
 */
const ProgressBar = ({
  value,
  max = 100,
  thin = false,
  label,
  className = '',
}) => {
  const percentage = Math.min(Math.max((value / max) * 100, 0), 100);
  const heightClass = thin ? 'h-1' : 'h-2';

  return (
    <div className={`w-full flex flex-col gap-1.5 ${className}`}>
      {label && (
        <div className="flex items-center justify-between text-xs text-on-surface-variant">
          <span>{label}</span>
          <span className="tabular-nums font-medium text-on-surface">{Math.round(percentage)}%</span>
        </div>
      )}
      <div className={`w-full bg-surface-container-highest rounded-full overflow-hidden ${heightClass}`}>
        <div
          className="bg-primary h-full rounded-full transition-all duration-300 ease-in-out"
          style={{ width: `${percentage}%` }}
        />
      </div>
    </div>
  );
};

export default ProgressBar;
