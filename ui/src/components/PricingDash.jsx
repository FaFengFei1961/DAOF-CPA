import React, { useMemo, useState } from 'react';
import { Search, Server } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import BillingRulesPanel from './BillingRulesPanel';
import { useCurrency } from '../context/CurrencyContext';
import { usePublicPricing } from '../hooks/usePublicPricing';
import { StorePage } from './store/StorePrimitives';
import { brandFor, groupModelsByProviderAndFamily, hexA } from '../utils/modelProviders';

const PricingDash = () => {
  const { t } = useTranslation();
  const { formatCurrency } = useCurrency();
  const [searchTerm, setSearchTerm] = useState('');
  const { models, loading, error } = usePublicPricing();

  const filteredModels = useMemo(() => {
    const q = searchTerm.trim().toLowerCase();
    if (!q) return models;
    return models.filter((m) => String(m.model_id || '').toLowerCase().includes(q));
  }, [models, searchTerm]);

  const providerGroups = useMemo(
    () => groupModelsByProviderAndFamily(filteredModels),
    [filteredModels],
  );

  return (
    <StorePage
      icon={Server}
      title={t('PRICING.TITLE', '模型与定价')}
      subtitle={t('PRICING.DESC', '当前可用模型与公开计费费率。')}
    >
      <BillingRulesPanel />

      <div className="fl-card flex flex-col sm:flex-row gap-4 items-center justify-between p-4">
        <div className="relative w-full sm:w-96">
          <Search className="absolute left-3 top-1/2 -translate-y-1/2 text-on-surface-variant" size={18} />
          <input
            type="text"
            placeholder={t('PRICING.SEARCH_PLACEHOLDER', '搜索模型名称 (如 gpt-4, claude)...')}
            value={searchTerm}
            onChange={(e) => setSearchTerm(e.target.value)}
            className="w-full bg-surface-container-high border border-outline-variant text-on-surface text-sm rounded-control focus:ring-1 focus:ring-primary focus:border-primary block p-2.5 pl-10"
          />
        </div>
        <div className="text-xs text-on-surface-variant whitespace-nowrap">
          {t('PRICING.RESULT_COUNT', '{{count}} 个模型', { count: filteredModels.length })}
        </div>
      </div>

      {error && (
        <div className="rounded-overlay border border-error/40 bg-error/10 px-4 py-3 text-sm text-error">
          {t('PRICING.LOAD_FAIL', '价格列表加载失败，请稍后重试。')}
        </div>
      )}

      {loading && filteredModels.length === 0 ? (
        <PricingSkeleton />
      ) : providerGroups.length === 0 ? (
        <div className="fl-card p-12 text-center text-sm text-on-surface-variant">
          {searchTerm ? t('PRICING.NOT_FOUND', '未查找到对应模型') : t('PRICING.EMPTY', '暂无可用模型接入')}
        </div>
      ) : (
        <div className="space-y-4">
          {providerGroups.map((group) => (
            <ProviderPricingSection
              key={group.provider.name}
              group={group}
              formatCurrency={formatCurrency}
              t={t}
            />
          ))}
        </div>
      )}
    </StorePage>
  );
};

const ProviderPricingSection = ({ group, formatCurrency, t }) => {
  const Icon = group.provider.icon;

  return (
    <section className="rounded-overlay border border-outline-variant bg-surface-container overflow-hidden">
      <header
        className="px-4 sm:px-5 py-4 border-b border-outline-variant/50 flex flex-col sm:flex-row sm:items-center sm:justify-between gap-3"
        style={{
          background: `linear-gradient(90deg, ${hexA(group.provider.hue, 0.14)} 0%, transparent 62%)`,
        }}
      >
        <div className="flex items-center gap-3 min-w-0">
          <ProviderIcon provider={group.provider} />
          <div className="min-w-0">
            <div className="flex items-center gap-2 flex-wrap">
              <h2 className="text-base font-semibold text-on-surface">{group.provider.name}</h2>
              <span className="fl-brand-chip" data-brand={brandFor(group.provider.name)}>
                {t('PRICING.PROVIDER_COUNT', '{{count}} 个模型', { count: group.items.length })}
              </span>
            </div>
            <p className="text-xs text-on-surface-variant mt-0.5">
              {t('PRICING.FAMILY_COUNT', '{{count}} 个系列', { count: group.families.length })}
            </p>
          </div>
        </div>
        <Icon size={22} style={{ color: group.provider.hue }} className="hidden sm:block opacity-70" />
      </header>

      <div className="divide-y divide-outline-variant/50">
        {group.families.map((familyGroup) => (
          <FamilyPriceGroup
            key={familyGroup.family.key}
            familyGroup={familyGroup}
            formatCurrency={formatCurrency}
            t={t}
          />
        ))}
      </div>
    </section>
  );
};

