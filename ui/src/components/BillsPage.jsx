import React, { useState, useEffect, useCallback, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import {
  ArrowDownCircle, ArrowUpCircle, RefreshCw, Receipt,
  Filter, Download, Calendar, Activity, Wallet,
} from 'lucide-react';
import toast from 'react-hot-toast';
import { authFetch, readAuthState } from '../utils/authFetch';
import Pagination from './common/Pagination';

// EntryType → 显示元数据。每种类型一个图标 + 颜色 + 中文标签。
// label 通过 i18n 拿，未配置时显示 fallback。
//
// fix MAJOR（codex 第十七轮）：补齐 admin_grant_* + api_usage_pending_reconcile，
// 与后端 allowedBillingTypes 同步。
const TYPE_META = {
  topup:                       { icon: ArrowDownCircle, color: 'text-green-600',   bg: 'bg-green-50',   i18n: 'BILL.T_TOPUP',              fallback: '充值',            direction: 'in' },
  purchase_sub:                { icon: ArrowUpCircle,   color: 'text-blue-600',    bg: 'bg-blue-50',    i18n: 'BILL.T_PURCHASE_SUB',       fallback: '购买套餐',         direction: 'out' },
  purchase_addon:              { icon: ArrowUpCircle,   color: 'text-cyan-600',    bg: 'bg-cyan-50',    i18n: 'BILL.T_PURCHASE_ADDON',     fallback: '购买增量包',       direction: 'out' },
  bonus_credit:                { icon: ArrowDownCircle, color: 'text-emerald-600', bg: 'bg-emerald-50', i18n: 'BILL.T_BONUS',              fallback: '套餐附赠',         direction: 'in' },
  refund_sub:                  { icon: ArrowDownCircle, color: 'text-amber-600',   bg: 'bg-amber-50',   i18n: 'BILL.T_REFUND_SUB',         fallback: '订阅退款',         direction: 'in' },
  refund_topup:                { icon: ArrowUpCircle,   color: 'text-orange-600',  bg: 'bg-orange-50',  i18n: 'BILL.T_REFUND_TOPUP',       fallback: '充值退款',         direction: 'out' },
  admin_adjust:                { icon: RefreshCw,       color: 'text-purple-600',  bg: 'bg-purple-50',  i18n: 'BILL.T_ADMIN_ADJUST',       fallback: '管理员调整',       direction: 'neutral' },
  admin_grant_sub:             { icon: ArrowDownCircle, color: 'text-indigo-600',  bg: 'bg-indigo-50',  i18n: 'BILL.T_ADMIN_GRANT_SUB',    fallback: '管理员赠送订阅',   direction: 'neutral' },
  admin_grant_addon:           { icon: ArrowDownCircle, color: 'text-indigo-600',  bg: 'bg-indigo-50',  i18n: 'BILL.T_ADMIN_GRANT_ADDON',  fallback: '管理员赠送增量包', direction: 'neutral' },
  api_consume_balance:         { icon: Activity,        color: 'text-rose-600',    bg: 'bg-rose-50',    i18n: 'BILL.T_API_BALANCE',        fallback: '余额扣费',         direction: 'out' },
  api_usage_sub:               { icon: Activity,        color: 'text-slate-500',   bg: 'bg-slate-50',   i18n: 'BILL.T_API_SUB',            fallback: '套餐扣额度',       direction: 'usage' },
  api_usage_addon:             { icon: Activity,        color: 'text-slate-500',   bg: 'bg-slate-50',   i18n: 'BILL.T_API_ADDON',          fallback: '增量包扣额度',     direction: 'usage' },
  api_usage_pending_reconcile: { icon: Activity,        color: 'text-yellow-600',  bg: 'bg-yellow-50',  i18n: 'BILL.T_API_PENDING',        fallback: '待对账',           direction: 'neutral' },
};

const fmtUSD = (n) => {
  if (n === undefined || n === null) return '$0.00';
  const sign = n > 0 ? '+' : (n < 0 ? '-' : '');
  return `${sign}$${Math.abs(n).toFixed(4).replace(/0+$/, '').replace(/\.$/, '.00')}`;
};

const fmtTime = (s) => {
  if (!s) return '';
  const d = new Date(s);
  if (Number.isNaN(d.getTime())) return s;
  return d.toLocaleString();
};

const BillsPage = () => {
  const { t } = useTranslation();

  const [entries, setEntries] = useState([]);
  const [summary, setSummary] = useState(null);
  const [loading, setLoading] = useState(true);
  const [page, setPage] = useState(1);
  const [pageSize] = useState(30);
  const [total, setTotal] = useState(0);

  // 筛选状态
  const [selectedTypes, setSelectedTypes] = useState([]); // 空 = 全部
  const [hideUsage, setHideUsage] = useState(true); // 默认折叠 api_usage_sub/addon（量大）
  const [fromDate, setFromDate] = useState('');
  const [toDate, setToDate] = useState('');

  // fix MAJOR M8（gemini 第二十轮）：抑制快速切换筛选/分页时的请求竞态。
  // 旧请求晚于新请求返回时丢弃结果，避免覆盖 entries / total。
  const reqIdRef = useRef(0);
  const summaryReqIdRef = useRef(0);

  const buildQuery = useCallback((extra = {}) => {
    const params = new URLSearchParams();
    let types = [...selectedTypes];
    if (hideUsage && types.length === 0) {
      // 默认排除 usage，等价于"显示所有非 usage 类型"
      types = Object.keys(TYPE_META).filter(
        (k) => k !== 'api_usage_sub' && k !== 'api_usage_addon'
      );
    }
    if (types.length > 0) params.set('types', types.join(','));
    if (fromDate) params.set('from', fromDate);
    if (toDate) params.set('to', toDate);
    Object.entries(extra).forEach(([k, v]) => params.set(k, v));
    return params.toString();
  }, [selectedTypes, hideUsage, fromDate, toDate]);

  const loadEntries = useCallback(async () => {
    const myReqId = ++reqIdRef.current;
    setLoading(true);
    try {
      const qs = buildQuery({ page, page_size: pageSize });
      const json = await authFetch(`/api/billing/mine?${qs}`);
      // M8: 旧请求晚于新请求返回时丢弃结果
      if (myReqId !== reqIdRef.current) return;
      if (json.success) {
        setEntries(json.data || []);
        setTotal(json.meta?.total || 0);
      } else {
        toast.error(t('BILL.LOAD_FAIL', '加载账单失败'));
      }
    } catch (e) {
      if (myReqId !== reqIdRef.current) return;
      toast.error(`${t('BILL.LOAD_FAIL', '加载账单失败')}: ${e.message || e}`);
    } finally {
      if (myReqId === reqIdRef.current) setLoading(false);
    }
  }, [buildQuery, page, pageSize, t]);

  const loadSummary = useCallback(async () => {
    const myReqId = ++summaryReqIdRef.current;
    try {
      const qs = buildQuery();
      const json = await authFetch(`/api/billing/mine/summary?${qs}`);
      if (myReqId !== summaryReqIdRef.current) return;
      if (json.success) setSummary(json.data);
    } catch {
      // 静默：列表已经显示了，summary 失败不阻塞
    }
  }, [buildQuery]);

  useEffect(() => { loadEntries(); }, [loadEntries]);
  useEffect(() => { loadSummary(); }, [loadSummary]);

  // fix Minor（gemini 第十四轮）：原实现 filter 改变同时触发独立的 setPage(1) effect +
  // buildQuery 重建 → 两次顺序 fetch 闪烁。改为在 toggle / 日期变化的 onChange 直接 setPage(1)
  // 同步执行（React 18+ batch 合并 set 调用），避免双 fetch。
  const toggleType = (type) => {
    setPage(1);
    setSelectedTypes((prev) => {
      if (prev.includes(type)) return prev.filter((x) => x !== type);
      return [...prev, type];
    });
  };

  const handleExport = async () => {
    try {
      // fix Major（codex+gemini 第十四轮）：readAuthState 实际返回 { isAdmin, userToken }；
      // 原读 auth.token 为 undefined → Bearer-only 用户的 CSV 导出 401。
      const auth = readAuthState();
      const qs = buildQuery();
      const url = `/api/billing/mine/export?${qs}`;
      const res = await fetch(url, {
        credentials: 'include',
        headers: auth.userToken ? { Authorization: `Bearer ${auth.userToken}` } : {},
      });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const blob = await res.blob();
      const a = document.createElement('a');
      const objectURL = URL.createObjectURL(blob);
      a.href = objectURL;
      a.download = `billing-${new Date().toISOString().slice(0, 10)}.csv`;
      a.click();
      // fix Minor（gemini）：a.click() 是同步派发但下载是异步消费；Firefox/Safari 上立即 revoke
      // 可能导致下载失败。延后 revoke 给浏览器时间消费 blob。
      setTimeout(() => URL.revokeObjectURL(objectURL), 1000);
      toast.success(t('BILL.EXPORT_OK', 'CSV 已下载'));
    } catch (e) {
      toast.error(`${t('BILL.EXPORT_FAIL', '导出失败')}: ${e.message || e}`);
    }
  };

  return (
    <div className="max-w-6xl mx-auto px-4 py-6 space-y-6">
      <header className="flex items-center justify-between flex-wrap gap-4">
        <div className="flex items-center gap-3">
          <div className="w-10 h-10 rounded-xl bg-primary/10 flex items-center justify-center">
            <Receipt className="w-5 h-5 text-primary" />
          </div>
          <div>
            <h1 className="text-2xl font-bold text-on-surface">
              {t('BILL.TITLE', '账单')}
            </h1>
            <p className="text-sm text-on-surface/60">
              {t('BILL.SUBTITLE', '所有金钱进出明细，按时间倒序展示')}
            </p>
          </div>
        </div>
        <div className="flex gap-2">
          <button
            type="button"
            onClick={() => { loadEntries(); loadSummary(); }}
            className="inline-flex items-center gap-1.5 px-3 py-2 rounded-lg border border-outline-variant text-sm hover:bg-on-surface/[0.04]"
          >
            <RefreshCw className="w-4 h-4" />{t('BILL.REFRESH', '刷新')}
          </button>
          <button
            type="button"
            onClick={handleExport}
            className="inline-flex items-center gap-1.5 px-3 py-2 rounded-lg bg-primary text-white text-sm hover:opacity-90"
          >
            <Download className="w-4 h-4" />{t('BILL.EXPORT_CSV', '导出 CSV')}
          </button>
        </div>
      </header>

      {/* 月度汇总卡片 */}
      {summary && (
        <div className="grid grid-cols-2 md:grid-cols-4 gap-3">
          <SummaryCard
            label={t('BILL.SUM_IN', '入账')}
            value={`$${(summary.total_in_usd || 0).toFixed(2)}`}
            color="text-green-600"
            icon={ArrowDownCircle}
          />
          <SummaryCard
            label={t('BILL.SUM_OUT', '消费')}
            value={`$${(summary.total_out_usd || 0).toFixed(2)}`}
            color="text-rose-600"
            icon={ArrowUpCircle}
          />
          <SummaryCard
            label={t('BILL.SUM_NET', '净变动')}
            value={`${summary.net_change_usd >= 0 ? '+' : ''}$${(summary.net_change_usd || 0).toFixed(2)}`}
            color={summary.net_change_usd >= 0 ? 'text-green-600' : 'text-rose-600'}
            icon={Activity}
          />
          <SummaryCard
            label={t('BILL.SUM_BALANCE', '当前余额')}
            value={`$${(summary.current_balance || 0).toFixed(2)}`}
            color="text-on-surface"
            icon={Wallet}
          />
        </div>
      )}

      {/* 筛选 */}
      <section className="rounded-xl bg-surface-container/40 border border-outline-variant/40 p-4 space-y-3">
        <div className="flex items-center gap-2 text-sm font-medium text-on-surface">
          <Filter className="w-4 h-4" />{t('BILL.FILTER', '筛选')}
        </div>
        <div className="flex flex-wrap gap-2">
          {Object.entries(TYPE_META).map(([key, meta]) => {
            const active = selectedTypes.includes(key);
            return (
              <button
                type="button"
                key={key}
                onClick={() => toggleType(key)}
                className={`text-xs px-3 py-1.5 rounded-full border transition ${
                  active
                    ? 'bg-primary text-white border-primary'
                    : 'bg-surface text-on-surface border-outline-variant hover:bg-on-surface/[0.04]'
                }`}
              >
                {t(meta.i18n, meta.fallback)}
              </button>
            );
          })}
        </div>
        <div className="flex flex-wrap items-center gap-3 text-sm">
          <label className="inline-flex items-center gap-1.5">
            <input
              type="checkbox"
              checked={hideUsage}
              onChange={(e) => { setPage(1); setHideUsage(e.target.checked); }}
              className="rounded"
            />
            <span>{t('BILL.HIDE_USAGE', '隐藏 API 用量行（按订阅扣额度）')}</span>
          </label>
          <div className="flex items-center gap-1.5">
            <Calendar className="w-4 h-4 text-on-surface/60" />
            <input
              type="date"
              value={fromDate}
              onChange={(e) => { setPage(1); setFromDate(e.target.value); }}
              className="px-2 py-1 rounded border border-outline-variant bg-surface text-sm"
            />
            <span>→</span>
            <input
              type="date"
              value={toDate}
              onChange={(e) => { setPage(1); setToDate(e.target.value); }}
              className="px-2 py-1 rounded border border-outline-variant bg-surface text-sm"
            />
          </div>
        </div>
      </section>

      {/* 列表 */}
      <section>
        {loading ? (
          <div className="text-center py-12 text-on-surface/60">{t('COMMON.LOADING', '加载中…')}</div>
        ) : entries.length === 0 ? (
          <div className="text-center py-12 text-on-surface/60">
            <Receipt className="w-12 h-12 mx-auto mb-3 opacity-40" />
            <p>{t('BILL.EMPTY', '暂无账单记录')}</p>
          </div>
        ) : (
          <ul className="divide-y divide-outline-variant/30 rounded-xl border border-outline-variant/40 overflow-hidden bg-surface">
            {entries.map((e) => <BillRow key={e.id} entry={e} t={t} />)}
          </ul>
        )}
        {/* fix MAJOR（gemini 第十七轮）：用共用 Pagination 组件 */}
        <Pagination
          page={page}
          pageSize={pageSize}
          total={total}
          loading={loading}
          onPageChange={setPage}
          className="mt-4"
        />
      </section>
    </div>
  );
};

const SummaryCard = ({ label, value, color, icon: Icon }) => (
  <div className="rounded-xl bg-surface-container/40 border border-outline-variant/40 p-4">
    <div className="flex items-center justify-between mb-2">
      <span className="text-xs text-on-surface/60">{label}</span>
      {Icon && <Icon className="w-4 h-4 text-on-surface/40" />}
    </div>
    <div className={`text-xl font-semibold ${color || 'text-on-surface'}`}>{value}</div>
  </div>
);

const BillRow = ({ entry, t }) => {
  const meta = TYPE_META[entry.entry_type] || {
    icon: Activity, color: 'text-on-surface', bg: 'bg-surface-container/30',
    fallback: entry.entry_type, i18n: '',
  };
  const Icon = meta.icon;
  const label = meta.i18n ? t(meta.i18n, meta.fallback) : meta.fallback;
  const isUsage = meta.direction === 'usage';
  const amountText = isUsage
    ? (entry.tokens_total > 0 ? `${entry.tokens_total.toLocaleString()} tok` : '—')
    : fmtUSD(entry.amount_usd);

  return (
    <li className="flex items-center gap-3 px-4 py-3 hover:bg-on-surface/[0.02]">
      <div className={`w-9 h-9 rounded-lg flex items-center justify-center ${meta.bg}`}>
        <Icon className={`w-4 h-4 ${meta.color}`} />
      </div>
      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-2">
          <span className="text-sm font-medium text-on-surface">{label}</span>
          {entry.model_name && (
            <span className="text-xs px-1.5 py-0.5 rounded bg-on-surface/[0.06] text-on-surface/70">
              {entry.model_name}
            </span>
          )}
        </div>
        <div className="text-xs text-on-surface/60 truncate">
          {entry.description || '—'}
          {entry.amount_original && entry.currency_original && (
            <span className="ml-2">
              · {entry.currency_original} {Math.abs(entry.amount_original).toFixed(2)}
            </span>
          )}
        </div>
        <div className="text-xs text-on-surface/40">{fmtTime(entry.occurred_at)}</div>
      </div>
      <div className="text-right shrink-0">
        <div className={`text-sm font-semibold ${
          isUsage
            ? 'text-on-surface/60'
            : entry.amount_usd > 0
              ? 'text-green-600'
              : entry.amount_usd < 0
                ? 'text-rose-600'
                : 'text-on-surface/60'
        }`}>
          {amountText}
        </div>
        {!isUsage && (
          <div className="text-xs text-on-surface/50">
            {t('BILL.BALANCE_AFTER', '余额')} ${(entry.balance_after_usd || 0).toFixed(2)}
          </div>
        )}
      </div>
    </li>
  );
};

export default BillsPage;
