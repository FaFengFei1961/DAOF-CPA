import React, { useState, useEffect } from 'react';
import Sidebar from './components/Sidebar';
import TopBar from './components/TopBar';
import Dashboard from './components/Dashboard';
import StatisticsDash from './components/StatisticsDash';
import PricingDash from './components/PricingDash';
import RequireAuth from './components/RequireAuth';
import AuthModal from './components/AuthModal';
import Settings from './components/Settings';
import AdminSecretLogin from './components/AdminSecretLogin';
import TokenManager from './components/TokenManager';
import CreditsPoolCard from './components/CreditsPoolCard';
import UpgradePage from './components/UpgradePage';
// "我的产品"已合并进 UpgradePage（产品中心 → 我的 tab），不再单独路由
// MySubscriptions 仍在 UpgradePage 里以 embedded 形式被渲染
import NotificationCenter from './components/NotificationCenter';
import Topup from './components/Topup';
import TopupResult from './components/TopupResult';
import Tickets from './components/Tickets';
import BillsPage from './components/BillsPage';
import { useTranslation } from 'react-i18next';
import toast, { Toaster } from 'react-hot-toast';
import { ConfirmProvider } from './context/ConfirmContext';
import { logger } from './utils/logger';
import { Home, KeySquare, Settings as SettingsIcon, BarChart2, CreditCard, Package, Receipt, MessageSquare, MoreHorizontal, X } from 'lucide-react';

