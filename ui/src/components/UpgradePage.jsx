import React, { useState, useEffect, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { useSearchParams } from 'react-router-dom';
import { ShoppingCart, Check, Layers, Sparkles, Cpu, Zap, Activity, Package as PackageIcon, BrainCircuit, Bot } from 'lucide-react';
import toast from 'react-hot-toast';
import { useConfirm } from '../context/ConfirmContext';
import { authFetch, isLoggedIn, readAuthState } from '../utils/authFetch';
import { clearPageCache, isPageCacheFresh, readPageCache, writePageCache } from '../utils/pageCache';
import { useAuth } from '../context/AuthContext';
import { useCurrency } from '../context/CurrencyContext';
import { formatDuration } from './DurationInput';
import { StorePage } from './store/StorePrimitives';
import Select from './ui/Select';
import StatusBadge from './ui/StatusBadge';
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

const PLAN_LIMIT_CALLS_UNIT = '\u6b21\u8c03\u7528';
const LEGACY_TRINITY_NAME = '\u5fa1\u4e09\u5bb6';

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

const UpgradePage = ({ onPurchaseSuccess }) => {



  const { t } = useTranslation();
  const confirm = useConfirm();
  const { formatCurrency } = useCurrency();
  const { isAuthenticated, openLogin } = useAuth();
  const onSignIn = openLogin;
  const couponCacheKey = React.useMemo(getCouponCacheKey, [isAuthenticated]);
  const [pkgs, setPkgs] = useState(() => readPageCache(PACKAGE_CACHE_KEY) || []);
  const [coupons, setCoupons] = useState(() => (isAuthenticated ? readPageCache(couponCacheKey) : null) || []);
  const [loading, setLoading] = useState(() => !readPageCache(PACKAGE_CACHE_KEY));
  const [purchasing, setPurchasing] = useState(null);

  const [selectedCouponByPkg, setSelectedCouponByPkg] = useState({});




  const [searchParams, setSearchParams] = useSearchParams();
  const paneFromUrl = searchParams.get('pane');
  const [pane, setPane] = useState(() => {
    if (paneFromUrl === 'mine' || paneFromUrl === 'store') return paneFromUrl;
    return isLoggedIn() ? 'mine' : 'store';
  });

  useEffect(() => {
    if (paneFromUrl === 'mine' || paneFromUrl === 'store') setPane(paneFromUrl);
  }, [paneFromUrl]);
  const setPaneAndUrl = useCallback((next) => {
    setPane(next);
    const params = new URLSearchParams(searchParams);
    params.set('pane', next);
    setSearchParams(params, { replace: true });
  }, [searchParams, setSearchParams]);





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
    return allowed.length === 0 || allowed.includes(pkgId);
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

  const purchase = async (pkg) => {
    if (!isLoggedIn()) {

      if (onSignIn) onSignIn();
      else toast.error(t('UPGRADE.LOGIN_REQUIRED', '请先登录后再购买'));
      return;
    }
    const couponId = selectedCouponByPkg[pkg.id] || 0;
    const finalPrice = effectivePriceFor(pkg, couponId);

    const confirmMsg = t('UPGRADE.CONFIRM_PURCHASE', {
      name: displayPackageName(pkg),
      price: formatCurrency(Number(finalPrice || 0), 2),
      defaultValue: '购买「{{name}}」？\n\n实际扣款：{{price}}（从你的余额扣除）',
    });
    if (!(await confirm(confirmMsg))) return;

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

        load({ force: true });
        if (onPurchaseSuccess) onPurchaseSuccess(json);
      } else {
        toast.error(json.message || t('UPGRADE.PURCHASE_FAIL', '购买失败'));
      }
    } catch { toast.error(t('API.ERR_NETWORK', '网络异常')); }
    finally { setPurchasing(null); }
  };

  return (

    <div className="w-full">
      <StorePage>


      {isAuthenticated && (
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


      {isAuthenticated && pane === 'mine' && <MySubscriptions isAuthenticated={isLoggedIn()} embedded />}


      {(!isAuthenticated || pane === 'store') && (<>
      {loading ? <div className="text-center py-20 text-on-surface-variant">{t('UPGRADE.LOADING', '加载中...')}</div>
        : (() => {
          const filtered = pkgs;
          if (filtered.length === 0) {
            return (
              <div className="fl-card p-16 text-center">
                <p className="text-on-surface-variant">{t('UPGRADE.STORE_EMPTY', '此分类暂无可购买的套餐')}</p>
              </div>
            );
          }
          const sorted = sortStorePackages(filtered);
          return (
          <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-3">
            {sorted.map(pkg => {
              const Icon = pickIcon(pkg.icon_key);
              const usableCoupons = usableCouponsForPkg(pkg.id);
              const selectedCouponId = selectedCouponByPkg[pkg.id] || 0;
              const finalPrice = effectivePriceFor(pkg, selectedCouponId);
              const hasDiscount = Boolean(selectedCouponId) && finalPrice < pkg.price_amount;
              const shownName = displayPackageName(pkg);
              const shownDescription = displayPackageDescription(pkg);
              const finalPriceText = formatCurrency(Number(finalPrice || 0), 2);
              const originalPriceText = formatCurrency(Number(pkg.price_amount || 0), 2);
              const isRecommended = !!pkg.highlight_tag;
              return (
                <div key={pkg.id}
                  className={`relative fl-card p-6 ${isRecommended ? 'border-primary' : ''}`}>

                  {isRecommended && (
                    <span className="absolute top-3 right-3 inline-flex items-center px-2 py-0.5 rounded-full text-[10px] font-medium bg-primary/15 text-primary border border-primary/30">
                      {pkg.highlight_tag}
                    </span>
                  )}

                  <Icon size={28} className="text-primary mb-3" style={pkg.badge_color ? { color: pkg.badge_color } : {}} />
                  <h3 className="text-lg font-bold mb-1">{shownName}</h3>
                  <div className="flex items-baseline gap-2 mb-1 flex-wrap">

                    {hasDiscount && (
                      <>
                        <span className="sr-only">{t('UPGRADE.SR_DISCOUNT_PRICE', '折扣价')} {finalPriceText}</span>
                        <span aria-hidden="true" className="text-2xl font-bold text-success">{finalPriceText}</span>
                        <span className="sr-only">{t('UPGRADE.SR_ORIGINAL_PRICE_TEXT', '，原价 {{price}}', { price: originalPriceText })}</span>
                        <span aria-hidden="true" className="text-xs text-outline line-through">{originalPriceText}</span>
                      </>
                    )}
                    {!hasDiscount && (
                      <span className="text-2xl font-bold">{originalPriceText}</span>
                    )}
                    <span className="text-xs text-on-surface-variant">/ {formatDuration(pkg.billing_period_seconds)}</span>
                  </div>

                  <div className="flex flex-wrap gap-1 mb-3 text-[10px]">
                    <span className="px-2 py-0.5 rounded-control bg-success/20 text-success">{t('UPGRADE.INSTANT', '⚡ 即时开通')}</span>
                    {Boolean(pkg.stackable) && <span className="px-2 py-0.5 rounded-control bg-primary/20 text-primary">{t('UPGRADE.STACKABLE', '可叠加')}</span>}
                    {hasDiscount && (
                      <span className="px-2 py-0.5 rounded-control bg-fuchsia-500/20 text-fuchsia-400 font-bold">
                        <span aria-hidden="true">🎟️ </span>{t('UPGRADE.COUPON_APPLIED', '使用券')}
                      </span>
                    )}
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
                              .replaceAll('GPT', 'Codex')
                              .replaceAll(LEGACY_TRINITY_NAME, 'Combo')}
                            {formatPlanLimit(p.plan, t) && (
                              <span className="text-outline"> · {formatPlanLimit(p.plan, t)}</span>
                            )}
                          </span>
                        </li>
                      ))}
                    </ul>
                  )}


                  {isAuthenticated && usableCoupons.length > 0 && (
                    <div className="mb-3">
                      <label htmlFor={`coupon-${pkg.id}`} className="block text-[10px] font-medium text-on-surface-variant mb-1">
                        {t('UPGRADE.USE_COUPON', '使用优惠券')}
                      </label>
                      <Select
                        id={`coupon-${pkg.id}`}
                        value={selectedCouponId}
                        onChange={(e) => setSelectedCouponByPkg((prev) => ({ ...prev, [pkg.id]: parseInt(e.target.value, 10) || 0 }))}
                        options={[
                          { value: 0, label: t('UPGRADE.COUPON_NONE', '不使用券') },
                          ...usableCoupons.map(c => ({
                            value: c.id,
                            label: `${c.snapshot_name}${c.snapshot_type === 'fixed_price' ? ` (${formatCurrency(Number(c.snapshot_value || 0), 2)})` : ''}`
                          }))
                        ]}
                      />
                    </div>
                  )}

                  <button type="button" onClick={() => purchase(pkg)}
                    disabled={purchasing === pkg.id}
                    className="w-full h-10 bg-primary text-on-primary rounded-control font-semibold flex items-center justify-center gap-2 hover:brightness-110 active:scale-[0.98] disabled:opacity-50 transition">
                    <ShoppingCart size={14} aria-hidden="true" />
                    {purchasing === pkg.id ? t('UPGRADE.PROCESSING', '处理中...') : t('UPGRADE.BUY_NOW', '立即购买')}
                  </button>
                </div>
              );
            })}
          </div>
          );
        })()}
      </>)}
      </StorePage>
    </div>
  );
};

export default UpgradePage;
