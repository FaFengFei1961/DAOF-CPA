/**
 * UpstreamMarginPage — 上游账号成本与毛利（Phase 1 拆出第 2 页）
 *
 * 替换原 UserUsageDash 内的"上游账号成本与毛利"区块（10 列 1280px 横滚 + 编辑表单混在一起）。
 * 重设计：
 *   - 5 张毛利 KPI（StatCard）
 *   - 上游账号表（DataTable，5-6 关键列：账号 / 套餐 / 请求 / Raw / 扣减 / 平台成本 / 毛利）
 *   - 行点击 → Drawer 打开"配置成本"编辑表单（旧设计是表格 + 表单上下堆叠，密度高）
 *   - 顶部"批量配置"action（多选行后弹同样 Drawer）
 */
import React, { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  Activity, Coins, Wallet, Zap, BarChart3, AlertTriangle, RefreshCw, Settings as SettingsIcon,
  Trash2, AlertOctagon, ChevronDown, ChevronUp,
} from 'lucide-react';
import toast from 'react-hot-toast';
import {
  PageContainer, PageHeader, StatCard, DataTable, Drawer, FormRow,
} from '../../components/ui';
import { useCurrency } from '../../context/CurrencyContext';
import { useConfirm } from '../../context/ConfirmContext';
import { authFetch } from '../../utils/authFetch';
import { PERIODS, formatPercent, makeFormatMeterCost } from './shared';

const STALE_REASON_LABEL = {
  not_in_cpa:    { tone: 'error',   label: 'CPA 已删除' },
  cpa_disabled:  { tone: 'warning', label: 'CPA 已禁用' },
  cpa_unseen_7d: { tone: 'warning', label: 'CPA 7 天未见' },
};

const PeriodSwitch = ({ value, onChange }) => (
  <div className="flex items-center gap-1 bg-surface-container p-0.5 rounded-control border border-outline-variant">
    {PERIODS.map(p => (
      <button
        key={p.value}
        type="button"
        onClick={() => onChange(p.value)}
        className={`px-3 py-1 text-xs font-medium rounded-control ${
          value === p.value ? 'bg-surface-variant text-on-surface ' : 'text-on-surface-variant hover:text-on-surface'
        }`}
      >{p.label}</button>
    ))}
  </div>
);

