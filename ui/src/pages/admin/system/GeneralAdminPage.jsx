import React, { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Monitor, Server, Save, Eye, EyeOff } from 'lucide-react';
import { PageContainer, PageHeader, Section } from '../../../components/ui';
import TextInput from '../../../components/ui/TextInput';
import { useAdminConfigs } from '../../../hooks/useAdminConfigs';
import { useTheme } from '../../../context/ThemeContext';
import { authFetch } from '../../../utils/authFetch';
import toast from 'react-hot-toast';

const SEED_COLORS = [
  { hex: '#7c5cff', nameKey: 'ADMIN_SYS.GENERAL.SEED_COLOR_PURPLE' },
  { hex: '#2563eb', nameKey: 'ADMIN_SYS.GENERAL.SEED_COLOR_BLUE' },
  { hex: '#059669', nameKey: 'ADMIN_SYS.GENERAL.SEED_COLOR_CYAN' },
  { hex: '#ea580c', nameKey: 'ADMIN_SYS.GENERAL.SEED_COLOR_ORANGE' },
  { hex: '#dc2626', nameKey: 'ADMIN_SYS.GENERAL.SEED_COLOR_RED' },
  { hex: '#0891b2', nameKey: 'ADMIN_SYS.GENERAL.SEED_COLOR_TEAL' },
  { hex: '#a16207', nameKey: 'ADMIN_SYS.GENERAL.SEED_COLOR_GOLD' },
  { hex: '#475569', nameKey: 'ADMIN_SYS.GENERAL.SEED_COLOR_GRAY' },
];

/**
 * Admin general settings for appearance and CLIProxyAPI connection details.
 */
const GeneralAdminPage = () => {
  const { t } = useTranslation();
  const { themePref, changeTheme, seedColor, changeSeedColor } = useTheme();
  const { configs, loading, handleChange } = useAdminConfigs();
  const [showClipKey, setShowClipKey] = useState(false);
  const [savingClip, setSavingClip] = useState(false);

  const saveClipProxy = async () => {
    if (!(configs.cliproxy_url || '').trim()) {
      toast.error(t('ADMIN_SYS.GENERAL.CLIPROXY_URL_REQUIRED'));
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
        toast.success(t('ADMIN_SYS.GENERAL.CLIPROXY_SAVE_OK'));
      } else {
        toast.error(data.message || t('SETTINGS.SAVE_FAILED'));
      }
    } catch {
      toast.error(t('SETTINGS.SAVE_FAILED'));
    } finally {
      setSavingClip(false);
    }
  };

  return (
    <PageContainer>
      <PageHeader
        title={t('ADMIN_SYS.GENERAL.TITLE')}
        sub={t('ADMIN_SYS.GENERAL.DESC')}
        icon={Monitor}
      />

      <Section title={t('ADMIN_SYS.GENERAL.APPEARANCE_TITLE')} sub={t('ADMIN_SYS.GENERAL.APPEARANCE_DESC')}>
        <div className="flex flex-col md:flex-row md:items-center justify-between gap-4 py-2">
          <span className="text-sm text-on-surface">{t('ADMIN_SYS.GENERAL.THEME_MODE_LABEL')}</span>
          <div role="radiogroup" aria-label={t('ADMIN_SYS.GENERAL.THEME_MODE_LABEL')}
            className="inline-flex rounded-control border border-outline-variant bg-surface p-0.5"
          >
            {[
              { v: 'light', label: t('ADMIN_SYS.GENERAL.THEME_LIGHT') },
              { v: 'dark',  label: t('ADMIN_SYS.GENERAL.THEME_DARK') },
              { v: 'system', label: t('ADMIN_SYS.GENERAL.THEME_SYSTEM') },
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
            <span className="text-sm text-on-surface">{t('ADMIN_SYS.GENERAL.SEED_COLOR_LABEL')}</span>
            <span className="text-xs text-on-surface-variant">{t('ADMIN_SYS.GENERAL.SEED_COLOR_HINT')}</span>
          </div>
          <div className="flex items-center gap-2 flex-wrap">
            {SEED_COLORS.map(({ hex, nameKey }) => {
              const name = t(nameKey);
              return (
              <button
                key={hex} type="button" onClick={() => changeSeedColor(hex)}
                title={name} aria-label={t('ADMIN_SYS.GENERAL.SEED_COLOR_ARIA', { name })}
                className={`w-7 h-7 rounded-full border-2 transition ${
                  seedColor.toLowerCase() === hex.toLowerCase()
                    ? 'border-on-surface scale-110' : 'border-outline-variant hover:scale-110'
                }`}
                style={{ background: hex }}
              />
              );
            })}
            <label className="w-7 h-7 rounded-full border-2 border-dashed border-outline-variant flex items-center justify-center cursor-pointer hover:border-primary text-[10px] text-on-surface-variant" title={t('ADMIN_SYS.GENERAL.SEED_COLOR_CUSTOM')}>
              <input type="color" value={seedColor} onChange={(e) => changeSeedColor(e.target.value)} className="w-0 h-0 opacity-0" />
              +
            </label>
          </div>
        </div>
      </Section>

      <Section
        title={t('ADMIN_SYS.GENERAL.CLIPROXY_TITLE')}
        sub={t('ADMIN_SYS.GENERAL.CLIPROXY_DESC')}
        icon={Server}
        actions={
          <button
            type="button"
            onClick={saveClipProxy}
            disabled={loading || savingClip}
            className="flex items-center gap-2 px-5 py-2 bg-primary text-on-primary rounded-full text-sm font-medium hover:opacity-90 disabled:opacity-50"
          >
            <Save size={14} />
            {savingClip ? t('COMMON.SAVING') : t('ADMIN_SYS.GENERAL.CLIPROXY_SAVE')}
          </button>
        }
      >
        <div className="flex flex-col md:flex-row md:items-center justify-between py-3 border-b border-outline-variant/20 gap-3">
          <div className="flex flex-col gap-1 w-full md:w-2/5">
            <span className="text-on-surface-variant font-medium text-sm">{t('ADMIN_SYS.GENERAL.CLIPROXY_URL_LABEL')}</span>
            <span className="text-xs text-outline">{t('ADMIN_SYS.GENERAL.CLIPROXY_URL_HINT')}</span>
          </div>
          <div className="w-full md:w-72">
            <TextInput
              type="text"
              value={configs.cliproxy_url || ''}
              onChange={e => handleChange('cliproxy_url', e.target.value)}
              placeholder="http://127.0.0.1:8080"
            />
          </div>
        </div>

        <div className="flex flex-col md:flex-row md:items-center justify-between py-3 gap-3">
          <div className="flex flex-col gap-1 w-full md:w-2/5">
            <span className="text-on-surface-variant font-medium text-sm">Management Key</span>
            <span className="text-xs text-outline">{t('ADMIN_SYS.GENERAL.CLIPROXY_KEY_HINT')}</span>
          </div>
          <div className="w-full md:w-72">
            <TextInput
              type={showClipKey ? 'text' : 'password'}
              value={configs.cliproxy_key || ''}
              onChange={e => handleChange('cliproxy_key', e.target.value)}
              placeholder={t('ADMIN_SYS.GENERAL.CLIPROXY_KEY_PLACEHOLDER')}
              suffix={showClipKey ? EyeOff : Eye}
              onSuffixClick={() => setShowClipKey(v => !v)}
            />
          </div>
        </div>
      </Section>
    </PageContainer>
  );
};

export default GeneralAdminPage;
