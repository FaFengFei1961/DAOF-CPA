import React, { useState, useEffect, useMemo, useRef } from 'react';
import { useTranslation, Trans } from 'react-i18next';
import { Search, Plus, Edit2, Trash2, Server, Save, X, RefreshCw, AlertTriangle, ArrowLeft, Network, Box, ChevronRight, ShieldAlert, ShieldCheck, ShieldOff } from 'lucide-react';
import { useCurrency } from '../context/CurrencyContext';
import toast from 'react-hot-toast';
import { authFetch } from '../utils/authFetch';
import { useConfirm } from '../context/ConfirmContext';
import { useModalA11y } from '../hooks/useModalA11y';
import { DestructiveIconButton, PageHeader } from './ui';
import DataTable from './ui/DataTable';
import StatusBadge from './ui/StatusBadge';
import ChannelCircuitMonitor from './ChannelCircuitMonitor';
import { groupModelsByProvider, inferModelProvider, brandFor, hexA } from '../utils/modelProviders';

// fix CRITICAL（codex money-unit + verify-final）：后端 channelModelPayload 只接受
// *_pico_per_token int64 字段，前端表单展示的是 USD/1M tokens（admin 习惯单位）。
//
// 后端常量（database/schema.go:121-123）：
//   PicoPerUSD                = 1e15 (1 USD = 1e15 pico)
//   PicoPerTokenPerUSDPerMTok = 1e9  (USD/1M tokens × 1e9 = pico/token)
// 推导：1 USD/1M tokens = 1e-6 USD/token = 1e-6 × 1e15 = 1e9 pico/token ✓
//
// 之前用 1e6（错 1000 倍 → 平台只收真实价 0.1%，灾难性少收）。必须用 1e9。
const PICO_PER_TOKEN_PER_USD_PER_M_TOK = 1_000_000_000;

const usdPerMillionToPicoPerToken = (usdPerMillion) => {
    const v = parseFloat(usdPerMillion);
    if (!isFinite(v) || v < 0) return 0;
    return Math.round(v * PICO_PER_TOKEN_PER_USD_PER_M_TOK);
};
const picoPerTokenToUsdPerMillion = (pico) => {
    const v = Number(pico);
    if (!isFinite(v) || v <= 0) return 0;
    return v / PICO_PER_TOKEN_PER_USD_PER_M_TOK;
};

const inferModelFamily = (modelId = '') => {
    const id = String(modelId).trim().toLowerCase();
    if (id.includes('claude')) {
        if (id.includes('claude-3-') || id === 'claude-opus-4-20250514' || id === 'claude-opus-4-1-20250805') {
            return { key: 'anthropic-legacy', name: 'Claude Legacy / Deprecated', order: 19 };
        }
        if (id.includes('opus')) return { key: 'anthropic-opus', name: 'Claude Opus', order: 10 };
        if (id.includes('sonnet')) return { key: 'anthropic-sonnet', name: 'Claude Sonnet', order: 11 };
        if (id.includes('haiku')) return { key: 'anthropic-haiku', name: 'Claude Haiku', order: 12 };
        return { key: 'anthropic-other', name: 'Claude Other', order: 18 };
    }
    if (id.includes('gpt-image') || id.includes('dall')) return { key: 'openai-image', name: 'OpenAI Image', order: 29 };
    if (id.includes('codex')) return { key: 'openai-codex', name: 'Codex / Internal', order: 22 };
    if (id.includes('gpt-5')) return { key: 'openai-gpt5', name: 'GPT-5', order: 20 };
    if (id.includes('gpt')) return { key: 'openai-gpt', name: 'GPT', order: 21 };
    if (id.startsWith('o')) return { key: 'openai-reasoning', name: 'OpenAI Reasoning', order: 23 };
    if (id.includes('gemini')) {
        if (id.includes('flash-image')) return { key: 'google-image', name: 'Gemini Image', order: 39 };
        if (id.includes('agent')) return { key: 'google-agent', name: 'Gemini Agent / Alias', order: 38 };
        if (id.startsWith('gemini-2.5')) return { key: 'google-25', name: 'Gemini 2.5', order: 30 };
        if (id.startsWith('gemini-3.1')) return { key: 'google-31', name: 'Gemini 3.1', order: 32 };
        if (id.startsWith('gemini-3')) return { key: 'google-3', name: 'Gemini 3', order: 31 };
        return { key: 'google-other', name: 'Gemini Other', order: 37 };
    }
    if (id.includes('grok-imagine')) return { key: 'xai-imagine', name: 'Grok Imagine', order: 79 };
    if (id.includes('grok-3')) return { key: 'xai-grok3', name: 'Grok 3', order: 70 };
    if (id.includes('grok-4')) return { key: 'xai-grok4', name: 'Grok 4', order: 71 };
    if (id.includes('grok') || id.includes('xai')) return { key: 'xai-other', name: 'Grok Other', order: 78 };
    return { key: 'other', name: 'Other Models', order: 1000 };
};

const hasTokenPrice = (model = {}) => [
    'input_price_pico_per_token',
    'output_price_pico_per_token',
    'cached_input_price_pico_per_token',
    'high_input_price_pico_per_token',
    'high_output_price_pico_per_token',
].some(key => Number(model[key]) > 0);

const looksLikeMediaModel = (modelId = '') => {
    const id = String(modelId).toLowerCase();
    return id.includes('image') || id.includes('video') || id.includes('imagine');
};

