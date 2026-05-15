import React, { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate } from 'react-router-dom';
import { ShieldAlert } from 'lucide-react';
import { useAuth } from '../context/AuthContext';
import { useCurrency } from '../context/CurrencyContext';
import { authFetch, isLoggedIn } from '../utils/authFetch';
import { formatCompactNumber, formatRelativeTime } from '../utils/format';
import { logger } from '../utils/logger';
import MySubscriptions from './MySubscriptions';
import UpgradePage from './UpgradePage';
import { StatusBadge } from './ui';

/**
 * Dashboard: consolidated user console.
 *
 * Three role branches:
 *   - admin: one-line admin banner
 *   - signed-in user: subscriptions plus stat strip
 *   - signed-out visitor: sign-in banner plus public packages
 *
 * Model lists, recent calls, and marketing hero content live elsewhere.
 */

const Dashboard = () => {
  const { t, i18n } = useTranslation();
  const { isAdmin, isAuthenticated, openLogin } = useAuth();
  const { formatCurrency } = useCurrency();
  const navigate = useNavigate();

  const [me, setMe] = useState(null);
  const [recentLogs, setRecentLogs] = useState([]);
  const [meLoading, setMeLoading] = useState(() => isAuthenticated && isLoggedIn() && !isAdmin);

  useEffect(() => {
    if (!isAuthenticated || !isLoggedIn() || isAdmin) {
      setMeLoading(false);
      return undefined;
    }
    setMeLoading(true);
    const ctrl = new AbortController();
    const swallow = (err) => {
      if (err?.name === 'AbortError') return null;
      logger.warn('[dashboard] fetch failed', err);
      return null;
    };
    Promise.all([
      authFetch('/api/user/me', { signal: ctrl.signal }).catch(swallow),
      authFetch('/api/logs?page=1&limit=8', { signal: ctrl.signal }).catch(swallow),
    ]).then(([meRes, logsRes]) => {
      if (ctrl.signal.aborted) return;
      if (meRes?.success) setMe(meRes.data);
      if (logsRes?.success) setRecentLogs(logsRes.data?.items || logsRes.data || []);
      setMeLoading(false);
    });
    return () => ctrl.abort();
  }, [isAuthenticated, isAdmin]);

  // Admin
  if (isAdmin) {
    return (
      <div className="space-y-6 py-6">
        <section className="fl-card flex items-center gap-3 px-4 py-3">
          <ShieldAlert size={16} className="text-on-surface-variant shrink-0" />
          <span className="text-sm text-on-surface-variant">
            {t('DASH.ADMIN_HINT', '当前为管理员模式，可前往管理控制台查看渠道、用户与计费')}
          </span>
          <button
            type="button"
            onClick={() => navigate('/admin')}
            className="ml-auto text-sm font-medium text-primary hover:underline"
          >
            {t('DASH.ADMIN_ENTER', '进入控制台')}
          </button>
        </section>
      </div>
    );
  }

  // Signed-out visitors see the sign-in prompt plus public package pricing.
  if (!isAuthenticated) {
    return (
      <div className="space-y-6 py-6">
        <section className="fl-card flex items-center gap-3 px-4 py-3">
          <span className="text-sm text-on-surface-variant">
            {t('DASH.SIGN_IN_HINT', '登录后可查看您的订阅、用量与账单')}
          </span>
          <button
            type="button"
            onClick={openLogin}
            className="ml-auto text-sm font-medium text-primary hover:underline"
          >
            {t('DASH.SIGN_IN_ACTION', '登录')}
          </button>
        </section>
        <UpgradePage />
      </div>
    );
  }

  // Signed-in user
  return (
    <div className="space-y-6 py-6">
      {meLoading ? (
        <StatStripSkeleton />
      ) : (
        <StatStrip me={me} recentLogs={recentLogs} formatCurrency={formatCurrency} i18n={i18n} t={t} />
      )}
      <MySubscriptions isAuthenticated embedded />
    </div>
  );
};

// Stat strip: balance, recent requests, token usage, and last call.
//
// The request and token numbers are recent snapshots, not full-window aggregates.
const StatStrip = ({ me, recentLogs, formatCurrency, i18n, t }) => {
  const totalReqs = recentLogs.length;
  const totalTokens = recentLogs.reduce(
    (s, l) => s + (l.prompt_tokens || 0) + (l.completion_tokens || 0),
    0
  );
  const lastTime = recentLogs[0]?.created_at;
  const lastRel = lastTime ? formatRelativeTime(lastTime, i18n.resolvedLanguage || i18n.language) : '—';
  const snapshotHint = totalReqs > 0
    ? t('DASH.STAT_SNAPSHOT_N', { n: totalReqs, defaultValue: '近 {{n}} 条快照' })
    : t('DASH.STAT_NO_DATA', '暂无数据');

  return (
    <section>
      <div className="fl-card grid grid-cols-2 md:grid-cols-4 divide-y md:divide-y-0 md:divide-x divide-outline-variant/30 overflow-hidden">
        <Stat
          label={t('DASH.STAT_BALANCE', '账户余额')}
          value={me ? formatCurrency(me.quota ?? 0, 2) : '—'}
          hint={me?.username || ''}
          prominent
        />
        <Stat
          label={t('DASH.STAT_REQUESTS', '最近请求')}
          value={totalReqs.toLocaleString()}
          hint={snapshotHint}
        />
        <Stat
          label={t('DASH.STAT_TOKENS', 'Token 用量')}
          value={formatCompactNumber(totalTokens)}
          hint={snapshotHint}
        />
        <Stat
          label={t('DASH.STAT_LAST', '上次调用')}
          value={lastRel}
          hint={lastTime ? '' : t('DASH.STAT_NO_DATA', '暂无数据')}
        />
      </div>
      <div className="text-[11px] text-on-surface-variant mt-2 px-1">
        <Link to="/stats" className="hover:text-primary hover:underline">
          {t('DASH.STAT_FULL_LINK', '查看完整用量统计 (24h / 7d / 30d) →')}
        </Link>
      </div>
    </section>
  );
};

const Stat = ({ label, value, hint, prominent = false }) => (
  <div className="px-5 py-4 min-w-0">
    <div className="text-[10px] uppercase tracking-[0.08em] text-on-surface-variant font-semibold">
      {label}
    </div>
    <div
      className={`font-bold text-on-surface tabular-nums tracking-tight mt-1.5 truncate ${
        prominent ? 'text-3xl' : 'text-2xl'
      }`}
    >
      {value}
    </div>
    <div className="text-[11px] text-on-surface-variant mt-1 truncate">{hint || ' '}</div>
  </div>
);

const StatStripSkeleton = () => (
  <section
    className="fl-card grid grid-cols-2 md:grid-cols-4 divide-y md:divide-y-0 md:divide-x divide-outline-variant/30 overflow-hidden"
    aria-hidden="true"
  >
    {[0, 1, 2, 3].map(i => (
      <div key={i} className="px-5 py-4">
        <div className="h-2.5 w-16 rounded-control bg-on-surface/[0.08] animate-pulse" />
        <div className={`mt-2 h-7 ${i === 0 ? 'w-28' : 'w-20'} rounded-control bg-on-surface/[0.10] animate-pulse`} />
        <div className="mt-2 h-2 w-20 rounded-control bg-on-surface/[0.06] animate-pulse" />
      </div>
    ))}
  </section>
);
// Helpers

export default Dashboard;
