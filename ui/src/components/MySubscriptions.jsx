import React, { useState, useEffect, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { Package as PackageIcon, Clock, X } from 'lucide-react';
import toast from 'react-hot-toast';
import { useConfirm } from '../context/ConfirmContext';
import { remainingColor, fmtTime, safePct } from '../utils/credits';
import { authFetch } from '../utils/authFetch';
import { StorePage, StoreSection } from './store/StorePrimitives';

// embedded=true 时由父组件提供页面级容器（hero/StorePage），自身只渲染列表内容。
// 用于把"我的产品"合并到"产品中心"，避免双 hero。
const MySubscriptions = ({ isAuthenticated = true, embedded = false }) => {
  const { t } = useTranslation();
  const confirm = useConfirm();
  const [subs, setSubs] = useState([]);
  const [loading, setLoading] = useState(isAuthenticated);

  // 用 ref 跟踪挂载状态，避免在 unmount 后 setState（embedded 模式快速切换 pane 时尤其重要）
  const mountedRef = React.useRef(true);
  useEffect(() => {
    mountedRef.current = true;
    return () => { mountedRef.current = false; };
  }, []);

  const load = useCallback(async () => {
    if (!isAuthenticated) return;
    try {
      const json = await authFetch('/api/subscriptions/mine');
      if (!mountedRef.current) return;
      if (json.success) setSubs(json.data || []);
      else toast.error(json.message || t('SUB.LOAD_FAIL', '加载失败'));
    } catch {
      if (mountedRef.current) toast.error(t('SUB.LOAD_FAIL', '加载失败'));
    } finally {
      if (mountedRef.current) setLoading(false);
    }
  }, [isAuthenticated, t]);

  useEffect(() => { load(); }, [load]);

  // fix Minor（codex r10）：业务模型已改为"取消不退款，退款须 admin 协商"。
  // 前端文案需同步：取消提示改为"立即停止订阅"，成功 toast 改为"已取消，如需退款请联系客服"。
  const cancel = async (sub) => {
    const msg = t('SUB.CANCEL_CONFIRM', { name: sub.package_name, defaultValue: '取消订阅「{{name}}」？\n\n订阅将立即停止消费您的额度。如需退款，请通过客服工单提交申请。' });
    if (!(await confirm(msg))) return;
    try {
      const json = await authFetch(`/api/subscriptions/${sub.id}/cancel`, { method: 'POST' });
      if (json.success) {
        toast.success(t('SUB.CANCEL_OK', '订阅已取消。如需退款请联系客服。'));
        load();
      } else toast.error(json.message || t('SUB.CANCEL_FAIL', '取消失败'));
    } catch {
      toast.error(t('SUB.CANCEL_NET_ERR', '网络异常，取消失败'));
    }
  };

  const activeSubs = subs.filter(s => s.status === 'active');
  // 从 package_snapshot 读 product_type（snapshot 字符串 JSON）
  const getProductType = (sub) => {
    try {
      const snap = typeof sub.package_snapshot === 'string' ? JSON.parse(sub.package_snapshot) : (sub.package_snapshot || {});
      return snap.product_type || 'subscription';
    } catch { return 'subscription'; }
  };
  const subscriptionGroup = activeSubs.filter(s => getProductType(s) === 'subscription');
  const addonGroup = activeSubs.filter(s => getProductType(s) === 'addon');

  // 列表内容（无 hero/容器）—— 给 embedded 模式直接复用
  const body = (
    loading ? (
      <div className="text-center py-20 text-on-surface-variant">{t('SUB.LOADING', '加载中...')}</div>
    ) : (
      <>
            {/* 订阅 */}
            <StoreSection title={t('MY_PRODUCTS.GROUP_SUBSCRIPTION', '订阅')}>
              {subscriptionGroup.length === 0 ? (
                <div className="fl-card p-8 text-center text-sm text-on-surface-variant">
                  {t('MY_PRODUCTS.GROUP_SUB_EMPTY', '暂无活跃订阅')}
                </div>
              ) : (
                <div className="space-y-3">
                  {subscriptionGroup.map((sub, idx) => <SubCard key={sub.id} sub={sub} priority={idx === 0} onCancel={() => cancel(sub)} t={t} />)}
                </div>
              )}
            </StoreSection>

            {/* 增量包 */}
            <StoreSection title={t('MY_PRODUCTS.GROUP_ADDON', '增量包')}>
              {addonGroup.length === 0 ? (
                <div className="fl-card p-8 text-center text-sm text-on-surface-variant">
                  {t('MY_PRODUCTS.GROUP_ADDON_EMPTY', '暂无活跃增量包')}
                </div>
              ) : (
                <div className="space-y-3">
                  {addonGroup.map((sub) => <SubCard key={sub.id} sub={sub} priority={false} onCancel={() => cancel(sub)} t={t} />)}
                </div>
              )}
            </StoreSection>

            {/* fix UX 反馈（用户 2026-05-10）：原"余额"区块直接嵌入 BalanceConsumePreferences 完整配置面板，
                既错位（余额不是产品）又冗余（顶栏已显示余额、账号设置已有专属配置页）。已删除。
                subtitle 仍引导用户去"账号设置 → 余额消费控制"配置三段消费的最后一段。 */}
      </>
    )
  );

  if (embedded) {
    return <div className="space-y-8">{body}</div>;
  }

  return (
    <div className="w-full max-w-6xl mx-auto px-4 md:px-8 py-8">
      <StorePage
        icon={PackageIcon}
        title={t('MY_PRODUCTS.TITLE', '我的产品')}
        subtitle={t('MY_PRODUCTS.SUBTITLE', '订阅最先消耗；订阅用尽后扣增量包；都用完才走余额扣费（在账号设置中开启）。')}
      >
        {body}
      </StorePage>
    </div>
  );
};

const SubCard = ({ sub, priority, onCancel, t }) => {
  const daysLeft = Math.max(0, Math.round((new Date(sub.end_at).getTime() - Date.now()) / 86400000));

  return (
    <div className={`fl-card p-6 ${priority ? 'border-primary' : ''}`}>
      <div className="flex items-start justify-between mb-4 pb-4 border-b border-outline-variant/30">
        <div>
          <div className="flex items-center gap-2 mb-1">
            <span className="font-bold text-lg">{sub.package_name}</span>
            <span className="text-xs px-2 py-0.5 rounded bg-primary/10 text-primary font-mono">#{sub.stack_index}</span>
            {priority && <span className="text-xs px-2 py-0.5 rounded bg-emerald-500/20 text-emerald-400">⚡ {t('SUB.ACTIVE_TAG', '优先消费中')}</span>}
            {!priority && <span className="text-xs px-2 py-0.5 rounded bg-surface-container-high text-outline">{t('SUB.QUEUED_TAG', '排队中')}</span>}
          </div>
          <div className="text-xs text-on-surface-variant flex items-center gap-3">
            <span><Clock size={11} className="inline mr-1" />{t('SUB.DAYS_LEFT', { n: daysLeft, defaultValue: '剩 {{n}} 天' })}</span>
            <span>{fmtTime(sub.start_at)} - {fmtTime(sub.end_at)}</span>
          </div>
        </div>
        <button type="button" onClick={onCancel} className="p-2 text-on-surface-variant hover:text-error" title={t('SUB.CANCEL_BTN', '取消订阅')}>
          <X size={16} />
        </button>
      </div>

      <div className="space-y-3">
        <div className="text-xs text-on-surface-variant mb-2">{t('SUB.QUOTA_USAGE', '配额使用情况')}</div>
        {(sub.usage || []).length === 0 ? (
          <div className="text-xs text-outline italic">{t('SUB.NOT_USED', '尚未使用')}</div>
        ) : (
          (sub.usage || []).map(u => {
            // safeParseJSON 失败返回 null；?.plans 也可能是 undefined
            // 用 ?? [] 兜底，避免在 undefined 上调 .find 抛 TypeError
            const planList = (sub.package_snapshot ? safeParseJSON(sub.package_snapshot)?.plans : null) ?? [];
            const plan = planList.find(p => p.id === u.quota_plan_id);
            // H-6 修复：?? 而非 ||，避免 limit_value=0（无限额）被错误兜底为 1
            const limit = plan?.limit_value ?? 1;
            const used = u.consumed_value ?? 0;
            const remaining = Math.max(0, limit - used);
            const remainingPct = limit > 0 ? (remaining / limit) * 100 : 100;
            const color = remainingColor(remainingPct);
            return (
              <div key={u.id}>
                <div className="flex items-center justify-between text-xs mb-1">
                  <span className="font-mono">{plan?.name || u.model_bucket} ({plan?.limit_unit || 'unknown'})</span>
                  <span className="font-mono" style={{ color }}>
                    {used.toFixed(2)} / {limit.toFixed(2)}
                  </span>
                </div>
                <div className="h-1.5 rounded-full bg-black/40 overflow-hidden">
                  <div className="h-full transition-all" style={{ width: `${safePct(100 - remainingPct)}%`, background: color }} />
                </div>
              </div>
            );
          })
        )}
      </div>
    </div>
  );
};

const safeParseJSON = (s) => { try { return JSON.parse(s); } catch { return null; } };

export default MySubscriptions;