const normalizeModerationLevel = (model = {}) => String(model.moderation_level || 'off').toLowerCase();
const normalizeModerationFailMode = (model = {}) => String(model.moderation_fail_mode || 'open').toLowerCase();
const modelHasAnyModeration = (model = {}) => normalizeModerationLevel(model) !== 'off';
const modelHasSmartModeration = (model = {}) => ['moderation', 'strict'].includes(normalizeModerationLevel(model));
const modelIsFailClosed = (model = {}) => normalizeModerationFailMode(model) === 'closed';
const normalizeModelCategory = (category, modelId = '') => {
    const c = String(category || '').toLowerCase();
    if (['text', 'image', 'video'].includes(c)) return c;
    const id = String(modelId || '').toLowerCase();
    if (id.includes('video')) return 'video';
    if (id.includes('image') || id.includes('imagine') || id.includes('imagen')) return 'image';
    return 'text';
};
const normalizeBillingMode = (mode, category = 'text') => {
    const m = String(mode || '').toLowerCase();
    if (['token', 'image', 'video_second'].includes(m)) return m;
    const c = normalizeModelCategory(category);
    if (c === 'image') return 'image';
    if (c === 'video') return 'video_second';
    return 'token';
};
const defaultAllowedEndpointsForCategory = (category) => {
    switch (normalizeModelCategory(category)) {
        case 'image':
            return '["/v1/images/generations"]';
        case 'video':
            return '["/v1/videos/generations"]';
        default:
            return '';
    }
};
const categoryLabel = (category, t) => ({
    text: t('CHANNEL_MGMT.RUNTIME.CATEGORY_TEXT', '文本'),
    image: t('CHANNEL_MGMT.RUNTIME.CATEGORY_IMAGE', '图片'),
    video: t('CHANNEL_MGMT.RUNTIME.CATEGORY_VIDEO', '视频'),
}[normalizeModelCategory(category)] || t('CHANNEL_MGMT.RUNTIME.CATEGORY_TEXT', '文本'));
const billingModeLabel = (mode, t) => ({
    token: t('CHANNEL_MGMT.RUNTIME.BILLING_TOKEN', '按 token'),
    image: t('CHANNEL_MGMT.RUNTIME.BILLING_IMAGE', '按图片'),
    video_second: t('CHANNEL_MGMT.RUNTIME.BILLING_VIDEO_SECOND', '按视频秒'),
}[normalizeBillingMode(mode)] || t('CHANNEL_MGMT.RUNTIME.BILLING_TOKEN', '按 token'));

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
    const [moderationFilter, setModerationFilter] = useState('all');

    // Models Modal State
    const [isModelModalOpen, setIsModelModalOpen] = useState(false);
    const [currentModel, setCurrentModel] = useState(null);

    // Upstream Fetch State
    const [isUpstreamModalOpen, setIsUpstreamModalOpen] = useState(false);
    const [upstreamModels, setUpstreamModels] = useState([]);
    const [loadingUpstream, setLoadingUpstream] = useState(false);
    const [selectedUpstreamModels, setSelectedUpstreamModels] = useState([]);

    // Group by provider instead of initial letter, then sort by model id inside each group.
    const upstreamModelsByProvider = useMemo(() => {
        if (!Array.isArray(upstreamModels) || upstreamModels.length === 0) return [];
        const classify = (id) => {
            const s = (id || '').toLowerCase();
            if (s.startsWith('claude') || s.startsWith('anthropic')) return 'Anthropic';
            if (s.startsWith('gemini') || s.startsWith('imagen') || s.startsWith('palm') || s.startsWith('bison')) return 'Google';
            if (s.startsWith('gpt') || s.startsWith('chatgpt') || s.startsWith('codex') || s.startsWith('o1') || s.startsWith('o3') || s.startsWith('o4') || s.startsWith('dall') || s.startsWith('whisper') || s.startsWith('tts') || s.startsWith('text-embedding')) return 'OpenAI';
            if (s.startsWith('grok') || s.startsWith('xai')) return 'xAI';
            return 'Other';
        };
        const grouped = upstreamModels.reduce((acc, modelId) => {
            const g = classify(modelId);
            if (!acc[g]) acc[g] = [];
            acc[g].push(modelId);
            return acc;
        }, {});
        // Sort inside each group.
        Object.values(grouped).forEach(arr => arr.sort((a, b) => a.localeCompare(b)));
        // Preserve preferred group order.
        const order = ['Anthropic', 'OpenAI', 'Google', 'xAI', 'Other'];
        return order.filter(g => grouped[g]?.length).map(g => [g, grouped[g]]);
    }, [upstreamModels]);

    // UX fix（用户反馈 "/pricing 暂无可用模型接入"）：
    // 之前 channel 表单没有 status 字段，channel 创建 / 编辑都漏掉了 enable/disable
    // 维度 —— 用户 seed 出一个 Status=2 的默认 channel 后没有任何 UI 能把它启用，
    // 导致后端 `/api/pricing` 的 `WHERE channels.status=1` 把所有模型滤掉。
    const initChanForm = { type: 'cliproxy', name: '', key: '', base_url: '', proxy_url: '', headers: '', weight: 1, status: 1 };
    const [chanForm, setChanForm] = useState(initChanForm);

    // a11y: each modal has its own initial focus ref, preferring the close button.
    const chanModalCloseRef = useRef(null);
    const modelModalCloseRef = useRef(null);
    const upstreamModalCloseRef = useRef(null);
    // fix CRITICAL C-F1 (gemini round 21): modalRef makes the focus trap effective.
    const chanModalRef = useRef(null);
    const modelModalRef = useRef(null);
    const upstreamModalRef = useRef(null);
    const { onBackdropClick: onChanBackdropClick } = useModalA11y(isChanModalOpen, () => setIsChanModalOpen(false), chanModalCloseRef, chanModalRef);
    const { onBackdropClick: onModelBackdropClick } = useModalA11y(isModelModalOpen, () => setIsModelModalOpen(false), modelModalCloseRef, modelModalRef);
    const { onBackdropClick: onUpstreamBackdropClick } = useModalA11y(isUpstreamModalOpen, () => setIsUpstreamModalOpen(false), upstreamModalCloseRef, upstreamModalRef);

    const channelTypes = [
        { id: 'cliproxy', label: t('CHANNEL_MGMT.TYPE_CLIPROXY', 'CLIProxyAPI 多协议网关') },
        { id: 'openai', label: t('CHANNEL_MGMT.TYPE_OPENAI_COMPAT', 'OpenAI / DeepSeek / 国产模型通用兼容') },
        { id: 'anthropic', label: 'Anthropic (Claude)' },
        { id: 'gemini', label: 'Google Gemini' },
        { id: 'google-cli', label: 'Google Gemini (CLI/Unofficial)' },
        { id: 'codex', label: 'Github Copilot (Codex)' }
    ];

    const initModelForm = {
        model_id: '', display_name: '', input_price: 0, output_price: 0,
        cached_input_price: 0, cache_write_input_price: 0, cache_write_1h_input_price: 0,
        context_price_threshold: 0, high_input_price: 0, high_cached_input_price: 0, high_output_price: 0,
        weight: 1, max_context_length: 0, status: 1,
        model_category: 'text',
        billing_mode: 'token',
        allowed_endpoints: '',
        endpoint_policy: 'all',
        // fix CRITICAL R23: content moderation fields per channel model.
        moderation_level: 'off',          // off / keyword / moderation / strict
        moderation_fail_mode: 'open',     // open / closed
        confirm_official_no_moderation: false, // UI-only state requiring explicit risk acknowledgement.
    };
    const [modelForm, setModelForm] = useState(initModelForm);

    // Official API hosts, kept in sync with controller/channel_model.go officialChannelHosts.
    const OFFICIAL_HOSTS = {
        openai: ['api.openai.com'],
        anthropic: ['api.anthropic.com'],
        gemini: ['generativelanguage.googleapis.com'],
    };

    const withEndpointPolicyDefaults = (form) => {
        const id = String(form.model_id || '').trim().toLowerCase();
        const category = normalizeModelCategory(form.model_category, id);
        const billingMode = normalizeBillingMode(form.billing_mode, category);
        const allowed = form.allowed_endpoints ?? defaultAllowedEndpointsForCategory(category);
        const next = { ...form, model_category: category, billing_mode: billingMode, allowed_endpoints: allowed };
        if (id === 'gpt-5.5' && (!next.endpoint_policy || next.endpoint_policy === 'all')) {
            return { ...next, endpoint_policy: 'no_chat_non_stream' };
        }
        return { ...next, endpoint_policy: next.endpoint_policy || 'all' };
    };

    // Detect whether the selected channel points to an official upstream.
    const isOfficialChannel = useMemo(() => {
        if (!selectedChannel) return false;
        const hosts = OFFICIAL_HOSTS[selectedChannel.type] || [];
        if (hosts.length === 0) return false;
        const base = (selectedChannel.base_url || '').trim();
        if (!base) return true; // Empty base_url uses the SDK default official host.
        try {
            const u = new URL(base);
            return hosts.includes(u.hostname.toLowerCase());
        } catch {
            return false; // Invalid URL is not assumed official.
        }
    }, [selectedChannel]);

    // Recommended preset: official direct channels use moderation+closed;
    // non-official / cloaked channels use off+open, matching server defaults.
    const applyRecommendedModerationPreset = () => {
        if (isOfficialChannel) {
            setModelForm(prev => ({ ...prev, moderation_level: 'moderation', moderation_fail_mode: 'closed', confirm_official_no_moderation: false }));
        } else {
            setModelForm(prev => ({ ...prev, moderation_level: 'off', moderation_fail_mode: 'open', confirm_official_no_moderation: false }));
        }
    };

    const applyModelCategory = (category) => {
        const nextCategory = normalizeModelCategory(category);
        setModelForm(prev => ({
            ...prev,
            model_category: nextCategory,
            billing_mode: normalizeBillingMode('', nextCategory),
            allowed_endpoints: defaultAllowedEndpointsForCategory(nextCategory),
        }));
    };

    // Small list badges for moderation levels, matching gemini R23 feedback.
    const ModerationBadge = ({ level, failMode, compact = false }) => {
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
            <span className={`inline-flex items-center gap-1 rounded-control border font-medium ${compact ? 'px-1.5 py-0.5 text-[10px]' : 'px-2 py-1 text-[11px]'} ${meta.cls}`}
                title={`${t('CHANNEL_MGMT.MOD.LEVEL', '审核等级')}: ${lvl} / ${t('CHANNEL_MGMT.MOD.FAIL_MODE', '失败模式')}: ${fm}`}
            >
                {meta.txt}
                {lvl !== 'off' && (
                    <span className="opacity-80">
                        {fm === 'closed'
                            ? t('CHANNEL_MGMT.MOD.FAIL_CLOSED_SHORT', 'closed')
                            : t('CHANNEL_MGMT.MOD.FAIL_OPEN_SHORT', 'open')}
                    </span>
                )}
            </span>
        );
    };

    const ModerationPolicyCell = ({ model }) => {
        const level = normalizeModerationLevel(model);
        const failMode = normalizeModerationFailMode(model);
        const copy = {
            off: t('CHANNEL_MGMT.MOD.CELL_OFF', '未接入审核'),
            keyword: t('CHANNEL_MGMT.MOD.CELL_KEYWORD', '本地关键词快扫'),
            moderation: t('CHANNEL_MGMT.MOD.CELL_MODERATION', '智能审核 provider'),
            strict: t('CHANNEL_MGMT.MOD.CELL_STRICT', '关键词 + 智能审核'),
        }[level] || level;
        const Icon = level === 'off' ? ShieldOff : modelIsFailClosed(model) ? ShieldCheck : ShieldAlert;
        const iconClass = level === 'off'
            ? 'text-on-surface-variant'
            : modelIsFailClosed(model)
            ? 'text-success'
            : 'text-warning';
        return (
            <div className="flex flex-col gap-1.5 min-w-[130px]">
                <div className="flex items-center gap-1.5">
                    <Icon size={14} className={iconClass} />
                    <ModerationBadge level={level} failMode={failMode} />
                </div>
                <div className="text-[11px] leading-snug text-on-surface-variant">
                    {copy}
                </div>
            </div>
        );
    };

    const ModerationGroupSummary = ({ row }) => {
        const off = row.items.length - (row.moderated || 0);
        return (
            <div className="flex flex-wrap gap-1.5 text-[11px]">
                {row.moderated > 0 && (
                    <span className="text-success font-medium">
                        {t('CHANNEL_MGMT.MOD.GROUP_REVIEWED', '{{count}} 审核', { count: row.moderated })}
                    </span>
                )}
                {row.smart > 0 && (
                    <span className="text-primary font-medium">
                        {t('CHANNEL_MGMT.MOD.GROUP_SMART', '{{count}} 智能', { count: row.smart })}
                    </span>
                )}
                {row.failOpen > 0 && (
                    <span className="text-warning font-medium">
                        {t('CHANNEL_MGMT.MOD.GROUP_FAIL_OPEN', '{{count}} open', { count: row.failOpen })}
                    </span>
                )}
                {off > 0 && (
                    <span className="text-on-surface-variant">
                        {t('CHANNEL_MGMT.MOD.GROUP_OFF', '{{count}} 未审', { count: off })}
                    </span>
                )}
            </div>
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

    const RuntimePolicyCell = ({ model }) => {
        const category = normalizeModelCategory(model.model_category, model.model_id);
        const billing = normalizeBillingMode(model.billing_mode, category);
        const endpointText = String(model.allowed_endpoints || defaultAllowedEndpointsForCategory(category) || '').replace(/[[\]"]/g, '') || '-';
        const categoryClass = {
            text: 'bg-primary/10 border-primary/30 text-primary',
            image: 'bg-success/10 border-success/30 text-success',
            video: 'bg-warning/10 border-warning/30 text-warning',
        }[category] || 'bg-surface-variant/10 border-outline-variant/30 text-on-surface-variant';
        return (
            <div className="flex flex-col gap-1.5 min-w-[120px]">
                <div className="flex flex-wrap gap-1">
                    <span className={`inline-flex items-center px-2 py-0.5 rounded-control border text-[11px] font-medium ${categoryClass}`}>
                        {categoryLabel(category, t)}
                    </span>
                    <span className="inline-flex items-center px-2 py-0.5 rounded-control border border-outline-variant/50 bg-surface-container-high text-[11px] text-on-surface-variant">
                        {billingModeLabel(billing, t)}
                    </span>
                </div>
                <span className="font-mono text-[10px] text-on-surface-variant truncate max-w-[180px]" title={endpointText}>
                    {endpointText}
                </span>
            </div>
        );
    };

    // Audit HIGH-3 fix：fetchChannels 用 useCallback 包起来，让 useEffect deps
    // 能正确列出，stale closure 风险消除。t 改变（语言切换）也会触发重拉。
    const fetchChannels = React.useCallback(async () => {
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
    }, [t]);

    useEffect(() => {
        if (view === 'channels') fetchChannels();
    }, [view, fetchChannels]);

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
            setChanForm({ type: chan.type, name: chan.name || '', key: chan.key || '', base_url: chan.base_url, proxy_url: chan.proxy_url || '', headers: chan.headers || '', weight: chan.weight, status: chan.status === 2 ? 2 : 1 });
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
            const payload = { ...chanForm, weight: parseInt(chanForm.weight) || 1, status: chanForm.status === 2 ? 2 : 1 };
            const data = await authFetch(url, { method, body: payload });
            if (data.success) {
                fetchChannels();
                setIsChanModalOpen(false);
                toast.success(currentChannel
                    ? t('CHANNEL_MGMT.CHANNEL_UPDATED', '渠道已更新')
                    : t('CHANNEL_MGMT.CHANNEL_CREATED', '渠道已创建'));
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
                toast.success(t('CHANNEL_MGMT.CHANNEL_DELETED', '渠道已删除'));
            } else {
                toast.error(data.message || t('API.' + data.message_code));
            }
        } catch {
            toast.error(t('API.ERR_NETWORK', '网络异常，删除失败'));
        }
    };

    // 单击启用 / 禁用：把整条 channel 在 channel.status=1↔2 之间翻转。
    // 后端 /api/admin/channels/:id 是部分更新 (只 set 非零字段)，但 status 是核心
    // 必传字段 —— 单独 PUT 一个只含 status 的 payload 会把 BaseURL/Headers/Weight
    // 当成"未传"留原值，正合所愿。
    const handleToggleChannelStatus = async (chan) => {
        const nextStatus = chan.status === 1 ? 2 : 1;
        setIsSubmitting(true);
        try {
            const data = await authFetch(`/api/admin/channels/${chan.id}`, {
                method: 'PUT',
                body: {
                    type: chan.type,
                    name: chan.name,
                    base_url: chan.base_url || '',
                    proxy_url: chan.proxy_url || '',
                    headers: chan.headers || '',
                    weight: chan.weight || 1,
                    status: nextStatus,
                },
            });
            if (data.success) {
                fetchChannels();
                toast.success(nextStatus === 1
                    ? t('CHANNEL_MGMT.CHANNEL_ENABLED', '渠道已启用')
                    : t('CHANNEL_MGMT.CHANNEL_DISABLED', '渠道已禁用'));
            } else {
                toast.error(data.message || t('API.' + data.message_code));
            }
        } catch {
            toast.error(t('API.ERR_NETWORK', '网络异常'));
        } finally {
            setIsSubmitting(false);
        }
    };

    // --- Model Operations ---
    const handleOpenModelModal = (model = null) => {
        setInputCurrency('USD');
        if (model) {
            setCurrentModel(model);
            // 后端返回 *_pico_per_token 字段；form 内部用 USD/1M tokens 单位（admin 习惯）。
            setModelForm(withEndpointPolicyDefaults({
                ...model,
                input_price: picoPerTokenToUsdPerMillion(model.input_price_pico_per_token),
                output_price: picoPerTokenToUsdPerMillion(model.output_price_pico_per_token),
                cached_input_price: picoPerTokenToUsdPerMillion(model.cached_input_price_pico_per_token),
                cache_write_input_price: picoPerTokenToUsdPerMillion(model.cache_write_input_price_pico_per_token),
                cache_write_1h_input_price: picoPerTokenToUsdPerMillion(model.cache_write_1h_input_price_pico_per_token),
                high_input_price: picoPerTokenToUsdPerMillion(model.high_input_price_pico_per_token),
                high_cached_input_price: picoPerTokenToUsdPerMillion(model.high_cached_input_price_pico_per_token),
                high_output_price: picoPerTokenToUsdPerMillion(model.high_output_price_pico_per_token),
                endpoint_policy: model.endpoint_policy || 'all',
                moderation_level: model.moderation_level,
                moderation_fail_mode: model.moderation_fail_mode,
                confirm_official_no_moderation: false,
            }));
        } else {
            setCurrentModel(null);
            // New model path applies recommended presets based on official-channel detection.
            const hosts = OFFICIAL_HOSTS[selectedChannel?.type] || [];
            const baseEmpty = !((selectedChannel?.base_url || '').trim());
            let isOfficial = false;
            if (hosts.length > 0) {
                if (baseEmpty) isOfficial = true;
                else {
                    try {
                        const u = new URL(selectedChannel.base_url);
                        isOfficial = hosts.includes(u.hostname.toLowerCase());
                    } catch { /* Invalid URL is not treated as official. */ }
                }
            }
            setModelForm(withEndpointPolicyDefaults({
                ...initModelForm,
                moderation_level: isOfficial ? 'moderation' : 'off',
                moderation_fail_mode: isOfficial ? 'closed' : 'open',
            }));
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

            // form 是 USD/1M tokens（admin 单位），后端只接受 *_pico_per_token int64。
            // 若 admin 选了 CNY 输入，先 ÷ exchangeRate 折回 USD；再 × 1e9 转 pico/token。
            const toUsdPerMillion = (raw) => {
                const v = parseFloat(raw) || 0;
                return inputCurrency === 'CNY' ? v / exchangeRate : v;
            };
            const payload = {
                ...modelForm,
                // 删 USD/1M 旧字段：后端 BodyParser 不读这些字段，留着只会让 admin 误以为生效
                input_price: undefined,
                output_price: undefined,
                cached_input_price: undefined,
                cache_write_input_price: undefined,
                cache_write_1h_input_price: undefined,
                high_input_price: undefined,
                high_cached_input_price: undefined,
                high_output_price: undefined,
                // 加 pico/token 新字段（后端契约）
                input_price_pico_per_token: usdPerMillionToPicoPerToken(toUsdPerMillion(modelForm.input_price)),
                output_price_pico_per_token: usdPerMillionToPicoPerToken(toUsdPerMillion(modelForm.output_price)),
                cached_input_price_pico_per_token: usdPerMillionToPicoPerToken(toUsdPerMillion(modelForm.cached_input_price)),
                cache_write_input_price_pico_per_token: usdPerMillionToPicoPerToken(toUsdPerMillion(modelForm.cache_write_input_price)),
                cache_write_1h_input_price_pico_per_token: usdPerMillionToPicoPerToken(toUsdPerMillion(modelForm.cache_write_1h_input_price)),
                high_input_price_pico_per_token: usdPerMillionToPicoPerToken(toUsdPerMillion(modelForm.high_input_price)),
                high_cached_input_price_pico_per_token: usdPerMillionToPicoPerToken(toUsdPerMillion(modelForm.high_cached_input_price)),
                high_output_price_pico_per_token: usdPerMillionToPicoPerToken(toUsdPerMillion(modelForm.high_output_price)),
                context_price_threshold: parseInt(modelForm.context_price_threshold) || 0,
                weight: parseInt(modelForm.weight) || 1,
                status: parseInt(modelForm.status) === 2 ? 2 : 1,
                max_context_length: parseInt(modelForm.max_context_length) || 0,
                model_category: normalizeModelCategory(modelForm.model_category, modelForm.model_id),
                billing_mode: normalizeBillingMode(modelForm.billing_mode, modelForm.model_category),
                allowed_endpoints: modelForm.allowed_endpoints ?? defaultAllowedEndpointsForCategory(modelForm.model_category),
                endpoint_policy: modelForm.endpoint_policy || 'all',
                // fix CRITICAL R23: pass moderation fields through; backend validates enums.
                moderation_level: modelForm.moderation_level || 'off',
                moderation_fail_mode: modelForm.moderation_fail_mode || 'open',
                confirm_official_no_moderation: !!modelForm.confirm_official_no_moderation,
            };

            const data = await authFetch(url, { method, body: payload });
            if (data.success) {
                fetchModels(selectedChannel.id);
                setIsModelModalOpen(false);
                toast.success(currentModel
                    ? t('CHANNEL_MGMT.MODEL_UPDATED', '模型已更新')
                    : t('CHANNEL_MGMT.MODEL_ADDED', '模型已添加'));
            } else {
                toast.error(data.message || t('API.' + data.message_code));
            }
        } catch {
            toast.error(t('API.ERR_NETWORK', '网络异常'));
        } finally {
            setIsSubmitting(false);
        }
    };

    const buildChannelModelPayload = (model, overrides = {}) => ({
        model_id: model.model_id,
        display_name: model.display_name || model.model_id,
        input_price_pico_per_token: Number(model.input_price_pico_per_token) || 0,
        output_price_pico_per_token: Number(model.output_price_pico_per_token) || 0,
        cached_input_price_pico_per_token: Number(model.cached_input_price_pico_per_token) || 0,
        cache_write_input_price_pico_per_token: Number(model.cache_write_input_price_pico_per_token) || 0,
        cache_write_1h_input_price_pico_per_token: Number(model.cache_write_1h_input_price_pico_per_token) || 0,
        context_price_threshold: Number(model.context_price_threshold) || 0,
        high_input_price_pico_per_token: Number(model.high_input_price_pico_per_token) || 0,
        high_cached_input_price_pico_per_token: Number(model.high_cached_input_price_pico_per_token) || 0,
        high_output_price_pico_per_token: Number(model.high_output_price_pico_per_token) || 0,
        max_context_length: Number(model.max_context_length) || 0,
        weight: Number(model.weight) || 1,
        status: model.status === 2 ? 2 : 1,
        model_category: normalizeModelCategory(model.model_category, model.model_id),
        billing_mode: normalizeBillingMode(model.billing_mode, model.model_category),
        allowed_endpoints: model.allowed_endpoints ?? defaultAllowedEndpointsForCategory(model.model_category),
        endpoint_policy: model.endpoint_policy || 'all',
        moderation_level: model.moderation_level || 'off',
        moderation_fail_mode: model.moderation_fail_mode || 'open',
        ...overrides,
    });

    const handleToggleModelStatus = async (model) => {
        const nextStatus = model.status === 1 ? 2 : 1;
        if (nextStatus === 1) {
            const category = normalizeModelCategory(model.model_category, model.model_id);
            const billingMode = normalizeBillingMode(model.billing_mode, category);
            if (billingMode === 'token' && !hasTokenPrice(model)) {
                const ok = await confirm(t(
                    'CHANNEL_MGMT.MODEL.ZERO_PRICE_ENABLE_CONFIRM',
                    '该模型当前价格为 $0。启用后可能免费放量或计费异常，仍要启用吗？'
                ));
                if (!ok) return;
            } else if (category === 'image') {
                const ok = await confirm(t(
                    'CHANNEL_MGMT.MODEL.IMAGE_ENABLE_CONFIRM',
                    billingMode === 'token'
                        ? '该图片模型将只开放 /v1/images/generations，并按上游返回的 token usage 计费。确认启用吗？'
                        : '该图片模型将只开放 /v1/images/generations，并按官方图片张数矩阵计费。确认启用吗？'
                ));
                if (!ok) return;
            } else if (category === 'video') {
                const ok = await confirm(t(
                    'CHANNEL_MGMT.MODEL.VIDEO_ENABLE_CONFIRM',
                    '该视频模型将只开放 /v1/videos/generations，并按官方输出视频秒数矩阵计费。确认启用吗？'
                ));
                if (!ok) return;
            } else if (looksLikeMediaModel(model.model_id)) {
                toast.error(t('CHANNEL_MGMT.MODEL.MEDIA_METADATA_MISMATCH', '模型名称像媒体模型，但能力分类不是图片/视频，请先编辑模型能力后再启用。'));
                return;
            }
        }
        setIsSubmitting(true);
        try {
            const payload = buildChannelModelPayload(model, { status: nextStatus });
            const data = await authFetch(`/api/admin/channel-models/${model.id}`, { method: 'PUT', body: payload });
            if (data.success) {
                fetchModels(selectedChannel.id);
                toast.success(nextStatus === 1
                    ? t('CHANNEL_MGMT.MODEL_ENABLED', '模型已启用')
                    : t('CHANNEL_MGMT.MODEL_DISABLED', '模型已禁用'));
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
                toast.success(t('CHANNEL_MGMT.MODEL_DELETED', '模型已删除'));
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
                toast.success(skipped > 0
                    ? t('CHANNEL_MGMT.IMPORT_SUCCESS_SKIPPED', '已添加 {{added}} 个，跳过 {{skipped}} 个已存在', { added, skipped })
                    : t('CHANNEL_MGMT.IMPORT_SUCCESS', '已添加 {{added}} 个模型', { added }));
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
    const modelModerationStats = useMemo(() => {
        const stats = {
            total: channelModels.length,
            reviewed: 0,
            smart: 0,
            strict: 0,
            off: 0,
            failClosed: 0,
            failOpen: 0,
        };
        for (const model of channelModels) {
            const level = normalizeModerationLevel(model);
            const hasReview = level !== 'off';
            if (hasReview) stats.reviewed++;
            else stats.off++;
            if (modelHasSmartModeration(model)) stats.smart++;
            if (level === 'strict') stats.strict++;
            if (hasReview && modelIsFailClosed(model)) stats.failClosed++;
            if (hasReview && !modelIsFailClosed(model)) stats.failOpen++;
        }
        return stats;
    }, [channelModels]);

    const filteredModels = useMemo(() => {
        const q = modelSearchTerm.trim().toLowerCase();
        return channelModels.filter(m => {
            const matchesSearch = !q || m.model_id.toLowerCase().includes(q);
            if (!matchesSearch) return false;
            switch (moderationFilter) {
                case 'reviewed':
                    return modelHasAnyModeration(m);
                case 'smart':
                    return modelHasSmartModeration(m);
                case 'strict':
                    return normalizeModerationLevel(m) === 'strict';
                case 'off':
                    return !modelHasAnyModeration(m);
                case 'fail_open':
                    return modelHasAnyModeration(m) && !modelIsFailClosed(m);
                default:
                    return true;
            }
        });
    }, [channelModels, modelSearchTerm, moderationFilter]);
    const groupedModelRows = useMemo(() => groupModelsByProvider(filteredModels).flatMap(providerGroup => {
        const providerEnabled = providerGroup.items.filter(m => m.status === 1).length;
        const providerDisabled = providerGroup.items.length - providerEnabled;
        const providerModerated = providerGroup.items.filter(modelHasAnyModeration).length;
        const providerSmart = providerGroup.items.filter(modelHasSmartModeration).length;
        const providerFailOpen = providerGroup.items.filter(m => modelHasAnyModeration(m) && !modelIsFailClosed(m)).length;
        const familyMap = new Map();
        for (const model of providerGroup.items) {
            const family = inferModelFamily(model.model_id);
            if (!familyMap.has(family.key)) {
                familyMap.set(family.key, { family, items: [] });
            }
            familyMap.get(family.key).items.push(model);
        }
        const familyRows = Array.from(familyMap.values())
            .map(group => ({
                ...group,
                items: [...group.items].sort((a, b) => {
                    const statusDelta = (b.status === 1 ? 1 : 0) - (a.status === 1 ? 1 : 0);
                    return statusDelta || String(a.model_id || '').localeCompare(String(b.model_id || ''));
                }),
            }))
            .sort((a, b) => a.family.order - b.family.order || a.family.name.localeCompare(b.family.name))
            .flatMap(group => {
                const enabled = group.items.filter(m => m.status === 1).length;
                const disabled = group.items.length - enabled;
                const moderated = group.items.filter(modelHasAnyModeration).length;
                const smart = group.items.filter(modelHasSmartModeration).length;
                const failOpen = group.items.filter(m => modelHasAnyModeration(m) && !modelIsFailClosed(m)).length;
                return [
                    { isFamilyGroup: true, provider: providerGroup.provider, family: group.family, items: group.items, enabled, disabled, moderated, smart, failOpen },
                    ...group.items.map(m => ({ model: m, providerObj: inferModelProvider(m.model_id), family: group.family })),
                ];
            });
        return [
            {
                isProviderGroup: true,
                provider: providerGroup.provider,
                items: providerGroup.items,
                enabled: providerEnabled,
                disabled: providerDisabled,
                moderated: providerModerated,
                smart: providerSmart,
                failOpen: providerFailOpen,
            },
            ...familyRows,
        ];
    }), [filteredModels]);

    // --- Sub-Renders ---

    if (view === 'models') {
        const moderationFilterOptions = [
            { id: 'all', label: t('CHANNEL_MGMT.MOD.FILTER_ALL', '全部'), count: channelModels.length },
            { id: 'reviewed', label: t('CHANNEL_MGMT.MOD.FILTER_REVIEWED', '已接入审核'), count: modelModerationStats.reviewed },
            { id: 'smart', label: t('CHANNEL_MGMT.MOD.FILTER_SMART', '智能审核'), count: modelModerationStats.smart },
            { id: 'strict', label: t('CHANNEL_MGMT.MOD.FILTER_STRICT', 'STRICT'), count: modelModerationStats.strict },
            { id: 'off', label: t('CHANNEL_MGMT.MOD.FILTER_OFF', '未审核'), count: modelModerationStats.off },
            { id: 'fail_open', label: t('CHANNEL_MGMT.MOD.FILTER_FAIL_OPEN', '审核失败放行'), count: modelModerationStats.failOpen },
        ];
        return (
            <div className="w-full animation-fade-in relative z-10">
                <button onClick={() => setView('channels')} className="flex items-center gap-2 text-on-surface-variant hover:text-primary mb-6 text-sm font-medium">
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
                            <Network size={18} className="text-primary" />
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

                <div className="grid grid-cols-2 md:grid-cols-5 gap-2 mb-4">
                    <ModerationStatCard
                        icon={Box}
                        label={t('CHANNEL_MGMT.MOD.SUMMARY_TOTAL', '模型总数')}
                        value={modelModerationStats.total}
                        tone="neutral"
                    />
                    <ModerationStatCard
                        icon={ShieldCheck}
                        label={t('CHANNEL_MGMT.MOD.SUMMARY_REVIEWED', '接入审核')}
                        value={modelModerationStats.reviewed}
                        tone="success"
                    />
                    <ModerationStatCard
                        icon={AlertTriangle}
                        label={t('CHANNEL_MGMT.MOD.SUMMARY_SMART', '智能审核')}
                        value={modelModerationStats.smart}
                        tone="primary"
                    />
                    <ModerationStatCard
                        icon={ShieldCheck}
                        label={t('CHANNEL_MGMT.MOD.SUMMARY_FAIL_CLOSED', 'Fail-closed')}
                        value={modelModerationStats.failClosed}
                        tone="success"
                    />
                    <ModerationStatCard
                        icon={ShieldOff}
                        label={t('CHANNEL_MGMT.MOD.SUMMARY_OFF', '未审核')}
                        value={modelModerationStats.off}
                        tone={modelModerationStats.off > 0 ? 'warning' : 'neutral'}
                    />
                </div>

                <div className="flex flex-wrap items-center gap-2 mb-4">
                    <span className="text-xs font-semibold text-on-surface-variant mr-1">
                        {t('CHANNEL_MGMT.MOD.FILTER_LABEL', '审核筛选')}
                    </span>
                    {moderationFilterOptions.map(option => {
                        const active = moderationFilter === option.id;
                        return (
                            <button
                                key={option.id}
                                type="button"
                                onClick={() => setModerationFilter(option.id)}
                                className={`inline-flex items-center gap-1.5 rounded-control border px-3 py-1.5 text-xs font-medium transition ${
                                    active
                                        ? 'border-primary bg-primary text-on-primary'
                                        : 'border-outline-variant bg-surface-container text-on-surface-variant hover:text-on-surface hover:bg-surface-container-high'
                                }`}
                            >
                                <span>{option.label}</span>
                                <span className={`font-mono ${active ? 'text-on-primary/80' : 'text-on-surface-variant'}`}>
                                    {option.count}
                                </span>
                            </button>
                        );
                    })}
                    {moderationFilter !== 'all' && (
                        <span className="text-xs text-on-surface-variant">
                            {t('CHANNEL_MGMT.MOD.FILTER_RESULT', '当前显示 {{shown}} / {{total}} 个模型', { shown: filteredModels.length, total: channelModels.length })}
                        </span>
                    )}
                </div>

                {/* Model Table */}
                <div className="bg-surface-container border border-outline-variant rounded-overlay overflow-hidden ">
                    <div className="overflow-x-auto">
                        
                        <DataTable
                            columns={[
                                { key: 'model', header: t('CHANNEL_MGMT.MODEL.TABLE.MODEL_ID'), width: '27%', render: row => {
                                    if (row.isProviderGroup) {
                                        return (
                                            <div className="flex items-center gap-2.5 py-1">
                                                <ProviderIcon provider={row.provider} />
                                                <span className="text-sm font-semibold text-on-surface">{row.provider.name}</span>
                                                <span className="fl-brand-chip" data-brand={brandFor(row.provider.name)}>{row.items.length}</span>
                                                <ChevronRight size={16} className="text-on-surface-variant ml-auto" />
                                            </div>
                                        );
                                    }
                                    if (row.isFamilyGroup) {
                                        return (
                                            <div className="flex items-center gap-2 pl-8 py-0.5">
                                                <span className="h-1.5 w-1.5 rounded-full bg-primary/70" />
                                                <span className="text-xs font-semibold text-on-surface">{row.family.name}</span>
                                                <span className="font-mono text-[11px] text-on-surface-variant">{row.items.length}</span>
                                            </div>
                                        );
                                    }
                                    const m = row.model;
                                    return (
                                    <div className={`font-mono font-semibold flex flex-col gap-1 w-full pl-8 ${m.status === 1 ? 'text-primary' : 'text-on-surface-variant'}`}>
                                        <div className="truncate" title={m.model_id}>{m.model_id}</div>
                                        <div className="flex items-center gap-1.5 text-[10px] font-sans">
                                            <ProviderIcon provider={row.providerObj} compact />
                                            <span className="text-on-surface-variant">{row.family.name}</span>
                                        </div>
                                        {m.endpoint_policy && m.endpoint_policy !== 'all' && (
                                            <EndpointPolicyBadge policy={m.endpoint_policy} />
                                        )}
                                    </div>
                                );}},
                                { key: 'max_ctx', header: t('CHANNEL_MGMT.MODEL.TABLE.MAX_CTX'), width: '8%', render: row => {
                                    if (row.isProviderGroup || row.isFamilyGroup) {
                                        return null;
                                    }
                                    const m = row.model;
                                    return (
                                    m.max_context_length > 0 ? (
                                        <span className="text-xs bg-surface-container-high/50 text-on-surface-variant px-2 py-1 rounded-control border border-outline-variant/50">
                                            {formatTokens(m.max_context_length)}
                                        </span>
                                    ) : <span className="text-outline-variant">-</span>
                                );}},
                                { key: 'runtime', header: t('CHANNEL_MGMT.MODEL.TABLE.RUNTIME', '能力 / 计费'), width: '12%', render: row => {
                                    if (row.isProviderGroup || row.isFamilyGroup) return null;
                                    return <RuntimePolicyCell model={row.model} />;
                                }},
                                { key: 'status', header: t('CHANNEL_MGMT.MODEL.TABLE.STATUS'), width: '9%', render: row => {
                                    if (row.isProviderGroup || row.isFamilyGroup) {
                                        return (
                                            <div className="flex flex-wrap gap-1.5 text-[11px]">
                                                <span className="text-success font-medium">{t('CHANNEL_MGMT.MODEL.GROUP_ENABLED', '{{count}} 启用', { count: row.enabled })}</span>
                                                {row.disabled > 0 && <span className="text-on-surface-variant">{t('CHANNEL_MGMT.MODEL.GROUP_DISABLED', '{{count}} 禁用', { count: row.disabled })}</span>}
                                            </div>
                                        );
                                    }
                                    const enabled = row.model.status === 1;
                                    return (
                                        <button
                                            type="button"
                                            onClick={() => handleToggleModelStatus(row.model)}
                                            disabled={isSubmitting}
                                            aria-pressed={enabled}
                                            title={enabled ? t('CHANNEL_MGMT.MODEL.TOGGLE_DISABLE', '点击禁用') : t('CHANNEL_MGMT.MODEL.TOGGLE_ENABLE', '点击启用')}
                                            className={`inline-flex items-center gap-1.5 rounded-full border px-2 py-1 text-[11px] font-semibold transition disabled:opacity-50 ${
                                                enabled
                                                    ? 'bg-success/10 text-success border-success/30 hover:bg-success/15'
                                                    : 'bg-surface-container text-on-surface-variant border-outline-variant hover:text-on-surface hover:bg-surface-container-high'
                                            }`}
                                        >
                                            <span className={`relative inline-flex h-3.5 w-6 rounded-full transition ${enabled ? 'bg-success/45' : 'bg-outline-variant/50'}`}>
                                                <span className={`absolute top-0.5 h-2.5 w-2.5 rounded-full bg-current transition-transform ${enabled ? 'translate-x-3' : 'translate-x-0.5'}`} />
                                            </span>
                                            {enabled ? t('CHANNEL_MGMT.MODEL.STATUS_ENABLED') : t('CHANNEL_MGMT.MODEL.STATUS_DISABLED')}
                                        </button>
                                    );
                                }},
                                { key: 'moderation', header: t('CHANNEL_MGMT.MODEL.TABLE.MODERATION', '内容审核'), width: '14%', render: row => {
                                    if (row.isProviderGroup || row.isFamilyGroup) {
                                        return <ModerationGroupSummary row={row} />;
                                    }
                                    return <ModerationPolicyCell model={row.model} />;
                                }},
                                { key: 'base_pricing', header: t('CHANNEL_MGMT.MODEL.TABLE.BASE_PRICING'), width: '14%', render: row => {
                                    if (row.isProviderGroup || row.isFamilyGroup) return null;
                                    const m = row.model;
                                    const category = normalizeModelCategory(m.model_category, m.model_id);
                                    if (category === 'image') {
                                        const price = Number(m.min_image_price) || 0;
                                        return (
                                            <div className="flex flex-col text-xs font-mono space-y-1 text-on-surface-variant">
                                                <span>{t('CHANNEL_MGMT.MODEL.IMAGE_OUTPUT', '图片')}: {price > 0 ? `${formatCurrency(price, 4)}/张` : '-'}</span>
                                                <span className="text-[11px] font-sans text-on-surface-variant">{t('CHANNEL_MGMT.MODEL.IMAGE_MATRIX_NOTE', '官方矩阵')}</span>
                                            </div>
                                        );
                                    }
                                    if (category === 'video') {
                                        const price = Number(m.min_video_second_price) || 0;
                                        return (
                                            <div className="flex flex-col text-xs font-mono space-y-1 text-on-surface-variant">
                                                <span>{t('CHANNEL_MGMT.MODEL.VIDEO_OUTPUT', '视频')}: {price > 0 ? `${formatCurrency(price, 4)}/秒` : '-'}</span>
                                                <span className="text-[11px] font-sans text-warning">{t('CHANNEL_MGMT.MODEL.VIDEO_PRESEEDED', '按秒计费')}</span>
                                            </div>
                                        );
                                    }
                                    // 后端只返回 *_pico_per_token；UI 展示 USD/1M tokens。
                                    const inUsd = picoPerTokenToUsdPerMillion(m.input_price_pico_per_token);
                                    const outUsd = picoPerTokenToUsdPerMillion(m.output_price_pico_per_token);
                                    const cacheUsd = picoPerTokenToUsdPerMillion(m.cached_input_price_pico_per_token);
                                    return (
                                        <div className="flex flex-col text-xs font-mono space-y-1 text-on-surface-variant">
                                            <span>{t('CHANNEL_MGMT.MODEL.IN')}: {formatCurrency(inUsd, 6)}</span>
                                            <span>{t('CHANNEL_MGMT.MODEL.OUT')}: {formatCurrency(outUsd, 6)}</span>
                                            {cacheUsd > 0 && <span className="text-primary">{t('CHANNEL_MGMT.MODEL.CACHE')}: {formatCurrency(cacheUsd, 6)}</span>}
                                        </div>
                                    );
                                }},
                                { key: 'tier_pricing', header: t('CHANNEL_MGMT.MODEL.TABLE.TIER_PRICING'), width: '15%', render: row => {
                                    if (row.isProviderGroup || row.isFamilyGroup) return null;
                                    const m = row.model;
                                    if (!(m.context_price_threshold > 0)) {
                                        return <span className="text-outline-variant">-</span>;
                                    }
                                    const hiIn = picoPerTokenToUsdPerMillion(m.high_input_price_pico_per_token);
                                    const hiOut = picoPerTokenToUsdPerMillion(m.high_output_price_pico_per_token);
                                    const hiCache = picoPerTokenToUsdPerMillion(m.high_cached_input_price_pico_per_token);
                                    return (
                                        <div className="flex flex-col text-xs space-y-1 bg-warning/10 border border-warning/30 p-2 rounded-control w-fit">
                                            <div className="font-semibold text-warning whitespace-nowrap">
                                                {t('CHANNEL_MGMT.MODEL.TIER_ACTIVE', { threshold: formatTokens(m.context_price_threshold) })}
                                            </div>
                                            <div className="font-mono text-warning/80">
                                                <div>In: {formatCurrency(hiIn, 6)}</div>
                                                <div>Out: {formatCurrency(hiOut, 6)}</div>
                                                {hiCache > 0 && <div className="text-primary">{t('CHANNEL_MGMT.MODEL.CACHE')}: {formatCurrency(hiCache, 6)}</div>}
                                            </div>
                                        </div>
                                    );
                                }},
                                { key: 'weight', header: t('CHANNEL_MGMT.MODEL.TABLE.WEIGHT'), width: '5%', render: row => (row.isProviderGroup || row.isFamilyGroup) ? null : row.model.weight },
                                { key: 'actions', header: t('CHANNEL_MGMT.MODEL.TABLE.ACTIONS'), align: 'right', width: '8%', render: row => (row.isProviderGroup || row.isFamilyGroup) ? null : (
                                    <>
                                        <button onClick={() => handleOpenModelModal(row.model)} className="p-2 hover:bg-primary/20 text-primary rounded-control mr-2" aria-label={t('COMMON.EDIT', '编辑')}><Edit2 size={16} /></button>
                                        <DestructiveIconButton onClick={() => handleDeleteModel(row.model.id)} icon={Trash2} size={16} title={t('COMMON.DELETE', '删除')} />
                                    </>
                                )}
                            ]}
                            rows={groupedModelRows}
                            rowKey={row => row.isProviderGroup ? `provider-${row.provider.name}` : row.isFamilyGroup ? `family-${row.provider.name}-${row.family.key}` : row.model.id}
                            rowClassName={row => row.isProviderGroup
                                ? 'bg-surface-container-high/80 hover:bg-surface-container-high border-t border-outline-variant/80'
                                : row.isFamilyGroup
                                ? 'bg-surface-container/65 hover:bg-surface-container border-t border-outline-variant/40'
                                : row.model?.status === 1 ? '' : 'opacity-70'
                            }
                            loading={loadingModels}
                            emptyTitle={t('CHANNEL_MGMT.MODEL.TABLE.EMPTY', 'No models found for this channel.')}
                        />

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
                                        onChange={e=>setModelForm(withEndpointPolicyDefaults({...modelForm, model_id: e.target.value}))}
                                        className="w-full bg-surface-container-high border border-outline-variant rounded-overlay px-4 py-2.5 text-on-surface"
                                    />
                                </div>
                                <div>
                                    <label htmlFor="channel-model-display-name" className="block text-xs font-medium text-on-surface-variant mb-1">{t('CHANNEL_MGMT.MODEL.MODAL.DISPLAY_NAME')}</label>
                                    <input id="channel-model-display-name" type="text" value={modelForm.display_name || ''} onChange={e=>setModelForm({...modelForm, display_name: e.target.value})} className="w-full bg-surface-container-high border border-outline-variant rounded-overlay px-4 py-2.5 text-on-surface" />
                                </div>
                                <fieldset className="border border-outline-variant rounded-overlay p-4">
                                    <legend className="px-2 text-xs font-semibold text-on-surface flex items-center gap-2">
                                        <Box size={13} className="text-on-surface-variant" />
                                        {t('CHANNEL_MGMT.RUNTIME.LEGEND', '运行能力与计费单元')}
                                    </legend>
                                    <div className="grid grid-cols-1 sm:grid-cols-3 gap-4 mt-2">
                                        <div>
                                            <label htmlFor="channel-model-category" className="block text-xs font-medium text-on-surface-variant mb-1">
                                                {t('CHANNEL_MGMT.RUNTIME.CATEGORY', '能力分类')}
                                            </label>
                                            <select
                                                id="channel-model-category"
                                                value={normalizeModelCategory(modelForm.model_category, modelForm.model_id)}
                                                onChange={e => applyModelCategory(e.target.value)}
                                                className="w-full bg-surface-container-high border border-outline-variant rounded-overlay px-4 py-2.5 text-on-surface"
                                            >
                                                <option value="text">{t('CHANNEL_MGMT.RUNTIME.CATEGORY_TEXT', '文本')}</option>
                                                <option value="image">{t('CHANNEL_MGMT.RUNTIME.CATEGORY_IMAGE', '图片')}</option>
                                                <option value="video">{t('CHANNEL_MGMT.RUNTIME.CATEGORY_VIDEO', '视频')}</option>
                                            </select>
                                        </div>
                                        <div>
                                            <label htmlFor="channel-model-billing-mode" className="block text-xs font-medium text-on-surface-variant mb-1">
                                                {t('CHANNEL_MGMT.RUNTIME.BILLING_MODE', '计费单元')}
                                            </label>
                                            <select
                                                id="channel-model-billing-mode"
                                                value={normalizeBillingMode(modelForm.billing_mode, modelForm.model_category)}
                                                onChange={e => setModelForm({...modelForm, billing_mode: e.target.value})}
                                                className="w-full bg-surface-container-high border border-outline-variant rounded-overlay px-4 py-2.5 text-on-surface"
                                            >
                                                <option value="token">{t('CHANNEL_MGMT.RUNTIME.BILLING_TOKEN', '按 token')}</option>
                                                <option value="image">{t('CHANNEL_MGMT.RUNTIME.BILLING_IMAGE', '按图片')}</option>
                                                <option value="video_second">{t('CHANNEL_MGMT.RUNTIME.BILLING_VIDEO_SECOND', '按视频秒')}</option>
                                            </select>
                                        </div>
                                        <div>
                                            <label htmlFor="channel-model-allowed-endpoints" className="block text-xs font-medium text-on-surface-variant mb-1">
                                                {t('CHANNEL_MGMT.RUNTIME.ALLOWED_ENDPOINTS', '允许端点 JSON')}
                                            </label>
                                            <input
                                                id="channel-model-allowed-endpoints"
                                                type="text"
                                                value={modelForm.allowed_endpoints ?? ''}
                                                onChange={e => setModelForm({...modelForm, allowed_endpoints: e.target.value})}
                                                placeholder={defaultAllowedEndpointsForCategory(modelForm.model_category) || t('CHANNEL_MGMT.RUNTIME.TEXT_ENDPOINTS_PLACEHOLDER', '空 = 文本默认端点')}
                                                className="w-full bg-surface-container-high border border-outline-variant rounded-overlay px-4 py-2.5 text-on-surface font-mono text-xs"
                                            />
                                        </div>
                                    </div>
                                    {normalizeModelCategory(modelForm.model_category, modelForm.model_id) !== 'text' && (
                                        <p className="mt-3 text-[11px] leading-relaxed text-on-surface-variant">
                                            {t('CHANNEL_MGMT.RUNTIME.MEDIA_HINT', '媒体模型价格来自默认官方计费矩阵。当前支持：xAI 图像/视频（按 cost_in_usd_ticks 实扣）、Gemini image 系列（BillingMode=token，按 candidatesTokenCount × output rate 计费）、Imagen 系列（BillingMode=image，按 candidates[].inlineData 数量 × flat 价计费）。启用前请确保 AllowedEndpoints 包含正确路径（如 Gemini/Imagen 需 /v1beta/models）。')}
                                        </p>
                                    )}
                                </fieldset>
                                <div className="grid grid-cols-1 sm:grid-cols-3 gap-4">
                                    <div>
                                        <label htmlFor="channel-model-max-context" className="block text-xs font-medium text-on-surface-variant mb-1">{t('CHANNEL_MGMT.MODEL.MODAL.MAX_CONTEXT_LENGTH')} <span className="ml-1 text-on-surface-variant/70">(Tokens)</span></label>
                                        <input id="channel-model-max-context" type="number" min="0" value={modelForm.max_context_length || ''} onChange={e=>setModelForm({...modelForm, max_context_length: parseInt(e.target.value) || 0})} className="w-full bg-surface-container-high border border-outline-variant rounded-overlay px-4 py-2.5 text-on-surface" placeholder={t('CHANNEL_MGMT.MODEL.MODAL.NO_LIMIT_PLACEHOLDER', '0 = 不限制')} />
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
                                    <div>
                                        <label htmlFor="channel-model-status" className="block text-xs font-medium text-on-surface-variant mb-1">{t('CHANNEL_MGMT.MODEL.MODAL.STATUS', '启用状态')}</label>
                                        <select
                                            id="channel-model-status"
                                            value={modelForm.status === 2 ? 2 : 1}
                                            onChange={e=>setModelForm({...modelForm, status: parseInt(e.target.value)})}
                                            className="w-full bg-surface-container-high border border-outline-variant rounded-overlay px-4 py-2.5 text-on-surface"
                                        >
                                            <option value={1}>{t('CHANNEL_MGMT.MODEL.STATUS_ENABLED')}</option>
                                            <option value={2}>{t('CHANNEL_MGMT.MODEL.STATUS_DISABLED')}</option>
                                        </select>
                                    </div>
                                </div>
                                {normalizeBillingMode(modelForm.billing_mode, modelForm.model_category) === 'token' ? (
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
                                        <label htmlFor="channel-model-cache-price" className="block text-xs font-medium text-primary mb-1">
                                            {t('CHANNEL_MGMT.MODEL.MODAL.CACHE_PRICE')}
                                            <span className="ml-1 text-primary/70">({inputCurrency === 'CNY' ? '￥/1M' : '$/1M'})</span>
                                        </label>
                                        <input id="channel-model-cache-price" type="number" step="0.000001" min="0" value={modelForm.cached_input_price} onChange={e=>setModelForm({...modelForm, cached_input_price: e.target.value})} className="w-full bg-surface border border-primary/30 rounded-control px-3 py-2 text-on-surface" />
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
                                ) : (
                                    <div className="p-4 bg-surface-container-high border border-outline-variant rounded-overlay text-xs text-on-surface-variant leading-relaxed">
                                        {normalizeModelCategory(modelForm.model_category, modelForm.model_id) === 'image'
                                            ? t('CHANNEL_MGMT.RUNTIME.IMAGE_PRICING_HINT', '固定图片单价模型按 model_pricing_rules 的官方图片价格矩阵扣费；token 计费图片模型在渠道绑定里配置 token 单价，并只允许 /v1/images/generations。')
                                            : t('CHANNEL_MGMT.RUNTIME.VIDEO_PRICING_HINT', '视频模型不在渠道绑定里手填 token 单价。启用后按 model_pricing_rules 的官方输出视频秒数矩阵扣费，并只允许 /v1/videos/generations。')}
                                    </div>
                                )}
                                {normalizeBillingMode(modelForm.billing_mode, modelForm.model_category) === 'token' && (
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
                                )}

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

                                {/* fix CRITICAL R23: per-ChannelModel moderation policy. */}
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
                                                onChange={e=>setModelForm({...modelForm, moderation_level: e.target.value, confirm_official_no_moderation: false})}
                                                className="w-full bg-surface-container-high border border-outline-variant rounded-overlay px-4 py-2.5 text-on-surface"
                                            >
                                                <option value="off">{t('CHANNEL_MGMT.MOD.LEVEL_OFF', 'OFF — 不审核')}</option>
                                                <option value="keyword">{t('CHANNEL_MGMT.MOD.LEVEL_KEYWORD', 'KW — 仅关键字快扫')}</option>
                                                <option value="moderation">{t('CHANNEL_MGMT.MOD.LEVEL_MODERATION', 'MOD — 仅智能审核服务')}</option>
                                                <option value="strict">{t('CHANNEL_MGMT.MOD.LEVEL_STRICT', 'STRICT — 关键词预警 + 智能二审（高风险模型）')}</option>
                                            </select>
                                        </div>
                                        <div>
                                            <label htmlFor="moderation-fail-mode" className="block text-xs font-medium text-on-surface-variant mb-1">
                                                {t('CHANNEL_MGMT.MOD.FAIL_MODE', '审核服务不可达时')}
                                            </label>
                                            <select
                                                id="moderation-fail-mode"
                                                value={modelForm.moderation_fail_mode || 'open'}
                                                onChange={e=>setModelForm({...modelForm, moderation_fail_mode: e.target.value})}
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
                                            {t('CHANNEL_MGMT.MOD.OFFICIAL_HINT', '当前渠道指向官方 API（OpenAI / Anthropic / Gemini）。建议至少设为 MOD + CLOSED 防账号被封禁。点击右下角"应用推荐预设"。')}
                                        </div>
                                    )}
                                    <div className="flex items-center justify-between mt-3">
                                        <span className="text-[11px] text-on-surface-variant italic">
                                            {isOfficialChannel
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
                                {/* Disable save until official-channel moderation-off risk is acknowledged. */}
                                <button
                                    onClick={handleModelSubmit}
                                    disabled={isSubmitting || (isOfficialChannel && modelForm.moderation_level === 'off' && !modelForm.confirm_official_no_moderation)}
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
                                        <Network size={22} className="text-primary" />
                                        {t('CHANNEL_MGMT.UPSTREAM_MODAL.TITLE')}
                                    </h3>
                                    <p className="text-xs text-on-surface-variant mt-1">{t('CHANNEL_MGMT.UPSTREAM_MODAL.DESC', { type: selectedChannel.type })}</p>
                                </div>
                                <button ref={upstreamModalCloseRef} onClick={() => setIsUpstreamModalOpen(false)} aria-label={t('COMMON.CLOSE', '关闭')}><X size={20} className="text-on-surface-variant hover:text-white" /></button>
                            </div>

                            <div className="p-6 overflow-y-auto flex-1">
                                {loadingUpstream ? (
                                    <div className="flex flex-col items-center justify-center py-12">
                                        <RefreshCw size={32} className="text-primary mb-4" />
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
                                                className="text-xs text-primary hover:underline font-medium"
                                            >
                                                {selectedUpstreamModels.length === upstreamModels.length ? t('CHANNEL_MGMT.UPSTREAM_MODAL.BTN_DESELECT_ALL') : t('CHANNEL_MGMT.UPSTREAM_MODAL.BTN_SELECT_ALL')}
                                            </button>
                                        </div>
                                        <div className="space-y-6">
                                            {/* Group by provider instead of initial letter. */}
                                            {upstreamModelsByProvider.map(([provider, models]) => (
                                                <div key={provider} className="space-y-3">
                                                    <h4 className="flex items-center gap-3">
                                                        <span className="bg-primary/10 text-primary border border-primary/30 font-semibold px-2.5 py-0.5 flex items-center rounded-control text-xs whitespace-nowrap">
                                                            {provider} <span className="ml-1.5 text-on-surface-variant font-mono">{models.length}</span>
                                                        </span>
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
                                                                            ? 'bg-primary/10 border-primary/50 text-primary'
                                                                            : 'bg-surface-container-high border-outline-variant text-on-surface-variant hover:border-outline-variant'}`}
                                                                >
                                                                    <div className={`w-4 h-4 rounded-control border flex items-center justify-center shrink-0
                                                                        ${isSelected ? 'bg-primary border-primary' : 'border-outline-variant'}`}>
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
                                {/* Use Trans interpolation and JSX components to avoid dangerouslySetInnerHTML. */}
                                <span className="text-sm text-on-surface-variant">
                                    <Trans i18nKey="CHANNEL_MGMT.UPSTREAM_MODAL.SELECTED_COUNT"
                                        components={{ strong: <strong className="text-primary mx-1" /> }}
                                        values={{ count: selectedUpstreamModels.length }} />
                                </span>
                                <div className="flex gap-3">
                                    <button onClick={() => setIsUpstreamModalOpen(false)} className="px-5 py-2.5 text-on-surface-variant hover:text-white hover:bg-surface-container-high rounded-overlay">{t('CHANNEL_MGMT.MODEL.MODAL.BTN_CANCEL')}</button>
                                    <button
                                        onClick={handleBatchImport}
                                        disabled={isSubmitting || selectedUpstreamModels.length === 0}
                                        className="px-6 py-2.5 bg-primary hover:opacity-90 text-on-primary disabled:opacity-50 disabled:cursor-not-allowed rounded-overlay font-medium flex items-center gap-2 "
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
    // Sprint J-3 batch 5: migrated hand-rolled `<h1 text-4xl font-black>` to
    // the canonical PageHeader primitive so this page picks up the new
    // 32px display font + standard icon block + sub treatment used by
    // every other admin page.
    return (
        <div className="w-full animation-fade-in relative z-10">
            <PageHeader
                title={t('CHANNEL_MGMT.TITLE')}
                sub={t('CHANNEL_MGMT.SUBTITLE')}
                icon={Network}
            />

            <div className="flex flex-col md:flex-row justify-between items-start md:items-center gap-4 mb-6 relative z-20">
                <div className="relative w-full md:w-96">
                    <input
                        type="text"
                        placeholder={t('CHANNEL_MGMT.SEARCH_CHANNEL')}
                        value={searchTerm}
                        onChange={(e) => setSearchTerm(e.target.value)}
                        className="w-full bg-surface-container border border-outline-variant rounded-overlay pl-11 pr-4 py-3 text-sm text-on-surface-variant focus:outline-none focus:border-primary focus:ring-1 focus:ring-primary/50"
                    />
                    <Search size={18} className="absolute left-4 top-1/2 -translate-y-1/2 text-on-surface-variant" />
                </div>
                <button
                    onClick={() => handleOpenChanModal()}
                    className="flex items-center gap-2 bg-primary hover:opacity-90 text-on-primary px-5 py-3 rounded-overlay font-medium active:scale-95 border border-primary/50"
                >
                    <Plus size={18} />
                    {t('CHANNEL_MGMT.BTN_ADD_CHANNEL')}
                </button>
            </div>

            <ChannelCircuitMonitor />

            <div className="bg-surface-container border border-outline-variant rounded-overlay overflow-hidden ">
                <div className="overflow-x-auto">
                    
                    <DataTable
                        columns={[
                            { key: 'id', header: t('CHANNEL_MGMT.CHANNEL_TABLE.ID'), width: '15%', render: c => (
                                <div className="font-bold text-on-surface-variant">
                                    {c.id}
                                    <div className="text-xs text-on-surface-variant font-normal mt-1">{c.name}</div>
                                </div>
                            )},
                            { key: 'type', header: t('CHANNEL_MGMT.CHANNEL_TABLE.TYPE'), width: '15%', render: c => (
                                <StatusBadge variant="success">{c.type}</StatusBadge>
                            )},
                            { key: 'key_url', header: t('CHANNEL_MGMT.CHANNEL_TABLE.KEY') + ' / URL', render: c => (
                                <div className="font-mono text-xs opacity-80 flex flex-col gap-1 w-full overflow-hidden">
                                    <div className="truncate w-full max-w-[300px]" title={c.key}>{c.key}</div>
                                    {c.base_url && <div className="truncate text-on-surface-variant/70 w-full max-w-[300px]" title={c.base_url}>{c.base_url}</div>}
                                </div>
                            )},
                            { key: 'weight', header: t('CHANNEL_MGMT.CHANNEL_TABLE.WEIGHT'), width: '10%', render: c => (
                                <span className="text-primary">{c.weight}</span>
                            )},
                            { key: 'status', header: t('CHANNEL_MGMT.CHANNEL_TABLE.STATUS', '状态'), width: '11%', render: c => {
                                const enabled = c.status === 1;
                                return (
                                    <button
                                        type="button"
                                        onClick={() => handleToggleChannelStatus(c)}
                                        disabled={isSubmitting}
                                        aria-pressed={enabled}
                                        title={enabled
                                            ? t('CHANNEL_MGMT.CHANNEL_TOGGLE_DISABLE', '点击禁用整条渠道')
                                            : t('CHANNEL_MGMT.CHANNEL_TOGGLE_ENABLE', '点击启用整条渠道')}
                                        className={`inline-flex items-center gap-1.5 rounded-full border px-2 py-1 text-[11px] font-semibold transition disabled:opacity-50 ${
                                            enabled
                                                ? 'bg-success/10 text-success border-success/30 hover:bg-success/15'
                                                : 'bg-surface-container text-on-surface-variant border-outline-variant hover:text-on-surface hover:bg-surface-container-high'
                                        }`}
                                    >
                                        <span className={`relative inline-flex h-3.5 w-6 rounded-full transition ${enabled ? 'bg-success/45' : 'bg-outline-variant/50'}`}>
                                            <span className={`absolute top-0.5 h-2.5 w-2.5 rounded-full bg-current transition-transform ${enabled ? 'translate-x-3' : 'translate-x-0.5'}`} />
                                        </span>
                                        {enabled
                                            ? t('CHANNEL_MGMT.CHANNEL_TABLE.STATUS_ENABLED', '启用')
                                            : t('CHANNEL_MGMT.CHANNEL_TABLE.STATUS_DISABLED', '禁用')}
                                    </button>
                                );
                            }},
                            { key: 'actions', header: t('CHANNEL_MGMT.CHANNEL_TABLE.ACTIONS'), align: 'right', width: '240px', render: c => (
                                <div className="flex items-center justify-end gap-2">
                                    <button onClick={() => handleSelectChannel(c)} className="p-2 flex shrink-0 items-center gap-1 hover:bg-primary/20 text-primary rounded-control bg-surface-variant whitespace-nowrap"><Box size={14} /> {t('CHANNEL_MGMT.BTN_MODELS')}</button>
                                    <button onClick={() => handleOpenChanModal(c)} className="p-2 shrink-0 hover:bg-primary/20 text-primary rounded-control bg-surface-variant "><Edit2 size={16} /></button>
                                    <DestructiveIconButton onClick={() => handleDeleteChan(c.id)} icon={Trash2} size={16} title={t('COMMON.DELETE', '删除')} />
                                </div>
                            )}
                        ]}
                        rows={filteredChannels}
                        loading={loading}
                        emptyTitle={t('CHANNEL_MGMT.CHANNEL_TABLE.EMPTY', 'No channels connected yet.')}
                    />

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
                                <select id="channel-form-type" required value={chanForm.type} onChange={e=>setChanForm({...chanForm, type: e.target.value})} className="w-full bg-surface-container-high border border-outline-variant rounded-overlay px-4 py-2.5 text-on-surface cursor-pointer hover:border-primary/50 outline-none">
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
                                <label htmlFor="channel-form-proxy" className="block text-xs font-medium text-on-surface-variant mb-1">{t('CHANNEL_MGMT.MODAL_CHANNEL.PROXY_URL', '代理跳板 (Proxy URL)')}</label>
                                <input id="channel-form-proxy" type="text" value={chanForm.proxy_url} onChange={e=>setChanForm({...chanForm, proxy_url: e.target.value})} placeholder={t('CHANNEL_MGMT.MODAL_CHANNEL.PROXY_PLACEHOLDER', 'http://127.0.0.1:8080 或 https://proxy.example.com:443')} className="w-full bg-surface-container-high border border-outline-variant rounded-overlay px-4 py-2.5 text-on-surface font-mono text-sm" />
                            </div>
                            <div>
                                <label htmlFor="channel-form-headers" className="block text-xs font-medium text-on-surface-variant mb-1">{t('CHANNEL_MGMT.MODAL_CHANNEL.HEADERS_JSON', '自定义网关请求头 (Custom Headers JSON)')}</label>
                                <textarea id="channel-form-headers" value={chanForm.headers} onChange={e=>setChanForm({...chanForm, headers: e.target.value})} placeholder='{"x-custom-tenant": "vip-01"}' rows={3} className="w-full bg-surface-container-high border border-outline-variant rounded-overlay px-4 py-2.5 text-on-surface font-mono text-xs"></textarea>
                            </div>
                            <div>
                                <label htmlFor="channel-form-weight" className="block text-xs font-medium text-on-surface-variant mb-1">{t('CHANNEL_MGMT.MODAL_CHANNEL.WEIGHT')}</label>
                                <input id="channel-form-weight" type="number" min="1" value={chanForm.weight} onChange={e=>setChanForm({...chanForm, weight: e.target.value})} className="w-full bg-surface-container-high border border-outline-variant rounded-overlay px-4 py-2.5 text-on-surface" />
                            </div>
                            <div>
                                <label htmlFor="channel-form-status" className="block text-xs font-medium text-on-surface-variant mb-1">
                                    {t('CHANNEL_MGMT.MODAL_CHANNEL.STATUS', '启用状态')}
                                </label>
                                <select
                                    id="channel-form-status"
                                    value={chanForm.status}
                                    onChange={e=>setChanForm({...chanForm, status: parseInt(e.target.value, 10) || 1})}
                                    className="w-full bg-surface-container-high border border-outline-variant rounded-overlay px-4 py-2.5 text-on-surface cursor-pointer hover:border-primary/50 outline-none"
                                >
                                    <option value={1}>{t('CHANNEL_MGMT.CHANNEL_TABLE.STATUS_ENABLED', '启用')}</option>
                                    <option value={2}>{t('CHANNEL_MGMT.CHANNEL_TABLE.STATUS_DISABLED', '禁用')}</option>
                                </select>
                                <p className="text-[11px] text-on-surface-variant mt-1">
                                    {t('CHANNEL_MGMT.MODAL_CHANNEL.STATUS_HINT', '禁用后此渠道下所有模型对外不可见，但模型本体的启用/禁用配置保留。')}
                                </p>
                            </div>
                        </div>
                        <div className="p-6 border-t border-outline-variant bg-surface-container-high flex justify-end gap-3 rounded-control-b-2xl">
                            <button onClick={() => setIsChanModalOpen(false)} className="px-5 py-2.5 text-on-surface-variant hover:text-white hover:bg-surface-container-high rounded-overlay">{t('CHANNEL_MGMT.MODAL_CHANNEL.BTN_CANCEL')}</button>
                            <button onClick={handleChanSubmit} disabled={isSubmitting} className="px-6 py-2.5 bg-primary hover:opacity-90 text-on-primary rounded-overlay font-medium flex items-center gap-2">
                                {isSubmitting ? <RefreshCw className="animate-spin" size={18}/> : <Save size={18}/>} {t('CHANNEL_MGMT.MODAL_CHANNEL.BTN_SAVE')}
                            </button>
                        </div>
                    </div>
                </div>
            )}
        </div>
    );
};

const ProviderIcon = ({ provider, compact = false }) => {
    const Icon = provider.icon;
    const size = compact ? 18 : 28;
    const iconSize = compact ? 11 : 15;
    return (
        <span
            className="rounded-control flex items-center justify-center border shrink-0"
            style={{
                width: size,
                height: size,
                background: hexA(provider.hue, compact ? 0.1 : 0.14),
                borderColor: hexA(provider.hue, compact ? 0.18 : 0.24),
            }}
        >
            <Icon size={iconSize} style={{ color: provider.hue }} />
        </span>
    );
};

const ModerationStatCard = ({ icon: Icon, label, value, tone = 'neutral' }) => {
    const toneClass = {
        success: 'text-success bg-success/10 border-success/30',
        warning: 'text-warning bg-warning/10 border-warning/30',
        primary: 'text-primary bg-primary/10 border-primary/30',
        neutral: 'text-on-surface-variant bg-surface-container-high border-outline-variant/60',
    }[tone] || 'text-on-surface-variant bg-surface-container-high border-outline-variant/60';
    return (
        <div className="rounded-control border border-outline-variant bg-surface-container p-3 flex items-center gap-3 min-w-0">
            <span className={`w-8 h-8 rounded-control border flex items-center justify-center shrink-0 ${toneClass}`}>
                <Icon size={16} />
            </span>
            <div className="min-w-0">
                <div className="text-[11px] text-on-surface-variant truncate">{label}</div>
                <div className="text-lg font-semibold text-on-surface tabular-nums">{value}</div>
            </div>
        </div>
    );
};

export default ChannelManagement;