const UpstreamMarginPage = () => {
  const { t } = useTranslation();
  const { formatCurrencyFixed } = useCurrency();
  const formatMeterCost = makeFormatMeterCost(formatCurrencyFixed);
  const confirm = useConfirm();

  const [period, setPeriod] = useState('7d');
  const [data, setData] = useState(null);
  const [loading, setLoading] = useState(true);
  const [accountRows, setAccountRows] = useState([]);
  const [accountsLoading, setAccountsLoading] = useState(true);
  const [presets, setPresets] = useState([]);
  const [drawerRow, setDrawerRow] = useState(null);
  const [bulkMode, setBulkMode] = useState(false);
  const [selectedKeys, setSelectedKeys] = useState([]);
  const [saving, setSaving] = useState(false);
  const [form, setForm] = useState(emptyForm());
  const [selectedPreset, setSelectedPreset] = useState('');
  const [stale, setStale] = useState([]);
  const [staleOpen, setStaleOpen] = useState(false);

  const fetchData = async () => {
    setLoading(true);
    try {
      const res = await fetch(`/api/admin/upstream-margin?period=${period}`, { credentials: 'include' });
      const json = await res.json();
      if (json.success) setData(json.data);
      else toast.error(json.message || '加载毛利报表失败');
    } catch {
      toast.error('网络异常');
    }
    setLoading(false);
  };

  const fetchPresets = async () => {
    try {
      const res = await fetch('/api/admin/upstream-account-cost-presets', { credentials: 'include' });
      const json = await res.json();
      if (json.success) setPresets(json.data || []);
    } catch {
      // 静默
    }
  };

  const fetchAccountCandidates = async () => {
    setAccountsLoading(true);
    try {
      const res = await fetch('/api/admin/upstream-accounts/candidates', { credentials: 'include' });
      const json = await res.json();
      if (json.success) setAccountRows(json.data || []);
      else toast.error(json.message || '加载上游凭证失败');
    } catch {
      toast.error('网络异常，无法加载上游凭证');
    } finally {
      setAccountsLoading(false);
    }
  };

  const fetchStale = async () => {
    try {
      const res = await fetch('/api/admin/upstream-accounts/stale', { credentials: 'include' });
      const json = await res.json();
      if (json.success) {
        const list = json.data || [];
        setStale(list);
        if (list.length > 0) setStaleOpen(true); // 有孤儿时默认展开
      }
    } catch {
      // 静默：孤儿对账失败不影响主报表
    }
  };

  const deleteAccount = async (id, label) => {
    const ok = await confirm(`确认删除配置：${label}？\n\n该配置将从本地数据库永久删除，历史 ApiLog 不受影响。`);
    if (!ok) return;
    try {
      const json = await authFetch(`/api/admin/upstream-accounts/${id}`, { method: 'DELETE' });
      if (json.success) {
        toast.success('配置已删除');
        fetchStale();
        fetchAccountCandidates();
        fetchData();
      } else {
        toast.error(json.message || '删除失败');
      }
    } catch {
      toast.error('网络异常');
    }
  };

  useEffect(() => { fetchData(); }, [period]);
  useEffect(() => { fetchPresets(); fetchStale(); fetchAccountCandidates(); }, []);

  const summary = data?.summary || {};
  const rows = data?.rows || [];
  const accountSelectableKeys = useMemo(
    () => accountRows.filter(r => r.auth_index).map(r => `${r.provider}::${r.auth_index}`),
    [accountRows],
  );
  const configuredAccountCount = useMemo(
    () => accountRows.filter(r => r.account_configured).length,
    [accountRows],
  );

  const openSingle = (row) => {
    if (!row.auth_index) {
      toast.error('这组请求还没有归因到具体上游账号，不能配置账号成本');
      return;
    }
    setBulkMode(false);
    setSelectedKeys([]);
    setDrawerRow(row);
    setForm({
      provider: row.provider || '',
      auth_index: row.auth_index || '',
      auth_type: row.auth_type || '',
      label: row.label || row.email || row.file_name || '',
      plan_name: row.plan_name || '',
      monthly_cost_usd: row.monthly_cost_usd ? String(row.monthly_cost_usd) : '',
      estimated_monthly_capacity_usd: row.estimated_monthly_capacity_usd ? String(row.estimated_monthly_capacity_usd) : '',
      active: row.account_configured ? !!row.account_active : !row.credential_disabled,
      notes: row.notes || '',
    });
    setSelectedPreset('');
  };

  const openBulk = () => {
    if (!selectedKeys.length) {
      toast.error('请先在表格里勾选要批量配置的账号');
      return;
    }
    setBulkMode(true);
    setDrawerRow({ bulkCount: selectedKeys.length });
    setForm(emptyForm());
    setSelectedPreset('');
  };

  const closeDrawer = () => {
    setDrawerRow(null);
    setBulkMode(false);
  };

  const applyPreset = (presetId) => {
    setSelectedPreset(presetId);
    const p = presets.find(x => x.id === presetId);
    if (!p) return;
    setForm(prev => ({
      ...prev,
      plan_name: p.plan_name || prev.plan_name,
      monthly_cost_usd: p.monthly_cost_usd > 0 ? String(p.monthly_cost_usd) : '',
      estimated_monthly_capacity_usd: p.estimated_monthly_capacity_usd > 0 ? String(p.estimated_monthly_capacity_usd) : '',
      notes: p.notes || prev.notes,
    }));
  };

  const handleSave = async () => {
    if (!bulkMode && (!form.provider || !form.auth_index)) {
      toast.error('provider 和 auth_index 必填');
      return;
    }
    setSaving(true);
    try {
      const common = {
        plan_name: form.plan_name,
        monthly_cost_usd: Number(form.monthly_cost_usd || 0),
        estimated_monthly_capacity_usd: Number(form.estimated_monthly_capacity_usd || 0),
        active: form.active,
        notes: form.notes,
      };
      const payload = bulkMode
        ? {
            ...common,
            accounts: selectedKeys.map(key => {
              const [provider, auth_index] = key.split('::');
              const row = accountRows.find(r => r.provider === provider && r.auth_index === auth_index)
                || rows.find(r => r.provider === provider && r.auth_index === auth_index);
              return {
                provider, auth_index,
                auth_type: row?.auth_type || form.auth_type,
                label: row?.label || row?.email || row?.file_name || `${provider}:${auth_index}`,
              };
            }),
          }
        : { ...form, monthly_cost_usd: Number(form.monthly_cost_usd || 0), estimated_monthly_capacity_usd: Number(form.estimated_monthly_capacity_usd || 0) };
      const json = await authFetch(
        bulkMode ? '/api/admin/upstream-accounts/bulk' : '/api/admin/upstream-accounts',
        { method: 'POST', body: payload },
      );
      if (!json.success) {
        toast.error(json.message || '保存账号成本失败');
        return;
      }
      toast.success(bulkMode ? `已批量配置 ${selectedKeys.length} 个账号` : '账号成本配置已保存');
      setSelectedKeys([]);
      closeDrawer();
      fetchAccountCandidates();
      fetchStale();
      fetchData();
    } catch {
      toast.error('保存账号成本网络异常');
    } finally {
      setSaving(false);
    }
  };

  const allSelected = accountSelectableKeys.length > 0 && accountSelectableKeys.every(k => selectedKeys.includes(k));
  const toggleAll = () => {
    if (allSelected) setSelectedKeys(prev => prev.filter(k => !accountSelectableKeys.includes(k)));
    else setSelectedKeys(prev => Array.from(new Set([...prev, ...accountSelectableKeys])));
  };
  const toggleRow = (row) => {
    if (!row.auth_index) return;
    const key = `${row.provider}::${row.auth_index}`;
    setSelectedKeys(prev => prev.includes(key) ? prev.filter(x => x !== key) : [...prev, key]);
  };

  const accountColumns = [
    {
      key: 'sel', header: (
        <input type="checkbox" checked={allSelected} onChange={toggleAll} className="accent-primary" />
      ), width: 40, render: r => r.auth_index ? (
        <input
          type="checkbox"
          checked={selectedKeys.includes(`${r.provider}::${r.auth_index}`)}
          onChange={(e) => { e.stopPropagation(); toggleRow(r); }}
          onClick={(e) => e.stopPropagation()}
          className="accent-primary"
        />
      ) : <span className="text-outline-variant">-</span>,
    },
    {
      key: 'credential', header: '凭证账号', render: r => (
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <span className="font-medium text-on-surface">{r.provider || 'unknown'}</span>
            <span className={`text-[10px] px-1.5 py-0.5 rounded-control border ${
              r.credential_disabled
                ? 'bg-error/10 text-error border-error/20'
                : 'bg-success/10 text-success border-success/20'
            }`}>
              {r.credential_disabled ? '已禁用' : (r.credential_status || '可用')}
            </span>
          </div>
          <div className="text-[11px] text-on-surface-variant truncate" title={r.email || r.file_name || r.auth_index}>
            {r.email || r.file_name || '未命名凭证'}
          </div>
          <div className="text-[10px] text-outline font-mono truncate" title={r.auth_index}>
            {r.auth_index}
          </div>
        </div>
      ),
    },
    { key: 'configured', header: '成本配置', render: r => (
      <div className="min-w-0">
        <div className="text-xs font-medium text-on-surface">{r.label || r.plan_name || (r.account_configured ? '已配置' : '未配置')}</div>
        <div className="text-[10px] text-on-surface-variant truncate">
          月费 {formatMeterCost(r.monthly_cost_usd || 0)} · 容量 {formatMeterCost(r.estimated_monthly_capacity_usd || 0)}
        </div>
      </div>
    ) },
    { key: 'active', header: '毛利核算', render: r => (
      <span className={`inline-block text-[10px] px-2 py-0.5 rounded-control border ${
        r.account_configured && r.account_active
          ? 'bg-primary/10 text-primary border-primary/20'
          : 'bg-surface-container-high text-on-surface-variant border-outline-variant'
      }`}>
        {r.account_configured ? (r.account_active ? '纳入' : '暂停') : '待配置'}
      </span>
    ) },
    { key: 'seen', header: 'CPA 最后见', mono: true, render: r => (
      <span className="text-[10px] text-on-surface-variant">
        {r.last_seen_at ? new Date(r.last_seen_at).toLocaleString() : '—'}
      </span>
    ) },
    {
      key: 'actions', header: '', width: 50, render: r => r.account_configured ? (
        <button
          type="button"
          onClick={(e) => {
            e.stopPropagation();
            deleteAccount(r.account_id || 0, r.label || r.email || `${r.provider}:${r.auth_index}`);
          }}
          className="p-1.5 rounded-control text-on-surface-variant hover:text-error hover:bg-error/[0.08] transition"
          aria-label="删除配置"
          title="删除此账号的成本配置"
        >
          <Trash2 size={14} />
        </button>
      ) : null,
    },
  ];

  const columns = [
    {
      key: 'account', header: '上游账号', render: r => (
        <div className="min-w-0">
          <div className="font-medium text-on-surface">{r.provider || 'unknown'}</div>
          <div className="text-[11px] text-on-surface-variant font-mono truncate" title={r.auth_index || '未归因'}>
            {r.auth_index || '未归因'}
          </div>
          {r.missing_cost_config && (
            <span className="inline-block mt-1 text-[10px] px-1.5 py-0.5 rounded-control bg-error/10 text-error border border-error/20">
              缺成本配置
            </span>
          )}
        </div>
      ),
    },
    { key: 'plan', header: '套餐', truncate: 200, render: r => (
      <div className="min-w-0">
        <div className="text-on-surface text-xs">{r.label || r.plan_name || '-'}</div>
        <div className="text-[10px] text-outline-variant truncate">
          月费 {formatMeterCost(r.monthly_cost_usd || 0)} · 容量 {formatMeterCost(r.estimated_monthly_capacity_usd || 0)}
        </div>
      </div>
    ) },
    { key: 'requests', header: '请求', align: 'right', mono: true, render: r => (r.requests || 0).toLocaleString() },
    { key: 'revenue', header: '总营收', align: 'right', mono: true, render: r => (
      <div className="min-w-0">
        <div className="text-primary font-semibold">{formatMeterCost(r.total_revenue_usd || 0)}</div>
        <div className="text-[10px] font-normal text-on-surface-variant whitespace-nowrap">
          订阅 {formatMeterCost(r.subscription_revenue_usd || 0)} · 余额 {formatMeterCost(r.balance_revenue_usd || 0)}
        </div>
      </div>
    ) },
    { key: 'platform', header: '平台成本', align: 'right', mono: true, render: r => formatMeterCost(r.platform_cost_estimate_usd || 0) },
    { key: 'margin', header: '毛利', align: 'right', mono: true, render: r => (
      <span className={`font-semibold ${(r.gross_margin_usd || 0) >= 0 ? 'text-success' : 'text-error'}`}>
        {formatMeterCost(r.gross_margin_usd || 0)}
        <div className="text-[10px] font-normal text-on-surface-variant">{formatPercent(r.gross_margin_rate)}</div>
      </span>
    ) },
    { key: 'cap', header: '容量利用', align: 'right', mono: true, render: r => formatPercent(r.capacity_utilization) },
    {
      key: 'actions', header: '', width: 50, render: r => r.account_configured ? (
        <button
          type="button"
          onClick={(e) => {
            e.stopPropagation();
            deleteAccount(r.account_id || 0, r.label || `${r.provider}:${r.auth_index}`);
          }}
          className="p-1.5 rounded-control text-on-surface-variant hover:text-error hover:bg-error/[0.08] transition"
          aria-label="删除配置"
          title="删除此账号的成本配置"
        >
          <Trash2 size={14} />
        </button>
      ) : null,
    },
  ];

  const headerActions = (
    <>
      {selectedKeys.length > 0 && (
        <button
          type="button"
          onClick={openBulk}
          className="px-3 py-1.5 rounded-control text-xs font-medium bg-primary text-on-primary"
        >
          批量配置 {selectedKeys.length} 个
        </button>
      )}
      <PeriodSwitch value={period} onChange={setPeriod} />
      <button
        type="button"
        onClick={fetchData}
        className="p-2 rounded-control border border-outline-variant text-on-surface-variant hover:text-on-surface transition"
        aria-label="刷新"
      >
        <RefreshCw size={14} className={loading ? 'animate-spin' : ''} />
      </button>
    </>
  );

  return (
    <PageContainer>
      <PageHeader
        title={t('ADMIN.UPSTREAM.TITLE')}
        sub={t('ADMIN.UPSTREAM.SUB')}
        actions={headerActions}
      />

      <section className="space-y-3">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex items-center gap-2">
            <div className="w-8 h-8 rounded-control bg-primary/10 text-primary grid place-items-center">
              <SettingsIcon size={16} />
            </div>
            <div>
              <h2 className="text-sm font-semibold text-on-surface">上游凭证成本配置</h2>
              <p className="text-xs text-on-surface-variant">
                来自 CPA 凭证清单，未产生请求的账号也可以先配置成本。
              </p>
            </div>
          </div>
          <div className="text-xs text-on-surface-variant">
            已配置 <span className="font-mono text-on-surface">{configuredAccountCount}</span>
            <span className="mx-1">/</span>
            <span className="font-mono text-on-surface">{accountRows.length}</span>
          </div>
        </div>
        <DataTable
          columns={accountColumns}
          rows={accountRows}
          rowKey={r => `${r.provider}-${r.auth_index}`}
          loading={accountsLoading}
          emptyTitle="尚未同步到 CPA 凭证"
          emptySub="先在号池监控触发刷新，或等待后台自动同步凭证清单。"
          emptyIcon={SettingsIcon}
          onRowClick={openSingle}
        />
      </section>

      {stale.length > 0 && (
        <div className="rounded-overlay border border-warning/40 bg-warning/[0.06] overflow-hidden">
          <button
            type="button"
            onClick={() => setStaleOpen(o => !o)}
            className="w-full flex items-center gap-3 px-4 py-3 text-left hover:bg-warning/[0.04]"
            aria-expanded={staleOpen}
          >
            <AlertOctagon size={18} className="text-warning shrink-0" />
            <div className="flex-1 min-w-0">
              <div className="text-sm font-semibold text-on-surface">
                {stale.length} 条孤儿成本配置
              </div>
              <div className="text-xs text-on-surface-variant mt-0.5">
                本地配置过但 CPA 端已删除 / 已禁用 / 7 天未见。不影响业务，建议清理。
              </div>
            </div>
            {staleOpen ? <ChevronUp size={16} className="text-on-surface-variant" /> : <ChevronDown size={16} className="text-on-surface-variant" />}
          </button>
          {staleOpen && (
            <div className="border-t border-warning/30 bg-surface">
              <table className="w-full text-xs">
                <thead className="bg-surface-container-high text-[11px] uppercase tracking-wider text-on-surface-variant">
                  <tr>
                    <th className="text-left px-4 py-2 font-medium">账号</th>
                    <th className="text-left px-4 py-2 font-medium">原因</th>
                    <th className="text-left px-4 py-2 font-medium">配置</th>
                    <th className="text-left px-4 py-2 font-medium">CPA 最后见</th>
                    <th className="px-4 py-2"></th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-outline-variant/30">
                  {stale.map((s) => {
                    const reason = STALE_REASON_LABEL[s.stale_reason] || { tone: 'warning', label: s.stale_reason };
                    return (
                      <tr key={s.id}>
                        <td className="px-4 py-2">
                          <div className="font-medium text-on-surface">{s.provider}</div>
                          <div className="text-[10px] text-on-surface-variant font-mono truncate max-w-[260px]" title={s.auth_index}>
                            {s.auth_index}
                          </div>
                          {s.label && <div className="text-[10px] text-on-surface-variant">{s.label}</div>}
                        </td>
                        <td className="px-4 py-2">
                          <span className={`inline-block text-[10px] px-2 py-0.5 rounded-control bg-${reason.tone}/10 text-${reason.tone} border border-${reason.tone}/20`}>
                            {reason.label}
                          </span>
                        </td>
                        <td className="px-4 py-2 text-on-surface-variant">
                          月费 {formatMeterCost(s.monthly_cost_usd || 0)} · 容量 {formatMeterCost(s.estimated_monthly_capacity_usd || 0)}
                        </td>
                        <td className="px-4 py-2 text-on-surface-variant font-mono text-[10px]">
                          {s.cpa_last_seen_at ? new Date(s.cpa_last_seen_at).toLocaleString() : '—'}
                        </td>
                        <td className="px-4 py-2 text-right">
                          <button
                            type="button"
                            onClick={() => deleteAccount(s.id, s.label || `${s.provider}:${s.auth_index}`)}
                            className="inline-flex items-center gap-1 px-2 py-1 rounded-control text-error border border-error/30 hover:bg-error/[0.08] text-[11px]"
                          >
                            <Trash2 size={12} /> 删除
                          </button>
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
        </div>
      )}

      <div className="grid grid-cols-2 lg:grid-cols-6 gap-3">
        <StatCard
          icon={Activity}
          iconColor="text-primary"
          iconBg="bg-primary/10"
          label="请求数"
          value={(summary.requests || 0).toLocaleString()}
          sub={`归因率 ${formatPercent(summary.configured_request_ratio)}`}
        />
        <StatCard
          icon={Coins}
          iconColor="text-primary"
          iconBg="bg-primary/10"
          label="订阅营收"
          value={formatMeterCost(summary.subscription_revenue_usd || 0)}
          sub="按 charged_cost 扣套餐额度"
        />
        <StatCard
          icon={Wallet}
          iconColor="text-primary"
          iconBg="bg-primary/10"
          label="余额营收"
          value={formatMeterCost(summary.balance_revenue_usd || 0)}
          sub="按 raw_cost 1:1 扣余额"
        />
        <StatCard
          icon={Zap}
          iconColor="text-warning"
          iconBg="bg-warning/10"
          label="平台成本"
          value={formatMeterCost(summary.platform_cost_estimate_usd || 0)}
          sub="账号月费分摊估算"
        />
        <StatCard
          icon={BarChart3}
          iconColor="text-success"
          iconBg="bg-success/10"
          label="毛利"
          value={formatMeterCost(summary.gross_margin_usd || 0)}
          sub={`毛利率 ${formatPercent(summary.gross_margin_rate)}`}
        />
        <StatCard
          icon={AlertTriangle}
          iconColor="text-error"
          iconBg="bg-error/10"
          label="未配置请求"
          value={(summary.unconfigured_request_count || 0).toLocaleString()}
          sub="需补 auth_index 成本"
        />
      </div>

      <DataTable
        columns={columns}
        rows={rows}
        rowKey={r => `${r.provider}-${r.auth_index || 'none'}`}
        loading={loading}
        emptyTitle="当前时间窗内暂无可核算请求"
        emptySub="这不影响提前配置账号成本；上方凭证表仍可维护月费和估算容量。"
        emptyIcon={Coins}
        onRowClick={openSingle}
      />

      <Drawer
        open={!!drawerRow}
        onClose={closeDrawer}
        title={bulkMode ? `批量配置 ${drawerRow?.bulkCount} 个上游账号` : `配置上游账号成本`}
        description={bulkMode ? '批量套用月费 / 月容量' : `${form.provider || '?'} / ${form.auth_index || '?'}`}
        size="md"
        footer={
          <>
            <button
              type="button"
              onClick={closeDrawer}
              className="btn btn-ghost h-9"
            >取消</button>
            <button
              type="button"
              onClick={handleSave}
              disabled={saving}
              className="btn btn-primary h-9"
            >{saving ? '保存中…' : '保存'}</button>
          </>
        }
      >
        <FormRow.Group>
          <FormRow label="套用预设" hint="官方月费预设；月容量仍需按账号池实测填写">
            <select
              value={selectedPreset}
              onChange={(e) => applyPreset(e.target.value)}
              className="w-full bg-surface-container-high border border-outline-variant text-on-surface text-sm rounded-control px-2 py-1.5 outline-none"
            >
              <option value="">不套用</option>
              {presets.map(p => (
                <option key={p.id} value={p.id}>{p.label} · 月费 ${p.monthly_cost_usd}</option>
              ))}
            </select>
          </FormRow>

          {!bulkMode && (
            <>
              <FormRow label="provider" required>
                <input
                  value={form.provider}
                  onChange={(e) => setForm(p => ({ ...p, provider: e.target.value }))}
                  placeholder="codex / anthropic / gemini"
                  className="w-full bg-surface-container-high border border-outline-variant text-on-surface text-sm rounded-control px-3 py-1.5 outline-none focus:border-primary"
                />
              </FormRow>
              <FormRow label="auth_index" required>
                <input
                  value={form.auth_index}
                  onChange={(e) => setForm(p => ({ ...p, auth_index: e.target.value }))}
                  placeholder="账号索引（不可逆 hash）"
                  className="w-full bg-surface-container-high border border-outline-variant text-on-surface text-sm rounded-control px-3 py-1.5 font-mono outline-none focus:border-primary"
                />
              </FormRow>
              <FormRow label="auth_type">
                <input
                  value={form.auth_type}
                  onChange={(e) => setForm(p => ({ ...p, auth_type: e.target.value }))}
                  placeholder="oauth / api_key"
                  className="w-full bg-surface-container-high border border-outline-variant text-on-surface text-sm rounded-control px-3 py-1.5 outline-none focus:border-primary"
                />
              </FormRow>
              <FormRow label="账号备注">
                <input
                  value={form.label}
                  onChange={(e) => setForm(p => ({ ...p, label: e.target.value }))}
                  placeholder="例如 Codex Pro #1"
                  className="w-full bg-surface-container-high border border-outline-variant text-on-surface text-sm rounded-control px-3 py-1.5 outline-none focus:border-primary"
                />
              </FormRow>
            </>
          )}

          <FormRow label="套餐名">
            <input
              value={form.plan_name}
              onChange={(e) => setForm(p => ({ ...p, plan_name: e.target.value }))}
              placeholder="例如 Pro"
              className="w-full bg-surface-container-high border border-outline-variant text-on-surface text-sm rounded-control px-3 py-1.5 outline-none focus:border-primary"
            />
          </FormRow>
          <FormRow label="月成本 USD" hint="该账号每月的官方订阅费用">
            <input
              type="number" min="0" step="0.000001"
              value={form.monthly_cost_usd}
              onChange={(e) => setForm(p => ({ ...p, monthly_cost_usd: e.target.value }))}
              placeholder="20"
              className="w-full bg-surface-container-high border border-outline-variant text-on-surface text-sm rounded-control px-3 py-1.5 outline-none focus:border-primary"
            />
          </FormRow>
          <FormRow label="估算月容量" hint="API 等值美元（按平台实测填，非官方限额）">
            <input
              type="number" min="0" step="0.000001"
              value={form.estimated_monthly_capacity_usd}
              onChange={(e) => setForm(p => ({ ...p, estimated_monthly_capacity_usd: e.target.value }))}
              placeholder="5000"
              className="w-full bg-surface-container-high border border-outline-variant text-on-surface text-sm rounded-control px-3 py-1.5 outline-none focus:border-primary"
            />
          </FormRow>
          <FormRow label="启用成本分摊">
            <label className="inline-flex items-center gap-2 text-sm text-on-surface-variant">
              <input type="checkbox" checked={form.active} onChange={(e) => setForm(p => ({ ...p, active: e.target.checked }))} className="accent-primary" />
              纳入毛利核算
            </label>
          </FormRow>
          <FormRow label="备注" last>
            <input
              value={form.notes}
              onChange={(e) => setForm(p => ({ ...p, notes: e.target.value }))}
              placeholder="可留空"
              className="w-full bg-surface-container-high border border-outline-variant text-on-surface text-sm rounded-control px-3 py-1.5 outline-none focus:border-primary"
            />
          </FormRow>
        </FormRow.Group>
      </Drawer>
    </PageContainer>
  );
};

const emptyForm = () => ({
  provider: '', auth_index: '', auth_type: '', label: '',
  plan_name: '', monthly_cost_usd: '', estimated_monthly_capacity_usd: '',
  active: true, notes: '',
});

export default UpstreamMarginPage;
