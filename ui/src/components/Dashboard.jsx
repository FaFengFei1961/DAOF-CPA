import React, { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router-dom';
import { ChevronRight } from 'lucide-react';
import { authFetch, isLoggedIn } from '../utils/authFetch';
import { logger } from '../utils/logger';
import { useAuth } from '../context/AuthContext';
import { useCurrency } from '../context/CurrencyContext';
import { groupModelsByProvider, inferModelProvider } from '../utils/modelProviders';
import { usePublicPricing } from '../hooks/usePublicPricing';

// Phase 0：Dashboard 现在自己拿 isAuthenticated（useAuth）+ navigate（useNavigate），
// 不再依赖父组件 prop 注入。view-id → path 简单映射。
const VIEW_TO_PATH = {
  dashboard: '/',
  tokens:    '/tokens',
  stats:     '/stats',
  pricing:   '/pricing',
  upgrade:   '/upgrade',
  topup:     '/topup',
  bills:     '/bills',
  tickets:   '/tickets',
};

const Dashboard = () => {
  const { t } = useTranslation();
  const { isAuthenticated } = useAuth();
  const navigate = useNavigate();
  const onNavigate = (view) => navigate(VIEW_TO_PATH[view] || `/${view}`);
  const { formatCurrency } = useCurrency();

  const [me, setMe] = useState(null);
  const [recentLogs, setRecentLogs] = useState([]);
  const [loadError, setLoadError] = useState(false);
  const { models, error: pricingError } = usePublicPricing();

  useEffect(() => {
    if (!isAuthenticated || !isLoggedIn()) return undefined;

    const ctrl = new AbortController();
    const swallow = (err) => {
      if (err?.name === 'AbortError') return null;
      logger.warn('[dashboard] fetch failed', err);
      return { __failed: true };
    };

    const run = async () => {
      const tasks = [
        authFetch('/api/user/me', { signal: ctrl.signal }).catch(swallow),
        authFetch('/api/logs?page=1&limit=8', { signal: ctrl.signal }).catch(swallow),
      ];

      const results = await Promise.all(tasks);
      if (ctrl.signal.aborted) return;

      const [meRes, logsRes] = results;
      if (meRes?.success) setMe(meRes.data);
      if (logsRes?.success) setRecentLogs(logsRes.data?.items || logsRes.data || []);

      const allFailed = results.every(r => r?.__failed || !r);
      if (allFailed && results.length > 0) setLoadError(true);
    };

    run();
    return () => ctrl.abort();
  }, [isAuthenticated]);

  const providerGroups = useMemo(() => groupModelsByProvider(models), [models]);

  return (
    <div className="space-y-8">
      {(loadError || (pricingError && models.length === 0)) && (
        <div className="rounded-lg border border-error/40 bg-error/10 px-4 py-2 text-sm text-error">
          {t('DASH.LOAD_FAILED', '数据加载失败，请检查网络或稍后重试')}
        </div>
      )}

      {/* Phase 7.5：撤掉 540px 高 brand-color hero（"装饰为王"），改成 Stripe Dashboard 式
          数据 strip："余额 / 最近请求 / 总 Token / 模型数"，首屏直接看到自己账户的真实数字。
          未登录态走轻量 PublicHero 引导（< 140px 高，比原 hero 小 4 倍） */}
      {isAuthenticated && me ? (
        <StatStrip me={me} recentLogs={recentLogs} formatCurrency={formatCurrency} t={t} />
      ) : (
        <PublicHero onNavigate={onNavigate} t={t} />
      )}

      {providerGroups.length > 0 && (
        <div className="space-y-8">
          {providerGroups.map(group => (
            <ProviderModelSection
              key={group.provider.name}
              group={group}
              formatCurrency={formatCurrency}
              onSeeAll={() => onNavigate('pricing')}
              onModelClick={() => onNavigate('pricing')}
            />
          ))}
        </div>
      )}

      {isAuthenticated && recentLogs.length > 0 && (
        <RecentLogs logs={recentLogs} formatCurrency={formatCurrency} onSeeAll={() => onNavigate('stats')} t={t} />
      )}
    </div>
  );
};

// ─── Stat Strip ─────────────────────────────────────────────────────────
// Stripe Dashboard / Vercel 风格：4-up stat 横排，纯数据零装饰
// 数字用 tabular-nums + bold；标签用 caption 全大写；hint 用次要色提示来源
const StatStrip = ({ me, recentLogs, formatCurrency, t }) => {
  const totalReqs = recentLogs.length;
  const totalTokens = recentLogs.reduce(
    (s, l) => s + (l.prompt_tokens || 0) + (l.completion_tokens || 0),
    0
  );
  const uniqueModels = new Set(recentLogs.map(l => l.model_name).filter(Boolean)).size;
  const lastTime = recentLogs[0]?.created_at;
  const lastRel = lastTime ? relativeTime(lastTime) : '—';

  return (
    <section className="fl-card grid grid-cols-2 md:grid-cols-4 divide-y md:divide-y-0 md:divide-x divide-outline-variant/30 overflow-hidden">
      <Stat
        label={t('DASH.STAT_BALANCE', '账户余额')}
        value={formatCurrency(me.quota ?? 0, 2)}
        hint={`Hi, ${me.username}`}
        prominent
      />
      <Stat
        label={t('DASH.STAT_REQUESTS', '最近请求')}
        value={totalReqs.toLocaleString()}
        hint={t('DASH.STAT_RECENT_HINT', '近 8 条')}
      />
      <Stat
        label={t('DASH.STAT_TOKENS', 'Token 用量')}
        value={formatCompactNumber(totalTokens)}
        hint={t('DASH.STAT_RECENT_HINT', '近 8 条')}
      />
      <Stat
        label={t('DASH.STAT_LAST', '上次调用')}
        value={lastRel}
        hint={uniqueModels ? t('DASH.STAT_MODELS', { n: uniqueModels, defaultValue: '{{n}} 个模型' }) : '—'}
      />
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
    <div className="text-[11px] text-on-surface-variant mt-1 truncate">{hint}</div>
  </div>
);

// ─── Public Hero ────────────────────────────────────────────────────────
// 未登录态：左侧 H1 + 副标 + 双 CTA；右侧渐变品牌锚（subtle Mica + 大字体 logo
// 替代之前的"大紫色块装饰"）。整体高度 ~180px 比纯文字横条更有视觉重量。
const PublicHero = ({ onNavigate, t }) => (
  <section className="fl-card relative overflow-hidden grid grid-cols-1 md:grid-cols-[1fr_auto] items-center gap-6 px-6 sm:px-8 py-8 sm:py-10">
    {/* 左：标题 + CTA */}
    <div className="min-w-0">
      <div className="text-[11px] uppercase tracking-[0.12em] font-semibold text-primary mb-2">
        DAOF · CPA
      </div>
      <h1 className="text-2xl sm:text-[34px] font-bold tracking-tight text-on-surface leading-[1.15]">
        {t('DASH.PUBLIC_HERO_TITLE', '一个 sk- token 接入主流模型')}
      </h1>
      <p className="text-sm sm:text-base text-on-surface-variant mt-3 max-w-2xl leading-relaxed">
        {t('DASH.PUBLIC_HERO_SUB', 'OpenAI / Anthropic / Gemini 协议全兼容，按 token 计费，无月费门槛')}
      </p>
      <div className="flex items-center gap-2 mt-5">
        <button
          type="button"
          onClick={() => onNavigate('upgrade')}
          className="fl-btn fl-btn-prominent h-10 px-5"
        >
          {t('DASH.SEE_PLANS', '查看套餐')}
        </button>
        <button
          type="button"
          onClick={() => onNavigate('pricing')}
          className="fl-btn fl-btn-subtle h-10 px-5"
        >
          {t('DASH.SEE_PRICING', '查看定价')}
        </button>
      </div>
    </div>

    {/* 右：subtle 品牌锚（不抢戏，用 primary 半透明 + logo） */}
    <div className="hidden md:flex items-center justify-center shrink-0">
      <div
        className="w-32 h-32 rounded-2xl flex items-center justify-center"
        style={{
          background:
            'radial-gradient(circle at 30% 30%, color-mix(in srgb, var(--color-primary) 20%, transparent), transparent 70%), color-mix(in srgb, var(--color-primary) 8%, transparent)',
          border: '1px solid color-mix(in srgb, var(--color-primary) 25%, transparent)',
        }}
      >
        <img src="/daof_logo.png" alt="" className="w-16 h-16 opacity-90" />
      </div>
    </div>
  </section>
);

// 数字紧凑表示：1234 → 1.2k；1234567 → 1.23M
function formatCompactNumber(n) {
  if (!n) return '0';
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(2) + 'M';
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'k';
  return n.toLocaleString();
}

// 相对时间：刚刚 / N 分钟前 / N 小时前 / N 天前
function relativeTime(ts) {
  const t = new Date(ts).getTime();
  if (isNaN(t)) return '—';
  const diff = Date.now() - t;
  if (diff < 60_000) return '刚刚';
  if (diff < 3_600_000) return `${Math.floor(diff / 60_000)} 分钟前`;
  if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)} 小时前`;
  if (diff < 30 * 86_400_000) return `${Math.floor(diff / 86_400_000)} 天前`;
  return new Date(ts).toLocaleDateString('zh-CN');
}

// Phase 7.5+：撤掉 Phase 7 的横向滚动大色块卡（用户截图反馈"好难看"），改 Vercel 风
// 紧凑 grid + 横排极简卡：每张卡 ~72px 高，去掉饱和色 hero 渐变，brand 色仅作图标
// tint 出现，整体回归数据/控制台调性
const ProviderModelSection = ({ group, formatCurrency, onSeeAll, onModelClick }) => {
  const Icon = group.provider.icon;
  const total = group.items.length;
  const items = group.items.slice(0, 6); // grid 6 张刚好覆盖 sm 2x3 / md 3x2 / lg 6x1
  return (
    <section className="space-y-3">
      <header className="flex items-baseline justify-between gap-3 px-1">
        <h2 className="flex items-center gap-2 text-base font-semibold text-on-surface">
          <span
            className="w-6 h-6 rounded-md flex items-center justify-center"
            style={{
              background: hexA(group.provider.hue, 0.16),
              border: `1px solid ${hexA(group.provider.hue, 0.3)}`,
            }}
          >
            <Icon size={13} style={{ color: group.provider.hue }} />
          </span>
          <span>{group.provider.name}</span>
          <span className="text-xs text-on-surface-variant font-normal">({total})</span>
        </h2>
        {total > items.length && (
          <button
            type="button"
            onClick={onSeeAll}
            className="text-xs text-on-surface-variant hover:text-primary transition inline-flex items-center gap-0.5"
          >
            {`查看全部 ${total} 个`}
            <ChevronRight size={12} />
          </button>
        )}
      </header>
      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-2">
        {items.map(model => (
          <ModelCard
            key={model.model_id}
            model={model}
            provider={group.provider}
            formatCurrency={formatCurrency}
            onClick={onModelClick}
          />
        ))}
      </div>
    </section>
  );
};

const ModelCard = ({ model, provider, formatCurrency, onClick }) => {
  const Icon = provider.icon;
  const inPrice = parseFloat(model.min_input_price) || 0;
  const outPrice = parseFloat(model.min_output_price) || 0;
  const isFree = !inPrice;

  return (
    <button
      type="button"
      onClick={onClick}
      className="fl-card flex items-center gap-3 px-3.5 py-2.5 text-left"
      title={model.model_id}
    >
      {/* brand-tint 小图标 — 不再大色块铺背景 */}
      <div
        className="w-9 h-9 rounded-md flex items-center justify-center shrink-0"
        style={{
          background: hexA(provider.hue, 0.14),
          border: `1px solid ${hexA(provider.hue, 0.28)}`,
        }}
      >
        <Icon size={16} style={{ color: provider.hue }} />
      </div>

      <div className="flex-1 min-w-0">
        <div className="font-mono text-[13px] font-semibold text-on-surface truncate leading-tight">
          {model.model_id}
        </div>
        <div className="text-[11px] text-on-surface-variant mt-0.5">{provider.name}</div>
      </div>

      <div className="shrink-0 text-right">
        {isFree ? (
          <span className="inline-flex items-center px-1.5 py-0.5 rounded text-[10px] font-bold bg-emerald-500/15 text-emerald-400 border border-emerald-500/30">
            FREE
          </span>
        ) : (
          <>
            <div className="font-mono text-[13px] font-semibold text-on-surface tabular-nums leading-tight">
              {formatCurrency(inPrice, 2)}
              <span className="text-on-surface-variant font-normal">/M</span>
            </div>
            {outPrice > 0 && outPrice !== inPrice && (
              <div className="font-mono text-[10px] text-on-surface-variant tabular-nums">
                out {formatCurrency(outPrice, 2)}
              </div>
            )}
          </>
        )}
      </div>
    </button>
  );
};

const RecentLogs = ({ logs, formatCurrency, onSeeAll, t }) => (
  <section className="fl-card overflow-hidden">
    <header className="flex items-center justify-between px-4 py-2.5 border-b border-outline-variant">
      <h2 className="text-sm font-semibold text-on-surface">{t('DASH.RECENT_LOGS', '最近请求')}</h2>
      <button type="button" onClick={onSeeAll} className="text-xs text-on-surface-variant hover:text-on-surface inline-flex items-center gap-0.5">
        {t('DASH.SEE_ALL', '查看全部')} <ChevronRight size={12} />
      </button>
    </header>
    <div className="overflow-x-auto">
      <table className="w-full text-sm">
        <thead>
          <tr className="text-[11px] uppercase tracking-wider text-on-surface-variant border-b border-outline-variant/60">
            <th className="text-left font-medium px-4 py-2">{t('DASH.TBL_TIME', '时间')}</th>
            <th className="text-left font-medium px-3 py-2">{t('DASH.TBL_MODEL', '模型')}</th>
            <th className="text-right font-medium px-3 py-2">{t('DASH.TBL_TOKENS', 'Token')}</th>
            <th className="text-right font-medium px-4 py-2">{t('DASH.TBL_COST', '花费')}</th>
          </tr>
        </thead>
        <tbody>
          {logs.slice(0, 6).map((log, i) => (
            <tr key={log.id || i} className="border-b border-outline-variant/30 last:border-0 hover:bg-surface-container-high">
              <td className="px-4 py-2 text-xs text-on-surface-variant tabular-nums whitespace-nowrap">
                {formatTime(log.created_at)}
              </td>
              <td className="px-3 py-2 font-mono text-xs text-on-surface truncate max-w-[200px]">{log.model_name || '—'}</td>
              <td className="px-3 py-2 text-right tabular-nums text-on-surface-variant">
                {((log.prompt_tokens || 0) + (log.completion_tokens || 0)).toLocaleString()}
              </td>
              <td className="px-4 py-2 text-right tabular-nums text-on-surface">{formatCurrency(log.cost || 0, 4)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  </section>
);

function formatTime(s) {
  if (!s) return '—';
  const d = new Date(s);
  if (isNaN(d)) return '—';
  const now = new Date();
  const sameDay = d.getDate() === now.getDate() && d.getMonth() === now.getMonth() && d.getFullYear() === now.getFullYear();
  const pad = (n) => String(n).padStart(2, '0');
  const hm = `${pad(d.getHours())}:${pad(d.getMinutes())}`;
  return sameDay ? hm : `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${hm}`;
}

function hexA(hex, alpha) {
  if (!hex || hex[0] !== '#') return `rgba(124, 92, 255, ${alpha})`;
  const m = hex.match(/^#([0-9a-f]{6})$/i);
  if (!m) return `rgba(124, 92, 255, ${alpha})`;
  const n = parseInt(m[1], 16);
  return `rgba(${(n >> 16) & 255}, ${(n >> 8) & 255}, ${n & 255}, ${alpha})`;
}

export default Dashboard;
