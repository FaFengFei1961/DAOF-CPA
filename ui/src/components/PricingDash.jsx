import React, { useState, useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { Search, Server, Zap, Database, CheckCircle, HelpCircle } from 'lucide-react';
import { useCurrency } from '../context/CurrencyContext';
import { StorePage } from './store/StorePrimitives';

const PricingDash = () => {
    const { t } = useTranslation();
    const { formatCurrency } = useCurrency();
    const [models, setModels] = useState([]);
    const [loading, setLoading] = useState(true);
    const [searchTerm, setSearchTerm] = useState('');

    const formatTokens = (t) => {
        if (!t) return '0';
        if (t >= 1000000) return (t / 1000000) + 'M';
        if (t >= 1000) return (t / 1000) + 'K';
        return t;
    };

    useEffect(() => {
        const fetchPricing = async () => {
            try {
                const res = await fetch('/api/pricing');
                const data = await res.json();
                if (data.success) {
                    setModels(data.data || []);
                }
            } catch (error) {
                /* fetch error swallowed */;
            }
            setLoading(false);
        };
        fetchPricing();
    }, []);

    const filteredModels = models.filter(m => 
        m.model_id.toLowerCase().includes(searchTerm.toLowerCase())
    );

    return (
        <StorePage
            icon={Server}
            title={t('PRICING.TITLE') || '模型费率大盘'}
            subtitle={t('PRICING.DESC') || '这里展示了当前平台全网聚合的底层可用大语言模型池。平台会自动进行智能负载均衡调度，为您提供各个模型最低廉的基础开销单价和最佳的并发通道保障。'}
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
                                <th className="p-4 text-xs font-semibold text-on-surface-variant uppercase tracking-wider text-center">
                                    {t('PRICING.COL_PATHS') || 'Health Paths'}
                                </th>
                            </tr>
                        </thead>
                        <tbody className="divide-y divide-[#2b2b2b]">
                            {loading ? (
                                <tr>
                                    <td colSpan="6" className="p-12 text-center text-on-surface-variant">
                                        <div className="flex flex-col items-center justify-center space-y-3">
                                            <Database className="animate-pulse" size={32} />
                                            <span>{t('PRICING.LOADING') || '聚合全网模型算力中...'}</span>
                                        </div>
                                    </td>
                                </tr>
                            ) : filteredModels.length === 0 ? (
                                <tr>
                                    <td colSpan="6" className="p-12 text-center text-on-surface-variant">
                                        {searchTerm ? t('PRICING.NOT_FOUND') || '未找到该模型' : t('PRICING.EMPTY') || '暂无可用模型'}
                                    </td>
                                </tr>
                            ) : (
                                filteredModels.map((m, idx) => (
                                    <tr key={`${m.model_id}-${idx}`} className="hover:bg-[#25262c]  group">
                                        <td className="p-4">
                                            <div className="flex items-center gap-3">
                                                <div className="p-2 bg-surface-variant rounded-lg group-hover:bg-[#3b3b3b] ">
                                                    <Zap size={16} className="text-yellow-400" />
                                                </div>
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
                                            {m.min_cache_price > 0 ? formatCurrency(m.min_cache_price, 4) : '-'}
                                        </td>
                                        <td className="p-4 text-center">
                                            <span className="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-full text-xs font-medium bg-[#0e4429] text-[#39d353] border border-[#26a641]">
                                                <CheckCircle size={12} />
                                                {m.available_paths} {t('PRICING.ACTIVE_NODES') || 'Nodes'}
                                            </span>
                                        </td>
                                    </tr>
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

export default PricingDash;
