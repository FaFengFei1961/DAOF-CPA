import React, { useState, useEffect, useCallback, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { Receipt, RefreshCw, RotateCcw, ExternalLink, AlertCircle, X, CheckCircle2 } from 'lucide-react';
import toast from 'react-hot-toast';
import { authFetch } from '../utils/authFetch';
import { useModalA11y } from '../hooks/useModalA11y';

const STATUS_OPTIONS = ['', 'created', 'paid', 'failed', 'refunded'];

const AdminTopupOrders = () => {
  const { t } = useTranslation();
  const [rows, setRows] = useState([]);
  const [loading, setLoading] = useState(true);
  const [statusFilter, setStatusFilter] = useState('');
  const [refundingId, setRefundingId] = useState(null);
  const [markingPaidId, setMarkingPaidId] = useState(null);
  // fix Major Codex UX review round 25: pagination state is required so admins can see beyond 100 orders.
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [total, setTotal] = useState(0);

  // Manual refund workflow: admin refunds in Yifut first, then records it here.
  const [refundModal, setRefundModal] = useState({
    open: false,
    order: null,
    step: 1,            // 1=confirm external refund / 2=amount + reclaim option + refund reference
    confirmedExternal: false,
    moneyRmb: '',
    externalRef: '',
    reclaimQuota: true,
  });
  const closeRefundModal = () => setRefundModal({
    open: false, order: null, step: 1, confirmedExternal: false,
    moneyRmb: '', externalRef: '', reclaimQuota: true,
  });
  // Move focus into the step 1 checkbox or step 2 amount input when the modal opens.
  const refundCheckboxRef = useRef(null);
  const refundAmountRef = useRef(null);
  const refundModalRef = useRef(null); // C5 round 20: focus trap scope.
  const initialFocusRef = refundModal.step === 1 ? refundCheckboxRef : refundAmountRef;
  const { onBackdropClick: onRefundBackdropClick } = useModalA11y(refundModal.open, closeRefundModal, initialFocusRef, refundModalRef);

  // Manual paid workflow: admin confirms the payment in Yifut first, then marks the local order paid.
  const [markPaidModal, setMarkPaidModal] = useState({
    open: false,
    order: null,
    confirmedExternal: false,
    externalRef: '',
    reason: '',
  });
  const closeMarkPaidModal = () => setMarkPaidModal({
    open: false, order: null, confirmedExternal: false, externalRef: '', reason: '',
  });
  const markPaidModalRef = useRef(null);
  const markPaidRefInputRef = useRef(null);
  const { onBackdropClick: onMarkPaidBackdropClick } = useModalA11y(markPaidModal.open, closeMarkPaidModal, markPaidRefInputRef, markPaidModalRef);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const params = new URLSearchParams({ page: String(page), page_size: String(pageSize) });
      if (statusFilter) params.set('status', statusFilter);
      const json = await authFetch(`/api/admin/topup/orders?${params.toString()}`);
      if (json.success) {
        setRows(json.data || []);
        setTotal(json.meta?.total ?? (json.data || []).length);
      }
    } catch {
      toast.error(t('SYSTEM.ERROR', '加载失败'));
    } finally {
      setLoading(false);
    }
  }, [t, statusFilter, page, pageSize]);

  // Reset to page 1 when the status filter changes to avoid out-of-range empty pages.
  useEffect(() => { setPage(1); }, [statusFilter]);
  useEffect(() => { load(); }, [load]);

  const totalPages = Math.max(1, Math.ceil(total / pageSize));

  // The admin must confirm the external Yifut refund before entering step 2.
  const openRefundModal = (order) => {
    setRefundModal({
      open: true, order, step: 1, confirmedExternal: false,
      moneyRmb: '', externalRef: '', reclaimQuota: true,
    });
  };

  const submitRefund = async () => {
    const { order, moneyRmb, externalRef, reclaimQuota } = refundModal;
    if (!order) return;
    const remaining = Number((order.money_rmb - (order.refunded_amount_rmb || 0)).toFixed(2));
    const inputStr = String(moneyRmb || '').trim();
    const amount = inputStr === '' ? 0 : parseFloat(inputStr);
    if (inputStr !== '' && (isNaN(amount) || amount <= 0)) {
      toast.error(t('PAY_ADMIN.REFUND_AMOUNT_INVALID', '退款金额无效'));
      return;
    }
    if (amount > remaining + 0.001) {
      toast.error(t('PAY_ADMIN.REFUND_AMOUNT_EXCEEDS', '退款金额超过剩余可退'));
      return;
    }
    // fix CRITICAL C3 (codex round 20): validate required external_refund_ref before sending.
    const cleanedRef = externalRef.trim();
    if (!cleanedRef) {
      toast.error(t('PAY_ADMIN.REFUND_REF_REQUIRED', '请填入易付通后台的商户退款单号'));
      return;
    }
    setRefundingId(order.id);
    try {
      // Sprint4-M3: protocol changed from money_rmb float to money_fen int64.
      // amount=0 means full refund, so money_fen=0.
      const moneyFen = inputStr === '' ? 0 : Math.round(amount * 100);
      const json = await authFetch(`/api/admin/topup/orders/${order.id}/refund`, {
        method: 'POST',
        body: {
          money_fen: moneyFen,
          reclaim_quota: reclaimQuota,
          external_refund_ref: cleanedRef,
        },
      });
      if (json.success) {
        toast.success(t('PAY_ADMIN.REFUND_OK', '退款已登记'));
        closeRefundModal();
        load();
      } else {
        toast.error(json.message || t('PAY_ADMIN.REFUND_FAIL', '登记失败'));
      }
    } catch {
      toast.error(t('PAY_ADMIN.REFUND_FAIL', '登记失败'));
    } finally {
      setRefundingId(null);
    }
  };

  const openMarkPaidModal = (order) => {
    setMarkPaidModal({
      open: true,
      order,
      confirmedExternal: false,
      externalRef: '',
      reason: '',
    });
  };

  const submitMarkPaid = async () => {
    const { order, externalRef, reason, confirmedExternal } = markPaidModal;
    if (!order || !confirmedExternal) return;
    const cleanedRef = externalRef.trim();
    if (!cleanedRef) {
      toast.error(t('PAY_ADMIN.MANUAL_PAID_REF_REQUIRED', '请填入支付通道后台的付款凭证号'));
      return;
    }
    setMarkingPaidId(order.id);
    try {
      const json = await authFetch(`/api/admin/topup/orders/${order.id}/mark-paid`, {
        method: 'POST',
        body: {
          external_trade_ref: cleanedRef,
          reason: reason.trim(),
        },
      });
      if (json.success) {
        toast.success(t('PAY_ADMIN.MANUAL_PAID_OK', '已补登到账'));
        closeMarkPaidModal();
        load();
      } else {
        toast.error(json.message || (json.message_code ? t('API.' + json.message_code) : '') || t('PAY_ADMIN.MANUAL_PAID_FAIL', '补登到账失败'));
      }
    } catch {
      toast.error(t('PAY_ADMIN.MANUAL_PAID_FAIL', '补登到账失败'));
    } finally {
      setMarkingPaidId(null);
    }
  };

  return (
    <div className="space-y-4">
      <header className="flex items-center justify-between">
        <div className="flex items-center gap-3">
          <Receipt size={24} className="text-primary" />
          <h2 className="text-xl font-bold text-on-surface tracking-tight">
            {t('PAY_ADMIN.ORDERS_TITLE', '用户充值订单')}
          </h2>
        </div>
        <div className="flex items-center gap-2">
          <select
            value={statusFilter}
            onChange={e => setStatusFilter(e.target.value)}
            className="h-9 bg-surface-container border border-outline-variant rounded-control px-3 text-sm text-on-surface"
          >
            {STATUS_OPTIONS.map(s => (
              <option key={s} value={s}>
                {s === '' ? t('PAY_ADMIN.FILTER_ALL', '全部') : t(`TOPUP.STATUS_${s.toUpperCase()}`, s)}
              </option>
            ))}
          </select>
          <button
            type="button"
            onClick={load}
            className="h-9 w-9 flex items-center justify-center rounded-control bg-surface-container hover:bg-on-surface/[0.04]"
          >
            <RefreshCw size={14} className={loading ? 'animate-spin' : ''} />
          </button>
        </div>
      </header>

      <section className="bg-surface-container-high border border-outline-variant rounded-overlay overflow-hidden">
        {rows.length === 0 ? (
          <div className="text-center py-12 text-sm text-on-surface-variant">
            {t('TOPUP.EMPTY', '暂无充值记录')}
          </div>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead className="bg-surface-container text-xs uppercase font-mono tracking-wider text-on-surface-variant border-b border-outline-variant">
                <tr>
                  <th className="px-3 py-2 text-left">{t('TOPUP.TABLE_TIME', '时间')}</th>
                  <th className="px-3 py-2 text-left">{t('TOPUP.TABLE_USER', '用户')}</th>
                  <th className="px-3 py-2 text-right">{t('TOPUP.TABLE_AMOUNT', '金额')}</th>
                  <th className="px-3 py-2 text-left">{t('TOPUP.TABLE_METHOD', '方式')}</th>
                  <th className="px-3 py-2 text-left">{t('TOPUP.TABLE_STATUS', '状态')}</th>
                  <th className="px-3 py-2 text-left">{t('TOPUP.TABLE_OUT_TRADE_NO', '订单号')}</th>
                  <th className="px-3 py-2 text-right">{t('TOPUP.TABLE_OPS', '操作')}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-outline-variant">
                {rows.map(o => (
                  <tr key={o.id} className="hover:bg-surface-container">
                    <td className="px-3 py-2 text-xs text-on-surface-variant">
                      {new Date(o.created_at).toLocaleString('zh-CN', { hour12: false })}
                    </td>
                    <td className="px-3 py-2 text-xs font-mono">#{o.user_id}</td>
                    <td className="px-3 py-2 text-right font-mono">
                      ¥{o.money_rmb.toFixed(2)}
                      <span className="text-xs text-on-surface-variant ml-1">/ ${o.amount_usd.toFixed(2)}</span>
                      {o.refunded_amount_rmb > 0 && (
                        <div className="text-[10px] text-warning">
                          {t('PAY_ADMIN.REFUNDED_PREFIX', '已退')} ¥{o.refunded_amount_rmb.toFixed(2)}
                        </div>
                      )}
                    </td>
                    <td className="px-3 py-2 text-xs">{o.pay_type}</td>
                    <td className="px-3 py-2">
                      <span className={statusClass(o.status)}>
                        {t(`TOPUP.STATUS_${o.status.toUpperCase()}`, o.status)}
                      </span>
                    </td>
                    <td className="px-3 py-2 text-xs font-mono text-on-surface-variant max-w-[180px] truncate" title={o.out_trade_no}>
                      {o.out_trade_no}
                    </td>
                    <td className="px-3 py-2 text-right">
                      {o.status === 'created' && (
                        <button
                          type="button"
                          disabled={markingPaidId === o.id}
                          onClick={() => openMarkPaidModal(o)}
                          className="mr-3 text-xs text-success hover:text-success inline-flex items-center gap-1 disabled:opacity-50"
                        >
                          <CheckCircle2 size={12} />
                          {markingPaidId === o.id ? '...' : t('PAY_ADMIN.MANUAL_PAID_BTN', '确认到账')}
                        </button>
                      )}
                      {o.status === 'paid' && (
                        <button
                          type="button"
                          disabled={refundingId === o.id}
                          onClick={() => openRefundModal(o)}
                          className="text-xs text-warning hover:text-warning inline-flex items-center gap-1 disabled:opacity-50"
                        >
                          <RotateCcw size={12} />
                          {refundingId === o.id ? '...' : t('PAY_ADMIN.REFUND_BTN', '退款')}
                        </button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}

        {/* Pagination */}
        {!loading && total > 0 && (
          <div className="flex flex-col md:flex-row items-center justify-between gap-3 px-4 py-3 border-t border-outline-variant bg-surface-container">
            <div className="text-xs text-on-surface-variant">
              {t('PAGINATION.SUMMARY', '共 {{total}} 条 · 第 {{page}}/{{total_pages}} 页', { total, page, total_pages: totalPages })}
            </div>
            <div className="flex items-center gap-2">
              <select
                value={pageSize}
                onChange={(e) => { setPageSize(Number(e.target.value)); setPage(1); }}
                aria-label={t('PAGINATION.SIZE_LABEL', '每页条数')}
                className="bg-surface-container-high border border-outline-variant rounded-control px-2 py-1 text-xs text-on-surface"
              >
                {[10, 20, 50, 100].map(n => <option key={n} value={n}>{n} / {t('PAGINATION.PAGE', '页')}</option>)}
              </select>
              <button
                type="button"
                onClick={() => setPage(p => Math.max(1, p - 1))}
                disabled={page <= 1}
                className="px-3 py-1 text-xs bg-surface-container-high border border-outline-variant rounded-control disabled:opacity-30 hover:border-primary"
              >
                {t('PAGINATION.PREV', '上一页')}
              </button>
              <button
                type="button"
                onClick={() => setPage(p => Math.min(totalPages, p + 1))}
                disabled={page >= totalPages}
                className="px-3 py-1 text-xs bg-surface-container-high border border-outline-variant rounded-control disabled:opacity-30 hover:border-primary"
              >
                {t('PAGINATION.NEXT', '下一页')}
              </button>
            </div>
          </div>
        )}
      </section>

      {/* Manual refund modal: refund in Yifut first, then record in platform. */}
      {refundModal.open && refundModal.order && (
        <div
          ref={refundModalRef}
          role="dialog"
          aria-modal="true"
          aria-labelledby="refund-modal-title"
          onClick={onRefundBackdropClick}
          className="fixed inset-0 z-[60] flex items-center justify-center p-4 bg-black/60 backdrop-blur-sm"
        >
          <div className="bg-surface w-full max-w-md rounded-overlay shadow-2xl shadow-black/40 overflow-hidden">
            <div className="flex items-center justify-between px-5 py-4 border-b border-outline-variant/40">
              <h2 id="refund-modal-title" className="text-base font-semibold text-on-surface">
                {t('PAY_ADMIN.REFUND_MANUAL_TITLE', '手动退款登记')} · #{refundModal.order.id}
              </h2>
              <button onClick={closeRefundModal} className="p-1.5 rounded-control hover:bg-on-surface/[0.04]" aria-label={t('COMMON.CLOSE', '关闭')}>
                <X className="w-4 h-4" />
              </button>
            </div>

            <div className="p-5 space-y-4">
              {refundModal.step === 1 && (
                <>
                  <div className="rounded-control bg-warning/10 border border-warning/40 p-3 text-sm">
                    <div className="flex items-start gap-2">
                      <AlertCircle className="w-4 h-4 text-warning mt-0.5 shrink-0" />
                      <div className="text-warning dark:text-warning leading-relaxed">
                        {t('PAY_ADMIN.REFUND_MANUAL_STEP1_HINT', '本平台已关闭自动退款 API。请先登录易付通商户后台手动完成退款（钱将原路退回用户支付宝/微信），完成后再返回此处登记。')}
                      </div>
                    </div>
                  </div>
                  <div className="text-sm space-y-1.5 text-on-surface/80">
                    <div>{t('PAY_ADMIN.REFUND_ORDER_NO', '订单号')}: <code className="px-1 bg-on-surface/[0.06] rounded-control">{refundModal.order.out_trade_no}</code></div>
                    <div>{t('PAY_ADMIN.REFUND_ORDER_AMOUNT', '订单金额')}: ¥{refundModal.order.money_rmb.toFixed(2)}</div>
                    <div>{t('PAY_ADMIN.REFUND_REMAINING', '剩余可退')}: ¥{(refundModal.order.money_rmb - (refundModal.order.refunded_amount_rmb || 0)).toFixed(2)}</div>
                  </div>
                  <a
                    href="https://www.yifut.com/admin/order"
                    target="_blank"
                    rel="noopener noreferrer"
                    className="inline-flex items-center gap-1.5 text-sm text-primary hover:underline"
                  >
                    <ExternalLink className="w-4 h-4" />
                    {t('PAY_ADMIN.YIFUT_OPEN_BACKEND', '打开易付通商户后台')}
                  </a>
                  <label className="flex items-start gap-2 text-sm cursor-pointer">
                    <input
                      ref={refundCheckboxRef}
                      type="checkbox"
                      checked={refundModal.confirmedExternal}
                      onChange={(e) => setRefundModal(prev => ({ ...prev, confirmedExternal: e.target.checked }))}
                      className="mt-0.5"
                    />
                    <span>{t('PAY_ADMIN.REFUND_CONFIRM_EXTERNAL', '我已在易付通后台完成此订单的退款操作')}</span>
                  </label>
                  <div className="flex gap-2 justify-end pt-2">
                    <button onClick={closeRefundModal} className="px-4 py-2 rounded-control border border-outline-variant text-sm hover:bg-on-surface/[0.04]">
                      {t('CONFIRM.CANCEL', '取消')}
                    </button>
                    <button
                      disabled={!refundModal.confirmedExternal}
                      onClick={() => setRefundModal(prev => ({ ...prev, step: 2 }))}
                      className="px-4 py-2 rounded-control bg-primary text-white text-sm hover:opacity-90 disabled:opacity-40"
                    >
                      {t('CONFIRM.NEXT', '下一步')} →
                    </button>
                  </div>
                </>
              )}

              {refundModal.step === 2 && (
                <>
                  <div className="text-xs text-on-surface/60">
                    {t('PAY_ADMIN.REFUND_STEP2_HINT', '请填入易付通后台显示的退款金额、单号；并选择是否扣回用户余额。')}
                  </div>
                  <label className="block">
                    <span className="text-xs font-medium text-on-surface/80">{t('PAY_ADMIN.REFUND_AMOUNT', '退款金额（RMB，留空=全额剩余）')}</span>
                    <input
                      ref={refundAmountRef}
                      type="number"
                      step="0.01"
                      min="0"
                      max={refundModal.order.money_rmb - (refundModal.order.refunded_amount_rmb || 0)}
                      placeholder={String((refundModal.order.money_rmb - (refundModal.order.refunded_amount_rmb || 0)).toFixed(2))}
                      value={refundModal.moneyRmb}
                      onChange={(e) => setRefundModal(prev => ({ ...prev, moneyRmb: e.target.value }))}
                      className="mt-1 w-full px-3 py-2 rounded-control border border-outline-variant bg-surface text-sm"
                    />
                  </label>
                  <label className="block">
                    <span className="text-xs font-medium text-on-surface/80">{t('PAY_ADMIN.REFUND_EXTERNAL_REF', '易付通退款单号（对账锚点）')}</span>
                    <input
                      type="text"
                      maxLength={64}
                      placeholder="rxxxxxxxxxxxxxxxxxxxx"
                      value={refundModal.externalRef}
                      onChange={(e) => setRefundModal(prev => ({ ...prev, externalRef: e.target.value }))}
                      className="mt-1 w-full px-3 py-2 rounded-control border border-outline-variant bg-surface text-sm font-mono"
                    />
                  </label>
                  <label className="flex items-start gap-2 text-sm cursor-pointer">
                    <input
                      type="checkbox"
                      checked={refundModal.reclaimQuota}
                      onChange={(e) => setRefundModal(prev => ({ ...prev, reclaimQuota: e.target.checked }))}
                      className="mt-0.5"
                    />
                    <div className="flex-1">
                      <div className="font-medium">{t('PAY_ADMIN.REFUND_RECLAIM_QUOTA', '同时扣回用户美元余额')}</div>
                      <div className="text-xs text-on-surface/60 mt-0.5">
                        {t('PAY_ADMIN.REFUND_RECLAIM_HINT', '勾选 = 钱回 + 余额按汇率扣回（取消订单场景）；不勾选 = 仅钱回，余额保留（客服补偿场景）')}
                      </div>
                    </div>
                  </label>
                  <div className="flex gap-2 justify-end pt-2">
                    <button
                      onClick={() => setRefundModal(prev => ({ ...prev, step: 1 }))}
                      className="px-4 py-2 rounded-control border border-outline-variant text-sm hover:bg-on-surface/[0.04]"
                    >
                      ← {t('CONFIRM.PREV', '上一步')}
                    </button>
                    <button
                      disabled={refundingId === refundModal.order.id}
                      onClick={submitRefund}
                      className="px-4 py-2 rounded-control bg-primary text-white text-sm hover:opacity-90 disabled:opacity-40"
                    >
                      {refundingId === refundModal.order.id ? '...' : t('PAY_ADMIN.REFUND_SUBMIT', '登记退款')}
                    </button>
                  </div>
                </>
              )}
            </div>
          </div>
        </div>
      )}

      {/* Manual paid modal: verify in Yifut first, then mark created order as paid locally. */}
      {markPaidModal.open && markPaidModal.order && (
        <div
          ref={markPaidModalRef}
          role="dialog"
          aria-modal="true"
          aria-labelledby="mark-paid-modal-title"
          onClick={onMarkPaidBackdropClick}
          className="fixed inset-0 z-[60] flex items-center justify-center p-4 bg-black/60 backdrop-blur-sm"
        >
          <div className="bg-surface w-full max-w-md rounded-overlay shadow-2xl shadow-black/40 overflow-hidden">
            <div className="flex items-center justify-between px-5 py-4 border-b border-outline-variant/40">
              <h2 id="mark-paid-modal-title" className="text-base font-semibold text-on-surface">
                {t('PAY_ADMIN.MANUAL_PAID_TITLE', '手动确认到账')} · #{markPaidModal.order.id}
              </h2>
              <button onClick={closeMarkPaidModal} className="p-1.5 rounded-control hover:bg-on-surface/[0.04]" aria-label={t('COMMON.CLOSE', '关闭')}>
                <X className="w-4 h-4" />
              </button>
            </div>
            <div className="p-5 space-y-4">
              <div className="rounded-control bg-success/10 border border-success/30 p-3 text-sm text-success leading-relaxed">
                {t('PAY_ADMIN.MANUAL_PAID_HINT', '仅在你已经登录支付通道后台，确认这笔订单真实付款成功但回调未到账时使用。补登后会同时增加用户余额和自充余额。')}
              </div>
              <div className="text-sm space-y-1.5 text-on-surface/80">
                <div>{t('PAY_ADMIN.REFUND_ORDER_NO', '订单号')}: <code className="px-1 bg-on-surface/[0.06] rounded-control">{markPaidModal.order.out_trade_no}</code></div>
                <div>{t('PAY_ADMIN.REFUND_ORDER_AMOUNT', '订单金额')}: ¥{markPaidModal.order.money_rmb.toFixed(2)} / ${markPaidModal.order.amount_usd.toFixed(2)}</div>
              </div>
              <label className="flex items-start gap-2 text-sm cursor-pointer">
                <input
                  type="checkbox"
                  checked={markPaidModal.confirmedExternal}
                  onChange={(e) => setMarkPaidModal(prev => ({ ...prev, confirmedExternal: e.target.checked }))}
                  className="mt-0.5"
                />
                <span>{t('PAY_ADMIN.MANUAL_PAID_CONFIRM', '我已在支付通道后台确认这笔订单已真实付款')}</span>
              </label>
              <label className="block">
                <span className="text-xs font-medium text-on-surface/80">{t('PAY_ADMIN.MANUAL_PAID_REF_LABEL', '支付通道付款凭证号')}</span>
                <input
                  ref={markPaidRefInputRef}
                  type="text"
                  maxLength={64}
                  placeholder={t('PAY_ADMIN.MANUAL_PAID_REF_PLACEHOLDER', '易付通订单号 / 支付宝或微信流水号')}
                  value={markPaidModal.externalRef}
                  onChange={(e) => setMarkPaidModal(prev => ({ ...prev, externalRef: e.target.value }))}
                  className="mt-1 w-full px-3 py-2 rounded-control border border-outline-variant bg-surface text-sm font-mono"
                />
              </label>
              <label className="block">
                <span className="text-xs font-medium text-on-surface/80">{t('PAY_ADMIN.MANUAL_PAID_REASON_LABEL', '备注')}</span>
                <input
                  type="text"
                  maxLength={500}
                  placeholder={t('PAY_ADMIN.MANUAL_PAID_REASON_PLACEHOLDER', '例如：回调未送达，后台核验已支付')}
                  value={markPaidModal.reason}
                  onChange={(e) => setMarkPaidModal(prev => ({ ...prev, reason: e.target.value }))}
                  className="mt-1 w-full px-3 py-2 rounded-control border border-outline-variant bg-surface text-sm"
                />
              </label>
              <div className="flex gap-2 justify-end pt-2">
                <button onClick={closeMarkPaidModal} className="px-4 py-2 rounded-control border border-outline-variant text-sm hover:bg-on-surface/[0.04]">
                  {t('CONFIRM.CANCEL', '取消')}
                </button>
                <button
                  disabled={markingPaidId === markPaidModal.order.id || !markPaidModal.confirmedExternal || !markPaidModal.externalRef.trim()}
                  onClick={submitMarkPaid}
                  className="px-4 py-2 rounded-control bg-primary text-white text-sm hover:opacity-90 disabled:opacity-40"
                >
                  {markingPaidId === markPaidModal.order.id ? '...' : t('PAY_ADMIN.MANUAL_PAID_SUBMIT', '确认补登')}
                </button>
              </div>
            </div>
          </div>
        </div>
      )}
    </div>
  );
};

const statusClass = (s) => {
  switch (s) {
    case 'paid': return 'text-success text-xs';
    case 'created': return 'text-warning text-xs';
    case 'failed': return 'text-error text-xs';
    case 'refunded': return 'text-on-surface-variant text-xs line-through';
    default: return 'text-on-surface-variant text-xs';
  }
};

export default AdminTopupOrders;
