import React, { useState, useEffect, useMemo } from 'react';
import { useTranslation } from 'react-i18next';
// IA audit M-IA1 fix: dropped Lock + ShieldAlert + useConfirm — the admin
// credentials form that used them lived in two places (here and
// pages/admin/system/GeneralAdminPage). AccountProfile is a user-facing
// page; admin credential rotation belongs only at /admin/general so we
// don't have two ways to wipe the only-admin account.
import { User, Copy, Gift, UserPlus } from 'lucide-react';
import toast from 'react-hot-toast';
import { authFetch, readAuthState } from '../utils/authFetch';
import { isPageCacheFresh, readPageCache, writePageCache } from '../utils/pageCache';
import UserEmailBinding from './UserEmailBinding';
import UserLinkedAccounts from './UserLinkedAccounts';

const PROFILE_CACHE_TTL_MS = 30000;
const MICRO_PER_USD = 1_000_000;
const BANNED_MARKER = '\u5c01\u7981';
const getProfileCacheKey = () => {
    const { isAdmin, userToken } = readAuthState();
    return `profile:${isAdmin ? 'admin' : userToken || 'guest'}`;
};

const formatMicroUSD = (microValue) => {
    if (microValue == null || microValue === '') return '—';
    const micro = Number.parseInt(microValue, 10);
    if (!Number.isFinite(micro) || micro < 0) return '—';
    const fractionDigits = micro % MICRO_PER_USD === 0 ? 0 : 2;
    return new Intl.NumberFormat(undefined, {
        style: 'currency',
        currency: 'USD',
        minimumFractionDigits: fractionDigits,
        maximumFractionDigits: 6,
    }).format(micro / MICRO_PER_USD);
};

const formatBpsPercent = (bpsValue) => {
    const bps = Number.parseInt(bpsValue || '0', 10);
    if (!Number.isFinite(bps) || bps <= 0) return '0%';
    const pct = bps / 100;
    return `${Number.isInteger(pct) ? pct.toFixed(0) : pct.toFixed(2).replace(/0+$/, '').replace(/\.$/, '')}%`;
};

const formatWindowDays = (secondsValue) => {
    const seconds = Number.parseInt(secondsValue || '0', 10);
    if (!Number.isFinite(seconds) || seconds <= 0) return 0;
    return Math.max(1, Math.round(seconds / 86400));
};

