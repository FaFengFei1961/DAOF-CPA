import React from 'react';
import { useTranslation } from 'react-i18next';
import { Shield } from 'lucide-react';
import TextInput from '../ui/TextInput';
import FormRow from '../ui/FormRow';

const UpstreamModelPoolEditor = ({ configs, handleChange }) => {
    const { t } = useTranslation();
    const threshold = parseFloat(configs.moderation_threshold || '0.8');
    const moderationModelKey = 'moderation_cliproxy_model';
    const moderationModelFallback = 'gpt-5.4-mini';

    return (
        <FormRow.Group
            title={
                <span className="flex items-center gap-2 text-primary">
                    <Shield size={16} />
                    {t('MODERATION.SECTION_API', '智能审核 Provider')}
                </span>
            }
            sub={t('MODERATION.TEST_SAVED_HINT', '审核统一走上游模型池。刚改过配置请先点击页面底部「保存」。')}
            className="mb-6"
        >
            <FormRow
                label={t('MODERATION.PROVIDER', '审核供应商')}
                hint={t('MODERATION.PROVIDER_HINT', '审核统一走上游模型池，优先复用同地址 cliproxy 渠道的 API key。')}
            >
                <div className="w-full rounded-control border border-outline bg-surface-container-high px-3 py-2 text-on-surface text-sm">
                    {t('MODERATION.PROVIDER_CLIPROXY_MODEL', '上游模型池')}
                </div>
            </FormRow>

            <FormRow
                label={t('MODERATION.MODEL', '审核模型')}
                hint={t('MODERATION.MODEL_HINT', '推荐使用 gpt-5.4-mini 做默认二审；也可以换成 上游模型池里额度更宽裕的模型。')}
                htmlFor="mod-model"
            >
                <TextInput
                    id="mod-model"
                    type="text"
                    value={configs[moderationModelKey] || ''}
                    onChange={e => handleChange(moderationModelKey, e.target.value)}
                    placeholder={moderationModelFallback}
                    className="w-full"
                />
            </FormRow>

            <FormRow
                label={<span className="flex items-center gap-1">{t('MODERATION.THRESHOLD', '命中阈值')}: <span className="font-mono text-primary ml-1">{threshold.toFixed(2)}</span></span>}
                hint={t('MODERATION.THRESHOLD_HINT', '分类器 confidence 或上游 safety score ≥ 阈值即判为命中。0.8 是内测阶段的保守起点。')}
                htmlFor="mod-threshold"
            >
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
                    className="w-full accent-primary mt-2"
                />
            </FormRow>

            <FormRow
                label={t('MODERATION.API_TIMEOUT', '审核超时（秒）')}
                hint={t('MODERATION.API_TIMEOUT_HINT', '上游模型池二审的总等待时间。gpt-5.4-mini 实测常见 4-6 秒，默认 15 秒更稳。')}
                htmlFor="mod-api-timeout"
                last
            >
                <TextInput
                    id="mod-api-timeout"
                    type="number"
                    min="1"
                    max="120"
                    value={configs.moderation_api_timeout_seconds || ''}
                    onChange={e => handleChange('moderation_api_timeout_seconds', e.target.value)}
                    placeholder="15"
                    className="w-full"
                />
            </FormRow>
        </FormRow.Group>
    );
};

export default UpstreamModelPoolEditor;
