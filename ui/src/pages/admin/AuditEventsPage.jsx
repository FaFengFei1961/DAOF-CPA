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
import React, { useEffect, useMemo, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
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
import { formatUsageLine, formatUsageLinesSummary, usageLinesOf } from '../../utils/usageLines';

const PAGE_SIZE = 50;

const isAuxiliaryEvent = (event) => (
  String(event?.request_path || '').includes('/messages/count_tokens')
);

// IA audit C7 fix: status options are built inside the component so labels
// honor the user's current locale (was hardcoded Chinese array at module scope).
const buildStatusOptions = (t) => [
  { v: '',         l: t('ADMIN.AUDIT.STATUS_ALL') },
  { v: 'success',  l: t('ADMIN.AUDIT.STATUS_SUCCESS') },
  { v: 'failed',   l: t('ADMIN.AUDIT.STATUS_FAILED') },
  { v: '400',      l: '400' },
  { v: '401',      l: '401' },
  { v: '402',      l: t('ADMIN.AUDIT.STATUS_402_LABEL') },
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
  const { t } = useTranslation();
  const { formatCurrencyFixed } = useCurrency();
  const formatMeterCost = makeFormatMeterCost(formatCurrencyFixed);
  const formatEventFailure = makeFormatEventFailure(formatMeterCost);
  // Memo so toggling locale rebuilds; cheap dependency on t.
  const STATUS_OPTIONS = useMemo(() => buildStatusOptions(t), [t]);
  const auxiliaryEventLabel = (event) => (
    isAuxiliaryEvent(event)
      ? t('ADMIN.AUDIT.AUXILIARY')
      : t('ADMIN.AUDIT.GENERATIVE')
  );

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
      else toast.error(json.message || t('ADMIN.AUDIT.TOAST_LOAD_FAIL'));
    } catch {
      toast.error(t('ADMIN.AUDIT.TOAST_NET_ERROR'));
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
      toast.error(t('ADMIN.AUDIT.TOAST_EXPORT_EMPTY'));
      return;
    }
    // CSV header stays English for machine-friendly processing and analyst
    // tooling that pivots on column names. Localizing would break downstream
    // pipelines without giving humans a meaningful win (they get the same
    // tab-separated columns either way).
    const header = ['time', 'user', 'interface', 'requested_model', 'served_model', 'upstream_provider', 'upstream_auth_index', 'token_source', 'status', 'error_type', 'error_summary', 'precheck_input', 'precheck_output', 'precheck_charged_cost', 'precheck_quota_remaining', 'request_path', 'ttft_ms', 'latency_ms', 'input', 'output', 'reasoning', 'total_tokens', 'media_usage', 'raw_cost', 'charged_cost', 'model_weight', 'fallback_opt_in', 'ip'];
    const rows = events.map(e => [
      formatTime(e.created_at), e.username || `#${e.user_id}`, auxiliaryEventLabel(e), e.requested_model || e.model_name, e.served_model || e.model_name,
      e.upstream_provider || '', e.upstream_auth_index || '',
      e.token_name || '', e.status, e.error_type || '', formatEventFailure(e)?.detail || e.error_message || '',
      e.precheck_input_tokens || 0, e.precheck_output_tokens || 0, e.precheck_charged_cost || 0, e.precheck_quota_remaining || 0,
      e.request_path || '', e.upstream_ttft_ms || 0, e.latency_ms || 0,
      e.prompt_tokens || 0, e.completion_tokens || 0, e.reasoning_tokens || 0, e.total_tokens || 0,
      formatUsageLinesSummary(e, formatMeterCost),
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

  // 6 核心列（compact 模式）：完整字段在右侧 Drawer 查看。
  // Token 总量折入 model 列次行，新增 latency 列方便排查慢响应。
  const columns = [
    { key: 'time', header: t('ADMIN.AUDIT.COL_TIME'), width: 118, render: e => (
      <span className="text-[10px] text-on-surface-variant font-mono whitespace-nowrap">{formatTime(e.created_at)}</span>
    ) },
    { key: 'user', header: t('ADMIN.AUDIT.COL_USER'), truncate: 140, render: e => (
      <div className="min-w-0">
        <div className="text-on-surface text-xs">{e.username || `#${e.user_id}`}</div>
        <div className="text-[10px] text-on-surface-variant font-mono truncate">{e.token_name || '-'}</div>
      </div>
    ) },
    { key: 'model', header: t('ADMIN.AUDIT.COL_MODEL'), truncate: 200, render: e => (
      <div className="min-w-0">
        <div className="flex items-center gap-1 min-w-0">
          <span className="text-xs font-mono text-on-surface truncate" title={e.requested_model || e.model_name}>
            {e.requested_model || e.model_name}
          </span>
          {isAuxiliaryEvent(e) && (
            <span
              className="shrink-0 inline-flex items-center px-1 h-4 rounded text-[9px] font-medium bg-primary-container/60 text-on-primary-container border border-primary/30"
              title={t('ADMIN.AUDIT.AUXILIARY_BADGE_TIP')}
            >
              {t('ADMIN.AUDIT.AUXILIARY')}
            </span>
          )}
        </div>
        {/* tokens + served-model redirect in subrow */}
        <div className="flex items-center gap-2 mt-0.5 min-w-0">
          <span className="text-[10px] text-on-surface-variant font-mono">
            {formatTokens((e.total_tokens || 0))} tok
          </span>
          {e.served_model && e.requested_model && e.served_model !== e.requested_model && (
            <span className="text-[10px] text-warning font-mono truncate" title={`served as ${e.served_model}`}>
              → {e.served_model}
            </span>
          )}
        </div>
      </div>
    ) },
    { key: 'status', header: t('ADMIN.AUDIT.COL_STATUS'), width: 100, render: e => {
      if (e.error_type) {
        const failure = formatEventFailure(e);
        const isPrecheck = isPrecheckLimitEvent(e);
        return (
          <span
            className={`inline-flex items-center gap-1 px-1.5 h-5 rounded-full text-[10px] font-medium border ${
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
        <span className={`inline-flex items-center px-1.5 h-5 rounded-full text-[10px] font-medium border ${
          ok ? 'bg-success/10 text-success border-success/30'
             : 'bg-error/10 text-error border-error/30'
        }`}>
          {e.status}
        </span>
      );
    } },
    // TTFT (Time-To-First-Token) 从 CLIProxyAPI 上游 usage event 同步过来。
    // 阈值：>3s warn (LLM 反应慢)；>5s error (用户基本会觉得卡死)。
    // 0 = 上游未上报（旧 record 或非流式调用），用 "-" 显示。
    { key: 'ttft', header: t('ADMIN.AUDIT.COL_TTFT', '首字'), width: 76, align: 'right', render: e => {
      const ms = e.upstream_ttft_ms || 0;
      const cls = ms > 5_000 ? 'text-error' : ms > 3_000 ? 'text-warning' : 'text-on-surface-variant';
      return <span className={`font-mono text-[11px] ${cls}`}>{ms ? formatLatency(ms) : '-'}</span>;
    } },
    { key: 'latency', header: t('ADMIN.AUDIT.COL_LATENCY', '延迟'), width: 76, align: 'right', render: e => {
      const ms = e.latency_ms || 0;
      const cls = ms > 10_000 ? 'text-error' : ms > 3_000 ? 'text-warning' : 'text-on-surface-variant';
      return <span className={`font-mono text-[11px] ${cls}`}>{ms ? formatLatency(ms) : '-'}</span>;
    } },
    { key: 'cost', header: t('ADMIN.AUDIT.COL_COST'), align: 'right', render: e => (
      <span className="font-mono text-[11px] text-primary">{formatMeterCost(e.charged_cost ?? e.cost)}</span>
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
        {t('ADMIN.AUDIT.FILTER_BTN')}
        {(userIdFilter || modelFilter || statusFilter || errorTypeFilter) && (
          <span className="inline-block w-1.5 h-1.5 rounded-full bg-primary" />
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
        aria-label={t('ADMIN.AUDIT.REFRESH_ARIA')}
      >
        <RefreshCw size={14} className={loading ? 'animate-spin' : ''} />
      </button>
    </>
  );

  return (
    <PageContainer>
      <PageHeader
        title={t('ADMIN.AUDIT.TITLE')}
        sub={t('ADMIN.AUDIT.SUB')}
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
              placeholder={t('ADMIN.AUDIT.FILTER_MODEL_PH')}
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
              placeholder={t('ADMIN.AUDIT.FILTER_ERROR_TYPE_PH')}
              className="bg-surface-container-high border border-outline-variant text-on-surface text-sm rounded-control px-3 py-1.5 outline-none focus:border-primary"
            />
          </div>
          <div className="flex items-center justify-end mt-3">
            <button
              type="button"
              onClick={() => { setUserIdFilter(''); setModelFilter(''); setStatusFilter(''); setErrorTypeFilter(''); }}
              className="text-xs text-on-surface-variant hover:text-on-surface"
            >{t('ADMIN.AUDIT.FILTER_CLEAR')}</button>
          </div>
        </Section>
      )}

      <DataTable
        compact
        columns={columns}
        rows={events}
        rowKey={e => e.id}
        loading={loading}
        emptyTitle={t('ADMIN.AUDIT.EMPTY_TITLE')}
        emptySub={(userIdFilter || modelFilter || statusFilter || errorTypeFilter)
          ? t('ADMIN.AUDIT.FILTER_TIP_TRY_CLEAR')
          : t('ADMIN.AUDIT.EMPTY_NORMAL_DESC')}
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
        title={drawerEvent ? `${t('ADMIN.AUDIT.DRAWER_PREFIX')} #${drawerEvent.id}` : ''}
        description={drawerEvent ? `${drawerEvent.username || `#${drawerEvent.user_id}`} · ${formatTime(drawerEvent.created_at)}` : ''}
        size="lg"
      >
        {drawerEvent && (
          <EventDetail event={drawerEvent} formatMeterCost={formatMeterCost} formatEventFailure={formatEventFailure} t={t} />
        )}
      </Drawer>
    </PageContainer>
  );
};

const EventDetail = ({ event, formatMeterCost, formatEventFailure, t }) => {
  const failure = formatEventFailure(event);
  const isPrecheck = isPrecheckLimitEvent(event);
  const aux = isAuxiliaryEvent(event);
  const revenueLabel = event.revenue_source === 'subscription'
    ? t('ADMIN.AUDIT.REVENUE_SUBSCRIPTION')
    : event.revenue_source === 'balance'
      ? t('ADMIN.AUDIT.REVENUE_BALANCE')
      : t('ADMIN.AUDIT.REVENUE_NONE');
  return (
    <div className="space-y-5">
      {/* 状态条 */}
      <div className={`rounded-overlay border px-4 py-3 ${
        failure
          ? (isPrecheck ? 'bg-warning/10 border-warning/30' : 'bg-error/10 border-error/30')
          : 'bg-success/10 border-success/30'
      }`}>
        <div className={`text-sm font-semibold ${failure ? (isPrecheck ? 'text-warning' : 'text-error') : 'text-success'}`}>
          {failure ? failure.label : t('ADMIN.AUDIT.STATUS_OK_PREFIX', { status: event.status })}
        </div>
        {failure?.detail && (
          <div className="text-xs text-on-surface-variant mt-1 break-all">{failure.detail}</div>
        )}
      </div>

      {/* 三列分组 */}
      <Section flat noPadding>
        <Field label={t('ADMIN.AUDIT.DETAIL_MODEL_LABEL')} mono value={`${event.requested_model || event.model_name} → ${event.served_model || event.model_name}`} />
        <Field
          label={t('ADMIN.AUDIT.DETAIL_INTERFACE_LABEL')}
          value={aux ? t('ADMIN.AUDIT.AUXILIARY_TIP') : t('ADMIN.AUDIT.GENERATIVE')}
          highlight={aux}
        />
        <Field label={t('ADMIN.AUDIT.DETAIL_TOKEN_SOURCE')} mono value={event.token_name || '-'} />
        <Field label={t('ADMIN.AUDIT.DETAIL_PATH')} mono value={event.request_path || '-'} />
        <Field label={t('ADMIN.AUDIT.DETAIL_IP')} mono value={event.ip_address || '-'} />
        <Field label={t('ADMIN.AUDIT.DETAIL_LATENCY')} value={formatLatency(event.latency_ms)} />
        <Field label={t('ADMIN.AUDIT.DETAIL_TTFT', '首字延迟')} value={event.upstream_ttft_ms ? formatLatency(event.upstream_ttft_ms) : '-'} />
        <Field label={t('ADMIN.AUDIT.DETAIL_HTTP_STATUS')} value={event.status} />
        {event.fallback_user_opt_in && (
          <Field label={t('ADMIN.AUDIT.DETAIL_FALLBACK_LABEL')} value={event.fallback_reason || t('ADMIN.AUDIT.DETAIL_FALLBACK_DEFAULT')} />
        )}
      </Section>

      <Section title={t('ADMIN.AUDIT.SECTION_BILLING')} flat>
        <div className="grid grid-cols-2 gap-x-4">
          <Field label={t('ADMIN.AUDIT.DETAIL_RAW_COST')} mono value={formatMeterCost(event.raw_cost ?? event.cost)} />
          <Field label={t('ADMIN.AUDIT.DETAIL_CHARGED_COST')} mono value={formatMeterCost(event.charged_cost ?? event.cost)} />
          <Field label={t('ADMIN.AUDIT.DETAIL_REVENUE_SOURCE')} value={revenueLabel} />
          <Field
            label={t('ADMIN.AUDIT.DETAIL_REVENUE_AMOUNT')}
            mono
            value={event.revenue_source ? formatMeterCost(event.effective_revenue || 0) : '-'}
            highlight={!!event.revenue_source}
          />
          <Field label={t('ADMIN.AUDIT.DETAIL_MODEL_WEIGHT')} mono value={`×${Number(event.model_weight || 1).toFixed(2)}`} />
          {Number(event.health_multiplier || 1) !== 1 && (
            <Field label={t('ADMIN.AUDIT.DETAIL_HEALTH_MULT')} mono value={`H×${Number(event.health_multiplier || 1).toFixed(2)}`} />
          )}
          <Field label={t('ADMIN.AUDIT.DETAIL_RULES_VERSION')} mono value={event.billing_rules_version || '-'} />
        </div>
      </Section>

      <Section title={t('ADMIN.AUDIT.SECTION_TOKEN')} flat>
        <div className="grid grid-cols-2 gap-x-4">
          <Field label={t('ADMIN.AUDIT.DETAIL_INPUT')} mono value={(event.prompt_tokens || 0).toLocaleString()} />
          <Field label={t('ADMIN.AUDIT.DETAIL_OUTPUT')} mono value={(event.completion_tokens || 0).toLocaleString()} />
          <Field label={t('ADMIN.AUDIT.DETAIL_REASONING')} mono value={(event.reasoning_tokens || 0).toLocaleString()} />
          <Field label={t('ADMIN.AUDIT.DETAIL_CACHE_READ')} mono value={(event.cached_tokens || 0).toLocaleString()} />
          <Field label={t('ADMIN.AUDIT.DETAIL_CACHE_WRITE')} mono value={(event.cache_write_tokens || 0).toLocaleString()} />
          <Field label={t('ADMIN.AUDIT.DETAIL_TOTAL_TOKENS')} mono value={(event.total_tokens || 0).toLocaleString()} highlight />
        </div>
      </Section>

      {usageLinesOf(event).length > 0 && (
        <Section title={t('ADMIN.AUDIT.SECTION_MEDIA')} flat sub={t('ADMIN.AUDIT.SECTION_MEDIA_SUB')}>
          <div className="space-y-2">
            {usageLinesOf(event).map((line) => (
              <div key={line.id || `${line.unit}-${line.direction}-${line.quantity}`} className="rounded-control border border-outline-variant bg-surface-container/40 px-3 py-2">
                <div className="flex items-center justify-between gap-3">
                  <span className="text-xs text-on-surface font-mono truncate" title={formatUsageLine(line, formatMeterCost)}>
                    {formatUsageLine(line, formatMeterCost)}
                  </span>
                  <span className="text-[10px] text-on-surface-variant shrink-0">{line.direction || '-'}</span>
                </div>
                {(line.cost_source || line.request_path) && (
                  <div className="mt-1 text-[10px] text-on-surface-variant font-mono truncate">
                    {line.cost_source || '-'} · {line.request_path || '-'}
                  </div>
                )}
              </div>
            ))}
          </div>
        </Section>
      )}

      {(event.precheck_input_tokens || event.precheck_charged_cost || event.precheck_quota_limit) && (
        <Section title={t('ADMIN.AUDIT.SECTION_PRECHECK')} flat sub={t('ADMIN.AUDIT.SECTION_PRECHECK_SUB')}>
          <div className="grid grid-cols-2 gap-x-4">
            <Field label={t('ADMIN.AUDIT.DETAIL_PC_INPUT')} mono value={(event.precheck_input_tokens || 0).toLocaleString()} />
            <Field label={t('ADMIN.AUDIT.DETAIL_PC_OUTPUT')} mono value={(event.precheck_output_tokens || 0).toLocaleString()} />
            <Field label={t('ADMIN.AUDIT.DETAIL_PC_RAW_COST')} mono value={formatMeterCost(event.precheck_raw_cost || 0)} />
            <Field label={t('ADMIN.AUDIT.DETAIL_PC_CHARGED_COST')} mono value={formatMeterCost(event.precheck_charged_cost || 0)} />
            <Field label={t('ADMIN.AUDIT.DETAIL_PC_QUOTA_LIMIT')} mono value={formatMeterCost(event.precheck_quota_limit || 0)} />
            <Field label={t('ADMIN.AUDIT.DETAIL_PC_QUOTA_USED')} mono value={formatMeterCost(event.precheck_quota_used || 0)} />
            <Field label={t('ADMIN.AUDIT.DETAIL_PC_QUOTA_REMAIN')} mono value={formatMeterCost(event.precheck_quota_remaining || 0)} />
            <Field label={t('ADMIN.AUDIT.DETAIL_PC_WINDOW_END')} mono value={event.precheck_window_end_at ? formatTime(event.precheck_window_end_at) : '-'} />
            <Field label={t('ADMIN.AUDIT.DETAIL_PC_PLAN_ID')} mono value={event.precheck_quota_plan_id || '-'} />
            {/* block_reason / provider / auth_index etc remain English machine codes — labels stay literal */}
            <Field label="block_reason" mono value={event.block_reason || '-'} />
          </div>
        </Section>
      )}

      {(event.upstream_provider || event.upstream_auth_index) && (
        <Section title={t('ADMIN.AUDIT.SECTION_UPSTREAM')} flat sub={t('ADMIN.AUDIT.SECTION_UPSTREAM_SUB')}>
          <div className="grid grid-cols-2 gap-x-4">
            {/* Upstream attribution field labels stay literal — they are machine-side identifiers. */}
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
        <Section title={t('ADMIN.AUDIT.SECTION_ERROR')} flat>
          <Field label="error_type" mono value={event.error_type || '-'} />
          <div className="mt-2 text-[11px] text-error font-mono whitespace-pre-wrap break-all bg-error/5 border border-error/20 rounded-control p-2">
            {event.error_message || t('ADMIN.AUDIT.ERROR_NO_MESSAGE')}
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
