import React, { useState, useEffect, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { useSearchParams } from 'react-router-dom';
import { ShoppingCart, Check, Layers, Sparkles, Cpu, Zap, Activity, Package as PackageIcon, BrainCircuit, Bot } from 'lucide-react';
import toast from 'react-hot-toast';
import { authFetch, isLoggedIn, readAuthState } from '../utils/authFetch';
import { clearPageCache, isPageCacheFresh, readPageCache, writePageCache } from '../utils/pageCache';
import { useAuth } from '../context/AuthContext';
import { useCurrency } from '../context/CurrencyContext';
import { formatDuration } from './DurationInput';
import { StorePage } from './store/StorePrimitives';
import Select from './ui/Select';
import Modal from './ui/Modal';
import MySubscriptions from './MySubscriptions';



const ICON_MAP = {
  Sparkles, Cpu, Zap, Activity, Layers, PackageIcon,
  anthropic: BrainCircuit,
  claude: BrainCircuit,
  codex: Bot,
  openai: Bot,
  google: Sparkles,
  gemini: Sparkles,
  combo: Layers,
  trinity: Layers,
};
const pickIcon = (key) => {
  const raw = String(key || '').trim();
  return ICON_MAP[raw] || ICON_MAP[raw.toLowerCase()] || PackageIcon;
};
const UPGRADE_CACHE_TTL_MS = 60000;
const PACKAGE_CACHE_KEY = 'upgrade:packages:v4';
const getCouponCacheKey = () => {
  const { isAdmin, userToken } = readAuthState();
  return `upgrade:coupons:${isAdmin ? 'admin' : userToken || 'guest'}`;
};






//




const displayPackageName = (pkg) => String(pkg.name || '');
const displayPackageDescription = (pkg) => String(pkg.description || '');
// Audit DELETE-3 fix: legacyMap 已删（subscription_seeds 已存中文 highlight_tag，
// 旧英文标签 Pro/Max 5x/Max 20x 在公测期数据库里不可能存在）
const displayHighlightTag = (tag) => String(tag || '').trim();

const PLAN_LIMIT_CALLS_UNIT = '\u6b21\u8c03\u7528';
// Audit DELETE-4 fix: LEGACY_TRINITY_NAME \u5df2\u5220\uff08subscription_seeds \u5df2\u5b58 'Combo'\uff09

const formatPlanLimit = (plan, t) => {
  const value = Number(plan?.limit_value || 0);
  if (value <= 0) return '';
  const unit = String(plan?.limit_label || plan?.limit_unit || '').trim();
  const displayValue = Number.isInteger(value) ? String(value) : value.toFixed(2).replace(/0+$/, '').replace(/\.$/, '');
  if (!unit) return displayValue;
  if (unit === PLAN_LIMIT_CALLS_UNIT) return t('UPGRADE.PLAN_LIMIT_CALLS', '{{value}} 次调用', { value: displayValue });
  if (unit === 'Tokens') return `${displayValue} Tokens`;
  return `${displayValue} ${unit}`;
};


const sortStorePackages = (packages) =>
  [...packages].sort((a, b) =>
    (a.sort_order || 0) - (b.sort_order || 0) ||
    String(a.name || '').localeCompare(String(b.name || ''))
  );

/**
 * UpgradePage 只作为 BrowsePackagesModal 的嵌入内容渲染（embedded=true）：
 *   - 强制 pane='store'，不读/不写 URL ?pane（避免污染外层路由）
 *   - 不渲染 mine/store tab 切换器
 *   - 不渲染 MySubscriptions（避免 Dashboard → Modal → MySubscriptions → "浏览套餐" 递归）
 */
const UpgradePage = ({ onPurchaseSuccess, embedded = false }) => {



  const { t } = useTranslation();
  const { formatCurrency } = useCurrency();
  const { isAuthenticated, openLogin } = useAuth();
  const onSignIn = openLogin;
  const couponCacheKey = React.useMemo(getCouponCacheKey, [isAuthenticated]);
  const [pkgs, setPkgs] = useState(() => readPageCache(PACKAGE_CACHE_KEY) || []);
  const [coupons, setCoupons] = useState(() => (isAuthenticated ? readPageCache(couponCacheKey) : null) || []);
  const [loading, setLoading] = useState(() => !readPageCache(PACKAGE_CACHE_KEY));
  const [purchasing, setPurchasing] = useState(null);
  const [purchaseDraft, setPurchaseDraft] = useState(null);




  const [searchParams, setSearchParams] = useSearchParams();
  const paneFromUrl = searchParams.get('pane');
  const [pane, setPane] = useState(() => {
    if (embedded) return 'store';
    if (paneFromUrl === 'mine' || paneFromUrl === 'store') return paneFromUrl;
    return isLoggedIn() ? 'mine' : 'store';
  });

  useEffect(() => {
    if (embedded) return;
    if (paneFromUrl === 'mine' || paneFromUrl === 'store') setPane(paneFromUrl);
  }, [paneFromUrl, embedded]);
  const setPaneAndUrl = useCallback((next) => {
    setPane(next);
    if (embedded) return;
    const params = new URLSearchParams(searchParams);
    params.set('pane', next);
    setSearchParams(params, { replace: true });
  }, [searchParams, setSearchParams, embedded]);





  const load = useCallback(async ({ force = false } = {}) => {
    const cachedPkgs = readPageCache(PACKAGE_CACHE_KEY);
    const cachedCoupons = isAuthenticated ? readPageCache(couponCacheKey) : [];
    const hasUsableCache = !!cachedPkgs && (!isAuthenticated || !!cachedCoupons);

    if (cachedPkgs) setPkgs(cachedPkgs);
    if (isAuthenticated && cachedCoupons) setCoupons(cachedCoupons);
    if (!isAuthenticated) setCoupons([]);

    if (hasUsableCache) {
      setLoading(false);
      const packagesFresh = isPageCacheFresh(PACKAGE_CACHE_KEY, UPGRADE_CACHE_TTL_MS);
      const couponsFresh = !isAuthenticated || isPageCacheFresh(couponCacheKey, UPGRADE_CACHE_TTL_MS);
      if (!force && packagesFresh && couponsFresh) return;
    } else {
      setLoading(true);
    }

    try {
      const requests = [authFetch('/api/packages')];
      if (isAuthenticated) requests.push(authFetch('/api/coupons/my'));
      const results = await Promise.all(requests);
      const pkgJson = results[0];
      if (pkgJson?.success) {
        const nextPkgs = pkgJson.data || [];
        writePageCache(PACKAGE_CACHE_KEY, nextPkgs);
        setPkgs(nextPkgs);
      }
      if (isAuthenticated && results[1]?.success) {

        const nextCoupons = (results[1].data || []).filter((c) => c.effective_status === 'available');
        writePageCache(couponCacheKey, nextCoupons);
        setCoupons(nextCoupons);
      } else {
        setCoupons([]);
      }
    } catch { toast.error(t('UPGRADE.LOAD_FAIL', '加载失败')); }
    finally { setLoading(false); }
  }, [t, isAuthenticated, couponCacheKey]);


  useEffect(() => { load(); }, [load]);


  const usableCouponsForPkg = (pkgId) => coupons.filter((c) => {
    let allowed = [];
    try {
      const arr = JSON.parse(c.snapshot_package_ids || '[]');
      if (Array.isArray(arr)) allowed = arr;
    } catch { /* ignore */ }
    return allowed.length === 0 || allowed.some((id) => String(id) === String(pkgId));
  });


  const effectivePriceFor = (pkg, couponId) => {
    if (!couponId) return pkg.price_amount;
    const c = coupons.find((x) => x.id === couponId);
    if (!c) return pkg.price_amount;
    if (c.snapshot_type === 'fixed_price' && c.snapshot_value < pkg.price_amount) {
      return c.snapshot_value;
    }
    return pkg.price_amount;
  };

  const couponLabelFor = (pkg, coupon) => {
    const finalPrice = effectivePriceFor(pkg, coupon.id);
    const saving = Math.max(0, Number(pkg.price_amount || 0) - Number(finalPrice || 0));
    if (saving > 0) {
      return t('UPGRADE.COUPON_OPTION_WITH_SAVING', '{{name}} - {{price}}，省 {{saving}}', {
        name: coupon.snapshot_name,
        price: formatCurrency(Number(finalPrice || 0), 2),
        saving: formatCurrency(saving, 2),
      });
    }
    return String(coupon.snapshot_name || coupon.code || coupon.id);
  };

  const openPurchaseDialog = (pkg) => {
    if (!isLoggedIn()) {

      if (onSignIn) onSignIn();
      else toast.error(t('UPGRADE.LOGIN_REQUIRED', '请先登录后再购买'));
      return;
    }
    setPurchaseDraft({ pkg, couponId: 0 });
  };

  const purchase = async () => {
    if (!purchaseDraft?.pkg) return;
    const pkg = purchaseDraft.pkg;
    const couponId = Number(purchaseDraft.couponId || 0);

    setPurchasing(pkg.id);
    try {
      const body = { package_id: pkg.id, quantity: 1 };
      if (couponId > 0) body.coupon_id = couponId;
      const json = await authFetch('/api/subscriptions/purchase', { method: 'POST', body });
      if (json.success) {
        toast.success(t('UPGRADE.PURCHASE_OK', '🎉 购买成功'));

        setPaneAndUrl('mine');
        clearPageCache('subscriptions:');
        clearPageCache('billing:');
        clearPageCache('user-coupons:');
        window.dispatchEvent(new CustomEvent('user-profile-refresh'));

        setPurchaseDraft(null);
        load({ force: true });
        if (onPurchaseSuccess) onPurchaseSuccess(json);
      } else {
        // Bug fix（"双重弹窗"反馈）：
        //   1) 关闭确认购买 modal —— 之前失败时只在 success 分支 reset，导致用户
        //      看到错误 toast 同时 modal 还覆盖在上面，得手动点取消，体验崩坏。
        //   2) 跳过 402 重复 toast —— authFetch.js 已经针对 402 弹了带「去充值」
        //      按钮的 toast；这里再 toast 一次 json.message 就成了双重错误提示，
        //      其它非 402 错误才回退到 json.message。
        setPurchaseDraft(null);
        if (json.status !== 402) {
          toast.error(json.message || t('UPGRADE.PURCHASE_FAIL', '购买失败'));
        }
      }
    } catch {
      setPurchaseDraft(null);
      toast.error(t('API.ERR_NETWORK', '网络异常'));
    }
    finally { setPurchasing(null); }
  };

  const purchasePkg = purchaseDraft?.pkg || null;
  const purchaseUsableCoupons = purchasePkg ? usableCouponsForPkg(purchasePkg.id) : [];
  const purchaseCouponId = Number(purchaseDraft?.couponId || 0);
  const purchaseFinalPrice = purchasePkg ? effectivePriceFor(purchasePkg, purchaseCouponId) : 0;
  const purchaseOriginalPrice = Number(purchasePkg?.price_amount || 0);
  const purchaseSaving = Math.max(0, purchaseOriginalPrice - Number(purchaseFinalPrice || 0));
  const purchaseSelectedCoupon = purchaseCouponId > 0 ? coupons.find((c) => c.id === purchaseCouponId) : null;

  return (

    <div className="w-full">
      <StorePage>


      {!embedded && isAuthenticated && (
        <div className="inline-flex rounded-overlay border border-outline-variant bg-surface-container p-0.5 self-start">
          {[
            { id: 'mine', label: t('UPGRADE.PANE_MINE', '我的') },
            { id: 'store', label: t('UPGRADE.PANE_STORE', '商店') },
          ].map(p => {
            const active = pane === p.id;
            return (
              <button
                key={p.id}
                type="button"
                onClick={() => setPaneAndUrl(p.id)}
                className={`px-6 py-2 rounded-control text-sm font-semibold transition ${active
                  ? 'bg-primary text-on-primary'
                  : 'text-on-surface-variant hover:text-on-surface'}`}
              >
                {p.label}
              </button>
            );
          })}
        </div>
      )}


      {!embedded && isAuthenticated && pane === 'mine' && <MySubscriptions isAuthenticated={isLoggedIn()} embedded />}


      {(embedded || !isAuthenticated || pane === 'store') && (<>
      {loading ? <div className="text-center py-20 text-on-surface-variant">{t('UPGRADE.LOADING', '加载中...')}</div>
        : (() => {
          const filtered = pkgs;
          if (filtered.length === 0) {
            return (
              <div className="card p-16 text-center">
                <p className="text-on-surface-variant">{t('UPGRADE.STORE_EMPTY', '此分类暂无可购买的套餐')}</p>
              </div>
            );
          }
          const sorted = sortStorePackages(filtered);
          return (
          <>
          <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-3">
            {sorted.map(pkg => {
              const Icon = pickIcon(pkg.icon_key);
              const shownName = displayPackageName(pkg);
              const shownDescription = displayPackageDescription(pkg);
              const originalPriceText = formatCurrency(Number(pkg.price_amount || 0), 2);
              const highlightTag = displayHighlightTag(pkg.highlight_tag);
              const isRecommended = !!highlightTag;
              return (
                <div key={pkg.id}
                  className={`relative card p-6 ${isRecommended ? 'border-primary' : ''}`}>

                  {isRecommended && (
                    <span className="absolute top-3 right-3 inline-flex items-center px-2 py-0.5 rounded-full text-[10px] font-medium bg-primary/15 text-primary border border-primary/30">
                      {highlightTag}
                    </span>
                  )}

                  <Icon size={28} className="text-primary mb-3" style={pkg.badge_color ? { color: pkg.badge_color } : {}} />
                  <h3 className="text-lg font-bold mb-1">{shownName}</h3>
                  <div className="flex items-baseline gap-2 mb-1 flex-wrap">
                    <span className="text-2xl font-bold">{originalPriceText}</span>
                    <span className="text-xs text-on-surface-variant">/ {formatDuration(pkg.billing_period_seconds)}</span>
                  </div>

                  <div className="flex flex-wrap gap-1 mb-3 text-[10px]">
                    <span className="px-2 py-0.5 rounded-control bg-success/20 text-success">{t('UPGRADE.INSTANT', '⚡ 即时开通')}</span>
                    {Boolean(pkg.stackable) && <span className="px-2 py-0.5 rounded-control bg-primary/20 text-primary">{t('UPGRADE.STACKABLE', '可叠加')}</span>}
                  </div>

                  {shownDescription && (
                    <p className="text-xs text-on-surface-variant mb-3 line-clamp-2">{shownDescription}</p>
                  )}

                  {pkg.plans && pkg.plans.length > 0 && (
                    <ul className="space-y-1 mb-3">
                      {pkg.plans.map(p => (
                        <li key={p.id} className="text-xs text-on-surface-variant flex items-start gap-1.5">
                          <Check size={12} className="text-success shrink-0 mt-0.5" aria-hidden="true" />
                          <span className="min-w-0 break-words leading-relaxed">
                            {String(p.plan?.display_name || p.plan?.name || '')
                              .replaceAll('GPT', 'Codex')}
                            {formatPlanLimit(p.plan, t) && (
                              <span className="text-outline"> · {formatPlanLimit(p.plan, t)}</span>
                            )}
                          </span>
                        </li>
                      ))}
                    </ul>
                  )}


                  <button type="button" onClick={() => openPurchaseDialog(pkg)}
                    disabled={purchasing === pkg.id}
                    className="w-full h-10 bg-primary text-on-primary rounded-control font-semibold flex items-center justify-center gap-2 hover:brightness-110 active:scale-[0.98] disabled:opacity-50 transition">
                    <ShoppingCart size={14} aria-hidden="true" />
                    {purchasing === pkg.id ? t('UPGRADE.PROCESSING', '处理中...') : t('UPGRADE.BUY_NOW', '立即购买')}
                  </button>
                </div>
              );
            })}
          </div>
          </>
          );
        })()}
      </>)}
      </StorePage>
      <Modal
        open={!!purchasePkg}
        onClose={() => {
          if (!purchasing) setPurchaseDraft(null);
        }}
        title={t('UPGRADE.PURCHASE_TITLE', '确认购买')}
        description={purchasePkg ? displayPackageName(purchasePkg) : ''}
        size="sm"
        closeOnBackdrop={!purchasing}
        footer={(
          <>
            <button
              type="button"
              onClick={() => setPurchaseDraft(null)}
              disabled={!!purchasing}
              className="btn btn-secondary"
            >
              {t('CONFIRM.CANCEL', '取消')}
            </button>
            <button
              type="button"
              onClick={purchase}
              disabled={!!purchasing}
              className="btn disabled:opacity-50 disabled:cursor-not-allowed"
            >
              {purchasing ? t('UPGRADE.PROCESSING', '处理中...') : t('UPGRADE.PURCHASE_CONFIRM', '确认购买')}
            </button>
          </>
        )}
      >
        {purchasePkg && (
          <div className="space-y-5">
            <div className="space-y-2 text-sm">
              <div className="flex items-center justify-between gap-4">
                <span className="text-on-surface-variant">{t('UPGRADE.PRICE_ORIGINAL', '套餐原价')}</span>
                <span className="font-semibold text-on-surface">{formatCurrency(purchaseOriginalPrice, 2)}</span>
              </div>
              {purchaseSaving > 0 && (
                <div className="flex items-center justify-between gap-4">
                  <span className="text-on-surface-variant">{t('UPGRADE.PRICE_DISCOUNT', '优惠减免')}</span>
                  <span className="font-semibold text-success">-{formatCurrency(purchaseSaving, 2)}</span>
                </div>
              )}
              <div className="flex items-center justify-between gap-4 border-t border-outline-variant pt-3">
                <span className="text-on-surface font-semibold">{t('UPGRADE.PRICE_FINAL', '实际扣款')}</span>
                <span className="text-xl font-bold text-on-surface">{formatCurrency(Number(purchaseFinalPrice || 0), 2)}</span>
              </div>
            </div>

            <div className="space-y-2">
              <label htmlFor="purchase-coupon" className="block text-xs font-semibold text-on-surface-variant">
                {t('UPGRADE.USE_COUPON', '使用优惠券')}
              </label>
              {purchaseUsableCoupons.length > 0 ? (
                <Select
                  id="purchase-coupon"
                  value={purchaseCouponId}
                  onChange={(e) => setPurchaseDraft((prev) => prev ? { ...prev, couponId: parseInt(e.target.value, 10) || 0 } : prev)}
                  options={[
                    { value: 0, label: t('UPGRADE.COUPON_NONE', '不使用券') },
                    ...purchaseUsableCoupons.map((c) => ({
                      value: c.id,
                      label: couponLabelFor(purchasePkg, c),
                    })),
                  ]}
                />
              ) : (
                <div className="rounded-control border border-outline-variant bg-surface-container px-3 py-2 text-sm text-on-surface-variant">
                  {t('UPGRADE.COUPON_NONE_FOR_PACKAGE', '暂无可用于此套餐的优惠券')}
                </div>
              )}
              <p className="text-xs text-on-surface-variant">
                {t('UPGRADE.COUPON_SINGLE_USE_NOTE', '每笔订单最多使用 1 张优惠券，不支持叠加使用。')}
              </p>
              {purchaseSelectedCoupon && purchaseSaving > 0 && (
                <p className="text-xs text-success">
                  {t('UPGRADE.COUPON_SELECTED_SAVING', '已选择「{{name}}」，本次节省 {{saving}}。', {
                    name: purchaseSelectedCoupon.snapshot_name,
                    saving: formatCurrency(purchaseSaving, 2),
                  })}
                </p>
              )}
            </div>

            <p className="text-xs text-on-surface-variant">
              {t('UPGRADE.BALANCE_DEDUCT_NOTE', '购买成功后会从你的余额扣除实际扣款金额，并立即开通套餐。')}
            </p>
          </div>
        )}
      </Modal>
    </div>
  );
};

export default UpgradePage;
