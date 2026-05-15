import React, { useState, useEffect, useCallback, useRef } from 'react';
import { Package as PackageIcon, Plus, Edit, Trash2, X, Save, Download, Upload } from 'lucide-react';
import toast from 'react-hot-toast';
import { useTranslation } from 'react-i18next';
import { useConfirm } from '../context/ConfirmContext';
import { authFetch } from '../utils/authFetch';
import { logger } from '../utils/logger';
import { SortableGrid, GripHandle } from './ui';
import { toCSV, downloadCSV, parseCSV, pickCSVFile } from '../utils/csv';
import { useModalA11y } from '../hooks/useModalA11y';

// CSV columns are shared by import/export to keep headers stable.
const CSV_COLUMNS = [
  'id', 'name', 'description', 'highlight_tag',
  'price_amount', 'cost_floor_micro_usd', 'price_currency', 'billing_period_seconds',
  'stackable', 'max_active_per_user', 'purchase_when_owned',
  'public', 'sort_order', 'enabled',
  'plan_ids', 'plan_multipliers', 'extra_config',
];

// Parse CSV cells into package field types.
const parseRow = (r) => ({
  id: r.id ? Number(r.id) : undefined,
  name: r.name || '',
  description: r.description || '',
  highlight_tag: r.highlight_tag || '',
  icon_key: 'Package',
  badge_color: '',
  gradient: '',
  price_amount: parseFloat(r.price_amount) || 0,
  cost_floor_micro_usd: parseInt(r.cost_floor_micro_usd) || 0,
  price_currency: r.price_currency || 'USD',
  billing_period_seconds: parseInt(r.billing_period_seconds) || 2592000,
  stackable: r.stackable === '1' || r.stackable === 'true',
  max_active_per_user: parseInt(r.max_active_per_user) || 0,
  purchase_when_owned: r.purchase_when_owned || 'ask',
  public: r.public === '1' || r.public === 'true',
  sort_order: parseInt(r.sort_order) || 0,
  enabled: r.enabled === '1' || r.enabled === 'true',
  extra_config: r.extra_config || '{}',
  plan_ids: (r.plan_ids || '').split('|').map((s) => parseInt(s.trim())).filter(Boolean),
  plan_multipliers: (r.plan_multipliers || '').split('|').map((s) => parseFloat(s.trim()) || 1).filter((n) => !isNaN(n)),
});

