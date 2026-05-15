import React, { useState, useEffect, useCallback, useRef } from 'react';
import { Package as PackageIcon, Plus, Edit, Trash2, X, Save, Download, Upload } from 'lucide-react';
import toast from 'react-hot-toast';
import { useConfirm } from '../context/ConfirmContext';
import { authFetch } from '../utils/authFetch';
import { logger } from '../utils/logger';
import DurationInput, { formatDuration } from './DurationInput';
import { SortableGrid, GripHandle } from './ui';
import { toCSV, downloadCSV, parseCSV, pickCSVFile } from '../utils/csv';
import { useModalA11y } from '../hooks/useModalA11y';

// CSV 列定义。导入/导出共享，保持表头稳定。
const CSV_COLUMNS = [
  'id', 'name', 'description', 'highlight_tag',
  'price_amount', 'price_currency', 'billing_period_seconds',
  'stackable', 'max_active_per_user', 'purchase_when_owned',
  'public', 'sort_order', 'enabled',
  'plan_ids', 'plan_multipliers', 'extra_config',
];

// CSV 单元格 → 对应字段类型的解析
const parseRow = (r) => ({
  id: r.id ? Number(r.id) : undefined,
  name: r.name || '',
  description: r.description || '',
  highlight_tag: r.highlight_tag || '',
  icon_key: 'Package',
  badge_color: '',
  gradient: '',
  price_amount: parseFloat(r.price_amount) || 0,
  price_currency: r.price_currency || 'USD',
  billing_period_seconds: parseInt(r.billing_period_seconds) || 2592000,
  stackable: r.stackable === '1' || r.stackable === 'true' || r.stackable === '是',
  max_active_per_user: parseInt(r.max_active_per_user) || 0,
  purchase_when_owned: r.purchase_when_owned || 'ask',
  public: r.public === '1' || r.public === 'true' || r.public === '是',
  sort_order: parseInt(r.sort_order) || 0,
  enabled: r.enabled === '1' || r.enabled === 'true' || r.enabled === '是',
  extra_config: r.extra_config || '{}',
  plan_ids: (r.plan_ids || '').split('|').map((s) => parseInt(s.trim())).filter(Boolean),
  plan_multipliers: (r.plan_multipliers || '').split('|').map((s) => parseFloat(s.trim()) || 1).filter((n) => !isNaN(n)),
});

const EMPTY_PKG = {
  product_type: 'subscription',
  name: '', description: '',
  icon_key: 'Package', badge_color: '', gradient: '', highlight_tag: '',
  price_amount: 0, price_currency: 'USD',
  billing_period_seconds: 2592000, // 订阅默认 30 天
  stackable: true,
  max_active_per_user: 5,
  purchase_when_owned: 'ask',
  public: false,
  sort_order: 0,
  enabled: true,
  extra_config: '{}',
  plan_ids: [],
  plan_multipliers: [],
};

