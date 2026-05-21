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
import { Plus, Trash2, Save, RefreshCw, AlertTriangle, Scale, Activity, Send, Clock, XCircle } from 'lucide-react';
import { authFetch } from '../../../utils/authFetch';

// Audit 2026-05-21 HIGH-6 fix：每行带稳定 _rowId。原本 key={idx} 在删行 / 重排时
// 让 React 把上一行的 controlled input state 错配给下一行 —— admin 输入的价格
// 静默应用到错的 model pattern，是金融正确性 bug。
//
// _rowId 仅前端用，提交时由 buildPayload 剥离。
const newRowId = () => {
  if (typeof crypto !== 'undefined' && crypto.randomUUID) return crypto.randomUUID();
  return `row-${Date.now()}-${Math.random().toString(36).slice(2, 9)}`;
};

const emptyModelRow = () => ({
  _rowId: newRowId(),
  pattern: '',
  weight: 1,
  thinking_weight: '',
  label: '',
  reason: '',
});

const emptyHealthRow = () => ({
  _rowId: newRowId(),
  pattern: '*',
  weight: 1,
  label: '',
  reason: '',
});

// 版本号不再承载生效日期；发布时间/生效时间由独立字段保存。
const todayUTCStamp = () => {
  const d = new Date();
  const pad = (n) => String(n).padStart(2, '0');
  return `${d.getUTCFullYear()}-${pad(d.getUTCMonth() + 1)}-${pad(d.getUTCDate())}-${pad(d.getUTCHours())}${pad(d.getUTCMinutes())}${pad(d.getUTCSeconds())}`;
};

const toDateTimeLocalValue = (date) => {
  const d = date || new Date(Date.now() + 60 * 60 * 1000);
  const pad = (n) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
};

const formatDateTime = (value) => {
  if (!value) return '-';
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return String(value);
  return d.toLocaleString();
};

const revisionStatusLabel = (status, t) => {
  switch (status) {
    case 'active': return t('ADMIN_BILLING_RULES.STATUS_ACTIVE', '当前生效');
    case 'scheduled': return t('ADMIN_BILLING_RULES.STATUS_SCHEDULED', '待生效');
    case 'canceled': return t('ADMIN_BILLING_RULES.STATUS_CANCELED', '已撤销');
    default: return t('ADMIN_BILLING_RULES.STATUS_SUPERSEDED', '历史版本');
  }
};

const revisionStatusClass = (status) => {
  switch (status) {
    case 'active': return 'bg-primary/12 text-primary border-primary/25';
    case 'scheduled': return 'bg-warning/12 text-warning border-warning/25';
    case 'canceled': return 'bg-error/10 text-error border-error/20';
    default: return 'bg-on-surface/[0.05] text-on-surface-variant border-outline-variant/50';
  }
};

