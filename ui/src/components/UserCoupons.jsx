import React, { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Ticket, Check, X, Clock } from 'lucide-react';
import { authFetch } from '../utils/authFetch';
import toast from 'react-hot-toast';

/**
 * UserCoupons — 用户中心"我的券"列表
 *
 * 后端返回带 effective_status 字段（available + 未过期 / used / expired / revoked），
 * 前端按 effective_status 分组展示。
 */
const UserCoupons = () => {
  const { t } = useTranslation();
  const [list, setList] = useState([]);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let alive = true;
    setLoading(true);
    authFetch('/api/coupons/my')
      .then((j) => {
        if (!alive) return;
        if (j?.success) setList(j.data || []);
        else toast.error(j?.message || t('COUPON.LOAD_FAIL', '加载失败'));
      })
      .catch(() => alive && toast.error(t('API.ERR_NETWORK', '网络异常')))
      .finally(() => alive && setLoading(false));
    return () => { alive = false; };
  }, [t]);

  if (loading) {
    return <div className="text-on-surface-variant text-sm py-8 text-center">{t('COUPON.LOADING', '加载中...')}</div>;
  }
  if (list.length === 0) {
    return (
      <div className="text-on-surface-variant text-sm py-12 text-center flex flex-col items-center gap-3">
        <Ticket size={32} className="text-outline-variant" aria-hidden="true" />
        {t('COUPON.MY_EMPTY', '暂无优惠券')}
      </div>
    );
  }

  // 按 effective_status 分组
  const groups = {
    available: list.filter((x) => x.effective_status === 'available'),
    used: list.filter((x) => x.effective_status === 'used'),
    expired: list.filter((x) => x.effective_status === 'expired'),
    revoked: list.filter((x) => x.effective_status === 'revoked'),
  };

  const groupTitles = {
    available: t('COUPON.STATUS_AVAILABLE', '可用'),
    used: t('COUPON.STATUS_USED', '已使用'),
    expired: t('COUPON.STATUS_EXPIRED', '已过期'),
    revoked: t('COUPON.STATUS_REVOKED', '已撤销'),
  };

  return (
    <div className="space-y-6">
      {Object.entries(groups).map(([status, items]) => {
        if (items.length === 0) return null;
        return (
          <div key={status}>
            <h3 className="text-sm font-semibold text-on-surface-variant mb-3 flex items-center gap-2">
              {status === 'available' && <Check size={14} className="text-emerald-400" aria-hidden="true" />}
              {status === 'used' && <Check size={14} className="text-zinc-400" aria-hidden="true" />}
              {status === 'expired' && <Clock size={14} className="text-amber-400" aria-hidden="true" />}
              {status === 'revoked' && <X size={14} className="text-rose-400" aria-hidden="true" />}
              {groupTitles[status]} ({items.length})
            </h3>
            <ul className="space-y-2">
              {items.map((c) => <CouponCard key={c.id} coupon={c} t={t} />)}
            </ul>
          </div>
        );
      })}
    </div>
  );
};

const CouponCard = ({ coupon, t }) => {
  const isAvailable = coupon.effective_status === 'available';
  const expires = coupon.expires_at ? new Date(coupon.expires_at).toLocaleDateString() : null;
  return (
    <li className={`fl-card p-4 flex items-start gap-3 ${isAvailable ? 'border-l-4 border-l-emerald-400' : 'opacity-60'}`}>
      <Ticket size={20} className={isAvailable ? 'text-emerald-400 mt-0.5' : 'text-on-surface-variant mt-0.5'} aria-hidden="true" />
      <div className="flex-1 min-w-0">
        <div className="font-semibold text-on-surface">{coupon.snapshot_name}</div>
        {coupon.snapshot_type === 'fixed_price' && (
          <div className="text-xs text-emerald-400 mt-0.5">
            {t('COUPON.CARD_FIXED', '券价：${{p}}', { p: coupon.snapshot_value })}
          </div>
        )}
        <div className="text-xs text-on-surface-variant mt-1">
          {t('COUPON.CARD_CODE', '券码：')}<code className="font-mono">{coupon.code}</code>
        </div>
        {expires && (
          <div className="text-xs text-on-surface-variant mt-0.5">
            {t('COUPON.CARD_EXPIRES', '过期：{{d}}', { d: expires })}
          </div>
        )}
        {coupon.grant_reason && (
          <div className="text-xs text-on-surface-variant mt-0.5 italic">
            {t('COUPON.CARD_REASON', '理由：{{r}}', { r: coupon.grant_reason })}
          </div>
        )}
      </div>
    </li>
  );
};

export default UserCoupons;
