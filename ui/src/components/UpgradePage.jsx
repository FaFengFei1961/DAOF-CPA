import React, { useState, useEffect, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { ShoppingCart, Check, Layers, Sparkles, Cpu, Zap, Activity, Package as PackageIcon, BrainCircuit, Bot } from 'lucide-react';
import toast from 'react-hot-toast';
import { useConfirm } from '../context/ConfirmContext';
import { authFetch, isLoggedIn, readAuthState } from '../utils/authFetch';
import { clearPageCache, isPageCacheFresh, readPageCache, writePageCache } from '../utils/pageCache';
import { useCurrency } from '../context/CurrencyContext';
import { formatDuration } from './DurationInput';
import { StorePage, StoreHero } from './store/StorePrimitives';
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
const STORE_GROUPS = [
  { id: 'claude', label: 'Claude', order: 10 },
  { id: 'codex', label: 'Codex', order: 20 },
  { id: 'gemini', label: 'Gemini', order: 30 },
  { id: 'combo', label: 'Combo', order: 40 },
  { id: 'other', label: 'Other', order: 1000 },
];
const STORE_GROUP_BY_ID = Object.fromEntries(STORE_GROUPS.map((g) => [g.id, g]));
const getCouponCacheKey = () => {
  const { isAdmin, userToken } = readAuthState();
  return `upgrade:coupons:${isAdmin ? 'admin' : userToken || 'guest'}`;
};

const readPackageExtraConfig = (pkg) => {
  try {
    const parsed = JSON.parse(pkg.extra_config || '{}');
    return parsed && typeof parsed === 'object' ? parsed : {};
  } catch {
    return {};
  }
};

const inferPackageGroupId = (pkg) => {
  const cfg = readPackageExtraConfig(pkg);
  const provider = String(cfg.provider || pkg.icon_key || '').trim().toLowerCase();
  if (provider === 'combo' || provider === 'trinity') return 'combo';
  if (provider === 'codex' || provider === 'openai') return 'codex';
  if (provider === 'google' || provider === 'gemini') return 'gemini';
  if (provider === 'anthropic' || provider === 'claude') return 'claude';

  const raw = [
    pkg.name,
    pkg.description,
  ].filter(Boolean).join(' ').toLowerCase();

  if (raw.includes('combo') || raw.includes('trinity') || raw.includes('御三家')) return 'combo';
  if (raw.includes('anthropic') || raw.includes('claude')) return 'claude';
  if (raw.includes('codex') || raw.includes('openai') || /\bgpt\b/.test(raw)) return 'codex';
  if (raw.includes('google') || raw.includes('gemini')) return 'gemini';
  return 'other';
};

const displayPackageName = (pkg) => String(pkg.name || '')
  .replace(/^GPT\b/, 'Codex')
  .replace(/^御三家\b/, 'Combo');

const displayPackageDescription = (pkg) => String(pkg.description || '')
  .replaceAll('OpenAI / GPT', 'Codex / OpenAI')
  .replaceAll('Claude + GPT + Gemini', 'Claude + Codex + Gemini')
  .replaceAll('御三家', 'Combo');

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

const groupStorePackages = (packages) => {
  const grouped = new Map();
  for (const pkg of packages) {
    const groupId = inferPackageGroupId(pkg);
    const group = STORE_GROUP_BY_ID[groupId] || STORE_GROUP_BY_ID.other;
    if (!grouped.has(group.id)) grouped.set(group.id, { ...group, items: [] });
    grouped.get(group.id).items.push(pkg);
  }
  return Array.from(grouped.values())
    .map((group) => ({
      ...group,
      items: [...group.items].sort((a, b) => (a.sort_order || 0) - (b.sort_order || 0) || String(a.name || '').localeCompare(String(b.name || ''))),
    }))
    .sort((a, b) => a.order - b.order);
};

const UpgradePage = ({ onPurchaseSuccess, isAuthenticated = true, onSignIn }) => {
  // 注：套餐列表 /api/packages 完全公开，未登录也能看价格；
  // 仅"购买"动作需要登录（已在 purchase() 内 isLoggedIn() 校验）。
  const { t } = useTranslation();
  const confirm = useConfirm();
  const { formatCurrency } = useCurrency();
  const couponCacheKey = React.useMemo(getCouponCacheKey, [isAuthenticated]);
  const [pkgs, setPkgs] = useState(() => readPageCache(PACKAGE_CACHE_KEY) || []);
  const [coupons, setCoupons] = useState(() => (isAuthenticated ? readPageCache(couponCacheKey) : null) || []); // 用户可用券（仅登录用户）
  const [loading, setLoading] = useState(() => !readPageCache(PACKAGE_CACHE_KEY));
  const [purchasing, setPurchasing] = useState(null);
  // 选中的券：key=packageId, value=couponId（用户在卡片上为每个 package 单独选）
  const [selectedCouponByPkg, setSelectedCouponByPkg] = useState({});
  // fix Critical Codex UX 审查（第二十五轮 #2）：从 hash query 读 ?pane=mine|store。
  // 通知系统的"查看订阅"链接走 /upgrade?pane=mine 深链跳转，需自动切到我的 tab。
  const [pane, setPane] = useState(() => {
    const rawHash = window.location.hash.replace('#', '');
    const query = rawHash.split('?')[1] || '';
    const paneFromUrl = new URLSearchParams(query).get('pane');
    if (paneFromUrl === 'mine' || paneFromUrl === 'store') return paneFromUrl;
    return isLoggedIn() ? 'mine' : 'store';
  });
  const [typeFilter, setTypeFilter] = useState('all');

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
        setPane('mine');
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
    <div className="w-full max-w-[1680px] mx-auto px-4 md:px-8 2xl:px-10 py-8">
      <StorePage>
        <StoreHero
          icon={Sparkles}
          hue="#a855f7"
          badge={pane === 'mine' ? t('PRODUCTS.BADGE_MINE', '我的产品') : t('PRODUCTS.BADGE_STORE', '商店')}
          title={t('PRODUCTS.TITLE', '产品中心')}
          subtitle={pane === 'mine'
            ? t('MY_PRODUCTS.SUBTITLE', '订阅最先消耗；订阅用尽后扣增量包；都用完才走余额扣费（在账号设置中开启）。')
            : t('PRODUCTS.SUBTITLE', '订阅周期套餐为主消费来源；增量包用于订阅用尽后的临时补充；余额扣费在账号设置中开启。')}
        />

      {/* 一级 tab：我的 / 商店（segmented control） */}
      <div className="inline-flex rounded-overlay border border-outline-variant bg-surface-container p-0.5 self-start">
        {[
          { id: 'mine', label: t('PRODUCTS.PANE_MINE', '我的') },
          { id: 'store', label: t('PRODUCTS.PANE_STORE', '商店') },
        ].map(p => {
          const active = pane === p.id;
          return (
            <button
              key={p.id}
              type="button"
              onClick={() => setPane(p.id)}
              className={`px-6 py-2 rounded-control text-sm font-semibold transition ${active
                ? 'bg-primary text-on-primary'
                : 'text-on-surface-variant hover:text-on-surface'}`}
            >
              {p.label}
            </button>
          );
        })}
      </div>

      {/* "我的"分支：直接渲染 MySubscriptions 内容（无自身 hero） */}
      {pane === 'mine' && <MySubscriptions isAuthenticated={isLoggedIn()} embedded />}

      {/* "商店"分支：保留原有的 类型 tab + 卡片网格 */}
      {pane === 'store' && (<>
      <div className="flex gap-2">
        {[
          { id: 'all', label: t('PRODUCTS.TAB_ALL', '全部') },
          { id: 'subscription', label: t('PRODUCTS.TAB_SUBSCRIPTION', '订阅'), hint: t('PRODUCTS.SUBSCRIPTION_HINT', '周期套餐，每月刷新（先扣）') },
          { id: 'addon', label: t('PRODUCTS.TAB_ADDON', '增量包'), hint: t('PRODUCTS.ADDON_HINT', '订阅用完后扣，临时补充') },
        ].map(tab => {
          const active = typeFilter === tab.id;
          return (
            <button
              key={tab.id}
              type="button"
              onClick={() => setTypeFilter(tab.id)}
              className={`flex-1 sm:flex-initial px-5 py-2.5 rounded-lg text-sm font-semibold border transition ${active
                ? 'bg-primary text-on-primary border-primary'
                : 'bg-surface-container text-on-surface-variant border-outline-variant hover:border-primary'}`}
              title={tab.hint}
            >
              {tab.label}
            </button>
          );
        })}
      </div>

      {loading ? <div className="text-center py-20 text-on-surface-variant">{t('UPGRADE.LOADING', '加载中...')}</div>
        : (() => {
          const filtered = typeFilter === 'all' ? pkgs : pkgs.filter(p => (p.product_type || 'subscription') === typeFilter);
          if (filtered.length === 0) {
            return (
              <div className="fl-card p-16 text-center">
                <p className="text-on-surface-variant">{t('PRODUCTS.EMPTY', '此分类暂无可购买的产品')}</p>
              </div>
            );
          }
          const groups = groupStorePackages(filtered);
          return (
          <div className="space-y-8">
            {groups.map(group => (
              <section key={group.id} aria-labelledby={`store-group-${group.id}`} className="space-y-3">
                <div className="flex items-center justify-between gap-4">
                  <h2 id={`store-group-${group.id}`} className="text-lg font-bold text-on-surface">{group.label}</h2>
                  <span className="text-xs text-on-surface-variant">{group.items.length} 个套餐</span>
                </div>
                <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-3">
            {group.items.map(pkg => {
              const Icon = pickIcon(pkg.icon_key);
              const usableCoupons = usableCouponsForPkg(pkg.id);
              const selectedCouponId = selectedCouponByPkg[pkg.id] || 0;
              const finalPrice = effectivePriceFor(pkg, selectedCouponId);
              const hasDiscount = Boolean(selectedCouponId) && finalPrice < pkg.price_amount;
              const shownName = displayPackageName(pkg);
              const shownDescription = displayPackageDescription(pkg);
              const finalPriceText = formatCurrency(Number(finalPrice || 0), 2);
              const originalPriceText = formatCurrency(Number(pkg.price_amount || 0), 2);
              return (
                <div key={pkg.id}
                  className="relative fl-card p-6"
                  style={pkg.gradient ? { background: pkg.gradient } : {}}>
                  {pkg.highlight_tag && (
                    <div className="absolute -top-3 left-6 px-3 py-1 bg-primary text-on-primary text-xs font-bold rounded-full">
                      {pkg.highlight_tag}
                    </div>
                  )}
                  <Icon size={28} className="text-primary mb-3" style={pkg.badge_color ? { color: pkg.badge_color } : {}} />
                  <h3 className="text-lg font-bold mb-1">{shownName}</h3>
                  <div className="flex items-baseline gap-2 mb-1 flex-wrap">
                    {/* fix MAJOR R23+2-F3 / F6（gemini 二轮）：sr-only 必须包含具体金额，
                        否则屏幕阅读器只读"折扣价 / 原价"，听不到数字。
                        fix MINOR Phase-3-review（gemini 第十七轮）：所有金额统一走全局法币格式化 */}
                    {hasDiscount && (
                      <>
                        <span className="sr-only">{t('UPGRADE.SR_DISCOUNT_PRICE', '折扣价')} {finalPriceText}</span>
                        <span aria-hidden="true" className="text-2xl font-bold text-emerald-400">{finalPriceText}</span>
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
                    <span className="px-2 py-0.5 rounded bg-emerald-500/20 text-emerald-400">{t('UPGRADE.INSTANT', '⚡ 即时开通')}</span>
                    {Boolean(pkg.stackable) && <span className="px-2 py-0.5 rounded bg-purple-500/20 text-purple-400">{t('UPGRADE.STACKABLE', '可叠加')}</span>}
                    {hasDiscount && (
                      <span className="px-2 py-0.5 rounded bg-fuchsia-500/20 text-fuchsia-400 font-bold">
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
                          <Check size={12} className="text-emerald-400 shrink-0 mt-0.5" aria-hidden="true" />
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
                      <select
                        id={`coupon-${pkg.id}`}
                        value={selectedCouponId}
                        onChange={(e) => setSelectedCouponByPkg((prev) => ({ ...prev, [pkg.id]: parseInt(e.target.value, 10) || 0 }))}
                        className="w-full bg-surface-container-high border border-outline rounded-lg px-2 py-1 text-xs text-on-surface focus:border-primary focus:ring-1 focus:ring-primary"
                      >
                        <option value={0}>{t('UPGRADE.COUPON_NONE', '不使用券')}</option>
                        {usableCoupons.map((c) => (
                          <option key={c.id} value={c.id}>
                            {c.snapshot_name}
                            {c.snapshot_type === 'fixed_price' ? ` (${formatCurrency(Number(c.snapshot_value || 0), 2)})` : ''}
                          </option>
                        ))}
                      </select>
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
              </section>
            ))}
          </div>
          );
        })()}
      </>)}
      </StorePage>
    </div>
  );
};

export default UpgradePage;
