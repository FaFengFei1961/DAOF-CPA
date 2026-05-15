import React, { useState, useEffect, useRef, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { Gift, X, Search } from 'lucide-react';
import toast from 'react-hot-toast';
import { authFetch } from '../utils/authFetch';
import { useModalA11y } from '../hooks/useModalA11y';

/**
 * AdminGrantSubscriptionModal
 *
 * 管理员赠送订阅对话框。
 * 表单字段：
 *   - 用户选择（按 username / phone / github_id 搜索）
 *   - 套餐选择（来自 /api/admin/packages）
 *   - 数量
 *   - 赠送理由（必填，进审计）
 *
 * 与后端 POST /api/admin/subscriptions/grant 对齐：
 *   { user_id, package_id, quantity, reason }
 *
 * Props:
 *   open       - 是否显示
 *   onClose    - 关闭回调（取消 / ESC / 背景点击 / 提交成功）
 *   onSuccess  - 提交成功后回调（父组件可刷新订阅列表）
 *   prefillUser - 可选：预填的用户 { id, username }（从用户管理页直接打开时用）
 */
const AdminGrantSubscriptionModal = ({ open, onClose, onSuccess, prefillUser = null }) => {
  const { t } = useTranslation();

  // 用户搜索状态
  const [userQuery, setUserQuery] = useState('');
  const [userSuggestions, setUserSuggestions] = useState([]);
  const [selectedUser, setSelectedUser] = useState(null);
  const [searchingUsers, setSearchingUsers] = useState(false);

  // 套餐选择
  const [packages, setPackages] = useState([]);
  const [selectedPackageId, setSelectedPackageId] = useState('');
  const [loadingPackages, setLoadingPackages] = useState(false);

  // 表单字段
  const [quantity, setQuantity] = useState(1);
  const [reason, setReason] = useState('');
  const [submitting, setSubmitting] = useState(false);

  // a11y
  const userInputRef = useRef(null);
  const modalRef = useRef(null); // C5 第二十轮: focus trap 范围
  const initialFocusRef = prefillUser ? null : userInputRef;
  const { onBackdropClick } = useModalA11y(open, () => !submitting && onClose(), initialFocusRef, modalRef);

  // 初始化 / 重置（每次 open 切 false→true 时清空）
  useEffect(() => {
    if (open) {
      setUserQuery(prefillUser?.username || '');
      setSelectedUser(prefillUser);
      setUserSuggestions([]);
      setSelectedPackageId('');
      setQuantity(1);
      setReason('');
    }
  }, [open, prefillUser]);

  // 加载套餐列表（admin 端）
  useEffect(() => {
    if (!open) return;
    setLoadingPackages(true);
    authFetch('/api/admin/packages')
      .then((j) => {
        if (j?.success) {
          // 只保留启用的（Phase 8 后只剩 subscription 类）
          setPackages((j.data || []).filter((p) => p.enabled !== false));
        } else {
          toast.error(j?.message || t('ADMIN_GRANT.LOAD_PKG_FAIL', '套餐列表加载失败'));
        }
      })
      .catch(() => toast.error(t('API.ERR_NETWORK', '网络异常')))
      .finally(() => setLoadingPackages(false));
  }, [open, t]);

  // 搜索用户（debounce 300ms）
  // fix MAJOR M9（gemini 第二十轮）：reqIdRef 防慢搜索覆盖快搜索
  const searchReqRef = useRef(0);
  const searchUsers = useCallback(async (q) => {
    if (!q || q.length < 2) {
      setUserSuggestions([]);
      return;
    }
    const myReqId = ++searchReqRef.current;
    setSearchingUsers(true);
    try {
      const params = new URLSearchParams({ search: q, page: '1', page_size: '10' });
      const j = await authFetch(`/api/admin/users?${params.toString()}`);
      // 慢搜索回来时，新搜索已发起 → 丢弃结果
      if (myReqId !== searchReqRef.current) return;
      if (j?.success) {
        // 只显示 role=user 的（赠送目标必须是普通用户）
        setUserSuggestions((j.data || []).filter((u) => u.role === 'user'));
      }
    } catch {
      // 静默失败：搜索失败不强阻塞，admin 仍可手填 user_id
    } finally {
      if (myReqId === searchReqRef.current) setSearchingUsers(false);
    }
  }, []);

  useEffect(() => {
    if (!open) return;
    const trimmed = userQuery.trim();
    // 已选用户且 query 等于 username 时不搜（避免选择后还无谓查询）
    if (selectedUser && trimmed === selectedUser.username) {
      setUserSuggestions([]);
      return;
    }
    const handle = setTimeout(() => searchUsers(trimmed), 300);
    return () => clearTimeout(handle);
  }, [userQuery, open, selectedUser, searchUsers]);

  if (!open) return null;

  // 表单基础有效性（Submit 按钮 disabled 用，减少无效点击）
  const isFormValid = !!selectedUser?.id && !!selectedPackageId && reason.trim().length > 0;

  const submit = async () => {
    if (!selectedUser?.id) {
      toast.error(t('ADMIN_GRANT.ERR_NO_USER', '请先选择目标用户'));
      return;
    }
    if (!selectedPackageId) {
      toast.error(t('ADMIN_GRANT.ERR_NO_PKG', '请选择套餐'));
      return;
    }
    const qty = parseInt(quantity, 10);
    if (!Number.isInteger(qty) || qty < 1 || qty > 100) {
      toast.error(t('ADMIN_GRANT.ERR_QTY', '数量必须是 1-100 的整数'));
      return;
    }
    const trimmedReason = reason.trim();
    if (!trimmedReason) {
      toast.error(t('ADMIN_GRANT.ERR_REASON', '请填写赠送理由'));
      return;
    }
    if (trimmedReason.length > 500) {
      toast.error(t('ADMIN_GRANT.ERR_REASON_LONG', '理由不能超过 500 字符'));
      return;
    }
    if (/[\r\n\t]/.test(trimmedReason)) {
      toast.error(t('ADMIN_GRANT.ERR_REASON_CTRL', '理由不能包含换行 / 制表符'));
      return;
    }

    setSubmitting(true);
    try {
      const json = await authFetch('/api/admin/subscriptions/grant', {
        method: 'POST',
        body: {
          user_id: selectedUser.id,
          package_id: parseInt(selectedPackageId, 10),
          quantity: qty,
          reason: trimmedReason,
        },
      });
      if (json?.success) {
        toast.success(t('ADMIN_GRANT.SUCCESS', { qty, defaultValue: '已赠送 {{qty}} 份' }));
        onSuccess?.();
        onClose();
      } else {
        toast.error(json?.message || t('ADMIN_GRANT.FAIL', '赠送失败'));
      }
    } catch {
      toast.error(t('API.ERR_NETWORK', '网络异常'));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div
      ref={modalRef}
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/60"
      onClick={onBackdropClick}
      role="dialog"
      aria-modal="true"
      aria-labelledby="grant-modal-title"
    >
      <div className="bg-surface-container rounded-overlay border border-outline-variant w-full max-w-lg m-4 max-h-[90vh] overflow-y-auto">
        {/* Header */}
        <div className="flex items-center justify-between px-5 py-4 border-b border-outline-variant">
          <h3 id="grant-modal-title" className="text-lg font-semibold text-on-surface flex items-center gap-2">
            <Gift size={18} className="text-success" />
            {t('ADMIN_GRANT.TITLE', '赠送订阅')}
          </h3>
          <button
            type="button"
            onClick={onClose}
            disabled={submitting}
            className="text-on-surface-variant hover:text-on-surface p-1 rounded-control disabled:opacity-50"
            aria-label={t('COMMON.CLOSE', '关闭')}
          >
            <X size={18} />
          </button>
        </div>

        {/* Body */}
        <div className="px-5 py-4 space-y-4">
          {/* 用户选择 */}
          <div>
            <label htmlFor="grant-target-user" className="block text-sm text-on-surface mb-1">
              {t('ADMIN_GRANT.TARGET_USER', '目标用户')} <span className="text-error">*</span>
            </label>
            {/* fix MAJOR M9（gemini 第二十轮）：去掉 role="listbox"/option（需要复杂方向键管理才完整），
                改为 plain button list + Tab 导航；input 仍用 aria-autocomplete + aria-expanded
                让屏幕阅读器了解搜索状态。这样既符合 a11y，又不需要写键盘焦点管理。 */}
            <div className="relative">
              <Search size={14} className="absolute left-3 top-1/2 -translate-y-1/2 text-on-surface-variant" />
              <input
                ref={userInputRef}
                id="grant-target-user"
                type="text"
                value={userQuery}
                onChange={(e) => {
                  setUserQuery(e.target.value);
                  setSelectedUser(null);
                }}
                placeholder={t('ADMIN_GRANT.USER_SEARCH_PH', '输入用户名 / 手机号 / GitHub ID（≥2 字符）')}
                className="w-full bg-surface-container-high border border-outline-variant rounded-control pl-9 pr-3 py-2 text-sm focus:outline-none focus:border-primary"
                disabled={submitting}
                aria-autocomplete="list"
                aria-expanded={userSuggestions.length > 0 && !selectedUser}
                aria-controls="grant-user-suggestions"
              />
            </div>
            {/* 建议列表：plain button group，Tab 键即可遍历 */}
            {userSuggestions.length > 0 && !selectedUser && (
              <ul
                id="grant-user-suggestions"
                aria-label={t('ADMIN_GRANT.SUGGESTIONS_ARIA', '用户建议列表')}
                className="mt-1 bg-surface-container-high border border-outline-variant rounded-control max-h-44 overflow-y-auto"
              >
                {userSuggestions.map((u) => (
                  <li key={u.id}>
                    <button
                      type="button"
                      onClick={() => {
                        setSelectedUser({ id: u.id, username: u.username });
                        setUserQuery(u.username);
                        setUserSuggestions([]);
                      }}
                      className="w-full text-left px-3 py-2 hover:bg-surface-container text-sm border-b border-outline-variant/40 last:border-0"
                    >
                      <div className="text-on-surface">
                        {u.username} <span className="text-on-surface-variant text-xs">#{u.id}</span>
                      </div>
                      {u.phone && <div className="text-xs text-on-surface-variant font-mono">{u.phone}</div>}
                    </button>
                  </li>
                ))}
              </ul>
            )}
            {searchingUsers && (
              <div className="text-xs text-on-surface-variant mt-1">{t('ADMIN_GRANT.SEARCHING', '搜索中...')}</div>
            )}
            {selectedUser && (
              <div className="mt-1 text-xs text-success">
                {t('ADMIN_GRANT.SELECTED_USER', '已选择')}: {selectedUser.username} #{selectedUser.id}
              </div>
            )}
          </div>

          {/* 套餐选择 */}
          <div>
            <label htmlFor="grant-package" className="block text-sm text-on-surface mb-1">
              {t('ADMIN_GRANT.PACKAGE', '套餐')} <span className="text-error">*</span>
            </label>
            <select
              id="grant-package"
              value={selectedPackageId}
              onChange={(e) => setSelectedPackageId(e.target.value)}
              disabled={submitting || loadingPackages}
              className="w-full bg-surface-container-high border border-outline-variant rounded-control px-3 py-2 text-sm focus:outline-none focus:border-primary"
            >
              <option value="">{loadingPackages ? t('ADMIN_GRANT.LOADING_PKG', '加载中...') : t('ADMIN_GRANT.SELECT_PKG', '请选择')}</option>
              {packages.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name} - ${p.price_amount?.toFixed(2)}
                </option>
              ))}
            </select>
          </div>

          {/* 数量 */}
          <div>
            <label htmlFor="grant-quantity" className="block text-sm text-on-surface mb-1">
              {t('ADMIN_GRANT.QUANTITY', '数量')}
            </label>
            <input
              id="grant-quantity"
              type="number"
              min={1}
              max={100}
              step={1}
              value={quantity}
              onChange={(e) => setQuantity(e.target.value)}
              disabled={submitting}
              className="w-32 bg-surface-container-high border border-outline-variant rounded-control px-3 py-2 text-sm font-mono focus:outline-none focus:border-primary"
            />
            <span className="ml-3 text-xs text-on-surface-variant">{t('ADMIN_GRANT.QTY_HINT', '受套餐持有上限约束')}</span>
          </div>

          {/* Reason */}
          <div>
            <label htmlFor="grant-reason" className="block text-sm text-on-surface mb-1">
              {t('ADMIN_GRANT.REASON', '赠送理由')} <span className="text-error">*</span>
            </label>
            <textarea
              id="grant-reason"
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              disabled={submitting}
              rows={3}
              maxLength={500}
              placeholder={t('ADMIN_GRANT.REASON_PH', '客服补偿订单 #xxx / 活动赠送 / 撤销错误退款 ...')}
              className="w-full bg-surface-container-high border border-outline-variant rounded-control px-3 py-2 text-sm focus:outline-none focus:border-primary"
            />
            <div className="text-xs text-on-surface-variant text-right mt-0.5">
              {reason.length} / 500
            </div>
          </div>

          {/* 信息提示 */}
          <div className="text-xs text-on-surface-variant bg-surface-container-high rounded-control px-3 py-2">
            <div>{t('ADMIN_GRANT.INFO_LINE_1', '• 赠送的订阅会被标记 IsGranted=true，不可退款（防止白送钱）')}</div>
            <div>{t('ADMIN_GRANT.INFO_LINE_2', '• 用户会收到一条系统通知（强制送达，不被偏好屏蔽）')}</div>
            <div>{t('ADMIN_GRANT.INFO_LINE_3', '• 操作会记入审计日志（admin id + 理由）')}</div>
          </div>
        </div>

        {/* Footer */}
        <div className="flex items-center justify-end gap-2 px-5 py-3 border-t border-outline-variant bg-surface-container-high/50">
          <button
            type="button"
            onClick={onClose}
            disabled={submitting}
            className="px-4 py-1.5 text-sm rounded-control text-on-surface-variant hover:bg-surface-container disabled:opacity-50"
          >
            {t('COMMON.CANCEL', '取消')}
          </button>
          <button
            type="button"
            onClick={submit}
            disabled={submitting || !isFormValid}
            className="px-4 py-1.5 text-sm rounded-control bg-success text-white hover:bg-success disabled:opacity-50 disabled:cursor-not-allowed flex items-center gap-1.5"
            title={!isFormValid && !submitting ? t('ADMIN_GRANT.SUBMIT_DISABLED_HINT', '请先选择目标用户、套餐并填写理由') : undefined}
          >
            <Gift size={14} />
            {submitting ? t('ADMIN_GRANT.SUBMITTING', '提交中...') : t('ADMIN_GRANT.SUBMIT', '确认赠送')}
          </button>
        </div>
      </div>
    </div>
  );
};

export default AdminGrantSubscriptionModal;
