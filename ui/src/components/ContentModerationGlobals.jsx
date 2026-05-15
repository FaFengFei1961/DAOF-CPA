import React, { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Shield, AlertTriangle, RotateCw, Eye, EyeOff, PlugZap, CheckCircle2, XCircle, AlertCircle, Bot, ListChecks, Ban, Clock, UserX, Filter, Gauge, Hash, Server, ShieldAlert } from 'lucide-react';
import toast from 'react-hot-toast';
import { useConfirm } from '../context/ConfirmContext';
import { authFetch } from '../utils/authFetch';

/**
 * 内容审核全局配置（per-ChannelModel 风控的全局共享层）
 *
 * fix CRITICAL R23 (codex 第二十三轮反馈)：admin 在这里配置的是"全平台共享"参数，
 * 真实的"哪个渠道哪个模型走哪种风控"在 ChannelManagement → 模型编辑里设置。
 *
 * 包括：
 *   - 上游模型池审核配置（model / threshold）
 *   - 关键字词库（textarea，前端 split('\n') ↔ JSON 数组）
 *   - 缓存参数（TTL / 容量 / HMAC secret 重置）
 *   - 长 prompt 限制（max_chars / chunk_chars / max_chunks）
 *   - 多模态图片策略（skip / submit / reject）
 *   - 双语拒绝文案（zh / en）
 *
 * @param {{
 *   configs: Record<string, string>,
 *   handleChange: (key: string, val: string) => void,
 * }} props
 *
 * fix MAJOR R23-M11（gemini 审查）：移除内部 Save 按钮 —— Settings.jsx 在外面
 * 统一调 <SaveBar>（与 oauth/sms/risk/finance tab 保持一致），避免按钮风格撕裂。
 */
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

const formatRiskTime = (value) => {
    if (!value) return '-';
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return value;
    return date.toLocaleString();
};

