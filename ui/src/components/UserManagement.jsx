import React, { useState, useEffect, useMemo, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { Users, Edit2, Trash2, X, RefreshCw, Search, History, Filter, ShieldAlert, Key, CheckCircle2, Plus, Minus, Equal, AlertTriangle, Receipt, Ticket } from 'lucide-react';
import AdminUserCouponsModal from './AdminUserCouponsModal';
import { useCurrency } from '../context/CurrencyContext';
import toast from 'react-hot-toast';
import { useConfirm } from '../context/ConfirmContext';
import { authFetch } from '../utils/authFetch';
import AdminUserBills from './AdminUserBills';
import BulkGrantCouponModal from './BulkGrantCouponModal';
import { useModalA11y } from '../hooks/useModalA11y';
import DataTable from './ui/DataTable';
import StatusBadge from './ui/StatusBadge';

const UserManagement = () => {
    const confirm = useConfirm();
    const { t } = useTranslation();
    const { formatCurrency, displayCurrency, exchangeRate } = useCurrency();
    const [users, setUsers] = useState([]);
    const [loading, setLoading] = useState(true);
    const [modalConfig, setModalConfig] = useState({ isOpen: false, data: null });

    // modal form state
    const [formData, setFormData] = useState({ id: null, username: '', role: 'user', quota: 1.0, status: 1, ban_reason: '' });

    // list queries
    const [searchQuery, setSearchQuery] = useState('');
    const [sortBy, setSortBy] = useState('id_desc');
    // Backend GetUsers is paginated, so the frontend sends page/page_size and consumes meta.
    const [page, setPage] = useState(1);
    const [pageSize] = useState(50);
    const [total, setTotal] = useState(0);

    // audit log state
    const [logModal, setLogModal] = useState({ isOpen: false, user: null, logs: [], loading: false });
    const [billsModal, setBillsModal] = useState({ isOpen: false, user: null });
    const [couponsModal, setCouponsModal] = useState({ isOpen: false, user: null });

    // Bulk selection state.
    const [selectedIds, setSelectedIds] = useState(() => new Set());
    const [bulkModal, setBulkModal] = useState({ isOpen: false, mode: 'add', amount: '' });
    const [bulkCouponModalOpen, setBulkCouponModalOpen] = useState(false);
    const [bulkProcessing, setBulkProcessing] = useState(false);

    // Align modal ESC and backdrop behavior with AdminUserBills.
    const closeLogModal = () => setLogModal({ isOpen: false, user: null, logs: [] });
    const closeBulkModal = () => setBulkModal({ isOpen: false, mode: 'add', amount: '' });
    // Define closeEditModal referenced by the edit modal.
    const closeEditModal = () => setModalConfig({ isOpen: false, data: null });
    // fix CRITICAL C-F1 (gemini round 21): modal refs make the focus traps effective.
    const logModalRef = useRef(null);
    const bulkModalRef = useRef(null);
    const editModalRef = useRef(null);
    const editCloseBtnRef = useRef(null);
    const { onBackdropClick: onLogBackdropClick } = useModalA11y(logModal.isOpen, closeLogModal, undefined, logModalRef);
    const { onBackdropClick: onBulkBackdropClick } = useModalA11y(bulkModal.isOpen, closeBulkModal, undefined, bulkModalRef);
    const { onBackdropClick: onEditBackdropClick } = useModalA11y(modalConfig.isOpen, closeEditModal, editCloseBtnRef, editModalRef);

    const selectableUsers = useMemo(() => users.filter(u => u.role !== 'admin'), [users]);
    const allSelected = selectableUsers.length > 0 && selectableUsers.every(u => selectedIds.has(u.id));
    const someSelected = selectedIds.size > 0 && !allSelected;

    const toggleSelect = (id) => {
        setSelectedIds(prev => {
            const next = new Set(prev);
            if (next.has(id)) next.delete(id); else next.add(id);
            return next;
        });
    };

    const toggleSelectAll = () => {
        setSelectedIds(prev => {
            if (selectableUsers.every(u => prev.has(u.id))) return new Set();
            return new Set(selectableUsers.map(u => u.id));
        });
    };

    const clearSelection = () => setSelectedIds(new Set());

    const openBulkModal = (mode) => {
        if (selectedIds.size === 0) return;
        setBulkModal({ isOpen: true, mode, amount: '' });
    };

    const submitBulkQuota = async () => {
        const amt = parseFloat(bulkModal.amount);
        if (isNaN(amt) || amt < 0) {
            toast.error(t('USER_MGMT.BULK_AMOUNT_INVALID', '请输入有效的非负金额'));
            return;
        }
        setBulkProcessing(true);
        try {
            // UI displayCurrency may be CNY, but the backend accepts USD only.
            const usdAmount = displayCurrency === 'CNY' ? amt / exchangeRate : amt;
            const data = await authFetch('/api/admin/users/bulk-quota', {
                method: 'POST',
                body: {
                    user_ids: Array.from(selectedIds),
                    mode: bulkModal.mode,
                    amount: usdAmount,
                },
            });
            if (data.success) {
                toast.success(t('USER_MGMT.BULK_QUOTA_OK', '已更新 {{updated}} 人{{skippedText}}', {
                    updated: data.updated,
                    skippedText: data.skipped > 0
                        ? t('USER_MGMT.BULK_SKIPPED_ADMINS', '，跳过 {{count}} 人（管理员保护）', { count: data.skipped })
                        : '',
                }));
                setBulkModal({ isOpen: false, mode: 'add', amount: '' });
                clearSelection();
                fetchUsers();
            } else {
                toast.error((data.message_code ? t('API.' + data.message_code) : data.message) || t('USER_MGMT.BULK_OP_FAIL', '批量操作失败'));
            }
        } catch (e) {
            toast.error(t('USER_MGMT.BULK_OP_NET_FAIL', '网络异常，批量操作失败'));
        }
        setBulkProcessing(false);
    };

    const submitBulkDelete = async () => {
        if (!(await confirm(t('USER_MGMT.BULK_DELETE_CONFIRM', '即将物理删除 {{count}} 个用户（含其所有 token），不可恢复，确认？', { count: selectedIds.size })))) return;
        setBulkProcessing(true);
        try {
            const data = await authFetch('/api/admin/users/bulk-delete', {
                method: 'POST',
                body: { user_ids: Array.from(selectedIds) },
            });
            if (data.success) {
                toast.success(t('USER_MGMT.BULK_DELETE_OK', '已抹除 {{deleted}} 个用户{{skippedText}}', {
                    deleted: data.deleted,
                    skippedText: data.skipped > 0
                        ? t('USER_MGMT.BULK_DELETE_SKIPPED', '，跳过 {{count}} 个（管理员保护）', { count: data.skipped })
                        : '',
                }));
                clearSelection();
                fetchUsers();
            } else {
                toast.error((data.message_code ? t('API.' + data.message_code) : data.message) || t('USER_MGMT.BULK_DELETE_FAIL', '批量删除失败'));
            }
        } catch (e) {
            toast.error(t('USER_MGMT.BULK_DELETE_NET_FAIL', '网络异常，批量删除失败'));
        }
        setBulkProcessing(false);
    };

    const fetchUsers = async (query = searchQuery, sort = sortBy, p = page) => {
        setLoading(true);
        try {
            const params = new URLSearchParams();
            // Backend accepts search length 2..64; omit empty/too-short values to avoid 400.
            if (query && query.length >= 2 && query.length <= 64) {
                params.set('search', query);
            }
            params.set('sort', sort);
            params.set('page', String(p));
            params.set('page_size', String(pageSize));
            const data = await authFetch(`/api/admin/users?${params.toString()}`);
            if (data.success) {
                setUsers(data.data);
                if (data.meta) setTotal(data.meta.total || 0);
            }
        } catch {
            toast.error(t('USER_MGMT.LOAD_FAIL', '加载用户列表失败'));
        }
        setLoading(false);
    };

    const handleDelete = async (id) => {
        if (!(await confirm(t('USER_MGMT.DELETE_CONFIRM')))) return;
        try {
            const data = await authFetch(`/api/admin/users/${id}`, { method: 'DELETE' });
            if (data.success) {
                fetchUsers();
                toast.success(t('USER_MGMT.DELETE_OK', '用户已删除'));
            } else {
                toast.error((data.message_code ? t('API.' + data.message_code) : data.message) || t('USER_MGMT.DELETE_FAILED'));
            }
        } catch {
            toast.error(t('USER_MGMT.NET_ERROR'));
        }
    };

    // Project is not live; remove the stale add mode because users self-register through OAuth/SMS.
    const handleOpenModal = (user) => {
        setFormData({ id: user.id, username: user.username, role: user.role, quota: user.quota, status: user.status, ban_reason: user.ban_reason || '' });
        setModalConfig({ isOpen: true, data: user });
    };

    const handleSubmit = async (e) => {
        e.preventDefault();
        try {
            const data = await authFetch(`/api/admin/users/${formData.id}`, { method: 'PUT', body: formData });
            if (data.success) {
                setModalConfig({ isOpen: false, data: null });
                fetchUsers();
                toast.success(t('USER_MGMT.UPDATE_OK', '用户已更新'));
            } else {
                toast.error((data.message_code ? t('API.' + data.message_code) : data.message) || t('USER_MGMT.SUBMIT_FAILED'));
            }
        } catch {
            toast.error(t('USER_MGMT.SUBMIT_NET_ERROR'));
        }
    };

    const isFirstMount = React.useRef(true);
    useEffect(() => {
        if (isFirstMount.current) {
            isFirstMount.current = false;
            fetchUsers(searchQuery, sortBy, page);
            return;
        }
        const timeout = setTimeout(() => fetchUsers(searchQuery, sortBy, page), 300);
        return () => clearTimeout(timeout);
    // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [searchQuery, sortBy, page]);

    // Reset to page 1 on search/sort changes; the page effect reloads.
    useEffect(() => { setPage(1); }, [searchQuery, sortBy]);

    const openLogModal = async (u) => {
        setLogModal({ isOpen: true, user: u, logs: [], loading: true });
        try {
            const data = await authFetch(`/api/admin/users/${u.id}/operations`);
            if (data.success) {
                setLogModal(prev => ({ ...prev, logs: data.data, loading: false }));
            } else {
                setLogModal(prev => ({ ...prev, loading: false }));
                toast.error(t('USER_MGMT.LOG_FETCH_FAILED'));
            }
        } catch {
            setLogModal(prev => ({ ...prev, loading: false }));
            toast.error(t('USER_MGMT.LOG_FETCH_FAILED'));
        }
    };

    return (
        <div className="w-full">
            <div className="mb-8 border-b border-outline-variant pb-6 flex flex-col md:flex-row md:items-center justify-between gap-4">
              <div>
                <h1 className="text-2xl font-bold tracking-tight text-on-surface flex items-center gap-3">
                   {t('USER_MGMT.TITLE')}
                </h1>
                <p className="text-on-surface-variant mt-2 text-sm max-w-2xl">
                  {t('USER_MGMT.DESC')}
                </p>
              </div>

              <div className="flex items-center gap-3">
                 <div className="relative group">
                    <div className="absolute inset-y-0 left-3 flex items-center pointer-events-none">
                        <Search size={16} className="text-on-surface-variant group-focus-within:text-primary" />
                    </div>
                    <input
                        type="text"
                        placeholder={t('USER_MGMT.SEARCH_PLACEHOLDER')}
                        value={searchQuery}
                        onChange={e => setSearchQuery(e.target.value)}
                        className="h-10 pl-9 pr-4 bg-surface-container-high border border-outline-variant rounded-overlay text-sm text-on-surface focus:border-primary focus:bg-surface-container outline-none w-64 placeholder:text-on-surface-variant"
                    />
                 </div>

                 <div className="relative">
                    <select
                        value={sortBy}
                        onChange={e => setSortBy(e.target.value)}
                        className="h-10 pl-10 pr-8 bg-surface-container-high border border-outline-variant rounded-overlay text-sm text-on-surface-variant focus:border-primary outline-none appearance-none cursor-pointer"
                    >
                        <option value="id_desc">{t('USER_MGMT.SORT_ID_DESC')}</option>
                        <option value="id_asc">{t('USER_MGMT.SORT_ID_ASC')}</option>
                        <option value="quota_desc">{t('USER_MGMT.SORT_QUOTA_DESC')}</option>
                        <option value="status_desc">{t('USER_MGMT.SORT_STATUS_DESC')}</option>
                        <option value="status_asc">{t('USER_MGMT.SORT_STATUS_ASC')}</option>
                    </select>
                    <Filter size={14} className="absolute left-4 top-1/2 -translate-y-1/2 text-on-surface-variant" />
                 </div>
              </div>
            </div>

            {/* Bulk actions bar */}
            {selectedIds.size > 0 && (
                <div className="mb-4 flex items-center justify-between bg-primary/10 border border-primary/30 rounded-overlay px-4 py-3 sticky top-2 z-10 backdrop-blur-md">
                    <div className="flex items-center gap-3">
                        <CheckCircle2 size={18} className="text-primary" />
                        <span className="text-sm text-on-surface font-medium">
                             {t('USER_MGMT.SELECTED_PREFIX', '已选中')} <span className="text-primary font-bold">{selectedIds.size}</span> {t('USER_MGMT.SELECTED_SUFFIX', '个用户')}
                        </span>
                        <button onClick={clearSelection} className="text-xs text-on-surface-variant hover:text-on-surface underline">
                            {t('USER_MGMT.CLEAR_SELECTION', '取消选择')}
                        </button>
                    </div>
                    <div className="flex items-center gap-2">
                        <button
                            onClick={() => openBulkModal('add')}
                            className="flex items-center gap-1.5 px-3 py-1.5 bg-success/20 hover:bg-success/40 text-success border border-success/40 rounded-control text-xs font-medium transition-colors"
                            title={t('USER_MGMT.BULK_ADD_TITLE', '为所选用户增加额度')}
                        >
                            <Plus size={14} /> {t('USER_MGMT.BULK_ADD', '增加额度')}
                        </button>
                        <button
                            onClick={() => openBulkModal('sub')}
                            className="flex items-center gap-1.5 px-3 py-1.5 bg-warning/20 hover:bg-warning/40 text-warning border border-warning/40 rounded-control text-xs font-medium transition-colors"
                            title={t('USER_MGMT.BULK_SUB_TITLE', '为所选用户扣减额度（不会扣到负数）')}
                        >
                            <Minus size={14} /> {t('USER_MGMT.BULK_SUB', '减少额度')}
                        </button>
                        <button
                            onClick={() => openBulkModal('set')}
                            className="flex items-center gap-1.5 px-3 py-1.5 bg-primary/20 hover:bg-primary/40 text-primary border border-primary/40 rounded-control text-xs font-medium transition-colors"
                            title={t('USER_MGMT.BULK_SET_TITLE', '把所选用户的额度直接设置为某值')}
                        >
                            <Equal size={14} /> {t('USER_MGMT.BULK_SET', '设为定值')}
                        </button>
                        <div className="w-px h-6 bg-outline-variant mx-1" />
                        <button
                            onClick={() => setBulkCouponModalOpen(true)}
                            className="flex items-center gap-1.5 px-3 py-1.5 bg-primary/20 hover:bg-primary/40 text-primary border border-primary/40 rounded-control text-xs font-medium transition-colors"
                            title={t('USER_MGMT.BULK_GRANT_COUPON', '批量发券')}
                        >
                            <Ticket size={14} /> {t('USER_MGMT.BULK_GRANT_COUPON', '批量发券')}
                        </button>
                        <div className="w-px h-6 bg-outline-variant mx-1" />
                        <button
                            onClick={submitBulkDelete}
                            disabled={bulkProcessing}
                            className="flex items-center gap-1.5 px-3 py-1.5 bg-error/20 hover:bg-error/40 text-error border border-error/40 rounded-control text-xs font-medium transition-colors disabled:opacity-50"
                            title={t('USER_MGMT.BULK_DELETE_TITLE', '物理删除所选用户（含 token），不可恢复')}
                        >
                            <Trash2 size={14} /> {t('USER_MGMT.BULK_DELETE', '批量删除')}
                        </button>
                    </div>
                </div>
            )}

            <div className="bg-surface-container border border-outline-variant rounded-overlay overflow-hidden ">
                <DataTable
                    columns={[
                        {
                            key: 'select',
                            header: (
                                <input
                                    type="checkbox"
                                    checked={allSelected}
                                    ref={el => { if (el) el.indeterminate = someSelected; }}
                                    onChange={toggleSelectAll}
                                    className="w-4 h-4 cursor-pointer accent-primary"
                                    title={t('USER_MGMT.SELECT_ALL_NORMAL_TITLE', '全选普通用户（admin 不可选）')}
                                />
                            ),
                            width: 60,
                            render: u => u.role === 'admin' ? (
                                <span className="text-fuchsia-400 text-xs" title={t('USER_MGMT.ADMIN_PROTECTED_TITLE', '管理员账号受保护')}>🔒</span>
                            ) : (
                                <input
                                    type="checkbox"
                                    checked={selectedIds.has(u.id)}
                                    onChange={() => toggleSelect(u.id)}
                                    className="w-4 h-4 cursor-pointer accent-primary"
                                    aria-label={t('USER_MGMT.SELECT_USER_ARIA', '选择用户 {{username}}', { username: u.username })}
                                />
                            )
                        },
                        {
                            key: 'username',
                            header: t('USER_MGMT.TABLE.ID_NAME'),
                            render: u => (
                                <div className="flex flex-col">
                                    <span className="text-on-surface font-medium">{u.username}</span>
                                    <span className="text-xs text-primary/70 font-mono mt-1">
                                        {t('USER_MGMT.ID_PREFIX', { id: u.id })} {u.role === 'admin' ? '[GOD]' : ''}
                                    </span>
                                </div>
                            )
                        },
                        {
                            key: 'binding',
                            header: t('USER_MGMT.TABLE.BINDING'),
                            render: u => (
                                <div className="flex flex-col gap-1">
                                    {u.github_id ? <span className="text-xs text-on-surface-variant bg-surface-variant px-2 py-0.5 rounded-control w-max">{t('USER_MGMT.GITHUB_BOUND', { id: u.github_id })}</span> : <span className="text-xs text-outline-variant italic">{t('USER_MGMT.GITHUB_UNBOUND')}</span>}
                                    {u.phone ? <span className="text-xs text-warning bg-warning/10 px-2 py-0.5 rounded-control w-max">{t('USER_MGMT.PHONE_BOUND', { phone: u.phone })}</span> : <span className="text-xs text-outline-variant italic">{t('USER_MGMT.PHONE_UNBOUND')}</span>}
                                </div>
                            )
                        },
                        {
                            key: 'reg_time',
                            header: t('USER_MGMT.TABLE.REG_TIME'),
                            render: u => <span className="text-xs text-on-surface-variant">{new Date(u.created_at).toLocaleString('zh-CN', { hour12: false })}</span>
                        },
                        {
                            key: 'quota',
                            header: t('USER_MGMT.TABLE.QUOTA'),
                            mono: true,
                            render: u => u.role === 'admin'
                                ? <span className="text-fuchsia-400 font-bold tracking-widest text-lg">∞</span>
                                : <span className={u.quota > 0 ? "text-success" : "text-on-surface-variant"}>{formatCurrency(u.quota, 2)}</span>
                        },
                        {
                            key: 'status',
                            header: t('USER_MGMT.TABLE.STATUS'),
                            align: 'center',
                            render: u => u.status === 1
                                ? <StatusBadge variant="success">{t('USER_MGMT.STATUS_NORMAL')}</StatusBadge>
                                : <StatusBadge variant="error">{t('USER_MGMT.STATUS_BANNED')}</StatusBadge>
                        },
                        {
                            key: 'actions',
                            header: t('USER_MGMT.TABLE.ACTIONS'),
                            align: 'right',
                            render: u => (
                                <div className="flex items-center justify-end gap-3 opacity-50 group-hover:opacity-100 transition-opacity">
                                    <button onClick={(e) => { e.stopPropagation(); openLogModal(u); }} className="text-on-surface-variant hover:text-success tooltip" aria-label={t('USER_MGMT.LOG_TOOLTIP')} title={t('USER_MGMT.LOG_TOOLTIP')}>
                                        <History size={16} />
                                    </button>
                                    <button
                                        onClick={(e) => { e.stopPropagation(); setBillsModal({ isOpen: true, user: u }); }}
                                        className="text-on-surface-variant hover:text-primary tooltip"
                                        aria-label={t('USER_MGMT.BILLS_TOOLTIP', '查看账单')}
                                        title={t('USER_MGMT.BILLS_TOOLTIP', '查看账单')}
                                    >
                                        <Receipt size={16} />
                                    </button>
                                    <button
                                        onClick={(e) => { e.stopPropagation(); setCouponsModal({ isOpen: true, user: u }); }}
                                        className="text-on-surface-variant hover:text-fuchsia-400 tooltip"
                                        aria-label={t('USER_MGMT.COUPONS_TOOLTIP', '查看/发放优惠券')}
                                        title={t('USER_MGMT.COUPONS_TOOLTIP', '查看/发放优惠券')}
                                    >
                                        <Ticket size={16} />
                                    </button>
                                    {u.role !== 'admin' && (
                                        <>
                                            <button onClick={(e) => { e.stopPropagation(); handleOpenModal(u); }} className="text-on-surface-variant hover:text-primary tooltip" aria-label={t('USER_MGMT.EDIT_TOOLTIP')} title={t('USER_MGMT.EDIT_TOOLTIP')}>
                                                <Edit2 size={16} />
                                            </button>
                                            <button onClick={(e) => { e.stopPropagation(); handleDelete(u.id); }} className="text-on-surface-variant hover:text-error" aria-label={t('USER_MGMT.DELETE_TOOLTIP')} title={t('USER_MGMT.DELETE_TOOLTIP')}>
                                                <Trash2 size={16} />
                                            </button>
                                        </>
                                    )}
                                </div>
                            )
                        }
                    ]}
                    rows={users}
                    rowKey={u => u.id}
                    loading={loading}
                    emptyTitle={t('USER_MGMT.EMPTY')}
                    emptyIcon={Users}
                    pagination={{
                        page,
                        pageSize,
                        total,
                        onPageChange: setPage
                    }}
                />
            </div>

            {/* Audit timeline modal */}
            {logModal.isOpen && (
                <div
                    ref={logModalRef}
                    role="dialog"
                    aria-modal="true"
                    aria-labelledby="log-modal-title"
                    onClick={onLogBackdropClick}
                    className="fixed inset-0 z-[60] flex items-center justify-center p-4 bg-black/70 backdrop-blur-md animate-in fade-in "
                >
                    <div className="relative w-full max-w-4xl max-h-[80vh] flex flex-col bg-surface-container-high border border-outline-variant rounded-overlay shadow-2xl shadow-black/40 overflow-hidden">
                        <div className="p-5 border-b border-outline-variant flex items-center justify-between bg-surface-container">
                            <div className="flex items-center gap-3">
                                <History className="text-success" size={20} />
                                <h3 id="log-modal-title" className="text-lg font-bold text-on-surface">{t('USER_MGMT.LOG_MODAL_TITLE', { id: logModal.user?.id })}</h3>
                            </div>
                            <button onClick={closeLogModal} className="text-on-surface-variant hover:text-white" aria-label={t('COMMON.CLOSE', '关闭')}>
                                <X size={20} />
                            </button>
                        </div>
                        <div className="p-6 overflow-y-auto flex-1 bg-surface-container">
                            {logModal.loading ? (
                                <div className="text-center py-10 text-on-surface-variant"><RefreshCw size={24} className="mx-auto mb-2" /> {t('USER_MGMT.LOG_MODAL_LOADING')}</div>
                            ) : logModal.logs.length === 0 ? (
                                <div className="text-center py-10 text-outline-variant">{t('USER_MGMT.LOG_MODAL_EMPTY')}</div>
                            ) : (
                                <div className="space-y-4 relative before:absolute before:inset-0 before:ml-5 before:-translate-x-px md:before:mx-auto md:before:translate-x-0 before:h-full before:w-0.5 before:bg-gradient-to-b before:from-transparent before:via-[#2b2b2b] before:to-transparent">
                                    {logModal.logs.map((log) => (
                                        <div key={log.id} className="relative flex items-center justify-between md:justify-normal md:odd:flex-row-reverse group is-active">
                                            <div className="flex items-center justify-center w-6 h-6 rounded-full border-2 border-surface bg-surface-variant text-on-surface-variant group-hover:bg-primary group-hover:text-on-primary shadow shrink-0 md:order-1 md:group-odd:-translate-x-1/2 md:group-even:translate-x-1/2 z-10 font-mono text-xs">
                                                {log.id}
                                            </div>
                                            <div className="w-[calc(100%-2.5rem)] md:w-[calc(50%-2rem)] p-4 rounded-overlay border border-outline-variant bg-surface-container ">
                                                <div className="flex items-center justify-between mb-2">
                                                    <div className="text-sm font-bold text-primary">
                                                        {log.action_type === 'CREATE' ? t('USER_MGMT.ACTION_CREATE') :
                                                         log.action_type === 'UPDATE' ? t('USER_MGMT.ACTION_UPDATE') :
                                                         log.action_type === 'DELETE' ? t('USER_MGMT.ACTION_DELETE') :
                                                          log.action_type === 'LOGIN' ? t('USER_MGMT.ACTION_LOGIN', '🔓 用户登录') :
                                                          log.action_type === 'REGISTER' ? t('USER_MGMT.ACTION_REGISTER', '🎉 注册成功') :
                                                          log.action_type === 'CREATE_TOKEN' ? t('USER_MGMT.ACTION_CREATE_TOKEN', '🔑 创建 API 令牌') :
                                                          log.action_type === 'UPDATE_TOKEN' ? t('USER_MGMT.ACTION_UPDATE_TOKEN', '✏️ 修改 API 令牌') :
                                                          log.action_type === 'DELETE_TOKEN' ? t('USER_MGMT.ACTION_DELETE_TOKEN', '🗑️ 删除 API 令牌') :
                                                          log.action_type === 'BULK_QUOTA' ? t('USER_MGMT.ACTION_BULK_QUOTA', '💰 批量调整额度') :
                                                          log.action_type === 'BULK_HARD_DELETE' ? t('USER_MGMT.ACTION_BULK_HARD_DELETE', '☢️ 物理抹除') :
                                                          log.action_type === 'ADMIN_LOGIN' ? t('USER_MGMT.ACTION_ADMIN_LOGIN', '🛡️ 管理员登录') :
                                                          log.action_type === 'ADMIN_LOGIN_FAIL' ? t('USER_MGMT.ACTION_ADMIN_LOGIN_FAIL', '⚠️ 管理员登录失败') :
                                                          log.action_type === 'ADMIN_SETUP' ? t('USER_MGMT.ACTION_ADMIN_SETUP', '🔧 管理员凭证 setup') :
                                                          log.action_type === 'ADMIN_CREDENTIALS_UPDATE' ? t('USER_MGMT.ACTION_ADMIN_CREDENTIALS_UPDATE', '🔐 管理员凭证修改') :
                                                         log.action_type}
                                                    </div>
                                                    <div className="text-xs text-on-surface font-mono bg-surface-container-high px-2 py-0.5 rounded-control">{new Date(log.created_at).toLocaleString('zh-CN', { hour12: false })}</div>
                                                </div>
                                                <div className="text-base text-on-surface font-medium mt-3 mb-3 leading-relaxed break-all">
                                                    {(() => {
                                                        let lines = [];
                                                        try {
                                                            const changes = JSON.parse(log.details);
                                                            if (!Array.isArray(changes)) throw new Error("Not new array format");
                                                            if (changes.length === 0) lines = [t('USER_MGMT.LOG_NO_CHANGES')];
                                                            else {
                                                                lines = changes.map(c => {
                                                                    if (c.type === 'USERNAME') return t('USER_MGMT.LOG_UPDATE_USERNAME', { target: c.target, old: c.old, new: c.new });
                                                                    if (c.type === 'QUOTA') return t('USER_MGMT.LOG_UPDATE_QUOTA', { target: c.target, old: formatCurrency(Number(c.old), 2), new: formatCurrency(Number(c.new), 2) });
                                                                    if (c.type === 'STATUS') return t('USER_MGMT.LOG_UPDATE_STATUS', { target: c.target, old: c.old == 1 ? t('USER_MGMT.STATUS_NORMAL') : t('USER_MGMT.STATUS_BANNED'), new: c.new == 1 ? t('USER_MGMT.STATUS_NORMAL') : t('USER_MGMT.STATUS_BANNED') });
                                                                    if (c.type === 'BAN_REASON') return t('USER_MGMT.LOG_UPDATE_BAN_REASON', { target: c.target, old: c.old || t('USER_MGMT.NONE'), new: c.new || t('USER_MGMT.NONE') });
                                                                    if (c.type === 'CREATE') return t('USER_MGMT.LOG_CREATE', { target: c.target, quota: formatCurrency(Number(c.quota), 2) });
                                                                    if (c.type === 'DELETE') return t('USER_MGMT.LOG_DELETE', { target: c.target });
                                                                    if (c.type === 'LOGIN') return t('USER_MGMT.LOG_LOGIN', '通过 [{{via}}] 登录回归', { via: c.via || 'unknown' });
                                                                    if (c.type === 'REGISTER') return t('USER_MGMT.LOG_REGISTER', '经 [{{via}}] 完成注册（用户名 [{{username}}]{{extra}}）', {
                                                                        via: c.via || 'unknown',
                                                                        username: c.username,
                                                                        extra: `${c.github_id ? `, gh:${c.github_id}` : ''}${c.phone ? `, phone:${c.phone}` : ''}`,
                                                                    });
                                                                    if (c.type === 'CREATE_TOKEN') return t('USER_MGMT.LOG_CREATE_TOKEN', '创建 API 令牌 [{{name}}]，限额 {{quota}}（0 表示不限）', { name: c.name, quota: formatCurrency(Number(c.quota_limit) || 0, 2) });
                                                                    if (c.type === 'UPDATE_TOKEN') return t('USER_MGMT.LOG_UPDATE_TOKEN', '修改 API 令牌 [{{name}}]（ID {{id}}）', { name: c.name, id: c.token_id });
                                                                    if (c.type === 'DELETE_TOKEN') return t('USER_MGMT.LOG_DELETE_TOKEN', '删除 API 令牌 [{{name}}]（ID {{id}}）', { name: c.token_name, id: c.token_id });
                                                                    if (c.type === 'BULK_QUOTA') {
                                                                        const modeText = c.mode === 'add'
                                                                            ? t('USER_MGMT.BULK_MODE_ADD', '增加')
                                                                            : c.mode === 'sub'
                                                                                ? t('USER_MGMT.BULK_MODE_SUB', '扣减')
                                                                                : t('USER_MGMT.BULK_MODE_SET', '设为');
                                                                        return t('USER_MGMT.LOG_BULK_QUOTA', '批量{{mode}}额度 → 用户 [{{target}}] 从 {{old}} 变为 {{new}}（操作金额 {{amount}}）', {
                                                                            mode: modeText,
                                                                            target: c.target,
                                                                            old: formatCurrency(Number(c.old), 2),
                                                                            new: formatCurrency(Number(c.new), 2),
                                                                            amount: formatCurrency(Number(c.amount), 2),
                                                                        });
                                                                    }
                                                                    if (c.type === 'BULK_HARD_DELETE') return t('USER_MGMT.LOG_BULK_HARD_DELETE', '物理抹除用户 [{{target}}]（ID {{id}}{{extra}}）', {
                                                                        target: c.target,
                                                                        id: c.user_id,
                                                                        extra: c.github_id ? `, gh:${c.github_id}` : '',
                                                                    });
                                                                    if (c.type === 'ADMIN_LOGIN') return t('USER_MGMT.LOG_ADMIN_LOGIN', '管理员账号 [{{username}}] 登录成功', { username: c.username });
                                                                    if (c.type === 'ADMIN_LOGIN_FAIL') return t('USER_MGMT.LOG_ADMIN_LOGIN_FAIL', '管理员账号 [{{username}}] 登录失败（密码错误）', { username: c.username });
                                                                    if (c.type === 'ADMIN_SETUP') return t('USER_MGMT.LOG_ADMIN_SETUP', '管理员凭证 setup：[{{old}}] → [{{next}}]{{mode}}', {
                                                                        old: c.old_username,
                                                                        next: c.new_username,
                                                                        mode: c.initial_setup
                                                                            ? t('USER_MGMT.LOG_ADMIN_SETUP_INITIAL', '（首次安装态）')
                                                                            : t('USER_MGMT.LOG_ADMIN_SETUP_WITH_OLD_PASSWORD', '（带旧密码校验）'),
                                                                    });
                                                                    if (c.type === 'ADMIN_CREDENTIALS_UPDATE') return t('USER_MGMT.LOG_ADMIN_CREDENTIALS_UPDATE', '管理员从面板修改凭证：[{{old}}] → [{{next}}]', { old: c.old_username, next: c.new_username });
                                                                    return JSON.stringify(c);
                                                                });
                                                            }
                                                        } catch (e) {
                                                            lines = log.details.split('；');
                                                        }

                                                        return lines.map((line, i) => (
                                                            <div key={i} className="mb-1.5">
                                                                {line.split(/\[([^\]]+)\]/g).map((part, index) => (
                                                                    index % 2 === 1
                                                                        ? <span key={index} className="text-primary font-bold tracking-wide mx-0.5">{part}</span>
                                                                        : <span key={index}>{part}</span>
                                                                ))}
                                                            </div>
                                                        ));
                                                    })()}
                                                </div>
                                                <div className="mt-4 flex items-center justify-between text-xs text-on-surface-variant border-t border-outline-variant pt-3">
                                                    <span>{t('USER_MGMT.LOG_OPERATOR')} <span className="text-warning font-bold">{(log.operator_role === 'admin' || log.operator_role === 'Admin') ? t('USER_MGMT.LOG_ADMIN') : log.operator_role}</span></span>
                                                    <span>{t('USER_MGMT.LOG_IP')} <span className="text-on-surface font-mono">{log.ip_address}</span></span>
                                                </div>
                                            </div>
                                        </div>
                                    ))}
                                </div>
                            )}
                        </div>
                    </div>
                </div>
            )}

            {/* Edit modal */}
            {modalConfig.isOpen && (
                <div
                    ref={editModalRef}
                    role="dialog"
                    aria-modal="true"
                    aria-labelledby="user-edit-modal-title"
                    onClick={onEditBackdropClick}
                    className="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/60 backdrop-blur-sm animate-in fade-in "
                >
                    <div className="relative w-full max-w-sm bg-surface-container border border-outline-variant rounded-overlay shadow-2xl shadow-black/40 p-6">
                        <button type="button" ref={editCloseBtnRef} onClick={closeEditModal} className="absolute top-4 right-4 text-on-surface-variant hover:text-white ">
                            <X size={18} />
                        </button>
                        <h2 id="user-edit-modal-title" className="text-xl font-bold text-on-surface mb-6">
                            {t('USER_MGMT.MODAL_EDIT_TITLE')}
                        </h2>
                        <form onSubmit={handleSubmit} className="flex flex-col gap-4">
                            <div className="flex flex-col gap-1.5">
                                <label htmlFor="user-mgmt-username" className="text-xs font-semibold text-on-surface-variant ml-1">{t('USER_MGMT.MODAL_USERNAME')}</label>
                                <input
                                    id="user-mgmt-username"
                                    type="text" required
                                    value={formData.username}
                                    onChange={e => setFormData({...formData, username: e.target.value})}
                                    className="w-full h-10 bg-surface-container-high border border-outline rounded-control px-3 text-sm text-on-surface focus:border-primary outline-none"
                                />
                            </div>

                            <div className="flex gap-4">
                                <div className="flex flex-col gap-1.5 flex-1">
                                    <label htmlFor="user-mgmt-quota" className="text-xs font-semibold text-on-surface-variant ml-1">{t('USER_MGMT.MODAL_QUOTA')} {displayCurrency === 'CNY' ? '(￥)' : '($)'}</label>
                                    <input
                                        id="user-mgmt-quota"
                                        type="number" required step="0.001" min="0"
                                        value={displayCurrency === 'CNY' ? (formData.quota * exchangeRate).toFixed(2) : formData.quota}
                                        onChange={e => setFormData({...formData, quota: (parseFloat(e.target.value) || 0) / (displayCurrency === 'CNY' ? exchangeRate : 1)})}
                                        className="w-full h-10 bg-surface-container-high border border-outline rounded-control px-3 text-sm text-on-surface focus:border-primary outline-none"
                                    />
                                </div>
                                <div className="flex flex-col gap-1.5 flex-1">
                                    <label htmlFor="user-mgmt-status" className="text-xs font-semibold text-on-surface-variant ml-1">{t('USER_MGMT.MODAL_STATUS')}</label>
                                    <select
                                        id="user-mgmt-status"
                                        value={formData.status}
                                        onChange={e => setFormData({...formData, status: parseInt(e.target.value)})}
                                        className="w-full h-10 bg-surface-container-high border border-outline rounded-control px-3 text-sm text-on-surface focus:border-primary outline-none"
                                    >
                                        <option value={1}>{t('USER_MGMT.STATUS_NORMAL_OPT')}</option>
                                        <option value={2}>{t('USER_MGMT.STATUS_BANNED_OPT')}</option>
                                    </select>
                                </div>
                            </div>

                            {formData.status === 2 && (
                                <div className="flex flex-col gap-1.5">
                                    <label htmlFor="user-mgmt-ban-reason" className="text-xs font-semibold text-on-surface-variant ml-1">{t('USER_MGMT.MODAL_BAN_REASON')}</label>
                                    {/* The red border is a sensitive-field cue, not a validation error. */}
                                    <textarea
                                        id="user-mgmt-ban-reason"
                                        value={formData.ban_reason}
                                        onChange={e => setFormData({...formData, ban_reason: e.target.value})}
                                        className="w-full bg-surface-container-high border border-error/30 rounded-control p-3 text-sm text-error focus:border-error outline-none placeholder:text-on-surface-variant"
                                        rows={2}
                                        placeholder={t('USER_MGMT.MODAL_BAN_PLACEHOLDER')}
                                    />
                                </div>
                            )}

                            <button type="submit" className="w-full h-10 mt-4 bg-gradient-to-r from-blue-600 to-cyan-500 text-on-surface font-medium rounded-control hover:opacity-90 -opacity">
                                {t('USER_MGMT.BTN_SUBMIT')}
                            </button>
                        </form>
                    </div>
                </div>
            )}

            {/* Bulk quota modal */}
            {bulkModal.isOpen && (
                <div
                    ref={bulkModalRef}
                    role="dialog"
                    aria-modal="true"
                    aria-labelledby="bulk-modal-title"
                    onClick={onBulkBackdropClick}
                    className="fixed inset-0 z-[55] flex items-center justify-center p-4 bg-black/60 backdrop-blur-sm"
                >
                    <div className="relative w-full max-w-sm bg-surface-container border border-outline-variant rounded-overlay shadow-2xl shadow-black/40 p-6">
                        <button type="button" onClick={closeBulkModal} className="absolute top-4 right-4 text-on-surface-variant hover:text-white" aria-label={t('COMMON.CLOSE', '关闭')}>
                            <X size={18} />
                        </button>
                        <h2 id="bulk-modal-title" className="text-xl font-bold text-on-surface mb-2 flex items-center gap-2">
                            {bulkModal.mode === 'add' && <><Plus size={18} className="text-success" /> {t('USER_MGMT.BULK_MODAL_ADD_TITLE', '批量增加额度')}</>}
                            {bulkModal.mode === 'sub' && <><Minus size={18} className="text-warning" /> {t('USER_MGMT.BULK_MODAL_SUB_TITLE', '批量减少额度')}</>}
                            {bulkModal.mode === 'set' && <><Equal size={18} className="text-primary" /> {t('USER_MGMT.BULK_MODAL_SET_TITLE', '批量设置额度')}</>}
                        </h2>
                        <p className="text-xs text-on-surface-variant mb-5">
                            {t('USER_MGMT.BULK_MODAL_DESC_PREFIX', '将作用于已选中的')}{' '}
                            <span className="text-primary font-bold">{selectedIds.size}</span>{' '}
                            {t('USER_MGMT.BULK_MODAL_DESC_SUFFIX', '个用户。管理员账号会被自动跳过。')}
                        </p>

                        <div className="flex flex-col gap-2">
                            <label htmlFor="user-mgmt-bulk-amount" className="text-xs font-semibold text-on-surface-variant ml-1">
                                {t('USER_MGMT.BULK_AMOUNT_LABEL', '金额')} ({displayCurrency === 'CNY' ? '￥' : '$'})
                            </label>
                            <input
                                id="user-mgmt-bulk-amount"
                                type="number"
                                step="0.01"
                                min="0"
                                autoFocus
                                value={bulkModal.amount}
                                onChange={e => setBulkModal({ ...bulkModal, amount: e.target.value })}
                                placeholder={bulkModal.mode === 'set' ? t('USER_MGMT.BULK_AMOUNT_SET_PLACEHOLDER', '例如 1.00') : t('USER_MGMT.BULK_AMOUNT_DELTA_PLACEHOLDER', '例如 0.50')}
                                className="w-full h-11 bg-surface-container-high border border-outline rounded-control px-3 text-base text-on-surface focus:border-primary outline-none font-mono"
                            />
                            {bulkModal.mode === 'sub' && (
                                <p className="text-xs text-warning/80 flex items-start gap-1.5 mt-1">
                                    <AlertTriangle size={12} className="mt-0.5 shrink-0" />
                                    {t('USER_MGMT.BULK_SUB_HINT', '扣减后若额度低于 0，将自动 clamp 到 0，不会变成负数。')}
                                </p>
                            )}
                        </div>

                        <div className="flex gap-3 mt-6">
                            <button
                                onClick={() => setBulkModal({ isOpen: false, mode: 'add', amount: '' })}
                                className="flex-1 h-10 bg-surface-container-high border border-outline-variant text-on-surface-variant rounded-control hover:bg-surface-variant transition-colors text-sm"
                            >
                                {t('COMMON.CANCEL', '取消')}
                            </button>
                            <button
                                onClick={submitBulkQuota}
                                disabled={bulkProcessing || !bulkModal.amount}
                                className="flex-1 h-10 bg-primary text-on-primary font-medium rounded-control hover:opacity-90 disabled:opacity-40 transition-opacity text-sm"
                            >
                                {bulkProcessing ? t('USER_MGMT.BULK_PROCESSING', '处理中...') : t('USER_MGMT.BULK_APPLY', '确认应用')}
                            </button>
                        </div>
                    </div>
                </div>
            )}

            {/* Billing modal */}
            {billsModal.isOpen && billsModal.user && (
                <AdminUserBills
                    userId={billsModal.user.id}
                    username={billsModal.user.username}
                    onClose={() => setBillsModal({ isOpen: false, user: null })}
                />
            )}

            {/* Coupon view/grant modal */}
            {couponsModal.isOpen && couponsModal.user && (
                <AdminUserCouponsModal
                    userId={couponsModal.user.id}
                    username={couponsModal.user.username}
                    onClose={() => setCouponsModal({ isOpen: false, user: null })}
                />
            )}

            {/* Bulk coupon grant modal */}
            <BulkGrantCouponModal
                open={bulkCouponModalOpen}
                onClose={() => setBulkCouponModalOpen(false)}
                userIds={Array.from(selectedIds)}
                onSuccess={() => {
                    setBulkCouponModalOpen(false);
                    clearSelection();
                    fetchUsers();
                }}
            />
        </div>
    );
};

export default UserManagement;
