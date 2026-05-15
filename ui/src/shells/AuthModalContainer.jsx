import React, { Suspense, lazy } from 'react';
import { useAuth } from '../context/AuthContext';

const AuthModal = lazy(() => import('../components/AuthModal'));

/**
 * Global AuthModal renderer backed by auth context state.
 */
const AuthModalContainer = () => {
  const { authModal, closeLogin, onLoginSuccess, setAuthModal } = useAuth();
  if (!authModal.isOpen) return null;
  return (
    <Suspense fallback={null}>
      <AuthModal
        isOpen={authModal.isOpen}
        initialStep={authModal.step}
        tmpToken={authModal.tmpToken}
        initialLoading={authModal.loading}
        defaultName={authModal.defaultName}
        onClose={closeLogin}
        onLoginSuccess={onLoginSuccess}
        // Preserve the updater-style step API expected by AuthModal.
        onStepChange={(updater) => setAuthModal(prev => typeof updater === 'function' ? updater(prev) : { ...prev, ...updater })}
      />
    </Suspense>
  );
};

export default AuthModalContainer;
