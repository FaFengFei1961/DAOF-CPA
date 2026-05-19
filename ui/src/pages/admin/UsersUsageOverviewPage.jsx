/**
 * UsersUsageOverviewPage — 用户用量大盘（Phase 1 拆出第 1 页）
 *
 * 替换原 UserUsageDash.jsx 的 5 张顶层 KPI + 用户列表 + 用户趋势折线图。
 * 保留：核心数值，去掉巨型横滚事件表（独立 AuditEventsPage）和上游账号成本（独立 UpstreamMarginPage）。
 *
 * 视觉规则（gemini ccg "强制呼吸感"）：
 *  - PageContainer 强制 32px 模块间距
 *  - 4 张 KPI 卡（StatCard，独立组件，统一交互 + reveal）
 *  - 用户列表用 DataTable（5-6 关键列，行点击 → 切到 AuditEvents 带 user_id 筛选）
 *  - 趋势图独立 ChartContainer
 */
import React, { useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Users, Activity, Zap, Coins, AlertTriangle, RefreshCw } from 'lucide-react';
import {
  AreaChart, Area, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer, Legend,
} from 'recharts';
import toast from 'react-hot-toast';
import {
  PageContainer, PageHeader, StatCard, DataTable, ChartContainer, useChartColors,
} from '../../components/ui';
import { useCurrency } from '../../context/CurrencyContext';
import {
  PERIODS, formatTokens, formatPercent, formatRelativeTime, makeFormatMeterCost,
} from './shared';

const SORTS = [
  { value: 'cost_desc',        label: '扣减 ↓' },
  { value: 'requests_desc',    label: '请求数 ↓' },
  { value: 'tokens_desc',      label: 'Token ↓' },
  { value: 'last_active_desc', label: '最近活跃 ↓' },
  { value: 'username_asc',     label: '用户名 A→Z' },
];

const rawCostOf = (row) => Number(row?.raw_cost ?? row?.total_cost ?? row?.cost ?? 0) || 0;
const chargedCostOf = (row) => Number(row?.charged_cost ?? row?.total_charged_cost ?? row?.total_cost ?? row?.cost ?? 0) || 0;
const costsDiffer = (raw, charged) => Math.abs(Number(raw || 0) - Number(charged || 0)) > 0.0000005;

