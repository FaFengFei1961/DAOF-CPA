import React, { useState, useEffect, useCallback, useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { Wallet, RefreshCw, ExternalLink, Banknote, History } from 'lucide-react';
import toast from 'react-hot-toast';
import { QRCodeSVG } from 'qrcode.react';
import { authFetch, readAuthState } from '../utils/authFetch';
import { isPageCacheFresh, readPageCache, writePageCache } from '../utils/pageCache';
import { StorePage, StoreSection } from './store/StorePrimitives';
import PageHeader from './ui/PageHeader';
import Pagination from './common/Pagination';
import { PAGE_SIZE_HISTORY } from './common/constants';

const PAY_METHOD_META = {
  alipay:    { i18n: 'PAY_ALIPAY',    color: 'bg-[#1677ff]', text: 'text-white' },
  wxpay:     { i18n: 'PAY_WXPAY',     color: 'bg-[#07c160]', text: 'text-white' },
  qqpay:     { i18n: 'PAY_QQPAY',     color: 'bg-[#12b7f5]', text: 'text-white' },
  bank:      { i18n: 'PAY_BANK',      color: 'bg-error',   text: 'text-white' },
  jdpay:     { i18n: 'PAY_JDPAY',     color: 'bg-error',   text: 'text-white' },
  paypal:    { i18n: 'PAY_PAYPAL',    color: 'bg-[#003087]', text: 'text-white' },
  douyinpay: { i18n: 'PAY_DOUYINPAY', color: 'bg-black',     text: 'text-white' },
};
const TOPUP_CACHE_TTL_MS = 30000;
const TOPUP_OPTIONS_CACHE_KEY = 'topup:options';
const getTopupHistoryCacheKey = (page) => {
  const { isAdmin, userToken } = readAuthState();
  return `topup:history:${isAdmin ? 'admin' : userToken || 'guest'}:${page}`;
};

const Topup = ({ isAuthenticated }) => {
  const { t } = useTranslation();

  const [opts, setOpts] = useState(() => readPageCache(TOPUP_OPTIONS_CACHE_KEY));
  const [loadingOpts, setLoadingOpts] = useState(() => !readPageCache(TOPUP_OPTIONS_CACHE_KEY));

  const [amount, setAmount] = useState('');
  const [payType, setPayType] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [orderResult, setOrderResult] = useState(null); // {gateway_pay_type, pay_info, ...}

  // fix MAJOR（gemini 第十六轮）：充值历史改为完整分页（原硬编码 page=1&page_size=20，>20 条永远看不到）
  const [historyPage, setHistoryPage] = useState(1);
  const historyCacheKey = useMemo(() => getTopupHistoryCacheKey(historyPage), [historyPage]);
  const initialHistoryCache = readPageCache(getTopupHistoryCacheKey(1));
  const [history, setHistory] = useState(() => initialHistoryCache?.rows || []);
  const [historyLoading, setHistoryLoading] = useState(false);
  const [historyTotal, setHistoryTotal] = useState(() => initialHistoryCache?.total || 0);

  // 注意：依赖空数组——此函数引用稳定，避免 "切换支付方式 → loadOptions 重新触发拉取" 的 race。
  // 使用函数式 setPayType 在拿到方法列表时只在为空时填默认。
  const loadOptions = useCallback(async ({ force = false } = {}) => {
    const cached = readPageCache(TOPUP_OPTIONS_CACHE_KEY);
    if (cached) {
      setOpts(cached);
      const methods = cached.methods || [];
      setPayType(prev => prev || methods[0] || '');
      setLoadingOpts(false);
      if (!force && isPageCacheFresh(TOPUP_OPTIONS_CACHE_KEY, TOPUP_CACHE_TTL_MS)) return;
    } else {
      setLoadingOpts(true);
    }
    try {
      const json = await authFetch('/api/topup/options');
      if (json.success && json.data) {
        writePageCache(TOPUP_OPTIONS_CACHE_KEY, json.data);
        setOpts(json.data);
        const methods = json.data.methods || [];
        setPayType(prev => prev || methods[0] || '');
      }
    } catch {
      // 静默：未配置时下方 banner 会提示
    } finally {
      setLoadingOpts(false);
    }
  }, []);

  const loadHistory = useCallback(async ({ force = false } = {}) => {
    if (!isAuthenticated) return;
    const cached = readPageCache(historyCacheKey);
    if (cached) {
      setHistory(cached.rows || []);
      setHistoryTotal(cached.total || 0);
      setHistoryLoading(false);
      if (!force && isPageCacheFresh(historyCacheKey, TOPUP_CACHE_TTL_MS)) return;
    } else {
      setHistoryLoading(true);
    }
    try {
      const json = await authFetch(`/api/topup/mine?page=${historyPage}&page_size=${PAGE_SIZE_HISTORY}`);
      if (json.success) {
        const next = { rows: json.data || [], total: json.meta?.total || 0 };
        writePageCache(historyCacheKey, next);
        setHistory(next.rows);
        setHistoryTotal(next.total);
      }
    } catch {
      // ignore
    } finally {
      setHistoryLoading(false);
    }
  }, [isAuthenticated, historyCacheKey, historyPage]);

  useEffect(() => { loadOptions(); }, [loadOptions]);
  useEffect(() => { loadHistory(); }, [loadHistory]);

  // 创建订单后启动状态轮询：每 3 秒查一次本订单 status
  // 看到 paid → 弹 toast、关闭支付面板、触发顶栏余额刷新
  // 看到 failed/refunded → 停止轮询
  // 10 分钟超时自动停止
  useEffect(() => {
    const targetOrderNo = orderResult?.out_trade_no;
    if (!targetOrderNo) return;

    let cancelled = false;
    let timerId = null; // fix gemini-Major: 跟踪当前 setTimeout id，cleanup 时能清掉最新一次（之前只清第一次 initial）
    const startedAt = Date.now();

    const tick = async () => {
      if (cancelled) return;
      if (Date.now() - startedAt > 10 * 60 * 1000) return; // 10 分钟超时

      try {
        // 轮询固定查第 1 页（最新订单一定在头部），不影响用户翻页查看的状态
        const json = await authFetch(`/api/topup/mine?page=1&page_size=${PAGE_SIZE_HISTORY}`);
        if (json.success && Array.isArray(json.data)) {
          const latestPageOne = { rows: json.data, total: json.meta?.total || 0 };
          writePageCache(getTopupHistoryCacheKey(1), latestPageOne);
          // 仅当用户当前在第 1 页时才同步覆盖列表，否则只更新 total
          if (historyPage === 1) {
            setHistory(latestPageOne.rows);
            setHistoryTotal(latestPageOne.total);
          }
          const order = json.data.find(o => o.out_trade_no === targetOrderNo);
          if (order) {
            if (order.status === 'paid') {
              toast.success(t('TOPUP.PAID_TOAST', '充值已到账！余额已更新'));
              window.dispatchEvent(new CustomEvent('user-profile-refresh'));
              if (!cancelled) setOrderResult(null);
              return;
            }
            if (order.status === 'failed' || order.status === 'refunded') {
              return;
            }
          }
        }
      } catch { /* 静默 */ }

      if (!cancelled) timerId = setTimeout(tick, 3000);
    };

    timerId = setTimeout(tick, 3000);
    return () => {
      cancelled = true;
      if (timerId) clearTimeout(timerId);
    };
  }, [orderResult?.out_trade_no, t, historyPage]);

  const handleSubmit = async () => {
    const amt = parseFloat(amount);
    if (isNaN(amt) || amt <= 0) {
      toast.error(t('TOPUP.ERR_AMOUNT', '金额不在允许范围内'));
      return;
    }
    if (opts && (amt < opts.min_amount_rmb || amt > opts.max_amount_rmb)) {
      toast.error(t('TOPUP.ERR_AMOUNT', '金额不在允许范围内'));
      return;
    }
    if (!payType) {
      toast.error(t('TOPUP.ERR_PAY_TYPE', '请选择支付方式'));
      return;
    }

    setSubmitting(true);
    setOrderResult(null);
    try {
      const json = await authFetch('/api/topup/create', {
        method: 'POST',
        body: {
          amount_rmb: amt,
          pay_type: payType,
          device: detectDevice(),
        },
      });
      if (json.success && json.data) {
        setOrderResult(json.data);
        // 不能 window.open：在 await 之后调用会被浏览器弹窗拦截器拦掉。
        // 改为下方 <a> 链接展示，用户主动点击才打开（绕过拦截 + 满足"用户手势"要求）。
        if (json.data.pay_info) {
          toast.success(t('TOPUP.GO_PAY_HINT', '订单已创建，请点击下方链接支付'));
        }
        loadHistory({ force: true });
      } else {
        toast.error(json.message || t('TOPUP.ERR_GATEWAY', '支付通道暂时不可用'));
      }
    } catch (e) {
      toast.error(t('TOPUP.ERR_GATEWAY', '支付通道暂时不可用'));
    } finally {
      setSubmitting(false);
    }
  };

  const usdEstimate = (() => {
    const amt = parseFloat(amount);
    if (isNaN(amt) || !opts || opts.exchange_rate <= 0) return null;
    return (amt / opts.exchange_rate).toFixed(2);
  })();

  const gatewayPayType = (orderResult?.gateway_pay_type || '').toLowerCase();
  const payInfo = typeof orderResult?.pay_info === 'string' ? orderResult.pay_info : '';
  const showQRCode = gatewayPayType === 'qrcode' && payInfo;
  const showPaymentLink = payInfo && isSafePaymentTarget(payInfo, gatewayPayType);
  const showRawPayInfo = payInfo && !showQRCode && !showPaymentLink;

  if (loadingOpts) {
    return <div className="text-center py-12 text-on-surface-variant">{t('SYSTEM.LOADING', '加载中...')}</div>;
  }

  if (!opts || !opts.configured) {
    return (
      <div className="max-w-3xl mx-auto py-8">
        <StorePage title={t('TOPUP.TITLE', '余额充值')} icon={Wallet}>
          <div className="fl-card p-10 text-center border-warning/40">
            <Wallet size={48} className="mx-auto mb-4 text-warning" />
            <p className="text-sm text-on-surface-variant">
              {t('TOPUP.UNAVAILABLE', '充值功能尚未配置，请联系管理员')}
            </p>
          </div>
        </StorePage>
      </div>
    );
  }

  return (
    <div className="max-w-3xl mx-auto py-6">
      <StorePage>
        {/* Phase 8：去 StoreHero（"即时到账"营销 badge + "扫码即时到账"营销
            副标），改纯 PageHeader 信息标题 */}
        <PageHeader
          icon={Wallet}
          title={t('TOPUP.TITLE', '余额充值')}
          sub={t('TOPUP.SUB_PLAIN', '人民币按当前汇率换算入账 USD 余额')}
        />

      {/* 充值表单 */}
      <section className="fl-card p-6 space-y-5">
        {/* 金额 */}
        <div className="space-y-3">
          <label htmlFor="topup-amount" className="text-xs font-semibold text-on-surface-variant uppercase tracking-wide">
            {t('TOPUP.AMOUNT_LABEL', '充值金额（人民币）')}
          </label>

          {/* 输入框始终显示，主元素 */}
          <div className="flex items-center gap-2">
            <span className="text-lg text-on-surface-variant font-semibold" aria-hidden="true">¥</span>
            <TextInput
              id="topup-amount"
              type="number"
              step="0.01"
              min={opts.min_amount_rmb}
              max={opts.max_amount_rmb}
              value={amount}
              onChange={e => { setAmount(e.target.value); }}
              placeholder={`${opts.min_amount_rmb} - ${opts.max_amount_rmb}`}
              className="flex-1 font-mono"
            />
          </div>

          {/* 预设档位（如果有）：点击同步到输入框 */}
          {opts.presets_rmb.length > 0 && (
            <div className="flex flex-wrap gap-2">
              {opts.presets_rmb.map(v => {
                const active = parseFloat(amount) === v;
                return (
                  <button
                    key={v}
                    type="button"
                    onClick={() => setAmount(String(v))}
                    className={`px-4 py-2 rounded-control text-sm font-semibold border transition ${
                      active
                        ? 'bg-primary text-on-primary border-primary'
                        : 'bg-surface-container text-on-surface-variant border-outline-variant hover:border-primary hover:text-primary'
                    }`}
                  >
                    {t('TOPUP.PRESET_RMB', { amount: v, defaultValue: '¥{{amount}}' })}
                  </button>
                );
              })}
            </div>
          )}

          <div className="flex items-center justify-between text-xs">
            <span className="text-on-surface-variant">
              {t('TOPUP.RANGE_HINT', {
                min: opts.min_amount_rmb,
                max: opts.max_amount_rmb,
                defaultValue: '可充值范围：¥{{min}} - ¥{{max}}',
              })}
            </span>
            {usdEstimate && (
              <span className="text-primary font-semibold">
                {t('TOPUP.ESTIMATED_USD', {
                  amount: usdEstimate,
                  rate: opts.exchange_rate,
                  defaultValue: '预计入账 {{amount}} USD（汇率 {{rate}}）',
                })}
              </span>
            )}
          </div>
        </div>

        {/* 支付方式 */}
        <div className="space-y-3">
          <span id="topup-pay-method-label" className="text-xs font-semibold text-on-surface-variant uppercase tracking-wide block">
            {t('TOPUP.PAY_METHOD_LABEL', '支付方式')}
          </span>
          {/* fix Minor 第二十轮：去掉 role="radiogroup"（与子按钮 aria-pressed 不再是 radio 语义），
              改用 group + aria-labelledby 让屏幕阅读器正确理解这是一组关联按钮 */}
          <div role="group" aria-labelledby="topup-pay-method-label" className="grid grid-cols-2 sm:grid-cols-3 gap-2">
            {opts.methods.map(m => {
              const meta = PAY_METHOD_META[m] || { i18n: m.toUpperCase(), color: 'bg-surface-container', text: 'text-on-surface' };
              const active = payType === m;
              return (
                <button
                  key={m}
                  type="button"
                  // fix Minor 第二十轮（gemini）：原 role="radio" 需要 radiogroup + 方向键管理才完整。
                  // 改用 aria-pressed 让按钮回归普通切换语义，Tab+Enter/Space 即可操作。
                  aria-pressed={active}
                  onClick={() => setPayType(m)}
                  className={`relative h-12 rounded-control flex items-center justify-center gap-2 border transition font-medium ${
                    active
                      ? `${meta.color} ${meta.text} border-transparent `
                      : 'bg-surface-container text-on-surface border-outline-variant hover:border-primary'
                  }`}
                >
                  <Banknote size={16} aria-hidden="true" />
                  {t(`TOPUP.${meta.i18n}`, meta.i18n)}
                </button>
              );
            })}
          </div>
        </div>

        {/* 提交 */}
        <button
          type="button"
          onClick={handleSubmit}
          disabled={submitting || !amount || !payType}
          className="w-full h-12 bg-primary text-on-primary rounded-control text-base font-semibold hover:opacity-90 disabled:opacity-50 transition"
        >
          {submitting ? t('TOPUP.SUBMITTING', '下单中...') : t('TOPUP.SUBMIT', '立即支付')}
        </button>
      </section>

      {/* 下单结果：二维码 / 跳转链接 */}
      {orderResult && (
        <section className="fl-card p-8 flex flex-col items-center gap-5 border-primary/40 shadow-primary/5">
          <div className="text-center">
            <div className="text-base font-semibold text-on-surface flex items-center justify-center gap-2">
              <span className={`w-2 h-2 rounded-control-full bg-primary animate-pulse`} />
              {t('TOPUP.WAITING_PAYMENT', '等待支付中…')}
            </div>
            <p className="text-xs text-on-surface-variant mt-1">
              {t('TOPUP.QR_HINT', '使用对应客户端扫码完成支付')}
            </p>
          </div>

          {showQRCode && (
            <div className="bg-white p-4 rounded-overlay flex items-center justify-center">
              <QRCodeSVG value={payInfo} size={224} level="M" />
            </div>
          )}

          <div className="text-center space-y-1">
            <div className="text-2xl font-bold text-primary">¥{Number(amount).toFixed(2)}</div>
            {usdEstimate && (
              <div className="text-xs text-on-surface-variant">≈ ${usdEstimate} USD</div>
            )}
          </div>

          {showPaymentLink && (
            <a
              href={payInfo}
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex items-center gap-2 px-6 h-11 bg-primary text-on-primary rounded-control font-semibold hover:opacity-90 transition"
            >
              <ExternalLink size={16} />
              {t('TOPUP.GO_PAY', '前往支付页面')}
            </a>
          )}

          {showRawPayInfo && (
            <div className="w-full rounded-control border border-outline-variant bg-surface-container p-3 text-left">
              <div className="text-xs font-semibold text-on-surface-variant mb-2">
                {t('TOPUP.PAY_INFO_LABEL', '支付参数')}
              </div>
              <pre className="max-h-32 overflow-auto whitespace-pre-wrap break-all text-xs font-mono text-on-surface">
                {payInfo}
              </pre>
            </div>
          )}

          <div className="w-full pt-3 border-t border-outline-variant/40 flex items-center justify-between text-[11px] text-on-surface-variant">
            <span>{t('TOPUP.TABLE_OUT_TRADE_NO', '订单号')}</span>
            <span className="font-mono select-all">{orderResult.out_trade_no}</span>
          </div>
        </section>
      )}

      {/* 历史 */}
      <StoreSection
        title={t('TOPUP.HISTORY_TITLE', '充值记录')}
        right={
          <button
            onClick={() => loadHistory({ force: true })}
            className="w-8 h-8 rounded-control flex items-center justify-center text-on-surface-variant hover:bg-on-surface/[0.04]"
            aria-label={t('SYSTEM.REFRESH', '刷新')}
          >
            <RefreshCw size={14} className={historyLoading ? 'animate-spin' : ''} />
          </button>
        }
      >
      <div className="fl-card p-6">
        {historyTotal === 0 ? (
          <div className="text-center py-8 text-sm text-on-surface-variant">
            {t('TOPUP.EMPTY', '暂无充值记录')}
          </div>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead className="bg-surface-container text-xs uppercase font-mono tracking-wider text-on-surface-variant border-b border-outline-variant">
                <tr>
                  <th className="px-3 py-2 text-left">{t('TOPUP.TABLE_TIME', '时间')}</th>
                  <th className="px-3 py-2 text-right">{t('TOPUP.TABLE_AMOUNT', '金额')}</th>
                  <th className="px-3 py-2 text-left">{t('TOPUP.TABLE_METHOD', '方式')}</th>
                  <th className="px-3 py-2 text-left">{t('TOPUP.TABLE_STATUS', '状态')}</th>
                  <th className="px-3 py-2 text-left">{t('TOPUP.TABLE_OUT_TRADE_NO', '订单号')}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-outline-variant">
                {history.map(o => (
                  <tr key={o.id} className="hover:bg-surface-container">
                    <td className="px-3 py-2 text-xs text-on-surface-variant">
                      {new Date(o.created_at).toLocaleString('zh-CN', { hour12: false })}
                    </td>
                    <td className="px-3 py-2 text-right font-mono">
                      ¥{o.money_rmb.toFixed(2)}
                      <span className="text-xs text-on-surface-variant ml-1">/ ${o.amount_usd.toFixed(2)}</span>
                    </td>
                    <td className="px-3 py-2">
                      {t(`TOPUP.${(PAY_METHOD_META[o.pay_type] || {}).i18n || o.pay_type.toUpperCase()}`, o.pay_type)}
                    </td>
                    <td className="px-3 py-2">
                      <span className={statusClass(o.status)}>
                        {t(`TOPUP.STATUS_${o.status.toUpperCase()}`, o.status)}
                      </span>
                    </td>
                    <td className="px-3 py-2 text-xs font-mono text-on-surface-variant max-w-[180px] truncate" title={o.out_trade_no}>
                      {o.out_trade_no}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        {/* fix MAJOR（gemini 第十六轮）：充值历史分页 */}
        <Pagination
          page={historyPage}
          pageSize={PAGE_SIZE_HISTORY}
          total={historyTotal}
          loading={historyLoading}
          onPageChange={setHistoryPage}
          className="mt-4"
        />
      </div>
      </StoreSection>
      </StorePage>
    </div>
  );
};

const statusClass = (s) => {
  switch (s) {
    case 'paid': return 'text-success text-xs';
    case 'created': return 'text-warning text-xs';
    case 'failed': return 'text-error text-xs';
    case 'refunded': return 'text-on-surface-variant text-xs line-through';
    default: return 'text-on-surface-variant text-xs';
  }
};

// isSafePaymentTarget 防御性校验：网页跳转只允许 http(s)，App scheme 只允许已知支付客户端。
const isSafePaymentTarget = (target, gatewayPayType) => {
  if (typeof target !== 'string') return false;
  try {
    const u = new URL(target);
    if (u.protocol === 'https:' || u.protocol === 'http:') {
      return gatewayPayType === 'jump' || gatewayPayType === 'html' || gatewayPayType === 'urlscheme';
    }
    if (gatewayPayType !== 'urlscheme') return false;
    return ['alipay:', 'alipays:', 'weixin:', 'wechat:', 'weixinpay:', 'mqqapi:', 'qqwallet:', 'douyinpay:', 'snssdk1128:']
      .includes(u.protocol);
  } catch {
    return false;
  }
};

const detectDevice = () => {
  const ua = navigator.userAgent.toLowerCase();
  if (/micromessenger/.test(ua)) return 'wechat';
  if (/alipay/.test(ua)) return 'alipay';
  if (/qq\//.test(ua)) return 'qq';
  if (/aweme|bytedance/.test(ua)) return 'douyin';
  if (/mobile|android|iphone|ipad/.test(ua)) return 'mobile';
  return 'pc';
};

export default Topup;
