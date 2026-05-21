import React, { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate } from 'react-router-dom';
import { ShieldAlert, ArrowRight, Wallet, Activity, BarChart3, Clock, Sparkles } from 'lucide-react';
import { useAuth } from '../context/AuthContext';
import { useCurrency } from '../context/CurrencyContext';
import { authFetch, isLoggedIn } from '../utils/authFetch';
import { formatCompactNumber, formatRelativeTime } from '../utils/format';
import { logger } from '../utils/logger';
import MySubscriptions from './MySubscriptions';
import UpgradePage from './UpgradePage';

/**
 * Dashboard — Sprint J-3 batch 3 (真激进版).
 *
 * 三个角色分支：
 *   - admin：简短引导栏 + 进入控制台
 *   - 已登录用户：hero (44px) + bento stat grid（余额卡占 6 列大卡 +
 *     3 张小 stat 各 2 列）+ 订阅
 *   - 未登录访客：hero + CTA + UpgradePage
 *
 * 视觉决策：
 *   - .page-title-hero (44px) 替代旧 .page-title (32px)，hero 有真权重
 *   - bento 而不是 4-equal grid（余额是主角，应该最显眼）
 *   - .stat-spotlight 给余额卡 1.5% accent 染色 + 顶部 highlight
 *   - 副标 hint 用 hero-eyebrow（accent 色 + dot）替代普通灰文本
 */

const Dashboard = () => {
  const { t, i18n } = useTranslation();
  // IA audit M-D2 fix: AuthContext 是 /api/user/me 单源 — 它已经在挂载 +
  // 30s 轮询 + 'user-profile-refresh' 事件三处拉 profile，Dashboard 不再
  // 自己 fetch，直接读 context.profile。/api/logs 是独立 endpoint 保留。
  const { isAdmin, isAuthenticated, openLogin, profile: me } = useAuth();
  const { formatCurrency } = useCurrency();
  const navigate = useNavigate();

  const [recentLogs, setRecentLogs] = useState([]);
  const meLoading = isAuthenticated && isLoggedIn() && !isAdmin && me === null;

  useEffect(() => {
    if (!isAuthenticated || !isLoggedIn() || isAdmin) {
      return undefined;
    }
    const ctrl = new AbortController();
    (async () => {
      try {
        const logsRes = await authFetch('/api/logs?page=1&limit=8', { signal: ctrl.signal });
        if (ctrl.signal.aborted) return;
        if (logsRes?.success) {
          const raw = logsRes.data?.logs ?? logsRes.data?.items ?? logsRes.data;
          setRecentLogs(Array.isArray(raw) ? raw : []);
        }
      } catch (err) {
        if (err?.name !== 'AbortError') {
          logger.warn('[dashboard] logs fetch failed', err);
        }
      }
    })();
    return () => ctrl.abort();
  }, [isAuthenticated, isAdmin]);

  // Admin
  if (isAdmin) {
    return (
      <div className="space-y-6 py-6">
        <section className="card row gap-3" style={{ padding: '14px 18px' }}>
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
      <div className="space-y-10">
        <section className="hero">
          <span className="hero-eyebrow">
            <span className="dot dot-info" aria-hidden="true" />
            {t('DASH.GUEST_EYEBROW', 'DAOF-CPA 控制台')}
          </span>
          <div className="hero-row">
            <div className="min-w-0">
              <h1 className="page-title-hero">
                {t('DASH.GUEST_TITLE', '探索 DAOF-CPA 模型矩阵')}
              </h1>
              <p className="page-subtitle">
                {t('DASH.GUEST_SUBTITLE', '一站式聚合 Claude / Codex / Gemini / xAI 等主流模型，按你的预算和容量挑选合适的套餐。')}
              </p>
            </div>
            <button
              type="button"
              onClick={openLogin}
              className="btn btn-primary btn-lg"
            >
              {t('DASH.GUEST_CTA', '登录开始')}
              <ArrowRight size={15} strokeWidth={2.2} />
            </button>
          </div>
        </section>
        <UpgradePage />
      </div>
    );
  }

  // Signed-in user
  return (
    <div className="space-y-10">
      <DashboardHero me={me} t={t} />
      {meLoading ? (
        <BentoSkeleton />
      ) : (
        <BentoGrid me={me} recentLogs={recentLogs} formatCurrency={formatCurrency} i18n={i18n} t={t} />
      )}
      <MySubscriptions isAuthenticated embedded />
    </div>
  );
};

