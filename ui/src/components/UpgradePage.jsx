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
import MySubscriptions from './MySubscriptions';

// 用户购买套餐入口页。展示元数据（图标 / 颜色 / 渐变 / 标签）来自 Package 表，admin 自由配置。

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

// Phase 8：产品线收敛到只剩 Combo。
// 之前的 STORE_GROUPS（claude / codex / gemini / combo / other）+
// inferPackageGroupId（基于 extra_config.provider / pkg.icon_key / 名字字符串
// 推断分组）+ groupStorePackages（按 store_group section 分组渲染）全删。
// 现在套餐 flat grid 渲染，按 sort_order + name 排序。
//
// 注意：后端 Package model 没有 store_group 字段，分组逻辑本来就是前端推断。
// 所以这次 admin 想"全平台只剩 Combo" → 去 admin 套餐管理把 Claude / Codex /
// Gemini 三个独立套餐删掉即可（数据清理，不需要 schema 改动）。

const displayPackageName = (pkg) => String(pkg.name || '');
const displayPackageDescription = (pkg) => String(pkg.description || '');

const formatPlanLimit = (plan) => {
  const value = Number(plan?.limit_value || 0);
  if (value <= 0) return '';
  const unit = String(plan?.limit_label || plan?.limit_unit || '').trim();
  const displayValue = Number.isInteger(value) ? String(value) : value.toFixed(2).replace(/0+$/, '').replace(/\.$/, '');
  if (!unit) return displayValue;
  if (unit === '次调用') return `${displayValue} 次调用`;
  if (unit === 'Tokens') return `${displayValue} Tokens`;
  return `${displayValue} ${unit}`;
};

// Phase 8：扁平排序，不再分组（产品线只剩 Combo 一种，无分组必要）
const sortStorePackages = (packages) =>
  [...packages].sort((a, b) =>
    (a.sort_order || 0) - (b.sort_order || 0) ||
    String(a.name || '').localeCompare(String(b.name || ''))
  );

