import React, { useState, useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { Search, Server, Database, ChevronRight } from 'lucide-react';
import { useCurrency } from '../context/CurrencyContext';
import { StorePage } from './store/StorePrimitives';
import { groupModelsByProvider, inferModelProvider, brandFor, hexA } from '../utils/modelProviders';
import { usePublicPricing } from '../hooks/usePublicPricing';
import BillingRulesPanel from './BillingRulesPanel';
import DataTable from './ui/DataTable';

const PricingDash = () => {
    const { t } = useTranslation();
    const { formatCurrency } = useCurrency();
    const [searchTerm, setSearchTerm] = useState('');
    const { models, loading } = usePublicPricing();

    const formatTokens = (t) => {
        if (!t) return '0';
        if (t >= 1000000) return (t / 1000000) + 'M';
        if (t >= 1000) return (t / 1000) + 'K';
        return t;
    };

    // Cache filter and grouping work so high-frequency search input stays smooth.
    const filteredModels = useMemo(() => {
        const q = searchTerm.toLowerCase();
        return q ? models.filter(m => m.model_id.toLowerCase().includes(q)) : models;
    }, [models, searchTerm]);
    const providerGroups = useMemo(() => groupModelsByProvider(filteredModels), [filteredModels]);

    return (
            <StorePage
            icon={Server}
            title={t('PRICING.TITLE', '模型费率大盘')}
            subtitle={t('PRICING.DESC', '这里展示当前可用模型与公开计费费率。')}
        >
            <div className="fl-card flex flex-col sm:flex-row gap-4 items-center justify-between p-4">
                <div className="relative w-full sm:w-96">
                    <Search className="absolute left-3 top-1/2 -translate-y-1/2 text-on-surface-variant" size={18} />
                    <input
                        type="text"
                        placeholder={t('PRICING.SEARCH_PLACEHOLDER', '搜索模型名称 (如 gpt-4, claude)...')}
                        value={searchTerm}
                        onChange={(e) => setSearchTerm(e.target.value)}
                        className="w-full bg-surface-container-high border border-outline-variant text-on-surface text-sm rounded-control focus:ring-1 focus:ring-primary focus:border-primary block p-2.5 pl-10 "
                    />
                </div>
            </div>

            <BillingRulesPanel />

            {/* Use the shared table shell instead of page-local table styling. */}
            <div className="fl-table-shell">
                <div className="fl-table-scroll">
                    
                    <DataTable
                        edgeGradient={false}
                        columns={[
                            { key: 'model', header: t('PRICING.COL_MODEL', 'Model'), render: row => {
                                if (row.isGroup) {
                                    return (
                                        <div className="flex items-center gap-2.5">
                                            <ProviderIcon provider={row.provider} />
                                            <span className="text-sm font-semibold text-on-surface">{row.provider.name}</span>
                                            <span className="fl-brand-chip" data-brand={brandFor(row.provider.name)}>{row.items.length}</span>
                                            <ChevronRight size={16} className="text-on-surface-variant ml-auto" />
                                        </div>
                                    );
                                }
                                return (
                                    <div className="flex items-center gap-3">
                                        <ProviderIcon provider={row.providerObj} />
                                        <span className="font-mono text-sm font-semibold text-on-surface">
                                            {row.m.model_id}
                                        </span>
                                    </div>
                                );
                            }},
                            { key: 'max_ctx', header: t('PRICING.COL_MAX_CTX', 'Max Context'), align: 'center', width: 120, render: row => {
                                if (row.isGroup) return null;
                                return row.m.max_context_length > 0 ? (
                                    <span className="text-xs font-medium bg-surface-container text-on-surface-variant border border-outline-variant/60 px-2 py-0.5 rounded-control font-mono">
                                        {formatTokens(row.m.max_context_length)}
                                    </span>
                                ) : (
                                    <span className="text-xs text-outline-variant">-</span>
                                );
                            }},
                            { key: 'input', header: t('PRICING.COL_INPUT', 'Input ($/1M)'), align: 'right', render: row => {
                                if (row.isGroup) return null;
                                return (
                                    <div className="flex flex-col items-end gap-1.5">
                                        <div className="font-mono text-sm tabular-nums text-on-surface">
                                            {formatCurrency(row.m.min_input_price, 4)}
                                        </div>
                                        {row.m.context_threshold > 0 && (
                                            <div className="flex items-center gap-1.5 cursor-help" title={t('PRICING.LONG_CONTEXT_HINT', { threshold: row.m.context_threshold })}>
                                                <span className="text-[10px] font-medium bg-warning/15 text-warning rounded-control px-1.5 py-0.5 border border-warning/30">{`>${formatTokens(row.m.context_threshold)} `}{t('PRICING.TIER_TAG', '阶梯')}</span>
                                                <span className="font-mono text-xs tabular-nums text-warning/90">{formatCurrency(row.m.min_high_in_price, 4)}</span>
                                            </div>
                                        )}
                                    </div>
                                );
                            }},
                            { key: 'output', header: t('PRICING.COL_OUTPUT', 'Output ($/1M)'), align: 'right', render: row => {
                                if (row.isGroup) return null;
                                return (
                                    <div className="flex flex-col items-end gap-1.5">
                                        <div className="font-mono text-sm tabular-nums text-on-surface">
                                            {formatCurrency(row.m.min_output_price, 4)}
                                        </div>
                                        {row.m.context_threshold > 0 && (
                                            <div className="flex items-center gap-1.5 cursor-help" title={t('PRICING.LONG_CONTEXT_HINT', { threshold: row.m.context_threshold })}>
                                                <span className="text-[10px] font-medium bg-warning/15 text-warning rounded-control px-1.5 py-0.5 border border-warning/30">{`>${formatTokens(row.m.context_threshold)} `}{t('PRICING.TIER_TAG', '阶梯')}</span>
                                                <span className="font-mono text-xs tabular-nums text-warning/90">{formatCurrency(row.m.min_high_out_price, 4)}</span>
                                            </div>
                                        )}
                                    </div>
                                );
                            }},
                            { key: 'cache', header: t('PRICING.COL_CACHE', 'Cached ($/1M)'), align: 'right', render: row => {
                                if (row.isGroup) return null;
                                return (
                                    <div className="flex flex-col items-end gap-1.5">
                                        <div className="font-mono text-sm tabular-nums text-on-surface">{row.m.min_cache_price > 0 ? formatCurrency(row.m.min_cache_price, 4) : '-'}</div>
                                        {row.m.context_threshold > 0 && row.m.min_high_cache_price > 0 && (
                                            <div className="flex items-center gap-1.5 cursor-help" title={t('PRICING.LONG_CONTEXT_HINT', { threshold: row.m.context_threshold })}>
                                                <span className="text-[10px] font-medium bg-warning/15 text-warning rounded-control px-1.5 py-0.5 border border-warning/30">{`>${formatTokens(row.m.context_threshold)} `}{t('PRICING.TIER_TAG', '阶梯')}</span>
                                                <span className="font-mono text-xs tabular-nums text-warning/90">{formatCurrency(row.m.min_high_cache_price, 4)}</span>
                                            </div>
                                        )}
                                        {row.m.min_cache_write_price > 0 && (
                                            <div className="font-mono text-[11px] tabular-nums text-on-surface-variant">
                                                {t('PRICING.CACHE_WRITE_5M', '写入5m')}: {formatCurrency(row.m.min_cache_write_price, 4)}
                                            </div>
                                        )}
                                        {row.m.min_cache_write_1h_price > 0 && (
                                            <div className="font-mono text-[11px] tabular-nums text-on-surface-variant">
                                                {t('PRICING.CACHE_WRITE_1H', '写入1h')}: {formatCurrency(row.m.min_cache_write_1h_price, 4)}
                                            </div>
                                        )}
                                    </div>
                                );
                            }}
                        ]}
                        rows={providerGroups.flatMap(group => [
                            { isGroup: true, provider: group.provider, items: group.items },
                            ...group.items.map(m => ({ m, providerObj: inferModelProvider(m.model_id) }))
                        ])}
                        rowKey={row => row.isGroup ? `group-${row.provider.name}` : row.m.model_id}
                        loading={loading}
                        emptyTitle={searchTerm ? t('PRICING.NOT_FOUND', '未找到该模型') : t('PRICING.EMPTY', '暂无可用模型')}
                    />

                </div>
            </div>

            {/* Billing rules are already explained above by BillingRulesPanel. */}
        </StorePage>
    );
};

const ProviderIcon = ({ provider }) => {
    const Icon = provider.icon;
    return (
        <span
            className="w-7 h-7 rounded-control flex items-center justify-center border"
            style={{
                background: hexA(provider.hue, 0.14),
                borderColor: hexA(provider.hue, 0.24),
            }}
        >
            <Icon size={15} style={{ color: provider.hue }} />
        </span>
    );
};

export default PricingDash;
