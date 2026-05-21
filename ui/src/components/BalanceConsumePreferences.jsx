import React, { useState, useEffect, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { Wallet, Edit3 } from 'lucide-react';
import toast from 'react-hot-toast';
import { useConfirm } from '../context/ConfirmContext';
import { authFetch, isLoggedIn, readAuthState } from '../utils/authFetch';
import { isPageCacheFresh, readPageCache, writePageCache } from '../utils/pageCache';
import TextInput from './ui/TextInput';
import Select from './ui/Select';

// Unit conversion aligned with the backend 60s to 365d window.
const UNITS = [
  { id: 'second', secs: 1 },
  { id: 'minute', secs: 60 },
  { id: 'hour',   secs: 3600 },
  { id: 'day',    secs: 86400 },
  { id: 'month',  secs: 30 * 86400 },
];

const unitLabel = (id, t) => {
  switch (id) {
    case 'second': return t('BALANCE_PREF.UNIT_SECOND', '秒');
    case 'minute': return t('BALANCE_PREF.UNIT_MINUTE', '分钟');
    case 'hour': return t('BALANCE_PREF.UNIT_HOUR', '小时');
    case 'day': return t('BALANCE_PREF.UNIT_DAY', '天');
    case 'month': return t('BALANCE_PREF.UNIT_MONTH', '月');
    default: return id;
  }
};

// Split totalSeconds into the largest evenly divisible display unit.
function decomposeSeconds(total) {
  if (!total || total <= 0) return { value: 30, unit: 'day' };
  for (let i = UNITS.length - 1; i >= 0; i--) {
    if (total % UNITS[i].secs === 0) {
      return { value: total / UNITS[i].secs, unit: UNITS[i].id };
    }
  }
  return { value: total, unit: 'second' };
}

const MIN_WINDOW_SEC = 60;
const MAX_WINDOW_SEC = 365 * 86400;
const BALANCE_PREF_CACHE_TTL_MS = 30000;
const getBalancePrefCacheKey = () => {
  const { isAdmin, userToken } = readAuthState();
  return `balance-pref:${isAdmin ? 'admin' : userToken || 'guest'}`;
};

// User balance-spending controls. Spending falls back from subscription quota to cash balance only after opt-in.
const BalanceConsumePreferences = () => {
  const { t, i18n } = useTranslation();
  const confirm = useConfirm();
  const cacheKey = React.useMemo(getBalancePrefCacheKey, []);
  const [data, setData] = useState(() => readPageCache(cacheKey));
  const [loading, setLoading] = useState(() => !readPageCache(cacheKey));
  const [saving, setSaving] = useState(false);

  const load = useCallback(async ({ force = false } = {}) => {
    if (!isLoggedIn()) {
      setLoading(false);
      return;
    }
    const cached = readPageCache(cacheKey);
    if (cached) {
      setData(cached);
      setLoading(false);
      if (!force && isPageCacheFresh(cacheKey, BALANCE_PREF_CACHE_TTL_MS)) return;
    } else {
      setLoading(true);
    }
    try {
      const json = await authFetch('/api/balance-consume/preference');
      if (json.success && json.data) {
        writePageCache(cacheKey, json.data);
        setData(json.data);
      }
    } catch { /* keep the cached panel quiet on transient failures */ }
    finally { setLoading(false); }
  }, [cacheKey]);

  useEffect(() => { load(); }, [load]);

  const update = async (patch) => {
    setSaving(true);
    try {
      const json = await authFetch('/api/balance-consume/preference', {
        method: 'PUT',
        body: patch,
      });
      if (json.success) {
        if (json.data) {
          writePageCache(cacheKey, json.data);
          setData(json.data);
        }
        toast.success(t('BALANCE_PREF.SAVE_OK', '已保存'));
        // The balance may change after a window reset, so refresh the top bar.
        window.dispatchEvent(new CustomEvent('user-profile-refresh'));
      } else {
        toast.error(json.message || t('BALANCE_PREF.SAVE_FAIL', '保存失败'));
      }
    } catch {
      toast.error(t('BALANCE_PREF.SAVE_FAIL', '保存失败'));
    } finally {
      setSaving(false);
    }
  };

  const handleAdjustLimit = async () => {
    const res = await confirm({
      title: t('BALANCE_PREF.ADJUST_LIMIT', '调整限额'),
      message: t('BALANCE_PREF.LIMIT_HINT_LONG', '设置本周期最多可消费多少美元（0 = 不限）'),
      confirmText: t('BALANCE_PREF.SAVE', '保存'),
      input: {
        label: t('BALANCE_PREF.LIMIT_LABEL', '本周期消费上限（USD）'),
        type: 'number',
        defaultValue: data?.limit_usd != null ? String(data.limit_usd) : '',
        placeholder: '0',
      },
    });
    if (!res) return;
    const v = parseFloat(String(res.value || '').trim() || '0');
    if (isNaN(v) || v < 0) {
      toast.error(t('BALANCE_PREF.LIMIT_INVALID', '限额必须 ≥ 0'));
      return;
    }
    update({ limit_usd: v });
  };

  if (loading) {
    return <div className="text-sm text-on-surface-variant py-4">{t('SYSTEM.LOADING', '加载中...')}</div>;
  }
  if (!data) return null;

  const limitUSD = Number(data.limit_usd) || 0;
  const consumed = Number(data.consumed_in_window) || 0;
  const percent = limitUSD > 0 ? Math.min(100, (consumed / limitUSD) * 100) : 0;
  const resetsAt = data.resets_at ? new Date(data.resets_at).toLocaleDateString(i18n.resolvedLanguage || i18n.language) : '-';

  return (
    <section className="space-y-4 py-4">
      <header className="flex items-center gap-3">
        <Wallet size={20} className="text-primary" />
        <div>
          <h3 className="text-base font-semibold text-on-surface">
            {t('BALANCE_PREF.TITLE', '余额消费控制')}
          </h3>
          <p className="text-xs text-on-surface-variant mt-0.5">
            {t('BALANCE_PREF.DESC', '订阅用尽后，是否允许从美元余额继续扣费')}
          </p>
        </div>
      </header>

      {/* Master switch */}
      <div className="flex items-center justify-between card p-4">
        <div className="flex-1 min-w-0">
          <div id="balance-consume-enable-label" className="text-sm font-semibold text-on-surface">{t('BALANCE_PREF.ENABLED', '允许余额消费')}</div>
          <div className="text-[11px] text-on-surface-variant mt-0.5">
            {data.enabled
              ? t('BALANCE_PREF.ENABLED_ON', '订阅用尽后自动从余额扣费')
              : t('BALANCE_PREF.ENABLED_OFF', '订阅用尽后请求将被拒绝（402）')}
          </div>
        </div>
        <button
          type="button"
          role="switch"
          aria-checked={data.enabled}
          aria-labelledby="balance-consume-enable-label"
          disabled={saving}
          onClick={() => update({ enabled: !data.enabled })}
          className={`relative shrink-0 w-12 h-6 rounded-full transition disabled:opacity-50 ${data.enabled ? 'bg-primary' : 'bg-on-surface/20'}`}
        >
          <span className={`absolute top-0.5 w-5 h-5 rounded-full bg-white transition-all ${data.enabled ? 'left-6' : 'left-0.5'}`} />
        </button>
      </div>

      {/* Limit and progress */}
      <div className="card p-4 space-y-3">
        <div className="flex items-center justify-between">
          <div>
            <div className="text-xs text-on-surface-variant">
              {t('BALANCE_PREF.SPENT_LABEL', '本周期已消费')}
            </div>
            <div className="text-base font-semibold text-on-surface mt-0.5">
              ${consumed.toFixed(2)}
              {limitUSD > 0 && <span className="text-on-surface-variant text-sm"> / ${limitUSD.toFixed(2)}</span>}
            </div>
          </div>
          <button
            type="button"
            onClick={handleAdjustLimit}
            disabled={saving}
            className="h-8 px-3 bg-surface-container-high border border-outline-variant rounded-control text-xs hover:bg-on-surface/[0.04] flex items-center gap-1 disabled:opacity-50"
          >
            <Edit3 size={12} />
            {t('BALANCE_PREF.ADJUST_LIMIT', '调整限额')}
          </button>
        </div>

        {limitUSD > 0 ? (
          <div className="h-2 rounded-full bg-on-surface/10 overflow-hidden">
            <div className={`h-full transition-all ${percent >= 90 ? 'bg-error' : percent >= 70 ? 'bg-warning' : 'bg-primary'}`}
              style={{ width: `${percent}%` }} />
          </div>
        ) : (
          <div className="text-[11px] text-on-surface-variant">
            {t('BALANCE_PREF.LIMIT_HINT', '0 = 不限')}
          </div>
        )}

        <div className="text-[11px] text-on-surface-variant">
          {t('BALANCE_PREF.RESETS_AT', { date: resetsAt, defaultValue: '下次重置时间：{{date}}' })}
        </div>
      </div>

      {/* Window editor */}
      <WindowEditor
        currentSeconds={data.window_seconds}
        saving={saving}
        onSave={(secs) => update({ window_seconds: secs })}
        t={t}
      />

      {/* Current balance is already shown in the top bar. */}
    </section>
  );
};

// WindowEditor customizes the reset period with a numeric value and unit selector.
const WindowEditor = ({ currentSeconds, saving, onSave, t }) => {
  const initial = decomposeSeconds(currentSeconds);
  const [value, setValue] = useState(initial.value);
  const [unit, setUnit] = useState(initial.unit);

  // Keep local editor state in sync after a successful refresh.
  useEffect(() => {
    const d = decomposeSeconds(currentSeconds);
    setValue(d.value);
    setUnit(d.unit);
  }, [currentSeconds]);

  const unitDef = UNITS.find(u => u.id === unit) || UNITS[3];
  const totalSeconds = Math.round(Number(value) * unitDef.secs);
  const valid = totalSeconds >= MIN_WINDOW_SEC && totalSeconds <= MAX_WINDOW_SEC;
  const dirty = totalSeconds !== currentSeconds;

  const handleApply = () => {
    if (!valid || !dirty) return;
    onSave(totalSeconds);
  };

  return (
    <div className="rounded-overlay border border-outline-variant bg-surface-container p-4 space-y-3">
      <div>
        <div className="text-xs font-semibold text-on-surface-variant">
          {t('BALANCE_PREF.WINDOW_LABEL', '重置周期')}
        </div>
        <div className="text-[11px] text-on-surface-variant mt-0.5">
          {t('BALANCE_PREF.WINDOW_HINT', { secs: currentSeconds, defaultValue: '当前 {{secs}} 秒（最短 60 秒，最长 365 天）' })}
        </div>
      </div>

      <div className="flex items-center gap-2">
        <div className="w-32">
          <TextInput
            type="number"
            min={1}
            step={1}
            value={value}
            onChange={e => setValue(e.target.value)}
            className="font-mono"
          />
        </div>
        <div className="w-28">
          <Select
            value={unit}
            onChange={e => setUnit(e.target.value)}
            options={UNITS.map(u => ({ value: u.id, label: unitLabel(u.id, t) }))}
          />
        </div>
        <button
          type="button"
          onClick={handleApply}
          disabled={saving || !valid || !dirty}
          className="h-9 px-4 bg-primary text-on-primary rounded-control text-sm font-semibold hover:opacity-90 disabled:opacity-40"
        >
          {saving ? t('BALANCE_PREF.SAVING', '保存中...') : t('BALANCE_PREF.APPLY', '应用')}
        </button>
      </div>

      {!valid && (
        <div className="text-[11px] text-error">
          {t('BALANCE_PREF.WINDOW_OUT_OF_RANGE', '范围必须在 60 秒到 365 天之间')}
        </div>
      )}
      <p className="text-[11px] text-on-surface-variant">
        {t('BALANCE_PREF.WINDOW_RESET_HINT', '修改周期会立即重置当前已消费计数')}
      </p>
    </div>
  );
};

export default BalanceConsumePreferences;
