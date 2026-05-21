import React from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { CheckCircle2, Clock, XCircle, AlertTriangle } from 'lucide-react';




const TopupResult = () => {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const status = searchParams.get('status') || 'pending';
  const outTradeNo = searchParams.get('out_trade_no') || '';




  const config = (() => {
    switch (status) {
      case 'success':
        return {
          icon: <CheckCircle2 size={56} className="text-success" />,
          title: t('TOPUP.RESULT.SUCCESS', '支付完成，正在确认到账'),
          subtitle: t('TOPUP.RESULT.SUCCESS_SUB', '余额会在收到支付平台异步通知后自动入账，通常几秒到几分钟'),
          tone: 'border-success/30',
        };
      case 'pending':
        return {
          icon: <Clock size={56} className="text-warning" />,
          title: t('TOPUP.RESULT.PENDING', '我们正在确认您的支付，稍后将自动到账'),
          tone: 'border-warning/30',
        };
      case 'sign_invalid':
        return {
          icon: <AlertTriangle size={56} className="text-error" />,
          title: t('TOPUP.RESULT.SIGN_INVALID', '回调签名异常，请提交工单'),
          tone: 'border-error/30',
        };
      default:
        return {
          icon: <XCircle size={56} className="text-error" />,
          title: t('TOPUP.RESULT.FAILED', '支付失败或已取消'),
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
            {/* fix P3（codex review verify-r7）：原 navigate('/') 让已订阅用户进 dashboard 后看不到
                浏览套餐入口（modal 只在空订阅状态弹）。改用 ?openBrowse=store query 与 /upgrade?pane=store
                compat 路径对齐，MySubscriptions 检测后自动弹 BrowsePackagesModal。 */}
            <button
              onClick={() => navigate('/?openBrowse=store')}
              className="card p-4 hover:border-primary/50 hover:bg-primary/5 transition group"
            >
              <div className="font-semibold text-sm group-hover:text-primary mb-1">{t('TOPUP.RESULT.ACTION_SUBSCRIBE', '立刻订阅套餐')}</div>
              <div className="text-xs text-on-surface-variant">{t('TOPUP.RESULT.ACTION_SUBSCRIBE_DESC', '获取专属额度和优先排队')}</div>
            </button>
            <button
              onClick={() => navigate('/tokens')}
              className="card p-4 hover:border-primary/50 hover:bg-primary/5 transition group"
            >
              <div className="font-semibold text-sm group-hover:text-primary mb-1">{t('TOPUP.RESULT.ACTION_API', '去使用 API')}</div>
              <div className="text-xs text-on-surface-variant">{t('TOPUP.RESULT.ACTION_API_DESC', '管理 Token 并开始调用')}</div>
            </button>
            <button
              onClick={() => navigate('/bills')}
              className="card p-4 hover:border-primary/50 hover:bg-primary/5 transition group"
            >
              <div className="font-semibold text-sm group-hover:text-primary mb-1">{t('TOPUP.RESULT.ACTION_BILLS', '查看账单')}</div>
              <div className="text-xs text-on-surface-variant">{t('TOPUP.RESULT.ACTION_BILLS_DESC', '查看交易明细与记录')}</div>
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
              {t('TOPUP.RESULT.RETRY', '重试充值')}
            </button>
            <button
              type="button"
              onClick={() => navigate('/tickets')}
              className="h-10 px-6 bg-surface-container border border-outline rounded-control text-sm font-semibold hover:border-primary hover:text-primary transition"
            >
              {t('TOPUP.RESULT.CONTACT_SUPPORT', '提交工单')}
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
