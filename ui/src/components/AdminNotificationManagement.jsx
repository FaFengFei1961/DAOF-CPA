import React, { useState, useEffect, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { Megaphone, Send, RefreshCw, AlertTriangle, Eye, Trash2 } from 'lucide-react';
import toast from 'react-hot-toast';
import { useConfirm } from '../context/ConfirmContext';
import { authFetch } from '../utils/authFetch';

const SEVERITY_OPTIONS = ['info', 'success', 'warning', 'error'];
const TARGET_MODES = ['all', 'package', 'user_ids'];

const AdminNotificationManagement = () => {
  const { t } = useTranslation();
  const confirm = useConfirm();

  // form state
  const [form, setForm] = useState({
    title: '',
    body: '',
    severity: 'info',
    action_url: '',
    action_text: '',
    target_mode: 'all',
    target_package_id: '',
    target_user_ids: '',
  });
  const [previewCount, setPreviewCount] = useState(null);
  const [previewing, setPreviewing] = useState(false);
  const [sending, setSending] = useState(false);

  // history state
  const [history, setHistory] = useState([]);
  const [historyLoading, setHistoryLoading] = useState(false);

  const buildTargetSpec = useCallback(() => {
    if (form.target_mode === 'package') {
      return { package_id: parseInt(form.target_package_id, 10) || 0 };
    }
    if (form.target_mode === 'user_ids') {
      const ids = form.target_user_ids.split(',')
        .map(s => parseInt(s.trim(), 10))
        .filter(n => !isNaN(n) && n > 0);
      return { user_ids: ids };
    }
    return {};
  }, [form]);

  const buildPreviewQuery = useCallback(() => {
    const params = new URLSearchParams({ mode: form.target_mode });
    if (form.target_mode === 'package' && form.target_package_id) {
      params.set('package_id', form.target_package_id);
    }
    if (form.target_mode === 'user_ids' && form.target_user_ids.trim()) {
      params.set('user_ids', form.target_user_ids.trim());
    }
    return params.toString();
  }, [form]);

  const loadHistory = useCallback(async () => {
    setHistoryLoading(true);
    try {
      const json = await authFetch('/api/admin/notifications/broadcasts?page=1&page_size=50');
      if (json.success) setHistory(json.data || []);
    } catch {
      toast.error(t('SYSTEM.ERROR', '加载失败'));
    } finally {
      setHistoryLoading(false);
    }
  }, [t]);

  useEffect(() => { loadHistory(); }, [loadHistory]);

  const handlePreview = async () => {
    setPreviewing(true);
    try {
      const json = await authFetch(`/api/admin/notifications/preview-targets?${buildPreviewQuery()}`);
      if (json.success && json.data) {
        setPreviewCount(json.data.count);
      } else {
        toast.error(json.message || t('SYSTEM.ERROR', '预览失败'));
        setPreviewCount(null);
      }
    } catch {
      toast.error(t('SYSTEM.ERROR', '预览失败'));
    } finally {
      setPreviewing(false);
    }
  };

  const handleSend = async () => {
    if (!form.title.trim()) {
      toast.error(t('NOTIF.ADMIN.FIELD_TITLE', '标题') + ' *');
      return;
    }
    const ok = await confirm({
      title: t('NOTIF.ADMIN.SEND', '发送'),
      message: form.title,
      confirmText: t('NOTIF.ADMIN.SEND', '发送'),
    });
    if (!ok) return;

    setSending(true);
    try {
      const json = await authFetch('/api/admin/notifications/broadcasts', {
        method: 'POST',
        body: {
          title: form.title,
          body: form.body,
          severity: form.severity,
          action_url: form.action_url,
          action_text: form.action_text,
          target_mode: form.target_mode,
          target_spec: buildTargetSpec(),
        },
      });
      if (json.success && json.data) {
        toast.success(
          t('NOTIF.ADMIN.SEND_OK', '群发已发送（实际触达 {{count}} 人）')
            .replace('{{count}}', json.data.recipient_count || 0)
        );
        setForm({ ...form, title: '', body: '', action_url: '', action_text: '' });
        setPreviewCount(null);
        loadHistory();
      } else {
        toast.error(json.message || t('NOTIF.ADMIN.SEND_FAIL', '发送失败'));
      }
    } catch {
      toast.error(t('NOTIF.ADMIN.SEND_FAIL', '发送失败'));
    } finally {
      setSending(false);
    }
  };

  const handleRevoke = async (b) => {
    const ok = await confirm({
      title: t('NOTIF.ADMIN.REVOKE', '撤回'),
      message: t('NOTIF.ADMIN.REVOKE_CONFIRM', '确认撤回？已发的通知不会从用户铃铛中删除。'),
      confirmText: t('NOTIF.ADMIN.REVOKE', '撤回'),
    });
    if (!ok) return;
    try {
      const json = await authFetch(`/api/admin/notifications/broadcasts/${b.id}/revoke`, { method: 'POST' });
      if (json.success) {
        toast.success(t('NOTIF.ADMIN.REVOKE_OK', '已撤回'));
        loadHistory();
      } else {
        toast.error(json.message || t('SYSTEM.ERROR', '操作失败'));
      }
    } catch {
      toast.error(t('SYSTEM.ERROR', '操作失败'));
    }
  };

  return (
    <div className="space-y-6">
      <div className="flex items-center gap-3">
        <Megaphone size={24} className="text-primary" />
        <h2 className="text-xl font-bold text-on-surface tracking-tight">
          {t('NOTIF.ADMIN.TAB', '通知管理')}
        </h2>
      </div>

      {/* 创建表单 */}
      <section className="bg-surface-container-high border border-outline-variant rounded-2xl p-6 space-y-4">
        <h3 className="text-base font-semibold text-on-surface flex items-center gap-2">
          <Send size={16} /> {t('NOTIF.ADMIN.CREATE_TITLE', '创建系统通知')}
        </h3>

        <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
          <div className="space-y-1.5 md:col-span-2">
            <label htmlFor="notif-admin-title" className="text-xs font-semibold text-on-surface-variant">
              {t('NOTIF.ADMIN.FIELD_TITLE', '标题')}
            </label>
            <input
              id="notif-admin-title"
              type="text"
              value={form.title}
              onChange={e => setForm({ ...form, title: e.target.value })}
              className="w-full h-10 bg-surface-container border border-outline rounded-lg px-3 text-sm text-on-surface focus:border-primary outline-none"
              maxLength={200}
            />
          </div>
          <div className="space-y-1.5 md:col-span-2">
            <label htmlFor="notif-admin-body" className="text-xs font-semibold text-on-surface-variant">
              {t('NOTIF.ADMIN.FIELD_BODY', '正文')}
            </label>
            <textarea
              id="notif-admin-body"
              rows={3}
              value={form.body}
              onChange={e => setForm({ ...form, body: e.target.value })}
              className="w-full bg-surface-container border border-outline rounded-lg px-3 py-2 text-sm text-on-surface focus:border-primary outline-none resize-none"
            />
          </div>
          <div className="space-y-1.5">
            <label htmlFor="notif-admin-severity" className="text-xs font-semibold text-on-surface-variant">
              {t('NOTIF.ADMIN.FIELD_SEVERITY', '严重级别')}
            </label>
            <select
              id="notif-admin-severity"
              value={form.severity}
              onChange={e => setForm({ ...form, severity: e.target.value })}
              className="w-full h-10 bg-surface-container border border-outline rounded-lg px-3 text-sm text-on-surface focus:border-primary outline-none"
            >
              {SEVERITY_OPTIONS.map(s => <option key={s} value={s}>{s}</option>)}
            </select>
          </div>
          <div className="space-y-1.5">
            <label htmlFor="notif-admin-target-mode" className="text-xs font-semibold text-on-surface-variant">
              {t('NOTIF.ADMIN.TARGET_LABEL', '发送对象')}
            </label>
            <select
              id="notif-admin-target-mode"
              value={form.target_mode}
              onChange={e => { setForm({ ...form, target_mode: e.target.value }); setPreviewCount(null); }}
              className="w-full h-10 bg-surface-container border border-outline rounded-lg px-3 text-sm text-on-surface focus:border-primary outline-none"
            >
              {TARGET_MODES.map(m => (
                <option key={m} value={m}>
                  {t(`NOTIF.ADMIN.TARGET_${m.toUpperCase()}`)}
                </option>
              ))}
            </select>
          </div>
          {form.target_mode === 'package' && (
            <div className="space-y-1.5 md:col-span-2">
              <label htmlFor="notif-admin-target-package" className="text-xs font-semibold text-on-surface-variant">
                {t('NOTIF.ADMIN.TARGET_PACKAGE_HINT', '套餐 ID')}
              </label>
              <input
                id="notif-admin-target-package"
                type="number"
                value={form.target_package_id}
                onChange={e => setForm({ ...form, target_package_id: e.target.value })}
                className="w-full h-10 bg-surface-container border border-outline rounded-lg px-3 text-sm text-on-surface focus:border-primary outline-none"
              />
            </div>
          )}
          {form.target_mode === 'user_ids' && (
            <div className="space-y-1.5 md:col-span-2">
              <label htmlFor="notif-admin-target-user-ids" className="text-xs font-semibold text-on-surface-variant">
                {t('NOTIF.ADMIN.TARGET_USER_IDS_HINT', '用逗号分隔，例：1,2,3')}
              </label>
              <input
                id="notif-admin-target-user-ids"
                type="text"
                value={form.target_user_ids}
                onChange={e => setForm({ ...form, target_user_ids: e.target.value })}
                className="w-full h-10 bg-surface-container border border-outline rounded-lg px-3 text-sm text-on-surface focus:border-primary outline-none font-mono"
                placeholder="1, 2, 3"
              />
            </div>
          )}
          <div className="space-y-1.5">
            <label htmlFor="notif-admin-action-url" className="text-xs font-semibold text-on-surface-variant">
              {t('NOTIF.ADMIN.FIELD_ACTION_URL', '跳转链接（可空）')}
            </label>
            <input
              id="notif-admin-action-url"
              type="text"
              value={form.action_url}
              onChange={e => setForm({ ...form, action_url: e.target.value })}
              className="w-full h-10 bg-surface-container border border-outline rounded-lg px-3 text-sm text-on-surface focus:border-primary outline-none"
              placeholder="/subscriptions"
            />
          </div>
          <div className="space-y-1.5">
            <label htmlFor="notif-admin-action-text" className="text-xs font-semibold text-on-surface-variant">
              {t('NOTIF.ADMIN.FIELD_ACTION_TEXT', '按钮文案（可空）')}
            </label>
            <input
              id="notif-admin-action-text"
              type="text"
              value={form.action_text}
              onChange={e => setForm({ ...form, action_text: e.target.value })}
              className="w-full h-10 bg-surface-container border border-outline rounded-lg px-3 text-sm text-on-surface focus:border-primary outline-none"
            />
          </div>
        </div>

        <div className="flex items-center gap-3 pt-2">
          <button
            type="button"
            onClick={handlePreview}
            disabled={previewing}
            className="h-10 px-4 bg-surface-container border border-outline-variant text-on-surface rounded-lg text-sm font-medium hover:bg-on-surface/[0.04] transition flex items-center gap-2"
          >
            <Eye size={14} />
            {previewing ? '...' : t('NOTIF.ADMIN.PREVIEW', '预览触达')}
          </button>
          {previewCount !== null && (
            <span className="text-sm text-primary font-semibold">
              {t('NOTIF.ADMIN.PREVIEW_COUNT', '预计触达 {{count}} 人').replace('{{count}}', previewCount)}
            </span>
          )}
          <div className="flex-1" />
          <button
            type="button"
            onClick={handleSend}
            disabled={sending || !form.title.trim()}
            className="h-10 px-6 bg-primary text-on-primary rounded-lg text-sm font-medium hover:opacity-90 disabled:opacity-50 transition flex items-center gap-2"
          >
            <Send size={14} />
            {sending ? '...' : t('NOTIF.ADMIN.SEND', '发送')}
          </button>
        </div>
      </section>

      {/* 历史列表 */}
      <section className="bg-surface-container-high border border-outline-variant rounded-2xl p-6">
        <div className="flex items-center justify-between mb-4">
          <h3 className="text-base font-semibold text-on-surface">
            {t('NOTIF.ADMIN.HISTORY_TITLE', '历史群发')}
          </h3>
          <button
            type="button"
            onClick={loadHistory}
            disabled={historyLoading}
            className="h-8 px-3 text-xs text-on-surface-variant hover:text-on-surface flex items-center gap-1"
          >
            <RefreshCw size={12} className={historyLoading ? 'animate-spin' : ''} />
            {historyLoading ? t('SYSTEM.LOADING', '加载中...') : t('SYSTEM.REFRESH', '刷新')}
          </button>
        </div>

        {history.length === 0 ? (
          <div className="text-center py-12 text-on-surface-variant text-sm">
            {t('NOTIF.ADMIN.EMPTY', '暂无群发记录')}
          </div>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead className="bg-surface-container text-xs uppercase font-mono tracking-wider text-on-surface-variant border-b border-outline-variant">
                <tr>
                  <th className="px-3 py-2 text-left">{t('NOTIF.ADMIN.TABLE_TITLE', '标题')}</th>
                  <th className="px-3 py-2 text-left">{t('NOTIF.ADMIN.TABLE_SEVERITY', '级别')}</th>
                  <th className="px-3 py-2 text-left">{t('NOTIF.ADMIN.TABLE_TARGET', '对象')}</th>
                  <th className="px-3 py-2 text-right">{t('NOTIF.ADMIN.TABLE_RECIPIENTS', '触达数')}</th>
                  <th className="px-3 py-2 text-right">{t('NOTIF.ADMIN.TABLE_READ_RATE', '已读率')}</th>
                  <th className="px-3 py-2 text-left">{t('NOTIF.ADMIN.TABLE_STATUS', '状态')}</th>
                  <th className="px-3 py-2 text-left">{t('NOTIF.ADMIN.TABLE_CREATED_AT', '创建时间')}</th>
                  <th className="px-3 py-2 text-right">{t('NOTIF.ADMIN.TABLE_OPS', '操作')}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-outline-variant">
                {history.map(b => (
                  <tr key={b.id} className="hover:bg-surface-container">
                    <td className="px-3 py-2 max-w-xs truncate" title={b.title}>{b.title}</td>
                    <td className="px-3 py-2">
                      <span className={`text-xs px-2 py-0.5 rounded ${severityClass(b.severity)}`}>
                        {b.severity}
                      </span>
                    </td>
                    <td className="px-3 py-2 text-xs text-on-surface-variant">{b.target_mode}</td>
                    <td className="px-3 py-2 text-right font-mono">{b.recipient_count}</td>
                    <td className="px-3 py-2 text-right font-mono">
                      {(b.read_rate * 100).toFixed(0)}%
                    </td>
                    <td className="px-3 py-2">
                      <span className={`text-xs ${statusClass(b.status)}`}>
                        {t(`NOTIF.ADMIN.STATUS_${b.status.toUpperCase()}`, b.status)}
                      </span>
                    </td>
                    <td className="px-3 py-2 text-xs text-on-surface-variant">
                      {new Date(b.created_at).toLocaleString('zh-CN', { hour12: false })}
                    </td>
                    <td className="px-3 py-2 text-right">
                      {b.status === 'sent' && (
                        <button
                          type="button"
                          onClick={() => handleRevoke(b)}
                          className="text-xs text-red-400 hover:text-red-300 inline-flex items-center gap-1"
                          title={t('NOTIF.ADMIN.REVOKE', '撤回')}
                        >
                          <Trash2 size={12} />
                          {t('NOTIF.ADMIN.REVOKE', '撤回')}
                        </button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      <div className="flex items-start gap-2 text-xs text-on-surface-variant px-1">
        <AlertTriangle size={12} className="mt-0.5 shrink-0" />
        <p>
          {t('NOTIF.ADMIN.HINT_FORCE_DELIVER', '系统通知（category=broadcast）默认强制送达，绕过用户偏好屏蔽。撤回仅改群发状态，不会从用户铃铛中删除已发的通知。')}
        </p>
      </div>
    </div>
  );
};

const severityClass = (s) => {
  switch (s) {
    case 'success': return 'bg-emerald-500/10 text-emerald-400';
    case 'warning': return 'bg-amber-500/10 text-amber-400';
    case 'error': return 'bg-red-500/10 text-red-400';
    default: return 'bg-blue-500/10 text-blue-400';
  }
};

const statusClass = (s) => {
  switch (s) {
    case 'sent': return 'text-emerald-400';
    case 'revoked': return 'text-on-surface-variant line-through';
    case 'draft': return 'text-amber-400';
    default: return 'text-on-surface-variant';
  }
};

export default AdminNotificationManagement;
