import React, { useEffect, useMemo, useState } from 'react';
import { History, RefreshCw } from 'lucide-react';
import { useTranslation } from 'react-i18next';

const formatWeight = (n) => {
  const v = Number(n);
  if (!Number.isFinite(v)) return '1.00';
  return v.toFixed(v % 1 === 0 ? 0 : 2);
};

const humanModelLabel = (rule, t) => {
  if (rule?.label) return rule.label;
  return String(rule?.pattern || '*')
    .replaceAll('*', '')
    .replaceAll('gpt', 'GPT')
    .replaceAll('gemini', 'Gemini')
    .replaceAll('opus', 'Claude Opus')
    .replaceAll('sonnet', 'Claude Sonnet')
    .replaceAll('haiku', 'Claude Haiku')
    .trim() || t('BILLING_RULES.OTHER_MODEL', '其他模型');
};

const fetchBillingRules = async () => {
  const res = await fetch('/api/billing/rules', { credentials: 'same-origin' });
  const json = await res.json();
  if (!res.ok || !json.success) {
    throw new Error(json.message || `HTTP ${res.status}`);
  }
  return json.data;
};

const fetchBillingRuleHistory = async () => {
  const res = await fetch('/api/billing/rules/history?limit=30', { credentials: 'same-origin' });
  const json = await res.json();
  if (!res.ok || !json.success) {
    throw new Error(json.message || `HTTP ${res.status}`);
  }
  return json.data || [];
};

const formatRevisionTime = (value) => {
  if (!value) return '-';
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return String(value);
  return d.toLocaleString();
};

