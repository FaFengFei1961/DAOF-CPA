import React, { createContext, useContext, useState, useCallback, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { useModalA11y } from '../hooks/useModalA11y';

const ConfirmContext = createContext();

export const useConfirm = () => useContext(ConfirmContext);

/**
 * 全局确认/输入对话框。
 *
 * 调用方式：
 *   const ok = await confirm("确认删除？")                   // 字符串：纯确认
 *   const ok = await confirm({ title, message })             // 对象：自定义标题
 *   const ok = await confirm({ title, message, confirmText })// 自定义按钮
 *   const res = await confirm({ title, message, input: { label, placeholder, type:'number' } })
 *      → res === false 表示取消，否则 res = { value: '<用户输入>' }
 *   const ok = await confirm({ level: 'L3', title, message, impactCount, impactDetails, confirmPhrase, danger: true }) // L2/L3分级确认
 */
export const ConfirmProvider = ({ children }) => {
  const { t } = useTranslation();
  const [state, setState] = useState({
    isOpen: false,
    level: 'L1', // 'L1', 'L2', 'L3'
    title: '',
    message: '',
    confirmText: '',
    cancelText: '',
    danger: false,
    input: null, // { label, placeholder, type, defaultValue }
    inputValue: '',
    impactCount: 0,
    impactDetails: null, // []
    confirmPhrase: '',
    phraseInput: '',
    detailsExpanded: false,
    resolve: null,
  });

  const confirm = useCallback((arg) => {
    const opts = typeof arg === 'string' ? { message: arg } : (arg || {});
    return new Promise((resolve) => {
      setState({
        isOpen: true,
        level: opts.level || 'L1',
        title: opts.title || '',
        message: opts.message || '',
        confirmText: opts.confirmText || '',
        cancelText: opts.cancelText || '',
        danger: opts.danger !== false, // 默认使用危险操作样式
        input: opts.input || null,
        inputValue: opts.input?.defaultValue || '',
        impactCount: opts.impactCount || 0,
        impactDetails: opts.impactDetails || null,
        confirmPhrase: opts.confirmPhrase || '',
        phraseInput: '',
        detailsExpanded: false,
        resolve,
      });
    });
  }, []);

  const close = (result) => {
    if (state.resolve) state.resolve(result);
    setState((s) => ({ ...s, isOpen: false, resolve: null }));
  };

  const handleConfirm = () => {
    if (state.input) {
      close({ value: state.inputValue });
    } else {
      close(true);
    }
  };

  const handleCancel = () => close(false);

  const modalRef = useRef(null);
  const { onBackdropClick } = useModalA11y(state.isOpen, handleCancel, null, modalRef);

  const isConfirmDisabled = state.level === 'L3' && state.confirmPhrase && state.phraseInput !== state.confirmPhrase;

  return (
    <ConfirmContext.Provider value={confirm}>
      {children}
      {state.isOpen && (
        <div 
          className="fixed inset-0 z-[10000] flex items-end sm:items-center justify-center p-3 sm:p-4 bg-black/60 backdrop-blur-sm animate-in fade-in duration-200"
          onClick={onBackdropClick}
        >
          <div 
            ref={modalRef}
            className="bg-surface-container-high border border-outline-variant rounded-overlay w-full max-w-md p-5 sm:p-6 shadow-2xl relative flex flex-col scale-in-center max-h-screen overflow-y-auto"
          >
            <div className={`flex items-center gap-3 mb-4 shrink-0 ${state.danger ? 'text-error' : 'text-primary'}`}>
              <svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                <path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3Z" />
                <path d="M12 9v4" />
                <path d="M12 17h.01" />
              </svg>
              <h3 className="text-lg font-bold text-on-surface">
                {state.title || t('CONFIRM.TITLE', '请确认操作')}
              </h3>
            </div>
            
            <p className="text-on-surface-variant mb-4 text-sm leading-relaxed whitespace-pre-line shrink-0">
              {state.message}
            </p>

            {(state.level === 'L2' || state.level === 'L3') && state.impactCount > 0 && (
              <div className="mb-4 bg-error-container/20 border border-error/30 rounded-lg p-3 shrink-0">
                <div className="flex items-center justify-between text-sm font-semibold text-error">
                  <span>影响范围：{state.impactCount} 项</span>
                  {state.impactDetails && state.impactDetails.length > 0 && (
                    <button 
                      type="button"
                      className="text-xs underline hover:opacity-80"
                      onClick={() => setState(s => ({ ...s, detailsExpanded: !s.detailsExpanded }))}
                    >
                      {state.detailsExpanded ? '收起详情' : '查看详情'}
                    </button>
                  )}
                </div>
                {state.detailsExpanded && state.impactDetails && (
                  <div className="mt-2 pt-2 border-t border-error/20 text-xs text-on-surface-variant max-h-32 overflow-y-auto space-y-1">
                    {state.impactDetails.slice(0, 10).map((item, idx) => (
                      <div key={idx}>- {item}</div>
                    ))}
                    {state.impactDetails.length > 10 && (
                      <div className="text-error/70 italic mt-1">...还有 {state.impactDetails.length - 10} 项</div>
                    )}
                  </div>
                )}
              </div>
            )}

            {state.input && (
              <div className="mb-6 space-y-1.5 shrink-0">
                {state.input.label && (
                  <label className="text-xs font-semibold text-on-surface-variant block">{state.input.label}</label>
                )}
                <input
                  type={state.input.type || 'text'}
                  value={state.inputValue}
                  onChange={(e) => setState((s) => ({ ...s, inputValue: e.target.value }))}
                  placeholder={state.input.placeholder || ''}
                  className="w-full h-10 bg-surface-container border border-outline rounded-control px-3 text-sm text-on-surface focus:border-primary outline-none"
                  autoFocus
                />
              </div>
            )}

            {state.level === 'L3' && state.confirmPhrase && (
              <div className="mb-6 space-y-1.5 shrink-0">
                <label className="text-xs font-semibold text-error block">
                  请输入 <code className="bg-error/10 px-1 py-0.5 rounded select-all">{state.confirmPhrase}</code> 确认操作
                </label>
                <input
                  type="text"
                  value={state.phraseInput}
                  onChange={(e) => setState((s) => ({ ...s, phraseInput: e.target.value }))}
                  placeholder={state.confirmPhrase}
                  className="w-full h-10 bg-surface-container border border-error/50 rounded-control px-3 text-sm text-on-surface focus:border-error focus:ring-1 focus:ring-error outline-none"
                  autoFocus={!state.input}
                />
              </div>
            )}

            <div className="flex justify-end gap-2 mt-auto shrink-0">
              <button type="button" onClick={handleCancel} className="fl-btn fl-btn-standard">
                {state.cancelText || t('CONFIRM.CANCEL', '取消')}
              </button>
              <button
                type="button"
                onClick={handleConfirm}
                disabled={isConfirmDisabled}
                className="fl-btn disabled:opacity-50 disabled:cursor-not-allowed"
                style={
                  state.danger
                    ? { background: 'var(--color-error)', color: 'var(--color-on-error)' }
                    : { background: 'var(--color-primary)', color: 'var(--color-on-primary)' }
                }
              >
                {state.confirmText || t('CONFIRM.OK', '确认')}
              </button>
            </div>
          </div>
        </div>
      )}
    </ConfirmContext.Provider>
  );
};
