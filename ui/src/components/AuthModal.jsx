import React, { useState, useRef, useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { X, Phone, UserRound, ArrowRight } from 'lucide-react';
import toast from 'react-hot-toast';
import { useModalA11y } from '../hooks/useModalA11y';

const validateProfileName = (name) => /^[a-zA-Z0-9_\u4e00-\u9fa5]{2,20}$/.test(String(name || '').trim());

const sanitizeProfileName = (name) => {
  const cleaned = String(name || '')
    .trim()
    .replace(/[^a-zA-Z0-9_\u4e00-\u9fa5]+/g, '_')
    .replace(/^_+|_+$/g, '')
    .slice(0, 20);
  if (cleaned.length >= 2) return cleaned;
  return `user_${Math.random().toString(36).slice(2, 8)}`.slice(0, 20);
};

const AuthModal = ({ isOpen, onClose, onLoginSuccess, initialStep = 'github', tmpToken = '', initialLoading = false, defaultName = '' }) => {
  const { t } = useTranslation();
  const [step, setStep] = useState('github'); // 'github' / 'bind' / 'profile'
  const [loading, setLoading] = useState(false);
  const [countdown, setCountdown] = useState(0);
  const [sendingSms, setSendingSms] = useState(false);
  const countdownTimerRef = useRef(null);
  // a11y: 模态首次焦点目标。GitHub step 聚焦关闭按钮，bind/profile 聚焦第一个输入框
  const closeBtnRef = useRef(null);
  const modalRef = useRef(null); // C-F1 第二十一轮: focus trap 范围
  const { onBackdropClick } = useModalA11y(isOpen, onClose, closeBtnRef, modalRef);

  // 卸载时清理 SMS 倒计时定时器，避免在已卸载组件上 setState
  useEffect(() => () => {
    if (countdownTimerRef.current) clearInterval(countdownTimerRef.current);
  }, []);

  // Bind Form State
  const [username, setUsername] = useState('');
  const [phone, setPhone] = useState('');
  const [code, setCode] = useState('');

  // Profile Form State (H-2 修复：state 必须在 useEffect 引用前声明)
  const [profileName, setProfileName] = useState('');
  const [profileError, setProfileError] = useState('');

  // Sync state from App level initial loading interceptors
  React.useEffect(() => {
    if (isOpen) {
      setStep(initialStep);
      setLoading(initialLoading);
      if (initialStep === 'profile') {
        const suggestedName = sanitizeProfileName(defaultName);
        setProfileName(suggestedName);
        setProfileError(validateProfileName(suggestedName) ? '' : t('AUTH.PROFILE_NAME_ERROR'));
      }
    }
  }, [isOpen, initialStep, initialLoading, defaultName, t]);

  const handleProfileChange = (e) => {
    const val = e.target.value;
    setProfileName(val);
    if (val && !validateProfileName(val)) {
        setProfileError(t('AUTH.PROFILE_NAME_ERROR'));
    } else {
        setProfileError('');
    }
  };

  if (!isOpen) return null;

  const handleGithubLogin = async () => {
    setLoading(true);
    try {
      // 先到后端拿一次性 state + PKCE challenge，把公开参数透传到 GitHub 授权 URL
      const [pubRes, stateRes] = await Promise.all([
        fetch('/api/public-config'),
        fetch('/api/auth/github/prepare', { credentials: 'include' }),
      ]);
      // 5xx 时后端通常返 HTML 错误页；显式检查 status 给出真实原因
      if (!pubRes.ok) {
        toast.error(`服务端异常 (HTTP ${pubRes.status})，无法读取 GitHub OAuth 配置`);
        setLoading(false);
        return;
      }
      if (!stateRes.ok) {
        toast.error(`服务端异常 (HTTP ${stateRes.status})，无法生成 OAuth state`);
        setLoading(false);
        return;
      }
      const pub = await pubRes.json();
      const stateJson = await stateRes.json();
      if (!pub.success || !pub.github_client_id) {
        toast.error(t('AUTH.GITHUB_NOT_CONFIGURED'));
        setLoading(false);
        return;
      }
      if (!stateJson.success || !stateJson.state || !stateJson.code_challenge || !stateJson.code_challenge_method) {
        toast.error(t('AUTH.GITHUB_FETCH_ERROR'));
        setLoading(false);
        return;
      }
      const client_id = pub.github_client_id.trim();
      // fix Major（自审第十轮）：原代码直接用后端 SysConfig.server_address 拼接 OAuth redirect_uri，
      // 任何 admin 误填 / SysConfig 入侵都能让 GitHub 把 authorization code 重定向到攻击者域。
      // GitHub 会对照已注册的 redirect_uri 白名单（强 mitigation），但前端缺少独立校验是 defense-in-depth 缺口。
      // 校验：server_address 必须与当前页面 origin 同源；不一致直接退化到 window.location.origin。
      const baseAddress = (() => {
        const raw = (pub.server_address || '').trim().replace(/\/$/, '');
        if (!raw) return window.location.origin;
        try {
          const u = new URL(raw);
          if (u.origin === window.location.origin) return raw;
        } catch { /* 解析失败直接退化 */ }
        return window.location.origin;
      })();
      const callbackUri = `${baseAddress}/oauth/github`;
      const url = `https://github.com/login/oauth/authorize?client_id=${client_id}` +
        `&redirect_uri=${encodeURIComponent(callbackUri)}` +
        `&state=${encodeURIComponent(stateJson.state)}` +
        `&code_challenge=${encodeURIComponent(stateJson.code_challenge)}` +
        `&code_challenge_method=${encodeURIComponent(stateJson.code_challenge_method)}`;
      window.location.href = url;
    } catch {
      toast.error(t('AUTH.GITHUB_FETCH_ERROR'));
      setLoading(false);
    }
  };

  const handleSendCode = async () => {
    if (!phone || sendingSms) return;
    if (!/^1[3-9]\d{9}$/.test(phone)) {
      toast.error(t('AUTH.PHONE_INVALID', '请输入正确的 11 位手机号'));
      return;
    }
    setSendingSms(true);
    try {
      const res = await fetch('/api/auth/send-sms', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ phone }),
      });
      const data = await res.json();
      if (!data.success) {
        toast.error(data.message || t('AUTH.SMS_SEND_FAILED', '验证码发送失败'));
        // 服务器返回 retry_after 时使用其值开始倒计时
        if (data.retry_after && data.retry_after > 0) {
          setCountdown(data.retry_after);
          startCountdown(data.retry_after);
        }
        return;
      }
      toast.success(t('AUTH.SMS_SENT', '验证码已发送'));
      setCountdown(60);
      startCountdown(60);
    } catch {
      toast.error(t('AUTH.SMS_SEND_FAILED', '验证码发送失败'));
    } finally {
      setSendingSms(false);
    }
  };

  const startCountdown = (seconds) => {
    if (countdownTimerRef.current) clearInterval(countdownTimerRef.current);
    let current = seconds;
    countdownTimerRef.current = setInterval(() => {
      current -= 1;
      setCountdown(current);
      if (current <= 0) {
        clearInterval(countdownTimerRef.current);
        countdownTimerRef.current = null;
      }
    }, 1000);
  };

  const handleBind = async (e) => {
    e.preventDefault();
    setLoading(true);
    try {
      const res = await fetch('/api/auth/complete-risk', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ tmp_token: tmpToken, phone: phone, sms_code: code, username: username })
      });
      const data = await res.json();
      if (data.success) {
         if (!data.session_id) throw new Error('missing session_id');
         localStorage.setItem('daof_token', data.session_id);
         if (data.msg_code) { toast.success(t('API.' + data.msg_code)); } onLoginSuccess();
      } else {
         toast.error((data.message_code ? t('API.' + data.message_code) : data.message) || t('AUTH.BIND_FAILED'));
         setLoading(false);
      }
    } catch (err) {
      toast.error(t('AUTH.BIND_NET_ERROR'));
      setLoading(false);
    }
  };

  const handleCreateProfile = async (e) => {
    e.preventDefault();
    const nextProfileName = profileName.trim();
    if (!validateProfileName(nextProfileName)) {
      setProfileError(t('AUTH.PROFILE_NAME_ERROR'));
      return;
    }
    setProfileName(nextProfileName);
    setLoading(true);
    try {
      const res = await fetch('/api/auth/complete-profile', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ tmp_token: tmpToken, username: nextProfileName })
      });
      const data = await res.json();
      if (data.success) {
         if (!data.session_id) throw new Error('missing session_id');
         localStorage.setItem('daof_token', data.session_id);
         if (data.msg_code) { toast.success(t('API.' + data.msg_code)); } onLoginSuccess();
      } else {
         toast.error((data.message_code ? t('API.' + data.message_code) : data.message) || t('AUTH.PROFILE_FAILED'));
         setLoading(false);
      }
    } catch (err) {
      toast.error(t('AUTH.PROFILE_NET_ERROR'));
      setLoading(false);
    }
  };

  return (
    <div
      ref={modalRef}
      role="dialog"
      aria-modal="true"
      aria-labelledby="auth-modal-title"
      onClick={onBackdropClick}
      className="fixed inset-0 z-50 flex items-start sm:items-center justify-center p-2 sm:p-4 bg-black/60 backdrop-blur-sm overflow-y-auto animate-in fade-in"
    >
      <div className="relative w-full max-w-[400px] bg-surface-container border border-outline-variant rounded-overlay shadow-2xl shadow-black/40 overflow-hidden shadow-black/50 my-4 sm:my-0">

        {/* Close Button */}
        <button
          ref={closeBtnRef}
          type="button"
          onClick={onClose}
          aria-label={t('COMMON.CLOSE', '关闭')}
          className="absolute right-3 top-3 sm:right-4 sm:top-4 text-on-surface-variant hover:text-white p-1"
        >
          <X size={20} />
        </button>

        <div className="p-6 sm:p-8 pt-10 flex flex-col items-center">
          <h2 id="auth-modal-title" className="text-xl font-bold tracking-tight text-on-surface mb-2">
            {step === 'github' ? t('AUTH.TITLE_GITHUB') : (step === 'bind' ? t('AUTH.TITLE_BIND') : t('AUTH.TITLE_PROFILE'))}
          </h2>
          <p className="text-center text-sm text-on-surface-variant mb-8 mx-auto leading-relaxed">
            {step === 'github' && t('AUTH.DESC_GITHUB')}
            {step === 'bind' && t('AUTH.DESC_BIND')}
            {step === 'profile' && t('AUTH.DESC_PROFILE')}
          </p>

          {/* GitHub Step */}
          {step === 'github' && (
            <div className="w-full flex justify-center">
               <button
                  type="button"
                  onClick={handleGithubLogin}
                  disabled={loading}
                  className="w-full h-12 bg-white text-black font-semibold rounded-overlay flex items-center justify-center gap-3 hover:bg-surface-container active:scale-[0.98] disabled:opacity-70 disabled:active:scale-100"
               >
                 {loading ? (
                   <span className="flex items-center gap-2">
                     <div className="w-4 h-4 border-2 border-black/30 border-t-black rounded-full "></div>
                     {t('AUTH.BTN_GITHUB_LOADING')}
                   </span>
                 ) : (
                   <>
                     <svg height="20" width="20" viewBox="0 0 16 16" fill="currentColor">
                       <path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.013 8.013 0 0016 8c0-4.42-3.58-8-8-8z"></path>
                     </svg>
                     {t('AUTH.BTN_GITHUB')}
                   </>
                 )}
               </button>
            </div>
          )}

          {/* Bind Phone Step */}
          {step === 'bind' && (
            <form onSubmit={handleBind} className="w-full flex gap-4 flex-col">

              <div className="relative group">
                <input
                  type="text"
                  required
                  value={username}
                  onChange={(e) => setUsername(e.target.value)}
                  placeholder={t('AUTH.USERNAME_PLACEHOLDER') + ' *'}
                  className="w-full h-11 bg-surface-container border border-white/10 rounded-control px-4 text-sm text-on-surface outline-none focus:border-primary/50 placeholder-on-surface-variant/50"
                />
              </div>

              <div className="relative group">
                <div className="absolute inset-y-0 left-0 pl-4 flex items-center pointer-events-none">
                  <span className="text-on-surface-variant text-sm border-r border-outline-variant pr-3 mr-1">+86</span>
                </div>
                <input
                  type="tel"
                  required
                  value={phone}
                  onChange={(e) => setPhone(e.target.value)}
                  placeholder={t('AUTH.PHONE_PLACEHOLDER') + ' *'}
                  className="w-full h-11 bg-surface-container border border-white/10 rounded-control pl-[68px] pr-4 text-sm text-on-surface outline-none focus:border-primary/50 placeholder-on-surface-variant/50"
                />
              </div>

               <div className="flex gap-2">
                  <input
                    type="text"
                    required
                    value={code}
                    onChange={(e) => setCode(e.target.value)}
                    placeholder={t('AUTH.CODE_PLACEHOLDER') + ' *'}
                    className="flex-1 h-11 bg-surface-container border border-white/10 rounded-control px-4 text-sm text-on-surface outline-none focus:border-primary/50 placeholder-on-surface-variant/50"
                  />
                  <button
                    type="button"
                    onClick={handleSendCode}
                    disabled={countdown > 0 || !phone}
                    className="h-11 px-4 bg-surface-variant text-on-surface text-xs font-semibold rounded-control hover:bg-surface-container-high disabled:opacity-50 whitespace-nowrap"
                  >
                    {countdown > 0 ? t('AUTH.BTN_RE_SEND', { countdown }) : t('AUTH.BTN_SEND_CODE')}
                  </button>
               </div>

               <button
                  type="submit"
                  disabled={loading || !username || !phone || !code}
                  className="w-full h-11 mt-2 bg-gradient-to-r from-blue-600 to-cyan-500 text-on-surface font-semibold rounded-control flex items-center justify-center hover:opacity-90 -opacity disabled:opacity-50"
               >
                 {loading ? t('AUTH.BTN_BIND_LOADING') : t('AUTH.BTN_BIND')}
               </button>

            </form>
          )}

          {/* Setup Profile Step */}
          {step === 'profile' && (
            <form onSubmit={handleCreateProfile} className="w-full flex gap-4 flex-col">
              <div className="relative group">
                <input
                  type="text"
                  required
                  value={profileName}
                  onChange={handleProfileChange}
                  placeholder={t('AUTH.USERNAME_PLACEHOLDER') + ' *'}
                  aria-invalid={!!profileError}
                  aria-describedby={profileError ? 'auth-profile-error' : undefined}
                  className={`w-full h-11 bg-surface-container border ${profileError ? 'border-error/50 focus:border-error' : 'border-white/10 focus:border-primary/50'} rounded-control px-4 text-sm text-on-surface outline-none  placeholder-on-surface-variant/50`}
                />
                {profileError && (
                  <span id="auth-profile-error" role="alert" className="absolute -bottom-5 left-1 text-xs text-error">
                    {profileError}
                  </span>
                )}
              </div>

               <button
                  type="submit"
                  disabled={loading || !!profileError || !profileName}
                  className="w-full h-11 mt-4 bg-gradient-to-r from-blue-600 to-cyan-500 text-on-surface font-semibold rounded-control flex items-center justify-center hover:opacity-90 -opacity disabled:opacity-50"
               >
                 {loading ? t('AUTH.BTN_PROFILE_LOADING') : t('AUTH.BTN_PROFILE')}
               </button>
            </form>
          )}

        </div>

        {/* Footer info mock */}
        <div className="w-full p-4 border-t border-outline-variant text-center">
            <span className="text-xs text-on-surface-variant">{t('AUTH.FOOTER_TOS')}</span>
        </div>
      </div>
    </div>
  );
};

export default AuthModal;
