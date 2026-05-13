import React, { useState, useEffect, useCallback, useRef } from 'react';
import { Layers, Plus, Edit, Trash2, X, Save, AlertTriangle, Download, Upload } from 'lucide-react';
import toast from 'react-hot-toast';
import { useConfirm } from '../context/ConfirmContext';
import { authFetch } from '../utils/authFetch';
import DurationInput, { formatDuration } from './DurationInput';
import { toCSV, downloadCSV, parseCSV, pickCSVFile } from '../utils/csv';
import { useModalA11y } from '../hooks/useModalA11y';

const QP_CSV_COLUMNS = [
  'id', 'name', 'display_name', 'description',
  'model_match', 'limit_unit', 'limit_value', 'window_seconds',
  'weight_factor', 'priority', 'overflow_strategy',
  'auto_sync_from_channel_models', 'enabled', 'extra_config',
];

const parseQuotaRow = (r) => ({
  id: r.id ? Number(r.id) : undefined,
  name: r.name || '',
  display_name: r.display_name || '',
  description: r.description || '',
  model_match: r.model_match || '[]',
  limit_unit: r.limit_unit || 'request_count',
  limit_value: parseFloat(r.limit_value) || 0,
  window_seconds: parseInt(r.window_seconds) || 0,
  weight_factor: r.weight_factor || '{}',
  priority: parseInt(r.priority) || 100,
  overflow_strategy: r.overflow_strategy || 'block',
  auto_sync_from_channel_models: r.auto_sync_from_channel_models === '1' || r.auto_sync_from_channel_models === 'true',
  enabled: r.enabled === '1' || r.enabled === 'true' || r.enabled === '是',
  extra_config: r.extra_config || '{}',
});

// 配额计划库 admin UI。所有字段都暴露给 admin 自由配置，包括：
//   - limit_unit: api_cost_usd/request_count/input_tokens/output_tokens/total_tokens/weighted_tokens
//   - model_match: JSON 数组的 glob 通配
//   - weight_factor: 灵活 JSON
//   - overflow_strategy: block/next_subscription/degrade_model/任意自定义

const EMPTY_PLAN = {
  name: '', display_name: '', description: '',
  model_match: '[]', limit_unit: 'request_count', limit_value: 0,
  window_seconds: 0, weight_factor: '{}',
  auto_sync_from_channel_models: false,
  priority: 100, overflow_strategy: 'block',
  extra_config: '{}', enabled: true,
};

