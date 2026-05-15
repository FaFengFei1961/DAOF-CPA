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

const SUB_CACHE_TTL_MS = 15000;

const getSubCacheKey = () => {
  const { isAdmin, userToken } = readAuthState();
  return `subscriptions:v3:${isAdmin ? 'admin' : userToken || 'guest'}`;
};

const MySubscriptions = ({ isAuthenticated = true, embedded = false }) => {
  const { t } = useTranslation();
  const confirm = useConfirm();
  const { formatCurrencyFixed } = useCurrency();
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
        toast.error(json.message || t('SUB.LOAD_FAIL', '加载失败'));
      }
    } catch {
      if (mountedRef.current) toast.error(t('SUB.LOAD_FAIL', '加载失败'));
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
    const msg = t('SUB.CANCEL_CONFIRM', { name, defaultValue: '取消订阅「{{name}}」？\n\n订阅将立即停止消费您的额度。如需退款，请通过客服工单提交申请。' });
    if (!(await confirm(msg))) return;
    try {
      const json = await authFetch(`/api/subscriptions/${sub.id}/cancel`, { method: 'POST' });
      if (json.success) {
        toast.success(t('SUB.CANCEL_OK', '订阅已取消。如需退款请联系客服。'));
        load({ force: true });
      } else toast.error(json.message || t('SUB.CANCEL_FAIL', '取消失败'));
    } catch {
      toast.error(t('SUB.CANCEL_NET_ERR', '网络异常，取消失败'));
    }
  };

  const activeItems = React.useMemo(() => subs
    .filter((s) => s.status === 'active')
    .map((sub) => normalizeSubscription(sub)), [subs]);

  const activeSubscriptions = activeItems;

  const body = loading ? (
    <div className="text-center py-20 text-on-surface-variant">{t('SUB.LOADING', '加载中...')}</div>
  ) : (
    <>
      <UsageOverview
        items={activeItems}
        refreshing={refreshing}
        onRefresh={() => load({ force: true })}
        formatMeterCurrency={formatCurrencyFixed}
      />

      <StoreSection
        title={t('MY_PRODUCTS.GROUP_SUBSCRIPTION', '订阅')}
        right={<span className="text-xs text-on-surface-variant">{activeSubscriptions.length} 个活跃订阅</span>}
      >
        {activeSubscriptions.length === 0 ? (
          <EmptyUsageCard>{t('MY_PRODUCTS.GROUP_SUB_EMPTY', '暂无活跃订阅')}</EmptyUsageCard>
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
    return <div className="space-y-8">{body}</div>;
  }

  return (
    <div className="w-full max-w-[1680px] mx-auto px-4 md:px-8 2xl:px-10 py-8">
      <StorePage
        icon={PackageIcon}
        title={t('MY_PRODUCTS.TITLE', '我的产品')}
        subtitle={t('MY_PRODUCTS.SUBTITLE', '订阅最先消耗；用尽后才走余额扣费（在账号设置中开启）。')}
      >
        {body}
      </StorePage>
    </div>
  );
};

const UsageOverview = ({ items, refreshing, onRefresh, formatMeterCurrency }) => {
  const apiRows = items.flatMap((item) => item.summaries).filter((u) => u.unit === 'api_cost_usd');
  const fiveHour = sumUsage(apiRows.filter((u) => Number(u.window_seconds || 0) === 5 * 3600));
  const sevenDay = sumUsage(apiRows.filter((u) => Number(u.window_seconds || 0) === 7 * 86400));
  const observed = summarizeObservedApiUsage(items);
  const priorityName = items[0]?.packageName || '—';

  return (
    <section className="space-y-3">
      <div className="flex items-center justify-between gap-3">
        <h2 className="text-xl font-bold text-on-surface">用量监控</h2>
        <button
          type="button"
          onClick={onRefresh}
          disabled={refreshing}
          className="inline-flex items-center gap-2 h-9 px-3 rounded-control bg-surface-container-high border border-outline-variant text-xs font-semibold text-on-surface-variant hover:text-on-surface disabled:opacity-60"
        >
          <RefreshCw size={14} className={refreshing ? 'animate-spin' : ''} />
          刷新
        </button>
      </div>
      <div className="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-4 gap-3">
        <MetricCard icon={Layers} label="当前优先消费" value={priorityName} sub={`${items.length} 个活跃订阅`} />
        <MetricCard icon={TimerReset} label="5 小时剩余" value={formatRemainingMetric(fiveHour, formatMeterCurrency)} sub={formatUsedMetric(fiveHour, formatMeterCurrency)} tone={fiveHour.remainingPct} />
        <MetricCard icon={Gauge} label="7 天剩余" value={formatRemainingMetric(sevenDay, formatMeterCurrency)} sub={formatUsedMetric(sevenDay, formatMeterCurrency)} tone={sevenDay.remainingPct} />
        <MetricCard icon={Activity} label="已采集用量" value={formatUsageValue(observed.consumed, 'api_cost_usd', formatMeterCurrency)} sub={`${observed.requestCount} 次调用`} />
      </div>
    </section>
  );
};

const MetricCard = ({ icon: Icon, label, value, sub, tone }) => {
  const color = tone == null ? '#c4b5fd' : remainingColor(tone);
  return (
    <div className="fl-card p-4 min-h-[104px]">
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
  <div className="fl-card p-8 text-center text-sm text-on-surface-variant">
    {children}
  </div>
);

const SubscriptionUsageCard = ({ item, priority, onCancel, t, formatMeterCurrency }) => {
  const { sub, packageName, summaries } = item;
  const daysLeft = Math.max(0, Math.ceil((new Date(sub.end_at).getTime() - Date.now()) / 86400000));
  const avgRemaining = averageRemainingPct(summaries);
  const color = remainingColor(avgRemaining);

  return (
    <div className={`fl-card overflow-hidden border ${priority ? 'border-primary/70' : 'border-outline-variant/60'}`}>
      <div className="p-5 border-b border-outline-variant/40">
        <div className="flex items-start justify-between gap-4">
          <div className="min-w-0">
            <div className="flex items-center gap-2 flex-wrap">
              <span className="font-bold text-lg text-on-surface min-w-0 break-words leading-snug">{packageName}</span>
              <span className="text-xs px-2 py-0.5 rounded-control bg-primary/10 text-primary font-mono">#{sub.stack_index}</span>
              {priority ? (
                <span className="text-xs px-2 py-0.5 rounded-control bg-success/20 text-success">{t('SUB.ACTIVE_TAG', '优先消费中')}</span>
              ) : (
                <span className="text-xs px-2 py-0.5 rounded-control bg-surface-container-high text-outline">{t('SUB.QUEUED_TAG', '排队中')}</span>
              )}
              {sub.is_granted && (
                <span className="text-xs px-2 py-0.5 rounded-control bg-surface-container-high text-on-surface-variant">内测赠送</span>
              )}
            </div>
            <div className="mt-2 text-xs text-on-surface-variant flex flex-wrap items-center gap-x-3 gap-y-1">
              <span><Clock size={11} className="inline mr-1" />{t('SUB.DAYS_LEFT', { n: daysLeft, defaultValue: '剩 {{n}} 天' })}</span>
              <span>{fmtTime(sub.start_at)} - {fmtTime(sub.end_at)}</span>
            </div>
          </div>
          <div className="shrink-0 text-right">
            <div className="text-[11px] text-on-surface-variant">平均剩余</div>
            <div className="text-2xl font-bold" style={{ color }}>{avgRemaining.toFixed(1)}%</div>
          </div>
          <button type="button" onClick={onCancel} className="p-2 -mr-2 -mt-2 text-on-surface-variant hover:text-error" title={t('SUB.CANCEL_BTN', '取消订阅')}>
            <X size={16} />
          </button>
        </div>
      </div>

      <div className="p-5 space-y-3">
        {summaries.length === 0 ? (
          <div className="text-xs text-outline italic">{t('SUB.NOT_USED', '暂无可展示的配额计划')}</div>
        ) : (
          summaries.map((u) => <UsageMeter key={`${u.plan_id}:${u.model_bucket}:${u.window_seconds}`} usage={u} formatMeterCurrency={formatMeterCurrency} />)
        )}
      </div>
    </div>
  );
};

const UsageMeter = ({ usage, formatMeterCurrency }) => {
  const consumedPct = usage.is_unlimited ? 0 : safePct(usage.usage_pct);
  const remainingPct = usage.is_unlimited ? 100 : Math.max(0, 100 - consumedPct);
  const color = remainingColor(remainingPct);
  const resetText = usage.window_end_at
    ? `${fmtRelativeFromNow(usage.window_end_at) || '窗口已结束'} · ${fmtTime(usage.window_end_at)}`
    : '首次使用后开始计时';

  return (
    <div className="rounded-overlay bg-surface-container-low border border-outline-variant/40 p-4">
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0">
          <div className="text-sm font-semibold text-on-surface break-words leading-snug">{formatPlanTitle(usage)}</div>
          <div className="mt-1 text-xs text-outline font-mono break-words [overflow-wrap:anywhere]">{usage.model_bucket || 'default'}</div>
        </div>
        <div className="text-right shrink-0">
          <div className="text-xs text-on-surface-variant">剩余</div>
          <div className="text-lg font-bold" style={{ color }}>{remainingPct.toFixed(1)}%</div>
        </div>
      </div>

      <div className="mt-3 h-2 rounded-control-full bg-black/35 overflow-hidden">
        <div className="h-full transition-all" style={{ width: `${consumedPct}%`, background: color }} />
      </div>

      <div className="mt-3 grid grid-cols-2 lg:grid-cols-4 gap-3 text-xs">
        <UsageDatum label="已用" value={formatUsageValue(usage.consumed, usage.unit, formatMeterCurrency)} />
        <UsageDatum label="额度" value={usage.is_unlimited ? '不限' : formatUsageValue(usage.limit, usage.unit, formatMeterCurrency)} />
        <UsageDatum label="剩余" value={usage.is_unlimited ? '不限' : formatUsageValue(usage.remaining, usage.unit, formatMeterCurrency)} />
        <UsageDatum label="调用" value={`${Number(usage.request_count || 0)} 次`} />
      </div>
      <div className="mt-3 text-[11px] text-on-surface-variant">{resetText}</div>
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

const formatPlanTitle = (usage) => {
  const windowText = formatWindow(usage.window_seconds);
  if (usage.unit === 'api_cost_usd') return `${windowText} API 等值额度`;
  if (usage.unit === 'request_count') return `${windowText} 调用次数`;
  if (usage.unit?.includes('tokens')) return `${windowText} Token 额度`;
  return usage.plan_name || windowText;
};

const formatWindow = (seconds) => {
  const n = Number(seconds || 0);
  if (!n) return '套餐周期';
  if (n === 5 * 3600) return '5 小时';
  if (n === 7 * 86400) return '7 天';
  if (n % 86400 === 0) return `${n / 86400} 天`;
  if (n % 3600 === 0) return `${n / 3600} 小时`;
  return `${n} 秒`;
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

const formatUsedMetric = (sum, formatMeterCurrency) => {
  if (sum.limit <= 0) return '暂无额度';
  return `已用 ${formatUsageValue(sum.consumed, 'api_cost_usd', formatMeterCurrency)}`;
};

export default MySubscriptions;
