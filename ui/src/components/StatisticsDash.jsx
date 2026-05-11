import React, { useState, useEffect, useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { Activity, Coins, Zap, RefreshCw, BarChart2, Check, ChevronLeft, ChevronRight, Download } from 'lucide-react';
import { AreaChart, Area, LineChart, Line, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer } from 'recharts';
import { useCurrency } from '../context/CurrencyContext';
import { HealthMonitor } from './HealthMonitor';
import { authFetch } from '../utils/authFetch';

const CHART_COLORS = ['#3b82f6', '#10b981', '#f59e0b', '#ef4444', '#8b5cf6', '#ec4899', '#06b6d4', '#f97316', '#14b8a6'];

/* ═══════════════ StatCard (sparkline) ═══════════════ */
/* ═══════════════ StatCard (sparkline) ═══════════════ */
const StatCard = ({ title, value, subLabel, metaNode, data, dataKey, color, bgClass, icon: Icon }) => (
    <div className={`rounded-xl p-5 border border-outline-variant shadow-lg relative overflow-hidden flex flex-col justify-between ${bgClass} bg-opacity-40`}>
        <div className="flex items-start justify-between relative z-10 mb-2">
            <div className="flex flex-col gap-1 w-full relative z-20">
                <div className="flex items-center gap-2 mb-1">
                    <span className="text-sm font-semibold text-on-surface-variant tracking-wide">{title}</span>
                </div>
                <span className="text-3xl font-bold text-on-surface tracking-tight" style={{textShadow: '0 2px 10px rgba(0,0,0,0.5)'}}>{value}</span>
                {subLabel && <span className="text-xs text-on-surface-variant font-medium mt-1">{subLabel}</span>}
                {metaNode && <div className="mt-2 space-y-1 z-30">{metaNode}</div>}
            </div>
            <div className="p-2 rounded-lg opacity-80 shadow-md shrink-0 z-20" style={{ backgroundColor: `${color}20`, color, border: `1px solid ${color}40` }}>
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
const CustomTooltip = ({ active, payload, label }) => {
    if (!active || !payload?.length) return null;
    return (
        <div className="bg-surface-container-high border border-outline-variant p-3 rounded-lg shadow-xl shadow-black/50 text-xs">
            <p className="font-mono text-on-surface-variant mb-2 border-b border-outline-variant/50 pb-1">{label}</p>
            {payload.map((entry, i) => (
                <div key={i} className="flex justify-between items-center gap-4 py-0.5">
                    <span className="flex items-center gap-1.5 min-w-[100px]">
                        <span className="w-2 h-2 rounded-full" style={{ backgroundColor: entry.color }} />
                        <span className="text-on-surface-variant font-medium truncate">{entry.name}</span>
                    </span>
                    <span className="text-on-surface font-mono">{typeof entry.value === 'number' ? entry.value.toLocaleString() : entry.value}</span>
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

/* ═══════════════ Main Component ═══════════════ */
const StatisticsDash = ({ isAdmin = false, isAuthenticated = true }) => {
    const { t } = useTranslation();
    const { formatCurrency } = useCurrency();
    const [period, setPeriod] = useState('7d');
    const [stats, setStats] = useState(null);
    // 未登录时不应显示"加载中…"骨架，让 RequireAuth banner 单独负责提示
    const [loading, setLoading] = useState(isAuthenticated || isAdmin);
    const [selectedModels, setSelectedModels] = useState([]);
    const [logsPage, setLogsPage] = useState(1);

    // Filters for request events
    const [filterModel, setFilterModel] = useState('');
    const [filterToken, setFilterToken] = useState('');

    // Sort state for tables
    const [tokenSort, setTokenSort] = useState({ key: 'reqs', dir: 'desc' });
    const [modelSort, setModelSort] = useState({ key: 'reqs', dir: 'desc' });

    const fetchStats = async () => {
        setLoading(true);
        try {
            if (isAdmin) {
                // ─── 管理员：通过 Go 后端安全代理访问 CLIProxyAPI ───
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
                    if (!tokenStatsMap[apiKey]) tokenStatsMap[apiKey] = { token_name: apiKey, reqs: 0, tokens: 0, failed: 0, latencySum: 0, input_tokens: 0, output_tokens: 0, cached_tokens: 0, reasoning_tokens: 0, cost: 0 };
                    for (const [modelName, modelData] of Object.entries(apiData.models || {})) {
                        if (!modelStatsMap[modelName]) modelStatsMap[modelName] = { model_name: modelName, reqs: 0, tokens: 0, failed: 0, latencySum: 0, input_tokens: 0, output_tokens: 0, cached_tokens: 0, reasoning_tokens: 0, cost: 0 };
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
                                reasoning_tokens: tkn.reasoning_tokens || 0,
                                tokens: tkn.total_tokens || 0,
                                cost: 0,
                                latency_ms: detail.latency_ms || 0,
                                failed: detail.failed || false,
                                source: detail.source || '',
                                auth_index: detail.auth_index || '',
                            });
                            const t = tokenStatsMap[apiKey]; const m = modelStatsMap[modelName];
                            t.reqs++; t.tokens += tkn.total_tokens||0; t.input_tokens += tkn.input_tokens||0; t.output_tokens += tkn.output_tokens||0; t.cached_tokens += tkn.cached_tokens||0; t.reasoning_tokens += tkn.reasoning_tokens||0; t.latencySum += detail.latency_ms||0; if (detail.failed) t.failed++;
                            m.reqs++; m.tokens += tkn.total_tokens||0; m.input_tokens += tkn.input_tokens||0; m.output_tokens += tkn.output_tokens||0; m.cached_tokens += tkn.cached_tokens||0; m.reasoning_tokens += tkn.reasoning_tokens||0; m.latencySum += detail.latency_ms||0; if (detail.failed) m.failed++;
                        }
                    }
                }

                const chartMap = {};
                for (const d of allDetails) {
                    const dt = new Date(d.created_at);
                    const bucket = period === '24h'
                        ? `${dt.getFullYear()}-${String(dt.getMonth()+1).padStart(2,'0')}-${String(dt.getDate()).padStart(2,'0')} ${String(dt.getHours()).padStart(2,'0')}:00`
                        : `${dt.getFullYear()}-${String(dt.getMonth()+1).padStart(2,'0')}-${String(dt.getDate()).padStart(2,'0')}`;
                    if (!chartMap[bucket]) chartMap[bucket] = { date: bucket, reqs: 0, tokens: 0, cost: 0, prompt_tokens: 0, completion_tokens: 0, cached_tokens: 0, reasoning_tokens: 0, models: {} };
                    const c = chartMap[bucket];
                    c.reqs++; c.tokens += d.tokens; c.prompt_tokens += d.prompt_tokens; c.completion_tokens += d.completion_tokens; c.cached_tokens += d.cached_tokens; c.reasoning_tokens += d.reasoning_tokens;
                    if (!c.models[d.model_name]) c.models[d.model_name] = { reqs: 0, tokens: 0 };
                    c.models[d.model_name].reqs++; c.models[d.model_name].tokens += d.tokens;
                }
                const chart_data = Object.values(chartMap).sort((a, b) => a.date.localeCompare(b.date));

                const totalReqs = allDetails.length;
                const failedReqs = allDetails.filter(d => d.failed).length;
                const successReqs = totalReqs - failedReqs;
                const totalTokens = allDetails.reduce((s, d) => s + d.tokens, 0);
                const totalCached = allDetails.reduce((s, d) => s + d.cached_tokens, 0);
                const totalReasoning = allDetails.reduce((s, d) => s + d.reasoning_tokens, 0);
                const latencyTotalMs = allDetails.reduce((s, d) => s + d.latency_ms, 0);
                const avgLatency = totalReqs > 0 ? latencyTotalMs / totalReqs / 1000 : 0;
                const periodSecs = period === '24h' ? 86400 : period === '7d' ? 604800 : 2592000;
                const rpm = totalReqs / (periodSecs / 60);

                allDetails.sort((a, b) => new Date(b.created_at) - new Date(a.created_at));

                const token_stats = Object.values(tokenStatsMap).filter(t => t.reqs > 0).map(t => ({ ...t, avg_latency: t.reqs > 0 ? t.latencySum / t.reqs / 1000 : 0 }));
                const model_stats = Object.values(modelStatsMap).filter(m => m.reqs > 0).map(m => ({ ...m, avg_latency: m.reqs > 0 ? m.latencySum / m.reqs / 1000 : 0 }));

                setStats({
                    chart_data,
                    token_stats,
                    model_stats,
                    recent_logs: { logs: allDetails, total: allDetails.length },
                    summary: { totalReqs, successReqs, failedReqs, totalTokens, totalCached, totalReasoning, avgLatency, rpm, totalCost: 0 }
                });
                setLogsPage(1);
            } else {
                // ─── 普通用户：走原始 one-api 日志接口（接入 authFetch）
                const data = await authFetch(`/api/logs/stats?period=${period}`, { cache: 'no-store' });
                if (data.success) {
                    setStats(data.data);
                    setLogsPage(1);
                }
            }
        } catch (e) {
            /* stats error swallowed */;
        }
        setLoading(false);
    };

    useEffect(() => {
        setSelectedModels([]);
        // 未登录跳过 — 避免 401 + 让 RequireAuth banner 提示用户登录
        if (!isAuthenticated && !isAdmin) return;
        fetchStats();
    }, [period, isAuthenticated, isAdmin]);

    /* ── Data Processing ── */
    const { globalData, multiLineData, uniqueModels, summary } = useMemo(() => {
        if (!stats) return { globalData: [], multiLineData: [], uniqueModels: [], summary: {} };
        const raw = stats.chart_data || [];
        const timeMap = {};
        const modelsSet = new Set();
        raw.forEach(r => {
            const mn = r.model_name || 'unknown';
            modelsSet.add(mn);
            if (!timeMap[r.date]) timeMap[r.date] = { date: r.date, reqs: 0, tokens: 0, cost: 0, prompt_tokens: 0, completion_tokens: 0, cached_tokens: 0, reasoning_tokens: 0, models: {} };
            timeMap[r.date].reqs += r.reqs;
            timeMap[r.date].tokens += r.tokens;
            timeMap[r.date].cost += r.cost;
            timeMap[r.date].prompt_tokens += (r.prompt_tokens || 0);
            timeMap[r.date].completion_tokens += (r.completion_tokens || 0);
            timeMap[r.date].cached_tokens += (r.cached_tokens || 0);
            timeMap[r.date].reasoning_tokens += (r.reasoning_tokens || 0);
            timeMap[r.date].models[mn] = { reqs: r.reqs, tokens: r.tokens };
        });
        let expectedDates = [];
        const now = new Date();
        if (period === '24h') {
            for (let i = 23; i >= 0; i--) {
                const d = new Date(now.getTime() - i * 60 * 60 * 1000);
                expectedDates.push(`${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')} ${String(d.getHours()).padStart(2, '0')}:00`);
            }
        } else {
            const days = period === '30d' ? 30 : 7;
            for (let i = days - 1; i >= 0; i--) {
                const d = new Date(now.getTime() - i * 24 * 60 * 60 * 1000);
                expectedDates.push(`${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`);
            }
        }
        
        const mergedSet = new Set([...expectedDates, ...Object.keys(timeMap)]);
        const sortedDates = Array.from(mergedSet).sort();
        
        const gData = sortedDates.map(d => {
            const row = timeMap[d] || { reqs: 0, tokens: 0, cost: 0, prompt_tokens: 0, completion_tokens: 0, cached_tokens: 0, reasoning_tokens: 0 };
            return { date: d, reqs: row.reqs, tokens: row.tokens, cost: row.cost, prompt_tokens: row.prompt_tokens, completion_tokens: row.completion_tokens, cached_tokens: row.cached_tokens, reasoning_tokens: row.reasoning_tokens };
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
    }, [stats]);

    useEffect(() => {
        if (uniqueModels.length > 0 && selectedModels.length === 0) {
            setSelectedModels(uniqueModels.slice(0, 9));
        }
    }, [uniqueModels]);

    /* ── Sorted token stats ── */
    const sortedTokenStats = useMemo(() => {
        const list = [...(stats?.token_stats || [])];
        const { key, dir } = tokenSort;
        list.sort((a, b) => {
            const diff = key === 'token_name' ? a.token_name.localeCompare(b.token_name) : a[key] - b[key];
            return dir === 'asc' ? diff : -diff;
        });
        return list;
    }, [stats, tokenSort]);

    /* ── Sorted model stats ── */
    const sortedModelStats = useMemo(() => {
        const list = [...(stats?.model_stats || [])];
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

    const logsTotal = stats?.recent_logs?.total || 0;
    const logsTotalPages = Math.ceil(logsTotal / 20);

    if (!stats) {
        // 未登录 / 还没拉到数据：分别提示
        if (!isAuthenticated && !isAdmin) {
            return <div className="p-12 text-center text-on-surface-variant text-sm">{t('STATS.AUTH_REQUIRED', '登录后查看您的使用统计')}</div>;
        }
        return <div className="p-12 text-center text-on-surface-variant font-mono tracking-widest">{t('STATS.LOADING')}</div>;
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
        const header = ['时间', '模型', '令牌来源', '输入Tokens', '输出Tokens', '缓存Tokens', '思考Tokens', '花费($)'];
        const rows = logs.map(l => [
            l.created_at, l.model_name, l.token_name,
            l.prompt_tokens, l.completion_tokens, l.cached_tokens || 0, l.reasoning_tokens || 0, l.cost
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
            {/* Header — Microsoft Store 标题节奏（大字 + 副标 + 右侧 period chip） */}
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
                <div className="flex items-center gap-3 fl-acrylic p-1 rounded-control">
                    {['24h', '7d', '30d'].map(p => (
                        <button key={p} onClick={() => setPeriod(p)}
                            className={`px-4 py-1.5 text-xs font-semibold rounded-md ${period === p ? 'bg-surface-variant text-on-surface shadow-sm' : 'text-on-surface-variant hover:text-on-surface'}`}>
                            {p === '24h' ? t('STATS.RANGE_24H') : p === '7d' ? t('STATS.RANGE_7D') : t('STATS.RANGE_30D')}
                        </button>
                    ))}
                    <button onClick={() => fetchStats()} className="p-1.5 text-on-surface-variant hover:text-white cursor-pointer mr-1">
                        <RefreshCw size={14} className={loading ? '' : ''} />
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
                               <span className="w-1.5 h-1.5 rounded-full bg-[#10b981]"></span>{t('STATS.SUCCESS_REQS') || '成功请求'}: {(summary.successReqs || summary.totalReqs || 0).toLocaleString()}
                               <span className="w-1.5 h-1.5 rounded-full bg-red-500 ml-2"></span>失败请求: {(summary.failedReqs || 0).toLocaleString()}
                               <span className="ml-2">平均延迟: {summary.avgLatency ? `${summary.avgLatency.toFixed(1)}秒` : '-'}</span>
                           </span>
                        </div>
                    }
                    data={globalData} dataKey="reqs" color="#8b8680" icon={Activity} bgClass="bg-[#8b8680]/5" 
                />
                <StatCard 
                    title={t('STATS.TOTAL_TOKENS')} 
                    value={formatTokens(summary.totalTokens)} 
                    metaNode={
                        <div className="flex flex-col gap-0.5 mt-2 transition-opacity opacity-80 hover:opacity-100">
                           <span className="text-xs text-on-surface-variant flex items-center gap-2"><span className="w-1.5 h-1.5 rounded-full bg-[#3b82f6]"></span>{t('STATS.CACHED_TOKENS') || '缓存 Tokens'}: {formatTokens(summary.totalCached)}</span>
                           <span className="text-xs text-on-surface-variant flex items-center gap-2"><span className="w-1.5 h-1.5 rounded-full bg-[#a855f7]"></span>{t('STATS.REASONING_TOKENS') || '思考 Tokens'}: {formatTokens(summary.totalReasoning)}</span>
                        </div>
                    }
                    data={globalData} dataKey="tokens" color="#8b5cf6" icon={Zap} bgClass="bg-[#8b5cf6]/5" 
                />
            </div>
            <div className="grid grid-cols-1 sm:grid-cols-3 gap-4 mb-6">
                <StatCard 
                    title={t('STATS.RPM')} 
                    value={typeof summary.rpm === 'number' ? summary.rpm.toFixed(2) : '0.00'} 
                    metaNode={<span className="text-xs text-on-surface-variant opacity-70">{t('STATS.RPM_DESC')}</span>}
                    data={globalData} dataKey="reqs" color="#22c55e" icon={Activity} bgClass="bg-[#22c55e]/5" 
                />
                <StatCard 
                    title={t('STATS.TPM')} 
                    value={typeof summary.tpm === 'number' ? Math.round(summary.tpm).toLocaleString() : '0'} 
                    metaNode={<span className="text-xs text-on-surface-variant opacity-70">{t('STATS.TPM_DESC')}</span>}
                    data={globalData} dataKey="tokens" color="#f97316" icon={Zap} bgClass="bg-[#f97316]/5" 
                />
                <StatCard 
                    title={t('STATS.COST')} 
                    value={formatCurrency(summary.totalCost)} 
                    metaNode={<span className="text-xs text-on-surface-variant opacity-70">{t('STATS.COST_DESC')}</span>}
                    data={globalData} dataKey="cost" color="#f59e0b" icon={Coins} bgClass="bg-[#f59e0b]/5" 
                />
            </div>

            {/* Chart Line Selector */}
            {uniqueModels.length > 0 && (
                <div className="bg-surface border border-outline-variant rounded-xl p-4 mb-4 shadow-lg">
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
                                    className={`flex items-center gap-2 pl-2 pr-3 py-1.5 border rounded-lg ${isSelected ? 'text-on-surface' : 'border-outline-variant text-on-surface-variant hover:border-outline'}`}>
                                    <div className="w-4 h-4 rounded-[4px] flex items-center justify-center" style={{ backgroundColor: isSelected ? activeColor : 'transparent', border: isSelected ? 'none' : '1px solid #444' }}>
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
                <div className="bg-surface border border-outline-variant rounded-xl p-6 shadow-lg min-h-[400px]">
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
                <div className="bg-surface border border-outline-variant rounded-xl p-6 shadow-lg min-h-[400px]">
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
                <div className="bg-surface border border-outline-variant rounded-xl p-6 shadow-lg">
                    <div className="flex justify-between items-center mb-6">
                        <h3 className="text-sm font-semibold text-on-surface-variant">Token 类型分布</h3>
                    </div>
                    <div className="w-full h-[300px]">
                        <ResponsiveContainer width="100%" height="100%">
                            <AreaChart data={globalData || []}>
                                <CartesianGrid strokeDasharray="3 3" stroke="#2b2b2b" vertical={false} />
                                <XAxis dataKey="date" stroke="#6b7280" fontSize={10} tickMargin={10} minTickGap={20} />
                                <YAxis stroke="#6b7280" fontSize={10} tickCount={6} axisLine={false} tickLine={false} tickFormatter={formatTokens} />
                                <Tooltip content={<CustomTooltip />} />
                                <Area isAnimationActive={false} type="monotone" dataKey="prompt_tokens" name="输入 Tokens" stroke="#9ca3af" fill="#9ca3af" fillOpacity={0.1} strokeWidth={2} activeDot={{ r: 4, strokeWidth: 0 }} />
                                <Area isAnimationActive={false} type="monotone" dataKey="completion_tokens" name="输出 Tokens" stroke="#10b981" fill="#10b981" fillOpacity={0.1} strokeWidth={2} activeDot={{ r: 4, strokeWidth: 0 }} />
                                <Area isAnimationActive={false} type="monotone" dataKey="cached_tokens" name="缓存 Tokens" stroke="#f59e0b" fill="#f59e0b" fillOpacity={0.1} strokeWidth={2} activeDot={{ r: 4, strokeWidth: 0 }} />
                                <Area isAnimationActive={false} type="monotone" dataKey="reasoning_tokens" name="思考 Tokens" stroke="#8b5cf6" fill="#8b5cf6" fillOpacity={0.1} strokeWidth={2} activeDot={{ r: 4, strokeWidth: 0 }} />
                            </AreaChart>
                        </ResponsiveContainer>
                    </div>
                </div>
                
                <div className="bg-surface border border-outline-variant rounded-xl p-6 shadow-lg">
                    <div className="flex justify-between items-center mb-6">
                        <h3 className="text-sm font-semibold text-on-surface-variant">花费统计</h3>
                    </div>
                    <div className="w-full h-[300px]">
                        <ResponsiveContainer width="100%" height="100%">
                            <LineChart data={globalData || []}>
                                <CartesianGrid strokeDasharray="3 3" stroke="#2b2b2b" vertical={false} />
                                <XAxis dataKey="date" stroke="#6b7280" fontSize={10} tickMargin={10} minTickGap={20} />
                                <YAxis stroke="#6b7280" fontSize={10} tickCount={6} axisLine={false} tickLine={false} tickFormatter={(val) => `$${val.toFixed(2)}`} />
                                <Tooltip content={<CustomTooltip />} />
                                <Line isAnimationActive={false} type="monotone" dataKey="cost" name="花费" stroke="#f59e0b" strokeWidth={2} dot={{ r: 2, fill: '#f59e0b', strokeWidth: 0 }} activeDot={{ r: 4, strokeWidth: 0 }} />
                            </LineChart>
                        </ResponsiveContainer>
                    </div>
                </div>
            </div>

            {/* API Details (by token_name) + Model Stats */}
            <div className="grid grid-cols-1 xl:grid-cols-2 gap-4 mb-6">
                {/* API Details Card */}
                <div className="bg-surface border border-outline-variant rounded-xl p-6 shadow-lg">
                    <h3 className="text-sm font-semibold text-on-surface-variant mb-4">{t('STATS.API_DETAILS')}</h3>
                    {sortedTokenStats.length > 0 ? (
                        <div className="overflow-x-auto">
                            <table className="w-full min-w-[500px]">
                                <thead><tr className="border-b border-outline-variant">
                                    <SortHeader label={t('STATS.TOKEN_SOURCE')} sortKey="token_name" currentSort={tokenSort} onSort={(k) => handleSort(setTokenSort, tokenSort, k)} />
                                    <SortHeader label={t('STATS.REQUESTS_COUNT')} sortKey="reqs" currentSort={tokenSort} onSort={(k) => handleSort(setTokenSort, tokenSort, k)} />
                                    <SortHeader label={t('STATS.TOKENS_COUNT')} sortKey="tokens" currentSort={tokenSort} onSort={(k) => handleSort(setTokenSort, tokenSort, k)} />
                                    <SortHeader label={t('STATS.COST')} sortKey="cost" currentSort={tokenSort} onSort={(k) => handleSort(setTokenSort, tokenSort, k)} />
                                </tr></thead>
                                <tbody>
                                    {sortedTokenStats.map((row, i) => (
                                        <tr key={i} className="border-b border-outline-variant/30 hover:bg-surface-variant/50">
                                            <td className="px-4 py-3 text-xs text-on-surface-variant font-mono">{row.token_name || '-'}</td>
                                            <td className="px-4 py-3 text-xs text-on-surface">{row.reqs.toLocaleString()}</td>
                                            <td className="px-4 py-3 text-xs text-on-surface">{formatTokens(row.tokens)}</td>
                                            <td className="px-4 py-3 text-xs text-on-surface">{formatCurrency(row.cost)}</td>
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
                <div className="bg-surface border border-outline-variant rounded-xl p-6 shadow-lg">
                    <h3 className="text-sm font-semibold text-on-surface-variant mb-4">{t('STATS.MODEL_STATS')}</h3>
                    {sortedModelStats.length > 0 ? (
                        <div className="overflow-x-auto">
                            <table className="w-full min-w-[500px]">
                                <thead><tr className="border-b border-outline-variant">
                                    <SortHeader label={t('STATS.MODEL_NAME')} sortKey="model_name" currentSort={modelSort} onSort={(k) => handleSort(setModelSort, modelSort, k)} />
                                    <SortHeader label={t('STATS.REQUESTS_COUNT')} sortKey="reqs" currentSort={modelSort} onSort={(k) => handleSort(setModelSort, modelSort, k)} />
                                    <SortHeader label={t('STATS.TOKENS_COUNT')} sortKey="tokens" currentSort={modelSort} onSort={(k) => handleSort(setModelSort, modelSort, k)} />
                                    <SortHeader label={t('STATS.COST')} sortKey="cost" currentSort={modelSort} onSort={(k) => handleSort(setModelSort, modelSort, k)} />
                                </tr></thead>
                                <tbody>
                                    {sortedModelStats.map((row, i) => (
                                        <tr key={i} className="border-b border-outline-variant/30 hover:bg-surface-variant/50">
                                            <td className="px-4 py-3 text-xs text-on-surface-variant font-mono">{row.model_name || '-'}</td>
                                            <td className="px-4 py-3 text-xs text-on-surface">{row.reqs.toLocaleString()}</td>
                                            <td className="px-4 py-3 text-xs text-on-surface">{formatTokens(row.tokens)}</td>
                                            <td className="px-4 py-3 text-xs text-on-surface">{formatCurrency(row.cost)}</td>
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
            <div className="bg-surface border border-outline-variant rounded-xl p-6 shadow-lg">
                <div className="flex flex-col sm:flex-row items-start sm:items-center justify-between mb-4 gap-3">
                    <h3 className="text-sm font-semibold text-on-surface-variant">{t('STATS.REQUEST_EVENTS')}</h3>
                    <div className="flex items-center gap-2 flex-wrap">
                        <button onClick={() => { setFilterModel(''); setFilterToken(''); }} className="text-xs text-on-surface-variant hover:text-on-surface px-2 py-1 rounded border border-outline-variant hover:border-outline">{t('STATS.CLEAR_FILTERS')}</button>
                        <button onClick={handleExportCsv} disabled={!filteredLogs.length} className="text-xs text-on-surface-variant hover:text-on-surface px-2 py-1 rounded border border-outline-variant hover:border-outline disabled:opacity-30 flex items-center gap-1"><Download size={12} />{t('STATS.EXPORT_CSV')}</button>
                    </div>
                </div>

                {/* Filters */}
                <div className="flex flex-wrap gap-4 mb-4">
                    <div className="flex items-center gap-2">
                        <span className="text-xs text-on-surface-variant">{t('STATS.MODEL_NAME')}</span>
                        <select value={filterModel} onChange={e => setFilterModel(e.target.value)} className="bg-surface-container-high border border-outline-variant text-xs text-on-surface-variant rounded-lg px-2 py-1.5 outline-none">
                            <option value="">{t('STATS.FILTER_ALL')}</option>
                            {logModels.map(m => <option key={m} value={m}>{m}</option>)}
                        </select>
                    </div>
                    <div className="flex items-center gap-2">
                        <span className="text-xs text-on-surface-variant">{t('STATS.TOKEN_SOURCE')}</span>
                        <select value={filterToken} onChange={e => setFilterToken(e.target.value)} className="bg-surface-container-high border border-outline-variant text-xs text-on-surface-variant rounded-lg px-2 py-1.5 outline-none">
                            <option value="">{t('STATS.FILTER_ALL')}</option>
                            {logTokens.map(tk => <option key={tk} value={tk}>{tk}</option>)}
                        </select>
                    </div>
                </div>

                <div className="text-xs text-on-surface-variant mb-3">{filteredLogs.length} {t('STATS.EVENTS_COUNT')}</div>

                {filteredLogs.length > 0 ? (
                    <div className="overflow-x-auto">
                        <table className="w-full min-w-[900px]">
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
                                <th className="text-left px-4 py-3 text-xs font-semibold text-on-surface-variant whitespace-nowrap">{t('STATS.CACHED_TOKENS')}</th>
                                <th className="text-left px-4 py-3 text-xs font-semibold text-on-surface-variant whitespace-nowrap">{t('STATS.TOTAL_TOKENS', '总Token数')}</th>
                                <th className="text-left px-4 py-3 text-xs font-semibold text-on-surface-variant whitespace-nowrap">{t('STATS.COST')}</th>
                            </tr></thead>
                            <tbody>
                                {filteredLogs.map((log) => (
                                    <tr key={log.id} className="border-b border-outline-variant/30 hover:bg-surface-variant/50">
                                        <td className="px-4 py-3 text-xs text-on-surface-variant font-mono whitespace-nowrap">{new Date(log.created_at).toLocaleString()}</td>
                                        <td className="px-4 py-3 text-xs font-mono">
                                            <span className={`px-2 py-0.5 rounded flex items-center justify-center w-max text-[10px] font-bold ${log.status >= 200 && log.status < 300 ? 'bg-green-500/20 text-green-400' : 'bg-red-500/20 text-red-400'}`}>
                                                {(log.status >= 200 && log.status < 300) ? t('STATS.SUCCESS', '成功') : (log.status || t('STATS.FAIL', '失败'))}
                                            </span>
                                        </td>
                                        <td className="px-4 py-3 text-xs text-on-surface-variant font-mono">{log.model_name}</td>
                                        <td className="px-4 py-3 text-xs text-on-surface-variant font-mono">{log.token_name || '-'}</td>
                                        <td className="px-4 py-3 text-xs text-outline-variant font-mono">{log.ip_address || '-'}</td>
                                        <td className="px-4 py-3 text-xs text-on-surface-variant font-mono">{formatLatency(log.latency)}</td>
                                        <td className="px-4 py-3 text-xs text-on-surface">{(log.prompt_tokens || 0).toLocaleString()}</td>
                                        <td className="px-4 py-3 text-xs text-on-surface">{(log.completion_tokens || 0).toLocaleString()}</td>
                                        <td className="px-4 py-3 text-xs text-purple-400">{(log.reasoning_tokens || 0).toLocaleString()}</td>
                                        <td className="px-4 py-3 text-xs text-primary">{(log.cached_tokens || 0).toLocaleString()}</td>
                                        <td className="px-4 py-3 text-xs text-on-surface font-mono">{((log.prompt_tokens || 0) + (log.completion_tokens || 0) + (log.reasoning_tokens || 0)).toLocaleString()}</td>
                                        <td className="px-4 py-3 text-xs text-on-surface">{formatCurrency(log.cost)}</td>
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
                        <button onClick={() => setLogsPage(p => Math.max(1, p - 1))} disabled={logsPage <= 1} className="p-1.5 rounded-lg border border-outline-variant text-on-surface-variant hover:text-white hover:border-gray-500 disabled:opacity-30"><ChevronLeft size={16} /></button>
                        <span className="text-xs text-on-surface-variant font-mono">{logsPage} / {logsTotalPages}</span>
                        <button onClick={() => setLogsPage(p => Math.min(logsTotalPages, p + 1))} disabled={logsPage >= logsTotalPages} className="p-1.5 rounded-lg border border-outline-variant text-on-surface-variant hover:text-white hover:border-gray-500 disabled:opacity-30"><ChevronRight size={16} /></button>
                    </div>
                )}
            </div>
        </div>
    );
};

export default StatisticsDash;
