import React from 'react';
import RequireAuth from '../components/RequireAuth';
import { useAuth } from '../context/AuthContext';

/**
 * RouteGuard — 用户路由级软守卫（Phase 0）
 *
 * 替换原 App.jsx 内 `<RequireAuth isAuthenticated onSignIn>` 的散用。
 * 现在统一 routes.jsx 用 `<RouteGuard><Page /></RouteGuard>` 包受保护页面。
 *
 * 行为：未登录显示"需要登录"banner（保持原 RequireAuth 视觉），登录后渲染 children。
 * admin 解锁也算已登录。
 */
const RouteGuard = ({ children }) => {
  const { isAuthenticated, isAdmin, openLogin } = useAuth();
  return (
    <RequireAuth isAuthenticated={isAuthenticated || isAdmin} onSignIn={openLogin}>
      {children}
    </RequireAuth>
  );
};

export default RouteGuard;
