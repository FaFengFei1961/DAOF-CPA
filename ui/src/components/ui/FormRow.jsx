import React from 'react';

/**
 * FormRow — 表单行（Phase 1，gemini ccg 推荐 #4 优先级）
 *
 * 取代 Settings.jsx / ContentModerationGlobals.jsx 内裸 <input>/<select> 满屏的状态。
 * 视觉规则（Win11 Settings 一致）：
 *  - 桌面：左 label/hint 占 ~2/3，右 input 占 ~1/3，中间 gap-4
 *  - 移动：上下堆叠，input 100%
 *  - 行间用 border-b 分隔（最后一行 `last` prop 关掉）
 *  - 必填用红色 *；hint 用 caption2 (10px) muted
 */
const FormRow = ({
  label,
  hint,
  required,
  htmlFor,
  children,
  last = false,
  className = '',
}) => (
  <div className={`flex flex-col md:flex-row md:items-center justify-between gap-3 py-3 ${last ? '' : 'border-b border-outline-variant/20'} ${className}`}>
    <div className="flex flex-col gap-1 w-full md:w-2/3 min-w-0">
      <label htmlFor={htmlFor} className="text-on-surface font-medium text-sm flex items-center gap-1">
        {label}
        {required && <span className="text-red-400 text-xs">*</span>}
      </label>
      {hint && (
        <span className="text-xs text-on-surface-variant leading-relaxed max-w-2xl">{hint}</span>
      )}
    </div>
    <div className="w-full md:w-auto md:min-w-[200px] md:max-w-md flex justify-start md:justify-end">
      {children}
    </div>
  </div>
);

/**
 * FormGroup — 一组相关 FormRow 的卡片包装
 */
FormRow.Group = ({ title, sub, children, className = '' }) => (
  <section className={`fl-card p-4 md:p-6 ${className}`}>
    {(title || sub) && (
      <header className="mb-4 pb-4 border-b border-outline-variant/30">
        {title && <h3 className="text-sm font-semibold text-on-surface">{title}</h3>}
        {sub && <p className="text-xs text-on-surface-variant mt-1">{sub}</p>}
      </header>
    )}
    {children}
  </section>
);

export default FormRow;
