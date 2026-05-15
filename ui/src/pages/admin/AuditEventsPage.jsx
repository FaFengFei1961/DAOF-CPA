/**
 * AuditEventsPage — 请求事件审计（Phase 1 拆出第 3 页，gemini ccg #1 重设计典型）
 *
 * 解决"看起来不舒服"的视觉灾难：
 *   原 UserUsageDash 的请求事件表 17 列、min-w-[1960px] 强制横滚 → 用户必须滚动才能看全。
 *   重设计：
 *     - DataTable 只保留 6 个核心列（时间/用户/请求模型/状态/Token/扣减成本）
 *     - 行点击 → Drawer 显示完整 17+ 字段（precheck / upstream / latency / error 等）
 *     - 顶部筛选条 + 分页控件 + CSV 导出独立放置
 */
import React, { useEffect, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { Activity, RefreshCw, Download, Filter } from 'lucide-react';
import toast from 'react-hot-toast';
import {
  PageContainer, PageHeader, DataTable, Drawer, Section,
} from '../../components/ui';
import { useCurrency } from '../../context/CurrencyContext';
import {
  PERIODS, formatTokens, formatLatency, formatTime,
  makeFormatMeterCost, makeFormatEventFailure, isPrecheckLimitEvent,
} from './shared';

const PAGE_SIZE = 50;

const STATUS_OPTIONS = [
  { v: '',         l: '全部状态' },
  { v: 'success',  l: '成功' },
  { v: 'failed',   l: '失败' },
  { v: '400',      l: '400' },
  { v: '401',      l: '401' },
  { v: '402',      l: '402（预检/额度）' },
  { v: '403',      l: '403' },
  { v: '404',      l: '404' },
  { v: '500',      l: '500' },
  { v: '502',      l: '502' },
  { v: '503',      l: '503' },
];

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

const AuditEventsPage = () => {
  const [searchParams, setSearchParams] = useSearchParams();
  const { formatCurrencyFixed } = useCurrency();
  const formatMeterCost = makeFormatMeterCost(formatCurrencyFixed);
  const formatEventFailure = makeFormatEventFailure(formatMeterCost);

  const [period, setPeriod] = useState(searchParams.get('period') || '7d');
  const [page, setPage] = useState(1);
  const [data, setData] = useState(null);
  const [loading, setLoading] = useState(false);
  const [drawerEvent, setDrawerEvent] = useState(null);
  const [filtersOpen, setFiltersOpen] = useState(false);

  const [userIdFilter, setUserIdFilter] = useState(searchParams.get('user_id') || '');
  const [modelFilter, setModelFilter] = useState(searchParams.get('model') || '');
  const [statusFilter, setStatusFilter] = useState(searchParams.get('status') || '');
  const [errorTypeFilter, setErrorTypeFilter] = useState(searchParams.get('error_type') || '');

  const fetchData = async () => {
    setLoading(true);
    try {
      const params = new URLSearchParams({ period, page, page_size: PAGE_SIZE });
      if (userIdFilter)    params.set('user_id', userIdFilter);
      if (modelFilter)     params.set('model', modelFilter);
      if (statusFilter)    params.set('status', statusFilter);
      if (errorTypeFilter) params.set('error_type', errorTypeFilter);
      const res = await fetch(`/api/admin/users-usage/events?${params}`, { credentials: 'include' });
      const json = await res.json();
      if (json.success) setData(json.data);
      else toast.error(json.message || '加载请求事件失败');
    } catch {
      toast.error('网络异常');
    }
    setLoading(false);
  };

  useEffect(() => {
    fetchData();
    // 同步 URL 便于书签
    const next = new URLSearchParams();
    next.set('period', period);
    if (userIdFilter)    next.set('user_id', userIdFilter);
    if (modelFilter)     next.set('model', modelFilter);
    if (statusFilter)    next.set('status', statusFilter);
    if (errorTypeFilter) next.set('error_type', errorTypeFilter);
    setSearchParams(next, { replace: true });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [period, page, userIdFilter, modelFilter, statusFilter, errorTypeFilter]);

  // 切筛选回到第一页
  useEffect(() => { setPage(1); }, [period, userIdFilter, modelFilter, statusFilter, errorTypeFilter]);

  const events = data?.events || [];
  const total = data?.total || 0;

  const handleExportCsv = () => {
    if (!events.length) {
      toast.error('当前页无数据可导出');
      return;
    }
    const header = ['时间', '用户', '请求模型', '服务模型', '上游Provider', '上游账号索引', 'Token Source', '状态', '失败类型', '失败摘要', '预检输入', '预检输出', '预检扣减成本', '预检剩余额度', '请求路径', '延迟ms', '输入', '输出', '思考', '总Token', '原始成本', '扣减成本', '模型权重', 'Fallback授权', 'IP'];
    const rows = events.map(e => [
      formatTime(e.created_at), e.username || `#${e.user_id}`, e.requested_model || e.model_name, e.served_model || e.model_name,
      e.upstream_provider || '', e.upstream_auth_index || '',
      e.token_name || '', e.status, e.error_type || '', formatEventFailure(e)?.detail || e.error_message || '',
      e.precheck_input_tokens || 0, e.precheck_output_tokens || 0, e.precheck_charged_cost || 0, e.precheck_quota_remaining || 0,
      e.request_path || '', e.latency_ms || 0,
      e.prompt_tokens || 0, e.completion_tokens || 0, e.reasoning_tokens || 0, e.total_tokens || 0,
      e.raw_cost ?? e.cost, e.charged_cost ?? e.cost, e.model_weight || 1,
      e.fallback_user_opt_in ? 'yes' : 'no', e.ip_address || '',
    ].map(v => `"${String(v ?? '').replace(/"/g, '""')}"`).join(','));
    const csv = [header.join(','), ...rows].join('\n');
    const blob = new Blob([csv], { type: 'text/csv;charset=utf-8' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `audit-events-${new Date().toISOString().slice(0, 19).replace(/[:T]/g, '-')}.csv`;
    a.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  };

  // 6 个核心列。完整字段在 Drawer 里看
  const columns = [
    { key: 'time', header: '时间', width: 140, render: e => (
      <span className="text-[11px] text-on-surface-variant font-mono whitespace-nowrap">{formatTime(e.created_at)}</span>
    ) },
    { key: 'user', header: '用户', truncate: 160, render: e => (
      <div className="min-w-0">
        <div className="text-on-surface text-xs">{e.username || `#${e.user_id}`}</div>
        <div className="text-[10px] text-on-surface-variant truncate">{e.token_name || '-'}</div>
      </div>
    ) },
    { key: 'model', header: '模型', truncate: 220, render: e => (
      <div className="min-w-0">
        <div className="text-on-surface text-xs font-mono truncate" title={e.requested_model || e.model_name}>
          {e.requested_model || e.model_name}
        </div>
        {e.served_model && e.requested_model && e.served_model !== e.requested_model && (
          <div className="text-[10px] text-warning font-mono truncate" title={`served as ${e.served_model}`}>
            → {e.served_model}
          </div>
        )}
      </div>
    ) },
    { key: 'status', header: '状态', width: 120, render: e => {
      if (e.error_type) {
        const failure = formatEventFailure(e);
        const isPrecheck = isPrecheckLimitEvent(e);
        return (
          <span
            className={`inline-flex items-center gap-1 px-2 h-6 rounded-control-full text-[11px] font-medium border ${
              isPrecheck
                ? 'bg-warning/10 text-warning border-warning/30'
                : 'bg-error/10 text-error border-error/30'
            }`}
            title={failure?.detail || e.error_message || e.error_type}
          >
            {failure?.label || e.error_type}
          </span>
        );
      }
      const ok = e.status >= 200 && e.status < 300;
      return (
        <span className={`inline-flex items-center px-2 h-6 rounded-control-full text-[11px] font-medium border ${
          ok ? 'bg-success/10 text-success border-success/30'
             : 'bg-error/10 text-error border-error/30'
        }`}>
          {e.status}
        </span>
      );
    } },
    { key: 'tokens', header: 'Token', align: 'right', mono: true, render: e => formatTokens(e.total_tokens || 0) },
    { key: 'cost', header: '扣减', align: 'right', mono: true, render: e => (
      <span className="text-primary">{formatMeterCost(e.charged_cost ?? e.cost)}</span>
    ) },
  ];

  const headerActions = (
    <>
      <button
        type="button"
        onClick={() => setFiltersOpen(o => !o)}
        className={`inline-flex items-center gap-1.5 px-3 py-1.5 rounded-control text-xs font-medium border ${
          filtersOpen || userIdFilter || modelFilter || statusFilter || errorTypeFilter
            ? 'bg-primary-container text-on-primary-container border-primary/40'
            : 'border-outline-variant text-on-surface-variant hover:text-on-surface'
        }`}
      >
        <Filter size={12} />
        筛选
        {(userIdFilter || modelFilter || statusFilter || errorTypeFilter) && (
          <span className="inline-block w-1.5 h-1.5 rounded-control-full bg-primary" />
        )}
      </button>
      <button
        type="button"
        onClick={handleExportCsv}
        className="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-control text-xs font-medium border border-outline-variant text-on-surface-variant hover:text-on-surface"
      >
        <Download size={12} />
        CSV
      </button>
      <PeriodSwitch value={period} onChange={setPeriod} />
      <button
        type="button"
        onClick={fetchData}
        className="p-2 rounded-control border border-outline-variant text-on-surface-variant hover:text-on-surface transition"
        aria-label="刷新"
      >
        <RefreshCw size={14} className={loading ? 'animate-spin' : ''} />
      </button>
    </>
  );

  return (
    <PageContainer>
      <PageHeader
        title="请求事件审计"
        sub="按时间倒序的所有 LLM 请求事件。点击行查看完整字段（预检 / 上游归因 / 延迟 / 错误）。"
        actions={headerActions}
      />

      {filtersOpen && (
        <Section flat noPadding className="bg-surface-container/40 border border-outline-variant rounded-overlay p-4">
          <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-3">
            <input
              value={userIdFilter}
              onChange={(e) => setUserIdFilter(e.target.value)}
              placeholder="user_id"
              className="bg-surface-container-high border border-outline-variant text-on-surface text-sm rounded-control px-3 py-1.5 outline-none focus:border-primary"
            />
            <input
              value={modelFilter}
              onChange={(e) => setModelFilter(e.target.value)}
              placeholder="模型 (substring)"
              className="bg-surface-container-high border border-outline-variant text-on-surface text-sm rounded-control px-3 py-1.5 outline-none focus:border-primary"
            />
            <select
              value={statusFilter}
              onChange={(e) => setStatusFilter(e.target.value)}
              className="bg-surface-container-high border border-outline-variant text-on-surface text-sm rounded-control px-3 py-1.5 outline-none focus:border-primary"
            >
              {STATUS_OPTIONS.map(o => <option key={o.v} value={o.v}>{o.l}</option>)}
            </select>
            <input
              value={errorTypeFilter}
              onChange={(e) => setErrorTypeFilter(e.target.value)}
              placeholder="error_type (e.g. upstream_unmetered)"
              className="bg-surface-container-high border border-outline-variant text-on-surface text-sm rounded-control px-3 py-1.5 outline-none focus:border-primary"
            />
          </div>
          <div className="flex items-center justify-end mt-3">
            <button
              type="button"
              onClick={() => { setUserIdFilter(''); setModelFilter(''); setStatusFilter(''); setErrorTypeFilter(''); }}
              className="text-xs text-on-surface-variant hover:text-on-surface"
            >清空筛选</button>
          </div>
        </Section>
      )}

      <DataTable
        columns={columns}
        rows={events}
        rowKey={e => e.id}
        loading={loading}
        emptyTitle="暂无请求事件"
        emptySub={(userIdFilter || modelFilter || statusFilter || errorTypeFilter) ? '试试清空筛选' : '该时间窗内还没有请求'}
        emptyIcon={Activity}
        onRowClick={setDrawerEvent}
        pagination={{
          page,
          pageSize: PAGE_SIZE,
          total,
          onPageChange: setPage,
        }}
      />

      <Drawer
        open={!!drawerEvent}
        onClose={() => setDrawerEvent(null)}
        title={drawerEvent ? `请求 #${drawerEvent.id}` : ''}
        description={drawerEvent ? `${drawerEvent.username || `#${drawerEvent.user_id}`} · ${formatTime(drawerEvent.created_at)}` : ''}
        size="lg"
      >
        {drawerEvent && (
          <EventDetail event={drawerEvent} formatMeterCost={formatMeterCost} formatEventFailure={formatEventFailure} />
        )}
      </Drawer>
    </PageContainer>
  );
};

const EventDetail = ({ event, formatMeterCost, formatEventFailure }) => {
  const failure = formatEventFailure(event);
  const isPrecheck = isPrecheckLimitEvent(event);
  return (
    <div className="space-y-5">
      {/* 状态条 */}
      <div className={`rounded-overlay border px-4 py-3 ${
        failure
          ? (isPrecheck ? 'bg-warning/10 border-warning/30' : 'bg-error/10 border-error/30')
          : 'bg-success/10 border-success/30'
      }`}>
        <div className={`text-sm font-semibold ${failure ? (isPrecheck ? 'text-warning' : 'text-error') : 'text-success'}`}>
          {failure ? failure.label : `成功 (${event.status})`}
        </div>
        {failure?.detail && (
          <div className="text-xs text-on-surface-variant mt-1 break-all">{failure.detail}</div>
        )}
      </div>

      {/* 三列分组 */}
      <Section flat noPadding>
        <Field label="模型 (请求 → 服务)" mono value={`${event.requested_model || event.model_name} → ${event.served_model || event.model_name}`} />
        <Field label="Token Source" mono value={event.token_name || '-'} />
        <Field label="路径" mono value={event.request_path || '-'} />
        <Field label="客户端 IP" mono value={event.ip_address || '-'} />
        <Field label="延迟" value={formatLatency(event.latency_ms)} />
        <Field label="HTTP 状态" value={event.status} />
        {event.fallback_user_opt_in && (
          <Field label="Fallback opt-in" value={event.fallback_reason || '用户已显式允许 fallback'} />
        )}
      </Section>

      <Section title="计费明细" flat>
        <div className="grid grid-cols-2 gap-x-4">
          <Field label="原始成本 (raw)" mono value={formatMeterCost(event.raw_cost ?? event.cost)} />
          <Field label="扣减成本 (charged)" mono value={formatMeterCost(event.charged_cost ?? event.cost)} highlight />
          <Field label="模型权重" mono value={`×${Number(event.model_weight || 1).toFixed(2)}`} />
          {Number(event.health_multiplier || 1) !== 1 && (
            <Field label="高峰系数" mono value={`H×${Number(event.health_multiplier || 1).toFixed(2)}`} />
          )}
          <Field label="规则版本" mono value={event.billing_rules_version || '-'} />
        </div>
      </Section>

      <Section title="Token 明细" flat>
        <div className="grid grid-cols-2 gap-x-4">
          <Field label="输入" mono value={(event.prompt_tokens || 0).toLocaleString()} />
          <Field label="输出" mono value={(event.completion_tokens || 0).toLocaleString()} />
          <Field label="思考 (reasoning)" mono value={(event.reasoning_tokens || 0).toLocaleString()} />
          <Field label="缓存读" mono value={(event.cached_tokens || 0).toLocaleString()} />
          <Field label="缓存写" mono value={(event.cache_write_tokens || 0).toLocaleString()} />
          <Field label="总 Token" mono value={(event.total_tokens || 0).toLocaleString()} highlight />
        </div>
      </Section>

      {(event.precheck_input_tokens || event.precheck_charged_cost || event.precheck_quota_limit) && (
        <Section title="预检 (precheck) 状态" flat sub="路由决策时的估算与窗口快照">
          <div className="grid grid-cols-2 gap-x-4">
            <Field label="预估输入" mono value={(event.precheck_input_tokens || 0).toLocaleString()} />
            <Field label="预估输出" mono value={(event.precheck_output_tokens || 0).toLocaleString()} />
            <Field label="预估原始成本" mono value={formatMeterCost(event.precheck_raw_cost || 0)} />
            <Field label="预估扣减成本" mono value={formatMeterCost(event.precheck_charged_cost || 0)} />
            <Field label="窗口限额" mono value={formatMeterCost(event.precheck_quota_limit || 0)} />
            <Field label="窗口已用" mono value={formatMeterCost(event.precheck_quota_used || 0)} />
            <Field label="窗口剩余" mono value={formatMeterCost(event.precheck_quota_remaining || 0)} />
            <Field label="窗口结束" mono value={event.precheck_window_end_at ? formatTime(event.precheck_window_end_at) : '-'} />
            <Field label="拦截 plan_id" mono value={event.precheck_quota_plan_id || '-'} />
            <Field label="block_reason" mono value={event.block_reason || '-'} />
          </div>
        </Section>
      )}

      {(event.upstream_provider || event.upstream_auth_index) && (
        <Section title="上游归因" flat sub="本平台从 CLIProxy usage queue 同步并匹配到的上游账号">
          <div className="grid grid-cols-2 gap-x-4">
            <Field label="provider" mono value={event.upstream_provider || '-'} />
            <Field label="auth_index" mono value={event.upstream_auth_index || '-'} />
            <Field label="auth_type" mono value={event.upstream_auth_type || '-'} />
            <Field label="source" mono value={event.upstream_source || '-'} />
            <Field label="request_id" mono value={event.upstream_request_id || '-'} />
            <Field label="usage_match" mono value={event.upstream_usage_match || '-'} />
            <Field label="usage_record_id" mono value={event.upstream_usage_record_id || '-'} />
            <Field label="synced_at" mono value={event.upstream_usage_synced_at ? formatTime(event.upstream_usage_synced_at) : '-'} />
          </div>
        </Section>
      )}

      {(event.error_type || event.error_message) && (
        <Section title="错误详情" flat>
          <Field label="error_type" mono value={event.error_type || '-'} />
          <div className="mt-2 text-[11px] text-error font-mono whitespace-pre-wrap break-all bg-error/5 border border-error/20 rounded-control p-2">
            {event.error_message || '(无 message)'}
          </div>
        </Section>
      )}
    </div>
  );
};

const Field = ({ label, value, mono, highlight }) => (
  <div className="flex items-center justify-between gap-3 py-1.5 border-b border-outline-variant/20 last:border-0 min-w-0">
    <span className="text-xs text-on-surface-variant shrink-0">{label}</span>
    <span className={`text-xs ${mono ? 'font-mono' : ''} ${highlight ? 'text-primary font-semibold' : 'text-on-surface'} truncate min-w-0 text-right`} title={String(value)}>
      {value}
    </span>
  </div>
);

export default AuditEventsPage;
