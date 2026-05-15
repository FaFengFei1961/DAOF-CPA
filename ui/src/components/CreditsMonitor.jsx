import React, { useState, useEffect, useCallback, useMemo, useRef } from 'react';
import {
  Activity, RefreshCw, AlertTriangle, CheckCircle2, XCircle, Clock, Layers,
  Zap, Sparkles, Cpu, ServerCrash
} from 'lucide-react';
import toast from 'react-hot-toast';
import { remainingColor, fmtTime, fmtAbsoluteShort, safePct } from '../utils/credits';

// ─── Provider 元数据 ──────────────────────────────────────────────────
const PROVIDER_META = {
  claude: {
    label: 'Claude (Anthropic OAuth)',
    color: '#d97757',
    bgClass: 'bg-warning/10 border-warning/30',
    iconColor: 'text-warning',
    Icon: Sparkles,
  },
  antigravity: {
    label: 'Antigravity (Multi-model)',
    color: '#6366f1',
    bgClass: 'bg-primary/10 border-primary/30',
    iconColor: 'text-primary',
    Icon: Layers,
  },
  codex: {
    label: 'Codex / OpenAI ChatGPT',
    color: '#10b981',
    bgClass: 'bg-success/10 border-success/30',
    iconColor: 'text-success',
    Icon: Cpu,
  },
  'gemini-cli': {
    label: 'Gemini CLI',
    color: '#3b82f6',
    bgClass: 'bg-primary/10 border-primary/30',
    iconColor: 'text-primary',
    Icon: Zap,
  },
  kimi: {
    label: 'Kimi (Moonshot)',
    color: '#f59e0b',
    bgClass: 'bg-warning/10 border-warning/30',
    iconColor: 'text-warning',
    Icon: Activity,
  },
};

const getProviderMeta = (provider) => {
  return PROVIDER_META[provider] || {
    label: provider || 'Unknown',
    color: '#6b7280',
    bgClass: 'bg-surface-variant/10 border-outline-variant/30',
    iconColor: 'text-on-surface-variant',
    Icon: ServerCrash,
  };
};

// ─── 进度条 ───────────────────────────────────────────────────────────
const QuotaBar = ({ remaining, label, resetsAt }) => {
  const safeRem = safePct(remaining);
  const color = remainingColor(safeRem);
  const reset = fmtAbsoluteShort(resetsAt);
  return (
    <div className="space-y-1.5">
      <div className="flex items-center justify-between text-xs">
        <span className="text-on-surface-variant">{label}</span>
        <span className="font-mono font-semibold" style={{ color }}>
          {safeRem.toFixed(1)}%
        </span>
      </div>
      <div className="h-2 rounded-full bg-black/40 overflow-hidden border border-outline-variant/40">
        <div
          className="h-full transition-all duration-500 ease-out"
          style={{ width: `${safeRem}%`, background: color, boxShadow: `0 0 12px ${color}80` }}
        />
      </div>
      {reset && reset !== '—' && (
        <div className="flex items-center gap-1 text-[10px] text-outline font-mono">
          <Clock size={10} /> {reset}
        </div>
      )}
    </div>
  );
};

