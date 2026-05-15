import React from 'react';

/**
 * Select — 原生 select 包装
 * h-9, rounded-control, border-outline-variant, focus:border-primary
 */
const Select = React.forwardRef(({
  value,
  onChange,
  options = [],
  disabled,
  error,
  className = '',
  ...props
}, ref) => {
  return (
    <div className={`flex flex-col gap-1 w-full ${className}`}>
      <div className="relative flex items-center w-full">
        <select
          ref={ref}
          value={value}
          onChange={onChange}
          disabled={disabled}
          className={`
            w-full h-9 bg-surface-container border appearance-none
            ${error ? 'border-error focus:border-error' : 'border-outline-variant focus:border-primary'}
            rounded-control text-sm text-on-surface outline-none transition-colors px-3 pr-8
            ${disabled ? 'opacity-60 cursor-not-allowed bg-surface-container-high' : ''}
          `}
          {...props}
        >
          {options.map((opt) => (
            <option key={opt.value} value={opt.value} disabled={opt.disabled}>
              {opt.label}
            </option>
          ))}
        </select>
        <div className="absolute right-3 flex items-center justify-center text-on-surface-variant pointer-events-none">
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
            <polyline points="6 9 12 15 18 9"></polyline>
          </svg>
        </div>
      </div>
      {error && (
        <span className="text-xs text-error">{error}</span>
      )}
    </div>
  );
});

Select.displayName = 'Select';

export default Select;
