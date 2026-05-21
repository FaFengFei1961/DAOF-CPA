import React, { useState, useEffect, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import toast from 'react-hot-toast';
import { authFetch, isLoggedIn, readAuthState } from '../utils/authFetch';
import { isPageCacheFresh, readPageCache, writePageCache } from '../utils/pageCache';



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

const getCategoryLabel = (key, t) => {
  switch (key) {
    case 'subscription_expiring':
      return t('NOTIF.PREF.CAT_SUB_EXPIRING', '订阅到期提醒');
    case 'subscription_usage_warn':
      return t('NOTIF.PREF.CAT_SUB_USAGE', '套餐用量预警');
    case 'refund':
      return t('NOTIF.PREF.CAT_REFUND', '退款通知');
    case 'ticket_message':
      return t('NOTIF.PREF.CAT_TICKET_MESSAGE', '工单消息');
    default:
      return key;
  }
};

const getCategoryHint = (key, t) => {
  switch (key) {
    case 'subscription_expiring':
      return t('NOTIF.PREF.CAT_SUB_EXPIRING_HINT', '订阅过期与即将到期预警');
    case 'subscription_usage_warn':
      return t('NOTIF.PREF.CAT_SUB_USAGE_HINT', '用量达到阈值时提醒');
    case 'refund':
      return t('NOTIF.PREF.CAT_REFUND_HINT', '取消订阅退款到账提醒');
    case 'ticket_message':
      return t('NOTIF.PREF.CAT_TICKET_MESSAGE_HINT', '客服在你提交的工单里发新消息时通知你');
    default:
      return '';
  }
};

const getForcedCategoryLabel = (key, t) => {
  switch (key) {
    case 'CAT_SYSTEM_FORCED':
      return t('NOTIF.PREF.CAT_SYSTEM_FORCED', '系统公告（必收）');
    case 'CAT_SECURITY_FORCED':
      return t('NOTIF.PREF.CAT_SECURITY_FORCED', '账户安全（必收）');
    default:
      return key;
  }
};

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
  // Phase G-1.7：邮件 channel 偏好（per-category 开关）。空 map = 全关。
  const [enabledEmailCategories, setEnabledEmailCategories] = useState(() => cached?.enabled_email_categories || {});
  // 后端告知是否能用邮件 channel（master enable + 邮箱已验证）。
  const [emailChannelAvailable, setEmailChannelAvailable] = useState(() => !!cached?.email_channel_available);
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
      setEnabledEmailCategories(cachedPref.enabled_email_categories || {});
      setEmailChannelAvailable(!!cachedPref.email_channel_available);
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
        setEnabledEmailCategories(json.data.enabled_email_categories || {});
        setEmailChannelAvailable(!!json.data.email_channel_available);
      }
    } catch {
      // Use cached/default preferences when the preference endpoint is unavailable.
    } finally {
      setLoading(false);
    }
  }, [cacheKey]);

  useEffect(() => { load(); }, [load]);

  const toggleCategory = (key) => {
    const current = enabledCategories[key];
    const next = current === false ? true : false;
    setEnabledCategories({ ...enabledCategories, [key]: next });
  };

  // Phase G-1.7：邮件 channel 是保守 opt-in，缺失/false 都视为关。
  const isEmailEnabled = (key) => enabledEmailCategories[key] === true;
  const toggleEmailCategory = (key) => {
    setEnabledEmailCategories({ ...enabledEmailCategories, [key]: !isEmailEnabled(key) });
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
          enabled_email_categories: enabledEmailCategories,
        },
      });
      if (json.success) {
        toast.success(t('NOTIF.PREF.SAVE_OK', '通知偏好已更新'));
        if (json.data) {
          writePageCache(cacheKey, json.data);
          setEnabledCategories(json.data.enabled_categories || {});
          setUsageThresholds(Array.isArray(json.data.usage_thresholds) ? json.data.usage_thresholds : []);
          setEnabledEmailCategories(json.data.enabled_email_categories || {});
          setEmailChannelAvailable(!!json.data.email_channel_available);
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
    return <div className="text-sm text-on-surface-variant py-4">{t('COMMON.LOADING', '加载中…')}</div>;
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


      <div className="rounded-overlay border border-outline-variant bg-surface-container p-4 space-y-3">
        <div className="flex items-center justify-between">
          <div className="text-xs font-semibold text-on-surface-variant uppercase tracking-wide">
            {t('NOTIF.PREF.CATEGORIES_LABEL', '通知类别')}
          </div>
          <div className="hidden md:flex items-center gap-6 text-[10px] font-semibold text-on-surface-variant uppercase tracking-wide">
            <span className="w-12 text-center">{t('NOTIF.PREF.CHANNEL_INAPP', '站内')}</span>
            <span className="w-12 text-center">
              {t('NOTIF.PREF.CHANNEL_EMAIL', '邮件')}
            </span>
          </div>
        </div>
        {!emailChannelAvailable && (
          <p className="text-[11px] text-on-surface-variant bg-black/20 border border-outline-variant/60 rounded-control px-3 py-2">
            {t('NOTIF.PREF.EMAIL_UNAVAILABLE_HINT',
              '邮件 channel 不可用：管理员未启用邮箱功能，或您的邮箱尚未验证。请先在【账号】里绑定并验证邮箱。')}
          </p>
        )}
        {TOGGLABLE_CATEGORIES.map(cat => (
          <div key={cat.key} className="flex items-start gap-3">
            <div className="flex-1 group">
              <div className="text-sm text-on-surface group-hover:text-primary transition">
                {getCategoryLabel(cat.key, t)}
              </div>
              <div className="text-[11px] text-on-surface-variant mt-0.5">
                {getCategoryHint(cat.key, t)}
              </div>
            </div>
            <label className="w-12 flex items-center justify-center cursor-pointer" title={t('NOTIF.PREF.CHANNEL_INAPP_HINT', '站内通知')}>
              <input
                type="checkbox"
                checked={isCatEnabled(cat.key)}
                onChange={() => toggleCategory(cat.key)}
                className="w-4 h-4 accent-primary"
              />
            </label>
            <label className={`w-12 flex items-center justify-center ${emailChannelAvailable ? 'cursor-pointer' : 'cursor-not-allowed opacity-40'}`}
              title={emailChannelAvailable
                ? t('NOTIF.PREF.CHANNEL_EMAIL_HINT', '邮件通知（需先绑定并验证邮箱）')
                : t('NOTIF.PREF.EMAIL_UNAVAILABLE_HINT_SHORT', '需先验证邮箱')}>
              <input
                type="checkbox"
                disabled={!emailChannelAvailable}
                checked={isEmailEnabled(cat.key)}
                onChange={() => toggleEmailCategory(cat.key)}
                className="w-4 h-4 accent-primary"
              />
            </label>
          </div>
        ))}
        {FORCED_CATEGORIES.map(cat => (
          <div key={cat.i18n} className="flex items-start gap-3 opacity-70">
            <div className="flex-1">
              <div className="text-sm text-on-surface-variant">
                {getForcedCategoryLabel(cat.i18n, t)}
              </div>
            </div>
            <div className="w-12 flex items-center justify-center">
              <div className="w-4 h-4 rounded-control border border-outline-variant bg-on-surface/[0.06] flex items-center justify-center">
                <span className="text-[10px] text-on-surface-variant">✓</span>
              </div>
            </div>
            <div className="w-12 flex items-center justify-center text-[10px] text-on-surface-variant">
              {t('NOTIF.PREF.OPT_IN', 'opt-in')}
            </div>
          </div>
        ))}
      </div>


      <div className="rounded-overlay border border-outline-variant bg-surface-container p-4 space-y-3">
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
          className="btn btn-primary h-9 px-4 disabled:opacity-50"
        >
          {saving ? t('COMMON.SAVING', '保存中...') : t('COMMON.SAVE', '保存')}
        </button>
      </div>
    </section>
  );
};

export default NotificationPreferences;
