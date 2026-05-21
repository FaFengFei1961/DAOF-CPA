import React, { useState, useEffect, useCallback, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router-dom';
import { ArrowLeft, Bell, ExternalLink, X } from 'lucide-react';
import toast from 'react-hot-toast';
import { authFetch, isLoggedIn } from '../utils/authFetch';

const SEVERITY_COLOR = {
  info: 'text-primary',
  success: 'text-success',
  warning: 'text-warning',
  error: 'text-error',
};




const NotificationCenter = ({ isAuthenticated, onSignIn }) => {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const [notifs, setNotifs] = useState([]);
  const [unread, setUnread] = useState(0);
  const [selectedNotif, setSelectedNotif] = useState(null);
  const ref = useRef(null);

  const load = useCallback(async () => {
    if (!isLoggedIn()) return;
    try {
      const json = await authFetch('/api/notifications');
      if (json.success) {
        setNotifs(json.data || []);
        setUnread(json.unread_count || 0);
      }
    } catch {
      // Notification polling is best-effort.
    }
  }, []);

  useEffect(() => {
    if (!isAuthenticated) return;
    load();


    const intervalId = setInterval(() => {
      if (typeof document !== 'undefined' && document.hidden) return;
      load();
    }, 60000);
    const onVisible = () => {
      if (typeof document !== 'undefined' && !document.hidden) load();
    };
    if (typeof document !== 'undefined') {
      document.addEventListener('visibilitychange', onVisible);
    }
    return () => {
      clearInterval(intervalId);
      if (typeof document !== 'undefined') {
        document.removeEventListener('visibilitychange', onVisible);
      }
    };
  }, [load, isAuthenticated]);


  useEffect(() => {
    const handler = (e) => {
      if (ref.current && !ref.current.contains(e.target)) {
        setOpen(false);
        setSelectedNotif(null);
      }
    };
    if (open) document.addEventListener('mousedown', handler);
    return () => document.removeEventListener('mousedown', handler);
  }, [open]);

  const markRead = async (n) => {
    if (n.read_at) return;
    try {
      await authFetch(`/api/notifications/${n.id}/read`, { method: 'POST' });
      load();
    } catch {
      // A single read-marker failure should not interrupt the user.
    }
  };

  const markAll = async () => {
    try {
      const json = await authFetch('/api/notifications/read-all', { method: 'POST' });
      if (json.success) {
        toast.success(t('NOTIF.MARK_ALL_OK', '已全部标为已读'));
        load();
      } else {
        toast.error(json.message || t('NOTIF.MARK_FAIL', '标记失败'));
      }
    } catch {
      toast.error(t('NOTIF.NET_ERROR', '网络异常，标记失败'));
    }
  };




  const isSafeNavigateURL = (raw) => {
    if (typeof raw !== 'string') return false;
    const s = raw.trim();
    if (!s) return false;
    if (/[\r\n\t]/.test(s)) return false;
    if (s.startsWith('//')) return false;
    if (!s.startsWith('/')) return false;
    try {

      const u = new URL(s, window.location.origin);
      if (u.origin !== window.location.origin) return false;
      const proto = u.protocol.toLowerCase();
      if (proto !== 'http:' && proto !== 'https:') return false;
      return true;
    } catch {
      return false;
    }
  };

  const openDetail = (n) => {
    setSelectedNotif(n);
    markRead(n);
  };

  const handleNavigate = (n) => {
    markRead(n);
    if (n.action_url) {
      if (!isSafeNavigateURL(n.action_url)) {

        setOpen(false);
        setSelectedNotif(null);
        return;
      }


      try {
        navigate(n.action_url);
      } catch {

        window.location.href = n.action_url;
      }
      setOpen(false);
      setSelectedNotif(null);
    }
  };

  return (
    <div className="relative" ref={ref}>

      <button
        type="button"
        onClick={() => {
          setOpen(v => {
            if (v) setSelectedNotif(null);
            return !v;
          });
        }}
        className="relative w-8 h-8 flex items-center justify-center rounded-control text-on-surface-variant hover:text-on-surface hover:bg-on-surface/[0.04] transition"
        aria-label={t('NOTIF.CENTER', '通知中心')}
        aria-haspopup="dialog"
        aria-expanded={open}
        aria-controls="notification-center-popover"
      >
        <Bell size={16} />
        {unread > 0 && (
          <span className="absolute -top-0.5 -right-0.5 min-w-[16px] h-[16px] px-1 rounded-full bg-error text-white text-[9px] font-bold flex items-center justify-center">
            {unread > 99 ? '99+' : unread}
          </span>
        )}
      </button>

      {open && (
        <div
          id="notification-center-popover"
          role="dialog"
          aria-label={t('NOTIF.CENTER', '通知中心')}
          className="absolute right-0 top-full mt-2 w-96 max-w-[calc(100vw-2rem)] max-h-[560px] bg-surface-container border border-outline-variant rounded-overlay shadow-black/40 z-50 flex flex-col overflow-hidden">
          <div className="p-3 border-b border-outline-variant/40 flex items-center justify-between">
            <div className="min-w-0 flex items-center gap-2">
              {selectedNotif && (
                <button
                  type="button"
                  onClick={() => setSelectedNotif(null)}
                  className="w-7 h-7 flex items-center justify-center rounded-control text-on-surface-variant hover:text-on-surface hover:bg-on-surface/[0.06]"
                  aria-label={t('COMMON.PREV', '上一页')}
                >
                  <ArrowLeft size={15} />
                </button>
              )}
              <span className="text-sm font-semibold truncate">
                {selectedNotif ? t('NOTIF.DETAIL', '通知详情') : t('NOTIF.CENTER', '通知中心')}
              </span>
            </div>
            <div className="flex items-center gap-2">
              {!selectedNotif && unread > 0 && (
                <button type="button" onClick={markAll} className="text-xs text-primary hover:underline">{t('NOTIF.MARK_ALL', '全部已读')}</button>
              )}
              <button
                type="button"
                onClick={() => { setOpen(false); setSelectedNotif(null); }}
                className="w-7 h-7 flex items-center justify-center rounded-control text-on-surface-variant hover:text-on-surface hover:bg-on-surface/[0.06]"
                aria-label={t('COMMON.CLOSE', '关闭')}
              >
                <X size={15} />
              </button>
            </div>
          </div>
          <div className="flex-1 overflow-y-auto">
            {selectedNotif ? (
              <div className="p-4 flex flex-col gap-3">
                <div className={`text-sm font-semibold break-words ${SEVERITY_COLOR[selectedNotif.severity] || 'text-on-surface'}`}>
                  {selectedNotif.title}
                </div>
                <div className="text-[11px] text-outline">
                  {new Date(selectedNotif.created_at).toLocaleString('zh-CN', { hour12: false })}
                </div>
                <div className="max-h-[360px] overflow-y-auto pr-1 text-sm leading-5 text-on-surface-variant whitespace-pre-wrap break-words">
                  {selectedNotif.body || t('NOTIF.NO_BODY', '无正文')}
                </div>
                {selectedNotif.action_url && (
                  <button
                    type="button"
                    onClick={() => handleNavigate(selectedNotif)}
                    className="mt-1 inline-flex w-fit items-center gap-1.5 px-3 py-1.5 text-xs bg-primary text-on-primary rounded-control font-medium hover:opacity-90"
                  >
                    {selectedNotif.action_text || t('NOTIF.OPEN_ACTION', '打开链接')}
                    <ExternalLink size={12} />
                  </button>
                )}
              </div>
            ) : !isAuthenticated ? (
              <div className="text-center py-10 px-4">
                <div className="text-sm text-on-surface-variant mb-3">
                  {t('NOTIF.AUTH_REQUIRED', '登录后即可查看您的通知')}
                </div>
                <button
                  type="button"
                  onClick={() => { setOpen(false); onSignIn?.(); }}
                  className="btn btn-primary h-8"
                >
                  {t('AUTH_GATE.SIGN_IN', '登录')}
                </button>
              </div>
            ) : notifs.length === 0 ? (
              <div className="text-center py-12 text-on-surface-variant text-sm">{t('NOTIF.EMPTY', '没有通知')}</div>
            ) : (



              notifs.map(n => (
                <article key={n.id}
                  className={`px-3 py-3 border-b border-outline-variant/20 hover:bg-surface-container-high ${!n.read_at ? 'bg-primary/5' : ''}`}>
                  <div className="flex items-start gap-2">
                    <div className={`w-1.5 h-1.5 rounded-full mt-1.5 ${!n.read_at ? 'bg-primary' : 'bg-transparent'}`} />
                    <div className="flex-1 min-w-0">
                      <button
                        type="button"
                        onClick={() => openDetail(n)}
                        className="w-full text-left bg-transparent border-0 p-0 cursor-pointer"
                        aria-label={t('NOTIF.OPEN_DETAIL', '查看通知详情')}
                      >
                        <div className={`text-xs font-semibold break-words ${SEVERITY_COLOR[n.severity] || 'text-on-surface'}`}>
                          {n.title}
                        </div>
                        <div className="text-xs text-on-surface-variant mt-0.5 line-clamp-2 break-words">{n.body || t('NOTIF.NO_BODY', '无正文')}</div>
                        <div className="text-[10px] text-outline mt-1">
                          {new Date(n.created_at).toLocaleString('zh-CN', { hour12: false })}
                        </div>
                        <div className="text-[11px] text-primary mt-1">{t('NOTIF.VIEW_FULL', '查看全文')}</div>
                      </button>
                      {n.action_url && (
                        <button
                          type="button"
                          onClick={() => handleNavigate(n)}
                          className="mt-2 inline-flex items-center gap-1 px-2 py-1 text-[11px] bg-primary text-on-primary rounded-control font-medium hover:opacity-90"
                        >
                          {n.action_text || t('NOTIF.OPEN_ACTION', '打开链接')}
                          <ExternalLink size={11} />
                        </button>
                      )}
                    </div>
                  </div>
                </article>
              ))
            )}
          </div>
        </div>
      )}
    </div>
  );
};

export default NotificationCenter;
