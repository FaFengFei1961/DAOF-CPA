import React, { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Activity, Eye, EyeOff, KeyRound, RefreshCw, ShieldAlert } from 'lucide-react';
import toast from 'react-hot-toast';
import { PageContainer, PageHeader, FormRow } from '../../../components/ui';
import TextInput from '../../../components/ui/TextInput';
import Switch from '../../../components/ui/Switch';
import { useAdminConfigs } from '../../../hooks/useAdminConfigs';
import { useMaskState } from '../../../hooks/useMaskState';
import { authFetch } from '../../../utils/authFetch';
import { SaveBar } from './_AdminFormPrimitives';

const SYNC_CONFIG_DEFAULTS = {
  // 号池额度采集器
  credits_refresh_interval: '15',
  credits_max_retries: '3',
  credits_retry_interval: '5',
  // CLIProxy 用量同步
  moderation_cliproxy_api_key: '',
  cpa_project_id_refresh_seconds: '86400',
  proxy_tls_skip_verify: 'false',
  cliproxy_usage_sync_enabled: 'true',
  cliproxy_usage_sync_interval_seconds: '60',
  cliproxy_usage_sync_batch_size: '100',
  apilog_retention_days: '90',
  apilog_cleanup_batch_size: '5000',
};

const toBool = (value) => ['true', '1', 'yes', 'on'].includes(String(value ?? '').trim().toLowerCase());

const NumberInput = ({ id, value, onChange, min, max, unit }) => (
  <div className="relative w-full md:w-56">
    <TextInput
      id={id}
      type="number"
      min={min}
      max={max}
      value={value}
      onChange={(e) => onChange(e.target.value)}
      className="text-right"
      style={{ paddingRight: '3rem' }}
    />
    <span className="absolute right-3 top-2.5 text-xs text-on-surface-variant pointer-events-none z-10">{unit}</span>
  </div>
);

const BoolSwitch = ({ id, checked, onChange }) => (
  <div className="inline-flex items-center gap-2">
    <Switch
      id={id}
      checked={checked}
      onChange={(newChecked) => onChange(newChecked ? 'true' : 'false')}
    />
    <span className="text-xs font-mono text-on-surface-variant cursor-pointer select-none" onClick={() => onChange(checked ? 'false' : 'true')}>{checked ? 'true' : 'false'}</span>
  </div>
);

