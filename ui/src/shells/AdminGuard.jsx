import React from 'react';
import { Navigate, useLocation } from 'react-router-dom';
import { useAuth } from '../context/AuthContext';

/**
 * AdminGuard — admin shell 路由守卫（Phase 0）
 *
 * 替换原 App.jsx 用 UI state (godModeUnlocked) 控制渲染的方式。
 * 现在 admin 通过路由 /admin/* 进入，未解锁会被 redirect 到 /?sys=... 走 AdminSecretLogin。
 *
 * 注意：admin 解锁仍依赖 cookie + localStorage flag，没有 token in URL。
 * 没解锁但访问 /admin → redirect 回首页。AdminSecretLogin 入口靠 ?sys= URL 参数触发。
 */
const AdminGuard = ({ children }) => {
  const { isAdmin } = useAuth();
  const location = useLocation();

  if (!isAdmin) {
    // 没 admin 权限不让进 admin shell。回首页（带原 from 用于潜在登录后回跳）。
    return <Navigate to="/" replace state={{ from: location.pathname }} />;
  }
  return children;
};

export default AdminGuard;