const FamilyPriceGroup = ({ familyGroup, formatCurrency, t }) => (
  <section>
    <div className="px-4 sm:px-5 py-2.5 bg-surface-container-high/45 flex items-center justify-between gap-3">
      <h3 className="text-xs font-semibold uppercase tracking-wider text-on-surface-variant">
        {familyGroup.family.name}
      </h3>
      <span className="text-[11px] text-on-surface-variant tabular-nums">
        {t('PRICING.RESULT_COUNT', '{{count}} 个模型', { count: familyGroup.items.length })}
      </span>
    </div>

    <div className="hidden lg:grid grid-cols-[minmax(260px,2fr)_110px_minmax(120px,1fr)_minmax(120px,1fr)_minmax(150px,1fr)_minmax(180px,1.35fr)] gap-3 px-4 sm:px-5 py-2 border-y border-outline-variant/35 bg-surface-container/40 text-[11px] font-semibold uppercase tracking-wider text-on-surface-variant">
      <div>{t('PRICING.COL_MODEL', '模型标识')}</div>
      <div>{t('PRICING.COL_MAX_CTX', '最大上下文')}</div>
      <div className="text-right">{t('PRICING.COL_INPUT', '输入单价 ($/1M)')}</div>
      <div className="text-right">{t('PRICING.COL_OUTPUT', '输出单价 ($/1M)')}</div>
      <div className="text-right">{t('PRICING.COL_CACHE', '缓存命中 ($/1M)')}</div>
      <div>{t('PRICING.COL_NOTES', '阶梯 / 缓存写入')}</div>
    </div>

    <div className="divide-y divide-outline-variant/30">
      {familyGroup.items.map((model) => (
        <ModelPriceRow
          key={model.model_id}
          model={model}
          formatCurrency={formatCurrency}
          t={t}
        />
      ))}
    </div>
  </section>
);

const ModelPriceRow = ({ model, formatCurrency, t }) => (
  <div className="grid grid-cols-1 lg:grid-cols-[minmax(260px,2fr)_110px_minmax(120px,1fr)_minmax(120px,1fr)_minmax(150px,1fr)_minmax(180px,1.35fr)] gap-3 px-4 sm:px-5 py-3.5 hover:bg-surface-container-high/45 transition-colors">
    <div className="min-w-0">
      <div className="font-mono text-sm font-semibold text-on-surface break-all">
        {model.model_id}
      </div>
      <div className="mt-1 flex flex-wrap gap-1.5 lg:hidden">
        <SmallBadge label={t('PRICING.COL_MAX_CTX', '最大上下文')} value={formatContext(model.max_context_length)} />
      </div>
    </div>

    <PriceColumn
      label={t('PRICING.COL_MAX_CTX', '最大上下文')}
      value={formatContext(model.max_context_length)}
      muted={!model.max_context_length}
    />
    <PriceColumn
      align="right"
      label={t('PRICING.COL_INPUT', '输入单价 ($/1M)')}
      value={formatPrice(model.min_input_price, formatCurrency)}
    />
    <PriceColumn
      align="right"
      label={t('PRICING.COL_OUTPUT', '输出单价 ($/1M)')}
      value={formatPrice(model.min_output_price, formatCurrency)}
    />
    <PriceColumn
      align="right"
      label={t('PRICING.COL_CACHE', '缓存命中 ($/1M)')}
      value={formatPrice(model.min_cache_price, formatCurrency)}
      muted={!isPositive(model.min_cache_price)}
    />
    <RateNotes model={model} formatCurrency={formatCurrency} t={t} />
  </div>
);

const PriceColumn = ({ label, value, align = 'left', muted = false }) => (
  <div className={`min-w-0 ${align === 'right' ? 'lg:text-right' : ''}`}>
    <div className="lg:hidden text-[10px] font-semibold uppercase tracking-wider text-on-surface-variant mb-0.5">
      {label}
    </div>
    <div className={`font-mono text-sm tabular-nums ${muted ? 'text-outline' : 'text-on-surface'}`}>
      {value}
    </div>
  </div>
);

