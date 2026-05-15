import React, { useState, useEffect, useCallback, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { X, Ticket, Plus, Trash2, RefreshCw } from 'lucide-react';
import toast from 'react-hot-toast';
import { authFetch } from '../utils/authFetch';
import { useConfirm } from '../context/ConfirmContext';
import { useModalA11y } from '../hooks/useModalA11y';
import Pagination from './common/Pagination';
import { PAGE_SIZE_DEFAULT } from './common/constants';

/**
 * AdminUserCouponsModal lets admins view, grant, and revoke a user's coupons.
 *
 * Business rules:
 *   - refunds no longer touch coupons; admins grant compensation coupons here
 *   - list all user coupons: available / used / expired / revoked
 *   - grant button selects an enabled CouponTemplate, records a reason, and creates UserCoupon
 *   - revoke button only applies to available coupons; used coupons are never revoked
 *
 * Props:
 *   userId   - target user ID
 *   username - target username for the title
 *   onClose  - close callback
 */
const AdminUserCouponsModal = ({ userId, username, onClose }) => {
    const { t } = useTranslation();
    const confirm = useConfirm();
    const closeBtnRef = useRef(null);
    const modalRef = useRef(null); // C5 round 20: focus trap scope.
    const { onBackdropClick } = useModalA11y(true, onClose, closeBtnRef, modalRef);

    const [list, setList] = useState([]);
    const [templates, setTemplates] = useState([]);
    const [loading, setLoading] = useState(true);
    const [granting, setGranting] = useState(false);
    const [showGrantForm, setShowGrantForm] = useState(false);
    const [grantTemplateID, setGrantTemplateID] = useState('');
    const [grantReason, setGrantReason] = useState('');
    // Keep string state and parse/clamp only on submit so clearing the field remains possible.
    const [grantQuantity, setGrantQuantity] = useState('1');
    // Add pagination so large coupon grants can be reviewed beyond the first page.
    const [page, setPage] = useState(1);
    const [total, setTotal] = useState(0);
    // Avoid loading flicker while automatically moving back from an out-of-range page.
    const transitionRef = useRef(false);

    const load = useCallback(async () => {
        setLoading(true);
        try {
            const [coupons, tpls] = await Promise.all([
                authFetch(`/api/admin/users/${userId}/coupons?page=${page}&page_size=${PAGE_SIZE_DEFAULT}`),
                authFetch('/api/admin/coupon-templates'),
            ]);
            if (coupons?.success) {
                const newTotal = coupons.meta?.total || 0;
                const newTotalPages = Math.max(1, Math.ceil(newTotal / PAGE_SIZE_DEFAULT));
                // After revoking the last item on the last page, move to the new last page.
                if (page > newTotalPages && newTotal > 0) {
                    transitionRef.current = true;
                    setPage(newTotalPages);
                    return; // Let the next useEffect own the reload and avoid flashing stale data.
                }
                // If the last coupon was revoked, reset to page 1.
                if (newTotal === 0 && page !== 1) {
                    transitionRef.current = true;
                    setPage(1);
                    return;
                }
                setList(coupons.data || []);
                setTotal(newTotal);
            } else {
                // Clear page data on success=false to avoid stale UI state.
                setList([]);
                toast.error(coupons?.message || t('API.ERR_NETWORK', '网络异常'));
            }
            if (tpls?.success) {
                // Only enabled templates are grantable.
                setTemplates((tpls.data || []).filter((tp) => tp.enabled !== false));
            }
        } catch {
            // Keep the current page and stale list on transient network errors so the admin can retry.
            toast.error(t('API.ERR_NETWORK', '网络异常'));
        } finally {
            if (transitionRef.current) {
                transitionRef.current = false; // The next useEffect will setLoading(true).
            } else {
                setLoading(false);
            }
        }
    }, [userId, t, page]);

    useEffect(() => { load(); }, [load]);

    const onGrant = async () => {
        if (!grantTemplateID) {
            toast.error(t('COUPON.ADMIN_GRANT_TPL_REQUIRED', '请选择模板'));
            return;
        }
        if (!grantReason.trim()) {
            toast.error(t('COUPON.ADMIN_GRANT_REASON_REQUIRED', '请填写发放理由（写入审计）'));
            return;
        }
        // Parse and clamp on submit so the input can hold intermediate empty state.
        const parsed = parseInt(grantQuantity, 10);
        if (Number.isNaN(parsed) || parsed < 1) {
            toast.error(t('COUPON.ADMIN_GRANT_QTY_INVALID', '数量必须 ≥ 1'));
            return;
        }
        const qty = Math.min(100, parsed);
        setGranting(true);
        try {
            const j = await authFetch('/api/admin/coupons/grant', {
                method: 'POST',
                body: {
                    user_id: userId,
                    template_id: parseInt(grantTemplateID, 10),
                    reason: grantReason.trim(),
                    quantity: qty,
                },
            });
            if (j?.success) {
                const granted = j.granted || 1;
                toast.success(t('COUPON.ADMIN_GRANT_OK_N', '已发放 {{count}} 张券', { count: granted }));
                setShowGrantForm(false);
                setGrantTemplateID('');
                setGrantReason('');
                setGrantQuantity('1');
                // Large grants jump to page 1 where new coupons appear first under id DESC sorting.
                if (page !== 1) setPage(1);
                else load();
            } else {
                toast.error(j?.message || t('COUPON.ADMIN_GRANT_FAIL', '发放失败'));
            }
        } catch {
            toast.error(t('API.ERR_NETWORK', '网络异常'));
        } finally {
            setGranting(false);
        }
    };

    // Warn before revoke and collect an audit reason.
    const onRevoke = async (coupon) => {
        const msg = t(
            'COUPON.ADMIN_REVOKE_CONFIRM',
            '确认撤销「{{name}}」？\n\n撤销后该券立即失效，用户无法再用此券购买套餐。',
            { name: coupon.snapshot_name }
        );
        if (!(await confirm(msg))) return;
        // Prompt for an optional revoke reason for audit.
        const reason = window.prompt(t('COUPON.ADMIN_REVOKE_REASON_PROMPT', '撤销原因（可选，写入审计）')) || '';
        try {
            const j = await authFetch(`/api/admin/coupons/${coupon.id}`, { method: 'DELETE', body: { reason } });
            if (j?.success) {
                toast.success(t('COUPON.ADMIN_REVOKE_OK', '已撤销'));
                load();
            } else {
                toast.error(j?.message || t('COUPON.ADMIN_REVOKE_FAIL', '撤销失败'));
            }
        } catch {
            toast.error(t('API.ERR_NETWORK', '网络异常'));
        }
    };

    const statusLabel = (s) => ({
        available: t('COUPON.STATUS_AVAILABLE', '可用'),
        used: t('COUPON.STATUS_USED', '已使用'),
        expired: t('COUPON.STATUS_EXPIRED', '已过期'),
        revoked: t('COUPON.STATUS_REVOKED', '已撤销'),
    }[s] || s);

    const statusColor = (s) => ({
        available: 'bg-success/20 text-success',
        used: 'bg-surface-variant/20 text-on-surface-variant',
        expired: 'bg-warning/20 text-warning',
        revoked: 'bg-error/20 text-error',
    }[s] || 'bg-surface-variant/20 text-on-surface-variant');

    return (
        <div ref={modalRef} role="dialog" aria-modal="true" aria-labelledby="admin-user-coupons-title"
            onClick={onBackdropClick}
            className="fixed inset-0 bg-black/80 backdrop-blur-sm z-[100] flex items-start sm:items-center justify-center p-2 sm:p-4 overflow-y-auto">
            <div className="bg-surface-container border border-outline-variant rounded-overlay w-full max-w-3xl flex flex-col max-h-[90vh]">
                <div className="p-6 border-b border-outline-variant flex justify-between items-center">
                    <h3 id="admin-user-coupons-title" className="text-lg font-bold text-on-surface flex items-center gap-2">
                        <Ticket size={18} className="text-fuchsia-400" aria-hidden="true" />
                        {t('COUPON.ADMIN_USER_TITLE', '用户优惠券：')}{username}
                    </h3>
                    <button ref={closeBtnRef} onClick={onClose} aria-label={t('COMMON.CLOSE', '关闭')}>
                        <X size={18} className="text-on-surface-variant hover:text-on-surface" />
                    </button>
                </div>

                {/* Top action bar: grant button and collapsible form */}
                <div className="px-6 py-4 border-b border-outline-variant bg-surface-container-high">
                    {!showGrantForm ? (
                        <button
                            type="button"
                            onClick={() => setShowGrantForm(true)}
                            disabled={templates.length === 0}
                            className="px-4 py-2 bg-primary text-on-primary rounded-control font-medium flex items-center gap-2 hover:brightness-110 disabled:opacity-50 disabled:cursor-not-allowed"
                            title={templates.length === 0 ? t('COUPON.ADMIN_NO_TEMPLATES', '没有可用模板，请先到「优惠券模板」创建') : ''}
                        >
                            <Plus size={16} aria-hidden="true" />
                            {t('COUPON.ADMIN_GRANT_NEW', '发放新券')}
                        </button>
                    ) : (
                        <div className="space-y-3">
                            <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
                                <div className="sm:col-span-2">
                                    <label htmlFor="grant-tpl" className="block text-xs font-medium text-on-surface-variant mb-1">
                                        {t('COUPON.ADMIN_GRANT_TPL', '选择模板')}
                                    </label>
                                    <select
                                        id="grant-tpl"
                                        value={grantTemplateID}
                                        onChange={(e) => setGrantTemplateID(e.target.value)}
                                        className="w-full bg-surface-container border border-outline rounded-control px-3 py-2 text-on-surface"
                                    >
                                        <option value="">{t('COMMON.PLEASE_SELECT', '请选择...')}</option>
                                        {/* Human-readable option text */}
                                        {templates.map((tp) => {
                                            const validity = tp.valid_days > 0
                                                ? t('COUPON.VALID_N_DAYS', '(有效 {{n}} 天)', { n: tp.valid_days })
                                                : t('COUPON.PERMANENT_PAREN', '(永久有效)');
                                            const price = tp.discount_type === 'fixed_price' ? ` - $${tp.discount_value}` : '';
                                            return (
                                                <option key={tp.id} value={tp.id}>
                                                    {tp.name}{price} {validity}
                                                </option>
                                            );
                                        })}
                                    </select>
                                </div>
                                <div>
                                    <label htmlFor="grant-reason" className="block text-xs font-medium text-on-surface-variant mb-1">
                                        {t('COUPON.ADMIN_GRANT_REASON', '理由（写审计）')}
                                    </label>
                                    <input
                                        id="grant-reason"
                                        type="text"
                                        value={grantReason}
                                        onChange={(e) => setGrantReason(e.target.value)}
                                        placeholder={t('COUPON.ADMIN_GRANT_REASON_PLACEHOLDER', '如：客诉补偿 / 节日活动')}
                                        className="w-full bg-surface-container border border-outline rounded-control px-3 py-2 text-on-surface"
                                    />
                                </div>
                                <div>
                                    <label htmlFor="grant-qty" className="block text-xs font-medium text-on-surface-variant mb-1">
                                        {t('COUPON.ADMIN_GRANT_QUANTITY', '数量（1-100）')}
                                    </label>
                                    <input
                                        id="grant-qty"
                                        type="number"
                                        min="1"
                                        max="100"
                                        step="1"
                                        value={grantQuantity}
                                        onChange={(e) => setGrantQuantity(e.target.value)}
                                        className="w-full bg-surface-container border border-outline rounded-control px-3 py-2 text-on-surface"
                                    />
                                    <p className="text-[11px] text-on-surface-variant mt-1">
                                        {t('COUPON.ADMIN_GRANT_QTY_HINT', '一次发放多张同款券（节日补偿场景）')}
                                    </p>
                                </div>
                            </div>
                            <div className="flex justify-end gap-2">
                                <button
                                    type="button"
                                    onClick={() => { setShowGrantForm(false); setGrantTemplateID(''); setGrantReason(''); setGrantQuantity('1'); }}
                                    className="px-4 py-2 text-on-surface-variant hover:text-on-surface rounded-control"
                                >
                                    {t('COMMON.CANCEL', '取消')}
                                </button>
                                <button
                                    type="button"
                                    onClick={onGrant}
                                    disabled={granting}
                                    className="px-4 py-2 bg-primary text-on-primary rounded-control font-medium flex items-center gap-2 disabled:opacity-50"
                                >
                                    {granting && <RefreshCw size={14} className="animate-spin" aria-hidden="true" />}
                                    {t('COUPON.ADMIN_GRANT_SUBMIT', '确认发放')}
                                </button>
                            </div>
                        </div>
                    )}
                </div>

                {/* Coupon list */}
                {/* Keep aria-live scoped to a short loading status instead of the whole list. */}
                <div className="p-6 overflow-y-auto flex-1">
                    {loading ? (
                        <div role="status" aria-live="polite" className="text-center text-on-surface-variant py-8">
                            <RefreshCw size={20} className="inline animate-spin mr-2" aria-hidden="true" />
                            {t('COUPON.LOADING', '加载中...')}
                        </div>
                    ) : total === 0 ? (
                        <div className="text-center text-on-surface-variant py-12 flex flex-col items-center gap-3">
                            <Ticket size={32} className="text-outline-variant" aria-hidden="true" />
                            {t('COUPON.ADMIN_USER_EMPTY', '该用户暂无优惠券')}
                        </div>
                    ) : (
                        <table className="w-full text-sm">
                            <thead className="text-xs text-on-surface-variant border-b border-outline-variant">
                                <tr>
                                    <th className="px-2 py-2 text-left">{t('COUPON.COL_NAME', '名称')}</th>
                                    <th className="px-2 py-2 text-left">{t('COUPON.COL_DISCOUNT', '优惠')}</th>
                                    <th className="px-2 py-2 text-left">{t('COUPON.COL_STATUS', '状态')}</th>
                                    <th className="px-2 py-2 text-left">{t('COUPON.COL_GRANTED_AT', '发放时间')}</th>
                                    <th className="px-2 py-2 text-left">{t('COUPON.COL_REASON', '理由')}</th>
                                    <th className="px-2 py-2 text-right">{t('COUPON.COL_ACTIONS', '操作')}</th>
                                </tr>
                            </thead>
                            <tbody className="divide-y divide-outline-variant/50">
                                {list.map((c) => (
                                    <tr key={c.id} className="hover:bg-surface-container-high">
                                        <td className="px-2 py-2 font-medium">{c.snapshot_name}</td>
                                        <td className="px-2 py-2 text-success font-mono text-xs">
                                            {c.snapshot_type === 'fixed_price' ? `$${c.snapshot_value}` : c.snapshot_type}
                                        </td>
                                        <td className="px-2 py-2">
                                            <span className={`px-2 py-0.5 rounded-control text-xs ${statusColor(c.status)}`}>
                                                {statusLabel(c.status)}
                                            </span>
                                        </td>
                                        <td className="px-2 py-2 text-xs text-on-surface-variant">
                                            {new Date(c.granted_at).toLocaleString()}
                                        </td>
                                        <td className="px-2 py-2 text-xs text-on-surface-variant max-w-[200px] truncate" title={c.grant_reason}>
                                            {c.grant_reason || '—'}
                                        </td>
                                        <td className="px-2 py-2 text-right">
                                            {c.status === 'available' && (
                                                <button
                                                    onClick={() => onRevoke(c)}
                                                    className="p-1 text-error hover:bg-error/20 rounded-control"
                                                    aria-label={t('COUPON.ADMIN_REVOKE', '撤销')}
                                                    title={t('COUPON.ADMIN_REVOKE', '撤销')}
                                                >
                                                    <Trash2 size={14} />
                                                </button>
                                            )}
                                        </td>
                                    </tr>
                                ))}
                            </tbody>
                        </table>
                    )}
                    {/* Shared pagination */}
                    <Pagination
                        page={page}
                        pageSize={PAGE_SIZE_DEFAULT}
                        total={total}
                        loading={loading}
                        onPageChange={setPage}
                        className="mt-4 pt-4 border-t border-outline-variant"
                    />
                </div>
            </div>
        </div>
    );
};

export default AdminUserCouponsModal;
