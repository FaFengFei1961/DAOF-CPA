import React, { useState, useEffect, useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { User, Copy, CheckCircle2, Lock, ShieldAlert, Key } from 'lucide-react';
import toast from 'react-hot-toast';
import { useConfirm } from '../context/ConfirmContext';
import { authFetch, readAuthState } from '../utils/authFetch';
import { isPageCacheFresh, readPageCache, writePageCache } from '../utils/pageCache';

const PROFILE_CACHE_TTL_MS = 30000;
const BANNED_MARKER = '\u5c01\u7981';
const getProfileCacheKey = () => {
    const { isAdmin, userToken } = readAuthState();
    return `profile:${isAdmin ? 'admin' : userToken || 'guest'}`;
};

const AccountProfile = () => {
    const confirm = useConfirm();
    const { t } = useTranslation();
    const profileCacheKey = useMemo(getProfileCacheKey, []);
    const cachedProfile = readPageCache(profileCacheKey);
    const [profile, setProfile] = useState(() => cachedProfile);
    const [loading, setLoading] = useState(() => !cachedProfile);


    // Admin form state
    const [adminForm, setAdminForm] = useState(() => ({
        username: cachedProfile?.role === 'admin' ? cachedProfile.username : '',
        password: ''
    }));
    const [updatingAdmin, setUpdatingAdmin] = useState(false);

    useEffect(() => {
        const isAdmin = localStorage.getItem('daof_admin_unlocked') === '1';
        const userToken = localStorage.getItem('daof_token');
        if (!isAdmin && !userToken) {
            setLoading(false);
            return;
        }
        const cached = readPageCache(profileCacheKey);
        if (cached) {
            setProfile(cached);
            if (cached.role === 'admin') setAdminForm({ username: cached.username, password: '' });
            setLoading(false);
            if (isPageCacheFresh(profileCacheKey, PROFILE_CACHE_TTL_MS)) return;
        }
        authFetch('/api/user/me')
            .then(data => {
                if (data.success) {
                    writePageCache(profileCacheKey, data.data);
                    setProfile(data.data);
                    if (data.data.role === 'admin') {
                        setAdminForm({ username: data.data.username, password: '' });
                    }
                } else if (data.message_code === 'ERR_BANNED' || (data.message && data.message.includes(BANNED_MARKER))) {
                    return;
                }
                setLoading(false);
            })
            .catch(() => setLoading(false));
    }, [profileCacheKey]);



    const handleAdminUpdate = async (e) => {
        e.preventDefault();
        if (!(await confirm(t('ACCOUNT.ADMIN_UPDATE_WARN', { username: adminForm.username })))) {
            return;
        }
        setUpdatingAdmin(true);
        try {
            const data = await authFetch('/api/admin/credentials', {
                method: 'PUT',
                body: adminForm,
            });
            if (data.success) {
                toast.success(t('ACCOUNT.SAVE_SUCCESS') + "\n" + t('ACCOUNT.NEW_ENTRY_POINT', { username: adminForm.username }), { duration: 5000 });
                localStorage.removeItem('daof_admin_unlocked');
                await fetch('/api/root/logout', { method: 'POST', credentials: 'include' }).catch(() => {});
                window.location.href = `/?sys=${adminForm.username}`;
            } else {
                toast.error((data.message_code ? t('API.' + data.message_code) : data.message));
            }
        } catch {
            toast.error(t('ACCOUNT.REQ_ERROR'));
        }
        setUpdatingAdmin(false);
    };

    if (loading) return <div className="text-on-surface-variant p-8 text-center ">{t('ACCOUNT.LOADING')}</div>;
    if (!profile) return <div className="bg-error/10 border border-error/30 text-error p-6 rounded-overlay text-center">{t('ACCOUNT.LOAD_FAILED')}</div>;

    return (
        <div className="fl-card p-8 mb-8">
            <div className="flex items-center gap-3 mb-6">
                <div className="w-10 h-10 rounded-overlay bg-primary/30 flex items-center justify-center">
                    <User size={20} className="text-primary" />
                </div>
                <h2 className="text-xl font-bold">{t('ACCOUNT.TITLE')}</h2>
            </div>

            <div className="space-y-6">

                <div className="bg-surface-container-high border border-outline rounded-overlay p-6 relative overflow-hidden">
                   <div className="absolute top-0 right-0 w-32 h-32 bg-primary/5 rounded-full blur-2xl -mr-10 -mt-10 pointer-events-none"></div>
                   <div className="flex flex-col md:flex-row md:items-center justify-between gap-4 relative z-10">
                      <div>
                         <p className="text-sm text-on-surface-variant mb-1">{t('ACCOUNT.USERNAME_LABEL')}</p>
                         <p className="text-2xl font-bold font-mono tracking-tight text-on-surface">{profile.username}</p>
                      </div>
                      <div className="text-left md:text-right">
                         <p className="text-sm text-on-surface-variant mb-1">{t('ACCOUNT.ROLE_LABEL')}</p>
                         <div className="inline-flex items-center gap-2">
                             {profile.role === 'admin'
                               ? <span className="bg-fuchsia-500/20 text-fuchsia-400 text-sm px-3 py-1 rounded-control font-medium border border-fuchsia-500/30">{t('ACCOUNT.ROLE_ADMIN')}</span>
                               : <span className="bg-surface-container-high text-on-surface-variant text-sm px-3 py-1 rounded-control font-medium border border-outline-variant">{t('ACCOUNT.ROLE_USER')}</span>
                             }
                         </div>
                      </div>
                   </div>
                </div>



                {profile.role !== 'admin' && (
                    <div className="bg-surface-container-high border border-outline rounded-overlay p-6">
                        <div className="flex items-start gap-3 mb-3">
                            <span className="text-2xl">🎁</span>
                            <div>
                                <h3 className="text-base font-bold text-on-surface tracking-tight">
                                    {t('ACCOUNT.REFERRAL_TITLE', '我的拉新推荐链接')}
                                </h3>
                                <p className="text-xs text-on-surface-variant mt-1">
                                    {t('ACCOUNT.REFERRAL_DESC', '分享给朋友。每成功带来一个新用户，你和对方都将获得平台奖励额度（金额由管理员配置）。')}
                                </p>
                            </div>
                        </div>
                        <div className="flex items-stretch gap-2">
                            <input
                                type="text"
                                readOnly
                                value={`${window.location.origin}/?ref=${profile.username}`}
                                className="flex-1 h-10 bg-black/40 border border-outline-variant rounded-control px-3 text-xs text-on-surface font-mono outline-none select-all"
                            />
                            <button
                                onClick={() => {
                                    navigator.clipboard.writeText(`${window.location.origin}/?ref=${profile.username}`);
                                    toast.success(t('ACCOUNT.REFERRAL_COPIED', '推荐链接已复制'));
                                }}
                                className="px-4 bg-primary text-on-primary rounded-control text-sm font-medium hover:opacity-90 flex items-center gap-1"
                            >
                                <Copy size={14} />
                                {t('ACCOUNT.COPY', '复制')}
                            </button>
                        </div>
                    </div>
                )}




                {profile.role === 'admin' && (
                    <div className="bg-surface-container-high border border-error/30 rounded-overlay p-6">
                        <div className="flex items-start gap-3 mb-6">
                            <ShieldAlert className="text-error shrink-0 mt-0.5" size={20} />
                            <div>
                                <h3 className="text-lg font-bold text-on-surface tracking-tight">{t('ACCOUNT.ROOT_OVERRIDE_TITLE')}</h3>
                                <p className="text-sm text-on-surface-variant mt-1 leading-relaxed">
                                    {t('ACCOUNT.ROOT_OVERRIDE_DESC')}
                                </p>
                            </div>
                        </div>

                        <form onSubmit={handleAdminUpdate} className="space-y-4">
                            <div className="flex flex-col md:flex-row gap-4">
                                <div className="flex-1 space-y-1.5">
                                    <label htmlFor="profile-admin-username" className="text-xs font-semibold text-on-surface-variant">{t('ACCOUNT.NEW_USERNAME_LABEL')}</label>
                                    <input
                                        id="profile-admin-username"
                                        type="text" required
                                        value={adminForm.username}
                                        onChange={e => setAdminForm({...adminForm, username: e.target.value})}
                                        className="w-full h-11 bg-black/50 border border-outline rounded-control px-4 text-sm text-on-surface focus:border-error focus:bg-surface outline-none "
                                        placeholder={t('ACCOUNT.NEW_USERNAME_PLACEHOLDER')}
                                    />
                                </div>
                                <div className="flex-1 space-y-1.5">
                                    <label htmlFor="profile-admin-password" className="text-xs font-semibold text-on-surface-variant">{t('ACCOUNT.NEW_PASSWORD_LABEL')}</label>
                                    <input
                                        id="profile-admin-password"
                                        type="password" required autoComplete="new-password"
                                        value={adminForm.password}
                                        onChange={e => setAdminForm({...adminForm, password: e.target.value})}
                                        className="w-full h-11 bg-black/50 border border-outline rounded-control px-4 text-sm text-on-surface focus:border-error focus:bg-surface outline-none "
                                        placeholder={t('ACCOUNT.NEW_PASSWORD_PLACEHOLDER')}
                                    />
                                </div>
                            </div>
                            <button
                                type="submit"
                                disabled={updatingAdmin}
                                className="h-11 px-6 bg-error hover:bg-error disabled:opacity-50 disabled:cursor-not-allowed text-on-surface font-medium rounded-control flex items-center justify-center gap-2 mt-4"
                            >
                                <Lock size={18} />
                                {updatingAdmin ? t('ACCOUNT.BTN_UPDATING') : t('ACCOUNT.BTN_UPDATE')}
                            </button>
                        </form>
                    </div>
                )}
            </div>
        </div>
    );
};

export default AccountProfile;
