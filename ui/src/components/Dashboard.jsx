import React, { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router-dom';
import {
  ArrowUpRight,
  ChevronRight,
  CreditCard,
  KeyRound,
  Layers,
  Sparkles as SparkIcon,
} from 'lucide-react';
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
  const sortedModels = useMemo(
    () => providerGroups.flatMap(group => group.items),
    [providerGroups]
  );

  const heroModel = sortedModels[0];
  const heroProvider = heroModel ? inferModelProvider(heroModel.model_id) : { name: 'AI', hue: '#7c5cff', icon: SparkIcon };

  return (
    <div className="space-y-8">
      {(loadError || (pricingError && models.length === 0)) && (
        <div className="rounded-lg border border-error/40 bg-error/10 px-4 py-2 text-sm text-error">
          {t('DASH.LOAD_FAILED', '数据加载失败，请检查网络或稍后重试')}
        </div>
      )}

      <section className="grid grid-cols-1 lg:grid-cols-3 gap-3 h-auto lg:h-[540px]">
        <HeroFeatured
          provider={heroProvider}
          authed={isAuthenticated}
          me={me}
          formatCurrency={formatCurrency}
          onPrimary={() => onNavigate(isAuthenticated ? 'tokens' : 'pricing')}
          t={t}
        />

        <div className="lg:col-span-1 grid grid-rows-[1.5fr_1fr] gap-3 lg:h-full">
          <HeroBlock
            hue="#a855f7"
            title={t('DASH.UPGRADE_TITLE', '订阅套餐')}
            sub={t('DASH.UPGRADE_SUB', '月度 / 季度灵活组合，按 token 或消息计费')}
            actionLabel={t('DASH.SEE_PLANS', '查看套餐')}
            onClick={() => onNavigate('upgrade')}
            icon={Layers}
          />
          <div className="grid grid-cols-2 gap-3">
            <HeroBlock
              compact
              hue="#0891b2"
              title={t('DASH.PRICING_TITLE', '定价')}
              sub={t('DASH.PRICING_SUB', '逐字 token')}
              icon={CreditCard}
              onClick={() => onNavigate('pricing')}
            />
            <HeroBlock
              compact
              hue="#059669"
              title={isAuthenticated ? t('DASH.MY_TOKENS', '我的 Token') : t('DASH.GET_TOKEN', '获取 Token')}
              sub={isAuthenticated ? t('DASH.MANAGE_KEYS', '管理 API Key') : t('DASH.SIGN_IN_FIRST', '需要登录')}
              icon={KeyRound}
              onClick={() => onNavigate(isAuthenticated ? 'tokens' : 'pricing')}
            />
          </div>
        </div>
      </section>

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

const HeroFeatured = ({ provider, authed, me, formatCurrency, onPrimary, t }) => {
  const Icon = provider.icon;
  return (
    <div
      className="lg:col-span-2 fl-hero p-8 sm:p-10 flex flex-col h-full min-h-[480px]"
      style={{
        '--hero-bg-1': darkenHex(provider.hue, 0.45),
        '--hero-bg-2': provider.hue,
        '--hero-bg-3': darkenHex(provider.hue, 0.7),
      }}
    >
      <div className="flex items-center gap-2">
        <span className="inline-flex items-center gap-1.5 px-2.5 py-1 rounded text-[11px] font-semibold uppercase tracking-wider bg-white/15 backdrop-blur-sm">
          <Icon size={12} /> {provider.name}
        </span>
        <span className="text-[11px] uppercase tracking-wider fl-hero-text-secondary">
          {t('DASH.FEATURED', '今日推荐')}
        </span>
      </div>

      <div className="flex-1 min-h-[80px]" />

      <div className="space-y-4">
        <div className="w-20 h-20 sm:w-24 sm:h-24 rounded-2xl flex items-center justify-center bg-white/15 backdrop-blur-md shadow-2xl">
          <Icon size={40} className="text-white" />
        </div>
        <div>
          <h1 className="text-3xl sm:text-5xl font-semibold tracking-tight leading-tight text-white">
            {authed && me ? `Hi, ${me.username}` : 'DAOF-CPA'}
          </h1>
          <p className="text-sm sm:text-base fl-hero-text-secondary mt-2 max-w-xl">
            {authed
              ? t('DASH.SUB_AUTHED', { balance: formatCurrency(me?.quota ?? 0, 2), defaultValue: '余额 {{balance}} · 一个 sk- token 接入主流模型' })
              : t('DASH.SUB_PUBLIC', '一个 sk- token 接入主流模型，OpenAI / Anthropic / Gemini 协议全兼容')}
          </p>
        </div>
        <button
          type="button"
          onClick={onPrimary}
          className="inline-flex items-center justify-center gap-1.5 h-9 px-5 rounded text-sm font-semibold bg-white text-zinc-900 hover:bg-white/90 active:scale-[0.98] transition"
        >
          {authed ? t('DASH.MANAGE_TOKENS', '管理 Token') : t('DASH.GET_STARTED', '开始使用')}
          <ArrowUpRight size={14} />
        </button>
      </div>
    </div>
  );
};

const HeroBlock = ({ title, sub, actionLabel, onClick, icon, hue = '#7c5cff', compact = false }) => {
  const BlockIcon = icon;
  return (
    <button
      type="button"
      onClick={onClick}
      className="fl-hero p-4 sm:p-5 text-left flex flex-col justify-between h-full min-h-[120px]"
      style={{
        '--hero-bg-1': darkenHex(hue, 0.5),
        '--hero-bg-2': hue,
        '--hero-bg-3': darkenHex(hue, 0.75),
      }}
    >
      <BlockIcon size={compact ? 18 : 22} className="text-white" />
      <div>
        <div className={`font-semibold text-white ${compact ? 'text-sm' : 'text-lg sm:text-xl'}`}>{title}</div>
        <div className={`fl-hero-text-secondary mt-0.5 ${compact ? 'text-[11px]' : 'text-xs sm:text-sm'}`}>{sub}</div>
        {actionLabel && !compact && (
          <div className="mt-3 inline-flex items-center gap-1 text-sm font-medium text-white">
            {actionLabel}
            <ArrowUpRight size={14} />
          </div>
        )}
      </div>
    </button>
  );
};

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
