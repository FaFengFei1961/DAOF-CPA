import React, { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { RotateCw, PlugZap, CheckCircle2, AlertCircle, XCircle } from 'lucide-react';
import toast from 'react-hot-toast';
import { authFetch } from '../../utils/authFetch';
import FormRow from '../ui/FormRow';

const TestPlayground = () => {
    const { t } = useTranslation();
    const [testing, setTesting] = useState(false);
    const [testResult, setTestResult] = useState(null);

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
        <FormRow.Group
            title={t('MODERATION.TEST_SANDBOX', '内容审核测试沙盒')}
            sub={t('MODERATION.TEST_SANDBOX_DESC', '使用当前已保存的配置，模拟发送测试文本请求审核。')}
            className="mb-6"
        >
            <div className="flex flex-col gap-4">
                <button
                    type="button"
                    onClick={runModerationTest}
                    disabled={testing}
                    className="inline-flex w-fit items-center justify-center gap-2 rounded-control border border-primary/40 bg-primary/10 px-4 py-2 text-sm font-semibold text-primary hover:bg-primary/15 disabled:cursor-not-allowed disabled:opacity-60 transition-colors"
                >
                    {testing ? <RotateCw size={16} className="animate-spin" /> : <PlugZap size={16} />}
                    {testing ? t('MODERATION.TESTING', '测试中...') : t('MODERATION.TEST_BUTTON', '测试已保存配置')}
                </button>

                {testResult && (
                    <div className={`rounded-overlay border px-4 py-4 ${testTone}`}>
                        <div className="flex items-start gap-3">
                            <TestIcon size={20} className="mt-0.5 shrink-0" />
                            <div className="min-w-0 flex-1">
                                <div className="text-sm font-semibold break-words mb-3">{getTestMessage(testResult)}</div>
                                <div className="grid grid-cols-1 gap-x-4 gap-y-2 text-xs md:grid-cols-2 xl:grid-cols-4 bg-surface/30 p-3 rounded-control border border-outline-variant/30">
                                    <span className="min-w-0 break-words">
                                        <span className="text-on-surface-variant mr-1">{t('MODERATION.TEST_PROVIDER', '供应商')}:</span>
                                        {testResult.provider || 'cliproxy_model'}
                                    </span>
                                    <span className="min-w-0 break-words">
                                        <span className="text-on-surface-variant mr-1">{t('MODERATION.TEST_MODEL', '模型')}:</span>
                                        {testResult.model || '-'}
                                    </span>
                                    <span className="min-w-0 break-words">
                                        <span className="text-on-surface-variant mr-1">{t('MODERATION.TEST_ENDPOINT', 'Endpoint')}:</span>
                                        {testResult.endpoint || '-'}
                                    </span>
                                    <span>
                                        <span className="text-on-surface-variant mr-1">{t('MODERATION.TEST_LATENCY', '延迟')}:</span>
                                        {Number.isFinite(testResult.latency_ms) ? `${testResult.latency_ms} ms` : '-'}
                                    </span>
                                    {testResult.upstream_status && (
                                        <span>
                                            <span className="text-on-surface-variant mr-1">{t('MODERATION.TEST_UPSTREAM_STATUS', '上游状态')}:</span>
                                            HTTP {testResult.upstream_status}
                                        </span>
                                    )}
                                    {testResult.upstream_error_type && (
                                        <span className="min-w-0 break-words">
                                            <span className="text-on-surface-variant mr-1">{t('MODERATION.TEST_UPSTREAM_ERROR', '上游错误')}:</span>
                                            {testResult.upstream_error_type}{testResult.upstream_error_code ? ` / ${testResult.upstream_error_code}` : ''}
                                        </span>
                                    )}
                                    {testResult.retry_after && (
                                        <span>
                                            <span className="text-on-surface-variant mr-1">{t('MODERATION.TEST_RETRY_AFTER', '建议等待')}:</span>
                                            {testResult.retry_after}
                                        </span>
                                    )}
                                    {rateLimit['x-ratelimit-remaining-requests'] && (
                                        <span>
                                            <span className="text-on-surface-variant mr-1">{t('MODERATION.TEST_RL_REQ', '请求剩余')}:</span>
                                            {rateLimit['x-ratelimit-remaining-requests']} / {rateLimit['x-ratelimit-limit-requests'] || '-'}
                                        </span>
                                    )}
                                    {rateLimit['x-ratelimit-remaining-tokens'] && (
                                        <span>
                                            <span className="text-on-surface-variant mr-1">{t('MODERATION.TEST_RL_TOKENS', 'Token 剩余')}:</span>
                                            {rateLimit['x-ratelimit-remaining-tokens']} / {rateLimit['x-ratelimit-limit-tokens'] || '-'}
                                        </span>
                                    )}
                                </div>
                            </div>
                        </div>
                    </div>
                )}
            </div>
        </FormRow.Group>
    );
};

export default TestPlayground;