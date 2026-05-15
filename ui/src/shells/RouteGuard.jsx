import React from 'react';
import RequireAuth from '../components/RequireAuth';
import { useAuth } from '../context/AuthContext';

/**
 * Soft guard for user routes. Unauthenticated users see the shared
 * RequireAuth banner, while authenticated users render the route content.
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
