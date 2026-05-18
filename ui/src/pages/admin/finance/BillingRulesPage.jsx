/**
 * BillingRulesPage — admin 计费规则编辑器（金融对账单风）
 *
 * 编辑 SysConfig:
 *   - billing_model_weights_json
 *   - billing_health_multipliers_json
 *   - billing_rules_version
 *
 * 仅订阅扣减口径走这些系数；余额扣减永远 = raw_cost 1:1，与本页无关。
 * 公示页（user 侧 BillingRulesPanel）会立即同步显示新版本号 + 新系数。
 */
import React, { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import toast from 'react-hot-toast';
import { Plus, Trash2, Save, RefreshCw, AlertTriangle, Scale, Activity } from 'lucide-react';
import { authFetch } from '../../../utils/authFetch';

const emptyModelRow = () => ({
  pattern: '',
  weight: 1,
  thinking_weight: '',
  label: '',
  reason: '',
});

const emptyHealthRow = () => ({
  pattern: '*',
  weight: 1,
  label: '',
  reason: '',
});

// fix P2（codex review verify-r6）：后端 extractEffectiveSinceFromVersion 取版本号最后 10
// 字符当 YYYY-MM-DD。原 todayUTCStamp 返回 `YYYY-MM-DD-HHMM`，尾段 `MM-DD-HHMM` 解析失败
// → 公示页 effective_since 空。改为纯日期，与后端默认值（controller/billing_rules.go:80）一致。
const todayUTCStamp = () => {
  const d = new Date();
  const pad = (n) => String(n).padStart(2, '0');
  return `${d.getUTCFullYear()}-${pad(d.getUTCMonth() + 1)}-${pad(d.getUTCDate())}`;
};

const BillingRulesPage = () => {
  const { t } = useTranslation();
  const [version, setVersion] = useState('');
  const [loadedVersion, setLoadedVersion] = useState('');
  const [models, setModels] = useState([]);
  const [healths, setHealths] = useState([]);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');

  const load = async () => {
    setLoading(true);
    setError('');
    try {
      const res = await fetch('/api/billing/rules', { credentials: 'same-origin' });
      const json = await res.json();
      if (!res.ok || !json.success) throw new Error(json.message || `HTTP ${res.status}`);
      const data = json.data || {};
      setModels((data.model_weights || []).map((r) => ({
        pattern: r.pattern || '',
        weight: r.weight ?? 1,
        thinking_weight: r.thinking_weight ?? '',
        label: r.label || '',
        reason: r.reason || '',
      })));
      setHealths((data.health_multipliers || []).map((r) => ({
        pattern: r.pattern || '*',
        weight: r.weight ?? 1,
        label: r.label || '',
        reason: r.reason || '',
      })));
      setLoadedVersion(data.version || '');
      setVersion('');
    } catch (e) {
      setError(e?.message || String(e));
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => { load(); }, []);

  const updateModel = (idx, key, val) => {
    setModels((prev) => prev.map((r, i) => (i === idx ? { ...r, [key]: val } : r)));
  };
  const updateHealth = (idx, key, val) => {
    setHealths((prev) => prev.map((r, i) => (i === idx ? { ...r, [key]: val } : r)));
  };

  const addModel = () => setModels((prev) => [...prev, emptyModelRow()]);
  const removeModel = (idx) => setModels((prev) => prev.filter((_, i) => i !== idx));
  const addHealth = () => setHealths((prev) => [...prev, emptyHealthRow()]);
  const removeHealth = (idx) => setHealths((prev) => prev.filter((_, i) => i !== idx));

  const validateBeforeSave = () => {
    if (models.length === 0) {
      toast.error('模型权重至少保留一条（建议保留 *=1 兜底）');
      return false;
    }
    const seen = new Set();
    for (const [i, r] of models.entries()) {
      const p = String(r.pattern || '').trim();
      if (!p) { toast.error(`第 ${i + 1} 条 pattern 不能为空`); return false; }
      const lower = p.toLowerCase();
      if (seen.has(lower)) { toast.error(`pattern 重复：${p}`); return false; }
      seen.add(lower);
      const w = Number(r.weight);
      if (!(w > 0 && w <= 1000)) { toast.error(`第 ${i + 1} 条 weight 必须 > 0 且 ≤ 1000`); return false; }
      if (r.thinking_weight !== '' && r.thinking_weight !== null) {
        const tw = Number(r.thinking_weight);
        if (!(tw > 0 && tw <= 1000)) { toast.error(`第 ${i + 1} 条 thinking_weight 必须 > 0 且 ≤ 1000`); return false; }
      }
    }
    const seenH = new Set();
    for (const [i, r] of healths.entries()) {
      const p = String(r.pattern || '').trim();
      if (!p) { toast.error(`繁忙时段第 ${i + 1} 条 pattern 不能为空`); return false; }
      const lower = p.toLowerCase();
      if (seenH.has(lower)) { toast.error(`繁忙时段 pattern 重复：${p}`); return false; }
      seenH.add(lower);
      const w = Number(r.weight);
      if (!(w > 0 && w <= 1000)) { toast.error(`繁忙时段第 ${i + 1} 条 weight 必须 > 0 且 ≤ 1000`); return false; }
    }
    return true;
  };

  const handleSave = async () => {
    if (!validateBeforeSave()) return;
    setSaving(true);
    try {
      const payload = {
        version: String(version || '').trim() || `editor-${todayUTCStamp()}`,
        model_weights: models.map((r) => ({
          pattern: String(r.pattern).trim(),
          weight: Number(r.weight),
          ...(r.thinking_weight !== '' && r.thinking_weight !== null
            ? { thinking_weight: Number(r.thinking_weight) }
            : {}),
          label: String(r.label || '').trim(),
          reason: String(r.reason || '').trim(),
        })),
        health_multipliers: healths.map((r) => ({
          pattern: String(r.pattern).trim(),
          weight: Number(r.weight),
          label: String(r.label || '').trim(),
          reason: String(r.reason || '').trim(),
        })),
      };
      const json = await authFetch('/api/admin/billing/rules', { method: 'POST', body: payload });
      if (!json.success) {
        toast.error(json.message || '保存计费规则失败');
        return;
      }
      toast.success('计费规则已发布');
      load();
    } catch (e) {
      toast.error('保存计费规则网络异常');
    } finally {
      setSaving(false);
    }
  };

  return (
    <section className="space-y-4">
      {/* 顶部 header — 版本号 + 保存按钮 */}
      <header className="rounded-overlay border border-outline-variant/60 bg-surface px-5 py-4 flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between">
        <div>
          <h2 className="text-base font-semibold text-on-surface">{t('ADMIN_BILLING_RULES.TITLE', '计费规则编辑器')}</h2>
          <p className="text-xs text-on-surface-variant mt-1 max-w-2xl">
            {t('ADMIN_BILLING_RULES.SUB', '修改保存后立即生效并刷新公示页。仅订阅扣减按下表系数；余额扣减永远等于上游真实成本。')}
          </p>
        </div>
        <div className="flex items-end gap-2">
          <div className="flex flex-col gap-1">
            <label className="text-[11px] uppercase tracking-wider text-on-surface-variant/80">
              {t('ADMIN_BILLING_RULES.NEW_VERSION', '新版本号')}
            </label>
            <input
              value={version}
              onChange={(e) => setVersion(e.target.value)}
              placeholder={loadedVersion ? `当前 ${loadedVersion}（留空则自动 editor-YYYY-MM-DD-HHMM）` : '自动 editor-YYYY-MM-DD-HHMM'}
              className="w-72 bg-surface-container-high border border-outline-variant text-on-surface text-sm font-mono rounded-control px-3 py-1.5 outline-none focus:border-primary"
            />
          </div>
          <button
            type="button"
            onClick={load}
            disabled={loading}
            className="h-9 px-3 rounded-control border border-outline-variant text-sm text-on-surface-variant hover:text-on-surface hover:bg-on-surface/[0.04] flex items-center gap-1.5"
          >
            <RefreshCw size={14} className={loading ? 'animate-spin' : ''} />
            {t('COMMON.RELOAD', '重新加载')}
          </button>
          <button
            type="button"
            onClick={handleSave}
            disabled={saving || loading}
            className="h-9 px-4 rounded-control bg-primary text-on-primary text-sm font-medium flex items-center gap-1.5 hover:opacity-90 disabled:opacity-50"
          >
            <Save size={14} />
            {saving ? t('COMMON.SAVING', '保存中…') : t('ADMIN_BILLING_RULES.SAVE', '保存并发布')}
          </button>
        </div>
      </header>

      {error && (
        <div className="rounded-control border border-error/30 bg-error/10 px-3 py-2 text-sm text-error flex items-center gap-2">
          <AlertTriangle size={14} />
          {t('BILLING_RULES.LOAD_FAIL', '计费规则加载失败')}: {error}
        </div>
      )}

      {/* 模型权重表 */}
      <RuleTableCard
        icon={Scale}
        title={t('ADMIN_BILLING_RULES.MODEL_TABLE_TITLE', '模型计费系数（仅订阅扣减）')}
        scope={t('ADMIN_BILLING_RULES.MODEL_TABLE_SCOPE', '匹配模式自上而下首条命中即生效；通配符支持 *')}
        onAdd={addModel}
        addLabel={t('ADMIN_BILLING_RULES.ADD_MODEL_ROW', '新增模型规则')}
      >
        <table className="w-full text-sm">
          <thead className="bg-surface-container-high text-[11px] uppercase tracking-wider text-on-surface-variant">
            <tr>
              <th className="text-left px-3 py-2 font-medium w-44">{t('ADMIN_BILLING_RULES.COL_PATTERN', '匹配模式')}</th>
              <th className="text-right px-3 py-2 font-medium w-24">{t('ADMIN_BILLING_RULES.COL_WEIGHT', '普通')}</th>
              <th className="text-right px-3 py-2 font-medium w-24">{t('ADMIN_BILLING_RULES.COL_THINKING', 'Thinking')}</th>
              <th className="text-left px-3 py-2 font-medium w-44">{t('ADMIN_BILLING_RULES.COL_LABEL', '展示标签')}</th>
              <th className="text-left px-3 py-2 font-medium">{t('ADMIN_BILLING_RULES.COL_REASON', '说明')}</th>
              <th className="w-10" />
            </tr>
          </thead>
          <tbody className="divide-y divide-outline-variant/30">
            {loading && (
              <tr><td colSpan="6" className="px-3 py-6 text-center text-on-surface-variant">{t('COMMON.LOADING', '加载中…')}</td></tr>
            )}
            {!loading && models.length === 0 && (
              <tr><td colSpan="6" className="px-3 py-6 text-center text-on-surface-variant">{t('ADMIN_BILLING_RULES.EMPTY_MODEL', '点击右上方"新增"添加第一条规则')}</td></tr>
            )}
            {!loading && models.map((r, idx) => (
              <tr key={idx}>
                <td className="px-3 py-1.5">
                  <input
                    value={r.pattern}
                    onChange={(e) => updateModel(idx, 'pattern', e.target.value)}
                    placeholder="*haiku*"
                    className="w-full bg-surface-container-high border border-outline-variant text-on-surface text-sm font-mono rounded-control px-2 py-1 outline-none focus:border-primary"
                  />
                </td>
                <td className="px-3 py-1.5 text-right">
                  <input
                    type="number" min="0" step="0.05"
                    value={r.weight}
                    onChange={(e) => updateModel(idx, 'weight', e.target.value)}
                    className="w-20 bg-surface-container-high border border-outline-variant text-primary text-sm font-mono rounded-control px-2 py-1 text-right outline-none focus:border-primary"
                  />
                </td>
                <td className="px-3 py-1.5 text-right">
                  <input
                    type="number" min="0" step="0.05"
                    value={r.thinking_weight}
                    onChange={(e) => updateModel(idx, 'thinking_weight', e.target.value)}
                    placeholder="—"
                    className="w-20 bg-surface-container-high border border-outline-variant text-warning text-sm font-mono rounded-control px-2 py-1 text-right outline-none focus:border-primary"
                  />
                </td>
                <td className="px-3 py-1.5">
                  <input
                    value={r.label}
                    onChange={(e) => updateModel(idx, 'label', e.target.value)}
                    placeholder="Claude Haiku"
                    className="w-full bg-surface-container-high border border-outline-variant text-on-surface text-sm rounded-control px-2 py-1 outline-none focus:border-primary"
                  />
                </td>
                <td className="px-3 py-1.5">
                  <input
                    value={r.reason}
                    onChange={(e) => updateModel(idx, 'reason', e.target.value)}
                    placeholder="低成本/轻量模型"
                    className="w-full bg-surface-container-high border border-outline-variant text-on-surface-variant text-xs rounded-control px-2 py-1 outline-none focus:border-primary"
                  />
                </td>
                <td className="px-2 py-1.5 text-right">
                  <button
                    type="button"
                    onClick={() => removeModel(idx)}
                    className="p-1 rounded-control text-on-surface-variant hover:text-error hover:bg-error/10"
                    title={t('COMMON.DELETE', '删除')}
                  >
                    <Trash2 size={14} />
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </RuleTableCard>

      {/* 健康倍率表 */}
      <RuleTableCard
        icon={Activity}
        title={t('ADMIN_BILLING_RULES.HEALTH_TABLE_TITLE', '繁忙时段系数')}
        scope={t('ADMIN_BILLING_RULES.HEALTH_TABLE_SCOPE', '×1.00 等价无加价。pattern=* 兜底，全部 ×1 时公示页会标"未启用"')}
        onAdd={addHealth}
        addLabel={t('ADMIN_BILLING_RULES.ADD_HEALTH_ROW', '新增繁忙时段规则')}
      >
        <table className="w-full text-sm">
          <thead className="bg-surface-container-high text-[11px] uppercase tracking-wider text-on-surface-variant">
            <tr>
              <th className="text-left px-3 py-2 font-medium w-44">{t('ADMIN_BILLING_RULES.COL_PATTERN', '匹配模式')}</th>
              <th className="text-right px-3 py-2 font-medium w-24">{t('ADMIN_BILLING_RULES.COL_WEIGHT', '系数')}</th>
              <th className="text-left px-3 py-2 font-medium w-44">{t('ADMIN_BILLING_RULES.COL_LABEL', '展示标签')}</th>
              <th className="text-left px-3 py-2 font-medium">{t('ADMIN_BILLING_RULES.COL_REASON', '说明')}</th>
              <th className="w-10" />
            </tr>
          </thead>
          <tbody className="divide-y divide-outline-variant/30">
            {loading && (
              <tr><td colSpan="5" className="px-3 py-6 text-center text-on-surface-variant">{t('COMMON.LOADING', '加载中…')}</td></tr>
            )}
            {!loading && healths.length === 0 && (
              <tr><td colSpan="5" className="px-3 py-6 text-center text-on-surface-variant">{t('ADMIN_BILLING_RULES.EMPTY_HEALTH', '保存时若空，服务端自动补 *=1 兜底')}</td></tr>
            )}
            {!loading && healths.map((r, idx) => (
              <tr key={idx}>
                <td className="px-3 py-1.5">
                  <input
                    value={r.pattern}
                    onChange={(e) => updateHealth(idx, 'pattern', e.target.value)}
                    placeholder="*"
                    className="w-full bg-surface-container-high border border-outline-variant text-on-surface text-sm font-mono rounded-control px-2 py-1 outline-none focus:border-primary"
                  />
                </td>
                <td className="px-3 py-1.5 text-right">
                  <input
                    type="number" min="0" step="0.05"
                    value={r.weight}
                    onChange={(e) => updateHealth(idx, 'weight', e.target.value)}
                    className="w-20 bg-surface-container-high border border-outline-variant text-primary text-sm font-mono rounded-control px-2 py-1 text-right outline-none focus:border-primary"
                  />
                </td>
                <td className="px-3 py-1.5">
                  <input
                    value={r.label}
                    onChange={(e) => updateHealth(idx, 'label', e.target.value)}
                    placeholder="Normal"
                    className="w-full bg-surface-container-high border border-outline-variant text-on-surface text-sm rounded-control px-2 py-1 outline-none focus:border-primary"
                  />
                </td>
                <td className="px-3 py-1.5">
                  <input
                    value={r.reason}
                    onChange={(e) => updateHealth(idx, 'reason', e.target.value)}
                    placeholder="默认无高峰加权"
                    className="w-full bg-surface-container-high border border-outline-variant text-on-surface-variant text-xs rounded-control px-2 py-1 outline-none focus:border-primary"
                  />
                </td>
                <td className="px-2 py-1.5 text-right">
                  <button
                    type="button"
                    onClick={() => removeHealth(idx)}
                    className="p-1 rounded-control text-on-surface-variant hover:text-error hover:bg-error/10"
                    title={t('COMMON.DELETE', '删除')}
                  >
                    <Trash2 size={14} />
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </RuleTableCard>
    </section>
  );
};

const RuleTableCard = ({ icon: Icon, title, scope, onAdd, addLabel, children }) => (
  <section className="rounded-overlay border border-outline-variant/60 bg-surface overflow-hidden">
    <header className="px-4 py-2.5 bg-surface-container-highest border-b border-outline-variant/40 flex items-center justify-between flex-wrap gap-2">
      <div className="flex items-center gap-2">
        {Icon && <Icon size={15} className="text-primary" />}
        <h3 className="text-sm font-semibold text-on-surface">{title}</h3>
      </div>
      <div className="flex items-center gap-3 flex-wrap">
        <span className="text-[11px] text-on-surface-variant">{scope}</span>
        <button
          type="button"
          onClick={onAdd}
          className="h-8 px-2.5 rounded-control border border-outline-variant text-xs text-on-surface-variant hover:text-on-surface hover:bg-on-surface/[0.04] flex items-center gap-1"
        >
          <Plus size={12} />
          {addLabel}
        </button>
      </div>
    </header>
    <div className="fl-table-shell"><div className="fl-table-scroll">{children}</div></div>
  </section>
);

export default BillingRulesPage;
