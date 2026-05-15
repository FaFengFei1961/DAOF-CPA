import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Activity, RefreshCw, RotateCcw } from 'lucide-react';
import toast from 'react-hot-toast';
import { authFetch } from '../utils/authFetch';
import DataTable from './ui/DataTable';
import StatusBadge from './ui/StatusBadge';

const stateVariant = {
  closed: 'success',
  open: 'error',
  half_open: 'warning',
};

const circuitStateLabel = (state, t) => {
  switch (state) {
    case 'closed': return t('CHANNEL_MGMT.CIRCUIT.STATE_CLOSED', '正常 (closed)');
    case 'open': return t('CHANNEL_MGMT.CIRCUIT.STATE_OPEN', '熔断 (open)');
    case 'half_open': return t('CHANNEL_MGMT.CIRCUIT.STATE_HALF_OPEN', '半开探测 (half-open)');
    default: return t('CHANNEL_MGMT.CIRCUIT.STATE_UNKNOWN', '未知');
  }
};

const circuitApiMessage = (code, t) => {
  switch (code) {
    case 'ERR_CHANNEL_READ_FAILED':
      return t('API.ERR_CHANNEL_READ_FAILED', '获取渠道配置失败。');
    case 'ERR_CIRCUIT_RESET_AUDIT_FAILED':
      return t('API.ERR_CIRCUIT_RESET_AUDIT_FAILED', '渠道熔断已重置，但审计日志写入失败，请人工核查。');
    case 'ERR_INVALID_PARAMS':
      return t('API.ERR_INVALID_PARAMS', '参数不合法。');
    case 'ERR_FORBIDDEN':
      return t('API.ERR_FORBIDDEN', '无权执行该操作。');
    default:
      return '';
  }
};

const formatRemaining = (openUntil, nowMs) => {
  if (!openUntil) return '0s';
  const target = Date.parse(openUntil);
  if (!Number.isFinite(target)) return '0s';
  const totalSeconds = Math.max(0, Math.ceil((target - nowMs) / 1000));
  if (totalSeconds <= 0) return '0s';
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  if (minutes > 0) return `${minutes}m ${seconds}s`;
  return `${seconds}s`;
};