const EMPTY_PKG = {
  product_type: 'subscription',
  name: '', description: '',
  icon_key: 'Package', badge_color: '', gradient: '', highlight_tag: '',
  price_amount: 0, cost_floor_micro_usd: 0, price_currency: 'USD',
  billing_period_seconds: 2592000, // Default subscription period is 30 days.
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

const MICRO_PER_USD = 1000000;
const MAX_DURATION_SECONDS = 100 * 365 * 86400;
const DURATION_UNITS = [
  { key: 's', sec: 1 },
  { key: 'm', sec: 60 },
  { key: 'h', sec: 3600 },
  { key: 'd', sec: 86400 },
  { key: 'w', sec: 604800 },
  { key: 'mo', sec: 2592000 },
];

const pickUnitForSec = (totalSec) => {
  if (!totalSec || totalSec <= 0) return 's';
  for (let i = DURATION_UNITS.length - 1; i >= 0; i--) {
    if (totalSec % DURATION_UNITS[i].sec === 0) return DURATION_UNITS[i].key;
  }
  return 's';
};

const durationUnitMeta = (key) => DURATION_UNITS.find((u) => u.key === key) || DURATION_UNITS[0];

const durationSelectLabel = (key, t) => {
  switch (key) {
    case 'm':
      return t('PACKAGE_MGMT.UNIT_MINUTE_SELECT', '分');
    case 'h':
      return t('PACKAGE_MGMT.UNIT_HOUR_SELECT', '小时');
    case 'd':
      return t('PACKAGE_MGMT.UNIT_DAY_SELECT', '天');
    case 'w':
      return t('PACKAGE_MGMT.UNIT_WEEK_SELECT', '周');
    case 'mo':
      return t('PACKAGE_MGMT.UNIT_MONTH_SELECT', '月');
    case 's':
    default:
      return t('PACKAGE_MGMT.UNIT_SECOND_SELECT', '秒');
  }
};

const durationValueLabel = (key, value, t) => {
  const one = Number(value) === 1;
  switch (key) {
    case 'm':
      return one ? t('PACKAGE_MGMT.UNIT_MINUTE_ONE', '分钟') : t('PACKAGE_MGMT.UNIT_MINUTE_OTHER', '分钟');
    case 'h':
      return one ? t('PACKAGE_MGMT.UNIT_HOUR_ONE', '小时') : t('PACKAGE_MGMT.UNIT_HOUR_OTHER', '小时');
    case 'd':
      return one ? t('PACKAGE_MGMT.UNIT_DAY_ONE', '天') : t('PACKAGE_MGMT.UNIT_DAY_OTHER', '天');
    case 'w':
      return one ? t('PACKAGE_MGMT.UNIT_WEEK_ONE', '周') : t('PACKAGE_MGMT.UNIT_WEEK_OTHER', '周');
    case 'mo':
      return one ? t('PACKAGE_MGMT.UNIT_MONTH_ONE', '月') : t('PACKAGE_MGMT.UNIT_MONTH_OTHER', '月');
    case 's':
    default:
      return one ? t('PACKAGE_MGMT.UNIT_SECOND_ONE', '秒') : t('PACKAGE_MGMT.UNIT_SECOND_OTHER', '秒');
  }
};

const formatPackageDuration = (sec, t) => {
  const n = Math.floor(Number(sec) || 0);
  if (n <= 0) return '0';
  for (let i = DURATION_UNITS.length - 1; i >= 0; i--) {
    if (n % DURATION_UNITS[i].sec === 0) {
      const value = n / DURATION_UNITS[i].sec;
      return t('PACKAGE_MGMT.DURATION_VALUE', {
        value,
        unit: durationValueLabel(DURATION_UNITS[i].key, value, t),
        defaultValue: '{{value}} {{unit}}',
      });
    }
  }
  return t('PACKAGE_MGMT.DURATION_VALUE', {
    value: n,
    unit: durationValueLabel('s', n, t),
    defaultValue: '{{value}} {{unit}}',
  });
};

const usdToMicro = (value) => {
  const n = parseFloat(value);
  if (!Number.isFinite(n) || n <= 0) return 0;
  return Math.round(n * MICRO_PER_USD);
};

const microToUSDInput = (micro) => {
  const n = Number(micro || 0);
  if (!Number.isFinite(n) || n <= 0) return 0;
  return Number((n / MICRO_PER_USD).toFixed(6));
};

const formatCostFloorUSD = (micro, unrestrictedLabel) => {
  const n = Number(micro || 0);
  if (!Number.isFinite(n) || n <= 0) return unrestrictedLabel;
  return `$${(n / MICRO_PER_USD).toFixed(2)}`;
};

const PackageManagement = () => {
  const confirm = useConfirm();
  const { t } = useTranslation();
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
      toast.error(t('PACKAGE_MGMT.LOAD_FAIL', '加载失败'));
    } finally {
      setLoading(false);
    }
  }, [t]);

  useEffect(() => { load(); }, [load]);

  const startCreate = () => setEditing({ ...EMPTY_PKG });
  const startEdit = async (p) => {
    try {
      // authFetch already returns parsed JSON here.
      const json = await authFetch(`/api/admin/packages/${p.id}`);
      if (json.success) {
        const full = json.data;
        setEditing({
          ...EMPTY_PKG, ...full,
          plan_ids: (full.plans || []).map(pp => pp.quota_plan_id),
          plan_multipliers: (full.plans || []).map(pp => pp.quantity_multiplier),
        });
      } else {
        toast.error(json.message || t('PACKAGE_MGMT.LOAD_DETAIL_FAIL', '加载套餐详情失败'));
      }
    } catch (e) {
      logger.error('[PackageManagement] startEdit failed', e);
      toast.error(t('PACKAGE_MGMT.LOAD_DETAIL_NETWORK_FAIL', '网络异常，无法加载套餐详情'));
    }
  };

  const cancel = () => setEditing(null);

  // a11y: move initial modal focus to the close button.
  const editCloseBtnRef = useRef(null);
  const editModalRef = useRef(null);
  const { onBackdropClick: onEditBackdropClick } = useModalA11y(!!editing, cancel, editCloseBtnRef, editModalRef);

  const save = async () => {
    if (!editing.name) { toast.error(t('PACKAGE_MGMT.NAME_REQUIRED', '名称必填')); return; }
    try { JSON.parse(editing.extra_config || '{}'); }
    catch { toast.error(t('PACKAGE_MGMT.EXTRA_CONFIG_INVALID', 'extra_config 必须是合法 JSON')); return; }
    const rawCostFloorMicro = Number(editing.cost_floor_micro_usd || 0);
    const costFloorMicro = Number.isFinite(rawCostFloorMicro) ? Math.trunc(rawCostFloorMicro) : 0;
    if (costFloorMicro < 0) {
      toast.error(t('PACKAGE_MGMT.COST_FLOOR_NEGATIVE', '成本下限不能为负数'));
      return;
    }
    if (costFloorMicro > usdToMicro(editing.price_amount)) {
      toast.error(t('PACKAGE_MGMT.COST_FLOOR_EXCEEDS_PRICE', '成本下限不能高于套餐售价'));
      return;
    }
    setSaving(true);
    const isNew = !editing.id;
    try {
      const payload = { ...editing, cost_floor_micro_usd: costFloorMicro };
      // Admin writes go through authFetch for consistent auth and error handling.
      const json = await authFetch(
        isNew ? '/api/admin/packages' : `/api/admin/packages/${editing.id}`,
        {
          method: isNew ? 'POST' : 'PUT',
          body: payload,
        }
      );
      if (json.success) {
        toast.success(isNew ? t('PACKAGE_MGMT.CREATE_OK', '已创建') : t('PACKAGE_MGMT.UPDATE_OK', '已更新'));
        setEditing(null);
        load();
      } else {
        toast.error(json.message || t('PACKAGE_MGMT.SAVE_FAIL', '保存失败'));
      }
    } catch {
      toast.error(t('PACKAGE_MGMT.NETWORK_ERROR', '网络异常'));
    } finally {
      setSaving(false);
    }
  };

  const remove = async (p) => {
    if (!(await confirm({
      level: 'L1',
      danger: true,
      message: t('PACKAGE_MGMT.DELETE_CONFIRM', {
        name: p.name,
        defaultValue: '删除套餐「{{name}}」？该套餐下的活跃订阅不受影响，但用户不能再续费',
      }),
    }))) return;
    try {
      // Admin writes go through authFetch for consistent auth.
      const json = await authFetch(`/api/admin/packages/${p.id}`, { method: 'DELETE' });
      if (json.success) { toast.success(t('PACKAGE_MGMT.DELETE_OK', '已删除')); load(); }
      else toast.error(json.message || t('PACKAGE_MGMT.DELETE_FAIL', '删除失败'));
    } catch {
      toast.error(t('PACKAGE_MGMT.DELETE_NETWORK_FAIL', '网络异常，删除失败'));
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

  // CSV import/export.
  const exportCSV = async () => {
    try {
      // Fetch complete package data, including attached quota plans.
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
        cost_floor_micro_usd: p.cost_floor_micro_usd || 0,
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
      toast.success(t('PACKAGE_MGMT.EXPORT_SUCCESS', {
        count: rows.length,
        defaultValue: '已导出 {{count}} 条',
      }));
    } catch (e) {
      toast.error(t('PACKAGE_MGMT.EXPORT_FAIL', {
        reason: e.message || t('PACKAGE_MGMT.UNKNOWN_ERROR', '未知错误'),
        defaultValue: '导出失败：{{reason}}',
      }));
    }
  };

  const importCSV = async () => {
    let text;
    try {
      text = await pickCSVFile();
    } catch {
      return;
    }
    const { rows } = parseCSV(text);
    if (rows.length === 0) {
      toast.error(t('PACKAGE_MGMT.CSV_EMPTY', 'CSV 为空或解析失败'));
      return;
    }
    if (!(await confirm(t('PACKAGE_MGMT.IMPORT_CONFIRM', {
      count: rows.length,
      defaultValue: '确认导入 {{count}} 条套餐？\n有 id 的按 id 更新，无 id 的新建。',
    })))) return;

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
          failures.push(t('PACKAGE_MGMT.IMPORT_FAILURE_ITEM', {
            item: raw.name || `#${id || '?'}`,
            reason: r.message || r.message_code || t('PACKAGE_MGMT.UNKNOWN_ERROR', '未知错误'),
            defaultValue: '「{{item}}」: {{reason}}',
          }));
        }
      } catch (e) {
        fail++;
        failures.push(t('PACKAGE_MGMT.IMPORT_FAILURE_ITEM', {
          item: raw.name || `#${id || '?'}`,
          reason: e.message || t('PACKAGE_MGMT.NETWORK_ERROR', '网络异常'),
          defaultValue: '「{{item}}」: {{reason}}',
        }));
      }
    }
    if (fail === 0) {
      toast.success(t('PACKAGE_MGMT.IMPORT_SUCCESS', {
        count: ok,
        defaultValue: '导入成功 {{count}} 条',
      }));
    } else if (ok === 0) {
      toast.error(t('PACKAGE_MGMT.IMPORT_ALL_FAILED', {
        fail,
        details: failures.slice(0, 3).join('\n'),
        defaultValue: '全部 {{fail}} 条导入失败：\n{{details}}',
      }), { duration: 8000 });
    } else {
      toast.error(t('PACKAGE_MGMT.IMPORT_PARTIAL_FAILED', {
        ok,
        fail,
        details: failures.slice(0, 3).join('\n'),
        defaultValue: '部分失败：成功 {{ok}}，失败 {{fail}}\n{{details}}',
      }), { duration: 8000 });
    }
    load();
  };

  return (
    <div className="w-full">
      <div className="mb-8 border-b border-outline-variant pb-6 flex flex-col md:flex-row md:items-end md:justify-between gap-4">
        <div>
          <h1 className="text-xl md:text-2xl font-bold text-on-surface flex items-center gap-3">
            <PackageIcon size={22} className="text-primary" /> {t('PACKAGE_MGMT.TITLE', '销售套餐')}
          </h1>
          <p className="text-on-surface-variant mt-2 text-sm">
            {t('PACKAGE_MGMT.DESCRIPTION', '组合配额计划 + 定价 + 周期 + 叠加策略。支持 CSV 批量导入/导出。')}
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <button
            type="button"
            onClick={exportCSV}
            className="h-10 px-4 bg-surface-container-high border border-outline-variant rounded-control text-sm flex items-center gap-1.5 hover:bg-surface-variant"
            title={t('PACKAGE_MGMT.EXPORT_CSV_TITLE', '导出当前所有套餐为 CSV（Excel 可直接打开）')}
          >
            <Download size={14} /> {t('PACKAGE_MGMT.EXPORT_CSV', '导出 CSV')}
          </button>
          <button
            type="button"
            onClick={importCSV}
            className="h-10 px-4 bg-surface-container-high border border-outline-variant rounded-control text-sm flex items-center gap-1.5 hover:bg-surface-variant"
            title={t('PACKAGE_MGMT.IMPORT_CSV_TITLE', '从 CSV 批量导入套餐（有 id 则更新，无 id 则新建）')}
          >
            <Upload size={14} /> {t('PACKAGE_MGMT.IMPORT_CSV', '导入 CSV')}
          </button>
          <button
            type="button"
            onClick={startCreate}
            className="h-10 px-4 bg-primary text-on-primary rounded-control flex items-center gap-1.5 hover:opacity-90 text-sm font-medium"
          >
            <Plus size={14} /> {t('PACKAGE_MGMT.CREATE_PACKAGE', '新建套餐')}
          </button>
        </div>
      </div>

      {loading ? <div className="text-center py-20 text-on-surface-variant">{t('COMMON.LOADING', '加载中…')}</div>
        : pkgs.length === 0 ? (
          <div className="text-center py-16 bg-surface-container border border-outline-variant rounded-overlay">
            <p className="text-on-surface-variant text-sm">{t('PACKAGE_MGMT.EMPTY_STATE', '还没有套餐，点右上角创建')}</p>
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
                  toast.success(t('PACKAGE_MGMT.REORDER_OK', '排序已保存'));
                } else {
                  setPkgs(oldItems);
                  toast.error(res.message || t('PACKAGE_MGMT.REORDER_FAIL', '排序保存失败'));
                }
              } catch (e) {
                setPkgs(oldItems);
                toast.error(t('PACKAGE_MGMT.REORDER_NETWORK_FAIL', '网络异常，排序失败'));
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
                        {p.price_currency} {p.price_amount} / {formatPackageDuration(p.billing_period_seconds, t)}
                      </div>
                    </div>
                  </div>
                  <div className="flex gap-1 shrink-0">
                    <button
                      type="button"
                      onClick={() => startEdit(p)}
                      className="p-1.5 text-on-surface-variant hover:text-primary"
                      aria-label={t('PACKAGE_MGMT.EDIT_PACKAGE', '编辑套餐')}
                      title={t('PACKAGE_MGMT.EDIT_PACKAGE', '编辑套餐')}
                    >
                      <Edit size={14} />
                    </button>
                    <button
                      type="button"
                      onClick={() => remove(p)}
                      className="p-1.5 text-on-surface-variant hover:text-error"
                      aria-label={t('PACKAGE_MGMT.DELETE_PACKAGE', '删除套餐')}
                      title={t('PACKAGE_MGMT.DELETE_PACKAGE', '删除套餐')}
                    >
                      <Trash2 size={14} />
                    </button>
                  </div>
                </div>
                <div className="space-y-1 text-xs text-on-surface-variant">
                  <div>
                    {t('PACKAGE_MGMT.STACKING_LABEL', '叠加')}:{' '}
                    {p.stackable
                      ? t('PACKAGE_MGMT.STACKING_ALLOWED_WITH_LIMIT', {
                          limit: p.max_active_per_user || '∞',
                          defaultValue: '允许 (上限 {{limit}})',
                        })
                      : t('PACKAGE_MGMT.STACKING_NOT_ALLOWED', '不允许')}
                  </div>
                  <div>
                    {t('PACKAGE_MGMT.COST_FLOOR_LABEL', '成本下限')}:{' '}
                    {formatCostFloorUSD(p.cost_floor_micro_usd, t('PACKAGE_MGMT.UNRESTRICTED', '不限制'))}
                  </div>
                  <div>
                    {t('PACKAGE_MGMT.PLAN_AND_ACTIVE_COUNT', {
                      planCount: p.plan_count || 0,
                      activeCount: p.active_subs_count || 0,
                      defaultValue: '计划数: {{planCount}} · 活跃订阅: {{activeCount}}',
                    })}
                  </div>
                </div>
                <div className="mt-3 pt-3 border-t border-outline-variant/30 flex items-center justify-between text-xs">
                  <span className={p.public ? 'text-success' : 'text-warning'}>
                    {p.public ? t('PACKAGE_MGMT.STATUS_PUBLIC', '● 公开销售') : t('PACKAGE_MGMT.STATUS_INTERNAL', '○ 内部')}
                  </span>
                  <span className={p.enabled ? 'text-success' : 'text-outline'}>
                    {p.enabled ? t('PACKAGE_MGMT.STATUS_ENABLED', '启用') : t('PACKAGE_MGMT.STATUS_DISABLED', '禁用')}
                  </span>
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
            {/* Fixed modal header. */}
            <div className="flex items-center justify-between px-4 sm:px-6 py-4 border-b border-outline-variant/60 shrink-0">
              <h2 id="package-edit-modal-title" className="text-lg font-bold text-on-surface">
                {editing.id ? t('PACKAGE_MGMT.EDIT_PACKAGE', '编辑套餐') : t('PACKAGE_MGMT.CREATE_PACKAGE', '新建套餐')}
              </h2>
              <button ref={editCloseBtnRef} type="button" onClick={cancel} aria-label={t('COMMON.CLOSE', '关闭')} className="text-on-surface-variant hover:text-on-surface p-1 rounded-control">
                <X size={18} />
              </button>
            </div>

            {/* Scrollable form body. */}
            <div className="px-4 sm:px-6 py-5 space-y-6 overflow-y-auto flex-1 min-h-0">
              {/* Basic information. */}
              <Section title={t('PACKAGE_MGMT.SECTION_BASIC', '基础信息')}>
                <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
                  <Field label={t('PACKAGE_MGMT.FIELD_NAME', '名称')} required>
                    <input className={inputCls} value={editing.name} onChange={e => updateField('name', e.target.value)} />
                  </Field>
                  <Field label={t('PACKAGE_MGMT.FIELD_BADGE', '徽章标签')} hint={t('PACKAGE_MGMT.FIELD_BADGE_HINT', '如 🔥 热门 / 新品')}>
                    <input className={inputCls} value={editing.highlight_tag} onChange={e => updateField('highlight_tag', e.target.value)} />
                  </Field>
                </div>
                <Field label={t('PACKAGE_MGMT.FIELD_DESCRIPTION', '描述')} hint={t('PACKAGE_MGMT.FIELD_DESCRIPTION_HINT', '用户可见富文本')}>
                  <textarea className={inputCls + ' min-h-[72px]'} value={editing.description} onChange={e => updateField('description', e.target.value)} />
                </Field>
              </Section>

              {/* Billing. */}
              <Section title={t('PACKAGE_MGMT.SECTION_BILLING', '计费与周期')}>
                <div className="grid grid-cols-1 sm:grid-cols-3 gap-3">
                  <Field label={t('PACKAGE_MGMT.FIELD_PRICE', '价格')}>
                    <input type="number" step="0.01" min="0" className={inputCls} value={editing.price_amount}
                      onChange={e => updateField('price_amount', parseFloat(e.target.value) || 0)} />
                  </Field>
                  <Field
                    label={t('PACKAGE_MGMT.FIELD_COST_FLOOR', '成本下限')}
                    hint={t('PACKAGE_MGMT.FIELD_COST_FLOOR_HINT', 'admin 估算的套餐上游真实成本下限。配置后系统会防止 admin 创建低于此值的 fixed_price 优惠券，避免亏损。0 = 不限制（仅由全局 couponMinFixedPriceMicroUSD 兜底）')}
                  >
                    <input
                      type="number"
                      step="0.01"
                      min="0"
                      className={inputCls}
                      value={microToUSDInput(editing.cost_floor_micro_usd)}
                      onChange={e => updateField('cost_floor_micro_usd', usdToMicro(e.target.value))}
                    />
                  </Field>
                  <Field label={t('PACKAGE_MGMT.FIELD_CURRENCY', '货币')}>
                    <input className={inputCls} value={editing.price_currency} onChange={e => updateField('price_currency', e.target.value)} />
                  </Field>
                  {/* Introductory pricing is handled by coupon templates, not package fields. */}
                </div>
                <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
                  <Field
                    label={t('PACKAGE_MGMT.FIELD_BILLING_PERIOD', '计费周期')}
                    hint={t('PACKAGE_MGMT.FIELD_BILLING_PERIOD_HINT', {
                      duration: formatPackageDuration(editing.billing_period_seconds, t),
                      defaultValue: '当前 = {{duration}}',
                    })}
                  >
                    <BillingPeriodInput
                      value={editing.billing_period_seconds}
                      onChange={(sec) => updateField('billing_period_seconds', sec)}
                      className={inputCls}
                      selectClass={inputCls}
                      t={t}
                    />
                  </Field>
                </div>
              </Section>

              {/* Stacking rules. */}
              <Section title={t('PACKAGE_MGMT.SECTION_STACKING', '叠加策略')}>
                <div className="grid grid-cols-1 sm:grid-cols-3 gap-3">
                  <Field label={t('PACKAGE_MGMT.FIELD_STACKABLE', '允许叠加')}>
                    <select className={inputCls} value={editing.stackable ? '1' : '0'}
                      onChange={e => updateField('stackable', e.target.value === '1')}>
                      <option value="1">{t('PACKAGE_MGMT.YES', '是')}</option>
                      <option value="0">{t('PACKAGE_MGMT.NO', '否')}</option>
                    </select>
                  </Field>
                  <Field label={t('PACKAGE_MGMT.FIELD_MAX_ACTIVE', '叠加上限/人')} hint={t('PACKAGE_MGMT.FIELD_MAX_ACTIVE_HINT', '0 = 无限')}>
                    <input type="number" className={inputCls} value={editing.max_active_per_user}
                      onChange={e => updateField('max_active_per_user', parseInt(e.target.value) || 0)} />
                  </Field>
                  <Field label={t('PACKAGE_MGMT.FIELD_PURCHASE_WHEN_OWNED', '已持有时购买行为')}>
                    <select className={inputCls} value={editing.purchase_when_owned}
                      onChange={e => updateField('purchase_when_owned', e.target.value)}>
                      <option value="ask">{t('PACKAGE_MGMT.PURCHASE_ASK', '弹窗询问')}</option>
                      <option value="stack">{t('PACKAGE_MGMT.PURCHASE_STACK', '自动叠加')}</option>
                      <option value="extend">{t('PACKAGE_MGMT.PURCHASE_EXTEND', '自动续期')}</option>
                    </select>
                  </Field>
                </div>
              </Section>

              {/* Quota plan bundle. */}
              <Section
                title={t('PACKAGE_MGMT.SECTION_PLANS', '配额计划组合')}
                hint={allPlans.length === 0
                  ? t('PACKAGE_MGMT.NO_QUOTA_PLANS', '尚未创建任何配额计划')
                  : t('PACKAGE_MGMT.SELECTED_PLANS_COUNT', {
                      selected: (editing.plan_ids || []).length,
                      total: allPlans.length,
                      defaultValue: '已选 {{selected}} / {{total}}',
                    })}
              >
                {allPlans.length === 0 ? (
                  <div className="text-xs text-on-surface-variant text-center py-6 border border-dashed border-outline-variant rounded-control">
                    {t('PACKAGE_MGMT.CREATE_QUOTA_PLANS_FIRST', '去"配额计划库"先创建几个计划')}
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
                            <div className="text-xs text-on-surface-variant">
                              {t('PACKAGE_MGMT.PLAN_WINDOW_LINE', {
                                unit: plan.limit_unit,
                                value: plan.limit_value,
                                window: plan.window_seconds === 0
                                  ? t('PACKAGE_MGMT.IN_PACKAGE_WINDOW', '套餐内')
                                  : formatPackageDuration(plan.window_seconds, t),
                                defaultValue: '{{unit}} × {{value}} · 窗口 {{window}}',
                              })}
                            </div>
                          </div>
                          {checked && (
                            <input
                              type="number"
                              step="0.1"
                              className="w-16 h-7 text-xs bg-surface border border-outline-variant rounded-control px-2"
                              value={editing.plan_multipliers[idx] || 1.0}
                              onChange={e => setMultiplier(idx, e.target.value)}
                              onClick={(e) => e.preventDefault()}
                              title={t('PACKAGE_MGMT.MULTIPLIER_TITLE', '数量倍数')}
                            />
                          )}
                        </label>
                      );
                    })}
                  </div>
                )}
              </Section>

              {/* Availability. */}
              <Section title={t('PACKAGE_MGMT.SECTION_VISIBILITY', '上架与可见性')}>
                <div className="grid grid-cols-1 sm:grid-cols-3 gap-3">
                  <Field label={t('PACKAGE_MGMT.FIELD_SORT_ORDER', '排序权重')} hint={t('PACKAGE_MGMT.FIELD_SORT_ORDER_HINT', '小先显示')}>
                    <input type="number" className={inputCls} value={editing.sort_order}
                      onChange={e => updateField('sort_order', parseInt(e.target.value) || 0)} />
                  </Field>
                  <Field label={t('PACKAGE_MGMT.FIELD_PUBLIC', '公开销售')}>
                    <select className={inputCls} value={editing.public ? '1' : '0'}
                      onChange={e => updateField('public', e.target.value === '1')}>
                      <option value="0">{t('PACKAGE_MGMT.NO_INTERNAL_ONLY', '否（仅内部）')}</option>
                      <option value="1">{t('PACKAGE_MGMT.YES', '是')}</option>
                    </select>
                  </Field>
                  <Field label={t('PACKAGE_MGMT.FIELD_ENABLED', '启用')}>
                    <select className={inputCls} value={editing.enabled ? '1' : '0'}
                      onChange={e => updateField('enabled', e.target.value === '1')}>
                      <option value="1">{t('PACKAGE_MGMT.YES', '是')}</option>
                      <option value="0">{t('PACKAGE_MGMT.NO', '否')}</option>
                    </select>
                  </Field>
                </div>
              </Section>

              {/* Advanced settings. */}
              <Section title={t('PACKAGE_MGMT.SECTION_ADVANCED', '高级')} collapsible>
                <Field label={t('PACKAGE_MGMT.FIELD_EXTRA_CONFIG', '自由扩展配置')} hint={t('PACKAGE_MGMT.FIELD_EXTRA_CONFIG_HINT', 'JSON 格式，按需写')}>
                  <textarea className={inputCls + ' font-mono min-h-[60px] text-xs'}
                    value={editing.extra_config}
                    onChange={e => updateField('extra_config', e.target.value)} />
                </Field>
              </Section>
            </div>

            {/* Fixed action bar. */}
            <div className="flex justify-end gap-2 px-4 sm:px-6 py-4 border-t border-outline-variant/60 bg-surface-container-low rounded-control-b-2xl shrink-0">
              <button type="button" onClick={cancel} className="px-4 py-2 bg-surface-container-high border border-outline-variant rounded-control text-sm hover:bg-surface-variant">
                {t('COMMON.CANCEL', '取消')}
              </button>
              <button type="button" onClick={save} disabled={saving}
                className="px-5 py-2 bg-primary text-on-primary rounded-control text-sm font-medium flex items-center gap-1.5 disabled:opacity-50 hover:opacity-90">
                <Save size={14} /> {saving ? t('COMMON.SAVING', '保存中...') : t('COMMON.SAVE', '保存')}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
};

