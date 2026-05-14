import { useState, useCallback } from 'react';

/**
 * useMaskState — 一组隐私字段的 show/hide 状态（Phase 6 拆出独立 hook）
 *
 * 拆分原因：原放在 pages/admin/system/_AdminFormPrimitives.jsx 跟组件混合
 * export，触发 react-refresh/only-export-components lint 错误。拆出后该文件
 * 仅 export 组件，hook 走独立 module。
 *
 * 用法：
 *   const [mask, toggle] = useMaskState();
 *   <input type={mask.foo ? 'text' : 'password'} />
 *   <button onClick={() => toggle('foo')}>toggle</button>
 */
export const useMaskState = () => {
  const [mask, setMask] = useState({});
  const toggle = useCallback((key) => {
    setMask(prev => ({ ...prev, [key]: !prev[key] }));
  }, []);
  return [mask, toggle];
};