const ChannelCircuitMonitor = () => {
  const { t } = useTranslation();
  const [rows, setRows] = useState([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [resettingID, setResettingID] = useState(null);
  const [nowMs, setNowMs] = useState(Date.now());

  const fetchCircuits = useCallback(async ({ silent = false } = {}) => {
    if (silent) setRefreshing(true);
    else setLoading(true);
    try {
      const data = await authFetch('/api/admin/channels/circuits');
      if (data.success) {
        setRows(Array.isArray(data.data) ? data.data : []);
      } else {
        toast.error(data.message || circuitApiMessage(data.message_code, t) || t('CHANNEL_MGMT.CIRCUIT.LOAD_FAIL', '读取 Channel Circuit 状态失败'));
      }
    } catch {
      toast.error(t('API.ERR_NETWORK', '网络异常'));
    } finally {
      if (silent) setRefreshing(false);
      else setLoading(false);
    }
  }, [t]);

  useEffect(() => {
    fetchCircuits();
    const refreshTimer = window.setInterval(() => fetchCircuits({ silent: true }), 30000);
    return () => window.clearInterval(refreshTimer);
  }, [fetchCircuits]);

  useEffect(() => {
    const tickTimer = window.setInterval(() => setNowMs(Date.now()), 1000);
    return () => window.clearInterval(tickTimer);
  }, []);

  const resetCircuit = async (channelID) => {
    setResettingID(channelID);
    try {
      const data = await authFetch(`/api/admin/channels/${channelID}/circuit-reset`, { method: 'POST' });
      if (data.success) {
        toast.success(t('API.SUCCESS_CIRCUIT_RESET', 'Circuit 已重置'));
        await fetchCircuits({ silent: true });
      } else {
        toast.error(data.message || circuitApiMessage(data.message_code, t) || t('CHANNEL_MGMT.CIRCUIT.RESET_FAIL', 'Circuit 重置失败'));
      }
    } catch {
      toast.error(t('API.ERR_NETWORK', '网络异常'));
    } finally {
      setResettingID(null);
    }
  };

  const columns = useMemo(() => ([
    {
      key: 'channel',
      header: t('CHANNEL_MGMT.CIRCUIT.CHANNEL', 'Channel'),
      render: row => (
        <div className="min-w-0">
          <div className="font-semibold text-on-surface truncate" title={row.channel_name}>
            {row.channel_name || 'unknown_channel'}
          </div>
          <div className="text-[11px] text-on-surface-variant font-mono truncate" title={row.base_url || row.channel_type}>
            #{row.channel_id} {row.channel_type ? `· ${row.channel_type}` : ''}{row.base_url ? ` · ${row.base_url}` : ''}
          </div>
        </div>
      ),
    },
    {
      key: 'state',
      header: t('CHANNEL_MGMT.CIRCUIT.STATE', 'State'),
      width: 130,
      render: row => (
        <StatusBadge variant={stateVariant[row.state] || 'neutral'}>
          {circuitStateLabel(row.state, t)}
        </StatusBadge>
      ),
    },
    {
      key: 'failures',
      header: t('CHANNEL_MGMT.CIRCUIT.FAILURES', 'Failures'),
      width: 110,
      render: row => (
        <span className="font-mono text-sm tabular-nums">{row.consecutive_failures ?? 0}</span>
      ),
    },
    {
      key: 'cooldown',
      header: t('CHANNEL_MGMT.CIRCUIT.COOLDOWN_REMAINING', 'Cooldown'),
      width: 150,
      render: row => (
        <span className="font-mono text-sm tabular-nums">
          {row.state === 'open' ? formatRemaining(row.open_until, nowMs) : '0s'}
        </span>
      ),
    },
    {
      key: 'actions',
      header: t('CHANNEL_MGMT.CIRCUIT.ACTIONS', 'Actions'),
      align: 'right',
      width: 150,
      render: row => (
        <button
          type="button"
          onClick={() => resetCircuit(row.channel_id)}
          disabled={resettingID === row.channel_id}
          className="inline-flex items-center justify-center gap-1.5 px-3 py-2 rounded-control bg-surface-variant text-primary hover:bg-primary/15 disabled:opacity-50 disabled:cursor-not-allowed"
        >
          {resettingID === row.channel_id ? <RefreshCw className="animate-spin" size={14} /> : <RotateCcw size={14} />}
          <span className="whitespace-nowrap">{t('CHANNEL_MGMT.CIRCUIT.RESET', '强制重置')}</span>
        </button>
      ),
    },
  ]), [nowMs, resettingID, t]);

  return (
    <section className="bg-surface-container border border-outline-variant rounded-overlay overflow-hidden mb-8">
      <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-3 px-4 py-3 border-b border-outline-variant bg-surface-container-high">
        <div className="flex items-center gap-2 min-w-0">
          <Activity size={18} className="text-primary shrink-0" />
          <h2 className="text-sm font-bold text-on-surface truncate">
            {t('CHANNEL_MGMT.CIRCUIT.TITLE', 'Channel Circuit Monitor')}
          </h2>
        </div>
        <button
          type="button"
          onClick={() => fetchCircuits({ silent: true })}
          disabled={refreshing || loading}
          className="inline-flex items-center justify-center gap-2 px-3 py-2 rounded-control border border-outline-variant text-on-surface-variant hover:text-primary hover:bg-primary/10 disabled:opacity-50 disabled:cursor-not-allowed"
        >
          <RefreshCw size={15} className={refreshing ? 'animate-spin' : ''} />
          <span className="text-xs font-medium">{t('COMMON.REFRESH', '刷新')}</span>
        </button>
      </div>
      <DataTable
        columns={columns}
        rows={rows}
        rowKey={row => row.channel_id}
        loading={loading}
        emptyTitle={t('CHANNEL_MGMT.CIRCUIT.EMPTY', '暂无 circuit 状态')}
      />
    </section>
  );
};

export default ChannelCircuitMonitor;