const UsersUsageOverviewPage = () => {
  const navigate = useNavigate();
  const { formatCurrencyFixed } = useCurrency();
  const formatMeterCost = makeFormatMeterCost(formatCurrencyFixed);
  const colors = useChartColors();

  const [period, setPeriod] = useState('7d');
  const [sortKey, setSortKey] = useState('cost_desc');
  const [searchTerm, setSearchTerm] = useState('');
  const [data, setData] = useState(null);
  const [loading, setLoading] = useState(true);
  const [chart, setChart] = useState(null);
  const [chartLoading, setChartLoading] = useState(true);
  const [chartMetric, setChartMetric] = useState('requests');

  const fetchOverview = async () => {
    setLoading(true);
    try {
      const res = await fetch(`/api/admin/users-usage?period=${period}&sort=${sortKey}&include_models=false`, { credentials: 'include' });
      const json = await res.json();
      if (json.success) setData(json.data);
      else toast.error(json.message || '加载用户用量失败');
    } catch {
      toast.error('网络异常');
    }
    setLoading(false);
  };

  const fetchChart = async () => {
    setChartLoading(true);
    try {
      const res = await fetch(`/api/admin/users-usage/timeseries?period=${period}&top_n=6`, { credentials: 'include' });
      const json = await res.json();
      if (json.success) setChart(json.data);
    } catch {
      // 静默
    }
    setChartLoading(false);
  };

  useEffect(() => {
    fetchOverview();
    fetchChart();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [period, sortKey]);

  const summary = data?.summary || {};
  const filteredUsers = useMemo(() => {
    if (!data?.users) return [];
    if (!searchTerm) return data.users;
    const q = searchTerm.toLowerCase();
    return data.users.filter(u =>
      u.username?.toLowerCase().includes(q)
      || u.github_id?.toLowerCase().includes(q)
      || u.phone?.toLowerCase().includes(q)
      || String(u.user_id).includes(q),
    );
  }, [data, searchTerm]);

  const failedTotal = filteredUsers.reduce((s, u) => s + (u.failed_requests || 0), 0);

  // 趋势图数据：series → recharts rows
  const chartRows = useMemo(() => {
    if (!chart?.buckets || !chart?.series) return [];
    return chart.buckets.map((bucket, i) => {
      const row = { bucket };
      chart.series.forEach(s => {
        const key = s.is_other ? '__other' : `u_${s.user_id}`;
        const p = s.points[i] || {};
        if (chartMetric === 'requests') row[key] = p.requests || 0;
        else if (chartMetric === 'tokens') row[key] = p.tokens || 0;
        else if (chartMetric === 'cost')  row[key] = chargedCostOf(p);
      });
      return row;
    });
  }, [chart, chartMetric]);

  const seriesKeys = useMemo(() => {
    if (!chart?.series) return [];
    return chart.series.map(s => ({
      key: s.is_other ? '__other' : `u_${s.user_id}`,
      label: s.is_other ? '其他' : (s.username || `#${s.user_id}`),
    }));
  }, [chart]);

  const userColumns = [
    { key: 'username', header: '用户', render: u => (
      <div className="min-w-0">
        <div className="font-medium text-on-surface truncate">{u.username || `#${u.user_id}`}</div>
        <div className="text-[11px] text-on-surface-variant truncate">{u.github_id || u.phone || '-'}</div>
      </div>
    ) },
    { key: 'requests',  header: '请求数', align: 'right', render: u => (u.total_requests || 0).toLocaleString() },
    { key: 'failed',    header: '失败', align: 'right', render: u => {
      const r = u.total_requests || 0;
      const rate = r ? (u.failed_requests || 0) / r : 0;
      return (
        <span className={rate > 0.1 ? 'text-error' : 'text-on-surface-variant'}>
          {(u.failed_requests || 0).toLocaleString()}
          <span className="text-[10px] ml-1">{formatPercent(rate)}</span>
        </span>
      );
    } },
    { key: 'tokens',    header: 'Token', align: 'right', render: u => formatTokens(u.total_tokens || 0) },
    { key: 'cost',      header: '扣减', align: 'right', render: u => {
      const raw = rawCostOf(u);
      const charged = chargedCostOf(u);
      return (
        <div className="font-mono leading-tight">
          <div className="text-primary">{formatMeterCost(charged)}</div>
          {costsDiffer(raw, charged) && (
            <div className="text-[10px] text-on-surface-variant">raw {formatMeterCost(raw)}</div>
          )}
        </div>
      );
    } },
    { key: 'last',      header: '最近活跃', align: 'right', render: u => (
      <span className="text-xs text-on-surface-variant">{formatRelativeTime(u.last_active_at)}</span>
    ) },
  ];

  const headerActions = (
    <>
      <input
        type="text"
        placeholder="搜索用户…"
        value={searchTerm}
        onChange={(e) => setSearchTerm(e.target.value)}
        className="bg-surface-container-high border border-outline-variant text-on-surface text-sm rounded-control px-3 py-1.5 w-44 focus:border-primary outline-none"
      />
      <select
        value={sortKey}
        onChange={(e) => setSortKey(e.target.value)}
        className="bg-surface-container-high border border-outline-variant text-on-surface text-sm rounded-control px-2 py-1.5 outline-none"
      >
        {SORTS.map(s => <option key={s.value} value={s.value}>{s.label}</option>)}
      </select>
      <PeriodSwitch value={period} onChange={setPeriod} />
      <button
        type="button"
        onClick={() => { fetchOverview(); fetchChart(); }}
        className="p-2 rounded-control border border-outline-variant text-on-surface-variant hover:text-on-surface hover:border-outline transition"
        aria-label="刷新"
        title="刷新"
      >
        <RefreshCw size={14} className={loading ? 'animate-spin' : ''} />
      </button>
    </>
  );

  return (
    <PageContainer>
      <PageHeader
        title="用户用量大盘"
        sub="按用户聚合的请求 / Token / 扣减 / 失败率 + 用户趋势。点击行跳转事件审计页查看明细。"
        actions={headerActions}
      />

      {/* 4 张 KPI */}
      <div className="grid grid-cols-2 lg:grid-cols-4 gap-3">
        <StatCard
          icon={Users}
          iconColor="text-primary"
          iconBg="bg-primary/10"
          label="活跃用户"
          value={`${summary.active_users ?? 0} / ${summary.total_users ?? 0}`}
          sub={`${period} 内有调用`}
        />
        <StatCard
          icon={Activity}
          iconColor="text-success"
          iconBg="bg-success/10"
          label="总请求数"
          value={(summary.total_requests ?? 0).toLocaleString()}
          sub={`失败 ${failedTotal.toLocaleString()}`}
        />
        <StatCard
          icon={Zap}
          iconColor="text-primary"
          iconBg="bg-primary/10"
          label="总 Token"
          value={formatTokens(summary.total_tokens)}
        />
        <StatCard
          icon={Coins}
          iconColor="text-warning"
          iconBg="bg-warning/10"
          label="总扣减"
          value={formatMeterCost(summary.total_charged_cost ?? summary.total_cost ?? 0)}
          sub={costsDiffer(summary.total_cost, summary.total_charged_cost ?? summary.total_cost)
            ? `raw ${formatMeterCost(summary.total_cost ?? 0)}`
            : undefined}
        />
      </div>

      {/* 用户列表 */}
      <DataTable
        columns={userColumns}
        rows={filteredUsers}
        rowKey={u => u.user_id}
        loading={loading}
        emptyTitle="暂无用户用量"
        emptySub={searchTerm ? '试试清空搜索' : '还没有任何请求'}
        emptyIcon={Users}
        onRowClick={u => navigate(`/admin/audit/events?user_id=${u.user_id}`)}
      />

      {/* 用户趋势 */}
      <ChartContainer
        title="用户趋势"
        sub="Top 6 用户 + 其他合计"
        actions={
          <div className="flex gap-1">
            {[
              { v: 'requests', l: '请求' },
              { v: 'tokens',   l: 'Token' },
              { v: 'cost',     l: '扣减' },
            ].map(({ v, l }) => (
              <button
                key={v}
                type="button"
                onClick={() => setChartMetric(v)}
                className={`px-2 py-1 text-xs rounded-control ${
                  chartMetric === v ? 'bg-primary text-on-primary' : 'text-on-surface-variant hover:text-on-surface'
                }`}
              >{l}</button>
            ))}
          </div>
        }
        h="md"
        noPadding
      >
        {chartLoading && !chartRows.length ? (
          <div className="h-full flex items-center justify-center text-sm text-on-surface-variant">加载中…</div>
        ) : !chartRows.length ? (
          <div className="h-full flex items-center justify-center text-sm text-on-surface-variant">暂无趋势数据</div>
        ) : (
          <ResponsiveContainer width="100%" height="100%">
            <AreaChart data={chartRows} margin={{ top: 10, right: 16, bottom: 0, left: 0 }}>
              <defs>
                {seriesKeys.map((s, i) => (
                  <linearGradient key={s.key} id={`grad-${s.key}`} x1="0" y1="0" x2="0" y2="1">
                    <stop offset="5%" stopColor={colors[i % colors.length]} stopOpacity={0.5} />
                    <stop offset="95%" stopColor={colors[i % colors.length]} stopOpacity={0} />
                  </linearGradient>
                ))}
              </defs>
              <CartesianGrid strokeDasharray="3 3" stroke="var(--color-outline-variant)" opacity={0.3} />
              <XAxis dataKey="bucket" tick={{ fontSize: 11, fill: 'var(--color-on-surface-variant)' }} stroke="var(--color-outline-variant)" />
              <YAxis tick={{ fontSize: 11, fill: 'var(--color-on-surface-variant)' }} stroke="var(--color-outline-variant)" />
              <Tooltip
                contentStyle={{
                  background: 'var(--color-surface-container-high)',
                  border: '1px solid var(--color-outline-variant)',
                  borderRadius: 8, fontSize: 12,
                }}
              />
              <Legend wrapperStyle={{ fontSize: 11 }} />
              {seriesKeys.map((s, i) => (
                <Area
                  key={s.key}
                  type="monotone"
                  dataKey={s.key}
                  name={s.label}
                  stroke={colors[i % colors.length]}
                  fill={`url(#grad-${s.key})`}
                  strokeWidth={2}
                />
              ))}
            </AreaChart>
          </ResponsiveContainer>
        )}
      </ChartContainer>
    </PageContainer>
  );
};

const PeriodSwitch = ({ value, onChange }) => (
  <div className="flex items-center gap-1 bg-surface-container p-0.5 rounded-control border border-outline-variant">
    {PERIODS.map(p => (
      <button
        key={p.value}
        type="button"
        onClick={() => onChange(p.value)}
        className={`px-3 py-1 text-xs font-medium rounded-control ${
          value === p.value ? 'bg-surface-variant text-on-surface ' : 'text-on-surface-variant hover:text-on-surface'
        }`}
      >{p.label}</button>
    ))}
  </div>
);

export default UsersUsageOverviewPage;
