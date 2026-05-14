import React, { Suspense, lazy } from 'react';
import { useAuth } from '../context/AuthContext';

const AuthModal = lazy(() => import('../components/AuthModal'));

/**
 * AuthModalContainer — 全局 AuthModal 渲染（Phase 0）
 *
 * 替换原 App.jsx 内分散管理 authModalConfig 的代码。
 * 现在所有 page / shell 通过 useAuth().openLogin() 触发，container 监听 context 渲染。
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
        // 兼容原 AuthModal 内部需要切 step 的接口（旧 AuthModal 用 prev => ... 风格）
        onStepChange={(updater) => setAuthModal(prev => typeof updater === 'function' ? updater(prev) : { ...prev, ...updater })}
      />
    </Suspense>
  );
};

export default AuthModalContainer;
