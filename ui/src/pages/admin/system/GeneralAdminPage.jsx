import React, { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Monitor, Server, Save, Eye, EyeOff } from 'lucide-react';
import { PageContainer, PageHeader, Section } from '../../../components/ui';
import { useAdminConfigs } from '../../../hooks/useAdminConfigs';
import { useTheme } from '../../../context/ThemeContext';
import { authFetch } from '../../../utils/authFetch';
import toast from 'react-hot-toast';

const SEED_COLORS = [
  { hex: '#7c5cff', name: '紫' },
  { hex: '#2563eb', name: '蓝' },
  { hex: '#059669', name: '青' },
  { hex: '#ea580c', name: '橙' },
  { hex: '#dc2626', name: '红' },
  { hex: '#0891b2', name: '湖' },
  { hex: '#a16207', name: '金' },
  { hex: '#475569', name: '灰' },
];

/**
 * GeneralAdminPage — admin 常规设置（Phase 4 抽出）
 *
 * 替换 Settings.jsx 内 activeTab === 'general' 的 admin 部分。
 * 包含两块：
 *   1. 外观（主题模式 + seed color）— admin 也是用户，需要个人化
 *   2. CLIProxyAPI 连接配置（管理 cliproxy_url + cliproxy_key）
 */
const GeneralAdminPage = () => {
  const { t } = useTranslation();
  const { themePref, changeTheme, seedColor, changeSeedColor } = useTheme();
  const { configs, loading, handleChange } = useAdminConfigs();
  const [showClipKey, setShowClipKey] = useState(false);
  const [savingClip, setSavingClip] = useState(false);

  const saveClipProxy = async () => {
    if (!(configs.cliproxy_url || '').trim()) {
      toast.error('CLIProxyAPI 服务地址不能为空');
      return;
    }
    setSavingClip(true);
    try {
      const data = await authFetch('/api/admin/config', {
        method: 'POST',
        body: {
          cliproxy_url: (configs.cliproxy_url || '').trim(),
          cliproxy_key: (configs.cliproxy_key || '').trim(),
        },
      });
      if (data.success) {
        toast.success('CLIProxyAPI 连接配置已安全保存到服务端');
      } else {
        toast.error(data.message || t('SETTINGS.SAVE_FAILED', '保存失败'));
      }
    } catch {
      toast.error(t('SETTINGS.SAVE_FAILED', '保存失败'));
    } finally {
      setSavingClip(false);
    }
  };

  return (
    <PageContainer>
      <PageHeader
        title={t('SETTINGS.GENERAL_TITLE', '常规设置')}
        sub={t('SETTINGS.GENERAL_DESC', '外观、上游连接等基础配置')}
        icon={Monitor}
      />

      {/* ─── 外观（admin 个人化）───────── */}
      <Section title={t('SETTINGS.THEME_LABEL', '外观')} sub={t('SETTINGS.THEME_HINT', '深色 / 浅色 / 跟随系统')}>
        <div className="flex flex-col md:flex-row md:items-center justify-between gap-4 py-2">
          <span className="text-sm text-on-surface">{t('SETTINGS.THEME_LABEL', '外观模式')}</span>
          <div role="radiogroup" aria-label={t('SETTINGS.THEME_LABEL', '外观')}
            className="inline-flex rounded-control border border-outline-variant bg-surface p-0.5"
          >
            {[
              { v: 'light', label: t('SETTINGS.THEME_LIGHT', '浅色') },
              { v: 'dark',  label: t('SETTINGS.THEME_DARK',  '深色') },
              { v: 'system', label: t('SETTINGS.THEME_SYSTEM', '跟随系统') },
            ].map(({ v, label }) => (
              <button key={v} type="button" role="radio" aria-checked={themePref === v}
                onClick={() => changeTheme(v)}
                className={`px-3 py-1.5 text-sm rounded-control transition ${
                  themePref === v ? 'bg-primary text-on-primary font-medium' : 'text-on-surface-variant hover:text-on-surface'
                }`}
              >{label}</button>
            ))}
          </div>
        </div>

        <div className="flex flex-col md:flex-row md:items-center justify-between gap-4 py-3 border-t border-outline-variant/30">
          <div className="flex flex-col gap-1">
            <span className="text-sm text-on-surface">{t('SETTINGS.SEED_COLOR_LABEL', '主题色')}</span>
            <span className="text-xs text-on-surface-variant">{t('SETTINGS.SEED_COLOR_HINT', '选一个种子色，整套界面调色板自动生成')}</span>
          </div>
          <div className="flex items-center gap-2 flex-wrap">
            {SEED_COLORS.map(({ hex, name }) => (
              <button
                key={hex} type="button" onClick={() => changeSeedColor(hex)}
                title={name} aria-label={`主题色: ${name}`}
                className={`w-7 h-7 rounded-full border-2 transition ${
                  seedColor.toLowerCase() === hex.toLowerCase()
                    ? 'border-on-surface scale-110' : 'border-outline-variant hover:scale-110'
                }`}
                style={{ background: hex }}
              />
            ))}
            <label className="w-7 h-7 rounded-full border-2 border-dashed border-outline-variant flex items-center justify-center cursor-pointer hover:border-primary text-[10px] text-on-surface-variant" title="自定义">
              <input type="color" value={seedColor} onChange={(e) => changeSeedColor(e.target.value)} className="w-0 h-0 opacity-0" />
              ＋
            </label>
          </div>
        </div>
      </Section>

      {/* ─── CLIProxyAPI 连接 ────────── */}
      <Section
        title="CLIProxyAPI 连接配置"
        sub="配置本地 CLIProxyAPI 服务地址和 Management Key，用于统计看板读取原生数据"
        icon={Server}
        actions={
          <button
            type="button"
            onClick={saveClipProxy}
            disabled={loading || savingClip}
            className="flex items-center gap-2 px-5 py-2 bg-primary text-on-primary rounded-full text-sm font-medium hover:opacity-90 disabled:opacity-50"
          >
            <Save size={14} />
            {savingClip ? t('SETTINGS.BTN_SAVING', '保存中…') : '保存连接配置'}
          </button>
        }
      >
        <div className="flex flex-col md:flex-row md:items-center justify-between py-3 border-b border-outline-variant/20 gap-3">
          <div className="flex flex-col gap-1 w-full md:w-2/5">
            <span className="text-on-surface-variant font-medium text-sm">服务地址</span>
            <span className="text-xs text-outline">CLIProxyAPI 本地服务的 HTTP 地址</span>
          </div>
          <input
            type="text"
            value={configs.cliproxy_url || ''}
            onChange={e => handleChange('cliproxy_url', e.target.value)}
            placeholder="http://127.0.0.1:8080"
            className="bg-surface-container-high border border-outline text-on-surface rounded-control px-4 py-2 outline-none text-sm w-full md:w-72 focus:border-primary transition-colors"
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
              value={configs.cliproxy_key || ''}
              onChange={e => handleChange('cliproxy_key', e.target.value)}
              placeholder="输入 Management Key"
              className="bg-surface-container-high border border-outline text-on-surface rounded-control px-4 py-2 pr-10 outline-none text-sm w-full focus:border-primary transition-colors"
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
      </Section>
    </PageContainer>
  );
};

export default GeneralAdminPage;
