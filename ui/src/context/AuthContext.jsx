/**
 * AuthContext — Phase 0 重构后的全局 auth 状态
 *
 * 替换原 App.jsx 内多个 useState（isAuthenticated / godModeUnlocked / globalProfile / banAlert）
 * 散落在 600 行 main 组件里的状况。
 *
 * 提供给所有 shell / page：
 *   - isAuthenticated: 普通用户登录态（Bearer token）
 *   - isAdmin: admin 模式解锁（cookie + localStorage flag）
 *   - profile: /api/user/me 拉到的 user 对象
 *   - openLogin(): 打开 AuthModal
 *   - signOut(): 服务端吊销 session + 清本地状态 + 刷新页
 *   - refreshProfile(): 手动重拉 profile
 *
 * 内部实现保持原 App.jsx 第 30s 轮询 + ban 拦截 + URL ?ref= 推荐逻辑，仅做剥离。
 */
import React, { createContext, useContext, useEffect, useMemo, useState, useCallback } from 'react';
import { logger } from '../utils/logger';

const AuthContext = createContext(null);

export const useAuth = () => {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error('useAuth must be used within AuthProvider');
  return ctx;
};

export const AuthProvider = ({ children }) => {
  const [isAuthenticated, setIsAuthenticated] = useState(() => !!localStorage.getItem('daof_token'));
  const [isAdmin, setIsAdmin] = useState(() => localStorage.getItem('daof_admin_unlocked') === '1');
  const [profile, setProfile] = useState(null);

  // 全局 AuthModal 状态（任何组件用 useAuth().openLogin() 触发）
  const [authModal, setAuthModal] = useState({
    isOpen: false,
    step: 'github',
    tmpToken: '',
    loading: false,
    defaultName: '',
  });

  // 全局 ban alert 状态
  const [banAlert, setBanAlert] = useState({ isOpen: false, reason: '', message: '' });

  const refreshProfile = useCallback(async () => {
    const userToken = localStorage.getItem('daof_token');
    if (!userToken) return;
    try {
      const res = await fetch('/api/user/me', { headers: { 'Authorization': `Bearer ${userToken}` } });
      const data = await res.json();
      if (data.success) setProfile(data.data);
    } catch {
      // 静默
    }
  }, []);

  // 全局 'user-profile-refresh' 事件钩子（充值到账、订阅购买、admin 调额触发）
  useEffect(() => {
    window.addEventListener('user-profile-refresh', refreshProfile);
    return () => window.removeEventListener('user-profile-refresh', refreshProfile);
  }, [refreshProfile]);

  // token 存活校验 + admin cookie 校验 + 30s 轮询
  useEffect(() => {
    const triggerBan = (data) => setBanAlert({
      isOpen: true,
      reason: data.ban_reason ||
        (data.message ? data.message.replace('账户被封禁', '').replace('理由：', '').trim() : ''),
      message: data.message || '',
    });

    const verifyUserToken = async (token) => {
      try {
        const res = await fetch('/api/user/me', { headers: { 'Authorization': `Bearer ${token}` } });
        const data = await res.json();
        if (data.success) {
          setProfile(data.data);
        } else {
          localStorage.removeItem('daof_token');
          setIsAuthenticated(false);
          if (res.status === 401 || data.message_code === 'ERR_BANNED' ||
              (data.message && data.message.includes('封禁'))) {
            triggerBan(data);
          }
        }
      } catch {
        // 网络异常不清空
      }
    };

    const verifyAdminCookie = async () => {
      try {
        const res = await fetch('/api/admin/config', { credentials: 'include' });
        if (res.status === 401 || res.status === 403) {
          localStorage.removeItem('daof_admin_unlocked');
          setIsAdmin(false);
        }
      } catch {
        // 网络异常不清空
      }
    };

    const tick = () => {
      const tok = localStorage.getItem('daof_token');
      const adm = localStorage.getItem('daof_admin_unlocked') === '1';
      if (tok) verifyUserToken(tok);
      if (adm) verifyAdminCookie();
    };
    tick();
    const intervalId = setInterval(tick, 30000);

    const handleBanEvent = (e) => {
      localStorage.removeItem('daof_token');
      setIsAuthenticated(false);
      // Phase 5（codex 审查 P5-1b）：BanAlertContainer 只展示 reason 字段，
      // 之前事件 detail 被写到 message 导致 GitHub OAuth ban 拒绝原因不显示。
      // 把 detail 同时写到 reason，让两条入口（轮询拒绝 + OAuth ban event）展示一致。
      const detail = typeof e.detail === 'string' ? e.detail : '';
      setBanAlert({ isOpen: true, reason: detail, message: detail });
    };
    window.addEventListener('daof_banned', handleBanEvent);

    return () => {
      clearInterval(intervalId);
      window.removeEventListener('daof_banned', handleBanEvent);
    };
  }, []);

  // 拉新链接：?ref=xxx 进站时存到 sessionStorage
  useEffect(() => {
    const refFromUrl = new URLSearchParams(window.location.search).get('ref');
    if (refFromUrl) {
      sessionStorage.setItem('daof_ref', refFromUrl.trim().slice(0, 32));
    }
  }, []);

  const signOut = useCallback(async () => {
    const userToken = localStorage.getItem('daof_token');
    const adminUnlocked = localStorage.getItem('daof_admin_unlocked') === '1';
    try {
      if (userToken) {
        await fetch('/api/auth/logout', {
          method: 'POST',
          headers: { 'Authorization': `Bearer ${userToken}` },
        }).catch(() => {});
      }
      if (adminUnlocked) {
        await fetch('/api/root/logout', { method: 'POST', credentials: 'include' }).catch(() => {});
      }
    } finally {
      localStorage.removeItem('daof_token');
      localStorage.removeItem('daof_admin_unlocked');
      window.location.href = '/';
    }
  }, []);

  // ─── AuthModal 控制 ────────────────────────────────────────
  const openLogin = useCallback((config = {}) => {
    setAuthModal({
      isOpen: true,
      step: config.step || 'github',
      tmpToken: config.tmpToken || '',
      loading: !!config.loading,
      defaultName: config.defaultName || '',
    });
  }, []);

  const closeLogin = useCallback(() => {
    setAuthModal(prev => ({ ...prev, isOpen: false }));
  }, []);

  // 手动通知 AuthContext 完成登录（AuthModal 调用）
  const onLoginSuccess = useCallback(() => {
    closeLogin();
    setIsAuthenticated(true);
    refreshProfile().catch(err => logger.warn('[auth] post-login profile fetch failed', err));
  }, [refreshProfile, closeLogin]);

  // ─── BanAlert 控制 ────────────────────────────────────────
  const closeBan = useCallback(() => {
    setBanAlert({ isOpen: false, reason: '', message: '' });
  }, []);

  const value = useMemo(() => ({
    isAuthenticated,
    isAdmin,
    profile,
    signOut,
    refreshProfile,
    // modal
    authModal,
    openLogin,
    closeLogin,
    onLoginSuccess,
    setAuthModal, // 给 AuthModal 内部 step 切换用
    // ban
    banAlert,
    closeBan,
  }), [isAuthenticated, isAdmin, profile, signOut, refreshProfile, authModal, openLogin, closeLogin, onLoginSuccess, banAlert, closeBan]);

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
};
