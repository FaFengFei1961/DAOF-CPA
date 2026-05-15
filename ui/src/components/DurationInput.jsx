import React, { useRef, useState, useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import i18n from '../i18n';

// Unit to seconds. Month is intentionally normalized to 30 days.
const UNITS = [
  { key: 's', sec: 1 },
  { key: 'm', sec: 60 },
  { key: 'h', sec: 3600 },
  { key: 'd', sec: 86400 },
  { key: 'w', sec: 604800 },
  { key: 'mo', sec: 2592000 },
];

// Roughly 100 years; prevents extreme values from producing NaN or Infinity.
const MAX_SECONDS = 100 * 365 * 86400;

// Pick the largest unit that evenly divides the authoritative second value.
const pickUnitForSec = (totalSec) => {
  if (!totalSec || totalSec <= 0) return 's';
  for (let i = UNITS.length - 1; i >= 0; i--) {
    if (totalSec % UNITS[i].sec === 0) return UNITS[i].key;
  }
  return 's';
};

const unitMeta = (key) => UNITS.find((u) => u.key === key) || UNITS[0];

const durationSelectLabel = (key, t) => {
  switch (key) {
    case 's': return t('DURATION.UNIT_SECOND_SELECT', '秒');
    case 'm': return t('DURATION.UNIT_MINUTE_SELECT', '分');
    case 'h': return t('DURATION.UNIT_HOUR_SELECT', '小时');
    case 'd': return t('DURATION.UNIT_DAY_SELECT', '天');
    case 'w': return t('DURATION.UNIT_WEEK_SELECT', '周');
    case 'mo': return t('DURATION.UNIT_MONTH_SELECT', '月');
    default: return key;
  }
};

const durationValueLabel = (key, value, t) => {
  const singular = Number(value) === 1;
  switch (key) {
    case 's':
      return singular ? t('DURATION.UNIT_SECOND_ONE', '秒') : t('DURATION.UNIT_SECOND_OTHER', '秒');
    case 'm':
      return singular ? t('DURATION.UNIT_MINUTE_ONE', '分钟') : t('DURATION.UNIT_MINUTE_OTHER', '分钟');
    case 'h':
      return singular ? t('DURATION.UNIT_HOUR_ONE', '小时') : t('DURATION.UNIT_HOUR_OTHER', '小时');
    case 'd':
      return singular ? t('DURATION.UNIT_DAY_ONE', '天') : t('DURATION.UNIT_DAY_OTHER', '天');
    case 'w':
      return singular ? t('DURATION.UNIT_WEEK_ONE', '周') : t('DURATION.UNIT_WEEK_OTHER', '周');
    case 'mo':
      return singular ? t('DURATION.UNIT_MONTH_ONE', '月') : t('DURATION.UNIT_MONTH_OTHER', '月');
    default:
      return key;
  }
};

/**
 * Generic duration input. The controlled value is always stored as integer seconds.
 *
 * The second value is authoritative; switching units only changes presentation.
 *
 * Props:
 *   value          controlled seconds
 *   onChange(sec)  callback with the next second value
 *   className      number input className
 *   selectClass    select className
 *   allowZero      whether zero is valid
 */
const DurationInput = ({ value, onChange, className = '', selectClass = '', allowZero = false }) => {
  const { t } = useTranslation();
  const totalSec = Math.max(0, Math.min(MAX_SECONDS, Math.floor(Number(value) || 0)));

  // User-selected display unit, initialized from the authoritative seconds.
  const [unitKey, setUnitKey] = useState(() => pickUnitForSec(totalSec));
  // Refresh the display unit only when an external value change makes it invalid.
  const lastSecRef = useRef(totalSec);
  useEffect(() => {
    if (totalSec !== lastSecRef.current) {
      lastSecRef.current = totalSec;
      const meta = unitMeta(unitKey);
      if (totalSec % meta.sec !== 0) {
        setUnitKey(pickUnitForSec(totalSec));
      }
    }
  }, [totalSec, unitKey]);

  const meta = unitMeta(unitKey);
  // Display at six-digit precision; calculations never depend on this string.
  const displayValue =
    totalSec === 0 ? 0 : Number((totalSec / meta.sec).toFixed(6));

  const baseInputCls =
    'w-full rounded-control bg-surface border border-outline-variant text-on-surface text-sm px-3 py-2 focus:outline-none focus:border-primary';

  const handleNumberChange = (raw) => {
    if (raw === '' || raw === '-') {
      onChange(0);
      return;
    }
    const num = parseFloat(raw);
    if (!Number.isFinite(num) || num < 0) return;
    const newSec = Math.max(0, Math.min(MAX_SECONDS, Math.round(num * meta.sec)));
    if (!allowZero && newSec === 0) {
      onChange(meta.sec);
      return;
    }
    onChange(newSec);
  };

  const handleUnitChange = (newKey) => {
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
        aria-label={t('DURATION.UNIT_ARIA', '单位')}
        className={`${selectClass || baseInputCls} w-14 sm:w-16 shrink-0 px-1`}
        value={unitKey}
        onChange={(e) => handleUnitChange(e.target.value)}
      >
        {UNITS.map((u) => (
          <option key={u.key} value={u.key}>
            {durationSelectLabel(u.key, t)}
          </option>
        ))}
      </select>
    </div>
  );
};

// Format seconds into a localized human-readable duration.
// eslint-disable-next-line react-refresh/only-export-components
export const formatDuration = (sec) => {
  const n = Math.floor(Number(sec) || 0);
  if (n <= 0) return '0';
  const t = i18n.t.bind(i18n);
  for (let i = UNITS.length - 1; i >= 0; i--) {
    if (n % UNITS[i].sec === 0) {
      const value = n / UNITS[i].sec;
      return t('DURATION.VALUE', {
        value,
        unit: durationValueLabel(UNITS[i].key, value, t),
        defaultValue: '{{value}} {{unit}}',
      });
    }
  }
  return t('DURATION.VALUE', {
    value: n,
    unit: durationValueLabel('s', n, t),
    defaultValue: '{{value}} {{unit}}',
  });
};

export default DurationInput;