const ContentModerationGlobals = ({ configs, handleChange }) => {
    const { t } = useTranslation();
    const confirm = useConfirm();
    const [showSecret, setShowSecret] = useState(false);
    const [testing, setTesting] = useState(false);
    const [testResult, setTestResult] = useState(null);
    const [keywordAIFocus, setKeywordAIFocus] = useState('');
    const [keywordCandidates, setKeywordCandidates] = useState([]);
    const [selectedCandidates, setSelectedCandidates] = useState(new Set());
    const [generatingKeywords, setGeneratingKeywords] = useState(false);
    const [riskEvents, setRiskEvents] = useState([]);
    const [riskEventsLoading, setRiskEventsLoading] = useState(false);
    const [riskEventAction, setRiskEventAction] = useState('ALL');
    const [riskEventUserID, setRiskEventUserID] = useState('');
    const [riskEventLimit, setRiskEventLimit] = useState('80');
    const [riskEventsLoadedAt, setRiskEventsLoadedAt] = useState(null);

    // 关键字词库 textarea ↔ JSON 数组 互转：
    //   - configs.moderation_keywords 来自后端，是 JSON 数组字符串
    //   - 用户在 textarea 里 line-by-line 编辑
    //   - onBlur 时序列化回 JSON 数组（去空白行 + 去重）
    const keywordList = useMemo(() => {
        try {
            const raw = configs.moderation_keywords || '[]';
            const arr = JSON.parse(raw);
            if (Array.isArray(arr)) return arr;
        } catch { /* fallthrough */ }
        return [];
    }, [configs.moderation_keywords]);

    const [keywordText, setKeywordText] = useState(keywordList.join('\n'));
    const formatRiskRules = (raw) => {
        try {
            return JSON.stringify(JSON.parse(raw || '[]'), null, 2);
        } catch {
            return raw || '[]';
        }
    };
    const riskRuleCount = useMemo(() => {
        try {
            const arr = JSON.parse(configs.moderation_risk_rules || '[]');
            return Array.isArray(arr) ? arr.length : 0;
        } catch {
            return 0;
        }
    }, [configs.moderation_risk_rules]);
    const [riskRulesText, setRiskRulesText] = useState(formatRiskRules(configs.moderation_risk_rules));

    // 当 configs 从后端刷新时同步 textarea
    React.useEffect(() => {
        setKeywordText(keywordList.join('\n'));
    }, [keywordList]);
    React.useEffect(() => {
        setRiskRulesText(formatRiskRules(configs.moderation_risk_rules));
    }, [configs.moderation_risk_rules]);

    const flushKeywords = () => {
        const cleaned = Array.from(new Set(
            keywordText.split('\n').map(s => s.trim()).filter(Boolean)
        ));
        handleChange('moderation_keywords', JSON.stringify(cleaned));
    };

    const flushRiskRules = () => {
        try {
            const parsed = JSON.parse(riskRulesText || '[]');
            if (!Array.isArray(parsed)) {
                throw new Error('risk rules must be a JSON array');
            }
            const compact = JSON.stringify(parsed);
            handleChange('moderation_risk_rules', compact);
            setRiskRulesText(JSON.stringify(parsed, null, 2));
        } catch {
            toast.error(t('MODERATION.RISK_RULES_INVALID', '风险规则必须是合法 JSON 数组'));
        }
    };

    // HMAC secret 重置：写入空字符串触发后端在下次启动时重新生成
    // fix MINOR R23-m3（gemini 审查）：用统一 useConfirm 替代 window.confirm，避免阻塞主线程 + UI 风格突变
    const resetHmacSecret = async () => {
        const ok = await confirm(t('MODERATION.SECRET_RESET_CONFIRM', '重置 HMAC 密钥会让全部审核缓存立即失效，确认继续？'));
        if (!ok) return;
        handleChange('moderation_cache_secret', '');
        toast.success(t('MODERATION.SECRET_RESET_DONE', '已清空，请点击「保存」让后端在下次启动时重新生成 256bit 密钥'));
    };

    // 阈值滑杆 0.0–1.0 步进 0.05；显式给出 ARIA 属性满足 WCAG 2.2
    const threshold = parseFloat(configs.moderation_threshold || '0.8');
    const moderationModelKey = 'moderation_cliproxy_model';
    const moderationModelFallback = 'gpt-5.4-mini';

    React.useEffect(() => {
        if ((configs.moderation_provider || 'cliproxy_model') !== 'cliproxy_model') {
            handleChange('moderation_provider', 'cliproxy_model');
        }
    }, [configs.moderation_provider, handleChange]);

    const getTestMessage = (result) => {
        if (!result) return '';
        const messages = {
            ok: t('MODERATION.TEST_OK', '已连通：测试文本通过审核'),
            flagged: t('MODERATION.TEST_FLAGGED', '已连通，但无害测试文本被判定命中，请检查兼容服务或阈值'),
            not_configured: t('MODERATION.TEST_NOT_CONFIGURED', '请先保存上游地址和审核模型后再测试'),
            config_invalid: t('MODERATION.TEST_CONFIG_INVALID', 'Endpoint 不合法，请检查后保存'),
            auth_failed: t('MODERATION.TEST_AUTH_FAILED', '审核 provider 鉴权失败，请检查同地址 cliproxy 渠道 API key 或模型权限'),
            rate_limited: t('MODERATION.TEST_RATE_LIMITED', '审核 provider 返回限流，请稍后重试，或切换更充足的模型'),
            billing_or_quota: t('MODERATION.TEST_BILLING', '审核 provider quota 或计费异常，请检查该模型的可用额度'),
            timeout: t('MODERATION.TEST_TIMEOUT', '审核请求超时，请检查网络、代理或 endpoint 可达性'),
            network_error: t('MODERATION.TEST_NETWORK', '无法连接审核 provider，请检查上游、网络或 DNS'),
            api_5xx: t('MODERATION.TEST_5XX', '审核 provider 上游暂时异常，请稍后重试'),
            input_too_long: t('MODERATION.TEST_INPUT_TOO_LONG', '测试文本被当前长度限制拒绝，请检查长 Prompt 限制配置'),
            api_error: t('MODERATION.TEST_API_ERROR', '审核调用失败，请检查上游地址、模型名和调用权限'),
        };
        return messages[result.status] || result.message || t('MODERATION.TEST_UNKNOWN', '测试失败，请检查配置');
    };

    const runModerationTest = async () => {
        setTesting(true);
        setTestResult(null);
        try {
            const result = await authFetch('/api/admin/moderation/test', { method: 'POST' });
            setTestResult(result);
            const message = getTestMessage(result);
            if (result?.success && result?.status === 'ok') {
                toast.success(message);
            } else {
                toast.error(message);
            }
        } finally {
            setTesting(false);
        }
    };

    const generateKeywordCandidates = async () => {
        setGeneratingKeywords(true);
        setKeywordCandidates([]);
        setSelectedCandidates(new Set());
        try {
            const result = await authFetch('/api/admin/moderation/keywords/generate', {
                method: 'POST',
                body: {
                    focus: keywordAIFocus,
                    max_candidates: parseInt(configs.moderation_keyword_ai_max_candidates || '80', 10) || 80,
                },
            });
            if (!result?.success) {
                toast.error(result?.message || t('MODERATION.KEYWORD_AI_FAIL', 'AI 词库候选生成失败'));
                return;
            }
            const rows = Array.isArray(result.data) ? result.data : [];
            setKeywordCandidates(rows);
            setSelectedCandidates(new Set(rows.map((_, idx) => idx)));
            toast.success(t('MODERATION.KEYWORD_AI_DONE', '已生成 {{count}} 条候选', { count: rows.length }));
        } finally {
            setGeneratingKeywords(false);
        }
    };

    const toggleCandidate = (idx) => {
        setSelectedCandidates(prev => {
            const next = new Set(prev);
            if (next.has(idx)) next.delete(idx);
            else next.add(idx);
            return next;
        });
    };

    const mergeSelectedCandidates = () => {
        const selected = keywordCandidates
            .filter((_, idx) => selectedCandidates.has(idx))
            .map(c => (c.keyword || '').trim())
            .filter(Boolean);
        if (selected.length === 0) {
            toast.error(t('MODERATION.KEYWORD_AI_NONE_SELECTED', '请先选择至少一条候选'));
            return;
        }
        const merged = Array.from(new Set([
            ...keywordText.split('\n').map(s => s.trim()).filter(Boolean),
            ...selected,
        ]));
        const text = merged.join('\n');
        setKeywordText(text);
        handleChange('moderation_keywords', JSON.stringify(merged));
        toast.success(t('MODERATION.KEYWORD_AI_MERGED', '已合并到词库，记得点击底部保存'));
    };

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

    React.useEffect(() => {
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

    const testTone = testResult?.success && testResult?.status === 'ok'
        ? 'border-success/30 bg-success/10 text-success'
        : testResult?.success
            ? 'border-warning/30 bg-warning/10 text-warning'
            : 'border-error/30 bg-error/10 text-error';
    const TestIcon = testResult?.success && testResult?.status === 'ok'
        ? CheckCircle2
        : testResult?.success
            ? AlertCircle
            : XCircle;
    const rateLimit = testResult?.rate_limit || {};

    return (
        <div className="w-full">
            <div className="mb-8 border-b border-outline-variant pb-6">
                <h1 className="text-xl md:text-2xl font-bold tracking-tight text-on-surface flex items-center gap-3">
                    <Shield size={22} className="text-primary" />
                    {t('MODERATION.TITLE', '内容审核（全局配置）')}
                </h1>
                <p className="text-on-surface-variant mt-2 text-sm max-w-3xl">
                    {t('MODERATION.DESC', '这里配置的是"全平台共享"的审核参数。具体每条渠道每个模型走哪种风控策略请到「渠道与模型」→ 模型编辑里设置。')}
                </p>
            </div>

            {/* ── Smart moderation provider ─────────────────────────────────── */}
            <section className="bg-surface-container border border-outline-variant rounded-overlay p-4 md:p-6 mb-6 ">
                <div className="mb-4 pb-3 border-b border-outline-variant/50 flex flex-col gap-3 lg:flex-row lg:items-start lg:justify-between">
                    <div>
                        <h3 className="text-sm font-semibold text-on-surface flex items-center gap-2">
                            <Shield size={16} className="text-primary" />
                            {t('MODERATION.SECTION_API', '智能审核 Provider')}
                        </h3>
                        <p className="text-[11px] text-on-surface-variant mt-1">
                            {t('MODERATION.TEST_SAVED_HINT', '审核统一走上游模型池。刚改过配置请先点击页面底部「保存」。')}
                        </p>
                    </div>
                    <button
                        type="button"
                        onClick={runModerationTest}
                        disabled={testing}
                        className="inline-flex items-center justify-center gap-2 rounded-control border border-primary/40 bg-primary/10 px-3 py-2 text-xs font-semibold text-primary hover:bg-primary/15 disabled:cursor-not-allowed disabled:opacity-60"
                    >
                        {testing ? <RotateCw size={15} className="animate-spin" /> : <PlugZap size={15} />}
                        {testing ? t('MODERATION.TESTING', '测试中...') : t('MODERATION.TEST_BUTTON', '测试已保存配置')}
                    </button>
                </div>

                {testResult && (
                    <div className={`mb-4 rounded-overlay border px-3 py-3 ${testTone}`}>
                        <div className="flex items-start gap-2">
                            <TestIcon size={17} className="mt-0.5 shrink-0" />
                            <div className="min-w-0">
                                <div className="text-sm font-semibold break-words">{getTestMessage(testResult)}</div>
                                <div className="mt-2 grid grid-cols-1 gap-1 text-[11px] text-on-surface-variant md:grid-cols-2 xl:grid-cols-4">
                                    <span className="min-w-0 break-words">
                                        {t('MODERATION.TEST_PROVIDER', '供应商')}：{testResult.provider || 'cliproxy_model'}
                                    </span>
                                    <span className="min-w-0 break-words">
                                        {t('MODERATION.TEST_MODEL', '模型')}：{testResult.model || '-'}
                                    </span>
                                    <span className="min-w-0 break-words">
                                        {t('MODERATION.TEST_ROUTE', '路由')}：{t('MODERATION.PROVIDER_CLIPROXY_MODEL', '上游模型池')}
                                    </span>
                                    <span className="min-w-0 break-words">
                                        {t('MODERATION.TEST_ENDPOINT', 'Endpoint')}：{testResult.endpoint || '-'}
                                    </span>
                                    <span>
                                        {t('MODERATION.TEST_LATENCY', '延迟')}：{Number.isFinite(testResult.latency_ms) ? `${testResult.latency_ms} ms` : '-'}
                                    </span>
                                    <span>
                                        {t('MODERATION.TEST_CACHE', '缓存')}：{testResult.from_cache ? t('COMMON.YES', '是') : t('COMMON.NO', '否')}
                                    </span>
                                    {testResult.upstream_status && (
                                        <span>
                                            {t('MODERATION.TEST_UPSTREAM_STATUS', '上游状态')}：HTTP {testResult.upstream_status}
                                        </span>
                                    )}
                                    {testResult.upstream_error_type && (
                                        <span className="min-w-0 break-words">
                                            {t('MODERATION.TEST_UPSTREAM_ERROR', '上游错误')}：{testResult.upstream_error_type}{testResult.upstream_error_code ? ` / ${testResult.upstream_error_code}` : ''}
                                        </span>
                                    )}
                                    {testResult.upstream_message && (
                                        <span className="min-w-0 break-words md:col-span-2">
                                            {t('MODERATION.TEST_UPSTREAM_MESSAGE', '上游消息')}：{testResult.upstream_message}
                                        </span>
                                    )}
                                    {testResult.retry_after && (
                                        <span>
                                            {t('MODERATION.TEST_RETRY_AFTER', '建议等待')}：{testResult.retry_after}
                                        </span>
                                    )}
                                    {testResult.upstream_request_id && (
                                        <span className="min-w-0 break-words">
                                            {t('MODERATION.TEST_REQUEST_ID', '请求 ID')}：{testResult.upstream_request_id}
                                        </span>
                                    )}
                                    {rateLimit['x-ratelimit-remaining-requests'] && (
                                        <span>
                                            {t('MODERATION.TEST_RL_REQ', '请求剩余')}：{rateLimit['x-ratelimit-remaining-requests']} / {rateLimit['x-ratelimit-limit-requests'] || '-'}
                                        </span>
                                    )}
                                    {rateLimit['retry-after'] && !testResult.retry_after && (
                                        <span>
                                            {t('MODERATION.TEST_RETRY_AFTER', '建议等待')}：{rateLimit['retry-after']}
                                        </span>
                                    )}
                                    {rateLimit['x-ratelimit-reset-requests'] && (
                                        <span>
                                            {t('MODERATION.TEST_RL_RESET_REQ', '请求重置')}：{rateLimit['x-ratelimit-reset-requests']}
                                        </span>
                                    )}
                                    {rateLimit['x-ratelimit-remaining-tokens'] && (
                                        <span>
                                            {t('MODERATION.TEST_RL_TOKENS', 'Token 剩余')}：{rateLimit['x-ratelimit-remaining-tokens']} / {rateLimit['x-ratelimit-limit-tokens'] || '-'}
                                        </span>
                                    )}
                                    {rateLimit['x-ratelimit-reset-tokens'] && (
                                        <span>
                                            {t('MODERATION.TEST_RL_RESET_TOKENS', 'Token 重置')}：{rateLimit['x-ratelimit-reset-tokens']}
                                        </span>
                                    )}
                                </div>
                            </div>
                        </div>
                    </div>
                )}

                <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                    <div>
                        <span className="block text-xs font-medium text-on-surface-variant mb-1.5">
                            {t('MODERATION.PROVIDER', '审核供应商')}
                        </span>
                        <div className="w-full rounded-control border border-outline bg-surface-container-high px-3 py-2 text-on-surface">
                            {t('MODERATION.PROVIDER_CLIPROXY_MODEL', '上游模型池')}
                        </div>
                        <p className="text-[11px] text-on-surface-variant mt-1">
                            {t('MODERATION.PROVIDER_HINT', '审核统一走上游模型池，优先复用同地址 cliproxy 渠道的 API key。')}
                        </p>
                    </div>
                    <div>
                        <label htmlFor="mod-model" className="block text-xs font-medium text-on-surface-variant mb-1.5">
                            {t('MODERATION.MODEL', '审核模型')}
                        </label>
                        <input
                            id="mod-model"
                            type="text"
                            value={configs[moderationModelKey] || ''}
                            onChange={e => handleChange(moderationModelKey, e.target.value)}
                            placeholder={moderationModelFallback}
                            className="w-full bg-surface-container-high border border-outline rounded-control px-3 py-2 text-on-surface outline-none focus:border-primary"
                        />
                        <p className="text-[11px] text-on-surface-variant mt-1">
                            {t('MODERATION.MODEL_HINT', '推荐使用 gpt-5.4-mini 做默认二审；也可以换成 上游模型池里额度更宽裕的模型。')}
                        </p>
                    </div>
                    <div>
                        <label htmlFor="mod-threshold" className="block text-xs font-medium text-on-surface-variant mb-1.5">
                            {t('MODERATION.THRESHOLD', '命中阈值')}: <span className="font-mono text-primary">{threshold.toFixed(2)}</span>
                        </label>
                        {/* native input[type=range] WCAG 2.2 AA：keyboard accessible + aria 属性 */}
                        <input
                            id="mod-threshold"
                            type="range"
                            min="0"
                            max="1"
                            step="0.05"
                            value={threshold}
                            aria-valuemin={0}
                            aria-valuemax={1}
                            aria-valuenow={threshold}
                            onChange={e => handleChange('moderation_threshold', e.target.value)}
                            className="w-full accent-primary"
                        />
                        <p className="text-[11px] text-on-surface-variant mt-1">
                            {t('MODERATION.THRESHOLD_HINT', '分类器 confidence 或上游 safety score ≥ 阈值即判为命中。0.8 是内测阶段的保守起点。')}
                        </p>
                    </div>
                    <div>
                        <label htmlFor="mod-api-timeout" className="block text-xs font-medium text-on-surface-variant mb-1.5">
                            {t('MODERATION.API_TIMEOUT', '审核超时（秒）')}
                        </label>
                        <input
                            id="mod-api-timeout"
                            type="number"
                            min="1"
                            max="120"
                            value={configs.moderation_api_timeout_seconds || ''}
                            onChange={e => handleChange('moderation_api_timeout_seconds', e.target.value)}
                            placeholder="15"
                            className="w-full bg-surface-container-high border border-outline rounded-control px-3 py-2 text-on-surface outline-none focus:border-primary"
                        />
                        <p className="text-[11px] text-on-surface-variant mt-1">
                            {t('MODERATION.API_TIMEOUT_HINT', '上游模型池二审的总等待时间。gpt-5.4-mini 实测常见 4-6 秒，默认 15 秒更稳。')}
                        </p>
                    </div>
                </div>
            </section>

            {/* ── 关键字词库 ───────────────────────────────────────────────── */}
            <section className="bg-surface-container border border-outline-variant rounded-overlay p-4 md:p-6 mb-6 ">
                <h3 className="text-sm font-semibold text-on-surface flex items-center gap-2 mb-4 pb-3 border-b border-outline-variant/50">
                    <AlertTriangle size={16} className="text-warning" />
                    {t('MODERATION.SECTION_KEYWORDS', '关键字快扫词库')}
                </h3>
                <p className="text-xs text-on-surface-variant mb-3">
                    {t('MODERATION.KEYWORDS_DESC', '一行一个关键字，会自动 lowercase + 去重。审核等级 = keyword 或 strict 时启用。strings.Contains 子串匹配。')}
                </p>
                <label htmlFor="mod-keywords" className="block text-xs font-medium text-on-surface-variant mb-1.5">
                    {t('MODERATION.KEYWORDS_LABEL', '词库（一行一个）')}
                </label>
                <textarea
                    id="mod-keywords"
                    rows={8}
                    value={keywordText}
                    onChange={e => setKeywordText(e.target.value)}
                    onBlur={flushKeywords}
                    placeholder={'Kiro_workspace\nkiro_session_id\nDAN mode\nignore previous instructions'}
                    className="w-full bg-surface-container-high border border-outline rounded-control px-3 py-2 text-on-surface font-mono text-xs outline-none focus:border-primary"
                />
                <p className="text-[11px] text-on-surface-variant mt-1">
                    {t('MODERATION.KEYWORDS_HINT', '当前 {{count}} 条关键字。修改后点页面底部的「保存」按钮生效。', { count: keywordList.length })}
                </p>

                <div className="mt-5 rounded-overlay border border-outline-variant bg-surface/40 p-4">
                    <div className="flex flex-col gap-3 lg:flex-row lg:items-start lg:justify-between">
                        <div>
                            <h4 className="text-sm font-semibold text-on-surface flex items-center gap-2">
                                <Bot size={16} className="text-primary" />
                                {t('MODERATION.KEYWORD_AI_TITLE', 'AI 词库候选')}
                            </h4>
                            <p className="mt-1 text-[11px] text-on-surface-variant max-w-2xl">
                                {t('MODERATION.KEYWORD_AI_DESC', '通过已保存的审核 provider 生成候选词，只返回候选，不会自动写入词库。选择后合并，再点击底部保存。')}
                            </p>
                        </div>
                        <div className="flex items-center gap-2">
                            <input
                                type="number"
                                min="1"
                                max="200"
                                value={configs.moderation_keyword_ai_max_candidates || '80'}
                                onChange={e => handleChange('moderation_keyword_ai_max_candidates', e.target.value)}
                                className="h-9 w-24 rounded-control border border-outline bg-surface-container-high px-3 text-sm text-on-surface outline-none focus:border-primary"
                                aria-label={t('MODERATION.KEYWORD_AI_MAX', '候选数量')}
                            />
                            <button
                                type="button"
                                onClick={generateKeywordCandidates}
                                disabled={generatingKeywords}
                                className="inline-flex h-9 items-center justify-center gap-2 rounded-control border border-primary/40 bg-primary/10 px-3 text-xs font-semibold text-primary hover:bg-primary/15 disabled:opacity-60"
                            >
                                {generatingKeywords ? <RotateCw size={15} className="animate-spin" /> : <Bot size={15} />}
                                {generatingKeywords ? t('MODERATION.KEYWORD_AI_RUNNING', '生成中...') : t('MODERATION.KEYWORD_AI_BUTTON', '生成候选')}
                            </button>
                        </div>
                    </div>
                    <textarea
                        rows={2}
                        value={keywordAIFocus}
                        onChange={e => setKeywordAIFocus(e.target.value)}
                        placeholder={t('MODERATION.KEYWORD_AI_FOCUS_PLACEHOLDER', '可选：补充本轮重点，例如“Claude Code 破甲、泄露系统提示词、伪造工具调用”')}
                        className="mt-3 w-full rounded-control border border-outline bg-surface-container-high px-3 py-2 text-xs text-on-surface outline-none focus:border-primary"
                    />
                    {keywordCandidates.length > 0 && (
                        <div className="mt-4 overflow-hidden rounded-overlay border border-outline-variant">
                            <div className="flex items-center justify-between gap-3 border-b border-outline-variant bg-surface-container-high px-3 py-2">
                                <span className="text-xs font-semibold text-on-surface">
                                    {t('MODERATION.KEYWORD_AI_CANDIDATES', '候选词')} · {keywordCandidates.length}
                                </span>
                                <button
                                    type="button"
                                    onClick={mergeSelectedCandidates}
                                    className="inline-flex items-center gap-1.5 rounded-control bg-primary px-3 py-1.5 text-xs font-medium text-on-primary hover:opacity-90"
                                >
                                    <ListChecks size={14} />
                                    {t('MODERATION.KEYWORD_AI_MERGE', '合并已选')}
                                </button>
                            </div>
                            <div className="max-h-72 overflow-auto divide-y divide-outline-variant/70">
                                {keywordCandidates.map((c, idx) => (
                                    <label key={`${c.keyword}-${idx}`} className="grid cursor-pointer grid-cols-[auto,1fr] gap-3 px-3 py-2 hover:bg-surface-container-high/60">
                                        <input
                                            type="checkbox"
                                            checked={selectedCandidates.has(idx)}
                                            onChange={() => toggleCandidate(idx)}
                                            className="mt-1 accent-primary"
                                        />
                                        <div className="min-w-0">
                                            <div className="flex flex-wrap items-center gap-2">
                                                <span className="font-mono text-xs text-on-surface break-all">{c.keyword}</span>
                                                <span className="rounded-control-full bg-primary/10 px-2 py-0.5 text-[10px] uppercase text-primary">{c.category || 'jailbreak'}</span>
                                                <span className="rounded-control-full bg-warning/10 px-2 py-0.5 text-[10px] uppercase text-warning">{c.severity || 'medium'}</span>
                                            </div>
                                            {c.reason && <p className="mt-1 text-[11px] text-on-surface-variant">{c.reason}</p>}
                                        </div>
                                    </label>
                                ))}
                            </div>
                        </div>
                    )}
                </div>
            </section>

            {/* ── 风险规则层 ───────────────────────────────────────────────── */}
            <section className="bg-surface-container border border-outline-variant rounded-overlay p-4 md:p-6 mb-6 ">
                <h3 className="text-sm font-semibold text-on-surface flex items-center gap-2 mb-4 pb-3 border-b border-outline-variant/50">
                    <ListChecks size={16} className="text-primary" />
                    {t('MODERATION.SECTION_RISK_RULES', '组合规则与风险打分')}
                </h3>
                <p className="text-xs text-on-surface-variant mb-3">
                    {t('MODERATION.RISK_RULES_DESC', '用于承载 regex / combo / score_only 规则。block 直接拦截，model_review 升级到智能审核 provider 二审，score_only 只记录风控事件。')}
                </p>
                <label htmlFor="mod-risk-rules" className="block text-xs font-medium text-on-surface-variant mb-1.5">
                    {t('MODERATION.RISK_RULES_LABEL', '规则 JSON')}
                </label>
                <textarea
                    id="mod-risk-rules"
                    rows={12}
                    value={riskRulesText}
                    onChange={e => setRiskRulesText(e.target.value)}
                    onBlur={flushRiskRules}
                    spellCheck={false}
                    className="w-full bg-surface-container-high border border-outline rounded-control px-3 py-2 text-on-surface font-mono text-xs outline-none focus:border-primary"
                />
                <p className="text-[11px] text-on-surface-variant mt-1">
                    {t('MODERATION.RISK_RULES_HINT', '当前 {{count}} 条规则。修改后失焦会校验 JSON，点击页面底部「保存」后生效。', { count: riskRuleCount })}
                </p>
                <div className="mt-3 grid grid-cols-1 gap-3 text-[11px] text-on-surface-variant md:grid-cols-3">
                    <div className="rounded-control border border-outline-variant bg-surface/40 p-3">
                        <span className="font-semibold text-on-surface">block</span>
                        <p className="mt-1">{t('MODERATION.RISK_RULES_BLOCK_HINT', '极低误伤规则，命中后直接拒绝。')}</p>
                    </div>
                    <div className="rounded-control border border-outline-variant bg-surface/40 p-3">
                        <span className="font-semibold text-on-surface">model_review</span>
                        <p className="mt-1">{t('MODERATION.RISK_RULES_REVIEW_HINT', '高风险但需上下文判断，命中后走智能审核 provider 二审。')}</p>
                    </div>
                    <div className="rounded-control border border-outline-variant bg-surface/40 p-3">
                        <span className="font-semibold text-on-surface">score_only</span>
                        <p className="mt-1">{t('MODERATION.RISK_RULES_SCORE_HINT', '中风险信号，只写审计和累计风险。')}</p>
                    </div>
                </div>
            </section>

            {/* ── 自动处置与风控记录 ───────────────────────────────────────── */}
            <section className="bg-surface-container border border-outline-variant rounded-overlay p-4 md:p-6 mb-6 ">
                <div className="mb-4 flex flex-col gap-3 border-b border-outline-variant/50 pb-3 lg:flex-row lg:items-start lg:justify-between">
                    <div>
                        <h3 className="text-sm font-semibold text-on-surface flex items-center gap-2">
                            <Ban size={16} className="text-error" />
                            {t('MODERATION.SECTION_AUTOBAN', '自动处置与风控记录')}
                        </h3>
                        <p className="text-[11px] text-on-surface-variant mt-1 max-w-2xl">
                            {t('MODERATION.AUTOBAN_DESC', '审核命中会写入风控记录；开启自动封禁后，达到阈值的普通用户会立即封禁并刷新 token 缓存。管理员账号不会被自动封禁。')}
                        </p>
                    </div>
                    <button
                        type="button"
                        onClick={loadRiskEvents}
                        disabled={riskEventsLoading}
                        className="inline-flex h-9 items-center justify-center gap-2 rounded-control border border-outline bg-surface-container-high px-3 text-xs font-semibold text-on-surface hover:border-primary disabled:opacity-60"
                    >
                        <RotateCw size={15} className={riskEventsLoading ? 'animate-spin' : ''} />
                        {t('SYSTEM.REFRESH', '刷新')}
                    </button>
                </div>

                <div className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-5">
                    <label className="flex min-h-[72px] cursor-pointer items-center justify-between gap-3 rounded-overlay border border-outline bg-surface-container-high px-3 py-3 xl:col-span-1">
                        <span>
                            <span className="block text-xs font-semibold text-on-surface">{t('MODERATION.AUTOBAN_ENABLED', '自动封禁')}</span>
                            <span className="mt-1 block text-[11px] text-on-surface-variant">{t('MODERATION.AUTOBAN_ENABLED_HINT', '命中阈值后直接限制账户')}</span>
                        </span>
                        <input
                            type="checkbox"
                            checked={String(configs.moderation_autoban_enabled || 'false').toLowerCase() === 'true'}
                            onChange={e => handleChange('moderation_autoban_enabled', e.target.checked ? 'true' : 'false')}
                            className="h-4 w-4 accent-primary"
                        />
                    </label>
                    {[
                        ['moderation_autoban_keyword_threshold', t('MODERATION.AUTOBAN_KEYWORD', '关键字阈值'), '1'],
                        ['moderation_autoban_policy_threshold', t('MODERATION.AUTOBAN_POLICY', '智能审核阈值'), '0'],
                        ['moderation_autoban_risk_rule_threshold', t('MODERATION.AUTOBAN_RISK_RULE', '风险规则阈值'), '1'],
                        ['moderation_autoban_risk_score_threshold', t('MODERATION.AUTOBAN_RISK_SCORE', '风险打分阈值'), '0'],
                        ['moderation_autoban_image_threshold', t('MODERATION.AUTOBAN_IMAGE', '图片策略阈值'), '2'],
                        ['moderation_autoban_oversize_threshold', t('MODERATION.AUTOBAN_OVERSIZE', '超长阈值'), '0'],
                    ].map(([key, label, fallback]) => (
                        <div key={key}>
                            <label className="mb-1.5 block text-xs font-medium text-on-surface-variant">{label}</label>
                            <input
                                type="number"
                                min="0"
                                max="100"
                                value={configs[key] || fallback}
                                onChange={e => handleChange(key, e.target.value)}
                                className="w-full rounded-control border border-outline bg-surface-container-high px-3 py-2 text-sm text-on-surface outline-none focus:border-primary"
                            />
                            <p className="mt-1 text-[10px] text-on-surface-variant">{t('MODERATION.AUTOBAN_ZERO_OFF', '0 表示关闭该类自动封禁')}</p>
                        </div>
                    ))}
                    <div className="md:col-span-2 xl:col-span-2">
                        <label className="mb-1.5 block text-xs font-medium text-on-surface-variant">
                            {t('MODERATION.AUTOBAN_WINDOW', '统计窗口（秒）')}
                        </label>
                        <input
                            type="number"
                            min="60"
                            value={configs.moderation_autoban_window_seconds || '86400'}
                            onChange={e => handleChange('moderation_autoban_window_seconds', e.target.value)}
                            className="w-full rounded-control border border-outline bg-surface-container-high px-3 py-2 text-sm text-on-surface outline-none focus:border-primary"
                        />
                    </div>
                </div>

                <div className="mt-5 rounded-overlay border border-outline-variant bg-surface/30">
                    <div className="grid grid-cols-2 gap-px overflow-hidden rounded-control-t-xl border-b border-outline-variant bg-outline-variant/60 md:grid-cols-4 xl:grid-cols-7">
                        {[
                            [t('MODERATION.RISK_SUMMARY_SHOWN', '当前显示'), riskSummary.shown],
                            [t('MODERATION.RISK_SUMMARY_BLOCKED', '请求拦截'), riskSummary.blocked],
                            [t('MODERATION.RISK_SUMMARY_REVIEW', '规则二审/打分'), riskSummary.review],
                            [t('MODERATION.RISK_SUMMARY_UNAVAILABLE', '审核不可达'), riskSummary.unavailable],
                            [t('MODERATION.RISK_SUMMARY_AUTOBAN', '自动封禁'), riskSummary.autoban],
                            [t('MODERATION.RISK_SUMMARY_USER_SCOPE', '用户输入'), riskSummary.userMessage],
                            [t('MODERATION.RISK_SUMMARY_CONTEXT_SCOPE', '上下文噪声'), riskSummary.nonUserContext],
                        ].map(([label, value]) => (
                            <div key={label} className="bg-surface-container-high px-3 py-3">
                                <div className="text-[10px] font-semibold uppercase tracking-wide text-on-surface-variant">{label}</div>
                                <div className="mt-1 font-mono text-lg font-semibold text-on-surface">{value}</div>
                            </div>
                        ))}
                    </div>

                    <div className="grid grid-cols-1 gap-3 border-b border-outline-variant px-3 py-3 md:grid-cols-[1.2fr,0.8fr,0.7fr,auto] md:items-end">
                        <label className="block">
                            <span className="mb-1.5 flex items-center gap-1.5 text-[11px] font-medium text-on-surface-variant">
                                <Filter size={12} />
                                {t('MODERATION.RISK_FILTER_ACTION', '事件类型')}
                            </span>
                            <select
                                value={riskEventAction}
                                onChange={e => setRiskEventAction(e.target.value)}
                                className="w-full rounded-control border border-outline bg-surface-container-high px-3 py-2 text-xs text-on-surface outline-none focus:border-primary"
                            >
                                {riskActionOptions.map(([value, label]) => (
                                    <option key={value} value={value}>{label}</option>
                                ))}
                            </select>
                        </label>
                        <label className="block">
                            <span className="mb-1.5 flex items-center gap-1.5 text-[11px] font-medium text-on-surface-variant">
                                <Hash size={12} />
                                {t('MODERATION.RISK_FILTER_USER', '用户 ID')}
                            </span>
                            <input
                                type="number"
                                min="1"
                                value={riskEventUserID}
                                onChange={e => setRiskEventUserID(e.target.value)}
                                placeholder={t('MODERATION.RISK_FILTER_USER_PLACEHOLDER', '全部用户')}
                                className="w-full rounded-control border border-outline bg-surface-container-high px-3 py-2 text-xs text-on-surface outline-none focus:border-primary"
                            />
                        </label>
                        <label className="block">
                            <span className="mb-1.5 block text-[11px] font-medium text-on-surface-variant">
                                {t('MODERATION.RISK_FILTER_LIMIT', '数量')}
                            </span>
                            <input
                                type="number"
                                min="1"
                                max="200"
                                value={riskEventLimit}
                                onChange={e => setRiskEventLimit(e.target.value)}
                                className="w-full rounded-control border border-outline bg-surface-container-high px-3 py-2 text-xs text-on-surface outline-none focus:border-primary"
                            />
                        </label>
                        <button
                            type="button"
                            onClick={loadRiskEvents}
                            disabled={riskEventsLoading}
                            className="inline-flex h-9 items-center justify-center gap-2 rounded-control bg-primary px-3 text-xs font-semibold text-on-primary hover:bg-primary/90 disabled:opacity-60"
                        >
                            <RotateCw size={14} className={riskEventsLoading ? 'animate-spin' : ''} />
                            {t('MODERATION.RISK_APPLY_FILTERS', '应用筛选')}
                        </button>
                    </div>

                    <div className="flex flex-wrap items-center justify-between gap-2 border-b border-outline-variant px-3 py-2 text-[11px] text-on-surface-variant">
                        <span>{t('MODERATION.RISK_AUDIT_NOTE', '新拦截会显示脱敏内容预览和指纹；历史旧记录没有保存请求内容。')}</span>
                        <span>
                            {riskEventsLoadedAt
                                ? t('MODERATION.RISK_LOADED_AT', '加载于 {{time}}', { time: riskEventsLoadedAt.toLocaleTimeString() })
                                : t('MODERATION.RISK_NOT_LOADED', '尚未加载')}
                        </span>
                    </div>

                    <div className="max-h-[34rem] overflow-auto divide-y divide-outline-variant/70">
                        {riskEventsLoading && (
                            <div className="flex items-center gap-2 px-3 py-4 text-sm text-on-surface-variant">
                                <RotateCw size={15} className="animate-spin" />
                                {t('COMMON.LOADING', '加载中...')}
                            </div>
                        )}
                        {!riskEventsLoading && riskEvents.length === 0 && (
                            <div className="px-3 py-4 text-sm text-on-surface-variant">
                                {t('MODERATION.RISK_EMPTY', '暂无风控记录')}
                            </div>
                        )}
                        {!riskEventsLoading && riskEvents.map((evt) => {
                            const Icon = actionIcon(evt.action_type);
                            const { parsed, badges, segmentScope } = riskBadgesForEvent(evt);
                            return (
                                <div key={evt.id} className="px-3 py-3 text-xs">
                                    <div className="grid grid-cols-1 gap-3 lg:grid-cols-[1.25fr,1fr,2fr]">
                                        <div className="min-w-0">
                                            <div className={`inline-flex max-w-full items-center gap-2 rounded-control-full border px-2.5 py-1 ${actionTone(evt.action_type)}`}>
                                                <Icon size={14} className="shrink-0" />
                                                <span className="truncate font-semibold">
                                                    {actionLabels[evt.action_type] || evt.action_type}
                                                </span>
                                            </div>
                                            <div className="mt-2 font-mono text-[11px] text-on-surface-variant">
                                                {evt.action_type}
                                            </div>
                                            {segmentScope && (
                                                <div className={`mt-2 inline-flex max-w-full items-center gap-1 rounded-control-full border px-2 py-1 text-[11px] ${segmentScopeTone(segmentScope)}`}>
                                                    <span className="shrink-0 text-on-surface-variant">{t('MODERATION.RISK_SEGMENT_SCOPE', '来源')}</span>
                                                    <span className="truncate font-mono">{formatSegmentScope(segmentScope)}</span>
                                                </div>
                                            )}
                                        </div>

                                        <div className="min-w-0 space-y-1.5 text-on-surface-variant">
                                            <div className="flex min-w-0 items-center gap-2">
                                                <Hash size={13} className="shrink-0" />
                                                <span className="truncate">
                                                    #{evt.target_user_id} {evt.username || '-'}
                                                </span>
                                            </div>
                                            <div className="flex min-w-0 items-center gap-2">
                                                <Clock size={13} className="shrink-0" />
                                                <span className="truncate">{formatRiskTime(evt.created_at)}</span>
                                            </div>
                                            {evt.ip_address && (
                                                <div className="truncate font-mono text-[11px] text-on-surface-variant/80">
                                                    IP {evt.ip_address}
                                                </div>
                                            )}
                                        </div>

                                        <div className="min-w-0">
                                            <div className="flex flex-wrap gap-1.5">
                                                {badges.length > 0 ? badges.map((badge, idx) => (
                                                    <span key={`${badge.label}-${idx}`} className={`inline-flex max-w-full items-center gap-1 rounded-control-full border px-2 py-1 text-[11px] ${badge.tone}`}>
                                                        <span className="shrink-0 text-on-surface-variant">{badge.label}</span>
                                                        <span className="truncate font-mono">{badge.value}</span>
                                                    </span>
                                                )) : (
                                                    <span className="text-on-surface-variant">{t('MODERATION.RISK_NO_STRUCTURED_DETAILS', '无结构化详情')}</span>
                                                )}
                                            </div>
                                            {parsed.data?.content_preview && (
                                                <div className="mt-2 rounded-control border border-success/20 bg-success/5">
                                                    <div className="flex flex-wrap items-center justify-between gap-2 border-b border-success/10 px-3 py-2">
                                                        <span className="text-[11px] font-semibold text-success">
                                                            {t('MODERATION.RISK_CONTENT_PREVIEW', '命中内容预览（已脱敏）')}
                                                        </span>
                                                        {parsed.data.content_sha256 && (
                                                            <span className="max-w-full truncate font-mono text-[10px] text-on-surface-variant">
                                                                sha256 {parsed.data.content_sha256}
                                                            </span>
                                                        )}
                                                    </div>
                                                    <pre className="max-h-40 overflow-auto whitespace-pre-wrap break-words px-3 py-2 font-mono text-[11px] leading-relaxed text-on-surface">
                                                        {parsed.data.content_preview}
                                                    </pre>
                                                </div>
                                            )}
                                            {parsed.formatted && (
                                                <details className="mt-2 rounded-control border border-outline-variant bg-surface-container-high/60">
                                                    <summary className="cursor-pointer px-3 py-2 text-[11px] font-medium text-on-surface-variant">
                                                        {t('MODERATION.RISK_RAW_DETAILS', '查看原始详情')}
                                                    </summary>
                                                    <pre className="max-h-52 overflow-auto whitespace-pre-wrap break-words border-t border-outline-variant px-3 py-2 font-mono text-[11px] leading-relaxed text-on-surface-variant">
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
            </section>

            {/* ── 缓存参数 ────────────────────────────────────────────────── */}
            <section className="bg-surface-container border border-outline-variant rounded-overlay p-4 md:p-6 mb-6 ">
                <h3 className="text-sm font-semibold text-on-surface mb-4 pb-3 border-b border-outline-variant/50">
                    {t('MODERATION.SECTION_CACHE', '缓存与防侧信道')}
                </h3>
                <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                    <div>
                        <label htmlFor="mod-cache-ttl" className="block text-xs font-medium text-on-surface-variant mb-1.5">
                            {t('MODERATION.CACHE_TTL', '缓存 TTL (秒)')}
                        </label>
                        <input
                            id="mod-cache-ttl"
                            type="number"
                            min="0"
                            value={configs.moderation_cache_ttl_sec || ''}
                            onChange={e => handleChange('moderation_cache_ttl_sec', e.target.value)}
                            placeholder="300"
                            className="w-full bg-surface-container-high border border-outline rounded-control px-3 py-2 text-on-surface outline-none focus:border-primary"
                        />
                    </div>
                    <div>
                        <label htmlFor="mod-cache-max" className="block text-xs font-medium text-on-surface-variant mb-1.5">
                            {t('MODERATION.CACHE_MAX', 'LRU 最大条目数')}
                        </label>
                        <input
                            id="mod-cache-max"
                            type="number"
                            min="100"
                            value={configs.moderation_cache_max_entries || ''}
                            onChange={e => handleChange('moderation_cache_max_entries', e.target.value)}
                            placeholder="10000"
                            className="w-full bg-surface-container-high border border-outline rounded-control px-3 py-2 text-on-surface outline-none focus:border-primary"
                        />
                    </div>
                    <div className="md:col-span-2">
                        <label htmlFor="mod-secret" className="block text-xs font-medium text-on-surface-variant mb-1.5">
                            {t('MODERATION.HMAC_SECRET', 'HMAC 缓存密钥（防侧信道）')}
                        </label>
                        <div className="relative">
                            <input
                                id="mod-secret"
                                type={showSecret ? 'text' : 'password'}
                                value={configs.moderation_cache_secret || ''}
                                onChange={e => handleChange('moderation_cache_secret', e.target.value)}
                                placeholder={t('MODERATION.HMAC_AUTO', '留空让后端首次启动时自动生成 256bit 随机密钥')}
                                className="w-full bg-surface-container-high border border-outline rounded-control pl-3 pr-20 py-2 text-on-surface font-mono text-xs outline-none focus:border-primary"
                            />
                            <div className="absolute right-2 top-1/2 -translate-y-1/2 flex gap-1">
                                <button
                                    type="button"
                                    onClick={() => setShowSecret(s => !s)}
                                    aria-label={showSecret ? t('COMMON.HIDE', '隐藏') : t('COMMON.SHOW', '显示')}
                                    className="p-1 text-on-surface-variant hover:text-on-surface"
                                >
                                    {showSecret ? <EyeOff size={16} /> : <Eye size={16} />}
                                </button>
                                <button
                                    type="button"
                                    onClick={resetHmacSecret}
                                    aria-label={t('MODERATION.HMAC_RESET', '重置')}
                                    className="p-1 text-warning hover:text-warning"
                                >
                                    <RotateCw size={16} />
                                </button>
                            </div>
                        </div>
                        <p className="text-[11px] text-on-surface-variant mt-1">
                            {t('MODERATION.HMAC_HINT', '重置 = 让全部审核缓存立即失效（防长效侧信道猜测）')}
                        </p>
                    </div>
                </div>
            </section>

            {/* ── 长 prompt 处理 ──────────────────────────────────────────── */}
            <section className="bg-surface-container border border-outline-variant rounded-overlay p-4 md:p-6 mb-6 ">
                <h3 className="text-sm font-semibold text-on-surface mb-4 pb-3 border-b border-outline-variant/50">
                    {t('MODERATION.SECTION_LIMITS', '长 Prompt 限制')}
                </h3>
                <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
                    <div>
                        <label htmlFor="mod-max-chars" className="block text-xs font-medium text-on-surface-variant mb-1.5">
                            {t('MODERATION.MAX_CHARS', '单次最大字符数 (rune)')}
                        </label>
                        <input
                            id="mod-max-chars"
                            type="number"
                            min="0"
                            value={configs.moderation_max_chars || ''}
                            onChange={e => handleChange('moderation_max_chars', e.target.value)}
                            placeholder="262144"
                            className="w-full bg-surface-container-high border border-outline rounded-control px-3 py-2 text-on-surface outline-none focus:border-primary"
                        />
                    </div>
                    <div>
                        <label htmlFor="mod-chunk-chars" className="block text-xs font-medium text-on-surface-variant mb-1.5">
                            {t('MODERATION.CHUNK_CHARS', '分块大小 (rune)')}
                        </label>
                        <input
                            id="mod-chunk-chars"
                            type="number"
                            min="0"
                            value={configs.moderation_chunk_chars || ''}
                            onChange={e => handleChange('moderation_chunk_chars', e.target.value)}
                            placeholder="28672"
                            className="w-full bg-surface-container-high border border-outline rounded-control px-3 py-2 text-on-surface outline-none focus:border-primary"
                        />
                    </div>
                    <div>
                        <label htmlFor="mod-max-chunks" className="block text-xs font-medium text-on-surface-variant mb-1.5">
                            {t('MODERATION.MAX_CHUNKS', '最大分块数')}
                        </label>
                        <input
                            id="mod-max-chunks"
                            type="number"
                            min="1"
                            value={configs.moderation_max_chunks || ''}
                            onChange={e => handleChange('moderation_max_chunks', e.target.value)}
                            placeholder="8"
                            className="w-full bg-surface-container-high border border-outline rounded-control px-3 py-2 text-on-surface outline-none focus:border-primary"
                        />
                    </div>
                    <div>
                        <label htmlFor="mod-long-min-tokens" className="block text-xs font-medium text-on-surface-variant mb-1.5">
                            {t('MODERATION.LONG_CONTEXT_MIN_TOKENS', '长上下文阈值 tokens')}
                        </label>
                        <input
                            id="mod-long-min-tokens"
                            type="number"
                            min="0"
                            value={configs.moderation_long_context_min_tokens || ''}
                            onChange={e => handleChange('moderation_long_context_min_tokens', e.target.value)}
                            placeholder="800000"
                            className="w-full bg-surface-container-high border border-outline rounded-control px-3 py-2 text-on-surface outline-none focus:border-primary"
                        />
                    </div>
                    <div>
                        <label htmlFor="mod-long-max-chars" className="block text-xs font-medium text-on-surface-variant mb-1.5">
                            {t('MODERATION.LONG_CONTEXT_MAX_CHARS', '长上下文最大字符数')}
                        </label>
                        <input
                            id="mod-long-max-chars"
                            type="number"
                            min="0"
                            value={configs.moderation_long_context_max_chars || ''}
                            onChange={e => handleChange('moderation_long_context_max_chars', e.target.value)}
                            placeholder="4194304"
                            className="w-full bg-surface-container-high border border-outline rounded-control px-3 py-2 text-on-surface outline-none focus:border-primary"
                        />
                    </div>
                    <div>
                        <label htmlFor="mod-long-max-chunks" className="block text-xs font-medium text-on-surface-variant mb-1.5">
                            {t('MODERATION.LONG_CONTEXT_MAX_CHUNKS', '长上下文抽样块数')}
                        </label>
                        <input
                            id="mod-long-max-chunks"
                            type="number"
                            min="1"
                            value={configs.moderation_long_context_max_chunks || ''}
                            onChange={e => handleChange('moderation_long_context_max_chunks', e.target.value)}
                            placeholder="12"
                            className="w-full bg-surface-container-high border border-outline rounded-control px-3 py-2 text-on-surface outline-none focus:border-primary"
                        />
                    </div>
                </div>
                <p className="text-[11px] text-on-surface-variant mt-2">
                    {t('MODERATION.LIMITS_HINT', '普通模型超过 max_chars 直接拒绝；长上下文模型按 max_context_length 自动放宽，并抽样若干分布块送智能审核。')}
                </p>
            </section>

            {/* ── 多模态图片策略 ───────────────────────────────────────────── */}
            <section className="bg-surface-container border border-outline-variant rounded-overlay p-4 md:p-6 mb-6 ">
                <h3 className="text-sm font-semibold text-on-surface mb-4 pb-3 border-b border-outline-variant/50">
                    {t('MODERATION.SECTION_IMAGE', '多模态图片策略')}
                </h3>
                <div className="flex flex-col md:flex-row md:items-center justify-between gap-3">
                    {/* fix CRITICAL R23-C5（gemini 审查）：必须用 <label htmlFor> 而不是 <span>，
                        否则屏幕阅读器无法识别 image_policy select 的语义 → 不满足 WCAG 2.2 AA */}
                    <label htmlFor="mod-image-policy" className="flex flex-col gap-1 w-full md:w-2/3 cursor-pointer">
                        <span className="text-on-surface-variant font-medium text-sm">
                            {t('MODERATION.IMAGE_POLICY', 'image_url 处理')}
                        </span>
                        <span className="text-[11px] text-on-surface-variant">
                            {t('MODERATION.IMAGE_POLICY_HINT', '智能审核第一版不直接审核外部 image_url。当前推荐 reject；skip 只适合确认上游自带安全拦截的模型。')}
                        </span>
                    </label>
                    {/* fix MINOR R23-m5：补 focus 状态，键盘 Tab 用户能看到聚焦 */}
                    <select
                        id="mod-image-policy"
                        value={configs.moderation_image_policy || 'submit'}
                        onChange={e => handleChange('moderation_image_policy', e.target.value)}
                        className="bg-surface-container-high border border-outline text-on-surface rounded-control px-4 py-2 outline-none text-sm w-full md:w-48 cursor-pointer hover:border-primary focus:border-primary focus:ring-2 focus:ring-primary/40"
                    >
                        <option value="submit">{t('MODERATION.IMAGE_SUBMIT', 'submit — 预留，当前会按审核不可达处理')}</option>
                        <option value="skip">{t('MODERATION.IMAGE_SKIP', 'skip — 跳过图片')}</option>
                        <option value="reject">{t('MODERATION.IMAGE_REJECT', 'reject — 直接拒绝（推荐）')}</option>
                    </select>
                </div>
            </section>

            {/* ── 拒绝文案 ────────────────────────────────────────────────── */}
            <section className="bg-surface-container border border-outline-variant rounded-overlay p-4 md:p-6 mb-6 ">
                <h3 className="text-sm font-semibold text-on-surface mb-4 pb-3 border-b border-outline-variant/50">
                    {t('MODERATION.SECTION_MESSAGES', '拒绝文案（按 Accept-Language 自动选）')}
                </h3>
                <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                    <div>
                        <label htmlFor="mod-block-zh" className="block text-xs font-medium text-on-surface-variant mb-1.5">
                            {t('MODERATION.BLOCK_ZH', '违规拒绝（中文）')}
                        </label>
                        <textarea
                            id="mod-block-zh"
                            rows={3}
                            value={configs.moderation_block_message_zh || ''}
                            onChange={e => handleChange('moderation_block_message_zh', e.target.value)}
                            className="w-full bg-surface-container-high border border-outline rounded-control px-3 py-2 text-on-surface outline-none focus:border-primary"
                        />
                    </div>
                    <div>
                        <label htmlFor="mod-block-en" className="block text-xs font-medium text-on-surface-variant mb-1.5">
                            {t('MODERATION.BLOCK_EN', '违规拒绝（英文）')}
                        </label>
                        <textarea
                            id="mod-block-en"
                            rows={3}
                            value={configs.moderation_block_message_en || ''}
                            onChange={e => handleChange('moderation_block_message_en', e.target.value)}
                            className="w-full bg-surface-container-high border border-outline rounded-control px-3 py-2 text-on-surface outline-none focus:border-primary"
                        />
                    </div>
                    <div>
                        <label htmlFor="mod-unavail-zh" className="block text-xs font-medium text-on-surface-variant mb-1.5">
                            {t('MODERATION.UNAVAIL_ZH', '审核不可达（中文）')}
                        </label>
                        <textarea
                            id="mod-unavail-zh"
                            rows={3}
                            value={configs.moderation_unavailable_message_zh || ''}
                            onChange={e => handleChange('moderation_unavailable_message_zh', e.target.value)}
                            className="w-full bg-surface-container-high border border-outline rounded-control px-3 py-2 text-on-surface outline-none focus:border-primary"
                        />
                    </div>
                    <div>
                        <label htmlFor="mod-unavail-en" className="block text-xs font-medium text-on-surface-variant mb-1.5">
                            {t('MODERATION.UNAVAIL_EN', '审核不可达（英文）')}
                        </label>
                        <textarea
                            id="mod-unavail-en"
                            rows={3}
                            value={configs.moderation_unavailable_message_en || ''}
                            onChange={e => handleChange('moderation_unavailable_message_en', e.target.value)}
                            className="w-full bg-surface-container-high border border-outline rounded-control px-3 py-2 text-on-surface outline-none focus:border-primary"
                        />
                    </div>
                </div>
            </section>

            {/* fix MAJOR R23-M11：保存按钮移交给 Settings.jsx 的全局 <SaveBar /> 统一渲染 */}
        </div>
    );
};

export default ContentModerationGlobals;