const SyncPage = () => {
  const { t } = useTranslation();
  const { configs, handleChange, refetch } = useAdminConfigs();
  const [mask, toggleMask] = useMaskState();
  const [saving, setSaving] = useState(false);
  const [syncingUsage, setSyncingUsage] = useState(false);
  const [usageSyncResult, setUsageSyncResult] = useState(null);

  const values = useMemo(() => Object.fromEntries(
    Object.entries(SYNC_CONFIG_DEFAULTS).map(([key, fallback]) => [key, configs[key] ?? fallback]),
  ), [configs]);

  const validate = () => {
    const checks = [
      ['credits_refresh_interval', 1, 1440, 'ADMIN_SYS.SYNC.VALIDATION.COLLECTOR_REFRESH'],
      ['credits_max_retries', 0, 100, 'ADMIN_SYS.SYNC.VALIDATION.COLLECTOR_RETRIES'],
      ['credits_retry_interval', 1, 1440, 'ADMIN_SYS.SYNC.VALIDATION.COLLECTOR_RETRY_INTERVAL'],
      ['cpa_project_id_refresh_seconds', 300, 31536000, 'ADMIN_SYS.SYNC.VALIDATION.PROJECT_REFRESH'],
      ['cliproxy_usage_sync_interval_seconds', 10, 3600, 'ADMIN_SYS.SYNC.VALIDATION.SYNC_INTERVAL'],
      ['cliproxy_usage_sync_batch_size', 1, 1000, 'ADMIN_SYS.SYNC.VALIDATION.SYNC_BATCH'],
      ['apilog_retention_days', 0, 3650, 'ADMIN_SYS.SYNC.VALIDATION.LOG_RETENTION'],
      ['apilog_cleanup_batch_size', 1, 100000, 'ADMIN_SYS.SYNC.VALIDATION.LOG_CLEANUP_BATCH'],
    ];
    for (const [key, min, max, messageKey] of checks) {
      const n = parseInt(values[key], 10);
      if (Number.isNaN(n) || n < min || n > max) {
        toast.error(t(messageKey));
        return false;
      }
    }
    return true;
  };

  const save = async () => {
    if (!validate()) return;
    setSaving(true);
    try {
      const payload = Object.fromEntries(
        Object.keys(SYNC_CONFIG_DEFAULTS).map((key) => [key, String(values[key] ?? '')]),
      );
      const data = await authFetch('/api/admin/config?allow_empty=1', {
        method: 'POST',
        body: payload,
      });
      if (data.success) {
        toast.success(
          (data.message_code ? t(`API.${data.message_code}`) : null)
          || data.message
          || t('SETTINGS.SAVE_SUCCESS'),
        );
        refetch();
      } else {
        toast.error(
          (data.message_code ? t(`API.${data.message_code}`) : data.message)
          || t('SETTINGS.SAVE_FAILED'),
        );
      }
    } finally {
      setSaving(false);
    }
  };

  const syncUsageNow = async () => {
    const count = parseInt(values.cliproxy_usage_sync_batch_size, 10);
    if (Number.isNaN(count) || count < 1 || count > 1000) {
      toast.error(t('ADMIN_SYS.SYNC.VALIDATION.SYNC_BATCH'));
      return;
    }
    setSyncingUsage(true);
    try {
      const data = await authFetch(`/api/admin/cliproxy/usage/sync?count=${count}`, {
        method: 'POST',
      });
      if (data.success) {
        const result = data.data || {};
        setUsageSyncResult(result);
        toast.success(t('ADMIN_SYS.SYNC.MANUAL_SYNC_OK', {
          fetched: result.fetched ?? 0,
          stored: result.stored ?? 0,
          matched: result.matched ?? 0,
          unmatched: result.unmatched ?? 0,
          ignored: result.ignored ?? 0,
        }));
      } else {
        toast.error(
          (data.message_code ? t(`API.${data.message_code}`) : data.message)
          || t('ADMIN_SYS.SYNC.MANUAL_SYNC_FAIL'),
        );
      }
    } catch {
      toast.error(t('ADMIN_SYS.SYNC.MANUAL_SYNC_FAIL'));
    } finally {
      setSyncingUsage(false);
    }
  };

  return (
    <PageContainer>
      <PageHeader
        title={t('ADMIN_SYS.SYNC.TITLE')}
        sub={t('ADMIN_SYS.SYNC.DESC')}
        icon={Activity}
      />

      <div className="space-y-6">
        <FormRow.Group
          title={t('ADMIN_SYS.SYNC.COLLECTOR_GROUP_TITLE')}
          sub={t('ADMIN_SYS.SYNC.COLLECTOR_GROUP_DESC')}
        >
          <FormRow
            label={t('ADMIN_SYS.SYNC.COLLECTOR_REFRESH_LABEL')}
            hint={t('ADMIN_SYS.SYNC.COLLECTOR_REFRESH_HINT')}
            htmlFor="credits_refresh_interval"
          >
            <NumberInput
              id="credits_refresh_interval"
              min="1"
              max="1440"
              unit={t('ADMIN_SYS.UNIT.MINUTES')}
              value={values.credits_refresh_interval}
              onChange={(value) => handleChange('credits_refresh_interval', value)}
            />
          </FormRow>
          <FormRow
            label={t('ADMIN_SYS.SYNC.COLLECTOR_RETRIES_LABEL')}
            hint={t('ADMIN_SYS.SYNC.COLLECTOR_RETRIES_HINT')}
            htmlFor="credits_max_retries"
          >
            <NumberInput
              id="credits_max_retries"
              min="0"
              max="100"
              unit={t('ADMIN_SYS.UNIT.TIMES')}
              value={values.credits_max_retries}
              onChange={(value) => handleChange('credits_max_retries', value)}
            />
          </FormRow>
          <FormRow
            label={t('ADMIN_SYS.SYNC.COLLECTOR_RETRY_INTERVAL_LABEL')}
            hint={t('ADMIN_SYS.SYNC.COLLECTOR_RETRY_INTERVAL_HINT')}
            htmlFor="credits_retry_interval"
            last
          >
            <NumberInput
              id="credits_retry_interval"
              min="1"
              max="1440"
              unit={t('ADMIN_SYS.UNIT.MINUTES')}
              value={values.credits_retry_interval}
              onChange={(value) => handleChange('credits_retry_interval', value)}
            />
          </FormRow>
        </FormRow.Group>

        <FormRow.Group
          title={t('ADMIN_SYS.SYNC.USAGE_GROUP_TITLE')}
          sub={t('ADMIN_SYS.SYNC.USAGE_GROUP_DESC')}
        >
          <FormRow
            label={t('ADMIN_SYS.SYNC.AUTO_SYNC_LABEL')}
            hint={t('ADMIN_SYS.SYNC.AUTO_SYNC_HINT')}
            htmlFor="cliproxy_usage_sync_enabled"
          >
            <BoolSwitch
              id="cliproxy_usage_sync_enabled"
              checked={toBool(values.cliproxy_usage_sync_enabled)}
              onChange={(value) => handleChange('cliproxy_usage_sync_enabled', value)}
            />
          </FormRow>
          <FormRow
            label={t('ADMIN_SYS.SYNC.MANUAL_SYNC_LABEL')}
            hint={usageSyncResult
              ? t('ADMIN_SYS.SYNC.MANUAL_SYNC_LAST', {
                  fetched: usageSyncResult.fetched ?? 0,
                  stored: usageSyncResult.stored ?? 0,
                  matched: usageSyncResult.matched ?? 0,
                  unmatched: usageSyncResult.unmatched ?? 0,
                  ignored: usageSyncResult.ignored ?? 0,
                })
              : t('ADMIN_SYS.SYNC.MANUAL_SYNC_HINT')}
          >
            <button
              type="button"
              onClick={syncUsageNow}
              disabled={syncingUsage}
              className="inline-flex items-center justify-center gap-2 px-4 py-2 rounded-overlay border border-primary/40 bg-primary/15 text-primary hover:bg-primary/25 disabled:opacity-60 disabled:cursor-not-allowed min-w-[128px]"
            >
              <RefreshCw size={16} className={syncingUsage ? 'animate-spin' : ''} />
              {syncingUsage ? t('ADMIN_SYS.SYNC.MANUAL_SYNC_RUNNING') : t('ADMIN_SYS.SYNC.MANUAL_SYNC_BUTTON')}
            </button>
          </FormRow>
          <FormRow
            label={t('ADMIN_SYS.SYNC.INTERVAL_LABEL')}
            hint={t('ADMIN_SYS.SYNC.INTERVAL_HINT')}
            htmlFor="cliproxy_usage_sync_interval_seconds"
          >
            <NumberInput
              id="cliproxy_usage_sync_interval_seconds"
              min="10"
              max="3600"
              unit={t('ADMIN_SYS.UNIT.SECONDS')}
              value={values.cliproxy_usage_sync_interval_seconds}
              onChange={(value) => handleChange('cliproxy_usage_sync_interval_seconds', value)}
            />
          </FormRow>
          <FormRow
            label={t('ADMIN_SYS.SYNC.BATCH_LABEL')}
            hint={t('ADMIN_SYS.SYNC.BATCH_HINT')}
            htmlFor="cliproxy_usage_sync_batch_size"
          >
            <NumberInput
              id="cliproxy_usage_sync_batch_size"
              min="1"
              max="1000"
              unit={t('ADMIN_SYS.UNIT.ROWS')}
              value={values.cliproxy_usage_sync_batch_size}
              onChange={(value) => handleChange('cliproxy_usage_sync_batch_size', value)}
            />
          </FormRow>
          <FormRow
            label={t('ADMIN_SYS.SYNC.PROJECT_REFRESH_LABEL')}
            hint={t('ADMIN_SYS.SYNC.PROJECT_REFRESH_HINT')}
            htmlFor="cpa_project_id_refresh_seconds"
            last
          >
            <NumberInput
              id="cpa_project_id_refresh_seconds"
              min="300"
              max="31536000"
              unit={t('ADMIN_SYS.UNIT.SECONDS')}
              value={values.cpa_project_id_refresh_seconds}
              onChange={(value) => handleChange('cpa_project_id_refresh_seconds', value)}
            />
          </FormRow>
        </FormRow.Group>

        <FormRow.Group
          title={t('ADMIN_SYS.SYNC.SECURITY_GROUP_TITLE')}
          sub={t('ADMIN_SYS.SYNC.SECURITY_GROUP_DESC')}
        >
          <FormRow
            label={t('ADMIN_SYS.SYNC.MODERATION_KEY_LABEL')}
            hint={t('ADMIN_SYS.SYNC.MODERATION_KEY_HINT')}
            htmlFor="moderation_cliproxy_api_key"
          >
            <div className="w-full md:w-[420px]">
              <TextInput
                id="moderation_cliproxy_api_key"
                type={mask.moderation_cliproxy_api_key ? 'text' : 'password'}   
                value={values.moderation_cliproxy_api_key}
                onChange={(e) => handleChange('moderation_cliproxy_api_key', e.target.value)}
                placeholder={t('ADMIN_SYS.SYNC.MODERATION_KEY_PLACEHOLDER')}
                prefix={KeyRound}
                suffix={mask.moderation_cliproxy_api_key ? EyeOff : Eye}
                onSuffixClick={() => toggleMask('moderation_cliproxy_api_key')}
                className="font-mono"
              />
            </div>
          </FormRow>
          <FormRow
            label={t('ADMIN_SYS.SYNC.TLS_SKIP_LABEL')}
            hint={t('ADMIN_SYS.SYNC.TLS_SKIP_HINT')}
            htmlFor="proxy_tls_skip_verify"
            last
          >
            <BoolSwitch
              id="proxy_tls_skip_verify"
              checked={toBool(values.proxy_tls_skip_verify)}
              onChange={(value) => handleChange('proxy_tls_skip_verify', value)}
            />
          </FormRow>
        </FormRow.Group>

        <FormRow.Group title={t('ADMIN_SYS.SYNC.LOG_GROUP_TITLE')} sub={t('ADMIN_SYS.SYNC.LOG_GROUP_DESC')}>
          <FormRow
            label={t('ADMIN_SYS.SYNC.RETENTION_DAYS_LABEL')}
            hint={t('ADMIN_SYS.SYNC.RETENTION_DAYS_HINT')}
            htmlFor="apilog_retention_days"
          >
            <NumberInput
              id="apilog_retention_days"
              min="0"
              max="3650"
              unit={t('ADMIN_SYS.UNIT.DAYS')}
              value={values.apilog_retention_days}
              onChange={(value) => handleChange('apilog_retention_days', value)}
            />
          </FormRow>
          <FormRow
            label={t('ADMIN_SYS.SYNC.CLEANUP_BATCH_LABEL')}
            hint={t('ADMIN_SYS.SYNC.CLEANUP_BATCH_HINT')}
            htmlFor="apilog_cleanup_batch_size"
            last
          >
            <NumberInput
              id="apilog_cleanup_batch_size"
              min="1"
              max="100000"
              unit={t('ADMIN_SYS.UNIT.ROWS')}
              value={values.apilog_cleanup_batch_size}
              onChange={(value) => handleChange('apilog_cleanup_batch_size', value)}
            />
          </FormRow>
        </FormRow.Group>
      </div>

      <div className="mt-6 flex items-start gap-2 text-xs text-on-surface-variant">
        <ShieldAlert size={14} className="mt-0.5 shrink-0 text-warning" />
        <p>{t('ADMIN_SYS.SYNC.SECRET_MASK_NOTE')}</p>
      </div>

      <div className="mt-8">
        <SaveBar loading={saving} onSave={save} />
      </div>
    </PageContainer>
  );
};

export default SyncPage;
