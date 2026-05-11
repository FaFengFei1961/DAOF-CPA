import React, { useState, useEffect, useCallback, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { MessageSquare, Send, Plus, X, ArrowLeft, RefreshCw, CheckCircle2, ArrowDown } from 'lucide-react';
import toast from 'react-hot-toast';
import { useConfirm } from '../context/ConfirmContext';
import { authFetch, isLoggedIn } from '../utils/authFetch';
import { StorePage } from './store/StorePrimitives';

// 用户工单页（独立 sidebar tab）
// - 列表：所有工单（按 last_message_at 倒序），含未读徽章
// - 进入工单：消息流（user/admin 气泡）+ 输入框 + "结束会话"按钮
// - 创建：输入 subject + 第一条消息
const Tickets = () => {
  const { t } = useTranslation();
  const confirm = useConfirm();
  const [view, setView] = useState('list'); // list | create | detail
  const [tickets, setTickets] = useState([]);
  const [loading, setLoading] = useState(true);
  const [activeTicket, setActiveTicket] = useState(null);
  const [activeMessages, setActiveMessages] = useState([]);
  const [composingSubject, setComposingSubject] = useState('');
  const [composingBody, setComposingBody] = useState('');
  const [replyBody, setReplyBody] = useState('');
  const [submitting, setSubmitting] = useState(false);

  // generation counter：防快速切换工单时旧请求覆盖新状态。
  // 每次 openTicket 自增，loadDetail 检查归属再 setState。
  const detailGenRef = useRef(0);
  // 组件卸载标记：避免对已卸载组件 setState
  const mountedRef = useRef(true);
  useEffect(() => {
    mountedRef.current = true;
    return () => { mountedRef.current = false; };
  }, []);

  // 聊天滚动 UX（参照微信）：
  // - selfSendRef：标记最近一次 messages 增加是不是用户主动发送
  // - showNewMsgBtn：右下角浮动"新消息 ↓"，仅在用户上滑阅读历史时收到对方消息才显示
  // - messagesContainerRef：消息容器 DOM，用来读取/设置 scrollTop
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

  // 消息数变化时分支决策：自己发的强制滚 / 在底部跟随 / 在历史区显示按钮
  useEffect(() => {
    const el = messagesContainerRef.current;
    if (!el || activeMessages.length === 0) return;
    if (selfSendRef.current) {
      selfSendRef.current = false;
      // 自己发的：强制滚到自己刚发那条（即底部）
      requestAnimationFrame(() => scrollMessagesToBottom(true));
      return;
    }
    if (isNearBottom(el)) {
      // 用户已经在底部，自然跟随对方新消息
      requestAnimationFrame(() => scrollMessagesToBottom(true));
    } else {
      // 用户在阅读历史，不打扰；右下角浮按钮提示
      setShowNewMsgBtn(true);
    }
  }, [activeMessages.length]);

  // 进入工单 / 切换工单：立即滚到底（不带平滑动画，避免观感跳变）
  useEffect(() => {
    if (view !== 'detail' || !activeTicket) return;
    setShowNewMsgBtn(false);
    requestAnimationFrame(() => scrollMessagesToBottom(false));
  }, [view, activeTicket?.id]);

  // 用户手动滚到底时清掉"新消息"按钮
  const handleMessagesScroll = () => {
    if (isNearBottom(messagesContainerRef.current)) setShowNewMsgBtn(false);
  };

  const loadList = useCallback(async () => {
    if (!isLoggedIn()) { if (mountedRef.current) setLoading(false); return; }
    if (mountedRef.current) setLoading(true);
    try {
      const json = await authFetch('/api/tickets/mine?page=1&page_size=30');
      if (mountedRef.current && json.success) setTickets(json.data || []);
    } catch { /* 静默：toast 在父级或下一次刷新时表达 */ }
    finally {
      if (mountedRef.current) setLoading(false);
    }
  }, []);

  const loadDetail = useCallback(async (ticketId, gen) => {
    try {
      const json = await authFetch(`/api/tickets/${ticketId}`);
      // 快速切换 / 卸载场景下丢弃过期响应
      if (gen !== detailGenRef.current || !mountedRef.current) return;
      if (json.success && json.data) {
        setActiveTicket(json.data.ticket);
        setActiveMessages(json.data.messages || []);
        // 标记已读（不影响 gen 失效逻辑）
        await authFetch(`/api/tickets/${ticketId}/read`, { method: 'POST' });
        return;
      }
      // 失败时 toast + 自动退回列表（否则会卡在 loading skeleton）
      toast.error(json.message || t('TICKET.LOAD_FAIL', '加载失败'));
      setActiveTicket(null);
      setActiveMessages([]);
      setView('list');
    } catch {
      if (gen !== detailGenRef.current || !mountedRef.current) return;
      toast.error(t('TICKET.LOAD_FAIL', '加载失败'));
      setActiveTicket(null);
      setActiveMessages([]);
      setView('list');
    }
  }, [t]);

  useEffect(() => { loadList(); }, [loadList]);

  const openTicket = async (ticketId) => {
    // 切到详情前先清空旧 ticket，避免渲染错误数据
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
      toast.error(t('TICKET.FIELDS_REQUIRED', '请填写标题和内容'));
      return;
    }
    setSubmitting(true);
    try {
      const json = await authFetch('/api/tickets', {
        method: 'POST',
        body: { subject: sub, body },
      });
      if (json.success) {
        toast.success(t('TICKET.CREATED', '工单已创建'));
        setComposingSubject(''); setComposingBody('');
        await loadList();
        if (json.data?.id) await openTicket(json.data.id);
      } else if (json.message_code === 'ERR_TOO_MANY_MESSAGES') {
        toast.error(t('TICKET.RATE_LIMIT', '操作过于频繁，每小时最多 10 条消息'));
      } else {
        toast.error(json.message || t('TICKET.CREATE_FAIL', '创建失败'));
      }
    } catch {
      toast.error(t('TICKET.CREATE_FAIL', '创建失败'));
    } finally {
      setSubmitting(false);
    }
  };

  const handleReply = async () => {
    const body = replyBody.trim();
    if (!body || !activeTicket) return;
    setSubmitting(true);
    const ticketId = activeTicket.id; // 锁定本次发送目标，防中途切换
    try {
      const json = await authFetch(`/api/tickets/${ticketId}/messages`, {
        method: 'POST',
        body: { body },
      });
      if (json.success) {
        setReplyBody('');
        // 乐观追加：把后端返回的新消息（含 id/created_at）直接 append 到列表
        // 比 await loadDetail 快一倍，且避免 detailGenRef 旧值导致丢更新
        if (json.data && activeTicket && activeTicket.id === ticketId) {
          selfSendRef.current = true; // 标记：滚动 useEffect 会强制滚到底
          setActiveMessages(prev => [...prev, json.data]);
        }
        return;
      }
      if (json.message_code === 'ERR_TICKET_CLOSED') {
        toast.error(t('TICKET.ALREADY_CLOSED', '工单已关闭，无法继续发言'));
        // 重新拉详情以同步关闭状态——主动+1 gen 让响应被采用
        detailGenRef.current += 1;
        await loadDetail(ticketId, detailGenRef.current);
      } else if (json.message_code === 'ERR_TOO_MANY_MESSAGES') {
        toast.error(t('TICKET.RATE_LIMIT', '操作过于频繁，每小时最多 10 条消息'));
      } else {
        toast.error(json.message || t('TICKET.SEND_FAIL', '发送失败'));
      }
    } catch {
      toast.error(t('TICKET.SEND_FAIL', '发送失败'));
    } finally {
      setSubmitting(false);
    }
  };

  // 详情视图打开时启动轻量轮询：每 5 秒拉一次最新消息，让对方（admin）回复能自动出现
  // 关闭视图 / unmount 时立即停。轮询本身用 silentRefreshDetail，避免动 detailGenRef 干扰主路径。
  useEffect(() => {
    if (view !== 'detail' || !activeTicket || activeTicket.status === 'closed') return;
    const ticketId = activeTicket.id;
    const tick = async () => {
      if (!mountedRef.current || document.hidden) return;
      try {
        const json = await authFetch(`/api/tickets/${ticketId}`);
        if (!mountedRef.current || !json.success || !json.data) return;
        // 仅在仍在同一工单详情时刷新
        setActiveTicket(prev => (prev && prev.id === ticketId ? json.data.ticket : prev));
        setActiveMessages(prev => {
          const incoming = json.data.messages || [];
          // 长度变化或最后消息 id 不同时才覆盖（减少不必要 re-render）
          const lastPrev = prev[prev.length - 1]?.id;
          const lastNew = incoming[incoming.length - 1]?.id;
          if (prev.length !== incoming.length || lastPrev !== lastNew) return incoming;
          return prev;
        });
      } catch { /* 静默：下一轮再试 */ }
    };
    const timer = setInterval(tick, 5000);
    return () => clearInterval(timer);
  }, [view, activeTicket]);

  const handleClose = async () => {
    if (!activeTicket) return;
    const ok = await confirm({
      title: t('TICKET.CLOSE_TITLE', '结束会话'),
      message: t('TICKET.CLOSE_CONFIRM', '确定结束这次会话？\n\n关闭后双方都无法继续发言。\n关闭超过 15 天后系统会自动清除工单记录。'),
      confirmText: t('TICKET.CLOSE_BTN', '结束会话'),
      danger: true,
    });
    if (!ok) return;
    try {
      const json = await authFetch(`/api/tickets/${activeTicket.id}/close`, { method: 'POST' });
      if (json.success) {
        toast.success(t('TICKET.CLOSED', '会话已结束'));
        // fix TS-H1: 必须传 gen 参数；loadDetail 内部 `if (gen !== detailGenRef.current) return`
        // 没传 gen 则 undefined !== <number> 永远 true，整个 setActiveTicket 被跳过 →
        // 关闭工单后 UI 不刷新，"结束会话" 按钮残留直到手动刷新页面。
        detailGenRef.current += 1;
        await loadDetail(activeTicket.id, detailGenRef.current);
        await loadList();
      } else {
        toast.error(json.message || t('TICKET.CLOSE_FAIL', '关闭失败'));
      }
    } catch {
      toast.error(t('TICKET.CLOSE_FAIL', '关闭失败'));
    }
  };

  // ── 渲染：详情 ──────────────────────────────────────────
  // 关键：只看 view，不再用 activeTicket 双重保护——原本 setView('detail') 后 activeTicket 还是 null，
  // 第一帧会 fallback 回列表 → 用户看到列表"闪一下"以为没反应；待 loadDetail 完成才进详情。
  // 现在改成：view === 'detail' 立即进入详情视图，activeTicket 未到时显示加载占位。
  if (view === 'detail') {
    const isClosed = activeTicket?.status === 'closed';
    if (!activeTicket) {
      return (
        <div className="w-full max-w-3xl mx-auto px-4 md:px-8 py-6">
          <button
            type="button"
            onClick={() => { setView('list'); setActiveMessages([]); loadList(); }}
            className="mb-4 text-sm text-on-surface-variant hover:text-on-surface flex items-center gap-1"
          >
            <ArrowLeft size={14} /> {t('TICKET.BACK', '返回列表')}
          </button>
          <div className="fl-card p-12 text-center text-sm text-on-surface-variant">
            {t('SYSTEM.LOADING', '加载中...')}
          </div>
        </div>
      );
    }
    return (
      <div className="w-full max-w-3xl mx-auto px-4 md:px-8 py-6">
        <button
          type="button"
          onClick={() => { setView('list'); setActiveTicket(null); setActiveMessages([]); loadList(); }}
          className="mb-4 text-sm text-on-surface-variant hover:text-on-surface flex items-center gap-1"
        >
          <ArrowLeft size={14} /> {t('TICKET.BACK', '返回列表')}
        </button>

        <div className="fl-card overflow-hidden">
          <div className="px-4 sm:px-6 py-4 border-b border-outline-variant/40 flex items-center justify-between gap-3 flex-wrap">
            <div className="flex-1 min-w-0">
              <h2 className="text-base font-bold text-on-surface truncate">{activeTicket.subject}</h2>
              <div className="text-xs text-on-surface-variant mt-1 flex items-center gap-2 flex-wrap">
                <span className={`px-2 py-0.5 rounded font-mono ${isClosed
                  ? 'bg-on-surface/10 text-on-surface-variant'
                  : 'bg-emerald-500/15 text-emerald-400'}`}>
                  {isClosed ? t('TICKET.STATUS_CLOSED', '已关闭') : t('TICKET.STATUS_OPEN', '进行中')}
                </span>
                <span>#{activeTicket.id}</span>
                <span>{new Date(activeTicket.created_at).toLocaleString('zh-CN', { hour12: false })}</span>
              </div>
            </div>
            {!isClosed && (
              <button
                type="button"
                onClick={handleClose}
                className="h-8 px-3 text-xs border border-outline-variant rounded-lg hover:bg-on-surface/[0.04] flex items-center gap-1"
              >
                <CheckCircle2 size={12} /> {t('TICKET.CLOSE_BTN', '结束会话')}
              </button>
            )}
          </div>

          {/* 消息流（relative 给"新消息↓"浮动按钮做定位锚点） */}
          <div className="relative">
          <div
            ref={messagesContainerRef}
            onScroll={handleMessagesScroll}
            className="px-4 sm:px-6 py-5 space-y-3 max-h-[480px] overflow-y-auto fl-scroll-chat"
          >
            {activeMessages.length === 0 ? (
              <div className="text-center text-sm text-on-surface-variant py-8">
                {t('TICKET.NO_MESSAGES', '暂无消息')}
              </div>
            ) : activeMessages.map(m => {
              const fromAdmin = m.sender === 'admin';
              return (
                <div key={m.id} className={`flex ${fromAdmin ? 'justify-start' : 'justify-end'}`}>
                  <div className={`max-w-[75%] rounded-2xl px-4 py-2.5 text-sm ${fromAdmin
                    ? 'bg-emerald-500/10 border border-emerald-500/20 text-on-surface'
                    : 'bg-primary text-on-primary'}`}>
                    <div className="text-[10px] opacity-70 mb-1">
                      {fromAdmin ? t('TICKET.SENDER_ADMIN', '客服') : t('TICKET.SENDER_USER', '我')} · {new Date(m.created_at).toLocaleString('zh-CN', { hour12: false })}
                    </div>
                    <div className="whitespace-pre-line break-words">{m.body}</div>
                  </div>
                </div>
              );
            })}
          </div>

          {/* "新消息 ↓" 浮动按钮：仅当用户上滑阅读历史时收到新消息才显示 */}
          {showNewMsgBtn && (
            <button
              type="button"
              onClick={() => scrollMessagesToBottom(true)}
              className="absolute right-4 bottom-3 z-10 inline-flex items-center gap-1.5 h-8 px-3 rounded-full bg-primary text-on-primary text-xs font-semibold shadow-lg hover:brightness-110 active:scale-[0.97] transition animate-in fade-in slide-in-from-bottom-2"
              aria-label={t('TICKET.NEW_MSG_BELOW', '新消息')}
            >
              <ArrowDown size={12} />
              {t('TICKET.NEW_MSG_BELOW', '新消息')}
            </button>
          )}
          </div>

          {/* 输入区（仅 open 状态） */}
          {!isClosed ? (
            <div className="px-4 sm:px-6 py-4 border-t border-outline-variant/40 space-y-2">
              <textarea
                rows={3}
                value={replyBody}
                onChange={e => setReplyBody(e.target.value)}
                maxLength={5000}
                placeholder={t('TICKET.REPLY_PLACEHOLDER', '输入回复内容')}
                className="w-full bg-surface-container-high border border-outline rounded-lg px-3 py-2 text-sm text-on-surface focus:border-primary outline-none resize-y"
              />
              <div className="flex items-center justify-between gap-3">
                <span className="text-[11px] text-on-surface-variant">{replyBody.length} / 5000</span>
                <button
                  type="button"
                  onClick={handleReply}
                  disabled={submitting || !replyBody.trim()}
                  className="h-9 px-4 bg-primary text-on-primary rounded-lg text-sm font-semibold hover:opacity-90 disabled:opacity-50 transition flex items-center gap-2"
                >
                  <Send size={12} />
                  {submitting ? t('TICKET.SENDING', '发送中...') : t('TICKET.SEND', '发送')}
                </button>
              </div>
            </div>
          ) : (
            <div className="px-4 sm:px-6 py-3 border-t border-outline-variant/40 text-xs text-on-surface-variant text-center">
              {t('TICKET.CLOSED_HINT', '此工单已关闭，如需继续咨询请创建新工单')}
            </div>
          )}
        </div>
      </div>
    );
  }

  // ── 渲染：创建 ──────────────────────────────────────────
  if (view === 'create') {
    return (
      <div className="w-full max-w-2xl mx-auto px-4 md:px-8 py-6">
        <button
          type="button"
          onClick={() => setView('list')}
          className="mb-4 text-sm text-on-surface-variant hover:text-on-surface flex items-center gap-1"
        >
          <ArrowLeft size={14} /> {t('TICKET.BACK', '返回列表')}
        </button>

        <div className="fl-card p-6 space-y-4">
          <h2 className="text-lg font-bold flex items-center gap-2">
            <Plus size={18} /> {t('TICKET.NEW', '创建工单')}
          </h2>
          <div className="space-y-1.5">
            <label htmlFor="ticket-compose-subject" className="text-xs font-semibold text-on-surface-variant">
              {t('TICKET.SUBJECT', '标题')}
            </label>
            <input
              id="ticket-compose-subject"
              type="text"
              value={composingSubject}
              onChange={e => setComposingSubject(e.target.value)}
              maxLength={200}
              className="w-full h-10 bg-surface-container-high border border-outline rounded-lg px-3 text-sm focus:border-primary outline-none"
              placeholder={t('TICKET.SUBJECT_PLACEHOLDER', '一句话描述问题')}
            />
          </div>
          <div className="space-y-1.5">
            <label htmlFor="ticket-compose-body" className="text-xs font-semibold text-on-surface-variant">
              {t('TICKET.BODY', '内容')}
            </label>
            <textarea
              id="ticket-compose-body"
              rows={6}
              value={composingBody}
              onChange={e => setComposingBody(e.target.value)}
              maxLength={5000}
              className="w-full bg-surface-container-high border border-outline rounded-lg px-3 py-2 text-sm focus:border-primary outline-none resize-y"
              placeholder={t('TICKET.BODY_PLACEHOLDER', '尽量提供订单号、报错截图描述等信息')}
            />
            <div className="text-[11px] text-on-surface-variant text-right">{composingBody.length} / 5000</div>
          </div>
          <button
            type="button"
            onClick={handleCreate}
            disabled={submitting || !composingSubject.trim() || !composingBody.trim()}
            className="h-10 px-5 bg-primary text-on-primary rounded-lg text-sm font-semibold hover:opacity-90 disabled:opacity-50 flex items-center gap-2"
          >
            <Send size={14} />
            {submitting ? t('TICKET.SUBMITTING', '提交中...') : t('TICKET.SUBMIT', '提交工单')}
          </button>
        </div>
      </div>
    );
  }

  // ── 渲染：列表 ──────────────────────────────────────────
  return (
    <div className="w-full max-w-4xl mx-auto px-4 md:px-8 py-6">
      <StorePage
        icon={MessageSquare}
        title={t('TICKET.TITLE', '我的工单')}
        subtitle={t('TICKET.SUBTITLE', '与客服双向沟通；任一方关闭后保留 15 天自动清除')}
        actions={
          <div className="flex gap-2">
            <button
              type="button"
              onClick={loadList}
              className="h-9 w-9 flex items-center justify-center rounded-control bg-surface-container hover:bg-on-surface/[0.04]"
              aria-label={t('SYSTEM.REFRESH', '刷新')}
            >
              <RefreshCw size={14} className={loading ? 'animate-spin' : ''} />
            </button>
            <button
              type="button"
              onClick={() => setView('create')}
              className="h-9 px-4 bg-primary text-on-primary rounded-control text-sm font-semibold hover:brightness-110 active:scale-[0.98] flex items-center gap-1.5 transition"
            >
              <Plus size={14} /> {t('TICKET.NEW_BTN', '新工单')}
            </button>
          </div>
        }
      >
      {loading ? (
        <div className="text-center py-20 text-on-surface-variant">{t('SYSTEM.LOADING', '加载中...')}</div>
      ) : tickets.length === 0 ? (
        <div className="fl-card p-16 text-center">
          <MessageSquare size={36} className="mx-auto mb-3 text-on-surface-variant" />
          <p className="text-on-surface-variant text-sm mb-3">{t('TICKET.EMPTY', '还没有工单')}</p>
          <button
            type="button"
            onClick={() => setView('create')}
            className="h-9 px-4 bg-primary text-on-primary rounded-control text-sm font-semibold hover:brightness-110 active:scale-[0.98] transition"
          >
            {t('TICKET.NEW_BTN', '新工单')}
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
                    <span className={`text-[10px] px-2 py-0.5 rounded font-mono ${isClosed
                      ? 'bg-on-surface/10 text-on-surface-variant'
                      : 'bg-emerald-500/15 text-emerald-400'}`}>
                      {isClosed ? t('TICKET.STATUS_CLOSED', '已关闭') : t('TICKET.STATUS_OPEN', '进行中')}
                    </span>
                    {unread > 0 && (
                      <span className="text-[10px] px-2 py-0.5 rounded-full bg-red-500 text-white font-mono">
                        {unread}
                      </span>
                    )}
                  </div>
                  <div className="text-[11px] text-on-surface-variant">
                    {t('TICKET.LAST_MSG', '最后活动')}: {new Date(t2.last_message_at).toLocaleString('zh-CN', { hour12: false })}
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