const BillingRulesPanel = ({ compact = false }) => {
  const { t } = useTranslation();
  const [rules, setRules] = useState(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [historyOpen, setHistoryOpen] = useState(false);
  const [historyLoading, setHistoryLoading] = useState(false);
  const [historyError, setHistoryError] = useState('');
  const [historyRows, setHistoryRows] = useState([]);

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

  useEffect(() => { load(); }, []);

  const loadHistory = async () => {
    setHistoryLoading(true);
    setHistoryError('');
    try {
      setHistoryRows(await fetchBillingRuleHistory());
    } catch (e) {
      setHistoryError(e?.message || String(e));
    } finally {
      setHistoryLoading(false);
    }
  };

  const toggleHistory = async () => {
    const next = !historyOpen;
    setHistoryOpen(next);
    if (next && historyRows.length === 0 && !historyLoading) {
      await loadHistory();
    }
  };

  const modelWeights = rules?.model_weights || [];
  const healthMultipliers = rules?.health_multipliers || [];
  // fix P2（codex review verify-1）：subscription/balance/fallback 元数据由后端 i18n bullet 表达，
  // 此处不再 destructure 占用 lint warning（no-unused-vars）。如需展示后端字段，使用 rules?.subscription 等内联访问。
  const version = rules?.version || '-';
  const effectiveSince = rules?.effective_since || '';

  const visibleWeights = useMemo(
    () => (compact ? modelWeights.slice(0, 5) : modelWeights),
    [compact, modelWeights]
  );
  const visibleHealth = (healthMultipliers.length ? healthMultipliers : [{ pattern: '*', weight: 1 }]);
  const peakActive = visibleHealth.some((r) => Number(r.weight || 1) !== 1);

  return (
    <section className="rounded-overlay border border-outline-variant/60 bg-surface p-6 space-y-6">
      {/* 顶部 header — 版本号 + 生效日 + 刷新 */}
      <header className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between pb-4 border-b border-outline-variant/40">
        <div className="min-w-0">
          <h2 className="text-lg font-semibold text-on-surface tracking-tight">
            {t('BILLING_RULES.PAGE_TITLE', '计费规则')}
          </h2>
          <p className="text-sm text-on-surface-variant mt-1 max-w-2xl">
            {t('BILLING_RULES.PAGE_SUB', '平台对每次请求如何计费的公开承诺。账单和审计同时按此口径记录。')}
          </p>
        </div>
        <div className="flex flex-col items-start sm:items-end gap-1">
          <div className="flex items-center gap-2">
            <span className="text-[11px] uppercase tracking-wider text-on-surface-variant/80">
              {t('BILLING_RULES.VERSION_LABEL', '规则版本')}
            </span>
            <span className="font-mono text-xs px-2 py-0.5 rounded-control bg-on-surface/[0.06] text-on-surface">
              {version}
            </span>
          </div>
          {effectiveSince && (
            <div className="text-[11px] text-on-surface-variant">
              {t('BILLING_RULES.EFFECTIVE_SINCE', { defaultValue: '自 {{date}} 起生效', date: effectiveSince })}
            </div>
          )}
          {!compact && (
            <button
              type="button"
              onClick={toggleHistory}
              className="mt-1 inline-flex items-center gap-1.5 h-8 px-2.5 rounded-control border border-outline-variant text-xs text-on-surface-variant hover:text-on-surface hover:bg-on-surface/[0.04]"
            >
              <History className="w-3.5 h-3.5" />
              {t('BILLING_RULES.HISTORY_BUTTON', '历史版本')}
            </button>
          )}
          <button
            type="button"
            onClick={load}
            className="inline-flex items-center gap-1.5 h-8 px-2.5 rounded-control border border-outline-variant text-xs text-on-surface-variant hover:text-on-surface hover:bg-on-surface/[0.04]"
          >
            <RefreshCw className={`w-3.5 h-3.5 ${loading ? 'animate-spin' : ''}`} />
            {t('BILLING_RULES.REFRESH', '刷新')}
          </button>
        </div>
      </header>

      {error && (
        <div className="rounded-control border border-error/30 bg-error/10 px-3 py-2 text-sm text-error">
          {t('BILLING_RULES.LOAD_FAIL', '计费规则加载失败')}: {error}
        </div>
      )}

      {!error && (
        <>
          {historyOpen && !compact && (
            <div className="rounded-control border border-outline-variant/40 bg-surface-container/30 overflow-hidden">
              <div className="px-4 py-2.5 bg-surface-container-highest border-b border-outline-variant/40 flex items-center justify-between gap-3 flex-wrap">
                <div>
                  <h3 className="text-sm font-semibold text-on-surface">
                    {t('BILLING_RULES.HISTORY_TITLE', '规则历史版本')}
                  </h3>
                  <p className="text-[11px] text-on-surface-variant mt-0.5">
                    {t('BILLING_RULES.HISTORY_SUB', '每次管理员发布都会保存一份快照，历史记录不会随当前规则改动而变化。')}
                  </p>
                </div>
                <button
                  type="button"
                  onClick={loadHistory}
                  disabled={historyLoading}
                  className="inline-flex items-center gap-1.5 h-8 px-2.5 rounded-control border border-outline-variant text-xs text-on-surface-variant hover:text-on-surface hover:bg-on-surface/[0.04] disabled:opacity-50"
                >
                  <RefreshCw className={`w-3.5 h-3.5 ${historyLoading ? 'animate-spin' : ''}`} />
                  {t('BILLING_RULES.HISTORY_RELOAD', '刷新历史')}
                </button>
              </div>
              {historyError && (
                <div className="px-4 py-3 text-sm text-error bg-error/10 border-b border-error/20">
                  {t('BILLING_RULES.HISTORY_LOAD_FAIL', '历史版本加载失败')}: {historyError}
                </div>
              )}
              {historyLoading && historyRows.length === 0 && (
                <div className="px-4 py-6 text-center text-sm text-on-surface-variant">
                  {t('BILLING_RULES.LOADING', '加载中…')}
                </div>
              )}
              {!historyLoading && !historyError && historyRows.length === 0 && (
                <div className="px-4 py-6 text-center text-sm text-on-surface-variant">
                  {t('BILLING_RULES.HISTORY_EMPTY', '暂无历史版本')}
                </div>
              )}
              {historyRows.length > 0 && (
                <div className="divide-y divide-outline-variant/30">
                  {historyRows.map((rev) => (
                    <details key={rev.id} className="group">
                      <summary className="cursor-pointer list-none px-4 py-3 hover:bg-on-surface/[0.03]">
                        <div className="flex items-center justify-between gap-3 flex-wrap">
                          <div className="min-w-0">
                            <div className="font-mono text-xs text-on-surface truncate">{rev.version || '-'}</div>
                            <div className="text-[11px] text-on-surface-variant mt-0.5">
                              {t('BILLING_RULES.HISTORY_CREATED_AT', '发布时间')}: {formatRevisionTime(rev.created_at)}
                              {rev.effective_since ? ` · ${t('BILLING_RULES.EFFECTIVE_SINCE', { defaultValue: '自 {{date}} 起生效', date: rev.effective_since })}` : ''}
                            </div>
                          </div>
                          <div className="text-[11px] text-on-surface-variant">
                            {t('BILLING_RULES.HISTORY_RULE_COUNTS', '{{models}} 条模型 / {{health}} 条繁忙系数', {
                              models: rev.model_count ?? (rev.model_weights || []).length,
                              health: rev.health_count ?? (rev.health_multipliers || []).length,
                            })}
                          </div>
                        </div>
                      </summary>
                      <div className="px-4 pb-4 grid lg:grid-cols-[1fr_0.7fr] gap-4">
                        <RuleSnapshotTable
                          title={t('BILLING_RULES.MODEL_TABLE_TITLE', '模型计费系数')}
                          rows={rev.model_weights || []}
                          t={t}
                          showThinking
                        />
                        <RuleSnapshotTable
                          title={t('BILLING_RULES.HEALTH_TITLE', '繁忙时段调整')}
                          rows={rev.health_multipliers || []}
                          t={t}
                        />
                      </div>
                    </details>
                  ))}
                </div>
              )}
            </div>
          )}

          {/* 订阅 / 余额 两段口径并列 */}
          <div className="grid md:grid-cols-2 gap-4">
            <ScopeBox
              kind="subscription"
              title={t('BILLING_RULES.SUBSCRIPTION_TITLE', '订阅扣减口径')}
              body={t('BILLING_RULES.SUBSCRIPTION_BODY', '命中订阅时，按下方「模型计费系数」扣减套餐额度。')}
              formulaLabel={t('BILLING_RULES.SUBSCRIPTION_FORMULA_LABEL', '扣减公式')}
              formula={t('BILLING_RULES.SUBSCRIPTION_FORMULA', '套餐扣减额度 = 上游真实成本 × 模型权重 × 繁忙时段系数')}
              exampleLabel={t('BILLING_RULES.EXAMPLE_LABEL', '举例')}
              example={t('BILLING_RULES.SUBSCRIPTION_EXAMPLE', '调一次 Claude Opus（模型权重 ×3.5），上游真实成本 $1，当前繁忙系数 ×1.00 → 套餐扣减 $1 × 3.5 × 1.00 = $3.50。')}
            />
            <ScopeBox
              kind="balance"
              title={t('BILLING_RULES.BALANCE_TITLE', '余额扣减口径')}
              body={t('BILLING_RULES.BALANCE_BODY', '按上游真实成本 1:1 扣减余额，不应用模型权重或繁忙时段系数。')}
              formulaLabel={t('BILLING_RULES.BALANCE_FORMULA_LABEL', '扣减公式')}
              formula={t('BILLING_RULES.BALANCE_FORMULA', '余额扣减额度 = 上游真实成本（与下表系数无关）')}
              exampleLabel={t('BILLING_RULES.EXAMPLE_LABEL', '举例')}
              example={t('BILLING_RULES.BALANCE_EXAMPLE', '调一次 Claude Opus，上游真实成本 $1 → 余额扣减 $1（不论模型权重多少）。')}
            />
          </div>

          {/* 模型计费系数表 — 金融对账单风：深色头 + 等宽体 */}
          <div className="rounded-control border border-outline-variant/40 overflow-hidden">
            <div className="px-4 py-2.5 bg-surface-container-highest border-b border-outline-variant/40 flex items-center justify-between flex-wrap gap-2">
              <h3 className="text-sm font-semibold text-on-surface">
                {t('BILLING_RULES.MODEL_TABLE_TITLE', '模型计费系数')}
              </h3>
              <span className="text-[11px] text-on-surface-variant">
                {t('BILLING_RULES.MODEL_TABLE_SCOPE', '仅适用于订阅扣减；余额扣减按上游真实成本 1:1，不受下表影响')}
              </span>
            </div>
            <div className="fl-table-shell"><div className="fl-table-scroll"><table className="w-full text-sm">
              <thead className="bg-surface-container-high text-[11px] uppercase tracking-wider text-on-surface-variant">
                <tr>
                  <th className="text-left px-4 py-2 font-medium">{t('BILLING_RULES.MODEL_TYPE', '模型')}</th>
                  <th className="text-left px-4 py-2 font-medium font-mono">{t('BILLING_RULES.PATTERN', '匹配模式')}</th>
                  <th className="text-right px-4 py-2 font-medium">{t('BILLING_RULES.NORMAL_USE', '普通调用')}</th>
                  <th className="text-right px-4 py-2 font-medium">{t('BILLING_RULES.THINKING_USE', 'Thinking 调用')}</th>
                  <th className="text-left px-4 py-2 font-medium">{t('BILLING_RULES.REASON', '说明')}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-outline-variant/30">
                {loading && (
                  <tr>
                    <td colSpan="5" className="px-4 py-6 text-center text-on-surface-variant">
                      {t('BILLING_RULES.LOADING', '加载中…')}
                    </td>
                  </tr>
                )}
                {!loading && visibleWeights.map((r, idx) => (
                  <tr key={`${r.pattern}-${idx}`}>
                    <td className="px-4 py-2 text-on-surface font-medium">{humanModelLabel(r, t)}</td>
                    <td className="px-4 py-2 text-on-surface-variant font-mono text-[12px]">{r.pattern}</td>
                    <td className="px-4 py-2 text-right font-mono text-primary">×{formatWeight(r.weight)}</td>
                    <td className="px-4 py-2 text-right font-mono text-warning">
                      {r.thinking_weight ? `×${formatWeight(r.thinking_weight)}` : '—'}
                    </td>
                    <td className="px-4 py-2 text-xs text-on-surface-variant">{r.reason || '—'}</td>
                  </tr>
                ))}
              </tbody>
            </table></div></div>
            {compact && modelWeights.length > visibleWeights.length && (
              <div className="px-4 py-2 border-t border-outline-variant/40 text-xs text-on-surface-variant bg-surface-container-high/40">
                {t('BILLING_RULES.OPEN_PRICING_FULL', '完整规则在「定价」页面查看。')}
              </div>
            )}
          </div>

          {/* 三段附则：繁忙时段 / 模型替换 / Thinking 判定 */}
          <div className="grid md:grid-cols-3 gap-4">
            <Clause title={t('BILLING_RULES.HEALTH_TITLE', '繁忙时段调整')}>
              {!peakActive ? (
                <p>{t('BILLING_RULES.HEALTH_NORMAL', '当前未启用繁忙时段加价（系数 ×1.00）。')}</p>
              ) : (
                <ul className="list-disc list-inside space-y-0.5">
                  {visibleHealth.filter((r) => Number(r.weight || 1) !== 1).map((r, idx) => (
                    <li key={idx} className="font-mono text-[12px]">
                      {r.pattern}: ×{formatWeight(r.weight)}
                      {r.reason ? ` · ${r.reason}` : ''}
                    </li>
                  ))}
                </ul>
              )}
              <p className="mt-2 text-[11px] text-on-surface-variant/80">
                {t('BILLING_RULES.HEALTH_APPLIES', '该调整仅对订阅扣减生效，不影响余额扣减。')}
              </p>
            </Clause>

            <Clause title={t('BILLING_RULES.FALLBACK_TITLE', '模型替换政策')}>
              <ul className="list-disc list-inside space-y-1">
                <li>{t('BILLING_RULES.FALLBACK_LINE_1', '默认不启用模型替换；账单同时记录"请求模型"与"实际模型"。')}</li>
                <li>{t('BILLING_RULES.FALLBACK_LINE_2', '仅当请求显式声明 X-Allow-Fallback: true 时，平台可按规则切换可用上游。')}</li>
              </ul>
              {/* fallbackRule.rule 是后端原样吐出的英文 raw 政策文案，与上面两条 i18n bullet 完全等价
                  但不本地化，且字体 mono 看起来像 debug 信息。改用下方"技术字段对照"里的 X-Allow-Fallback 行覆盖技术细节，
                  普通用户视角不再展示此 raw 字符串。 */}
            </Clause>

            <Clause title={t('BILLING_RULES.THINKING_TITLE', 'Thinking 判定')}>
              <ul className="list-disc list-inside space-y-1">
                <li>{t('BILLING_RULES.THINKING_LINE_1', '请求显式启用 thinking / reasoning，且上游返回 reasoning_tokens > 0，才按 Thinking 倍率扣减。')}</li>
                <li>{t('BILLING_RULES.THINKING_LINE_2', '仅出现 disabled / none / off / 空 thinking 对象不算 Thinking 调用。')}</li>
              </ul>
            </Clause>
          </div>

          {/* 技术字段对照 */}
          <details className="rounded-control border border-outline-variant/40 bg-surface-container/30 px-4 py-3">
            <summary className="cursor-pointer text-sm font-medium text-on-surface">
              {t('BILLING_RULES.TECH_TITLE', '技术字段对照')}
            </summary>
            <div className="mt-3 grid grid-cols-1 md:grid-cols-2 gap-2 text-xs text-on-surface-variant">
              <TechLine name="raw_cost" text={t('BILLING_RULES.TECH_RAW', '上游官方 API 等值美元成本（公开单价折算）。')} />
              <TechLine name="model_weight" text={t('BILLING_RULES.TECH_WEIGHT', '仅订阅扣减下使用的模型系数。')} />
              <TechLine name="health_multiplier" text={t('BILLING_RULES.TECH_HEALTH', '繁忙时段或健康度调整系数。')} />
              <TechLine name="charged_cost" text={t('BILLING_RULES.TECH_CHARGED', '订阅扣减口径 = raw × weight × health。')} />
              <TechLine name="balance_consume" text={t('BILLING_RULES.TECH_BALANCE', '余额扣减口径 = raw 1:1。')} />
              <TechLine name="X-Allow-Fallback: true" text={t('BILLING_RULES.TECH_FALLBACK', '显式允许平台按规则切换 served_model。')} />
            </div>
          </details>
        </>
      )}
    </section>
  );
};

const ScopeBox = ({ kind, title, body, formulaLabel, formula, exampleLabel, example }) => (
  <div className={`rounded-control border bg-surface-container/40 p-4 space-y-2 ${
    kind === 'subscription' ? 'border-primary/40' : 'border-outline-variant/60'
  }`}>
    <div className="flex items-center gap-2">
      <span className={`inline-block w-1.5 h-4 rounded-full ${
        kind === 'subscription' ? 'bg-primary' : 'bg-on-surface-variant'
      }`} />
      <h3 className="text-sm font-semibold text-on-surface">{title}</h3>
    </div>
    <p className="text-sm text-on-surface-variant leading-relaxed">{body}</p>
    <div className="pt-1">
      <div className="text-[10px] uppercase tracking-wider text-on-surface-variant/70">{formulaLabel}</div>
      <code className="block mt-0.5 text-xs text-on-surface bg-on-surface/[0.04] rounded-control px-2.5 py-1.5 break-words leading-relaxed">
        {formula}
      </code>
    </div>
    {example && (
      <div className="pt-1">
        <div className="text-[10px] uppercase tracking-wider text-on-surface-variant/70">{exampleLabel}</div>
        <div className="mt-0.5 text-xs text-on-surface-variant leading-relaxed">{example}</div>
      </div>
    )}
  </div>
);

const Clause = ({ title, children }) => (
  <div className="rounded-control border border-outline-variant/40 bg-surface p-4">
    <div className="text-sm font-semibold text-on-surface mb-2">{title}</div>
    <div className="text-xs text-on-surface-variant leading-relaxed space-y-1">
      {children}
    </div>
  </div>
);

const TechLine = ({ name, text }) => (
  <div className="rounded-control bg-on-surface/[0.03] px-2.5 py-1.5">
    <span className="font-mono text-primary">{name}</span>
    <span className="ml-2">{text}</span>
  </div>
);

const RuleSnapshotTable = ({ title, rows, t, showThinking = false }) => (
  <div className="rounded-control border border-outline-variant/30 overflow-hidden">
    <div className="px-3 py-2 bg-surface-container-high text-xs font-semibold text-on-surface">{title}</div>
    <div className="fl-table-shell"><div className="fl-table-scroll"><table className="w-full text-xs">
      <thead className="bg-surface-container text-[10px] uppercase tracking-wider text-on-surface-variant">
        <tr>
          <th className="text-left px-3 py-1.5 font-medium">{t('BILLING_RULES.MODEL_TYPE', '模型')}</th>
          <th className="text-left px-3 py-1.5 font-medium font-mono">{t('BILLING_RULES.PATTERN', '匹配模式')}</th>
          <th className="text-right px-3 py-1.5 font-medium">{t('BILLING_RULES.NORMAL_USE', '普通调用')}</th>
          {showThinking && <th className="text-right px-3 py-1.5 font-medium">{t('BILLING_RULES.THINKING_USE', 'Thinking 调用')}</th>}
        </tr>
      </thead>
      <tbody className="divide-y divide-outline-variant/25">
        {rows.length === 0 ? (
          <tr>
            <td colSpan={showThinking ? 4 : 3} className="px-3 py-4 text-center text-on-surface-variant">
              {t('COMMON.EMPTY', '暂无数据')}
            </td>
          </tr>
        ) : rows.map((r, idx) => (
          <tr key={`${r.pattern}-${idx}`}>
            <td className="px-3 py-1.5 text-on-surface font-medium">{humanModelLabel(r, t)}</td>
            <td className="px-3 py-1.5 text-on-surface-variant font-mono">{r.pattern}</td>
            <td className="px-3 py-1.5 text-right font-mono text-primary">×{formatWeight(r.weight)}</td>
            {showThinking && (
              <td className="px-3 py-1.5 text-right font-mono text-warning">
                {r.thinking_weight ? `×${formatWeight(r.thinking_weight)}` : '—'}
              </td>
            )}
          </tr>
        ))}
      </tbody>
    </table></div></div>
  </div>
);

export default BillingRulesPanel;
