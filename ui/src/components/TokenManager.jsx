import React, { useState, useEffect, useCallback, useMemo, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { Key, Plus, Copy, Trash2, CheckCircle2, Activity, ShieldAlert, Power, Clock, Save, FileBox, Edit2, Link } from 'lucide-react';
import { useCurrency } from '../context/CurrencyContext';
import toast from 'react-hot-toast';
import { useConfirm } from '../context/ConfirmContext';
import { useAuth } from '../context/AuthContext';
import { authFetch, readAuthState } from '../utils/authFetch';
import { isPageCacheFresh, readPageCache, writePageCache } from '../utils/pageCache';
import { StorePage } from './store/StorePrimitives';
import TextInput from './ui/TextInput';
import StatusBadge from './ui/StatusBadge';
import { DestructiveIconButton } from './ui';

const TOKEN_CACHE_TTL_MS = 30000;

const getTokenCacheKey = () => {
    const { isAdmin, userToken } = readAuthState();
    return `tokens:${isAdmin ? 'admin' : userToken || 'guest'}`;
};

const tokenApiMessage = (code, t) => {
    switch (code) {
        case 'ERR_EMPTY_KEY':
            return t('API.ERR_EMPTY_KEY', '密钥不能为空。');
        case 'ERR_EXPIRED_AT_PAST':
            return t('API.ERR_EXPIRED_AT_PAST', '过期时间不能早于当前时间。');
        case 'ERR_TOKEN_LIMIT_REACHED':
            return t('API.ERR_TOKEN_LIMIT_REACHED', '已达到 Token 创建数量上限。');
        case 'ERR_TOKEN_LOST':
            return t('API.ERR_TOKEN_LOST', '指定的令牌不存在。');
        case 'ERR_MISSING_AUTH_TOKEN':
            return t('API.ERR_MISSING_AUTH_TOKEN', '请求未附带鉴权令牌。');
        case 'ERR_NO_AUTH':
            return t('API.ERR_NO_AUTH', '请先登录。');
        case 'ERR_DB_UPDATE':
            return t('API.ERR_DB_UPDATE', '数据库更新失败。');
        case 'ERR_BAD_REQUEST':
            return t('API.ERR_BAD_REQUEST', '请求格式不正确。');
        case 'ERR_FORBIDDEN':
            return t('API.ERR_FORBIDDEN', '无权执行该操作。');
        default:
            return '';
    }
};

const TokenManager = ({ isAuthenticated }) => {
    const confirm = useConfirm();
    const { isAuthenticated: contextAuthenticated } = useAuth();
    const effectiveIsAuthenticated = isAuthenticated ?? contextAuthenticated;
    const { t } = useTranslation();
    const { formatCurrency } = useCurrency();
    const newTokenNameRef = useRef(null);
    const tokenCacheKey = useMemo(getTokenCacheKey, [effectiveIsAuthenticated]);
    const [tokens, setTokens] = useState(() => readPageCache(tokenCacheKey) || []);
    // Avoid a loading card for signed-out users; RequireAuth already owns that state.
    const [loadingTokens, setLoadingTokens] = useState(() => effectiveIsAuthenticated && !readPageCache(tokenCacheKey));
    const [isCreating, setIsCreating] = useState(false);
    const [newTokenName, setNewTokenName] = useState('');
    const [editingTokenId, setEditingTokenId] = useState(null);
    const [editingName, setEditingName] = useState('');
    const [newQuotaLimit, setNewQuotaLimit] = useState('');
    const [newExpiredAt, setNewExpiredAt] = useState('');
    const [editingQuota, setEditingQuota] = useState('');
    const [editingExpiry, setEditingExpiry] = useState('');

    const fetchTokens = useCallback(async ({ force = false } = {}) => {
        if (!effectiveIsAuthenticated) {
            setTokens([]);
            setLoadingTokens(false);
            return;
        }

        const cached = readPageCache(tokenCacheKey);
        if (cached) {
            setTokens(cached);
            setLoadingTokens(false);
            if (!force && isPageCacheFresh(tokenCacheKey, TOKEN_CACHE_TTL_MS)) return;
        } else {
            setLoadingTokens(true);
        }

        try {
            const data = await authFetch('/api/tokens');
            if (data.success) {
                const nextTokens = data.data || [];
                writePageCache(tokenCacheKey, nextTokens);
                setTokens(nextTokens);
            }
        } catch {
            toast.error(t('TOKEN_MGMT.NET_ERROR'));
        } finally {
            setLoadingTokens(false);
        }
    }, [effectiveIsAuthenticated, t, tokenCacheKey]);

    useEffect(() => {
        if (effectiveIsAuthenticated) {
            fetchTokens();
        }
    }, [effectiveIsAuthenticated, fetchTokens]);

    const handleCreateToken = async () => {
        if (isCreating) return;
        setIsCreating(true);
        try {
            // fix CRITICAL（codex money-unit）：后端 CreateTokenPayload 读 `quota_limit_usd` (USD float)。
            // 旧前端发 `quota_limit` 字段名 → 后端 BodyParser 收不到 → req.QuotaLimitUSD=0 → 无限额漏洞。
            const data = await authFetch('/api/tokens', {
                method: 'POST',
                body: {
                    name: newTokenName.trim() || t('TOKEN_MGMT.UNTITLED_TOKEN'),
                    quota_limit_usd: newQuotaLimit ? parseFloat(newQuotaLimit) : 0,
                    expired_at: newExpiredAt ? new Date(newExpiredAt).toISOString() : null
                }
            });
            if (data.success) {
                if (data.data) {
                    setTokens(prev => {
                        const withoutDuplicate = prev.filter(token => token.id !== data.data.id);
                        const nextTokens = [data.data, ...withoutDuplicate];
                        writePageCache(tokenCacheKey, nextTokens);
                        return nextTokens;
                    });
                }
                setNewTokenName('');
                setNewQuotaLimit('');
                setNewExpiredAt('');
                await fetchTokens({ force: true });
                toast.success(t('TOKEN_MGMT.CREATE_OK', '令牌已创建'));
            } else {
                toast.error(tokenApiMessage(data.message_code, t) || data.message || t('TOKEN_MGMT.CREATE_FAIL', '创建失败'));
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
                fetchTokens({ force: true });
                toast.success(newStatus === 1 ? t('TOKEN_MGMT.ENABLED_OK', '已启用') : t('TOKEN_MGMT.DISABLED_OK', '已禁用'));
            } else {
                toast.error(data.message || tokenApiMessage(data.message_code, t) || t('TOKEN_MGMT.OPERATION_FAILED', '操作失败'));
            }
        } catch {
            toast.error(t('TOKEN_MGMT.NET_ERROR'));
        }
    };

    const handleEditName = (token) => {
        // fix P1（codex review verify-r6）：后端 AccessToken.MarshalJSON 已把 QuotaLimit / UsedQuota
        // 从 int64 micro_usd 转成 USD float（见 database/marshaling.go:46-57）。
        // 前端不应再 / 1e6，否则 $10 会显示为 $0.00001。
        setEditingTokenId(token.id);
        setEditingName(token.name);
        setEditingQuota(token.quota_limit > 0 ? token.quota_limit.toString() : '');
        setEditingExpiry(token.expired_at ? new Date(token.expired_at).toISOString().slice(0,16) : '');
    };

    const handleSaveName = async (id) => {
        // Prevent multiple triggers or empty saves.
        if (editingTokenId !== id) return;

        const trimmed = editingName.trim();
        setEditingTokenId(null);

        if (!trimmed) return;

        // parsedQuota 是 admin 输入的 USD float；后端 MarshalJSON 也输出 USD float，optimistic UI 直接用 USD。
        const parsedQuota = editingQuota ? parseFloat(editingQuota) : 0;
        const parsedExpiry = editingExpiry ? new Date(editingExpiry).toISOString() : null;

        // Optimistic UI update: instantly show the new name to prevent flashing.
        setTokens(prev => prev.map(t => t.id === id ? { ...t, name: trimmed, quota_limit: parsedQuota, expired_at: parsedExpiry } : t));

        try {
            // fix CRITICAL（codex money-unit）：后端 UpdateTokenPayload 读 `quota_limit_usd`，旧前端发 `quota_limit` → 后端忽略。
            const data = await authFetch(`/api/tokens/${id}`, {
                method: 'PUT',
                body: { name: trimmed, quota_limit_usd: parsedQuota, expired_at: parsedExpiry, clear_expiry: !parsedExpiry }
            });
            if (data.success) {
                fetchTokens({ force: true });
            } else {
                // Show the rejection explicitly because the optimistic UI already changed the row.
                toast.error(tokenApiMessage(data.message_code, t) || data.message || t('TOKEN_MGMT.UPDATE_FAILED', '保存失败'));
                fetchTokens({ force: true });
            }
        } catch {
            toast.error(t('TOKEN_MGMT.NET_ERROR'));
            fetchTokens({ force: true });
        }
    };

    const handleDeleteToken = async (id) => {
        if (!(await confirm(t('TOKEN_MGMT.DELETE_CONFIRM')))) return;
        try {
            const data = await authFetch(`/api/tokens/${id}`, { method: 'DELETE' });
            if (data.success) {
                fetchTokens({ force: true });
                toast.success(t('TOKEN_MGMT.DELETE_OK', '令牌已删除'));
            } else {
                toast.error(data.message || tokenApiMessage(data.message_code, t) || t('TOKEN_MGMT.DELETE_FAIL', '删除失败'));
            }
        } catch {
            toast.error(t('TOKEN_MGMT.NET_ERROR'));
        }
    };

    // Phase H Critical fix：原来是 fire-and-forget，复制失败（HTTP / permission denied /
    // clipboard API 不可用）也会无条件弹 success toast。API token 是安全凭据，
    // 假成功比无操作更糟 — 用户以为复制了，粘贴时拿到上次剪贴板里的别人 token。
    const handleCopy = async (text) => {
        try {
            await navigator.clipboard.writeText(text);
            toast.success(t('TOKEN_MGMT.COPY_SUCCESS'));
        } catch (err) {
            toast.error(t('TOKEN_MGMT.COPY_FAIL', '复制失败，请手动选中复制'));
            // eslint-disable-next-line no-console
            console.warn('[TokenManager] clipboard write failed', err);
        }
    };

    const renderTokens = () => (
        <div className="space-y-6">
            {/* Sprint J-3：Token 创建卡 — 用 .card / .input / .btn-primary 原语，
                去掉 bg-primary/5 blur-3xl 装饰，避免主色发散的 noise */}
            <div className="card p-6">
                <div className="flex flex-col md:flex-row md:items-center justify-between gap-4">
                    <div className="min-w-0">
                        <h2 className="text-xl font-bold tracking-tight text-on-surface flex items-center gap-2">
                            <Key className="text-primary" size={20} />
                            {t('TOKEN_MGMT.CREATE_CARD_TITLE')}
                        </h2>
                        <p className="text-sm text-on-surface-variant mt-1">{t('TOKEN_MGMT.CREATE_CARD_DESC')}</p>
                    </div>
                    <div className="flex w-full md:w-auto items-center gap-2 flex-wrap md:flex-nowrap mt-4 md:mt-0">
                        <input
                            ref={newTokenNameRef}
                            type="text"
                            placeholder={t('TOKEN_MGMT.INPUT_PLACEHOLDER')}
                            className="input w-full md:w-[180px]"
                            value={newTokenName}
                            onChange={e => setNewTokenName(e.target.value)}
                        />
                        <input
                            type="number"
                            placeholder={t('TOKEN_MGMT.LIMIT', 'Quota($)')}
                            className="input w-[110px]"
                            value={newQuotaLimit}
                            onChange={e => setNewQuotaLimit(e.target.value)}
                            min="0"
                            step="0.01"
                        />
                        <input
                            type="datetime-local"
                            className="input w-[180px] font-mono"
                            value={newExpiredAt}
                            onChange={e => setNewExpiredAt(e.target.value)}
                        />
                        <button
                            onClick={handleCreateToken}
                            disabled={isCreating}
                            className="btn btn-primary btn-md whitespace-nowrap"
                        >
                            <Plus size={14} />
                            {isCreating ? t('TOKEN_MGMT.BTN_CREATING') : t('TOKEN_MGMT.BTN_CREATE')}
                        </button>
                    </div>
                </div>
            </div>

            {/* Base URL 卡 — 同样换 .card 原语，避免重复的样式声明 */}
            <div className="card p-5 flex flex-col md:flex-row md:items-center justify-between gap-4">
                <div className="flex items-center gap-3 min-w-0">
                    <div className="w-10 h-10 rounded-control bg-primary/10 text-primary flex items-center justify-center shrink-0">
                        <Link size={18} />
                    </div>
                    <div className="min-w-0">
                        <h3 className="text-sm font-semibold text-on-surface">{t('TOKEN_MGMT.BASE_URL_TITLE')}</h3>
                        <p className="text-xs text-on-surface-variant mt-0.5">{t('TOKEN_MGMT.BASE_URL_DESC')}</p>
                    </div>
                </div>
                <div className="flex bg-surface-container-high border border-outline-variant rounded-control overflow-hidden font-mono text-sm w-full md:w-auto">
                    <div className="px-3 py-2 text-on-surface tracking-tight truncate max-w-full md:max-w-md w-full">
                        {window.location.origin}/v1
                    </div>
                    <button
                        onClick={() => handleCopy(`${window.location.origin}/v1`)}
                        className="px-3 py-2 text-primary hover:bg-on-surface/[0.05] border-l border-outline-variant flex items-center justify-center shrink-0"
                        title={t('TOKEN_MGMT.COPY_URL')}
                    >
                        <Copy size={14} />
                    </button>
                </div>
            </div>

            <div className="bg-surface border border-outline-variant rounded-overlay overflow-hidden ">
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
                                        <div className="flex flex-col items-center gap-3">
                                            <Key size={32} className="text-on-surface-variant/50" />
                                            <span>{t('TOKEN_MGMT.EMPTY')}</span>
                                            <button 
                                                onClick={() => newTokenNameRef.current?.focus()}
                                                className="mt-2 text-sm font-semibold text-primary hover:underline inline-flex items-center gap-1"
                                            >
                                                <Plus size={14} /> {t('TOKEN_MGMT.CREATE_FIRST', '创建你的第一个 token')}
                                            </button>
                                        </div>
                                    </td>
                                </tr>
                            ) : tokens.map(token => (
                                <tr key={token.id} className="hover:bg-surface-variant group">
                                    <td className="p-4 text-sm font-medium text-on-surface-variant group/name">
                                        {editingTokenId === token.id ? (
                                            <input
                                                autoFocus
                                                type="text"
                                                className="bg-surface-container-high border border-primary rounded-control px-2 py-1 text-sm w-full md:w-32 outline-none text-on-surface focus:ring-2 ring-primary/20"
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
                                                    type="button"
                                                    onClick={() => handleEditName(token)}
                                                    aria-label={t('TOKEN_MGMT.EDIT_NAME_TOOLTIP', '编辑名称')}
                                                    title={t('TOKEN_MGMT.EDIT_NAME_TOOLTIP', '编辑名称')}
                                                    className="text-on-surface-variant hover:text-white opacity-0 group-hover/name:opacity-100 p-1 rounded-control hover:bg-surface-container-high"
                                                >
                                                    <Edit2 size={12} />
                                                </button>
                                            </div>
                                        )}
                                    </td>
                                    <td className="p-4">
                                        <div className="flex items-center gap-2">
                                            <code
                                                className="text-xs font-mono text-on-surface bg-surface-container-high border border-outline-variant/60 px-2 py-1 rounded-control max-w-[180px] truncate"
                                                title={token.key}
                                            >
                                                {token.key}
                                            </code>
                                            <button
                                                onClick={() => handleCopy(token.key)}
                                                className="text-on-surface-variant hover:text-on-surface p-1 rounded-control hover:bg-on-surface/[0.05]"
                                                title={t('TOKEN_MGMT.COPY_TOKEN', '复制 token')}
                                            >
                                                <Copy size={14} />
                                            </button>
                                        </div>
                                    </td>
                                    <td className="p-4 text-sm tracking-tight text-on-surface-variant">
                                        {editingTokenId === token.id ? (
                                            <input
                                                type="number"
                                                className="bg-surface-container-high border border-primary rounded-control px-2 py-1 text-xs w-20 outline-none text-on-surface focus:ring-2 ring-primary/20"
                                                value={editingQuota}
                                                placeholder={t('TOKEN_MGMT.EDIT_LIMIT_PLACEHOLDER', '额度')}
                                                onChange={e => setEditingQuota(e.target.value)}
                                            />
                                        ) : (
                                            <div className="flex items-center gap-1">
                                                {/* 后端 AccessToken.MarshalJSON 已转 USD float，formatCurrency 直接用。 */}
                                                <span>{formatCurrency(token.used_quota || 0, 3)}</span>
                                                {token.quota_limit > 0 && <span className="text-xs text-outline-variant">/ {formatCurrency(token.quota_limit, 3)}</span>}
                                            </div>
                                        )}
                                    </td>
                                    <td className="p-4 text-xs text-on-surface-variant">
                                        {editingTokenId === token.id ? (
                                            <input
                                                type="datetime-local"
                                                className="bg-surface-container-high border border-primary rounded-control px-2 py-1 flex-1 outline-none text-on-surface focus:ring-2 ring-primary/20 text-xs w-full font-mono"
                                                value={editingExpiry}
                                                onChange={e => setEditingExpiry(e.target.value)}
                                            />
                                        ) : (
                                            token.expired_at ? new Date(token.expired_at).toLocaleString() : t('TOKEN_MGMT.NEVER_EXPIRE', 'Never')
                                        )}
                                    </td>
                                    <td className="p-4">
                                        <button
                                            type="button"
                                            onClick={() => handleToggleStatus(token.id, token.status)}
                                            aria-label={token.status === 1
                                                ? t('TOKEN_MGMT.FREEZE_TOOLTIP', '冻结令牌')
                                                : t('TOKEN_MGMT.ACTIVATE_TOOLTIP', '激活令牌')}
                                        >
                                            <StatusBadge variant={token.status === 1 ? 'success' : 'error'} className="cursor-pointer hover:opacity-80">
                                                <Power size={12} className="mr-1" />
                                                {token.status === 1 ? t('TOKEN_MGMT.STATUS_ACTIVE') : t('TOKEN_MGMT.STATUS_FROZEN')}
                                            </StatusBadge>
                                        </button>
                                    </td>
                                    <td className="p-4 text-right">
                                        <DestructiveIconButton onClick={() => handleDeleteToken(token.id)} icon={Trash2} size={16} title={t('TOKEN_MGMT.DELETE_TOOLTIP', '删除令牌')} />
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