const PackageManagement = () => {
  const confirm = useConfirm();
  const [pkgs, setPkgs] = useState([]);
  const [allPlans, setAllPlans] = useState([]);
  const [loading, setLoading] = useState(true);
  const [editing, setEditing] = useState(null);
  const [saving, setSaving] = useState(false);

  const load = useCallback(async () => {
    try {
      const [r1, r2] = await Promise.all([
        authFetch('/api/admin/packages'),
        authFetch('/api/admin/quota-plans?enabled=1'),
      ]);
      if (r1.success) setPkgs(r1.data || []);
      if (r2.success) setAllPlans(r2.data || []);
    } catch {
      toast.error('加载失败');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { load(); }, [load]);

  const startCreate = () => setEditing({ ...EMPTY_PKG });
  const startEdit = async (p) => {
    try {
      // authFetch 已自动 JSON.parse，直接拿到对象（之前 res.json() 是 bug，res 不是 Response）
      const json = await authFetch(`/api/admin/packages/${p.id}`);
      if (json.success) {
        const full = json.data;
        setEditing({
          ...EMPTY_PKG, ...full,
          plan_ids: (full.plans || []).map(pp => pp.quota_plan_id),
          plan_multipliers: (full.plans || []).map(pp => pp.quantity_multiplier),
        });
      } else {
        toast.error(json.message || '加载套餐详情失败');
      }
    } catch (e) {
      logger.error('[PackageManagement] startEdit failed', e);
      toast.error('网络异常，无法加载套餐详情');
    }
  };

  const cancel = () => setEditing(null);

  // a11y: 编辑模态首次焦点 → 关闭按钮，避免键盘用户卡死
  const editCloseBtnRef = useRef(null);
  const editModalRef = useRef(null); // C-F1 第二十一轮: focus trap 范围
  const { onBackdropClick: onEditBackdropClick } = useModalA11y(!!editing, cancel, editCloseBtnRef, editModalRef);

  const save = async () => {
    if (!editing.name) { toast.error('名称必填'); return; }
    try { JSON.parse(editing.extra_config || '{}'); }
    catch { toast.error('extra_config 必须是合法 JSON'); return; }
    setSaving(true);
    const isNew = !editing.id;
    try {
      // fix MAJOR（多模型审计第二十五轮 P2）：admin 写操作改 authFetch，统一鉴权 + 错误归一化
      const json = await authFetch(
        isNew ? '/api/admin/packages' : `/api/admin/packages/${editing.id}`,
        {
          method: isNew ? 'POST' : 'PUT',
          body: editing,
        }
      );
      if (json.success) {
        toast.success(isNew ? '已创建' : '已更新');
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
    if (!(await confirm({ level: 'L1', danger: true, message: `删除套餐「${p.name}」？该套餐下的活跃订阅不受影响，但用户不能再续费` }))) return;
    try {
      // fix MAJOR（多模型审计第二十五轮 P2）：admin 写操作改 authFetch
      const json = await authFetch(`/api/admin/packages/${p.id}`, { method: 'DELETE' });
      if (json.success) { toast.success('已删除'); load(); }
      else toast.error(json.message || '删除失败');
    } catch {
      toast.error('网络异常，删除失败');
    }
  };

  const updateField = (k, v) => setEditing(prev => ({ ...prev, [k]: v }));

  const togglePlan = (planId) => {
    const idx = (editing.plan_ids || []).indexOf(planId);
    if (idx >= 0) {
      const ids = [...editing.plan_ids]; ids.splice(idx, 1);
      const mults = [...(editing.plan_multipliers || [])]; mults.splice(idx, 1);
      setEditing(prev => ({ ...prev, plan_ids: ids, plan_multipliers: mults }));
    } else {
      setEditing(prev => ({
        ...prev,
        plan_ids: [...(prev.plan_ids || []), planId],
        plan_multipliers: [...(prev.plan_multipliers || []), 1.0],
      }));
    }
  };

  const setMultiplier = (idx, val) => {
    const mults = [...(editing.plan_multipliers || [])];
    mults[idx] = parseFloat(val) || 1.0;
    setEditing(prev => ({ ...prev, plan_multipliers: mults }));
  };

  // ─── CSV 导入 / 导出 ───────────────────────────────────────────
  const exportCSV = async () => {
    try {
      // 拉每个套餐的完整数据（含 plans）
      const detailed = await Promise.all(
        pkgs.map(async (p) => {
          try {
            const r = await authFetch(`/api/admin/packages/${p.id}`);
            return r.success ? r.data : p;
          } catch {
            return p;
          }
        })
      );
      const rows = detailed.map((p) => ({
        id: p.id,
        name: p.name,
        description: p.description || '',
        highlight_tag: p.highlight_tag || '',
        price_amount: p.price_amount,
        price_currency: p.price_currency,
        billing_period_seconds: p.billing_period_seconds,
        stackable: p.stackable ? '1' : '0',
        max_active_per_user: p.max_active_per_user || 0,
        purchase_when_owned: p.purchase_when_owned || 'ask',
        public: p.public ? '1' : '0',
        sort_order: p.sort_order || 0,
        enabled: p.enabled ? '1' : '0',
        plan_ids: (p.plans || []).map((pp) => pp.quota_plan_id).join('|'),
        plan_multipliers: (p.plans || []).map((pp) => pp.quantity_multiplier).join('|'),
        extra_config: p.extra_config || '{}',
      }));
      const csv = toCSV(CSV_COLUMNS, rows);
      const stamp = new Date().toISOString().slice(0, 10);
      downloadCSV(`packages-${stamp}.csv`, csv);
      toast.success(`已导出 ${rows.length} 条`);
    } catch (e) {
      toast.error('导出失败：' + (e.message || '未知错误'));
    }
  };

  const importCSV = async () => {
    let text;
    try {
      text = await pickCSVFile();
    } catch {
      return; // 用户取消
    }
    const { rows } = parseCSV(text);
    if (rows.length === 0) {
      toast.error('CSV 为空或解析失败');
      return;
    }
    if (!(await confirm(`确认导入 ${rows.length} 条套餐？\n有 id 的按 id 更新，无 id 的新建。`))) return;

    let ok = 0;
    let fail = 0;
    const failures = [];
    for (const raw of rows) {
      const payload = parseRow(raw);
      const id = payload.id;
      delete payload.id;
      try {
        const url = id ? `/api/admin/packages/${id}` : '/api/admin/packages';
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
            <PackageIcon size={22} className="text-primary" /> 销售套餐
          </h1>
          <p className="text-on-surface-variant mt-2 text-sm">
            组合配额计划 + 定价 + 周期 + 叠加策略。支持 CSV 批量导入/导出。
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <button
            type="button"
            onClick={exportCSV}
            className="h-10 px-4 bg-surface-container-high border border-outline-variant rounded-control text-sm flex items-center gap-1.5 hover:bg-surface-variant"
            title="导出当前所有套餐为 CSV（Excel 可直接打开）"
          >
            <Download size={14} /> 导出 CSV
          </button>
          <button
            type="button"
            onClick={importCSV}
            className="h-10 px-4 bg-surface-container-high border border-outline-variant rounded-control text-sm flex items-center gap-1.5 hover:bg-surface-variant"
            title="从 CSV 批量导入套餐（有 id 则更新，无 id 则新建）"
          >
            <Upload size={14} /> 导入 CSV
          </button>
          <button
            type="button"
            onClick={startCreate}
            className="h-10 px-4 bg-primary text-on-primary rounded-control flex items-center gap-1.5 hover:opacity-90 text-sm font-medium"
          >
            <Plus size={14} /> 新建套餐
          </button>
        </div>
      </div>

      {loading ? <div className="text-center py-20 text-on-surface-variant">加载中...</div>
        : pkgs.length === 0 ? (
          <div className="text-center py-16 bg-surface-container border border-outline-variant rounded-overlay">
            <p className="text-on-surface-variant text-sm">还没有套餐，点右上角创建</p>
          </div>
        ) : (
          <SortableGrid
            items={pkgs}
            getId={(p) => p.id}
            onReorder={async (newOrderIds, newItems) => {
              const oldItems = pkgs;
              setPkgs(newItems);
              try {
                const res = await authFetch('/api/admin/packages/reorder', {
                  method: 'POST',
                  body: JSON.stringify({ ids: newOrderIds })
                });
                if (res.success) {
                  toast.success('排序已保存');
                } else {
                  setPkgs(oldItems);
                  toast.error(res.message || '排序保存失败');
                }
              } catch (e) {
                setPkgs(oldItems);
                toast.error('网络异常，排序失败');
              }
            }}
            renderItem={(p, dragHandleProps) => (
              <div className="bg-surface-container border border-outline-variant rounded-overlay p-5 h-full">
                <div className="flex items-start justify-between mb-3">
                  <div className="flex gap-2">
                    <GripHandle {...dragHandleProps} className="shrink-0 -ml-2 -mt-1 cursor-grab active:cursor-grabbing text-on-surface-variant hover:text-on-surface p-1 rounded-control hover:bg-on-surface/[0.04]" />
                    <div className="flex-1 min-w-0">
                      <div className="font-bold text-on-surface flex items-center gap-2">
                        {p.name}
                        {p.highlight_tag && <span className="text-[10px] px-1.5 py-0.5 rounded-control bg-primary/20 text-primary">{p.highlight_tag}</span>}
                      </div>
                      <div className="text-xs text-outline mt-0.5">
                        {p.price_currency} {p.price_amount} / {formatDuration(p.billing_period_seconds)}
                      </div>
                    </div>
                  </div>
                  <div className="flex gap-1 shrink-0">
                    <button onClick={() => startEdit(p)} className="p-1.5 text-on-surface-variant hover:text-primary"><Edit size={14} /></button>
                    <button onClick={() => remove(p)} className="p-1.5 text-on-surface-variant hover:text-error"><Trash2 size={14} /></button>
                  </div>
                </div>
                <div className="space-y-1 text-xs text-on-surface-variant">
                  <div>叠加: {p.stackable ? `允许 (上限 ${p.max_active_per_user || '∞'})` : '不允许'}</div>
                  <div>计划数: {p.plan_count || 0} · 活跃订阅: {p.active_subs_count || 0}</div>
                </div>
                <div className="mt-3 pt-3 border-t border-outline-variant/30 flex items-center justify-between text-xs">
                  <span className={p.public ? 'text-success' : 'text-warning'}>{p.public ? '● 公开销售' : '○ 内部'}</span>
                  <span className={p.enabled ? 'text-success' : 'text-outline'}>{p.enabled ? '启用' : '禁用'}</span>
                </div>
              </div>
            )}
          />
        )}

      {editing && (
        <div
          ref={editModalRef}
          role="dialog"
          aria-modal="true"
          aria-labelledby="package-edit-modal-title"
          onClick={onEditBackdropClick}
          className="fixed inset-0 z-[100] flex items-start sm:items-center justify-center p-2 sm:p-4 bg-black/80 backdrop-blur-sm"
        >
          <div className="relative w-full max-w-4xl bg-surface-container border border-outline-variant rounded-overlay flex flex-col max-h-[92vh] shadow-2xl shadow-black/40">
            {/* 标题栏（固定不滚动） */}
            <div className="flex items-center justify-between px-4 sm:px-6 py-4 border-b border-outline-variant/60 shrink-0">
              <h2 id="package-edit-modal-title" className="text-lg font-bold text-on-surface">{editing.id ? '编辑套餐' : '新建套餐'}</h2>
              <button ref={editCloseBtnRef} type="button" onClick={cancel} aria-label="关闭" className="text-on-surface-variant hover:text-on-surface p-1 rounded-control">
                <X size={18} />
              </button>
            </div>

            {/* 表单（独立滚动区域） */}
            <div className="px-4 sm:px-6 py-5 space-y-6 overflow-y-auto flex-1 min-h-0">
              {/* 基础信息 */}
              <Section title="基础信息">
                <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
                  <Field label="名称" required>
                    <input className={inputCls} value={editing.name} onChange={e => updateField('name', e.target.value)} />
                  </Field>
                  <Field label="徽章标签" hint="如 🔥 热门 / 新品">
                    <input className={inputCls} value={editing.highlight_tag} onChange={e => updateField('highlight_tag', e.target.value)} />
                  </Field>
                </div>
                <Field label="描述" hint="用户可见富文本">
                  <textarea className={inputCls + ' min-h-[72px]'} value={editing.description} onChange={e => updateField('description', e.target.value)} />
                </Field>
              </Section>

              {/* 计费 */}
              <Section title="计费与周期">
                <div className="grid grid-cols-1 sm:grid-cols-3 gap-3">
                  <Field label="价格">
                    <input type="number" step="0.01" min="0" className={inputCls} value={editing.price_amount}
                      onChange={e => updateField('price_amount', parseFloat(e.target.value) || 0)} />
                  </Field>
                  <Field label="货币">
                    <input className={inputCls} value={editing.price_currency} onChange={e => updateField('price_currency', e.target.value)} />
                  </Field>
                  {/* 首单价字段已彻底移除 — 任何"优惠"通过独立的优惠券系统实现：
                      admin 在「优惠券模板」tab 创建模板 → 给特定用户发券 → 用户购买时选用 */}
                </div>
                <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
                  <Field label="计费周期" hint={`当前 = ${formatDuration(editing.billing_period_seconds)}`}>
                    <DurationInput
                      value={editing.billing_period_seconds}
                      onChange={(sec) => updateField('billing_period_seconds', sec)}
                      className={inputCls}
                      selectClass={inputCls}
                    />
                  </Field>
                </div>
              </Section>

              {/* 叠加策略 */}
              <Section title="叠加策略">
                <div className="grid grid-cols-1 sm:grid-cols-3 gap-3">
                  <Field label="允许叠加">
                    <select className={inputCls} value={editing.stackable ? '1' : '0'}
                      onChange={e => updateField('stackable', e.target.value === '1')}>
                      <option value="1">是</option>
                      <option value="0">否</option>
                    </select>
                  </Field>
                  <Field label="叠加上限/人" hint="0 = 无限">
                    <input type="number" className={inputCls} value={editing.max_active_per_user}
                      onChange={e => updateField('max_active_per_user', parseInt(e.target.value) || 0)} />
                  </Field>
                  <Field label="已持有时购买行为">
                    <select className={inputCls} value={editing.purchase_when_owned}
                      onChange={e => updateField('purchase_when_owned', e.target.value)}>
                      <option value="ask">弹窗询问</option>
                      <option value="stack">自动叠加</option>
                      <option value="extend">自动续期</option>
                    </select>
                  </Field>
                </div>
              </Section>

              {/* 配额计划组合 */}
              <Section title="配额计划组合" hint={allPlans.length === 0 ? '尚未创建任何配额计划' : `已选 ${(editing.plan_ids || []).length} / ${allPlans.length}`}>
                {allPlans.length === 0 ? (
                  <div className="text-xs text-on-surface-variant text-center py-6 border border-dashed border-outline-variant rounded-control">
                    去"配额计划库"先创建几个计划
                  </div>
                ) : (
                  <div className="grid grid-cols-1 md:grid-cols-2 gap-2 max-h-64 overflow-y-auto pr-1">
                    {allPlans.map(plan => {
                      const idx = (editing.plan_ids || []).indexOf(plan.id);
                      const checked = idx >= 0;
                      return (
                        <label key={plan.id} className={`flex items-center gap-2 p-2 rounded-control border cursor-pointer transition ${checked ? 'border-primary bg-primary/5' : 'border-outline-variant/40 hover:border-outline-variant'}`}>
                          <input type="checkbox" checked={checked} onChange={() => togglePlan(plan.id)} />
                          <div className="flex-1 min-w-0">
                            <div className="text-sm text-on-surface truncate">{plan.display_name || plan.name}</div>
                            <div className="text-xs text-on-surface-variant">{plan.limit_unit} × {plan.limit_value} · 窗口 {plan.window_seconds === 0 ? '套餐内' : formatDuration(plan.window_seconds)}</div>
                          </div>
                          {checked && (
                            <input
                              type="number"
                              step="0.1"
                              className="w-16 h-7 text-xs bg-surface border border-outline-variant rounded-control px-2"
                              value={editing.plan_multipliers[idx] || 1.0}
                              onChange={e => setMultiplier(idx, e.target.value)}
                              onClick={(e) => e.preventDefault()}
                              title="数量倍数"
                            />
                          )}
                        </label>
                      );
                    })}
                  </div>
                )}
              </Section>

              {/* 上架/可见性 */}
              <Section title="上架与可见性">
                <div className="grid grid-cols-1 sm:grid-cols-3 gap-3">
                  <Field label="排序权重" hint="小先显示">
                    <input type="number" className={inputCls} value={editing.sort_order}
                      onChange={e => updateField('sort_order', parseInt(e.target.value) || 0)} />
                  </Field>
                  <Field label="公开销售">
                    <select className={inputCls} value={editing.public ? '1' : '0'}
                      onChange={e => updateField('public', e.target.value === '1')}>
                      <option value="0">否（仅内部）</option>
                      <option value="1">是</option>
                    </select>
                  </Field>
                  <Field label="启用">
                    <select className={inputCls} value={editing.enabled ? '1' : '0'}
                      onChange={e => updateField('enabled', e.target.value === '1')}>
                      <option value="1">是</option>
                      <option value="0">否</option>
                    </select>
                  </Field>
                </div>
              </Section>

              {/* 高级 */}
              <Section title="高级" collapsible>
                <Field label="自由扩展配置" hint="JSON 格式，按需写">
                  <textarea className={inputCls + ' font-mono min-h-[60px] text-xs'}
                    value={editing.extra_config}
                    onChange={e => updateField('extra_config', e.target.value)} />
                </Field>
              </Section>
            </div>

            {/* 操作栏（固定不滚动） */}
            <div className="flex justify-end gap-2 px-4 sm:px-6 py-4 border-t border-outline-variant/60 bg-surface-container-low rounded-control-b-2xl shrink-0">
              <button type="button" onClick={cancel} className="px-4 py-2 bg-surface-container-high border border-outline-variant rounded-control text-sm hover:bg-surface-variant">
                取消
              </button>
              <button type="button" onClick={save} disabled={saving}
                className="px-5 py-2 bg-primary text-on-primary rounded-control text-sm font-medium flex items-center gap-1.5 disabled:opacity-50 hover:opacity-90">
                <Save size={14} /> {saving ? '保存中…' : '保存'}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
};

const inputCls = 'w-full h-10 bg-surface border border-outline-variant rounded-control px-3 text-sm text-on-surface outline-none focus:border-primary';

// 表单分组：可选 collapsible，统一标题 + 内边距
const Section = ({ title, hint, collapsible = false, children }) => {
  const [open, setOpen] = useState(!collapsible);
  return (
    <div className="rounded-control border border-outline-variant bg-surface-container-high">
      <button
        type="button"
        onClick={() => collapsible && setOpen(!open)}
        className={`w-full flex items-center justify-between px-4 py-2.5 ${collapsible ? 'cursor-pointer hover:bg-surface-variant/30' : 'cursor-default'}`}
      >
        <div className="flex items-center gap-2">
          <span className="text-sm font-semibold text-on-surface">{title}</span>
          {hint && <span className="text-xs text-on-surface-variant">{hint}</span>}
        </div>
        {collapsible && (
          <span className="text-xs text-on-surface-variant">{open ? '收起' : '展开'}</span>
        )}
      </button>
      {open && <div className="px-4 pb-4 pt-2 space-y-3">{children}</div>}
    </div>
  );
};

const Field = ({ label, hint, required, children }) => {
  const id = React.useId();
  const enhancedChildren = React.Children.map(children, (child) => {
    if (React.isValidElement(child) && !child.props.id) {
      return React.cloneElement(child, { id });
    }
    return child;
  });
  const hintId = hint ? `${id}-hint` : undefined;
  return (
    <div className="space-y-1 min-w-0">
      <label htmlFor={id} className="block text-xs font-medium text-on-surface-variant">
        {label}
        {required && <span className="text-error ml-0.5">*</span>}
      </label>
      {enhancedChildren && enhancedChildren.length > 0 && hint
        ? React.Children.map(enhancedChildren, (child) =>
            React.isValidElement(child) ? React.cloneElement(child, { 'aria-describedby': hintId }) : child)
        : enhancedChildren}
      {hint && <div id={hintId} className="text-[10px] text-on-surface-variant/80">{hint}</div>}
    </div>
  );
};

export default PackageManagement;
