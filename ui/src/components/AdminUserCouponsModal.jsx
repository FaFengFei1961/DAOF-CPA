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
 * AdminUserCouponsModal — admin 查看 / 发放 / 撤销 某用户的优惠券。
 *
 * 业务规则（用户 2026-05-10 第三次反馈）：
 *   - 退款流程不再触碰券；admin 视情况手动发券作为补偿，入口在这里
 *   - 列表展示用户所有券（available / used / expired / revoked）
 *   - "发券"按钮：选择 enabled 的 CouponTemplate → 写理由 → 创建 UserCoupon
 *   - "撤销"按钮：仅对 status=available 的券有效（已用券永不撤销）
 *
 * Props:
 *   userId   - 目标用户 ID
 *   username - 目标用户名（标题展示）
 *   onClose  - 关闭回调
 */
const AdminUserCouponsModal = ({ userId, username, onClose }) => {
    const { t } = useTranslation();
    const confirm = useConfirm();
    const closeBtnRef = useRef(null);
    const modalRef = useRef(null); // C5 第二十轮: focus trap 范围
    const { onBackdropClick } = useModalA11y(true, onClose, closeBtnRef, modalRef);

    const [list, setList] = useState([]);
    const [templates, setTemplates] = useState([]);
    const [loading, setLoading] = useState(true);
    const [granting, setGranting] = useState(false);
    const [showGrantForm, setShowGrantForm] = useState(false);
    const [grantTemplateID, setGrantTemplateID] = useState('');
    const [grantReason, setGrantReason] = useState('');
    // fix Major（gemini 第十五轮）：保留 string state，提交时再 parseInt + clamp。
    // 原 parseInt(e.target.value, 10) || 1 在用户清空输入框时会重置为 1，
    // 让"批量发 100 张"先清空再输入的用户体验断裂。
    const [grantQuantity, setGrantQuantity] = useState('1');
    // fix Major（gemini 第十五轮）：admin 一次批量发 100 张时旧 UI 只能看到前 50 条；
    // 加分页 + 读 meta，上下页切换显示全量
    const [page, setPage] = useState(1);
    const [total, setTotal] = useState(0);
    // fix MAJOR（gemini 第十七轮）：用 ref 抑制越界自动回退场景下的 loading 闪烁。
    // 当 load 检测到 page > totalPages 触发 setPage(newTotalPages) 时，
    // 让 finally 跳过 setLoading(false)，把"加载中"状态传递给下一轮 useEffect。
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
                // fix MAJOR（gemini 第十六轮）：撤销最后一页最后一张券后 page > totalPages，
                // 自动回退到新的最后一页（让 useEffect 再触发一次 load 拿到正确数据）。
                if (page > newTotalPages && newTotal > 0) {
                    transitionRef.current = true;
                    setPage(newTotalPages);
                    return; // 让下一轮 useEffect 接管，避免短暂显示旧 list
                }
                // fix Minor（gemini 第十七轮）：newTotal === 0（最后一张被撤）时也要把 page 重置为 1
                if (newTotal === 0 && page !== 1) {
                    transitionRef.current = true;
                    setPage(1);
                    return;
                }
                setList(coupons.data || []);
                setTotal(newTotal);
            } else {
                // fix MAJOR（gemini 第十六轮）：success=false 时清空当页数据避免状态失步
                setList([]);
                toast.error(coupons?.message || t('API.ERR_NETWORK', '网络异常'));
            }
            if (tpls?.success) {
                // 仅 enabled=true 的可用模板
                setTemplates((tpls.data || []).filter((tp) => tp.enabled !== false));
            }
        } catch {
            // fix MAJOR（gemini 第十七轮）：网络抖动不应踢用户回第 1 页（破坏上下文）。
            // 仅 toast 报错，保留当前 page + 旧 list 让用户重试。
            toast.error(t('API.ERR_NETWORK', '网络异常'));
        } finally {
            if (transitionRef.current) {
                transitionRef.current = false; // 让下一轮 useEffect 自己 setLoading(true)
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
        // fix Major（gemini 第十五轮）：提交时统一 parse + clamp（输入框允许中间态空字符串）
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
                toast.success(t('COUPON.ADMIN_GRANT_OK_N', `已发放 ${granted} 张券`, { count: granted }));
                setShowGrantForm(false);
                setGrantTemplateID('');
                setGrantReason('');
                setGrantQuantity('1');
                // 大批量发完跳到第一页看新券（id DESC 排序新券在最前）
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

    // fix CRITICAL R23+3-F2（gemini 第四轮）：撤销前提示用户立即失效 + 收集 reason 写审计
    const onRevoke = async (coupon) => {
        const msg = t('COUPON.ADMIN_REVOKE_CONFIRM',
            `确认撤销「${coupon.snapshot_name}」？\n\n撤销后该券立即失效，用户无法再用此券购买套餐。`,
            { name: coupon.snapshot_name });
        if (!(await confirm(msg))) return;
        // 简单输入"撤销原因"（写审计；空允许但建议填）
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

                {/* 顶部操作栏：发券按钮 + 表单（折叠） */}
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
                                        {/* fix MINOR R23+3（gemini）：可读性更好的下拉文案 */}
                                        {templates.map((tp) => {
                                            const validity = tp.valid_days > 0
                                                ? t('COUPON.VALID_N_DAYS', `(有效 ${tp.valid_days} 天)`, { n: tp.valid_days })
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

                {/* 券列表 */}
                {/* fix CRITICAL（gemini 第十七轮）：移除外层 aria-live="polite" 反模式 ——
                    挂在整个 List 容器上会让屏幕阅读器在翻页时把全部表格内容从头朗读一遍。
                    Loading 状态用独立 role="status" 区域代替（仅播报"加载中"短消息）。 */}
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
                    {/* fix MAJOR（gemini 第十七轮）：用共用 Pagination 组件替换重复 UI */}
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
