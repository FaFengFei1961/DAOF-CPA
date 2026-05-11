// AdminUserBills - admin 查看任意用户的账单流水。
// 作为模态弹窗嵌入 UserManagement 行操作 → "账单"按钮。
//
// 与 BillsPage 共享数据形态但调用 admin endpoint。i18n 复用 BILL.* 命名空间。
import React, { useState, useEffect, useCallback, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { X, RefreshCw, Download, Receipt, ArrowDownCircle, ArrowUpCircle, Activity } from 'lucide-react';
import toast from 'react-hot-toast';
import { authFetch, readAuthState } from '../utils/authFetch';
import { useModalA11y } from '../hooks/useModalA11y';
import Pagination from './common/Pagination';
import { PAGE_SIZE_DEFAULT } from './common/constants';

// fix MAJOR（codex 第十七轮）：补齐 admin_grant_* + api_usage_pending_reconcile，
// 与后端 allowedBillingTypes 同步——否则 admin 默认隐藏 API 用量时
// 这两类账单也会被排除掉。
const TYPE_I18N = {
  topup:                       { i18n: 'BILL.T_TOPUP',          fallback: '充值' },
  purchase_sub:                { i18n: 'BILL.T_PURCHASE_SUB',   fallback: '购买套餐' },
  purchase_addon:              { i18n: 'BILL.T_PURCHASE_ADDON', fallback: '购买增量包' },
  bonus_credit:                { i18n: 'BILL.T_BONUS',          fallback: '套餐附赠' },
  refund_sub:                  { i18n: 'BILL.T_REFUND_SUB',     fallback: '订阅退款' },
  refund_topup:                { i18n: 'BILL.T_REFUND_TOPUP',   fallback: '充值退款' },
  admin_adjust:                { i18n: 'BILL.T_ADMIN_ADJUST',   fallback: '管理员调整' },
  admin_grant_sub:             { i18n: 'BILL.T_ADMIN_GRANT_SUB',   fallback: '管理员赠送订阅' },
  admin_grant_addon:           { i18n: 'BILL.T_ADMIN_GRANT_ADDON', fallback: '管理员赠送增量包' },
  api_consume_balance:         { i18n: 'BILL.T_API_BALANCE',    fallback: '余额扣费' },
  api_usage_sub:               { i18n: 'BILL.T_API_SUB',        fallback: '套餐扣额度' },
  api_usage_addon:             { i18n: 'BILL.T_API_ADDON',      fallback: '增量包扣额度' },
  api_usage_pending_reconcile: { i18n: 'BILL.T_API_PENDING',    fallback: '待对账' },
};

const fmtUSD = (n) => {
  if (n === undefined || n === null) return '$0.00';
  const sign = n > 0 ? '+' : (n < 0 ? '-' : '');
  return `${sign}$${Math.abs(n).toFixed(4).replace(/0+$/, '').replace(/\.$/, '.00')}`;
};

const AdminUserBills = ({ userId, username, onClose }) => {
  const { t } = useTranslation();

  const [entries, setEntries] = useState([]);
  const [summary, setSummary] = useState(null);
  const [loading, setLoading] = useState(true);
  const [hideUsage, setHideUsage] = useState(true);
  const [page, setPage] = useState(1);
  const [total, setTotal] = useState(0);
  const closeBtnRef = useRef(null);
  const modalRef = useRef(null); // C5 第二十轮: focus trap 范围
  // fix Minor（codex 第十七轮）：抑制 hideUsage 切换的竞态——切换瞬间 page!=1 时
  // 先 setPage(1) 触发新 load，期间忽略旧 load 的回调避免覆盖新页数据
  const reqIdRef = useRef(0);

  const buildQuery = useCallback(() => {
    if (!hideUsage) return '';
    // 仅在勾选"隐藏 API 用量"时排除 api_usage_*；其他全部展示（含 admin_grant_* 与 pending_reconcile）
    const types = Object.keys(TYPE_I18N)
      .filter((k) => k !== 'api_usage_sub' && k !== 'api_usage_addon');
    return `types=${types.join(',')}`;
  }, [hideUsage]);

  const load = useCallback(async () => {
    if (!userId) return;
    const myReqId = ++reqIdRef.current;
    setLoading(true);
    try {
      const qs = buildQuery();
      const [listJson, sumJson] = await Promise.all([
        authFetch(`/api/admin/billing/users/${userId}?page=${page}&page_size=${PAGE_SIZE_DEFAULT}${qs ? '&' + qs : ''}`),
        authFetch(`/api/admin/billing/users/${userId}/summary${qs ? '?' + qs : ''}`),
      ]);
      // 旧请求晚于新请求返回时丢弃结果（防 hideUsage 切换覆盖 page=1 新数据）
      if (myReqId !== reqIdRef.current) return;
      if (listJson.success) {
        setEntries(listJson.data || []);
        setTotal(listJson.meta?.total || 0);
      }
      if (sumJson.success) setSummary(sumJson.data);
    } catch (e) {
      if (myReqId !== reqIdRef.current) return;
      toast.error(`${t('BILL.LOAD_FAIL', '加载账单失败')}: ${e.message || e}`);
    } finally {
      if (myReqId === reqIdRef.current) {
        setLoading(false);
      }
    }
  }, [userId, buildQuery, t, page]);

  useEffect(() => { load(); }, [load]);

  // 切换 hideUsage 筛选时回到第一页（避免新筛选下 page 越界）
  useEffect(() => {
    setPage(1);
  }, [hideUsage]);

  // a11y：ESC 关闭 + 背景点击 + 自动焦点统一走 useModalA11y hook
  // （第十八轮 H4 修复后 hook 支持 initialFocusRef，不再需要手写 useEffect 聚焦）
  const { onBackdropClick } = useModalA11y(true, onClose, closeBtnRef, modalRef);

  const handleExport = async () => {
    try {
      // fix Major（codex+gemini）：readAuthState 返回 userToken（非 token）
      const auth = readAuthState();
      const qs = buildQuery();
      const url = `/api/admin/billing/users/${userId}/export${qs ? '?' + qs : ''}`;
      const res = await fetch(url, {
        credentials: 'include',
        headers: auth.userToken ? { Authorization: `Bearer ${auth.userToken}` } : {},
      });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const blob = await res.blob();
      const a = document.createElement('a');
      const objectURL = URL.createObjectURL(blob);
      a.href = objectURL;
      a.download = `billing-user-${userId}-${new Date().toISOString().slice(0, 10)}.csv`;
      a.click();
      setTimeout(() => URL.revokeObjectURL(objectURL), 1000);
      toast.success(t('BILL.EXPORT_OK', 'CSV 已下载'));
    } catch (e) {
      toast.error(`${t('BILL.EXPORT_FAIL', '导出失败')}: ${e.message || e}`);
    }
  };

  return (
    <div
      ref={modalRef}
      role="dialog"
      aria-modal="true"
      aria-labelledby="admin-bills-title"
      onClick={onBackdropClick}
      className="fixed inset-0 z-[60] flex items-center justify-center p-4 bg-black/70 backdrop-blur-md"
    >
      <div className="bg-surface w-full max-w-5xl max-h-[90vh] rounded-xl shadow-2xl overflow-hidden flex flex-col">
        <div className="flex items-center justify-between px-6 py-4 border-b border-outline-variant/40">
          <div className="flex items-center gap-3">
            <div className="w-9 h-9 rounded-lg bg-primary/10 flex items-center justify-center">
              <Receipt className="w-4 h-4 text-primary" />
            </div>
            <div>
              <h2 id="admin-bills-title" className="text-lg font-semibold text-on-surface">
                {t('BILL.ADMIN_USER_TITLE', '用户账单')} · {username || `#${userId}`}
              </h2>
              <p className="text-xs text-on-surface/60">{t('BILL.ADMIN_USER_ID', '用户 ID')} #{userId}</p>
            </div>
          </div>
          <div className="flex items-center gap-2">
            <button
              type="button"
              onClick={load}
              className="p-2 rounded-lg hover:bg-on-surface/[0.04]"
              title={t('BILL.REFRESH', '刷新')}
              aria-label={t('BILL.REFRESH', '刷新')}
            >
              <RefreshCw className="w-4 h-4" />
            </button>
            <button
              type="button"
              onClick={handleExport}
              className="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-primary text-white text-sm hover:opacity-90"
            >
              <Download className="w-4 h-4" />{t('BILL.EXPORT_CSV', '导出 CSV')}
            </button>
            <button
              ref={closeBtnRef}
              type="button"
              onClick={onClose}
              className="p-2 rounded-lg hover:bg-on-surface/[0.04]"
              aria-label={t('COMMON.CLOSE', '关闭')}
            >
              <X className="w-4 h-4" />
            </button>
          </div>
        </div>

        {/* 汇总卡片 */}
        {summary && (
          <div className="grid grid-cols-2 md:grid-cols-4 gap-3 p-4 border-b border-outline-variant/30 bg-surface-container/40">
            <SumCard label={t('BILL.SUM_IN', '入账')} value={`$${(summary.total_in_usd || 0).toFixed(2)}`} color="text-green-600" />
            <SumCard label={t('BILL.SUM_OUT', '消费')} value={`$${(summary.total_out_usd || 0).toFixed(2)}`} color="text-rose-600" />
            <SumCard
              label={t('BILL.SUM_NET', '净变动')}
              value={`${summary.net_change_usd >= 0 ? '+' : ''}$${(summary.net_change_usd || 0).toFixed(2)}`}
              color={summary.net_change_usd >= 0 ? 'text-green-600' : 'text-rose-600'}
            />
            <SumCard label={t('BILL.SUM_BALANCE', '当前余额')} value={`$${(summary.current_balance || 0).toFixed(2)}`} color="text-on-surface" />
          </div>
        )}

        {/* 筛选切换 */}
        <div className="px-4 py-2 border-b border-outline-variant/30 text-sm">
          <label className="inline-flex items-center gap-1.5">
            <input
              type="checkbox"
              checked={hideUsage}
              onChange={(e) => setHideUsage(e.target.checked)}
            />
            <span>{t('BILL.HIDE_USAGE', '隐藏 API 用量行（按订阅扣额度）')}</span>
          </label>
        </div>

        {/* 列表 */}
        {/* fix CRITICAL（gemini 第十七轮）：移除外层 aria-live 反模式 */}
        <div className="flex-1 overflow-y-auto">
          {loading ? (
            <div role="status" aria-live="polite" className="text-center py-12 text-on-surface/60">{t('COMMON.LOADING', '加载中…')}</div>
          ) : total === 0 ? (
            <div className="text-center py-12 text-on-surface/60">{t('BILL.ADMIN_USER_EMPTY', '该用户暂无账单')}</div>
          ) : (
            <ul className="divide-y divide-outline-variant/30">
              {entries.map((e) => <AdminBillRow key={e.id} entry={e} t={t} />)}
            </ul>
          )}
        </div>

        {/* fix MAJOR（gemini 第十七轮）：用共用 Pagination 组件 */}
        <Pagination
          page={page}
          pageSize={PAGE_SIZE_DEFAULT}
          total={total}
          loading={loading}
          onPageChange={setPage}
          className="px-4 py-3 border-t border-outline-variant/30 bg-surface-container/30"
        />
      </div>
    </div>
  );
};

const SumCard = ({ label, value, color }) => (
  <div className="rounded-lg bg-surface border border-outline-variant/40 p-3">
    <div className="text-xs text-on-surface/60 mb-1">{label}</div>
    <div className={`text-lg font-semibold ${color}`}>{value}</div>
  </div>
);

const AdminBillRow = ({ entry, t }) => {
  const isCredit = entry.amount_usd > 0;
  const isUsage = entry.entry_type === 'api_usage_sub' || entry.entry_type === 'api_usage_addon';
  const Icon = isUsage ? Activity : (isCredit ? ArrowDownCircle : ArrowUpCircle);
  const iconColor = isUsage
    ? 'text-slate-500'
    : isCredit ? 'text-green-600' : 'text-rose-600';
  const meta = TYPE_I18N[entry.entry_type] || { i18n: '', fallback: entry.entry_type };

  return (
    <li className="flex items-center gap-3 px-4 py-3">
      <Icon className={`w-5 h-5 shrink-0 ${iconColor}`} />
      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-2">
          <span className="text-sm font-medium">
            {meta.i18n ? t(meta.i18n, meta.fallback) : meta.fallback}
          </span>
          {entry.model_name && (
            <span className="text-xs px-1.5 py-0.5 rounded bg-on-surface/[0.06] text-on-surface/70">
              {entry.model_name}
            </span>
          )}
        </div>
        <div className="text-xs text-on-surface/60 truncate">
          {entry.description || '—'}
        </div>
        <div className="text-xs text-on-surface/40">
          {entry.occurred_at && new Date(entry.occurred_at).toLocaleString()}
          {entry.related_type && ` · ${entry.related_type}#${entry.related_id}`}
        </div>
      </div>
      <div className="text-right shrink-0">
        <div className={`text-sm font-semibold ${
          isUsage ? 'text-on-surface/60' :
          isCredit ? 'text-green-600' : 'text-rose-600'
        }`}>
          {isUsage
            ? (entry.tokens_total > 0 ? `${entry.tokens_total.toLocaleString()} tok` : '—')
            : fmtUSD(entry.amount_usd)}
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

export default AdminUserBills;
