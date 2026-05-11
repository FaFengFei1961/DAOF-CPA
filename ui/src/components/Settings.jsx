import React, { useState, useEffect, useRef, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { useModalA11y } from '../hooks/useModalA11y';
import { ShieldAlert, Save, Eye, EyeOff, KeyRound, Monitor, Key, MessageSquare, ShieldCheck, Users, User, Globe, Network, Server, BarChart3, AlertOctagon, X, Activity, Layers, Bell, Wallet, Receipt, Package as PackageIcon, Shield, RefreshCw } from 'lucide-react';
import toast from 'react-hot-toast';
import UserManagement from './UserManagement';
import UserUsageDash from './UserUsageDash';
import AccountProfile from './AccountProfile';
import I18nManagement from './I18nManagement';
import ChannelManagement from './ChannelManagement';
import CreditsMonitor from './CreditsMonitor';
import QuotaPlanManagement from './QuotaPlanManagement';
import PackageManagement from './PackageManagement';
import CouponManagement from './CouponManagement';
import UserCoupons from './UserCoupons';
import AdminNotificationManagement from './AdminNotificationManagement';
import AdminPaymentChannels from './AdminPaymentChannels';
import AdminTopupOrders from './AdminTopupOrders';
import AdminSubscriptions from './AdminSubscriptions';
import AdminCustomerMessages from './AdminCustomerMessages';
import NotificationPreferences from './NotificationPreferences';
import ContentModerationGlobals from './ContentModerationGlobals';
import { useTheme } from '../context/ThemeContext';

const Settings = ({ isAdmin, isAuthenticated }) => {
  const { themePref, changeTheme, seedColor, changeSeedColor, isDarkMode } = useTheme();
  const { t } = useTranslation();
  const [activeTab, setActiveTab] = useState('general');
  const [financeTab, setFinanceTab] = useState('settings');
  const [showClipKey, setShowClipKey] = useState(false);
  const [configs, setConfigs] = useState({
    github_client_id: '',
    github_client_secret: '',
    aliyun_access_key: '',
    aliyun_access_secret: '',
    aliyun_sms_sign: '',
    aliyun_sms_template: '',
    reg_strategy: 'dynamic',
    reg_ip_limit: '3',
    max_users: '0',
    signup_bonus: '1',
    referrer_bonus: '0',
    referee_bonus: '0',
    signup_coupon_template_id: '0',
    server_address: '',
    exchange_rate: '',
    balance_consume_default_enabled: 'false',
    balance_consume_default_limit_usd: '0',
    balance_consume_default_window_secs: '2592000',
    cliproxy_url: '',
    cliproxy_key: '',
    credits_refresh_interval: '15',
    credits_max_retries: '3',
    credits_retry_interval: '5'
  });

  // 保存 CLIProxyAPI 连接配置到 Go 后端加密存储。
  // 复用统一的 handleSave 逻辑，避免两个独立 POST 路径互相覆盖。
  const saveClipProxySettings = async () => {
    if (!(configs.cliproxy_url || '').trim()) {
      toast.error('CLIProxyAPI 服务地址不能为空');
      return;
    }
    await handleSave({
      cliproxy_url: (configs.cliproxy_url || '').trim(),
      cliproxy_key: (configs.cliproxy_key || '').trim()
    }, 'CLIProxyAPI 连接配置已安全保存到服务端');
  };

  const [showMask, setShowMask] = useState({});
  const [loading, setLoading] = useState(false);
  const [couponTemplates, setCouponTemplates] = useState([]);
  const [couponTemplatesLoading, setCouponTemplatesLoading] = useState(false);

  // 出厂重置弹窗（多了 password 字段做二次鉴权）
  const [resetModal, setResetModal] = useState({ open: false, confirmText: '', password: '', loading: false });

  // fix Major M8（gemini 第十五轮）：原 resetModal 无 ESC + 背景点击关闭
  // 用 useModalA11y hook 统一行为；loading 期间禁用关闭防误操作
  const closeResetModal = () => {
    if (!resetModal.loading) {
      setResetModal({ open: false, confirmText: '', password: '', loading: false });
    }
  };
  const resetModalRef = useRef(null); // C-F1 第二十一轮: focus trap 范围
  const { onBackdropClick: onResetBackdropClick } = useModalA11y(resetModal.open, closeResetModal, undefined, resetModalRef);
  const performFactoryReset = async () => {
    if (resetModal.confirmText !== 'FACTORY_RESET') {
      toast.error('请精确输入 FACTORY_RESET 才能执行');
      return;
    }
    if (!resetModal.password) {
      toast.error('需要输入当前管理员密码做二次鉴权');
      return;
    }
    setResetModal(prev => ({ ...prev, loading: true }));
    try {
      const res = await fetch('/api/admin/factory-reset', {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ confirm: 'FACTORY_RESET', password: resetModal.password }),
      });
      const data = await res.json();
      if (data.success) {
        // 立刻打断所有同步轮询，避免重置完成期间的 401 触发 godModeUnlocked 闪烁
        // 用 sessionStorage 标志位让 App.jsx 的 verifyAdminCookie 跳过本次轮询
        sessionStorage.setItem('daof_factory_resetting', '1');
        toast.success('平台已恢复出厂设置，3 秒后跳转到 setup 入口...', { duration: 3000 });
        localStorage.removeItem('daof_token');
        localStorage.removeItem('daof_admin_unlocked');
        setTimeout(() => {
          sessionStorage.removeItem('daof_factory_resetting');
          window.location.href = '/?sys=root';
        }, 3000);
      } else {
        toast.error((data.message_code ? t('API.' + data.message_code) : data.message) || '出厂重置失败');
        setResetModal(prev => ({ ...prev, loading: false }));
      }
    } catch (e) {
      toast.error('网络异常，出厂重置失败');
      setResetModal(prev => ({ ...prev, loading: false }));
    }
  };

  // 挂载时去 Go 后台把已存的配置拉取回来
  useEffect(() => {
    if (!isAdmin) return; // 只有管理员才去拉取机密配置！
    const fetchConfigs = async () => {
      try {
          const response = await fetch('/api/admin/config', { credentials: 'include' });
          const data = await response.json();
        if(data.success && data.data) {
          setConfigs(prev => ({ ...prev, ...data.data }));
        }
      } catch (e) {
        toast.error('加载系统配置失败');
      }
    };
    fetchConfigs();
  }, [isAdmin]);

  const fetchCouponTemplates = useCallback(async () => {
    if (!isAdmin) return;
    setCouponTemplatesLoading(true);
    try {
      const response = await fetch('/api/admin/coupon-templates', { credentials: 'include' });
      const data = await response.json();
      if (data.success) {
        setCouponTemplates(data.data || []);
      } else {
        toast.error((data.message_code ? t('API.' + data.message_code) : data.message) || t('COUPON.LOAD_FAIL', '加载失败'));
      }
    } catch {
      toast.error(t('API.ERR_NETWORK', '网络异常'));
    } finally {
      setCouponTemplatesLoading(false);
    }
  }, [isAdmin, t]);

  useEffect(() => {
    if (activeTab === 'coupons') {
      fetchCouponTemplates();
    }
  }, [activeTab, fetchCouponTemplates]);

  const handleChange = (key, val) => {
    setConfigs(prev => ({ ...prev, [key]: val }));
  };

  const toggleMask = (key) => {
    setShowMask(prev => ({ ...prev, [key]: !prev[key] }));
  };

  const openFinanceSettings = () => {
    setFinanceTab('settings');
    setActiveTab('finance');
  };

  // 校验号池采集器三个数值配置范围。返回错误数组（空数组 = 通过）。
  const validateCreditsConfig = (cfg) => {
    const errors = [];
    const refresh = parseInt(cfg.credits_refresh_interval, 10);
    const retries = parseInt(cfg.credits_max_retries, 10);
    const retry = parseInt(cfg.credits_retry_interval, 10);
    if (cfg.credits_refresh_interval !== undefined && (Number.isNaN(refresh) || refresh < 1 || refresh > 1440)) {
      errors.push('号池刷新周期必须是 1-1440 分钟');
    }
    if (cfg.credits_max_retries !== undefined && (Number.isNaN(retries) || retries < 0 || retries > 100)) {
      errors.push('号池失败重试次数必须是 0-100 之间的整数');
    }
    if (cfg.credits_retry_interval !== undefined && (Number.isNaN(retry) || retry < 1 || retry > 1440)) {
      errors.push('号池重试间隔必须是 1-1440 分钟');
    }
    if (cfg.balance_consume_default_enabled !== undefined) {
      const enabled = String(cfg.balance_consume_default_enabled).trim().toLowerCase();
      if (!['true', 'false'].includes(enabled)) {
        errors.push('新用户余额消费默认开关必须是 true/false');
      }
    }
    if (cfg.balance_consume_default_limit_usd !== undefined) {
      const limit = parseFloat(cfg.balance_consume_default_limit_usd);
      if (Number.isNaN(limit) || !Number.isFinite(limit) || limit < 0) {
        errors.push('新用户余额消费默认限额必须 ≥ 0');
      }
    }
    if (cfg.balance_consume_default_window_secs !== undefined) {
      const windowSecs = parseInt(cfg.balance_consume_default_window_secs, 10);
      if (Number.isNaN(windowSecs) || windowSecs < 60 || windowSecs > 365 * 24 * 60 * 60) {
        errors.push('新用户余额消费默认窗口必须在 60 秒到 365 天之间');
      }
    }
    return errors;
  };

  const saveSignupCouponSetting = async () => {
    await handleSave(
      { signup_coupon_template_id: configs.signup_coupon_template_id || '0' },
      t('SETTINGS.SIGNUP_COUPON_SAVE_OK', '新人券配置已保存')
    );
  };

  // 统一保存逻辑。partialPayload 给定时只 POST 该子集（用于 saveClipProxySettings），否则 POST 全部 configs。
  const handleSave = async (partialPayload = null, successMsg = null) => {
    const payload = partialPayload || configs;
    const errs = validateCreditsConfig(payload);
    if (errs.length > 0) {
      toast.error(errs.join('；'));
      return;
    }
    setLoading(true);
    try {
      const response = await fetch('/api/admin/config', {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      });
      const data = await response.json();
      if (data.success) {
        const msg = successMsg
          || (data.message_code ? t('API.' + data.message_code) : null)
          || data.message
          || t('SETTINGS.SAVE_SUCCESS', '保存成功');
        toast.success(msg);
      } else {
        toast.error((data.message_code ? t('API.' + data.message_code) : data.message) || t('SETTINGS.SAVE_FAILED', '保存失败'));
      }
    } catch (e) {
      toast.error(t('SETTINGS.SAVE_FAILED', '保存失败'));
    } finally {
      setLoading(false);
    }
  };

  // 分组的菜单：把扁平的 tab 列表按职能分块，避免一长串看着累
  const menuGroups = [
    {
      title: t('SETTINGS.GROUP_PERSONAL', '个人'),
      items: [
        { id: 'general', label: t('SETTINGS.TAB_GENERAL'), icon: Monitor },
        ...(isAdmin || isAuthenticated
          ? [
              { id: 'account', label: t('SETTINGS.TAB_ACCOUNT'), icon: User },
              { id: 'notification_prefs', label: t('SETTINGS.TAB_NOTIFICATION_PREFS', '通知偏好'), icon: Bell },
              { id: 'my_coupons', label: t('SETTINGS.TAB_MY_COUPONS', '我的优惠券'), icon: PackageIcon },
            ]
          : []),
      ],
    },
    ...(isAdmin
      ? [
          {
            title: t('SETTINGS.GROUP_BUSINESS', '业务'),
            items: [
              { id: 'channels', label: t('MENU.CHANNELS', '渠道枢纽'), icon: Network },
              { id: 'credits_monitor', label: t('SETTINGS.TAB_CREDITS', '号池监控'), icon: Activity },
              { id: 'quota_plans', label: t('SETTINGS.TAB_QUOTA_PLANS', '配额计划库'), icon: Layers },
              { id: 'packages', label: t('SETTINGS.TAB_PACKAGES', '销售套餐'), icon: PackageIcon },
              { id: 'coupons', label: t('SETTINGS.TAB_COUPONS', '优惠券模板'), icon: PackageIcon },
              { id: 'finance', label: t('SETTINGS.TAB_FINANCE'), icon: ShieldAlert },
            ],
          },
          {
            title: t('SETTINGS.GROUP_USERS', '用户'),
            items: [
              { id: 'users', label: t('SETTINGS.TAB_USERS'), icon: Users },
              { id: 'user_usage', label: t('SETTINGS.TAB_USER_USAGE', '用户用量'), icon: BarChart3 },
            ],
          },
          {
            title: t('SETTINGS.GROUP_SYSTEM', '系统'),
            items: [
              { id: 'notifications', label: t('NOTIF.ADMIN.TAB', '通知管理'), icon: Bell },
              { id: 'admin_tickets', label: t('TICKET.ADMIN.TAB', '工单管理'), icon: MessageSquare },
              { id: 'i18n', label: t('SETTINGS.TAB_I18N'), icon: Globe },
              { id: 'oauth', label: t('SETTINGS.TAB_OAUTH'), icon: Key },
              { id: 'sms', label: t('SETTINGS.TAB_SMS'), icon: MessageSquare },
              { id: 'risk', label: t('SETTINGS.TAB_RISK'), icon: ShieldCheck },
              { id: 'moderation', label: t('SETTINGS.TAB_MODERATION', '内容审核'), icon: Shield },
            ],
          },
        ]
      : []),
  ];

  const financeTabs = [
    { id: 'settings', label: t('SETTINGS.FINANCE_TAB_SETTINGS', '基础设置'), icon: ShieldAlert },
    { id: 'payment_channels', label: t('SETTINGS.FINANCE_TAB_PAYMENT_CHANNELS', '支付通道'), icon: Wallet },
    { id: 'topup_orders', label: t('SETTINGS.FINANCE_TAB_TOPUP_ORDERS', '充值订单'), icon: Receipt },
    { id: 'admin_subscriptions', label: t('SETTINGS.FINANCE_TAB_SUBSCRIPTIONS', '订阅总览'), icon: PackageIcon },
  ];

  return (
    <div className="w-full h-full flex flex-col md:flex-row gap-6 animate-in fade-in slide-in-from-bottom-2">

      {/* 移动端：下拉切换 */}
      <div className="md:hidden -mx-4 px-4 py-3 sticky top-0 z-10 bg-surface/90 backdrop-blur-md border-b border-outline-variant">
        <select
          value={activeTab}
          onChange={(e) => setActiveTab(e.target.value)}
          className="w-full rounded-lg bg-surface-container border border-outline-variant text-on-surface text-sm px-3 py-2"
        >
          {menuGroups.map((g) => (
            <optgroup key={g.title} label={g.title}>
              {g.items.map((it) => (
                <option key={it.id} value={it.id}>{it.label}</option>
              ))}
            </optgroup>
          ))}
        </select>
      </div>

      {/* 桌面端：左侧菜单 — 用 acrylic 二级面板让整套看起来"嵌在 mica 大背景里" */}
      <aside className="hidden md:block w-56 shrink-0">
        <nav aria-label={t('SETTINGS.NAV_LABEL', '设置导航')} className="sticky top-16 space-y-5 fl-acrylic rounded-overlay p-3">
          {menuGroups.map((group) => (
            <div key={group.title}>
              <p className="px-3 mb-1.5 text-[11px] uppercase tracking-wider text-on-surface-variant/70 font-medium">
                {group.title}
              </p>
              <ul className="space-y-0.5">
                {group.items.map((it) => {
                  const Icon = it.icon;
                  const isActive = activeTab === it.id;
                  return (
                    <li key={it.id}>
                      <button
                        type="button"
                        aria-current={isActive ? 'page' : undefined}
                        onClick={() => setActiveTab(it.id)}
                        className={`w-full flex items-center gap-2.5 px-3 py-2 rounded-lg text-sm text-left transition
                          ${isActive
                            ? 'bg-primary-container text-on-primary-container font-medium'
                            : 'text-on-surface-variant hover:bg-surface-container'
                          }`}
                      >
                        <Icon size={16} className={isActive ? 'opacity-100' : 'opacity-70'} />
                        <span className="truncate">{it.label}</span>
                      </button>
                    </li>
                  );
                })}
              </ul>
            </div>
          ))}
        </nav>
      </aside>

      {/* 主面板替换区 */}
      <div className="flex-1 min-w-0 pb-12">
        
        {/* =========================================================
            常规设置 (任何人可见) 
            ========================================================= */}
        {activeTab === 'general' && (
          <div className="w-full">
            <div className="mb-8 border-b border-outline-variant pb-6">
              <h1 className="text-xl md:text-2xl font-bold tracking-tight text-on-surface flex items-center gap-3">
                {t('SETTINGS.GENERAL_TITLE')}
              </h1>
              <p className="text-on-surface-variant mt-2 text-sm">
                {t('SETTINGS.GENERAL_DESC')}
              </p>
            </div>

             <div className="bg-surface-container border border-outline-variant rounded-2xl p-4 md:p-6 mb-12 w-full">
               {/* 主题模式切换 */}
               <div className="flex flex-col md:flex-row md:items-center justify-between py-4 border-b border-outline-variant/30 gap-4">
                  <div className="flex flex-col gap-1">
                     <span className="text-on-surface font-medium">{t('SETTINGS.THEME_LABEL', '外观')}</span>
                     <span className="text-xs text-on-surface-variant">{t('SETTINGS.THEME_HINT', '深色 / 浅色 / 跟随系统')}</span>
                  </div>
                  {/* fix Minor Gemini UX 审查（第二十五轮 #5）：三选一互斥按钮组需要 radiogroup 语义，
                      否则屏幕阅读器把它们读成三个独立开关，无法表达"只能选一个"。 */}
                  <div
                    role="radiogroup"
                    aria-label={t('SETTINGS.THEME_LABEL', '外观')}
                    className="inline-flex rounded-lg border border-outline-variant bg-surface p-0.5 self-start md:self-auto"
                  >
                    {[
                      { v: 'light', label: t('SETTINGS.THEME_LIGHT', '浅色') },
                      { v: 'dark',  label: t('SETTINGS.THEME_DARK',  '深色') },
                      { v: 'system', label: t('SETTINGS.THEME_SYSTEM', '跟随系统') },
                    ].map(({ v, label }) => (
                      <button
                        key={v}
                        type="button"
                        role="radio"
                        aria-checked={themePref === v}
                        onClick={() => changeTheme(v)}
                        className={`px-3 py-1.5 text-sm rounded-md transition ${
                          themePref === v
                            ? 'bg-primary text-on-primary font-medium'
                            : 'text-on-surface-variant hover:text-on-surface'
                        }`}
                      >
                        {label}
                      </button>
                    ))}
                  </div>
               </div>

               {/* 主题色（seed） */}
               <div className="flex flex-col md:flex-row md:items-center justify-between py-4 border-b border-outline-variant/30 gap-4">
                  <div className="flex flex-col gap-1">
                     <span className="text-on-surface font-medium">{t('SETTINGS.SEED_COLOR_LABEL', '主题色')}</span>
                     <span className="text-xs text-on-surface-variant">{t('SETTINGS.SEED_COLOR_HINT', '选一个种子色，整套界面调色板自动生成')}</span>
                  </div>
                  <div className="flex items-center gap-2 flex-wrap">
                    {[
                      { hex: '#7c5cff', name: '紫' },
                      { hex: '#2563eb', name: '蓝' },
                      { hex: '#059669', name: '青' },
                      { hex: '#ea580c', name: '橙' },
                      { hex: '#dc2626', name: '红' },
                      { hex: '#0891b2', name: '湖' },
                      { hex: '#a16207', name: '金' },
                      { hex: '#475569', name: '灰' },
                    ].map(({ hex, name }) => (
                      <button
                        key={hex}
                        type="button"
                        onClick={() => changeSeedColor(hex)}
                        title={name}
                        aria-label={`主题色: ${name}`}
                        className={`w-7 h-7 rounded-full border-2 transition ${
                          seedColor.toLowerCase() === hex.toLowerCase()
                            ? 'border-on-surface scale-110'
                            : 'border-outline-variant hover:scale-110'
                        }`}
                        style={{ background: hex }}
                      />
                    ))}
                    <label
                      className="w-7 h-7 rounded-full border-2 border-dashed border-outline-variant flex items-center justify-center cursor-pointer hover:border-primary text-[10px] text-on-surface-variant"
                      title="自定义"
                    >
                      <input
                        type="color"
                        value={seedColor}
                        onChange={(e) => changeSeedColor(e.target.value)}
                        className="w-0 h-0 opacity-0"
                      />
                      ＋
                    </label>
                  </div>
               </div>
             </div>

            {isAdmin && (
            <div className="bg-surface-container border border-outline-variant rounded-2xl p-4 md:p-6 mb-6 shadow-sm w-full">
              <div className="flex items-center gap-3 mb-5 pb-4 border-b border-outline-variant/40">
                <Server size={18} className="text-primary" />
                <div>
                  <h3 className="text-sm font-semibold text-on-surface">CLIProxyAPI 连接配置</h3>
                  <p className="text-xs text-on-surface-variant mt-0.5">配置本地 CLIProxyAPI 服务地址和 Management Key，用于统计看板读取原生数据</p>
                </div>
              </div>

              <div className="flex flex-col md:flex-row md:items-center justify-between py-3 border-b border-outline-variant/20 gap-3">
                <div className="flex flex-col gap-1 w-full md:w-2/5">
                  <span className="text-on-surface-variant font-medium text-sm">服务地址</span>
                  <span className="text-xs text-outline">CLIProxyAPI 本地服务的 HTTP 地址</span>
                </div>
                <input
                  type="text"
                  value={configs.cliproxy_url}
                  onChange={e => handleChange('cliproxy_url', e.target.value)}
                  placeholder="http://127.0.0.1:8080"
                  className="bg-surface-container-high border border-outline text-on-surface rounded-lg px-4 py-2 outline-none text-sm w-full md:w-72 focus:border-primary transition-colors"
                />
              </div>

              <div className="flex flex-col md:flex-row md:items-center justify-between py-3 gap-3">
                <div className="flex flex-col gap-1 w-full md:w-2/5">
                  <span className="text-on-surface-variant font-medium text-sm">Management Key</span>
                  <span className="text-xs text-outline">config.yaml 中 remote-management.secret-key 或环境变量 MANAGEMENT_PASSWORD</span>
                </div>
                <div className="relative w-full md:w-72">
                  <input
                    type={showClipKey ? 'text' : 'password'}
                    value={configs.cliproxy_key}
                    onChange={e => handleChange('cliproxy_key', e.target.value)}
                    placeholder="输入 Management Key"
                    className="bg-surface-container-high border border-outline text-on-surface rounded-lg px-4 py-2 pr-10 outline-none text-sm w-full focus:border-primary transition-colors"
                  />
                  <button
                    type="button"
                    onClick={() => setShowClipKey(v => !v)}
                    className="absolute right-3 top-2.5 text-on-surface-variant hover:text-on-surface transition-colors"
                  >
                    {showClipKey ? <EyeOff size={16} /> : <Eye size={16} />}
                  </button>
                </div>
              </div>

              <div className="flex justify-end mt-4">
                <button
                  type="button"
                  onClick={saveClipProxySettings}
                  className="flex items-center gap-2 px-5 py-2 bg-primary text-on-primary rounded-full text-sm font-medium hover:opacity-90 active:scale-95 transition-all"
                >
                  <Save size={14} />
                  保存连接配置
                </button>
              </div>
            </div>
            )}

          </div>
        )}

        {/* =========================================================
            渠道与模型矩阵
            ========================================================= */}
        {isAdmin && activeTab === 'channels' && (
           <ChannelManagement />
        )}

        {/* =========================================================
            号池监控（CPA 凭证额度采集看板）
            ========================================================= */}
        {isAdmin && activeTab === 'credits_monitor' && (
           <CreditsMonitor />
        )}

        {/* =========================================================
            套餐订阅系统 admin 面板
            ========================================================= */}
        {isAdmin && activeTab === 'quota_plans' && <QuotaPlanManagement />}
        {isAdmin && activeTab === 'packages' && <PackageManagement />}
        {isAdmin && activeTab === 'coupons' && (
          <div className="w-full space-y-8">
            <section className="bg-surface-container border border-outline-variant rounded-2xl p-4 md:p-6 shadow-sm">
              <div className="flex flex-col gap-4 md:flex-row md:items-start md:justify-between">
                <div className="flex items-start gap-3">
                  <PackageIcon size={20} className="text-primary mt-0.5" />
                  <div>
                    <h2 className="text-base font-semibold text-on-surface">
                      {t('SETTINGS.SIGNUP_COUPON_TITLE', '新用户自动发券')}
                    </h2>
                    <p className="text-xs text-on-surface-variant mt-1 max-w-2xl">
                      {t('SETTINGS.SIGNUP_COUPON_DESC', '选择一个已启用的优惠券模板。新用户完成注册时会自动获得一张该模板的券；选择"不自动发放"则关闭此流程。')}
                    </p>
                  </div>
                </div>
                <button
                  type="button"
                  onClick={fetchCouponTemplates}
                  disabled={couponTemplatesLoading}
                  className="h-9 px-3 bg-surface-container-high border border-outline rounded-lg text-xs text-on-surface hover:border-primary disabled:opacity-50 flex items-center gap-2 self-start"
                >
                  <RefreshCw size={14} className={couponTemplatesLoading ? 'animate-spin' : ''} />
                  {t('SYSTEM.REFRESH', '刷新')}
                </button>
              </div>

              <div className="mt-5 flex flex-col md:flex-row md:items-center gap-3">
                <select
                  value={configs.signup_coupon_template_id || '0'}
                  onChange={(e) => handleChange('signup_coupon_template_id', e.target.value)}
                  className="h-10 flex-1 bg-surface-container-high border border-outline rounded-lg px-3 text-sm text-on-surface focus:border-primary outline-none"
                >
                  <option value="0">{t('SETTINGS.SIGNUP_COUPON_NONE', '不自动发放')}</option>
                  {couponTemplates.map((tpl) => (
                    <option key={tpl.id} value={String(tpl.id)} disabled={tpl.enabled === false}>
                      #{tpl.id} · {tpl.name}{tpl.enabled === false ? `（${t('COUPON.NO', '禁用')}）` : ''}
                    </option>
                  ))}
                </select>
                <button
                  type="button"
                  onClick={saveSignupCouponSetting}
                  disabled={loading || couponTemplatesLoading}
                  className="h-10 px-5 bg-primary text-on-primary rounded-lg text-sm font-medium hover:opacity-90 disabled:opacity-50 flex items-center justify-center gap-2"
                >
                  <Save size={16} />
                  {loading ? t('SETTINGS.BTN_SAVING') : t('SETTINGS.SIGNUP_COUPON_SAVE', '保存新人券')}
                </button>
              </div>
            </section>
            <CouponManagement />
          </div>
        )}
        {(isAdmin || isAuthenticated) && activeTab === 'my_coupons' && (
          <div className="w-full">
            <div className="mb-8 border-b border-outline-variant pb-6">
              <h1 className="text-xl md:text-2xl font-bold tracking-tight text-on-surface flex items-center gap-3">
                {t('COUPON.MY_TITLE', '我的优惠券')}
              </h1>
              <p className="text-on-surface-variant mt-2 text-sm max-w-2xl">
                {t('COUPON.MY_DESC', '查看你账户下的所有优惠券。可用券会在购买套餐时自动出现在选择列表里。')}
              </p>
            </div>
            <UserCoupons />
          </div>
        )}
        {isAdmin && activeTab === 'notifications' && <AdminNotificationManagement />}
        {isAdmin && activeTab === 'admin_tickets' && <AdminCustomerMessages />}

        {/* =========================================================
            个人档案配置
            ========================================================= */}
        {(isAdmin || isAuthenticated) && activeTab === 'account' && (
           <AccountProfile />
        )}

        {/* =========================================================
            通知偏好（独立 tab，避免与 AccountProfile 杂糅）
            ========================================================= */}
        {(isAdmin || isAuthenticated) && activeTab === 'notification_prefs' && (
           <div className="w-full">
             <div className="mb-8 border-b border-outline-variant pb-6">
               <h1 className="text-xl md:text-2xl font-bold tracking-tight text-on-surface flex items-center gap-3">
                 <Bell size={22} className="text-primary" />
                 {t('SETTINGS.TAB_NOTIFICATION_PREFS', '通知偏好')}
               </h1>
               <p className="text-on-surface-variant mt-2 text-sm">
                 {t('SETTINGS.NOTIFICATION_PREFS_DESC', '配置站内铃铛、邮件、短信等通知渠道的接收偏好')}
               </p>
             </div>
             <div className="bg-surface-container border border-outline-variant rounded-2xl p-6">
               <NotificationPreferences />
             </div>
           </div>
        )}

        {/* =========================================================
            用户管理配置
            ========================================================= */}
        {isAdmin && activeTab === 'users' && (
           <UserManagement />
        )}

        {/* =========================================================
            用户用量看板（按 user 聚合 ApiLog）
            ========================================================= */}
        {isAdmin && activeTab === 'user_usage' && (
           <UserUsageDash />
        )}

        {/* =========================================================
            语境管理配置
            ========================================================= */}
        {isAdmin && activeTab === 'i18n' && (
           <I18nManagement />
        )}

        {/* =========================================================
            内容审核（per-ChannelModel 风控的全局共享层）
            fix CRITICAL R23 (codex 第二十三轮反馈)：御三家 GPT 最易因 jailbreak 封号；
            Claude/Gemini 自带防护。这里配置 OpenAI Moderation API 凭证、关键字词库、
            缓存参数、长 prompt 限制、多模态图片策略、双语拒绝文案。
            具体每个渠道每个模型走哪种风控等级在 ChannelManagement → 模型编辑里设。
            ========================================================= */}
        {isAdmin && activeTab === 'moderation' && (
           <div className="w-full">
              <ContentModerationGlobals
                 configs={configs}
                 handleChange={handleChange}
              />
              {/* fix MAJOR R23-M11（gemini 审查）：用全局 SaveBar 与其他 tab 风格一致 */}
              <SaveBar loading={loading} onSave={handleSave} t={t} />
           </div>
        )}

        {/* =========================================================
            财务工作区 (Admin)
            ========================================================= */}
        {isAdmin && activeTab === 'finance' && (
          <div className="w-full">
            <div className="mb-8 border-b border-outline-variant pb-6">
              <h1 className="text-xl md:text-2xl font-bold tracking-tight text-on-surface flex items-center gap-3">
                <ShieldAlert size={22} className="text-primary" />
                {t('SETTINGS.FINANCE_TITLE')}
              </h1>
              <p className="text-on-surface-variant mt-2 text-sm max-w-3xl">
                {t('SETTINGS.FINANCE_DESC')}
              </p>
            </div>

            <div
              role="tablist"
              aria-label={t('SETTINGS.FINANCE_TABS_LABEL', '财务工作区分区')}
              className="mb-6 flex flex-wrap gap-2"
            >
              {financeTabs.map((tab) => {
                const Icon = tab.icon;
                const selected = financeTab === tab.id;
                return (
                  <button
                    key={tab.id}
                    type="button"
                    role="tab"
                    aria-selected={selected}
                    onClick={() => setFinanceTab(tab.id)}
                    className={`h-10 px-4 rounded-lg border text-sm font-medium transition-colors flex items-center gap-2 ${
                      selected
                        ? 'bg-primary text-on-primary border-primary'
                        : 'bg-surface-container border-outline-variant text-on-surface-variant hover:text-on-surface hover:border-primary/50'
                    }`}
                  >
                    <Icon size={15} />
                    {tab.label}
                  </button>
                );
              })}
            </div>

            {financeTab === 'settings' && (
              <>
             <div className="bg-surface-container border border-outline-variant rounded-2xl p-4 md:p-6 mb-12 shadow-sm w-full">
               <div className="flex flex-col md:flex-row md:items-center justify-between py-4 gap-4">
                  <div className="flex flex-col gap-1 w-full md:w-2/3">
                     <span className="text-on-surface-variant font-medium">{t('SETTINGS.EXCHANGE_RATE_TITLE')}</span>
                     <span className="text-xs text-on-surface-variant">{t('SETTINGS.EXCHANGE_RATE_DESC')}</span>
                  </div>
                  <div className="relative w-full md:w-auto">
                    <span className="absolute left-3 top-2.5 text-on-surface-variant text-sm pointer-events-none">￥</span>
                    <input
                      type="number"
                      step="0.01"
                      value={configs.exchange_rate || ''}
                      onChange={(e) => handleChange('exchange_rate', e.target.value)}
                      placeholder="7.25"
                      className="w-full md:w-32 bg-surface-container-high border border-outline rounded-lg pl-8 pr-4 py-2 text-on-surface outline-none text-right focus:border-primary  placeholder-[#444]"
                    />
                  </div>
               </div>

               {/* fix Gemini UX 审查（第二十五轮 Major #2）：server_address 提示文案承诺"在财务工作区基础设置中配置"，
                   但原实际位置在 OAuth tab——典型"承诺不兑现"错位。挪到这里兑现承诺。
                   该字段同时驱动 OAuth 回调 URL + 易付通 notify_url + return_url，是系统全局服务地址。 */}
               <div className="flex flex-col md:flex-row md:items-center justify-between py-4 gap-4 border-t border-outline-variant/50">
                  <div className="flex flex-col gap-1 w-full md:w-2/3">
                    <span className="text-on-surface-variant font-medium">{t('SETTINGS.SERVER_ADDR_LABEL')}</span>
                    <span className="text-xs text-on-surface-variant">{t('SETTINGS.SERVER_ADDR_DESC')}</span>
                  </div>
                  <input
                    type="text"
                    value={configs.server_address || ''}
                    onChange={(e) => handleChange('server_address', e.target.value)}
                    placeholder="http://localhost:5174/"
                    className="bg-surface-container-high border border-outline text-on-surface-variant rounded-lg px-4 py-2 outline-none text-sm w-full md:w-64 hover:border-blue-500/50 focus:border-primary "
                  />
               </div>

               <div className="py-4 border-t border-outline-variant/50">
                 <div className="flex items-start gap-3 mb-4">
                   <Wallet size={18} className="text-primary mt-0.5" />
                   <div>
                     <h3 className="text-sm font-semibold text-on-surface">
                       {t('SETTINGS.BALANCE_DEFAULT_TITLE', '新用户余额消费默认值')}
                     </h3>
                     <p className="text-xs text-on-surface-variant mt-0.5 max-w-2xl">
                       {t('SETTINGS.BALANCE_DEFAULT_DESC', '只影响之后注册的新用户。余额消费仍排在订阅和增量包之后；默认关闭可保持最小攻击面。')}
                     </p>
                   </div>
                 </div>

                 <div className="flex flex-col md:flex-row md:items-center justify-between py-3 border-b border-outline-variant/20 gap-3">
                   <div className="flex flex-col gap-1 w-full md:w-2/3">
                     <span id="balance-default-enabled-label" className="text-on-surface-variant font-medium text-sm">
                       {t('SETTINGS.BALANCE_DEFAULT_ENABLED', '默认允许余额消费')}
                     </span>
                     <span className="text-xs text-outline">
                       {t('SETTINGS.BALANCE_DEFAULT_ENABLED_HINT', '开启后，新用户在订阅和增量包都耗尽时会自动扣余额。')}
                     </span>
                   </div>
                   <button
                     type="button"
                     role="switch"
                     aria-checked={String(configs.balance_consume_default_enabled).toLowerCase() === 'true'}
                     aria-labelledby="balance-default-enabled-label"
                     onClick={() => handleChange('balance_consume_default_enabled', String(configs.balance_consume_default_enabled).toLowerCase() === 'true' ? 'false' : 'true')}
                     className={`relative shrink-0 w-12 h-6 rounded-full transition ${String(configs.balance_consume_default_enabled).toLowerCase() === 'true' ? 'bg-primary' : 'bg-on-surface/20'}`}
                   >
                     <span className={`absolute top-0.5 w-5 h-5 rounded-full bg-white transition-all ${String(configs.balance_consume_default_enabled).toLowerCase() === 'true' ? 'left-6' : 'left-0.5'}`} />
                   </button>
                 </div>

                 <div className="grid grid-cols-1 md:grid-cols-2 gap-4 pt-4">
                   <label className="flex flex-col gap-2">
                     <span className="text-xs font-medium text-on-surface-variant">
                       {t('SETTINGS.BALANCE_DEFAULT_LIMIT', '默认周期消费上限（USD）')}
                     </span>
                     <div className="relative">
                       <span className="absolute left-3 top-2.5 text-on-surface-variant text-sm pointer-events-none">$</span>
                       <input
                         type="number"
                         min="0"
                         step="0.01"
                         value={configs.balance_consume_default_limit_usd ?? '0'}
                         onChange={(e) => handleChange('balance_consume_default_limit_usd', e.target.value)}
                         placeholder="0"
                         className="w-full bg-surface-container-high border border-outline rounded-lg pl-7 pr-3 py-2 text-on-surface outline-none text-right focus:border-primary"
                       />
                     </div>
                     <span className="text-[11px] text-on-surface-variant">
                       {t('SETTINGS.BALANCE_DEFAULT_LIMIT_HINT', '0 表示不限额。')}
                     </span>
                   </label>

                   <label className="flex flex-col gap-2">
                     <span className="text-xs font-medium text-on-surface-variant">
                       {t('SETTINGS.BALANCE_DEFAULT_WINDOW', '默认统计窗口（秒）')}
                     </span>
                     <input
                       type="number"
                       min="60"
                       max={365 * 24 * 60 * 60}
                       step="60"
                       value={configs.balance_consume_default_window_secs ?? '2592000'}
                       onChange={(e) => handleChange('balance_consume_default_window_secs', e.target.value)}
                       placeholder="2592000"
                       className="w-full bg-surface-container-high border border-outline rounded-lg px-3 py-2 text-on-surface outline-none text-right focus:border-primary"
                     />
                     <span className="text-[11px] text-on-surface-variant">
                       {t('SETTINGS.BALANCE_DEFAULT_WINDOW_HINT', '60 秒到 365 天；2592000 = 30 天。')}
                     </span>
                   </label>
                 </div>
               </div>
            </div>

            <SaveBar loading={loading} onSave={handleSave} t={t} />

            {/* 极端危险区：出厂重置 */}
            <div className="mt-16 mb-12 border-2 border-red-900/50 rounded-2xl p-6 bg-red-950/10 relative overflow-hidden">
              <div className="absolute top-0 right-0 text-red-900/10 pointer-events-none -mr-4 -mt-4">
                <AlertOctagon size={120} strokeWidth={1} />
              </div>
              <div className="flex items-start gap-3 mb-5 relative z-10">
                <AlertOctagon className="text-red-500 shrink-0 mt-1" size={22} />
                <div>
                  <h3 className="text-lg font-bold text-red-400 tracking-tight">极端危险区 / DANGER ZONE</h3>
                  <p className="text-xs text-on-surface-variant mt-1">下方操作会**不可逆地**抹除所有数据，仅在你完全清楚后果时使用。</p>
                </div>
              </div>

              <div className="bg-black/30 border border-red-900/30 rounded-xl p-5 relative z-10">
                <div className="flex flex-col md:flex-row md:items-center md:justify-between gap-4">
                  <div className="flex-1">
                    <h4 className="text-base font-bold text-on-surface mb-2">恢复出厂设置</h4>
                    <p className="text-xs text-on-surface-variant leading-relaxed">
                      将清空所有 <span className="text-red-400">用户、API 令牌、调用日志、审计日志、上游渠道、模型映射、系统配置（含 cliproxy_key、GitHub OAuth、阿里云密钥等）</span>，
                      并重新创建默认管理员 <span className="text-amber-400 font-mono">root / 123456</span>。
                      操作后你将自动登出，需用 <span className="font-mono">?sys=root</span> 入口重新引导。
                    </p>
                    <p className="text-xs text-red-400/90 mt-3 font-medium">
                      ⚠️ 不可恢复，请确保已备份必要数据（如 daof.key、SQLite 文件副本）。
                    </p>
                  </div>
                  <button
                    type="button"
                    onClick={() => setResetModal({ open: true, confirmText: '', password: '', loading: false })}
                    className="shrink-0 px-5 py-2.5 bg-red-700 hover:bg-red-600 text-white font-medium rounded-lg flex items-center gap-2 shadow-[0_0_15px_rgba(220,38,38,0.3)] transition-colors"
                  >
                    <AlertOctagon size={16} />
                    恢复出厂设置
                  </button>
                </div>
              </div>
            </div>
              </>
            )}

            {financeTab === 'payment_channels' && <AdminPaymentChannels />}
            {financeTab === 'topup_orders' && <AdminTopupOrders />}
            {financeTab === 'admin_subscriptions' && <AdminSubscriptions />}
          </div>
        )}

        {/* 出厂重置确认弹窗 */}
        {resetModal.open && (
          <div
            ref={resetModalRef}
            role="dialog"
            aria-modal="true"
            aria-labelledby="reset-modal-title"
            onClick={onResetBackdropClick}
            className="fixed inset-0 z-[70] flex items-start sm:items-center justify-center p-2 sm:p-4 bg-black/80 backdrop-blur-md overflow-y-auto"
          >
            <div className="relative w-full max-w-md bg-surface-container border-2 border-red-700 rounded-2xl shadow-[0_0_40px_rgba(220,38,38,0.4)] p-6">
              <button
                onClick={closeResetModal}
                className="absolute top-4 right-4 text-on-surface-variant hover:text-white"
                disabled={resetModal.loading}
                aria-label={t('COMMON.CLOSE', '关闭')}
              >
                <X size={18} />
              </button>

              <div className="flex items-center gap-3 mb-5">
                <div className="w-12 h-12 rounded-full bg-red-900/40 border border-red-700 flex items-center justify-center">
                  <AlertOctagon className="text-red-400" size={24} />
                </div>
                <div>
                  <h2 id="reset-modal-title" className="text-xl font-bold text-red-400">最终确认</h2>
                  <p className="text-xs text-on-surface-variant mt-0.5">此操作不可撤销</p>
                </div>
              </div>

              <div className="space-y-3 mb-5 text-sm">
                <p className="text-on-surface">即将抹除：</p>
                <ul className="text-xs text-on-surface-variant space-y-1 ml-4 list-disc">
                  <li>所有普通用户与管理员（重建默认 root）</li>
                  <li>所有 API 令牌（含子凭证）</li>
                  <li>所有调用日志、审计日志</li>
                  <li>所有上游渠道与模型映射</li>
                  <li>所有系统配置（GitHub OAuth、阿里云、cliproxy_key 等）</li>
                </ul>
              </div>

              <div className="space-y-2 mb-4">
                <label htmlFor="settings-factory-reset-confirm" className="text-xs font-semibold text-red-400">
                  请精确输入 <span className="font-mono bg-red-900/40 px-1.5 py-0.5 rounded">FACTORY_RESET</span> 以确认：
                </label>
                <input
                  id="settings-factory-reset-confirm"
                  type="text"
                  autoFocus
                  value={resetModal.confirmText}
                  onChange={e => setResetModal(prev => ({ ...prev, confirmText: e.target.value }))}
                  placeholder="FACTORY_RESET"
                  aria-invalid={resetModal.confirmText !== '' && resetModal.confirmText !== 'FACTORY_RESET'}
                  className="w-full h-11 bg-black/50 border border-red-900/50 rounded-lg px-3 text-base text-red-300 font-mono focus:border-red-500 outline-none"
                  disabled={resetModal.loading}
                />
              </div>

              <div className="space-y-2 mb-5">
                <label htmlFor="settings-factory-reset-password" className="text-xs font-semibold text-red-400">
                  二次鉴权：再次输入当前管理员密码
                </label>
                <input
                  id="settings-factory-reset-password"
                  type="password"
                  value={resetModal.password}
                  onChange={e => setResetModal(prev => ({ ...prev, password: e.target.value }))}
                  placeholder="当前管理员密码"
                  className="w-full h-11 bg-black/50 border border-red-900/50 rounded-lg px-3 text-base text-on-surface font-mono focus:border-red-500 outline-none"
                  disabled={resetModal.loading}
                />
              </div>

              <div className="flex gap-3">
                <button
                  onClick={() => setResetModal({ open: false, confirmText: '', password: '', loading: false })}
                  disabled={resetModal.loading}
                  className="flex-1 h-10 bg-surface-container-high border border-outline-variant text-on-surface rounded-lg hover:bg-surface-variant transition-colors text-sm font-medium"
                >
                  取消
                </button>
                <button
                  onClick={performFactoryReset}
                  disabled={resetModal.loading || resetModal.confirmText !== 'FACTORY_RESET' || !resetModal.password}
                  className="flex-1 h-10 bg-red-700 hover:bg-red-600 disabled:opacity-30 disabled:cursor-not-allowed text-white rounded-lg transition-colors text-sm font-bold"
                >
                  {resetModal.loading ? '正在抹除...' : '确认重置'}
                </button>
              </div>
            </div>
          </div>
        )}

        {/* =========================================================
            GitHub OAuth 配置
            ========================================================= */}
        {isAdmin && activeTab === 'oauth' && (
          <div className="w-full">
            <div className="mb-8 border-b border-outline-variant pb-6">
              <h1 className="text-xl md:text-2xl font-bold tracking-tight text-on-surface flex items-center gap-3">
                {t('SETTINGS.OAUTH_TITLE')}
              </h1>
              <p className="text-blue-500/70 mt-2 text-sm font-mono tracking-wide uppercase">
                {t('SETTINGS.SECURE_ZONE_DESC')}
              </p>
            </div>

            <div className="bg-surface-container border border-outline-variant rounded-2xl p-4 md:p-6 mb-8 shadow-sm w-full">
              <div className="flex items-center gap-2 mb-6">
                <div className="w-1 h-5 bg-primary text-on-primary rounded-r-md"></div>
                <h2 className="text-lg font-semibold text-on-surface">{t('SETTINGS.OAUTH_APP_PARAMS')}</h2>
              </div>
              <div className="flex flex-col gap-6">
                 <InputField label="Client ID" id="github_client_id" val={configs.github_client_id} onChange={handleChange} show={showMask.github_client_id} onToggle={() => toggleMask('github_client_id')} />
                 <InputField label="Client Secret" id="github_client_secret" val={configs.github_client_secret} onChange={handleChange} show={showMask.github_client_secret} onToggle={() => toggleMask('github_client_secret')} isPassword />
              </div>

               <div className="flex items-center gap-2 mb-6 mt-10">
                 <div className="w-1 h-5 bg-primary text-on-primary rounded-r-md"></div>
                 <h2 className="text-lg font-semibold text-on-surface">{t('SETTINGS.OAUTH_CALLBACK_TITLE')}</h2>
               </div>
               {/* fix Gemini UX 审查（第二十五轮 Major #2）：server_address 已挪到 财务工作区基础设置。
                   此处只读展示当前值 + 跳转链接，避免双入口（写值的入口仅财务工作区一处）。 */}
               <div className="flex flex-col md:flex-row md:items-center justify-between py-4 border-t border-outline-variant/50 gap-4">
                  <div className="flex flex-col gap-1 w-full md:w-2/3">
                    <span className="text-on-surface-variant font-medium text-sm">{t('SETTINGS.SERVER_ADDR_LABEL')}</span>
                    <span className="text-xs text-on-surface-variant">
                      {configs.server_address
                        ? <>当前值：<code className="font-mono text-primary">{configs.server_address}</code> · OAuth 回调地址将自动拼接 <code className="font-mono">/api/auth/github</code></>
                        : <>尚未配置。请在 <button type="button" onClick={openFinanceSettings} className="text-primary underline hover:opacity-80">财务工作区 → 基础设置</button> 中填入 server_address。</>}
                    </span>
                  </div>
                  <button type="button" onClick={openFinanceSettings} className="bg-surface-container-high border border-outline rounded-lg px-4 py-2 text-sm text-on-surface hover:border-primary transition-colors">
                    去财务工作区
                  </button>
               </div>
            </div>

            <SaveBar loading={loading} onSave={handleSave} t={t} />
          </div>
        )}

        {/* =========================================================
            阿里云短信配置
            ========================================================= */}
        {isAdmin && activeTab === 'sms' && (
          <div className="w-full">
            <div className="mb-8 border-b border-outline-variant pb-6">
              <h1 className="text-xl md:text-2xl font-bold tracking-tight text-on-surface flex items-center gap-3">
                {t('SETTINGS.SMS_TITLE')}
              </h1>
              <p className="text-blue-500/70 mt-2 text-sm font-mono tracking-wide uppercase">
                {t('SETTINGS.SECURE_ZONE_DESC')}
              </p>
            </div>

            <div className="bg-surface-container border border-outline-variant rounded-2xl p-4 md:p-6 mb-8 shadow-sm w-full">
              <div className="flex items-center gap-2 mb-6">
                <div className="w-1 h-5 bg-orange-500 rounded-r-md"></div>
                <h2 className="text-lg font-semibold text-on-surface">{t('SETTINGS.SMS_RAM_TITLE')}</h2>
              </div>
              <div className="flex flex-col gap-6">
                 <InputField label="AccessKey ID" id="aliyun_access_key" val={configs.aliyun_access_key} onChange={handleChange} show={showMask.aliyun_access_key} onToggle={() => toggleMask('aliyun_access_key')} />
                 <InputField label="AccessKey Secret" id="aliyun_access_secret" val={configs.aliyun_access_secret} onChange={handleChange} show={showMask.aliyun_access_secret} onToggle={() => toggleMask('aliyun_access_secret')} isPassword />
              </div>

              <div className="flex items-center gap-2 mb-6 mt-10">
                <div className="w-1 h-5 bg-orange-500 rounded-r-md"></div>
                <h2 className="text-lg font-semibold text-on-surface">{t('SETTINGS.SMS_TPL_TITLE')}</h2>
              </div>
              <div className="flex flex-col gap-6">
                 <InputField label={t('SETTINGS.SMS_SIGN_LABEL')} id="aliyun_sms_sign" val={configs.aliyun_sms_sign} onChange={handleChange} show={showMask.aliyun_sms_sign} onToggle={() => toggleMask('aliyun_sms_sign')} />
                 <InputField label={t('SETTINGS.SMS_TPL_LABEL')} id="aliyun_sms_template" val={configs.aliyun_sms_template} onChange={handleChange} show={showMask.aliyun_sms_template} onToggle={() => toggleMask('aliyun_sms_template')} />
              </div>
            </div>

            <SaveBar loading={loading} onSave={handleSave} t={t} />
          </div>
        )}

        {/* =========================================================
            注册体感与风控引擎
            ========================================================= */}
        {isAdmin && activeTab === 'risk' && (
          <div className="w-full">
            <div className="mb-8 border-b border-outline-variant pb-6">
              <h1 className="text-xl md:text-2xl font-bold tracking-tight text-on-surface flex items-center gap-3">
                 {t('SETTINGS.RISK_TITLE')}
              </h1>
              <p className="text-on-surface-variant mt-2 text-sm max-w-2xl">
                {t('SETTINGS.RISK_DESC')}
              </p>
            </div>

            <div className="bg-surface-container border border-outline-variant rounded-2xl p-4 md:p-6 mb-8 shadow-sm w-full">
               <div className="flex flex-col md:flex-row md:items-center justify-between py-4 border-b border-outline-variant/50 gap-4">
                  <div className="flex flex-col gap-1 w-full md:w-2/3">
                     <span className="text-on-surface-variant font-medium">{t('SETTINGS.RISK_STRATEGY_LABEL')}</span>
                     <span className="text-xs text-on-surface-variant">{t('SETTINGS.RISK_STRATEGY_DESC')}</span>
                  </div>
                  <select 
                    value={configs.reg_strategy || 'dynamic'}
                    onChange={(e) => handleChange('reg_strategy', e.target.value)}
                    className="bg-surface-container-high border border-outline text-on-surface-variant rounded-lg px-4 py-2 outline-none text-sm w-full md:w-64 cursor-pointer hover:border-blue-500/50 "
                  >
                    <option value="trust">{t('SETTINGS.STRATEGY_TRUST')}</option>
                    <option value="dynamic">{t('SETTINGS.STRATEGY_DYNAMIC')}</option>
                    <option value="strict">{t('SETTINGS.STRATEGY_STRICT')}</option>
                  </select>
               </div>

               <div className="flex flex-col md:flex-row md:items-center justify-between py-4 border-b border-outline-variant/50 gap-4">
                 <div className="flex flex-col gap-1 w-full md:w-2/3">
                     <span className="text-on-surface-variant font-medium">{t('SETTINGS.IP_LIMIT_LABEL')}</span>
                     <span className="text-xs text-on-surface-variant">{t('SETTINGS.IP_LIMIT_DESC')}</span>
                  </div>
                  <div className="relative w-full md:w-auto">
                    <input
                      type="number"
                      value={configs.reg_ip_limit || '3'}
                      onChange={(e) => handleChange('reg_ip_limit', e.target.value)}
                      className="w-full md:w-32 bg-surface-container-high border border-outline rounded-lg pl-4 pr-10 py-2 text-on-surface outline-none text-right focus:border-primary "
                    />
                    <span className="absolute right-4 top-2.5 text-on-surface-variant text-sm pointer-events-none">{t('SETTINGS.UNIT_COUNT')}</span>
                  </div>
               </div>

               <div className="flex flex-col md:flex-row md:items-center justify-between py-4 gap-4">
                 <div className="flex flex-col gap-1 w-full md:w-2/3">
                     <span className="text-on-surface-variant font-medium">平台用户总量上限</span>
                     <span className="text-xs text-on-surface-variant">达到上限后停止接受新用户注册（仅统计普通用户，不含管理员）。设为 0 表示无限制。</span>
                  </div>
                  <div className="relative w-full md:w-auto">
                    <input
                      type="number"
                      min="0"
                      value={configs.max_users ?? '0'}
                      onChange={(e) => handleChange('max_users', e.target.value)}
                      placeholder="0"
                      className="w-full md:w-32 bg-surface-container-high border border-outline rounded-lg pl-4 pr-10 py-2 text-on-surface outline-none text-right focus:border-primary "
                    />
                    <span className="absolute right-4 top-2.5 text-on-surface-variant text-sm pointer-events-none">人</span>
                  </div>
               </div>
            </div>

            <div className="bg-surface-container border border-outline-variant rounded-2xl p-4 md:p-6 mb-8 shadow-sm w-full">
              <div className="flex items-center gap-3 mb-5 pb-4 border-b border-outline-variant/40">
                <ShieldCheck size={18} className="text-primary" />
                <div>
                  <h3 className="text-sm font-semibold text-on-surface">新用户奖励 / 拉新激励配置</h3>
                  <p className="text-xs text-on-surface-variant mt-0.5">所有金额按 USD 计；填 0 表示该项不发放。拉新链接格式：<span className="font-mono text-primary">https://your-domain/?ref=&lt;推荐人用户名&gt;</span></p>
                </div>
              </div>

              <div className="flex flex-col md:flex-row md:items-center justify-between py-3 border-b border-outline-variant/20 gap-3">
                <div className="flex flex-col gap-1 w-full md:w-2/3">
                  <span className="text-on-surface-variant font-medium text-sm">新用户初始金额（signup_bonus）</span>
                  <span className="text-xs text-outline">每个新注册用户开局送多少额度（无论是否带 ref）。</span>
                </div>
                <div className="relative w-full md:w-32">
                  <span className="absolute left-3 top-2.5 text-on-surface-variant text-sm pointer-events-none">$</span>
                  <input
                    type="number"
                    step="0.01" min="0"
                    value={configs.signup_bonus ?? '1'}
                    onChange={(e) => handleChange('signup_bonus', e.target.value)}
                    placeholder="1.00"
                    className="w-full bg-surface-container-high border border-outline rounded-lg pl-7 pr-3 py-2 text-on-surface outline-none text-right focus:border-primary"
                  />
                </div>
              </div>

              <div className="flex flex-col md:flex-row md:items-center justify-between py-3 border-b border-outline-variant/20 gap-3">
                <div className="flex flex-col gap-1 w-full md:w-2/3">
                  <span className="text-on-surface-variant font-medium text-sm">拉新者奖励（referrer_bonus）</span>
                  <span className="text-xs text-outline">推荐人通过 ?ref=自己用户名 的链接成功带来一个新用户，给推荐人加多少额度。</span>
                </div>
                <div className="relative w-full md:w-32">
                  <span className="absolute left-3 top-2.5 text-on-surface-variant text-sm pointer-events-none">$</span>
                  <input
                    type="number"
                    step="0.01" min="0"
                    value={configs.referrer_bonus ?? '0'}
                    onChange={(e) => handleChange('referrer_bonus', e.target.value)}
                    placeholder="0.50"
                    className="w-full bg-surface-container-high border border-outline rounded-lg pl-7 pr-3 py-2 text-on-surface outline-none text-right focus:border-primary"
                  />
                </div>
              </div>

              <div className="flex flex-col md:flex-row md:items-center justify-between py-3 gap-3">
                <div className="flex flex-col gap-1 w-full md:w-2/3">
                  <span className="text-on-surface-variant font-medium text-sm">被拉新者奖励（referee_bonus）</span>
                  <span className="text-xs text-outline">通过推荐链接进来的新用户，除了 signup_bonus 外**额外**多送多少额度（叠加，不替换）。</span>
                </div>
                <div className="relative w-full md:w-32">
                  <span className="absolute left-3 top-2.5 text-on-surface-variant text-sm pointer-events-none">$</span>
                  <input
                    type="number"
                    step="0.01" min="0"
                    value={configs.referee_bonus ?? '0'}
                    onChange={(e) => handleChange('referee_bonus', e.target.value)}
                    placeholder="0.30"
                    className="w-full bg-surface-container-high border border-outline rounded-lg pl-7 pr-3 py-2 text-on-surface outline-none text-right focus:border-primary"
                  />
                </div>
              </div>
            </div>

            {/* 号池额度采集器配置 */}
            <div className="bg-surface-container border border-outline-variant rounded-2xl p-4 md:p-6 mb-8 shadow-sm w-full">
              <div className="flex items-center gap-3 mb-5 pb-4 border-b border-outline-variant/40">
                <Activity size={18} className="text-primary" />
                <div>
                  <h3 className="text-sm font-semibold text-on-surface">号池额度采集器</h3>
                  <p className="text-xs text-on-surface-variant mt-0.5">控制后台 goroutine 多久轮询一次 CPA 上所有凭证的剩余额度，决定<span className="text-primary font-mono"> 号池监控</span> 看板与用户首页号池卡片的数据新鲜度。</p>
                </div>
              </div>

              <div className="flex flex-col md:flex-row md:items-center justify-between py-3 border-b border-outline-variant/20 gap-3">
                <div className="flex flex-col gap-1 w-full md:w-2/3">
                  <span className="text-on-surface-variant font-medium text-sm">全量刷新周期</span>
                  <span className="text-xs text-outline">每隔多少分钟把所有凭证的额度全量重新拉一遍。建议 10-30 分钟，过短会被上游限流。</span>
                </div>
                <div className="relative w-full md:w-32">
                  <input
                    type="number"
                    min="1"
                    max="1440"
                    value={configs.credits_refresh_interval ?? '15'}
                    onChange={(e) => handleChange('credits_refresh_interval', e.target.value)}
                    placeholder="15"
                    className="w-full bg-surface-container-high border border-outline rounded-lg pl-3 pr-12 py-2 text-on-surface outline-none text-right focus:border-primary"
                  />
                  <span className="absolute right-3 top-2.5 text-on-surface-variant text-sm pointer-events-none">分钟</span>
                </div>
              </div>

              <div className="flex flex-col md:flex-row md:items-center justify-between py-3 border-b border-outline-variant/20 gap-3">
                <div className="flex flex-col gap-1 w-full md:w-2/3">
                  <span className="text-on-surface-variant font-medium text-sm">失败重试次数</span>
                  <span className="text-xs text-outline">单个凭证连续失败时最多重试几次后放弃，等下一轮全量周期。<span className="text-amber-400 font-mono">0</span> = 无限重试，仍带指数退避（封顶 60 分钟）防止雪崩。</span>
                </div>
                <div className="relative w-full md:w-32">
                  <input
                    type="number"
                    min="0"
                    max="100"
                    value={configs.credits_max_retries ?? '3'}
                    onChange={(e) => handleChange('credits_max_retries', e.target.value)}
                    placeholder="3"
                    className="w-full bg-surface-container-high border border-outline rounded-lg pl-3 pr-10 py-2 text-on-surface outline-none text-right focus:border-primary"
                  />
                  <span className="absolute right-3 top-2.5 text-on-surface-variant text-sm pointer-events-none">次</span>
                </div>
              </div>

              <div className="flex flex-col md:flex-row md:items-center justify-between py-3 border-b border-outline-variant/20 gap-3">
                <div className="flex flex-col gap-1 w-full md:w-2/3">
                  <span className="text-on-surface-variant font-medium text-sm">重试间隔（基础值）</span>
                  <span className="text-xs text-outline">每次重试之间等待多少分钟。<span className="text-amber-400">实际间隔会按指数退避（base × 2^retry_count），封顶 60 分钟</span>，避免上游持续被冲击。</span>
                </div>
                <div className="relative w-full md:w-32">
                  <input
                    type="number"
                    min="1"
                    max="1440"
                    value={configs.credits_retry_interval ?? '5'}
                    onChange={(e) => handleChange('credits_retry_interval', e.target.value)}
                    placeholder="5"
                    className="w-full bg-surface-container-high border border-outline rounded-lg pl-3 pr-12 py-2 text-on-surface outline-none text-right focus:border-primary"
                  />
                  <span className="absolute right-3 top-2.5 text-on-surface-variant text-sm pointer-events-none">分钟</span>
                </div>
              </div>

              {/*
                Antigravity GCP Project ID 字段已移除：
                project_id 现在由 daof-ai-hub 自动从每个 CPA 凭证文件的
                cloudaicompanionProject 字段提取并缓存到 cpa_credentials 表，
                按凭证粒度独立管理（多账号自动支持），admin 零配置。
              */}
            </div>

            <SaveBar loading={loading} onSave={handleSave} t={t} />
          </div>
        )}

      </div>
    </div>
  );
};

