import React, { useState, useEffect, useCallback, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import {
  ArrowDownCircle, ArrowUpCircle, RefreshCw, Receipt,
  Filter, Download, Calendar, Activity, Wallet,
  X,
} from 'lucide-react';
import toast from 'react-hot-toast';
import { authFetch, readAuthState } from '../utils/authFetch';
import { isPageCacheFresh, readPageCache, writePageCache } from '../utils/pageCache';
import { useCurrency } from '../context/CurrencyContext';



//


const TYPE_META = {
  topup:                       { icon: ArrowDownCircle, color: 'text-success', bg: 'bg-success', direction: 'in' },
  purchase_sub:                { icon: ArrowUpCircle, color: 'text-primary', bg: 'bg-primary', direction: 'out' },
  bonus_credit:                { icon: ArrowDownCircle, color: 'text-success', bg: 'bg-success', direction: 'in' },
  refund_sub:                  { icon: ArrowDownCircle, color: 'text-warning', bg: 'bg-warning', direction: 'in' },
  refund_topup:                { icon: ArrowUpCircle, color: 'text-warning', bg: 'bg-warning', direction: 'out' },
  admin_adjust:                { icon: RefreshCw, color: 'text-primary', bg: 'bg-primary/10', direction: 'neutral' },
  admin_grant_sub:             { icon: ArrowDownCircle, color: 'text-primary', bg: 'bg-primary', direction: 'neutral' },
  admin_revoke_grant:          { icon: RefreshCw, color: 'text-warning', bg: 'bg-warning', direction: 'neutral' },
  api_consume_balance:         { icon: Activity, color: 'text-error', bg: 'bg-error', direction: 'out' },
  api_usage_sub:               { icon: Activity, color: 'text-on-surface-variant', bg: 'bg-surface-container', direction: 'usage' },
  api_usage_pending_reconcile: { icon: Activity, color: 'text-warning', bg: 'bg-warning', direction: 'neutral' },
};

const getBillingTypeLabel = (type, t) => {
  switch (type) {
    case 'topup': return t('BILL.T_TOPUP', '充值');
    case 'purchase_sub': return t('BILL.T_PURCHASE_SUB', '购买套餐');
    case 'bonus_credit': return t('BILL.T_BONUS', '奖励入账');
    case 'refund_sub': return t('BILL.T_REFUND_SUB', '订阅退款');
    case 'refund_topup': return t('BILL.T_REFUND_TOPUP', '充值退款');
    case 'admin_adjust': return t('BILL.T_ADMIN_ADJUST', '管理员调整');
    case 'admin_grant_sub': return t('BILL.T_ADMIN_GRANT_SUB', '管理员赠送订阅');
    case 'admin_revoke_grant': return t('BILL.T_ADMIN_REVOKE_GRANT', '管理员收回赠送');
    case 'api_consume_balance': return t('BILL.T_API_BALANCE', '余额扣费');
    case 'api_usage_sub': return t('BILL.T_API_SUB', '套餐扣额度');
    case 'api_usage_pending_reconcile': return t('BILL.T_API_PENDING', '待对账');
    default: return type;
  }
};

const getBillingStateLabel = (state, t) => {
  switch (state) {
    case 'settled': return t('BILL.STATE_SETTLED', '已结算');
    case 'pending_reconcile': return t('BILL.STATE_PENDING_RECONCILE', '待对账');
    case 'upstream_unmetered': return t('BILL.STATE_UPSTREAM_UNMETERED', '上游未计量');
    default: return state || '—';
  }
};

const getReconcileErrorMessage = (code, t) => {
  switch (code) {
    case 'ERR_RECONCILE_RESULT_INVALID':
      return t('BILL.ERR_RECONCILE_RESULT_INVALID', '对账结果无效');
    case 'ERR_RECONCILE_NOTE_REQUIRED':
      return t('BILL.ERR_RECONCILE_NOTE_REQUIRED', '请填写对账说明');
    case 'ERR_RECONCILE_NOTE_TOO_LONG':
      return t('BILL.ERR_RECONCILE_NOTE_TOO_LONG', '对账说明不能超过 500 字');
    case 'ERR_RECONCILE_NOT_PENDING':
      return t('BILL.ERR_RECONCILE_NOT_PENDING', '该账单当前不可对账');
    case 'ERR_RECONCILE_ALREADY_DONE':
      return t('BILL.ERR_RECONCILE_ALREADY_DONE', '该账单已完成对账');
    case 'ERR_RECONCILE_RACED':
      return t('BILL.ERR_RECONCILE_RACED', '账单状态已变化，请刷新后重试');
    default:
      return '';
  }
};

const formatSignedCurrency = (n, formatCurrency, decimals = 2) => {
  if (n === undefined || n === null) return formatCurrency(0, decimals);
  const sign = n > 0 ? '+' : (n < 0 ? '-' : '');
  return `${sign}${formatCurrency(Math.abs(n), decimals)}`;
};

const fmtTime = (s) => {
  if (!s) return '';
  const d = new Date(s);
  if (Number.isNaN(d.getTime())) return s;
  return d.toLocaleString();
};

const BILLING_CACHE_TTL_MS = 30000;
const BILLING_PAGE_SIZE = 30;
const DEFAULT_NON_USAGE_TYPES = Object.keys(TYPE_META).filter(
  (k) => k !== 'api_usage_sub'
);
const RECONCILABLE_BILLING_STATES = new Set(['pending_reconcile', 'upstream_unmetered']);
const BILLING_STATE_META = {
  settled: { className: 'bg-success/10 text-success border-success/20' },
  pending_reconcile: { className: 'bg-warning/10 text-warning border-warning/20' },
  upstream_unmetered: { className: 'bg-warning/10 text-warning border-warning/20' },
};

const getBillingAuthKey = () => {
  const { isAdmin, userToken } = readAuthState();
  return isAdmin ? 'admin' : userToken || 'guest';
};

const buildDefaultBillingQuery = (extra = {}) => {
  const params = new URLSearchParams();
  params.set('types', DEFAULT_NON_USAGE_TYPES.join(','));
  Object.entries(extra).forEach(([k, v]) => params.set(k, v));
  return params.toString();
};

const getBillingListCacheKey = (authKey, qs) => `billing:list:v3:${authKey}:${qs}`;
const getBillingSummaryCacheKey = (authKey, qs) => `billing:summary:${authKey}:${qs}`;

const BillsPage = () => {
  const { t } = useTranslation();
  const { formatCurrency } = useCurrency();
  const [billingAuthKey] = useState(getBillingAuthKey);
  const [isAdmin] = useState(() => readAuthState().isAdmin);
  const initialListCache = readPageCache(getBillingListCacheKey(
    billingAuthKey,
    buildDefaultBillingQuery({ page_size: BILLING_PAGE_SIZE })
  ));
  const initialSummaryCache = readPageCache(getBillingSummaryCacheKey(
    billingAuthKey,
    buildDefaultBillingQuery()
  ));

  const [entries, setEntries] = useState(() => initialListCache?.entries || []);
  const [summary, setSummary] = useState(() => initialSummaryCache || null);
  const [loading, setLoading] = useState(() => !initialListCache);
  const [loadingMore, setLoadingMore] = useState(false);
  const [nextCursor, setNextCursor] = useState(() => initialListCache?.nextCursor || 0);
  const [reconcileEntry, setReconcileEntry] = useState(null);


  const [selectedTypes, setSelectedTypes] = useState([]);
  const [hideUsage, setHideUsage] = useState(true);
  const [fromDate, setFromDate] = useState('');
  const [toDate, setToDate] = useState('');



  const reqIdRef = useRef(0);
  const summaryReqIdRef = useRef(0);

  const buildQuery = useCallback((extra = {}) => {
    const params = new URLSearchParams();
    let types = [...selectedTypes];
    if (hideUsage && types.length === 0) {

      types = Object.keys(TYPE_META).filter(
        (k) => k !== 'api_usage_sub'
      );
    }
    if (types.length > 0) params.set('types', types.join(','));
    if (fromDate) params.set('from', fromDate);
    if (toDate) params.set('to', toDate);
    Object.entries(extra).forEach(([k, v]) => params.set(k, v));
    return params.toString();
  }, [selectedTypes, hideUsage, fromDate, toDate]);

  const loadEntries = useCallback(async ({ force = false, append = false, cursor = 0 } = {}) => {
    const myReqId = ++reqIdRef.current;
    const extra = { page_size: BILLING_PAGE_SIZE };
    if (cursor > 0) extra.cursor = cursor;
    const qs = buildQuery(extra);
    const cacheKey = getBillingListCacheKey(billingAuthKey, qs);
    const cached = append ? null : readPageCache(cacheKey);
    if (cached) {
      setEntries(cached.entries || []);
      setNextCursor(cached.nextCursor || 0);
      setLoading(false);
      if (!force && isPageCacheFresh(cacheKey, BILLING_CACHE_TTL_MS)) return;
    } else {
      if (append) {
        setLoadingMore(true);
      } else {
        setLoading(true);
      }
    }
    try {
      const json = await authFetch(`/api/billing/mine?${qs}`);

      if (myReqId !== reqIdRef.current) return;
      if (json.success) {
        const pageEntries = json.data || [];
        const next = Number(json.next_cursor || 0);
        if (append) {
          setEntries((prev) => [...prev, ...pageEntries]);
        } else {
          const cacheValue = { entries: pageEntries, nextCursor: next };
          writePageCache(cacheKey, cacheValue);
          setEntries(pageEntries);
        }
        setNextCursor(next);
      } else {
        toast.error(t('BILL.LOAD_FAIL', '加载账单失败'));
      }
    } catch (e) {
      if (myReqId !== reqIdRef.current) return;
      toast.error(`${t('BILL.LOAD_FAIL', '加载账单失败')}: ${e.message || e}`);
    } finally {
      if (myReqId === reqIdRef.current) {
        setLoading(false);
        setLoadingMore(false);
      }
    }
  }, [billingAuthKey, buildQuery, t]);

  const loadSummary = useCallback(async ({ force = false } = {}) => {
    const myReqId = ++summaryReqIdRef.current;
    const qs = buildQuery();
    const cacheKey = getBillingSummaryCacheKey(billingAuthKey, qs);
    const cached = readPageCache(cacheKey);
    if (cached) {
      setSummary(cached);
      if (!force && isPageCacheFresh(cacheKey, BILLING_CACHE_TTL_MS)) return;
    }
    try {
      const json = await authFetch(`/api/billing/mine/summary?${qs}`);
      if (myReqId !== summaryReqIdRef.current) return;
      if (json.success) {
        writePageCache(cacheKey, json.data);
        setSummary(json.data);
      }
    } catch {
      // Summary is non-blocking; keep the list visible if it fails.
    }
  }, [billingAuthKey, buildQuery]);

  useEffect(() => { loadEntries(); }, [loadEntries]);
  useEffect(() => { loadSummary(); }, [loadSummary]);

  const toggleType = (type) => {
    setSelectedTypes((prev) => {
      if (prev.includes(type)) return prev.filter((x) => x !== type);
      return [...prev, type];
    });
  };

  const handleExport = async () => {
    try {


      const auth = readAuthState();
      const qs = buildQuery();
      const url = `/api/billing/mine/export?${qs}`;
      const res = await fetch(url, {
        credentials: 'include',
        headers: auth.userToken ? { Authorization: `Bearer ${auth.userToken}` } : {},
      });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const blob = await res.blob();
      const a = document.createElement('a');
      const objectURL = URL.createObjectURL(blob);
      a.href = objectURL;
      a.download = `billing-${new Date().toISOString().slice(0, 10)}.csv`;
      a.click();


      setTimeout(() => URL.revokeObjectURL(objectURL), 1000);
      toast.success(t('BILL.EXPORT_OK', 'CSV 已下载'));
    } catch (e) {
      toast.error(`${t('BILL.EXPORT_FAIL', '导出失败')}: ${e.message || e}`);
    }
  };

  return (
    <div className="max-w-6xl mx-auto px-4 py-6 space-y-6">
      <header className="flex items-center justify-between flex-wrap gap-4">
        <div className="flex items-center gap-3">
          <div className="w-10 h-10 rounded-overlay bg-primary/10 flex items-center justify-center">
            <Receipt className="w-5 h-5 text-primary" />
          </div>
          <div>
            <h1 className="text-2xl font-bold text-on-surface">
              {t('BILL.TITLE', '账单')}
            </h1>
            <p className="text-sm text-on-surface/60">
              {t('BILL.SUBTITLE', '所有金钱进出明细，按时间倒序展示')}
            </p>
          </div>
        </div>
        <div className="flex gap-2">
          <button
            type="button"
            onClick={() => { loadEntries({ force: true }); loadSummary({ force: true }); }}
            className="inline-flex items-center gap-1.5 px-3 py-2 rounded-control border border-outline-variant text-sm hover:bg-on-surface/[0.04]"
          >
            <RefreshCw className="w-4 h-4" />{t('BILL.REFRESH', '刷新')}
          </button>
          <button
            type="button"
            onClick={handleExport}
            className="inline-flex items-center gap-1.5 px-3 py-2 rounded-control bg-primary text-white text-sm hover:opacity-90"
          >
            <Download className="w-4 h-4" />{t('BILL.EXPORT_CSV', '导出 CSV')}
          </button>
        </div>
      </header>


      {summary && (
        <div className="grid grid-cols-2 md:grid-cols-4 gap-3">
          <SummaryCard
            label={t('BILL.SUM_IN', '入账')}
            value={formatCurrency(summary.total_in_usd || 0, 2)}
            color="text-success"
            icon={ArrowDownCircle}
          />
          <SummaryCard
            label={t('BILL.SUM_OUT', '消费')}
            value={formatCurrency(summary.total_out_usd || 0, 2)}
            color="text-error"
            icon={ArrowUpCircle}
          />
          <SummaryCard
            label={t('BILL.SUM_NET', '净变动')}
            value={formatSignedCurrency(summary.net_change_usd || 0, formatCurrency, 2)}
            color={summary.net_change_usd >= 0 ? 'text-success' : 'text-error'}
            icon={Activity}
          />
          <SummaryCard
            label={t('BILL.SUM_BALANCE', '当前余额')}
            value={formatCurrency(summary.current_balance || 0, 2)}
            color="text-on-surface"
            icon={Wallet}
          />
        </div>
      )}




      <section className="rounded-overlay bg-surface-container/40 border border-outline-variant/40 p-4 space-y-3">
        <div className="flex items-center gap-2 text-sm font-medium text-on-surface">
          <Filter className="w-4 h-4" />{t('BILL.FILTER', '筛选')}
        </div>
        <div className="flex flex-wrap gap-2">
          {Object.keys(TYPE_META).map((key) => {
            const active = selectedTypes.includes(key);
            return (
              <button
                type="button"
                key={key}
                onClick={() => toggleType(key)}
                className={`text-xs px-3 py-1.5 rounded-full border transition ${
                  active
                    ? 'bg-primary text-white border-primary'
                    : 'bg-surface text-on-surface border-outline-variant hover:bg-on-surface/[0.04]'
                }`}
              >
                {getBillingTypeLabel(key, t)}
              </button>
            );
          })}
        </div>
        <div className="flex flex-wrap items-center gap-3 text-sm">
          <label className="inline-flex items-center gap-1.5">
            <input
              type="checkbox"
              checked={hideUsage}
              onChange={(e) => setHideUsage(e.target.checked)}
              className="rounded-control"
            />
            <span>{t('BILL.HIDE_USAGE', '隐藏 API 用量行（按订阅扣额度）')}</span>
          </label>
          <div className="flex items-center gap-1.5">
            <Calendar className="w-4 h-4 text-on-surface/60" />
            <input
              type="date"
              value={fromDate}
              onChange={(e) => setFromDate(e.target.value)}
              className="px-2 py-1 rounded-control border border-outline-variant bg-surface text-sm"
            />
            <span>→</span>
            <input
              type="date"
              value={toDate}
              onChange={(e) => setToDate(e.target.value)}
              className="px-2 py-1 rounded-control border border-outline-variant bg-surface text-sm"
            />
          </div>
        </div>
      </section>


      <section>
        {loading ? (
          <div className="text-center py-12 text-on-surface/60">{t('COMMON.LOADING', '加载中…')}</div>
        ) : entries.length === 0 ? (
          <div className="text-center py-12 text-on-surface/60 fl-card">
            <Receipt className="w-12 h-12 mx-auto mb-3 opacity-40" />
            <p className="font-semibold text-on-surface mb-1">{t('BILL.EMPTY_TITLE', '暂无账单')}</p>
            <p className="text-sm">{t('BILL.EMPTY_DESC', '充值或订阅后会显示账单')}</p>
          </div>
        ) : (
          <ul className="divide-y divide-outline-variant/30 rounded-overlay border border-outline-variant/40 overflow-hidden bg-surface">
            {entries.map((e) => (
              <BillRow
                key={e.id}
                entry={e}
                t={t}
                formatCurrency={formatCurrency}
                onReconcile={isAdmin ? setReconcileEntry : null}
              />
            ))}
          </ul>
        )}
        {entries.length > 0 && (
          <div className="mt-4 flex justify-center">
            {nextCursor > 0 ? (
              <button
                type="button"
                disabled={loadingMore}
                onClick={() => loadEntries({ append: true, cursor: nextCursor })}
                className="inline-flex items-center gap-1.5 px-4 py-2 rounded-control border border-outline-variant text-sm hover:bg-on-surface/[0.04] disabled:opacity-50 disabled:cursor-not-allowed"
              >
                {loadingMore ? t('COMMON.LOADING', '加载中…') : t('BILL.LOAD_MORE', '加载更多')}
              </button>
            ) : (
              <span className="text-xs text-on-surface/50">{t('BILL.NO_MORE', '没有更多账单')}</span>
            )}
          </div>
        )}
      </section>

      {reconcileEntry && (
        <ReconcileBillingModal
          entry={reconcileEntry}
          t={t}
          onClose={() => setReconcileEntry(null)}
          onSuccess={() => {
            setReconcileEntry(null);
            loadEntries({ force: true });
            loadSummary({ force: true });
          }}
        />
      )}
    </div>
  );
};

const SummaryCard = ({ label, value, color, icon: Icon }) => (
  <div className="rounded-overlay bg-surface-container/40 border border-outline-variant/40 p-4">
    <div className="flex items-center justify-between mb-2">
      <span className="text-xs text-on-surface/60">{label}</span>
      {Icon && <Icon className="w-4 h-4 text-on-surface/40" />}
    </div>
    <div className={`text-xl font-semibold ${color || 'text-on-surface'}`}>{value}</div>
  </div>
);

const BillRow = ({ entry, t, formatCurrency, onReconcile }) => {
  const meta = TYPE_META[entry.entry_type] || {
    icon: Activity, color: 'text-on-surface', bg: 'bg-surface-container/30',
  };
  const Icon = meta.icon;
  const label = getBillingTypeLabel(entry.entry_type, t);
  const isUsage = meta.direction === 'usage';
  const amountText = isUsage
    ? (entry.tokens_total > 0 ? `${entry.tokens_total.toLocaleString()} tok` : '—')
    : formatSignedCurrency(entry.amount_usd, formatCurrency, 2);
  const description = formatBillingDescription(entry, formatCurrency, t);
  const canReconcile = Boolean(onReconcile) && RECONCILABLE_BILLING_STATES.has(entry.billing_state);

  return (
    <li className="flex items-center gap-3 px-4 py-3 hover:bg-on-surface/[0.02]">
      <div className={`w-9 h-9 rounded-control flex items-center justify-center ${meta.bg}`}>
        <Icon className={`w-4 h-4 ${meta.color}`} />
      </div>
      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-2">
          <span className="text-sm font-medium text-on-surface">{label}</span>
          {entry.model_name && (
            <span className="text-xs px-1.5 py-0.5 rounded-control bg-on-surface/[0.06] text-on-surface/70">
              {entry.model_name}
            </span>
          )}
        </div>
        <div className="text-xs text-on-surface/60 truncate">
          {description || '—'}
          {entry.amount_original && entry.currency_original && (
            <span className="ml-2">
              · {entry.currency_original} {Math.abs(entry.amount_original).toFixed(2)}
            </span>
          )}
        </div>
        <div className="text-xs text-on-surface/40">{fmtTime(entry.occurred_at)}</div>
      </div>
      <div className="text-right shrink-0">
        <div className={`text-sm font-semibold ${
          isUsage
            ? 'text-on-surface/60'
            : entry.amount_usd > 0
              ? 'text-success'
              : entry.amount_usd < 0
                ? 'text-error'
                : 'text-on-surface/60'
        }`}>
          {amountText}
        </div>
        <div className="mt-1 flex justify-end">
          {canReconcile ? (
            <button
              type="button"
              onClick={() => onReconcile(entry)}
              className="inline-flex items-center px-2.5 py-1 rounded-control bg-warning text-white text-xs font-medium hover:opacity-90"
            >
              {t('BILL.RECONCILE_ACTION', '对账')}
            </button>
          ) : (
            <BillingStateBadge state={entry.billing_state} t={t} />
          )}
        </div>
        {!isUsage && (
          <div className="mt-1 text-xs text-on-surface/50">
            {t('BILL.BALANCE_AFTER', '余额')} {formatCurrency(entry.balance_after_usd || 0, 2)}
          </div>
        )}
      </div>
    </li>
  );
};

const BillingStateBadge = ({ state, t }) => {
  const meta = BILLING_STATE_META[state] || {
    className: 'bg-surface-container text-on-surface/70 border-outline-variant',
  };
  const label = getBillingStateLabel(state, t);
  return (
    <span className={`inline-flex items-center px-2 py-0.5 rounded-control border text-[11px] ${meta.className}`}>
      {label}
    </span>
  );
};

const ReconcileBillingModal = ({ entry, t, onClose, onSuccess }) => {
  const [result, setResult] = useState('absorbed');
  const [note, setNote] = useState('');
  const [submitting, setSubmitting] = useState(false);

  const submit = async (e) => {
    e.preventDefault();
    const trimmedNote = note.trim();
    if (!trimmedNote) {
      toast.error(t('BILL.ERR_RECONCILE_NOTE_REQUIRED', '请填写对账说明'));
      return;
    }
    if ([...trimmedNote].length > 500) {
      toast.error(t('BILL.ERR_RECONCILE_NOTE_TOO_LONG', '对账说明不能超过 500 字'));
      return;
    }
    setSubmitting(true);
    try {
      const json = await authFetch(`/api/admin/billing/${entry.id}/reconcile`, {
        method: 'POST',
        body: { result, note: trimmedNote },
      });
      if (json.success) {
        toast.success(t('BILL.SUCCESS_RECONCILED', '对账已提交'));
        onSuccess();
        return;
      }
      const code = json.message_code;
      const mapped = getReconcileErrorMessage(code, t);
      toast.error(mapped || json.message || t('BILL.RECONCILE_FAILED', '对账失败'));
    } catch (err) {
      toast.error(`${t('BILL.RECONCILE_FAILED', '对账失败')}: ${err.message || err}`);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby="reconcile-billing-title"
      className="fixed inset-0 z-[70] flex items-center justify-center p-4 bg-black/70 backdrop-blur-md"
      onClick={(e) => {
        if (e.target === e.currentTarget && !submitting) onClose();
      }}
    >
      <form
        onSubmit={submit}
        className="w-full max-w-lg rounded-overlay bg-surface shadow-2xl shadow-black/40 border border-outline-variant/40 overflow-hidden"
      >
        <div className="flex items-center justify-between px-5 py-4 border-b border-outline-variant/40">
          <div>
            <h2 id="reconcile-billing-title" className="text-lg font-semibold text-on-surface">
              {t('BILL.RECONCILE_TITLE', '账单对账')}
            </h2>
            <p className="text-xs text-on-surface/60">
              #{entry.id} · {entry.model_name || entry.entry_type}
            </p>
          </div>
          <button
            type="button"
            disabled={submitting}
            onClick={onClose}
            className="p-2 rounded-control hover:bg-on-surface/[0.04] disabled:opacity-50"
            aria-label={t('COMMON.CLOSE', '关闭')}
          >
            <X className="w-4 h-4" />
          </button>
        </div>

        <div className="p-5 space-y-4">
          <label className="block">
            <span className="block text-sm font-medium text-on-surface mb-1.5">
              {t('BILL.RECONCILE_RESULT_LABEL', '对账结果')}
            </span>
            <select
              value={result}
              onChange={(e) => setResult(e.target.value)}
              className="w-full px-3 py-2 rounded-control border border-outline-variant bg-surface text-sm"
            >
              <option value="absorbed">{t('BILL.RECONCILE_RESULT_ABSORBED', '平台吸收')}</option>
              <option value="charged">{t('BILL.RECONCILE_RESULT_CHARGED', '补扣用户')}</option>
              <option value="voided">{t('BILL.RECONCILE_RESULT_VOIDED', '作废')}</option>
            </select>
          </label>

          <label className="block">
            <span className="block text-sm font-medium text-on-surface mb-1.5">
              {t('BILL.RECONCILE_NOTE_LABEL', '对账说明')}
            </span>
            <textarea
              required
              maxLength={500}
              value={note}
              onChange={(e) => setNote(e.target.value)}
              rows={5}
              className="w-full px-3 py-2 rounded-control border border-outline-variant bg-surface text-sm resize-y"
              placeholder={t('BILL.RECONCILE_NOTE_PLACEHOLDER', '填写决策原因，最多 500 字')}
            />
            <span className="block mt-1 text-xs text-on-surface/50 text-right">
              {[...note].length}/500
            </span>
          </label>
        </div>

        <div className="flex justify-end gap-2 px-5 py-4 border-t border-outline-variant/40 bg-surface-container/30">
          <button
            type="button"
            disabled={submitting}
            onClick={onClose}
            className="px-4 py-2 rounded-control border border-outline-variant text-sm hover:bg-on-surface/[0.04] disabled:opacity-50"
          >
            {t('COMMON.CANCEL', '取消')}
          </button>
          <button
            type="submit"
            disabled={submitting}
            className="px-4 py-2 rounded-control bg-primary text-white text-sm hover:opacity-90 disabled:opacity-50"
          >
            {submitting ? t('COMMON.SUBMITTING', '提交中…') : t('BILL.RECONCILE_SUBMIT', '提交对账')}
          </button>
        </div>
      </form>
    </div>
  );
};

const formatBillingDescription = (entry, formatCurrency, t) => {
  const raw = String(entry.description || '').trim();
  if (entry.entry_type === 'admin_adjust') {
    const amount = Number(entry.amount_usd || 0);
    if (amount > 0) {
      return t('BILL.ADMIN_ADJUST_INCREASE', '管理员调整额度 · 余额增加 {{amount}}', {
        amount: formatCurrency(Math.abs(amount), 2),
      });
    }
    if (amount < 0) {
      return t('BILL.ADMIN_ADJUST_DECREASE', '管理员调整额度 · 余额减少 {{amount}}', {
        amount: formatCurrency(Math.abs(amount), 2),
      });
    }
    return t('BILL.ADMIN_ADJUST_UNCHANGED', '管理员调整额度 · 余额未变化');
  }
  if (entry.entry_type === 'purchase_sub') {
    return raw.replace(/ · USD\s+-?\d+(\.\d+)?$/i, ` · ${formatCurrency(Math.abs(Number(entry.amount_usd || 0)), 2)}`);
  }
  if (entry.entry_type === 'api_consume_balance') {
    return raw.replace(/cost=\$?-?\d+(\.\d+)?/i, `cost=${formatCurrency(Math.abs(Number(entry.amount_usd || 0)), 2)}`);
  }
  return raw
    .replace(/(^| · )admin#\d+($| · )/g, ' · ')
    .replace(/ · \[.*$/g, '')
    .replace(/^ · | · $/g, '')
    .trim();
};

export default BillsPage;
