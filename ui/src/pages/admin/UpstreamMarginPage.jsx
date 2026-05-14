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
import {
  Activity, Coins, Zap, BarChart3, AlertTriangle, RefreshCw, Settings as SettingsIcon,
} from 'lucide-react';
import toast from 'react-hot-toast';
import {
  PageContainer, PageHeader, StatCard, DataTable, Drawer, FormRow,
} from '../../components/ui';
import { useCurrency } from '../../context/CurrencyContext';
import { authFetch } from '../../utils/authFetch';
import { PERIODS, formatPercent, makeFormatMeterCost } from './shared';

const PeriodSwitch = ({ value, onChange }) => (
  <div className="flex items-center gap-1 bg-surface-container p-0.5 rounded border border-outline-variant">
    {PERIODS.map(p => (
      <button
        key={p.value}
        type="button"
        onClick={() => onChange(p.value)}
        className={`px-3 py-1 text-xs font-medium rounded fl-spring ${
          value === p.value ? 'bg-surface-variant text-on-surface shadow-sm' : 'text-on-surface-variant hover:text-on-surface'
        }`}
      >{p.label}</button>
    ))}
  </div>
);

const UpstreamMarginPage = () => {
  const { formatCurrencyFixed } = useCurrency();
  const formatMeterCost = makeFormatMeterCost(formatCurrencyFixed);

  const [period, setPeriod] = useState('7d');
  const [data, setData] = useState(null);
  const [loading, setLoading] = useState(true);
  const [presets, setPresets] = useState([]);
  const [drawerRow, setDrawerRow] = useState(null);
  const [bulkMode, setBulkMode] = useState(false);
  const [selectedKeys, setSelectedKeys] = useState([]);
  const [saving, setSaving] = useState(false);
  const [form, setForm] = useState(emptyForm());
  const [selectedPreset, setSelectedPreset] = useState('');

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

  useEffect(() => { fetchData(); }, [period]);
  useEffect(() => { fetchPresets(); }, []);

  const summary = data?.summary || {};
  const rows = data?.rows || [];
  const configurableRows = useMemo(() => rows.filter(r => r.auth_index), [rows]);

  const openSingle = (row) => {
    setBulkMode(false);
    setSelectedKeys([]);
    setDrawerRow(row);
    setForm({
      provider: row.provider || '',
      auth_index: row.auth_index || '',
      auth_type: row.auth_type || '',
      label: row.label || '',
      plan_name: row.plan_name || '',
      monthly_cost_usd: row.monthly_cost_usd ? String(row.monthly_cost_usd) : '',
      estimated_monthly_capacity_usd: row.estimated_monthly_capacity_usd ? String(row.estimated_monthly_capacity_usd) : '',
      active: row.account_configured ? !!row.account_active : true,
      notes: '',
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
      provider: bulkMode ? prev.provider : (p.provider || prev.provider),
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
              const row = rows.find(r => r.provider === provider && r.auth_index === auth_index);
              return {
                provider, auth_index,
                auth_type: row?.auth_type || form.auth_type,
                label: row?.label || `${provider}:${auth_index}`,
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
      fetchData();
    } catch {
      toast.error('保存账号成本网络异常');
    } finally {
      setSaving(false);
    }
  };

  const allSelected = configurableRows.length > 0 && selectedKeys.length === configurableRows.length;
  const toggleAll = () => {
    if (allSelected) setSelectedKeys([]);
    else setSelectedKeys(configurableRows.map(r => `${r.provider}::${r.auth_index}`));
  };
  const toggleRow = (row) => {
    if (!row.auth_index) return;
    const key = `${row.provider}::${row.auth_index}`;
    setSelectedKeys(prev => prev.includes(key) ? prev.filter(x => x !== key) : [...prev, key]);
  };

  const columns = [
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
      key: 'account', header: '上游账号', render: r => (
        <div className="min-w-0">
          <div className="font-medium text-on-surface">{r.provider || 'unknown'}</div>
          <div className="text-[11px] text-on-surface-variant font-mono truncate" title={r.auth_index || '未归因'}>
            {r.auth_index || '未归因'}
          </div>
          {r.missing_cost_config && (
            <span className="inline-block mt-1 text-[10px] px-1.5 py-0.5 rounded bg-red-500/10 text-red-300 border border-red-500/20">
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
    { key: 'charged', header: '扣减', align: 'right', mono: true, render: r => (
      <span className="text-primary">{formatMeterCost(r.charged_cost_usd || 0)}</span>
    ) },
    { key: 'platform', header: '平台成本', align: 'right', mono: true, render: r => formatMeterCost(r.platform_cost_estimate_usd || 0) },
    { key: 'margin', header: '毛利', align: 'right', mono: true, render: r => (
      <span className={`font-semibold ${(r.gross_margin_usd || 0) >= 0 ? 'text-emerald-400' : 'text-red-400'}`}>
        {formatMeterCost(r.gross_margin_usd || 0)}
        <div className="text-[10px] font-normal text-on-surface-variant">{formatPercent(r.gross_margin_rate)}</div>
      </span>
    ) },
    { key: 'cap', header: '容量利用', align: 'right', mono: true, render: r => formatPercent(r.capacity_utilization) },
  ];

  const headerActions = (
    <>
      {selectedKeys.length > 0 && (
        <button
          type="button"
          onClick={openBulk}
          className="px-3 py-1.5 rounded text-xs font-medium bg-primary text-on-primary fl-spring"
        >
          批量配置 {selectedKeys.length} 个
        </button>
      )}
      <PeriodSwitch value={period} onChange={setPeriod} />
      <button
        type="button"
        onClick={fetchData}
        className="p-2 rounded border border-outline-variant text-on-surface-variant hover:text-on-surface transition fl-spring"
        aria-label="刷新"
      >
        <RefreshCw size={14} className={loading ? 'animate-spin' : ''} />
      </button>
    </>
  );

  return (
    <PageContainer>
      <PageHeader
        title="上游账号成本与毛利"
        sub="以上游用量记录的 provider/auth_index 归因，按账号月费 ÷ 估算月容量分摊平台成本。点击行配置成本。"
        actions={headerActions}
      />

      <div className="grid grid-cols-2 lg:grid-cols-5 gap-3">
        <StatCard
          icon={Activity}
          iconColor="text-cyan-400"
          iconBg="bg-cyan-500/10"
          label="请求数"
          value={(summary.requests || 0).toLocaleString()}
          sub={`归因率 ${formatPercent(summary.configured_request_ratio)}`}
        />
        <StatCard
          icon={Coins}
          iconColor="text-blue-400"
          iconBg="bg-blue-500/10"
          label="扣减 Credits"
          value={formatMeterCost(summary.charged_cost_usd || 0)}
          sub="套餐 / credits 核销口径"
        />
        <StatCard
          icon={Zap}
          iconColor="text-orange-400"
          iconBg="bg-orange-500/10"
          label="平台成本"
          value={formatMeterCost(summary.platform_cost_estimate_usd || 0)}
          sub="账号月费分摊估算"
        />
        <StatCard
          icon={BarChart3}
          iconColor="text-emerald-400"
          iconBg="bg-emerald-500/10"
          label="毛利"
          value={formatMeterCost(summary.gross_margin_usd || 0)}
          sub={`毛利率 ${formatPercent(summary.gross_margin_rate)}`}
        />
        <StatCard
          icon={AlertTriangle}
          iconColor="text-red-400"
          iconBg="bg-red-500/10"
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
              className="fl-btn fl-btn-subtle h-9"
            >取消</button>
            <button
              type="button"
              onClick={handleSave}
              disabled={saving}
              className="fl-btn fl-btn-prominent h-9"
            >{saving ? '保存中…' : '保存'}</button>
          </>
        }
      >
        <FormRow.Group>
          <FormRow label="套用预设" hint="官方月费预设；月容量仍需按账号池实测填写">
            <select
              value={selectedPreset}
              onChange={(e) => applyPreset(e.target.value)}
              className="w-full bg-surface-container-high border border-outline-variant text-on-surface text-sm rounded px-2 py-1.5 outline-none"
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
                  className="w-full bg-surface-container-high border border-outline-variant text-on-surface text-sm rounded px-3 py-1.5 outline-none focus:border-primary"
                />
              </FormRow>
              <FormRow label="auth_index" required>
                <input
                  value={form.auth_index}
                  onChange={(e) => setForm(p => ({ ...p, auth_index: e.target.value }))}
                  placeholder="账号索引（不可逆 hash）"
                  className="w-full bg-surface-container-high border border-outline-variant text-on-surface text-sm rounded px-3 py-1.5 font-mono outline-none focus:border-primary"
                />
              </FormRow>
              <FormRow label="auth_type">
                <input
                  value={form.auth_type}
                  onChange={(e) => setForm(p => ({ ...p, auth_type: e.target.value }))}
                  placeholder="oauth / api_key"
                  className="w-full bg-surface-container-high border border-outline-variant text-on-surface text-sm rounded px-3 py-1.5 outline-none focus:border-primary"
                />
              </FormRow>
              <FormRow label="账号备注">
                <input
                  value={form.label}
                  onChange={(e) => setForm(p => ({ ...p, label: e.target.value }))}
                  placeholder="例如 Codex Pro #1"
                  className="w-full bg-surface-container-high border border-outline-variant text-on-surface text-sm rounded px-3 py-1.5 outline-none focus:border-primary"
                />
              </FormRow>
            </>
          )}

          <FormRow label="套餐名">
            <input
              value={form.plan_name}
              onChange={(e) => setForm(p => ({ ...p, plan_name: e.target.value }))}
              placeholder="例如 Pro"
              className="w-full bg-surface-container-high border border-outline-variant text-on-surface text-sm rounded px-3 py-1.5 outline-none focus:border-primary"
            />
          </FormRow>
          <FormRow label="月成本 USD" hint="该账号每月的官方订阅费用">
            <input
              type="number" min="0" step="0.000001"
              value={form.monthly_cost_usd}
              onChange={(e) => setForm(p => ({ ...p, monthly_cost_usd: e.target.value }))}
              placeholder="20"
              className="w-full bg-surface-container-high border border-outline-variant text-on-surface text-sm rounded px-3 py-1.5 outline-none focus:border-primary"
            />
          </FormRow>
          <FormRow label="估算月容量" hint="API 等值美元（按平台实测填，非官方限额）">
            <input
              type="number" min="0" step="0.000001"
              value={form.estimated_monthly_capacity_usd}
              onChange={(e) => setForm(p => ({ ...p, estimated_monthly_capacity_usd: e.target.value }))}
              placeholder="5000"
              className="w-full bg-surface-container-high border border-outline-variant text-on-surface text-sm rounded px-3 py-1.5 outline-none focus:border-primary"
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
              className="w-full bg-surface-container-high border border-outline-variant text-on-surface text-sm rounded px-3 py-1.5 outline-none focus:border-primary"
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
