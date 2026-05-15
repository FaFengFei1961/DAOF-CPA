import React, { useState, useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { X, Ticket } from 'lucide-react';
import { authFetch } from '../utils/authFetch';
import toast from 'react-hot-toast';
import { useConfirm } from '../context/ConfirmContext';

const BulkGrantCouponModal = ({ open, onClose, userIds, onSuccess }) => {
    const { t } = useTranslation();
    const confirm = useConfirm();
    const [templates, setTemplates] = useState([]);
    const [loading, setLoading] = useState(true);
    const [selectedTemplateId, setSelectedTemplateId] = useState('');
    const [quantity, setQuantity] = useState(1);
    const [reason, setReason] = useState('');
    const [granting, setGranting] = useState(false);

    useEffect(() => {
        if (!open) return;
        const fetchTemplates = async () => {
            setLoading(true);
            try {
                const data = await authFetch('/api/admin/coupon-templates');
                if (data.success && Array.isArray(data.data)) {
                    const activeTemplates = data.data.filter(t => t.enabled);
                    setTemplates(activeTemplates);
                    if (activeTemplates.length > 0) {
                        setSelectedTemplateId(activeTemplates[0].id.toString());
                    }
                }
            } catch (err) {
                toast.error(t('USER_MGMT.NET_ERROR', '网络异常'));
            }
            setLoading(false);
        };
        fetchTemplates();
        setQuantity(1);
        setReason('');
    }, [open, t]);

    if (!open) return null;

    const handleNext = async () => {
        const selected = templates.find(t => t.id.toString() === selectedTemplateId);
        if (!selected) {
            toast.error(t('USER_MGMT.BULK_GRANT_TEMPLATE_LABEL'));
            return;
        }

        const qty = parseInt(quantity, 10);
        if (isNaN(qty) || qty < 1 || qty > 100) {
            toast.error('数量必须在 1 到 100 之间');
            return;
        }

        const total = userIds.length * qty;
        
        const confirmed = await confirm(
            t('USER_MGMT.BULK_GRANT_CONFIRM_MSG', { count: userIds.length, qty, name: selected.name, total }),
            {
                title: t('USER_MGMT.BULK_GRANT_CONFIRM_TITLE'),
                danger: true,
                impactCount: userIds.length,
                confirmPhrase: `GRANT ${total} COUPONS`
            }
        );

        if (!confirmed) return;

        setGranting(true);
        try {
            const res = await authFetch('/api/admin/users/bulk-grant-coupon', {
                method: 'POST',
                body: {
                    user_ids: userIds,
                    template_id: parseInt(selectedTemplateId, 10),
                    quantity: qty,
                    reason: reason.trim() || undefined
                }
            });

            if (res.success) {
                toast.success(t('USER_MGMT.BULK_GRANT_SUCCESS', { success: res.summary?.success_count || 0, total: res.summary?.total_users || userIds.length }));
                if (res.summary?.failed_count > 0 && Array.isArray(res.results)) {
                    const failed = res.results.filter(r => !r.success).slice(0, 5);
                    if (failed.length > 0) {
                        toast.error(`失败示例: User ${failed.map(f => f.user_id).join(', ')}`);
                    }
                }
                onSuccess();
            } else {
                toast.error(res.message || '批量发券失败');
            }
        } catch (e) {
            toast.error('网络异常，批量发券失败');
        }
        setGranting(false);
    };

    return (
        <div className="fixed inset-0 z-[60] flex items-center justify-center p-4 bg-black/60 backdrop-blur-sm">
            <div className="relative w-full max-w-lg bg-surface-container border border-outline-variant rounded-overlay shadow-2xl p-6">
                <button type="button" onClick={onClose} className="absolute top-4 right-4 text-on-surface-variant hover:text-white" aria-label="Close">
                    <X size={18} />
                </button>
                <h2 className="text-xl font-bold text-on-surface mb-2 flex items-center gap-2">
                    <Ticket size={20} className="text-primary" /> 
                    {t('USER_MGMT.BULK_GRANT_TITLE')}
                </h2>
                
                <p className="text-sm text-on-surface-variant mb-6">
                    将给 <span className="font-bold text-primary">{userIds.length}</span> 个用户每人发放 <span className="font-bold">{quantity}</span> 张选定优惠券。
                </p>

                {loading ? (
                    <div className="text-center py-6 text-on-surface-variant">加载模板中...</div>
                ) : (
                    <div className="flex flex-col gap-4">
                        <div className="flex flex-col gap-1.5">
                            <label className="text-xs font-semibold text-on-surface-variant ml-1">{t('USER_MGMT.BULK_GRANT_TEMPLATE_LABEL')}</label>
                            {templates.length === 0 ? (
                                <div className="text-sm text-warning p-3 bg-warning/10 rounded-control border border-warning/20">无可用模板</div>
                            ) : (
                                <select 
                                    value={selectedTemplateId} 
                                    onChange={(e) => setSelectedTemplateId(e.target.value)}
                                    className="w-full h-10 bg-surface-container-high border border-outline rounded-control px-3 text-sm text-on-surface focus:border-primary outline-none"
                                >
                                    {templates.map(t => (
                                        <option key={t.id} value={t.id}>
                                            {t.name} ({t.discount_type === 'FIXED' ? `$${t.discount_value}` : `${t.discount_value}%`}) - {t.valid_days ? `${t.valid_days}天` : '永久'}
                                        </option>
                                    ))}
                                </select>
                            )}
                        </div>

                        <div className="flex flex-col gap-1.5">
                            <label className="text-xs font-semibold text-on-surface-variant ml-1">{t('USER_MGMT.BULK_GRANT_QUANTITY_LABEL')}</label>
                            <input 
                                type="number" 
                                min="1" max="100" 
                                value={quantity} 
                                onChange={(e) => setQuantity(e.target.value)}
                                className="w-full h-10 bg-surface-container-high border border-outline rounded-control px-3 text-sm text-on-surface focus:border-primary outline-none"
                            />
                        </div>

                        <div className="flex flex-col gap-1.5">
                            <label className="text-xs font-semibold text-on-surface-variant ml-1">{t('USER_MGMT.BULK_GRANT_REASON_LABEL')}</label>
                            <textarea 
                                value={reason} 
                                onChange={(e) => setReason(e.target.value)}
                                maxLength={500}
                                className="w-full bg-surface-container-high border border-outline rounded-control p-3 text-sm text-on-surface focus:border-primary outline-none placeholder:text-on-surface-variant/50"
                                rows={3}
                                placeholder="选填，将记录在审计日志中..."
                            />
                        </div>
                    </div>
                )}

                <div className="flex gap-3 mt-8">
                    <button 
                        onClick={onClose} 
                        className="flex-1 h-10 bg-surface-container-high border border-outline-variant text-on-surface-variant rounded-control hover:bg-surface-variant transition-colors text-sm"
                    >
                        取消
                    </button>
                    <button 
                        onClick={handleNext} 
                        disabled={loading || granting || templates.length === 0}
                        className="flex-1 h-10 bg-primary text-on-primary font-medium rounded-control hover:opacity-90 disabled:opacity-40 transition-opacity text-sm"
                    >
                        {granting ? '处理中...' : '下一步：确认'}
                    </button>
                </div>
            </div>
        </div>
    );
};

export default BulkGrantCouponModal;