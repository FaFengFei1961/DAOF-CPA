import React, { useState, useEffect } from 'react';
import { QRCodeSVG } from 'qrcode.react';
import { Copy, Clock } from 'lucide-react';
import toast from 'react-hot-toast';

// W-4：epusdt 订单展示组件——钱包地址 + 精确金额 + 复制 + QR + 倒计时
//
// 用户体验关键点：
//  - actual_amount 必须精确到小数点后 4 位（epusdt 用尾数 0.0001 区分订单）
//  - 复制按钮一键复制地址 / 金额
//  - QR 码默认编码"网络:地址?amount=X"URI scheme（TronLink / MetaMask 等可解析自动填表）
//  - 过期倒计时（10 min 默认）让用户感知"再不付就过期"
//
// 拆分自 Topup.jsx（W-4 收口）保持文件 < 800 行准则。

// 网络 key → 显示名称的轻量映射；Topup.jsx 也有同款 EPUSDT_METHOD_META，
// 这里独立维护避免循环 import。epusdt 协议字段命名（network=tron/ethereum/bsc/polygon）。
const NETWORK_LABEL = {
  tron: 'TRC20',
  ethereum: 'ERC20',
  bsc: 'BEP20',
  polygon: 'Polygon',
};

/**
 * @param {{
 *   orderResult: { out_trade_no: string },
 *   details: {
 *     receiveAddress: string,
 *     actualAmount: number,
 *     token: string,
 *     network: string,
 *     expireAt: number
 *   },
 *   t: (key: string, opts?: any) => string
 * }} props
 */
export default function EpusdtOrderPanel({ orderResult, details, t }) {
  const { receiveAddress, actualAmount, token, network, expireAt } = details;
  const chainLabel = NETWORK_LABEL[network] || network.toUpperCase();

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

      {/* QR 码（URI scheme，钱包 app 可解析） */}
      <div className="bg-white p-4 rounded-overlay flex items-center justify-center">
        <QRCodeSVG value={qrPayload} size={224} level="M" />
      </div>

      {/* 链类型 + 精确金额 */}
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

      {/* 收款地址 + 复制 */}
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

      {/* 倒计时 */}
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
