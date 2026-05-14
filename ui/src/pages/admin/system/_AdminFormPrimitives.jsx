import React from 'react';
import { useTranslation } from 'react-i18next';
import { Save, Eye, EyeOff, KeyRound } from 'lucide-react';

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
        className="h-11 px-6 bg-primary text-on-primary hover:bg-primary-container hover:text-on-primary-container font-medium rounded-xl flex items-center justify-center gap-2 shadow-[0_0_15px_rgba(37,99,235,0.2)] disabled:opacity-50"
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
      <div className="relative group">
        <div className="absolute inset-y-0 left-0 pl-3 flex items-center pointer-events-none">
          <KeyRound size={16} className="text-on-surface-variant" />
        </div>
        <input
          id={inputId}
          type={isPassword && !show ? 'password' : 'text'}
          value={val ?? ''}
          onChange={(e) => onChange(id, e.target.value)}
          placeholder="••••••••••••"
          className="w-full h-11 bg-surface-container-high border border-outline group-hover:border-primary/50 rounded-lg pl-10 pr-10 text-sm text-on-surface outline-none focus:border-primary font-mono placeholder:text-on-surface-variant/50"
        />
        <button
          type="button"
          onClick={onToggle}
          aria-label={show ? '隐藏' : '显示'}
          className="absolute inset-y-0 right-0 pr-3 flex items-center text-on-surface-variant hover:text-white"
        >
          {show ? <EyeOff size={16} /> : <Eye size={16} />}
        </button>
      </div>
    </div>
  );
};

/**
 * SectionCard — admin form 子区块卡片（带左侧色条 + 标题）
 * 取代 Settings.jsx 内重复的 `<div className="bg-surface-container border... rounded-2xl p-6">` 模板
 */
export const SectionCard = ({ title, sub, accent = 'bg-primary text-on-primary', children, className = '' }) => (
  <div className={`bg-surface-container border border-outline-variant rounded-2xl p-4 md:p-6 mb-8 shadow-sm w-full ${className}`}>
    {(title || sub) && (
      <header className="flex items-center gap-2 mb-6">
        <div className={`w-1 h-5 ${accent} rounded-r-md`} />
        <div>
          <h2 className="text-lg font-semibold text-on-surface">{title}</h2>
          {sub && <p className="text-xs text-on-surface-variant mt-0.5">{sub}</p>}
        </div>
      </header>
    )}
    {children}
  </div>
);

