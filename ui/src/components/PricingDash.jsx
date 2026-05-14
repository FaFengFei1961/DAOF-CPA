import React, { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Search, Server, Database, HelpCircle } from 'lucide-react';
import { useCurrency } from '../context/CurrencyContext';
import { StorePage } from './store/StorePrimitives';
import { groupModelsByProvider, inferModelProvider } from '../utils/modelProviders';
import { usePublicPricing } from '../hooks/usePublicPricing';
import BillingRulesPanel from './BillingRulesPanel';

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

    const filteredModels = models.filter(m =>
        m.model_id.toLowerCase().includes(searchTerm.toLowerCase())
    );
    const providerGroups = groupModelsByProvider(filteredModels);

    return (
            <StorePage
            icon={Server}
            title={t('PRICING.TITLE') || '模型费率大盘'}
            subtitle={t('PRICING.DESC') || '这里展示当前可用模型与公开计费费率。'}
        >
            <div className="fl-card flex flex-col sm:flex-row gap-4 items-center justify-between p-4">
                <div className="relative w-full sm:w-96">
                    <Search className="absolute left-3 top-1/2 -translate-y-1/2 text-on-surface-variant" size={18} />
                    <input
                        type="text"
                        placeholder={t('PRICING.SEARCH_PLACEHOLDER') || '搜索模型名称 (如 gpt-4, claude)...'}
                        value={searchTerm}
                        onChange={(e) => setSearchTerm(e.target.value)}
                        className="w-full bg-surface-container-high border border-outline-variant text-on-surface text-sm rounded-lg focus:ring-1 focus:ring-blue-500 focus:border-primary block p-2.5 pl-10 "
                    />
                </div>
            </div>

            <BillingRulesPanel />

            <div className="bg-surface border border-outline-variant rounded-2xl overflow-hidden shadow-xl">
                <div className="overflow-x-auto">
                    <table className="w-full text-left border-collapse">
                        <thead>
                            <tr className="bg-surface-container-high border-b border-outline-variant">
                                <th className="p-4 text-xs font-semibold text-on-surface-variant uppercase tracking-wider">
                                    {t('PRICING.COL_MODEL') || 'Model Identifier'}
                                </th>
                                <th className="p-4 text-xs font-semibold text-on-surface-variant uppercase tracking-wider text-center w-[120px]">
                                    {t('PRICING.COL_MAX_CTX')}
                                </th>
                                <th className="p-4 text-xs font-semibold text-on-surface-variant uppercase tracking-wider text-right">
                                    {t('PRICING.COL_INPUT') || 'Input Price ($/1M)'}
                                </th>
                                <th className="p-4 text-xs font-semibold text-on-surface-variant uppercase tracking-wider text-right">
                                    {t('PRICING.COL_OUTPUT') || 'Output Price ($/1M)'}
                                </th>
                                <th className="p-4 text-xs font-semibold text-on-surface-variant uppercase tracking-wider text-right">
                                    {t('PRICING.COL_CACHE') || 'Cached ($/1M)'}
                                </th>
                            </tr>
                        </thead>
                        <tbody className="divide-y divide-[#2b2b2b]">
                            {loading ? (
                                <tr>
                                    <td colSpan="5" className="p-12 text-center text-on-surface-variant">
                                        <div className="flex flex-col items-center justify-center space-y-3">
                                            <Database className="animate-pulse" size={32} />
                                            <span>{t('PRICING.LOADING') || '聚合全网模型算力中...'}</span>
                                        </div>
                                    </td>
                                </tr>
                            ) : filteredModels.length === 0 ? (
                                <tr>
                                    <td colSpan="5" className="p-12 text-center text-on-surface-variant">
                                        {searchTerm ? t('PRICING.NOT_FOUND') || '未找到该模型' : t('PRICING.EMPTY') || '暂无可用模型'}
                                    </td>
                                </tr>
                            ) : (
                                providerGroups.map(group => (
                                    <React.Fragment key={group.provider.name}>
                                        <tr className="bg-surface-container/70">
                                            <td colSpan="5" className="px-4 py-3">
                                                <div className="flex items-center gap-2">
                                                    <ProviderIcon provider={group.provider} />
                                                    <span className="text-sm font-semibold text-on-surface">{group.provider.name}</span>
                                                </div>
                                            </td>
                                        </tr>
                                        {group.items.map((m, idx) => (
                                            <PricingRow
                                                key={`${m.model_id}-${idx}`}
                                                model={m}
                                                provider={inferModelProvider(m.model_id)}
                                                formatCurrency={formatCurrency}
                                                formatTokens={formatTokens}
                                                t={t}
                                            />
                                        ))}
                                    </React.Fragment>
                                ))
                            )}
                        </tbody>
                    </table>
                </div>
            </div>
            
            <div className="fl-card p-4 flex gap-3 text-on-surface-variant text-sm">
                <HelpCircle className="mt-0.5 text-primary shrink-0" size={18} />
                <p>
                    {t('PRICING.FOOTER_HINT') || '费率均以 $ USD 每 100 万 (1M) Tokens 计算。当前系统智能汇率与计价单位可能在个人偏好设置中全局转换，但本质底座均按此标准比例消耗您的钱包余额。'}
                </p>
            </div>
        </StorePage>
    );
};

