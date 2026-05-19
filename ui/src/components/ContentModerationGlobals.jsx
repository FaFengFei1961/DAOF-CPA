import React, { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Eye, EyeOff, RotateCw } from 'lucide-react';
import toast from 'react-hot-toast';
import { useConfirm } from '../context/ConfirmContext';
import TextInput from './ui/TextInput';
import Select from './ui/Select';
import FormRow from './ui/FormRow';

// Subcomponents
import UpstreamModelPoolEditor from './moderation/UpstreamModelPoolEditor';
import KeywordRulesEditor from './moderation/KeywordRulesEditor';
import TestPlayground from './moderation/TestPlayground';
import DryRunPanel from './moderation/DryRunPanel';

const ContentModerationGlobals = ({ configs, handleChange }) => {
    const { t } = useTranslation();
    const confirm = useConfirm();
    const [showSecret, setShowSecret] = useState(false);

    const resetHmacSecret = async () => {
        const ok = await confirm(t('MODERATION.SECRET_RESET_CONFIRM', '重置 HMAC 密钥会让全部审核缓存立即失效，确认继续？'));
        if (!ok) return;
        handleChange('moderation_cache_secret', '');
        toast.success(t('MODERATION.SECRET_RESET_DONE', '已清空，请点击「保存」让后端在下次启动时重新生成 256bit 密钥'));
    };

    return (
        <div className="w-full">
            <UpstreamModelPoolEditor configs={configs} handleChange={handleChange} />
            <TestPlayground />
            <KeywordRulesEditor configs={configs} handleChange={handleChange} />
            <DryRunPanel configs={configs} handleChange={handleChange} />

            <FormRow.Group
                title={t('MODERATION.SECTION_CACHE', '缓存与防侧信道')}
                className="mb-6"
            >
                <FormRow
                    label={t('MODERATION.CACHE_TTL', '缓存 TTL (秒)')}
                    htmlFor="mod-cache-ttl"
                >
                    <TextInput
                        id="mod-cache-ttl"
                        type="number"
                        min="0"
                        value={configs.moderation_cache_ttl_sec || ''}
                        onChange={e => handleChange('moderation_cache_ttl_sec', e.target.value)}
                        placeholder="300"
                        className="w-full"
                    />
                </FormRow>

                <FormRow
                    label={t('MODERATION.CACHE_MAX', 'LRU 最大条目数')}
                    htmlFor="mod-cache-max"
                >
                    <TextInput
                        id="mod-cache-max"
                        type="number"
                        min="100"
                        value={configs.moderation_cache_max_entries || ''}
                        onChange={e => handleChange('moderation_cache_max_entries', e.target.value)}
                        placeholder="10000"
                        className="w-full"
                    />
                </FormRow>

                <FormRow
                    label={t('MODERATION.HMAC_SECRET', 'HMAC 缓存密钥（防侧信道）')}
                    hint={t('MODERATION.HMAC_HINT', '重置 = 让全部审核缓存立即失效（防长效侧信道猜测）')}
                    htmlFor="mod-secret"
                    last
                >
                    <div className="relative w-full">
                        <TextInput
                            id="mod-secret"
                            type={showSecret ? 'text' : 'password'}
                            value={configs.moderation_cache_secret || ''}
                            onChange={e => handleChange('moderation_cache_secret', e.target.value)}
                            placeholder={t('MODERATION.HMAC_AUTO', '留空让后端首次启动自动生成 256bit 随机密钥')}
                            className="w-full pr-20 font-mono"
                        />
                        <div className="absolute right-2 top-1/2 -translate-y-1/2 flex gap-1">
                            <button
                                type="button"
                                onClick={() => setShowSecret(s => !s)}
                                aria-label={showSecret ? t('COMMON.HIDE', '隐藏') : t('COMMON.SHOW', '显示')}
                                className="p-1.5 text-on-surface-variant hover:text-on-surface rounded-control hover:bg-surface-container"
                            >
                                {showSecret ? <EyeOff size={16} /> : <Eye size={16} />}
                            </button>
                            <button
                                type="button"
                                onClick={resetHmacSecret}
                                aria-label={t('MODERATION.HMAC_RESET', '重置')}
                                className="p-1.5 text-warning hover:text-warning rounded-control hover:bg-warning/10"
                            >
                                <RotateCw size={16} />
                            </button>
                        </div>
                    </div>
                </FormRow>
            </FormRow.Group>

            <FormRow.Group
                title={t('MODERATION.SECTION_LIMITS', '长 Prompt 限制')}
                sub={t('MODERATION.LIMITS_HINT', '普通模型超过 max_chars 直接拒绝；长上下文模型按 max_context_length 自动放宽，并抽样若干分布块送智能审核。')}
                className="mb-6"
            >
                <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-x-6 gap-y-4 pt-2">
                    {[
                        ['moderation_max_chars', t('MODERATION.MAX_CHARS', '单次最大字符数 (rune)'), '262144'],
                        ['moderation_chunk_chars', t('MODERATION.CHUNK_CHARS', '分块大小 (rune)'), '28672'],
                        ['moderation_max_chunks', t('MODERATION.MAX_CHUNKS', '最大分块数'), '8'],
                        ['moderation_long_context_min_tokens', t('MODERATION.LONG_CONTEXT_MIN_TOKENS', '长上下文阈值 tokens'), '800000'],
                        ['moderation_long_context_max_chars', t('MODERATION.LONG_CONTEXT_MAX_CHARS', '长上下文最大字符数'), '4194304'],
                        ['moderation_long_context_max_chunks', t('MODERATION.LONG_CONTEXT_MAX_CHUNKS', '长上下文抽样块数'), '12'],
                    ].map(([key, label, placeholder]) => (
                        <div key={key}>
                            <label htmlFor={`mod-${key}`} className="block text-sm font-medium text-on-surface mb-1.5">
                                {label}
                            </label>
                            <TextInput
                                id={`mod-${key}`}
                                type="number"
                                min="0"
                                value={configs[key] || ''}
                                onChange={e => handleChange(key, e.target.value)}
                                placeholder={placeholder}
                                className="w-full"
                            />
                        </div>
                    ))}
                </div>
            </FormRow.Group>

            <FormRow.Group
                title={t('MODERATION.SECTION_IMAGE', '多模态图片策略')}
                className="mb-6"
            >
                <FormRow
                    label={t('MODERATION.IMAGE_POLICY', 'image_url 处理')}
                    hint={t('MODERATION.IMAGE_POLICY_HINT', '智能审核第一版不直接审核外部 image_url。GPT 等多模态模型推荐 skip；高风险直连模型可改为 reject。')}
                    htmlFor="mod-image-policy"
                    last
                >
                    <Select
                        id="mod-image-policy"
                        value={configs.moderation_image_policy || 'skip'}
                        onChange={e => handleChange('moderation_image_policy', e.target.value)}
                        className="w-full"
                        options={[
                            {value: 'submit', label: t('MODERATION.IMAGE_SUBMIT', 'submit — 预留，当前按审核不可达处理')}, 
                            {value: 'skip', label: t('MODERATION.IMAGE_SKIP', 'skip — 跳过图片（推荐多模态模型）')},
                            {value: 'reject', label: t('MODERATION.IMAGE_REJECT', 'reject — 直接拒绝（最保守）')}
                        ]} 
                    />
                </FormRow>
            </FormRow.Group>

            <FormRow.Group
                title={t('MODERATION.SECTION_MESSAGES', '拒绝文案（按 Accept-Language 自动选）')}
                className="mb-6"
            >
                <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
                    <div>
                        <label htmlFor="mod-block-zh" className="block text-sm font-medium text-on-surface mb-1.5">
                            {t('MODERATION.BLOCK_ZH', '违规拒绝（中文）')}
                        </label>
                        <textarea
                            id="mod-block-zh"
                            rows={3}
                            value={configs.moderation_block_message_zh || ''}
                            onChange={e => handleChange('moderation_block_message_zh', e.target.value)}
                            className="w-full bg-surface-container border border-outline rounded-control px-3 py-2 text-on-surface outline-none focus:border-primary transition-colors"
                        />
                    </div>
                    <div>
                        <label htmlFor="mod-block-en" className="block text-sm font-medium text-on-surface mb-1.5">
                            {t('MODERATION.BLOCK_EN', '违规拒绝（英文）')}
                        </label>
                        <textarea
                            id="mod-block-en"
                            rows={3}
                            value={configs.moderation_block_message_en || ''}
                            onChange={e => handleChange('moderation_block_message_en', e.target.value)}
                            className="w-full bg-surface-container border border-outline rounded-control px-3 py-2 text-on-surface outline-none focus:border-primary transition-colors"
                        />
                    </div>
                    <div>
                        <label htmlFor="mod-unavail-zh" className="block text-sm font-medium text-on-surface mb-1.5">
                            {t('MODERATION.UNAVAIL_ZH', '审核不可达（中文）')}
                        </label>
                        <textarea
                            id="mod-unavail-zh"
                            rows={3}
                            value={configs.moderation_unavailable_message_zh || ''}
                            onChange={e => handleChange('moderation_unavailable_message_zh', e.target.value)}
                            className="w-full bg-surface-container border border-outline rounded-control px-3 py-2 text-on-surface outline-none focus:border-primary transition-colors"
                        />
                    </div>
                    <div>
                        <label htmlFor="mod-unavail-en" className="block text-sm font-medium text-on-surface mb-1.5">
                            {t('MODERATION.UNAVAIL_EN', '审核不可达（英文）')}
                        </label>
                        <textarea
                            id="mod-unavail-en"
                            rows={3}
                            value={configs.moderation_unavailable_message_en || ''}
                            onChange={e => handleChange('moderation_unavailable_message_en', e.target.value)}
                            className="w-full bg-surface-container border border-outline rounded-control px-3 py-2 text-on-surface outline-none focus:border-primary transition-colors"
                        />
                    </div>
                </div>
            </FormRow.Group>
        </div>
    );
};

export default ContentModerationGlobals;
