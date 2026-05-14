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
// 未登录态：单行卡 + CTA，去掉大紫色块装饰；信息密度优先
const PublicHero = ({ onNavigate, t }) => (
  <section className="fl-card flex flex-col sm:flex-row items-start sm:items-center gap-4 p-5 sm:p-6">
    <div className="flex-1 min-w-0">
      <h1 className="text-xl sm:text-2xl font-bold tracking-tight text-on-surface">
        {t('DASH.PUBLIC_HERO_TITLE', '一个 sk- token 接入主流模型')}
      </h1>
      <p className="text-sm text-on-surface-variant mt-1.5 max-w-2xl">
        {t('DASH.PUBLIC_HERO_SUB', 'OpenAI / Anthropic / Gemini 协议全兼容，按 token 计费，无月费门槛')}
      </p>
    </div>
    <div className="flex items-center gap-2 shrink-0">
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

// Phase 7：MS Store 风格 — section title + horizontal scroll + store-card
// 改造前：3 列 grid，所有模型一次性铺满，视觉密度过高
// 改造后：横向滚动 group（snap），单 group 最多展示 8 个 store-card
const ProviderModelSection = ({ group, formatCurrency, onSeeAll, onModelClick }) => {
  const Icon = group.provider.icon;
  const items = group.items.slice(0, 8); // MS Store 单行最多 6-8 张
  return (
    <section className="space-y-3">
      <header className="flex items-center justify-between">
        <button
          type="button"
          onClick={onSeeAll}
          className="fl-section-title group"
        >
          <span
            className="w-8 h-8 rounded-lg flex items-center justify-center border mr-2"
            style={{
              background: hexA(group.provider.hue, 0.16),
              borderColor: hexA(group.provider.hue, 0.25),
            }}
          >
            <Icon size={16} style={{ color: group.provider.hue }} />
          </span>
          <span>{group.provider.name}</span>
          <ChevronRight size={20} className="ml-1" />
        </button>
        <span className="text-xs text-on-surface-variant tabular-nums">
          {group.items.length}
        </span>
      </header>
      <div className="fl-h-scroll -mx-1 px-1">
        {items.map(model => (
          <ModelStoreCard
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

const ModelStoreCard = ({ model, provider, formatCurrency, onClick }) => {
  const Icon = provider.icon;
  const inPrice = parseFloat(model.min_input_price) || 0;
  const isFree = !inPrice;

  return (
    <button
      type="button"
      onClick={onClick}
      className="fl-store-card text-left"
      style={{
        '--hero-bg-1': darkenHex(provider.hue, 0.55),
        '--hero-bg-2': provider.hue,
        '--hero-bg-3': darkenHex(provider.hue, 0.78),
        width: '240px',
        minHeight: '170px',
      }}
      title={model.model_id}
    >
      <div className="flex items-start justify-between">
        <div className="w-11 h-11 rounded-xl flex items-center justify-center bg-white/15 backdrop-blur-md">
          <Icon size={22} className="text-white" />
        </div>
        {isFree && (
          <span className="px-2 py-0.5 rounded-full text-[10px] font-semibold bg-emerald-400/25 text-emerald-100 border border-emerald-300/40">
            FREE
          </span>
        )}
      </div>
      <div className="fl-store-card-meta">
        <div className="fl-store-card-title font-mono text-[15px] truncate">
          {model.model_id}
        </div>
        <div className="fl-store-card-sub flex items-center justify-between gap-2">
          <span>{provider.name}</span>
          {!isFree && (
            <span className="font-mono tabular-nums text-white/85 font-medium">
              {formatCurrency(inPrice, 2)}<span className="text-white/55">/M</span>
            </span>
          )}
        </div>
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

function darkenHex(hex, factor = 0.5) {
  if (!hex || hex[0] !== '#') return '#1e1b4b';
  const m = hex.match(/^#([0-9a-f]{6})$/i);
  if (!m) return '#1e1b4b';
  const n = parseInt(m[1], 16);
  const r = Math.round(((n >> 16) & 255) * (1 - factor));
  const g = Math.round(((n >> 8) & 255) * (1 - factor));
  const b = Math.round((n & 255) * (1 - factor));
  return '#' + [r, g, b].map((x) => x.toString(16).padStart(2, '0')).join('');
}

export default Dashboard;
