import React from 'react';
import { useTranslation } from 'react-i18next';
import { Save, Eye, EyeOff, KeyRound } from 'lucide-react';
import TextInput from '../../../components/ui/TextInput';

// useMaskState is imported directly by callers to keep Fast Refresh happy.

/**
 * Shared building blocks for admin form pages.
 */
export const SaveBar = ({ loading, onSave }) => {
  const { t } = useTranslation();
  return (
    <div className="flex items-center justify-end gap-6 mb-12">
      <button
        type="button"
        onClick={() => onSave()}
        disabled={loading}
        className="btn btn-primary flex items-center justify-center gap-2 disabled:opacity-50"
      >
        {loading ? t('COMMON.SAVING') : (
          <>
            <Save size={18} />
            {t('COMMON.SAVE')}
          </>
        )}
      </button>
    </div>
  );
};

export const SecretInputField = ({ label, id, val, onChange, show, onToggle, isPassword }) => {
  const inputId = `admin-input-${id}`;
  // Eye toggle 仅对密码类敏感字段才有意义。Client ID 等公开字段不该长一个无操作的眼睛图标。
  const showToggle = !!isPassword;
  return (
    <div className="flex flex-col gap-2">
      <label htmlFor={inputId} className="text-xs font-semibold text-on-surface-variant ml-1">{label}</label>
      <TextInput
        id={inputId}
        type={isPassword && !show ? 'password' : 'text'}
        value={val ?? ''}
        onChange={(e) => onChange(id, e.target.value)}
        placeholder="••••••••••••"
        prefix={KeyRound}
        suffix={showToggle ? (show ? EyeOff : Eye) : undefined}
        onSuffixClick={showToggle ? onToggle : undefined}
        suffixAriaLabel={showToggle ? (show ? 'Hide secret' : 'Show secret') : undefined}
        className="font-mono"
      />
    </div>
  );
};

/**
 * Section card with an accent rail and optional heading/subheading.
 */
export const SectionCard = ({ title, sub, accent = 'bg-primary text-on-primary', children, className = '' }) => (
  <div className={`card p-4 md:p-6 mb-6 w-full ${className}`}>
    {(title || sub) && (
      <header className="flex items-center gap-2 mb-6">
        <div className={`w-1 h-5 ${accent} rounded-r-control`} />
        <div>
          <h2 className="text-lg font-semibold text-on-surface">{title}</h2>
          {sub && <p className="text-xs text-on-surface-variant mt-0.5">{sub}</p>}
        </div>
      </header>
    )}
    {children}
  </div>
);
