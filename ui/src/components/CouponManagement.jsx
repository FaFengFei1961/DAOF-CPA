import React, { useState, useEffect, useCallback, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { Ticket, Plus, Edit, Trash2, X, Save } from 'lucide-react';
import toast from 'react-hot-toast';
import { useConfirm } from '../context/ConfirmContext';
import { authFetch } from '../utils/authFetch';
import { useModalA11y } from '../hooks/useModalA11y';

/**
 * CouponManagement — admin 端优惠券模板 CRUD
 *
 * 模板定义"券蓝本"（折扣类型 + 值 + 适用 package + 有效期）；
 * 实际发给用户在 AdminGrantCouponModal（按用户发）或注册自动发（SysConfig 配置）。
 */

const EMPTY_TEMPLATE = {
  name: '',
  description: '',
  discount_type: 'fixed_price',
  discount_value: 0,
  package_ids: '', // JSON 数组字符串 "[1,2,3]" 或 ""=全部
  valid_days: 0,
  enabled: true,
};

const CouponManagement = () => {
  const { t } = useTranslation();
  const confirm = useConfirm();
  const [list, setList] = useState([]);
  const [packages, setPackages] = useState([]);
  const [loading, setLoading] = useState(true);
  const [editing, setEditing] = useState(null);
  const [saving, setSaving] = useState(false);

  const closeBtnRef = useRef(null);
  const modalRef = useRef(null); // C-F1 第二十一轮: focus trap 范围
  const isOpen = !!editing;
  const { onBackdropClick } = useModalA11y(isOpen, () => !saving && setEditing(null), closeBtnRef, modalRef);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const [j1, j2] = await Promise.all([
        authFetch('/api/admin/coupon-templates'),
        authFetch('/api/admin/packages'),
      ]);
      if (j1?.success) setList(j1.data || []);
      else toast.error(j1?.message || t('COUPON.LOAD_FAIL', '加载失败'));
      if (j2?.success) setPackages((j2.data || []).filter((p) => p.enabled !== false));
    } catch {
      toast.error(t('API.ERR_NETWORK', '网络异常'));
    } finally {
      setLoading(false);
    }
  }, [t]);

  useEffect(() => { load(); }, [load]);

  const updateField = (k, v) => setEditing((prev) => ({ ...prev, [k]: v }));

  const togglePackage = (pkgId) => {
    let ids = [];
    try {
      ids = JSON.parse(editing.package_ids || '[]');
      if (!Array.isArray(ids)) ids = [];
    } catch { ids = []; }
    if (ids.includes(pkgId)) ids = ids.filter((x) => x !== pkgId);
    else ids.push(pkgId);
    updateField('package_ids', ids.length === 0 ? '' : JSON.stringify(ids));
  };

  const isPackageSelected = (pkgId) => {
    try {
      const ids = JSON.parse(editing?.package_ids || '[]');
      return Array.isArray(ids) && ids.includes(pkgId);
    } catch { return false; }
  };

  const onSave = async () => {
    // 客户端校验
    if (!editing.name?.trim()) {
      toast.error(t('COUPON.NAME_REQUIRED', '名称必填'));
      return;
    }
    if (editing.discount_value < 0) {
      toast.error(t('COUPON.VALUE_NEGATIVE', '优惠金额不能为负'));
      return;
    }
    if (editing.valid_days < 0) {
      toast.error(t('COUPON.VALID_DAYS_NEGATIVE', '有效天数不能为负'));
      return;
    }
    setSaving(true);
    try {
      const url = editing.id
        ? `/api/admin/coupon-templates/${editing.id}`
        : '/api/admin/coupon-templates';
      const method = editing.id ? 'PUT' : 'POST';
      const body = { ...editing };
      const j = await authFetch(url, { method, body });
      if (j?.success) {
        toast.success(t('COUPON.SAVE_OK', '保存成功'));
        setEditing(null);
        load();
      } else {
        toast.error(j?.message || t('COUPON.SAVE_FAIL', '保存失败'));
      }
    } catch {
      toast.error(t('API.ERR_NETWORK', '网络异常'));
    } finally {
      setSaving(false);
    }
  };

  const onDelete = async (item) => {
    if (!(await confirm(t('COUPON.DELETE_CONFIRM', `确认删除模板「${item.name}」？已发出的券不受影响。`)))) return;
    try {
      const j = await authFetch(`/api/admin/coupon-templates/${item.id}`, { method: 'DELETE' });
      if (j?.success) {
        toast.success(t('COUPON.DELETE_OK', '已删除'));
        load();
      } else {
        toast.error(j?.message || t('COUPON.DELETE_FAIL', '删除失败'));
      }
    } catch {
      toast.error(t('API.ERR_NETWORK', '网络异常'));
    }
  };

  const renderPackageScope = (template) => {
    const ids = (() => {
      try { const a = JSON.parse(template.package_ids || '[]'); return Array.isArray(a) ? a : []; } catch { return []; }
    })();
    if (ids.length === 0) return t('COUPON.SCOPE_ALL', '全部套餐');
    const names = ids.map((id) => packages.find((p) => p.id === id)?.name || `#${id}`);
    return names.join(', ');
  };

  return (
    <div className="w-full">
      <div className="mb-8 border-b border-outline-variant pb-6">
        <h1 className="text-xl md:text-2xl font-bold tracking-tight text-on-surface flex items-center gap-3">
          <Ticket size={22} className="text-primary" />
          {t('COUPON.ADMIN_TITLE', '优惠券模板')}
        </h1>
        <p className="text-on-surface-variant mt-2 text-sm max-w-3xl">
          {t('COUPON.ADMIN_DESC', '管理"券蓝本"。创建模板后可在用户管理页给特定用户发券，或在 SysConfig 里配置 signup_coupon_template_id 让新注册用户自动获得。')}
        </p>
      </div>

      <div className="flex justify-end mb-4">
        <button
          type="button"
          onClick={() => setEditing({ ...EMPTY_TEMPLATE })}
          className="px-4 py-2 bg-primary text-on-primary rounded-control font-medium flex items-center gap-2 hover:brightness-110"
        >
          <Plus size={16} /> {t('COUPON.NEW', '新建模板')}
        </button>
      </div>

      <div className="bg-surface-container border border-outline-variant rounded-overlay overflow-hidden">
        <table className="w-full text-sm">
          <thead className="bg-surface-container-high text-on-surface-variant text-xs">
            <tr>
              <th className="px-4 py-3 text-left">{t('COUPON.COL_ID', 'ID')}</th>
              <th className="px-4 py-3 text-left">{t('COUPON.COL_NAME', '名称')}</th>
              <th className="px-4 py-3 text-left">{t('COUPON.COL_DISCOUNT', '优惠')}</th>
              <th className="px-4 py-3 text-left">{t('COUPON.COL_SCOPE', '适用范围')}</th>
              <th className="px-4 py-3 text-left">{t('COUPON.COL_VALID', '有效期')}</th>
              <th className="px-4 py-3 text-left">{t('COUPON.COL_ENABLED', '启用')}</th>
              <th className="px-4 py-3 text-right">{t('COUPON.COL_ACTIONS', '操作')}</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-outline-variant/50">
            {loading ? (
              <tr><td colSpan="7" className="px-4 py-8 text-center text-on-surface-variant">{t('COUPON.LOADING', '加载中...')}</td></tr>
            ) : list.length === 0 ? (
              <tr><td colSpan="7" className="px-4 py-8 text-center text-on-surface-variant">{t('COUPON.EMPTY', '暂无模板')}</td></tr>
            ) : (
              list.map((tpl) => (
                <tr key={tpl.id} className="hover:bg-surface-container-high">
                  <td className="px-4 py-3 font-mono text-on-surface-variant">#{tpl.id}</td>
                  <td className="px-4 py-3 font-medium">{tpl.name}</td>
                  <td className="px-4 py-3">
                    {tpl.discount_type === 'fixed_price' && (
                      <span className="text-success">{t('COUPON.FIXED_PRICE', '固定价 $')}{tpl.discount_value}</span>
                    )}
                  </td>
                  <td className="px-4 py-3 text-xs text-on-surface-variant">{renderPackageScope(tpl)}</td>
                  <td className="px-4 py-3 text-xs">
                    {tpl.valid_days === 0 ? t('COUPON.PERMANENT', '永久') : t('COUPON.DAYS', '{{n}} 天', { n: tpl.valid_days })}
                  </td>
                  <td className="px-4 py-3">
                    {tpl.enabled !== false
                      ? <span className="text-xs px-2 py-0.5 rounded-control bg-success/20 text-success">{t('COUPON.YES', '启用')}</span>
                      : <span className="text-xs px-2 py-0.5 rounded-control bg-surface-variant/20 text-on-surface-variant">{t('COUPON.NO', '禁用')}</span>}
                  </td>
                  <td className="px-4 py-3 text-right">
                    <button onClick={() => setEditing({ ...tpl, package_ids: tpl.package_ids || '' })}
                      className="p-1.5 hover:bg-primary/20 text-primary rounded-control mr-1" aria-label={t('COUPON.EDIT', '编辑')}>
                      <Edit size={14} />
                    </button>
                    <button onClick={() => onDelete(tpl)}
                      className="p-1.5 hover:bg-error/20 text-error rounded-control" aria-label={t('COUPON.DELETE', '删除')}>
                      <Trash2 size={14} />
                    </button>
                  </td>
                </tr>
              ))
            )}
          </tbody>
        </table>
      </div>

      {isOpen && (
        <div ref={modalRef} role="dialog" aria-modal="true" aria-labelledby="coupon-modal-title"
          onClick={onBackdropClick}
          className="fixed inset-0 bg-black/80 backdrop-blur-sm z-[100] flex items-center justify-center p-4 overflow-y-auto">
          <div className="bg-surface-container border border-outline-variant rounded-overlay w-full max-w-xl max-h-[90vh] flex flex-col">
            <div className="p-6 border-b border-outline-variant flex justify-between items-center">
              <h3 id="coupon-modal-title" className="text-lg font-bold flex items-center gap-2">
                <Ticket size={18} className="text-primary" />
                {editing.id ? t('COUPON.EDIT_TITLE', '编辑模板') : t('COUPON.CREATE_TITLE', '新建模板')}
              </h3>
              <button ref={closeBtnRef} onClick={() => !saving && setEditing(null)} aria-label={t('COMMON.CLOSE', '关闭')}>
                <X size={18} className="text-on-surface-variant hover:text-on-surface" />
              </button>
            </div>
            <div className="p-6 overflow-y-auto space-y-4">
              <div>
                <label htmlFor="ct-name" className="block text-xs font-medium text-on-surface-variant mb-1">
                  {t('COUPON.FIELD_NAME', '名称（admin 内部用）')}
                </label>
                <input id="ct-name" type="text" value={editing.name}
                  onChange={(e) => updateField('name', e.target.value)}
                  placeholder={t('COUPON.NAME_PLACEHOLDER', '如：新人 5 折券')}
                  className="w-full bg-surface-container-high border border-outline rounded-control px-3 py-2 text-on-surface" />
              </div>
              <div>
                <label htmlFor="ct-desc" className="block text-xs font-medium text-on-surface-variant mb-1">
                  {t('COUPON.FIELD_DESC', '描述（用户看到）')}
                </label>
                <input id="ct-desc" type="text" value={editing.description}
                  onChange={(e) => updateField('description', e.target.value)}
                  placeholder={t('COUPON.DESC_PLACEHOLDER', '如：限时新人首单专享')}
                  className="w-full bg-surface-container-high border border-outline rounded-control px-3 py-2 text-on-surface" />
              </div>
              <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
                <div>
                  <label htmlFor="ct-type" className="block text-xs font-medium text-on-surface-variant mb-1">
                    {t('COUPON.FIELD_TYPE', '优惠类型')}
                  </label>
                  <select id="ct-type" value={editing.discount_type}
                    onChange={(e) => updateField('discount_type', e.target.value)}
                    className="w-full bg-surface-container-high border border-outline rounded-control px-3 py-2 text-on-surface">
                    <option value="fixed_price">{t('COUPON.TYPE_FIXED', '固定价（直接定价）')}</option>
                  </select>
                </div>
                <div>
                  <label htmlFor="ct-value" className="block text-xs font-medium text-on-surface-variant mb-1">
                    {t('COUPON.FIELD_VALUE', '券价 (USD)')}
                  </label>
                  <input id="ct-value" type="number" min="0" step="0.01" value={editing.discount_value}
                    onChange={(e) => updateField('discount_value', parseFloat(e.target.value) || 0)}
                    className="w-full bg-surface-container-high border border-outline rounded-control px-3 py-2 text-on-surface" />
                </div>
              </div>
              <div>
                <label className="block text-xs font-medium text-on-surface-variant mb-2">
                  {t('COUPON.FIELD_SCOPE', '适用套餐（不选 = 全部适用）')}
                </label>
                <div className="flex flex-wrap gap-2">
                  {packages.map((p) => (
                    <button key={p.id} type="button" onClick={() => togglePackage(p.id)}
                      className={`px-3 py-1.5 rounded-control text-xs border transition ${
                        isPackageSelected(p.id)
                          ? 'bg-primary text-on-primary border-primary'
                          : 'bg-surface-container-high border-outline-variant text-on-surface-variant hover:border-primary'
                      }`}>
                      {p.name}
                    </button>
                  ))}
                </div>
              </div>
              <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
                <div>
                  <label htmlFor="ct-valid" className="block text-xs font-medium text-on-surface-variant mb-1">
                    {t('COUPON.FIELD_VALID', '有效天数（0=永久）')}
                  </label>
                  <input id="ct-valid" type="number" min="0" value={editing.valid_days}
                    onChange={(e) => updateField('valid_days', parseInt(e.target.value, 10) || 0)}
                    className="w-full bg-surface-container-high border border-outline rounded-control px-3 py-2 text-on-surface" />
                </div>
                <div className="flex items-end">
                  <label className="flex items-center gap-2 cursor-pointer text-sm">
                    <input type="checkbox" checked={editing.enabled !== false}
                      onChange={(e) => updateField('enabled', e.target.checked)}
                      className="w-4 h-4" />
                    <span>{t('COUPON.FIELD_ENABLED', '启用此模板')}</span>
                  </label>
                </div>
              </div>
            </div>
            <div className="p-6 border-t border-outline-variant flex justify-end gap-3">
              <button onClick={() => !saving && setEditing(null)}
                className="px-5 py-2 text-on-surface-variant hover:text-on-surface rounded-control">
                {t('COMMON.CANCEL', '取消')}
              </button>
              <button onClick={onSave} disabled={saving}
                className="px-5 py-2 bg-primary text-on-primary rounded-control font-medium flex items-center gap-2 disabled:opacity-50">
                <Save size={16} /> {saving ? t('COMMON.SAVING', '保存中...') : t('COMMON.SAVE', '保存')}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
};

export default CouponManagement;
