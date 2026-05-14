import React from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router-dom';
import { ShieldAlert } from 'lucide-react';
import { useAuth } from '../context/AuthContext';
import UpgradePage from './UpgradePage';

/**
 * Dashboard — 用户首页（Phase 8 重做）
 *
 * 业务定位：平台只供应 Combo 御三家订阅套餐，所以首页就是套餐购买页。
 * - admin → AdminBanner（admin 不买套餐，引导去管理控制台）
 * - 普通用户 / 未登录 → UpgradePage（套餐购买流程含"我的/商店"切换）
 *
 * 之前的 StatStrip / ProviderModelSection / ModelCard / RecentLogs / PublicHero
 * 全部移除（用户反馈："把当前的仪表盘内容全部移除，订阅都放仪表盘去"）：
 * - 模型相关 → /pricing 页（费率与模型）
 * - 用量数据 → /stats 页（数据看板）
 * - 最近调用 → /stats 页
 * - 余额 → TopBar avatar 菜单 + /bills 页
 */
const Dashboard = () => {
  const { t } = useTranslation();
  const { isAdmin } = useAuth();
  const navigate = useNavigate();

  if (isAdmin) {
    return (
      <div className="space-y-8 py-6">
        <section className="fl-card flex items-center gap-3 px-4 py-3">
          <ShieldAlert size={16} className="text-on-surface-variant shrink-0" />
          <span className="text-sm text-on-surface-variant">
            {t('DASH.ADMIN_HINT', '当前为管理员模式，可前往管理控制台查看渠道、用户与计费')}
          </span>
          <button
            type="button"
            onClick={() => navigate('/admin')}
            className="ml-auto text-sm font-medium text-primary hover:underline"
          >
            {t('DASH.ADMIN_ENTER', '进入控制台')}
          </button>
        </section>
      </div>
    );
  }

  return <UpgradePage />;
};

export default Dashboard;
