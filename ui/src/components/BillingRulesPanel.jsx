import React, { useEffect, useMemo, useState } from 'react';
import { Activity, RefreshCw, Scale, ShieldCheck } from 'lucide-react';
import { useTranslation } from 'react-i18next';

const formatWeight = (n) => {
  const v = Number(n);
  if (!Number.isFinite(v)) return '1.00';
  return v.toFixed(v % 1 === 0 ? 0 : 2);
};

const humanModelLabel = (rule) => {
  if (rule?.label) return rule.label;
  return String(rule?.pattern || '*')
    .replaceAll('*', '')
    .replaceAll('gpt', 'GPT')
    .replaceAll('gemini', 'Gemini')
    .replaceAll('opus', 'Claude Opus')
    .replaceAll('sonnet', 'Claude Sonnet')
    .replaceAll('haiku', 'Claude Haiku')
    .trim() || '其他模型';
};

const fetchBillingRules = async () => {
  const res = await fetch('/api/billing/rules', { credentials: 'same-origin' });
  const json = await res.json();
  if (!res.ok || !json.success) {
    throw new Error(json.message || `HTTP ${res.status}`);
  }
  return json.data;
};

const BillingRulesPanel = ({ compact = false }) => {
  const { t } = useTranslation();
  const [rules, setRules] = useState(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  const load = async () => {
    setLoading(true);
    setError('');
    try {
      setRules(await fetchBillingRules());
    } catch (e) {
      setError(e?.message || String(e));
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    load();
  }, []);

  const modelWeights = rules?.model_weights || [];
  const healthMultipliers = rules?.health_multipliers || [];
  const fallbackRule = rules?.fallback || {};
  const version = rules?.version || '-';
  const visibleWeights = useMemo(
    () => (compact ? modelWeights.slice(0, 5) : modelWeights),
    [compact, modelWeights]
  );
  const normalHealth = (healthMultipliers.length ? healthMultipliers : [{ pattern: '*', weight: 1, reason: '默认无高峰加权' }])
    .every((r) => Number(r.weight || 1) === 1);

  return (
    <section className="rounded-overlay border border-outline-variant/50 bg-surface-container/40 p-4 space-y-4">
      <header className="flex flex-col sm:flex-row sm:items-start sm:justify-between gap-3">
        <div className="flex items-start gap-3">
          <div className="w-10 h-10 rounded-control bg-primary/10 flex items-center justify-center shrink-0">
            <Scale className="w-5 h-5 text-primary" />
          </div>
          <div>
            <div className="flex items-center gap-2 flex-wrap">
              <h2 className="text-base font-semibold text-on-surface">
                {t('BILLING_RULES.TITLE', '额度怎么扣？')}
              </h2>
              <span className="text-[11px] px-2 py-0.5 rounded-full bg-on-surface/[0.06] text-on-surface-variant font-mono">
                {t('BILLING_RULES.VERSION', '规则版本')} {version}
              </span>
            </div>
            <p className="text-sm text-on-surface-variant mt-1 max-w-3xl">
              {t('BILLING_RULES.SUBTITLE', '不同模型消耗额度不同：轻量模型扣得少，Opus / Thinking / 高推理模型扣得多。平台不会在后台偷偷把你请求的模型换成别的模型。')}
            </p>
          </div>
        </div>
        <button
          type="button"
          onClick={load}
          className="inline-flex items-center justify-center gap-1.5 h-9 px-3 rounded-control border border-outline-variant text-sm text-on-surface-variant hover:text-on-surface hover:bg-on-surface/[0.04]"
        >
          <RefreshCw className={`w-4 h-4 ${loading ? 'animate-spin' : ''}`} />
          {t('COMMON.REFRESH', '刷新')}
        </button>
      </header>

      {error && (
        <div className="rounded-control border border-error/30 bg-error/10 px-3 py-2 text-sm text-error">
          {t('BILLING_RULES.LOAD_FAIL', '规则加载失败')}：{error}
        </div>
      )}

      {!error && (
        <>
          <div className="grid grid-cols-1 md:grid-cols-4 gap-2">
            <RuleStep label={t('BILLING_RULES.STEP_BASE', '先算基础成本')} value={t('BILLING_RULES.STEP_BASE_VALUE', '按公开模型单价折算')} />
            <RuleStep label={t('BILLING_RULES.STEP_MODEL', '再看模型消耗')} value={t('BILLING_RULES.STEP_MODEL_VALUE', '轻量少扣，重型多扣')} />
            <RuleStep label={t('BILLING_RULES.STEP_PEAK', '繁忙时段')} value={normalHealth ? t('BILLING_RULES.NO_PEAK', '当前没有加价') : t('BILLING_RULES.PEAK_ACTIVE', '按公开系数调整')} />
            <RuleStep label={t('BILLING_RULES.STEP_FINAL', '实际扣减额度')} value={t('BILLING_RULES.STEP_FINAL_VALUE', '写入账单明细')} strong />
          </div>

          <div className="grid grid-cols-1 xl:grid-cols-[minmax(0,1fr)_320px] gap-4">
            <div className="overflow-hidden rounded-control border border-outline-variant/40 bg-surface">
              <div className="px-3 py-2 border-b border-outline-variant/40 text-sm font-semibold text-on-surface flex items-center gap-2">
                <Activity className="w-4 h-4 text-primary" />
                {t('BILLING_RULES.MODEL_TABLE', '模型消耗系数')}
              </div>
              <div className="overflow-x-auto">
                <table className="w-full text-sm">
                  <thead className="bg-surface-container-high text-xs text-on-surface-variant">
                    <tr>
                      <th className="text-left px-3 py-2 font-medium">{t('BILLING_RULES.MODEL_TYPE', '模型类型')}</th>
                      <th className="text-right px-3 py-2 font-medium">{t('BILLING_RULES.NORMAL_USE', '普通使用')}</th>
                      <th className="text-right px-3 py-2 font-medium">{t('BILLING_RULES.THINKING_USE', 'Thinking 使用')}</th>
                      <th className="text-left px-3 py-2 font-medium">{t('BILLING_RULES.HOW_TO_READ', '怎么理解')}</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-outline-variant/30">
                    {loading && (
                      <tr>
                        <td colSpan="4" className="px-3 py-6 text-center text-on-surface-variant">
                          {t('COMMON.LOADING', '加载中…')}
                        </td>
                      </tr>
                    )}
                    {!loading && visibleWeights.map((r, idx) => (
                      <tr key={`${r.pattern}-${idx}`} className="hover:bg-on-surface/[0.02]">
                        <td className="px-3 py-2 text-on-surface">
                          <div className="font-medium">{humanModelLabel(r)}</div>
                          <div className="text-[11px] text-on-surface-variant font-mono mt-0.5">{r.pattern}</div>
                        </td>
                        <td className="px-3 py-2 text-right font-mono text-primary">×{formatWeight(r.weight)}</td>
                        <td className="px-3 py-2 text-right font-mono text-warning">
                          {r.thinking_weight ? `×${formatWeight(r.thinking_weight)}` : '-'}
                        </td>
                        <td className="px-3 py-2 text-xs text-on-surface-variant">{r.reason || r.label || '-'}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              {compact && modelWeights.length > visibleWeights.length && (
                <div className="px-3 py-2 border-t border-outline-variant/40 text-xs text-on-surface-variant">
                  {t('BILLING_RULES.OPEN_PRICING_FULL', '完整规则可在左侧“定价”页面查看。')}
                </div>
              )}
            </div>

            <div className="space-y-3">
              <InfoBox
                icon={ShieldCheck}
                title={t('BILLING_RULES.FALLBACK_TITLE', '不会偷偷换模型')}
                lines={[
                  t('BILLING_RULES.FALLBACK_DEFAULT_HUMAN', '默认不启用模型替换。'),
                  t('BILLING_RULES.FALLBACK_AUDIT_HUMAN', '账单会记录“你请求的模型”和“实际服务的模型”。'),
                  t('BILLING_RULES.FALLBACK_OPTIN_HUMAN', '只有你主动允许 fallback 时，平台才可以按规则切换可用模型。'),
                ]}
              />
              <InfoBox
                title={t('BILLING_RULES.THINKING_TITLE', 'Thinking 怎么判定')}
                lines={[
                  t('BILLING_RULES.THINKING_ENABLED_HUMAN', '请求明确启用 thinking / reasoning，或上游返回 reasoning tokens，才按 Thinking 使用扣减。'),
                  t('BILLING_RULES.THINKING_DISABLED_HUMAN', '仅出现 disabled / none / off / 空 thinking 对象，不算 Thinking 使用。'),
                  t('BILLING_RULES.THINKING_AUDIT_HUMAN', '请求事件明细会显示思考 tokens 和本次实际模型倍率。'),
                ]}
              />
              <InfoBox
                title={t('BILLING_RULES.HEALTH_TITLE', '繁忙时段系数')}
                lines={(healthMultipliers.length ? healthMultipliers : [{ pattern: '*', weight: 1, reason: '默认无高峰加权' }]).map(
                  (r) => Number(r.weight || 1) === 1
                    ? t('BILLING_RULES.HEALTH_NORMAL', '当前没有繁忙时段加价。')
                    : `${r.pattern}: ×${formatWeight(r.weight)}${r.reason ? ` · ${r.reason}` : ''}`
                )}
              />
            </div>
          </div>

          <details className="rounded-control border border-outline-variant/40 bg-surface px-3 py-2">
            <summary className="cursor-pointer text-sm font-medium text-on-surface">
              {t('BILLING_RULES.TECH_DETAILS', '技术字段对照')}
            </summary>
            <div className="mt-2 grid grid-cols-1 md:grid-cols-2 gap-2 text-xs text-on-surface-variant">
              <TechLine name="raw_cost" text={t('BILLING_RULES.TECH_RAW', '公开模型单价折算出的 API 等值成本。')} />
              <TechLine name="model_weight" text={t('BILLING_RULES.TECH_WEIGHT', '该模型对应的消耗系数。')} />
              <TechLine name="health_multiplier" text={t('BILLING_RULES.TECH_HEALTH', '繁忙时段或健康度调整系数。')} />
              <TechLine name="charged_cost" text={t('BILLING_RULES.TECH_CHARGED', '最终从套餐额度里扣减的数值。')} />
              <TechLine name="X-Allow-Fallback: true" text={fallbackRule.rule || t('BILLING_RULES.TECH_FALLBACK', '只有显式允许 fallback 时才可能改变 served_model。')} />
            </div>
          </details>
        </>
      )}
    </section>
  );
};

const RuleStep = ({ label, value, strong = false }) => (
  <div className={`rounded-control border border-outline-variant/40 p-3 ${strong ? 'bg-primary/10' : 'bg-surface'}`}>
    <div className="text-xs text-on-surface-variant">{label}</div>
    <div className={`mt-1 font-mono text-sm ${strong ? 'text-primary font-semibold' : 'text-on-surface'}`}>{value}</div>
  </div>
);

const InfoBox = ({ icon: Icon, title, lines }) => (
  <div className="rounded-control border border-outline-variant/40 bg-surface p-3">
    <div className="flex items-center gap-2 text-sm font-semibold text-on-surface mb-2">
      {Icon && <Icon className="w-4 h-4 text-primary" />}
      {title}
    </div>
    <ul className="space-y-1">
      {lines.map((line, idx) => (
        <li key={idx} className="text-xs text-on-surface-variant leading-relaxed break-words">
          {line}
        </li>
      ))}
    </ul>
  </div>
);

const TechLine = ({ name, text }) => (
  <div className="rounded-control bg-on-surface/[0.03] px-2 py-1.5">
    <span className="font-mono text-primary">{name}</span>
    <span className="ml-2">{text}</span>
  </div>
);

export default BillingRulesPanel;
