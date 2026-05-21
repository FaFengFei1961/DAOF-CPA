import React, { useState, useEffect, useMemo, useCallback, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { Activity, Coins, Zap, RefreshCw, BarChart2, Check, ChevronLeft, ChevronRight, Download } from 'lucide-react';
import { AreaChart, Area, LineChart, Line, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer } from 'recharts';
import { useCurrency } from '../context/CurrencyContext';
import { HealthMonitor } from './HealthMonitor';
import { authFetch } from '../utils/authFetch';
import { formatUsageLine, formatUsageLinesSummary, usageLinesOf } from '../utils/usageLines';
import Drawer from './ui/Drawer';
import Section from './ui/Section';

const CHART_COLORS = ['#3b82f6', '#10b981', '#f59e0b', '#ef4444', '#8b5cf6', '#ec4899', '#06b6d4', '#f97316', '#14b8a6'];
const STATS_CACHE_TTL_MS = 30000;
const statsCache = new Map();

const getStatsCacheKey = (isAdmin, period) => `${isAdmin ? 'admin' : 'user'}:${period}`;

const readStatsCache = (key) => statsCache.get(key)?.data || null;

const isStatsCacheFresh = (key) => {
    const entry = statsCache.get(key);
    return !!entry && Date.now() - entry.updatedAt < STATS_CACHE_TTL_MS;
};

const writeStatsCache = (key, data) => {
    statsCache.set(key, { data, updatedAt: Date.now() });
};

/* ═══════════════ StatCard (sparkline) ═══════════════ */
/* ═══════════════ StatCard (sparkline) ═══════════════ */
const StatCard = ({ title, value, subLabel, metaNode, data, dataKey, color, bgClass, icon: Icon }) => (
    <div className={`rounded-overlay p-5 border border-outline-variant  relative overflow-hidden flex flex-col justify-between ${bgClass} bg-opacity-40`}>
        <div className="flex items-start justify-between relative z-10 mb-2">
            <div className="flex flex-col gap-1 w-full relative z-20">
                <div className="flex items-center gap-2 mb-1">
                    <span className="text-sm font-semibold text-on-surface-variant tracking-wide">{title}</span>
                </div>
                <span className="text-3xl font-bold text-on-surface tracking-tight" style={{textShadow: '0 2px 10px rgba(0,0,0,0.5)'}}>{value}</span>
                {subLabel && <span className="text-xs text-on-surface-variant font-medium mt-1">{subLabel}</span>}
                {metaNode && <div className="mt-2 space-y-1 z-30">{metaNode}</div>}
            </div>
            <div className="p-2 rounded-control opacity-80 shrink-0 z-20" style={{ backgroundColor: `${color}20`, color, border: `1px solid ${color}40` }}>
                <Icon size={20} />
            </div>
        </div>
        <div className="absolute bottom-0 left-0 right-0 h-24 w-full z-0 opacity-80 pointer-events-none">
            <ResponsiveContainer width="100%" height="100%">
                <AreaChart data={data || []}>
                    <defs>
                        <linearGradient id={`color-${dataKey}`} x1="0" y1="0" x2="0" y2="1">
                            <stop offset="5%" stopColor={color} stopOpacity={0.4} />
                            <stop offset="95%" stopColor={color} stopOpacity={0} />
                        </linearGradient>
                    </defs>
                    <Area isAnimationActive={false} type="monotone" dataKey={dataKey} stroke={color} strokeWidth={2.5} fillOpacity={1} fill={`url(#color-${dataKey})`} />
                </AreaChart>
            </ResponsiveContainer>
        </div>
    </div>
);


/* ═══════════════ Custom Tooltip ═══════════════ */
const CustomTooltip = ({ active, payload, label, formatValue }) => {
    if (!active || !payload?.length) return null;
    return (
        <div className="bg-surface-container-high border border-outline-variant p-3 rounded-control shadow-black/50 text-xs">
            <p className="font-mono text-on-surface-variant mb-2 border-b border-outline-variant/50 pb-1">{label}</p>
            {payload.map((entry, i) => (
                <div key={i} className="flex justify-between items-center gap-4 py-0.5">
                    <span className="flex items-center gap-1.5 min-w-[100px]">
                        <span className="w-2 h-2 rounded-full" style={{ backgroundColor: entry.color }} />
                        <span className="text-on-surface-variant font-medium truncate">{entry.name}</span>
                    </span>
                    <span className="text-on-surface font-mono">{formatValue ? formatValue(entry.value) : (typeof entry.value === 'number' ? entry.value.toLocaleString() : entry.value)}</span>
                </div>
            ))}
        </div>
    );
};

const formatLatency = (ms) => {
    if (!ms) return '-';
    if (ms < 1000) return `${ms}ms`;
    if (ms < 60000) return `${(ms/1000).toFixed(2)}s`;
    const m = Math.floor(ms/60000);
    const s = Math.floor((ms%60000)/1000);
    return `${m}m${s}s`;
};

const rawCostOf = (row) => Number(row?.raw_cost ?? row?.cost ?? 0) || 0;
const chargedCostOf = (row) => Number(row?.charged_cost ?? row?.total_charged_cost ?? row?.cost ?? 0) || 0;
const costsDiffer = (raw, charged) => Math.abs(Number(raw || 0) - Number(charged || 0)) > 0.0000005;

const UsageLinesCell = ({ row, formatMeterCost }) => {
    const lines = usageLinesOf(row);
    if (!lines.length) {
        return <span className="text-outline">-</span>;
    }
    return (
        <div className="space-y-1 max-w-[260px]">
            {lines.slice(0, 2).map((line) => (
                <div key={line.id || `${line.unit}-${line.direction}-${line.quantity}`} className="text-[11px] text-on-surface-variant font-mono leading-tight truncate" title={formatUsageLine(line, formatMeterCost)}>
                    {formatUsageLine(line, formatMeterCost)}
                </div>
            ))}
            {lines.length > 2 && (
                <div className="text-[10px] text-outline">+{lines.length - 2}</div>
            )}
        </div>
    );
};

/* ═══════════════ Sortable Table Header ═══════════════ */
const SortHeader = ({ label, sortKey, currentSort, onSort }) => {
    const active = currentSort.key === sortKey;
    const arrow = active ? (currentSort.dir === 'asc' ? ' ▲' : ' ▼') : '';
    return (
        <th className="text-left px-4 py-3 text-xs font-semibold text-on-surface-variant cursor-pointer hover:text-white select-none whitespace-nowrap" onClick={() => onSort(sortKey)}>
            {label}{arrow}
        </th>
    );
};

const StatsLoadingShell = ({ t }) => (
    <div className="w-full mb-8 animate-pulse">
        <div className="flex flex-col md:flex-row md:items-end md:justify-between mb-8 gap-3">
            <div>
                <div className="h-10 w-48 rounded-control bg-surface-container-high border border-outline-variant" />
                <div className="h-4 w-80 max-w-full rounded-control bg-surface-container-high border border-outline-variant mt-3" />
            </div>
            <div className="flex items-center gap-2 bg-surface-container/40 p-1 rounded-control">
                {['24h', '7d', '30d'].map(p => (
                    <div key={p} className="h-8 w-14 rounded-control bg-surface-container-high" />
                ))}
            </div>
        </div>
        <div className="grid grid-cols-1 lg:grid-cols-2 gap-4 mb-4">
            {[0, 1].map(i => (
                <div key={i} className="h-40 rounded-overlay border border-outline-variant bg-surface-container" />
            ))}
        </div>
        <div className="grid grid-cols-1 sm:grid-cols-3 gap-4 mb-6">
            {[0, 1, 2].map(i => (
                <div key={i} className="h-32 rounded-overlay border border-outline-variant bg-surface-container" />
            ))}
        </div>
        <div className="rounded-overlay border border-outline-variant bg-surface-container p-6">
            <div className="h-4 w-36 rounded-control bg-surface-container-high mb-5" />
            <div className="h-64 rounded-control bg-surface-container-high" />
        </div>
        <span className="sr-only">{t('STATS.LOADING')}</span>
    </div>
);

/* ═══════════════ Main Component ═══════════════ */
const StatisticsDash = ({ isAdmin = false, isAuthenticated = true }) => {
    const { t } = useTranslation();
    const { formatCurrencyFixed } = useCurrency();
    const [period, setPeriod] = useState('7d');
    const statsCacheKey = useMemo(() => getStatsCacheKey(isAdmin, period), [isAdmin, period]);
    const [stats, setStats] = useState(() => readStatsCache(getStatsCacheKey(isAdmin, '7d')));
    // When unauthenticated, let RequireAuth own the banner instead of showing a loading skeleton.
    const [loading, setLoading] = useState(() => (isAuthenticated || isAdmin) && !readStatsCache(getStatsCacheKey(isAdmin, '7d')));
    const [refreshing, setRefreshing] = useState(false);
    const [selectedModels, setSelectedModels] = useState([]);
    const [logsPage, setLogsPage] = useState(1);
    // 用户点行展开详情用。用户反馈"请求事件明细一行扁平看不清，改成 admin 那样的右侧面板吧"。
    // 但不要泄上游账号 / 内部 cost 归因 —— 详情面板只渲安全字段（PublicApiLog 已先过一层 DTO，
    // 这里 UI 再选一遍渲染哪些，双重保护）。
    const [selectedLog, setSelectedLog] = useState(null);
    const fetchSeqRef = useRef(0);

    // Filters for request events
    const [filterModel, setFilterModel] = useState('');
    const [filterToken, setFilterToken] = useState('');

    // Sort state for tables
    const [tokenSort, setTokenSort] = useState({ key: 'reqs', dir: 'desc' });
    const [modelSort, setModelSort] = useState({ key: 'reqs', dir: 'desc' });
    const formatMeterCost = useCallback((value) => formatCurrencyFixed(Number(value || 0), 3), [formatCurrencyFixed]);
    const renderCostPair = useCallback((row, { showWeight = false } = {}) => {
        const raw = rawCostOf(row);
        const charged = chargedCostOf(row);
        const weight = Number(row?.model_weight || 1);
        const health = Number(row?.health_multiplier || 1);
        const multiplier = weight * health;
        const hasMultiplier = Math.abs(multiplier - 1) > 0.000001;
        return (
            <div className="font-mono leading-tight">
                <div className="text-on-surface">{formatMeterCost(charged)}</div>
                {costsDiffer(raw, charged) && (
                    <div className="text-[10px] text-on-surface-variant whitespace-nowrap">
                        {t('STATS.RAW_COST_SHORT', 'raw')} {formatMeterCost(raw)}
                    </div>
                )}
                {showWeight && hasMultiplier && (
                    <div className="text-[10px] text-primary whitespace-nowrap">
                        ×{multiplier.toFixed(2)}
                    </div>
                )}
            </div>
        );
    }, [formatMeterCost, t]);

    const fetchStats = useCallback(async ({ force = false } = {}) => {
        const requestId = ++fetchSeqRef.current;
        const cachedStats = readStatsCache(statsCacheKey);

        if (!isAuthenticated && !isAdmin) {
            setStats(null);
            setLoading(false);
            setRefreshing(false);
            return;
        }

        if (cachedStats) {
            setStats(cachedStats);
            setLogsPage(1);
            setLoading(false);
            if (!force && isStatsCacheFresh(statsCacheKey)) {
                setRefreshing(false);
                return;
            }
            setRefreshing(true);
        } else {
            setStats(null);
            setLoading(true);
            setRefreshing(false);
        }

        try {
            let nextStats = null;
            if (isAdmin) {
                // Admin path: safely proxy CLIProxyAPI through the Go backend.
                const data = await authFetch('/api/admin/cliproxy/usage', { cache: 'no-store' });
                if (!data) throw new Error('No response');
                const usage = data.usage || {};

                const now = Date.now();
                const cutoffMs = period === '24h' ? now - 24*60*60*1000
                    : period === '7d'  ? now - 7*24*60*60*1000
                    : now - 30*24*60*60*1000;

                const allDetails = [];
                const tokenStatsMap = {};
                const modelStatsMap = {};

                for (const [apiKey, apiData] of Object.entries(usage.apis || {})) {
                    if (!tokenStatsMap[apiKey]) tokenStatsMap[apiKey] = { token_name: apiKey, reqs: 0, tokens: 0, failed: 0, latencySum: 0, input_tokens: 0, output_tokens: 0, cached_tokens: 0, cache_write_tokens: 0, reasoning_tokens: 0, cost: 0 };
                    for (const [modelName, modelData] of Object.entries(apiData.models || {})) {
                        if (!modelStatsMap[modelName]) modelStatsMap[modelName] = { model_name: modelName, reqs: 0, tokens: 0, failed: 0, latencySum: 0, input_tokens: 0, output_tokens: 0, cached_tokens: 0, cache_write_tokens: 0, reasoning_tokens: 0, cost: 0 };
                        for (const detail of (modelData.details || [])) {
                            const ts = new Date(detail.timestamp).getTime();
                            if (ts < cutoffMs) continue;
                            const tkn = detail.tokens || {};
                            allDetails.push({
                                created_at: detail.timestamp,
                                model_name: modelName,
                                token_name: apiKey,
                                prompt_tokens: tkn.input_tokens || 0,
                                completion_tokens: tkn.output_tokens || 0,
                                cached_tokens: tkn.cached_tokens || 0,
                                cache_write_tokens: tkn.cache_write_tokens || 0,
                                reasoning_tokens: tkn.reasoning_tokens || 0,
                                tokens: tkn.total_tokens || 0,
                                cost: 0,
                                latency_ms: detail.latency_ms || 0,
                                failed: detail.failed || false,
                                source: detail.source || '',
                                auth_index: detail.auth_index || '',
                            });
                            const t = tokenStatsMap[apiKey]; const m = modelStatsMap[modelName];
                            t.reqs++; t.tokens += tkn.total_tokens||0; t.input_tokens += tkn.input_tokens||0; t.output_tokens += tkn.output_tokens||0; t.cached_tokens += tkn.cached_tokens||0; t.cache_write_tokens += tkn.cache_write_tokens||0; t.reasoning_tokens += tkn.reasoning_tokens||0; t.latencySum += detail.latency_ms||0; if (detail.failed) t.failed++;
                            m.reqs++; m.tokens += tkn.total_tokens||0; m.input_tokens += tkn.input_tokens||0; m.output_tokens += tkn.output_tokens||0; m.cached_tokens += tkn.cached_tokens||0; m.cache_write_tokens += tkn.cache_write_tokens||0; m.reasoning_tokens += tkn.reasoning_tokens||0; m.latencySum += detail.latency_ms||0; if (detail.failed) m.failed++;
                        }
                    }
                }

                const chartMap = {};
                for (const d of allDetails) {
                    const dt = new Date(d.created_at);
                    const bucket = period === '24h'
                        ? `${dt.getFullYear()}-${String(dt.getMonth()+1).padStart(2,'0')}-${String(dt.getDate()).padStart(2,'0')} ${String(dt.getHours()).padStart(2,'0')}:00`
                        : `${dt.getFullYear()}-${String(dt.getMonth()+1).padStart(2,'0')}-${String(dt.getDate()).padStart(2,'0')}`;
                    if (!chartMap[bucket]) chartMap[bucket] = { date: bucket, reqs: 0, tokens: 0, cost: 0, prompt_tokens: 0, completion_tokens: 0, cached_tokens: 0, cache_write_tokens: 0, reasoning_tokens: 0, models: {} };
                    const c = chartMap[bucket];
                    c.reqs++; c.tokens += d.tokens; c.prompt_tokens += d.prompt_tokens; c.completion_tokens += d.completion_tokens; c.cached_tokens += d.cached_tokens; c.cache_write_tokens += d.cache_write_tokens; c.reasoning_tokens += d.reasoning_tokens;
                    if (!c.models[d.model_name]) c.models[d.model_name] = { reqs: 0, tokens: 0 };
                    c.models[d.model_name].reqs++; c.models[d.model_name].tokens += d.tokens;
                }
                const chart_data = Object.values(chartMap).sort((a, b) => a.date.localeCompare(b.date));

                const totalReqs = allDetails.length;
                const failedReqs = allDetails.filter(d => d.failed).length;
                const successReqs = totalReqs - failedReqs;
                const totalTokens = allDetails.reduce((s, d) => s + d.tokens, 0);
                const totalCached = allDetails.reduce((s, d) => s + d.cached_tokens, 0);
                const totalCacheWrite = allDetails.reduce((s, d) => s + d.cache_write_tokens, 0);
                const totalReasoning = allDetails.reduce((s, d) => s + d.reasoning_tokens, 0);
                const latencyTotalMs = allDetails.reduce((s, d) => s + d.latency_ms, 0);
                const avgLatency = totalReqs > 0 ? latencyTotalMs / totalReqs / 1000 : 0;
                const periodSecs = period === '24h' ? 86400 : period === '7d' ? 604800 : 2592000;
                const rpm = totalReqs / (periodSecs / 60);

                allDetails.sort((a, b) => new Date(b.created_at) - new Date(a.created_at));

                const token_stats = Object.values(tokenStatsMap).filter(t => t.reqs > 0).map(t => ({ ...t, avg_latency: t.reqs > 0 ? t.latencySum / t.reqs / 1000 : 0 }));
                const model_stats = Object.values(modelStatsMap).filter(m => m.reqs > 0).map(m => ({ ...m, avg_latency: m.reqs > 0 ? m.latencySum / m.reqs / 1000 : 0 }));

                nextStats = {
                    chart_data,
                    token_stats,
                    model_stats,
                    recent_logs: { logs: allDetails, total: allDetails.length },
                    summary: { totalReqs, successReqs, failedReqs, totalTokens, totalCached, totalCacheWrite, totalReasoning, avgLatency, rpm, totalCost: 0 }
                };
            } else {
                // User path: use the original one-api logs endpoint via authFetch.
                const data = await authFetch(`/api/logs/stats?period=${period}`, { cache: 'no-store' });
                if (data.success) {
                    nextStats = data.data;
                }
            }
            if (nextStats && fetchSeqRef.current === requestId) {
                writeStatsCache(statsCacheKey, nextStats);
                setStats(nextStats);
                setLogsPage(1);
            }
        } catch (e) {
            /* stats error swallowed */;
        } finally {
            if (fetchSeqRef.current === requestId) {
                setLoading(false);
                setRefreshing(false);
            }
        }
    }, [isAdmin, isAuthenticated, period, statsCacheKey]);

    useEffect(() => {
        // Skip unauthenticated fetches to avoid 401 noise and let RequireAuth prompt the user.
        if (!isAuthenticated && !isAdmin) return;
        fetchStats();
    }, [fetchStats, isAuthenticated, isAdmin]);

    /* ── Data Processing ── */
    const { globalData, multiLineData, uniqueModels, summary } = useMemo(() => {
        if (!stats) return { globalData: [], multiLineData: [], uniqueModels: [], summary: {} };
        const raw = stats.chart_data || [];
        const timeMap = {};
        const modelsSet = new Set();
        raw.forEach(r => {
            const mn = r.model_name || 'unknown';
            modelsSet.add(mn);
            if (!timeMap[r.date]) timeMap[r.date] = { date: r.date, reqs: 0, tokens: 0, cost: 0, charged_cost: 0, prompt_tokens: 0, completion_tokens: 0, cached_tokens: 0, cache_write_tokens: 0, reasoning_tokens: 0, models: {} };
            timeMap[r.date].reqs += r.reqs;
            timeMap[r.date].tokens += r.tokens;
            timeMap[r.date].cost += rawCostOf(r);
            timeMap[r.date].charged_cost += chargedCostOf(r);
            timeMap[r.date].prompt_tokens += (r.prompt_tokens || 0);
            timeMap[r.date].completion_tokens += (r.completion_tokens || 0);
            timeMap[r.date].cached_tokens += (r.cached_tokens || 0);
            timeMap[r.date].cache_write_tokens += (r.cache_write_tokens || 0);
            timeMap[r.date].reasoning_tokens += (r.reasoning_tokens || 0);
            timeMap[r.date].models[mn] = { reqs: r.reqs, tokens: r.tokens };
        });
        let expectedDates = [];
        const now = new Date();
        if (period === '24h' || period === '7d') {
            const hours = period === '24h' ? 24 : 7 * 24;
            for (let i = hours - 1; i >= 0; i--) {
                const d = new Date(now.getTime() - i * 60 * 60 * 1000);
                expectedDates.push(`${d.getUTCFullYear()}-${String(d.getUTCMonth() + 1).padStart(2, '0')}-${String(d.getUTCDate()).padStart(2, '0')} ${String(d.getUTCHours()).padStart(2, '0')}:00`);
            }
        } else {
            const days = period === '30d' ? 30 : 7;
            for (let i = days - 1; i >= 0; i--) {
                const d = new Date(now.getTime() - i * 24 * 60 * 60 * 1000);
                expectedDates.push(`${d.getUTCFullYear()}-${String(d.getUTCMonth() + 1).padStart(2, '0')}-${String(d.getUTCDate()).padStart(2, '0')}`);
            }
        }

        const mergedSet = new Set([...expectedDates, ...Object.keys(timeMap)]);
        const sortedDates = Array.from(mergedSet).sort();

        const gData = sortedDates.map(d => {
            const row = timeMap[d] || { reqs: 0, tokens: 0, cost: 0, charged_cost: 0, prompt_tokens: 0, completion_tokens: 0, cached_tokens: 0, cache_write_tokens: 0, reasoning_tokens: 0 };
            return { date: d, reqs: row.reqs, tokens: row.tokens, cost: row.cost, charged_cost: row.charged_cost, prompt_tokens: row.prompt_tokens, completion_tokens: row.completion_tokens, cached_tokens: row.cached_tokens, cache_write_tokens: row.cache_write_tokens, reasoning_tokens: row.reasoning_tokens };
        });
        const mData = sortedDates.map(d => {
            const point = { date: d };
            modelsSet.forEach(m => {
                point[`${m}_reqs`] = timeMap[d]?.models?.[m]?.reqs || 0;
                point[`${m}_tokens`] = timeMap[d]?.models?.[m]?.tokens || 0;
            });
            return point;
        });
        return { globalData: gData, multiLineData: mData, uniqueModels: Array.from(modelsSet), summary: stats.summary };
    }, [stats, period]);

    useEffect(() => {
        if (uniqueModels.length > 0 && selectedModels.length === 0) {
            setSelectedModels(uniqueModels.slice(0, 9));
        }
    }, [uniqueModels]);

    /* ── Sorted token stats ── */
    const sortedTokenStats = useMemo(() => {
        const list = [...(stats?.token_stats || [])].map(r => ({
            ...r,
            raw_cost: rawCostOf(r),
            charged_cost: chargedCostOf(r),
        }));
        const { key, dir } = tokenSort;
        list.sort((a, b) => {
            const diff = key === 'token_name' ? a.token_name.localeCompare(b.token_name) : a[key] - b[key];
            return dir === 'asc' ? diff : -diff;
        });
        return list;
    }, [stats, tokenSort]);

    /* ── Sorted model stats ── */
    const sortedModelStats = useMemo(() => {
        const list = [...(stats?.model_stats || [])].map(r => ({
            ...r,
            raw_cost: rawCostOf(r),
            charged_cost: chargedCostOf(r),
        }));
        const { key, dir } = modelSort;
        list.sort((a, b) => {
            const diff = key === 'model_name' ? a.model_name.localeCompare(b.model_name) : a[key] - b[key];
            return dir === 'asc' ? diff : -diff;
        });
        return list;
    }, [stats, modelSort]);

    /* ── Filtered recent logs ── */
    const filteredLogs = useMemo(() => {
        const logs = stats?.recent_logs?.logs || [];
        return logs.filter(log => {
            if (filterModel && log.model_name !== filterModel) return false;
            if (filterToken && log.token_name !== filterToken) return false;
            return true;
        });
    }, [stats, filterModel, filterToken]);

    const logsTotal = filteredLogs.length;
    const logsTotalPages = Math.ceil(logsTotal / 20);
    const paginatedLogs = useMemo(() => {
        const start = (logsPage - 1) * 20;
        return filteredLogs.slice(start, start + 20);
    }, [filteredLogs, logsPage]);

    if (!stats) {
        // Show separate states for unauthenticated and not-yet-loaded data.
        if (!isAuthenticated && !isAdmin) {
            return <div className="p-12 text-center text-on-surface-variant text-sm">{t('STATS.AUTH_REQUIRED', '登录后查看您的使用统计')}</div>;
        }
        return <StatsLoadingShell t={t} />;
    }

    const formatTokens = (val) => {
        if (!val && val !== 0) return '0';
        let num = typeof val === 'number' ? val : parseFloat(val);
        if (isNaN(num)) return val.toString();
        num = Math.round(num);
        if (num >= 1000000) return (num / 1000000).toFixed(1) + 'M';
        if (num >= 1000) return (num / 1000).toFixed(1) + 'K';
        return num.toString();
    };

    const toggleModel = (model) => {
        if (selectedModels.includes(model)) {
            setSelectedModels(selectedModels.filter(m => m !== model));
        } else if (selectedModels.length < 9) {
            setSelectedModels([...selectedModels, model]);
        }
    };

    const handleSort = (setter, current, key) => {
        if (current.key === key) {
            setter({ key, dir: current.dir === 'asc' ? 'desc' : 'asc' });
        } else {
            setter({ key, dir: key === 'token_name' || key === 'model_name' ? 'asc' : 'desc' });
        }
    };

    const handleExportCsv = () => {
        const logs = filteredLogs;
        if (!logs.length) return;
        const header = [
            t('STATS.TIMESTAMP', '时间'),
            t('STATS.MODEL_NAME', '模型'),
            t('STATS.TOKEN_SOURCE', '令牌来源'),
            t('STATS.INPUT_TOKENS', '输入Tokens'),
            t('STATS.OUTPUT_TOKENS', '输出Tokens'),
            t('STATS.CACHED_TOKENS', '缓存读Tokens'),
            t('STATS.CACHE_WRITE_TOKENS', '缓存写Tokens'),
            t('STATS.CACHE_WRITE_5M_TOKENS', '缓存写5mTokens'),
            t('STATS.CACHE_WRITE_1H_TOKENS', '缓存写1hTokens'),
            t('STATS.REASONING_TOKENS', '思考Tokens'),
            t('STATS.MEDIA_USAGE', '媒体用量'),
            t('STATS.RAW_COST_USD', '原始成本($)'),
            t('STATS.CHARGED_COST_USD', '套餐扣减($)'),
            t('STATS.MODEL_WEIGHT', '模型权重'),
            t('STATS.BILLING_RULES_VERSION', '规则版本'),
        ];
        const rows = logs.map(l => [
            l.created_at, l.model_name, l.token_name,
            l.prompt_tokens, l.completion_tokens, l.cached_tokens || 0, l.cache_write_tokens || 0, l.cache_write_5m_tokens || 0, l.cache_write_1h_tokens || 0, l.reasoning_tokens || 0,
            formatUsageLinesSummary(l, formatMeterCost),
            rawCostOf(l), chargedCostOf(l), l.model_weight || 1, l.billing_rules_version || ''
        ].map(v => `"${String(v ?? '').replace(/"/g, '""')}"`).join(','));
        const csv = [header.join(','), ...rows].join('\n');
        const blob = new Blob([csv], { type: 'text/csv;charset=utf-8' });
        const url = URL.createObjectURL(blob);
        const a = document.createElement('a');
        a.href = url;
        a.download = `usage-events-${new Date().toISOString().replace(/[:.]/g, '-')}.csv`;
        a.click();
        URL.revokeObjectURL(url);
    };

    /* ── Unique values for filters ── */
    const allLogs = stats?.recent_logs?.logs || [];
    const logModels = [...new Set(allLogs.map(l => l.model_name).filter(Boolean))];
    const logTokens = [...new Set(allLogs.map(l => l.token_name).filter(Boolean))];

    return (
        <div className="w-full mb-8">
            {/* Header with title, subtitle, and period chips. */}
            <div className="flex flex-col md:flex-row md:items-end md:justify-between mb-8 gap-3">
                <div>
                    <h1 className="text-3xl md:text-[40px] font-semibold tracking-tight text-on-surface flex items-center gap-3">
                        <BarChart2 size={28} className="text-primary" />
                        {t('STATS.TITLE')}
                    </h1>
                    <p className="text-on-surface-variant mt-2 text-sm md:text-base max-w-2xl">
                        {t('STATS.SUBTITLE', '按时间窗口聚合的请求、成本与失败率，支持模型 / Token 维度筛选')}
                    </p>
                </div>
                <div className="flex items-center gap-3 bg-surface-container/40 p-1 rounded-control">
                    {['24h', '7d', '30d'].map(p => (
                        <button key={p} onClick={() => {
                            if (p === period) return;
                            setSelectedModels([]);
                            setPeriod(p);
                        }}
                            className={`px-4 py-1.5 text-xs font-semibold rounded-control ${period === p ? 'bg-surface-variant text-on-surface ' : 'text-on-surface-variant hover:text-on-surface'}`}>
                            {p === '24h' ? t('STATS.RANGE_24H') : p === '7d' ? t('STATS.RANGE_7D') : t('STATS.RANGE_30D')}
                        </button>
                    ))}
                    <button onClick={() => fetchStats({ force: true })} disabled={loading || refreshing} className="p-1.5 text-on-surface-variant hover:text-white cursor-pointer mr-1 disabled:opacity-50 disabled:cursor-wait">
                        <RefreshCw size={14} className={loading || refreshing ? 'animate-spin' : ''} />
                    </button>
                </div>
            </div>

            {/* Stat Cards */}
            <div className="grid grid-cols-1 lg:grid-cols-2 gap-4 mb-4">
                <StatCard
                    title={t('STATS.TOTAL_REQS')}
                    value={(summary.totalReqs || 0).toLocaleString()}
                    metaNode={
                        <div className="flex flex-col gap-0.5 mt-2">
                           <span className="text-xs text-on-surface-variant flex items-center gap-2">
                               <span className="w-1.5 h-1.5 rounded-full bg-success"></span>{t('STATS.SUCCESS_REQS', '成功请求')}: {(summary.successReqs ?? 0).toLocaleString()}
                               <span className="w-1.5 h-1.5 rounded-full bg-error ml-2"></span>{t('STATS.FAILED_REQS', '失败请求')}: {(summary.failedReqs ?? 0).toLocaleString()}
                               <span className="ml-2">
                                   {t('STATS.AVG_LATENCY', '平均延迟')}: {(summary.totalReqs > 0 && typeof summary.avgLatency === 'number')
                                       ? t('STATS.SECONDS_VALUE', '{{value}}秒', { value: summary.avgLatency.toFixed(1) })
                                       : '-'}
                               </span>
                           </span>
                        </div>
                    }
                    data={globalData} dataKey="reqs" color="#8b8680" icon={Activity} bgClass="bg-surface-variant/5"
                />
                <StatCard
                    title={t('STATS.TOTAL_TOKENS')}
                    value={formatTokens(summary.totalTokens)}
                    metaNode={
                        <div className="flex flex-col gap-0.5 mt-2 transition-opacity opacity-80 hover:opacity-100">
                            <span className="text-xs text-on-surface-variant flex items-center gap-2"><span className="w-1.5 h-1.5 rounded-full bg-primary"></span>{t('STATS.CACHED_TOKENS', '缓存读 Tokens')}: {formatTokens(summary.totalCached)}</span>
                            <span className="text-xs text-on-surface-variant flex items-center gap-2"><span className="w-1.5 h-1.5 rounded-full bg-primary"></span>{t('STATS.REASONING_TOKENS', '思考 Tokens')}: {formatTokens(summary.totalReasoning)}</span>
                        </div>
                    }
                    data={globalData} dataKey="tokens" color="#8b5cf6" icon={Zap} bgClass="bg-primary/5"
                />
            </div>
            <div className="grid grid-cols-1 sm:grid-cols-3 gap-4 mb-6">
                <StatCard
                    title={t('STATS.RPM')}
                    value={typeof summary.rpm === 'number' ? summary.rpm.toFixed(2) : '0.00'}
                    metaNode={<span className="text-xs text-on-surface-variant opacity-70">{t('STATS.RPM_DESC')}</span>}
                    data={globalData} dataKey="reqs" color="#22c55e" icon={Activity} bgClass="bg-success/5"
                />
                <StatCard
                    title={t('STATS.TPM')}
                    value={typeof summary.tpm === 'number' ? Math.round(summary.tpm).toLocaleString() : '0'}
                    metaNode={<span className="text-xs text-on-surface-variant opacity-70">{t('STATS.TPM_DESC')}</span>}
                    data={globalData} dataKey="tokens" color="#f97316" icon={Zap} bgClass="bg-warning/5"
                />
                <StatCard
                    title={t('STATS.CHARGED_COST')}
                    value={formatMeterCost(summary.totalChargedCost ?? summary.total_charged_cost ?? summary.totalCost)}
                    metaNode={(
                        <div className="text-xs text-on-surface-variant opacity-70">
                            <div>{t('STATS.CHARGED_COST_DESC')}</div>
                            {costsDiffer(summary.totalCost, summary.totalChargedCost ?? summary.total_charged_cost ?? summary.totalCost) && (
                                <div>{t('STATS.RAW_COST_SHORT', 'raw')} {formatMeterCost(summary.totalCost)}</div>
                            )}
                        </div>
                    )}
                    data={globalData} dataKey="charged_cost" color="#f59e0b" icon={Coins} bgClass="bg-warning/5"
                />
            </div>

            {/* Chart Line Selector */}
            {uniqueModels.length > 0 && (
                <div className="bg-surface border border-outline-variant rounded-overlay p-4 mb-4 ">
                    <div className="flex items-center justify-between mb-4">
                        <h3 className="text-sm font-semibold text-on-surface-variant">{t('STATS.CHART_LINES')}</h3>
                        <span className="text-xs text-on-surface-variant font-mono">{selectedModels.length} / 9</span>
                    </div>
                    <div className="flex flex-wrap gap-2">
                        {uniqueModels.map((m) => {
                            const isSelected = selectedModels.includes(m);
                            const colorIndex = selectedModels.indexOf(m);
                            const activeColor = colorIndex !== -1 ? CHART_COLORS[colorIndex % CHART_COLORS.length] : '#1c1d22';
                            return (
                                <button key={m} onClick={() => toggleModel(m)}
                                    style={isSelected ? { borderColor: activeColor, backgroundColor: `${activeColor}15` } : {}}
                                    className={`flex items-center gap-2 pl-2 pr-3 py-1.5 border rounded-control ${isSelected ? 'text-on-surface' : 'border-outline-variant text-on-surface-variant hover:border-outline'}`}>
                                    <div className="w-4 h-4 rounded-control-[4px] flex items-center justify-center" style={{ backgroundColor: isSelected ? activeColor : 'transparent', border: isSelected ? 'none' : '1px solid #444' }}>
                                        {isSelected && <Check size={12} className="text-surface" strokeWidth={4} />}
                                    </div>
                                    <span className="text-xs font-mono">{m}</span>
                                </button>
                            );
                        })}
                    </div>
                </div>
            )}

            {/* Service Health Monitor */}
            <HealthMonitor logs={stats?.recent_logs?.logs || []} summary={summary} />

            {/* Trend Charts */}
            <div className="grid grid-cols-1 2xl:grid-cols-2 gap-4 mb-6">
                <div className="bg-surface border border-outline-variant rounded-overlay p-6 min-h-[400px]">
                    <h3 className="text-sm font-semibold text-on-surface-variant mb-6">{t('STATS.REQ_TREND')}</h3>
                    <div className="w-full h-[300px]">
                        <ResponsiveContainer width="100%" height="100%">
                            <LineChart data={multiLineData || []}>
                                <CartesianGrid strokeDasharray="3 3" stroke="#2b2b2b" vertical={false} />
                                <XAxis dataKey="date" stroke="#6b7280" fontSize={10} tickMargin={10} minTickGap={20} />
                                <YAxis stroke="#6b7280" fontSize={10} tickCount={6} axisLine={false} tickLine={false} />
                                <Tooltip content={<CustomTooltip />} />
                                {selectedModels.map((m, i) => (
                                    <Line isAnimationActive={false} key={m} type="monotone" dataKey={`${m}_reqs`} name={m} stroke={CHART_COLORS[i % CHART_COLORS.length]} strokeWidth={2} dot={false} activeDot={{ r: 4, strokeWidth: 0 }} />
                                ))}
                            </LineChart>
                        </ResponsiveContainer>
                    </div>
                </div>
                <div className="bg-surface border border-outline-variant rounded-overlay p-6 min-h-[400px]">
                    <h3 className="text-sm font-semibold text-on-surface-variant mb-6">{t('STATS.TOKENS_TREND')}</h3>
                    <div className="w-full h-[300px]">
                        <ResponsiveContainer width="100%" height="100%">
                            <AreaChart data={multiLineData || []}>
                                <CartesianGrid strokeDasharray="3 3" stroke="#2b2b2b" vertical={false} />
                                <XAxis dataKey="date" stroke="#6b7280" fontSize={10} tickMargin={10} minTickGap={20} />
                                <YAxis stroke="#6b7280" fontSize={10} tickCount={6} axisLine={false} tickLine={false} tickFormatter={formatTokens} />
                                <Tooltip content={<CustomTooltip />} />
                                {selectedModels.map((m, i) => (
                                    <Area isAnimationActive={false} key={m} type="monotone" dataKey={`${m}_tokens`} name={m} stroke={CHART_COLORS[i % CHART_COLORS.length]} fill={CHART_COLORS[i % CHART_COLORS.length]} fillOpacity={0.1} strokeWidth={2} activeDot={{ r: 4, strokeWidth: 0 }} />
                                ))}
                            </AreaChart>
                        </ResponsiveContainer>
                    </div>
                </div>
            </div>

            {/* Token Distribution and Cost Charts */}
            <div className="grid grid-cols-1 gap-4 mb-6">
                <div className="bg-surface border border-outline-variant rounded-overlay p-6 ">
                    <div className="flex justify-between items-center mb-6">
                        <h3 className="text-sm font-semibold text-on-surface-variant">{t('STATS.TOKEN_DISTRIBUTION', 'Token 类型分布')}</h3>
                    </div>
                    <div className="w-full h-[300px]">
                        <ResponsiveContainer width="100%" height="100%">
                            <AreaChart data={globalData || []}>
                                <CartesianGrid strokeDasharray="3 3" stroke="#2b2b2b" vertical={false} />
                                <XAxis dataKey="date" stroke="#6b7280" fontSize={10} tickMargin={10} minTickGap={20} />
                                <YAxis stroke="#6b7280" fontSize={10} tickCount={6} axisLine={false} tickLine={false} tickFormatter={formatTokens} />
                                <Tooltip content={<CustomTooltip />} />
                                <Area isAnimationActive={false} type="monotone" dataKey="prompt_tokens" name={t('STATS.INPUT_TOKENS', '输入 Tokens')} stroke="#9ca3af" fill="#9ca3af" fillOpacity={0.1} strokeWidth={2} activeDot={{ r: 4, strokeWidth: 0 }} />
                                <Area isAnimationActive={false} type="monotone" dataKey="completion_tokens" name={t('STATS.OUTPUT_TOKENS', '输出 Tokens')} stroke="#10b981" fill="#10b981" fillOpacity={0.1} strokeWidth={2} activeDot={{ r: 4, strokeWidth: 0 }} />
                                <Area isAnimationActive={false} type="monotone" dataKey="cached_tokens" name={t('STATS.CACHED_TOKENS', '缓存读 Tokens')} stroke="#f59e0b" fill="#f59e0b" fillOpacity={0.1} strokeWidth={2} activeDot={{ r: 4, strokeWidth: 0 }} />
                                <Area isAnimationActive={false} type="monotone" dataKey="cache_write_tokens" name={t('STATS.CACHE_WRITE_TOKENS', '缓存写 Tokens')} stroke="#f97316" fill="#f97316" fillOpacity={0.1} strokeWidth={2} activeDot={{ r: 4, strokeWidth: 0 }} />
                                <Area isAnimationActive={false} type="monotone" dataKey="reasoning_tokens" name={t('STATS.REASONING_TOKENS', '思考 Tokens')} stroke="#8b5cf6" fill="#8b5cf6" fillOpacity={0.1} strokeWidth={2} activeDot={{ r: 4, strokeWidth: 0 }} />
                            </AreaChart>
                        </ResponsiveContainer>
                    </div>
                </div>

                <div className="bg-surface border border-outline-variant rounded-overlay p-6 ">
                    <div className="flex justify-between items-center mb-6">
                        <h3 className="text-sm font-semibold text-on-surface-variant">{t('STATS.COST_CHART', '扣减统计')}</h3>
                    </div>
                    <div className="w-full h-[300px]">
                        <ResponsiveContainer width="100%" height="100%">
                            <LineChart data={globalData || []}>
                                <CartesianGrid strokeDasharray="3 3" stroke="#2b2b2b" vertical={false} />
                                <XAxis dataKey="date" stroke="#6b7280" fontSize={10} tickMargin={10} minTickGap={20} />
                                <YAxis stroke="#6b7280" fontSize={10} tickCount={6} axisLine={false} tickLine={false} tickFormatter={formatMeterCost} />
                                <Tooltip content={<CustomTooltip formatValue={formatMeterCost} />} />
                                <Line isAnimationActive={false} type="monotone" dataKey="charged_cost" name={t('STATS.CHARGED_COST_SERIES', '套餐扣减')} stroke="#f59e0b" strokeWidth={2} dot={{ r: 2, fill: '#f59e0b', strokeWidth: 0 }} activeDot={{ r: 4, strokeWidth: 0 }} />
                                <Line isAnimationActive={false} type="monotone" dataKey="cost" name={t('STATS.RAW_COST_SERIES', '原始成本')} stroke="#94a3b8" strokeWidth={1.5} strokeDasharray="4 4" dot={false} activeDot={{ r: 3, strokeWidth: 0 }} />
                            </LineChart>
                        </ResponsiveContainer>
                    </div>
                </div>
            </div>

            {/* API Details (by token_name) + Model Stats */}
            <div className="grid grid-cols-1 xl:grid-cols-2 gap-4 mb-6">
                {/* API Details Card */}
                <div className="bg-surface border border-outline-variant rounded-overlay p-6 ">
                    <h3 className="text-sm font-semibold text-on-surface-variant mb-4">{t('STATS.API_DETAILS')}</h3>
                    {sortedTokenStats.length > 0 ? (
                        <div className="overflow-x-auto">
                            <table className="w-full min-w-[500px]">
                                <thead><tr className="border-b border-outline-variant">
                                    <SortHeader label={t('STATS.TOKEN_SOURCE')} sortKey="token_name" currentSort={tokenSort} onSort={(k) => handleSort(setTokenSort, tokenSort, k)} />
                                    <SortHeader label={t('STATS.REQUESTS_COUNT')} sortKey="reqs" currentSort={tokenSort} onSort={(k) => handleSort(setTokenSort, tokenSort, k)} />
                                    <SortHeader label={t('STATS.TOKENS_COUNT')} sortKey="tokens" currentSort={tokenSort} onSort={(k) => handleSort(setTokenSort, tokenSort, k)} />
                                    <SortHeader label={t('STATS.CHARGED_COST')} sortKey="charged_cost" currentSort={tokenSort} onSort={(k) => handleSort(setTokenSort, tokenSort, k)} />
                                </tr></thead>
                                <tbody>
                                    {sortedTokenStats.map((row, i) => (
                                        <tr key={i} className="border-b border-outline-variant/30 hover:bg-surface-variant/50">
                                            <td className="px-4 py-3 text-xs text-on-surface-variant font-mono">{row.token_name || '-'}</td>
                                            <td className="px-4 py-3 text-xs text-on-surface">{row.reqs.toLocaleString()}</td>
                                            <td className="px-4 py-3 text-xs text-on-surface">{formatTokens(row.tokens)}</td>
                                            <td className="px-4 py-3 text-xs text-on-surface">{renderCostPair(row)}</td>
                                        </tr>
                                    ))}
                                </tbody>
                            </table>
                        </div>
                    ) : (
                        <div className="text-xs text-outline-variant py-8 text-center">{t('STATS.NO_DATA')}</div>
                    )}
                </div>

                {/* Model Stats Card */}
                <div className="bg-surface border border-outline-variant rounded-overlay p-6 ">
                    <h3 className="text-sm font-semibold text-on-surface-variant mb-4">{t('STATS.MODEL_STATS')}</h3>
                    {sortedModelStats.length > 0 ? (
                        <div className="overflow-x-auto">
                            <table className="w-full min-w-[500px]">
                                <thead><tr className="border-b border-outline-variant">
                                    <SortHeader label={t('STATS.MODEL_NAME')} sortKey="model_name" currentSort={modelSort} onSort={(k) => handleSort(setModelSort, modelSort, k)} />
                                    <SortHeader label={t('STATS.REQUESTS_COUNT')} sortKey="reqs" currentSort={modelSort} onSort={(k) => handleSort(setModelSort, modelSort, k)} />
                                    <SortHeader label={t('STATS.TOKENS_COUNT')} sortKey="tokens" currentSort={modelSort} onSort={(k) => handleSort(setModelSort, modelSort, k)} />
                                    <SortHeader label={t('STATS.CHARGED_COST')} sortKey="charged_cost" currentSort={modelSort} onSort={(k) => handleSort(setModelSort, modelSort, k)} />
                                </tr></thead>
                                <tbody>
                                    {sortedModelStats.map((row, i) => (
                                        <tr key={i} className="border-b border-outline-variant/30 hover:bg-surface-variant/50">
                                            <td className="px-4 py-3 text-xs text-on-surface-variant font-mono">{row.model_name || '-'}</td>
                                            <td className="px-4 py-3 text-xs text-on-surface">{row.reqs.toLocaleString()}</td>
                                            <td className="px-4 py-3 text-xs text-on-surface">{formatTokens(row.tokens)}</td>
                                            <td className="px-4 py-3 text-xs text-on-surface">{renderCostPair(row)}</td>
                                        </tr>
                                    ))}
                                </tbody>
                            </table>
                        </div>
                    ) : (
                        <div className="text-xs text-outline-variant py-8 text-center">{t('STATS.NO_DATA')}</div>
                    )}
                </div>
            </div>

            {/* Request Events Details */}
            <div className="bg-surface border border-outline-variant rounded-overlay p-6 ">
                <div className="flex flex-col sm:flex-row items-start sm:items-center justify-between mb-4 gap-3">
                    <h3 className="text-sm font-semibold text-on-surface-variant">{t('STATS.REQUEST_EVENTS')}</h3>
                    <div className="flex items-center gap-2 flex-wrap">
                        <button onClick={() => { setFilterModel(''); setFilterToken(''); setLogsPage(1); }} className="text-xs text-on-surface-variant hover:text-on-surface px-2 py-1 rounded-control border border-outline-variant hover:border-outline">{t('STATS.CLEAR_FILTERS')}</button>
                        <button onClick={handleExportCsv} disabled={!filteredLogs.length} className="text-xs text-on-surface-variant hover:text-on-surface px-2 py-1 rounded-control border border-outline-variant hover:border-outline disabled:opacity-30 flex items-center gap-1"><Download size={12} />{t('STATS.EXPORT_CSV')}</button>
                    </div>
                </div>

                {/* Filters */}
                <div className="flex flex-wrap gap-4 mb-4">
                    <div className="flex items-center gap-2">
                        <span className="text-xs text-on-surface-variant">{t('STATS.MODEL_NAME')}</span>
                        <select value={filterModel} onChange={e => { setFilterModel(e.target.value); setLogsPage(1); }} className="bg-surface-container-high border border-outline-variant text-xs text-on-surface-variant rounded-control px-2 py-1.5 outline-none">
                            <option value="">{t('STATS.FILTER_ALL')}</option>
                            {logModels.map(m => <option key={m} value={m}>{m}</option>)}
                        </select>
                    </div>
                    <div className="flex items-center gap-2">
                        <span className="text-xs text-on-surface-variant">{t('STATS.TOKEN_SOURCE')}</span>
                        <select value={filterToken} onChange={e => { setFilterToken(e.target.value); setLogsPage(1); }} className="bg-surface-container-high border border-outline-variant text-xs text-on-surface-variant rounded-control px-2 py-1.5 outline-none">
                            <option value="">{t('STATS.FILTER_ALL')}</option>
                            {logTokens.map(tk => <option key={tk} value={tk}>{tk}</option>)}
                        </select>
                    </div>
                </div>

                <div className="text-xs text-on-surface-variant mb-3">{filteredLogs.length} {t('STATS.EVENTS_COUNT')}</div>

                {filteredLogs.length > 0 ? (
                    <div className="overflow-x-auto">
                        <table className="w-full min-w-[1040px]">
                            <thead><tr className="border-b border-outline-variant">
                                <th className="text-left px-4 py-3 text-xs font-semibold text-on-surface-variant whitespace-nowrap">{t('STATS.TIMESTAMP')}</th>
                                <th className="text-left px-4 py-3 text-xs font-semibold text-on-surface-variant whitespace-nowrap">{t('STATS.STATUS', '结果')}</th>
                                <th className="text-left px-4 py-3 text-xs font-semibold text-on-surface-variant whitespace-nowrap">{t('STATS.MODEL_NAME')}</th>
                                <th className="text-left px-4 py-3 text-xs font-semibold text-on-surface-variant whitespace-nowrap">{t('STATS.TOKEN_SOURCE')}</th>
                                <th className="text-left px-4 py-3 text-xs font-semibold text-on-surface-variant whitespace-nowrap">{t('STATS.IP', '来源 IP')}</th>
                                <th className="text-left px-4 py-3 text-xs font-semibold text-on-surface-variant whitespace-nowrap">{t('STATS.LATENCY', '延迟')}</th>
                                <th className="text-left px-4 py-3 text-xs font-semibold text-on-surface-variant whitespace-nowrap">{t('STATS.INPUT_TOKENS')}</th>
                                <th className="text-left px-4 py-3 text-xs font-semibold text-on-surface-variant whitespace-nowrap">{t('STATS.OUTPUT_TOKENS')}</th>
                                <th className="text-left px-4 py-3 text-xs font-semibold text-on-surface-variant whitespace-nowrap">{t('STATS.REASONING_TOKENS')}</th>
                                <th className="text-left px-4 py-3 text-xs font-semibold text-on-surface-variant whitespace-nowrap" title={t('STATS.USAGE_METADATA_HINT', '来自 usage metadata，不是本平台会话缓存')}>{t('STATS.CACHED_TOKENS', '缓存读 Tokens')}</th>
                                <th className="text-left px-4 py-3 text-xs font-semibold text-on-surface-variant whitespace-nowrap" title={t('STATS.USAGE_METADATA_HINT', '来自 usage metadata，不是本平台会话缓存')}>{t('STATS.CACHE_WRITE_TOKENS', '缓存写 Tokens')}</th>
                                <th className="text-left px-4 py-3 text-xs font-semibold text-on-surface-variant whitespace-nowrap">{t('STATS.MEDIA_USAGE', '媒体用量')}</th>
                                <th className="text-left px-4 py-3 text-xs font-semibold text-on-surface-variant whitespace-nowrap">{t('STATS.TOTAL_TOKENS', '总Token数')}</th>
                                <th className="text-left px-4 py-3 text-xs font-semibold text-on-surface-variant whitespace-nowrap">{t('STATS.CHARGED_COST')}</th>
                            </tr></thead>
                            <tbody>
                                {paginatedLogs.map((log) => (
                                    <tr
                                        key={log.id}
                                        onClick={() => setSelectedLog(log)}
                                        className="border-b border-outline-variant/30 hover:bg-surface-variant/50 cursor-pointer transition"
                                        title={t('STATS.ROW_CLICK_HINT', '点击查看完整请求详情')}
                                    >
                                        <td className="px-4 py-3 text-xs text-on-surface-variant font-mono whitespace-nowrap">{new Date(log.created_at).toLocaleString()}</td>
                                        <td className="px-4 py-3 text-xs font-mono">
                                            <span className={`px-2 py-0.5 rounded-control flex items-center justify-center w-max text-[10px] font-bold ${log.status >= 200 && log.status < 300 ? 'bg-success/20 text-success' : 'bg-error/20 text-error'}`}>
                                                {(log.status >= 200 && log.status < 300) ? t('STATS.SUCCESS', '成功') : (log.status || t('STATS.FAIL', '失败'))}
                                            </span>
                                        </td>
                                        <td className="px-4 py-3 text-xs text-on-surface-variant font-mono">{log.model_name}</td>
                                        <td className="px-4 py-3 text-xs text-on-surface-variant font-mono">{log.token_name || '-'}</td>
                                        <td className="px-4 py-3 text-xs text-outline-variant font-mono">{log.ip_address || '-'}</td>
                                        <td className="px-4 py-3 text-xs text-on-surface-variant font-mono">{formatLatency(log.latency ?? log.latency_ms)}</td>
                                        <td className="px-4 py-3 text-xs text-on-surface">{(log.prompt_tokens || 0).toLocaleString()}</td>
                                        <td className="px-4 py-3 text-xs text-on-surface">{(log.completion_tokens || 0).toLocaleString()}</td>
                                        <td className="px-4 py-3 text-xs text-primary">{(log.reasoning_tokens || 0).toLocaleString()}</td>
                                        <td className="px-4 py-3 text-xs text-primary">{(log.cached_tokens || 0).toLocaleString()}</td>
                                        <td className="px-4 py-3 text-xs text-warning">
                                            <div>{(log.cache_write_tokens || 0).toLocaleString()}</div>
                                            {((log.cache_write_5m_tokens || 0) > 0 || (log.cache_write_1h_tokens || 0) > 0) && (
                                                <div className="text-[10px] text-warning/70 whitespace-nowrap">
                                                    5m {(log.cache_write_5m_tokens || 0).toLocaleString()} · 1h {(log.cache_write_1h_tokens || 0).toLocaleString()}
                                                </div>
                                            )}
                                        </td>
                                        <td className="px-4 py-3 text-xs text-on-surface"><UsageLinesCell row={log} formatMeterCost={formatMeterCost} /></td>
                                        <td className="px-4 py-3 text-xs text-on-surface font-mono">{((log.prompt_tokens || 0) + (log.completion_tokens || 0)).toLocaleString()}</td>
                                        <td className="px-4 py-3 text-xs text-on-surface">{renderCostPair(log, { showWeight: true })}</td>
                                    </tr>
                                ))}
                            </tbody>
                        </table>
                    </div>
                ) : (
                    <div className="text-xs text-outline-variant py-8 text-center">{t('STATS.NO_DATA')}</div>
                )}

                {/* Pagination */}
                {logsTotalPages > 1 && (
                    <div className="flex items-center justify-center gap-3 mt-4 pt-4 border-t border-outline-variant">
                        <button onClick={() => setLogsPage(p => Math.max(1, p - 1))} disabled={logsPage <= 1} className="p-1.5 rounded-control border border-outline-variant text-on-surface-variant hover:text-white hover:border-outline-variant disabled:opacity-30"><ChevronLeft size={16} /></button>
                        <span className="text-xs text-on-surface-variant font-mono">{logsPage} / {logsTotalPages}</span>
                        <button onClick={() => setLogsPage(p => Math.min(logsTotalPages, p + 1))} disabled={logsPage >= logsTotalPages} className="p-1.5 rounded-control border border-outline-variant text-on-surface-variant hover:text-white hover:border-outline-variant disabled:opacity-30"><ChevronRight size={16} /></button>
                    </div>
                )}
            </div>

            {/* 请求详情 Drawer —— 跟 admin 同款侧栏，但只渲安全字段。 */}
            <Drawer
                open={!!selectedLog}
                onClose={() => setSelectedLog(null)}
                title={selectedLog ? `${t('STATS.DETAIL_TITLE_PREFIX', '请求')} #${selectedLog.id}` : ''}
                description={selectedLog ? new Date(selectedLog.created_at).toLocaleString() : ''}
                size="lg"
            >
                {selectedLog && (
                    <UserLogDetail
                        log={selectedLog}
                        formatMeterCost={formatMeterCost}
                        t={t}
                    />
                )}
            </Drawer>
        </div>
    );
};

/**
 * UserLogDetail — 用户视角的请求详情面板。
 *
 * 设计原则（用户反馈"改成 admin 同款但别暴露隐私"）：
 *   - 只渲染 PublicApiLog DTO 里"用户能看懂 + 不泄漏平台内部"的字段
 *   - 故意不展示：raw_cost / health_multiplier / billing_rules_version /
 *     fallback_reason / precheck_* / block_reason —— 这些要么是平台内部计费
 *     状态、要么会泄上游 channel 健康度
 *   - upstream_provider / upstream_auth_index 这类字段后端 PublicApiLog DTO
 *     已经直接 drop 掉（见 database/public_log_dto.go），UI 层无需再防
 *   - 展示：错误（如失败）/ 基本信息 / 计费摘要 / Token 明细 / 上游计量行
 */
const UserLogDetail = ({ log, formatMeterCost, t }) => {
    const isError = !(log.status >= 200 && log.status < 300);
    const totalTokens = (log.prompt_tokens || 0) + (log.completion_tokens || 0);
    const lines = usageLinesOf(log);
    const modelMigrated = log.requested_model && log.served_model && log.requested_model !== log.served_model;
    return (
        <div className="space-y-5">
            {/* 状态条 */}
            <div className={`rounded-overlay border px-4 py-3 ${
                isError ? 'bg-error/10 border-error/30' : 'bg-success/10 border-success/30'
            }`}>
                <div className={`text-sm font-semibold ${isError ? 'text-error' : 'text-success'}`}>
                    {isError
                        ? t('STATS.DETAIL_STATUS_FAIL', { status: log.status, defaultValue: 'HTTP {{status}} · 请求失败' })
                        : t('STATS.DETAIL_STATUS_OK', { status: log.status, defaultValue: 'HTTP {{status}} · 请求成功' })}
                </div>
                {log.error_type && (
                    <div className="text-xs text-on-surface-variant mt-1 break-all font-mono">
                        {log.error_type}
                    </div>
                )}
            </div>

            {/* 基本信息 */}
            <Section flat noPadding>
                <UserLogField
                    label={t('STATS.DETAIL_MODEL_LABEL', '模型')}
                    mono
                    value={modelMigrated
                        ? `${log.requested_model} → ${log.served_model}`
                        : (log.served_model || log.model_name || '-')}
                />
                <UserLogField
                    label={t('STATS.DETAIL_PATH', '接口路径')}
                    mono
                    value={log.request_path || '-'}
                />
                <UserLogField
                    label={t('STATS.DETAIL_TOKEN_SOURCE', 'Token Source')}
                    mono
                    value={log.token_name || '-'}
                />
                <UserLogField
                    label={t('STATS.DETAIL_IP', '客户端 IP')}
                    mono
                    value={log.ip_address || '-'}
                />
                <UserLogField
                    label={t('STATS.DETAIL_LATENCY', '延迟')}
                    value={formatLatency(log.latency ?? log.latency_ms)}
                />
                {log.fallback_user_opt_in && (
                    <UserLogField
                        label={t('STATS.DETAIL_FALLBACK_LABEL', 'Fallback')}
                        value={t('STATS.DETAIL_FALLBACK_OPT_IN', '已开启自动降级')}
                    />
                )}
            </Section>

            {/* 计费摘要 —— 只暴露最终扣减 + 倍率，不暴露 raw / 营收 / 规则版本 */}
            <Section title={t('STATS.DETAIL_SECTION_BILLING', '计费摘要')} flat>
                <div className="grid grid-cols-2 gap-x-4">
                    <UserLogField
                        label={t('STATS.DETAIL_CHARGED_COST', '实际扣减')}
                        mono
                        highlight
                        value={formatMeterCost(log.charged_cost ?? log.cost ?? 0)}
                    />
                    <UserLogField
                        label={t('STATS.DETAIL_MODEL_WEIGHT', '模型权重')}
                        mono
                        value={`×${Number(log.model_weight || 1).toFixed(2)}`}
                    />
                </div>
            </Section>

            {/* Token 明细 */}
            <Section title={t('STATS.DETAIL_SECTION_TOKEN', 'Token 明细')} flat>
                <div className="grid grid-cols-2 gap-x-4">
                    <UserLogField label={t('STATS.DETAIL_INPUT', '输入')} mono value={(log.prompt_tokens || 0).toLocaleString()} />
                    <UserLogField label={t('STATS.DETAIL_OUTPUT', '输出')} mono value={(log.completion_tokens || 0).toLocaleString()} />
                    <UserLogField label={t('STATS.DETAIL_REASONING', '思考 (reasoning)')} mono value={(log.reasoning_tokens || 0).toLocaleString()} />
                    <UserLogField label={t('STATS.DETAIL_CACHE_READ', '缓存读')} mono value={(log.cached_tokens || 0).toLocaleString()} />
                    <UserLogField label={t('STATS.DETAIL_CACHE_WRITE', '缓存写')} mono value={(log.cache_write_tokens || 0).toLocaleString()} />
                    <UserLogField label={t('STATS.DETAIL_TOTAL_TOKENS', '总 Token')} mono value={totalTokens.toLocaleString()} highlight />
                </div>
            </Section>

            {/* 上游计量明细（有就显示） */}
            {lines.length > 0 && (
                <Section
                    title={t('STATS.DETAIL_SECTION_USAGE', '上游计量明细')}
                    flat
                    sub={t('STATS.DETAIL_SECTION_USAGE_SUB', '逐行记录 token / 图片 / 视频用量与单价')}
                >
                    <div className="space-y-2">
                        {lines.map((line) => (
                            <div
                                key={line.id || `${line.unit}-${line.direction}-${line.quantity}`}
                                className="rounded-control border border-outline-variant bg-surface-container/40 px-3 py-2"
                            >
                                <div className="flex items-center justify-between gap-3">
                                    <span className="text-xs text-on-surface font-mono truncate" title={formatUsageLine(line, formatMeterCost)}>
                                        {formatUsageLine(line, formatMeterCost)}
                                    </span>
                                    <span className="text-[10px] text-on-surface-variant shrink-0">{line.direction || '-'}</span>
                                </div>
                            </div>
                        ))}
                    </div>
                </Section>
            )}

            {/* 错误详情（仅失败请求） */}
            {(log.error_type || log.error_message) && (
                <Section title={t('STATS.DETAIL_SECTION_ERROR', '错误详情')} flat>
                    <UserLogField label="error_type" mono value={log.error_type || '-'} />
                    {log.error_message && (
                        <div className="mt-2 text-[11px] text-error font-mono whitespace-pre-wrap break-all bg-error/5 border border-error/20 rounded-control p-2">
                            {log.error_message}
                        </div>
                    )}
                </Section>
            )}
        </div>
    );
};

const UserLogField = ({ label, value, mono, highlight }) => (
    <div className="flex items-center justify-between gap-3 py-1.5 border-b border-outline-variant/20 last:border-0 min-w-0">
        <span className="text-xs text-on-surface-variant shrink-0">{label}</span>
        <span
            className={`text-xs ${mono ? 'font-mono' : ''} ${highlight ? 'text-primary font-semibold' : 'text-on-surface'} truncate min-w-0 text-right`}
            title={String(value)}
        >
            {value}
        </span>
    </div>
);

export default StatisticsDash;
