import React, { useState, useEffect, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { Package as PackageIcon, Clock, X, Activity, RefreshCw, Gauge, TimerReset, Layers } from 'lucide-react';
import toast from 'react-hot-toast';
import { useConfirm } from '../context/ConfirmContext';
import { remainingColor, fmtTime, fmtRelativeFromNow, safePct } from '../utils/credits';
import { authFetch, readAuthState } from '../utils/authFetch';
import { isPageCacheFresh, readPageCache, writePageCache } from '../utils/pageCache';
import { useCurrency } from '../context/CurrencyContext';
import { StorePage, StoreSection } from './store/StorePrimitives';
import StatusBadge from './ui/StatusBadge';
import ProgressBar from './ui/ProgressBar';
import BrowsePackagesModal from './BrowsePackagesModal';

const SUB_CACHE_TTL_MS = 15000;

const getSubCacheKey = () => {
  const { isAdmin, userToken } = readAuthState();
  return `subscriptions:v3:${isAdmin ? 'admin' : userToken || 'guest'}`;
};

const MySubscriptions = ({ isAuthenticated = true, embedded = false }) => {
  const { t } = useTranslation();
  const confirm = useConfirm();
  const { formatCurrencyFixed } = useCurrency();
  // fix P2（codex review verify-r4）：旧通知链接 /upgrade?pane=store 被 routes.jsx redirect 到
  // /?openBrowse=store。已订阅用户进 dashboard 时 modal 默认关闭 → 商店入口丢失。
  // 检测到该 query 自动 open modal 并清理 URL（避免刷新重复触发）。
  const [browseOpen, setBrowseOpen] = useState(() => {
    if (typeof window === 'undefined') return false;
    const params = new URLSearchParams(window.location.search);
    if (params.get('openBrowse') === 'store' || params.get('openBrowse') === '1') {
      // 清理 query 防 refresh 重弹
      const url = new URL(window.location.href);
      url.searchParams.delete('openBrowse');
      window.history.replaceState({}, '', url.pathname + url.search + url.hash);
      return true;
    }
    return false;
  });
  const cacheKey = React.useMemo(getSubCacheKey, [isAuthenticated]);
  const [subs, setSubs] = useState(() => readPageCache(cacheKey) || []);
  const [loading, setLoading] = useState(() => isAuthenticated && !readPageCache(cacheKey));
  const [refreshing, setRefreshing] = useState(false);

  const mountedRef = React.useRef(true);
  useEffect(() => {
    mountedRef.current = true;
    return () => { mountedRef.current = false; };
  }, []);

  const load = useCallback(async ({ force = false } = {}) => {
    if (!isAuthenticated) {
      setSubs([]);
      setLoading(false);
      return;
    }
    const cached = readPageCache(cacheKey);
    if (cached) {
      setSubs(cached);
      setLoading(false);
      if (!force && isPageCacheFresh(cacheKey, SUB_CACHE_TTL_MS)) return;
    } else {
      setLoading(true);
    }

    if (force) setRefreshing(true);
    try {
      const json = await authFetch('/api/subscriptions/mine');
      if (!mountedRef.current) return;
      if (json.success) {
        const nextSubs = json.data || [];
        writePageCache(cacheKey, nextSubs);
        setSubs(nextSubs);
      } else {
        toast.error(json.message || t('MY_SUBS.LOAD_FAIL', '加载失败'));
      }
    } catch {
      if (mountedRef.current) toast.error(t('MY_SUBS.LOAD_FAIL', '加载失败'));
    } finally {
      if (mountedRef.current) {
        setLoading(false);
        setRefreshing(false);
      }
    }
  }, [cacheKey, isAuthenticated, t]);

  useEffect(() => { load(); }, [load]);

  const cancel = async (sub) => {
    const name = getPackageName(sub);
    const msg = t('MY_SUBS.CANCEL_CONFIRM', { name, defaultValue: '取消订阅「{{name}}」？\n\n订阅将立即停止消费您的额度。如需退款，请通过客服工单提交申请。' });
    if (!(await confirm(msg))) return;
    try {
      const json = await authFetch(`/api/subscriptions/${sub.id}/cancel`, { method: 'POST' });
      if (json.success) {
        toast.success(t('MY_SUBS.CANCEL_OK', '订阅已取消。如需退款请提交工单。'));
        load({ force: true });
      } else toast.error(json.message || t('MY_SUBS.CANCEL_FAIL', '取消失败'));
    } catch {
      toast.error(t('MY_SUBS.CANCEL_NET_ERR', '网络异常，取消失败'));
    }
  };

  const activeItems = React.useMemo(() => subs
    .filter((s) => s.status === 'active')
    .map((sub) => normalizeSubscription(sub)), [subs]);

  const activeSubscriptions = activeItems;

  const body = loading ? (
    <div className="text-center py-20 text-on-surface-variant">{t('COMMON.LOADING', '加载中…')}</div>
  ) : (
    <>
      <UsageOverview
        items={activeItems}
        refreshing={refreshing}
        onRefresh={() => load({ force: true })}
        formatMeterCurrency={formatCurrencyFixed}
      />

      <StoreSection
        title={t('MY_SUBS.GROUP_SUBSCRIPTION', '订阅')}
        right={(
          <>
            <span className="text-xs text-on-surface-variant">
              {t('MY_SUBS.ACTIVE_COUNT', '{{count}} 个活跃订阅', { count: activeSubscriptions.length })}
            </span>
            <button
              type="button"
              onClick={() => setBrowseOpen(true)}
              className="ml-2 text-sm font-semibold text-primary hover:underline inline-flex items-center gap-1"
            >
              {t('MY_SUBS.BROWSE_PACKAGES', '浏览套餐')}
            </button>
          </>
        )}
      >
        {activeSubscriptions.length === 0 ? (
          <EmptyUsageCard>
            <div className="flex flex-col items-center gap-3">
              <PackageIcon size={32} className="text-on-surface-variant/50" />
              <span>{t('MY_SUBS.GROUP_SUB_EMPTY', '暂无活跃订阅')}</span>
              <button
                type="button"
                onClick={() => setBrowseOpen(true)}
                className="mt-2 text-sm font-semibold text-primary hover:underline inline-flex items-center gap-1"
              >
                {t('MY_SUBS.BROWSE_PACKAGES', '浏览套餐')}
              </button>
            </div>
          </EmptyUsageCard>
        ) : (
          <div className="grid grid-cols-1 xl:grid-cols-2 gap-4">
            {activeSubscriptions.map((item, idx) => (
              <SubscriptionUsageCard
                key={item.sub.id}
                item={item}
                priority={idx === 0}
                onCancel={() => cancel(item.sub)}
                t={t}
                formatMeterCurrency={formatCurrencyFixed}
              />
            ))}
          </div>
        )}
      </StoreSection>
    </>
  );

  if (embedded) {
    return (
      <>
        <div className="space-y-8">{body}</div>
        <BrowsePackagesModal isOpen={browseOpen} onClose={() => { setBrowseOpen(false); load({ force: true }); }} />
      </>
    );
  }

  return (
    <div className="w-full max-w-[1680px] mx-auto px-4 md:px-8 2xl:px-10 py-8">
      <StorePage
        icon={PackageIcon}
        title={t('MY_SUBS.MY_TITLE', '我的订阅')}
        subtitle={t('MY_SUBS.MY_SUBTITLE', '订阅最先消耗；用尽后才走余额扣费（在账号设置中开启）。')}
      >
        {body}
      </StorePage>
      <BrowsePackagesModal isOpen={browseOpen} onClose={() => { setBrowseOpen(false); load({ force: true }); }} />
    </div>
  );
};

const UsageOverview = ({ items, refreshing, onRefresh, formatMeterCurrency }) => {
  const { t } = useTranslation();
  const apiRows = items.flatMap((item) => item.summaries).filter((u) => u.unit === 'api_cost_usd');
  const fiveHour = sumUsage(apiRows.filter((u) => Number(u.window_seconds || 0) === 5 * 3600));
  const sevenDay = sumUsage(apiRows.filter((u) => Number(u.window_seconds || 0) === 7 * 86400));
  const observed = summarizeObservedApiUsage(items);
  const priorityName = items[0]?.packageName || '—';

  return (
    <section className="space-y-3">
      <div className="flex items-center justify-between gap-3">
        <h2 className="text-xl font-bold text-on-surface">{t('MY_SUBS.USAGE_MONITOR_TITLE', '用量监控')}</h2>
        <button
          type="button"
          onClick={onRefresh}
          disabled={refreshing}
          className="inline-flex items-center gap-2 h-9 px-3 rounded-control bg-surface-container-high border border-outline-variant text-xs font-semibold text-on-surface-variant hover:text-on-surface disabled:opacity-60"
        >
          <RefreshCw size={14} className={refreshing ? 'animate-spin' : ''} />
          {t('COMMON.REFRESH', '刷新')}
        </button>
      </div>
      <div className="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-4 gap-3">
        <MetricCard icon={Layers} label={t('MY_SUBS.METRIC_PRIORITY', '当前优先消费')} value={priorityName} sub={t('MY_SUBS.ACTIVE_COUNT', '{{count}} 个活跃订阅', { count: items.length })} />
        <MetricCard icon={TimerReset} label={t('MY_SUBS.METRIC_5H_REMAINING', '5 小时剩余')} value={formatRemainingMetric(fiveHour, formatMeterCurrency)} sub={formatUsedMetric(fiveHour, formatMeterCurrency, t)} tone={fiveHour.remainingPct} />
        <MetricCard icon={Gauge} label={t('MY_SUBS.METRIC_7D_REMAINING', '7 天剩余')} value={formatRemainingMetric(sevenDay, formatMeterCurrency)} sub={formatUsedMetric(sevenDay, formatMeterCurrency, t)} tone={sevenDay.remainingPct} />
        <MetricCard icon={Activity} label={t('MY_SUBS.METRIC_OBSERVED_USAGE', '已采集用量')} value={formatUsageValue(observed.consumed, 'api_cost_usd', formatMeterCurrency)} sub={t('MY_SUBS.API_CALL_COUNT', '{{count}} 次调用', { count: observed.requestCount })} />
      </div>
    </section>
  );
};

const MetricCard = ({ icon: Icon, label, value, sub, tone }) => {
  const color = tone == null ? '#c4b5fd' : remainingColor(tone);
  return (
    <div className="card p-4 min-h-[104px]">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="text-xs text-on-surface-variant">{label}</div>
          <div className="mt-2 text-xl 2xl:text-2xl font-bold text-on-surface leading-tight break-words [overflow-wrap:anywhere]" style={{ color }}>{value}</div>
          <div className="mt-1 text-xs text-outline leading-snug break-words [overflow-wrap:anywhere]">{sub}</div>
        </div>
        <div className="w-9 h-9 rounded-control bg-primary/10 flex items-center justify-center shrink-0">
          <Icon size={18} className="text-primary" />
        </div>
      </div>
    </div>
  );
};

const EmptyUsageCard = ({ children }) => (
  <div className="card p-8 text-center text-sm text-on-surface-variant">
    {children}
  </div>
);

const SubscriptionUsageCard = ({ item, priority, onCancel, t, formatMeterCurrency }) => {
  const { sub, packageName, summaries } = item;
  const daysLeft = Math.max(0, Math.ceil((new Date(sub.end_at).getTime() - Date.now()) / 86400000));
  const avgRemaining = averageRemainingPct(summaries);
  const color = remainingColor(avgRemaining);

  return (
    <div className={`card overflow-hidden border ${priority ? 'border-primary/70' : 'border-outline-variant/60'}`}>
      <div className="p-5 border-b border-outline-variant/40">
        <div className="flex items-start justify-between gap-4">
          <div className="min-w-0">
            <div className="flex items-center gap-2 flex-wrap">
              <span className="font-bold text-lg text-on-surface min-w-0 break-words leading-snug">{packageName}</span>
              <span className="text-xs px-2 py-0.5 rounded-control bg-primary/10 text-primary font-mono">#{sub.stack_index}</span>
              {priority ? (
                <span className="text-xs px-2 py-0.5 rounded-control bg-success/20 text-success">{t('MY_SUBS.ACTIVE_TAG', '优先消费中')}</span>
              ) : (
                <span className="text-xs px-2 py-0.5 rounded-control bg-surface-container-high text-outline">{t('MY_SUBS.QUEUED_TAG', '排队中')}</span>
              )}
              {sub.is_granted && (
                <span className="text-xs px-2 py-0.5 rounded-control bg-surface-container-high text-on-surface-variant">{t('MY_SUBS.GRANTED_TAG', '内测赠送')}</span>
              )}
            </div>
            <div className="mt-2 text-xs text-on-surface-variant flex flex-wrap items-center gap-x-3 gap-y-1">
              <span><Clock size={11} className="inline mr-1" />{t('MY_SUBS.DAYS_LEFT', { n: daysLeft, defaultValue: '剩 {{n}} 天' })}</span>
              <span>{fmtTime(sub.start_at)} - {fmtTime(sub.end_at)}</span>
            </div>
          </div>
          <div className="shrink-0 text-right">
            <div className="text-[11px] text-on-surface-variant">{t('MY_SUBS.AVG_REMAINING', '平均剩余')}</div>
            <div className="text-2xl font-bold" style={{ color }}>{avgRemaining.toFixed(1)}%</div>
          </div>
          <button type="button" onClick={onCancel} className="p-2 -mr-2 -mt-2 text-on-surface-variant hover:text-error" title={t('MY_SUBS.CANCEL_BTN', '取消订阅')}>
            <X size={16} />
          </button>
        </div>
      </div>

      <div className="p-5 space-y-3">
        {summaries.length === 0 ? (
          <div className="text-xs text-outline italic">{t('MY_SUBS.NOT_USED', '暂无可展示的配额计划')}</div>
        ) : (
          summaries.map((u) => <UsageMeter key={`${u.plan_id}:${u.model_bucket}:${u.window_seconds}`} usage={u} formatMeterCurrency={formatMeterCurrency} />)
        )}
      </div>
    </div>
  );
};

const UsageMeter = ({ usage, formatMeterCurrency }) => {
  const { t } = useTranslation();
  const isExpired = usage.window_end_at && new Date(usage.window_end_at).getTime() < Date.now();
  const consumedPct = usage.is_unlimited ? 0 : safePct(usage.usage_pct);
  const remainingPct = usage.is_unlimited ? 100 : Math.max(0, 100 - consumedPct);
  const color = isExpired ? 'var(--color-outline)' : remainingColor(remainingPct);
  const unlimitedText = t('MY_SUBS.UNLIMITED', '不限');
  const resetText = usage.window_end_at
    ? t('MY_SUBS.WINDOW_RESET_AT', '{{relative}} · {{time}}', {
      relative: fmtRelativeFromNow(usage.window_end_at) || t('MY_SUBS.WINDOW_ENDED', '窗口已结束'),
      time: fmtTime(usage.window_end_at),
    })
    : t('MY_SUBS.WINDOW_START_AFTER_FIRST_USE', '首次使用后开始计时');

  return (
    <div className={`rounded-overlay border border-outline-variant/40 p-4 ${isExpired ? 'bg-surface-container/50 grayscale opacity-80' : 'bg-surface-container-low'}`}>
      <div className="flex items-start justify-between gap-4 mb-2">
        <div className="min-w-0">
          <div className="text-sm font-semibold text-on-surface break-words leading-snug">{formatPlanTitle(usage, t)}</div>
          <div
            className="mt-1 text-xs text-on-surface-variant break-words [overflow-wrap:anywhere]"
            title={usage.model_bucket || 'default'}
          >
            {formatModelBucket(usage.model_bucket, t)}
          </div>
        </div>
        <div className="text-right shrink-0">
          {isExpired ? (
            <div className="text-sm font-bold text-outline mt-1">{t('MY_SUBS.WINDOW_EXPIRED_TITLE', '已结束')}</div>
          ) : (
            <>
              <div className="text-xs text-on-surface-variant">{t('MY_SUBS.REMAINING', '剩余')}</div>
              <div className="text-lg font-bold" style={{ color }}>{remainingPct.toFixed(1)}%</div>
            </>
          )}
        </div>
      </div>

      <ProgressBar value={isExpired ? 0 : consumedPct} max={100} />

      {isExpired && (
        <div className="mt-2 text-xs text-on-surface-variant">
          {t('MY_SUBS.WINDOW_EXPIRED_HINT', '等待下次请求触发新窗口')}
        </div>
      )}

      <div className="mt-3 grid grid-cols-2 lg:grid-cols-4 gap-3 text-xs">
        <UsageDatum label={t('MY_SUBS.USED', '已用')} value={formatUsageValue(usage.consumed, usage.unit, formatMeterCurrency)} />
        <UsageDatum label={t('MY_SUBS.QUOTA', '额度')} value={usage.is_unlimited ? unlimitedText : formatUsageValue(usage.limit, usage.unit, formatMeterCurrency)} />
        <UsageDatum label={isExpired ? t('MY_SUBS.WINDOW', '窗口') : t('MY_SUBS.REMAINING', '剩余')} value={isExpired ? <span className="text-outline">{t('MY_SUBS.WINDOW_ENDED_RELATIVE', '已过期 · {{relative}}', { relative: fmtRelativeFromNow(usage.window_end_at) || '' })}</span> : (usage.is_unlimited ? unlimitedText : formatUsageValue(usage.remaining, usage.unit, formatMeterCurrency))} />
        <UsageDatum label={t('MY_SUBS.CALLS', '调用')} value={t('MY_SUBS.CALL_COUNT_SHORT', '{{count}} 次', { count: Number(usage.request_count || 0) })} />
      </div>
      <div className="mt-3 flex flex-col gap-1">
        {!isExpired && <div className="text-[11px] text-on-surface-variant">{resetText}</div>}
        {isExpired && (
          <div className="text-[10px] text-outline">
            {t('MY_SUBS.WINDOW_LAST_USAGE', '上次窗口用量 {{used}} / {{limit}}', { used: formatUsageValue(usage.consumed, usage.unit, formatMeterCurrency), limit: usage.is_unlimited ? unlimitedText : formatUsageValue(usage.limit, usage.unit, formatMeterCurrency) })}
          </div>
        )}
      </div>
    </div>
  );
};

const UsageDatum = ({ label, value }) => (
  <div>
    <div className="text-outline">{label}</div>
    <div className="font-mono text-on-surface mt-0.5 break-words [overflow-wrap:anywhere]">{value}</div>
  </div>
);

const normalizeSubscription = (sub) => {
  const snapshot = parseSnapshot(sub.package_snapshot);
  return {
    sub,
    snapshot,
    productType: snapshot.product_type || 'subscription',
    packageName: getPackageName(sub, snapshot),
    summaries: buildDisplaySummaries(sub, snapshot),
  };
};

const parseSnapshot = (raw) => {
  try {
    if (!raw) return {};
    return typeof raw === 'string' ? JSON.parse(raw) : raw;
  } catch {
    return {};
  }
};

const getPackageName = (sub, snapshot = parseSnapshot(sub.package_snapshot)) => (
  sub.package_name || snapshot.package_name || `#${sub.id}`
);

const buildDisplaySummaries = (sub, snapshot) => {
  const existing = Array.isArray(sub.usage_summary) ? sub.usage_summary : [];
  const plans = Array.isArray(snapshot.plans) ? snapshot.plans : [];
  if (plans.length === 0) return existing;

  return plans.map((plan) => {
    const found = existing.find((u) => Number(u.plan_id) === Number(plan.id));
    if (found) return normalizeUsage(found);

    const limit = Number(plan.limit_value || 0) * Math.max(1, Number(plan.quantity_multiplier || 1));
    return normalizeUsage({
      plan_id: plan.id,
      plan_name: plan.name,
      unit: plan.limit_unit,
      model_bucket: usageBucketFromPlan(plan),
      window_seconds: plan.window_seconds,
      consumed: 0,
      limit,
      remaining: limit,
      usage_pct: 0,
      request_count: 0,
      is_unlimited: limit <= 0,
    });
  });
};

const normalizeUsage = (usage) => {
  const limit = Number(usage.limit || 0);
  const consumed = Number(usage.consumed || 0);
  const unlimited = Boolean(usage.is_unlimited) || limit <= 0;
  const remaining = unlimited ? 0 : Math.max(0, Number.isFinite(Number(usage.remaining)) ? Number(usage.remaining) : limit - consumed);
  const usagePct = unlimited ? 0 : safePct(Number.isFinite(Number(usage.usage_pct)) ? Number(usage.usage_pct) : (consumed / limit) * 100);
  return {
    ...usage,
    limit,
    consumed,
    remaining,
    usage_pct: usagePct,
    request_count: Number(usage.request_count || 0),
    window_seconds: Number(usage.window_seconds || 0),
    is_unlimited: unlimited,
  };
};

const usageBucketFromPlan = (plan) => {
  try {
    const cfg = JSON.parse(plan.extra_config || '{}');
    if (cfg.bucket || cfg.model_bucket) return String(cfg.bucket || cfg.model_bucket);
  } catch {
    // ignore
  }
  try {
    const patterns = JSON.parse(plan.model_match || '[]');
    if (Array.isArray(patterns) && patterns[0]) return String(patterns[0]);
  } catch {
    // ignore
  }
  return 'default';
};

const formatPlanTitle = (usage, t) => {
  const windowText = formatWindow(usage.window_seconds, t);
  if (usage.unit === 'api_cost_usd') return t('MY_SUBS.PLAN_TITLE_API_CREDIT', '{{window}} API 等值额度', { window: windowText });
  if (usage.unit === 'request_count') return t('MY_SUBS.PLAN_TITLE_REQUESTS', '{{window}} 调用次数', { window: windowText });
  if (usage.unit?.includes('tokens')) return t('MY_SUBS.PLAN_TITLE_TOKENS', '{{window}} Token 额度', { window: windowText });
  return usage.plan_name || windowText;
};

// formatModelBucket 把 plan 的 model_bucket 机器码（subscription_seeds 里挂的
// "combo:all" / "claude-*" / "default" 等）翻译成用户能看懂的文案。
//
// 用户反馈"这里的英文是个什么情况"——之前 UsageMeter 直接把原始 bucket
// 字符串当 mono 文本展示，给用户看 "combo:all" 完全不知道是啥。
// 不认识的 bucket 仍然返回原值（保留信息，不静默吞掉）。
const formatModelBucket = (bucket, t) => {
  const b = String(bucket || '').trim().toLowerCase();
  if (!b || b === 'default') return t('MY_SUBS.BUCKET_DEFAULT', '适用模型：通用额度');
  if (b === 'combo:all') {
    return t('MY_SUBS.BUCKET_COMBO_ALL', '适用模型：Claude + Codex + Gemini 全部模型共享');
  }
  if (b.startsWith('claude')) return t('MY_SUBS.BUCKET_CLAUDE', '适用模型：Claude 系列');
  if (b.startsWith('gpt') || b.startsWith('codex')) {
    return t('MY_SUBS.BUCKET_CODEX', '适用模型：GPT / Codex 系列');
  }
  if (b.startsWith('gemini')) return t('MY_SUBS.BUCKET_GEMINI', '适用模型：Gemini 系列');
  if (b.startsWith('grok')) return t('MY_SUBS.BUCKET_GROK', '适用模型：xAI Grok 系列');
  return bucket; // unknown bucket：原值兜底，避免静默丢信息
};

const formatWindow = (seconds, t) => {
  const n = Number(seconds || 0);
  if (!n) return t('MY_SUBS.WINDOW_PACKAGE_PERIOD', '套餐周期');
  if (n === 5 * 3600) return t('MY_SUBS.WINDOW_5H', '5 小时');
  if (n === 7 * 86400) return t('MY_SUBS.WINDOW_7D', '7 天');
  if (n % 86400 === 0) return t('MY_SUBS.WINDOW_DAYS', '{{count}} 天', { count: n / 86400 });
  if (n % 3600 === 0) return t('MY_SUBS.WINDOW_HOURS', '{{count}} 小时', { count: n / 3600 });
  return t('MY_SUBS.WINDOW_SECONDS', '{{count}} 秒', { count: n });
};

const formatUsageValue = (value, unit, formatMeterCurrency) => {
  const n = Number(value || 0);
  if (unit === 'api_cost_usd') return formatMeterCurrency(n, 3);
  if (unit === 'request_count') return `${Math.round(n)}`;
  if (unit && unit.includes('tokens')) return n >= 1000 ? `${(n / 1000).toFixed(1)}k` : `${Math.round(n)}`;
  return n.toFixed(2);
};

const averageRemainingPct = (summaries) => {
  const finite = summaries.filter((u) => !u.is_unlimited);
  if (finite.length === 0) return 100;
  const total = finite.reduce((sum, u) => sum + Math.max(0, 100 - safePct(u.usage_pct)), 0);
  return total / finite.length;
};

const sumUsage = (rows) => {
  const limit = rows.reduce((sum, u) => sum + (u.is_unlimited ? 0 : Number(u.limit || 0)), 0);
  const consumed = rows.reduce((sum, u) => sum + Number(u.consumed || 0), 0);
  const remaining = rows.reduce((sum, u) => sum + (u.is_unlimited ? 0 : Number(u.remaining || 0)), 0);
  return {
    limit,
    consumed,
    remaining,
    remainingPct: limit > 0 ? Math.max(0, Math.min(100, remaining / limit * 100)) : 100,
  };
};

const summarizeObservedApiUsage = (items) => {
  const byPool = new Map();
  items.forEach((item) => {
    const subID = item?.sub?.id ?? item?.packageName ?? 'unknown';
    (item?.summaries || [])
      .filter((u) => u.unit === 'api_cost_usd')
      .forEach((u) => {
        const bucket = u.model_bucket || 'default';
        const key = `${subID}\u0000${bucket}\u0000${u.unit}`;
        const prev = byPool.get(key) || { consumed: 0, requestCount: 0 };
        byPool.set(key, {
          consumed: Math.max(prev.consumed, Number(u.consumed || 0)),
          requestCount: Math.max(prev.requestCount, Number(u.request_count || 0)),
        });
      });
  });
  let consumed = 0;
  let requestCount = 0;
  byPool.forEach((row) => {
    consumed += row.consumed;
    requestCount += row.requestCount;
  });
  return { consumed, requestCount };
};

const formatRemainingMetric = (sum, formatMeterCurrency) => {
  if (sum.limit <= 0) return '—';
  return `${formatUsageValue(sum.remaining, 'api_cost_usd', formatMeterCurrency)} / ${formatUsageValue(sum.limit, 'api_cost_usd', formatMeterCurrency)}`;
};

const formatUsedMetric = (sum, formatMeterCurrency, t) => {
  if (sum.limit <= 0) return t('MY_SUBS.NO_QUOTA', '暂无额度');
  return t('MY_SUBS.USED_AMOUNT', '已用 {{amount}}', {
    amount: formatUsageValue(sum.consumed, 'api_cost_usd', formatMeterCurrency),
  });
};

export default MySubscriptions;
