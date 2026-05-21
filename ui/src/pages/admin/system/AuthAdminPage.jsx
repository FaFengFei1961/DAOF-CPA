/**
 * AuthAdminPage — 认证管理一站页（Sprint J-2）
 *
 * 合并旧 4 个独立 nav 项（GitHub OAuth / 邮箱 SMTP / 短信 / 注册策略与风控）
 * 为单页面 + 内嵌 4 个 tab。
 *
 * URL 形式：/admin/auth?tab=oauth|email|sms|risk
 * - 默认 oauth tab；其他 tab 通过 URL 深链可达
 * - 切 tab 同步写回 URL（与 Settings.jsx 范式一致）
 *
 * 子页面（OAuthPage / EmailPage / SmsPage / RiskPage）现在只渲染表单 body，
 * PageContainer + PageHeader 由本组件统一提供。
 */
import React, { useCallback } from 'react';
import { useSearchParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { ShieldCheck, Key, Mail, MessageSquare, Shield } from 'lucide-react';
import { PageContainer, PageHeader } from '../../../components/ui';
import OAuthPage from './OAuthPage';
import EmailPage from './EmailPage';
import SmsPage from './SmsPage';
import RiskPage from './RiskPage';

const TABS = [
  { id: 'oauth', icon: Key,             labelKey: 'ADMIN_AUTH.TAB_OAUTH',  fallback: '第三方登录' },
  { id: 'email', icon: Mail,            labelKey: 'ADMIN_AUTH.TAB_EMAIL',  fallback: '邮箱' },
  { id: 'sms',   icon: MessageSquare,   labelKey: 'ADMIN_AUTH.TAB_SMS',    fallback: '短信' },
  { id: 'risk',  icon: ShieldCheck,     labelKey: 'ADMIN_AUTH.TAB_RISK',   fallback: '注册策略' },
];

const VALID_TABS = TABS.map(t => t.id);

const AuthAdminPage = () => {
  const { t } = useTranslation();
  const [searchParams, setSearchParams] = useSearchParams();

  const queryTab = searchParams.get('tab');
  const activeTab = VALID_TABS.includes(queryTab) ? queryTab : 'oauth';

  const handleTabChange = useCallback((tabId) => {
    setSearchParams((prev) => {
      const next = new URLSearchParams(prev);
      next.set('tab', tabId);
      return next;
    }, { replace: true });
  }, [setSearchParams]);

  return (
    <PageContainer>
      <PageHeader
        title={t('ADMIN_AUTH.TITLE', '认证管理')}
        sub={t('ADMIN_AUTH.SUB', '第三方登录 / 邮箱 / 短信 / 注册策略 — 一站式配置')}
        icon={Shield}
      />

      {/* Tab navigation — 紧凑、低 chrome、active 用 accent underline */}
      <nav
        role="tablist"
        aria-label={t('ADMIN_AUTH.NAV_LABEL', '认证配置分区')}
        className="flex items-center gap-1 border-b border-outline-variant/40 -mt-2 mb-6 overflow-x-auto"
      >
        {TABS.map(tab => {
          const Icon = tab.icon;
          const isActive = activeTab === tab.id;
          return (
            <button
              key={tab.id}
              role="tab"
              type="button"
              aria-selected={isActive}
              aria-current={isActive ? 'page' : undefined}
              onClick={() => handleTabChange(tab.id)}
              className={`relative flex items-center gap-2 h-10 px-4 text-sm font-medium transition-colors whitespace-nowrap
                ${isActive
                  ? 'text-on-surface'
                  : 'text-on-surface-variant hover:text-on-surface'}`}
            >
              <Icon size={15} className="shrink-0 opacity-80" aria-hidden="true" />
              <span>{t(tab.labelKey, tab.fallback)}</span>
              {isActive && (
                <span
                  aria-hidden
                  className="absolute left-0 right-0 -bottom-px h-0.5 bg-primary"
                />
              )}
            </button>
          );
        })}
      </nav>

      {/* Tab content */}
      {activeTab === 'oauth' && <OAuthPage />}
      {activeTab === 'email' && <EmailPage />}
      {activeTab === 'sms'   && <SmsPage   />}
      {activeTab === 'risk'  && <RiskPage  />}
    </PageContainer>
  );
};

export default AuthAdminPage;
