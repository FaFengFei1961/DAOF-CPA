import React, { useState, useEffect, useRef, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { Gift, X, Search } from 'lucide-react';
import toast from 'react-hot-toast';
import { authFetch } from '../utils/authFetch';
import { useModalA11y } from '../hooks/useModalA11y';
import DurationInput from './DurationInput';

const MAX_GRANT_VALID_SECONDS = 158112000;

const adminGrantApiMessage = (code, t) => {
  switch (code) {
    case 'ERR_INVALID_VALID_SECONDS':
      return t('API.ERR_INVALID_VALID_SECONDS', '自定义有效期必须大于 0 秒。');
    case 'ERR_VALID_SECONDS_TOO_LARGE':
      return t('API.ERR_VALID_SECONDS_TOO_LARGE', '自定义有效期不能超过 5 年（158112000 秒）。');
    default:
      return null;
  }
};

/**
 * AdminGrantSubscriptionModal
 *
 * Admin dialog for granting subscriptions.
 * Form fields:
 *   - user picker, searchable by username / phone / OAuth external_id（任意 provider）
 *   - package picker from /api/admin/packages
 *   - quantity
 *   - optional custom validity seconds for compensation grants
 *   - required grant reason for audit
 *
 * Aligned with backend POST /api/admin/subscriptions/grant:
 *   { user_id, package_id, quantity, reason, valid_seconds? }
 *
 * Props:
 *   open        - whether the dialog is visible
 *   onClose     - close callback for cancel / ESC / backdrop / success
 *   onSuccess   - success callback so the parent can refresh
 *   prefillUser - optional preselected { id, username } from UserManagement
 */
const AdminGrantSubscriptionModal = ({ open, onClose, onSuccess, prefillUser = null }) => {
  const { t } = useTranslation();

  // User search state.
  const [userQuery, setUserQuery] = useState('');
  const [userSuggestions, setUserSuggestions] = useState([]);
  // 用户反馈"只能一个个赠送"——改成多用户支持。selectedUsers 是有序去重的 chip 列表，
  // 每个元素是 { id, username }。submit 时如果 ≥1 个就批量调 grant-batch endpoint。
  const [selectedUsers, setSelectedUsers] = useState([]);
  const [searchingUsers, setSearchingUsers] = useState(false);

  // Package selection.
  const [packages, setPackages] = useState([]);
  const [selectedPackageId, setSelectedPackageId] = useState('');
  const [loadingPackages, setLoadingPackages] = useState(false);

  // Form fields.
  const [quantity, setQuantity] = useState(1);
  const [reason, setReason] = useState('');
  const [customValidityOpen, setCustomValidityOpen] = useState(false);
  const [customValidSeconds, setCustomValidSeconds] = useState(0);
  const [submitting, setSubmitting] = useState(false);

  // a11y
  const userInputRef = useRef(null);
  const modalRef = useRef(null); // C5 round 20: focus trap scope.
  const initialFocusRef = prefillUser ? null : userInputRef;
  const { onBackdropClick } = useModalA11y(open, () => !submitting && onClose(), initialFocusRef, modalRef);

  // Initialize/reset each time open transitions to true.
  useEffect(() => {
    if (open) {
      setUserQuery('');
      setSelectedUsers(prefillUser ? [{ id: prefillUser.id, username: prefillUser.username }] : []);
      setUserSuggestions([]);
      setSelectedPackageId('');
      setQuantity(1);
      setReason('');
      setCustomValidityOpen(false);
      setCustomValidSeconds(0);
    }
  }, [open, prefillUser]);

  // Load the admin package list.
  useEffect(() => {
    if (!open) return;
    setLoadingPackages(true);
    authFetch('/api/admin/packages')
      .then((j) => {
        if (j?.success) {
          // Keep enabled packages only; Phase 8 leaves subscription packages only.
          setPackages((j.data || []).filter((p) => p.enabled !== false));
        } else {
          toast.error(j?.message || t('ADMIN_GRANT.LOAD_PKG_FAIL', '套餐列表加载失败'));
        }
      })
      .catch(() => toast.error(t('API.ERR_NETWORK', '网络异常')))
      .finally(() => setLoadingPackages(false));
  }, [open, t]);

  // Search users with a 300 ms debounce.
  // fix MAJOR M9 (gemini round 20): reqIdRef prevents slow searches from overwriting newer results.
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
      // Drop stale results when a newer search is already in flight.
      if (myReqId !== searchReqRef.current) return;
      if (j?.success) {
        // Only normal users can receive grants.
        setUserSuggestions((j.data || []).filter((u) => u.role === 'user'));
      }
    } catch {
      // Search failure should not hard-block the admin.
    } finally {
      if (myReqId === searchReqRef.current) setSearchingUsers(false);
    }
  }, []);

  useEffect(() => {
    if (!open) return;
    const trimmed = userQuery.trim();
    const handle = setTimeout(() => searchUsers(trimmed), 300);
    return () => clearTimeout(handle);
  }, [userQuery, open, searchUsers]);

  if (!open) return null;

  // Multi-user helpers.
  const addUser = (u) => {
    if (!u?.id) return;
    setSelectedUsers((prev) => (prev.some((x) => x.id === u.id) ? prev : [...prev, { id: u.id, username: u.username }]));
    setUserQuery('');
    setUserSuggestions([]);
  };
  const removeUser = (id) => {
    setSelectedUsers((prev) => prev.filter((u) => u.id !== id));
  };

  // Basic form validity for disabling submit and reducing invalid clicks.
  const customValidSecondsInt = Math.floor(Number(customValidSeconds) || 0);
  const hasCustomValidSeconds = customValidityOpen && customValidSecondsInt > 0;
  const isCustomValidityValid = !customValidityOpen ||
    (customValidSecondsInt > 0 && customValidSecondsInt <= MAX_GRANT_VALID_SECONDS);
  const isFormValid = selectedUsers.length > 0 && !!selectedPackageId && reason.trim().length > 0 && isCustomValidityValid;
  const submitDisabledHint = !isCustomValidityValid
    ? t('ADMIN_GRANT.ERR_VALID_SECONDS_INVALID', '启用自定义有效期后，请填写 1 秒到 5 年之间的时长')
    : t('ADMIN_GRANT.SUBMIT_DISABLED_HINT', '请先选择目标用户、套餐并填写理由');

  const submit = async () => {
    if (selectedUsers.length === 0) {
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
    if (customValidityOpen && customValidSecondsInt <= 0) {
      toast.error(t('ADMIN_GRANT.ERR_VALID_SECONDS_INVALID', '启用自定义有效期后，请填写 1 秒到 5 年之间的时长'));
      return;
    }
    if (customValidityOpen && customValidSecondsInt > MAX_GRANT_VALID_SECONDS) {
      toast.error(t('ADMIN_GRANT.ERR_VALID_SECONDS_TOO_LARGE', '自定义有效期不能超过 5 年（158112000 秒）'));
      return;
    }

    setSubmitting(true);
    try {
      // 始终走 grant-batch endpoint —— 单个用户也用同一条路径，少分支逻辑。
      // 后端会对 user_ids 去重 + 拒 admin 自己，并按 user 隔离失败。
      const payload = {
        user_ids: selectedUsers.map((u) => u.id),
        package_id: parseInt(selectedPackageId, 10),
        quantity: qty,
        reason: trimmedReason,
      };
      if (hasCustomValidSeconds) {
        payload.valid_seconds = customValidSecondsInt;
      }
      const json = await authFetch('/api/admin/subscriptions/grant-batch', {
        method: 'POST',
        body: payload,
      });
      if (!json?.success) {
        toast.error(adminGrantApiMessage(json?.message_code, t) || json?.message || t('ADMIN_GRANT.FAIL', '赠送失败'));
        return;
      }
      const okCount = json.data?.success_count || 0;
      const failCount = json.data?.failure_count || 0;
      const failures = Array.isArray(json.data?.failures) ? json.data.failures : [];
      if (okCount > 0 && failCount === 0) {
        toast.success(t('ADMIN_GRANT.SUCCESS_BATCH', '全部成功：{{n}} 个用户 × {{qty}} 份', { n: okCount, qty }));
        onSuccess?.();
        onClose();
        return;
      }
      if (okCount > 0 && failCount > 0) {
        // 部分失败：留住弹窗，详情列出来让 admin 知道哪些用户没成功。
        const tail = failures.slice(0, 5)
          .map((f) => `#${f.user_id} ${f.message_code || ''}`.trim())
          .join('；');
        toast.error(
          t('ADMIN_GRANT.PARTIAL', '成功 {{ok}}，失败 {{fail}}（{{detail}}{{more}}）', {
            ok: okCount,
            fail: failCount,
            detail: tail,
            more: failures.length > 5 ? '…' : '',
          }),
          { duration: 8000 },
        );
        onSuccess?.();
        return;
      }
      // 全部失败
      const firstFail = failures[0];
      toast.error(
        firstFail
          ? t('ADMIN_GRANT.ALL_FAIL_WITH_DETAIL', '全部失败：{{detail}}', { detail: `#${firstFail.user_id} ${firstFail.message || firstFail.message_code || ''}` })
          : t('ADMIN_GRANT.FAIL', '赠送失败'),
      );
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
          {/* User selection — 多用户 chip 列表 */}
          <div>
            <label htmlFor="grant-target-user" className="block text-sm text-on-surface mb-1">
              {t('ADMIN_GRANT.TARGET_USER', '目标用户')} <span className="text-error">*</span>
              <span className="ml-2 text-xs text-on-surface-variant font-normal">
                {t('ADMIN_GRANT.TARGET_USER_MULTI_HINT', '支持批量：可多次搜索添加')}
              </span>
            </label>
            <div className="relative">
              <Search size={14} className="absolute left-3 top-1/2 -translate-y-1/2 text-on-surface-variant" />
              <input
                ref={userInputRef}
                id="grant-target-user"
                type="text"
                value={userQuery}
                onChange={(e) => setUserQuery(e.target.value)}
                placeholder={t('ADMIN_GRANT.USER_SEARCH_PH', '输入用户名 / 手机号 / GitHub ID（≥2 字符）')}
                className="w-full bg-surface-container-high border border-outline-variant rounded-control pl-9 pr-3 py-2 text-sm focus:outline-none focus:border-primary"
                disabled={submitting}
                aria-autocomplete="list"
                aria-expanded={userSuggestions.length > 0}
                aria-controls="grant-user-suggestions"
              />
            </div>
            {/* Suggestions: 点一个加一个进 chip 列表 */}
            {userSuggestions.length > 0 && (
              <ul
                id="grant-user-suggestions"
                aria-label={t('ADMIN_GRANT.SUGGESTIONS_ARIA', '用户建议列表')}
                className="mt-1 bg-surface-container-high border border-outline-variant rounded-control max-h-44 overflow-y-auto"
              >
                {userSuggestions.map((u) => {
                  const already = selectedUsers.some((x) => x.id === u.id);
                  return (
                    <li key={u.id}>
                      <button
                        type="button"
                        onClick={() => !already && addUser(u)}
                        disabled={already}
                        className="w-full text-left px-3 py-2 hover:bg-surface-container text-sm border-b border-outline-variant/40 last:border-0 disabled:opacity-50 disabled:cursor-not-allowed"
                      >
                        <div className="text-on-surface flex items-center justify-between">
                          <span>
                            {u.username} <span className="text-on-surface-variant text-xs">#{u.id}</span>
                          </span>
                          {already && (
                            <span className="text-[10px] text-on-surface-variant">
                              {t('ADMIN_GRANT.ALREADY_SELECTED', '已添加')}
                            </span>
                          )}
                        </div>
                        {u.phone && <div className="text-xs text-on-surface-variant font-mono">{u.phone}</div>}
                      </button>
                    </li>
                  );
                })}
              </ul>
            )}
            {searchingUsers && (
              <div className="text-xs text-on-surface-variant mt-1">{t('ADMIN_GRANT.SEARCHING', '搜索中...')}</div>
            )}
            {/* 已选 chip 列表 */}
            {selectedUsers.length > 0 && (
              <div className="mt-2 flex flex-wrap gap-1.5">
                {selectedUsers.map((u) => (
                  <span
                    key={u.id}
                    className="inline-flex items-center gap-1.5 rounded-control bg-success/15 border border-success/30 text-success px-2 py-0.5 text-xs"
                  >
                    {u.username}
                    <span className="text-success/70 font-mono">#{u.id}</span>
                    <button
                      type="button"
                      onClick={() => removeUser(u.id)}
                      disabled={submitting}
                      className="text-success/70 hover:text-success disabled:opacity-50"
                      aria-label={t('ADMIN_GRANT.REMOVE_USER_ARIA', '移除 {{name}}', { name: u.username })}
                    >
                      <X size={11} />
                    </button>
                  </span>
                ))}
                <span className="text-[11px] text-on-surface-variant self-center ml-1">
                  {t('ADMIN_GRANT.SELECTED_COUNT', '共 {{n}} 个目标', { n: selectedUsers.length })}
                </span>
              </div>
            )}
          </div>

          {/* Package selection */}
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

          {/* Quantity */}
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

          {/* Optional custom validity */}
          <div className="border border-outline-variant rounded-control bg-surface-container-high/40 px-3 py-3 space-y-3">
            <div>
              <div className="text-sm text-on-surface">
                {t('ADMIN_GRANT.VALIDITY_TITLE', '赠送有效期')}
              </div>
              <div className="text-xs text-on-surface-variant mt-0.5">
                {t('ADMIN_GRANT.CUSTOM_VALIDITY_HELP', '留空 = 用套餐默认周期；填了 = 仅此次赠送生效')}
              </div>
            </div>
            <div className="grid grid-cols-2 gap-2">
              <button
                type="button"
                onClick={() => {
                  setCustomValidityOpen(false);
                  setCustomValidSeconds(0);
                }}
                disabled={submitting}
                className={`h-9 rounded-control border text-sm font-medium transition ${
                  !customValidityOpen
                    ? 'border-primary bg-primary/15 text-primary'
                    : 'border-outline-variant text-on-surface-variant hover:text-on-surface hover:bg-on-surface/[0.04]'
                }`}
              >
                {t('ADMIN_GRANT.VALIDITY_DEFAULT', '按套餐周期')}
              </button>
              <button
                type="button"
                onClick={() => {
                  setCustomValidityOpen(true);
                  if (customValidSecondsInt <= 0) setCustomValidSeconds(86400);
                }}
                disabled={submitting}
                className={`h-9 rounded-control border text-sm font-medium transition ${
                  customValidityOpen
                    ? 'border-primary bg-primary/15 text-primary'
                    : 'border-outline-variant text-on-surface-variant hover:text-on-surface hover:bg-on-surface/[0.04]'
                }`}
              >
                {t('ADMIN_GRANT.VALIDITY_CUSTOM', '自定义有效期')}
              </button>
            </div>
            {customValidityOpen && (
              <div id="grant-custom-validity-panel" className="space-y-2">
                <DurationInput
                  value={customValidSecondsInt}
                  onChange={setCustomValidSeconds}
                  className="w-full rounded-control bg-surface border border-outline-variant text-on-surface text-sm px-3 py-2 focus:outline-none focus:border-primary"
                  selectClass="rounded-control bg-surface border border-outline-variant text-on-surface text-sm px-2 py-2 focus:outline-none focus:border-primary"
                />
                <div className="flex items-center justify-between gap-3 text-xs text-on-surface-variant">
                  <span>{t('ADMIN_GRANT.CUSTOM_VALIDITY_DEFAULT_HINT', '默认按套餐周期；填了则按自定义')}</span>
                  <button
                    type="button"
                    onClick={() => {
                      setCustomValidityOpen(false);
                      setCustomValidSeconds(0);
                    }}
                    disabled={submitting}
                    className="text-primary hover:text-primary/80 disabled:opacity-50"
                  >
                    {t('ADMIN_GRANT.CUSTOM_VALIDITY_CLEAR', '清空')}
                  </button>
                </div>
                {!isCustomValidityValid && (
                  <div className="text-xs text-error" role="alert">
                    {t('ADMIN_GRANT.ERR_VALID_SECONDS_INVALID', '启用自定义有效期后，请填写 1 秒到 5 年之间的时长')}
                  </div>
                )}
              </div>
            )}
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

          {/* Info notice */}
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
            title={!isFormValid && !submitting ? submitDisabledHint : undefined}
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
