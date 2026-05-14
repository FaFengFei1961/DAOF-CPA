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
          icon: <CheckCircle2 size={56} className="text-emerald-400" />,
          title: t('TOPUP.RESULT_SUCCESS', '支付完成，正在确认到账'),
          subtitle: t('TOPUP.RESULT_SUCCESS_SUB', '余额会在收到支付平台异步通知后自动入账，通常几秒到几分钟'),
          tone: 'border-emerald-500/30',
        };
      case 'pending':
        return {
          icon: <Clock size={56} className="text-amber-400" />,
          title: t('TOPUP.RESULT_PENDING', '我们正在确认您的支付，稍后将自动到账'),
          tone: 'border-amber-500/30',
        };
      case 'sign_invalid':
        return {
          icon: <AlertTriangle size={56} className="text-red-400" />,
          title: t('TOPUP.RESULT_SIGN_INVALID', '回调签名异常，请联系客服'),
          tone: 'border-red-500/30',
        };
      default:
        return {
          icon: <XCircle size={56} className="text-red-400" />,
          title: t('TOPUP.RESULT_FAILED', '支付失败或已取消'),
          tone: 'border-red-500/30',
        };
    }
  })();

  return (
    <div className="max-w-xl mx-auto py-12">
      <div className={`bg-surface-container-high border ${config.tone} rounded-2xl p-10 text-center`}>
        <div className="flex justify-center mb-4">{config.icon}</div>
        <h1 className="text-lg font-bold text-on-surface mb-2">{config.title}</h1>
        {config.subtitle && (
          <p className="text-sm text-on-surface-variant mb-4">{config.subtitle}</p>
        )}
        {outTradeNo && (
          <p className="text-xs font-mono text-on-surface-variant mb-6 break-all">{outTradeNo}</p>
        )}
        <button
          type="button"
          onClick={() => navigate('/topup')}
          className="h-10 px-6 bg-primary text-on-primary rounded-lg text-sm font-semibold hover:opacity-90"
        >
          {t('TOPUP.BACK_TO_TOPUP', '返回充值')}
        </button>
      </div>
    </div>
  );
};

export default TopupResult;
