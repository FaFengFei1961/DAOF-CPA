import React, { useState, useEffect, useCallback, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { MessageSquare, Send, Plus, X, ArrowLeft, RefreshCw, CheckCircle2, ArrowDown } from 'lucide-react';
import toast from 'react-hot-toast';
import { useConfirm } from '../context/ConfirmContext';
import { authFetch, isLoggedIn, readAuthState } from '../utils/authFetch';
import { isPageCacheFresh, readPageCache, writePageCache } from '../utils/pageCache';
import { StorePage } from './store/StorePrimitives';





const TICKETS_CACHE_TTL_MS = 30000;
const getTicketsCacheKey = () => {
  const { isAdmin, userToken } = readAuthState();
  return `tickets:${isAdmin ? 'admin' : userToken || 'guest'}`;
};

const Tickets = () => {
  const { t } = useTranslation();
  const confirm = useConfirm();
  const [view, setView] = useState('list'); // list | create | detail
  const ticketsCacheKey = React.useMemo(getTicketsCacheKey, []);
  const [tickets, setTickets] = useState(() => readPageCache(ticketsCacheKey) || []);
  const [loading, setLoading] = useState(() => !readPageCache(ticketsCacheKey));
  const [activeTicket, setActiveTicket] = useState(null);
  const [activeMessages, setActiveMessages] = useState([]);
  const [composingSubject, setComposingSubject] = useState('');
  const [composingBody, setComposingBody] = useState('');
  const [replyBody, setReplyBody] = useState('');
  const [submitting, setSubmitting] = useState(false);



  const detailGenRef = useRef(0);

  const mountedRef = useRef(true);
  useEffect(() => {
    mountedRef.current = true;
    return () => { mountedRef.current = false; };
  }, []);





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


  useEffect(() => {
    if (view !== 'detail' || !activeTicket) return;
    setShowNewMsgBtn(false);
    requestAnimationFrame(() => scrollMessagesToBottom(false));
  }, [view, activeTicket?.id]);


  const handleMessagesScroll = () => {
    if (isNearBottom(messagesContainerRef.current)) setShowNewMsgBtn(false);
  };

  const loadList = useCallback(async ({ force = false } = {}) => {
    if (!isLoggedIn()) { if (mountedRef.current) setLoading(false); return; }
    const cached = readPageCache(ticketsCacheKey);
    if (cached) {
      if (mountedRef.current) {
        setTickets(cached);
        setLoading(false);
      }
      if (!force && isPageCacheFresh(ticketsCacheKey, TICKETS_CACHE_TTL_MS)) return;
    } else if (mountedRef.current) {
      setLoading(true);
    }
    try {
      const json = await authFetch('/api/tickets/mine?page=1&page_size=30');
      if (mountedRef.current && json.success) {
        const nextTickets = json.data || [];
        writePageCache(ticketsCacheKey, nextTickets);
        setTickets(nextTickets);
      }
    } catch {
      // List refresh is retried by the next explicit or automatic load.
    }
    finally {
      if (mountedRef.current) setLoading(false);
    }
  }, [ticketsCacheKey]);

  const loadDetail = useCallback(async (ticketId, gen) => {
    try {
      const json = await authFetch(`/api/tickets/${ticketId}`);

      if (gen !== detailGenRef.current || !mountedRef.current) return;
      if (json.success && json.data) {
        setActiveTicket(json.data.ticket);
        setActiveMessages(json.data.messages || []);

        await authFetch(`/api/tickets/${ticketId}/read`, { method: 'POST' });
        return;
      }

      toast.error(json.message || t('TICKETS.LOAD_FAIL', '加载失败'));
      setActiveTicket(null);
      setActiveMessages([]);
      setView('list');
    } catch {
      if (gen !== detailGenRef.current || !mountedRef.current) return;
      toast.error(t('TICKETS.LOAD_FAIL', '加载失败'));
      setActiveTicket(null);
      setActiveMessages([]);
      setView('list');
    }
  }, [t]);

  useEffect(() => { loadList(); }, [loadList]);

  const openTicket = async (ticketId) => {

    setActiveTicket(null);
    setActiveMessages([]);
    setView('detail');
    detailGenRef.current += 1;
    const myGen = detailGenRef.current;
    await loadDetail(ticketId, myGen);
  };

  const handleCreate = async () => {
    const sub = composingSubject.trim();
    const body = composingBody.trim();
    if (!sub || !body) {
      toast.error(t('TICKETS.FIELDS_REQUIRED', '请填写标题和内容'));
      return;
    }
    setSubmitting(true);
    try {
      const json = await authFetch('/api/tickets', {
        method: 'POST',
        body: { subject: sub, body },
      });
      if (json.success) {
        toast.success(t('TICKETS.CREATED', '工单已创建'));
        setComposingSubject(''); setComposingBody('');
        await loadList({ force: true });
        if (json.data?.id) await openTicket(json.data.id);
      } else if (json.message_code === 'ERR_TOO_MANY_MESSAGES') {
        toast.error(t('TICKETS.RATE_LIMIT', '操作过于频繁，每小时最多 10 条消息'));
      } else {
        toast.error(json.message || t('TICKETS.CREATE_FAIL', '创建失败'));
      }
    } catch {
      toast.error(t('TICKETS.CREATE_FAIL', '创建失败'));
    } finally {
      setSubmitting(false);
    }
  };

  const handleReply = async () => {
    const body = replyBody.trim();
    if (!body || !activeTicket) return;
    setSubmitting(true);
    const ticketId = activeTicket.id;
    try {
      const json = await authFetch(`/api/tickets/${ticketId}/messages`, {
        method: 'POST',
        body: { body },
      });
      if (json.success) {
        setReplyBody('');


        if (json.data && activeTicket && activeTicket.id === ticketId) {
          selfSendRef.current = true;
          setActiveMessages(prev => [...prev, json.data]);
        }
        return;
      }
      if (json.message_code === 'ERR_TICKET_CLOSED') {
        toast.error(t('TICKETS.ALREADY_CLOSED', '工单已关闭，无法继续发言'));

        detailGenRef.current += 1;
        await loadDetail(ticketId, detailGenRef.current);
      } else if (json.message_code === 'ERR_TOO_MANY_MESSAGES') {
        toast.error(t('TICKETS.RATE_LIMIT', '操作过于频繁，每小时最多 10 条消息'));
      } else {
        toast.error(json.message || t('TICKETS.SEND_FAIL', '发送失败'));
      }
    } catch {
      toast.error(t('TICKETS.SEND_FAIL', '发送失败'));
    } finally {
      setSubmitting(false);
    }
  };



  useEffect(() => {
    if (view !== 'detail' || !activeTicket || activeTicket.status === 'closed') return;
    const ticketId = activeTicket.id;
    const tick = async () => {
      if (!mountedRef.current || document.hidden) return;
      try {
        const json = await authFetch(`/api/tickets/${ticketId}`);
        if (!mountedRef.current || !json.success || !json.data) return;

        setActiveTicket(prev => (prev && prev.id === ticketId ? json.data.ticket : prev));
        setActiveMessages(prev => {
          const incoming = json.data.messages || [];

          const lastPrev = prev[prev.length - 1]?.id;
          const lastNew = incoming[incoming.length - 1]?.id;
          if (prev.length !== incoming.length || lastPrev !== lastNew) return incoming;
          return prev;
        });
      } catch {
        // Polling is best-effort; retry on the next interval.
      }
    };
    const timer = setInterval(tick, 5000);
    return () => clearInterval(timer);
  }, [view, activeTicket]);

  const handleClose = async () => {
    if (!activeTicket) return;
    const ok = await confirm({
      title: t('TICKETS.CLOSE_TITLE', '结束会话'),
      message: t('TICKETS.CLOSE_CONFIRM', '确定结束这次会话？\n\n关闭后双方都无法继续发言。\n关闭超过 15 天后系统会自动清除工单记录。'),
      confirmText: t('TICKETS.CLOSE_BTN', '结束会话'),
      danger: true,
    });
    if (!ok) return;
    try {
      const json = await authFetch(`/api/tickets/${activeTicket.id}/close`, { method: 'POST' });
      if (json.success) {
        toast.success(t('TICKETS.CLOSED', '会话已结束'));



        detailGenRef.current += 1;
        await loadDetail(activeTicket.id, detailGenRef.current);
        await loadList({ force: true });
      } else {
        toast.error(json.message || t('TICKETS.CLOSE_FAIL', '关闭失败'));
      }
    } catch {
      toast.error(t('TICKETS.CLOSE_FAIL', '关闭失败'));
    }
  };





  if (view === 'detail') {
    const isClosed = activeTicket?.status === 'closed';
    if (!activeTicket) {
      return (
        <div className="w-full max-w-3xl mx-auto px-4 md:px-8 py-6">
          <button
            type="button"
            onClick={() => { setView('list'); setActiveMessages([]); loadList({ force: true }); }}
            className="mb-4 text-sm text-on-surface-variant hover:text-on-surface flex items-center gap-1"
          >
            <ArrowLeft size={14} /> {t('TICKETS.BACK', '返回列表')}
          </button>
          <div className="fl-card p-12 text-center text-sm text-on-surface-variant">
            {t('COMMON.LOADING', '加载中…')}
          </div>
        </div>
      );
    }
    return (
      <div className="w-full max-w-3xl mx-auto px-4 md:px-8 py-6">
        <button
          type="button"
          onClick={() => { setView('list'); setActiveTicket(null); setActiveMessages([]); loadList({ force: true }); }}
          className="mb-4 text-sm text-on-surface-variant hover:text-on-surface flex items-center gap-1"
        >
          <ArrowLeft size={14} /> {t('TICKETS.BACK', '返回列表')}
        </button>

        <div className="fl-card overflow-hidden">
          <div className="px-4 sm:px-6 py-4 border-b border-outline-variant/40 flex items-center justify-between gap-3 flex-wrap">
            <div className="flex-1 min-w-0">
              <h2 className="text-base font-bold text-on-surface truncate">{activeTicket.subject}</h2>
              <div className="text-xs text-on-surface-variant mt-1 flex items-center gap-2 flex-wrap">
                <span className={`px-2 py-0.5 rounded-control font-mono ${isClosed
                  ? 'bg-on-surface/10 text-on-surface-variant'
                  : 'bg-success/15 text-success'}`}>
                  {isClosed ? t('TICKETS.STATUS_CLOSED', '已关闭') : t('TICKETS.STATUS_OPEN', '进行中')}
                </span>
                <span>#{activeTicket.id}</span>
                <span>{new Date(activeTicket.created_at).toLocaleString('zh-CN', { hour12: false })}</span>
              </div>
            </div>
            {!isClosed && (
              <button
                type="button"
                onClick={handleClose}
                className="h-8 px-3 text-xs border border-outline-variant rounded-control hover:bg-on-surface/[0.04] flex items-center gap-1"
              >
                <CheckCircle2 size={12} /> {t('TICKETS.CLOSE_BTN', '结束会话')}
              </button>
            )}
          </div>


          <div className="relative">
          <div
            ref={messagesContainerRef}
            onScroll={handleMessagesScroll}
            className="px-4 sm:px-6 py-5 space-y-3 max-h-[480px] overflow-y-auto fl-scroll-chat"
          >
            {activeMessages.length === 0 ? (
              <div className="text-center text-sm text-on-surface-variant py-8">
                {t('TICKETS.NO_MESSAGES', '暂无消息')}
              </div>
            ) : activeMessages.map(m => {
              const fromAdmin = m.sender === 'admin';
              return (
                <div key={m.id} className={`flex ${fromAdmin ? 'justify-start' : 'justify-end'}`}>
                  <div className={`max-w-[75%] rounded-overlay px-4 py-2.5 text-sm ${fromAdmin
                    ? 'bg-success/10 border border-success/20 text-on-surface'
                    : 'bg-primary text-on-primary'}`}>
                    <div className="text-[10px] opacity-70 mb-1">
                      {fromAdmin ? t('TICKETS.SENDER_ADMIN', '客服') : t('TICKETS.SENDER_USER', '我')} · {new Date(m.created_at).toLocaleString('zh-CN', { hour12: false })}
                    </div>
                    <div className="whitespace-pre-line break-words">{m.body}</div>
                  </div>
                </div>
              );
            })}
          </div>


          {showNewMsgBtn && (
            <button
              type="button"
              onClick={() => scrollMessagesToBottom(true)}
              className="absolute right-4 bottom-3 z-10 inline-flex items-center gap-1.5 h-8 px-3 rounded-full bg-primary text-on-primary text-xs font-semibold hover:brightness-110 active:scale-[0.97] transition animate-in fade-in slide-in-from-bottom-2"
              aria-label={t('TICKETS.NEW_MSG_BELOW', '新消息')}
            >
              <ArrowDown size={12} />
              {t('TICKETS.NEW_MSG_BELOW', '新消息')}
            </button>
          )}
          </div>


          {!isClosed ? (
            <div className="px-4 sm:px-6 py-4 border-t border-outline-variant/40 space-y-2">
              <textarea
                rows={3}
                value={replyBody}
                onChange={e => setReplyBody(e.target.value)}
                maxLength={5000}
                placeholder={t('TICKETS.REPLY_PLACEHOLDER', '输入回复内容')}
                className="w-full bg-surface-container-high border border-outline rounded-control px-3 py-2 text-sm text-on-surface focus:border-primary outline-none resize-y"
              />
              <div className="flex items-center justify-between gap-3">
                <span className="text-[11px] text-on-surface-variant">{replyBody.length} / 5000</span>
                <button
                  type="button"
                  onClick={handleReply}
                  disabled={submitting || !replyBody.trim()}
                  className="h-9 px-4 bg-primary text-on-primary rounded-control text-sm font-semibold hover:opacity-90 disabled:opacity-50 transition flex items-center gap-2"
                >
                  <Send size={12} />
                  {submitting ? t('TICKETS.SENDING', '发送中...') : t('TICKETS.SEND', '发送')}
                </button>
              </div>
            </div>
          ) : (
            <div className="px-4 sm:px-6 py-3 border-t border-outline-variant/40 text-xs text-on-surface-variant text-center">
              {t('TICKETS.CLOSED_HINT', '此工单已关闭，如需继续咨询请创建新工单')}
            </div>
          )}
        </div>
      </div>
    );
  }


  if (view === 'create') {
    return (
      <div className="w-full max-w-2xl mx-auto px-4 md:px-8 py-6">
        <button
          type="button"
          onClick={() => setView('list')}
          className="mb-4 text-sm text-on-surface-variant hover:text-on-surface flex items-center gap-1"
        >
          <ArrowLeft size={14} /> {t('TICKETS.BACK', '返回列表')}
        </button>

        <div className="fl-card p-6 space-y-4">
          <h2 className="text-lg font-bold flex items-center gap-2">
            <Plus size={18} /> {t('TICKETS.NEW', '创建工单')}
          </h2>
          <div className="space-y-1.5">
            <label htmlFor="ticket-compose-subject" className="text-xs font-semibold text-on-surface-variant">
              {t('TICKETS.SUBJECT', '标题')}
            </label>
            <input
              id="ticket-compose-subject"
              type="text"
              value={composingSubject}
              onChange={e => setComposingSubject(e.target.value)}
              maxLength={200}
              className="w-full h-10 bg-surface-container-high border border-outline rounded-control px-3 text-sm focus:border-primary outline-none"
              placeholder={t('TICKETS.SUBJECT_PLACEHOLDER', '一句话描述问题')}
            />
          </div>
          <div className="space-y-1.5">
            <label htmlFor="ticket-compose-body" className="text-xs font-semibold text-on-surface-variant">
              {t('TICKETS.BODY', '内容')}
            </label>
            <textarea
              id="ticket-compose-body"
              rows={6}
              value={composingBody}
              onChange={e => setComposingBody(e.target.value)}
              maxLength={5000}
              className="w-full bg-surface-container-high border border-outline rounded-control px-3 py-2 text-sm focus:border-primary outline-none resize-y"
              placeholder={t('TICKETS.BODY_PLACEHOLDER', '尽量提供订单号、报错截图描述等信息')}
            />
            <div className="text-[11px] text-on-surface-variant text-right">{composingBody.length} / 5000</div>
          </div>
          <button
            type="button"
            onClick={handleCreate}
            disabled={submitting || !composingSubject.trim() || !composingBody.trim()}
            className="h-10 px-5 bg-primary text-on-primary rounded-control text-sm font-semibold hover:opacity-90 disabled:opacity-50 flex items-center gap-2"
          >
            <Send size={14} />
            {submitting ? t('TICKETS.SUBMITTING', '提交中...') : t('TICKETS.SUBMIT', '提交工单')}
          </button>
        </div>
      </div>
    );
  }


  return (
    <div className="w-full max-w-4xl mx-auto px-4 md:px-8 py-6">
      <StorePage
        icon={MessageSquare}
        title={t('TICKETS.TITLE', '我的工单')}
        subtitle={t('TICKETS.SUBTITLE', '与客服双向沟通；任一方关闭后保留 15 天自动清除')}
        actions={
          <div className="flex gap-2">
            <button
              type="button"
              onClick={() => loadList({ force: true })}
              className="h-9 w-9 flex items-center justify-center rounded-control bg-surface-container hover:bg-on-surface/[0.04]"
              aria-label={t('COMMON.REFRESH', '刷新')}
            >
              <RefreshCw size={14} className={loading ? 'animate-spin' : ''} />
            </button>
            <button
              type="button"
              onClick={() => setView('create')}
              className="h-9 px-4 bg-primary text-on-primary rounded-control text-sm font-semibold hover:brightness-110 active:scale-[0.98] flex items-center gap-1.5 transition"
            >
              <Plus size={14} /> {t('TICKETS.NEW_BTN', '新工单')}
            </button>
          </div>
        }
      >
      {loading ? (
        <div className="text-center py-20 text-on-surface-variant">{t('COMMON.LOADING', '加载中…')}</div>
      ) : tickets.length === 0 ? (
        <div className="fl-card p-16 text-center">
          <MessageSquare size={36} className="mx-auto mb-3 text-on-surface-variant/50" />
          <p className="text-on-surface font-semibold mb-1">{t('TICKETS.EMPTY_TITLE', '暂无工单')}</p>
          <p className="text-on-surface-variant text-sm mb-4">{t('TICKETS.EMPTY_DESC', '有任何问题可以随时提交工单')}</p>
          <button
            type="button"
            onClick={() => setView('create')}
            className="h-9 px-4 bg-primary text-on-primary rounded-control text-sm font-semibold hover:brightness-110 active:scale-[0.98] transition"
          >
            {t('TICKETS.CONTACT_SUPPORT', '提交工单')}
          </button>
        </div>
      ) : (
        <div className="space-y-2">
          {tickets.map(({ ticket: t2, unread_count: unread }) => {
            const isClosed = t2.status === 'closed';
            return (
              <button
                key={t2.id}
                type="button"
                onClick={() => openTicket(t2.id)}
                className="fl-card group w-full text-left p-4 flex items-start gap-3"
              >
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-2 flex-wrap mb-1">
                    <span className="font-semibold text-sm truncate">{t2.subject}</span>
                    <span className={`text-[10px] px-2 py-0.5 rounded-control font-mono ${isClosed
                      ? 'bg-on-surface/10 text-on-surface-variant'
                      : 'bg-success/15 text-success'}`}>
                      {isClosed ? t('TICKETS.STATUS_CLOSED', '已关闭') : t('TICKETS.STATUS_OPEN', '进行中')}
                    </span>
                    {unread > 0 && (
                      <span className="text-[10px] px-2 py-0.5 rounded-full bg-error text-white font-mono">
                        {unread}
                      </span>
                    )}
                  </div>
                  <div className="text-[11px] text-on-surface-variant">
                    {t('TICKETS.LAST_MSG', '最后活动')}: {new Date(t2.last_message_at).toLocaleString('zh-CN', { hour12: false })}
                  </div>
                </div>
              </button>
            );
          })}
        </div>
      )}
      </StorePage>
    </div>
  );
};

export default Tickets;