const BillingRulesPage = () => {
  const { t } = useTranslation();
  const [version, setVersion] = useState('');
  const [loadedVersion, setLoadedVersion] = useState('');
  const [models, setModels] = useState([]);
  const [healths, setHealths] = useState([]);
  const [publishMode, setPublishMode] = useState('immediate');
  const [effectiveAtLocal, setEffectiveAtLocal] = useState(toDateTimeLocalValue());
  const [historyRows, setHistoryRows] = useState([]);
  const [historyLoading, setHistoryLoading] = useState(false);
  const [cancelingId, setCancelingId] = useState(null);
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
        _rowId: newRowId(),
        pattern: r.pattern || '',
        weight: r.weight ?? 1,
        thinking_weight: r.thinking_weight ?? '',
        label: r.label || '',
        reason: r.reason || '',
      })));
      setHealths((data.health_multipliers || []).map((r) => ({
        _rowId: newRowId(),
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

  const loadHistory = async () => {
    setHistoryLoading(true);
    try {
      const res = await fetch('/api/billing/rules/history?limit=50', { credentials: 'same-origin' });
      const json = await res.json();
      if (!res.ok || !json.success) throw new Error(json.message || `HTTP ${res.status}`);
      setHistoryRows(json.data || []);
    } catch (e) {
      toast.error(`${t('BILLING_RULES.HISTORY_LOAD_FAIL', '历史版本加载失败')}: ${e?.message || e}`);
    } finally {
      setHistoryLoading(false);
    }
  };

  useEffect(() => {
    load();
    loadHistory();
  }, []);

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
    if (publishMode === 'scheduled') {
      const d = new Date(effectiveAtLocal);
      if (Number.isNaN(d.getTime())) {
        toast.error(t('ADMIN_BILLING_RULES.EFFECTIVE_AT_INVALID', '请填写有效的预发布生效时间'));
        return false;
      }
      if (d.getTime() <= Date.now() + 30 * 1000) {
        toast.error(t('ADMIN_BILLING_RULES.EFFECTIVE_AT_TOO_SOON', '预发布生效时间至少要晚于当前 30 秒'));
        return false;
      }
    }
    return true;
  };

  const handleSave = async () => {
    if (!validateBeforeSave()) return;
    setSaving(true);
    try {
      const payload = {
        version: String(version || '').trim() || `editor-${todayUTCStamp()}`,
        publish_mode: publishMode,
        ...(publishMode === 'scheduled' ? { effective_at: new Date(effectiveAtLocal).toISOString() } : {}),
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
      toast.success(publishMode === 'scheduled'
        ? t('ADMIN_BILLING_RULES.SCHEDULED_OK', '计费规则已预发布，到点后自动生效')
        : t('ADMIN_BILLING_RULES.PUBLISHED_OK', '计费规则已立即发布'));
      load();
      loadHistory();
    } catch (e) {
      toast.error('保存计费规则网络异常');
    } finally {
      setSaving(false);
    }
  };

  const handleCancelRevision = async (revision) => {
    if (!revision?.id) return;
    const ok = window.confirm(t('ADMIN_BILLING_RULES.CANCEL_CONFIRM', '确认撤销这个尚未生效的预发布版本？'));
    if (!ok) return;
    setCancelingId(revision.id);
    try {
      const json = await authFetch(`/api/admin/billing/rules/revisions/${revision.id}/cancel`, {
        method: 'POST',
        body: { reason: 'admin canceled before effective_at' },
      });
      if (!json.success) {
        toast.error(json.message || t('ADMIN_BILLING_RULES.CANCEL_FAILED', '撤销失败'));
        return;
      }
      toast.success(t('ADMIN_BILLING_RULES.CANCELED_OK', '预发布版本已撤销'));
      loadHistory();
    } catch (e) {
      toast.error(t('ADMIN_BILLING_RULES.CANCEL_FAILED', '撤销失败'));
    } finally {
      setCancelingId(null);
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
        <div className="flex items-end gap-2 flex-wrap justify-end">
          <div className="flex flex-col gap-1">
            <label className="text-[11px] uppercase tracking-wider text-on-surface-variant/80">
              {t('ADMIN_BILLING_RULES.PUBLISH_MODE', '发布方式')}
            </label>
            <div className="h-9 inline-flex rounded-control border border-outline-variant bg-surface-container-high overflow-hidden">
              <button
                type="button"
                onClick={() => setPublishMode('immediate')}
                className={`px-3 text-xs font-medium inline-flex items-center gap-1.5 ${publishMode === 'immediate' ? 'bg-primary text-on-primary' : 'text-on-surface-variant hover:text-on-surface hover:bg-on-surface/[0.04]'}`}
              >
                <Send size={13} />
                {t('ADMIN_BILLING_RULES.MODE_IMMEDIATE', '立即发布')}
              </button>
              <button
                type="button"
                onClick={() => setPublishMode('scheduled')}
                className={`px-3 text-xs font-medium inline-flex items-center gap-1.5 border-l border-outline-variant ${publishMode === 'scheduled' ? 'bg-primary text-on-primary' : 'text-on-surface-variant hover:text-on-surface hover:bg-on-surface/[0.04]'}`}
              >
                <Clock size={13} />
                {t('ADMIN_BILLING_RULES.MODE_SCHEDULED', '预发布')}
              </button>
            </div>
          </div>
          {publishMode === 'scheduled' && (
            <div className="flex flex-col gap-1">
              <label className="text-[11px] uppercase tracking-wider text-on-surface-variant/80">
                {t('ADMIN_BILLING_RULES.EFFECTIVE_AT', '生效时间')}
              </label>
              <input
                type="datetime-local"
                value={effectiveAtLocal}
                onChange={(e) => setEffectiveAtLocal(e.target.value)}
                className="w-52 bg-surface-container-high border border-outline-variant text-on-surface text-sm rounded-control px-3 py-1.5 outline-none focus:border-primary"
              />
            </div>
          )}
          <div className="flex flex-col gap-1">
            <label className="text-[11px] uppercase tracking-wider text-on-surface-variant/80">
              {t('ADMIN_BILLING_RULES.NEW_VERSION', '新版本号')}
            </label>
            <input
              value={version}
              onChange={(e) => setVersion(e.target.value)}
              placeholder={loadedVersion ? `当前 ${loadedVersion}（留空自动生成）` : '自动 editor-YYYY-MM-DD-HHMMSS'}
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
            {saving
              ? t('COMMON.SAVING', '保存中…')
              : (publishMode === 'scheduled'
                ? t('ADMIN_BILLING_RULES.SCHEDULE_SAVE', '保存预发布')
                : t('ADMIN_BILLING_RULES.SAVE', '保存并发布'))}
          </button>
        </div>
      </header>

      {error && (
        <div className="rounded-control border border-error/30 bg-error/10 px-3 py-2 text-sm text-error flex items-center gap-2">
          <AlertTriangle size={14} />
          {t('BILLING_RULES.LOAD_FAIL', '计费规则加载失败')}: {error}
        </div>
      )}

      <RevisionHistoryCard
        rows={historyRows}
        loading={historyLoading}
        cancelingId={cancelingId}
        onReload={loadHistory}
        onCancel={handleCancelRevision}
        t={t}
      />

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
              <tr key={r._rowId}>
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
              <tr key={r._rowId}>
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

const RevisionHistoryCard = ({ rows, loading, cancelingId, onReload, onCancel, t }) => (
  <section className="rounded-overlay border border-outline-variant/60 bg-surface overflow-hidden">
    <header className="px-4 py-2.5 bg-surface-container-highest border-b border-outline-variant/40 flex items-center justify-between flex-wrap gap-2">
      <div>
        <h3 className="text-sm font-semibold text-on-surface">{t('ADMIN_BILLING_RULES.REVISION_TITLE', '发布计划与历史')}</h3>
        <p className="text-[11px] text-on-surface-variant mt-0.5">
          {t('ADMIN_BILLING_RULES.REVISION_SUB', '立即发布会马上生效；预发布会在生效时间到达后自动切换。')}
        </p>
      </div>
      <button
        type="button"
        onClick={onReload}
        disabled={loading}
        className="h-8 px-2.5 rounded-control border border-outline-variant text-xs text-on-surface-variant hover:text-on-surface hover:bg-on-surface/[0.04] disabled:opacity-50 flex items-center gap-1"
      >
        <RefreshCw size={12} className={loading ? 'animate-spin' : ''} />
        {t('BILLING_RULES.HISTORY_RELOAD', '刷新历史')}
      </button>
    </header>
    <div className="divide-y divide-outline-variant/30">
      {loading && rows.length === 0 && (
        <div className="px-4 py-6 text-center text-sm text-on-surface-variant">{t('COMMON.LOADING', '加载中…')}</div>
      )}
      {!loading && rows.length === 0 && (
        <div className="px-4 py-6 text-center text-sm text-on-surface-variant">{t('BILLING_RULES.HISTORY_EMPTY', '暂无历史版本')}</div>
      )}
      {rows.slice(0, 6).map((row) => (
        <div key={row.id} className="px-4 py-3 flex items-center justify-between gap-3 flex-wrap">
          <div className="min-w-0">
            <div className="flex items-center gap-2 flex-wrap">
              <span className="font-mono text-xs text-on-surface truncate">{row.version || '-'}</span>
              <span className={`inline-flex items-center h-5 px-2 rounded-full border text-[11px] ${revisionStatusClass(row.status)}`}>
                {revisionStatusLabel(row.status, t)}
              </span>
            </div>
            <div className="text-[11px] text-on-surface-variant mt-1 flex flex-wrap gap-x-3 gap-y-1">
              <span>{t('BILLING_RULES.HISTORY_CREATED_AT', '发布时间')}: {formatDateTime(row.published_at || row.created_at)}</span>
              <span>{t('BILLING_RULES.EFFECTIVE_AT', '生效时间')}: {formatDateTime(row.effective_at)}</span>
              <span>{t('BILLING_RULES.HISTORY_RULE_COUNTS', '{{models}} 条模型 / {{health}} 条繁忙系数', {
                models: row.model_count ?? (row.model_weights || []).length,
                health: row.health_count ?? (row.health_multipliers || []).length,
              })}</span>
            </div>
          </div>
          {row.status === 'scheduled' && (
            <button
              type="button"
              onClick={() => onCancel(row)}
              disabled={cancelingId === row.id}
              className="h-8 px-2.5 rounded-control border border-error/30 text-xs text-error hover:bg-error/10 disabled:opacity-50 flex items-center gap-1"
            >
              <XCircle size={12} />
              {cancelingId === row.id ? t('COMMON.SUBMITTING', '提交中…') : t('ADMIN_BILLING_RULES.CANCEL_SCHEDULE', '撤销')}
            </button>
          )}
        </div>
      ))}
    </div>
  </section>
);

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
