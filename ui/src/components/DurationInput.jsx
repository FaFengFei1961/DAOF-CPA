import React, { useRef, useState, useEffect } from 'react';

// 单位 → 秒（30 天 = 月，按习惯定义；7 天 = 周）
const UNITS = [
  { key: 's', label: '秒', sec: 1 },
  { key: 'm', label: '分', sec: 60 },
  { key: 'h', label: '小时', sec: 3600 },
  { key: 'd', label: '天', sec: 86400 },
  { key: 'w', label: '周', sec: 604800 },
  { key: 'mo', label: '月', sec: 2592000 },
];

// 最大允许秒数：~ 100 年，防极端值导致 NaN/Infinity
const MAX_SECONDS = 100 * 365 * 86400;

// 自动选最合适的单位（能整除的最大单位）
const pickUnitForSec = (totalSec) => {
  if (!totalSec || totalSec <= 0) return 's';
  for (let i = UNITS.length - 1; i >= 0; i--) {
    if (totalSec % UNITS[i].sec === 0) return UNITS[i].key;
  }
  return 's';
};

const unitMeta = (key) => UNITS.find((u) => u.key === key) || UNITS[0];

/**
 * 通用周期/时长输入：值始终以秒（整数）存储，UI 提供数字 + 单位下拉。
 *
 * 关键设计：以 **秒数** 为权威，单位切换不重新乘除（消除浮点累积误差）。
 *
 * Props:
 *   value          当前秒数（受控）
 *   onChange(sec)  回调返回新的秒数
 *   className      传入到 number input 的样式
 *   selectClass    传入到 select 的样式（默认与 input 一致风格）
 *   allowZero      是否允许 0（用于"套餐周期内累计"语义）
 */
const DurationInput = ({ value, onChange, className = '', selectClass = '', allowZero = false }) => {
  const totalSec = Math.max(0, Math.min(MAX_SECONDS, Math.floor(Number(value) || 0)));

  // 单位由用户选择（受控状态），否则按权威秒数自动推断
  const [unitKey, setUnitKey] = useState(() => pickUnitForSec(totalSec));
  // 记录权威秒数变化时刷新单位（外部传入新 value）
  const lastSecRef = useRef(totalSec);
  useEffect(() => {
    if (totalSec !== lastSecRef.current) {
      lastSecRef.current = totalSec;
      // 仅当当前展示单位无法整除时才重选
      const meta = unitMeta(unitKey);
      if (totalSec % meta.sec !== 0) {
        setUnitKey(pickUnitForSec(totalSec));
      }
    }
  }, [totalSec, unitKey]);

  const meta = unitMeta(unitKey);
  // 显示值用 6 位精度展示，但运算从不依赖它
  const displayValue =
    totalSec === 0 ? 0 : Number((totalSec / meta.sec).toFixed(6));

  const baseInputCls =
    'w-full rounded-lg bg-surface border border-outline-variant text-on-surface text-sm px-3 py-2 focus:outline-none focus:border-primary';

  const handleNumberChange = (raw) => {
    if (raw === '' || raw === '-') {
      onChange(0);
      return;
    }
    const num = parseFloat(raw);
    if (!Number.isFinite(num) || num < 0) return;
    const newSec = Math.max(0, Math.min(MAX_SECONDS, Math.round(num * meta.sec)));
    if (!allowZero && newSec === 0) {
      onChange(meta.sec); // 不允许 0 时，至少一个最小单位
      return;
    }
    onChange(newSec);
  };

  const handleUnitChange = (newKey) => {
    // 单位切换：只改展示单位，不重新计算秒数（避免精度损失）
    setUnitKey(newKey);
  };

  return (
    <div className="flex w-full min-w-0 gap-1">
      <input
        type="number"
        inputMode="numeric"
        min={allowZero ? 0 : 1}
        step="1"
        max={MAX_SECONDS}
        className={`${className || baseInputCls} flex-1 min-w-0`}
        value={displayValue}
        onChange={(e) => handleNumberChange(e.target.value)}
      />
      <select
        aria-label="单位"
        className={`${selectClass || baseInputCls} w-14 sm:w-16 shrink-0 px-1`}
        value={unitKey}
        onChange={(e) => handleUnitChange(e.target.value)}
      >
        {UNITS.map((u) => (
          <option key={u.key} value={u.key}>
            {u.label}
          </option>
        ))}
      </select>
    </div>
  );
};

// 把秒数格式化为人类可读，如 "30 天" / "12 小时"
// eslint-disable-next-line react-refresh/only-export-components
export const formatDuration = (sec) => {
  const n = Math.floor(Number(sec) || 0);
  if (n <= 0) return '0';
  for (let i = UNITS.length - 1; i >= 0; i--) {
    if (n % UNITS[i].sec === 0) return `${n / UNITS[i].sec} ${UNITS[i].label}`;
  }
  return `${n} 秒`;
};

export default DurationInput;
