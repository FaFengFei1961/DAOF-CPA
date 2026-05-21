import React, { useState, useEffect, useCallback, useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { Wallet, RefreshCw, ExternalLink, Banknote, Coins, Copy, Clock } from 'lucide-react';
import toast from 'react-hot-toast';
import { QRCodeSVG } from 'qrcode.react';
import { authFetch, readAuthState } from '../utils/authFetch';
import { isPageCacheFresh, readPageCache, writePageCache } from '../utils/pageCache';
import { StorePage, StoreSection } from './store/StorePrimitives';
import PageHeader from './ui/PageHeader';
import Pagination from './common/Pagination';
import { PAGE_SIZE_HISTORY } from './common/constants';
import TextInput from './ui/TextInput';

// yifut 支付方式的展示元数据（颜色 / 文字）
const PAY_METHOD_META = {
  alipay:    { color: 'bg-[#1677ff]', text: 'text-white' },
  wxpay:     { color: 'bg-[#07c160]', text: 'text-white' },
  qqpay:     { color: 'bg-[#12b7f5]', text: 'text-white' },
  bank:      { color: 'bg-error',   text: 'text-white' },
  jdpay:     { color: 'bg-error',   text: 'text-white' },
  paypal:    { color: 'bg-[#003087]', text: 'text-white' },
  douyinpay: { color: 'bg-black',     text: 'text-white' },
};

// W-4：epusdt method 展示元数据（链 token icon + 链显示名）
const EPUSDT_METHOD_META = {
  'trc20-usdt':   { color: 'bg-[#26a17b]', text: 'text-white', chain: 'TRC20',   token: 'USDT' },
  'erc20-usdt':   { color: 'bg-[#627eea]', text: 'text-white', chain: 'ERC20',   token: 'USDT' },
  'bep20-usdt':   { color: 'bg-[#f3ba2f]', text: 'text-black', chain: 'BEP20',   token: 'USDT' },
  'polygon-usdt': { color: 'bg-[#8247e5]', text: 'text-white', chain: 'Polygon', token: 'USDT' },
};

const getPayMethodLabel = (method, t) => {
  // epusdt methods
  const epusdtMeta = EPUSDT_METHOD_META[method];
  if (epusdtMeta) {
    return `${epusdtMeta.token} (${epusdtMeta.chain})`;
  }
  // yifut methods
  switch (method) {
    case 'alipay': return t('TOPUP.PAY_ALIPAY', '支付宝');
    case 'wxpay': return t('TOPUP.PAY_WXPAY', '微信支付');
    case 'qqpay': return t('TOPUP.PAY_QQPAY', 'QQ 钱包');
    case 'bank': return t('TOPUP.PAY_BANK', '银联');
    case 'jdpay': return t('TOPUP.PAY_JDPAY', '京东支付');
    case 'paypal': return t('TOPUP.PAY_PAYPAL', 'PayPal');
    case 'douyinpay': return t('TOPUP.PAY_DOUYINPAY', '抖音支付');
    default: return method;
  }
};

const getMethodMeta = (method) => EPUSDT_METHOD_META[method] || PAY_METHOD_META[method] || { color: 'bg-surface-container', text: 'text-on-surface' };

const getTopupStatusLabel = (status, t) => {
  switch (status) {
    case 'created': return t('TOPUP.STATUS_CREATED', '待支付');
    case 'paid': return t('TOPUP.STATUS_PAID', '已到账');
    case 'failed': return t('TOPUP.STATUS_FAILED', '失败/取消');
    case 'refunded': return t('TOPUP.STATUS_REFUNDED', '已退款');
    default: return status;
  }
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

  // W-4：providers[] 多 provider 支持
  const [selectedProvider, setSelectedProvider] = useState('');
  const [amount, setAmount] = useState('');
  const [payType, setPayType] = useState('');  // 通用 "method key"：yifut 是 alipay/wxpay；epusdt 是 trc20-usdt 等
  const [submitting, setSubmitting] = useState(false);
  const [orderResult, setOrderResult] = useState(null); // {provider, gateway_pay_type, pay_info, ...}

  // 计算当前选中 provider 的配置（presets / methods / min / max）。
  // 优先从 opts.providers[] (W-1 新字段) 取；fallback 到顶层 opts（向后兼容老后端）。
  const currentProviderOpts = useMemo(() => {
    if (!opts) return null;
    if (Array.isArray(opts.providers) && opts.providers.length > 0) {
      return opts.providers.find(p => p.key === selectedProvider) || opts.providers[0];
    }
    // 老后端兼容：把顶层 opts 当 yifut 单 provider 看待
    return {
      key: 'yifut',
      label: '易付通 (CNY)',
      configured: opts.configured,
      currency: 'CNY',
      presets_fen: opts.presets_fen || [],
      min_amount_fen: opts.min_amount_fen,
      max_amount_fen: opts.max_amount_fen,
      methods: opts.methods || [],
      icon_key: 'yifut',
    };
  }, [opts, selectedProvider]);


  const [historyPage, setHistoryPage] = useState(1);
  const historyCacheKey = useMemo(() => getTopupHistoryCacheKey(historyPage), [historyPage]);
  const initialHistoryCache = readPageCache(getTopupHistoryCacheKey(1));
  const [history, setHistory] = useState(() => initialHistoryCache?.rows || []);
  const [historyLoading, setHistoryLoading] = useState(false);
  const [historyTotal, setHistoryTotal] = useState(() => initialHistoryCache?.total || 0);



  // W-4：从 opts 初始化 selectedProvider + payType（多 provider 多态）
  const initFromOpts = useCallback((data) => {
    // 优先用 providers[] (W-1 新字段)
    if (Array.isArray(data.providers) && data.providers.length > 0) {
      const firstConfigured = data.providers.find(p => p.configured) || data.providers[0];
      setSelectedProvider(prev => prev || firstConfigured.key);
      const methods = firstConfigured.methods || [];
      setPayType(prev => prev || methods[0] || '');
      return;
    }
    // 老后端 fallback：当成 yifut 单 provider
    setSelectedProvider(prev => prev || 'yifut');
    const methods = data.methods || [];
    setPayType(prev => prev || methods[0] || '');
  }, []);

  const loadOptions = useCallback(async ({ force = false } = {}) => {
    const cached = readPageCache(TOPUP_OPTIONS_CACHE_KEY);
    if (cached) {
      setOpts(cached);
      initFromOpts(cached);
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
        initFromOpts(json.data);
      }
    } catch {
      // The unconfigured state below handles option-load failures.
    } finally {
      setLoadingOpts(false);
    }
  }, [initFromOpts]);

  // W-4：切换 provider 时重置选中的 method（默认选第一个）
  const handleProviderChange = useCallback((providerKey) => {
    setSelectedProvider(providerKey);
    const provider = opts?.providers?.find(p => p.key === providerKey);
    if (provider?.methods?.length > 0) {
      setPayType(provider.methods[0]);
    }
    setOrderResult(null); // 切 provider 时清旧订单展示
  }, [opts]);

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





  useEffect(() => {
    const targetOrderNo = orderResult?.out_trade_no;
    if (!targetOrderNo) return;

    let cancelled = false;
    let timerId = null;
    const startedAt = Date.now();

    const tick = async () => {
      if (cancelled) return;
      if (Date.now() - startedAt > 10 * 60 * 1000) return;

      try {

        const json = await authFetch(`/api/topup/mine?page=1&page_size=${PAGE_SIZE_HISTORY}`);
        if (json.success && Array.isArray(json.data)) {
          const latestPageOne = { rows: json.data, total: json.meta?.total || 0 };
          writePageCache(getTopupHistoryCacheKey(1), latestPageOne);

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
      } catch {
        // Payment status polling is best-effort.
      }

      if (!cancelled) timerId = setTimeout(tick, 3000);
    };

    timerId = setTimeout(tick, 3000);
    return () => {
      cancelled = true;
      if (timerId) clearTimeout(timerId);
    };
  }, [orderResult?.out_trade_no, t, historyPage]);

  const handleSubmit = async () => {
    const providerOpts = currentProviderOpts;
    if (!providerOpts) {
      toast.error(t('TOPUP.ERR_GATEWAY', '支付通道暂时不可用'));
      return;
    }

    const amt = parseFloat(amount);
    if (isNaN(amt) || amt <= 0) {
      toast.error(t('TOPUP.ERR_AMOUNT', '金额不在允许范围内'));
      return;
    }
    const amountFen = Math.round(amt * 100);
    if (amountFen < providerOpts.min_amount_fen || amountFen > providerOpts.max_amount_fen) {
      toast.error(t('TOPUP.ERR_AMOUNT', '金额不在允许范围内'));
      return;
    }
    if (!payType) {
      toast.error(t('TOPUP.ERR_PAY_TYPE', '请选择支付方式'));
      return;
    }

    // W-4：按 provider 装 body —— yifut 用 pay_type，epusdt 用 method
    const body = {
      provider: selectedProvider,
      amount_fen: amountFen,
      device: detectDevice(),
    };
    if (selectedProvider === 'epusdt') {
      body.method = payType;
    } else {
      body.pay_type = payType;
    }

    setSubmitting(true);
    setOrderResult(null);
    try {
      const json = await authFetch('/api/topup/create', { method: 'POST', body });
      if (json.success && json.data) {
        setOrderResult(json.data);

        if (json.data.pay_info) {
          // 按 provider 不同提示语
          if (selectedProvider === 'epusdt') {
            toast.success(t('TOPUP.EPUSDT_ORDER_CREATED', '订单已创建，请按下方信息转账'));
          } else {
            toast.success(t('TOPUP.GO_PAY_HINT', '订单已创建，请点击下方链接支付'));
          }
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
    const rateMicros = opts?.exchange_rate_rmb_per_usd_micros;
    if (isNaN(amt) || !rateMicros || rateMicros <= 0) return null;
    return (amt * 1_000_000 / rateMicros).toFixed(2);
  })();

  // W-4：epusdt 订单详情解析（pay_info 是 JSON 字符串：receive_address/actual_amount/token/network/expire_at）
  const epusdtPayDetails = useMemo(() => {
    if (orderResult?.provider !== 'epusdt' || !orderResult?.pay_info) return null;
    try {
      const parsed = JSON.parse(orderResult.pay_info);
      return {
        receiveAddress: String(parsed.receive_address || ''),
        actualAmount: Number(parsed.actual_amount) || 0,
        token: String(parsed.token || 'USDT').toUpperCase(),
        network: String(parsed.network || '').toLowerCase(),
        expireAt: Number(parsed.expire_at) || 0,
      };
    } catch {
      return null;
    }
  }, [orderResult]);

  const gatewayPayType = (orderResult?.gateway_pay_type || '').toLowerCase();
  const payInfo = typeof orderResult?.pay_info === 'string' ? orderResult.pay_info : '';
  const isEpusdtOrder = orderResult?.provider === 'epusdt';
  const showQRCode = !isEpusdtOrder && gatewayPayType === 'qrcode' && payInfo;
  const showPaymentLink = !isEpusdtOrder && payInfo && isSafePaymentTarget(payInfo, gatewayPayType);
  const showRawPayInfo = !isEpusdtOrder && payInfo && !showQRCode && !showPaymentLink;

  if (loadingOpts) {
    return <div className="text-center py-12 text-on-surface-variant">{t('COMMON.LOADING', '加载中…')}</div>;
  }

  // W-4：能用 providers[] 数组判定就用，fallback 检查顶层 configured（向后兼容）
  const providersList = Array.isArray(opts?.providers) ? opts.providers : [];
  const hasAnyConfigured = providersList.length > 0
    ? providersList.some(p => p.configured)
    : !!opts?.configured;

  if (!opts || !hasAnyConfigured) {
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

  const providerOpts = currentProviderOpts; // 简化模板里的引用

  return (
    <div className="max-w-3xl mx-auto py-6">
      <StorePage>

        <PageHeader
          icon={Wallet}
          title={t('TOPUP.TITLE', '余额充值')}
          sub={t('TOPUP.SUB_PLAIN', '人民币按当前汇率换算入账 USD 余额')}
        />


      <section className="fl-card p-6 space-y-5">

        {/* W-4：provider tab（只有 2+ providers 时才显示） */}
        {providersList.length > 1 && (
          <div className="space-y-2">
            <span className="text-xs font-semibold text-on-surface-variant uppercase tracking-wide block">
              {t('TOPUP.PROVIDER_LABEL', '充值通道')}
            </span>
            <div role="tablist" className="grid grid-cols-2 gap-2">
              {providersList.map(p => {
                const active = selectedProvider === p.key;
                const Icon = p.key === 'epusdt' ? Coins : Wallet;
                return (
                  <button
                    key={p.key}
                    type="button"
                    role="tab"
                    aria-selected={active}
                    disabled={!p.configured}
                    onClick={() => handleProviderChange(p.key)}
                    className={`h-12 rounded-control flex items-center justify-center gap-2 border transition font-medium ${
                      active
                        ? 'bg-primary text-on-primary border-primary'
                        : 'bg-surface-container text-on-surface border-outline-variant hover:border-primary disabled:opacity-50 disabled:cursor-not-allowed'
                    }`}
                  >
                    <Icon size={16} aria-hidden="true" />
                    {p.label || p.key}
                    {!p.configured && <span className="text-xs opacity-70">({t('TOPUP.UNCONFIGURED', '未配置')})</span>}
                  </button>
                );
              })}
            </div>
          </div>
        )}

        <div className="space-y-3">
          <label htmlFor="topup-amount" className="text-xs font-semibold text-on-surface-variant uppercase tracking-wide">
            {selectedProvider === 'epusdt'
              ? t('TOPUP.AMOUNT_LABEL_USDT', '充值金额（按汇率折 USDT）')
              : t('TOPUP.AMOUNT_LABEL', '充值金额（人民币）')}
          </label>

          <div className="flex items-center gap-2">
            <span className="text-lg text-on-surface-variant font-semibold" aria-hidden="true">¥</span>
            <TextInput
              id="topup-amount"
              type="number"
              step="0.01"
              min={(providerOpts.min_amount_fen / 100).toFixed(2)}
              max={(providerOpts.max_amount_fen / 100).toFixed(2)}
              value={amount}
              onChange={e => { setAmount(e.target.value); }}
              placeholder={`${(providerOpts.min_amount_fen / 100).toFixed(2)} - ${(providerOpts.max_amount_fen / 100).toFixed(2)}`}
              className="flex-1 font-mono"
            />
          </div>

          {(providerOpts.presets_fen || []).length > 0 && (
            <div className="flex flex-wrap gap-2">
              {providerOpts.presets_fen.map(fen => {
                const yuan = fen / 100;
                const active = parseFloat(amount) === yuan;
                return (
                  <button
                    key={fen}
                    type="button"
                    onClick={() => setAmount(String(yuan))}
                    className={`px-4 py-2 rounded-control text-sm font-semibold border transition ${
                      active
                        ? 'bg-primary text-on-primary border-primary'
                        : 'bg-surface-container text-on-surface-variant border-outline-variant hover:border-primary hover:text-primary'
                    }`}
                  >
                    {t('TOPUP.PRESET_RMB', { amount: yuan, defaultValue: '¥{{amount}}' })}
                  </button>
                );
              })}
            </div>
          )}

          <div className="flex items-center justify-between text-xs">
            <span className="text-on-surface-variant">
              {t('TOPUP.RANGE_HINT', {
                min: (providerOpts.min_amount_fen / 100).toFixed(2),
                max: (providerOpts.max_amount_fen / 100).toFixed(2),
                defaultValue: '可充值范围：¥{{min}} - ¥{{max}}',
              })}
            </span>
            {usdEstimate && (
              <span className="text-primary font-semibold">
                {selectedProvider === 'epusdt'
                  ? t('TOPUP.ESTIMATED_USDT', {
                      amount: usdEstimate,
                      defaultValue: '约 {{amount}} USDT',
                    })
                  : t('TOPUP.ESTIMATED_USD', {
                      amount: usdEstimate,
                      rate: (opts.exchange_rate_rmb_per_usd_micros / 1_000_000).toFixed(4),
                      defaultValue: '预计入账 {{amount}} USD（汇率 {{rate}}）',
                    })}
              </span>
            )}
          </div>
        </div>


        <div className="space-y-3">
          <span id="topup-pay-method-label" className="text-xs font-semibold text-on-surface-variant uppercase tracking-wide block">
            {selectedProvider === 'epusdt'
              ? t('TOPUP.CHAIN_LABEL', '选择网络')
              : t('TOPUP.PAY_METHOD_LABEL', '支付方式')}
          </span>

          <div role="group" aria-labelledby="topup-pay-method-label" className="grid grid-cols-2 sm:grid-cols-3 gap-2">
            {(providerOpts.methods || []).map(m => {
              const meta = getMethodMeta(m);
              const active = payType === m;
              const isEpusdt = !!EPUSDT_METHOD_META[m];
              return (
                <button
                  key={m}
                  type="button"
                  aria-pressed={active}
                  onClick={() => setPayType(m)}
                  className={`relative h-12 rounded-control flex items-center justify-center gap-2 border transition font-medium ${
                    active
                      ? `${meta.color} ${meta.text} border-transparent`
                      : 'bg-surface-container text-on-surface border-outline-variant hover:border-primary'
                  }`}
                >
                  {isEpusdt ? <Coins size={16} aria-hidden="true" /> : <Banknote size={16} aria-hidden="true" />}
                  {getPayMethodLabel(m, t)}
                </button>
              );
            })}
          </div>
        </div>


        <button
          type="button"
          onClick={handleSubmit}
          disabled={submitting || !amount || !payType}
          className="w-full h-12 bg-primary text-on-primary rounded-control text-base font-semibold hover:opacity-90 disabled:opacity-50 transition"
        >
          {submitting ? t('TOPUP.SUBMITTING', '下单中...') : t('TOPUP.SUBMIT', '立即支付')}
        </button>
      </section>


      {orderResult && !isEpusdtOrder && (
        <section className="fl-card p-8 flex flex-col items-center gap-5 border-primary/40 shadow-primary/5">
          <div className="text-center">
            <div className="text-base font-semibold text-on-surface flex items-center justify-center gap-2">
              <span className={`w-2 h-2 rounded-full bg-primary animate-pulse`} />
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

      {/* W-4：epusdt 订单展示——钱包地址 + 精确金额 + 复制按钮 + QR + 倒计时 */}
      {orderResult && isEpusdtOrder && epusdtPayDetails && (
        <EpusdtOrderPanel
          orderResult={orderResult}
          details={epusdtPayDetails}
          t={t}
        />
      )}


      <StoreSection
        title={t('TOPUP.HISTORY_TITLE', '充值记录')}
        right={
          <button
            onClick={() => loadHistory({ force: true })}
            className="w-8 h-8 rounded-control flex items-center justify-center text-on-surface-variant hover:bg-on-surface/[0.04]"
            aria-label={t('COMMON.REFRESH', '刷新')}
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
                      {getPayMethodLabel(o.pay_type, t)}
                    </td>
                    <td className="px-3 py-2">
                      <span className={statusClass(o.status)}>
                        {getTopupStatusLabel(o.status, t)}
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

// W-4：epusdt 订单展示组件——钱包地址 + 精确金额 + 复制 + QR + 倒计时
//
// 用户体验关键点：
//  - actual_amount 必须精确到小数点后 4 位（epusdt 用尾数 0.0001 区分订单）
//  - 复制按钮一键复制地址 / 金额
//  - QR 码默认编码"地址+金额"（部分钱包 app 能解析自动填表）
//  - 过期倒计时（10 min 默认）让用户感知"再不付就过期"
function EpusdtOrderPanel({ orderResult, details, t }) {
  const { receiveAddress, actualAmount, token, network, expireAt } = details;
  const networkMeta = EPUSDT_METHOD_META[`${network === 'tron' ? 'trc20' : network === 'bsc' ? 'bep20' : network === 'ethereum' ? 'erc20' : network}-${token.toLowerCase()}`];
  const chainLabel = networkMeta?.chain || network.toUpperCase();

  const [remainingSec, setRemainingSec] = useState(() => Math.max(0, expireAt - Math.floor(Date.now() / 1000)));
  useEffect(() => {
    if (!expireAt) return undefined;
    const id = setInterval(() => {
      setRemainingSec(Math.max(0, expireAt - Math.floor(Date.now() / 1000)));
    }, 1000);
    return () => clearInterval(id);
  }, [expireAt]);

  const handleCopy = async (text, label) => {
    try {
      await navigator.clipboard.writeText(text);
      toast.success(t('TOPUP.COPIED', { what: label, defaultValue: '{{what}} 已复制' }));
    } catch {
      toast.error(t('TOPUP.COPY_FAILED', '复制失败，请手动选中'));
    }
  };

  const amountStr = actualAmount.toFixed(4);
  // QR 码内容：URI scheme 形式（钱包 app 可解析），mainstream 钱包如 TronLink 支持 tron: scheme
  const qrPayload = `${network}:${receiveAddress}?amount=${amountStr}&token=${token}`;
  const expired = remainingSec === 0;
  const mins = Math.floor(remainingSec / 60);
  const secs = remainingSec % 60;

  return (
    <section className="fl-card p-6 flex flex-col items-center gap-5 border-primary/40 shadow-primary/5">
      <div className="text-center">
        <div className="text-base font-semibold text-on-surface flex items-center justify-center gap-2">
          <span className={`w-2 h-2 rounded-full ${expired ? 'bg-error' : 'bg-primary animate-pulse'}`} />
          {expired
            ? t('TOPUP.EPUSDT_EXPIRED', '订单已过期，请重新下单')
            : t('TOPUP.EPUSDT_WAITING', '等待链上确认中…')}
        </div>
        <p className="text-xs text-on-surface-variant mt-1">
          {t('TOPUP.EPUSDT_HINT', '请用您的钱包扫码或复制下方地址，金额必须精确到小数点后 4 位')}
        </p>
      </div>

      {/* QR 码 */}
      <div className="bg-white p-4 rounded-overlay flex items-center justify-center">
        <QRCodeSVG value={qrPayload} size={224} level="M" />
      </div>

      {/* 链类型 + 精确金额（高亮） */}
      <div className="w-full grid grid-cols-2 gap-3">
        <div className="rounded-control border border-outline-variant bg-surface-container p-3 text-center">
          <div className="text-xs text-on-surface-variant mb-1">{t('TOPUP.EPUSDT_NETWORK', '网络')}</div>
          <div className="text-base font-semibold text-on-surface">{token} ({chainLabel})</div>
        </div>
        <div className="rounded-control border border-primary/40 bg-primary/5 p-3 text-center">
          <div className="text-xs text-primary mb-1">{t('TOPUP.EPUSDT_AMOUNT', '精确金额')}</div>
          <div className="text-base font-bold text-primary font-mono flex items-center justify-center gap-1">
            {amountStr} {token}
            <button
              type="button"
              onClick={() => handleCopy(amountStr, t('TOPUP.EPUSDT_AMOUNT', '精确金额'))}
              className="ml-1 text-on-surface-variant hover:text-primary"
              aria-label={t('TOPUP.COPY', '复制')}
            >
              <Copy size={14} />
            </button>
          </div>
        </div>
      </div>

      {/* 收款地址 + 复制按钮 */}
      <div className="w-full rounded-control border border-outline-variant bg-surface-container p-3">
        <div className="text-xs font-semibold text-on-surface-variant mb-2">
          {t('TOPUP.EPUSDT_ADDRESS', '收款地址')}
        </div>
        <div className="flex items-center gap-2">
          <code className="flex-1 break-all text-xs font-mono text-on-surface select-all">
            {receiveAddress}
          </code>
          <button
            type="button"
            onClick={() => handleCopy(receiveAddress, t('TOPUP.EPUSDT_ADDRESS', '收款地址'))}
            className="shrink-0 inline-flex items-center gap-1 px-3 h-8 bg-primary text-on-primary rounded-control text-xs font-semibold hover:opacity-90"
          >
            <Copy size={12} />
            {t('TOPUP.COPY', '复制')}
          </button>
        </div>
      </div>

      {/* 过期倒计时 */}
      {expireAt > 0 && (
        <div className={`w-full text-center text-sm font-mono flex items-center justify-center gap-2 ${expired ? 'text-error' : 'text-on-surface-variant'}`}>
          <Clock size={14} />
          {expired
            ? t('TOPUP.EPUSDT_EXPIRED_SHORT', '订单已过期')
            : t('TOPUP.EPUSDT_COUNTDOWN', {
                mins: String(mins).padStart(2, '0'),
                secs: String(secs).padStart(2, '0'),
                defaultValue: '剩余 {{mins}}:{{secs}} 自动过期',
              })}
        </div>
      )}

      <div className="w-full pt-3 border-t border-outline-variant/40 flex items-center justify-between text-[11px] text-on-surface-variant">
        <span>{t('TOPUP.TABLE_OUT_TRADE_NO', '订单号')}</span>
        <span className="font-mono select-all">{orderResult.out_trade_no}</span>
      </div>
    </section>
  );
}

export default Topup;
