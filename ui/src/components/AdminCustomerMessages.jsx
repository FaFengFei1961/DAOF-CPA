import React, { useState, useEffect, useCallback, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { MessageSquare, RefreshCw, Send, ArrowLeft, CheckCircle2, ArrowDown } from 'lucide-react';
import toast from 'react-hot-toast';
import { useConfirm } from '../context/ConfirmContext';
import { authFetch } from '../utils/authFetch';
import { logger } from '../utils/logger';

const STATUS_OPTIONS = [
  { value: 'open', label: 'FILTER_OPEN' },
  { value: 'closed', label: 'FILTER_CLOSED' },
  { value: '', label: 'FILTER_ALL' },
];

// Admin ticket management, replacing the old single-message admin view.
const AdminCustomerMessages = () => {
  const { t } = useTranslation();
  const confirm = useConfirm();
  const [view, setView] = useState('list'); // list | detail
  const [list, setList] = useState([]);
  const [loading, setLoading] = useState(true);
  const [statusFilter, setStatusFilter] = useState('open');
  const [activeTicket, setActiveTicket] = useState(null);
  const [activeUser, setActiveUser] = useState('');
  const [activeMessages, setActiveMessages] = useState([]);
  const [reply, setReply] = useState('');
  const [submitting, setSubmitting] = useState(false);

  // Chat scroll behavior mirrors Tickets.jsx.
  const messagesContainerRef = useRef(null);
  const selfSendRef = useRef(false);
  const [showNewMsgBtn, setShowNewMsgBtn] = useState(false);

  const isNearBottom = (el, threshold = 80) => {
    if (!el) return true;
    return el.scrollHeight - el.scrollTop - el.clientHeight < threshold;
  };
  const scrollMessagesToBottom = (smooth = true) => {
    const el = messagesContainerRef.current;
    if (!el) return;
    el.scrollTo({ top: el.scrollHeight, behavior: smooth ? 'smooth' : 'auto' });
    setShowNewMsgBtn(false);
  };
  const handleMessagesScroll = () => {
    if (isNearBottom(messagesContainerRef.current)) setShowNewMsgBtn(false);
  };
  // Message count changes: own sends force-scroll, bottom users follow, history readers see the jump button.
  useEffect(() => {
    const el = messagesContainerRef.current;
    if (!el || activeMessages.length === 0) return;
    if (selfSendRef.current) {
      selfSendRef.current = false;
      requestAnimationFrame(() => scrollMessagesToBottom(true));
      return;
    }
    if (isNearBottom(el)) {
      requestAnimationFrame(() => scrollMessagesToBottom(true));
    } else {
      setShowNewMsgBtn(true);
    }
  }, [activeMessages.length]);
  // Entering or switching tickets scrolls to the latest message immediately.
  useEffect(() => {
    if (view !== 'detail' || !activeTicket) return;
    setShowNewMsgBtn(false);
    requestAnimationFrame(() => scrollMessagesToBottom(false));
  }, [view, activeTicket?.id]);

  const loadList = useCallback(async () => {
    setLoading(true);
    try {
      const params = new URLSearchParams({ page: '1', page_size: '100' });
      if (statusFilter) params.set('status', statusFilter);
      const json = await authFetch(`/api/admin/tickets?${params.toString()}`);
      if (json.success) setList(json.data || []);
    } catch (e) {
      logger.error('[AdminTickets] loadList', e);
      toast.error(t('SYSTEM.ERROR', '加载失败'));
    } finally {
      setLoading(false);
    }
  }, [statusFilter, t]);

  const loadDetail = useCallback(async (ticketId) => {
    try {
      const json = await authFetch(`/api/tickets/${ticketId}`);
      if (json.success && json.data) {
        setActiveTicket(json.data.ticket);
        setActiveUser(json.data.username || '');
        setActiveMessages(json.data.messages || []);
        await authFetch(`/api/tickets/${ticketId}/read`, { method: 'POST' });
      }
    } catch (e) {
      logger.error('[AdminTickets] loadDetail', e);
    }
  }, []);

  useEffect(() => { loadList(); }, [loadList]);

  const openTicket = async (id) => {
    setView('detail');
    await loadDetail(id);
  };

  const handleReply = async () => {
    const body = reply.trim();
    if (!body || !activeTicket) return;
    setSubmitting(true);
    const ticketId = activeTicket.id; // Lock the send target for this request.
    try {
      const json = await authFetch(`/api/tickets/${ticketId}/messages`, {
        method: 'POST',
        body: { body },
      });
      if (json.success) {
        setReply('');
        // Optimistically append the message returned by the backend, including id/created_at.
        if (json.data && activeTicket && activeTicket.id === ticketId) {
          selfSendRef.current = true;
          setActiveMessages(prev => [...prev, json.data]);
        }
      } else if (json.message_code === 'ERR_TICKET_CLOSED') {
        toast.error(t('TICKET.ADMIN.ALREADY_CLOSED', '工单已关闭'));
        await loadDetail(ticketId);
      } else {
        toast.error(json.message || t('TICKET.ADMIN.SEND_FAIL', '发送失败'));
      }
    } catch {
      toast.error(t('TICKET.ADMIN.SEND_FAIL', '发送失败'));
    } finally {
      setSubmitting(false);
    }
  };

  // Poll while the detail view is open so user replies appear in the admin panel.
  useEffect(() => {
    if (view !== 'detail' || !activeTicket || activeTicket.status === 'closed') return;
    const ticketId = activeTicket.id;
    const tick = async () => {
      if (document.hidden) return; // Skip background tabs to reduce backend load.
      try {
        const json = await authFetch(`/api/tickets/${ticketId}`);
        if (!json.success || !json.data) return;
        setActiveTicket(prev => (prev && prev.id === ticketId ? json.data.ticket : prev));
        setActiveMessages(prev => {
          const incoming = json.data.messages || [];
          const lastPrev = prev[prev.length - 1]?.id;
          const lastNew = incoming[incoming.length - 1]?.id;
          if (prev.length !== incoming.length || lastPrev !== lastNew) return incoming;
          return prev;
        });
      } catch { /* silent */ }
    };
    const timer = setInterval(tick, 5000);
    return () => clearInterval(timer);
  }, [view, activeTicket]);

  const handleClose = async () => {
    if (!activeTicket) return;
    const ok = await confirm({
      title: t('TICKET.ADMIN.CLOSE_TITLE', '关闭工单'),
      message: t('TICKET.ADMIN.CLOSE_CONFIRM', '确定关闭此工单？\n关闭后双方都无法继续发言。\n关闭超过 15 天自动清除。'),
      confirmText: t('TICKET.ADMIN.CLOSE_BTN', '关闭'),
      danger: true,
    });
    if (!ok) return;
    try {
      const json = await authFetch(`/api/tickets/${activeTicket.id}/close`, { method: 'POST' });
      if (json.success) {
        toast.success(t('TICKET.ADMIN.CLOSED', '已关闭'));
        await loadDetail(activeTicket.id);
        await loadList();
      } else {
        toast.error(json.message || t('TICKET.ADMIN.CLOSE_FAIL', '关闭失败'));
      }
    } catch {
      toast.error(t('TICKET.ADMIN.CLOSE_FAIL', '关闭失败'));
    }
  };

  // Detail view
  if (view === 'detail' && activeTicket) {
    const isClosed = activeTicket.status === 'closed';
    return (
      <div className="space-y-4">
        <button
          type="button"
          onClick={() => { setView('list'); setActiveTicket(null); setActiveMessages([]); loadList(); }}
          className="text-sm text-on-surface-variant hover:text-on-surface flex items-center gap-1"
        >
          <ArrowLeft size={14} /> {t('TICKET.ADMIN.BACK', '返回列表')}
        </button>

        <div className="bg-surface-container-high border border-outline-variant rounded-overlay overflow-hidden">
          <div className="px-4 sm:px-6 py-4 border-b border-outline-variant/40 flex items-center justify-between gap-3 flex-wrap">
            <div className="flex-1 min-w-0">
              <h2 className="text-base font-bold truncate">{activeTicket.subject}</h2>
              <div className="text-xs text-on-surface-variant mt-1 flex items-center gap-2 flex-wrap">
                <span className={`px-2 py-0.5 rounded-control font-mono ${isClosed
                  ? 'bg-on-surface/10 text-on-surface-variant'
                  : 'bg-success/15 text-success'}`}>
                  {isClosed ? t('TICKET.STATUS_CLOSED', '已关闭') : t('TICKET.STATUS_OPEN', '进行中')}
                </span>
                <span>#{activeTicket.id}</span>
                <span className="font-mono">@{activeUser || `user${activeTicket.user_id}`}</span>
                <span>{new Date(activeTicket.created_at).toLocaleString('zh-CN', { hour12: false })}</span>
              </div>
            </div>
            {!isClosed && (
              <button
                type="button"
                onClick={handleClose}
                className="h-8 px-3 text-xs border border-outline-variant rounded-control hover:bg-on-surface/[0.04] flex items-center gap-1"
              >
                <CheckCircle2 size={12} /> {t('TICKET.ADMIN.CLOSE_BTN', '关闭工单')}
              </button>
            )}
          </div>

          <div className="relative">
          <div
            ref={messagesContainerRef}
            onScroll={handleMessagesScroll}
            className="px-4 sm:px-6 py-5 space-y-3 max-h-[480px] overflow-y-auto bg-surface-container fl-scroll-chat"
          >
            {activeMessages.length === 0 ? (
              <div className="text-center text-sm text-on-surface-variant py-8">
                {t('TICKET.NO_MESSAGES', '暂无消息')}
              </div>
            ) : activeMessages.map(m => {
              const fromAdmin = m.sender === 'admin';
              return (
                <div key={m.id} className={`flex ${fromAdmin ? 'justify-end' : 'justify-start'}`}>
                  <div className={`max-w-[75%] rounded-overlay px-4 py-2.5 text-sm ${fromAdmin
                    ? 'bg-primary text-on-primary'
                    : 'bg-surface-container-high border border-outline-variant text-on-surface'}`}>
                    <div className="text-[10px] opacity-70 mb-1">
                      {fromAdmin ? t('TICKET.ADMIN.SENDER_ADMIN', '客服') : t('TICKET.ADMIN.SENDER_USER', '用户')} · {new Date(m.created_at).toLocaleString('zh-CN', { hour12: false })}
                    </div>
                    <div className="whitespace-pre-line break-words">{m.body}</div>
                  </div>
                </div>
              );
            })}
          </div>
          {/* Floating new-message button appears only when the admin is reading history. */}
          {showNewMsgBtn && (
            <button
              type="button"
              onClick={() => scrollMessagesToBottom(true)}
              className="absolute right-4 bottom-3 z-10 inline-flex items-center gap-1.5 h-8 px-3 rounded-full bg-primary text-on-primary text-xs font-semibold hover:brightness-110 active:scale-[0.97] transition animate-in fade-in slide-in-from-bottom-2"
              aria-label={t('TICKET.NEW_MSG_BELOW', '新消息')}
            >
              <ArrowDown size={12} />
              {t('TICKET.NEW_MSG_BELOW', '新消息')}
            </button>
          )}
          </div>

          {!isClosed ? (
            <div className="px-4 sm:px-6 py-4 border-t border-outline-variant/40 space-y-2">
              <textarea
                rows={3}
                value={reply}
                onChange={e => setReply(e.target.value)}
                maxLength={5000}
                placeholder={t('TICKET.ADMIN.REPLY_PLACEHOLDER', '输入回复内容（提交后会以站内通知形式发给用户）')}
                className="w-full bg-surface-container border border-outline rounded-control px-3 py-2 text-sm focus:border-primary outline-none resize-y"
              />
              <div className="flex items-center justify-between gap-3">
                <span className="text-[11px] text-on-surface-variant">{reply.length} / 5000</span>
                <button
                  type="button"
                  onClick={handleReply}
                  disabled={submitting || !reply.trim()}
                  className="h-9 px-4 bg-primary text-on-primary rounded-control text-sm font-semibold hover:opacity-90 disabled:opacity-50 flex items-center gap-2"
                >
                  <Send size={12} />
                  {submitting ? t('TICKET.ADMIN.SENDING', '发送中...') : t('TICKET.ADMIN.SEND', '回复')}
                </button>
              </div>
            </div>
          ) : (
            <div className="px-4 sm:px-6 py-3 border-t border-outline-variant/40 text-xs text-on-surface-variant text-center">
              {t('TICKET.ADMIN.CLOSED_HINT', '此工单已关闭')}
            </div>
          )}
        </div>
      </div>
    );
  }

  // List view
  return (
    <div className="space-y-4">
      <header className="flex items-center justify-between">
        <div className="flex items-center gap-3">
          <MessageSquare size={24} className="text-primary" />
          <h2 className="text-xl font-bold tracking-tight">
            {t('TICKET.ADMIN.TITLE', '工单管理')}
          </h2>
        </div>
        <div className="flex items-center gap-2">
          <select
            value={statusFilter}
            onChange={e => setStatusFilter(e.target.value)}
            className="h-9 bg-surface-container border border-outline-variant rounded-control px-3 text-sm"
          >
            {STATUS_OPTIONS.map(s => (
              <option key={s.value} value={s.value}>
                {s.label === 'FILTER_OPEN'
                  ? t('TICKET.ADMIN.FILTER_OPEN', '进行中')
                  : s.label === 'FILTER_CLOSED'
                    ? t('TICKET.ADMIN.FILTER_CLOSED', '已关闭')
                    : t('TICKET.ADMIN.FILTER_ALL', '全部')}
              </option>
            ))}
          </select>
          <button
            type="button"
            onClick={loadList}
            className="h-9 w-9 flex items-center justify-center rounded-control bg-surface-container hover:bg-on-surface/[0.04]"
          >
            <RefreshCw size={14} className={loading ? 'animate-spin' : ''} />
          </button>
        </div>
      </header>

      <section className="bg-surface-container-high border border-outline-variant rounded-overlay overflow-hidden">
        {list.length === 0 ? (
          <div className="text-center py-12 text-sm text-on-surface-variant">
            {t('TICKET.ADMIN.EMPTY', '暂无工单')}
          </div>
        ) : (
          <div className="divide-y divide-outline-variant">
            {list.map(({ ticket: it, username, unread_count: unread }) => {
              const isClosed = it.status === 'closed';
              return (
                <button
                  key={it.id}
                  type="button"
                  onClick={() => openTicket(it.id)}
                  className="w-full text-left px-4 py-3 flex items-start gap-3 hover:bg-surface-container"
                >
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-2 flex-wrap mb-1">
                      <span className="text-sm font-semibold truncate">{it.subject}</span>
                      <span className={`text-[10px] px-2 py-0.5 rounded-control font-mono ${isClosed
                        ? 'bg-on-surface/10 text-on-surface-variant'
                        : 'bg-success/15 text-success'}`}>
                        {isClosed
                          ? t('TICKET.STATUS_CLOSED', '已关闭')
                          : t('TICKET.STATUS_OPEN', '进行中')}
                      </span>
                      <span className="text-[10px] text-on-surface-variant font-mono">
                        @{username || `user${it.user_id}`}
                      </span>
                      {unread > 0 && (
                        <span className="text-[10px] px-2 py-0.5 rounded-full bg-error text-white font-mono">
                          {unread}
                        </span>
                      )}
                    </div>
                    <div className="text-[10px] text-outline mt-0.5">
                      {t('TICKET.ADMIN.LAST_MSG', '最后活动')}: {new Date(it.last_message_at).toLocaleString('zh-CN', { hour12: false })}
                    </div>
                  </div>
                </button>
              );
            })}
          </div>
        )}
      </section>
    </div>
  );
};

export default AdminCustomerMessages;
