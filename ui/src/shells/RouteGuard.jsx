import React from 'react';
import { Navigate } from 'react-router-dom';
import RequireAuth from '../components/RequireAuth';
import { useAuth } from '../context/AuthContext';

/**
 * Soft guard for user routes. Unauthenticated users see the shared
 * RequireAuth banner, while authenticated users render the route content.
 *
 * admin 不算用户身份（后端 UserGuard 只认 Bearer），直接 redirect 走避免
 * 401 toast 满屏；UserShell 也会做一次同样的兜底重定向。
 *
 * 封禁用户（profile.status === 2）路由层不拦截——业务写动作（充值/购买/调 API）
 * 由后端按端点粒度 403 拒绝，浏览查看类页面允许进入。
 */
const RouteGuard = ({ children }) => {
  const { isAuthenticated, isAdmin, openLogin } = useAuth();
  if (isAdmin) {
    return <Navigate to="/admin" replace />;
  }
  return (
    <RequireAuth isAuthenticated={isAuthenticated} onSignIn={openLogin}>
      {children}
    </RequireAuth>
  );
};

export default RouteGuard;
