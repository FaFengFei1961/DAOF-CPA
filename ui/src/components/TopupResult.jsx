import React from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { CheckCircle2, Clock, XCircle, AlertTriangle } from 'lucide-react';

// 支付完成跳转后的落地页。仅展示状态，不会主动加额度（加额度只在后端 notify 路径）。
// Phase 0：从 React Router 的 useSearchParams 读 query（旧 hash redirect 已把
// /#topup_result?status=success 改写为 /topup-result?status=success）。
const TopupResult = () => {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const status = searchParams.get('status') || 'pending';
  const outTradeNo = searchParams.get('out_trade_no') || '';

  // fix Critical Codex UX 审查（第二十五轮 #3）：return 路径**不加额度**（详见后端 controller/topup.go），
  // 余额到账走异步 notify 路径。原 success 文案"余额已到账"承诺过早，与真实流程不符。
  // 改为"支付完成，正在确认到账"，让用户回 topup/account 看实时余额而不是基于错误承诺退出。
  const config = (() => {
    switch (status) {
      case 'success':
        return {
          icon: <CheckCircle2 size={56} className="text-success" />,
          title: t('TOPUP.RESULT_SUCCESS', '支付完成，正在确认到账'),
          subtitle: t('TOPUP.RESULT_SUCCESS_SUB', '余额会在收到支付平台异步通知后自动入账，通常几秒到几分钟'),
          tone: 'border-success/30',
        };
      case 'pending':
        return {
          icon: <Clock size={56} className="text-warning" />,
          title: t('TOPUP.RESULT_PENDING', '我们正在确认您的支付，稍后将自动到账'),
          tone: 'border-warning/30',
        };
      case 'sign_invalid':
        return {
          icon: <AlertTriangle size={56} className="text-error" />,
          title: t('TOPUP.RESULT_SIGN_INVALID', '回调签名异常，请联系客服'),
          tone: 'border-error/30',
        };
      default:
        return {
          icon: <XCircle size={56} className="text-error" />,
          title: t('TOPUP.RESULT_FAILED', '支付失败或已取消'),
          tone: 'border-error/30',
        };
    }
  })();

  return (
    <div className="max-w-xl mx-auto py-12">
      <div className={`bg-surface-container-high border ${config.tone} rounded-overlay p-10 text-center`}>
        <div className="flex justify-center mb-4">{config.icon}</div>
        <h1 className="text-lg font-bold text-on-surface mb-2">{config.title}</h1>
        {config.subtitle && (
          <p className="text-sm text-on-surface-variant mb-4">{config.subtitle}</p>
        )}
        {outTradeNo && (
          <p className="text-xs font-mono text-on-surface-variant mb-6 break-all">{outTradeNo}</p>
        )}
        
        {status === 'success' && (
          <div className="grid grid-cols-1 sm:grid-cols-3 gap-3 mb-8 text-left">
            <button
              onClick={() => navigate('/upgrade')}
              className="fl-card p-4 hover:border-primary/50 hover:bg-primary/5 transition group"
            >
              <div className="font-semibold text-sm group-hover:text-primary mb-1">立刻订阅套餐</div>
              <div className="text-xs text-on-surface-variant">获取专属额度和优先排队</div>
            </button>
            <button
              onClick={() => navigate('/tokens')}
              className="fl-card p-4 hover:border-primary/50 hover:bg-primary/5 transition group"
            >
              <div className="font-semibold text-sm group-hover:text-primary mb-1">去使用 API</div>
              <div className="text-xs text-on-surface-variant">管理 Token 并开始调用</div>
            </button>
            <button
              onClick={() => navigate('/bills')}
              className="fl-card p-4 hover:border-primary/50 hover:bg-primary/5 transition group"
            >
              <div className="font-semibold text-sm group-hover:text-primary mb-1">查看账单</div>
              <div className="text-xs text-on-surface-variant">查看交易明细与记录</div>
            </button>
          </div>
        )}

        {status === 'failed' && (
          <div className="flex gap-3 justify-center mb-6">
            <button
              type="button"
              onClick={() => navigate('/topup')}
              className="h-10 px-6 bg-primary text-on-primary rounded-control text-sm font-semibold hover:opacity-90"
            >
              重试充值
            </button>
            <button
              type="button"
              onClick={() => navigate('/tickets')}
              className="h-10 px-6 bg-surface-container border border-outline rounded-control text-sm font-semibold hover:border-primary hover:text-primary transition"
            >
              联系客服
            </button>
          </div>
        )}
        
        {status !== 'failed' && (
          <button
            type="button"
            onClick={() => navigate('/topup')}
            className="h-10 px-6 bg-primary text-on-primary rounded-control text-sm font-semibold hover:opacity-90"
          >
            {t('TOPUP.BACK_TO_TOPUP', '返回充值')}
          </button>
        )}
      </div>
    </div>
  );
};

export default TopupResult;
