import React, { useState, useEffect, useMemo, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { Users, Edit2, Trash2, X, RefreshCw, Search, History, Filter, ShieldAlert, Key, CheckCircle2, Plus, Minus, Equal, AlertTriangle, Receipt, Ticket } from 'lucide-react';
import AdminUserCouponsModal from './AdminUserCouponsModal';
import { useCurrency } from '../context/CurrencyContext';
import toast from 'react-hot-toast';
import { useConfirm } from '../context/ConfirmContext';
import { authFetch } from '../utils/authFetch';
import AdminUserBills from './AdminUserBills';
import { useModalA11y } from '../hooks/useModalA11y';

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
    // fix（codex 第十六轮验证）：后端 GetUsers 已加分页（默认 50/页, max 200），
    // 前端必须传 page/page_size 并消费 meta，否则只能看到第一页数据。
    const [page, setPage] = useState(1);
    const [pageSize] = useState(50);
    const [total, setTotal] = useState(0);

    // audit log state
    const [logModal, setLogModal] = useState({ isOpen: false, user: null, logs: [], loading: false });
    const [billsModal, setBillsModal] = useState({ isOpen: false, user: null });
    const [couponsModal, setCouponsModal] = useState({ isOpen: false, user: null });

    // 批量选择 state
    const [selectedIds, setSelectedIds] = useState(() => new Set());
    const [bulkModal, setBulkModal] = useState({ isOpen: false, mode: 'add', amount: '' });
    const [bulkProcessing, setBulkProcessing] = useState(false);

    // fix Major M8（gemini 第十五轮）：原模态框无 ESC + 背景点击关闭，与 AdminUserBills 标准不一致
    const closeLogModal = () => setLogModal({ isOpen: false, user: null, logs: [] });
    const closeBulkModal = () => setBulkModal({ isOpen: false, mode: 'add', amount: '' });
    // fix CRITICAL C22-F1（gemini 第二十二轮）：编辑模态原 closeEditModal 未声明，
    // linter 已加 editModalRef/editCloseBtnRef 但 closeEditModal 仍漏 → 补声明。
    const closeEditModal = () => setModalConfig({ isOpen: false, data: null });
    // fix CRITICAL C-F1（gemini 第二十一轮）：补 modalRef 让 focus trap 真正生效
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
            toast.error('请输入有效的非负金额');
            return;
        }
        setBulkProcessing(true);
        try {
            // 前端 displayCurrency 可能是 CNY，但后端只接受 USD
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
                toast.success(`已更新 ${data.updated} 人${data.skipped > 0 ? `，跳过 ${data.skipped} 人（管理员保护）` : ''}`);
                setBulkModal({ isOpen: false, mode: 'add', amount: '' });
                clearSelection();
                fetchUsers();
            } else {
                toast.error((data.message_code ? t('API.' + data.message_code) : data.message) || '批量操作失败');
            }
        } catch (e) {
            toast.error('网络异常，批量操作失败');
        }
        setBulkProcessing(false);
    };

    const submitBulkDelete = async () => {
        if (!(await confirm(`即将物理删除 ${selectedIds.size} 个用户（含其所有 token），不可恢复，确认？`))) return;
        setBulkProcessing(true);
        try {
            const data = await authFetch('/api/admin/users/bulk-delete', {
                method: 'POST',
                body: { user_ids: Array.from(selectedIds) },
            });
            if (data.success) {
                toast.success(`已抹除 ${data.deleted} 个用户${data.skipped > 0 ? `，跳过 ${data.skipped} 个（管理员保护）` : ''}`);
                clearSelection();
                fetchUsers();
            } else {
                toast.error((data.message_code ? t('API.' + data.message_code) : data.message) || '批量删除失败');
            }
        } catch (e) {
            toast.error('网络异常，批量删除失败');
        }
        setBulkProcessing(false);
    };

    const fetchUsers = async (query = searchQuery, sort = sortBy, p = page) => {
        setLoading(true);
        try {
            const params = new URLSearchParams();
            // 后端 search 长度 ≥2 ≤64；空字符串不传，前端 query<2 也不传避免 400
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

    // fix Major Codex UX 审查（第二十五轮）：原有 'add' 模式残留 —— 后端无 POST /api/admin/users，
    // 前端也没有"添加用户"按钮，但 handleOpenModal/handleSubmit 仍含 add 分支。
    // 项目尚未上线，直接删除 add 模式（用户走 OAuth/SMS 注册自助创建，admin 无需手动添加）。
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

    // search/sort 变化时重置到第一页（page 改了会触发上面的 effect 重新拉取）
    useEffect(() => { setPage(1); }, [searchQuery, sortBy]);

    const totalPages = Math.max(1, Math.ceil(total / pageSize));

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

            {/* 批量操作浮动栏 */}
            {selectedIds.size > 0 && (
                <div className="mb-4 flex items-center justify-between bg-primary/10 border border-primary/30 rounded-overlay px-4 py-3 sticky top-2 z-10 backdrop-blur-md">
                    <div className="flex items-center gap-3">
                        <CheckCircle2 size={18} className="text-primary" />
                        <span className="text-sm text-on-surface font-medium">
                            已选中 <span className="text-primary font-bold">{selectedIds.size}</span> 个用户
                        </span>
                        <button onClick={clearSelection} className="text-xs text-on-surface-variant hover:text-on-surface underline">
                            取消选择
                        </button>
                    </div>
                    <div className="flex items-center gap-2">
                        <button
                            onClick={() => openBulkModal('add')}
                            className="flex items-center gap-1.5 px-3 py-1.5 bg-success/20 hover:bg-success/40 text-success border border-success/40 rounded-control text-xs font-medium transition-colors"
                            title="为所选用户增加额度"
                        >
                            <Plus size={14} /> 增加额度
                        </button>
                        <button
                            onClick={() => openBulkModal('sub')}
                            className="flex items-center gap-1.5 px-3 py-1.5 bg-warning/20 hover:bg-warning/40 text-warning border border-warning/40 rounded-control text-xs font-medium transition-colors"
                            title="为所选用户扣减额度（不会扣到负数）"
                        >
                            <Minus size={14} /> 减少额度
                        </button>
                        <button
                            onClick={() => openBulkModal('set')}
                            className="flex items-center gap-1.5 px-3 py-1.5 bg-primary/20 hover:bg-primary/40 text-primary border border-primary/40 rounded-control text-xs font-medium transition-colors"
                            title="把所选用户的额度直接设置为某值"
                        >
                            <Equal size={14} /> 设为定值
                        </button>
                        <div className="w-px h-6 bg-outline-variant mx-1" />
                        <button
                            onClick={submitBulkDelete}
                            disabled={bulkProcessing}
                            className="flex items-center gap-1.5 px-3 py-1.5 bg-error/20 hover:bg-error/40 text-error border border-error/40 rounded-control text-xs font-medium transition-colors disabled:opacity-50"
                            title="物理删除所选用户（含 token），不可恢复"
                        >
                            <Trash2 size={14} /> 批量删除
                        </button>
                    </div>
                </div>
            )}

            <div className="bg-surface-container border border-outline-variant rounded-overlay overflow-hidden ">
                <div className="overflow-x-auto">
                    <table className="w-full min-w-[900px] text-left text-sm text-on-surface-variant table-fixed">
                        <thead className="bg-surface-container-high text-xs uppercase font-mono tracking-wider text-on-surface-variant border-b border-outline-variant">
                            <tr>
                                <th className="px-4 py-4 font-medium w-[40px]">
                                    <input
                                        type="checkbox"
                                        checked={allSelected}
                                        ref={el => { if (el) el.indeterminate = someSelected; }}
                                        onChange={toggleSelectAll}
                                        className="w-4 h-4 cursor-pointer accent-primary"
                                        title="全选普通用户（admin 不可选）"
                                    />
                                </th>
                                <th className="px-6 py-4 font-medium w-[18%]">{t('USER_MGMT.TABLE.ID_NAME')}</th>
                                <th className="px-6 py-4 font-medium w-[24%]">{t('USER_MGMT.TABLE.BINDING')}</th>
                                <th className="px-6 py-4 font-medium w-[18%]">{t('USER_MGMT.TABLE.REG_TIME')}</th>
                                <th className="px-6 py-4 font-medium w-[14%]">{t('USER_MGMT.TABLE.QUOTA')}</th>
                                <th className="px-6 py-4 font-medium text-center w-[10%]">{t('USER_MGMT.TABLE.STATUS')}</th>
                                <th className="px-6 py-4 font-medium text-right w-[10%]">{t('USER_MGMT.TABLE.ACTIONS')}</th>
                            </tr>
                        </thead>
                        <tbody className="divide-y divide-[#2b2b2b]/50">
                            {loading ? (
                                <tr>
                                    <td colSpan="7" className="px-6 py-12 text-center text-on-surface-variant">
                                        <RefreshCw size={24} className="mx-auto mb-2" />
                                        {t('USER_MGMT.LOADING_TEXT')}
                                    </td>
                                </tr>
                            ) : users.length === 0 ? (
                                <tr>
                                    <td colSpan="7" className="px-6 py-12 text-center text-on-surface-variant">
                                        {t('USER_MGMT.EMPTY')}
                                    </td>
                                </tr>
                            ) : (
                                users.map(u => (
                                    <tr key={u.id} className={`hover:bg-surface-variant group ${selectedIds.has(u.id) ? 'bg-primary/5' : ''}`}>
                                        <td className="px-4 py-4">
                                            {u.role === 'admin' ? (
                                                <span className="text-fuchsia-400 text-xs" title="管理员账号受保护">🔒</span>
                                            ) : (
                                                <input
                                                    type="checkbox"
                                                    checked={selectedIds.has(u.id)}
                                                    onChange={() => toggleSelect(u.id)}
                                                    className="w-4 h-4 cursor-pointer accent-primary"
                                                    aria-label={`选择用户 ${u.username}`}
                                                />
                                            )}
                                        </td>
                                        <td className="px-6 py-4">
                                            <div className="flex flex-col">
                                                <span className="text-on-surface font-medium">{u.username}</span>
                                                <span className="text-xs text-primary/70 font-mono mt-1">{t('USER_MGMT.ID_PREFIX', { id: u.id })} {u.role === 'admin' ? '[GOD]' : ''}</span>
                                            </div>
                                        </td>
                                        <td className="px-6 py-4">
                                            <div className="flex flex-col gap-1">
                                                {u.github_id ? <span className="text-xs text-on-surface-variant bg-surface-variant px-2 py-0.5 rounded-control w-max">{t('USER_MGMT.GITHUB_BOUND', { id: u.github_id })}</span> : <span className="text-xs text-outline-variant italic">{t('USER_MGMT.GITHUB_UNBOUND')}</span>}
                                                {u.phone ? <span className="text-xs text-warning bg-warning/10 px-2 py-0.5 rounded-control w-max">{t('USER_MGMT.PHONE_BOUND', { phone: u.phone })}</span> : <span className="text-xs text-outline-variant italic">{t('USER_MGMT.PHONE_UNBOUND')}</span>}
                                            </div>
                                        </td>
                                        <td className="px-6 py-4 text-xs text-on-surface-variant">
                                            {new Date(u.created_at).toLocaleString('zh-CN', { hour12: false })}
                                        </td>
                                        <td className="px-6 py-4 font-mono">
                                            {u.role === 'admin'
                                                ? <span className="text-fuchsia-400 font-bold tracking-widest text-lg">∞</span>
                                                : <span className={u.quota > 0 ? "text-success" : "text-on-surface-variant"}>{formatCurrency(u.quota, 2)}</span>}
                                        </td>
                                        <td className="px-6 py-4 text-center">
                                            {u.status === 1
                                                ? <div className="flex items-center gap-2 justify-center"><span className="w-2 h-2 rounded-control-full bg-success "></span> <span className="text-xs text-success">{t('USER_MGMT.STATUS_NORMAL')}</span></div>
                                                : <div className="flex items-center gap-2 justify-center"><span className="w-2 h-2 rounded-control-full bg-error"></span> <span className="text-xs text-error">{t('USER_MGMT.STATUS_BANNED')}</span></div>
                                            }
                                        </td>
                                        <td className="px-6 py-4 text-right">
                                            <div className="flex items-center justify-end gap-3 opacity-50 group-hover:opacity-100 -opacity">
                                                <button onClick={() => openLogModal(u)} className="text-on-surface-variant hover:text-success tooltip" aria-label={t('USER_MGMT.LOG_TOOLTIP')} title={t('USER_MGMT.LOG_TOOLTIP')}>
                                                    <History size={16} />
                                                </button>
                                                <button
                                                    onClick={() => setBillsModal({ isOpen: true, user: u })}
                                                    className="text-on-surface-variant hover:text-primary tooltip"
                                                    aria-label={t('USER_MGMT.BILLS_TOOLTIP', '查看账单')}
                                                    title={t('USER_MGMT.BILLS_TOOLTIP', '查看账单')}
                                                >
                                                    <Receipt size={16} />
                                                </button>
                                                <button
                                                    onClick={() => setCouponsModal({ isOpen: true, user: u })}
                                                    className="text-on-surface-variant hover:text-fuchsia-400 tooltip"
                                                    aria-label={t('USER_MGMT.COUPONS_TOOLTIP', '查看/发放优惠券')}
                                                    title={t('USER_MGMT.COUPONS_TOOLTIP', '查看/发放优惠券')}
                                                >
                                                    <Ticket size={16} />
                                                </button>
                                                {u.role !== 'admin' && (
                                                    <>
                                                        <button onClick={() => handleOpenModal(u)} className="text-on-surface-variant hover:text-primary tooltip" aria-label={t('USER_MGMT.EDIT_TOOLTIP')} title={t('USER_MGMT.EDIT_TOOLTIP')}>
                                                            <Edit2 size={16} />
                                                        </button>
                                                        <button onClick={() => handleDelete(u.id)} className="text-on-surface-variant hover:text-error" aria-label={t('USER_MGMT.DELETE_TOOLTIP')} title={t('USER_MGMT.DELETE_TOOLTIP')}>
                                                            <Trash2 size={16} />
                                                        </button>
                                                    </>
                                                )}
                                            </div>
                                        </td>
                                    </tr>
                                ))
                            )}
                        </tbody>
                    </table>
                </div>
                {/* 分页控件（codex 第十六轮 fix）：后端已分页，前端必须暴露翻页 */}
                {total > pageSize && (
                    <div className="flex items-center justify-between px-4 py-3 border-t border-outline-variant text-sm">
                        <span className="text-on-surface-variant">
                            {t('USER_MGMT.PAGE_INFO', '第 {{page}}/{{total}} 页 · 共 {{count}} 条', {
                                page,
                                total: totalPages,
                                count: total,
                            })}
                        </span>
                        <div className="flex gap-2">
                            <button
                                type="button"
                                disabled={page <= 1}
                                onClick={() => setPage(p => Math.max(1, p - 1))}
                                className="px-3 py-1.5 rounded-control border border-outline-variant disabled:opacity-40 hover:bg-on-surface/[0.04]"
                            >
                                ← {t('COMMON.PREV', '上一页')}
                            </button>
                            <button
                                type="button"
                                disabled={page >= totalPages}
                                onClick={() => setPage(p => Math.min(totalPages, p + 1))}
                                className="px-3 py-1.5 rounded-control border border-outline-variant disabled:opacity-40 hover:bg-on-surface/[0.04]"
                            >
                                {t('COMMON.NEXT', '下一页')} →
                            </button>
                        </div>
                    </div>
                )}
            </div>

            {/* 时空审计流弹窗 */}
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
                                            <div className="flex items-center justify-center w-6 h-6 rounded-control-full border-2 border-surface bg-surface-variant text-on-surface-variant group-hover:bg-primary group-hover:text-on-primary shadow shrink-0 md:order-1 md:group-odd:-translate-x-1/2 md:group-even:translate-x-1/2 z-10 font-mono text-xs">
                                                {log.id}
                                            </div>
                                            <div className="w-[calc(100%-2.5rem)] md:w-[calc(50%-2rem)] p-4 rounded-overlay border border-outline-variant bg-surface-container ">
                                                <div className="flex items-center justify-between mb-2">
                                                    <div className="text-sm font-bold text-primary">
                                                        {log.action_type === 'CREATE' ? t('USER_MGMT.ACTION_CREATE') :
                                                         log.action_type === 'UPDATE' ? t('USER_MGMT.ACTION_UPDATE') :
                                                         log.action_type === 'DELETE' ? t('USER_MGMT.ACTION_DELETE') :
                                                         log.action_type === 'LOGIN' ? '🔓 用户登录' :
                                                         log.action_type === 'REGISTER' ? '🎉 注册成功' :
                                                         log.action_type === 'CREATE_TOKEN' ? '🔑 创建 API 令牌' :
                                                         log.action_type === 'UPDATE_TOKEN' ? '✏️ 修改 API 令牌' :
                                                         log.action_type === 'DELETE_TOKEN' ? '🗑️ 删除 API 令牌' :
                                                         log.action_type === 'BULK_QUOTA' ? '💰 批量调整额度' :
                                                         log.action_type === 'BULK_HARD_DELETE' ? '☢️ 物理抹除' :
                                                         log.action_type === 'ADMIN_LOGIN' ? '🛡️ 管理员登录' :
                                                         log.action_type === 'ADMIN_LOGIN_FAIL' ? '⚠️ 管理员登录失败' :
                                                         log.action_type === 'ADMIN_SETUP' ? '🔧 管理员凭证 setup' :
                                                         log.action_type === 'ADMIN_CREDENTIALS_UPDATE' ? '🔐 管理员凭证修改' :
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
                                                                    if (c.type === 'LOGIN') return `通过 [${c.via || 'unknown'}] 登录回归`;
                                                                    if (c.type === 'REGISTER') return `经 [${c.via || 'unknown'}] 完成注册（用户名 [${c.username}]${c.github_id ? `, gh:${c.github_id}` : ''}${c.phone ? `, 📱${c.phone}` : ''}）`;
                                                                    if (c.type === 'CREATE_TOKEN') return `创建 API 令牌 [${c.name}]，限额 ${formatCurrency(Number(c.quota_limit) || 0, 2)}（0 表示不限）`;
                                                                    if (c.type === 'UPDATE_TOKEN') return `修改 API 令牌 [${c.name}]（ID ${c.token_id}）`;
                                                                    if (c.type === 'DELETE_TOKEN') return `删除 API 令牌 [${c.token_name}]（ID ${c.token_id}）`;
                                                                    if (c.type === 'BULK_QUOTA') {
                                                                        const modeText = c.mode === 'add' ? '增加' : c.mode === 'sub' ? '扣减' : '设为';
                                                                        return `批量${modeText}额度 → 用户 [${c.target}] 从 ${formatCurrency(Number(c.old), 2)} 变为 ${formatCurrency(Number(c.new), 2)}（操作金额 ${formatCurrency(Number(c.amount), 2)}）`;
                                                                    }
                                                                    if (c.type === 'BULK_HARD_DELETE') return `物理抹除用户 [${c.target}]（ID ${c.user_id}${c.github_id ? `, gh:${c.github_id}` : ''}）`;
                                                                    if (c.type === 'ADMIN_LOGIN') return `管理员账号 [${c.username}] 登录成功`;
                                                                    if (c.type === 'ADMIN_LOGIN_FAIL') return `管理员账号 [${c.username}] 登录失败（密码错误）`;
                                                                    if (c.type === 'ADMIN_SETUP') return `管理员凭证 setup：[${c.old_username}] → [${c.new_username}]${c.initial_setup ? '（首次安装态）' : '（带旧密码校验）'}`;
                                                                    if (c.type === 'ADMIN_CREDENTIALS_UPDATE') return `管理员从面板修改凭证：[${c.old_username}] → [${c.new_username}]`;
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
                                                    <span>{t('USER_MGMT.LOG_OPERATOR')} <span className="text-warning font-bold">{(log.operator_role === '管理员(Admin)' || log.operator_role === '管理员') ? t('USER_MGMT.LOG_ADMIN') : log.operator_role}</span></span>
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

            {/* 编辑/新建 弹窗 */}
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
                                    {/* fix M22-F1（codex 第二十二轮）：原 aria-invalid="true" 是误用 ——
                                        红色边框只是"这是敏感字段"的视觉提示，不是表单校验错误。
                                        恒真的 aria-invalid 会让屏幕阅读器每键都播报"无效"，反而是噪音。
                                        ban reason 暂无校验规则，移除 aria-invalid。 */}
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

            {/* 批量额度调整弹窗 */}
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
                            {bulkModal.mode === 'add' && <><Plus size={18} className="text-success" /> 批量增加额度</>}
                            {bulkModal.mode === 'sub' && <><Minus size={18} className="text-warning" /> 批量减少额度</>}
                            {bulkModal.mode === 'set' && <><Equal size={18} className="text-primary" /> 批量设置额度</>}
                        </h2>
                        <p className="text-xs text-on-surface-variant mb-5">
                            将作用于已选中的 <span className="text-primary font-bold">{selectedIds.size}</span> 个用户。管理员账号会被自动跳过。
                        </p>

                        <div className="flex flex-col gap-2">
                            <label htmlFor="user-mgmt-bulk-amount" className="text-xs font-semibold text-on-surface-variant ml-1">
                                金额 ({displayCurrency === 'CNY' ? '￥' : '$'})
                            </label>
                            <input
                                id="user-mgmt-bulk-amount"
                                type="number"
                                step="0.01"
                                min="0"
                                autoFocus
                                value={bulkModal.amount}
                                onChange={e => setBulkModal({ ...bulkModal, amount: e.target.value })}
                                placeholder={bulkModal.mode === 'set' ? '例如 1.00' : '例如 0.50'}
                                className="w-full h-11 bg-surface-container-high border border-outline rounded-control px-3 text-base text-on-surface focus:border-primary outline-none font-mono"
                            />
                            {bulkModal.mode === 'sub' && (
                                <p className="text-xs text-warning/80 flex items-start gap-1.5 mt-1">
                                    <AlertTriangle size={12} className="mt-0.5 shrink-0" />
                                    扣减后若额度低于 0，将自动 clamp 到 0，不会变成负数。
                                </p>
                            )}
                        </div>

                        <div className="flex gap-3 mt-6">
                            <button
                                onClick={() => setBulkModal({ isOpen: false, mode: 'add', amount: '' })}
                                className="flex-1 h-10 bg-surface-container-high border border-outline-variant text-on-surface-variant rounded-control hover:bg-surface-variant transition-colors text-sm"
                            >
                                取消
                            </button>
                            <button
                                onClick={submitBulkQuota}
                                disabled={bulkProcessing || !bulkModal.amount}
                                className="flex-1 h-10 bg-primary text-on-primary font-medium rounded-control hover:opacity-90 disabled:opacity-40 transition-opacity text-sm"
                            >
                                {bulkProcessing ? '处理中...' : '确认应用'}
                            </button>
                        </div>
                    </div>
                </div>
            )}

            {/* 账单查看模态框 */}
            {billsModal.isOpen && billsModal.user && (
                <AdminUserBills
                    userId={billsModal.user.id}
                    username={billsModal.user.username}
                    onClose={() => setBillsModal({ isOpen: false, user: null })}
                />
            )}

            {/* 优惠券查看 + 发放模态框（fix R23+2-F2/F3：admin 独立看券+发券入口） */}
            {couponsModal.isOpen && couponsModal.user && (
                <AdminUserCouponsModal
                    userId={couponsModal.user.id}
                    username={couponsModal.user.username}
                    onClose={() => setCouponsModal({ isOpen: false, user: null })}
                />
            )}
        </div>
    );
};

export default UserManagement;
