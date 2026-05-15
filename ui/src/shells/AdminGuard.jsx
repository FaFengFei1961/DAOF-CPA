import React from 'react';
import { Navigate, useLocation } from 'react-router-dom';
import { useAuth } from '../context/AuthContext';

/**
 * AdminGuard protects the admin route tree.
 *
 * Admin access still depends on the auth cookie and local admin flag.
 * Unlocked users may enter /admin/*; everyone else returns to the user home.
 */
const AdminGuard = ({ children }) => {
  const { isAdmin } = useAuth();
  const location = useLocation();

  if (!isAdmin) {
    // Keep the original target for a possible future post-login redirect.
    return <Navigate to="/" replace state={{ from: location.pathname }} />;
  }
  return children;
};

export default AdminGuard;