function App() {
  const { t } = useTranslation();
  const [isAuthenticated, setIsAuthenticated] = useState(() => !!localStorage.getItem('daof_token'));
  const [authModalConfig, setAuthModalConfig] = useState({ isOpen: false, step: 'github', tmpToken: '', loading: false, defaultName: '' });
  // H-6 修复：currentView 从 URL hash 恢复，刷新不丢失页面（同时支持 admin 直接访问 #settings）
  const [currentView, setCurrentView] = useState(() => {
    // 支持形如 #topup_result?status=success 的 hash（query 放在 hash 后）
    const rawHash = window.location.hash.replace('#', '').trim();
    const hash = rawHash.split('?')[0];
    const allowedViews = ['dashboard', 'tokens', 'stats', 'pricing', 'upgrade', 'topup', 'topup_result', 'tickets', 'bills', 'settings'];
    if (allowedViews.includes(hash)) return hash;
    return localStorage.getItem('daof_admin_unlocked') === '1' ? 'settings' : 'dashboard';
  });

  // 同步 currentView → URL hash
  // fix Critical Codex UX 审查（第二十五轮 #4）：原实现把 `#topup_result?status=success`、
  // `#upgrade?pane=mine` 等带 query 的 hash 直接覆盖成裸 view，导致深链 query 丢失。
  // 改为：当前 hash 的 view 与 currentView 一致时**保留** query 部分（query 由组件自己消费）；
  // view 切换时才重写为裸 hash。
  useEffect(() => {
    const rawHash = window.location.hash.replace('#', '');
    const [hashView] = rawHash.split('?');
    if (hashView === currentView) {
      return; // view 一致，保留可能存在的 query
    }
    const newHash = '#' + currentView;
    if (window.location.hash !== newHash) {
      window.history.replaceState(null, '', newHash);
    }
  }, [currentView]);

  // 上帝模式与站点封锁逻辑
  // L-5 修复：把 URL/localStorage 副作用移出 render，仅在挂载时执行一次
  // sysParam 仍在 render 时计算（用于条件渲染），但仅读取 URL 不写副作用
  const sysParam = React.useMemo(() => new URLSearchParams(window.location.search).get('sys'), []);

  useEffect(() => {
    // 推荐人 username（拉新链接 ?ref=xxx）：进站时保存到 sessionStorage
    const refFromUrl = new URLSearchParams(window.location.search).get('ref');
    if (refFromUrl) {
      sessionStorage.setItem('daof_ref', refFromUrl.trim().slice(0, 32));
    }
  }, []);

  const [godModeUnlocked, setGodModeUnlocked] = useState(() => localStorage.getItem('daof_admin_unlocked') === '1');
  const [mobileMoreOpen, setMobileMoreOpen] = useState(false);

  const [sysCheckStatus, setSysCheckStatus] = useState({ loading: true, setupNeeded: false });
  const [banAlert, setBanAlert] = useState({ isOpen: false, message: '' });
  const [globalProfile, setGlobalProfile] = useState(null);

  // 全局事件 'user-profile-refresh'：任何子组件触发后立即重新拉 /api/user/me
  // 充值到账、订阅购买、admin 调额等场景都可触发，避免顶栏余额陈旧
  useEffect(() => {
    const refresh = async () => {
      const userToken = localStorage.getItem('daof_token');
      if (!userToken) return;
      try {
        const res = await fetch('/api/user/me', { headers: { 'Authorization': `Bearer ${userToken}` } });
        const data = await res.json();
        if (data.success) setGlobalProfile(data.data);
      } catch { /* 静默 */ }
    };
    window.addEventListener('user-profile-refresh', refresh);
    return () => window.removeEventListener('user-profile-refresh', refresh);
  }, []);

  // 令牌存活期前置校验
  useEffect(() => {
    // 验证普通用户 token（仍然 localStorage，前端要靠它调 LLM API）
    const verifyUserToken = async (token) => {
      try {
        const res = await fetch('/api/user/me', {
          headers: { 'Authorization': `Bearer ${token}` }
        });
        const data = await res.json();
        if (data.success) {
           setGlobalProfile(data.data);
        }
        if (!data.success) {
           localStorage.removeItem('daof_token');
           setIsAuthenticated(false);
           if (res.status === 401 || data.message_code === 'ERR_BANNED' || (data.message && data.message.includes('封禁'))) {
               setBanAlert({ isOpen: true, reason: data.ban_reason || (data.message ? data.message.replace("账户被封禁", "").replace("理由：", "").trim() : "") });
           }
        }
      } catch (e) {
          // 忽略网络错误，不清空
      }
    };

    // 验证 admin cookie：调一个轻量的 admin 接口，由 cookie 自动鉴权
    const verifyAdminCookie = async () => {
      // 出厂重置进行中：跳过本轮验证，避免轮询触发 401 闪烁
      if (sessionStorage.getItem('daof_factory_resetting') === '1') return;
      try {
        const res = await fetch('/api/admin/config', { credentials: 'include' });
        if (res.status === 401 || res.status === 403) {
          // cookie 失效或被清，回退普通态
          localStorage.removeItem('daof_admin_unlocked');
          setGodModeUnlocked(false);
        }
      } catch (e) {
        // 网络错误，不清空
      }
    };

    // fix Minor（codex 第四轮）：原实现把 userToken/adminUnlocked 在 effect 顶层一次性
    // 读取并捕获到闭包里，导致用户登录/登出后 30s 轮询仍按"挂载时"的旧值判断，
    // ban / token rotate 感知失效直到刷新。
    // 改为每次 tick 重新从 localStorage 读取最新状态。
    const checkNow = () => {
        const tok = localStorage.getItem('daof_token');
        const adm = localStorage.getItem('daof_admin_unlocked') === '1';
        if (tok) verifyUserToken(tok);
        if (adm) verifyAdminCookie();
    };
    checkNow();

    // 30s 轮询：足够快感知 ban / token rotate，又不会每 10s 制造请求噪音
    const intervalId = setInterval(checkNow, 30000);

    const handleBanEvent = (e) => {
        localStorage.removeItem('daof_token');
        setIsAuthenticated(false);
        setBanAlert({ isOpen: true, message: e.detail });
    };
    window.addEventListener('daof_banned', handleBanEvent);
    
    return () => {
        clearInterval(intervalId);
        window.removeEventListener('daof_banned', handleBanEvent);
    };
  }, []);

  // 拦截 Github Callback
  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    const code = params.get('code');
    const state = params.get('state') || '';
    if (code) {
      // 把"我是哪个推荐人带来的"从 sessionStorage 读出来（landing page 进站时存的）
      const ref = sessionStorage.getItem('daof_ref') || '';
      window.history.replaceState({}, document.title, "/");
      setAuthModalConfig({ isOpen: true, step: 'github', tmpToken: '', loading: true, defaultName: '' });

      fetch('/api/auth/github', {
        method: 'POST',
        credentials: 'include', // OAuth state cookie 必须随请求带回后端比对
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ code, state, ref })
      }).then(async (res) => {
        // 5xx HTML 错误页直接走 catch；4xx 仍走 JSON 路径以读出 message_code
        const ct = res.headers.get('content-type') || '';
        if (!ct.includes('application/json')) {
          throw new Error(`HTTP ${res.status}：服务端返回非 JSON 响应`);
        }
        return res.json();
      }).then(data => {
        if (data.success) {
          localStorage.setItem('daof_token', data.token);
          setIsAuthenticated(true);
          setAuthModalConfig(prev => ({ ...prev, isOpen: false }));
          fetch('/api/user/me', { headers: { 'Authorization': `Bearer ${data.token}` }})
            .then(r => r.ok ? r.json() : null)
            .then(d => { if (d?.success) setGlobalProfile(d.data); })
            .catch(err => { /* 登录已成功，profile 拉取失败不阻塞，下次自动 poll 会补 */ logger.warn('[profile] post-login fetch failed', err); });
        } else if (data.action === 'require_sms_bind') {
          setAuthModalConfig({ isOpen: true, step: 'bind', tmpToken: data.tmp_token, loading: false, defaultName: '' });
        } else if (data.action === 'require_profile_setup') {
          setAuthModalConfig({ isOpen: true, step: 'profile', tmpToken: data.tmp_token, loading: false, defaultName: data.default_name || '' });
        } else {
          if (data.message_code === 'ERR_BANNED' || (data.message && data.message.includes('封禁'))) {
              setAuthModalConfig(prev => ({ ...prev, isOpen: false }));
              setBanAlert({ isOpen: true, reason: data.ban_reason || (data.message ? data.message.replace("账户被封禁", "").replace("理由：", "").trim() : "") });
          } else {
              toast.error((data.message_code ? t('API.' + data.message_code) : data.message) || t('APP.GITHUB_OAUTH_FAILED'));
              setAuthModalConfig({ isOpen: true, step: 'github', tmpToken: '', loading: false, defaultName: '' });
          }
        }
      }).catch(() => {
        toast.error(t('APP.LOGIN_NET_ERROR'));
        setAuthModalConfig({ isOpen: true, step: 'github', tmpToken: '', loading: false, defaultName: '' });
      });
    }
  }, []);

  // 探测系统状态：仅取 setup_required 用于决定是否进入引导态。
  useEffect(() => {
    const checkSys = async () => {
      try {
        const response = await fetch('/api/root/check-sys', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
        });
        const data = await response.json();
        setSysCheckStatus({
          loading: false,
          setupNeeded: !!data.setup_required
        });
      } catch (e) {
        setSysCheckStatus({ loading: false, setupNeeded: false });
      }
    };
    checkSys();
  }, [sysParam]);

  useEffect(() => {
    if (!mobileMoreOpen) return;
    const handleKeyDown = (event) => {
      if (event.key === 'Escape') {
        setMobileMoreOpen(false);
      }
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [mobileMoreOpen]);

  if (sysCheckStatus.loading) {
    return <div className="min-h-screen bg-surface flex items-center justify-center text-outline">{t('APP.INITIALIZING')}</div>;
  }

  // 1. 首次安装态：必须带 sysParam 才能进入 setup 引导，否则锁站。
  // 后端 GodSetup 会自行判定首次安装态，无需前端再确认 sysParam 是否命中。
  if (sysCheckStatus.setupNeeded) {
    if (sysParam && !godModeUnlocked) {
       return <AdminSecretLogin sysParam={sysParam} setupMode={true} onSuccess={() => setGodModeUnlocked(true)} />;
    }
    return (
      <div className="min-h-screen bg-surface flex flex-col items-center justify-center text-center p-6">
        <h1 className="text-2xl font-semibold text-on-surface-variant mb-2">{t('APP.SERVICE_UNAVAILABLE.TITLE')}</h1>
        <p className="text-outline-variant">{t('APP.SERVICE_UNAVAILABLE.DESC')}</p>
      </div>
    );
  }

  // 2. 正常态：带 sysParam 即渲染管理员登录页。
  // 是否命中真实用户名由后端 GodLogin 校验密码时决定，前端不区分。
  // 外网访客通过 cloudflare 访问时 LanGuard 会在 /api/root/* 层面直接拦截 403。
  if (sysParam && !godModeUnlocked) {
    return <AdminSecretLogin sysParam={sysParam} setupMode={false} onSuccess={() => {
      setGodModeUnlocked(true);
      setIsAuthenticated(true);
      setCurrentView('settings');
    }} />;
  }

  return (
    <ConfirmProvider>
      <div className="min-h-screen bg-surface text-on-surface flex font-sans animate-in fade-in duration-500">
        {/* fix CRITICAL C-F2（gemini 第二十一轮 + WCAG 2.4.1 Bypass Blocks）：
            键盘 / 屏幕阅读器用户每次进站都需绕过 sidebar + topbar 才能到达主内容。
            可见性：默认 sr-only 隐藏；获焦时（按 Tab 第一下）显形为可点击锚点，跳转到 #main-content。 */}
        <a
          href="#main-content"
          className="sr-only focus:not-sr-only focus:absolute focus:top-2 focus:left-2 focus:z-[200] focus:px-4 focus:py-2 focus:bg-primary focus:text-on-primary focus:rounded-lg focus:shadow-lg focus:outline focus:outline-2 focus:outline-offset-2 focus:outline-primary"
        >
          跳至主要内容
        </a>
        <Toaster
          position="top-center"
          containerStyle={{ top: 16 }}
          toastOptions={{
            style: {
              background: 'var(--color-surface-container-high)',
              color: 'var(--color-on-surface)',
              border: '1px solid var(--color-outline-variant)'
            }
          }}
        />
      {!godModeUnlocked && (
      <Sidebar
        currentView={currentView} 
        onNav={(v) => setCurrentView(v)} 
        isAdmin={godModeUnlocked} 
      />
      )}
      
      {/* Main Content Area: Offset by sidebar width (88px) only on desktop */}
      <div className={`flex-1 ${godModeUnlocked ? '' : 'md:ml-16'} flex flex-col h-screen overflow-y-auto pb-20 md:pb-8`}>
        <TopBar
          isAuthenticated={isAuthenticated}
          onOpenAuth={() => setAuthModalConfig({ isOpen: true, step: 'github', tmpToken: '', loading: false, defaultName: '' })}
          isAdmin={godModeUnlocked}
          profile={globalProfile}
          onNavigate={setCurrentView}
        />
        
        <main id="main-content" tabIndex="-1" className="flex-1 w-full max-w-[1600px] 2xl:max-w-none mx-auto px-3 sm:px-6 lg:px-8 xl:px-10 mt-2 sm:mt-4 focus:outline-none">
          {(() => {
            // admin 通过 cookie 鉴权，user 通过 Bearer token；两者任一即视为已登录
            const authed = isAuthenticated || godModeUnlocked;
            const openAuth = () =>
              setAuthModalConfig({ isOpen: true, step: 'github', tmpToken: '', loading: false, defaultName: '' });

            switch (currentView) {
              case 'dashboard':
                // 公开页面：未登录也展示模型列表，TopBar 已有登录按钮
                return <Dashboard isAuthenticated={authed} onNavigate={setCurrentView} />;
              case 'pricing':
                // 完全公开（用户决策买不买套餐前要看价格）
                return <PricingDash />;
              case 'stats':
                return (
                  <RequireAuth isAuthenticated={authed} onSignIn={openAuth}>
                    <StatisticsDash isAdmin={godModeUnlocked} isAuthenticated={authed} />
                  </RequireAuth>
                );
              case 'tokens':
                return (
                  <RequireAuth isAuthenticated={authed} onSignIn={openAuth}>
                    <TokenManager isAuthenticated={authed} />
                  </RequireAuth>
                );
              case 'upgrade':
                // 产品中心：套餐列表对未登录用户也可见（拉新场景）。
                // fix CRITICAL R23+2-F1（gemini 全方面审查）：之前被 RequireAuth 包裹导致未登录用户
                // 看到 inert + pointer-events-none 的灰色页面，连切 tab 都做不到。
                // UpgradePage.purchase() 内部已 isAuthenticated 校验，未登录点购买会弹 AuthModal。
                return <UpgradePage isAuthenticated={authed} onSignIn={openAuth} onPurchaseSuccess={() => { /* 内部已切到 mine */ }} />;
              case 'topup':
                return (
                  <RequireAuth isAuthenticated={authed} onSignIn={openAuth}>
                    <Topup isAuthenticated={authed} onNavigate={setCurrentView} />
                  </RequireAuth>
                );
              case 'bills':
                return (
                  <RequireAuth isAuthenticated={authed} onSignIn={openAuth}>
                    <BillsPage />
                  </RequireAuth>
                );
              case 'topup_result':
                return <TopupResult onNavigate={setCurrentView} />;
              case 'tickets':
                return (
                  <RequireAuth isAuthenticated={authed} onSignIn={openAuth}>
                    <Tickets />
                  </RequireAuth>
                );
              case 'settings':
                return <Settings isAdmin={godModeUnlocked} isAuthenticated={authed} />;
              default:
                return null;
            }
          })()}
        </main>
      </div>

      <AuthModal 
        isOpen={authModalConfig.isOpen} 
        initialStep={authModalConfig.step}
        tmpToken={authModalConfig.tmpToken}
        initialLoading={authModalConfig.loading}
        defaultName={authModalConfig.defaultName}
        onClose={() => setAuthModalConfig(prev => ({ ...prev, isOpen: false }))} 
        onLoginSuccess={() => {
          setAuthModalConfig(prev => ({ ...prev, isOpen: false }));
          setIsAuthenticated(true);
          // H-4 修复：变量名不能用 t（遮蔽 useTranslation 的 t）
          const userToken = localStorage.getItem('daof_token');
          if (userToken) {
            fetch('/api/user/me', { headers: { 'Authorization': `Bearer ${userToken}` }})
              .then(r => r.json())
              .then(d => { if(d.success) setGlobalProfile(d.data); })
              .catch(() => { /* network error swallowed; UI stays in current state */ });
          }
        }}
      />

      {/* 封禁拦截全屏弹窗 */}
      {banAlert.isOpen && (
          <div className="fixed inset-0 z-[9999] flex items-center justify-center bg-black/90 backdrop-blur-md animate-in fade-in zoom-in duration-300">
              <div className="bg-surface-container-high border border-red-900/50 rounded-2xl w-full max-w-md p-8 shadow-[0_0_80px_rgba(220,38,38,0.2)] text-center relative overflow-hidden">
                 <div className="absolute top-0 right-0 w-48 h-48 bg-red-600/10 rounded-full blur-3xl -mr-20 -mt-20 pointer-events-none"></div>
                 <div className="w-20 h-20 bg-red-900/30 rounded-full flex items-center justify-center mx-auto mb-6 relative z-10">
                    <div className="w-12 h-12 bg-red-600 rounded-full flex items-center justify-center shadow-lg shadow-red-600/30 text-on-surface font-bold text-3xl">!</div>
                 </div>
                 <h2 className="text-2xl font-bold text-on-surface tracking-tight mb-2 relative z-10">{t('APP.BANNED.TITLE')}</h2>
                 {banAlert.reason && (
                     <div className="mt-4 p-4 rounded-xl bg-red-900/40 border border-red-500/30 text-red-200 text-sm italic">
                        {banAlert.reason}
                     </div>
                 )}
                 <button 
                    onClick={() => {
                        setBanAlert({ isOpen: false, message: '', reason: '' })
                        window.location.href = '/'
                    }}
                    className="w-full h-12 mt-6 bg-surface-variant hover:bg-white hover:text-black font-semibold text-on-surface-variant rounded-xl transition-all border border-outline shadow-sm relative z-10"
                 >
                    {t('APP.BANNED.ACCEPT_BTN')}
                 </button>
              </div>
          </div>
      )}

      {/* Mobile Bottom Navigation - Only visible below md breakpoint */}
      {!godModeUnlocked && (
        <>
          {mobileMoreOpen && (
            <div className="md:hidden fixed inset-0 z-[95]" role="presentation">
              <button
                type="button"
                aria-label={t('COMMON.CLOSE', '关闭')}
                onClick={() => setMobileMoreOpen(false)}
                className="absolute inset-0 w-full h-full bg-black/45"
              />
              <section
                role="dialog"
                aria-modal="true"
                aria-labelledby="mobile-more-title"
                className="absolute left-3 right-3 bottom-[72px] rounded-2xl border border-outline-variant bg-surface-container shadow-2xl overflow-hidden animate-in fade-in slide-in-from-bottom-2"
              >
                <div className="flex items-center justify-between px-4 py-3 border-b border-outline-variant/60">
                  <h2 id="mobile-more-title" className="text-sm font-semibold text-on-surface">
                    {t('MENU.MORE', '更多')}
                  </h2>
                  <button
                    type="button"
                    onClick={() => setMobileMoreOpen(false)}
                    aria-label={t('COMMON.CLOSE', '关闭')}
                    className="w-8 h-8 rounded-lg flex items-center justify-center text-on-surface-variant hover:bg-on-surface/[0.06] hover:text-on-surface"
                  >
                    <X size={18} />
                  </button>
                </div>
                <div className="grid grid-cols-2 gap-2 p-3">
                  {[
                    { id: 'pricing',  icon: CreditCard,   label: t('MENU.PRICING', '费率与模型') },
                    { id: 'stats',    icon: BarChart2,    label: t('MENU.STATS', '数据看板') },
                    { id: 'bills',    icon: Receipt,      label: t('MENU.BILLS', '账单') },
                    { id: 'settings', icon: SettingsIcon, label: t('MENU.SETTINGS', '系统设置') },
                  ].map(item => {
                    const Icon = item.icon;
                    const active = currentView === item.id;
                    return (
                      <button
                        key={item.id}
                        type="button"
                        onClick={() => {
                          setCurrentView(item.id);
                          setMobileMoreOpen(false);
                        }}
                        aria-current={active ? 'page' : undefined}
                        className={`min-h-16 rounded-xl border px-3 py-3 text-left flex items-center gap-3 transition active:scale-[0.98] focus-visible:ring-2 focus-visible:ring-primary ${
                          active
                            ? 'bg-primary-container border-primary/40 text-on-primary-container'
                            : 'bg-surface-container-high border-outline-variant text-on-surface hover:border-primary/60'
                        }`}
                      >
                        <Icon size={20} className={active ? 'text-primary' : 'text-on-surface-variant'} />
                        <span className="text-sm font-medium leading-tight">{item.label}</span>
                      </button>
                    );
                  })}
                </div>
              </section>
            </div>
          )}
          <nav aria-label={t('MOBILE_NAV.BOTTOM_LABEL', '底部导航')} className="md:hidden fixed bottom-0 left-0 right-0 h-[60px] bg-surface/95 backdrop-blur-md border-t border-outline-variant flex items-center justify-around z-[100] pb-1">
            {[
              { id: 'dashboard', icon: Home,          label: t('MENU.DASHBOARD', '仪表盘') },
              { id: 'tokens',    icon: KeySquare,     label: t('MENU.TOKENS', 'API 令牌') },
              { id: 'upgrade',   icon: Package,       label: t('MENU.PRODUCTS', '产品中心') },
              { id: 'topup',     icon: CreditCard,    label: t('MENU.TOPUP', '充值') },
              { id: 'tickets',   icon: MessageSquare, label: t('MENU.TICKETS', '工单') },
              { id: 'more',      icon: MoreHorizontal, label: t('MENU.MORE', '更多') },
            ].map(item => {
              const Icon = item.icon;
              const active = item.id === 'more'
                ? mobileMoreOpen || ['pricing', 'stats', 'bills', 'settings'].includes(currentView)
                : currentView === item.id;
              return (
                <button
                  key={item.id}
                  type="button"
                  onClick={() => {
                    if (item.id === 'more') {
                      setMobileMoreOpen(open => !open);
                    } else {
                      setCurrentView(item.id);
                      setMobileMoreOpen(false);
                    }
                  }}
                  aria-label={item.label}
                  aria-current={active && item.id !== 'more' ? 'page' : undefined}
                  aria-expanded={item.id === 'more' ? mobileMoreOpen : undefined}
                  className="flex flex-col items-center gap-1 p-2 cursor-pointer transition-transform active:scale-95 bg-transparent border-0 outline-none focus-visible:ring-2 focus-visible:ring-primary rounded-md"
                >
                  <Icon size={22} className={active ? 'text-primary' : 'text-on-surface-variant'} />
                  <span className={`text-[10px] font-medium leading-none ${active ? 'text-primary' : 'text-on-surface-variant'}`}>{item.label}</span>
                </button>
              );
            })}
          </nav>
        </>
      )}

      </div>
    </ConfirmProvider>
  );
}

export default App;
