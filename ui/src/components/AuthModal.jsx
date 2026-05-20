import React, { useState, useRef, useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { X, Phone, UserRound, ArrowRight, Mail, Eye, EyeOff, Lock } from 'lucide-react';
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

  const closeBtnRef = useRef(null);
  const modalRef = useRef(null);
  const { onBackdropClick } = useModalA11y(isOpen, onClose, closeBtnRef, modalRef);


  useEffect(() => () => {
    if (countdownTimerRef.current) clearInterval(countdownTimerRef.current);
  }, []);

  // Bind Form State
  const [username, setUsername] = useState('');
  const [phone, setPhone] = useState('');
  const [code, setCode] = useState('');


  const [profileName, setProfileName] = useState('');
  const [profileError, setProfileError] = useState('');

  // Email auth state (Phase G-2.6)
  const [email, setEmail] = useState('');
  const [pwd, setPwd] = useState('');
  const [pwdConfirm, setPwdConfirm] = useState('');
  const [showPwd, setShowPwd] = useState(false);
  const [emailUsername, setEmailUsername] = useState('');

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

      const [pubRes, stateRes] = await Promise.all([
        fetch('/api/public-config'),
        fetch('/api/auth/github/prepare', { credentials: 'include' }),
      ]);

      if (!pubRes.ok) {
        toast.error(t('AUTH.GITHUB_CONFIG_HTTP_ERROR', {
          status: pubRes.status,
          defaultValue: '服务端异常 (HTTP {{status}})，无法读取 GitHub OAuth 配置',
        }));
        setLoading(false);
        return;
      }
      if (!stateRes.ok) {
        toast.error(t('AUTH.OAUTH_STATE_HTTP_ERROR', {
          status: stateRes.status,
          defaultValue: '服务端异常 (HTTP {{status}})，无法生成 OAuth state',
        }));
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




      const baseAddress = (() => {
        const raw = (pub.server_address || '').trim().replace(/\/$/, '');
        if (!raw) return window.location.origin;
        try {
          const u = new URL(raw);
          if (u.origin === window.location.origin) return raw;
        } catch {
          // Fall back to the current origin when server_address is invalid.
        }
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

  // Phase G-2.6 email login
  const handleEmailLogin = async (e) => {
    e.preventDefault();
    setLoading(true);
    try {
      const res = await fetch('/api/auth/email/login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ email, password: pwd }),
      });
      const data = await res.json();
      if (data.success) {
        if (!data.session_id) throw new Error('missing session_id');
        localStorage.setItem('daof_token', data.session_id);
        if (data.message_code) toast.success(t('API.' + data.message_code, '登录成功'));
        onLoginSuccess();
      } else {
        toast.error((data.message_code ? t('API.' + data.message_code) : data.message) || t('AUTH.EMAIL_LOGIN_FAILED', '邮箱登录失败'));
        setLoading(false);
      }
    } catch {
      toast.error(t('AUTH.EMAIL_LOGIN_NET_ERROR', '登录网络异常'));
      setLoading(false);
    }
  };

  // Phase G-2.6 email signup
  const handleEmailSignup = async (e) => {
    e.preventDefault();
    if (pwd !== pwdConfirm) {
      toast.error(t('AUTH.EMAIL_PWD_MISMATCH', '两次密码不一致'));
      return;
    }
    setLoading(true);
    try {
      const res = await fetch('/api/auth/email/signup', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ email, password: pwd, username: emailUsername }),
      });
      const data = await res.json();
      if (data.success) {
        // 注册成功不带 session_id —— 必须先验证邮箱
        if (data.message_code) toast.success(t('API.' + data.message_code, '注册成功，请到邮箱完成验证后再登录'));
        setStep('email-signup-sent');
      } else {
        toast.error((data.message_code ? t('API.' + data.message_code) : data.message) || t('AUTH.EMAIL_SIGNUP_FAILED', '注册失败'));
      }
    } catch {
      toast.error(t('AUTH.EMAIL_SIGNUP_NET_ERROR', '注册网络异常'));
    } finally {
      setLoading(false);
    }
  };

  // Phase G-2.6 forgot password
  const handleForgotPassword = async (e) => {
    e.preventDefault();
    setLoading(true);
    try {
      const res = await fetch('/api/auth/email/forgot-password', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ email }),
      });
      const data = await res.json();
      if (data.success) {
        if (data.message_code) toast.success(t('API.' + data.message_code, '如该邮箱已注册并验证，重置链接已发出'));
        setStep('email-forgot-sent');
      } else {
        toast.error((data.message_code ? t('API.' + data.message_code) : data.message) || t('AUTH.EMAIL_FORGOT_FAILED', '请求失败'));
      }
    } catch {
      toast.error(t('AUTH.EMAIL_FORGOT_NET_ERROR', '网络异常'));
    } finally {
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
            {step === 'github' && t('AUTH.TITLE_GITHUB')}
            {step === 'bind' && t('AUTH.TITLE_BIND')}
            {step === 'profile' && t('AUTH.TITLE_PROFILE')}
            {step === 'email-login' && t('AUTH.TITLE_EMAIL_LOGIN', '邮箱登录')}
            {step === 'email-signup' && t('AUTH.TITLE_EMAIL_SIGNUP', '邮箱注册')}
            {step === 'email-forgot' && t('AUTH.TITLE_EMAIL_FORGOT', '找回密码')}
            {step === 'email-signup-sent' && t('AUTH.TITLE_SIGNUP_SENT', '验证邮件已发送')}
            {step === 'email-forgot-sent' && t('AUTH.TITLE_FORGOT_SENT', '邮件已发送')}
          </h2>
          <p className="text-center text-sm text-on-surface-variant mb-8 mx-auto leading-relaxed">
            {step === 'github' && t('AUTH.DESC_GITHUB')}
            {step === 'bind' && t('AUTH.DESC_BIND')}
            {step === 'profile' && t('AUTH.DESC_PROFILE')}
            {step === 'email-login' && t('AUTH.DESC_EMAIL_LOGIN', '使用您的邮箱和密码登录。')}
            {step === 'email-signup' && t('AUTH.DESC_EMAIL_SIGNUP', '创建一个新账号。注册后需到邮箱完成验证。')}
            {step === 'email-forgot' && t('AUTH.DESC_EMAIL_FORGOT', '我们会向您发送密码重置链接。')}
          </p>

          {/* GitHub Step */}
          {step === 'github' && (
            <div className="w-full flex flex-col gap-3 items-center">
               {/* fix P2（codex review verify-1）：避开 hardcoded hex 违反 design-system/strict-tokens。
                   GitHub 官方品牌色（#24292f）不属于本项目 design token，但作为 OAuth 提供商
                   品牌按钮是合理破例 → 用 style 内联绕过 Tailwind lint。 */}
               <button
                  type="button"
                  onClick={handleGithubLogin}
                  disabled={loading}
                  style={{
                    backgroundColor: 'var(--github-btn-bg, #24292f)',
                    color: '#ffffff',
                  }}
                  className="w-full h-12 font-semibold rounded-overlay flex items-center justify-center gap-3 hover:brightness-110 active:scale-[0.98] transition-all disabled:opacity-70 disabled:active:scale-100 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary"
               >
                 {loading ? (
                   <span className="flex items-center gap-2">
                     <div className="w-4 h-4 border-2 border-white/30 border-t-white rounded-full animate-spin"></div>
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

               {/* Phase G-2.6：在 GitHub 按钮下方提供"使用邮箱登录/注册"入口 */}
               <div className="flex items-center w-full gap-3 my-1">
                 <div className="flex-1 h-px bg-outline-variant" />
                 <span className="text-xs text-on-surface-variant">{t('AUTH.OR', '或')}</span>
                 <div className="flex-1 h-px bg-outline-variant" />
               </div>
               <button
                 type="button"
                 onClick={() => setStep('email-login')}
                 className="w-full h-11 bg-surface-container-high border border-outline text-on-surface rounded-control text-sm font-medium flex items-center justify-center gap-2 hover:bg-surface-container active:scale-[0.99] transition-all"
               >
                 <Mail size={16} />
                 {t('AUTH.BTN_USE_EMAIL', '使用邮箱登录 / 注册')}
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

          {/* Phase G-2.6 邮箱+密码登录 */}
          {step === 'email-login' && (
            <form onSubmit={handleEmailLogin} className="w-full flex gap-4 flex-col">
              <div className="relative">
                <div className="absolute inset-y-0 left-3 flex items-center pointer-events-none">
                  <Mail size={16} className="text-on-surface-variant" />
                </div>
                <input
                  type="email"
                  required
                  autoComplete="email"
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                  placeholder={t('AUTH.EMAIL_PLACEHOLDER', '邮箱')}
                  className="w-full h-11 bg-surface-container border border-white/10 rounded-control pl-10 pr-4 text-sm text-on-surface outline-none focus:border-primary/50 placeholder-on-surface-variant/50"
                />
              </div>
              <div className="relative">
                <div className="absolute inset-y-0 left-3 flex items-center pointer-events-none">
                  <Lock size={16} className="text-on-surface-variant" />
                </div>
                <input
                  type={showPwd ? 'text' : 'password'}
                  required
                  autoComplete="current-password"
                  value={pwd}
                  onChange={(e) => setPwd(e.target.value)}
                  placeholder={t('AUTH.PWD_PLACEHOLDER', '密码')}
                  className="w-full h-11 bg-surface-container border border-white/10 rounded-control pl-10 pr-10 text-sm text-on-surface outline-none focus:border-primary/50 placeholder-on-surface-variant/50"
                />
                <button
                  type="button"
                  aria-label={t('COMMON.TOGGLE_PASSWORD_VISIBILITY', '切换密码可见性')}
                  onClick={() => setShowPwd(s => !s)}
                  className="absolute right-3 top-1/2 -translate-y-1/2 text-on-surface-variant"
                >
                  {showPwd ? <EyeOff size={16} /> : <Eye size={16} />}
                </button>
              </div>
              <button
                type="submit"
                disabled={loading || !email || !pwd}
                className="w-full h-11 mt-1 bg-gradient-to-r from-blue-600 to-cyan-500 text-on-surface font-semibold rounded-control flex items-center justify-center hover:opacity-90 disabled:opacity-50"
              >
                {loading ? t('AUTH.BTN_LOGIN_LOADING', '登录中...') : t('AUTH.BTN_EMAIL_LOGIN', '登录')}
              </button>
              <div className="flex justify-between text-xs">
                <button type="button" className="text-primary hover:underline" onClick={() => setStep('email-forgot')}>
                  {t('AUTH.LINK_FORGOT_PWD', '忘记密码？')}
                </button>
                <button type="button" className="text-primary hover:underline" onClick={() => setStep('email-signup')}>
                  {t('AUTH.LINK_GOTO_SIGNUP', '注册新账号')}
                </button>
              </div>
              <button type="button" className="text-xs text-on-surface-variant hover:underline mt-1" onClick={() => setStep('github')}>
                {t('AUTH.LINK_BACK_OAUTH', '改用 GitHub 登录')}
              </button>
            </form>
          )}

          {/* Phase G-2.6 邮箱+密码注册 */}
          {step === 'email-signup' && (
            <form onSubmit={handleEmailSignup} className="w-full flex gap-4 flex-col">
              <input
                type="text"
                required
                value={emailUsername}
                onChange={(e) => setEmailUsername(e.target.value)}
                placeholder={t('AUTH.USERNAME_PLACEHOLDER', '用户名')}
                className="w-full h-11 bg-surface-container border border-white/10 rounded-control px-4 text-sm text-on-surface outline-none focus:border-primary/50 placeholder-on-surface-variant/50"
              />
              <div className="relative">
                <div className="absolute inset-y-0 left-3 flex items-center pointer-events-none">
                  <Mail size={16} className="text-on-surface-variant" />
                </div>
                <input
                  type="email"
                  required
                  autoComplete="email"
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                  placeholder={t('AUTH.EMAIL_PLACEHOLDER', '邮箱')}
                  className="w-full h-11 bg-surface-container border border-white/10 rounded-control pl-10 pr-4 text-sm text-on-surface outline-none focus:border-primary/50 placeholder-on-surface-variant/50"
                />
              </div>
              <div className="relative">
                <div className="absolute inset-y-0 left-3 flex items-center pointer-events-none">
                  <Lock size={16} className="text-on-surface-variant" />
                </div>
                <input
                  type={showPwd ? 'text' : 'password'}
                  required
                  autoComplete="new-password"
                  minLength={8}
                  maxLength={72}
                  value={pwd}
                  onChange={(e) => setPwd(e.target.value)}
                  placeholder={t('AUTH.SIGNUP_PWD_PLACEHOLDER', '密码（至少 8 字符）')}
                  className="w-full h-11 bg-surface-container border border-white/10 rounded-control pl-10 pr-10 text-sm text-on-surface outline-none focus:border-primary/50 placeholder-on-surface-variant/50"
                />
                <button
                  type="button"
                  aria-label={t('COMMON.TOGGLE_PASSWORD_VISIBILITY', '切换密码可见性')}
                  onClick={() => setShowPwd(s => !s)}
                  className="absolute right-3 top-1/2 -translate-y-1/2 text-on-surface-variant"
                >
                  {showPwd ? <EyeOff size={16} /> : <Eye size={16} />}
                </button>
              </div>
              <input
                type={showPwd ? 'text' : 'password'}
                required
                autoComplete="new-password"
                minLength={8}
                maxLength={72}
                value={pwdConfirm}
                onChange={(e) => setPwdConfirm(e.target.value)}
                placeholder={t('AUTH.PWD_CONFIRM_PLACEHOLDER', '确认密码')}
                className="w-full h-11 bg-surface-container border border-white/10 rounded-control px-4 text-sm text-on-surface outline-none focus:border-primary/50 placeholder-on-surface-variant/50"
              />
              <button
                type="submit"
                disabled={loading || !email || !pwd || !emailUsername || pwd !== pwdConfirm}
                className="w-full h-11 mt-1 bg-gradient-to-r from-blue-600 to-cyan-500 text-on-surface font-semibold rounded-control flex items-center justify-center hover:opacity-90 disabled:opacity-50"
              >
                {loading ? t('AUTH.BTN_SIGNUP_LOADING', '注册中...') : t('AUTH.BTN_EMAIL_SIGNUP', '注册')}
              </button>
              <button type="button" className="text-xs text-on-surface-variant hover:underline" onClick={() => setStep('email-login')}>
                {t('AUTH.LINK_BACK_LOGIN', '已有账号？去登录')}
              </button>
            </form>
          )}

          {/* Phase G-2.6 忘记密码 */}
          {step === 'email-forgot' && (
            <form onSubmit={handleForgotPassword} className="w-full flex gap-4 flex-col">
              <p className="text-sm text-on-surface-variant">
                {t('AUTH.FORGOT_DESC', '请输入您的注册邮箱，我们会发送重置链接。')}
              </p>
              <div className="relative">
                <div className="absolute inset-y-0 left-3 flex items-center pointer-events-none">
                  <Mail size={16} className="text-on-surface-variant" />
                </div>
                <input
                  type="email"
                  required
                  autoComplete="email"
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                  placeholder={t('AUTH.EMAIL_PLACEHOLDER', '邮箱')}
                  className="w-full h-11 bg-surface-container border border-white/10 rounded-control pl-10 pr-4 text-sm text-on-surface outline-none focus:border-primary/50 placeholder-on-surface-variant/50"
                />
              </div>
              <button
                type="submit"
                disabled={loading || !email}
                className="w-full h-11 mt-1 bg-gradient-to-r from-blue-600 to-cyan-500 text-on-surface font-semibold rounded-control flex items-center justify-center hover:opacity-90 disabled:opacity-50"
              >
                {loading ? t('AUTH.BTN_FORGOT_LOADING', '发送中...') : t('AUTH.BTN_FORGOT', '发送重置链接')}
              </button>
              <button type="button" className="text-xs text-on-surface-variant hover:underline" onClick={() => setStep('email-login')}>
                {t('AUTH.LINK_BACK_LOGIN', '返回登录')}
              </button>
            </form>
          )}

          {/* Phase G-2.6 注册成功提示 */}
          {step === 'email-signup-sent' && (
            <div className="w-full text-center">
              <Mail size={36} className="mx-auto text-primary mb-4" />
              <h3 className="text-base font-semibold text-on-surface">
                {t('AUTH.SIGNUP_SENT_TITLE', '请到邮箱完成验证')}
              </h3>
              <p className="text-sm text-on-surface-variant mt-3">
                {t('AUTH.SIGNUP_SENT_DESC', '我们已经向您的邮箱发送了验证链接。点击邮件中的链接完成验证后即可登录。')}
              </p>
              <button
                onClick={() => setStep('email-login')}
                className="mt-6 h-10 px-6 bg-primary text-on-primary rounded-control text-sm font-medium"
              >
                {t('AUTH.BTN_GOTO_LOGIN', '去登录')}
              </button>
            </div>
          )}

          {/* Phase G-2.6 忘记密码已发送提示 */}
          {step === 'email-forgot-sent' && (
            <div className="w-full text-center">
              <Mail size={36} className="mx-auto text-primary mb-4" />
              <h3 className="text-base font-semibold text-on-surface">
                {t('AUTH.FORGOT_SENT_TITLE', '请查收邮件')}
              </h3>
              <p className="text-sm text-on-surface-variant mt-3">
                {t('AUTH.FORGOT_SENT_DESC', '如该邮箱对应的账号存在并已验证，重置链接已发送。请查收并按邮件指引完成密码重置。')}
              </p>
              <button
                onClick={() => setStep('email-login')}
                className="mt-6 h-10 px-6 bg-primary text-on-primary rounded-control text-sm font-medium"
              >
                {t('AUTH.BTN_BACK_LOGIN', '返回登录')}
              </button>
            </div>
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
