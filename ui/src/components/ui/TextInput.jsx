import React from 'react';

/**
 * TextInput — 统一输入框
 * h-9, rounded-control, border-outline-variant, focus:border-primary
 * 支持 prefix/suffix icon, type, placeholder, error 提示
 */
const TextInput = React.forwardRef(({
  type = 'text',
  placeholder,
  value,
  onChange,
  disabled,
  error,
  prefix: PrefixIcon,
  suffix: SuffixIcon,
  onSuffixClick,
  suffixAriaLabel,
  className = '',
  ...props
}, ref) => {
  return (
    <div className={`flex flex-col gap-1 w-full ${className}`}>
      <div className="relative flex items-center w-full">
        {PrefixIcon && (
          <div className="absolute left-3 flex items-center justify-center text-on-surface-variant pointer-events-none">
            <PrefixIcon size={16} />
          </div>
        )}
        <input
          ref={ref}
          type={type}
          placeholder={placeholder}
          value={value}
          onChange={onChange}
          disabled={disabled}
          className={`
            w-full h-9 bg-surface-container border
            ${error ? 'border-error focus:border-error' : 'border-outline-variant focus:border-primary'}
            rounded-control text-sm text-on-surface placeholder:text-on-surface-variant/50 outline-none transition-colors
            ${PrefixIcon ? 'pl-9' : 'pl-3'}
            ${SuffixIcon ? 'pr-9' : 'pr-3'}
            ${disabled ? 'opacity-60 cursor-not-allowed bg-surface-container-high' : ''}
          `}
          {...props}
        />
        {SuffixIcon && (
          onSuffixClick ? (
            <button
              type="button"
              onClick={onSuffixClick}
              disabled={disabled}
              aria-label={suffixAriaLabel}
              className="absolute right-2 w-7 h-7 flex items-center justify-center rounded-control text-on-surface-variant hover:text-on-surface hover:bg-on-surface/[0.06] focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary disabled:opacity-50 disabled:cursor-not-allowed"
            >
              <SuffixIcon size={16} />
            </button>
          ) : (
            <div className="absolute right-3 flex items-center justify-center text-on-surface-variant pointer-events-none">
              <SuffixIcon size={16} />
            </div>
          )
        )}
      </div>
      {error && (
        <span className="text-xs text-error">{error}</span>
      )}
    </div>
  );
});

TextInput.displayName = 'TextInput';

export default TextInput;
