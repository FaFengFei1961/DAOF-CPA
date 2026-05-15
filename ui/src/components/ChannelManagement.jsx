import React, { useState, useEffect, useMemo, useRef } from 'react';
import { useTranslation, Trans } from 'react-i18next';
import { Search, Plus, Edit2, Trash2, Server, Save, X, RefreshCw, AlertTriangle, ArrowLeft, Network, Box } from 'lucide-react';
import { useCurrency } from '../context/CurrencyContext';
import toast from 'react-hot-toast';
import { authFetch } from '../utils/authFetch';
import { useConfirm } from '../context/ConfirmContext';
import { useModalA11y } from '../hooks/useModalA11y';

const ChannelManagement = () => {
    const confirm = useConfirm();
    const { t } = useTranslation();
    const { exchangeRate, formatCurrency } = useCurrency();
    const formatTokens = (t) => {
        if (!t) return '0';
        if (t >= 1000000) return (t / 1000000) + 'M';
        if (t >= 1000) return (t / 1000) + 'K';
        return t;
    };
    const [inputCurrency, setInputCurrency] = useState('USD');
    const [channels, setChannels] = useState([]);
    const [loading, setLoading] = useState(true);
    const [searchTerm, setSearchTerm] = useState('');

    // View state: 'channels' | 'models'
    const [view, setView] = useState('channels');
    const [selectedChannel, setSelectedChannel] = useState(null);

    // Channels Modal State
    const [isChanModalOpen, setIsChanModalOpen] = useState(false);
    const [currentChannel, setCurrentChannel] = useState(null);
    const [isSubmitting, setIsSubmitting] = useState(false);

    // Models List State
    const [channelModels, setChannelModels] = useState([]);
    const [loadingModels, setLoadingModels] = useState(false);
    const [modelSearchTerm, setModelSearchTerm] = useState('');

    // Models Modal State
    const [isModelModalOpen, setIsModelModalOpen] = useState(false);
    const [currentModel, setCurrentModel] = useState(null);

    // Upstream Fetch State
    const [isUpstreamModalOpen, setIsUpstreamModalOpen] = useState(false);
    const [upstreamModels, setUpstreamModels] = useState([]);
    const [loadingUpstream, setLoadingUpstream] = useState(false);
    const [selectedUpstreamModels, setSelectedUpstreamModels] = useState([]);

    // fix MAJOR F2（gemini 第二十一轮）：upstreamModels reduce + sort 原本在 render 内每次都重算
    // （甚至弹窗开关也触发重 render）。当 upstream 模型 100+ 时是 O(N log N) 的不必要开销。
    // 用 useMemo 缓存按首字母分组排序的结果，仅在 upstreamModels 变化时重建。
    const upstreamModelsByLetter = useMemo(() => {
        if (!Array.isArray(upstreamModels) || upstreamModels.length === 0) return [];
        const grouped = upstreamModels.reduce((acc, modelId) => {
            const firstChar = (modelId[0] || '?').toUpperCase();
            if (!acc[firstChar]) acc[firstChar] = [];
            acc[firstChar].push(modelId);
            return acc;
        }, {});
        return Object.entries(grouped).sort(([a], [b]) => a.localeCompare(b));
    }, [upstreamModels]);

    const initChanForm = { type: 'cliproxy', name: '', key: '', base_url: '', proxy_url: '', headers: '', weight: 1 };
    const [chanForm, setChanForm] = useState(initChanForm);

    // a11y: 三个模态各自的初始焦点 ref —— 优先聚焦关闭按钮，避免 ESC/Tab 用户卡死
    const chanModalCloseRef = useRef(null);
    const modelModalCloseRef = useRef(null);
    const upstreamModalCloseRef = useRef(null);
    // fix CRITICAL C-F1（gemini 第二十一轮）：补 modalRef 让 focus trap 真正生效
    const chanModalRef = useRef(null);
    const modelModalRef = useRef(null);
    const upstreamModalRef = useRef(null);
    const { onBackdropClick: onChanBackdropClick } = useModalA11y(isChanModalOpen, () => setIsChanModalOpen(false), chanModalCloseRef, chanModalRef);
    const { onBackdropClick: onModelBackdropClick } = useModalA11y(isModelModalOpen, () => setIsModelModalOpen(false), modelModalCloseRef, modelModalRef);
    const { onBackdropClick: onUpstreamBackdropClick } = useModalA11y(isUpstreamModalOpen, () => setIsUpstreamModalOpen(false), upstreamModalCloseRef, upstreamModalRef);

    const channelTypes = [
        { id: 'cliproxy', label: 'CLIProxyAPI 多协议网关' },
        { id: 'openai', label: 'OpenAI / DeepSeek / 国产模型通用兼容' },
        { id: 'anthropic', label: 'Anthropic (Claude)' },
        { id: 'gemini', label: 'Google Gemini' },
        { id: 'google-cli', label: 'Google Gemini (CLI/Unofficial)' },
        { id: 'codex', label: 'Github Copilot (Codex)' }
    ];

    const initModelForm = {
        model_id: '', display_name: '', input_price: 0, output_price: 0,
        cached_input_price: 0, cache_write_input_price: 0, cache_write_1h_input_price: 0,
        context_price_threshold: 0, high_input_price: 0, high_cached_input_price: 0, high_output_price: 0,
        weight: 1, max_context_length: 0,
        endpoint_policy: 'all',
        // fix CRITICAL R23：内容审核字段（per-channel-per-model 风控）
        moderation_level: 'off',          // off / keyword / moderation / strict
        moderation_fail_mode: 'open',     // open / closed
        confirm_official_no_moderation: false, // 仅 UI 状态：官方渠道关审核时让 admin 显式 ack 风险
    };
    const [modelForm, setModelForm] = useState(initModelForm);

    // 各家官方 API 域名 — 与服务端 controller/channel_model.go officialChannelHosts 保持同步
    const OFFICIAL_HOSTS = {
        openai: ['api.openai.com'],
        anthropic: ['api.anthropic.com'],
        gemini: ['generativelanguage.googleapis.com'],
    };

    const isOpenAIModelId = (modelId = '') => {
        const id = String(modelId).trim().toLowerCase();
        if (!id) return false;
        const hasGptSegment = id.split(/[/: \t]+/).some(part => part === 'gpt' || part.startsWith('gpt-') || part.startsWith('gpt_'));
        return hasGptSegment
            || id.includes('openai')
            || id.startsWith('chatgpt-')
            || id.startsWith('codex-')
            || /^o\d/.test(id);
    };

    const withOpenAIModelModeration = (form) => (
        isOpenAIModelId(form.model_id)
            ? { ...form, moderation_level: 'strict', moderation_fail_mode: 'closed', confirm_official_no_moderation: false }
            : form
    );

    const withEndpointPolicyDefaults = (form) => {
        const id = String(form.model_id || '').trim().toLowerCase();
        if (id === 'gpt-5.5' && (!form.endpoint_policy || form.endpoint_policy === 'all')) {
            return { ...form, endpoint_policy: 'no_chat_non_stream' };
        }
        return { ...form, endpoint_policy: form.endpoint_policy || 'all' };
    };

    const isOpenAIModel = useMemo(() => isOpenAIModelId(modelForm.model_id), [modelForm.model_id]);

    // 判断当前选中渠道是否指向某家"官方上游"（影响审核默认值与告警）
    const isOfficialChannel = useMemo(() => {
        if (!selectedChannel) return false;
        const hosts = OFFICIAL_HOSTS[selectedChannel.type] || [];
        if (hosts.length === 0) return false;
        const base = (selectedChannel.base_url || '').trim();
        if (!base) return true; // 空 base_url = 走 SDK 默认 host = 官方
        try {
            const u = new URL(base);
            return hosts.includes(u.hostname.toLowerCase());
        } catch {
            return false; // 非法 URL → 不假设是官方（保守不报警）
        }
    }, [selectedChannel]);

    // 一键应用推荐预设：OpenAI 模型固定 strict+closed；官方渠道用 moderation+closed；
    // 非官方非 OpenAI 模型用 off+open（与服务端默认一致）
    const applyRecommendedModerationPreset = () => {
        if (isOpenAIModel) {
            setModelForm(prev => ({ ...prev, moderation_level: 'strict', moderation_fail_mode: 'closed', confirm_official_no_moderation: false }));
        } else if (isOfficialChannel) {
            setModelForm(prev => ({ ...prev, moderation_level: 'moderation', moderation_fail_mode: 'closed', confirm_official_no_moderation: false }));
        } else {
            setModelForm(prev => ({ ...prev, moderation_level: 'off', moderation_fail_mode: 'open', confirm_official_no_moderation: false }));
        }
    };

    // 列表小徽章（off=灰 / keyword=琥珀 / moderation=蓝 / strict=翠绿）—— 与 gemini R23 反馈一致
    const ModerationBadge = ({ level, failMode }) => {
        const lvl = (level || 'off').toLowerCase();
        const fm = (failMode || 'open').toLowerCase();
        const map = {
            off:        { txt: t('CHANNEL_MGMT.MOD.BADGE_OFF', 'OFF'),       cls: 'bg-surface-variant/10 border-outline-variant/30 text-on-surface-variant' },
            keyword:    { txt: t('CHANNEL_MGMT.MOD.BADGE_KW', 'KW'),         cls: 'bg-warning/10 border-warning/30 text-warning' },
            moderation: { txt: t('CHANNEL_MGMT.MOD.BADGE_MOD', 'MOD'),       cls: 'bg-primary/10 border-primary/30 text-primary' },
            strict:     { txt: t('CHANNEL_MGMT.MOD.BADGE_STRICT', 'STRICT'), cls: 'bg-success/10 border-success/30 text-success' },
        };
        const meta = map[lvl] || map.off;
        return (
            <span className={`inline-flex items-center gap-1 px-1.5 py-0.5 rounded-control border text-[10px] font-medium ${meta.cls}`}
                title={`${t('CHANNEL_MGMT.MOD.LEVEL', '审核等级')}: ${lvl} / ${t('CHANNEL_MGMT.MOD.FAIL_MODE', '失败模式')}: ${fm}`}
            >
                {meta.txt}{lvl !== 'off' && fm === 'closed' ? '·🔒' : ''}
            </span>
        );
    };

    const EndpointPolicyBadge = ({ policy }) => {
        const p = (policy || 'all').toLowerCase();
        const map = {
            all: { txt: t('CHANNEL_MGMT.ENDPOINT.BADGE_ALL', '端点: ALL'), cls: 'bg-surface-variant/10 border-outline-variant/30 text-on-surface-variant' },
            no_chat_non_stream: { txt: t('CHANNEL_MGMT.ENDPOINT.BADGE_NO_CHAT_NS', '端点: 禁非流式 Chat'), cls: 'bg-warning/10 border-warning/30 text-warning' },
            responses_only: { txt: t('CHANNEL_MGMT.ENDPOINT.BADGE_RESPONSES', '端点: Responses'), cls: 'bg-primary/10 border-primary/30 text-primary' },
        };
        const meta = map[p] || map.all;
        return (
            <span className={`inline-flex items-center gap-1 px-1.5 py-0.5 rounded-control border text-[10px] font-medium ${meta.cls}`}
                title={`${t('CHANNEL_MGMT.ENDPOINT.POLICY', '允许的客户端端点')}: ${p}`}
            >
                {meta.txt}
            </span>
        );
    };

    // Fetch Channels
    const fetchChannels = async () => {
        setLoading(true);
        try {
            const data = await authFetch('/api/admin/channels');
            if (data.success) setChannels(data.data || []);
            else toast.error(t('API.' + data.message_code));
        } catch {
            toast.error(t('API.ERR_NETWORK', '网络异常'));
        } finally {
            setLoading(false);
        }
    };

    useEffect(() => {
        if (view === 'channels') fetchChannels();
    }, [view]);

    // Fetch Models for a Channel
    const fetchModels = async (chanId) => {
        setLoadingModels(true);
        try {
            const data = await authFetch(`/api/admin/channels/${chanId}/models`);
            if (data.success) setChannelModels(data.data || []);
            else toast.error(t('API.' + data.message_code));
        } catch {
            toast.error(t('API.ERR_NETWORK', '网络异常'));
        } finally {
            setLoadingModels(false);
        }
    };

    const handleSelectChannel = (chan) => {
        setSelectedChannel(chan);
        setView('models');
        fetchModels(chan.id);
    };

    // --- Channel Operations ---
    const handleOpenChanModal = (chan = null) => {
        if (chan) {
            setCurrentChannel(chan);
            // 编辑模式：key 字段不预填（后端只下发掩码），留空表示"保持原 key 不变"。
            // 若用户希望换新 key，可直接在输入框输入新值；否则空着提交。
            setChanForm({ type: chan.type, name: chan.name || '', key: '', base_url: chan.base_url, proxy_url: chan.proxy_url || '', headers: chan.headers || '', weight: chan.weight });
        } else {
            setCurrentChannel(null);
            setChanForm(initChanForm);
        }
        setIsChanModalOpen(true);
    };

    const handleChanSubmit = async (e) => {
        e.preventDefault();
        setIsSubmitting(true);
        try {
            const url = currentChannel ? `/api/admin/channels/${currentChannel.id}` : '/api/admin/channels';
            const method = currentChannel ? 'PUT' : 'POST';
            const payload = { ...chanForm, weight: parseInt(chanForm.weight) || 1 };
            const data = await authFetch(url, { method, body: payload });
            if (data.success) {
                fetchChannels();
                setIsChanModalOpen(false);
                toast.success(currentChannel ? '渠道已更新' : '渠道已创建');
            } else {
                toast.error(data.message || t('API.' + data.message_code));
            }
        } catch {
            toast.error(t('API.ERR_NETWORK', '网络异常'));
        } finally {
            setIsSubmitting(false);
        }
    };

    const handleDeleteChan = async (id) => {
        if (!(await confirm(t('CHANNEL_MGMT.DELETE_CONFIRM')))) return;
        try {
            const data = await authFetch(`/api/admin/channels/${id}`, { method: 'DELETE' });
            if (data.success) {
                fetchChannels();
                toast.success('渠道已删除');
            } else {
                toast.error(data.message || t('API.' + data.message_code));
            }
        } catch {
            toast.error(t('API.ERR_NETWORK', '网络异常，删除失败'));
        }
    };

    // --- Model Operations ---
    const handleOpenModelModal = (model = null) => {
        setInputCurrency('USD');
        if (model) {
            setCurrentModel(model);
            setModelForm(withEndpointPolicyDefaults(withOpenAIModelModeration({
                ...model,
                endpoint_policy: model.endpoint_policy || 'all',
                moderation_level: model.moderation_level,
                moderation_fail_mode: model.moderation_fail_mode,
                confirm_official_no_moderation: false,
            })));
        } else {
            setCurrentModel(null);
            // 新建路径：根据渠道是否官方自动套推荐预设（admin 仍可在 UI 内修改）
            const hosts = OFFICIAL_HOSTS[selectedChannel?.type] || [];
            const baseEmpty = !((selectedChannel?.base_url || '').trim());
            let isOfficial = false;
            if (hosts.length > 0) {
                if (baseEmpty) isOfficial = true;
                else {
                    try {
                        const u = new URL(selectedChannel.base_url);
                        isOfficial = hosts.includes(u.hostname.toLowerCase());
                    } catch { /* 非法 URL → 不当作官方 */ }
                }
            }
            setModelForm(withEndpointPolicyDefaults(withOpenAIModelModeration({
                ...initModelForm,
                moderation_level: isOfficial ? 'moderation' : 'off',
                moderation_fail_mode: isOfficial ? 'closed' : 'open',
            })));
        }
        setIsModelModalOpen(true);
    };

    const toggleInputCurrency = (target) => {
        if (inputCurrency === target) return;
        const form = { ...modelForm };
        const fields = ['input_price', 'output_price', 'cached_input_price', 'cache_write_input_price', 'cache_write_1h_input_price', 'high_input_price', 'high_cached_input_price', 'high_output_price'];
        fields.forEach(f => {
            let val = parseFloat(form[f]) || 0;
            const converted = target === 'CNY' ? val * exchangeRate : val / exchangeRate;
            form[f] = Number(converted.toFixed(6)).toString();
        });
        setModelForm(form);
        setInputCurrency(target);
    };

    const handleModelSubmit = async (e) => {
        e.preventDefault();
        setIsSubmitting(true);
        try {
            const url = currentModel
                ? `/api/admin/channel-models/${currentModel.id}`
                : `/api/admin/channels/${selectedChannel.id}/models`;
            const method = currentModel ? 'PUT' : 'POST';

            const payload = {
                ...modelForm,
                input_price: inputCurrency === 'CNY' ? (parseFloat(modelForm.input_price) || 0) / exchangeRate : (parseFloat(modelForm.input_price) || 0),
                output_price: inputCurrency === 'CNY' ? (parseFloat(modelForm.output_price) || 0) / exchangeRate : (parseFloat(modelForm.output_price) || 0),
                cached_input_price: inputCurrency === 'CNY' ? (parseFloat(modelForm.cached_input_price) || 0) / exchangeRate : (parseFloat(modelForm.cached_input_price) || 0),
                cache_write_input_price: inputCurrency === 'CNY' ? (parseFloat(modelForm.cache_write_input_price) || 0) / exchangeRate : (parseFloat(modelForm.cache_write_input_price) || 0),
                cache_write_1h_input_price: inputCurrency === 'CNY' ? (parseFloat(modelForm.cache_write_1h_input_price) || 0) / exchangeRate : (parseFloat(modelForm.cache_write_1h_input_price) || 0),
                context_price_threshold: parseInt(modelForm.context_price_threshold) || 0,
                high_input_price: inputCurrency === 'CNY' ? (parseFloat(modelForm.high_input_price) || 0) / exchangeRate : (parseFloat(modelForm.high_input_price) || 0),
                high_cached_input_price: inputCurrency === 'CNY' ? (parseFloat(modelForm.high_cached_input_price) || 0) / exchangeRate : (parseFloat(modelForm.high_cached_input_price) || 0),
                high_output_price: inputCurrency === 'CNY' ? (parseFloat(modelForm.high_output_price) || 0) / exchangeRate : (parseFloat(modelForm.high_output_price) || 0),
                weight: parseInt(modelForm.weight) || 1,
                max_context_length: parseInt(modelForm.max_context_length) || 0,
                endpoint_policy: modelForm.endpoint_policy || 'all',
                // fix CRITICAL R23：审核字段透传（默认值在 initModelForm 已设；后端再做 enum 校验）
                moderation_level: isOpenAIModel ? 'strict' : (modelForm.moderation_level || 'off'),
                moderation_fail_mode: isOpenAIModel ? 'closed' : (modelForm.moderation_fail_mode || 'open'),
                confirm_official_no_moderation: isOpenAIModel ? false : !!modelForm.confirm_official_no_moderation,
            };

            const data = await authFetch(url, { method, body: payload });
            if (data.success) {
                fetchModels(selectedChannel.id);
                setIsModelModalOpen(false);
                toast.success(currentModel ? '模型已更新' : '模型已添加');
            } else {
                toast.error(data.message || t('API.' + data.message_code));
            }
        } catch {
            toast.error(t('API.ERR_NETWORK', '网络异常'));
        } finally {
            setIsSubmitting(false);
        }
    };

    const handleDeleteModel = async (id) => {
        if (!(await confirm(t('CHANNEL_MGMT.MODEL.DELETE_CONFIRM')))) return;
        try {
            const data = await authFetch(`/api/admin/channel-models/${id}`, { method: 'DELETE' });
            if (data.success) {
                fetchModels(selectedChannel.id);
                toast.success('模型已删除');
            } else {
                toast.error(data.message || t('API.' + data.message_code));
            }
        } catch {
            toast.error(t('API.ERR_NETWORK', '网络异常'));
        }
    };

    const handleOpenUpstreamSync = async () => {
        setIsUpstreamModalOpen(true);
        setLoadingUpstream(true);
        setSelectedUpstreamModels([]);
        try {
            const data = await authFetch(`/api/admin/channels/${selectedChannel.id}/upstream-models`);
            if (data.success) {
                setUpstreamModels(data.data || []);
            } else {
                toast.error(data.message || t('API.' + data.message_code));
                setIsUpstreamModalOpen(false);
            }
        } catch {
            toast.error(t('API.ERR_NETWORK', '网络异常'));
            setIsUpstreamModalOpen(false);
        } finally {
            setLoadingUpstream(false);
        }
    };

    const handleBatchImport = async () => {
        if (selectedUpstreamModels.length === 0) return;
        setIsSubmitting(true);
        try {
            const data = await authFetch(`/api/admin/channels/${selectedChannel.id}/models/batch`, {
                method: 'POST',
                body: { models: selectedUpstreamModels },
            });
            if (data.success) {
                fetchModels(selectedChannel.id);
                setIsUpstreamModalOpen(false);
                const added = data.data?.added ?? selectedUpstreamModels.length;
                const skipped = data.data?.skipped ?? 0;
                toast.success(skipped > 0 ? `已添加 ${added} 个，跳过 ${skipped} 个已存在` : `已添加 ${added} 个模型`);
            } else {
                toast.error(data.message || t('API.' + data.message_code));
            }
        } catch {
            toast.error(t('API.ERR_NETWORK', '网络异常'));
        } finally {
            setIsSubmitting(false);
        }
    };

    const filteredChannels = channels.filter(c => c.type.toLowerCase().includes(searchTerm.toLowerCase()));
    const filteredModels = channelModels.filter(m => m.model_id.toLowerCase().includes(modelSearchTerm.toLowerCase()));

    // --- Sub-Renders ---

    if (view === 'models') {
        return (
            <div className="w-full animation-fade-in relative z-10">
                <button onClick={() => setView('channels')} className="flex items-center gap-2 text-on-surface-variant hover:text-success mb-6 text-sm font-medium">
                    <ArrowLeft size={16} /> {t('CHANNEL_MGMT.MODEL.BTN_BACK')}
                </button>

                <div className="mb-8">
                    <h1 className="text-3xl font-black text-on-surface flex items-center gap-3">
                        <Box size={32} className="text-primary" />
                        {t('CHANNEL_MGMT.MODEL.TITLE', { id: selectedChannel.id })} ({selectedChannel.type})
                    </h1>
                </div>

                <div className="flex flex-col md:flex-row justify-between items-start md:items-center gap-4 mb-6 relative z-20">
                    <div className="relative w-full md:w-96">
                        <input
                            type="text"
                            placeholder={t('CHANNEL_MGMT.MODEL.SEARCH_PLACEHOLDER')}
                            value={modelSearchTerm}
                            onChange={(e) => setModelSearchTerm(e.target.value)}
                            className="w-full bg-surface-container border border-outline-variant rounded-overlay pl-11 pr-4 py-3 text-sm text-on-surface-variant focus:outline-none focus:border-primary focus:ring-1 focus:ring-blue-500/50"
                        />
                        <Search size={18} className="absolute left-4 top-1/2 -translate-y-1/2 text-on-surface-variant" />
                    </div>
                    <div className="flex gap-2">
                        <button
                            onClick={handleOpenUpstreamSync}
                            className="flex items-center gap-2 bg-surface hover:bg-surface-container-high text-on-surface-variant border border-outline-variant px-4 py-3 rounded-overlay font-medium "
                        >
                            <Network size={18} className="text-success" />
                            {t('CHANNEL_MGMT.MODEL.BTN_FETCH_UPSTREAM')}
                        </button>
                        <button
                            onClick={() => handleOpenModelModal()}
                            className="flex items-center gap-2 bg-primary text-on-primary hover:bg-primary-container hover:text-on-primary-container px-5 py-3 rounded-overlay font-medium /20 active:scale-95 border border-primary/50"
                        >
                            <Plus size={18} />
                            {t('CHANNEL_MGMT.MODEL.BTN_ADD')}
                        </button>
                    </div>
                </div>

                {/* Model Table */}
                <div className="bg-surface-container border border-outline-variant rounded-overlay overflow-hidden ">
                    <div className="overflow-x-auto">
                        <table className="w-full min-w-[900px] text-left text-sm text-on-surface-variant table-fixed">
                            <thead className="bg-surface-container-high text-xs uppercase font-mono tracking-wider text-on-surface-variant border-b border-outline-variant">
                            <tr>
                                <th className="px-6 py-4 font-medium w-[25%]">{t('CHANNEL_MGMT.MODEL.TABLE.MODEL_ID')}</th>
                                <th className="px-6 py-4 font-medium w-[15%]">{t('CHANNEL_MGMT.MODEL.TABLE.MAX_CTX')}</th>
                                <th className="px-6 py-4 font-medium w-[15%]">{t('CHANNEL_MGMT.MODEL.TABLE.BASE_PRICING')}</th>
                                <th className="px-6 py-4 font-medium w-[20%]">{t('CHANNEL_MGMT.MODEL.TABLE.TIER_PRICING')}</th>
                                <th className="px-6 py-4 font-medium w-[15%]">{t('CHANNEL_MGMT.MODEL.TABLE.WEIGHT')}</th>
                                <th className="px-6 py-4 font-medium text-right w-[15%]">{t('CHANNEL_MGMT.MODEL.TABLE.ACTIONS')}</th>
                            </tr>
                        </thead>
                        <tbody className="divide-y divide-[#2b2b2b]/50">
                            {loadingModels ? (
                                <tr><td colSpan="6" className="px-6 py-12 text-center text-on-surface-variant"><RefreshCw size={24} className="mx-auto mb-2" /></td></tr>
                            ) : filteredModels.length === 0 ? (
                                <tr><td colSpan="6" className="px-6 py-12 text-center text-on-surface-variant">No models found for this channel.</td></tr>
                            ) : (
                                filteredModels.map(m => (
                                    <tr key={m.id} className="hover:bg-surface group">
                                        <td className="px-6 py-4 font-mono text-primary">
                                            <div className="flex items-center gap-2 flex-wrap">
                                                <span>{m.model_id}</span>
                                                <ModerationBadge level={m.moderation_level} failMode={m.moderation_fail_mode} />
                                                <EndpointPolicyBadge policy={m.endpoint_policy} />
                                            </div>
                                            <div className="flex items-center gap-2 mt-1">
                                                <span className="text-xs text-on-surface-variant">{m.display_name}</span>
                                            </div>
                                        </td>
                                        <td className="px-6 py-4">
                                            {m.max_context_length > 0 ? (
                                                <span className="text-xs bg-surface-container-high/50 text-on-surface-variant px-2 py-1 rounded-control border border-outline-variant/50">
                                                    {formatTokens(m.max_context_length)}
                                                </span>
                                            ) : (
                                                <span className="text-xs text-outline">-</span>
                                            )}
                                        </td>
                                        <td className="px-6 py-4">
                                            <div className="flex flex-col text-xs space-y-1">
                                                <span className="text-on-surface-variant">{t('CHANNEL_MGMT.MODEL.IN')}: {formatCurrency(m.input_price, 6)}</span>
                                                <span className="text-on-surface-variant">{t('CHANNEL_MGMT.MODEL.OUT')}: {formatCurrency(m.output_price, 6)}</span>
                                                {m.cached_input_price > 0 && <span className="text-success">{t('CHANNEL_MGMT.MODEL.CACHE')}: {formatCurrency(m.cached_input_price, 6)}</span>}
                                                {m.cache_write_input_price > 0 && <span className="text-warning">{t('CHANNEL_MGMT.MODEL.CACHE_WRITE_5M', '缓存写5m')}: {formatCurrency(m.cache_write_input_price, 6)}</span>}
                                                {m.cache_write_1h_input_price > 0 && <span className="text-warning">{t('CHANNEL_MGMT.MODEL.CACHE_WRITE_1H', '缓存写1h')}: {formatCurrency(m.cache_write_1h_input_price, 6)}</span>}
                                            </div>
                                        </td>
                                        <td className="px-6 py-4">
                                            {m.context_price_threshold > 0 ? (
                                                <div className="flex flex-col text-xs space-y-1 bg-warning/10 border border-warning/30 p-2 rounded-control w-fit">
                                                    <span className="text-warning font-medium pb-1 mb-1 border-b border-warning/20">
                                                        <AlertTriangle size={12} className="inline mr-1 -mt-0.5" />
                                                        {t('CHANNEL_MGMT.MODEL.TIER_ACTIVE', { threshold: formatTokens(m.context_price_threshold) })}
                                                    </span>
                                                    <span className="text-on-surface-variant">{t('CHANNEL_MGMT.MODEL.IN')}: {formatCurrency(m.high_input_price, 6)}</span>
                                                    {m.high_cached_input_price > 0 && <span className="text-success">{t('CHANNEL_MGMT.MODEL.CACHE')}: {formatCurrency(m.high_cached_input_price, 6)}</span>}
                                                    <span className="text-on-surface-variant">{t('CHANNEL_MGMT.MODEL.OUT')}: {formatCurrency(m.high_output_price, 6)}</span>
                                                </div>
                                            ) : (
                                                <span className="text-xs text-outline-variant italic">{t('CHANNEL_MGMT.MODEL.NO_TIER')}</span>
                                            )}
                                        </td>
                                        <td className="px-6 py-4">{m.weight}</td>
                                        <td className="px-6 py-4 text-right">
                                            <button onClick={() => handleOpenModelModal(m)} className="p-2 hover:bg-primary/20 text-primary rounded-control mr-2"><Edit2 size={16} /></button>
                                            <button onClick={() => handleDeleteModel(m.id)} className="p-2 hover:bg-error/20 text-error rounded-control"><Trash2 size={16} /></button>
                                        </td>
                                    </tr>
                                ))
                            )}
                        </tbody>
                        </table>
                    </div>
                </div>

                {/* Sub-Model Modal */}
                {isModelModalOpen && (
                    <div
                        ref={modelModalRef}
                        role="dialog"
                        aria-modal="true"
                        aria-labelledby="channel-model-modal-title"
                        onClick={onModelBackdropClick}
                        className="fixed inset-0 bg-black/80 backdrop-blur-sm z-[100] flex items-start sm:items-center justify-center p-2 sm:p-4 overflow-y-auto"
                    >
                        <div className="bg-surface border border-outline-variant rounded-overlay w-full max-w-xl flex flex-col max-h-[90vh]">
                            <div className="p-6 border-b border-outline-variant flex justify-between">
                                <h3 id="channel-model-modal-title" className="text-xl font-bold text-on-surface flex items-center gap-2">
                                    {currentModel ? t('CHANNEL_MGMT.MODEL.MODAL.EDIT_TITLE') : t('CHANNEL_MGMT.MODEL.MODAL.ADD_TITLE')}
                                </h3>
                                <button ref={modelModalCloseRef} onClick={() => setIsModelModalOpen(false)} aria-label={t('COMMON.CLOSE', '关闭')}><X size={20} className="text-on-surface-variant hover:text-white" /></button>
                            </div>
                            <div className="bg-surface-container-high px-6 py-3 border-b border-outline-variant flex items-center justify-between">
                                <span className="text-xs text-on-surface-variant font-medium tracking-wide">{t('CHANNEL_MGMT.SETTLEMENT_CURRENCY')}</span>
                                <div className="flex bg-surface-variant rounded-control p-1">
                                    <button
                                        type="button"
                                        onClick={() => toggleInputCurrency('USD')}
                                        className={`px-4 py-1 text-xs font-bold rounded-control  ${inputCurrency === 'USD' ? 'bg-primary text-on-primary text-on-surface ' : 'text-on-surface-variant hover:text-white'}`}
                                    >USD ($)</button>
                                    <button
                                        type="button"
                                        onClick={() => toggleInputCurrency('CNY')}
                                        className={`px-4 py-1 text-xs font-bold rounded-control  ${inputCurrency === 'CNY' ? 'bg-warning text-on-surface ' : 'text-on-surface-variant hover:text-white'}`}
                                    >CNY (￥)</button>
                                </div>
                            </div>
                            <div className="p-6 overflow-y-auto space-y-4">
                                <div>
                                    <label htmlFor="channel-model-id" className="block text-xs font-medium text-on-surface-variant mb-1">{t('CHANNEL_MGMT.MODEL.MODAL.MODEL_ID')}</label>
                                    <input
                                        id="channel-model-id"
                                        type="text"
                                        required
                                        value={modelForm.model_id}
                                        onChange={e=>setModelForm(withEndpointPolicyDefaults(withOpenAIModelModeration({...modelForm, model_id: e.target.value})))}
                                        className="w-full bg-surface-container-high border border-outline-variant rounded-overlay px-4 py-2.5 text-on-surface"
                                    />
                                </div>
                                <div>
                                    <label htmlFor="channel-model-display-name" className="block text-xs font-medium text-on-surface-variant mb-1">{t('CHANNEL_MGMT.MODEL.MODAL.DISPLAY_NAME')}</label>
                                    <input id="channel-model-display-name" type="text" value={modelForm.display_name || ''} onChange={e=>setModelForm({...modelForm, display_name: e.target.value})} className="w-full bg-surface-container-high border border-outline-variant rounded-overlay px-4 py-2.5 text-on-surface" />
                                </div>
                                <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
                                    <div>
                                        <label htmlFor="channel-model-max-context" className="block text-xs font-medium text-on-surface-variant mb-1">{t('CHANNEL_MGMT.MODEL.MODAL.MAX_CONTEXT_LENGTH')} <span className="ml-1 text-on-surface-variant/70">(Tokens)</span></label>
                                        <input id="channel-model-max-context" type="number" min="0" value={modelForm.max_context_length || ''} onChange={e=>setModelForm({...modelForm, max_context_length: parseInt(e.target.value) || 0})} className="w-full bg-surface-container-high border border-outline-variant rounded-overlay px-4 py-2.5 text-on-surface" placeholder="0 = 不限制" />
                                        <div className="flex flex-wrap gap-1 mt-2">
                                            {[8000, 32000, 128000, 200000, 1000000].map(v => (
                                                <button type="button" key={v} onClick={()=>setModelForm({...modelForm, max_context_length: v})} className="px-2 py-0.5 rounded-control text-[10px] border border-outline-variant bg-surface hover:bg-surface-container-high text-on-surface-variant">{formatTokens(v)}</button>
                                            ))}
                                        </div>
                                    </div>
                                    <div>
                                        <label htmlFor="channel-model-weight" className="block text-xs font-medium text-on-surface-variant mb-1">{t('CHANNEL_MGMT.MODEL.MODAL.WEIGHT')}</label>
                                        <input id="channel-model-weight" type="number" min="0" value={modelForm.weight} onChange={e=>setModelForm({...modelForm, weight: e.target.value})} className="w-full bg-surface-container-high border border-outline-variant rounded-overlay px-4 py-2.5 text-on-surface" />
                                    </div>
                                </div>
                                <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
                                    <div>
                                        <label htmlFor="channel-model-input-price" className="block text-xs font-medium text-on-surface-variant mb-1">
                                            {t('CHANNEL_MGMT.MODEL.MODAL.INPUT_PRICE')}
                                            <span className="ml-1 text-primary">({inputCurrency === 'CNY' ? '￥/1M' : '$/1M'})</span>
                                        </label>
                                        <input id="channel-model-input-price" type="number" step="0.000001" min="0" required value={modelForm.input_price} onChange={e=>setModelForm({...modelForm, input_price: e.target.value})} className="w-full bg-surface border border-outline-variant rounded-control px-3 py-2 text-on-surface" />
                                    </div>
                                    <div>
                                        <label htmlFor="channel-model-output-price" className="block text-xs font-medium text-on-surface-variant mb-1">
                                            {t('CHANNEL_MGMT.MODEL.MODAL.OUTPUT_PRICE')}
                                            <span className="ml-1 text-primary">({inputCurrency === 'CNY' ? '￥/1M' : '$/1M'})</span>
                                        </label>
                                        <input id="channel-model-output-price" type="number" step="0.000001" min="0" required value={modelForm.output_price} onChange={e=>setModelForm({...modelForm, output_price: e.target.value})} className="w-full bg-surface border border-outline-variant rounded-control px-3 py-2 text-on-surface" />
                                    </div>
                                    <div>
                                        <label htmlFor="channel-model-cache-price" className="block text-xs font-medium text-success mb-1">
                                            {t('CHANNEL_MGMT.MODEL.MODAL.CACHE_PRICE')}
                                            <span className="ml-1 text-success/70">({inputCurrency === 'CNY' ? '￥/1M' : '$/1M'})</span>
                                        </label>
                                        <input id="channel-model-cache-price" type="number" step="0.000001" min="0" value={modelForm.cached_input_price} onChange={e=>setModelForm({...modelForm, cached_input_price: e.target.value})} className="w-full bg-surface border border-success/30 rounded-control px-3 py-2 text-on-surface" />
                                    </div>
                                    <div>
                                        <label htmlFor="channel-model-cache-write-price" className="block text-xs font-medium text-warning mb-1">
                                            {t('CHANNEL_MGMT.MODEL.MODAL.CACHE_WRITE_PRICE', '缓存写入单价 ($/1M Token)')}
                                            <span className="ml-1 text-warning/70">({inputCurrency === 'CNY' ? '￥/1M' : '$/1M'})</span>
                                        </label>
                                        <input id="channel-model-cache-write-price" type="number" step="0.000001" min="0" value={modelForm.cache_write_input_price} onChange={e=>setModelForm({...modelForm, cache_write_input_price: e.target.value})} className="w-full bg-surface border border-warning/30 rounded-control px-3 py-2 text-on-surface" />
                                    </div>
                                    <div className="col-span-1 sm:col-span-2">
                                        <label htmlFor="channel-model-cache-write-1h-price" className="block text-xs font-medium text-warning mb-1">
                                            {t('CHANNEL_MGMT.MODEL.MODAL.CACHE_WRITE_1H_PRICE', '1小时缓存写入单价 ($/1M Token)')}
                                            <span className="ml-1 text-warning/70">({inputCurrency === 'CNY' ? '￥/1M' : '$/1M'})</span>
                                        </label>
                                        <input id="channel-model-cache-write-1h-price" type="number" step="0.000001" min="0" value={modelForm.cache_write_1h_input_price} onChange={e=>setModelForm({...modelForm, cache_write_1h_input_price: e.target.value})} className="w-full bg-surface border border-warning/30 rounded-control px-3 py-2 text-on-surface" />
                                    </div>
                                </div>
                                <div className="p-4 bg-warning/5 border border-warning/20 rounded-overlay space-y-4">
                                    <div>
                                        <label htmlFor="channel-model-threshold" className="block text-xs font-medium text-on-surface-variant mb-1">{t('CHANNEL_MGMT.MODEL.MODAL.THRESHOLD')}</label>
                                        <input id="channel-model-threshold" type="number" min="0" value={modelForm.context_price_threshold} onChange={e=>setModelForm({...modelForm, context_price_threshold: e.target.value})} className="w-full bg-surface border border-warning/30 rounded-control px-3 py-2 text-warning" />
                                        <div className="flex flex-wrap gap-1 mt-2">
                                            {[8000, 32000, 128000, 200000, 1000000].map(v => (
                                                <button type="button" key={v} onClick={()=>setModelForm({...modelForm, context_price_threshold: v})} className="px-2 py-0.5 rounded-control text-[10px] border border-warning/20 bg-warning/10 hover:bg-warning/20 text-warning/80">{formatTokens(v)}</button>
                                            ))}
                                        </div>
                                    </div>
                                    {parseInt(modelForm.context_price_threshold) > 0 && (
                                        <div className="grid grid-cols-1 sm:grid-cols-3 gap-4">
                                            <div>
                                                <label htmlFor="channel-model-high-input" className="block text-xs font-medium text-on-surface-variant mb-1">
                                                    {t('CHANNEL_MGMT.MODEL.MODAL.HIGH_IN_PRICE')}
                                                    <span className="ml-1 text-primary">({inputCurrency === 'CNY' ? '￥/1M' : '$/1M'})</span>
                                                </label>
                                                <input id="channel-model-high-input" type="number" step="0.000001" min="0" required value={modelForm.high_input_price} onChange={e=>setModelForm({...modelForm, high_input_price: e.target.value})} className="w-full bg-surface border border-outline-variant rounded-control px-3 py-2 text-on-surface" />
                                            </div>
                                            <div>
                                                <label htmlFor="channel-model-high-cache" className="block text-xs font-medium text-on-surface-variant mb-1">
                                                    {t('CHANNEL_MGMT.MODEL.MODAL.HIGH_CACHE_PRICE', '阶梯缓存读取单价 ($/1M)')}
                                                    <span className="ml-1 text-primary">({inputCurrency === 'CNY' ? '￥/1M' : '$/1M'})</span>
                                                </label>
                                                <input id="channel-model-high-cache" type="number" step="0.000001" min="0" value={modelForm.high_cached_input_price} onChange={e=>setModelForm({...modelForm, high_cached_input_price: e.target.value})} className="w-full bg-surface border border-outline-variant rounded-control px-3 py-2 text-on-surface" />
                                            </div>
                                            <div>
                                                <label htmlFor="channel-model-high-output" className="block text-xs font-medium text-on-surface-variant mb-1">
                                                    {t('CHANNEL_MGMT.MODEL.MODAL.HIGH_OUT_PRICE')}
                                                    <span className="ml-1 text-primary">({inputCurrency === 'CNY' ? '￥/1M' : '$/1M'})</span>
                                                </label>
                                                <input id="channel-model-high-output" type="number" step="0.000001" min="0" required value={modelForm.high_output_price} onChange={e=>setModelForm({...modelForm, high_output_price: e.target.value})} className="w-full bg-surface border border-outline-variant rounded-control px-3 py-2 text-on-surface" />
                                            </div>
                                        </div>
                                    )}
                                </div>

                                <fieldset className="border border-outline-variant rounded-overlay p-4 mt-2">
                                    <legend className="px-2 text-xs font-semibold text-on-surface flex items-center gap-2">
                                        <Network size={13} className="text-on-surface-variant" />
                                        {t('CHANNEL_MGMT.ENDPOINT.LEGEND', '端点兼容策略')}
                                    </legend>
                                    <label htmlFor="endpoint-policy" className="block text-xs font-medium text-on-surface-variant mb-1 mt-2">
                                        {t('CHANNEL_MGMT.ENDPOINT.POLICY', '允许的客户端端点')}
                                    </label>
                                    <select
                                        id="endpoint-policy"
                                        value={modelForm.endpoint_policy || 'all'}
                                        onChange={e=>setModelForm({...modelForm, endpoint_policy: e.target.value})}
                                        className="w-full bg-surface-container-high border border-outline-variant rounded-overlay px-4 py-2.5 text-on-surface"
                                    >
                                        <option value="all">{t('CHANNEL_MGMT.ENDPOINT.ALL', '全部允许')}</option>
                                        <option value="no_chat_non_stream">{t('CHANNEL_MGMT.ENDPOINT.NO_CHAT_NON_STREAM', '禁止 Chat Completions 非流式')}</option>
                                        <option value="responses_only">{t('CHANNEL_MGMT.ENDPOINT.RESPONSES_ONLY', '仅 Responses API')}</option>
                                    </select>
                                    <p className="mt-2 text-[11px] text-on-surface-variant leading-relaxed">
                                        {t('CHANNEL_MGMT.ENDPOINT.HINT', '用于 gpt-5.5 等只在特定端点稳定工作的模型；不兼容请求会在平台侧直接返回明确错误。')}
                                    </p>
                                </fieldset>

                                {/* fix CRITICAL R23: per-ChannelModel 内容审核策略 (codex 第二十三轮反馈) */}
                                <fieldset className="border border-outline-variant rounded-overlay p-4 mt-2">
                                    <legend className="px-2 text-xs font-semibold text-on-surface flex items-center gap-2">
                                        <AlertTriangle size={13} className={isOfficialChannel ? 'text-warning' : 'text-on-surface-variant'} />
                                        {t('CHANNEL_MGMT.MOD.LEGEND', '内容审核（防账号被封禁）')}
                                    </legend>
                                    <div className="grid grid-cols-1 sm:grid-cols-2 gap-4 mt-2">
                                        <div>
                                            <label htmlFor="moderation-level" className="block text-xs font-medium text-on-surface-variant mb-1">
                                                {t('CHANNEL_MGMT.MOD.LEVEL', '审核等级')}
                                            </label>
                                            <select
                                                id="moderation-level"
                                                value={modelForm.moderation_level || 'off'}
                                                onChange={e=>setModelForm(withOpenAIModelModeration({...modelForm, moderation_level: e.target.value, confirm_official_no_moderation: false}))}
                                                disabled={isOpenAIModel}
                                                className="w-full bg-surface-container-high border border-outline-variant rounded-overlay px-4 py-2.5 text-on-surface"
                                            >
                                                <option value="off">{t('CHANNEL_MGMT.MOD.LEVEL_OFF', 'OFF — 不审核')}</option>
                                                <option value="keyword">{t('CHANNEL_MGMT.MOD.LEVEL_KEYWORD', 'KW — 仅关键字快扫')}</option>
                                                <option value="moderation">{t('CHANNEL_MGMT.MOD.LEVEL_MODERATION', 'MOD — 仅智能审核服务')}</option>
                                                <option value="strict">{t('CHANNEL_MGMT.MOD.LEVEL_STRICT', 'STRICT — 关键字 + 智能审核双层（推荐官方高风险模型）')}</option>
                                            </select>
                                        </div>
                                        <div>
                                            <label htmlFor="moderation-fail-mode" className="block text-xs font-medium text-on-surface-variant mb-1">
                                                {t('CHANNEL_MGMT.MOD.FAIL_MODE', '审核服务不可达时')}
                                            </label>
                                            <select
                                                id="moderation-fail-mode"
                                                value={modelForm.moderation_fail_mode || 'open'}
                                                onChange={e=>setModelForm(withOpenAIModelModeration({...modelForm, moderation_fail_mode: e.target.value}))}
                                                disabled={isOpenAIModel}
                                                className="w-full bg-surface-container-high border border-outline-variant rounded-overlay px-4 py-2.5 text-on-surface"
                                            >
                                                <option value="open">{t('CHANNEL_MGMT.MOD.FAIL_OPEN', 'OPEN — 放行（cloaked 路径推荐）')}</option>
                                                <option value="closed">{t('CHANNEL_MGMT.MOD.FAIL_CLOSED', 'CLOSED — 拒绝（直连官方推荐）')}</option>
                                            </select>
                                        </div>
                                    </div>
                                    {isOfficialChannel && (
                                        <div className="mt-3 p-3 bg-warning/10 border border-warning/30 rounded-control text-xs text-warning leading-relaxed">
                                            <AlertTriangle size={12} className="inline mr-1 -mt-0.5" />
                                            {t('CHANNEL_MGMT.MOD.OFFICIAL_HINT', '当前渠道指向官方 API（OpenAI / Anthropic / Gemini）。建议设为 STRICT + CLOSED 防账号被封禁。点击右下角"应用推荐预设"。')}
                                        </div>
                                    )}
                                    {isOpenAIModel && (
                                        <div className="mt-3 p-3 bg-success/10 border border-success/30 rounded-control text-xs text-success leading-relaxed">
                                            <AlertTriangle size={12} className="inline mr-1 -mt-0.5" />
                                            {t('CHANNEL_MGMT.MOD.OPENAI_LOCK_HINT', 'OpenAI / Codex-family 模型已全局强制启用 STRICT + CLOSED 内容审查。')}
                                        </div>
                                    )}
                                    <div className="flex items-center justify-between mt-3">
                                        <span className="text-[11px] text-on-surface-variant italic">
                                            {isOpenAIModel
                                                ? t('CHANNEL_MGMT.MOD.PRESET_OPENAI_DESC', '强制：STRICT + CLOSED')
                                                : isOfficialChannel
                                                ? t('CHANNEL_MGMT.MOD.PRESET_OFFICIAL_DESC', '推荐：MOD + CLOSED')
                                                : t('CHANNEL_MGMT.MOD.PRESET_CLOAKED_DESC', '推荐（cloaked / 自部署）：OFF + OPEN')}
                                        </span>
                                        <button type="button" onClick={applyRecommendedModerationPreset} className="text-xs text-primary hover:underline font-medium">
                                            {t('CHANNEL_MGMT.MOD.PRESET_RECOMMENDED', '应用推荐预设')}
                                        </button>
                                    </div>
                                    {isOfficialChannel && modelForm.moderation_level === 'off' && (
                                        <label className="flex items-start gap-2 mt-3 text-xs text-warning bg-warning/10 border border-warning/30 rounded-control p-2 cursor-pointer">
                                            <input
                                                type="checkbox"
                                                className="mt-0.5"
                                                checked={!!modelForm.confirm_official_no_moderation}
                                                onChange={e=>setModelForm({...modelForm, confirm_official_no_moderation: e.target.checked})}
                                            />
                                            <span>{t('CHANNEL_MGMT.MOD.CONFIRM_RISK', '我已了解：关闭官方渠道审核可能导致 API key 因用户违规被官方封禁。仍要保存。')}</span>
                                        </label>
                                    )}
                                </fieldset>
                            </div>
                            <div className="p-6 border-t border-outline-variant bg-surface-container-high flex justify-end gap-3 rounded-control-b-2xl">
                                <button onClick={() => setIsModelModalOpen(false)} className="px-5 py-2.5 text-on-surface-variant hover:text-white hover:bg-surface-container-high rounded-overlay">{t('CHANNEL_MGMT.MODEL.MODAL.BTN_CANCEL')}</button>
                                {/* fix MINOR R23-m4：官方渠道关审核没勾 confirm 时禁用 Save，省一次后端往返 */}
                                <button
                                    onClick={handleModelSubmit}
                                    disabled={isSubmitting || (!isOpenAIModel && isOfficialChannel && modelForm.moderation_level === 'off' && !modelForm.confirm_official_no_moderation)}
                                    className="px-6 py-2.5 bg-primary text-on-primary hover:bg-primary-container hover:text-on-primary-container rounded-overlay font-medium flex items-center gap-2 disabled:opacity-50 disabled:cursor-not-allowed"
                                >
                                    {isSubmitting ? <RefreshCw className="animate-spin" size={18}/> : <Save size={18}/>} {t('CHANNEL_MGMT.MODEL.MODAL.BTN_SAVE')}
                                </button>
                            </div>
                        </div>
                    </div>
                )}

                {/* Upstream Sync Modal */}
                {isUpstreamModalOpen && (
                    <div
                        ref={upstreamModalRef}
                        role="dialog"
                        aria-modal="true"
                        aria-labelledby="channel-upstream-modal-title"
                        onClick={onUpstreamBackdropClick}
                        className="fixed inset-0 bg-black/80 backdrop-blur-sm z-[100] flex items-start sm:items-center justify-center p-2 sm:p-4 overflow-y-auto"
                    >
                        <div className="bg-surface border border-outline-variant rounded-overlay w-full max-w-2xl flex flex-col max-h-[90vh]">
                            <div className="p-6 border-b border-outline-variant flex justify-between items-center bg-surface-container-high rounded-control-t-2xl">
                                <div>
                                    <h3 id="channel-upstream-modal-title" className="text-xl font-bold text-on-surface flex items-center gap-2">
                                        <Network size={22} className="text-success" />
                                        {t('CHANNEL_MGMT.UPSTREAM_MODAL.TITLE')}
                                    </h3>
                                    <p className="text-xs text-on-surface-variant mt-1">{t('CHANNEL_MGMT.UPSTREAM_MODAL.DESC', { type: selectedChannel.type })}</p>
                                </div>
                                <button ref={upstreamModalCloseRef} onClick={() => setIsUpstreamModalOpen(false)} aria-label={t('COMMON.CLOSE', '关闭')}><X size={20} className="text-on-surface-variant hover:text-white" /></button>
                            </div>

                            <div className="p-6 overflow-y-auto flex-1">
                                {loadingUpstream ? (
                                    <div className="flex flex-col items-center justify-center py-12">
                                        <RefreshCw size={32} className="text-success mb-4" />
                                        <p className="text-on-surface-variant">{t('CHANNEL_MGMT.UPSTREAM_MODAL.LOADING')}</p>
                                    </div>
                                ) : upstreamModels.length === 0 ? (
                                    <div className="text-center py-12 text-on-surface-variant">
                                        {t('CHANNEL_MGMT.UPSTREAM_MODAL.EMPTY')}
                                    </div>
                                ) : (
                                    <div className="space-y-4">
                                        <div className="flex items-center justify-between pb-2 border-b border-outline-variant">
                                            <span className="text-sm text-on-surface-variant">{t('CHANNEL_MGMT.UPSTREAM_MODAL.FOUND_COUNT', { count: upstreamModels.length })}</span>
                                            <button
                                                onClick={() => {
                                                    if(selectedUpstreamModels.length === upstreamModels.length) setSelectedUpstreamModels([]);
                                                    else setSelectedUpstreamModels([...upstreamModels]);
                                                }}
                                                className="text-xs text-success hover:text-success font-medium"
                                            >
                                                {selectedUpstreamModels.length === upstreamModels.length ? t('CHANNEL_MGMT.UPSTREAM_MODAL.BTN_DESELECT_ALL') : t('CHANNEL_MGMT.UPSTREAM_MODAL.BTN_SELECT_ALL')}
                                            </button>
                                        </div>
                                        <div className="space-y-6">
                                            {/* fix MAJOR F2（gemini 第二十一轮）：用 useMemo 缓存的分组结果替代 render 内重计算 */}
                                            {upstreamModelsByLetter.map(([letter, models]) => (
                                                <div key={letter} className="space-y-3">
                                                    <h4 className="flex items-center gap-2">
                                                        <span className="bg-surface-variant text-on-surface-variant font-bold w-6 h-6 flex items-center justify-center rounded-control text-xs">{letter}</span>
                                                        <span className="flex-1 border-t border-outline-variant/80"></span>
                                                    </h4>
                                                    <div className="grid grid-cols-2 md:grid-cols-3 gap-3">
                                                        {models.map(modelId => {
                                                            const isSelected = selectedUpstreamModels.includes(modelId);
                                                            const toggleSelect = () => {
                                                                if (isSelected) setSelectedUpstreamModels(prev => prev.filter(m => m !== modelId));
                                                                else setSelectedUpstreamModels(prev => [...prev, modelId]);
                                                            };
                                                            return (
                                                                <div
                                                                    key={modelId}
                                                                    role="checkbox"
                                                                    aria-checked={isSelected}
                                                                    tabIndex={0}
                                                                    onClick={toggleSelect}
                                                                    onKeyDown={(e) => {
                                                                        if (e.key === 'Enter' || e.key === ' ') {
                                                                            e.preventDefault();
                                                                            toggleSelect();
                                                                        }
                                                                    }}
                                                                    className={`cursor-pointer p-3 rounded-control border text-sm  flex items-center gap-2 focus:outline-none focus:ring-2 focus:ring-emerald-500/60
                                                                        ${isSelected
                                                                            ? 'bg-success/10 border-success/50 text-success'
                                                                            : 'bg-surface-container-high border-outline-variant text-on-surface-variant hover:border-outline-variant'}`}
                                                                >
                                                                    <div className={`w-4 h-4 rounded-control border flex items-center justify-center shrink-0
                                                                        ${isSelected ? 'bg-success border-success' : 'border-outline-variant'}`}>
                                                                        {isSelected && <div className="w-2 h-2 bg-surface-container-high rounded-control-sm" />}
                                                                    </div>
                                                                    <span className="truncate">{modelId}</span>
                                                                </div>
                                                            );
                                                        })}
                                                    </div>
                                                </div>
                                            ))}
                                        </div>
                                    </div>
                                )}
                            </div>

                            <div className="p-6 border-t border-outline-variant bg-surface-container-high flex justify-between items-center rounded-control-b-2xl">
                                {/* C-2 修复：用 i18n 插值占位 + JSX 子组件渲染数字，避免 dangerouslySetInnerHTML 注入面 */}
                                <span className="text-sm text-on-surface-variant">
                                    <Trans i18nKey="CHANNEL_MGMT.UPSTREAM_MODAL.SELECTED_COUNT"
                                        components={{ strong: <strong className="text-success mx-1" /> }}
                                        values={{ count: selectedUpstreamModels.length }} />
                                </span>
                                <div className="flex gap-3">
                                    <button onClick={() => setIsUpstreamModalOpen(false)} className="px-5 py-2.5 text-on-surface-variant hover:text-white hover:bg-surface-container-high rounded-overlay">{t('CHANNEL_MGMT.MODEL.MODAL.BTN_CANCEL')}</button>
                                    <button
                                        onClick={handleBatchImport}
                                        disabled={isSubmitting || selectedUpstreamModels.length === 0}
                                        className="px-6 py-2.5 bg-success hover:bg-success disabled:opacity-50 disabled:cursor-not-allowed text-on-surface rounded-overlay font-medium flex items-center gap-2 "
                                    >
                                        {isSubmitting ? <RefreshCw className="animate-spin" size={18}/> : <Save size={18}/>}
                                        {t('CHANNEL_MGMT.UPSTREAM_MODAL.BTN_IMPORT')}
                                    </button>
                                </div>
                            </div>
                        </div>
                    </div>
                )}
            </div>
        );
    }

    // --- Channel List View ---
    return (
        <div className="w-full animation-fade-in relative z-10">
            <div className="mb-10">
                <h1 className="text-4xl font-black text-transparent bg-clip-text bg-gradient-to-r from-emerald-400 to-teal-600 mb-3 tracking-tight drop- flex items-center gap-3">
                    <Network size={36} className="text-success" />
                    {t('CHANNEL_MGMT.TITLE')}
                </h1>
                <p className="text-on-surface-variant text-sm font-medium tracking-wide">
                    {t('CHANNEL_MGMT.SUBTITLE')}
                </p>
            </div>

            <div className="flex flex-col md:flex-row justify-between items-start md:items-center gap-4 mb-6 relative z-20">
                <div className="relative w-full md:w-96">
                    <input
                        type="text"
                        placeholder={t('CHANNEL_MGMT.SEARCH_CHANNEL')}
                        value={searchTerm}
                        onChange={(e) => setSearchTerm(e.target.value)}
                        className="w-full bg-surface-container border border-outline-variant rounded-overlay pl-11 pr-4 py-3 text-sm text-on-surface-variant focus:outline-none focus:border-success focus:ring-1 focus:ring-emerald-500/50"
                    />
                    <Search size={18} className="absolute left-4 top-1/2 -translate-y-1/2 text-on-surface-variant" />
                </div>
                <button
                    onClick={() => handleOpenChanModal()}
                    className="flex items-center gap-2 bg-success hover:bg-success text-on-surface px-5 py-3 rounded-overlay font-medium /20 active:scale-95 border border-success/50"
                >
                    <Plus size={18} />
                    {t('CHANNEL_MGMT.BTN_ADD_CHANNEL')}
                </button>
            </div>

            <div className="bg-surface-container border border-outline-variant rounded-overlay overflow-hidden ">
                <div className="overflow-x-auto">
                    <table className="w-full min-w-[1000px] text-left text-sm text-on-surface-variant table-fixed">
                        <thead className="bg-surface-container-high text-xs uppercase font-mono tracking-wider text-on-surface-variant border-b border-outline-variant">
                        <tr>
                            <th className="px-6 py-4 font-medium w-[15%]">{t('CHANNEL_MGMT.CHANNEL_TABLE.ID')}</th>
                            <th className="px-6 py-4 font-medium w-[15%]">{t('CHANNEL_MGMT.CHANNEL_TABLE.TYPE')}</th>
                            <th className="px-6 py-4 font-medium">{t('CHANNEL_MGMT.CHANNEL_TABLE.KEY')} / URL</th>
                            <th className="px-6 py-4 font-medium w-[10%]">{t('CHANNEL_MGMT.CHANNEL_TABLE.WEIGHT')}</th>
                            <th className="px-6 py-4 font-medium text-right w-[240px]">{t('CHANNEL_MGMT.CHANNEL_TABLE.ACTIONS')}</th>
                        </tr>
                    </thead>
                    <tbody className="divide-y divide-[#2b2b2b]/50">
                        {loading ? (
                            <tr><td colSpan="5" className="px-6 py-12 text-center text-on-surface-variant"><RefreshCw size={24} className="mx-auto mb-2" /></td></tr>
                        ) : filteredChannels.length === 0 ? (
                            <tr><td colSpan="5" className="px-6 py-12 text-center text-on-surface-variant">No channels connected yet.</td></tr>
                        ) : (
                            filteredChannels.map(c => (
                                <tr key={c.id} className="hover:bg-surface group">
                                    <td className="px-6 py-4 font-bold text-on-surface-variant">
                                        #{c.id}
                                        {c.name && <div className="text-xs text-success/80 font-normal mt-1">{c.name}</div>}
                                    </td>
                                    <td className="px-6 py-4">
                                        <span className="bg-success/10 text-success px-2.5 py-1 rounded-full text-xs box-border border border-success/20">{c.type}</span>
                                    </td>
                                    <td className="px-6 py-4 font-mono text-xs opacity-80">
                                        <div className="break-all max-w-xs">{c.key}</div>
                                        <span className="text-on-surface-variant mt-1 block">{c.base_url || 'default'}</span>
                                    </td>
                                    <td className="px-6 py-4 text-success">{c.weight}</td>
                                    <td className="px-6 py-4 text-right">
                                        <div className="flex justify-end gap-2 shrink-0 flex-nowrap">
                                            <button onClick={() => handleSelectChannel(c)} className="p-2 flex shrink-0 items-center gap-1 hover:bg-primary/20 text-primary rounded-control bg-surface-variant whitespace-nowrap"><Box size={14} /> {t('CHANNEL_MGMT.BTN_MODELS')}</button>
                                            <button onClick={() => handleOpenChanModal(c)} className="p-2 shrink-0 hover:bg-success/20 text-success rounded-control bg-surface-variant "><Edit2 size={16} /></button>
                                            <button onClick={() => handleDeleteChan(c.id)} className="p-2 shrink-0 hover:bg-error/20 text-error rounded-control bg-surface-variant "><Trash2 size={16} /></button>
                                        </div>
                                    </td>
                                </tr>
                            ))
                        )}
                    </tbody>
                    </table>
                </div>
            </div>

            {/* Channel Modal */}
            {isChanModalOpen && (
                <div
                    ref={chanModalRef}
                    role="dialog"
                    aria-modal="true"
                    aria-labelledby="channel-form-modal-title"
                    onClick={onChanBackdropClick}
                    className="fixed inset-0 bg-black/80 backdrop-blur-sm z-[100] flex items-start sm:items-center justify-center p-2 sm:p-4 overflow-y-auto"
                >
                    <div className="bg-surface border border-outline-variant rounded-overlay w-full max-w-xl flex flex-col">
                        <div className="p-6 border-b border-outline-variant flex justify-between">
                            <h3 id="channel-form-modal-title" className="text-xl font-bold text-on-surface">{currentChannel ? t('CHANNEL_MGMT.MODAL_CHANNEL.EDIT_TITLE') : t('CHANNEL_MGMT.MODAL_CHANNEL.ADD_TITLE')}</h3>
                            <button ref={chanModalCloseRef} onClick={() => setIsChanModalOpen(false)} aria-label={t('COMMON.CLOSE', '关闭')}><X size={20} className="text-on-surface-variant hover:text-white" /></button>
                        </div>
                        <div className="p-6 space-y-4">
                            <div>
                                <label htmlFor="channel-form-name" className="block text-xs font-medium text-on-surface-variant mb-1">{t('CHANNEL_MGMT.MODAL_CHANNEL.NAME_LABEL')}</label>
                                <input id="channel-form-name" type="text" required value={chanForm.name} onChange={e=>setChanForm({...chanForm, name: e.target.value})} placeholder={t('CHANNEL_MGMT.MODAL_CHANNEL.NAME_PLACEHOLDER')} className="w-full bg-surface-container-high border border-outline-variant rounded-overlay px-4 py-2.5 text-on-surface" />
                            </div>
                            <div>
                                <label htmlFor="channel-form-type" className="block text-xs font-medium text-on-surface-variant mb-1">{t('CHANNEL_MGMT.MODAL_CHANNEL.TYPE')}</label>
                                <select id="channel-form-type" required value={chanForm.type} onChange={e=>setChanForm({...chanForm, type: e.target.value})} className="w-full bg-surface-container-high border border-outline-variant rounded-overlay px-4 py-2.5 text-on-surface cursor-pointer hover:border-success/50 outline-none">
                                    {channelTypes.map(ct => (
                                        <option key={ct.id} value={ct.id}>{ct.label} ({ct.id})</option>
                                    ))}
                                </select>
                            </div>
                            <div>
                                <label htmlFor="channel-form-key" className="block text-xs font-medium text-on-surface-variant mb-1">{t('CHANNEL_MGMT.MODAL_CHANNEL.KEY')}</label>
                                <input id="channel-form-key" type="text" required value={chanForm.key} onChange={e=>setChanForm({...chanForm, key: e.target.value})} className="w-full bg-surface-container-high border border-outline-variant rounded-overlay px-4 py-2.5 text-on-surface font-mono text-sm tracking-widest" />
                            </div>
                            <div>
                                <label htmlFor="channel-form-base-url" className="block text-xs font-medium text-on-surface-variant mb-1">{t('CHANNEL_MGMT.MODAL_CHANNEL.BASE_URL')} (Base URL)</label>
                                <input id="channel-form-base-url" type="text" value={chanForm.base_url} onChange={e=>setChanForm({...chanForm, base_url: e.target.value})} placeholder="https://api.openai.com" className="w-full bg-surface-container-high border border-outline-variant rounded-overlay px-4 py-2.5 text-on-surface" />
                            </div>
                            <div>
                                <label htmlFor="channel-form-proxy" className="block text-xs font-medium text-on-surface-variant mb-1">代理跳板 (Proxy URL)</label>
                                <input id="channel-form-proxy" type="text" value={chanForm.proxy_url} onChange={e=>setChanForm({...chanForm, proxy_url: e.target.value})} placeholder="http://127.0.0.1:8080 或 https://proxy.example.com:443" className="w-full bg-surface-container-high border border-outline-variant rounded-overlay px-4 py-2.5 text-on-surface font-mono text-sm" />
                            </div>
                            <div>
                                <label htmlFor="channel-form-headers" className="block text-xs font-medium text-on-surface-variant mb-1">自定义网关请求头 (Custom Headers JSON)</label>
                                <textarea id="channel-form-headers" value={chanForm.headers} onChange={e=>setChanForm({...chanForm, headers: e.target.value})} placeholder='{"x-custom-tenant": "vip-01"}' rows={3} className="w-full bg-surface-container-high border border-outline-variant rounded-overlay px-4 py-2.5 text-on-surface font-mono text-xs"></textarea>
                            </div>
                            <div>
                                <label htmlFor="channel-form-weight" className="block text-xs font-medium text-on-surface-variant mb-1">{t('CHANNEL_MGMT.MODAL_CHANNEL.WEIGHT')}</label>
                                <input id="channel-form-weight" type="number" min="1" value={chanForm.weight} onChange={e=>setChanForm({...chanForm, weight: e.target.value})} className="w-full bg-surface-container-high border border-outline-variant rounded-overlay px-4 py-2.5 text-on-surface" />
                            </div>
                        </div>
                        <div className="p-6 border-t border-outline-variant bg-surface-container-high flex justify-end gap-3 rounded-control-b-2xl">
                            <button onClick={() => setIsChanModalOpen(false)} className="px-5 py-2.5 text-on-surface-variant hover:text-white hover:bg-surface-container-high rounded-overlay">{t('CHANNEL_MGMT.MODAL_CHANNEL.BTN_CANCEL')}</button>
                            <button onClick={handleChanSubmit} disabled={isSubmitting} className="px-6 py-2.5 bg-success hover:bg-success text-on-surface rounded-overlay font-medium flex items-center gap-2">
                                {isSubmitting ? <RefreshCw className="animate-spin" size={18}/> : <Save size={18}/>} {t('CHANNEL_MGMT.MODAL_CHANNEL.BTN_SAVE')}
                            </button>
                        </div>
                    </div>
                </div>
            )}
        </div>
    );
};

export default ChannelManagement;
