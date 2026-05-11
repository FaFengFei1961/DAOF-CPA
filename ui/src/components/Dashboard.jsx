import React, { useEffect, useState, useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import {
  ArrowUpRight, ChevronRight,
  Bot, BrainCircuit, Sparkles as SparkIcon, Code, MessageSquare, Cpu, Zap, Image as ImageIcon,
  Layers, KeyRound, CreditCard,
} from 'lucide-react';
import { authFetch, isLoggedIn } from '../utils/authFetch';
import { logger } from '../utils/logger';
import { useCurrency } from '../context/CurrencyContext';
import CreditsPoolCard from './CreditsPoolCard';
import { aggregateBySeries, remainingColor, safePct, OPENAI_O_SERIES_RE } from '../utils/credits';
import { useCreditsPoolSummary } from '../hooks/useCreditsPoolSummary';

/**
 * Microsoft Store 主页风格 Dashboard（实测对齐版）。
 *
 * 关键差异点（之前版本被指"不像"）：
 *  - Hero 高度从 260px 提到 ~540px / 60vh（MS Store 实际比例）
 *  - Hero 大卡用 mesh gradient + 装饰光斑作为视觉主导（替代产品 key art）
 *  - Hero 左下角应用图标 squircle 96×96（视觉锚点） + 大字标题（32-48px）
 *  - 右侧 1+2：上一个大块 + 下两个并排小块（参考 Ride the spectrum / Kiln / Golden Week 三联）
 *  - 页面下方加横滚 "热门模型 / 新潮模型" 列表（参考 MS Store "热门游戏 / 新潮应用"）
 */

// 厂牌色板
const PROVIDER_HUE = {
  Anthropic: '#d97706', OpenAI: '#10b981', Google: '#0ea5e9',
  DeepSeek: '#3b82f6', Moonshot: '#ef4444', Midjourney: '#a855f7',
  Qwen: '#6366f1', Meta: '#60a5fa', xAI: '#facc15',
};
const PROVIDER_ICON = {
  Anthropic: BrainCircuit, OpenAI: Bot, Google: SparkIcon,
  DeepSeek: Code, Moonshot: MessageSquare, Midjourney: ImageIcon,
  Qwen: Cpu, Meta: Cpu, xAI: Zap,
};

const inferProvider = (modelId = '') => {
  const id = modelId.toLowerCase();
  let name = 'AI';
  if (id.includes('claude') || id.includes('anthropic')) name = 'Anthropic';
  else if (id.includes('gpt') || id.includes('openai') || OPENAI_O_SERIES_RE.test(id)) name = 'OpenAI';
  else if (id.includes('gemini') || id.includes('google')) name = 'Google';
  else if (id.includes('deepseek')) name = 'DeepSeek';
  else if (id.includes('kimi') || id.includes('moonshot')) name = 'Moonshot';
  else if (id.includes('midjourney') || id.includes('mj-')) name = 'Midjourney';
  else if (id.includes('qwen') || id.includes('tongyi')) name = 'Qwen';
  else if (id.includes('llama') || id.includes('meta')) name = 'Meta';
  else if (id.includes('grok') || id.includes('xai')) name = 'xAI';
  return {
    name,
    hue: PROVIDER_HUE[name] || '#94a3b8',
    icon: PROVIDER_ICON[name] || SparkIcon,
  };
};

const Dashboard = ({ isAuthenticated, onNavigate }) => {
  const { t } = useTranslation();
  const { formatCurrency } = useCurrency();

  const [me, setMe] = useState(null);
  const [models, setModels] = useState([]);
  const [recentLogs, setRecentLogs] = useState([]);
  const [stats, setStats] = useState(null);
  // 号池模型聚合数据（按系列展示用）
  // fix Major Codex UX 审查（第二十五轮）：从共享 hook 取，避免和 CreditsPoolCard 双轮询消耗后端限流配额
  const { models: poolModels } = useCreditsPoolSummary();
  const [loadError, setLoadError] = useState(false);

  useEffect(() => {
    const ctrl = new AbortController();
    const swallow = (err) => {
      if (err?.name === 'AbortError') return null;
      logger.warn('[dashboard] fetch failed', err);
      return { __failed: true };
    };
    const run = async () => {
      // 公开数据：所有访客（含未登录）都能看到，作为落地页的营销信号
      // fix Major Codex UX 审查（第二十五轮）：credits-pool/summary 已从 useCreditsPoolSummary hook 获取，
      // 不再单独 fetch（hook 内部管轮询 + 共享 cache，避免双倍消耗后端 6/min IP 限流配额）。
      const tasks = [
        fetch('/api/pricing', { signal: ctrl.signal }).then(r => r.ok ? r.json() : { __failed: true }).catch(swallow),
      ];
      // 登录后才拉的私有数据：余额、日志、统计
      if (isAuthenticated && isLoggedIn()) {
        tasks.push(authFetch('/api/user/me', { signal: ctrl.signal }).catch(swallow));
        tasks.push(authFetch('/api/logs?page=1&limit=8', { signal: ctrl.signal }).catch(swallow));
        tasks.push(authFetch('/api/logs/stats?period=30d', { signal: ctrl.signal }).catch(swallow));
      }
      const results = await Promise.all(tasks);
      if (ctrl.signal.aborted) return;
      const [pricing, meRes, logsRes, statsRes] = results;
      if (pricing?.success) setModels(pricing.data || []);
      if (meRes?.success) setMe(meRes.data);
      if (logsRes?.success) setRecentLogs(logsRes.data?.items || logsRes.data || []);
      if (statsRes?.success) setStats(statsRes.data || null);
      const allFailed = results.every(r => r?.__failed || !r);
      if (allFailed && results.length > 0) setLoadError(true);
    };
    run();
    return () => ctrl.abort();
  }, [isAuthenticated]);

  // 按系列聚合，4 块固定卡片
  const seriesStats = useMemo(() => aggregateBySeries(poolModels), [poolModels]);

  // 排序：可用通道数最高的排前
  const sortedModels = useMemo(
    () => [...models].sort((a, b) => (b.available_paths || 0) - (a.available_paths || 0)),
    [models]
  );

  const heroModel = sortedModels[0];
  const heroProvider = heroModel ? inferProvider(heroModel.model_id) : { name: 'AI', hue: '#7c5cff', icon: SparkIcon };

  return (
    <div className="space-y-8">
      {loadError && (
        <div className="rounded-lg border border-error/40 bg-error/10 px-4 py-2 text-sm text-error">
          {t('DASH.LOAD_FAILED', '数据加载失败，请检查网络或稍后重试')}
        </div>
      )}

      {/* 平台号池实时余量（按模型聚合，无敏感字段） */}
      <CreditsPoolCard />


      {/* ════ HERO 区：1 + 2 × 2 布局（参考 MS Store 主页） ════ */}
      <section className="grid grid-cols-1 lg:grid-cols-3 gap-3 h-auto lg:h-[540px]">
        {/* 左大卡 — 占 2 列，全高 */}
        <HeroFeatured
          provider={heroProvider}
          authed={isAuthenticated}
          me={me}
          models={models}
          formatCurrency={formatCurrency}
          onPrimary={() => onNavigate(isAuthenticated ? 'tokens' : 'pricing')}
          t={t}
        />

        {/* 右侧：上 1 大 + 下 2 小 stack */}
        <div className="lg:col-span-1 grid grid-rows-[1.5fr_1fr] gap-3 lg:h-full">
          {/* 上：大块 — 紫色（订阅） */}
          <HeroBlock
            hue="#a855f7"
            title={t('DASH.UPGRADE_TITLE', '订阅套餐')}
            sub={t('DASH.UPGRADE_SUB', '月度 / 季度灵活组合，按 token 或消息计费')}
            actionLabel={t('DASH.SEE_PLANS', '查看套餐')}
            onClick={() => onNavigate('upgrade')}
            icon={Layers}
          />
          {/* 下：两小并排 — 青 / 翠绿 */}
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

      {/* 号池系列概览（所有访客可见——营销信号），替代旧的 4 块 KPI */}
      <SeriesGrid items={seriesStats} t={t} />

      {/* ════ 横滚列表区：热门模型 / 新潮模型 ════ */}
      {sortedModels.length > 0 && (
        <div className="grid grid-cols-1 lg:grid-cols-2 gap-x-10 gap-y-6">
          <ScrollableList
            title={t('DASH.HOT_MODELS', '热门模型')}
            onSeeAll={() => onNavigate('pricing')}
            items={sortedModels.slice(0, 8)}
            renderItem={(m) => (
              <ModelRow key={m.model_id} model={m} formatCurrency={formatCurrency} onClick={() => onNavigate('pricing')} t={t} />
            )}
          />
          <ScrollableList
            title={t('DASH.TRENDING_MODELS', '新潮模型')}
            onSeeAll={() => onNavigate('pricing')}
            items={[...sortedModels].reverse().slice(0, 8)}
            renderItem={(m) => (
              <ModelRow key={m.model_id} model={m} formatCurrency={formatCurrency} onClick={() => onNavigate('pricing')} t={t} />
            )}
          />
        </div>
      )}

      {/* 最近请求 */}
      {isAuthenticated && recentLogs.length > 0 && (
        <RecentLogs logs={recentLogs} formatCurrency={formatCurrency} onSeeAll={() => onNavigate('stats')} t={t} />
      )}
    </div>
  );
};

// ════════ Hero 大卡（左 2/3，全高）════════
//
// MS Store 大卡视觉规则（实测对齐）：
//   - 整张卡是"产品视觉"占据：饱和色块 / 大图作主导
//   - 文字与按钮全部白色（前景），叠在彩色背景上
//   - 左下角 80-96px squircle 应用图标
//   - 标题 32-48px semibold，副标 14-16px 70% 不透明白
//   - "获取/安装"按钮：填色 prominent，~32-36px 高
const HeroFeatured = ({ provider, authed, me, models, formatCurrency, onPrimary, t }) => {
  const Icon = provider.icon;
  const totalChannels = models.reduce((acc, m) => acc + (m.available_paths || 0), 0);
  return (
    <div
      className="lg:col-span-2 fl-hero p-8 sm:p-10 flex flex-col h-full min-h-[480px]"
      style={{
        '--hero-bg-1': darkenHex(provider.hue, 0.45),
        '--hero-bg-2': provider.hue,
        '--hero-bg-3': darkenHex(provider.hue, 0.7),
      }}
    >
      {/* 顶部 pill 徽章 */}
      <div className="flex items-center gap-2">
        <span className="inline-flex items-center gap-1.5 px-2.5 py-1 rounded text-[11px] font-semibold uppercase tracking-wider bg-white/15 backdrop-blur-sm">
          <Icon size={12} /> {provider.name}
        </span>
        <span className="text-[11px] uppercase tracking-wider fl-hero-text-secondary">
          {t('DASH.FEATURED', '今日推荐')}
        </span>
      </div>

      {/* 中央留白 */}
      <div className="flex-1 min-h-[80px]" />

      {/* 左下：图标 + 标题 + 副标题 + 主操作按钮（与 MS Store 一致） */}
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
        <div className="flex items-center gap-3 pt-2">
          {/* 主按钮：白底深字，参考 MS Store "安装/获取" 在彩色背景上的高对比 */}
          <button
            type="button"
            onClick={onPrimary}
            className="inline-flex items-center justify-center gap-1.5 h-9 px-5 rounded text-sm font-semibold bg-white text-zinc-900 hover:bg-white/90 active:scale-[0.98] transition"
          >
            {authed ? t('DASH.MANAGE_TOKENS', '管理 Token') : t('DASH.GET_STARTED', '开始使用')}
            <ArrowUpRight size={14} />
          </button>
          {/* 右侧统计 */}
          <div className="flex items-center gap-5 ml-2">
            <Stat2 label={t('DASH.STAT_MODELS', '模型')} value={models.length} />
            <span className="w-px h-8 bg-white/20" />
            <Stat2 label={t('DASH.STAT_CHANNELS', '通道')} value={totalChannels} />
          </div>
        </div>
      </div>
    </div>
  );
};

const Stat2 = ({ label, value }) => (
  <div>
    <div className="text-xl font-semibold text-white tabular-nums">{value}</div>
    <div className="text-[10px] uppercase tracking-wider text-white/70">{label}</div>
  </div>
);

// ════════ 右侧 Hero 块（大 / 紧凑） ════════
// 同样饱和色块风格，每块独立 hue
const HeroBlock = ({ title, sub, actionLabel, onClick, icon: Icon, hue = '#7c5cff', compact = false }) => {
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
      <Icon size={compact ? 18 : 22} className="text-white" />
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

// ════════ 模型列表区块（垂直 8 行展示）
//
// 历史：原版抄 MS Store 设计意图做横滚 carousel + 左右翻页，但 ModelRow 是 w-full 全宽行
// 不是固定宽度卡片 → flex-col 容器 + scrollBy({left}) 永远不动 → 翻页按钮成摆设。
// 现状 slice(0,8) 固定 8 项也不需要翻页，简化成纯垂直列表 + 标题旁 chevron 跳详情。
//
// 注：reviewer 提到的 t 参数现已不使用，保留 prop 不破坏调用方
const ScrollableList = ({ title, items, renderItem, onSeeAll }) => (
  <section>
    <header className="flex items-center justify-between mb-3">
      <button
        type="button"
        onClick={onSeeAll}
        className="group flex items-center gap-1 text-on-surface hover:text-primary"
      >
        <h2 className="text-xl font-semibold tracking-tight">{title}</h2>
        <ChevronRight size={20} className="text-on-surface-variant group-hover:text-primary transition" />
      </button>
    </header>
    <div className="flex flex-col gap-2">
      {items.map(renderItem)}
    </div>
  </section>
);

// ════════ 模型行（MS Store "Roblox - Windows / Fortnite" 行风格） ════════
const ModelRow = ({ model, formatCurrency, onClick, t }) => {
  const provider = inferProvider(model.model_id);
  const Icon = provider.icon;
  const inPrice = parseFloat(model.min_input_price) || 0;
  const isFree = !inPrice;
  const offline = !model.available_paths;

  return (
    <button
      type="button"
      onClick={onClick}
      className="group flex items-center gap-4 p-3 rounded-lg hover:bg-on-surface/[0.04] transition text-left w-full"
    >
      {/* 大图标 squircle — MS Store 应用图标尺寸 */}
      <div
        className="w-16 h-16 rounded-2xl flex items-center justify-center shrink-0 border"
        style={{
          background: `linear-gradient(135deg, ${hexA(provider.hue, 0.3)} 0%, ${hexA(provider.hue, 0.1)} 100%)`,
          borderColor: hexA(provider.hue, 0.25),
        }}
      >
        <Icon size={28} style={{ color: provider.hue }} />
      </div>

      {/* 中间：标题 + 副标题 */}
      <div className="flex-1 min-w-0">
        <div className="text-sm font-semibold text-on-surface truncate font-mono" title={model.model_id}>
          {model.model_id}
        </div>
        <div className="text-xs text-on-surface-variant mt-0.5">
          {provider.name}
          {!offline && (
            <>
              <span className="opacity-40 mx-1.5">·</span>
              {model.available_paths} {t('DASH.CHANNELS', '通道')}
            </>
          )}
        </div>
        {/* 操作按钮：替代 MS Store 的"免费下载" */}
        <div className="mt-1.5">
          {isFree ? (
            <span className="text-xs font-semibold text-emerald-500 dark:text-emerald-400">
              {t('DASH.FREE', '免费')}
            </span>
          ) : (
            <span className="text-xs font-medium text-on-surface tabular-nums font-mono">
              {formatCurrency(inPrice, 2)}
              <span className="text-on-surface-variant">/M tokens</span>
            </span>
          )}
        </div>
      </div>

      {/* 右侧：状态指示 */}
      {offline ? (
        <span className="text-[11px] text-on-surface-variant/60 shrink-0">{t('DASH.OFFLINE', '离线')}</span>
      ) : (
        <span className="w-2 h-2 rounded-full bg-emerald-500 shrink-0" title={t('DASH.ONLINE', '在线')} />
      )}
    </button>
  );
};

// ════════ KPI 行 ════════
// SeriesGrid — 号池按模型系列的 4 块平均额度卡片（Claude / OpenAI / Gemini / Kimi）
//
// 数据契约（来自 utils/credits.js::aggregateBySeries）：
//   { id, label, hue, avgRemaining, online, modelCount }
//
// 视觉规则：
//   - online=false → 灰底 + "离线" 标签（不显示百分比）
//   - online=true  → 进度条 + 剩余% + 系列色调底色
//   - modelCount=0（系列里一个模型都不在号池） → 显示"—"（数据不可用）
const SeriesGrid = ({ items, t }) => (
  <div className="grid grid-cols-2 lg:grid-cols-4 gap-3">
    {items.map((s) => {
      const hasData = s.modelCount > 0;
      const offline = !s.online;
      const rem = safePct(s.avgRemaining);
      const color = offline ? '#6b7280' : remainingColor(rem);
      return (
        <div key={s.id} className="fl-card px-4 py-3 relative overflow-hidden">
          <div className="flex items-center justify-between gap-2">
            <span
              className="text-[11px] uppercase tracking-wider font-semibold"
              style={{ color: hasData ? s.hue : 'var(--color-on-surface-variant)' }}
            >
              {s.label}
            </span>
            {hasData && (
              <span className={`text-[10px] px-1.5 py-0.5 rounded ${offline ? 'bg-red-500/15 text-red-400' : 'bg-emerald-500/15 text-emerald-400'}`}>
                {offline ? t('DASH.OFFLINE', '离线') : t('DASH.ONLINE', '在线')}
              </span>
            )}
          </div>
          <div className="mt-2 flex items-baseline justify-between">
            <span className="text-2xl font-semibold tabular-nums" style={{ color }}>
              {hasData ? (offline ? '—' : `${rem.toFixed(0)}%`) : '—'}
            </span>
            {hasData && !offline && (
              <span
                className="text-[10px] text-on-surface-variant"
                title={t('DASH.SERIES_ONLINE_TIP', '在线模型 / 系列总模型')}
              >
                {s.onlineCount}/{s.modelCount} {t('DASH.SERIES_ONLINE', '在线')}
              </span>
            )}
          </div>
          {hasData && !offline && (
            <div className="mt-2 h-1.5 rounded-full bg-on-surface/10 overflow-hidden">
              <div
                className="h-full transition-all duration-500"
                style={{ width: `${rem}%`, background: color, boxShadow: `0 0 8px ${color}80` }}
              />
            </div>
          )}
        </div>
      );
    })}
  </div>
);

// ════════ 最近请求 ════════
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

// hex → rgba 方便给 mesh gradient 用
function hexA(hex, alpha) {
  if (!hex || hex[0] !== '#') return `rgba(124, 92, 255, ${alpha})`;
  const m = hex.match(/^#([0-9a-f]{6})$/i);
  if (!m) return `rgba(124, 92, 255, ${alpha})`;
  const n = parseInt(m[1], 16);
  return `rgba(${(n >> 16) & 255}, ${(n >> 8) & 255}, ${n & 255}, ${alpha})`;
}

// hex 颜色变暗（factor 0..1，0=不变，1=纯黑）
// 用来给 hero 大卡的角落色块产生"加深"效果模拟产品图深度
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