const UpgradePage = ({ onPurchaseSuccess }) => {
  // 注：套餐列表 /api/packages 完全公开，未登录也能看价格；
  // 仅"购买"动作需要登录（已在 purchase() 内 isLoggedIn() 校验）。
  // Phase 0：从 useAuth 自取 isAuthenticated + openLogin（替代 prop 注入）
  const { t } = useTranslation();
  const confirm = useConfirm();
  const { formatCurrency } = useCurrency();
  const { isAuthenticated, openLogin } = useAuth();
  const onSignIn = openLogin;
  const couponCacheKey = React.useMemo(getCouponCacheKey, [isAuthenticated]);
  const [pkgs, setPkgs] = useState(() => readPageCache(PACKAGE_CACHE_KEY) || []);
  const [coupons, setCoupons] = useState(() => (isAuthenticated ? readPageCache(couponCacheKey) : null) || []); // 用户可用券（仅登录用户）
  const [loading, setLoading] = useState(() => !readPageCache(PACKAGE_CACHE_KEY));
  const [purchasing, setPurchasing] = useState(null);
  // 选中的券：key=packageId, value=couponId（用户在卡片上为每个 package 单独选）
  const [selectedCouponByPkg, setSelectedCouponByPkg] = useState({});
  // fix CRITICAL（Phase 5 codex 审查）：旧 hash 路由切到 React Router BrowserRouter 后，
  // hashRedirect 把 #upgrade?pane=mine 改写成 /upgrade?pane=mine。原从 location.hash 读
  // pane 在新架构永远拿不到值，导致通知"查看订阅"深链失效。改用 React Router 的
  // useSearchParams 从 location.search 读，URL 同步通过 setSearchParams 写。
  const [searchParams, setSearchParams] = useSearchParams();
  const paneFromUrl = searchParams.get('pane');
  const [pane, setPane] = useState(() => {
    if (paneFromUrl === 'mine' || paneFromUrl === 'store') return paneFromUrl;
    return isLoggedIn() ? 'mine' : 'store';
  });
  // URL 主导：URL 变化时同步 pane（前进/后退、外部带 ?pane= 进站）
  useEffect(() => {
    if (paneFromUrl === 'mine' || paneFromUrl === 'store') setPane(paneFromUrl);
  }, [paneFromUrl]);
  const setPaneAndUrl = useCallback((next) => {
    setPane(next);
    const params = new URLSearchParams(searchParams);
    params.set('pane', next);
    setSearchParams(params, { replace: true });
  }, [searchParams, setSearchParams]);
  // Phase 8：addon 已移除，所有套餐都是 subscription，typeFilter 整段去除

  // fix CRITICAL R23+2-F2（gemini 全方面审查）：用 authFetch 而不是原生 fetch，
  // 否则后端 getCurrentUserOptional 拿不到 token，老用户被识别为未登录新客。
  // fix MAJOR R23+2-F4（gemini 第三轮）：用 Promise.all 并发拉两个端点（之前 await 串行 → waterfall）
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
        // 仅 effective_status=available 的券能用
        const nextCoupons = (results[1].data || []).filter((c) => c.effective_status === 'available');
        writePageCache(couponCacheKey, nextCoupons);
        setCoupons(nextCoupons);
      } else {
        setCoupons([]);
      }
    } catch { toast.error(t('UPGRADE.LOAD_FAIL', '加载失败')); }
    finally { setLoading(false); }
  }, [t, isAuthenticated, couponCacheKey]);

  // fix MAJOR R23+2-F5：依赖 isAuthenticated，登录态切换时重新拉数据（含可用券）
  useEffect(() => { load(); }, [load]);

  // 给某 package 找可用的券（适用范围内 + status=available）
  const usableCouponsForPkg = (pkgId) => coupons.filter((c) => {
    let allowed = [];
    try {
      const arr = JSON.parse(c.snapshot_package_ids || '[]');
      if (Array.isArray(arr)) allowed = arr;
    } catch { /* ignore */ }
    return allowed.length === 0 || allowed.includes(pkgId);
  });

  // 给定 pkg 和已选 couponId 返回最终单价（前端预览，最终扣费以后端为准）
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
      // 未登录 → 弹登录而不是 toast 错误（更好的引导）
      if (onSignIn) onSignIn();
      else toast.error(t('UPGRADE.LOGIN_REQUIRED', '请先登录后再购买'));
      return;
    }
    const couponId = selectedCouponByPkg[pkg.id] || 0;
    const finalPrice = effectivePriceFor(pkg, couponId);
    // fix MAJOR R23+2-F3：confirm 弹窗显示**最终扣款金额**（含券折扣后），而不是原价
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
        // fix Major Codex UX 审查（第二十五轮）：原注释承诺"切到 mine"实际只 load/callback，没切。
        // Phase 5：用 setPaneAndUrl 同步 URL 让"购买成功 → 切到我的"也写到 URL（可分享/书签）
        setPaneAndUrl('mine');
        clearPageCache('subscriptions:');
        clearPageCache('billing:');
        clearPageCache('user-coupons:');
        window.dispatchEvent(new CustomEvent('user-profile-refresh'));
        // 重新拉券（已用券会从可用列表消失）
        load({ force: true });
        if (onPurchaseSuccess) onPurchaseSuccess(json);
      } else {
        toast.error(json.message || t('UPGRADE.PURCHASE_FAIL', '购买失败'));
      }
    } catch { toast.error(t('API.ERR_NETWORK', '网络异常')); }
    finally { setPurchasing(null); }
  };

  return (
    /* Phase 8：去 max-w-[1680px] 内层 wrapper（让外层 UserShell main 容器统一
       控制宽度），去 StoreHero 紫色营销头（"产品中心"标题 + badge + 长副标），
       页面直接以 segmented control + 套餐 grid 开始 */
    <div className="w-full">
      <StorePage>

      {/* Phase 8：未登录态隐藏"我的/商店"切换（强制 store 分支显示套餐），
          已登录才出现 segmented control 让用户在"我的订阅"和"套餐商店"之间切 */}
      {isAuthenticated && (
        <div className="inline-flex rounded-overlay border border-outline-variant bg-surface-container p-0.5 self-start">
          {[
            { id: 'mine', label: t('SUB.PANE_MINE', '我的') },
            { id: 'store', label: t('SUB.PANE_STORE', '商店') },
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

      {/* "我的"分支：仅已登录可见 */}
      {isAuthenticated && pane === 'mine' && <MySubscriptions isAuthenticated={isLoggedIn()} embedded />}

      {/* "商店"分支：套餐 grid（未登录或已登录选 store 都展示） */}
      {(!isAuthenticated || pane === 'store') && (<>
      {loading ? <div className="text-center py-20 text-on-surface-variant">{t('UPGRADE.LOADING', '加载中...')}</div>
        : (() => {
          const filtered = pkgs;
          if (filtered.length === 0) {
            return (
              <div className="fl-card p-16 text-center">
                <p className="text-on-surface-variant">{t('SUB.STORE_EMPTY', '此分类暂无可购买的套餐')}</p>
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
                  {/* Phase 8：去 ring-2 + ring-offset + （"超市价签"营销
                      视觉），改成 border-primary 高亮（推荐套餐边框由灰变主色）+
                      标题旁中性 chip 标识，避免 admin 自定义 pkg.gradient 覆盖背景 */}
                  {isRecommended && (
                    <span className="absolute top-3 right-3 inline-flex items-center px-2 py-0.5 rounded-full text-[10px] font-medium bg-primary/15 text-primary border border-primary/30">
                      {pkg.highlight_tag}
                    </span>
                  )}
                  {/* Phase 8：去 brand-chip 产品线标识（只剩 Combo 一种产品没必要标）*/}
                  <Icon size={28} className="text-primary mb-3" style={pkg.badge_color ? { color: pkg.badge_color } : {}} />
                  <h3 className="text-lg font-bold mb-1">{shownName}</h3>
                  <div className="flex items-baseline gap-2 mb-1 flex-wrap">
                    {/* fix MAJOR R23+2-F3 / F6（gemini 二轮）：sr-only 必须包含具体金额，
                        否则屏幕阅读器只读"折扣价 / 原价"，听不到数字。
                        fix MINOR Phase-3-review（gemini 第十七轮）：所有金额统一走全局法币格式化 */}
                    {hasDiscount && (
                      <>
                        <span className="sr-only">{t('UPGRADE.SR_DISCOUNT_PRICE', '折扣价')} {finalPriceText}</span>
                        <span aria-hidden="true" className="text-2xl font-bold text-success">{finalPriceText}</span>
                        <span className="sr-only">，{t('UPGRADE.SR_ORIGINAL_PRICE', '原价')} {originalPriceText}</span>
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
                              .replaceAll('御三家', 'Combo')}
                            {formatPlanLimit(p.plan) && (
                              <span className="text-outline"> · {formatPlanLimit(p.plan)}</span>
                            )}
                          </span>
                        </li>
                      ))}
                    </ul>
                  )}

                  {/* 优惠券选择器（仅登录用户 + 至少 1 张可用） */}
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
