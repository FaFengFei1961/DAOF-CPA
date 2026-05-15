import React, { useMemo, useState, useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { AlertTriangle, ListChecks, Bot, RotateCw } from 'lucide-react';
import toast from 'react-hot-toast';
import { authFetch } from '../../utils/authFetch';
import TextInput from '../ui/TextInput';
import Switch from '../ui/Switch';
import FormRow from '../ui/FormRow';

const KeywordRulesEditor = ({ configs, handleChange }) => {
    const { t } = useTranslation();

    const [keywordAIFocus, setKeywordAIFocus] = useState('');
    const [keywordCandidates, setKeywordCandidates] = useState([]);
    const [selectedCandidates, setSelectedCandidates] = useState(new Set());
    const [generatingKeywords, setGeneratingKeywords] = useState(false);

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

    useEffect(() => {
        setKeywordText(keywordList.join('\n'));
    }, [keywordList]);

    useEffect(() => {
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

    return (
        <div className="flex flex-col gap-6 mb-6">
            <FormRow.Group
                title={
                    <span className="flex items-center gap-2 text-warning">
                        <AlertTriangle size={16} />
                        {t('MODERATION.SECTION_KEYWORDS', '关键字快扫词库')}
                    </span>
                }
                sub={t('MODERATION.KEYWORDS_DESC', '一行一个关键字，会自动 lowercase + 去重。审核等级 = keyword 或 strict 时启用。strings.Contains 子串匹配。')}
            >
                <div className="flex flex-col gap-4">
                    <div>
                        <label htmlFor="mod-keywords" className="block text-sm font-medium text-on-surface mb-2">
                            {t('MODERATION.KEYWORDS_LABEL', '词库（一行一个）')}
                        </label>
                        <textarea
                            id="mod-keywords"
                            rows={8}
                            value={keywordText}
                            onChange={e => setKeywordText(e.target.value)}
                            onBlur={flushKeywords}
                            placeholder={'Kiro_workspace\nkiro_session_id\nDAN mode\nignore previous instructions'}
                            className="w-full bg-surface-container border border-outline rounded-control px-4 py-3 text-on-surface font-mono text-sm outline-none focus:border-primary focus:ring-1 focus:ring-primary/20 transition-all"
                        />
                        <p className="text-xs text-on-surface-variant mt-2">
                            {t('MODERATION.KEYWORDS_HINT', '当前 {{count}} 条关键字。修改后点页面底部的「保存」按钮生效。', { count: keywordList.length })}
                        </p>
                    </div>

                    <div className="rounded-overlay border border-outline-variant bg-surface/50 p-5 mt-2">
                        <div className="flex flex-col gap-4 lg:flex-row lg:items-center lg:justify-between mb-4">
                            <div>
                                <h4 className="text-sm font-semibold text-on-surface flex items-center gap-2">
                                    <Bot size={16} className="text-primary" />
                                    {t('MODERATION.KEYWORD_AI_TITLE', 'AI 词库候选')}
                                </h4>
                                <p className="mt-1 text-xs text-on-surface-variant">
                                    {t('MODERATION.KEYWORD_AI_DESC', '通过已保存的审核 provider 生成候选词。')}
                                </p>
                            </div>
                            <div className="flex items-center gap-3">
                                <TextInput
                                    type="number"
                                    min="1"
                                    max="200"
                                    value={configs.moderation_keyword_ai_max_candidates || '80'}
                                    onChange={e => handleChange('moderation_keyword_ai_max_candidates', e.target.value)}
                                    className="w-24 h-9"
                                    aria-label={t('MODERATION.KEYWORD_AI_MAX', '候选数量')}
                                />
                                <button
                                    type="button"
                                    onClick={generateKeywordCandidates}
                                    disabled={generatingKeywords}
                                    className="inline-flex h-9 items-center justify-center gap-2 rounded-control border border-primary/40 bg-primary/10 px-4 text-xs font-semibold text-primary hover:bg-primary/20 transition-colors disabled:opacity-60"
                                >
                                    {generatingKeywords ? <RotateCw size={14} className="animate-spin" /> : <Bot size={14} />}
                                    {generatingKeywords ? t('MODERATION.KEYWORD_AI_RUNNING', '生成中...') : t('MODERATION.KEYWORD_AI_BUTTON', '生成候选')}
                                </button>
                            </div>
                        </div>
                        
                        <TextInput
                            type="text"
                            value={keywordAIFocus}
                            onChange={e => setKeywordAIFocus(e.target.value)}
                            placeholder={t('MODERATION.KEYWORD_AI_FOCUS_PLACEHOLDER', '可选：补充本轮重点，例如“Claude Code 破甲”')}
                            className="w-full mb-4"
                        />
                        
                        {keywordCandidates.length > 0 && (
                            <div className="overflow-hidden rounded-overlay border border-outline-variant">
                                <div className="flex items-center justify-between gap-3 border-b border-outline-variant bg-surface-container px-4 py-3">
                                    <span className="text-sm font-semibold text-on-surface">
                                        {t('MODERATION.KEYWORD_AI_CANDIDATES', '候选词')} ({keywordCandidates.length})
                                    </span>
                                    <button
                                        type="button"
                                        onClick={mergeSelectedCandidates}
                                        className="inline-flex items-center gap-1.5 rounded-control bg-primary px-3 py-1.5 text-xs font-medium text-on-primary hover:bg-primary/90 transition-colors"
                                    >
                                        <ListChecks size={14} />
                                        {t('MODERATION.KEYWORD_AI_MERGE', '合并已选')}
                                    </button>
                                </div>
                                <div className="max-h-72 overflow-y-auto divide-y divide-outline-variant/50 bg-surface">
                                    {keywordCandidates.map((c, idx) => (
                                        <label key={`${c.keyword}-${idx}`} className="flex items-start gap-4 px-4 py-3 hover:bg-surface-container/50 cursor-pointer transition-colors">
                                            <div className="pt-0.5">
                                                <Switch checked={selectedCandidates.has(idx)} onChange={() => toggleCandidate(idx)} />
                                            </div>
                                            <div className="min-w-0 flex-1">
                                                <div className="flex flex-wrap items-center gap-2 mb-1">
                                                    <span className="font-mono text-sm text-on-surface break-all font-medium">{c.keyword}</span>
                                                    <span className="rounded-full bg-primary/10 px-2 py-0.5 text-[10px] font-semibold tracking-wide uppercase text-primary">{c.category || 'jailbreak'}</span>
                                                    <span className="rounded-full bg-warning/10 px-2 py-0.5 text-[10px] font-semibold tracking-wide uppercase text-warning">{c.severity || 'medium'}</span>
                                                </div>
                                                {c.reason && <p className="text-xs text-on-surface-variant leading-relaxed">{c.reason}</p>}
                                            </div>
                                        </label>
                                    ))}
                                </div>
                            </div>
                        )}
                    </div>
                </div>
            </FormRow.Group>

            <FormRow.Group
                title={
                    <span className="flex items-center gap-2 text-primary">
                        <ListChecks size={16} />
                        {t('MODERATION.SECTION_RISK_RULES', '组合规则与风险打分')}
                    </span>
                }
                sub={t('MODERATION.RISK_RULES_DESC', '用于承载 regex / combo / score_only 规则。block 直接拦截，model_review 升级到智能审核 provider 二审，score_only 只记录风控事件。')}
            >
                <div className="flex flex-col gap-4">
                    <textarea
                        id="mod-risk-rules"
                        rows={10}
                        value={riskRulesText}
                        onChange={e => setRiskRulesText(e.target.value)}
                        onBlur={flushRiskRules}
                        spellCheck={false}
                        className="w-full bg-surface-container border border-outline rounded-control px-4 py-3 text-on-surface font-mono text-sm outline-none focus:border-primary focus:ring-1 focus:ring-primary/20 transition-all"
                    />
                    <p className="text-xs text-on-surface-variant">
                        {t('MODERATION.RISK_RULES_HINT', '当前 {{count}} 条规则。修改后失焦会校验 JSON，点击页面底部「保存」后生效。', { count: riskRuleCount })}
                    </p>
                    
                    <div className="grid grid-cols-1 gap-3 md:grid-cols-3 mt-2">
                        <div className="rounded-control border border-outline-variant bg-surface/50 p-4 transition-colors hover:border-outline">
                            <span className="font-semibold text-sm text-on-surface">block</span>
                            <p className="mt-1.5 text-xs text-on-surface-variant leading-relaxed">{t('MODERATION.RISK_RULES_BLOCK_HINT', '极低误伤规则，命中后直接拒绝。')}</p>
                        </div>
                        <div className="rounded-control border border-outline-variant bg-surface/50 p-4 transition-colors hover:border-outline">
                            <span className="font-semibold text-sm text-on-surface">model_review</span>
                            <p className="mt-1.5 text-xs text-on-surface-variant leading-relaxed">{t('MODERATION.RISK_RULES_REVIEW_HINT', '高风险但需上下文判断，命中后走智能审核 provider 二审。')}</p>
                        </div>
                        <div className="rounded-control border border-outline-variant bg-surface/50 p-4 transition-colors hover:border-outline">
                            <span className="font-semibold text-sm text-on-surface">score_only</span>
                            <p className="mt-1.5 text-xs text-on-surface-variant leading-relaxed">{t('MODERATION.RISK_RULES_SCORE_HINT', '中风险信号，只写审计和累计风险。')}</p>
                        </div>
                    </div>
                </div>
            </FormRow.Group>
        </div>
    );
};

export default KeywordRulesEditor;