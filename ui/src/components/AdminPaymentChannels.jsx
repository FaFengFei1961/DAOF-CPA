import React, { useState, useEffect, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { Wallet, Save, Eye, EyeOff } from 'lucide-react';
import toast from 'react-hot-toast';
import { authFetch } from '../utils/authFetch';

const FIELDS = [
  { key: 'yifut_pid',                  label: 'FIELD_PID',              type: 'text' },
  { key: 'yifut_gateway',              label: 'FIELD_GATEWAY',          type: 'text' },
  { key: 'yifut_merchant_private_key', label: 'FIELD_PRIVATE_KEY',      type: 'pem-secret', hint: 'FIELD_PRIVATE_KEY_HINT' },
  { key: 'yifut_platform_public_key',  label: 'FIELD_PUBLIC_KEY',       type: 'pem',        hint: 'FIELD_PUBLIC_KEY_HINT' },
  { key: 'yifut_notify_allowed_cidrs', label: 'FIELD_NOTIFY_CIDRS',     type: 'textarea',   hint: 'FIELD_NOTIFY_CIDRS_HINT' },
  { key: 'yifut_enabled_methods',      label: 'FIELD_ENABLED_METHODS',  type: 'methods' },
  // Sprint4-M3: amounts are stored as integer fen; admin enters fen directly to avoid floats.
  { key: 'yifut_preset_amounts_fen',   label: 'FIELD_PRESETS',          type: 'text', hint: 'FIELD_PRESETS_FEN_HINT' },
  { key: 'yifut_min_amount_fen',       label: 'FIELD_MIN',              type: 'number', hint: 'FIELD_FEN_HINT' },
  { key: 'yifut_max_amount_fen',       label: 'FIELD_MAX',              type: 'number', hint: 'FIELD_FEN_HINT' },
  { key: 'yifut_product_name',         label: 'FIELD_PRODUCT_NAME',     type: 'text' },
];

// All payment methods supported by Yifut V2 RSA, aligned with controller/topup.go::allowedPayTypes.
// fix Major Codex UX review round 25: the old V1 comment was wrong; backend has moved to V2/RSA.
const ALL_PAY_METHODS = [
  { id: 'alipay',    i18n: 'PAY_ALIPAY',    color: 'bg-[#1677ff]', text: 'text-white' },
  { id: 'wxpay',     i18n: 'PAY_WXPAY',     color: 'bg-[#07c160]', text: 'text-white' },
  { id: 'qqpay',     i18n: 'PAY_QQPAY',     color: 'bg-[#12b7f5]', text: 'text-white' },
  { id: 'bank',      i18n: 'PAY_BANK',      color: 'bg-error',   text: 'text-white' },
  { id: 'jdpay',     i18n: 'PAY_JDPAY',     color: 'bg-error',   text: 'text-white' },
  { id: 'paypal',    i18n: 'PAY_PAYPAL',    color: 'bg-[#003087]', text: 'text-white' },
  { id: 'douyinpay', i18n: 'PAY_DOUYINPAY', color: 'bg-black',     text: 'text-white' },
];

const parseMethods = (csv) => (csv || '')
  .split(',')
  .map(s => s.trim())
  .filter(Boolean);

const stringifyMethods = (arr) => arr.join(',');

const AdminPaymentChannels = () => {
  const { t } = useTranslation();
  const [values, setValues] = useState({});
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [showSecret, setShowSecret] = useState({});

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const json = await authFetch('/api/admin/config');
      if (json.success && json.data) {
        const next = {};
        for (const f of FIELDS) next[f.key] = json.data[f.key] ?? '';
        setValues(next);
      }
    } catch {
      toast.error(t('PAY_ADMIN.LOAD_FAIL', '配置加载失败'));
    } finally {
      setLoading(false);
    }
  }, [t]);

  useEffect(() => { load(); }, [load]);

  const handleSave = async () => {
    setSaving(true);
    try {
      // Send only this panel's yifut_* fields to avoid overwriting other config panels.
      const payload = {};
      for (const f of FIELDS) payload[f.key] = values[f.key] ?? '';
      // ?allow_empty=1 lets empty strings mean "clear"; otherwise the backend skips empty fields.
      const json = await authFetch('/api/admin/config?allow_empty=1', {
        method: 'POST',
        body: payload,
      });
      if (json.success) {
        toast.success(t('PAY_ADMIN.SAVE_OK', '已保存'));
      } else {
        toast.error(json.message || t('PAY_ADMIN.SAVE_FAIL', '保存失败'));
      }
    } catch {
      toast.error(t('PAY_ADMIN.SAVE_FAIL', '保存失败'));
    } finally {
      setSaving(false);
    }
  };

  if (loading) {
    return <div className="text-center py-12 text-on-surface-variant">{t('SYSTEM.LOADING', '加载中...')}</div>;
  }

  return (
    <div className="space-y-6">
      <header className="flex items-center gap-3">
        <Wallet size={24} className="text-primary" />
        <div>
          <h2 className="text-xl font-bold text-on-surface tracking-tight">
            {t('PAY_ADMIN.CHANNELS_TITLE', '易付通配置')}
          </h2>
          <p className="text-xs text-on-surface-variant mt-1">
            {t('PAY_ADMIN.CHANNELS_DESC', '在易付通商户后台获取 PID + 商户 RSA 私钥 + 平台 RSA 公钥后填入此处。修改后立即生效。')}
          </p>
        </div>
      </header>

      <section className="bg-surface-container-high border border-outline-variant rounded-overlay p-6 grid grid-cols-1 md:grid-cols-2 gap-4">
        {FIELDS.map(f => {
          const fullWidth = f.type === 'methods' || f.type === 'pem' || f.type === 'pem-secret' || f.type === 'textarea' || f.key === 'yifut_gateway';
          const isPEM = f.type === 'pem' || f.type === 'pem-secret';
          const isSecretPEM = f.type === 'pem-secret';
          const fieldId = `pay-channel-${f.key}`;
          return (
            <div key={f.key} className={fullWidth ? 'md:col-span-2' : ''}>
              <label htmlFor={fieldId} className="text-xs font-semibold text-on-surface-variant uppercase tracking-wide block mb-1.5">
                {t(`PAY_ADMIN.${f.label}`)}
              </label>

              {f.type === 'methods' ? (
                <MethodsPicker
                  value={values[f.key] || ''}
                  onChange={(csv) => setValues({ ...values, [f.key]: csv })}
                  t={t}
                />
              ) : isPEM ? (
                <div className="space-y-1">
                  <textarea
                    id={fieldId}
                    rows={6}
                    value={values[f.key] || ''}
                    onChange={e => setValues({ ...values, [f.key]: e.target.value })}
                    placeholder={isSecretPEM ? '-----BEGIN PRIVATE KEY-----\n...\n-----END PRIVATE KEY-----' : '-----BEGIN PUBLIC KEY-----\n...\n-----END PUBLIC KEY-----'}
                    className={`w-full bg-surface-container border border-outline rounded-control px-3 py-2 text-xs text-on-surface focus:border-primary outline-none font-mono resize-y ${isSecretPEM && !showSecret[f.key] ? 'blur-sm focus:blur-none' : ''}`}
                  />
                  <div className="flex items-center justify-between">
                    {f.hint && (
                      <span className="text-[11px] text-on-surface-variant">
                        {t(`PAY_ADMIN.${f.hint}`)}
                      </span>
                    )}
                    {isSecretPEM && (
                      <button
                        type="button"
                        onClick={() => setShowSecret({ ...showSecret, [f.key]: !showSecret[f.key] })}
                        aria-label={showSecret[f.key]
                          ? t('PAY_ADMIN.HIDE_SECRET_ARIA', '隐藏密钥')
                          : t('PAY_ADMIN.SHOW_SECRET_ARIA', '显示密钥')}
                        className="text-[11px] text-primary hover:underline ml-auto flex items-center gap-1"
                      >
                        {showSecret[f.key] ? <EyeOff size={12} /> : <Eye size={12} />}
                        {showSecret[f.key] ? t('PAY_ADMIN.HIDE_SECRET', '隐藏') : t('PAY_ADMIN.SHOW_SECRET', '显示')}
                      </button>
                    )}
                  </div>
                </div>
              ) : f.type === 'textarea' ? (
                <div className="space-y-1">
                  <textarea
                    id={fieldId}
                    rows={3}
                    value={values[f.key] || ''}
                    onChange={e => setValues({ ...values, [f.key]: e.target.value })}
                    placeholder="1.2.3.4/32,5.6.7.0/24"
                    className="w-full bg-surface-container border border-outline rounded-control px-3 py-2 text-sm text-on-surface focus:border-primary outline-none font-mono resize-y"
                  />
                  {f.hint && (
                    <span className="text-[11px] text-on-surface-variant">
                      {t(`PAY_ADMIN.${f.hint}`)}
                    </span>
                  )}
                </div>
              ) : (
                <div className="relative">
                  <input
                    id={fieldId}
                    type="text"
                    value={values[f.key] || ''}
                    onChange={e => setValues({ ...values, [f.key]: e.target.value })}
                    className="w-full h-10 bg-surface-container border border-outline rounded-control px-3 text-sm text-on-surface focus:border-primary outline-none font-mono"
                  />
                </div>
              )}
            </div>
          );
        })}
      </section>

      <div className="flex justify-end">
        <button
          type="button"
          onClick={handleSave}
          disabled={saving}
          className="h-10 px-6 bg-primary text-on-primary rounded-control text-sm font-semibold hover:opacity-90 disabled:opacity-50 transition flex items-center gap-2"
        >
          <Save size={14} />
          {saving ? '...' : t('PAY_ADMIN.SAVE', '保存设置')}
        </button>
      </div>

      <div className="text-xs text-on-surface-variant space-y-1">
        <p>{t('PAY_ADMIN.HINT_PRESETS', '预设档位示例：')}<code className="text-primary">10,30,50,100,300,500</code></p>
        <p className="mt-2">
          {t('PAY_ADMIN.HINT_NOTIFY_URL', '异步通知地址：')}
          <code className="text-primary">{'{server_address}/api/payment/notify/yifut'}</code>
          {t('PAY_ADMIN.HINT_RETURN_URL', ' 同步跳转：')}
          <code className="text-primary">{'{server_address}/api/payment/return/yifut'}</code>
        </p>
        <p className="text-warning">
          {t('PAY_ADMIN.HINT_SERVER_ADDRESS', '注意：必须先在 财务工作区 → 基础设置 中配置 server_address 才能创建充值订单。')}
        </p>
      </div>
    </div>
  );
};

