import React, { useState, useEffect, useCallback, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { Bell, Check, X } from 'lucide-react';
import toast from 'react-hot-toast';
import { authFetch, isLoggedIn } from '../utils/authFetch';

const SEVERITY_COLOR = {
  info: 'text-blue-400',
  success: 'text-emerald-400',
  warning: 'text-amber-400',
  error: 'text-red-400',
};

// 通知中心。挂在 TopBar 作为下拉面板。
// 未登录 → 显示空态 + "登录后查看通知"引导按钮，不发请求
// 已登录 → 拉 /api/notifications，每 60s 自动刷新
const NotificationCenter = ({ trigger, onNavigate, isAuthenticated, onSignIn }) => {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const [notifs, setNotifs] = useState([]);
  const [unread, setUnread] = useState(0);
  const ref = useRef(null);

  const load = useCallback(async () => {
    if (!isLoggedIn()) return;
    try {
      const json = await authFetch('/api/notifications');
      if (json.success) {
        setNotifs(json.data || []);
        setUnread(json.unread_count || 0);
      }
    } catch { /* 静默 */ }
  }, []);

  useEffect(() => {
    if (!isAuthenticated) return;
    load();
    // fix Minor m23-f2（gemini 第二十三轮）：标签页隐藏时跳过轮询省流量。
    // tab 切回前台时立即触发一次 load 让用户拿到最新通知。
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

  // 点外部关闭
  useEffect(() => {
    const handler = (e) => {
      if (ref.current && !ref.current.contains(e.target)) setOpen(false);
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
      // 静默：单条标记失败不打扰用户
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

  // fix Major（codex 第四轮）：前端必须独立校验 action_url，不能信赖后端 dispatcher 一定过滤干净。
  // 拒绝 javascript:/data:/blob:/外部 URL/协议相对（//host）/含控制字符。
  // 即使 dispatcher 后期被绕过或脏数据残留，前端也不会把用户带去钓鱼站。
  const isSafeNavigateURL = (raw) => {
    if (typeof raw !== 'string') return false;
    const s = raw.trim();
    if (!s) return false;
    if (/[\r\n\t]/.test(s)) return false;
    if (s.startsWith('//')) return false;
    if (!s.startsWith('/')) return false;
    try {
      // 用 base URL 解析；如果结果 origin 不等于当前 origin，拒绝
      const u = new URL(s, window.location.origin);
      if (u.origin !== window.location.origin) return false;
      const proto = u.protocol.toLowerCase();
      if (proto !== 'http:' && proto !== 'https:') return false;
      return true;
    } catch {
      return false;
    }
  };

  const handleClick = (n) => {
    markRead(n);
    if (n.action_url) {
      if (!isSafeNavigateURL(n.action_url)) {
        // 不可信链接静默忽略；保留 markRead 的副作用
        setOpen(false);
        return;
      }
      // fix Critical Codex UX 审查（第二十五轮 #2）：解析 action_url 时保留 query/hash 提示，
      // 让后端发的深链如 "/upgrade?pane=mine" 能正确切到 UpgradePage 的"我的" 一级 tab。
      // 形如 "/upgrade" → onNavigate('upgrade')；
      //     "/upgrade?pane=mine" → onNavigate('upgrade')，同时把 ?pane=mine 写到 location.hash
      //     "/tickets" → onNavigate('tickets')
      if (onNavigate) {
        const path = n.action_url.replace(/^\/+/, '');
        const [pathOnly, query] = path.split('?');
        const view = pathOnly.split('/')[0] || 'dashboard';
        // 把 query 透传给目标 view（如 UpgradePage 读 pane）
        if (query) {
          window.location.hash = `#${view}?${query}`;
        }
        onNavigate(view);
      } else {
        window.location.href = n.action_url;
      }
      setOpen(false);
    }
  };

  return (
    <div className="relative" ref={ref}>
      {/* fix CRITICAL C23-F1（gemini 第二十三轮 + WCAG 4.1.2 Name, Role, Value）：
          自定义下拉缺 aria-haspopup + aria-expanded + 可识别的 dialog role。
          屏幕阅读器之前不知道这是个弹出面板。 */}
      <button
        type="button"
        onClick={() => setOpen(v => !v)}
        className="relative w-8 h-8 flex items-center justify-center rounded text-on-surface-variant hover:text-on-surface hover:bg-on-surface/[0.04] transition"
        aria-label={t('NOTIF.CENTER', '通知中心')}
        aria-haspopup="dialog"
        aria-expanded={open}
        aria-controls="notification-center-popover"
      >
        <Bell size={16} />
        {unread > 0 && (
          <span className="absolute -top-0.5 -right-0.5 min-w-[16px] h-[16px] px-1 rounded-full bg-red-500 text-white text-[9px] font-bold flex items-center justify-center">
            {unread > 99 ? '99+' : unread}
          </span>
        )}
      </button>

      {open && (
        <div
          id="notification-center-popover"
          role="dialog"
          aria-label={t('NOTIF.CENTER', '通知中心')}
          className="absolute right-0 top-full mt-2 w-80 max-h-[500px] bg-surface-container border border-outline-variant rounded-xl shadow-xl shadow-black/40 z-50 flex flex-col overflow-hidden">
          <div className="p-3 border-b border-outline-variant/40 flex items-center justify-between">
            <span className="text-sm font-semibold">{t('NOTIF.CENTER', '通知中心')}</span>
            {unread > 0 && (
              <button onClick={markAll} className="text-xs text-primary hover:underline">{t('NOTIF.MARK_ALL', '全部已读')}</button>
            )}
          </div>
          <div className="flex-1 overflow-y-auto">
            {!isAuthenticated ? (
              <div className="text-center py-10 px-4">
                <div className="text-sm text-on-surface-variant mb-3">
                  {t('NOTIF.AUTH_REQUIRED', '登录后即可查看您的通知')}
                </div>
                <button
                  type="button"
                  onClick={() => { setOpen(false); onSignIn?.(); }}
                  className="fl-btn fl-btn-prominent h-8"
                >
                  {t('AUTH_GATE.SIGN_IN', '登录')}
                </button>
              </div>
            ) : notifs.length === 0 ? (
              <div className="text-center py-12 text-on-surface-variant text-sm">{t('NOTIF.EMPTY', '没有通知')}</div>
            ) : (
              // fix Major Codex UX 审查（第二十五轮）：原实现把整条通知做成大 button，
              // action_text（如"查看订阅"、"联系客服"）后端存了但前端完全不展示，用户不知道点击会做什么。
              // 改用 article + 标题/正文 div + 独立 action_text CTA 按钮——保留整条点击行为，但语义清晰。
              notifs.map(n => (
                <article key={n.id}
                  className={`px-3 py-3 border-b border-outline-variant/20 hover:bg-surface-container-high ${!n.read_at ? 'bg-primary/5' : ''}`}>
                  <div className="flex items-start gap-2">
                    <div className={`w-1.5 h-1.5 rounded-full mt-1.5 ${!n.read_at ? 'bg-primary' : 'bg-transparent'}`} />
                    <div className="flex-1 min-w-0">
                      <button
                        type="button"
                        onClick={() => handleClick(n)}
                        className="w-full text-left bg-transparent border-0 p-0 cursor-pointer"
                      >
                        <div className={`text-xs font-semibold ${SEVERITY_COLOR[n.severity] || 'text-on-surface'}`}>
                          {n.title}
                        </div>
                        <div className="text-xs text-on-surface-variant mt-0.5 line-clamp-2">{n.body}</div>
                        <div className="text-[10px] text-outline mt-1">
                          {new Date(n.created_at).toLocaleString('zh-CN', { hour12: false })}
                        </div>
                      </button>
                      {n.action_text && n.action_url && (
                        <button
                          type="button"
                          onClick={() => handleClick(n)}
                          className="mt-2 inline-flex items-center gap-1 px-2 py-1 text-[11px] bg-primary text-on-primary rounded-md font-medium hover:opacity-90"
                        >
                          {n.action_text} →
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
