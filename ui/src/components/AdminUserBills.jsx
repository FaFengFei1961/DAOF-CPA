// AdminUserBills shows any user's billing ledger for admins.
// Embedded as a modal from the UserManagement row billing action.
//
// It shares the BillsPage data shape but calls admin endpoints and reuses BILL.* i18n keys.
import React, { useState, useEffect, useCallback, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { X, RefreshCw, Download, Receipt, ArrowDownCircle, ArrowUpCircle, Activity } from 'lucide-react';
import toast from 'react-hot-toast';
import { authFetch, readAuthState } from '../utils/authFetch';
import { useModalA11y } from '../hooks/useModalA11y';
import Pagination from './common/Pagination';
import { PAGE_SIZE_DEFAULT } from './common/constants';
import { PendingCostBreakdown, ReconcileBillingModal } from './BillsPage';

// Keep this list in sync with backend allowedBillingTypes, including admin_grant_* and pending reconcile.
// Phase 8 removed legacy billing types.
const TYPE_I18N = {
  topup: 'BILL.T_TOPUP',
  purchase_sub: 'BILL.T_PURCHASE_SUB',
  bonus_credit: 'BILL.T_BONUS',
  refund_sub: 'BILL.T_REFUND_SUB',
  refund_topup: 'BILL.T_REFUND_TOPUP',
  admin_adjust: 'BILL.T_ADMIN_ADJUST',
  admin_grant_sub: 'BILL.T_ADMIN_GRANT_SUB',
  admin_revoke_grant: 'BILL.T_ADMIN_REVOKE_GRANT',
  api_consume_balance: 'BILL.T_API_BALANCE',
  api_usage_sub: 'BILL.T_API_SUB',
  api_usage_pending_reconcile: 'BILL.T_API_PENDING',
};

const fmtUSD = (n) => {
  if (n === undefined || n === null) return '$0.00';
  const sign = n > 0 ? '+' : (n < 0 ? '-' : '');
  return `${sign}$${Math.abs(n).toFixed(4).replace(/0+$/, '').replace(/\.$/, '.00')}`;
};

const fmtUnsignedUSD = (n) => {
  const value = Number(n || 0);
  if (!Number.isFinite(value)) return '$0.00';
  return `$${Math.abs(value).toFixed(Math.abs(value) >= 1 ? 4 : 6).replace(/0+$/, '').replace(/\.$/, '.00')}`;
};

const RECONCILABLE_BILLING_STATES = new Set(['pending_reconcile', 'upstream_unmetered']);

const getBillingStateLabel = (state, t) => {
  switch (state) {
    case 'settled': return t('BILL.STATE_SETTLED', '已结算');
    case 'pending_reconcile': return t('BILL.STATE_PENDING_RECONCILE', '待对账');
    case 'upstream_unmetered': return t('BILL.STATE_UPSTREAM_UNMETERED', '上游未计量');
    default: return state || t('BILL.STATE_SETTLED', '已结算');
  }
};

const BillingStateBadge = ({ entry, t }) => {
  if (entry.is_reconciled) {
    return (
      <span className="inline-flex items-center px-2 py-0.5 rounded-control border border-success/20 bg-success/10 text-success text-[11px]">
        {t('BILL.RECONCILED_RESULT', '已对账')} · {entry.reconcile_result || '-'}
      </span>
    );
  }
  const state = entry.billing_state || 'settled';
  const isPending = RECONCILABLE_BILLING_STATES.has(state);
  return (
    <span className={`inline-flex items-center px-2 py-0.5 rounded-control border text-[11px] ${
      isPending
        ? 'border-warning/20 bg-warning/10 text-warning'
        : 'border-outline-variant bg-surface-container text-on-surface/70'
    }`}>
      {getBillingStateLabel(state, t)}
    </span>
  );
};

const AdminUserBills = ({ userId, username, onClose }) => {
  const { t } = useTranslation();

  const [entries, setEntries] = useState([]);
  const [summary, setSummary] = useState(null);
  const [loading, setLoading] = useState(true);
  const [hideUsage, setHideUsage] = useState(true);
  const [page, setPage] = useState(1);
  const [total, setTotal] = useState(0);
  const [reconcileEntry, setReconcileEntry] = useState(null);
  const closeBtnRef = useRef(null);
  const modalRef = useRef(null); // C5 round 20: focus trap scope.
  // Suppress races while hideUsage resets the page and triggers a new load.
  const reqIdRef = useRef(0);

  const buildQuery = useCallback(() => {
    if (!hideUsage) return '';
    // Hide only api_usage_sub when the usage filter is enabled; keep grants and pending reconcile visible.
    const types = Object.keys(TYPE_I18N).filter((k) => k !== 'api_usage_sub');
    return `types=${types.join(',')}`;
  }, [hideUsage]);

  const load = useCallback(async () => {
    if (!userId) return;
    const myReqId = ++reqIdRef.current;
    setLoading(true);
    try {
      const qs = buildQuery();
      const [listJson, sumJson] = await Promise.all([
        authFetch(`/api/admin/billing/users/${userId}?page=${page}&page_size=${PAGE_SIZE_DEFAULT}${qs ? '&' + qs : ''}`),
        authFetch(`/api/admin/billing/users/${userId}/summary${qs ? '?' + qs : ''}`),
      ]);
      // Drop stale responses so hideUsage toggles cannot overwrite page 1 data.
      if (myReqId !== reqIdRef.current) return;
      if (listJson.success) {
        setEntries(listJson.data || []);
        setTotal(listJson.meta?.total || 0);
      }
      if (sumJson.success) setSummary(sumJson.data);
    } catch (e) {
      if (myReqId !== reqIdRef.current) return;
      toast.error(`${t('BILL.LOAD_FAIL', '加载账单失败')}: ${e.message || e}`);
    } finally {
      if (myReqId === reqIdRef.current) {
        setLoading(false);
      }
    }
  }, [userId, buildQuery, t, page]);

  useEffect(() => { load(); }, [load]);

  // Reset to page 1 when toggling hideUsage to avoid out-of-range pages.
  useEffect(() => {
    setPage(1);
  }, [hideUsage]);

  // a11y: ESC, backdrop click, and initial focus are handled by useModalA11y.
  const { onBackdropClick } = useModalA11y(true, onClose, closeBtnRef, modalRef);

  const handleExport = async () => {
    try {
      // readAuthState returns userToken, not token.
      const auth = readAuthState();
      const qs = buildQuery();
      const url = `/api/admin/billing/users/${userId}/export${qs ? '?' + qs : ''}`;
      const res = await fetch(url, {
        credentials: 'include',
        headers: auth.userToken ? { Authorization: `Bearer ${auth.userToken}` } : {},
      });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const blob = await res.blob();
      const a = document.createElement('a');
      const objectURL = URL.createObjectURL(blob);
      a.href = objectURL;
      a.download = `billing-user-${userId}-${new Date().toISOString().slice(0, 10)}.csv`;
      a.click();
      setTimeout(() => URL.revokeObjectURL(objectURL), 1000);
      toast.success(t('BILL.EXPORT_OK', 'CSV 已下载'));
    } catch (e) {
      toast.error(`${t('BILL.EXPORT_FAIL', '导出失败')}: ${e.message || e}`);
    }
  };

  return (
    <div
      ref={modalRef}
      role="dialog"
      aria-modal="true"
      aria-labelledby="admin-bills-title"
      onClick={onBackdropClick}
      className="fixed inset-0 z-[60] flex items-center justify-center p-4 bg-black/60 backdrop-blur-sm"
    >
      <div className="bg-surface w-full max-w-5xl max-h-[90vh] rounded-overlay shadow-2xl shadow-black/40 overflow-hidden flex flex-col">
        <div className="flex items-center justify-between px-6 py-4 border-b border-outline-variant/40">
          <div className="flex items-center gap-3">
            <div className="w-9 h-9 rounded-control bg-primary/10 flex items-center justify-center">
              <Receipt className="w-4 h-4 text-primary" />
            </div>
            <div>
              <h2 id="admin-bills-title" className="text-lg font-semibold text-on-surface">
                {t('BILL.ADMIN_USER_TITLE', '用户账单')} · {username || `#${userId}`}
              </h2>
              <p className="text-xs text-on-surface/60">{t('BILL.ADMIN_USER_ID', '用户 ID')} #{userId}</p>
            </div>
          </div>
          <div className="flex items-center gap-2">
            <button
              type="button"
              onClick={load}
              className="p-2 rounded-control hover:bg-on-surface/[0.04]"
              title={t('BILL.REFRESH', '刷新')}
              aria-label={t('BILL.REFRESH', '刷新')}
            >
              <RefreshCw className="w-4 h-4" />
            </button>
            <button
              type="button"
              onClick={handleExport}
              className="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-control bg-primary text-white text-sm hover:opacity-90"
            >
              <Download className="w-4 h-4" />{t('BILL.EXPORT_CSV', '导出 CSV')}
            </button>
            <button
              ref={closeBtnRef}
              type="button"
              onClick={onClose}
              className="p-2 rounded-control hover:bg-on-surface/[0.04]"
              aria-label={t('COMMON.CLOSE', '关闭')}
            >
              <X className="w-4 h-4" />
            </button>
          </div>
        </div>

        {/* Summary cards */}
        {summary && (
          <div className="grid grid-cols-2 md:grid-cols-4 gap-3 p-4 border-b border-outline-variant/30 bg-surface-container/40">
            <SumCard label={t('BILL.SUM_IN', '入账')} value={`$${(summary.total_in_usd || 0).toFixed(2)}`} color="text-success" />
            <SumCard label={t('BILL.SUM_OUT', '消费')} value={`$${(summary.total_out_usd || 0).toFixed(2)}`} color="text-error" />
            <SumCard
              label={t('BILL.SUM_NET', '净变动')}
              value={`${summary.net_change_usd >= 0 ? '+' : ''}$${(summary.net_change_usd || 0).toFixed(2)}`}
              color={summary.net_change_usd >= 0 ? 'text-success' : 'text-error'}
            />
            <SumCard label={t('BILL.SUM_BALANCE', '当前余额')} value={`$${(summary.current_balance || 0).toFixed(2)}`} color="text-on-surface" />
          </div>
        )}

        {/* Filter toggle */}
        <div className="px-4 py-2 border-b border-outline-variant/30 text-sm">
          <label className="inline-flex items-center gap-1.5">
            <input
              type="checkbox"
              checked={hideUsage}
              onChange={(e) => setHideUsage(e.target.checked)}
            />
            <span>{t('BILL.HIDE_USAGE', '隐藏 API 用量行（按订阅扣额度）')}</span>
          </label>
        </div>

        {/* List */}
        {/* Keep aria-live scoped to the loading status instead of the whole list. */}
        <div className="flex-1 overflow-y-auto">
          {loading ? (
            <div role="status" aria-live="polite" className="text-center py-12 text-on-surface/60">{t('COMMON.LOADING', '加载中…')}</div>
          ) : total === 0 ? (
            <div className="text-center py-12 text-on-surface/60">{t('BILL.ADMIN_USER_EMPTY', '该用户暂无账单')}</div>
          ) : (
            <ul className="divide-y divide-outline-variant/30">
              {entries.map((e) => (
                <AdminBillRow
                  key={e.id}
                  entry={e}
                  t={t}
                  onReconcile={setReconcileEntry}
                />
              ))}
            </ul>
          )}
        </div>

        {/* Shared pagination */}
        <Pagination
          page={page}
          pageSize={PAGE_SIZE_DEFAULT}
          total={total}
          loading={loading}
          onPageChange={setPage}
          className="px-4 py-3 border-t border-outline-variant/30 bg-surface-container/30"
        />
      </div>
      {reconcileEntry && (
        <ReconcileBillingModal
          entry={reconcileEntry}
          t={t}
          onClose={() => setReconcileEntry(null)}
          onSuccess={() => {
            setReconcileEntry(null);
            load();
          }}
        />
      )}
    </div>
  );
};

const SumCard = ({ label, value, color }) => (
  <div className="rounded-control bg-surface border border-outline-variant/40 p-3">
    <div className="text-xs text-on-surface/60 mb-1">{label}</div>
    <div className={`text-lg font-semibold ${color}`}>{value}</div>
  </div>
);

const AdminBillRow = ({ entry, t, onReconcile }) => {
  const isCredit = entry.amount_usd > 0;
  const isUsage = entry.entry_type === 'api_usage_sub';
  const isPending = entry.entry_type === 'api_usage_pending_reconcile';
  const reconcileCost = Number(entry.estimated_reconcile_cost_usd || entry.estimated_cost_usd || 0);
  const Icon = isUsage ? Activity : (isCredit ? ArrowDownCircle : ArrowUpCircle);
  const iconColor = isUsage
    ? 'text-on-surface-variant'
    : isPending ? 'text-warning'
    : isCredit ? 'text-success' : 'text-error';
  const typeKey = TYPE_I18N[entry.entry_type];
  const canReconcile = Boolean(onReconcile) &&
    RECONCILABLE_BILLING_STATES.has(entry.billing_state) &&
    !entry.is_reconciled;
  let amountText = fmtUSD(entry.amount_usd);
  if (isUsage) {
    amountText = entry.tokens_total > 0 ? `${entry.tokens_total.toLocaleString()} tok` : '—';
  } else if (isPending && reconcileCost > 0) {
    amountText = fmtUnsignedUSD(reconcileCost);
  }
  let amountClass = 'text-error';
  if (isPending) {
    amountClass = 'text-warning';
  } else if (isUsage) {
    amountClass = 'text-on-surface/60';
  } else if (isCredit) {
    amountClass = 'text-success';
  }

  return (
    <li className="flex items-center gap-3 px-4 py-3">
      <Icon className={`w-5 h-5 shrink-0 ${iconColor}`} />
      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-2">
          <span className="text-sm font-medium">
            {typeKey ? t(typeKey, entry.entry_type) : entry.entry_type}
          </span>
          {entry.model_name && (
            <span className="text-xs px-1.5 py-0.5 rounded-control bg-on-surface/[0.06] text-on-surface/70">
              {entry.model_name}
            </span>
          )}
        </div>
        <div className="text-xs text-on-surface/60 truncate">
          {entry.description || '—'}
        </div>
        <PendingCostBreakdown entry={entry} t={t} />
        <div className="text-xs text-on-surface/40">
          {entry.occurred_at && new Date(entry.occurred_at).toLocaleString()}
          {entry.related_type && ` · ${entry.related_type}#${entry.related_id}`}
        </div>
        <div className="mt-1">
          <BillingStateBadge entry={entry} t={t} />
        </div>
      </div>
      <div className="text-right shrink-0">
        <div className={`text-sm font-semibold ${amountClass}`}>
          {amountText}
        </div>
        {!isUsage && (
          <div className="text-xs text-on-surface/50">
            {t('BILL.BALANCE_AFTER', '余额')} ${(entry.balance_after_usd || 0).toFixed(2)}
          </div>
        )}
        {canReconcile && (
          <button
            type="button"
            onClick={() => onReconcile(entry)}
            className="mt-2 inline-flex items-center px-2.5 py-1 rounded-control bg-warning text-white text-xs font-medium hover:opacity-90"
          >
            {t('BILL.RECONCILE_ACTION', '对账')}
          </button>
        )}
      </div>
    </li>
  );
};

export default AdminUserBills;
