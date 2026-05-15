import React, { useState, useEffect, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { Wallet, Edit3 } from 'lucide-react';
import toast from 'react-hot-toast';
import { useConfirm } from '../context/ConfirmContext';
import { authFetch, isLoggedIn, readAuthState } from '../utils/authFetch';
import { isPageCacheFresh, readPageCache, writePageCache } from '../utils/pageCache';

// 单位换算（与后端 60s ~ 365d 范围对齐）
const UNITS = [
  { id: 'second', label: '秒', secs: 1 },
  { id: 'minute', label: '分钟', secs: 60 },
  { id: 'hour',   label: '小时', secs: 3600 },
  { id: 'day',    label: '天',   secs: 86400 },
  { id: 'month',  label: '月',   secs: 30 * 86400 }, // 1 月 = 30 天近似
];

// 把 totalSeconds 拆成最大可整除的 (value, unit)
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

// 用户余额消费控制（参照 Claude Extra usage 面板）
// Phase 8：两段消费 — 订阅 → 余额（默认关闭，需在此开启 + 限额）
const BalanceConsumePreferences = () => {
  const { t } = useTranslation();
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
    } catch { /* 静默 */ }
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
        toast.success(t('BALANCE_CONSUME.SAVE_OK', '已保存'));
        // 余额可能因窗口重置变化，触发顶栏刷新
        window.dispatchEvent(new CustomEvent('user-profile-refresh'));
      } else {
        toast.error(json.message || t('BALANCE_CONSUME.SAVE_FAIL', '保存失败'));
      }
    } catch {
      toast.error(t('BALANCE_CONSUME.SAVE_FAIL', '保存失败'));
    } finally {
      setSaving(false);
    }
  };

  const handleAdjustLimit = async () => {
    const res = await confirm({
      title: t('BALANCE_CONSUME.ADJUST_LIMIT', '调整限额'),
      message: t('BALANCE_CONSUME.LIMIT_HINT_LONG', '设置本周期最多可消费多少美元（0 = 不限）'),
      confirmText: t('BALANCE_CONSUME.SAVE', '保存'),
      input: {
        label: t('BALANCE_CONSUME.LIMIT_LABEL', '本周期消费上限（USD）'),
        type: 'number',
        defaultValue: data?.limit_usd != null ? String(data.limit_usd) : '',
        placeholder: '0',
      },
    });
    if (!res) return;
    const v = parseFloat(String(res.value || '').trim() || '0');
    if (isNaN(v) || v < 0) {
      toast.error(t('BALANCE_CONSUME.LIMIT_INVALID', '限额必须 ≥ 0'));
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
  const resetsAt = data.resets_at ? new Date(data.resets_at).toLocaleDateString('zh-CN') : '-';

  return (
    <section className="space-y-4 py-4">
      <header className="flex items-center gap-3">
        <Wallet size={20} className="text-primary" />
        <div>
          <h3 className="text-base font-semibold text-on-surface">
            {t('BALANCE_CONSUME.TITLE', '余额消费控制')}
          </h3>
          <p className="text-xs text-on-surface-variant mt-0.5">
            {t('BALANCE_CONSUME.DESC', '订阅用尽后，是否允许从美元余额继续扣费')}
          </p>
        </div>
      </header>

      {/* 总开关 */}
      <div className="flex items-center justify-between fl-card p-4">
        <div className="flex-1 min-w-0">
          <div id="balance-consume-enable-label" className="text-sm font-semibold text-on-surface">{t('BALANCE_CONSUME.ENABLED', '允许余额消费')}</div>
          <div className="text-[11px] text-on-surface-variant mt-0.5">
            {data.enabled
              ? t('BALANCE_CONSUME.ENABLED_ON', '订阅用尽后自动从余额扣费')
              : t('BALANCE_CONSUME.ENABLED_OFF', '订阅用尽后请求将被拒绝（402）')}
          </div>
        </div>
        <button
          type="button"
          role="switch"
          aria-checked={data.enabled}
          aria-labelledby="balance-consume-enable-label"
          disabled={saving}
          onClick={() => update({ enabled: !data.enabled })}
          className={`relative shrink-0 w-12 h-6 rounded-control-full transition disabled:opacity-50 ${data.enabled ? 'bg-primary' : 'bg-on-surface/20'}`}
        >
          <span className={`absolute top-0.5 w-5 h-5 rounded-control-full bg-white transition-all ${data.enabled ? 'left-6' : 'left-0.5'}`} />
        </button>
      </div>

      {/* 限额 + 进度 */}
      <div className="fl-card p-4 space-y-3">
        <div className="flex items-center justify-between">
          <div>
            <div className="text-xs text-on-surface-variant">
              {t('BALANCE_CONSUME.SPENT_LABEL', '本周期已消费')}
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
            {t('BALANCE_CONSUME.ADJUST_LIMIT', '调整限额')}
          </button>
        </div>

        {limitUSD > 0 ? (
          <div className="h-2 rounded-control-full bg-on-surface/10 overflow-hidden">
            <div className={`h-full transition-all ${percent >= 90 ? 'bg-error' : percent >= 70 ? 'bg-warning' : 'bg-primary'}`}
              style={{ width: `${percent}%` }} />
          </div>
        ) : (
          <div className="text-[11px] text-on-surface-variant">
            {t('BALANCE_CONSUME.LIMIT_HINT', '0 = 不限')}
          </div>
        )}

        <div className="text-[11px] text-on-surface-variant">
          {t('BALANCE_CONSUME.RESETS_AT', { date: resetsAt, defaultValue: '下次重置时间：{{date}}' })}
        </div>
      </div>

      {/* 窗口自定义（数值 + 单位） */}
      <WindowEditor
        currentSeconds={data.window_seconds}
        saving={saving}
        onSave={(secs) => update({ window_seconds: secs })}
        t={t}
      />

      {/* 当前余额已由顶栏统一展示，此处不再冗余 */}
    </section>
  );
};

// WindowEditor 自定义重置周期：数字输入 + 单位选择（秒/分/时/天/月）
const WindowEditor = ({ currentSeconds, saving, onSave, t }) => {
  const initial = decomposeSeconds(currentSeconds);
  const [value, setValue] = useState(initial.value);
  const [unit, setUnit] = useState(initial.unit);

  // 当 currentSeconds 从外部变化时（保存后重新拉取），同步本地编辑态
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
          {t('BALANCE_CONSUME.WINDOW_LABEL', '重置周期')}
        </div>
        <div className="text-[11px] text-on-surface-variant mt-0.5">
          {t('BALANCE_CONSUME.WINDOW_HINT', '当前 {{secs}} 秒（最短 60 秒，最长 365 天）').replace('{{secs}}', currentSeconds)}
        </div>
      </div>

      <div className="flex items-center gap-2">
        <input
          type="number"
          min={1}
          step={1}
          value={value}
          onChange={e => setValue(e.target.value)}
          className="w-32 h-10 bg-surface-container-high border border-outline rounded-control px-3 text-sm font-mono focus:border-primary outline-none"
        />
        <select
          value={unit}
          onChange={e => setUnit(e.target.value)}
          className="h-10 bg-surface-container-high border border-outline rounded-control px-3 text-sm focus:border-primary outline-none"
        >
          {UNITS.map(u => (
            <option key={u.id} value={u.id}>{t(`BALANCE_CONSUME.UNIT_${u.id.toUpperCase()}`, u.label)}</option>
          ))}
        </select>
        <button
          type="button"
          onClick={handleApply}
          disabled={saving || !valid || !dirty}
          className="h-10 px-4 bg-primary text-on-primary rounded-control text-sm font-semibold hover:opacity-90 disabled:opacity-40"
        >
          {saving ? t('BALANCE_CONSUME.SAVING', '保存中...') : t('BALANCE_CONSUME.APPLY', '应用')}
        </button>
      </div>

      {!valid && (
        <div className="text-[11px] text-error">
          {t('BALANCE_CONSUME.WINDOW_OUT_OF_RANGE', '范围必须在 60 秒到 365 天之间')}
        </div>
      )}
      <p className="text-[11px] text-on-surface-variant">
        {t('BALANCE_CONSUME.WINDOW_RESET_HINT', '修改周期会立即重置当前已消费计数')}
      </p>
    </div>
  );
};

export default BalanceConsumePreferences;
