import React, { useState, useEffect, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import toast from 'react-hot-toast';
import { authFetch, isLoggedIn, readAuthState } from '../utils/authFetch';
import { isPageCacheFresh, readPageCache, writePageCache } from '../utils/pageCache';

// 用户级通知偏好：3 类可关闭 + 阈值多选。
// security/system 类强制送达，UI 仅展示提示，不渲染开关。
const TOGGLABLE_CATEGORIES = [
  { key: 'subscription_expiring', i18n: 'CAT_SUB_EXPIRING' },
  { key: 'subscription_usage_warn', i18n: 'CAT_SUB_USAGE' },
  { key: 'refund', i18n: 'CAT_REFUND' },
  { key: 'ticket_message', i18n: 'CAT_TICKET_MESSAGE' },
];

const FORCED_CATEGORIES = [
  { i18n: 'CAT_SYSTEM_FORCED' },
  { i18n: 'CAT_SECURITY_FORCED' },
];

const THRESHOLD_PRESETS = [70, 80, 90, 100];
const NOTIF_PREF_CACHE_TTL_MS = 30000;
const getNotifPrefCacheKey = () => {
  const { isAdmin, userToken } = readAuthState();
  return `notification-pref:${isAdmin ? 'admin' : userToken || 'guest'}`;
};

const NotificationPreferences = () => {
  const { t } = useTranslation();
  const cacheKey = React.useMemo(getNotifPrefCacheKey, []);
  const cached = readPageCache(cacheKey);
  const [enabledCategories, setEnabledCategories] = useState(() => cached?.enabled_categories || {});
  const [usageThresholds, setUsageThresholds] = useState(() => (
    Array.isArray(cached?.usage_thresholds) ? cached.usage_thresholds : [80, 100]
  ));
  const [loading, setLoading] = useState(() => !cached);
  const [saving, setSaving] = useState(false);

  const load = useCallback(async ({ force = false } = {}) => {
    if (!isLoggedIn()) {
      setLoading(false);
      return;
    }
    const cachedPref = readPageCache(cacheKey);
    if (cachedPref) {
      setEnabledCategories(cachedPref.enabled_categories || {});
      setUsageThresholds(Array.isArray(cachedPref.usage_thresholds) ? cachedPref.usage_thresholds : []);
      setLoading(false);
      if (!force && isPageCacheFresh(cacheKey, NOTIF_PREF_CACHE_TTL_MS)) return;
    } else {
      setLoading(true);
    }
    try {
      const json = await authFetch('/api/notifications/preference');
      if (json.success && json.data) {
        writePageCache(cacheKey, json.data);
        setEnabledCategories(json.data.enabled_categories || {});
        setUsageThresholds(Array.isArray(json.data.usage_thresholds) ? json.data.usage_thresholds : []);
      }
    } catch {
      // 静默：用首屏默认值
    } finally {
      setLoading(false);
    }
  }, [cacheKey]);

  useEffect(() => { load(); }, [load]);

  const toggleCategory = (key) => {
    // 缺失视为启用，所以第一次点击应当显式置 false
    const current = enabledCategories[key];
    const next = current === false ? true : false;
    setEnabledCategories({ ...enabledCategories, [key]: next });
  };

  const toggleThreshold = (val) => {
    if (usageThresholds.includes(val)) {
      setUsageThresholds(usageThresholds.filter(t => t !== val));
    } else {
      setUsageThresholds([...usageThresholds, val].sort((a, b) => a - b));
    }
  };

  const handleSave = async () => {
    setSaving(true);
    try {
      const json = await authFetch('/api/notifications/preference', {
        method: 'PUT',
        body: {
          enabled_categories: enabledCategories,
          usage_thresholds: usageThresholds,
        },
      });
      if (json.success) {
        toast.success(t('NOTIF.PREF.SAVE_OK', '通知偏好已更新'));
        if (json.data) {
          writePageCache(cacheKey, json.data);
          setEnabledCategories(json.data.enabled_categories || {});
          setUsageThresholds(Array.isArray(json.data.usage_thresholds) ? json.data.usage_thresholds : []);
        }
      } else {
        toast.error(json.message || t('NOTIF.PREF.SAVE_FAIL', '保存失败'));
      }
    } catch {
      toast.error(t('NOTIF.PREF.SAVE_FAIL', '保存失败'));
    } finally {
      setSaving(false);
    }
  };

  const isCatEnabled = (key) => enabledCategories[key] !== false;

  if (loading) {
    return <div className="text-sm text-on-surface-variant py-4">{t('SYSTEM.LOADING', '加载中...')}</div>;
  }

  return (
    <section className="space-y-4 py-4">
      <header>
        <h3 className="text-base font-semibold text-on-surface">
          {t('NOTIF.PREF.TITLE', '通知偏好')}
        </h3>
        <p className="text-xs text-on-surface-variant mt-1">
          {t('NOTIF.PREF.DESC', '选择您希望接收哪些类别的通知。系统公告与账户安全通知必收。')}
        </p>
      </header>

      {/* 类别开关 */}
      <div className="rounded-xl border border-outline-variant bg-surface-container p-4 space-y-3">
        <div className="text-xs font-semibold text-on-surface-variant uppercase tracking-wide">
          {t('NOTIF.PREF.CATEGORIES_LABEL', '通知类别')}
        </div>
        {TOGGLABLE_CATEGORIES.map(cat => (
          <label key={cat.key} className="flex items-start gap-3 cursor-pointer group">
            <input
              type="checkbox"
              checked={isCatEnabled(cat.key)}
              onChange={() => toggleCategory(cat.key)}
              className="mt-1 w-4 h-4 accent-primary"
            />
            <div className="flex-1">
              <div className="text-sm text-on-surface group-hover:text-primary transition">
                {t(`NOTIF.PREF.${cat.i18n}`)}
              </div>
              <div className="text-[11px] text-on-surface-variant mt-0.5">
                {t(`NOTIF.PREF.${cat.i18n}_HINT`)}
              </div>
            </div>
          </label>
        ))}
        {FORCED_CATEGORIES.map(cat => (
          <div key={cat.i18n} className="flex items-start gap-3 opacity-70">
            <div className="mt-1 w-4 h-4 rounded border border-outline-variant bg-on-surface/[0.06] flex items-center justify-center">
              <span className="text-[10px] text-on-surface-variant">✓</span>
            </div>
            <div className="text-sm text-on-surface-variant">
              {t(`NOTIF.PREF.${cat.i18n}`)}
            </div>
          </div>
        ))}
      </div>

      {/* 阈值 */}
      <div className="rounded-xl border border-outline-variant bg-surface-container p-4 space-y-3">
        <div>
          <div className="text-xs font-semibold text-on-surface-variant uppercase tracking-wide">
            {t('NOTIF.PREF.THRESHOLDS_LABEL', '用量预警阈值')}
          </div>
          <div className="text-[11px] text-on-surface-variant mt-1">
            {t('NOTIF.PREF.THRESHOLDS_HINT', '套餐用量跨过这些百分比时提醒（可多选；全不选=关闭用量预警）')}
          </div>
        </div>
        <div className="flex flex-wrap gap-2">
          {THRESHOLD_PRESETS.map(val => {
            const active = usageThresholds.includes(val);
            return (
              <button
                key={val}
                type="button"
                onClick={() => toggleThreshold(val)}
                className={`px-3 py-1.5 rounded-full text-xs font-semibold border transition ${
                  active
                    ? 'bg-primary text-on-primary border-primary'
                    : 'bg-transparent text-on-surface-variant border-outline-variant hover:border-primary hover:text-primary'
                }`}
              >
                {val}%
              </button>
            );
          })}
        </div>
      </div>

      <div className="flex justify-end">
        <button
          type="button"
          onClick={handleSave}
          disabled={saving}
          className="fl-btn fl-btn-prominent h-9 px-4 disabled:opacity-50"
        >
          {saving ? t('NOTIF.PREF.SAVING', '保存中...') : t('NOTIF.PREF.SAVE', '保存')}
        </button>
      </div>
    </section>
  );
};

export default NotificationPreferences;
