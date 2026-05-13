import React, { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
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
import { useCurrency } from '../context/CurrencyContext';
import { groupModelsByProvider, inferModelProvider } from '../utils/modelProviders';
import { usePublicPricing } from '../hooks/usePublicPricing';

const Dashboard = ({ isAuthenticated, onNavigate }) => {
  const { t } = useTranslation();
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

const ProviderModelSection = ({ group, formatCurrency, onSeeAll, onModelClick }) => {
  const Icon = group.provider.icon;
  return (
    <section className="space-y-3">
      <header className="flex items-center justify-between">
        <button
          type="button"
          onClick={onSeeAll}
          className="group flex items-center gap-2 text-on-surface hover:text-primary"
        >
          <span
            className="w-7 h-7 rounded-lg flex items-center justify-center border"
            style={{
              background: hexA(group.provider.hue, 0.16),
              borderColor: hexA(group.provider.hue, 0.25),
            }}
          >
            <Icon size={15} style={{ color: group.provider.hue }} />
          </span>
          <h2 className="text-xl font-semibold tracking-tight">{group.provider.name}</h2>
          <ChevronRight size={20} className="text-on-surface-variant group-hover:text-primary transition" />
        </button>
      </header>
      <div className="grid grid-cols-1 md:grid-cols-2 2xl:grid-cols-3 gap-3">
        {group.items.map(model => (
          <ModelRow
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

const ModelRow = ({ model, provider, formatCurrency, onClick }) => {
  const Icon = provider.icon;
  const inPrice = parseFloat(model.min_input_price) || 0;
  const isFree = !inPrice;

  return (
    <button
      type="button"
      onClick={onClick}
      className="group flex items-center gap-3 p-3 rounded-lg border border-outline-variant/40 bg-surface/40 hover:bg-on-surface/[0.04] hover:border-outline transition text-left w-full min-h-[84px]"
    >
      <div
        className="w-12 h-12 rounded-xl flex items-center justify-center shrink-0 border"
        style={{
          background: `linear-gradient(135deg, ${hexA(provider.hue, 0.3)} 0%, ${hexA(provider.hue, 0.1)} 100%)`,
          borderColor: hexA(provider.hue, 0.25),
        }}
      >
        <Icon size={22} style={{ color: provider.hue }} />
      </div>

      <div className="flex-1 min-w-0">
        <div className="text-sm font-semibold text-on-surface truncate font-mono leading-5" title={model.model_id}>
          {model.model_id}
        </div>
        <div className="text-xs text-on-surface-variant mt-1">{provider.name}</div>
        <div className="mt-1">
          {isFree ? (
            <span className="text-xs font-semibold text-emerald-500 dark:text-emerald-400">免费</span>
          ) : (
            <span className="text-xs font-medium text-on-surface tabular-nums font-mono">
              {formatCurrency(inPrice, 2)}
              <span className="text-on-surface-variant">/M tokens</span>
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
