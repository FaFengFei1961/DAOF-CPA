import React, { useState, useEffect, useMemo, useRef, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { Users, Activity, Coins, Zap, RefreshCw, ChevronRight, ChevronDown, BarChart3, AlertTriangle, ChevronLeft, Download, ChevronsLeft, ChevronsRight } from 'lucide-react';
import { LineChart, Line, AreaChart, Area, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer, Legend } from 'recharts';
import toast from 'react-hot-toast';
import { useCurrency } from '../context/CurrencyContext';

const PERIODS = [
  { value: '24h', label: '24 小时' },
  { value: '7d', label: '7 天' },
  { value: '30d', label: '30 天' },
  { value: 'all', label: '全部' },
];

const SORTS = [
  { value: 'cost_desc', label: '花费 ↓' },
  { value: 'requests_desc', label: '请求数 ↓' },
  { value: 'tokens_desc', label: 'Token ↓' },
  { value: 'last_active_desc', label: '最近活跃 ↓' },
  { value: 'username_asc', label: '用户名 A→Z' },
];

const SERIES_COLORS = ['#3b82f6', '#10b981', '#f59e0b', '#8b5cf6', '#ec4899', '#06b6d4', '#f97316', '#14b8a6', '#84cc16'];
const OTHER_COLOR = '#6b7280';

const formatTokens = (n) => {
  if (!n) return '0';
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M';
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'K';
  return n.toLocaleString();
};

const formatLatency = (ms) => {
  if (!ms) return '-';
  if (ms < 1000) return Math.round(ms) + 'ms';
  return (ms / 1000).toFixed(2) + 's';
};

const formatRelativeTime = (iso) => {
  if (!iso) return '从未活跃';
  const d = new Date(iso);
  const diffMs = Date.now() - d.getTime();
  const sec = Math.floor(diffMs / 1000);
  if (sec < 60) return `${sec}s 前`;
  if (sec < 3600) return `${Math.floor(sec / 60)}m 前`;
  if (sec < 86400) return `${Math.floor(sec / 3600)}h 前`;
  if (sec < 86400 * 30) return `${Math.floor(sec / 86400)}d 前`;
  return d.toLocaleDateString();
};

const StatCard = ({ icon: Icon, label, value, sub, color }) => (
  <div className="bg-surface-container border border-outline-variant rounded-xl p-5 flex items-start justify-between">
    <div className="flex flex-col gap-1">
      <span className="text-xs text-on-surface-variant font-medium tracking-wide">{label}</span>
      <span className="text-3xl font-bold text-on-surface tracking-tight">{value}</span>
      {sub && <span className="text-xs text-on-surface-variant mt-1">{sub}</span>}
    </div>
    <div className="p-2 rounded-lg shadow-md shrink-0" style={{ backgroundColor: `${color}20`, color, border: `1px solid ${color}40` }}>
      <Icon size={20} />
    </div>
  </div>
);

const UserUsageDash = () => {
  const { t } = useTranslation();
  const { formatCurrency, formatCurrencyFixed } = useCurrency();
  const [period, setPeriod] = useState('7d');
  const [sortKey, setSortKey] = useState('cost_desc');
  const [searchTerm, setSearchTerm] = useState('');
  const [data, setData] = useState(null);
  const [loading, setLoading] = useState(true);
  const [expandedUser, setExpandedUser] = useState(null);

  // 时间序列 chart 状态
  const [chartData, setChartData] = useState(null);
  const [chartLoading, setChartLoading] = useState(true);
  const [chartMetric, setChartMetric] = useState('requests'); // requests / tokens / cost

  // 请求事件明细
  const [eventsData, setEventsData] = useState(null);
  const [eventsLoading, setEventsLoading] = useState(false);
  const [eventsPage, setEventsPage] = useState(1);
  const [eventFilterUser, setEventFilterUser] = useState('');
  const [eventFilterModel, setEventFilterModel] = useState('');
  const [eventFilterStatus, setEventFilterStatus] = useState('');
  const [eventFilterErrorType, setEventFilterErrorType] = useState('');
  const [eventsJumpPage, setEventsJumpPage] = useState('1');
  const eventsScrollYRef = useRef(null);
  const EVENTS_PAGE_SIZE = 50;

  const fetchData = async () => {
    setLoading(true);
    try {
      const url = `/api/admin/users-usage?period=${period}&sort=${sortKey}&include_models=true`;
      const res = await fetch(url, { credentials: 'include' });
      const json = await res.json();
      if (json.success) setData(json.data);
      else toast.error(json.message || '加载用户用量失败');
    } catch (e) {
      /* swallow */;
      toast.error('网络异常');
    }
    setLoading(false);
  };

  const fetchTimeseries = async () => {
    setChartLoading(true);
    try {
      const res = await fetch(`/api/admin/users-usage/timeseries?period=${period}&top_n=6`, { credentials: 'include' });
      const json = await res.json();
      if (json.success) setChartData(json.data);
    } catch (e) {
      /* swallow */;
    }
    setChartLoading(false);
  };

  const fetchEvents = async () => {
    setEventsLoading(true);
    try {
      const params = new URLSearchParams({
        period,
        page: eventsPage,
        page_size: EVENTS_PAGE_SIZE,
      });
      if (eventFilterUser) params.set('user_id', eventFilterUser);
      if (eventFilterModel) params.set('model', eventFilterModel);
      if (eventFilterStatus) params.set('status', eventFilterStatus);
      if (eventFilterErrorType) params.set('error_type', eventFilterErrorType);
      const res = await fetch(`/api/admin/users-usage/events?${params}`, { credentials: 'include' });
      const json = await res.json();
      if (json.success) setEventsData(json.data);
    } catch (e) {
      /* swallow */;
    }
    setEventsLoading(false);
  };

  useEffect(() => {
    fetchData();
    fetchTimeseries();
  }, [period, sortKey]);

  useEffect(() => {
    fetchEvents();
  }, [period, eventsPage, eventFilterUser, eventFilterModel, eventFilterStatus, eventFilterErrorType]);

  useEffect(() => {
    setEventsJumpPage(String(eventsPage));
  }, [eventsPage]);

  useEffect(() => {
    if (!eventsLoading && eventsScrollYRef.current !== null) {
      const y = eventsScrollYRef.current;
      eventsScrollYRef.current = null;
      requestAnimationFrame(() => window.scrollTo({ top: y, left: window.scrollX }));
    }
  }, [eventsLoading, eventsData]);

  // 时间序列 chart 数据：把 series 转成 recharts 友好的扁平 rows
  const chartRows = useMemo(() => {
    if (!chartData?.buckets || !chartData?.series) return [];
    const rows = chartData.buckets.map((bucket, i) => {
      const row = { bucket };
      chartData.series.forEach(s => {
        const key = s.is_other ? '__other' : `u_${s.user_id}`;
        const p = s.points[i] || {};
        if (chartMetric === 'requests') row[key] = p.requests || 0;
        else if (chartMetric === 'tokens') row[key] = p.tokens || 0;
        else if (chartMetric === 'cost') row[key] = p.cost || 0;
      });
      return row;
    });
    return rows;
  }, [chartData, chartMetric]);

  // Token 类型堆叠图数据：把所有用户合并，只看时间维度上各类 token 占比
  const tokenStackRows = useMemo(() => {
    if (!chartData?.buckets || !chartData?.series) return [];
    return chartData.buckets.map((bucket, i) => {
      let prompt = 0, completion = 0, reasoning = 0, cached = 0, cacheWrite = 0;
      chartData.series.forEach(s => {
        const p = s.points[i] || {};
        prompt += p.prompt_tokens || 0;
        completion += p.completion_tokens || 0;
        reasoning += p.reasoning_tokens || 0;
        cached += p.cached_tokens || 0;
        cacheWrite += p.cache_write_tokens || 0;
      });
      return { bucket, prompt, completion, reasoning, cached, cacheWrite };
    });
  }, [chartData]);

  const filteredUsers = useMemo(() => {
    if (!data?.users) return [];
    if (!searchTerm) return data.users;
    const q = searchTerm.toLowerCase();
    return data.users.filter(
      (u) =>
        u.username?.toLowerCase().includes(q) ||
        u.github_id?.toLowerCase().includes(q) ||
        u.phone?.toLowerCase().includes(q) ||
        String(u.user_id).includes(q),
    );
  }, [data, searchTerm]);

  const allModels = useMemo(() => {
    if (!data?.users) return [];
    const set = new Set();
    data.users.forEach(u => (u.model_breakdown || []).forEach(m => set.add(m.model_name)));
    return Array.from(set).sort();
  }, [data]);

  const summary = data?.summary || {};
  const formatMeterCost = (value) => formatCurrencyFixed(Number(value || 0), 3);
  const eventsTotalPages = Math.max(1, Number(eventsData?.total_page || 1));
  const clampEventsPage = useCallback((page) => {
    const n = Number.parseInt(page, 10);
    if (!Number.isFinite(n)) return eventsPage;
    return Math.min(eventsTotalPages, Math.max(1, n));
  }, [eventsPage, eventsTotalPages]);
  const setEventsPagePreserveScroll = useCallback((nextPage) => {
    const normalized = clampEventsPage(nextPage);
    if (normalized === eventsPage) {
      setEventsJumpPage(String(normalized));
      return;
    }
    eventsScrollYRef.current = window.scrollY;
    setEventsPage(normalized);
  }, [clampEventsPage, eventsPage]);
  const handleEventsJumpSubmit = useCallback((e) => {
    e.preventDefault();
    setEventsPagePreserveScroll(eventsJumpPage);
  }, [eventsJumpPage, setEventsPagePreserveScroll]);

  const handleExportEventsCsv = () => {
    if (!eventsData?.events?.length) return;
    const header = ['时间', '用户', '模型', 'Token Source', '状态', '失败类型', '失败摘要', '请求路径', '延迟ms', '输入', '输出', '思考', '缓存读', '缓存写', '缓存写5m', '缓存写1h', '总Token', '花费', 'IP'];
    const rows = eventsData.events.map(e => [
      e.created_at, e.username || `#${e.user_id}`, e.model_name, e.token_name,
      e.status, e.error_type, e.error_message, e.request_path, e.latency_ms,
      e.prompt_tokens, e.completion_tokens, e.reasoning_tokens, e.cached_tokens,
      e.cache_write_tokens, e.cache_write_5m_tokens || 0, e.cache_write_1h_tokens || 0, e.total_tokens, e.cost, e.ip_address
    ].map(v => `"${String(v ?? '').replace(/"/g, '""')}"`).join(','));
    const csv = [header.join(','), ...rows].join('\n');
    const blob = new Blob([csv], { type: 'text/csv;charset=utf-8' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `user-events-${new Date().toISOString().replace(/[:.]/g, '-')}.csv`;
    a.click();
    URL.revokeObjectURL(url);
  };

  return (
    <div className="w-full">
      <div className="mb-6 border-b border-outline-variant pb-5">
        <h1 className="text-xl md:text-2xl font-bold tracking-tight text-on-surface flex items-center gap-3">
          <BarChart3 size={22} className="text-primary" />
          用户用量看板
        </h1>
        <p className="text-on-surface-variant mt-2 text-sm">
          按用户聚合的请求/Token/花费/失败率趋势 + 模型分布 + 完整请求事件明细。
        </p>
      </div>

      {/* 顶栏：时间窗口 + 排序 + 搜索 */}
      <div className="flex flex-col md:flex-row md:items-center justify-between gap-3 mb-6">
        <div className="flex items-center gap-2 bg-surface-container p-1 rounded-lg border border-outline-variant w-max">
          {PERIODS.map((p) => (
            <button
              key={p.value}
              onClick={() => { setPeriod(p.value); setEventsPage(1); }}
              className={`px-4 py-1.5 text-xs font-semibold rounded-md transition-colors ${
                period === p.value
                  ? 'bg-surface-variant text-on-surface shadow-sm'
                  : 'text-on-surface-variant hover:text-on-surface'
              }`}
            >
              {p.label}
            </button>
          ))}
        </div>

        <div className="flex items-center gap-3 flex-1 md:justify-end">
          <input
            type="text"
            placeholder="搜索用户..."
            value={searchTerm}
            onChange={(e) => setSearchTerm(e.target.value)}
            className="bg-surface-container-high border border-outline-variant text-on-surface text-sm rounded-lg p-2 px-3 w-full md:w-56 focus:border-primary outline-none"
          />
          <select
            value={sortKey}
            onChange={(e) => setSortKey(e.target.value)}
            className="bg-surface-container-high border border-outline-variant text-on-surface text-sm rounded-lg p-2 px-3 outline-none"
          >
            {SORTS.map((s) => (
              <option key={s.value} value={s.value}>{s.label}</option>
            ))}
          </select>
          <button
            onClick={() => { fetchData(); fetchTimeseries(); fetchEvents(); }}
            className="p-2 rounded-lg border border-outline-variant text-on-surface-variant hover:text-on-surface hover:border-outline transition-colors"
            title="刷新"
          >
            <RefreshCw size={14} className={loading ? 'animate-spin' : ''} />
          </button>
        </div>
      </div>

      {/* 5 张总览卡 */}
      <div className="grid grid-cols-2 lg:grid-cols-5 gap-4 mb-6">
        <StatCard icon={Users} label="活跃用户" value={`${summary.active_users ?? 0} / ${summary.total_users ?? 0}`} sub={`${period} 内有调用`} color="#3b82f6" />
        <StatCard icon={Activity} label="总请求数" value={(summary.total_requests ?? 0).toLocaleString()} color="#10b981" />
        <StatCard icon={AlertTriangle} label="失败请求" value={(filteredUsers.reduce((s, u) => s + (u.failed_requests || 0), 0)).toLocaleString()} color="#ef4444" />
        <StatCard icon={Zap} label="总 Token" value={formatTokens(summary.total_tokens)} color="#8b5cf6" />
        <StatCard icon={Coins} label="总花费" value={formatMeterCost(summary.total_cost ?? 0)} color="#f59e0b" />
      </div>

      {/* 用户趋势折线图 */}
      <div className="bg-surface border border-outline-variant rounded-xl p-5 mb-6">
        <div className="flex items-center justify-between mb-4">
          <h3 className="text-sm font-semibold text-on-surface-variant">用户活跃度趋势（Top 6 + 其他）</h3>
          <div className="flex items-center gap-1 bg-surface-container p-0.5 rounded-md border border-outline-variant text-xs">
            {[
              { v: 'requests', l: '请求' },
              { v: 'tokens', l: 'Token' },
              { v: 'cost', l: '花费' },
            ].map(opt => (
              <button
                key={opt.v}
                onClick={() => setChartMetric(opt.v)}
                className={`px-3 py-1 rounded font-medium transition-colors ${chartMetric === opt.v ? 'bg-surface-variant text-on-surface' : 'text-on-surface-variant hover:text-on-surface'}`}
              >
                {opt.l}
              </button>
            ))}
          </div>
        </div>
        <div className="h-[280px]">
          {chartLoading ? (
            <div className="flex items-center justify-center h-full text-on-surface-variant text-sm">加载中…</div>
          ) : chartRows.length === 0 ? (
            <div className="flex items-center justify-center h-full text-on-surface-variant text-sm">该时间窗内无数据</div>
          ) : (
            <ResponsiveContainer width="100%" height="100%">
              <LineChart data={chartRows}>
                <CartesianGrid strokeDasharray="3 3" stroke="#2b2b2b" vertical={false} />
                <XAxis dataKey="bucket" stroke="#6b7280" fontSize={10} tickMargin={6} minTickGap={20} />
                <YAxis stroke="#6b7280" fontSize={10} axisLine={false} tickLine={false}
                  tickFormatter={chartMetric === 'cost' ? formatMeterCost : (v) => formatTokens(v)} />
                <Tooltip contentStyle={{ backgroundColor: '#1a1a1a', border: '1px solid #2b2b2b', borderRadius: 8, fontSize: 12 }} />
                <Legend wrapperStyle={{ fontSize: 11 }} />
                {chartData?.series?.map((s, i) => {
                  const key = s.is_other ? '__other' : `u_${s.user_id}`;
                  const color = s.is_other ? OTHER_COLOR : SERIES_COLORS[i % SERIES_COLORS.length];
                  return <Line key={key} isAnimationActive={false} type="monotone" dataKey={key} name={s.username || `#${s.user_id}`} stroke={color} strokeWidth={2} dot={false} />;
                })}
              </LineChart>
            </ResponsiveContainer>
          )}
        </div>
      </div>

      {/* Token 类型分布堆叠图 */}
      <div className="bg-surface border border-outline-variant rounded-xl p-5 mb-6">
        <h3 className="text-sm font-semibold text-on-surface-variant mb-4">Token 类型分布</h3>
        <div className="h-[220px]">
          {chartLoading ? (
            <div className="flex items-center justify-center h-full text-on-surface-variant text-sm">加载中…</div>
          ) : tokenStackRows.length === 0 ? (
            <div className="flex items-center justify-center h-full text-on-surface-variant text-sm">该时间窗内无数据</div>
          ) : (
            <ResponsiveContainer width="100%" height="100%">
              <AreaChart data={tokenStackRows}>
                <CartesianGrid strokeDasharray="3 3" stroke="#2b2b2b" vertical={false} />
                <XAxis dataKey="bucket" stroke="#6b7280" fontSize={10} tickMargin={6} minTickGap={20} />
                <YAxis stroke="#6b7280" fontSize={10} axisLine={false} tickLine={false} tickFormatter={(v) => formatTokens(v)} />
                <Tooltip contentStyle={{ backgroundColor: '#1a1a1a', border: '1px solid #2b2b2b', borderRadius: 8, fontSize: 12 }} />
                <Legend wrapperStyle={{ fontSize: 11 }} />
                <Area isAnimationActive={false} type="monotone" dataKey="prompt" name="输入" stackId="1" stroke="#9ca3af" fill="#9ca3af" fillOpacity={0.4} />
                <Area isAnimationActive={false} type="monotone" dataKey="completion" name="输出" stackId="1" stroke="#10b981" fill="#10b981" fillOpacity={0.4} />
                <Area isAnimationActive={false} type="monotone" dataKey="reasoning" name="思考" stackId="1" stroke="#8b5cf6" fill="#8b5cf6" fillOpacity={0.5} />
                <Area isAnimationActive={false} type="monotone" dataKey="cached" name="缓存读" stackId="1" stroke="#f59e0b" fill="#f59e0b" fillOpacity={0.5} />
                <Area isAnimationActive={false} type="monotone" dataKey="cacheWrite" name="缓存写" stackId="1" stroke="#f97316" fill="#f97316" fillOpacity={0.5} />
              </AreaChart>
            </ResponsiveContainer>
          )}
        </div>
      </div>

      {/* 用户聚合表格 */}
      <div className="bg-surface border border-outline-variant rounded-xl overflow-hidden shadow-sm mb-6">
        <div className="px-5 py-3 border-b border-outline-variant flex items-center justify-between">
          <h3 className="text-sm font-semibold text-on-surface">用户聚合统计</h3>
          <span className="text-xs text-on-surface-variant">点击行展开查看模型分布</span>
        </div>
        {loading ? (
          <div className="py-16 text-center text-on-surface-variant text-sm">加载中…</div>
        ) : filteredUsers.length === 0 ? (
          <div className="py-16 text-center text-on-surface-variant text-sm">没有匹配的用户</div>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-left min-w-[1100px]">
              <thead>
                <tr className="bg-surface-container-high border-b border-outline-variant">
                  <th className="px-4 py-3 text-xs font-semibold text-on-surface-variant w-8"></th>
                  <th className="px-4 py-3 text-xs font-semibold text-on-surface-variant">用户</th>
                  <th className="px-4 py-3 text-xs font-semibold text-on-surface-variant text-center">角色</th>
                  <th className="px-4 py-3 text-xs font-semibold text-on-surface-variant text-right">余额</th>
                  <th className="px-4 py-3 text-xs font-semibold text-on-surface-variant text-right">请求</th>
                  <th className="px-4 py-3 text-xs font-semibold text-on-surface-variant text-right">失败率</th>
                  <th className="px-4 py-3 text-xs font-semibold text-on-surface-variant text-right">输入</th>
                  <th className="px-4 py-3 text-xs font-semibold text-on-surface-variant text-right">输出</th>
                  <th className="px-4 py-3 text-xs font-semibold text-on-surface-variant text-right">思考</th>
                  <th className="px-4 py-3 text-xs font-semibold text-on-surface-variant text-right" title="来自 usage metadata，不是本平台会话缓存">缓存读</th>
                  <th className="px-4 py-3 text-xs font-semibold text-on-surface-variant text-right" title="来自 usage metadata，不是本平台会话缓存">缓存写</th>
                  <th className="px-4 py-3 text-xs font-semibold text-on-surface-variant text-right">花费</th>
                  <th className="px-4 py-3 text-xs font-semibold text-on-surface-variant text-right">平均延迟</th>
                  <th className="px-4 py-3 text-xs font-semibold text-on-surface-variant text-right">最近活跃</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-outline-variant/40">
                {filteredUsers.map((u) => {
                  const failRate = u.requests > 0 ? ((u.failed_requests / u.requests) * 100).toFixed(1) : '0.0';
                  const failRateNum = parseFloat(failRate);
                  const isExpanded = expandedUser === u.user_id;
                  const isAdmin = u.role === 'admin';
                  const isBanned = u.status === 2;
                  return (
                    <React.Fragment key={u.user_id}>
                      <tr className="hover:bg-surface-container/40 cursor-pointer transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary"
                          tabIndex={0}
                          role="button"
                          aria-expanded={isExpanded}
                          onClick={() => setExpandedUser(isExpanded ? null : u.user_id)}
                          onKeyDown={(e) => {
                            if (e.key === 'Enter' || e.key === ' ') {
                              e.preventDefault();
                              setExpandedUser(isExpanded ? null : u.user_id);
                            }
                          }}>
                        <td className="px-4 py-3 text-on-surface-variant">{isExpanded ? <ChevronDown size={14} aria-hidden="true" /> : <ChevronRight size={14} aria-hidden="true" />}</td>
                        <td className="px-4 py-3">
                          <div className="flex flex-col">
                            <span className="text-sm font-semibold text-on-surface flex items-center gap-2">
                              {u.username}
                              {isAdmin && <span className="text-[10px] font-mono text-purple-400 bg-purple-500/10 border border-purple-500/30 px-1.5 py-0.5 rounded">GOD</span>}
                              {isBanned && <span className="text-[10px] font-mono text-red-400 bg-red-500/10 border border-red-500/30 px-1.5 py-0.5 rounded">封禁</span>}
                            </span>
                            <span className="text-[11px] text-outline-variant font-mono">
                              ID: {u.user_id}{u.github_id ? ` · gh:${u.github_id}` : ''}{u.phone ? ` · 📱${u.phone}` : ''}
                            </span>
                          </div>
                        </td>
                        <td className="px-4 py-3 text-center text-xs text-on-surface-variant">{u.role}</td>
                        <td className="px-4 py-3 text-right text-xs text-on-surface font-mono">{isAdmin ? '∞' : formatCurrency(u.quota || 0)}</td>
                        <td className="px-4 py-3 text-right text-xs text-on-surface font-mono">{(u.requests || 0).toLocaleString()}</td>
                        <td className={`px-4 py-3 text-right text-xs font-mono ${failRateNum > 10 ? 'text-red-400' : failRateNum > 0 ? 'text-amber-400' : 'text-emerald-400'}`}>{failRate}%</td>
                        <td className="px-4 py-3 text-right text-xs text-on-surface font-mono">{formatTokens(u.input_tokens)}</td>
                        <td className="px-4 py-3 text-right text-xs text-on-surface font-mono">{formatTokens(u.output_tokens)}</td>
                        <td className="px-4 py-3 text-right text-xs text-purple-400 font-mono">{formatTokens(u.reasoning_tokens)}</td>
                        <td className="px-4 py-3 text-right text-xs text-amber-400 font-mono">{formatTokens(u.cached_tokens)}</td>
                        <td className="px-4 py-3 text-right text-xs text-orange-400 font-mono">{formatTokens(u.cache_write_tokens)}</td>
                        <td className="px-4 py-3 text-right text-xs text-on-surface font-semibold font-mono">{formatMeterCost(u.cost || 0)}</td>
                        <td className="px-4 py-3 text-right text-xs text-on-surface-variant font-mono">{formatLatency(u.avg_latency_ms)}</td>
                        <td className="px-4 py-3 text-right text-xs text-on-surface-variant whitespace-nowrap">{formatRelativeTime(u.last_active_at)}</td>
                      </tr>
                      {isExpanded && (
                        <tr className="bg-surface-container/30">
                          <td colSpan={13} className="px-8 py-4">
                            {u.model_breakdown && u.model_breakdown.length > 0 ? (
                              <div className="space-y-2">
                                <div className="text-xs text-on-surface-variant mb-3 flex items-center gap-2">
                                  <BarChart3 size={12} />Top 5 模型分布（按花费）
                                </div>
                                <table className="w-full text-left text-xs">
                                  <thead>
                                    <tr className="text-on-surface-variant border-b border-outline-variant/40">
                                      <th className="py-2">模型</th>
                                      <th className="py-2 text-right">请求数</th>
                                      <th className="py-2 text-right">Token</th>
                                      <th className="py-2 text-right">花费</th>
                                      <th className="py-2 text-right w-32">操作</th>
                                    </tr>
                                  </thead>
                                  <tbody>
                                    {u.model_breakdown.map((m, i) => (
                                      <tr key={i} className="border-b border-outline-variant/20 last:border-b-0">
                                        <td className="py-2 font-mono text-on-surface">{m.model_name || 'unknown'}</td>
                                        <td className="py-2 text-right font-mono text-on-surface-variant">{m.requests.toLocaleString()}</td>
                                        <td className="py-2 text-right font-mono text-on-surface-variant">{formatTokens(m.tokens)}</td>
                                        <td className="py-2 text-right font-mono text-on-surface">{formatMeterCost(m.cost)}</td>
                                        <td className="py-2 text-right">
                                          <button
                                            onClick={(e) => { e.stopPropagation(); setEventFilterUser(String(u.user_id)); setEventFilterModel(m.model_name); setEventsPage(1); }}
                                            className="text-[11px] text-primary hover:underline"
                                          >
                                            查看事件 →
                                          </button>
                                        </td>
                                      </tr>
                                    ))}
                                  </tbody>
                                </table>
                              </div>
                            ) : (
                              <div className="text-xs text-outline-variant py-2">该用户在当前时间窗内无请求记录</div>
                            )}
                          </td>
                        </tr>
                      )}
                    </React.Fragment>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </div>

      {/* 请求事件明细 */}
      <div className="bg-surface border border-outline-variant rounded-xl overflow-hidden shadow-sm">
        <div className="px-5 py-3 border-b border-outline-variant flex flex-col sm:flex-row sm:items-center justify-between gap-3">
          <div className="flex items-center gap-2">
            <h3 className="text-sm font-semibold text-on-surface">请求事件明细</h3>
            {eventsData?.total !== undefined && (
              <span className="text-xs text-on-surface-variant">{eventsData.total} 条</span>
            )}
          </div>
          <div className="flex items-center gap-2 flex-wrap">
            <select
              value={eventFilterUser}
              onChange={(e) => { setEventFilterUser(e.target.value); setEventsPage(1); }}
              className="bg-surface-container-high border border-outline-variant text-xs text-on-surface-variant rounded-md px-2 py-1.5 outline-none"
            >
              <option value="">全部用户</option>
              {data?.users?.filter(u => u.role !== 'admin').map(u => (
                <option key={u.user_id} value={u.user_id}>{u.username} (#{u.user_id})</option>
              ))}
            </select>
            <select
              value={eventFilterModel}
              onChange={(e) => { setEventFilterModel(e.target.value); setEventsPage(1); }}
              className="bg-surface-container-high border border-outline-variant text-xs text-on-surface-variant rounded-md px-2 py-1.5 outline-none"
            >
              <option value="">全部模型</option>
              {allModels.map(m => <option key={m} value={m}>{m}</option>)}
            </select>
            <select
              value={eventFilterStatus}
              onChange={(e) => { setEventFilterStatus(e.target.value); setEventsPage(1); }}
              className="bg-surface-container-high border border-outline-variant text-xs text-on-surface-variant rounded-md px-2 py-1.5 outline-none"
            >
              <option value="">全部状态</option>
              <option value="success">成功</option>
              <option value="failed">失败</option>
              <option value="400">400</option>
              <option value="401">401</option>
              <option value="404">404</option>
              <option value="500">500</option>
              <option value="502">502</option>
            </select>
            <button
              onClick={() => { setEventFilterUser(''); setEventFilterModel(''); setEventFilterStatus(''); setEventFilterErrorType(''); setEventsPage(1); }}
              className="text-xs text-on-surface-variant hover:text-on-surface px-2 py-1.5 rounded border border-outline-variant"
            >
              清除筛选
            </button>
            <button
              onClick={handleExportEventsCsv}
              disabled={!eventsData?.events?.length}
              className="text-xs text-on-surface-variant hover:text-on-surface px-2 py-1.5 rounded border border-outline-variant disabled:opacity-30 flex items-center gap-1"
            >
              <Download size={12} />导出 CSV
            </button>
          </div>
        </div>

        {!!eventsData?.error_summary?.length && (
          <div className="px-5 py-3 border-b border-outline-variant bg-surface-container/30 flex flex-wrap items-center gap-2">
            <span className="text-xs font-semibold text-on-surface-variant">失败聚合</span>
            {eventsData.error_summary.map((item, idx) => (
              <button
                key={`${item.error_type}-${item.status}-${item.request_path}-${idx}`}
                onClick={() => { setEventFilterStatus(String(item.status)); setEventFilterErrorType(item.error_type?.startsWith('http_') ? '' : item.error_type); setEventsPage(1); }}
                className="text-[11px] px-2 py-1 rounded border border-red-500/30 bg-red-500/10 text-red-200 hover:bg-red-500/20 max-w-[360px] truncate"
                title={`${item.error_type || 'unknown'} · HTTP ${item.status} · ${item.request_path || '-'} · ${item.count} 条`}
              >
                <span className="font-mono">{item.error_type || 'unknown'}</span>
                <span className="text-red-300/80 ml-1">HTTP {item.status}</span>
                <span className="text-red-100/70 ml-1">{item.count} 条</span>
                {item.request_path ? <span className="text-red-100/50 ml-1">{item.request_path}</span> : null}
              </button>
            ))}
          </div>
        )}

        {eventsLoading && !eventsData ? (
          <div className="py-12 text-center text-on-surface-variant text-sm">加载中…</div>
        ) : !eventsData?.events?.length ? (
          <div className="py-12 text-center text-on-surface-variant text-sm">该时间窗内无请求事件</div>
        ) : (
          <>
            <div className="overflow-x-auto relative min-h-[560px]">
              {eventsLoading && (
                <div className="absolute inset-0 z-10 bg-surface/35 backdrop-blur-[1px] flex items-start justify-center pt-8 pointer-events-none">
                  <span className="inline-flex items-center gap-2 px-3 py-1.5 rounded-lg border border-outline-variant bg-surface-container-high text-xs text-on-surface-variant shadow-lg">
                    <RefreshCw size={12} className="animate-spin" />
                    加载新页…
                  </span>
                </div>
              )}
              <table className="w-full text-left min-w-[1500px]">
                <thead>
                  <tr className="bg-surface-container-high border-b border-outline-variant">
                    <th className="px-3 py-3 text-xs font-semibold text-on-surface-variant whitespace-nowrap">时间</th>
                    <th className="px-3 py-3 text-xs font-semibold text-on-surface-variant">用户</th>
                    <th className="px-3 py-3 text-xs font-semibold text-on-surface-variant">模型</th>
                    <th className="px-3 py-3 text-xs font-semibold text-on-surface-variant whitespace-nowrap">Token Source</th>
                    <th className="px-3 py-3 text-xs font-semibold text-on-surface-variant whitespace-nowrap">来源 IP</th>
                    <th className="px-3 py-3 text-xs font-semibold text-on-surface-variant whitespace-nowrap">路径</th>
                    <th className="px-3 py-3 text-xs font-semibold text-on-surface-variant text-center">状态</th>
                    <th className="px-3 py-3 text-xs font-semibold text-on-surface-variant">失败原因</th>
                    <th className="px-3 py-3 text-xs font-semibold text-on-surface-variant text-right">延迟</th>
                    <th className="px-3 py-3 text-xs font-semibold text-on-surface-variant text-right">输入</th>
                    <th className="px-3 py-3 text-xs font-semibold text-on-surface-variant text-right">输出</th>
                    <th className="px-3 py-3 text-xs font-semibold text-on-surface-variant text-right">思考</th>
                    <th className="px-3 py-3 text-xs font-semibold text-on-surface-variant text-right" title="来自 usage metadata，不是本平台会话缓存">缓存读</th>
                    <th className="px-3 py-3 text-xs font-semibold text-on-surface-variant text-right" title="来自 usage metadata，不是本平台会话缓存">缓存写</th>
                    <th className="px-3 py-3 text-xs font-semibold text-on-surface-variant text-right">总Token</th>
                    <th className="px-3 py-3 text-xs font-semibold text-on-surface-variant text-right">花费</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-outline-variant/30">
                  {eventsData.events.map(e => (
                    <tr key={e.id} className="hover:bg-surface-container/30">
                      <td className="px-3 py-2 text-[11px] text-on-surface-variant font-mono whitespace-nowrap">{new Date(e.created_at).toLocaleString('zh-CN', { hour12: false })}</td>
                      <td className="px-3 py-2 text-xs text-on-surface">{e.username || `#${e.user_id}`}</td>
                      <td className="px-3 py-2 text-xs text-on-surface-variant font-mono">{e.model_name}</td>
                      <td className="px-3 py-2 text-[11px] text-outline-variant font-mono truncate max-w-[180px]" title={e.token_name}>{e.token_name || '-'}</td>
                      <td className="px-3 py-2 text-[11px] text-outline-variant font-mono">{e.ip_address || '-'}</td>
                      <td className="px-3 py-2 text-[11px] text-outline-variant font-mono truncate max-w-[180px]" title={e.request_path}>{e.request_path || '-'}</td>
                      <td className="px-3 py-2 text-center">
                        <span className={`text-[10px] font-bold px-2 py-0.5 rounded ${e.status >= 200 && e.status < 300 ? 'bg-emerald-500/20 text-emerald-400' : 'bg-red-500/20 text-red-400'}`}>
                          {e.status >= 200 && e.status < 300 ? '✓' : (e.status || '×')}
                        </span>
                      </td>
                      <td className="px-3 py-2 text-[11px] text-on-surface-variant max-w-[240px]">
                        {e.error_type ? (
                          <div className="truncate" title={e.error_message || e.error_type}>
                            <span className="font-mono text-red-300">{e.error_type}</span>
                            {e.error_message ? <span className="text-outline-variant ml-1">{e.error_message}</span> : null}
                          </div>
                        ) : '-'}
                      </td>
                      <td className="px-3 py-2 text-right text-xs text-on-surface-variant font-mono">{formatLatency(e.latency_ms)}</td>
                      <td className="px-3 py-2 text-right text-xs font-mono">{(e.prompt_tokens || 0).toLocaleString()}</td>
                      <td className="px-3 py-2 text-right text-xs font-mono">{(e.completion_tokens || 0).toLocaleString()}</td>
                      <td className="px-3 py-2 text-right text-xs font-mono text-purple-400">{(e.reasoning_tokens || 0).toLocaleString()}</td>
                      <td className="px-3 py-2 text-right text-xs font-mono text-amber-400">{(e.cached_tokens || 0).toLocaleString()}</td>
                      <td className="px-3 py-2 text-right text-xs font-mono text-orange-400">
                        <div>{(e.cache_write_tokens || 0).toLocaleString()}</div>
                        {((e.cache_write_5m_tokens || 0) > 0 || (e.cache_write_1h_tokens || 0) > 0) && (
                          <div className="text-[10px] text-orange-300/70 whitespace-nowrap">
                            5m {(e.cache_write_5m_tokens || 0).toLocaleString()} · 1h {(e.cache_write_1h_tokens || 0).toLocaleString()}
                          </div>
                        )}
                      </td>
                      <td className="px-3 py-2 text-right text-xs font-mono text-on-surface">{e.total_tokens.toLocaleString()}</td>
                      <td className="px-3 py-2 text-right text-xs font-mono">{formatMeterCost(e.cost)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>

            {/* 分页 */}
            {eventsTotalPages > 1 && (
              <div className="px-5 py-3 border-t border-outline-variant flex flex-col md:flex-row md:items-center justify-center gap-3">
                <div className="flex items-center justify-center gap-2">
                  <button
                    type="button"
                    onClick={() => setEventsPagePreserveScroll(1)}
                    disabled={eventsPage <= 1 || eventsLoading}
                    className="p-1.5 rounded-lg border border-outline-variant text-on-surface-variant hover:text-on-surface hover:border-outline disabled:opacity-30"
                    title="第一页"
                  >
                    <ChevronsLeft size={14} />
                  </button>
                  <button
                    type="button"
                    onClick={() => setEventsPagePreserveScroll(eventsPage - 1)}
                    disabled={eventsPage <= 1 || eventsLoading}
                    className="p-1.5 rounded-lg border border-outline-variant text-on-surface-variant hover:text-on-surface hover:border-outline disabled:opacity-30"
                    title="上一页"
                  >
                    <ChevronLeft size={14} />
                  </button>
                </div>
                <span className="text-xs text-on-surface-variant font-mono text-center min-w-20">
                  {eventsPage} / {eventsTotalPages}
                </span>
                <div className="flex items-center justify-center gap-2">
                  <button
                    type="button"
                    onClick={() => setEventsPagePreserveScroll(eventsPage + 1)}
                    disabled={eventsPage >= eventsTotalPages || eventsLoading}
                    className="p-1.5 rounded-lg border border-outline-variant text-on-surface-variant hover:text-on-surface hover:border-outline disabled:opacity-30"
                    title="下一页"
                  >
                    <ChevronRight size={14} />
                  </button>
                  <button
                    type="button"
                    onClick={() => setEventsPagePreserveScroll(eventsTotalPages)}
                    disabled={eventsPage >= eventsTotalPages || eventsLoading}
                    className="p-1.5 rounded-lg border border-outline-variant text-on-surface-variant hover:text-on-surface hover:border-outline disabled:opacity-30"
                    title="最后一页"
                  >
                    <ChevronsRight size={14} />
                  </button>
                </div>
                <form onSubmit={handleEventsJumpSubmit} className="flex items-center justify-center gap-2">
                  <span className="text-xs text-on-surface-variant">跳至</span>
                  <input
                    type="number"
                    min="1"
                    max={eventsTotalPages}
                    value={eventsJumpPage}
                    onChange={(e) => setEventsJumpPage(e.target.value)}
                    disabled={eventsLoading}
                    className="h-8 w-20 rounded-lg border border-outline-variant bg-surface-container-high px-2 text-center text-xs font-mono text-on-surface outline-none focus:border-primary disabled:opacity-50"
                  />
                  <button
                    type="submit"
                    disabled={eventsLoading}
                    className="h-8 px-3 rounded-lg border border-outline-variant text-xs text-on-surface-variant hover:text-on-surface hover:border-outline disabled:opacity-30"
                  >
                    跳转
                  </button>
                </form>
              </div>
            )}
          </>
        )}
      </div>
    </div>
  );
};

export default UserUsageDash;
