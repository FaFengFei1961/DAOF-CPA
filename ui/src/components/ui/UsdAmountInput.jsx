import React, { useEffect, useState } from 'react';
import TextInput from './TextInput';

/**
 * UsdAmountInput — admin 金额输入框（USD 显示 / micro_usd 整数持久化）。
 *
 * 解决"金额单位错位"bug：后端约定金额走 int64 micro_usd（docs/coding-conventions.md §2.2，
 * 禁 float64 / 禁 ParseFloat）；admin 输入框习惯 USD 浮点。如果直接 onChange 原样字符串
 * 写后端，后端 ParseInt("1") → 1 micro = $0.000001（金额错 1e6 倍）。
 *
 * 本组件统一管道：
 *   - 编辑期间 local state 持本地 USD 字符串（保留 ".0" 这种过渡形态，不被吞）
 *   - 失焦（onBlur）一次性 USD float → Math.round × 1e6 → micro 整数字符串写上层 configs
 *   - 外部 microValue 变化（configs reload）时同步 local
 *
 * 关键不变量：写到上层 onMicroChange 的永远是 **micro 整数字符串**或 ''（清空）。
 */

const MICRO_PER_USD = 1_000_000;

// fix P2（codex review verify-1）：react-refresh/only-export-components 规则禁止 component file
// 同时 export 非组件 helper（HMR 边界会被破坏）。helper 仅在本文件使用，去掉 export 即可。
const microStringToUsdDisplay = (micro) => {
  if (micro == null || micro === '') return '';
  const n = parseInt(micro, 10);
  if (!Number.isFinite(n)) return '';
  // 用字符串拼接避免 0.1 + 0.2 这类 float 漂移；6 位小数覆盖 1 micro 精度
  const sign = n < 0 ? '-' : '';
  const abs = Math.abs(n);
  const whole = Math.floor(abs / MICRO_PER_USD);
  const frac = abs % MICRO_PER_USD;
  if (frac === 0) return `${sign}${whole}`;
  const fracStr = String(frac).padStart(6, '0').replace(/0+$/, '');
  return `${sign}${whole}.${fracStr}`;
};

const usdInputToMicroString = (raw) => {
  const s = String(raw ?? '').trim();
  if (s === '') return '';
  const usd = parseFloat(s);
  if (!Number.isFinite(usd) || usd < 0) return '';
  // 唯一 float 接触点：立刻 Math.round 整数化，之后全链路 int64 micro_usd 路径，零漂移
  return String(Math.round(usd * MICRO_PER_USD));
};

const UsdAmountInput = ({
  microValue,
  microDefault = '0',
  onMicroChange,
  placeholder,
  widthClass = 'w-full md:w-32',
  ...rest
}) => {
  const initialDisplay = () => {
    const raw = (microValue != null && microValue !== '') ? microValue : microDefault;
    return microStringToUsdDisplay(raw);
  };
  const [local, setLocal] = useState(initialDisplay);

  useEffect(() => {
    setLocal(microStringToUsdDisplay(
      (microValue != null && microValue !== '') ? microValue : microDefault,
    ));
  }, [microValue, microDefault]);

  return (
    <div className={`relative ${widthClass}`}>
      <span className="absolute left-3 top-2.5 text-on-surface-variant text-sm pointer-events-none z-10">$</span>
      <TextInput
        type="number"
        step="0.000001"
        min="0"
        value={local}
        onChange={(e) => setLocal(e.target.value)}
        onBlur={(e) => {
          const micro = usdInputToMicroString(e.target.value);
          onMicroChange(micro);
          // 重新格式化显示（"1.00" → "1"）
          setLocal(microStringToUsdDisplay(micro || microDefault));
        }}
        placeholder={placeholder}
        className="pl-7 text-right"
        {...rest}
      />
    </div>
  );
};

export default UsdAmountInput;