// MethodsPicker multi-select chips, one toggle button per payment method.
// State is saved to the parent as CSV for SysConfig compatibility.
const MethodsPicker = ({ value, onChange, t }) => {
  const selected = new Set(parseMethods(value));

  const toggle = (id) => {
    const next = new Set(selected);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    // Preserve ALL_PAY_METHODS order to avoid drift.
    const ordered = ALL_PAY_METHODS.filter(m => next.has(m.id)).map(m => m.id);
    onChange(stringifyMethods(ordered));
  };

  const selectAll = () => {
    onChange(stringifyMethods(ALL_PAY_METHODS.map(m => m.id)));
  };
  const clearAll = () => onChange('');

  return (
    <div className="space-y-2">
      <div className="flex flex-wrap gap-2">
        {ALL_PAY_METHODS.map(m => {
          const active = selected.has(m.id);
          return (
            <button
              key={m.id}
              type="button"
              onClick={() => toggle(m.id)}
              className={`px-3 py-1.5 rounded-full text-xs font-semibold border transition flex items-center gap-1.5 ${
                active
                  ? `${m.color} ${m.text} border-transparent `
                  : 'bg-surface-container text-on-surface-variant border-outline-variant hover:border-primary hover:text-primary'
              }`}
            >
              <span className={`w-1.5 h-1.5 rounded-full ${active ? 'bg-white/80' : 'bg-on-surface-variant/40'}`} />
              {t(`TOPUP.${m.i18n}`, m.id)}
            </button>
          );
        })}
      </div>
      <div className="flex items-center gap-3 text-[11px] text-on-surface-variant">
        <button type="button" onClick={selectAll} className="hover:text-primary">{t('PAY_ADMIN.SELECT_ALL', '全选')}</button>
        <span className="text-outline-variant">·</span>
        <button type="button" onClick={clearAll} className="hover:text-primary">{t('PAY_ADMIN.SELECT_NONE', '清空')}</button>
        <span className="text-outline-variant">·</span>
        <span className="font-mono">
          {selected.size === 0
            ? t('PAY_ADMIN.METHODS_NONE', '未启用任何支付方式')
            : t('PAY_ADMIN.METHODS_ENABLED', '已启用 {{count}} 项：{{list}}', {
                count: selected.size,
                list: [...selected].join(', '),
              })}
        </span>
      </div>
    </div>
  );
};

export default AdminPaymentChannels;