const inputCls = 'w-full h-10 bg-surface border border-outline-variant rounded-control px-3 text-sm text-on-surface outline-none focus:border-primary';

const BillingPeriodInput = ({ value, onChange, className = '', selectClass = '', t }) => {
  const totalSec = Math.max(0, Math.min(MAX_DURATION_SECONDS, Math.floor(Number(value) || 0)));
  const [unitKey, setUnitKey] = useState(() => pickUnitForSec(totalSec));
  const selectedMeta = durationUnitMeta(unitKey);
  const effectiveUnitKey = totalSec % selectedMeta.sec === 0 ? unitKey : pickUnitForSec(totalSec);
  const meta = durationUnitMeta(effectiveUnitKey);
  const displayValue = totalSec === 0 ? 0 : Number((totalSec / meta.sec).toFixed(6));
  const baseInputCls =
    'w-full rounded-control bg-surface border border-outline-variant text-on-surface text-sm px-3 py-2 focus:outline-none focus:border-primary';

  const handleNumberChange = (raw) => {
    if (raw === '' || raw === '-') {
      onChange(0);
      return;
    }
    const num = parseFloat(raw);
    if (!Number.isFinite(num) || num < 0) return;
    const newSec = Math.max(0, Math.min(MAX_DURATION_SECONDS, Math.round(num * meta.sec)));
    onChange(newSec === 0 ? meta.sec : newSec);
  };

  return (
    <div className="flex w-full min-w-0 gap-1">
      <input
        type="number"
        inputMode="numeric"
        min={1}
        step="1"
        max={MAX_DURATION_SECONDS}
        className={`${className || baseInputCls} flex-1 min-w-0`}
        value={displayValue}
        onChange={(e) => handleNumberChange(e.target.value)}
      />
      <select
        aria-label={t('PACKAGE_MGMT.DURATION_UNIT_ARIA', '单位')}
        className={`${selectClass || baseInputCls} w-14 sm:w-16 shrink-0 px-1`}
        value={effectiveUnitKey}
        onChange={(e) => setUnitKey(e.target.value)}
      >
        {DURATION_UNITS.map((u) => (
          <option key={u.key} value={u.key}>
            {durationSelectLabel(u.key, t)}
          </option>
        ))}
      </select>
    </div>
  );
};

// Form section with optional collapse support.
const Section = ({ title, hint, collapsible = false, children }) => {
  const { t } = useTranslation();
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
          <span className="text-xs text-on-surface-variant">
            {open ? t('PACKAGE_MGMT.COLLAPSE', '收起') : t('PACKAGE_MGMT.EXPAND', '展开')}
          </span>
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