// 抽取可复用的保存条组件
const SaveBar = ({ loading, onSave, t }) => (
  <div className="flex items-center justify-end gap-6 mb-12">
    <button
      onClick={() => onSave()}
      disabled={loading}
      className="h-11 px-6 bg-primary text-on-primary hover:bg-primary-container hover:text-on-primary-container font-medium rounded-xl flex items-center justify-center gap-2  shadow-[0_0_15px_rgba(37,99,235,0.2)] disabled:opacity-50"
    >
      {loading ? t('SETTINGS.BTN_SAVING') : (
        <>
          <Save size={18} />
          {t('SETTINGS.BTN_SAVE')}
        </>
      )}
    </button>
  </div>
);

const InputField = ({ label, id, val, onChange, show, onToggle, isPassword }) => {
  const inputId = `settings-input-${id}`;
  return (
    <div className="flex flex-col gap-2">
      <label htmlFor={inputId} className="text-xs font-semibold text-on-surface-variant ml-1">{label}</label>
      <div className="relative group">
        <div className="absolute inset-y-0 left-0 pl-3 flex items-center pointer-events-none">
          <KeyRound size={16} className="text-on-surface-variant" />
        </div>
        <input
          id={inputId}
          type={isPassword && !show ? "password" : "text"}
          value={val}
          onChange={(e) => onChange(id, e.target.value)}
          placeholder="••••••••••••"
          className="w-full h-11 bg-surface-container-high border border-outline group-hover:border-primary/50 rounded-lg pl-10 pr-10 text-sm text-on-surface outline-none focus:border-primary font-mono placeholder:text-on-surface-variant/50"
        />
        <button
          type="button"
          onClick={onToggle}
          aria-label={show ? '隐藏' : '显示'}
          className="absolute inset-y-0 right-0 pr-3 flex items-center text-on-surface-variant hover:text-white "
        >
          {show ? <EyeOff size={16} /> : <Eye size={16} />}
        </button>
      </div>
    </div>
  );
};

export default Settings;
