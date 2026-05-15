import React, { useState, useEffect, useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { Ban, RotateCw, Filter, Hash, Clock, ShieldAlert, Gauge, Server, UserX } from 'lucide-react';
import toast from 'react-hot-toast';
import { authFetch } from '../../utils/authFetch';
import TextInput from '../ui/TextInput';
import Switch from '../ui/Switch';
import Select from '../ui/Select';
import FormRow from '../ui/FormRow';

const riskActionOptions = [
    ['ALL', '全部事件'],
    ['MODERATION_BLOCK_POLICY', '智能审核拦截'],
    ['MODERATION_BLOCK_KEYWORD', '关键字拦截'],
    ['MODERATION_BLOCK_RISK_RULE', '规则直接拦截'],
    ['MODERATION_RISK_SCORE', '风险打分'],
    ['MODERATION_BLOCK_OVERSIZE', '超长拦截'],
    ['MODERATION_BLOCK_IMAGE_POLICY', '图片策略拦截'],
    ['MODERATION_UNAVAILABLE_CLOSED', '审核不可达拦截'],
    ['MODERATION_FAIL_OPEN', '审核失败放行'],
    ['SECURITY_AUTOBAN', '自动封禁'],
];

const actionLabels = Object.fromEntries(riskActionOptions.filter(([value]) => value !== 'ALL'));

const actionTone = (action) => {
    if (action === 'SECURITY_AUTOBAN') return 'border-error/30 bg-error/10 text-error';
    if (String(action || '').includes('BLOCK')) return 'border-warning/30 bg-warning/10 text-warning';
    if (action === 'MODERATION_UNAVAILABLE_CLOSED') return 'border-error/25 bg-error/10 text-error';
    if (action === 'MODERATION_FAIL_OPEN') return 'border-primary/30 bg-primary/10 text-primary';
    return 'border-primary/30 bg-primary/10 text-primary';
};

const actionIcon = (action) => {
    if (action === 'SECURITY_AUTOBAN') return UserX;
    if (action === 'MODERATION_RISK_SCORE') return Gauge;
    if (action === 'MODERATION_UNAVAILABLE_CLOSED' || action === 'MODERATION_FAIL_OPEN') return Server;
    return ShieldAlert;
};

const segmentScopeLabels = {
    user_message: '用户输入',
    non_user_context: '非用户上下文',
    tool_context: '工具上下文',
    client_context: '客户端上下文',
    system_instruction: '系统指令',
    tool_result: '工具结果',
    function_output: '函数结果',
    unknown: '未知来源',
};

const getSegmentScope = (data = {}) => {
    const scope = String(data.segment_scope || data.source || '').trim();
    if (scope) return scope;
    if (data.mode === 'context_score') return 'non_user_context';
    return '';
};

const formatSegmentScope = (scope) => segmentScopeLabels[scope] || scope;

const segmentScopeTone = (scope) => {
    if (scope === 'user_message') return 'border-error/25 bg-error/10 text-error';
    if (scope === 'non_user_context' || scope === 'tool_context' || scope === 'client_context' || scope === 'tool_result' || scope === 'function_output') {
        return 'border-success/25 bg-success/10 text-success';
    }
    return 'border-outline-variant bg-surface-container-high text-on-surface-variant';
};

const parseRiskDetails = (details) => {
    const raw = String(details || '').trim();
    if (!raw) return { raw: '', data: null, formatted: '' };
    try {
        const data = JSON.parse(raw);
        return { raw, data, formatted: JSON.stringify(data, null, 2) };
    } catch {
        const oversize = raw.match(/len=(\d+)\s+max=(\d+)/i);
        if (oversize) {
            const data = { len: Number(oversize[1]), max: Number(oversize[2]) };
            return { raw, data, formatted: JSON.stringify(data, null, 2) };
        }
        return { raw, data: null, formatted: raw };
    }
};

const formatRiskScore = (value) => {
    const num = Number(value);
    if (!Number.isFinite(num)) return value;
    if (num > 0 && num <= 1) return `${Math.round(num * 100)}%`;
    return String(num);
};

const formatRiskTime = (value) => {
    if (!value) return '-';
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return value;
    return date.toLocaleString();
};

const riskBadgesForEvent = (evt) => {
    const parsed = parseRiskDetails(evt.details);
    const data = parsed.data || {};
    const firstMatch = Array.isArray(data.matches) && data.matches.length > 0 ? data.matches[0] : null;
    const badges = [];

    const push = (label, value, tone = 'border-outline-variant bg-surface-container-high text-on-surface-variant') => {
        if (value === undefined || value === null || value === '') return;
        badges.push({ label, value: String(value), tone });
    };

    push('模型', data.model || data.trigger_model, 'border-primary/25 bg-primary/10 text-primary');
    const segmentScope = getSegmentScope(data);
    push('来源', formatSegmentScope(segmentScope), segmentScopeTone(segmentScope));
    push('分类', data.highest_cat || firstMatch?.category, 'border-warning/25 bg-warning/10 text-warning');
    push('分数', formatRiskScore(data.highest_score ?? data.total_score ?? firstMatch?.score), 'border-primary/25 bg-primary/10 text-primary');
    push('规则', firstMatch?.id || data.trigger_keyword || data.auto_ban_group);
    push('缓存', typeof data.from_cache === 'boolean' ? (data.from_cache ? '是' : '否') : undefined);
    push('错误', data.err_tag || data.upstream_error_type, 'border-error/25 bg-error/10 text-error');
    push('命中', data.hit_count && data.threshold ? `${data.hit_count}/${data.threshold}` : undefined, 'border-error/25 bg-error/10 text-error');
    push('长度', data.len && data.max ? `${data.len}/${data.max}` : undefined, 'border-warning/25 bg-warning/10 text-warning');
    push('内容', data.content_runes ? `${Number(data.content_runes).toLocaleString()} 字符` : undefined, 'border-success/25 bg-success/10 text-success');
    push('预览', data.content_truncated ? '已截断' : undefined, 'border-success/25 bg-success/10 text-success');
    push('脱敏', data.content_redacted ? '是' : undefined, 'border-success/25 bg-success/10 text-success');
    push('原因', data.trigger_reason || data.reason);

    return { parsed, badges, segmentScope };
};

const DryRunPanel = ({ configs, handleChange }) => {
    const { t } = useTranslation();
    const [riskEvents, setRiskEvents] = useState([]);
    const [riskEventsLoading, setRiskEventsLoading] = useState(false);
    const [riskEventAction, setRiskEventAction] = useState('ALL');
    const [riskEventUserID, setRiskEventUserID] = useState('');
    const [riskEventLimit, setRiskEventLimit] = useState('80');
    const [riskEventsLoadedAt, setRiskEventsLoadedAt] = useState(null);

    const loadRiskEvents = async () => {
        setRiskEventsLoading(true);
        try {
            const params = new URLSearchParams();
            params.set('limit', String(parseInt(riskEventLimit || '80', 10) || 80));
            if (riskEventAction && riskEventAction !== 'ALL') {
                params.set('action', riskEventAction);
            }
            if (String(riskEventUserID || '').trim()) {
                params.set('user_id', String(riskEventUserID).trim());
            }
            const result = await authFetch(`/api/admin/moderation/events?${params.toString()}`);
            if (result?.success) {
                setRiskEvents(Array.isArray(result.data) ? result.data : []);
                setRiskEventsLoadedAt(new Date());
            } else {
                toast.error(result?.message || t('MODERATION.RISK_EVENTS_LOAD_FAIL', '加载风控记录失败'));
            }
        } finally {
            setRiskEventsLoading(false);
        }
    };

    useEffect(() => {
        loadRiskEvents();
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, []);

    const riskSummary = useMemo(() => {
        const totals = {
            shown: riskEvents.length,
            blocked: 0,
            review: 0,
            unavailable: 0,
            autoban: 0,
            userMessage: 0,
            nonUserContext: 0,
        };
        riskEvents.forEach(evt => {
            const scope = getSegmentScope(parseRiskDetails(evt.details).data || {});
            if (scope === 'user_message') totals.userMessage += 1;
            if (scope && scope !== 'user_message') totals.nonUserContext += 1;
            if (evt.action_type === 'SECURITY_AUTOBAN') totals.autoban += 1;
            if (String(evt.action_type || '').includes('BLOCK')) totals.blocked += 1;
            if (evt.action_type === 'MODERATION_RISK_SCORE') totals.review += 1;
            if (evt.action_type === 'MODERATION_UNAVAILABLE_CLOSED' || evt.action_type === 'MODERATION_FAIL_OPEN') totals.unavailable += 1;
        });
        return totals;
    }, [riskEvents]);

    return (
        <FormRow.Group
            title={
                <div className="flex items-center justify-between w-full">
                    <span className="flex items-center gap-2 text-error">
                        <Ban size={16} />
                        {t('MODERATION.SECTION_AUTOBAN', '自动处置与风控记录')}
                    </span>
                    <button
                        type="button"
                        onClick={loadRiskEvents}
                        disabled={riskEventsLoading}
                        className="inline-flex h-8 items-center justify-center gap-1.5 rounded-control border border-outline bg-surface-container-high px-3 text-xs font-semibold text-on-surface hover:bg-surface-container-highest disabled:opacity-60 transition-colors"
                    >
                        <RotateCw size={14} className={riskEventsLoading ? 'animate-spin' : ''} />
                        {t('SYSTEM.REFRESH', '刷新')}
                    </button>
                </div>
            }
            sub={t('MODERATION.AUTOBAN_DESC', '审核命中会写入风控记录；开启自动封禁后，达到阈值的普通用户会立即封禁并刷新 token 缓存。管理员账号不会被自动封禁。')}
            className="mb-6"
        >
            <div className="flex flex-col gap-6">
                <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4 gap-4">
                    <div className="flex items-center justify-between gap-3 rounded-control border border-outline bg-surface px-4 py-3 md:col-span-2 lg:col-span-1">
                        <div>
                            <span className="block text-sm font-semibold text-on-surface">{t('MODERATION.AUTOBAN_ENABLED', '自动封禁')}</span>
                            <span className="mt-1 block text-xs text-on-surface-variant leading-tight">{t('MODERATION.AUTOBAN_ENABLED_HINT', '直接限制账户')}</span>
                        </div>
                        <Switch
                            checked={String(configs.moderation_autoban_enabled || 'false').toLowerCase() === 'true'}
                            onChange={e => handleChange('moderation_autoban_enabled', e.target.checked ? 'true' : 'false')}
                        />
                    </div>
                    
                    {[
                        ['moderation_autoban_keyword_threshold', t('MODERATION.AUTOBAN_KEYWORD', '关键字阈值'), '1'],
                        ['moderation_autoban_policy_threshold', t('MODERATION.AUTOBAN_POLICY', '智能审核阈值'), '0'],
                        ['moderation_autoban_risk_rule_threshold', t('MODERATION.AUTOBAN_RISK_RULE', '风险规则阈值'), '1'],
                        ['moderation_autoban_risk_score_threshold', t('MODERATION.AUTOBAN_RISK_SCORE', '风险打分阈值'), '0'],
                        ['moderation_autoban_image_threshold', t('MODERATION.AUTOBAN_IMAGE', '图片策略阈值'), '2'],
                        ['moderation_autoban_oversize_threshold', t('MODERATION.AUTOBAN_OVERSIZE', '超长阈值'), '0'],
                    ].map(([key, label, fallback]) => (
                        <div key={key} className="bg-surface-container rounded-control border border-outline/50 p-3">
                            <label className="mb-2 block text-xs font-semibold text-on-surface-variant uppercase tracking-wider">{label}</label>
                            <TextInput
                                type="number"
                                min="0"
                                max="100"
                                value={configs[key] || fallback}
                                onChange={e => handleChange(key, e.target.value)}
                                className="w-full bg-surface"
                            />
                        </div>
                    ))}
                    
                    <div className="bg-surface-container rounded-control border border-outline/50 p-3 md:col-span-2 lg:col-span-1">
                        <label className="mb-2 block text-xs font-semibold text-on-surface-variant uppercase tracking-wider">
                            {t('MODERATION.AUTOBAN_WINDOW', '统计窗口（秒）')}
                        </label>
                        <TextInput
                            type="number"
                            min="60"
                            value={configs.moderation_autoban_window_seconds || '86400'}
                            onChange={e => handleChange('moderation_autoban_window_seconds', e.target.value)}
                            className="w-full bg-surface"
                        />
                    </div>
                </div>

                <div className="rounded-overlay border border-outline-variant bg-surface/50 mt-2 overflow-hidden">
                    <div className="grid grid-cols-2 md:grid-cols-4 lg:grid-cols-7 divide-x divide-y md:divide-y-0 divide-outline-variant bg-surface-container-high border-b border-outline-variant">
                        {[
                            [t('MODERATION.RISK_SUMMARY_SHOWN', '当前显示'), riskSummary.shown],
                            [t('MODERATION.RISK_SUMMARY_BLOCKED', '请求拦截'), riskSummary.blocked],
                            [t('MODERATION.RISK_SUMMARY_REVIEW', '规则二审/打分'), riskSummary.review],
                            [t('MODERATION.RISK_SUMMARY_UNAVAILABLE', '审核不可达'), riskSummary.unavailable],
                            [t('MODERATION.RISK_SUMMARY_AUTOBAN', '自动封禁'), riskSummary.autoban],
                            [t('MODERATION.RISK_SUMMARY_USER_SCOPE', '用户输入'), riskSummary.userMessage],
                            [t('MODERATION.RISK_SUMMARY_CONTEXT_SCOPE', '上下文噪声'), riskSummary.nonUserContext],
                        ].map(([label, value]) => (
                            <div key={label} className="p-3 lg:px-4 lg:py-3 flex flex-col items-center justify-center text-center">
                                <div className="text-[10px] font-bold uppercase tracking-widest text-on-surface-variant/80 mb-1">{label}</div>
                                <div className="font-mono text-xl font-bold text-on-surface">{value}</div>
                            </div>
                        ))}
                    </div>

                    <div className="grid grid-cols-1 md:grid-cols-[1.5fr,1fr,1fr,auto] gap-4 p-4 border-b border-outline-variant bg-surface md:items-end">
                        <label className="block">
                            <span className="mb-2 flex items-center gap-1.5 text-xs font-medium text-on-surface-variant">
                                <Filter size={14} />
                                {t('MODERATION.RISK_FILTER_ACTION', '事件类型')}
                            </span>
                            <Select
                                value={riskEventAction}
                                onChange={e => setRiskEventAction(e.target.value)}
                                className="w-full h-10"
                                options={riskActionOptions.map(([value, label]) => ({value, label}))} 
                            />
                        </label>
                        <label className="block">
                            <span className="mb-2 flex items-center gap-1.5 text-xs font-medium text-on-surface-variant">
                                <Hash size={14} />
                                {t('MODERATION.RISK_FILTER_USER', '用户 ID')}
                            </span>
                            <TextInput
                                type="number"
                                min="1"
                                value={riskEventUserID}
                                onChange={e => setRiskEventUserID(e.target.value)}
                                placeholder={t('MODERATION.RISK_FILTER_USER_PLACEHOLDER', '全部用户')}
                                className="w-full h-10"
                            />
                        </label>
                        <label className="block">
                            <span className="mb-2 block text-xs font-medium text-on-surface-variant">
                                {t('MODERATION.RISK_FILTER_LIMIT', '数量')}
                            </span>
                            <TextInput
                                type="number"
                                min="1"
                                max="200"
                                value={riskEventLimit}
                                onChange={e => setRiskEventLimit(e.target.value)}
                                className="w-full h-10"
                            />
                        </label>
                        <button
                            type="button"
                            onClick={loadRiskEvents}
                            disabled={riskEventsLoading}
                            className="inline-flex h-10 w-full md:w-auto items-center justify-center gap-2 rounded-control bg-primary px-5 text-sm font-semibold text-on-primary hover:bg-primary/90 transition-colors disabled:opacity-60"
                        >
                            <RotateCw size={16} className={riskEventsLoading ? 'animate-spin' : ''} />
                            {t('MODERATION.RISK_APPLY_FILTERS', '应用筛选')}
                        </button>
                    </div>

                    <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-2 px-4 py-3 text-xs text-on-surface-variant bg-surface-container-high/50 border-b border-outline-variant">
                        <span>{t('MODERATION.RISK_AUDIT_NOTE', '新拦截会显示脱敏内容预览和指纹；历史旧记录没有保存请求内容。')}</span>
                        <span className="font-medium whitespace-nowrap">
                            {riskEventsLoadedAt
                                ? t('MODERATION.RISK_LOADED_AT', '加载于 {{time}}', { time: riskEventsLoadedAt.toLocaleTimeString() })
                                : t('MODERATION.RISK_NOT_LOADED', '尚未加载')}
                        </span>
                    </div>

                    <div className="max-h-[36rem] overflow-y-auto divide-y divide-outline-variant/50 bg-surface">
                        {riskEventsLoading && (
                            <div className="flex items-center justify-center gap-3 px-4 py-8 text-sm font-medium text-on-surface-variant">
                                <RotateCw size={18} className="animate-spin text-primary" />
                                {t('COMMON.LOADING', '加载中...')}
                            </div>
                        )}
                        {!riskEventsLoading && riskEvents.length === 0 && (
                            <div className="flex flex-col items-center justify-center px-4 py-12 text-on-surface-variant">
                                <ShieldAlert size={32} className="mb-3 opacity-20" />
                                <span className="text-sm font-medium">{t('MODERATION.RISK_EMPTY', '暂无风控记录')}</span>
                            </div>
                        )}
                        {!riskEventsLoading && riskEvents.map((evt) => {
                            const Icon = actionIcon(evt.action_type);
                            const { parsed, badges, segmentScope } = riskBadgesForEvent(evt);
                            return (
                                <div key={evt.id} className="px-4 py-4 text-sm hover:bg-surface-container/30 transition-colors">
                                    <div className="grid grid-cols-1 gap-4 lg:grid-cols-[200px,160px,1fr]">
                                        <div className="min-w-0">
                                            <div className={`inline-flex max-w-full items-center gap-2 rounded-full border px-3 py-1.5 ${actionTone(evt.action_type)}`}>
                                                <Icon size={16} className="shrink-0" />
                                                <span className="truncate font-bold text-xs tracking-wide">
                                                    {actionLabels[evt.action_type] || evt.action_type}
                                                </span>
                                            </div>
                                            <div className="mt-2.5 font-mono text-xs text-on-surface-variant/80 truncate">
                                                {evt.action_type}
                                            </div>
                                            {segmentScope && (
                                                <div className={`mt-2.5 inline-flex max-w-full items-center gap-1.5 rounded-full border px-2.5 py-1 text-xs font-medium ${segmentScopeTone(segmentScope)}`}>
                                                    <span className="shrink-0 text-on-surface-variant opacity-80">{t('MODERATION.RISK_SEGMENT_SCOPE', '来源')}</span>
                                                    <span className="truncate">{formatSegmentScope(segmentScope)}</span>
                                                </div>
                                            )}
                                        </div>

                                        <div className="min-w-0 space-y-2.5 text-on-surface-variant text-xs">
                                            <div className="flex min-w-0 items-center gap-2.5 font-medium">
                                                <Hash size={14} className="shrink-0" />
                                                <span className="truncate">
                                                    #{evt.target_user_id} {evt.username || '-'}
                                                </span>
                                            </div>
                                            <div className="flex min-w-0 items-center gap-2.5">
                                                <Clock size={14} className="shrink-0" />
                                                <span className="truncate">{formatRiskTime(evt.created_at)}</span>
                                            </div>
                                            {evt.ip_address && (
                                                <div className="truncate font-mono text-[11px] bg-surface-container-high px-2 py-1 rounded inline-block">
                                                    IP: {evt.ip_address}
                                                </div>
                                            )}
                                        </div>

                                        <div className="min-w-0">
                                            <div className="flex flex-wrap gap-2">
                                                {badges.length > 0 ? badges.map((badge, idx) => (
                                                    <span key={`${badge.label}-${idx}`} className={`inline-flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-xs font-medium ${badge.tone}`}>
                                                        <span className="text-on-surface-variant/80">{badge.label}</span>
                                                        <span className="font-mono">{badge.value}</span>
                                                    </span>
                                                )) : (
                                                    <span className="text-xs text-on-surface-variant italic">{t('MODERATION.RISK_NO_STRUCTURED_DETAILS', '无结构化详情')}</span>
                                                )}
                                            </div>
                                            
                                            {parsed.data?.content_preview && (
                                                <div className="mt-3 rounded-control border border-success/20 bg-success/5 overflow-hidden">
                                                    <div className="flex flex-wrap items-center justify-between gap-2 border-b border-success/10 bg-success/10 px-3 py-2">
                                                        <span className="text-xs font-bold text-success-dark">
                                                            {t('MODERATION.RISK_CONTENT_PREVIEW', '命中内容预览（已脱敏）')}
                                                        </span>
                                                        {parsed.data.content_sha256 && (
                                                            <span className="max-w-full truncate font-mono text-[10px] text-success-dark/70">
                                                                sha256: {parsed.data.content_sha256}
                                                            </span>
                                                        )}
                                                    </div>
                                                    <pre className="max-h-48 overflow-y-auto whitespace-pre-wrap break-words p-3 font-mono text-xs leading-relaxed text-on-surface">
                                                        {parsed.data.content_preview}
                                                    </pre>
                                                </div>
                                            )}
                                            
                                            {parsed.formatted && (
                                                <details className="mt-3 rounded-control border border-outline-variant bg-surface-container">
                                                    <summary className="cursor-pointer px-3 py-2 text-xs font-medium text-on-surface-variant hover:text-on-surface transition-colors">
                                                        {t('MODERATION.RISK_RAW_DETAILS', '查看原始详情')}
                                                    </summary>
                                                    <pre className="max-h-56 overflow-y-auto whitespace-pre-wrap break-words border-t border-outline-variant p-3 font-mono text-[11px] leading-relaxed text-on-surface-variant bg-surface">
                                                        {parsed.formatted}
                                                    </pre>
                                                </details>
                                            )}
                                        </div>
                                    </div>
                                </div>
                            );
                        })}
                    </div>
                </div>
            </div>
        </FormRow.Group>
    );
};

export default DryRunPanel;