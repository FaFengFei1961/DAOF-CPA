import React, { useState, useEffect, useCallback, useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { Coins, Save, Eye, EyeOff, AlertTriangle, Info } from 'lucide-react';
import toast from 'react-hot-toast';
import { authFetch } from '../utils/authFetch';

// W-4-Manual：epusdt provider admin 配置面板。
//
// 双模式：
//   - manual（默认，零部署）：admin 配 4 链地址 + 邮箱 → 用户下单后邮件通知 admin
//                            → admin 区块链浏览器验真 → 后台标记到账
//   - auto（部署 sidecar）：admin 配 sidecar endpoint + pid + secret → 全自动收款
//
// 切 mode 时立即生效（SysConfig 改完前端按 currentMode 渲染对应字段）。

const MANUAL_FIELDS = [
  { key: 'epusdt_manual_admin_email',     label: 'EPUSDT_ADMIN_EMAIL',     type: 'email',   hint: 'EPUSDT_ADMIN_EMAIL_HINT' },
  { key: 'epusdt_manual_address_trc20',   label: 'EPUSDT_ADDR_TRC20',      type: 'text',    hint: 'EPUSDT_ADDR_TRON_HINT' },
  { key: 'epusdt_manual_address_erc20',   label: 'EPUSDT_ADDR_ERC20',      type: 'text',    hint: 'EPUSDT_ADDR_EVM_HINT' },
  { key: 'epusdt_manual_address_bep20',   label: 'EPUSDT_ADDR_BEP20',      type: 'text',    hint: 'EPUSDT_ADDR_EVM_HINT' },
  { key: 'epusdt_manual_address_polygon', label: 'EPUSDT_ADDR_POLYGON',    type: 'text',    hint: 'EPUSDT_ADDR_EVM_HINT' },
];

const AUTO_FIELDS = [
  { key: 'epusdt_endpoint',         label: 'EPUSDT_ENDPOINT',     type: 'text',    hint: 'EPUSDT_ENDPOINT_HINT' },
  { key: 'epusdt_pid',              label: 'EPUSDT_PID',          type: 'text' },
  { key: 'epusdt_secret_key',       label: 'EPUSDT_SECRET',       type: 'secret',  hint: 'EPUSDT_SECRET_HINT' },
  { key: 'epusdt_enabled_chains',   label: 'EPUSDT_ENABLED_CHAINS', type: 'text', hint: 'EPUSDT_CHAINS_HINT' },
];

const SHARED_FIELDS = [
  // 共享字段：admin 通用层金额上下限 / 预设
  { key: 'epusdt_preset_amounts_fen', label: 'EPUSDT_PRESETS_FEN',  type: 'text',   hint: 'EPUSDT_PRESETS_FEN_HINT' },
  { key: 'epusdt_min_amount_fen',     label: 'EPUSDT_MIN_FEN',      type: 'number', hint: 'EPUSDT_FEN_HINT' },
  { key: 'epusdt_max_amount_fen',     label: 'EPUSDT_MAX_FEN',      type: 'number', hint: 'EPUSDT_FEN_HINT' },
  { key: 'epusdt_notify_allowed_cidrs', label: 'EPUSDT_NOTIFY_CIDRS', type: 'textarea', hint: 'EPUSDT_NOTIFY_CIDRS_HINT' },
];

// 所有 epusdt_ 前缀字段（含 mode），用于 load / save 时一次性读写
const ALL_KEYS = [
  'epusdt_mode',
  ...MANUAL_FIELDS.map(f => f.key),
  ...AUTO_FIELDS.map(f => f.key),
  ...SHARED_FIELDS.map(f => f.key),
];

const AdminPaymentChannelsEpusdt = () => {
  const { t } = useTranslation();
  const [values, setValues] = useState({});
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [showSecret, setShowSecret] = useState({});

  const mode = values.epusdt_mode || 'manual';
  const visibleFields = useMemo(
    () => (mode === 'auto' ? [...AUTO_FIELDS, ...SHARED_FIELDS] : [...MANUAL_FIELDS, ...SHARED_FIELDS]),
    [mode]
  );

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const json = await authFetch('/api/admin/config');
      if (json.success && json.data) {
        const next = {};
        for (const k of ALL_KEYS) next[k] = json.data[k] ?? '';
        if (!next.epusdt_mode) next.epusdt_mode = 'manual'; // 默认 manual
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
      const payload = {};
      for (const k of ALL_KEYS) payload[k] = values[k] ?? '';
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
        <Coins size={24} className="text-primary" />
        <div>
          <h2 className="text-xl font-bold text-on-surface tracking-tight">
            {t('PAY_ADMIN.EPUSDT_TITLE', 'Web3 USDT 配置')}
          </h2>
          <p className="text-xs text-on-surface-variant mt-1">
            {t('PAY_ADMIN.EPUSDT_DESC', '配置 USDT 多链收款。可选两种模式：手动确认（零部署）或 epusdt sidecar 全自动。')}
          </p>
        </div>
      </header>

      {/* Mode 切换器 */}
      <section className="bg-surface-container-high border border-outline-variant rounded-overlay p-6 space-y-4">
        <label className="text-xs font-semibold text-on-surface-variant uppercase tracking-wide block">
          {t('PAY_ADMIN.EPUSDT_MODE_LABEL', '工作模式')}
        </label>
        <div role="radiogroup" className="grid grid-cols-1 md:grid-cols-2 gap-3">
          <ModeCard
            active={mode === 'manual'}
            onClick={() => setValues({ ...values, epusdt_mode: 'manual' })}
            title={t('PAY_ADMIN.EPUSDT_MODE_MANUAL', '手动确认（零部署）')}
            desc={t('PAY_ADMIN.EPUSDT_MODE_MANUAL_DESC', '订单创建时邮件通知您。验证链上转账后在订单管理点"标记到账"。无需部署任何服务。')}
            tag={t('PAY_ADMIN.RECOMMENDED', '推荐')}
          />
          <ModeCard
            active={mode === 'auto'}
            onClick={() => setValues({ ...values, epusdt_mode: 'auto' })}
            title={t('PAY_ADMIN.EPUSDT_MODE_AUTO', '自动收款（epusdt sidecar）')}
            desc={t('PAY_ADMIN.EPUSDT_MODE_AUTO_DESC', '部署 epusdt Docker 容器后，链上确认自动入账。需要额外运维。')}
          />
        </div>

        {/* Mode-specific 提示 */}
        {mode === 'manual' && (
          <div className="flex items-start gap-2 p-3 rounded-control bg-info/10 border border-info/30 text-xs">
            <Info size={14} className="text-info shrink-0 mt-0.5" />
            <div className="text-on-surface-variant space-y-1">
              <p>{t('PAY_ADMIN.EPUSDT_MANUAL_TIP1', '配置好至少一条链的收款地址 + 您的邮箱即可上线。')}</p>
              <p>{t('PAY_ADMIN.EPUSDT_MANUAL_TIP2', '推荐使用 watch-only 地址（私钥不录入任何 DAOF 服务）。')}</p>
            </div>
          </div>
        )}
        {mode === 'auto' && (
          <div className="flex items-start gap-2 p-3 rounded-control bg-warning/10 border border-warning/30 text-xs">
            <AlertTriangle size={14} className="text-warning shrink-0 mt-0.5" />
            <div className="text-on-surface-variant">
              {t('PAY_ADMIN.EPUSDT_AUTO_TIP', '需先按 deploy/epusdt/README.md 部署 sidecar 并完成 admin 引导，再回此处填 endpoint / pid / secret_key。')}
            </div>
          </div>
        )}
      </section>

      {/* 字段表单 */}
      <section className="bg-surface-container-high border border-outline-variant rounded-overlay p-6 grid grid-cols-1 md:grid-cols-2 gap-4">
        {visibleFields.map(f => {
          const fullWidth = f.type === 'textarea' || f.type === 'secret' || f.key === 'epusdt_endpoint' || f.key === 'epusdt_manual_admin_email';
          const isSecret = f.type === 'secret';
          const fieldId = `epusdt-${f.key}`;
          return (
            <div key={f.key} className={fullWidth ? 'md:col-span-2' : ''}>
              <label htmlFor={fieldId} className="text-xs font-semibold text-on-surface-variant uppercase tracking-wide block mb-1.5">
                {t(`PAY_ADMIN.${f.label}`, f.label)}
              </label>

              {f.type === 'textarea' ? (
                <div className="space-y-1">
                  <textarea
                    id={fieldId}
                    rows={3}
                    value={values[f.key] || ''}
                    onChange={e => setValues({ ...values, [f.key]: e.target.value })}
                    placeholder="127.0.0.1/32,10.0.0.0/8"
                    className="w-full bg-surface-container border border-outline rounded-control px-3 py-2 text-sm text-on-surface focus:border-primary outline-none font-mono resize-y"
                  />
                  {f.hint && <span className="text-[11px] text-on-surface-variant">{t(`PAY_ADMIN.${f.hint}`, f.hint)}</span>}
                </div>
              ) : isSecret ? (
                <div className="space-y-1">
                  <div className="relative">
                    <input
                      id={fieldId}
                      type={showSecret[f.key] ? 'text' : 'password'}
                      value={values[f.key] || ''}
                      onChange={e => setValues({ ...values, [f.key]: e.target.value })}
                      autoComplete="off"
                      className="w-full h-10 bg-surface-container border border-outline rounded-control px-3 pr-10 text-sm text-on-surface focus:border-primary outline-none font-mono"
                    />
                    <button
                      type="button"
                      onClick={() => setShowSecret({ ...showSecret, [f.key]: !showSecret[f.key] })}
                      aria-label={showSecret[f.key]
                        ? t('PAY_ADMIN.HIDE_SECRET_ARIA', '隐藏密钥')
                        : t('PAY_ADMIN.SHOW_SECRET_ARIA', '显示密钥')}
                      className="absolute right-2 top-1/2 -translate-y-1/2 text-on-surface-variant hover:text-primary"
                    >
                      {showSecret[f.key] ? <EyeOff size={14} /> : <Eye size={14} />}
                    </button>
                  </div>
                  {f.hint && <span className="text-[11px] text-on-surface-variant">{t(`PAY_ADMIN.${f.hint}`, f.hint)}</span>}
                </div>
              ) : (
                <div className="space-y-1">
                  <input
                    id={fieldId}
                    type={f.type === 'number' ? 'number' : f.type === 'email' ? 'email' : 'text'}
                    value={values[f.key] || ''}
                    onChange={e => setValues({ ...values, [f.key]: e.target.value })}
                    placeholder={
                      f.key === 'epusdt_manual_address_trc20' ? 'T...' :
                      f.key.startsWith('epusdt_manual_address_') ? '0x...' :
                      f.key === 'epusdt_endpoint' ? 'http://localhost:8000' : ''
                    }
                    className="w-full h-10 bg-surface-container border border-outline rounded-control px-3 text-sm text-on-surface focus:border-primary outline-none font-mono"
                  />
                  {f.hint && <span className="text-[11px] text-on-surface-variant">{t(`PAY_ADMIN.${f.hint}`, f.hint)}</span>}
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

      <div className="text-xs text-on-surface-variant space-y-1.5">
        <p>{t('PAY_ADMIN.EPUSDT_HINT_PRESETS', '金额预设示例（fen 整数，1 fen = 0.01 元）：')}<code className="text-primary">1000,3000,5000,10000</code></p>
        {mode === 'manual' && (
          <p>{t('PAY_ADMIN.EPUSDT_HINT_WORKFLOW',
            '工作流：用户下单 → 系统邮件通知您 → 您在区块链浏览器查到账 → 充值订单管理 → 找到订单 → 点"标记到账"。')}</p>
        )}
        {mode === 'auto' && (
          <p>
            {t('PAY_ADMIN.EPUSDT_HINT_NOTIFY_URL', 'Webhook 地址（配到 epusdt admin 后台）：')}
            <code className="text-primary">{'{server_address}/api/payment/notify/epusdt'}</code>
          </p>
        )}
      </div>
    </div>
  );
};

const ModeCard = ({ active, onClick, title, desc, tag }) => (
  <button
    type="button"
    role="radio"
    aria-checked={active}
    onClick={onClick}
    className={`text-left p-4 rounded-control border-2 transition ${
      active
        ? 'border-primary bg-primary/5'
        : 'border-outline-variant bg-surface-container hover:border-primary/40'
    }`}
  >
    <div className="flex items-center gap-2 mb-1">
      <div className="text-sm font-semibold text-on-surface">{title}</div>
      {tag && (
        <span className="text-[10px] px-1.5 py-0.5 rounded bg-primary/10 text-primary font-mono">{tag}</span>
      )}
    </div>
    <div className="text-xs text-on-surface-variant leading-relaxed">{desc}</div>
  </button>
);

export default AdminPaymentChannelsEpusdt;