const ProviderIcon = ({ provider }) => {
    const Icon = provider.icon;
    return (
        <span
            className="w-7 h-7 rounded-lg flex items-center justify-center border"
            style={{
                background: hexA(provider.hue, 0.14),
                borderColor: hexA(provider.hue, 0.24),
            }}
        >
            <Icon size={15} style={{ color: provider.hue }} />
        </span>
    );
};

const PricingRow = ({ model: m, provider, formatCurrency, formatTokens, t }) => (
    <tr className="hover:bg-[#25262c] group">
        <td className="p-4">
            <div className="flex items-center gap-3">
                <ProviderIcon provider={provider} />
                <span className="font-mono text-sm font-semibold text-gray-200">
                    {m.model_id}
                </span>
            </div>
        </td>
        <td className="p-4 text-center">
            {m.max_context_length > 0 ? (
                <span className="text-xs font-medium bg-[#1a1b1e] text-indigo-400 border border-indigo-500/20 px-2 py-0.5 rounded-md shadow-sm">
                    {formatTokens(m.max_context_length)}
                </span>
            ) : (
                <span className="text-xs text-outline-variant">-</span>
            )}
        </td>
        <td className="p-4">
            <div className="flex flex-col items-end gap-1.5">
                <div className="font-mono text-sm tracking-tight text-blue-400">
                    {formatCurrency(m.min_input_price, 4)}
                </div>
                {m.context_threshold > 0 && (
                    <div className="flex items-center gap-1.5 group cursor-help" title={t('PRICING.LONG_CONTEXT_HINT', { threshold: m.context_threshold })}>
                        <span className="text-xs font-medium bg-gradient-to-r from-amber-500/20 to-orange-500/20 text-amber-500 rounded px-1.5 py-0.5 border border-amber-500/30 shadow-sm">{`> ${formatTokens(m.context_threshold)} `}{t('PRICING.TIER_TAG') || '阶梯'}</span>
                        <span className="font-mono text-xs text-amber-500/90 tracking-tight">{formatCurrency(m.min_high_in_price, 4)}</span>
                    </div>
                )}
            </div>
        </td>
        <td className="p-4">
            <div className="flex flex-col items-end gap-1.5">
                <div className="font-mono text-sm tracking-tight text-purple-400">
                    {formatCurrency(m.min_output_price, 4)}
                </div>
                {m.context_threshold > 0 && (
                    <div className="flex items-center gap-1.5 group cursor-help" title={t('PRICING.LONG_CONTEXT_HINT', { threshold: m.context_threshold })}>
                        <span className="text-xs font-medium bg-gradient-to-r from-amber-500/20 to-orange-500/20 text-amber-500 rounded px-1.5 py-0.5 border border-amber-500/30 shadow-sm">{`> ${formatTokens(m.context_threshold)} `}{t('PRICING.TIER_TAG') || '阶梯'}</span>
                        <span className="font-mono text-xs text-amber-500/90 tracking-tight">{formatCurrency(m.min_high_out_price, 4)}</span>
                    </div>
                )}
            </div>
        </td>
        <td className="p-4 text-right font-mono text-sm tracking-tight text-emerald-400">
            <div className="flex flex-col items-end gap-1.5">
                <div>{m.min_cache_price > 0 ? formatCurrency(m.min_cache_price, 4) : '-'}</div>
                {m.context_threshold > 0 && m.min_high_cache_price > 0 && (
                    <div className="flex items-center gap-1.5 group cursor-help" title={t('PRICING.LONG_CONTEXT_HINT', { threshold: m.context_threshold })}>
                        <span className="text-xs font-medium bg-gradient-to-r from-amber-500/20 to-orange-500/20 text-amber-500 rounded px-1.5 py-0.5 border border-amber-500/30 shadow-sm">{`> ${formatTokens(m.context_threshold)} `}{t('PRICING.TIER_TAG') || '阶梯'}</span>
                        <span className="font-mono text-xs text-amber-500/90 tracking-tight">{formatCurrency(m.min_high_cache_price, 4)}</span>
                    </div>
                )}
                {m.min_cache_write_price > 0 && (
                    <div className="font-mono text-xs text-orange-400/90 tracking-tight">
                        {t('PRICING.CACHE_WRITE_5M', '写入5m')}: {formatCurrency(m.min_cache_write_price, 4)}
                    </div>
                )}
                {m.min_cache_write_1h_price > 0 && (
                    <div className="font-mono text-xs text-orange-300/90 tracking-tight">
                        {t('PRICING.CACHE_WRITE_1H', '写入1h')}: {formatCurrency(m.min_cache_write_1h_price, 4)}
                    </div>
                )}
            </div>
        </td>
    </tr>
);

function hexA(hex, alpha) {
    if (!hex || hex[0] !== '#') return `rgba(124, 92, 255, ${alpha})`;
    const m = hex.match(/^#([0-9a-f]{6})$/i);
    if (!m) return `rgba(124, 92, 255, ${alpha})`;
    const n = parseInt(m[1], 16);
    return `rgba(${(n >> 16) & 255}, ${(n >> 8) & 255}, ${n & 255}, ${alpha})`;
}

export default PricingDash;