const AccountProfile = () => {
    const { t } = useTranslation();
    const profileCacheKey = useMemo(getProfileCacheKey, []);
    const cachedProfile = readPageCache(profileCacheKey);
    const [profile, setProfile] = useState(() => cachedProfile);
    const [loading, setLoading] = useState(() => !cachedProfile);
    const [publicConfig, setPublicConfig] = useState(null);

    useEffect(() => {
        let alive = true;
        fetch('/api/public-config', { credentials: 'same-origin' })
            .then(res => res.json())
            .then(data => {
                if (alive && data?.success) setPublicConfig(data);
            })
            .catch(() => {});
        return () => { alive = false; };
    }, []);

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
            setLoading(false);
            if (isPageCacheFresh(profileCacheKey, PROFILE_CACHE_TTL_MS)) return;
        }
        authFetch('/api/user/me')
            .then(data => {
                if (data.success) {
                    writePageCache(profileCacheKey, data.data);
                    setProfile(data.data);
                } else if (data.message_code === 'ERR_BANNED' || (data.message && data.message.includes(BANNED_MARKER))) {
                    return;
                }
                setLoading(false);
            })
            .catch(() => setLoading(false));
    }, [profileCacheKey]);

    // M-IA1 removed: handleAdminUpdate + adminForm state + the JSX block
    // below. Admin credentials are owned by /admin/general now; this
    // component is the user-facing AccountProfile only.

    if (loading) return <div className="text-on-surface-variant p-8 text-center ">{t('ACCOUNT.LOADING')}</div>;
    if (!profile) return <div className="bg-error/10 border border-error/30 text-error p-6 rounded-overlay text-center">{t('ACCOUNT.LOAD_FAILED')}</div>;
    const referralIncentives = publicConfig?.referral_incentives || {};
    const referralRewardWindowDays = formatWindowDays(referralIncentives.reward_window_seconds);
    const referralBaseUrl = String(publicConfig?.server_address || window.location.origin).trim().replace(/\/+$/, '') || window.location.origin;
    const referralUrl = `${referralBaseUrl}/?ref=${encodeURIComponent(profile.username)}`;

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



                {profile.role !== 'admin' && <UserEmailBinding />}

                {profile.role !== 'admin' && <UserLinkedAccounts />}

                {profile.role !== 'admin' && (
                    <div className="bg-surface-container-high border border-outline rounded-overlay p-6">
                        <div className="flex items-start gap-3 mb-3">
                            <div className="w-9 h-9 rounded-control bg-primary/15 text-primary flex items-center justify-center shrink-0">
                                <Gift size={18} />
                            </div>
                            <div>
                                <h3 className="text-base font-bold text-on-surface tracking-tight">
                                    {t('ACCOUNT.REFERRAL_TITLE', '我的拉新推荐链接')}
                                </h3>
                                <p className="text-xs text-on-surface-variant mt-1">
                                    {t('ACCOUNT.REFERRAL_DESC', '分享给朋友。每成功带来一个新用户，你和对方都将获得平台奖励额度。')}
                                </p>
                            </div>
                        </div>
                        <div className="grid grid-cols-1 md:grid-cols-3 gap-2 mb-3">
                            <div className="rounded-control border border-outline-variant/60 bg-black/20 px-3 py-2 flex items-center justify-between gap-3">
                                <div className="min-w-0">
                                    <div className="text-xs text-on-surface-variant">{t('ACCOUNT.REFERRER_REWARD_LABEL', '你获得')}</div>
                                    <div className="text-[11px] text-outline mt-0.5 truncate">{t('ACCOUNT.REFERRER_REWARD_HINT', '好友通过链接注册成功后发放')}</div>
                                </div>
                                <div className="text-sm font-semibold font-mono text-primary shrink-0">
                                    {formatMicroUSD(referralIncentives.referrer_bonus_micro_usd)}
                                </div>
                            </div>
                            <div className="rounded-control border border-outline-variant/60 bg-black/20 px-3 py-2 flex items-center justify-between gap-3">
                                <div className="min-w-0">
                                    <div className="text-xs text-on-surface-variant">{t('ACCOUNT.REFEREE_REWARD_LABEL', '好友获得')}</div>
                                    <div className="text-[11px] text-outline mt-0.5 truncate">
                                        {t('ACCOUNT.REFEREE_REWARD_HINT', '在注册基础奖励之外叠加')}
                                    </div>
                                </div>
                                <div className="text-sm font-semibold font-mono text-primary shrink-0">
                                    {formatMicroUSD(referralIncentives.referee_bonus_micro_usd)}
                                </div>
                            </div>
                            <div className="rounded-control border border-outline-variant/60 bg-black/20 px-3 py-2 flex items-center justify-between gap-3">
                                <div className="min-w-0">
                                    <div className="text-xs text-on-surface-variant">{t('ACCOUNT.REFERRAL_SPEND_REWARD_LABEL', '好友消费返佣')}</div>
                                    <div className="text-[11px] text-outline mt-0.5 truncate">
                                        {t('ACCOUNT.REFERRAL_SPEND_REWARD_HINT', {
                                            days: referralRewardWindowDays,
                                            defaultValue: '注册后 {{days}} 天内的自充消费',
                                        })}
                                    </div>
                                </div>
                                <div className="text-sm font-semibold font-mono text-primary shrink-0">
                                    {formatBpsPercent(referralIncentives.paid_spend_reward_bps)}
                                </div>
                            </div>
                        </div>
                        <div className="mb-3 inline-flex items-center gap-1.5 text-[11px] text-on-surface-variant">
                            <UserPlus size={12} />
                            {t('ACCOUNT.SIGNUP_REWARD_LABEL', {
                                amount: formatMicroUSD(referralIncentives.signup_bonus_micro_usd),
                                defaultValue: '新用户注册基础奖励：{{amount}}',
                            })}
                        </div>
                        <div className="flex items-stretch gap-2">
                            <input
                                type="text"
                                readOnly
                                value={referralUrl}
                                className="flex-1 h-10 bg-black/40 border border-outline-variant rounded-control px-3 text-xs text-on-surface font-mono outline-none select-all"
                            />
                            <button
                                onClick={() => {
                                    navigator.clipboard.writeText(referralUrl);
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




            </div>
        </div>
    );
};

export default AccountProfile;