/* ─────── Hero greeting ─────── */
const DashboardHero = ({ me, t }) => {
  const hr = new Date().getHours();
  const greetKey =
    hr < 5  ? 'DASH.HERO_GREET_NIGHT' :
    hr < 12 ? 'DASH.HERO_GREET_MORNING' :
    hr < 18 ? 'DASH.HERO_GREET_AFTERNOON' :
              'DASH.HERO_GREET_EVENING';
  const greetFallback =
    hr < 5  ? '夜深了' :
    hr < 12 ? '早上好' :
    hr < 18 ? '下午好' :
              '晚上好';
  const name = me?.username || '';
  return (
    <section className="hero">
      <span className="hero-eyebrow">
        <span className="dot dot-info" aria-hidden="true" />
        {t('DASH.HERO_EYEBROW', '账户全景')}
      </span>
      <div className="hero-row">
        <div className="min-w-0">
          <h1 className="page-title-hero">
            {t(greetKey, greetFallback)}{name ? `，${name}` : ''}
          </h1>
          <p className="page-subtitle">
            {t('DASH.HERO_SUBTITLE', '这里是你的账户全景：余额、近期用量与订阅状态一目了然。')}
          </p>
        </div>
      </div>
    </section>
  );
};

/* ─────── Bento grid ───────
 *
 * 12-col grid：
 *   - SpotlightBalance: span 6（主角，余额 + 充值 CTA + 健康度 chip）
 *   - 3 张 stat: span 2 each（请求 / token / 上次调用）
 *   总 = 6 + 2 + 2 + 2 = 12
 *
 * 中等屏幕 (md) 退化为 2 列；移动端单列。
 */