const QuotaPlanManagement = () => {
  const confirm = useConfirm();
  const [plans, setPlans] = useState([]);
  const [loading, setLoading] = useState(true);
  const [editing, setEditing] = useState(null); // null | EMPTY_PLAN | existing
  const [saving, setSaving] = useState(false);

  const load = useCallback(async () => {
    try {
      const res = await fetch('/api/admin/quota-plans', { credentials: 'include' });
      const json = await res.json();
      if (json.success) setPlans(json.data || []);
    } catch {
      toast.error('加载失败');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { load(); }, [load]);

  const startCreate = () => setEditing({ ...EMPTY_PLAN });
  const startEdit = (p) => setEditing({ ...p });
  const cancel = () => setEditing(null);

  // fix CRITICAL C-F1（gemini 第二十一轮）：补声明 onEditBackdropClick + editCloseBtnRef + modalRef，
  // 原代码在 JSX 中引用但从未声明 → 模态打开必抛 ReferenceError。补全 + 启用 focus trap。
  const editCloseBtnRef = useRef(null);
  const editModalRef = useRef(null);
  const { onBackdropClick: onEditBackdropClick } = useModalA11y(!!editing, cancel, editCloseBtnRef, editModalRef);

  const save = async () => {
    if (!editing.name) {
      toast.error('名称必填');
      return;
    }
    // JSON 字段格式校验
    for (const f of ['model_match', 'weight_factor', 'extra_config']) {
      try { JSON.parse(editing[f] || '{}'); }
      catch { toast.error(`${f} 必须是合法 JSON`); return; }
    }
    setSaving(true);
    const isNew = !editing.id;
    try {
      const res = await fetch(
        isNew ? '/api/admin/quota-plans' : `/api/admin/quota-plans/${editing.id}`,
        {
          method: isNew ? 'POST' : 'PUT',
          credentials: 'include',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(editing),
        }
      );
      const json = await res.json();
      if (json.success) {
        toast.success(isNew ? '创建成功' : '已更新');
        setEditing(null);
        load();
      } else {
        toast.error(json.message || '保存失败');
      }
    } catch {
      toast.error('网络异常');
    } finally {
      setSaving(false);
    }
  };

  const remove = async (p) => {
    if (!(await confirm(`删除配额计划「${p.name}」？`))) return;
    try {
      const res = await fetch(`/api/admin/quota-plans/${p.id}`, {
        method: 'DELETE', credentials: 'include',
      });
      const json = await res.json();
      if (json.success) {
        toast.success('已删除');
        load();
      } else if (json.message_code === 'ERR_PLAN_IN_USE') {
        toast.error(`仍被 ${json.ref_count} 个套餐引用`);
      } else {
        toast.error(json.message || '删除失败');
      }
    } catch {
      toast.error('网络异常，删除失败');
    }
  };

  const updateField = (k, v) => setEditing(prev => ({ ...prev, [k]: v }));

  // ─── CSV 导入 / 导出 ───────────────────────────────────────────
  const exportCSV = () => {
    const rows = plans.map((p) => ({
      id: p.id,
      name: p.name,
      display_name: p.display_name || '',
      description: p.description || '',
      model_match: p.model_match || '[]',
      limit_unit: p.limit_unit,
      limit_value: p.limit_value,
      window_seconds: p.window_seconds || 0,
      weight_factor: p.weight_factor || '{}',
      priority: p.priority || 100,
      overflow_strategy: p.overflow_strategy || 'block',
      auto_sync_from_channel_models: p.auto_sync_from_channel_models ? '1' : '0',
      enabled: p.enabled ? '1' : '0',
      extra_config: p.extra_config || '{}',
    }));
    const csv = toCSV(QP_CSV_COLUMNS, rows);
    const stamp = new Date().toISOString().slice(0, 10);
    downloadCSV(`quota-plans-${stamp}.csv`, csv);
    toast.success(`已导出 ${rows.length} 条`);
  };

  const importCSV = async () => {
    let text;
    try { text = await pickCSVFile(); } catch { return; }
    const { rows } = parseCSV(text);
    if (rows.length === 0) { toast.error('CSV 为空或解析失败'); return; }
    if (!(await confirm(`确认导入 ${rows.length} 条配额计划？\n有 id 的按 id 更新，无 id 的新建。`))) return;

    let ok = 0, fail = 0;
    const failures = [];
    for (const raw of rows) {
      const payload = parseQuotaRow(raw);
      const id = payload.id;
      delete payload.id;
      try {
        const url = id ? `/api/admin/quota-plans/${id}` : '/api/admin/quota-plans';
        const method = id ? 'PUT' : 'POST';
        const r = await authFetch(url, {
          method,
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(payload),
        });
        if (r.success) {
          ok++;
        } else {
          fail++;
          failures.push(`「${raw.name || `#${id || '?'}`}」: ${r.message || r.message_code || '未知错误'}`);
        }
      } catch (e) {
        fail++;
        failures.push(`「${raw.name || `#${id || '?'}`}」: ${e.message || '网络异常'}`);
      }
    }
    if (fail === 0) {
      toast.success(`导入成功 ${ok} 条`);
    } else if (ok === 0) {
      toast.error(`全部 ${fail} 条导入失败：\n${failures.slice(0, 3).join('\n')}`, { duration: 8000 });
    } else {
      toast.error(`部分失败：成功 ${ok}，失败 ${fail}\n${failures.slice(0, 3).join('\n')}`, { duration: 8000 });
    }
    load();
  };

  return (
    <div className="w-full">
      <div className="mb-8 border-b border-outline-variant pb-6 flex flex-col md:flex-row md:items-end md:justify-between gap-4">
        <div>
          <h1 className="text-xl md:text-2xl font-bold text-on-surface flex items-center gap-3">
            <Layers size={22} className="text-primary" /> 配额计划库
          </h1>
          <p className="text-on-surface-variant mt-2 text-sm">
            最小复用单元，被销售套餐通过引用方式组合使用。支持 CSV 批量导入/导出。
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <button type="button" onClick={exportCSV}
            className="h-10 px-4 bg-surface-container-high border border-outline-variant rounded-lg text-sm flex items-center gap-1.5 hover:bg-surface-variant">
            <Download size={14} /> 导出 CSV
          </button>
          <button type="button" onClick={importCSV}
            className="h-10 px-4 bg-surface-container-high border border-outline-variant rounded-lg text-sm flex items-center gap-1.5 hover:bg-surface-variant">
            <Upload size={14} /> 导入 CSV
          </button>
          <button type="button" onClick={startCreate}
            className="h-10 px-4 bg-primary text-on-primary rounded-lg flex items-center gap-1.5 hover:opacity-90 text-sm font-medium">
            <Plus size={14} /> 新建配额计划
          </button>
        </div>
      </div>

      {loading ? (
        <div className="text-center py-20 text-on-surface-variant">加载中...</div>
      ) : plans.length === 0 ? (
        <div className="text-center py-16 bg-surface-container border border-outline-variant rounded-2xl">
          <p className="text-on-surface-variant text-sm">还没有配额计划，点右上角创建</p>
        </div>
      ) : (
        <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4">
          {plans.map(p => (
            <div key={p.id} className="bg-surface-container border border-outline-variant rounded-xl p-5">
              <div className="flex items-start justify-between mb-3">
                <div className="flex-1 min-w-0">
                  <div className="font-bold text-on-surface truncate">{p.display_name || p.name}</div>
                  <div className="text-xs text-outline font-mono truncate">{p.name}</div>
                </div>
                <div className="flex gap-1 shrink-0">
                  <button onClick={() => startEdit(p)} className="p-1.5 text-on-surface-variant hover:text-primary">
                    <Edit size={14} />
                  </button>
                  <button onClick={() => remove(p)} className="p-1.5 text-on-surface-variant hover:text-red-400">
                    <Trash2 size={14} />
                  </button>
                </div>
              </div>
              <div className="space-y-1 text-xs text-on-surface-variant">
                <div>单位: <span className="text-on-surface font-mono">{p.limit_unit}</span></div>
                <div>额度: <span className="text-on-surface font-mono">{p.limit_value}</span></div>
                <div>窗口: <span className="text-on-surface font-mono">{p.window_seconds === 0 ? '套餐周期内' : formatDuration(p.window_seconds)}</span></div>
                <div>优先级: {p.priority} · 溢出: <span className="text-on-surface font-mono">{p.overflow_strategy}</span></div>
              </div>
              <div className="mt-3 pt-3 border-t border-outline-variant/30 flex items-center justify-between text-xs">
                <span className={p.enabled ? 'text-emerald-400' : 'text-outline'}>
                  {p.enabled ? '● 启用' : '○ 禁用'}
                </span>
                <span className="text-outline">ID: {p.id}</span>
              </div>
            </div>
          ))}
        </div>
      )}

      {editing && (
        <div
          ref={editModalRef}
          role="dialog"
          aria-modal="true"
          aria-labelledby="quota-plan-modal-title"
          onClick={onEditBackdropClick}
          className="fixed inset-0 z-[100] flex items-start sm:items-center justify-center p-2 sm:p-4 bg-black/80 backdrop-blur-sm"
        >
          <div className="relative w-full max-w-3xl bg-surface-container border border-outline-variant rounded-2xl flex flex-col max-h-[92vh] shadow-2xl">
            <div className="flex items-center justify-between px-4 sm:px-6 py-4 border-b border-outline-variant/60 shrink-0">
              <h2 id="quota-plan-modal-title" className="text-lg font-bold text-on-surface">{editing.id ? '编辑' : '新建'}配额计划</h2>
              <button type="button" ref={editCloseBtnRef} onClick={cancel} className="text-on-surface-variant hover:text-on-surface p-1 rounded">
                <X size={18} />
              </button>
            </div>

            <div className="px-4 sm:px-6 py-5 overflow-y-auto flex-1 min-h-0">
            <div className="grid grid-cols-1 md:grid-cols-2 gap-4 mb-4">
              <Field label="内部名 *">
                <input className={inputCls} value={editing.name}
                  onChange={e => updateField('name', e.target.value)} placeholder="Sonnet-5h" />
              </Field>
              <Field label="展示名">
                <input className={inputCls} value={editing.display_name}
                  onChange={e => updateField('display_name', e.target.value)} placeholder="Sonnet 高频" />
              </Field>
            </div>

            <Field label="描述（用户可见）">
              <textarea className={inputCls + ' min-h-[60px]'} value={editing.description}
                onChange={e => updateField('description', e.target.value)} />
            </Field>

            <div className="grid grid-cols-1 md:grid-cols-3 gap-4 mt-4">
              <Field label="计量单位 *" hint="api_cost_usd | request_count | input_tokens | output_tokens | total_tokens | weighted_tokens">
                <input className={inputCls} value={editing.limit_unit}
                  onChange={e => updateField('limit_unit', e.target.value)} placeholder="api_cost_usd" />
              </Field>
              <Field label="额度值">
                <input type="number" step="0.0001" className={inputCls} value={editing.limit_value}
                  onChange={e => updateField('limit_value', parseFloat(e.target.value) || 0)} />
              </Field>
              <Field
                label="刷新窗口"
                hint={editing.window_seconds === 0 ? '0 = 套餐周期内累计' : `每 ${formatDuration(editing.window_seconds)} 重置`}
              >
                <DurationInput
                  value={editing.window_seconds}
                  onChange={(sec) => updateField('window_seconds', sec)}
                  className={inputCls}
                  selectClass={inputCls}
                  allowZero
                />
              </Field>
            </div>

            <Field label="模型匹配 (JSON 数组，glob)" hint='例 ["claude-sonnet-*", "claude-haiku-*"]，[] 表示匹配所有'>
              <textarea className={inputCls + ' font-mono min-h-[60px]'} value={editing.model_match}
                onChange={e => updateField('model_match', e.target.value)} />
            </Field>

            <Field label="权重系数 (JSON)" hint='仅 weighted_tokens / token 单位需要；api_cost_usd 直接使用本次请求实价'>
              <textarea className={inputCls + ' font-mono min-h-[80px]'} value={editing.weight_factor}
                onChange={e => updateField('weight_factor', e.target.value)} />
            </Field>

            <div className="grid grid-cols-1 md:grid-cols-3 gap-4 mt-4">
              <Field label="优先级 (小先扣)">
                <input type="number" className={inputCls} value={editing.priority}
                  onChange={e => updateField('priority', parseInt(e.target.value) || 100)} />
              </Field>
              <Field label="溢出策略" hint="block | next_subscription | degrade_model">
                <input className={inputCls} value={editing.overflow_strategy}
                  onChange={e => updateField('overflow_strategy', e.target.value)} />
              </Field>
              <Field label="启用">
                <select className={inputCls} value={editing.enabled ? '1' : '0'}
                  onChange={e => updateField('enabled', e.target.value === '1')}>
                  <option value="1">是</option>
                  <option value="0">否</option>
                </select>
              </Field>
            </div>

            <Field label="自由扩展配置 (JSON)" hint="UI、计费等附加字段，自由定义">
              <textarea className={inputCls + ' font-mono min-h-[60px]'} value={editing.extra_config}
                onChange={e => updateField('extra_config', e.target.value)} />
            </Field>

            <label className="flex items-center gap-2 mt-4 text-sm text-on-surface-variant">
              <input type="checkbox" checked={editing.auto_sync_from_channel_models}
                onChange={e => updateField('auto_sync_from_channel_models', e.target.checked)} />
              从 channel_models 同步权重（仅 weighted_tokens 使用；api_cost_usd 不需要）
            </label>

            </div>{/* 表单滚动区结束 */}

            <div className="flex justify-end gap-2 px-4 sm:px-6 py-4 border-t border-outline-variant/60 bg-surface-container-low rounded-b-2xl shrink-0">
              <button type="button" onClick={cancel} className="px-4 py-2 bg-surface-container-high border border-outline-variant rounded-lg text-sm hover:bg-surface-variant">取消</button>
              <button type="button" onClick={save} disabled={saving}
                className="px-5 py-2 bg-primary text-on-primary rounded-lg text-sm font-medium flex items-center gap-1.5 disabled:opacity-50 hover:opacity-90">
                <Save size={14} /> {saving ? '保存中...' : '保存'}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
};

const inputCls = 'w-full h-10 bg-surface-container-high border border-outline rounded-lg px-3 text-sm text-on-surface outline-none focus:border-primary';

// Field 自动给子 input/textarea/select 注入 id，并把 label htmlFor 关联，提升 a11y。
const Field = ({ label, hint, children }) => {
  const id = React.useId();
  const enhancedChildren = React.Children.map(children, (child) => {
    if (React.isValidElement(child) && !child.props.id) {
      return React.cloneElement(child, { id });
    }
    return child;
  });
  const hintId = hint ? `${id}-hint` : undefined;
  return (
    <div className="space-y-1">
      <label htmlFor={id} className="block text-xs font-semibold text-on-surface-variant">{label}</label>
      {enhancedChildren && enhancedChildren.length > 0 && hint
        ? React.Children.map(enhancedChildren, (child) =>
            React.isValidElement(child) ? React.cloneElement(child, { 'aria-describedby': hintId }) : child)
        : enhancedChildren}
      {hint && <div id={hintId} className="text-[10px] text-outline">{hint}</div>}
    </div>
  );
};

export default QuotaPlanManagement;
