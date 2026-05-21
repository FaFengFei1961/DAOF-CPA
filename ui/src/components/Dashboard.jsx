import React, { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate } from 'react-router-dom';
import { ShieldAlert, ArrowRight, Wallet, Activity, BarChart3, Clock } from 'lucide-react';
import { useAuth } from '../context/AuthContext';
import { useCurrency } from '../context/CurrencyContext';
import { authFetch, isLoggedIn } from '../utils/authFetch';
import { formatCompactNumber, formatRelativeTime } from '../utils/format';
import { logger } from '../utils/logger';
import MySubscriptions from './MySubscriptions';
import UpgradePage from './UpgradePage';

/**
 * Dashboard — Sprint J-3 真重设计版本。
 *
 * 三个角色分支：
 *   - admin：admin 简短引导栏
 *   - 已登录普通用户：hero greeting + stat grid（带 trend chip + dot 状态）+ 订阅
 *   - 未登录访客：登录提示 + 公开套餐展示
 *
 * Stat 卡片用新 `.stat` 原语 + dot/chip 表达健康度，而不是单一灰色数字。
 */

const Dashboard = () => {
  const { t, i18n } = useTranslation();
  // IA audit M-D2 fix: AuthContext 是 /api/user/me 单源 — 它已经在挂载时
  // + 30s 轮询 + 'user-profile-refresh' 事件三处拉 profile。Dashboard 不再
  // 自己 fetch，直接读 context.profile。/api/logs 是独立 endpoint 保留。
  const { isAdmin, isAuthenticated, openLogin, profile: me } = useAuth();
  const { formatCurrency } = useCurrency();
  const navigate = useNavigate();

  const [recentLogs, setRecentLogs] = useState([]);
  // me 来自 AuthContext，初始可能是 null（轮询第一次还没到）→ 用骨架屏
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
        // 后端 /api/logs 返回 { data: { logs: [...], total, page, limit } }，
        // 字段名是 logs 不是 items。Array.isArray 兜底防御非数组响应（避免 reduce 崩）。
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
        <section className="card row gap-3" style={{ padding: '12px 16px' }}>
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
      <div className="space-y-8 py-6">
        <section className="hero">
          <div className="hero-row">
            <div>
              <h1 className="page-title">
                {t('DASH.GUEST_TITLE', '探索 DAOF-CPA 模型矩阵')}
              </h1>
              <p className="page-subtitle">
                {t('DASH.GUEST_SUBTITLE', '一站式聚合 Claude / Codex / Gemini / xAI 等主流模型，按你的预算和容量挑选合适的套餐。')}
              </p>
            </div>
            <button
              type="button"
              onClick={openLogin}
              className="btn btn-primary btn-md"
            >
              {t('DASH.GUEST_CTA', '登录开始')}
              <ArrowRight size={14} strokeWidth={2.2} />
            </button>
          </div>
        </section>
        <UpgradePage />
      </div>
    );
  }

  // Signed-in user
  return (
    <div className="space-y-8 py-6">
      <DashboardHero me={me} t={t} />
      {meLoading ? (
        <StatGridSkeleton />
      ) : (
        <StatGrid me={me} recentLogs={recentLogs} formatCurrency={formatCurrency} i18n={i18n} t={t} />
      )}
      <MySubscriptions isAuthenticated embedded />
    </div>
  );
};