const RateNotes = ({ model, formatCurrency, t }) => {
  const notes = [];
  const threshold = Number(model.context_threshold) || 0;

  if (threshold > 0 && (isPositive(model.min_high_in_price) || isPositive(model.min_high_out_price) || isPositive(model.min_high_cache_price))) {
    notes.push({
      key: 'tier',
      text: t('PRICING.LONG_TIER_COMPACT', '>{{threshold}}：输入 {{input}} / 输出 {{output}}', {
        threshold: formatContext(threshold),
        input: formatPrice(model.min_high_in_price, formatCurrency),
        output: formatPrice(model.min_high_out_price, formatCurrency),
      }),
      title: t('PRICING.LONG_CONTEXT_HINT', '超过 {{threshold}} 上下文时触发的阶梯费率。', { threshold: formatContext(threshold) }),
      tone: 'warning',
    });
    if (isPositive(model.min_high_cache_price)) {
      notes.push({
        key: 'tier-cache',
        text: t('PRICING.LONG_TIER_CACHE', '阶梯缓存 {{cache}}', {
          cache: formatPrice(model.min_high_cache_price, formatCurrency),
        }),
        title: t('PRICING.LONG_CONTEXT_HINT', '超过 {{threshold}} 上下文时触发的阶梯费率。', { threshold: formatContext(threshold) }),
        tone: 'warning',
      });
    }
  }

  if (isPositive(model.min_cache_write_price)) {
    notes.push({
      key: 'cache-write-5m',
      text: `${t('PRICING.CACHE_WRITE_5M', '写入5m')} ${formatPrice(model.min_cache_write_price, formatCurrency)}`,
      tone: 'neutral',
    });
  }

  if (isPositive(model.min_cache_write_1h_price)) {
    notes.push({
      key: 'cache-write-1h',
      text: `${t('PRICING.CACHE_WRITE_1H', '写入1h')} ${formatPrice(model.min_cache_write_1h_price, formatCurrency)}`,
      tone: 'neutral',
    });
  }

  return (
    <div className="min-w-0">
      <div className="lg:hidden text-[10px] font-semibold uppercase tracking-wider text-on-surface-variant mb-1">
        {t('PRICING.COL_NOTES', '阶梯 / 缓存写入')}
      </div>
      {notes.length > 0 ? (
        <div className="flex flex-wrap gap-1.5">
          {notes.map((note) => (
            <span
              key={note.key}
              title={note.title}
              className={`inline-flex max-w-full items-center rounded-control border px-2 py-0.5 text-[11px] font-medium ${
                note.tone === 'warning'
                  ? 'border-warning/30 bg-warning/15 text-warning'
                  : 'border-outline-variant/60 bg-surface-container-high text-on-surface-variant'
              }`}
            >
              <span className="truncate">{note.text}</span>
            </span>
          ))}
        </div>
      ) : (
        <span className="text-xs text-outline">{t('PRICING.NO_EXTRA_RATE', '无额外阶梯')}</span>
      )}
    </div>
  );
};

const ProviderIcon = ({ provider }) => {
  const Icon = provider.icon;
  return (
    <span
      className="w-9 h-9 rounded-control flex items-center justify-center border shrink-0"
      style={{
        background: hexA(provider.hue, 0.14),
        borderColor: hexA(provider.hue, 0.24),
      }}
    >
      <Icon size={18} style={{ color: provider.hue }} />
    </span>
  );
};

const SmallBadge = ({ label, value }) => (
  <span className="inline-flex items-center gap-1 rounded-control border border-outline-variant/60 bg-surface-container-high px-2 py-0.5 text-[11px] text-on-surface-variant">
    <span>{label}</span>
    <span className="font-mono text-on-surface">{value}</span>
  </span>
);

const PricingSkeleton = () => (
  <div className="space-y-4">
    {Array.from({ length: 2 }).map((_, sectionIndex) => (
      <div key={sectionIndex} className="rounded-overlay border border-outline-variant bg-surface-container overflow-hidden animate-pulse">
        <div className="px-5 py-4 border-b border-outline-variant/50 flex items-center gap-3">
          <div className="w-9 h-9 rounded-control bg-surface-container-high" />
          <div className="space-y-2">
            <div className="h-4 w-28 rounded-control bg-surface-container-high" />
            <div className="h-3 w-20 rounded-control bg-surface-container-high" />
          </div>
        </div>
        <div className="p-5 space-y-3">
          {Array.from({ length: 3 }).map((__, rowIndex) => (
            <div key={rowIndex} className="h-12 rounded-control bg-surface-container-high" />
          ))}
        </div>
      </div>
    ))}
  </div>
);

const isPositive = (value) => Number(value) > 0;

const formatPrice = (value, formatCurrency) => (
  isPositive(value) ? formatCurrency(Number(value), 4) : '-'
);

const formatContext = (value) => {
  const n = Number(value) || 0;
  if (n <= 0) return '-';
  if (n >= 1000000) return `${trimNumber(n / 1000000)}M`;
  if (n >= 1000) return `${trimNumber(n / 1000)}K`;
  return String(n);
};

const trimNumber = (value) => (
  Number.isInteger(value) ? String(value) : value.toFixed(1).replace(/\.0$/, '')
);

export default PricingDash;