// ─── 单条上游账号卡片（memo 化避免父组件 re-render 时全量重绘） ──────────
const EntryCard = React.memo(function EntryCard({ entry }) {
  const meta = getProviderMeta(entry.provider);
  const Icon = meta.Icon;
  return (
    <div
      className={`rounded-overlay p-4 border ${
        entry.healthy
          ? 'bg-surface-container border-outline-variant'
          : entry.last_error
          ? 'bg-error/20 border-error/40'
          : 'bg-surface-container border-outline-variant opacity-70'
      }`}
    >
      <div className="flex items-start justify-between gap-3 mb-3 pb-3 border-b border-outline-variant/30">
        <div className="flex items-start gap-2.5 min-w-0 flex-1">
          <div className={`p-1.5 rounded-control ${meta.iconColor} bg-black/30 shrink-0`}>
            <Icon size={14} />
          </div>
          <div className="min-w-0 flex-1">
            <div className="font-mono text-xs text-on-surface truncate" title={entry.email || entry.file_name}>
              {entry.email || entry.file_name || entry.auth_id}
            </div>
            <div className="flex items-center gap-2 mt-0.5">
              <span className="text-[10px] text-outline font-mono">#{entry.auth_index}</span>
              {entry.plan_type && (
                <span className="text-[10px] px-1.5 py-0.5 rounded-control bg-primary/10 text-primary border border-primary/30 font-mono">
                  {entry.plan_type}
                </span>
              )}
            </div>
          </div>
        </div>
        <div className="shrink-0 flex flex-col items-end gap-1">
          {entry.healthy ? (
            <span className="inline-flex items-center gap-1 text-[10px] text-success">
              <CheckCircle2 size={11} /> 健康
            </span>
          ) : entry.last_error ? (
            <span className="inline-flex items-center gap-1 text-[10px] text-error">
              <XCircle size={11} /> 失败
            </span>
          ) : (
            <span className="inline-flex items-center gap-1 text-[10px] text-warning">
              <AlertTriangle size={11} /> 用尽
            </span>
          )}
          <span className="text-[10px] text-outline font-mono">
            {fmtTime(entry.last_refresh).replace(/^\d{4}\//, '')}
          </span>
        </div>
      </div>

      {entry.last_error && (
        <div className="mb-3 px-2 py-1.5 rounded-control bg-error/30 border border-error/40 text-[10px] text-error font-mono break-all">
          重试 #{entry.retry_count}：{entry.last_error}
        </div>
      )}

      {entry.windows && entry.windows.length > 0 ? (
        <div className="space-y-3">
          {entry.windows.map((w, i) => (
            <QuotaBar
              key={`${w.id || 'window'}-${i}`}
              remaining={w.remaining_percent}
              label={w.label || w.id}
              resetsAt={w.resets_at}
            />
          ))}
        </div>
      ) : !entry.last_error ? (
        <div className="text-[11px] text-outline italic">暂无窗口数据（账号有效但上游未返回额度细节）</div>
      ) : null}
    </div>
  );
});

// ─── Provider 分区 ─────────────────────────────────────────────────────
const ProviderSection = ({ provider, entries }) => {
  const meta = getProviderMeta(provider);
  const Icon = meta.Icon;
  const healthyCount = entries.filter((e) => e.healthy).length;
  const failedCount = entries.filter((e) => e.last_error).length;

  const aggregateRemaining = useMemo(() => {
    const allRems = entries
      .flatMap((e) => (e.windows || []).map((w) => w.remaining_percent))
      .filter((v) => Number.isFinite(v));
    if (allRems.length === 0) return null;
    return allRems.reduce((a, b) => a + b, 0) / allRems.length;
  }, [entries]);

  return (
    <div className={`rounded-overlay border ${meta.bgClass} p-4 md:p-6 mb-6`}>
      <div className="flex flex-col md:flex-row md:items-center md:justify-between gap-3 mb-5 pb-4 border-b border-outline-variant/30">
        <div className="flex items-center gap-3">
          <div className={`p-2 rounded-control ${meta.iconColor} bg-black/30`}>
            <Icon size={20} />
          </div>
          <div>
            <h3 className="text-base font-bold text-on-surface">{meta.label}</h3>
            <div className="flex items-center gap-3 text-xs mt-0.5">
              <span className="text-success">
                <CheckCircle2 size={11} className="inline mr-1" />
                {healthyCount} 健康
              </span>
              {failedCount > 0 && (
                <span className="text-error">
                  <XCircle size={11} className="inline mr-1" />
                  {failedCount} 失败
                </span>
              )}
              <span className="text-outline">/ 共 {entries.length}</span>
            </div>
          </div>
        </div>
        {aggregateRemaining !== null && (
          <div className="text-right">
            <div className="text-[10px] text-on-surface-variant uppercase tracking-wider">平均剩余</div>
            <div
              className="text-2xl font-bold font-mono"
              style={{ color: remainingColor(aggregateRemaining) }}
            >
              {aggregateRemaining.toFixed(1)}%
            </div>
          </div>
        )}
      </div>

      <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-3">
        {entries.map((e) => (
          <EntryCard key={e.auth_id} entry={e} />
        ))}
      </div>
    </div>
  );
};

// ─── 主组件 ──────────────────────────────────────────────────────────
const CreditsMonitor = () => {
  const [data, setData] = useState({
    entries: [],
    total_count: 0,
    healthy_count: 0,
    by_provider: {},
    last_full: '',
    refreshing: false,
    server_time: '',
  });
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);

  // 引用用于 cleanup：会话失效时清掉轮询、unmount 时清掉所有 setTimeout
  const pollIntervalRef = useRef(null);
  const pendingTimeoutsRef = useRef([]);
  const sessionExpiredRef = useRef(false);
  const mountedRef = useRef(true);

  const stopPolling = useCallback(() => {
    if (pollIntervalRef.current) {
      clearInterval(pollIntervalRef.current);
      pollIntervalRef.current = null;
    }
  }, []);

  const load = useCallback(async (showLoading = false) => {
    if (sessionExpiredRef.current) return;
    if (showLoading) setLoading(true);
    try {
      const res = await fetch('/api/admin/credits-pool', {
        credentials: 'include',
        cache: 'no-store',
      });
      if (res.status === 401 || res.status === 403) {
        sessionExpiredRef.current = true;
        stopPolling();
        toast.error('管理员会话已过期，请重新登录', { id: 'credits-session-expired' });
        return;
      }
      const json = await res.json();
      if (!mountedRef.current) return;
      if (json.success) {
        setData(json.data);
      } else {
        toast.error(json.message || '号池数据加载失败', { id: 'credits-load-error' });
      }
    } catch (err) {
      if (mountedRef.current) {
        toast.error('网络异常，无法加载号池数据', { id: 'credits-load-error' });
      }
    } finally {
      if (mountedRef.current) setLoading(false);
    }
  }, [stopPolling]);

  useEffect(() => {
    mountedRef.current = true;
    load(true);
    pollIntervalRef.current = setInterval(() => load(false), 30000);
    return () => {
      mountedRef.current = false;
      stopPolling();
      // 清除所有 pending setTimeout，避免 unmount 后还在 setState
      pendingTimeoutsRef.current.forEach(clearTimeout);
      pendingTimeoutsRef.current = [];
    };
  }, [load, stopPolling]);

  const triggerRefresh = async () => {
    if (refreshing || sessionExpiredRef.current) return;
    setRefreshing(true);

    // 清掉之前可能堆积的 pending timeout，防止旧的 load() 与新的混叠
    pendingTimeoutsRef.current.forEach(clearTimeout);
    pendingTimeoutsRef.current = [];

    try {
      const res = await fetch('/api/admin/credits-pool/refresh', {
        method: 'POST',
        credentials: 'include',
      });
      if (res.status === 401 || res.status === 403) {
        sessionExpiredRef.current = true;
        stopPolling();
        toast.error('管理员会话已过期，请重新登录', { id: 'credits-session-expired' });
        return;
      }
      if (res.status === 409) {
        toast('已有刷新任务在进行中', { id: 'credits-refresh-busy' });
        return;
      }
      const json = await res.json();
      if (!json.success) {
        // 后端明确告知失败原因（CPA 未配置 / 不可达 / 鉴权失败 等）
        toast.error(json.message || '刷新失败', {
          id: 'credits-refresh-fail',
          duration: 6000,
        });
        return;
      }
      toast.success(json.message || '已触发后台刷新', { id: 'credits-refresh-trigger' });
      // 阶梯式重新拉取数据，timeout id 收集起来用于 cleanup
      const ids = [
        setTimeout(() => mountedRef.current && load(false), 5000),
        setTimeout(() => mountedRef.current && load(false), 15000),
        setTimeout(() => mountedRef.current && load(false), 30000),
      ];
      pendingTimeoutsRef.current.push(...ids);
    } catch (err) {
      toast.error('网络异常');
    } finally {
      // session 过期或组件已 unmount 时不再调度 setState（避免在 finally 内 return 吞异常 — 用 if/else 分支）
      if (sessionExpiredRef.current || !mountedRef.current) {
        setRefreshing(false);
      } else {
        const tid = setTimeout(() => {
          if (mountedRef.current && !sessionExpiredRef.current) {
            setRefreshing(false);
          }
        }, 3000);
        pendingTimeoutsRef.current.push(tid);
      }
    }
  };

  const byProvider = useMemo(() => {
    const groups = {};
    for (const e of data.entries || []) {
      const key = e.provider || 'unknown';
      if (!groups[key]) groups[key] = [];
      groups[key].push(e);
    }
    return groups;
  }, [data.entries]);

  const orderedProviders = useMemo(() => {
    const known = Object.keys(PROVIDER_META).filter((k) => byProvider[k]);
    const unknown = Object.keys(byProvider).filter((k) => !PROVIDER_META[k]);
    return [...known, ...unknown];
  }, [byProvider]);

  const lastFullStr = data.last_full && data.last_full !== '0001-01-01T00:00:00Z'
    ? fmtTime(data.last_full)
    : '尚未完成首轮';

  return (
    <div className="w-full">
      <div className="mb-8 border-b border-outline-variant pb-6 flex flex-col md:flex-row md:items-center md:justify-between gap-4">
        <div>
          <h1 className="text-xl md:text-2xl font-bold tracking-tight text-on-surface flex items-center gap-3">
            <Activity size={22} className="text-primary" />
            号池监控
          </h1>
          <p className="text-on-surface-variant mt-2 text-sm">
            上游账号池的健康度与剩余额度监控。后台 goroutine 按 <span className="text-primary font-mono">credits_refresh_interval</span> 周期自动刷新；失败的会按 <span className="text-primary font-mono">credits_retry_interval</span> 进入重试队列（指数退避封顶 60 分钟）。
          </p>
        </div>
        <button
          onClick={triggerRefresh}
          disabled={refreshing || data.refreshing}
          aria-label="立即刷新全部号池"
          className="shrink-0 h-11 px-5 bg-primary text-on-primary font-medium rounded-overlay flex items-center justify-center gap-2 hover:bg-primary-container hover:text-on-primary-container disabled:opacity-50 disabled:cursor-not-allowed"
        >
          <RefreshCw size={16} className={refreshing || data.refreshing ? 'animate-spin' : ''} />
          {refreshing || data.refreshing ? '刷新中...' : '立即刷新全部'}
        </button>
      </div>

      <div className="grid grid-cols-2 md:grid-cols-4 gap-3 mb-8">
        <div className="rounded-overlay bg-surface-container border border-outline-variant p-4">
          <div className="text-xs text-on-surface-variant mb-1">上游账号总数</div>
          <div className="text-2xl font-bold text-on-surface">{data.total_count || 0}</div>
        </div>
        <div className="rounded-overlay bg-success/20 border border-success/40 p-4">
          <div className="text-xs text-success mb-1">健康节点</div>
          <div className="text-2xl font-bold text-success">{data.healthy_count || 0}</div>
        </div>
        <div className="rounded-overlay bg-error/20 border border-error/40 p-4">
          <div className="text-xs text-error mb-1">异常节点</div>
          <div className="text-2xl font-bold text-error">
            {(data.total_count || 0) - (data.healthy_count || 0)}
          </div>
        </div>
        <div className="rounded-overlay bg-surface-container border border-outline-variant p-4">
          <div className="text-xs text-on-surface-variant mb-1">最近全量刷新</div>
          <div className="text-sm font-mono text-on-surface mt-1.5 truncate" title={lastFullStr}>
            {lastFullStr}
          </div>
        </div>
      </div>

      {loading ? (
        <div className="text-center py-20 text-on-surface-variant">
          <RefreshCw size={28} className="inline animate-spin mb-3" />
          <div>正在加载号池数据...</div>
        </div>
      ) : sessionExpiredRef.current ? (
        <div className="text-center py-16 bg-error/20 border border-error/40 rounded-overlay">
          <XCircle size={32} className="inline text-error mb-3" />
          <div className="text-error text-sm">管理员会话已过期，请刷新页面重新登录</div>
        </div>
      ) : data.total_count === 0 ? (
        <div className="text-center py-16 bg-surface-container border border-outline-variant rounded-overlay">
          <ServerCrash size={32} className="inline text-on-surface-variant mb-3" />
          <div className="text-on-surface-variant text-sm">
            尚未采集到任何上游账号。
            <br />
            请检查 CLIProxyAPI 连接配置（常规 tab）和后台日志。
          </div>
        </div>
      ) : (
        orderedProviders.map((p) => (
          <ProviderSection key={p} provider={p} entries={byProvider[p]} />
        ))
      )}
    </div>
  );
};

export default CreditsMonitor;
