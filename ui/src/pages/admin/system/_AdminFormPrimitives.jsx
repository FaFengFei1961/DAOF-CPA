import React from 'react';
import { useTranslation } from 'react-i18next';
import { Save, Eye, EyeOff, KeyRound } from 'lucide-react';
import TextInput from '../../../components/ui/TextInput';

// Phase 6：useMaskState 已拆到 hooks/useMaskState.js — 调用方直接 import 那个文件，
// 不再通过此文件中转（避免 react-refresh/only-export-components）。

/**
 * AdminFormPrimitives — admin form 通用 building blocks（Phase 3）
 *
 * 抽自 Settings.jsx 的内联 SaveBar / InputField，给所有 admin form page 复用。
 * 视觉规则不变，跟原 Settings 内一致。
 */
export const SaveBar = ({ loading, onSave }) => {
  const { t } = useTranslation();
  return (
    <div className="flex items-center justify-end gap-6 mb-12">
      <button
        type="button"
        onClick={() => onSave()}
        disabled={loading}
        className="fl-btn fl-btn-prominent flex items-center justify-center gap-2 disabled:opacity-50"
      >
        {loading ? t('SETTINGS.BTN_SAVING', '保存中…') : (
          <>
            <Save size={18} />
            {t('SETTINGS.BTN_SAVE', '保存')}
          </>
        )}
      </button>
    </div>
  );
};

export const SecretInputField = ({ label, id, val, onChange, show, onToggle, isPassword }) => {
  const inputId = `admin-input-${id}`;
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
        suffix={show ? EyeOff : Eye}
        onSuffixClick={onToggle}
        className="font-mono"
      />
    </div>
  );
};

/**
 * SectionCard — admin form 子区块卡片（带左侧色条 + 标题）
 * 取代 Settings.jsx 内重复的 `<div className="bg-surface-container border... rounded-overlay p-6">` 模板
 */
export const SectionCard = ({ title, sub, accent = 'bg-primary text-on-primary', children, className = '' }) => (
  <div className={`fl-card p-4 md:p-6 mb-6 w-full ${className}`}>
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

