import React, { useState, useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { User, Copy, CheckCircle2, Lock, ShieldAlert, Key } from 'lucide-react';
import toast from 'react-hot-toast';
import { useConfirm } from '../context/ConfirmContext';
import { authFetch } from '../utils/authFetch';
import BalanceConsumePreferences from './BalanceConsumePreferences';

const AccountProfile = () => {
    const confirm = useConfirm();
    const { t } = useTranslation();
    const [profile, setProfile] = useState(null);
    const [loading, setLoading] = useState(true);
    const [copied, setCopied] = useState(false);

    // Admin form state
    const [adminForm, setAdminForm] = useState({ username: '', password: '' });
    const [updatingAdmin, setUpdatingAdmin] = useState(false);

    useEffect(() => {
        const isAdmin = localStorage.getItem('daof_admin_unlocked') === '1';
        const userToken = localStorage.getItem('daof_token');
        if (!isAdmin && !userToken) {
            setLoading(false);
            return;
        }
        authFetch('/api/user/me')
            .then(data => {
                if (data.success) {
                    setProfile(data.data);
                    if (data.data.role === 'admin') {
                        setAdminForm({ username: data.data.username, password: '' });
                    }
                } else if (data.message_code === 'ERR_BANNED' || (data.message && data.message.includes('封禁'))) {
                    return; // 交给全站 App 拦截器处理
                }
                setLoading(false);
            })
            .catch(() => setLoading(false));
    }, []);

    const handleCopy = () => {
        if(profile && profile.token) {
            navigator.clipboard.writeText(profile.token);
            setCopied(true);
            setTimeout(() => setCopied(false), 2000);
        }
    };

    const handleAdminUpdate = async (e) => {
        e.preventDefault();
        if (!(await confirm(t('PROFILE.ADMIN_UPDATE_WARN', { username: adminForm.username })))) {
            return;
        }
        setUpdatingAdmin(true);
        try {
            const data = await authFetch('/api/admin/credentials', {
                method: 'PUT',
                body: adminForm,
            });
            if (data.success) {
                toast.success(t('PROFILE.SAVE_SUCCESS') + "\n" + t('PROFILE.NEW_ENTRY_POINT', { username: adminForm.username }), { duration: 5000 });
                localStorage.removeItem('daof_admin_unlocked');
                await fetch('/api/root/logout', { method: 'POST', credentials: 'include' }).catch(() => {});
                window.location.href = `/?sys=${adminForm.username}`;
            } else {
                toast.error((data.message_code ? t('API.' + data.message_code) : data.message));
            }
        } catch {
            toast.error(t('PROFILE.REQ_ERROR'));
        }
        setUpdatingAdmin(false);
    };

    if (loading) return <div className="text-on-surface-variant p-8 text-center ">{t('PROFILE.LOADING')}</div>;
    if (!profile) return <div className="bg-red-900/10 border border-red-900/30 text-red-400 p-6 rounded-xl text-center">{t('PROFILE.LOAD_FAILED')}</div>;

    return (
        <div className="fl-card p-8 mb-8">
            <div className="flex items-center gap-3 mb-6">
                <div className="w-10 h-10 rounded-overlay bg-blue-900/30 flex items-center justify-center">
                    <User size={20} className="text-primary" />
                </div>
                <h2 className="text-xl font-bold">{t('PROFILE.TITLE')}</h2>
            </div>

            <div className="space-y-6">
                {/* 身份 */}
                <div className="bg-surface-container-high border border-outline rounded-overlay p-6 relative overflow-hidden">
                   <div className="absolute top-0 right-0 w-32 h-32 bg-blue-500/5 rounded-full blur-2xl -mr-10 -mt-10 pointer-events-none"></div>
                   <div className="flex flex-col md:flex-row md:items-center justify-between gap-4 relative z-10">
                      <div>
                         <p className="text-sm text-on-surface-variant mb-1">{t('PROFILE.USERNAME_LABEL')}</p>
                         <p className="text-2xl font-bold font-mono tracking-tight text-on-surface">{profile.username}</p>
                      </div>
                      <div className="text-left md:text-right">
                         <p className="text-sm text-on-surface-variant mb-1">{t('PROFILE.ROLE_LABEL')}</p>
                         <div className="inline-flex items-center gap-2">
                             {profile.role === 'admin' 
                               ? <span className="bg-fuchsia-500/20 text-fuchsia-400 text-sm px-3 py-1 rounded-md font-medium border border-fuchsia-500/30">{t('PROFILE.ROLE_ADMIN')}</span>
                               : <span className="bg-gray-800 text-on-surface-variant text-sm px-3 py-1 rounded-md font-medium border border-gray-700">{t('PROFILE.ROLE_USER')}</span>
                             }
                         </div>
                      </div>
                   </div>
                </div>

                {/* 根 API 凭证不在此处展示——专属页面"API 令牌"已承担该职责，避免冗余 */}

                {profile.role !== 'admin' && (
                    <div className="bg-surface-container-high border border-outline rounded-xl p-6">
                        <div className="flex items-start gap-3 mb-3">
                            <span className="text-2xl">🎁</span>
                            <div>
                                <h3 className="text-base font-bold text-on-surface tracking-tight">我的拉新推荐链接</h3>
                                <p className="text-xs text-on-surface-variant mt-1">
                                    分享给朋友。每成功带来一个新用户，你和对方都将获得平台奖励额度（金额由管理员配置）。
                                </p>
                            </div>
                        </div>
                        <div className="flex items-stretch gap-2">
                            <input
                                type="text"
                                readOnly
                                value={`${window.location.origin}/?ref=${profile.username}`}
                                className="flex-1 h-10 bg-black/40 border border-outline-variant rounded-lg px-3 text-xs text-on-surface font-mono outline-none select-all"
                            />
                            <button
                                onClick={() => {
                                    navigator.clipboard.writeText(`${window.location.origin}/?ref=${profile.username}`);
                                    toast.success('推荐链接已复制');
                                }}
                                className="px-4 bg-primary text-on-primary rounded-lg text-sm font-medium hover:opacity-90 flex items-center gap-1"
                            >
                                <Copy size={14} />
                                复制
                            </button>
                        </div>
                    </div>
                )}

                {/* 通知偏好已迁移至独立的"通知偏好"二级 tab，避免账号档案与渠道偏好混杂 */}

                {/* fix UX 反馈（用户 2026-05-10）：余额消费控制原嵌在"我的产品"列表里错位 + 冗余，已挪到此处。
                    这是真正的账户配置项（开关 + 限额 + 重置窗口），与三段消费模型的第三段对应。 */}
                <BalanceConsumePreferences />

                {profile.role === 'admin' && (
                    <div className="bg-surface-container-high border border-red-900/30 rounded-xl p-6">
                        <div className="flex items-start gap-3 mb-6">
                            <ShieldAlert className="text-red-500 shrink-0 mt-0.5" size={20} />
                            <div>
                                <h3 className="text-lg font-bold text-on-surface tracking-tight">{t('PROFILE.ROOT_OVERRIDE_TITLE')}</h3>
                                <p className="text-sm text-on-surface-variant mt-1 leading-relaxed">
                                    {t('PROFILE.ROOT_OVERRIDE_DESC')}
                                </p>
                            </div>
                        </div>

                        <form onSubmit={handleAdminUpdate} className="space-y-4">
                            <div className="flex flex-col md:flex-row gap-4">
                                <div className="flex-1 space-y-1.5">
                                    <label htmlFor="profile-admin-username" className="text-xs font-semibold text-on-surface-variant">{t('PROFILE.NEW_USERNAME_LABEL')}</label>
                                    <input
                                        id="profile-admin-username"
                                        type="text" required
                                        value={adminForm.username}
                                        onChange={e => setAdminForm({...adminForm, username: e.target.value})}
                                        className="w-full h-11 bg-black/50 border border-outline rounded-lg px-4 text-sm text-on-surface focus:border-red-500 focus:bg-[#1a1515] outline-none "
                                        placeholder={t('PROFILE.NEW_USERNAME_PLACEHOLDER')}
                                    />
                                </div>
                                <div className="flex-1 space-y-1.5">
                                    <label htmlFor="profile-admin-password" className="text-xs font-semibold text-on-surface-variant">{t('PROFILE.NEW_PASSWORD_LABEL')}</label>
                                    <input
                                        id="profile-admin-password"
                                        type="password" required autoComplete="new-password"
                                        value={adminForm.password}
                                        onChange={e => setAdminForm({...adminForm, password: e.target.value})}
                                        className="w-full h-11 bg-black/50 border border-outline rounded-lg px-4 text-sm text-on-surface focus:border-red-500 focus:bg-[#1a1515] outline-none "
                                        placeholder={t('PROFILE.NEW_PASSWORD_PLACEHOLDER')}
                                    />
                                </div>
                            </div>
                            <button 
                                type="submit" 
                                disabled={updatingAdmin}
                                className="h-11 px-6 bg-red-600 hover:bg-red-500 disabled:opacity-50 disabled:cursor-not-allowed text-on-surface font-medium rounded-lg flex items-center justify-center gap-2  mt-4"
                            >
                                <Lock size={18} />
                                {updatingAdmin ? t('PROFILE.BTN_UPDATING') : t('PROFILE.BTN_UPDATE')}
                            </button>
                        </form>
                    </div>
                )}
            </div>
        </div>
    );
};

export default AccountProfile;
