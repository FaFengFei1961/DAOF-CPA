import React from 'react';

/**
 * Switch — 开关组件
 * 38x22 轨道，h-9 总高对齐，aria-checked
 */
const Switch = React.forwardRef(({
  checked,
  onChange,
  disabled,
  className = '',
  ...props
}, ref) => {
  return (
    <div className={`h-9 flex items-center ${className}`}>
      <button
        ref={ref}
        type="button"
        role="switch"
        aria-checked={checked}
        disabled={disabled}
        onClick={(e) => {
          if (disabled) return;
          if (onChange) {
            onChange(!checked, e);
          }
        }}
        className={`
          relative inline-flex items-center w-[38px] h-[22px] rounded-full shrink-0 transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary focus-visible:ring-offset-2 focus-visible:ring-offset-surface
          ${disabled ? 'opacity-50 cursor-not-allowed' : 'cursor-pointer'}
          ${checked ? 'bg-primary' : 'bg-surface-container-highest border border-outline-variant'}
        `}
        {...props}
      >
        <span
          className={`
            inline-block w-[18px] h-[18px] transform rounded-full bg-on-primary transition-transform
            ${checked ? 'translate-x-[16px]' : 'translate-x-[2px] bg-outline'}
          `}
        />
      </button>
    </div>
  );
});

Switch.displayName = 'Switch';

export default Switch;
