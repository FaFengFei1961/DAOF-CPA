import React, { useState, useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { Key, Plus, Copy, Trash2, CheckCircle2, Activity, ShieldAlert, Power, Clock, Save, FileBox, Edit2, Link } from 'lucide-react';
import { useCurrency } from '../context/CurrencyContext';
import toast from 'react-hot-toast';
import { useConfirm } from '../context/ConfirmContext';
import { authFetch } from '../utils/authFetch';
import { StorePage } from './store/StorePrimitives';

const TokenManager = ({ isAuthenticated }) => {
    const confirm = useConfirm();
    const { t } = useTranslation();
    const { formatCurrency } = useCurrency();
    const [tokens, setTokens] = useState([]);
    // 未登录时不需要加载（避免显示"加载中…"卡住，让 RequireAuth banner 提示登录即可）
    const [loadingTokens, setLoadingTokens] = useState(isAuthenticated);
    const [isCreating, setIsCreating] = useState(false);
    const [newTokenName, setNewTokenName] = useState('');
    const [editingTokenId, setEditingTokenId] = useState(null);
    const [editingName, setEditingName] = useState('');
    const [newQuotaLimit, setNewQuotaLimit] = useState('');
    const [newExpiredAt, setNewExpiredAt] = useState('');
    const [editingQuota, setEditingQuota] = useState('');
    const [editingExpiry, setEditingExpiry] = useState('');

    const fetchTokens = async () => {
        setLoadingTokens(true);
        try {
            const data = await authFetch('/api/tokens');
            if (data.success) {
                setTokens(data.data || []);
            }
        } catch {
            toast.error(t('TOKEN_MGMT.NET_ERROR'));
        }
        setLoadingTokens(false);
    };

    useEffect(() => {
        if (isAuthenticated) {
            fetchTokens();
        }
    }, [isAuthenticated]);

    const handleCreateToken = async () => {
        if (isCreating) return;
        setIsCreating(true);
        try {
            const data = await authFetch('/api/tokens', {
                method: 'POST',
                body: {
                    name: newTokenName.trim() || t('TOKEN_MGMT.UNTITLED_TOKEN'),
                    quota_limit: newQuotaLimit ? parseFloat(newQuotaLimit) : 0,
                    expired_at: newExpiredAt ? new Date(newExpiredAt).toISOString() : null
                }
            });
            if (data.success) {
                setNewTokenName('');
                setNewQuotaLimit('');
                setNewExpiredAt('');
                fetchTokens();
                toast.success(t('TOKEN_MGMT.CREATE_OK', '令牌已创建'));
            } else {
                toast.error((data.message_code ? t('API.' + data.message_code) : data.message));
            }
        } catch {
            toast.error(t('TOKEN_MGMT.NET_ERROR'));
        }
        setIsCreating(false);
    };

    const handleToggleStatus = async (id, currentStatus) => {
        const newStatus = currentStatus === 1 ? 2 : 1;
        try {
            const data = await authFetch(`/api/tokens/${id}`, {
                method: 'PUT',
                body: { status: newStatus }
            });
            if (data.success) {
                fetchTokens();
                toast.success(newStatus === 1 ? t('TOKEN_MGMT.ENABLED_OK', '已启用') : t('TOKEN_MGMT.DISABLED_OK', '已禁用'));
            } else {
                toast.error(data.message || '操作失败');
            }
        } catch {
            toast.error(t('TOKEN_MGMT.NET_ERROR'));
        }
    };

    const handleEditName = (token) => {
        setEditingTokenId(token.id);
        setEditingName(token.name);
        setEditingQuota(token.quota_limit > 0 ? token.quota_limit.toString() : '');
        setEditingExpiry(token.expired_at ? new Date(token.expired_at).toISOString().slice(0,16) : '');
    };

    const handleSaveName = async (id) => {
        // Prevent multiple triggers or empty saves
        if (editingTokenId !== id) return;
        
        const trimmed = editingName.trim();
        setEditingTokenId(null); // Instantly collapse input box

        if (!trimmed) return;

        const parsedQuota = editingQuota ? parseFloat(editingQuota) : 0;
        const parsedExpiry = editingExpiry ? new Date(editingExpiry).toISOString() : null;

        // Optimistic UI Update: instantly show the new name to prevent flashing
        setTokens(prev => prev.map(t => t.id === id ? { ...t, name: trimmed, quota_limit: parsedQuota, expired_at: parsedExpiry } : t));

        try {
            const data = await authFetch(`/api/tokens/${id}`, {
                method: 'PUT',
                body: { name: trimmed, quota_limit: parsedQuota, expired_at: parsedExpiry, clear_expiry: !parsedExpiry }
            });
            if (data.success) {
                fetchTokens();
            } else {
                // 修改失败必须告诉用户原因，而不是静默 fetch（之前 optimistic update 已写入界面，
                // 用户会以为修改成功，实际服务端拒绝了）
                toast.error((data.message_code ? t('API.' + data.message_code) : data.message) || t('TOKEN_MGMT.UPDATE_FAILED', '保存失败'));
                fetchTokens();
            }
        } catch {
            toast.error(t('TOKEN_MGMT.NET_ERROR'));
            fetchTokens();
        }
    };

    const handleDeleteToken = async (id) => {
        if (!(await confirm(t('TOKEN_MGMT.DELETE_CONFIRM')))) return;
        try {
            const data = await authFetch(`/api/tokens/${id}`, { method: 'DELETE' });
            if (data.success) {
                fetchTokens();
                toast.success(t('TOKEN_MGMT.DELETE_OK', '令牌已删除'));
            } else {
                toast.error(data.message || '删除失败');
            }
        } catch {
            toast.error(t('TOKEN_MGMT.NET_ERROR'));
        }
    };

    const handleCopy = (text) => {
        navigator.clipboard.writeText(text);
        toast.success(t('TOKEN_MGMT.COPY_SUCCESS'));
    };

    const renderTokens = () => (
        <div className="space-y-6">
            <div className="bg-surface border border-outline-variant rounded-2xl p-6 relative overflow-hidden">
                <div className="absolute top-0 right-0 w-32 h-32 bg-primary/5 rounded-full blur-3xl -mr-10 -mt-10 pointer-events-none"></div>
                <div className="relative z-10 flex flex-col md:flex-row md:items-center justify-between gap-4">
                    <div>
                        <h2 className="text-xl font-bold flex items-center gap-2">
                            <Key className="text-primary" size={24} />
                            {t('TOKEN_MGMT.CREATE_CARD_TITLE')}
                        </h2>
                        <p className="text-sm text-on-surface-variant mt-1">{t('TOKEN_MGMT.CREATE_CARD_DESC')}</p>
                    </div>
                    <div className="flex w-full md:w-auto items-center gap-3 flex-wrap md:flex-nowrap mt-4 md:mt-0">
                        <input 
                            type="text" 
                            placeholder={t('TOKEN_MGMT.INPUT_PLACEHOLDER')} 
                            className="w-full md:w-[150px] bg-surface-container-high border border-outline rounded-xl px-4 py-2 text-sm outline-none focus:border-primary "
                            value={newTokenName}
                            onChange={e => setNewTokenName(e.target.value)}
                        />
                        <input
                            type="number"
                            placeholder={t('TOKEN_MGMT.LIMIT', 'Quota($)')}
                            className="w-[110px] bg-surface-container-high border border-outline rounded-xl px-3 py-2 text-sm outline-none focus:border-primary"
                            value={newQuotaLimit}
                            onChange={e => setNewQuotaLimit(e.target.value)}
                            min="0"
                            step="0.01"
                        />
                        <input 
                            type="datetime-local" 
                            className="w-[160px] bg-surface-container-high border border-outline rounded-xl px-2 py-2 text-sm outline-none focus:border-primary text-on-surface-variant font-mono"
                            value={newExpiredAt}
                            onChange={e => setNewExpiredAt(e.target.value)}
                        />
                        <button 
                            onClick={handleCreateToken}
                            disabled={isCreating}
                            className="bg-primary text-on-primary hover:bg-primary-container hover:text-on-primary-container px-4 py-2 rounded-xl font-medium text-sm flex items-center gap-2 disabled:opacity-50 whitespace-nowrap shadow-sm"
                        >
                            <Plus size={16} />
                            {isCreating ? t('TOKEN_MGMT.BTN_CREATING') : t('TOKEN_MGMT.BTN_CREATE')}
                        </button>
                    </div>
                </div>
            </div>

            <div className="bg-surface border border-outline-variant rounded-2xl p-6 flex flex-col md:flex-row md:items-center justify-between gap-4 shadow-lg relative overflow-hidden">
                <div className="flex items-center gap-4 relative z-10">
                    <div className="p-3 bg-primary/10 rounded-xl text-primary">
                        <Link size={24} />
                    </div>
                    <div>
                        <h3 className="text-sm font-semibold text-on-surface">{t('TOKEN_MGMT.BASE_URL_TITLE')}</h3>
                        <p className="text-xs text-on-surface-variant mt-1">{t('TOKEN_MGMT.BASE_URL_DESC')}</p>
                    </div>
                </div>
                <div className="flex bg-surface-container-high border border-outline rounded-xl overflow-hidden font-mono text-sm group relative z-10 w-full md:w-auto">
                    <div className="px-4 py-3 text-on-surface-variant border-r border-outline tracking-tight truncate max-w-full md:max-w-md w-full">
                        {window.location.origin}/v1
                    </div>
                    <button 
                        onClick={() => handleCopy(`${window.location.origin}/v1`)}
                        className="px-4 py-3 text-primary hover:bg-white/5 flex items-center justify-center shrink-0"
                        title={t('TOKEN_MGMT.COPY_URL')}
                    >
                        <Copy size={16} />
                    </button>
                </div>
            </div>

            <div className="bg-surface border border-outline-variant rounded-2xl overflow-hidden shadow-lg">
                <div className="overflow-x-auto">
                    <table className="w-full min-w-[900px] text-left border-collapse table-fixed">
                        <thead>
                            <tr className="bg-surface-container-high border-b border-outline-variant">
                                <th className="p-4 text-xs font-semibold text-on-surface-variant uppercase tracking-wider w-[15%]">{t('TOKEN_MGMT.TABLE_HEAD_NAME')}</th>
                                <th className="p-4 text-xs font-semibold text-on-surface-variant uppercase tracking-wider w-[25%]">{t('TOKEN_MGMT.TABLE_HEAD_TOKEN')}</th>
                                <th className="p-4 text-xs font-semibold text-on-surface-variant uppercase tracking-wider w-[20%]">{t('TOKEN_MGMT.TABLE_HEAD_QUOTA')}</th>
                                <th className="p-4 text-xs font-semibold text-on-surface-variant uppercase tracking-wider w-[15%]">{t('TOKEN_MGMT.TABLE_HEAD_EXPIRY', 'Expires')}</th>
                                <th className="p-4 text-xs font-semibold text-on-surface-variant uppercase tracking-wider w-[15%]">{t('TOKEN_MGMT.TABLE_HEAD_STATUS')}</th>
                                <th className="p-4 text-xs font-semibold text-on-surface-variant uppercase tracking-wider text-right w-[10%]">{t('TOKEN_MGMT.TABLE_HEAD_CTRL')}</th>
                            </tr>
                        </thead>
                        <tbody className="divide-y divide-[#2b2b2b]">
                            {loadingTokens ? (
                                <tr>
                                    <td colSpan="5" className="p-8 h-[250px] align-middle text-center text-on-surface-variant">{t('TOKEN_MGMT.SYNCING')}</td>
                                </tr>
                            ) : tokens.length === 0 ? (
                                <tr>
                                    <td colSpan="6" className="p-8 text-center text-on-surface-variant">
                                        {t('TOKEN_MGMT.EMPTY')}
                                    </td>
                                </tr>
                            ) : tokens.map(token => (
                                <tr key={token.id} className="hover:bg-surface-variant group">
                                    <td className="p-4 text-sm font-medium text-on-surface-variant group/name">
                                        {editingTokenId === token.id ? (
                                            <input 
                                                autoFocus
                                                type="text"
                                                className="bg-surface-container-high border border-primary rounded px-2 py-1 text-sm w-full md:w-32 outline-none text-on-surface focus:ring-2 ring-primary/20"
                                                value={editingName}
                                                onChange={e => setEditingName(e.target.value)}
                                                onKeyDown={e => {
                                                    if (e.key === 'Enter') handleSaveName(token.id);
                                                    if (e.key === 'Escape') setEditingTokenId(null);
                                                }}
                                                onBlur={() => handleSaveName(token.id)}
                                            />
                                        ) : (
                                            <div className="flex items-center gap-2">
                                                <span className="truncate max-w-[150px]" title={token.name}>{token.name}</span>
                                                <button 
                                                    onClick={() => handleEditName(token)}
                                                    className="text-on-surface-variant hover:text-white opacity-0 group-hover/name:opacity-100 p-1 rounded hover:bg-surface-container-high"
                                                >
                                                    <Edit2 size={12} />
                                                </button>
                                            </div>
                                        )}
                                    </td>
                                    <td className="p-4">
                                        <div className="flex items-center gap-2">
                                            <code className="text-xs text-primary bg-primary/20 px-2.5 py-1 rounded-md max-w-[180px] truncate">{token.key}</code>
                                            <button onClick={() => handleCopy(token.key)} className="text-on-surface-variant hover:text-white p-1 rounded-md hover:bg-surface-container-high">
                                                <Copy size={14} />
                                            </button>
                                        </div>
                                    </td>
                                    <td className="p-4 text-sm tracking-tight text-on-surface-variant">
                                        {editingTokenId === token.id ? (
                                            <input 
                                                type="number"
                                                className="bg-surface-container-high border border-primary rounded px-2 py-1 text-xs w-20 outline-none text-on-surface focus:ring-2 ring-primary/20"
                                                value={editingQuota}
                                                placeholder="Limit"
                                                onChange={e => setEditingQuota(e.target.value)}
                                            />
                                        ) : (
                                            <div className="flex items-center gap-1">
                                                <span>{formatCurrency(token.used_quota, 3)}</span>
                                                {token.quota_limit > 0 && <span className="text-xs text-outline-variant">/ {formatCurrency(token.quota_limit, 3)}</span>}
                                            </div>
                                        )}
                                    </td>
                                    <td className="p-4 text-xs text-on-surface-variant">
                                        {editingTokenId === token.id ? (
                                            <input 
                                                type="datetime-local"
                                                className="bg-surface-container-high border border-primary rounded px-2 py-1 flex-1 outline-none text-on-surface focus:ring-2 ring-primary/20 text-xs w-full font-mono"
                                                value={editingExpiry}
                                                onChange={e => setEditingExpiry(e.target.value)}
                                            />
                                        ) : (
                                            token.expired_at ? new Date(token.expired_at).toLocaleString() : t('TOKEN_MGMT.NEVER_EXPIRE', 'Never')
                                        )}
                                    </td>
                                    <td className="p-4">
                                        <button 
                                            onClick={() => handleToggleStatus(token.id, token.status)}
                                            className={`inline-flex items-center gap-1.5 px-2.5 py-1 rounded-full text-xs font-medium cursor-pointer  ${token.status === 1 ? 'bg-green-500/10 text-green-400 hover:bg-green-500/20' : 'bg-red-500/10 text-red-400 hover:bg-red-500/20'}`}
                                        >
                                            <Power size={12} />
                                            {token.status === 1 ? t('TOKEN_MGMT.STATUS_ACTIVE') : t('TOKEN_MGMT.STATUS_FROZEN')}
                                        </button>
                                    </td>
                                    <td className="p-4 text-right">
                                        <button onClick={() => handleDeleteToken(token.id)} className="text-on-surface-variant hover:text-red-400 p-2 rounded-lg hover:bg-red-500/10 ">
                                            <Trash2 size={16} />
                                        </button>
                                    </td>
                                </tr>
                            ))}
                        </tbody>
                    </table>
                </div>
            </div>
        </div>
    );


    return (
        <StorePage
            icon={Key}
            title={t('TOKEN_MGMT.MAIN_TITLE')}
            subtitle={t('TOKEN_MGMT.MAIN_DESC')}
        >
            {renderTokens()}
        </StorePage>
    );
};

export default TokenManager;
