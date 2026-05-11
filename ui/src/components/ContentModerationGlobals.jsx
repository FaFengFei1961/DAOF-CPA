import React, { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Shield, AlertTriangle, RotateCw, Eye, EyeOff } from 'lucide-react';
import toast from 'react-hot-toast';
import { useConfirm } from '../context/ConfirmContext';

/**
 * 内容审核全局配置（per-ChannelModel 风控的全局共享层）
 *
 * fix CRITICAL R23 (codex 第二十三轮反馈)：admin 在这里配置的是"全平台共享"参数，
 * 真实的"哪个渠道哪个模型走哪种风控"在 ChannelManagement → 模型编辑里设置。
 *
 * 包括：
 *   - OpenAI Moderation API 凭证（key / endpoint / model / threshold）
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
const ContentModerationGlobals = ({ configs, handleChange }) => {
    const { t } = useTranslation();
    const confirm = useConfirm();
    const [showApiKey, setShowApiKey] = useState(false);
    const [showSecret, setShowSecret] = useState(false);

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

    // 当 configs 从后端刷新时同步 textarea
    React.useEffect(() => {
        setKeywordText(keywordList.join('\n'));
    }, [keywordList]);

    const flushKeywords = () => {
        const cleaned = Array.from(new Set(
            keywordText.split('\n').map(s => s.trim()).filter(Boolean)
        ));
        handleChange('moderation_keywords', JSON.stringify(cleaned));
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

            {/* ── OpenAI Moderation API 凭证 ────────────────────────────────── */}
            <section className="bg-surface-container border border-outline-variant rounded-2xl p-4 md:p-6 mb-6 shadow-sm">
                <h3 className="text-sm font-semibold text-on-surface flex items-center gap-2 mb-4 pb-3 border-b border-outline-variant/50">
                    <Shield size={16} className="text-primary" />
                    {t('MODERATION.SECTION_API', 'OpenAI Moderation API')}
                </h3>

                <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                    <div>
                        <label htmlFor="mod-api-key" className="block text-xs font-medium text-on-surface-variant mb-1.5">
                            {t('MODERATION.API_KEY', 'API Key')}
                        </label>
                        <div className="relative">
                            <input
                                id="mod-api-key"
                                type={showApiKey ? 'text' : 'password'}
                                autoComplete="off"
                                value={configs.moderation_openai_key || ''}
                                onChange={e => handleChange('moderation_openai_key', e.target.value)}
                                placeholder="sk-..."
                                className="w-full bg-surface-container-high border border-outline rounded-lg pl-3 pr-10 py-2 text-on-surface outline-none focus:border-primary"
                            />
                            <button
                                type="button"
                                onClick={() => setShowApiKey(s => !s)}
                                aria-label={showApiKey ? t('COMMON.HIDE', '隐藏') : t('COMMON.SHOW', '显示')}
                                className="absolute right-2 top-1/2 -translate-y-1/2 p-1 text-on-surface-variant hover:text-on-surface"
                            >
                                {showApiKey ? <EyeOff size={16} /> : <Eye size={16} />}
                            </button>
                        </div>
                    </div>
                    <div>
                        <label htmlFor="mod-endpoint" className="block text-xs font-medium text-on-surface-variant mb-1.5">
                            {t('MODERATION.ENDPOINT', 'Endpoint')}
                        </label>
                        <input
                            id="mod-endpoint"
                            type="text"
                            value={configs.moderation_openai_endpoint || ''}
                            onChange={e => handleChange('moderation_openai_endpoint', e.target.value)}
                            placeholder="https://api.openai.com/v1/moderations"
                            className="w-full bg-surface-container-high border border-outline rounded-lg px-3 py-2 text-on-surface outline-none focus:border-primary"
                        />
                    </div>
                    <div>
                        <label htmlFor="mod-model" className="block text-xs font-medium text-on-surface-variant mb-1.5">
                            {t('MODERATION.MODEL', '审核模型')}
                        </label>
                        <input
                            id="mod-model"
                            type="text"
                            value={configs.moderation_openai_model || ''}
                            onChange={e => handleChange('moderation_openai_model', e.target.value)}
                            placeholder="omni-moderation-latest"
                            className="w-full bg-surface-container-high border border-outline rounded-lg px-3 py-2 text-on-surface outline-none focus:border-primary"
                        />
                        <p className="text-[11px] text-on-surface-variant mt-1">
                            {t('MODERATION.MODEL_HINT', '推荐 omni-moderation-latest（多语言 + 多模态图片）')}
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
                            {t('MODERATION.THRESHOLD_HINT', '任一类别 score ≥ 阈值即判为命中。0.8 是 OpenAI 推荐起点；越严越易误伤合理 prompt。')}
                        </p>
                    </div>
                </div>
            </section>

            {/* ── 关键字词库 ───────────────────────────────────────────────── */}
            <section className="bg-surface-container border border-outline-variant rounded-2xl p-4 md:p-6 mb-6 shadow-sm">
                <h3 className="text-sm font-semibold text-on-surface flex items-center gap-2 mb-4 pb-3 border-b border-outline-variant/50">
                    <AlertTriangle size={16} className="text-amber-400" />
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
                    className="w-full bg-surface-container-high border border-outline rounded-lg px-3 py-2 text-on-surface font-mono text-xs outline-none focus:border-primary"
                />
                <p className="text-[11px] text-on-surface-variant mt-1">
                    {t('MODERATION.KEYWORDS_HINT', '当前 {{count}} 条关键字。修改后点页面底部的「保存」按钮生效。', { count: keywordList.length })}
                </p>
            </section>

            {/* ── 缓存参数 ────────────────────────────────────────────────── */}
            <section className="bg-surface-container border border-outline-variant rounded-2xl p-4 md:p-6 mb-6 shadow-sm">
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
                            className="w-full bg-surface-container-high border border-outline rounded-lg px-3 py-2 text-on-surface outline-none focus:border-primary"
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
                            className="w-full bg-surface-container-high border border-outline rounded-lg px-3 py-2 text-on-surface outline-none focus:border-primary"
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
                                className="w-full bg-surface-container-high border border-outline rounded-lg pl-3 pr-20 py-2 text-on-surface font-mono text-xs outline-none focus:border-primary"
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
                                    className="p-1 text-amber-400 hover:text-amber-300"
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
            <section className="bg-surface-container border border-outline-variant rounded-2xl p-4 md:p-6 mb-6 shadow-sm">
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
                            className="w-full bg-surface-container-high border border-outline rounded-lg px-3 py-2 text-on-surface outline-none focus:border-primary"
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
                            className="w-full bg-surface-container-high border border-outline rounded-lg px-3 py-2 text-on-surface outline-none focus:border-primary"
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
                            className="w-full bg-surface-container-high border border-outline rounded-lg px-3 py-2 text-on-surface outline-none focus:border-primary"
                        />
                    </div>
                </div>
                <p className="text-[11px] text-on-surface-variant mt-2">
                    {t('MODERATION.LIMITS_HINT', '超过 max_chars 直接拒绝；分块 chunk_chars × max_chunks 之内的 prompt 会按片审核（任一片命中即拒）。')}
                </p>
            </section>

            {/* ── 多模态图片策略 ───────────────────────────────────────────── */}
            <section className="bg-surface-container border border-outline-variant rounded-2xl p-4 md:p-6 mb-6 shadow-sm">
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
                            {t('MODERATION.IMAGE_POLICY_HINT', 'submit = 把 image_url 也送 omni-moderation-latest（推荐）；skip = 跳过图片不审；reject = 直接拒绝带图请求（最严，直连官方时推荐）')}
                        </span>
                    </label>
                    {/* fix MINOR R23-m5：补 focus 状态，键盘 Tab 用户能看到聚焦 */}
                    <select
                        id="mod-image-policy"
                        value={configs.moderation_image_policy || 'submit'}
                        onChange={e => handleChange('moderation_image_policy', e.target.value)}
                        className="bg-surface-container-high border border-outline text-on-surface rounded-lg px-4 py-2 outline-none text-sm w-full md:w-48 cursor-pointer hover:border-primary focus:border-primary focus:ring-2 focus:ring-primary/40"
                    >
                        <option value="submit">{t('MODERATION.IMAGE_SUBMIT', 'submit — 送 OpenAI 审核')}</option>
                        <option value="skip">{t('MODERATION.IMAGE_SKIP', 'skip — 跳过图片')}</option>
                        <option value="reject">{t('MODERATION.IMAGE_REJECT', 'reject — 直接拒绝')}</option>
                    </select>
                </div>
            </section>

            {/* ── 拒绝文案 ────────────────────────────────────────────────── */}
            <section className="bg-surface-container border border-outline-variant rounded-2xl p-4 md:p-6 mb-6 shadow-sm">
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
                            className="w-full bg-surface-container-high border border-outline rounded-lg px-3 py-2 text-on-surface outline-none focus:border-primary"
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
                            className="w-full bg-surface-container-high border border-outline rounded-lg px-3 py-2 text-on-surface outline-none focus:border-primary"
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
                            className="w-full bg-surface-container-high border border-outline rounded-lg px-3 py-2 text-on-surface outline-none focus:border-primary"
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
                            className="w-full bg-surface-container-high border border-outline rounded-lg px-3 py-2 text-on-surface outline-none focus:border-primary"
                        />
                    </div>
                </div>
            </section>

            {/* fix MAJOR R23-M11：保存按钮移交给 Settings.jsx 的全局 <SaveBar /> 统一渲染 */}
        </div>
    );
};

export default ContentModerationGlobals;