const BentoGrid = ({ me, recentLogs, formatCurrency, i18n, t }) => {
  const totalReqs = recentLogs.length;
  const totalTokens = recentLogs.reduce(
    (s, l) => s + (l.prompt_tokens || 0) + (l.completion_tokens || 0),
    0
  );
  const lastTime = recentLogs[0]?.created_at;
  const lastRel = lastTime ? formatRelativeTime(lastTime, i18n.resolvedLanguage || i18n.language) : '—';

  const balance = Number(me?.quota ?? 0);
  const balanceTone =
    balance > 5     ? 'success' :
    balance > 0     ? 'warning' :
                      'error';
  const balanceChipKey =
    balanceTone === 'success' ? 'DASH.STAT_BALANCE_OK'    :
    balanceTone === 'warning' ? 'DASH.STAT_BALANCE_LOW'   :
                                'DASH.STAT_BALANCE_EMPTY';
  const balanceChipFallback =
    balanceTone === 'success' ? '余额充足' :
    balanceTone === 'warning' ? '余额偏低' :
                                '余额不足';
  const isEmpty = balance <= 0;

  return (
    <section>
      <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-12 gap-4">
        {/* Spotlight: 余额主角卡 */}
        <div className="md:col-span-2 xl:col-span-6 stat-spotlight">
          <div className="stat-head">
            <div className="flex items-center gap-2.5 min-w-0">
              <span className={`dot dot-${balanceTone}`} aria-hidden="true" />
              <span className="stat-label">{t('DASH.STAT_BALANCE', '账户余额')}</span>
            </div>
            <Wallet size={18} className="text-on-surface-variant opacity-70 shrink-0" aria-hidden="true" />
          </div>
          <div className="flex items-baseline gap-2 mt-2">
            <span className="stat-value lg truncate" title={me ? formatCurrency(balance, 2) : '—'}>
              {me ? formatCurrency(balance, 2) : '—'}
            </span>
          </div>
          <div className="flex items-center justify-between gap-2 mt-auto pt-2">
            <span className={`chip chip-${balanceTone}`}>
              {balanceTone === 'success' && <Sparkles size={10} strokeWidth={2.5} aria-hidden="true" />}
              {t(balanceChipKey, balanceChipFallback)}
            </span>
            <Link
              to="/topup"
              className={`inline-flex items-center gap-1.5 text-xs font-semibold transition
                ${isEmpty ? 'text-primary hover:opacity-80' : 'text-on-surface-variant hover:text-primary'}`}
            >
              {isEmpty
                ? t('DASH.STAT_BALANCE_GO_TOPUP', '余额不足，去充值')
                : t('DASH.STAT_BALANCE_TOPUP_CTA', '充值')}
              <ArrowRight size={12} strokeWidth={2.4} />
            </Link>
          </div>
        </div>

        {/* 三张小 stat 卡 — 每张 2 列 */}
        <CompactStat
          icon={Activity}
          label={t('DASH.STAT_REQUESTS', '最近请求')}
          value={totalReqs.toLocaleString()}
          dotTone={totalReqs > 0 ? 'info' : 'muted'}
          hint={totalReqs > 0
            ? t('DASH.STAT_SNAPSHOT_N', { n: totalReqs, defaultValue: '近 {{n}} 条快照' })
            : t('DASH.STAT_NO_DATA', '暂无数据')}
        />
        <CompactStat
          icon={BarChart3}
          label={t('DASH.STAT_TOKENS', 'Token 用量')}
          value={formatCompactNumber(totalTokens)}
          dotTone={totalTokens > 0 ? 'info' : 'muted'}
          hint={totalTokens > 0
            ? t('DASH.STAT_SNAPSHOT_N', { n: totalReqs, defaultValue: '近 {{n}} 条快照' })
            : t('DASH.STAT_NO_DATA', '暂无数据')}
        />
        <CompactStat
          icon={Clock}
          label={t('DASH.STAT_LAST', '上次调用')}
          value={lastRel}
          dotTone={lastTime ? 'success' : 'muted'}
          hint={lastTime ? '' : t('DASH.STAT_NO_DATA', '暂无数据')}
        />
      </div>

      <div className="text-[12px] text-on-surface-variant mt-4 px-1">
        <Link to="/stats" className="hover:text-primary hover:underline inline-flex items-center gap-1.5">
          {t('DASH.STAT_FULL_LINK', '查看完整用量统计 (24h / 7d / 30d) →')}
        </Link>
      </div>
    </section>
  );
};

const CompactStat = ({ icon: Icon, label, value, dotTone, hint }) => (
  <div className="xl:col-span-2 stat">
    <div className="stat-head">
      <div className="flex items-center gap-2 min-w-0">
        <span className={`dot dot-${dotTone || 'muted'}`} aria-hidden="true" />
        <span className="stat-label truncate">{label}</span>
      </div>
      {Icon && <Icon size={14} className="text-on-surface-variant opacity-70 shrink-0" aria-hidden="true" />}
    </div>
    <div className="stat-value truncate" title={value}>{value}</div>
    <div className="stat-hint truncate">{hint || ' '}</div>
  </div>
);

const BentoSkeleton = () => (
  <section aria-hidden="true">
    <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-12 gap-4">
      <div className="md:col-span-2 xl:col-span-6 stat-spotlight">
        <div className="h-3 w-24 rounded-control bg-on-surface/[0.08] animate-pulse" />
        <div className="h-12 w-44 rounded-control bg-on-surface/[0.10] animate-pulse mt-2" />
        <div className="h-4 w-32 rounded-control bg-on-surface/[0.06] animate-pulse mt-auto" />
      </div>
      {[0, 1, 2].map(i => (
        <div key={i} className="xl:col-span-2 stat">
          <div className="h-2.5 w-20 rounded-control bg-on-surface/[0.08] animate-pulse" />
          <div className="h-7 w-16 rounded-control bg-on-surface/[0.10] animate-pulse" />
          <div className="h-3 w-24 rounded-control bg-on-surface/[0.06] animate-pulse" />
        </div>
      ))}
    </div>
  </section>
);

export default Dashboard;