/* ─────── Hero greeting ─────── */
const DashboardHero = ({ me, t }) => {
  // 简单的时段问候 — 不靠后端，就走客户端时钟，足够日常感
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
      <div className="hero-row">
        <div className="min-w-0">
          <h1 className="page-title">
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

/* ─────── Stat grid ─────── */
//
// 4 张统计卡：余额（带 dot 状态 + 充值 chip）/ 最近请求（活动 chip）/
// Token 用量（累计 chip）/ 上次调用（时间 chip）。
//
// 数据：me.quota / recentLogs。recentLogs 是 /api/logs?page=1&limit=8 的快照，
// 不是窗口聚合（这一点在 DASH.STAT_SNAPSHOT_N 文案里有提示）。
//
const StatGrid = ({ me, recentLogs, formatCurrency, i18n, t }) => {
  const totalReqs = recentLogs.length;
  const totalTokens = recentLogs.reduce(
    (s, l) => s + (l.prompt_tokens || 0) + (l.completion_tokens || 0),
    0
  );
  const lastTime = recentLogs[0]?.created_at;
  const lastRel = lastTime ? formatRelativeTime(lastTime, i18n.resolvedLanguage || i18n.language) : '—';

  const balance = Number(me?.quota ?? 0);
  // 余额健康度：>$5 健康；0.01-5 警告；<=0 异常
  const balanceTone =
    balance > 5     ? 'success' :
    balance > 0     ? 'warning' :
                      'error';
  const balanceChipKey =
    balanceTone === 'success' ? 'DASH.STAT_BALANCE_OK'       :
    balanceTone === 'warning' ? 'DASH.STAT_BALANCE_LOW'      :
                                'DASH.STAT_BALANCE_EMPTY';
  const balanceChipFallback =
    balanceTone === 'success' ? '余额充足' :
    balanceTone === 'warning' ? '余额偏低' :
                                '余额不足';

  return (
    <section>
      <div className="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-4 gap-3">
        <StatCard
          icon={Wallet}
          label={t('DASH.STAT_BALANCE', '账户余额')}
          value={me ? formatCurrency(balance, 2) : '—'}
          dotTone={balanceTone}
          chip={{
            label: t(balanceChipKey, balanceChipFallback),
            tone: balanceTone,
          }}
          // 余额不足直接给一个明显的"去充值"入口
          footer={balance <= 0 ? (
            <Link to="/topup" className="text-xs text-primary font-medium hover:underline inline-flex items-center gap-1">
              {t('DASH.STAT_BALANCE_GO_TOPUP', '余额不足，去充值')}
              <ArrowRight size={11} strokeWidth={2.4} />
            </Link>
          ) : (
            <Link to="/topup" className="text-xs text-on-surface-variant hover:text-primary inline-flex items-center gap-1">
              {t('DASH.STAT_BALANCE_TOPUP_CTA', '充值')}
              <ArrowRight size={11} strokeWidth={2.4} />
            </Link>
          )}
        />
        <StatCard
          icon={Activity}
          label={t('DASH.STAT_REQUESTS', '最近请求')}
          value={totalReqs.toLocaleString()}
          dotTone={totalReqs > 0 ? 'info' : 'muted'}
          chip={totalReqs > 0
            ? { label: t('DASH.STAT_SNAPSHOT_N', { n: totalReqs, defaultValue: '近 {{n}} 条快照' }), tone: 'accent' }
            : { label: t('DASH.STAT_NO_DATA', '暂无数据'), tone: 'default' }}
        />
        <StatCard
          icon={BarChart3}
          label={t('DASH.STAT_TOKENS', 'Token 用量')}
          value={formatCompactNumber(totalTokens)}
          dotTone={totalTokens > 0 ? 'info' : 'muted'}
          chip={totalTokens > 0
            ? { label: t('DASH.STAT_SNAPSHOT_N', { n: totalReqs, defaultValue: '近 {{n}} 条快照' }), tone: 'accent' }
            : { label: t('DASH.STAT_NO_DATA', '暂无数据'), tone: 'default' }}
        />
        <StatCard
          icon={Clock}
          label={t('DASH.STAT_LAST', '上次调用')}
          value={lastRel}
          dotTone={lastTime ? 'success' : 'muted'}
          chip={lastTime
            ? null
            : { label: t('DASH.STAT_NO_DATA', '暂无数据'), tone: 'default' }}
        />
      </div>
      <div className="text-[11px] text-on-surface-variant mt-3 px-1">
        <Link to="/stats" className="hover:text-primary hover:underline inline-flex items-center gap-1">
          {t('DASH.STAT_FULL_LINK', '查看完整用量统计 (24h / 7d / 30d) →')}
        </Link>
      </div>
    </section>
  );
};

const StatCard = ({ icon: Icon, label, value, dotTone, chip, footer }) => (
  <div className="stat">
    <div className="stat-head">
      <div className="flex items-center gap-2 min-w-0">
        <span className={`dot dot-${dotTone || 'muted'}`} aria-hidden="true" />
        <span className="stat-label truncate">{label}</span>
      </div>
      {Icon && <Icon size={14} className="text-on-surface-variant opacity-70 shrink-0" aria-hidden="true" />}
    </div>
    <div className="stat-value truncate">{value}</div>
    <div className="flex items-center justify-between gap-2 stat-hint">
      {chip ? <span className={`chip chip-${chip.tone === 'default' ? '' : chip.tone}`}>{chip.label}</span> : <span />}
      {footer}
    </div>
  </div>
);

const StatGridSkeleton = () => (
  <section className="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-4 gap-3" aria-hidden="true">
    {[0, 1, 2, 3].map(i => (
      <div key={i} className="stat">
        <div className="stat-head">
          <div className="h-2.5 w-20 rounded-control bg-on-surface/[0.08] animate-pulse" />
          <div className="h-3 w-3 rounded-control bg-on-surface/[0.06] animate-pulse" />
        </div>
        <div className={`h-7 ${i === 0 ? 'w-32' : 'w-20'} rounded-control bg-on-surface/[0.10] animate-pulse`} />
        <div className="h-3 w-24 rounded-control bg-on-surface/[0.06] animate-pulse" />
      </div>
    ))}
  </section>
);

export default Dashboard;
