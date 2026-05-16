import React, { useState, useEffect, useCallback, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { Package, RefreshCw, RotateCcw, Search, X, Gift, ChevronDown, Gauge, TimerReset, Activity, Users, Undo2, AlertTriangle } from 'lucide-react';
import toast from 'react-hot-toast';
import { useConfirm } from '../context/ConfirmContext';
import { authFetch } from '../utils/authFetch';
import { remainingColor, safePct, fmtRelativeFromNow, fmtAbsoluteShort } from '../utils/credits';
import { Clock } from 'lucide-react';
import AdminGrantSubscriptionModal from './AdminGrantSubscriptionModal';
import { useModalA11y } from '../hooks/useModalA11y';

// Aligned with backend adminSubItem fields.
const STATUS_OPTIONS = ['', 'active', 'canceled', 'expired', 'refunded', 'paused', 'revoked'];
const RESET_USAGE_STATUS_OPTIONS = ['active', 'canceled', 'expired'];

// Status display style.
const statusStyle = (s) => {
  switch (s) {
    case 'active': return { bg: 'bg-success/10', text: 'text-success' };
    case 'canceled': return { bg: 'bg-warning/10', text: 'text-warning' };
    case 'expired': return { bg: 'bg-surface-variant/10', text: 'text-on-surface-variant' };
    case 'refunded': return { bg: 'bg-error/10', text: 'text-error' };
    case 'paused': return { bg: 'bg-primary/10', text: 'text-primary' };
    case 'revoked': return { bg: 'bg-surface-variant/10', text: 'text-on-surface-variant' };
    default: return { bg: 'bg-surface-container-high', text: 'text-on-surface-variant' };
  }
};

const fmtTime = (iso) => {
  if (!iso) return '-';
  try { return new Date(iso).toLocaleString(); } catch { return iso; }
};

const parseUsageDetails = (raw) => {
  if (!raw) return [];
  try {
    const rows = JSON.parse(raw);
    return Array.isArray(rows) ? rows : [];
  } catch {
    return [];
  }
};

const fmtUsageValue = (value, unit) => {
  const n = Number(value || 0);
  if (unit === 'api_cost_usd') return `$${n.toFixed(n >= 100 ? 0 : 2)}`;
  if (unit === 'request_count') return `${Math.round(n)}`;
  if (unit && String(unit).includes('tokens')) return n >= 1000 ? `${(n / 1000).toFixed(1)}k` : `${Math.round(n)}`;
  return n.toFixed(2);
};

const formatWindowLabel = (seconds, t) => {
  const n = Number(seconds || 0);
  if (!n) return t('SUB.WINDOW_PACKAGE_PERIOD', '套餐周期');
  if (n === 5 * 3600) return t('SUB.WINDOW_5H', '5 小时');
  if (n === 7 * 86400) return t('SUB.WINDOW_7D', '7 天');
  if (n % 86400 === 0) return t('SUB.WINDOW_DAYS', '{{count}} 天', { count: n / 86400 });
  if (n % 3600 === 0) return t('SUB.WINDOW_HOURS', '{{count}} 小时', { count: n / 3600 });
  return t('SUB.WINDOW_SECONDS', '{{count}} 秒', { count: n });
};

const formatUsageName = (d, t) => {
  const window = formatWindowLabel(d.window_seconds, t);
  if (d.unit === 'api_cost_usd') return t('SUB.PLAN_TITLE_API_CREDIT', '{{window}} API 等值额度', { window });
  if (d.unit === 'request_count') return t('SUB.PLAN_TITLE_REQUESTS', '{{window}} 调用次数', { window });
  if (d.unit && String(d.unit).includes('tokens')) return t('SUB.PLAN_TITLE_TOKENS', '{{window}} Token 额度', { window });
  return d.name || `plan#${d.plan_id}`;
};

const readConfirmValue = (result) => {
  if (result && typeof result === 'object' && Object.prototype.hasOwnProperty.call(result, 'value')) {
    return result.value;
  }
  return result;
};

const resetUsageErrorMessage = (code, t) => {
  switch (code) {
    case 'ERR_RESET_CONFIRM_REQUIRED':
      return t('API.ERR_RESET_CONFIRM_REQUIRED', 'Reset confirmation is required.');
    case 'ERR_RESET_NOTE_REQUIRED':
      return t('API.ERR_RESET_NOTE_REQUIRED', 'Reset note is required.');
    case 'ERR_RESET_NOTE_TOO_LONG':
      return t('API.ERR_RESET_NOTE_TOO_LONG', 'Reset note must be 500 characters or less.');
    case 'ERR_RESET_SCOPE_TOO_LARGE':
      return t('API.ERR_RESET_SCOPE_TOO_LARGE', 'Reset scope is too large; narrow the filters.');
    default:
      return null;
  }
};

const summarizePageUsage = (rows) => {
  const details = rows.flatMap((row) => parseUsageDetails(row.usage_details_json));
  const active = rows.filter((row) => row.status === 'active').length;
  const apiRows = details.filter((d) => d.unit === 'api_cost_usd');
  const fiveHour = apiRows.filter((d) => Number(d.window_seconds || 0) === 5 * 3600);
  const sevenDay = apiRows.filter((d) => Number(d.window_seconds || 0) === 7 * 86400);
  const sum = (items, field) => items.reduce((acc, item) => acc + Number(item[field] || 0), 0);
  const maxPct = details.reduce((max, d) => Math.max(max, Number(d.pct || 0)), 0);
  return {
    active,
    fiveHourUsed: sum(fiveHour, 'consumed'),
    fiveHourLimit: sum(fiveHour, 'limit'),
    sevenDayUsed: sum(sevenDay, 'consumed'),
    sevenDayLimit: sum(sevenDay, 'limit'),
    maxPct: safePct(maxPct),
  };
};

const AdminSubscriptions = () => {
  const { t } = useTranslation();
  const confirm = useConfirm();
  const [rows, setRows] = useState([]);
  const [meta, setMeta] = useState({ total: 0, page: 1, page_size: 50 });
  const [loading, setLoading] = useState(true);
  const [statusFilter, setStatusFilter] = useState('');
  const [searchQ, setSearchQ] = useState('');
  const [searchSubmitted, setSearchSubmitted] = useState('');
  const [refundingId, setRefundingId] = useState(null);
  const [revokingId, setRevokingId] = useState(null);
  const [expandedRow, setExpandedRow] = useState(null);
  const [grantModalOpen, setGrantModalOpen] = useState(false);
  const [resetUsageModalOpen, setResetUsageModalOpen] = useState(false);

  // fix MAJOR M8 (gemini round 20): prevent rapid status/search changes from being overwritten by stale responses.
  const reqIdRef = useRef(0);

  const load = useCallback(async (page = 1) => {
    const myReqId = ++reqIdRef.current;
    setLoading(true);
    try {
      const params = new URLSearchParams({ page: String(page), page_size: '50' });
      if (statusFilter) params.set('status', statusFilter);
      if (searchSubmitted) params.set('q', searchSubmitted);
      const json = await authFetch(`/api/admin/subscriptions?${params.toString()}`);
      // M8: drop stale responses that arrive after a newer request.
      if (myReqId !== reqIdRef.current) return;
      if (json.success) {
        setRows(json.data || []);
        setMeta(json.meta || { total: 0, page, page_size: 50 });
      } else {
        toast.error(json.message || t('ADMIN_SUBS.LOAD_FAIL', '加载失败'));
      }
    } catch {
      if (myReqId !== reqIdRef.current) return;
      toast.error(t('ADMIN_SUBS.LOAD_FAIL', '加载失败'));
    } finally {
      if (myReqId === reqIdRef.current) setLoading(false);
    }
  }, [t, statusFilter, searchSubmitted]);

  useEffect(() => { load(1); }, [load]);

  const submitSearch = (e) => {
    e?.preventDefault?.();
    setSearchSubmitted(searchQ.trim());
  };

  const pageUsage = React.useMemo(() => summarizePageUsage(rows), [rows]);

  const refund = async (sub) => {
    // Ask for refund amount and reason; default suggestion is based on remaining days.
    const maxAmount = (sub.purchased_price_usd || 0).toFixed(2);
    const suggested = (sub.suggested_refund_usd || 0).toFixed(2);
    const amountResult = await confirm({
      level: 'L3',
      danger: true,
      title: t('ADMIN_SUBS.REFUND_TITLE', '订阅退款（平台内部）'),
      message: t('ADMIN_SUBS.REFUND_BODY', {
        user: sub.username || `#${sub.user_id}`,
        pkg: sub.package_name || `#${sub.package_id}`,
        remainDays: sub.remaining_days.toFixed(1),
        totalDays: sub.total_days.toFixed(1),
        suggested,
        max: maxAmount,
        usage: sub.usage_max_pct.toFixed(1),
      }),
      input: { label: t('ADMIN_SUBS.REFUND_AMOUNT', '退款金额（USD）'), placeholder: maxAmount, defaultValue: suggested },
      confirmPhrase: `REFUND ${sub.id}`,
      confirmText: t('ADMIN_SUBS.REFUND_CONFIRM', '确认退款'),
    });
    const amountStr = readConfirmValue(amountResult);
    if (amountStr === false || amountStr === null || amountStr === undefined || amountStr === '') return; // User canceled.
    const amount = parseFloat(amountStr);
    if (!isFinite(amount) || amount <= 0) {
      toast.error(t('ADMIN_SUBS.AMOUNT_INVALID', '请输入有效金额'));
      return;
    }
    const reasonResult = await confirm({
      title: t('ADMIN_SUBS.REASON_TITLE', '退款原因'),
      message: t('ADMIN_SUBS.REASON_BODY', '请简要说明退款原因（写入审计日志）'),
      input: {
        label: t('ADMIN_SUBS.REASON_LABEL', '原因'),
        placeholder: t('ADMIN_SUBS.REASON_PLACEHOLDER', '协商退款 / 服务问题 / 用户撤回 ...'),
        defaultValue: t('ADMIN_SUBS.REASON_DEFAULT', '协商退款'),
      },
    });
    if (reasonResult === false || reasonResult === null || reasonResult === undefined) return;
    const reason = readConfirmValue(reasonResult);

    setRefundingId(sub.id);
    try {
      const json = await authFetch(`/api/admin/subscriptions/${sub.id}/refund`, {
        method: 'POST',
        body: { amount_usd: amount, reason: String(reason || t('ADMIN_SUBS.REASON_DEFAULT', '协商退款')) },
      });
      if (json.success) {
        toast.success(t('ADMIN_SUBS.REFUND_OK', '已退款 ${{amount}}', { amount: amount.toFixed(2) }));
        load(meta.page);
      } else {
        toast.error(json.message || t('ADMIN_SUBS.REFUND_FAIL', '退款失败'));
      }
    } catch {
      toast.error(t('API.ERR_NETWORK', '网络异常'));
    } finally {
      setRefundingId(null);
    }
  };

  const revokeGrant = async (sub) => {
    const reasonResult = await confirm({
      title: t('ADMIN_SUBS.REVOKE_TITLE', '收回赠送权益'),
      message: t('ADMIN_SUBS.REVOKE_BODY', {
        user: sub.username || `#${sub.user_id}`,
        pkg: sub.package_name || `#${sub.package_id}`,
      }),
      input: {
        label: t('ADMIN_SUBS.REVOKE_REASON_LABEL', '收回原因'),
        placeholder: t('ADMIN_SUBS.REVOKE_REASON_PH', '发放错误 / 内测结束 / 风控处理 ...'),
        defaultValue: t('ADMIN_SUBS.REVOKE_REASON_DEFAULT', '发放错误，收回赠送权益'),
      },
      confirmText: t('ADMIN_SUBS.REVOKE_CONFIRM', '确认收回'),
    });
    if (reasonResult === false || reasonResult === null || reasonResult === undefined) return;
    const reason = readConfirmValue(reasonResult);
    const trimmed = String(reason || '').trim();
    if (!trimmed) {
      toast.error(t('ADMIN_SUBS.REVOKE_REASON_REQUIRED', '请填写收回原因'));
      return;
    }

    setRevokingId(sub.id);
    try {
      const json = await authFetch(`/api/admin/subscriptions/${sub.id}/revoke-grant`, {
        method: 'POST',
        body: { reason: trimmed },
      });
      if (json.success) {
        toast.success(t('ADMIN_SUBS.REVOKE_OK', '已收回赠送权益'));
        load(meta.page);
      } else {
        toast.error(json.message || t('ADMIN_SUBS.REVOKE_FAIL', '收回失败'));
      }
    } catch {
      toast.error(t('API.ERR_NETWORK', '网络异常'));
    } finally {
      setRevokingId(null);
    }
  };

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h2 className="text-xl font-semibold text-on-surface flex items-center gap-2">
          <Package size={20} className="text-primary" />
          {t('ADMIN_SUBS.TITLE', '订阅总览（用户买了什么）')}
        </h2>
        <div className="flex items-center gap-2">
          <button
            type="button"
            onClick={() => setResetUsageModalOpen(true)}
            className="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-control text-sm bg-surface-container-high text-on-surface-variant hover:text-on-surface hover:bg-surface-container-highest border border-outline-variant"
            title={t('ADMIN_SUBS.RESET_USAGE.BUTTON_TITLE', 'Reset current-cycle usage without moving refresh windows')}
          >
            <TimerReset size={14} />
            {t('ADMIN_SUBS.RESET_USAGE.BUTTON', '重置已用额度')}
          </button>
          <button
            type="button"
            onClick={() => setGrantModalOpen(true)}
            className="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-control text-sm bg-success/10 text-success hover:bg-success/20 border border-success/30"
            title={t('ADMIN_SUBS.GRANT_BTN_TITLE', '管理员赠送订阅给指定用户')}
          >
            <Gift size={14} />
            {t('ADMIN_SUBS.GRANT_BTN', '赠送')}
          </button>
          <button onClick={() => load(meta.page)}
            className="text-on-surface-variant hover:text-on-surface p-2 rounded-control hover:bg-surface-container-high"
            aria-label={t('ADMIN_SUBS.RELOAD', '重新加载')}>
            <RefreshCw size={16} className={loading ? 'animate-spin' : ''} />
          </button>
        </div>
      </div>

      <AdminGrantSubscriptionModal
        open={grantModalOpen}
        onClose={() => setGrantModalOpen(false)}
        onSuccess={() => load(meta.page)}
      />

      <ResetUsageModal
        open={resetUsageModalOpen}
        onClose={() => setResetUsageModalOpen(false)}
        onSuccess={() => load(meta.page)}
      />

      <div className="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-4 gap-3">
        <AdminUsageMetric
          icon={Users}
          label={t('ADMIN_SUBS.METRIC_ACTIVE_PAGE', '当前页活跃订阅')}
          value={`${pageUsage.active}`}
          sub={t('ADMIN_SUBS.METRIC_TOTAL_RECORDS', '共 {{count}} 条记录', { count: meta.total || rows.length })}
        />
        <AdminUsageMetric
          icon={TimerReset}
          label={t('ADMIN_SUBS.METRIC_5H_API', '5 小时 API 等值')}
          value={`${fmtUsageValue(pageUsage.fiveHourUsed, 'api_cost_usd')} / ${fmtUsageValue(pageUsage.fiveHourLimit, 'api_cost_usd')}`}
          sub={t('ADMIN_SUBS.METRIC_PAGE_TOTAL', '当前页合计')}
          pct={pageUsage.fiveHourLimit > 0 ? pageUsage.fiveHourUsed / pageUsage.fiveHourLimit * 100 : 0}
        />
        <AdminUsageMetric
          icon={Gauge}
          label={t('ADMIN_SUBS.METRIC_7D_API', '7 天 API 等值')}
          value={`${fmtUsageValue(pageUsage.sevenDayUsed, 'api_cost_usd')} / ${fmtUsageValue(pageUsage.sevenDayLimit, 'api_cost_usd')}`}
          sub={t('ADMIN_SUBS.METRIC_PAGE_TOTAL', '当前页合计')}
          pct={pageUsage.sevenDayLimit > 0 ? pageUsage.sevenDayUsed / pageUsage.sevenDayLimit * 100 : 0}
        />
        <AdminUsageMetric
          icon={Activity}
          label={t('ADMIN_SUBS.METRIC_MAX_USAGE', '最高用量水位')}
          value={`${pageUsage.maxPct.toFixed(1)}%`}
          sub={t('ADMIN_SUBS.METRIC_PAGE_MAX_PLAN', '当前页最高 plan')}
          pct={pageUsage.maxPct}
        />
      </div>

      {/* Filter bar */}
      <form onSubmit={submitSearch} className="flex flex-wrap gap-3 items-center">
        {/* Add an aria-label so screen readers announce the select purpose. */}
        <select value={statusFilter} onChange={e => setStatusFilter(e.target.value)}
          aria-label={t('ADMIN_SUBS.STATUS_FILTER_LABEL', '按状态筛选订阅')}
          className="bg-surface-container-high border border-outline-variant rounded-control px-3 py-1.5 text-sm">
          {/* Status value i18n. */}
          {STATUS_OPTIONS.map(s => (
            <option key={s} value={s}>
              {s ? t(`ADMIN_SUBS.STATUS_${s.toUpperCase()}`, s) : t('ADMIN_SUBS.ALL_STATUS', '全部状态')}
            </option>
          ))}
        </select>
        <div className="relative flex-1 min-w-[200px]">
          <Search size={14} className="absolute left-3 top-1/2 -translate-y-1/2 text-on-surface-variant" />
          <input type="text" value={searchQ} onChange={e => setSearchQ(e.target.value)}
            placeholder={t('ADMIN_SUBS.SEARCH_PLACEHOLDER', '搜索用户名 / 手机号 / GitHub ID')}
            className="w-full bg-surface-container-high border border-outline-variant rounded-control pl-9 pr-9 py-1.5 text-sm" />
          {searchQ && (
            <button type="button" onClick={() => { setSearchQ(''); setSearchSubmitted(''); }}
              className="absolute right-2 top-1/2 -translate-y-1/2 text-on-surface-variant hover:text-on-surface">
              <X size={14} />
            </button>
          )}
        </div>
        <button type="submit" className="bg-primary text-on-primary px-4 py-1.5 rounded-control text-sm font-medium">
          {t('ADMIN_SUBS.SEARCH', '搜索')}
        </button>
      </form>

      {/* List */}
      <div className="fl-card overflow-hidden">
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="bg-surface-container-high text-xs text-on-surface-variant">
              <tr>
                <th className="px-3 py-2 text-left">ID</th>
                <th className="px-3 py-2 text-left">{t('ADMIN_SUBS.USER', '用户')}</th>
                <th className="px-3 py-2 text-left">{t('ADMIN_SUBS.PACKAGE', '产品')}</th>
                <th className="px-3 py-2 text-right">{t('ADMIN_SUBS.PRICE', '价格')}</th>
                <th className="px-3 py-2 text-right">{t('ADMIN_SUBS.REMAINING_DAYS', '剩余天数')}</th>
                <th className="px-3 py-2 text-right">{t('ADMIN_SUBS.SUGGESTED_REFUND', '建议退款')}</th>
                <th className="px-3 py-2 text-left">{t('ADMIN_SUBS.STATUS', '状态')}</th>
                <th className="px-3 py-2 text-center">{t('ADMIN_SUBS.ACTIONS', '操作')}</th>
              </tr>
            </thead>
            <tbody>
              {loading ? (
                <tr><td colSpan={8} className="text-center py-8 text-on-surface-variant">{t('ADMIN_SUBS.LOADING', '加载中...')}</td></tr>
              ) : rows.length === 0 ? (
                <tr><td colSpan={8} className="text-center py-8 text-on-surface-variant">{t('ADMIN_SUBS.EMPTY', '没有匹配的订阅')}</td></tr>
              ) : rows.map(sub => {
                const sty = statusStyle(sub.status);
                // Granted subscriptions are not refundable; backend also rejects ERR_REFUND_GRANTED_SUB.
                // Include paused because the backend accepts paused subscriptions for refund.
                const refundable = !sub.is_granted && ['active', 'canceled', 'expired', 'paused'].includes(sub.status);
                const revocableGrant = sub.is_granted && ['active', 'paused'].includes(sub.status);
                // Remaining-days color: high/medium/low refund proportions.
                const daysPctColor =
                  sub.time_remaining_pct >= 50 ? 'text-success' :
                  sub.time_remaining_pct >= 20 ? 'text-warning' : 'text-error';
                const expanded = expandedRow === sub.id;
                const toggleExpand = () => setExpandedRow(expanded ? null : sub.id);
                return (
                  <React.Fragment key={sub.id}>
                    {/* Keep table semantics; the first-cell button owns keyboard expansion. */}
                    <tr className="border-t border-outline-variant/40 hover:bg-surface-container-high/40 cursor-pointer"
                        onClick={toggleExpand}>
                      <td className="px-3 py-2 font-mono text-xs text-on-surface-variant">
                        <div className="flex items-center gap-1.5">
                          <button
                            type="button"
                            onClick={(e) => { e.stopPropagation(); toggleExpand(); }}
                            aria-expanded={expanded}
                            aria-label={expanded
                              ? t('ADMIN_SUBS.COLLAPSE_DETAILS', '收起详情')
                              : t('ADMIN_SUBS.EXPAND_DETAILS', '展开详情')}
                            className="p-0.5 -ml-0.5 rounded-control hover:bg-on-surface/10 focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary">
                            <ChevronDown
                              size={14}
                              className={`transition-transform ${expanded ? 'rotate-180' : ''}`}
                            />
                          </button>
                          <span>#{sub.id}</span>
                        </div>
                      </td>
                      <td className="px-3 py-2">
                        <div className="text-on-surface">{sub.username || t('ADMIN_SUBS.USER_FALLBACK', '用户#{{id}}', { id: sub.user_id })}</div>
                        {sub.user_phone && <div className="text-xs text-on-surface-variant font-mono">{sub.user_phone}</div>}
                      </td>
                      <td className="px-3 py-2">
                        <div className="text-on-surface flex items-center gap-1.5">
                          <span>{sub.package_name || t('ADMIN_SUBS.PACKAGE_FALLBACK', '套餐#{{id}}', { id: sub.package_id })}</span>
                          {sub.is_granted && (
                            <span
                              className="inline-flex items-center gap-0.5 px-1.5 py-0.5 rounded-control text-[10px] bg-success/10 text-success border border-success/20"
                              title={sub.grant_reason || ''}
                            >
                              <Gift size={10} />{t('ADMIN_SUBS.GRANTED_TAG', '赠送')}
                            </span>
                          )}
                        </div>
                        {/* Phase 8 removed addons; product_type is always subscription. */}
                      </td>
                      <td className="px-3 py-2 text-right font-mono text-on-surface">
                        ${sub.purchased_price_usd?.toFixed(2) || '0.00'}
                      </td>
                      <td className="px-3 py-2 text-right">
                        <div className={`font-mono ${daysPctColor}`}>
                          {t('ADMIN_SUBS.DAYS_VALUE', '{{count}} 天', { count: sub.remaining_days?.toFixed(1) || '0' })}
                        </div>
                        <div className="text-xs text-on-surface-variant font-mono">
                          / {t('ADMIN_SUBS.DAYS_VALUE', '{{count}} 天', { count: sub.total_days?.toFixed(0) || '0' })}
                        </div>
                      </td>
                      <td className="px-3 py-2 text-right font-mono text-success">
                        ${sub.suggested_refund_usd?.toFixed(2) || '0.00'}
                      </td>
                      <td className="px-3 py-2">
                        <span className={`px-2 py-0.5 rounded-full text-xs ${sty.bg} ${sty.text}`}>
                          {/* Status enum i18n. */}
                          {t(`ADMIN_SUBS.STATUS_${sub.status.toUpperCase()}`, sub.status)}
                        </span>
                      </td>
                      <td className="px-3 py-2 text-center">
                        {sub.is_granted ? (
                          <button
                            disabled={!revocableGrant || revokingId === sub.id}
                            onClick={(e) => { e.stopPropagation(); revokeGrant(sub); }}
                            className={`inline-flex items-center gap-1 px-3 py-1 rounded-control text-xs ${
                              revocableGrant
                                ? 'bg-warning/10 text-warning hover:bg-warning/20'
                                : 'bg-surface-container-high text-on-surface-variant cursor-not-allowed'
                            }`}
                            title={revocableGrant ? t('ADMIN_SUBS.REVOKE_BTN', '收回') : t('ADMIN_SUBS.REVOKE_DISABLED', '该赠送状态不可收回')}>
                            <Undo2 size={12} />
                            {revokingId === sub.id ? t('ADMIN_SUBS.REVOKING', '处理中...') : t('ADMIN_SUBS.REVOKE_BTN', '收回')}
                          </button>
                        ) : (
                          <button
                            disabled={!refundable || refundingId === sub.id}
                            onClick={(e) => { e.stopPropagation(); refund(sub); }}
                            className={`inline-flex items-center gap-1 px-3 py-1 rounded-control text-xs ${
                              refundable
                                ? 'bg-error/10 text-error hover:bg-error/20'
                                : 'bg-surface-container-high text-on-surface-variant cursor-not-allowed'
                            }`}
                            title={refundable ? t('ADMIN_SUBS.REFUND_BTN', '退款') : t('ADMIN_SUBS.REFUND_DISABLED', '该状态不可退款')}>
                            <RotateCcw size={12} />
                            {refundingId === sub.id ? t('ADMIN_SUBS.REFUNDING', '处理中...') : t('ADMIN_SUBS.REFUND_BTN', '退款')}
                          </button>
                        )}
                      </td>
                    </tr>
                    {expandedRow === sub.id && (
                      <tr className="bg-surface-container-high/30">
                        <td colSpan={8} className="px-6 py-3 space-y-3">
                          {/* Timeline */}
                          <div>
                            <div className="text-xs text-on-surface-variant mb-1">{t('ADMIN_SUBS.TIMELINE', '订阅时间线')}</div>
                            <div className="text-xs space-y-0.5">
                              <div>{t('ADMIN_SUBS.START_AT', '开始：')}<span className="font-mono text-on-surface">{fmtTime(sub.start_at)}</span></div>
                              <div>{t('ADMIN_SUBS.END_AT', '结束：')}<span className="font-mono text-on-surface">{fmtTime(sub.end_at)}</span></div>
                              {sub.canceled_at && (
                                <div className="text-warning">
                                  {sub.status === 'revoked' ? t('ADMIN_SUBS.REVOKE_ACTION', '收回') : t('ADMIN_SUBS.CANCEL_ACTION', '取消')}:
                                  <span className="font-mono">{fmtTime(sub.canceled_at)}</span>
                                </div>
                              )}
                              <div className="text-on-surface-variant">
                                {t('ADMIN_SUBS.TIME_REMAINING', '时间剩余：')}<span className={`font-mono ${daysPctColor}`}>{sub.time_remaining_pct?.toFixed(1)}%</span>
                              </div>
                            </div>
                          </div>
                          {/* Usage details for reference. */}
                          {sub.usage_details_json && (
                            <div>
                              <div className="text-xs text-on-surface-variant mb-1">
                                {t('ADMIN_SUBS.USAGE_DETAIL', '用量参考（plan 周期内的滚动窗口）')}
                              </div>
                              <div className="grid grid-cols-1 lg:grid-cols-2 gap-3">
                                {parseUsageDetails(sub.usage_details_json).length === 0 ? (
                                  <div className="text-xs text-on-surface-variant">{t('ADMIN_SUBS.NO_USAGE_RECORDS', '无用量记录')}</div>
                                ) : (
                                  parseUsageDetails(sub.usage_details_json).map((d, i) => (
                                    <AdminUsageDetailMeter key={`${d.plan_id || i}:${d.name || ''}`} detail={d} />
                                  ))
                                )}
                              </div>
                              <div className="text-xs text-on-surface-variant mt-1 italic">
                                {t('ADMIN_SUBS.USAGE_REFUND_HINT', '* 用量是 plan 滚动窗口内的限额，仅供参考。退款应按剩余天数计算。')}
                              </div>
                            </div>
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
      </div>

      {/* Pagination */}
      <div className="flex items-center justify-between text-xs text-on-surface-variant">
        <span>{t('ADMIN_SUBS.TOTAL', '共 {{count}} 条', { count: meta.total })}</span>
        <div className="flex gap-2">
          <button disabled={meta.page <= 1} onClick={() => load(meta.page - 1)}
            className="px-3 py-1 rounded-control bg-surface-container-high disabled:opacity-50">←</button>
          <span>{meta.page} / {Math.max(1, Math.ceil(meta.total / meta.page_size))}</span>
          <button disabled={meta.page * meta.page_size >= meta.total} onClick={() => load(meta.page + 1)}
            className="px-3 py-1 rounded-control bg-surface-container-high disabled:opacity-50">→</button>
        </div>
      </div>
    </div>
  );
};

const ResetUsageModal = ({ open, onClose, onSuccess }) => {
  const { t } = useTranslation();
  const [packages, setPackages] = useState([]);
  const [loadingPackages, setLoadingPackages] = useState(false);
  const [selectedPackageIds, setSelectedPackageIds] = useState([]);
  const [selectedUsers, setSelectedUsers] = useState([]);
  const [userQuery, setUserQuery] = useState('');
  const [userSuggestions, setUserSuggestions] = useState([]);
  const [searchingUsers, setSearchingUsers] = useState(false);
  const [selectedStatuses, setSelectedStatuses] = useState(['active']);
  const [note, setNote] = useState('');
  const [confirmText, setConfirmText] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const modalRef = useRef(null);
  const closeBtnRef = useRef(null);
  const searchReqRef = useRef(0);
  const { onBackdropClick } = useModalA11y(open, () => !submitting && onClose(), closeBtnRef, modalRef);

  useEffect(() => {
    if (!open) return;
    setSelectedPackageIds([]);
    setSelectedUsers([]);
    setUserQuery('');
    setUserSuggestions([]);
    setSelectedStatuses(['active']);
    setNote('');
    setConfirmText('');
  }, [open]);

  useEffect(() => {
    if (!open) return;
    setLoadingPackages(true);
    authFetch('/api/admin/packages')
      .then((j) => {
        if (j?.success) {
          setPackages(j.data || []);
        } else {
          toast.error(j?.message || t('ADMIN_SUBS.RESET_USAGE.LOAD_PACKAGES_FAIL', '套餐列表加载失败'));
        }
      })
      .catch(() => toast.error(t('API.ERR_NETWORK', '网络异常')))
      .finally(() => setLoadingPackages(false));
  }, [open, t]);

  const searchUsers = useCallback(async (q) => {
    if (!q || q.length < 2) {
      setUserSuggestions([]);
      return;
    }
    const myReqId = ++searchReqRef.current;
    setSearchingUsers(true);
    try {
      const params = new URLSearchParams({ search: q, page: '1', page_size: '10' });
      const j = await authFetch(`/api/admin/users?${params.toString()}`);
      if (myReqId !== searchReqRef.current) return;
      if (j?.success) {
        const selectedIds = new Set(selectedUsers.map((u) => u.id));
        setUserSuggestions((j.data || []).filter((u) => u.role === 'user' && !selectedIds.has(u.id)));
      }
    } catch {
      // Suggestions are a convenience; the modal can continue without them.
    } finally {
      if (myReqId === searchReqRef.current) setSearchingUsers(false);
    }
  }, [selectedUsers]);

  useEffect(() => {
    if (!open) return;
    const trimmed = userQuery.trim();
    const handle = setTimeout(() => searchUsers(trimmed), 300);
    return () => clearTimeout(handle);
  }, [userQuery, open, searchUsers]);

  if (!open) return null;

  const togglePackage = (id) => {
    setSelectedPackageIds((prev) => prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id]);
  };
  const toggleStatus = (status) => {
    setSelectedStatuses((prev) => prev.includes(status) ? prev.filter((x) => x !== status) : [...prev, status]);
  };
  const addUser = (user) => {
    setSelectedUsers((prev) => prev.some((u) => u.id === user.id) ? prev : [...prev, { id: user.id, username: user.username }]);
    setUserQuery('');
    setUserSuggestions([]);
  };
  const removeUser = (id) => {
    setSelectedUsers((prev) => prev.filter((u) => u.id !== id));
  };

  const trimmedNote = note.trim();
  const canSubmit = !submitting && confirmText === 'RESET' && trimmedNote.length > 0 && trimmedNote.length <= 500;

  const submit = async () => {
    if (!trimmedNote) {
      toast.error(t('ADMIN_SUBS.RESET_USAGE.NOTE_REQUIRED', '请填写备注'));
      return;
    }
    if (trimmedNote.length > 500) {
      toast.error(t('ADMIN_SUBS.RESET_USAGE.NOTE_TOO_LONG', '备注不能超过 500 字'));
      return;
    }
    if (confirmText !== 'RESET') {
      toast.error(t('ADMIN_SUBS.RESET_USAGE.CONFIRM_REQUIRED', '请输入 RESET 解锁提交'));
      return;
    }
    setSubmitting(true);
    try {
      const json = await authFetch('/api/admin/subscriptions/reset-usage', {
        method: 'POST',
        body: {
          package_ids: selectedPackageIds,
          user_ids: selectedUsers.map((u) => u.id),
          statuses: selectedStatuses,
          note: trimmedNote,
          confirm: 'YES_RESET_USAGE',
        },
      });
      if (json?.success) {
        toast.success(t('ADMIN_SUBS.RESET_USAGE.SUCCESS', '已重置 {{count}} 个订阅', { count: json.reset_count || 0 }));
        onSuccess?.();
        onClose();
      } else {
        toast.error(resetUsageErrorMessage(json?.message_code, t) || json?.message || t('ADMIN_SUBS.RESET_USAGE.FAIL', '重置失败'));
      }
    } catch {
      toast.error(t('API.ERR_NETWORK', '网络异常'));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div
      ref={modalRef}
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/60"
      onClick={onBackdropClick}
      role="dialog"
      aria-modal="true"
      aria-labelledby="reset-usage-modal-title"
    >
      <div className="bg-surface-container rounded-overlay border border-outline-variant w-full max-w-2xl m-4 max-h-[90vh] overflow-y-auto">
        <div className="flex items-center justify-between px-5 py-4 border-b border-outline-variant">
          <h3 id="reset-usage-modal-title" className="text-lg font-semibold text-on-surface flex items-center gap-2">
            <TimerReset size={18} className="text-warning" />
            {t('ADMIN_SUBS.RESET_USAGE.TITLE', '重置已用额度')}
          </h3>
          <button
            ref={closeBtnRef}
            type="button"
            onClick={onClose}
            disabled={submitting}
            className="text-on-surface-variant hover:text-on-surface p-1 rounded-control disabled:opacity-50"
            aria-label={t('COMMON.CLOSE', '关闭')}
          >
            <X size={18} />
          </button>
        </div>

        <div className="px-5 py-4 space-y-5">
          <div className="rounded-overlay border border-warning/30 bg-warning/10 p-3 text-sm text-on-surface flex gap-3">
            <AlertTriangle size={18} className="text-warning shrink-0 mt-0.5" />
            <div>
              <div className="font-semibold">{t('ADMIN_SUBS.RESET_USAGE.WARNING_TITLE', '请确认操作范围')}</div>
              <div className="text-on-surface-variant mt-1">
                {t('ADMIN_SUBS.RESET_USAGE.WARNING_BODY', '此操作会把所选范围内订阅的当前周期已用额度归零，不会改变刷新窗口。')}
              </div>
            </div>
          </div>

          <div>
            <div className="text-sm text-on-surface mb-2">{t('ADMIN_SUBS.RESET_USAGE.PACKAGES', '套餐范围')}</div>
            <div className="rounded-control border border-outline-variant bg-surface-container-high max-h-40 overflow-y-auto p-2 space-y-1">
              {loadingPackages ? (
                <div className="text-sm text-on-surface-variant px-2 py-1">{t('COMMON.LOADING', '加载中…')}</div>
              ) : packages.length === 0 ? (
                <div className="text-sm text-on-surface-variant px-2 py-1">{t('COMMON.EMPTY', '暂无数据')}</div>
              ) : packages.map((pkg) => (
                <label key={pkg.id} className="flex items-center justify-between gap-3 px-2 py-1 rounded-control hover:bg-surface-container text-sm">
                  <span className="flex items-center gap-2 min-w-0">
                    <input
                      type="checkbox"
                      checked={selectedPackageIds.includes(pkg.id)}
                      onChange={() => togglePackage(pkg.id)}
                      disabled={submitting}
                    />
                    <span className="truncate">{pkg.name}</span>
                  </span>
                  <span className="text-xs text-on-surface-variant font-mono shrink-0">#{pkg.id}</span>
                </label>
              ))}
            </div>
            <div className="text-xs text-on-surface-variant mt-1">
              {selectedPackageIds.length === 0
                ? t('ADMIN_SUBS.RESET_USAGE.ALL_PACKAGES_HINT', '未选择套餐时表示不限套餐')
                : t('ADMIN_SUBS.RESET_USAGE.SELECTED_PACKAGES', '已选择 {{count}} 个套餐', { count: selectedPackageIds.length })}
            </div>
          </div>

          <div>
            <label htmlFor="reset-usage-user-search" className="block text-sm text-on-surface mb-2">
              {t('ADMIN_SUBS.RESET_USAGE.USERS', '用户范围')}
            </label>
            <div className="relative">
              <Search size={14} className="absolute left-3 top-1/2 -translate-y-1/2 text-on-surface-variant" />
              <input
                id="reset-usage-user-search"
                type="text"
                value={userQuery}
                onChange={(e) => setUserQuery(e.target.value)}
                disabled={submitting}
                placeholder={t('ADMIN_SUBS.RESET_USAGE.USER_SEARCH_PH', '输入用户名 / 手机号 / GitHub ID（≥2 字符）')}
                className="w-full bg-surface-container-high border border-outline-variant rounded-control pl-9 pr-3 py-2 text-sm focus:outline-none focus:border-primary"
                aria-autocomplete="list"
                aria-expanded={userSuggestions.length > 0}
                aria-controls="reset-usage-user-suggestions"
              />
            </div>
            {searchingUsers && (
              <div className="text-xs text-on-surface-variant mt-1">{t('ADMIN_GRANT.SEARCHING', '搜索中…')}</div>
            )}
            {userSuggestions.length > 0 && (
              <ul id="reset-usage-user-suggestions" className="mt-1 bg-surface-container-high border border-outline-variant rounded-control max-h-40 overflow-y-auto">
                {userSuggestions.map((u) => (
                  <li key={u.id}>
                    <button
                      type="button"
                      onClick={() => addUser(u)}
                      className="w-full text-left px-3 py-2 hover:bg-surface-container text-sm border-b border-outline-variant/40 last:border-0"
                    >
                      <div className="text-on-surface">
                        {u.username} <span className="text-on-surface-variant text-xs">#{u.id}</span>
                      </div>
                      {u.phone && <div className="text-xs text-on-surface-variant font-mono">{u.phone}</div>}
                    </button>
                  </li>
                ))}
              </ul>
            )}
            {selectedUsers.length > 0 && (
              <div className="mt-2 flex flex-wrap gap-2">
                {selectedUsers.map((u) => (
                  <span key={u.id} className="inline-flex items-center gap-1 rounded-control bg-primary/10 text-primary px-2 py-1 text-xs">
                    {u.username || t('ADMIN_SUBS.USER_FALLBACK', '用户#{{id}}', { id: u.id })} #{u.id}
                    <button type="button" onClick={() => removeUser(u.id)} className="hover:text-on-surface" aria-label={t('COMMON.CLOSE', '关闭')}>
                      <X size={12} />
                    </button>
                  </span>
                ))}
              </div>
            )}
            <div className="text-xs text-on-surface-variant mt-1">
              {selectedUsers.length === 0
                ? t('ADMIN_SUBS.RESET_USAGE.ALL_USERS_HINT', '未选择用户时表示全员')
                : t('ADMIN_SUBS.RESET_USAGE.SELECTED_USERS', '已选择 {{count}} 个用户', { count: selectedUsers.length })}
            </div>
          </div>

          <div>
            <div className="text-sm text-on-surface mb-2">{t('ADMIN_SUBS.RESET_USAGE.STATUSES', '订阅状态')}</div>
            <div className="flex flex-wrap gap-2">
              {RESET_USAGE_STATUS_OPTIONS.map((status) => (
                <label key={status} className="inline-flex items-center gap-2 rounded-control border border-outline-variant bg-surface-container-high px-3 py-1.5 text-sm">
                  <input
                    type="checkbox"
                    checked={selectedStatuses.includes(status)}
                    onChange={() => toggleStatus(status)}
                    disabled={submitting}
                  />
                  {t(`ADMIN_SUBS.STATUS_${status.toUpperCase()}`, status)}
                </label>
              ))}
            </div>
          </div>

          <div>
            <label htmlFor="reset-usage-note" className="block text-sm text-on-surface mb-1">
              {t('ADMIN_SUBS.RESET_USAGE.NOTE', '备注')} <span className="text-error">*</span>
            </label>
            <textarea
              id="reset-usage-note"
              value={note}
              onChange={(e) => setNote(e.target.value)}
              disabled={submitting}
              rows={3}
              maxLength={500}
              placeholder={t('ADMIN_SUBS.RESET_USAGE.NOTE_PH', '填写本次重置原因，最多 500 字')}
              className="w-full bg-surface-container-high border border-outline-variant rounded-control px-3 py-2 text-sm focus:outline-none focus:border-primary"
            />
            <div className="text-xs text-on-surface-variant mt-1">{note.length}/500</div>
          </div>

          <div>
            <label htmlFor="reset-usage-confirm" className="block text-sm text-on-surface mb-1">
              {t('ADMIN_SUBS.RESET_USAGE.CONFIRM_LABEL', '输入 RESET 确认')} <span className="text-error">*</span>
            </label>
            <input
              id="reset-usage-confirm"
              value={confirmText}
              onChange={(e) => setConfirmText(e.target.value)}
              disabled={submitting}
              placeholder="RESET"
              className="w-full bg-surface-container-high border border-outline-variant rounded-control px-3 py-2 text-sm font-mono focus:outline-none focus:border-primary"
            />
          </div>
        </div>

        <div className="px-5 py-4 border-t border-outline-variant flex items-center justify-end gap-2">
          <button
            type="button"
            onClick={onClose}
            disabled={submitting}
            className="px-4 py-2 rounded-control text-sm bg-surface-container-high text-on-surface hover:bg-surface-container-highest disabled:opacity-50"
          >
            {t('COMMON.CANCEL', '取消')}
          </button>
          <button
            type="button"
            onClick={submit}
            disabled={!canSubmit}
            className="inline-flex items-center gap-2 px-4 py-2 rounded-control text-sm font-medium bg-warning text-on-primary hover:brightness-110 disabled:opacity-50 disabled:cursor-not-allowed"
          >
            <TimerReset size={14} />
            {submitting
              ? t('ADMIN_SUBS.RESET_USAGE.SUBMITTING', '提交中…')
              : t('ADMIN_SUBS.RESET_USAGE.SUBMIT', '确认重置')}
          </button>
        </div>
      </div>
    </div>
  );
};

const AdminUsageMetric = ({ icon: Icon, label, value, sub, pct }) => {
  const usedPct = pct == null ? null : safePct(pct);
  const remainingPct = usedPct == null ? null : Math.max(0, 100 - usedPct);
  const color = remainingPct == null ? '#c4b5fd' : remainingColor(remainingPct);
  return (
    <div className="fl-card p-4 min-h-[96px]">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="text-xs text-on-surface-variant">{label}</div>
          <div className="mt-2 text-xl font-bold text-on-surface truncate" style={{ color }}>{value}</div>
          <div className="mt-1 text-xs text-outline truncate">{sub}</div>
        </div>
        <div className="w-9 h-9 rounded-control bg-primary/10 flex items-center justify-center shrink-0">
          <Icon size={18} className="text-primary" />
        </div>
      </div>
      {usedPct != null && (
        <div className="mt-3 h-1.5 rounded-full bg-black/35 overflow-hidden">
          <div className="h-full" style={{ width: `${usedPct}%`, background: color }} />
        </div>
      )}
    </div>
  );
};

const AdminUsageDetailMeter = ({ detail }) => {
  const { t } = useTranslation();
  const isExpired = detail.window_end_at && new Date(detail.window_end_at).getTime() < Date.now();
  const usedPct = safePct(detail.pct || 0);
  const remainingPct = Math.max(0, 100 - usedPct);
  const color = isExpired ? 'var(--color-outline)' : remainingColor(remainingPct);
  return (
    <div className={`rounded-overlay border border-outline-variant/40 p-3 ${isExpired ? 'bg-surface-container/50 grayscale opacity-80' : 'bg-surface-container-low'}`}>
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="text-sm font-semibold text-on-surface truncate">{formatUsageName(detail, t)}</div>
          <div className="text-[11px] text-outline font-mono truncate">{detail.name || `plan#${detail.plan_id}`}</div>
        </div>
        <div className="text-right shrink-0">
          {isExpired ? (
            <div className="text-sm font-bold text-outline mt-1">{t('SUB.WINDOW_EXPIRED_TITLE', '已结束')}</div>
          ) : (
            <>
              <div className="text-[11px] text-on-surface-variant">{t('SUB.USED', '已用')}</div>
              <div className="text-lg font-bold" style={{ color }}>{usedPct.toFixed(1)}%</div>
            </>
          )}
        </div>
      </div>
      <div className="mt-3 h-2 rounded-full bg-black/35 overflow-hidden">
        <div className="h-full" style={{ width: `${isExpired ? 0 : usedPct}%`, background: color }} />
      </div>
      {isExpired && (
        <div className="mt-2 text-xs text-on-surface-variant">
          {t('SUB.WINDOW_EXPIRED_HINT', '等待下次请求触发新窗口')}
        </div>
      )}
      <div className="mt-3 grid grid-cols-3 gap-2 text-xs">
        <div>
          <div className="text-outline">{t('SUB.USED', '已用')}</div>
          <div className="font-mono text-on-surface">{fmtUsageValue(detail.consumed, detail.unit)}</div>
        </div>
        <div>
          <div className="text-outline">{t('SUB.QUOTA', '额度')}</div>
          <div className="font-mono text-on-surface">{detail.limit > 0 ? fmtUsageValue(detail.limit, detail.unit) : t('SUB.UNLIMITED', '不限')}</div>
        </div>
        <div>
          <div className="text-outline">{t('SUB.WINDOW', '窗口')}</div>
          <div className="font-mono text-on-surface">
            {isExpired ? (
              <span className="text-outline">{t('SUB.WINDOW_ENDED_RELATIVE', '已过期 · {{relative}}', { relative: fmtRelativeFromNow(detail.window_end_at) || '' })}</span>
            ) : (
              formatWindowLabel(detail.window_seconds, t)
            )}
          </div>
        </div>
      </div>
      {isExpired && (
        <div className="mt-2 text-[10px] text-outline">
          {t('SUB.WINDOW_LAST_USAGE', '上次窗口用量 {{used}} / {{limit}}', {
            used: fmtUsageValue(detail.consumed, detail.unit),
            limit: detail.limit > 0 ? fmtUsageValue(detail.limit, detail.unit) : t('SUB.UNLIMITED', '不限'),
          })}
        </div>
      )}
      {!isExpired && detail.window_end_at && (
        <div className="mt-2 flex items-center gap-1 text-[10px] text-on-surface-variant font-mono">
          <Clock size={10} /> {t('SUB.WINDOW_NEXT_RESET', '下次重置')} {fmtAbsoluteShort(detail.window_end_at)}
        </div>
      )}
    </div>
  );
};

export default AdminSubscriptions;
