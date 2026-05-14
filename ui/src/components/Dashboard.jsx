import React, { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router-dom';
import { ChevronRight, ShieldAlert } from 'lucide-react';
import { authFetch, isLoggedIn } from '../utils/authFetch';
import { logger } from '../utils/logger';
import { useAuth } from '../context/AuthContext';
import { useCurrency } from '../context/CurrencyContext';
import { groupModelsByProvider, hexA } from '../utils/modelProviders';
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
  const { isAuthenticated, isAdmin, openLogin } = useAuth();
  const navigate = useNavigate();
  const onNavigate = (view) => navigate(VIEW_TO_PATH[view] || `/${view}`);
  const { formatCurrency } = useCurrency();

  const [me, setMe] = useState(null);
  const [recentLogs, setRecentLogs] = useState([]);
  const [loadError, setLoadError] = useState(false);
  // Phase 7.8 ccg P1-6：之前 isAuthenticated && me ? StatStrip : PublicHero —
  // 已登录用户在 me 返回前 me=null，会先闪一下 PublicHero 营销 CTA。
  // 加 meLoading 区分"未登录"与"登录中"，登录中走 skeleton 占位。
  const [meLoading, setMeLoading] = useState(() => isAuthenticated && isLoggedIn());
  const { models, error: pricingError } = usePublicPricing();

  useEffect(() => {
    if (!isAuthenticated || !isLoggedIn()) {
      setMeLoading(false);
      return undefined;
    }
    setMeLoading(true);

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
      if (meRes?.success) {
        setMe(meRes.data);
        setLoadError(false);
      }
      if (logsRes?.success) setRecentLogs(logsRes.data?.items || logsRes.data || []);

      const allFailed = results.every(r => r?.__failed || !r);
      if (allFailed && results.length > 0) setLoadError(true);
      setMeLoading(false);
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

      {/* Phase 8：四个分支 — admin 没有 quota/me，单独显示入口提示；
          普通用户登录中 → skeleton；登录完 → StatStrip；未登录 → SignInBanner */}
      {isAdmin ? (
        <AdminBanner onEnter={() => navigate('/admin')} t={t} />
      ) : meLoading ? (
        <StatStripSkeleton />
      ) : isAuthenticated && me ? (
        <StatStrip me={me} recentLogs={recentLogs} formatCurrency={formatCurrency} t={t} />
      ) : (
        <SignInBanner onSignIn={openLogin} t={t} />
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
// Phase 7.8 ccg P0-2：StatStrip 文案 i18n 化
//   - "Hi, ${username}" → t('DASH.HI', { name }) 模板
//   - relativeTime 接 i18n.language 走 Intl.RelativeTimeFormat（支持 zh/en）
//   - "近 8 条" 在 ccg 报告里被指出 misleading（实际只是 logs[0..7]，不是真"近 8"），
//     文案改成 "近 N 条"明示数量
const StatStrip = ({ me, recentLogs, formatCurrency, t }) => {
  const { i18n } = useTranslation();
  const totalReqs = recentLogs.length;
  const totalTokens = recentLogs.reduce(
    (s, l) => s + (l.prompt_tokens || 0) + (l.completion_tokens || 0),
    0
  );
  const uniqueModels = new Set(recentLogs.map(l => l.model_name).filter(Boolean)).size;
  const lastTime = recentLogs[0]?.created_at;
  const lastRel = lastTime ? relativeTime(lastTime, i18n.resolvedLanguage || i18n.language) : '—';
  const recentHint = totalReqs > 0
    ? t('DASH.STAT_RECENT_N', { n: totalReqs, defaultValue: '近 {{n}} 条' })
    : t('DASH.STAT_NO_DATA', '暂无数据');

  return (
    <section className="fl-card grid grid-cols-2 md:grid-cols-4 divide-y md:divide-y-0 md:divide-x divide-outline-variant/30 overflow-hidden">
      <Stat
        label={t('DASH.STAT_BALANCE', '账户余额')}
        value={formatCurrency(me.quota ?? 0, 2)}
        hint={me.username}
        prominent
      />
      <Stat
        label={t('DASH.STAT_REQUESTS', '最近请求')}
        value={totalReqs.toLocaleString()}
        hint={recentHint}
      />
      <Stat
        label={t('DASH.STAT_TOKENS', 'Token 用量')}
        value={formatCompactNumber(totalTokens)}
        hint={recentHint}
      />
      <Stat
        label={t('DASH.STAT_LAST', '上次调用')}
        value={lastRel}
        hint={uniqueModels ? t('DASH.STAT_MODELS', { n: uniqueModels, defaultValue: '{{n}} 个模型' }) : '—'}
      />
    </section>
  );
};

// 登录中骨架 — 4-up 灰条占位，避免闪 PublicHero 营销 CTA
const StatStripSkeleton = () => (
  <section
    className="fl-card grid grid-cols-2 md:grid-cols-4 divide-y md:divide-y-0 md:divide-x divide-outline-variant/30 overflow-hidden"
    aria-hidden="true"
  >
    {[0, 1, 2, 3].map(i => (
      <div key={i} className="px-5 py-4">
        <div className="h-2.5 w-16 rounded bg-on-surface/[0.08] animate-pulse" />
        <div className={`mt-2 h-7 ${i === 0 ? 'w-28' : 'w-20'} rounded bg-on-surface/[0.10] animate-pulse`} />
        <div className="mt-2 h-2 w-20 rounded bg-on-surface/[0.06] animate-pulse" />
      </div>
    ))}
  </section>
);

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

// ─── SignInBanner ───────────────────────────────────────────────────────
// 未登录态：单行信息条 — 不卖产品、不渐变 logo、不双 CTA。
const SignInBanner = ({ onSignIn, t }) => (
  <section className="fl-card flex items-center gap-3 px-4 py-3">
    <span className="text-sm text-on-surface-variant">
      {t('DASH.SIGN_IN_HINT', '登录后可查看账户余额、用量统计与 API Token')}
    </span>
    <button
      type="button"
      onClick={onSignIn}
      className="ml-auto text-sm font-medium text-primary hover:underline"
    >
      {t('DASH.SIGN_IN_ACTION', '登录')}
    </button>
  </section>
);

// ─── AdminBanner ────────────────────────────────────────────────────────
// admin 登录态：admin 没有 quota/me，不应该显示 StatStrip 也不该显示 "请
// 登录"。给一个中性入口提示条，引导去管理控制台。
const AdminBanner = ({ onEnter, t }) => (
  <section className="fl-card flex items-center gap-3 px-4 py-3">
    <ShieldAlert size={16} className="text-on-surface-variant shrink-0" />
    <span className="text-sm text-on-surface-variant">
      {t('DASH.ADMIN_HINT', '当前为管理员模式，可前往管理控制台查看渠道、用户与计费')}
    </span>
    <button
      type="button"
      onClick={onEnter}
      className="ml-auto text-sm font-medium text-primary hover:underline"
    >
      {t('DASH.ADMIN_ENTER', '进入控制台')}
    </button>
  </section>
);

// 数字紧凑表示：1234 → 1.2k；1234567 → 1.23M
function formatCompactNumber(n) {
  if (!n) return '0';
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(2) + 'M';
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'k';
  return n.toLocaleString();
}

// Phase 7.8 ccg P0-2：relativeTime 用 Intl.RelativeTimeFormat（ICU 国际化）
// 之前硬编码中文 "刚刚 / N 分钟前 / N 天前" 在英文场景下时间戳依然中文，破坏多语言闭环。
// Intl.RelativeTimeFormat 浏览器原生支持，0 依赖，自动按 locale 输出 "5 minutes ago" /
// "5 分钟前" / "il y a 5 minutes" 等。
function relativeTime(ts, locale) {
  const t = new Date(ts).getTime();
  if (isNaN(t)) return '—';
  const diffSec = Math.round((t - Date.now()) / 1000); // 负数 = 过去
  const lang = locale || (typeof navigator !== 'undefined' ? navigator.language : 'en');
  const rtf = new Intl.RelativeTimeFormat(lang, { numeric: 'auto' });
  const abs = Math.abs(diffSec);
  if (abs < 60) return rtf.format(diffSec, 'second');
  if (abs < 3600) return rtf.format(Math.round(diffSec / 60), 'minute');
  if (abs < 86400) return rtf.format(Math.round(diffSec / 3600), 'hour');
  if (abs < 30 * 86400) return rtf.format(Math.round(diffSec / 86400), 'day');
  return new Date(ts).toLocaleDateString(lang);
}

// Phase 7.5+：撤掉 Phase 7 的横向滚动大色块卡（用户截图反馈"好难看"），改 Vercel 风
// 紧凑 grid + 横排极简卡：每张卡 ~72px 高，去掉饱和色 hero 渐变，brand 色仅作图标
// tint 出现，整体回归数据/控制台调性
const ProviderModelSection = ({ group, formatCurrency, onSeeAll, onModelClick }) => {
  const { t } = useTranslation();
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
            {t('DASH.SEE_ALL_N', { n: total, defaultValue: '查看全部 {{n}} 个' })}
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
  const { t } = useTranslation();
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
        {/* Phase 8：去 FREE 绿色高亮 chip（营销吸睛）→ 直接 $0.00/M 跟其他价
            一致呈现，由数字本身告诉用户"免费"，不需要装饰提示 */}
        <div className="font-mono text-[13px] font-semibold text-on-surface tabular-nums leading-tight">
          {formatCurrency(inPrice, 2)}
          <span className="text-on-surface-variant font-normal">/M</span>
        </div>
        {!isFree && outPrice > 0 && outPrice !== inPrice && (
          <div className="font-mono text-[10px] text-on-surface-variant tabular-nums">
            {t('DASH.OUT_PRICE', { price: formatCurrency(outPrice, 2), defaultValue: 'out {{price}}' })}
          </div>
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

export default Dashboard;
