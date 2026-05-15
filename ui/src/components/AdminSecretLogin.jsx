import React, { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Shield, Eye, EyeOff, Save, Lock } from 'lucide-react';

const AdminSecretLogin = ({ sysParam, setupMode, onSuccess }) => {
  const { t } = useTranslation();
  const [password, setPassword] = useState('');
  const [newUsername, setNewUsername] = useState('');
  const [newPassword, setNewPassword] = useState('');

  const [show, setShow] = useState(false);
  const [loading, setLoading] = useState(false);
  const [errorMsg, setErrorMsg] = useState('');

  const handleLogin = async (e) => {
    e.preventDefault();
    setLoading(true);
    setErrorMsg('');
    try {
      const response = await fetch('/api/root/god-login', {
        method: 'POST',
        credentials: 'include', // 让浏览器接收 Set-Cookie 并在后续 admin 请求里自动携带
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ username: sysParam, password })
      });
      const data = await response.json();
      if(data.success) {
        // 真实 token 由后端 HttpOnly Cookie 持有，前端无法读到。
        // localStorage 只保留布尔标志，便于刷新后判定 godModeUnlocked，且 XSS 偷不到任何敏感值。
        localStorage.setItem('daof_admin_unlocked', '1');
        onSuccess();
      } else {
        setErrorMsg((data.message_code ? t('API.' + data.message_code) : data.message) || t('ADMIN_LOGIN.LOGIN_FAILED'));
      }
    } catch(err) {
      setErrorMsg(t('ADMIN_LOGIN.NET_ERROR'));
    }
    setLoading(false);
  };

  const handleSetup = async (e) => {
    e.preventDefault();
    setLoading(true);
    setErrorMsg('');
    try {
      const response = await fetch('/api/root/setup', {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
           current_username: sysParam,
           new_username: newUsername,
           new_password: newPassword
        })
      });
      const data = await response.json();
      if(data.success) {
        localStorage.setItem('daof_admin_unlocked', '1');
        // 配置完成后，让用户用新的链接重新进入
        window.location.href = `/?sys=${newUsername}`;
      } else {
        setErrorMsg((data.message_code ? t('API.' + data.message_code) : data.message) || t('ADMIN_LOGIN.SETUP_FAILED'));
      }
    } catch(err) {
      setErrorMsg(t('ADMIN_LOGIN.NET_ERROR'));
    }
    setLoading(false);
  };

  if (setupMode) {
    return (
      <div className="min-h-screen bg-surface flex items-center justify-center p-4 font-sans text-on-surface-variant">
        <div className="w-full max-w-md bg-surface-container border border-outline-variant rounded-overlay shadow-2xl shadow-black/40 p-8">
          <div className="flex flex-col items-center justify-center mb-8 gap-3">
             <div className="w-16 h-16 rounded-full bg-primary/30 flex items-center justify-center">
                 <Lock size={32} className="text-primary" />
             </div>
             <h1 className="text-2xl font-bold tracking-tight mt-2">{t('ADMIN_LOGIN.SETUP_TITLE')}</h1>
             <p className="text-xs text-on-surface-variant text-center font-medium px-4">
               {t('ADMIN_LOGIN.SETUP_DESC')}
             </p>
          </div>

          <form onSubmit={handleSetup} className="flex flex-col gap-5">
            <div className="flex flex-col gap-2">
               <label htmlFor="admin-setup-username" className="text-sm font-semibold text-on-surface-variant">{t('ADMIN_LOGIN.SETUP_USERNAME_LABEL')}</label>
               <input
                 id="admin-setup-username"
                 type="text"
                 required
                 value={newUsername}
                 onChange={(e) => setNewUsername(e.target.value)}
                 className="w-full bg-surface-container-high border border-outline rounded-overlay px-4 py-3 outline-none focus:border-primary "
                 placeholder={t('ADMIN_LOGIN.SETUP_USERNAME_PLACEHOLDER')}
               />
            </div>
            <div className="flex flex-col gap-2">
               <label htmlFor="admin-setup-password" className="text-sm font-semibold text-on-surface-variant">{t('ADMIN_LOGIN.SETUP_PASSWORD_LABEL')}</label>
               <input
                 id="admin-setup-password"
                 type="text"
                 required
                 value={newPassword}
                 onChange={(e) => setNewPassword(e.target.value)}
                 className="w-full bg-surface-container-high border border-outline rounded-overlay px-4 py-3 outline-none focus:border-primary "
                 placeholder={t('ADMIN_LOGIN.SETUP_PASSWORD_PLACEHOLDER')}
               />
            </div>

            {errorMsg && (
               <div className="text-xs text-error bg-error/10 border border-error/30 rounded-control p-3 text-center">
                 {errorMsg}
               </div>
            )}

            <button
              type="submit"
              disabled={loading || !newUsername || !newPassword}
              className="w-full mt-4 bg-primary text-on-primary hover:bg-primary-container hover:text-on-primary-container font-semibold rounded-overlay py-3.5 disabled:opacity-50 flex items-center justify-center gap-2"
            >
               <Save size={18} />
               {loading ? t('ADMIN_LOGIN.BTN_SETUP_LOADING') : t('ADMIN_LOGIN.BTN_SETUP')}
            </button>
          </form>
        </div>
      </div>
    );
  }

  return (
    <div className="min-h-screen bg-surface flex items-center justify-center p-4 font-sans text-on-surface-variant">
      <div className="w-full max-w-sm bg-surface-container border border-outline-variant rounded-overlay shadow-2xl shadow-black/40 p-8">
        <div className="flex flex-col items-center justify-center mb-8 gap-3">
           <div className="w-16 h-16 rounded-full bg-primary/20 flex items-center justify-center border border-primary/30">
               <Shield size={32} className="text-primary" />
           </div>
           <h1 className="text-2xl font-bold tracking-tight mt-2">{t('ADMIN_LOGIN.LOGIN_TITLE')}</h1>
        </div>

        <form onSubmit={handleLogin} className="flex flex-col gap-5">
          <div className="flex flex-col gap-2 relative">
             <label htmlFor="admin-login-password" className="text-sm font-semibold text-on-surface-variant">{t('ADMIN_LOGIN.PASSWORD_LABEL')}</label>
             <input
               id="admin-login-password"
               type={show ? "text" : "password"}
               required
               value={password}
               onChange={(e) => setPassword(e.target.value)}
               className="w-full bg-surface-container-high border border-outline rounded-overlay px-4 py-3 pr-10 outline-none focus:border-primary "
               placeholder="••••••••"
             />
             <button type="button" onClick={() => setShow(!show)} aria-label={show ? '隐藏密码' : '显示密码'} className="absolute right-3 top-[34px] text-on-surface-variant hover:text-on-surface-variant">
               {show ? <EyeOff size={18}/> : <Eye size={18}/>}
             </button>
          </div>

          {errorMsg && (
             <div className="text-xs text-error bg-error/10 border border-error/30 rounded-control p-3 text-center">
               {errorMsg}
             </div>
          )}

          <button
            type="submit"
            disabled={loading || !password}
            className="w-full mt-2 bg-white hover:bg-surface-container text-black font-semibold rounded-overlay py-3 disabled:opacity-50"
          >
             {loading ? t('ADMIN_LOGIN.BTN_LOGIN_LOADING') : t('ADMIN_LOGIN.BTN_LOGIN')}
          </button>
        </form>
      </div>
    </div>
  );
};

export default AdminSecretLogin;
