import React, { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Activity, Eye, EyeOff, KeyRound, ShieldAlert } from 'lucide-react';
import toast from 'react-hot-toast';
import { PageContainer, PageHeader, FormRow } from '../../../components/ui';
import TextInput from '../../../components/ui/TextInput';
import Switch from '../../../components/ui/Switch';
import { useAdminConfigs } from '../../../hooks/useAdminConfigs';
import { useMaskState } from '../../../hooks/useMaskState';
import { authFetch } from '../../../utils/authFetch';
import { SaveBar } from './_AdminFormPrimitives';

const SYNC_CONFIG_DEFAULTS = {
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

  const values = useMemo(() => Object.fromEntries(
    Object.entries(SYNC_CONFIG_DEFAULTS).map(([key, fallback]) => [key, configs[key] ?? fallback]),
  ), [configs]);

  const validate = () => {
    const checks = [
      ['cpa_project_id_refresh_seconds', 300, 31536000, 'Project ID 刷新周期必须是 300-31536000 秒'],
      ['cliproxy_usage_sync_interval_seconds', 10, 3600, 'CLIProxy 同步间隔必须 是 10-3600 秒'],
      ['cliproxy_usage_sync_batch_size', 1, 1000, 'CLIProxy 同步批次必须是 1-1000 条'],
      ['apilog_retention_days', 0, 3650, 'API 日志保留天数必须是 0-3650 天'],   
      ['apilog_cleanup_batch_size', 1, 100000, 'API 日志清理批次必须是 1-100000 条'],
    ];
    for (const [key, min, max, message] of checks) {
      const n = parseInt(values[key], 10);
      if (Number.isNaN(n) || n < min || n > max) {
        toast.error(message);
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
          (data.message_code ? t('API.' + data.message_code) : null)
          || data.message
          || t('SETTINGS.SAVE_SUCCESS', '保存成功'),
        );
        refetch();
      } else {
        toast.error(
          (data.message_code ? t('API.' + data.message_code) : data.message)    
          || t('SETTINGS.SAVE_FAILED', '保存失败'),
        );
      }
    } finally {
      setSaving(false);
    }
  };

  return (
    <PageContainer>
      <PageHeader
        title={t('SETTINGS.TAB_SYNC', '号池同步')}
        sub={t('SETTINGS.SYNC_DESC', 'CLIProxy 用量同步、鉴权和日志清理配置')}  
        icon={Activity}
      />

      <div className="space-y-6">
        <FormRow.Group
          title="CLIProxy 用量同步"
          sub="控制后台自动拉取 CLIProxyAPI usage queue 的节奏和批次。"
        >
          <FormRow
            label="自动同步"
            hint="SysConfig: cliproxy_usage_sync_enabled；默认 true。关闭后仅保 留手动同步。"
            htmlFor="cliproxy_usage_sync_enabled"
          >
            <BoolSwitch
              id="cliproxy_usage_sync_enabled"
              checked={toBool(values.cliproxy_usage_sync_enabled)}
              onChange={(value) => handleChange('cliproxy_usage_sync_enabled', value)}
            />
          </FormRow>
          <FormRow
            label="同步间隔"
            hint="SysConfig: cliproxy_usage_sync_interval_seconds；默认 60 秒， 范围 10-3600 秒。"
            htmlFor="cliproxy_usage_sync_interval_seconds"
          >
            <NumberInput
              id="cliproxy_usage_sync_interval_seconds"
              min="10"
              max="3600"
              unit="秒"
              value={values.cliproxy_usage_sync_interval_seconds}
              onChange={(value) => handleChange('cliproxy_usage_sync_interval_seconds', value)}
            />
          </FormRow>
          <FormRow
            label="同步批次"
            hint="SysConfig: cliproxy_usage_sync_batch_size；默认 100 条，最大 1000 条。"
            htmlFor="cliproxy_usage_sync_batch_size"
          >
            <NumberInput
              id="cliproxy_usage_sync_batch_size"
              min="1"
              max="1000"
              unit="条"
              value={values.cliproxy_usage_sync_batch_size}
              onChange={(value) => handleChange('cliproxy_usage_sync_batch_size', value)}
            />
          </FormRow>
          <FormRow
            label="Project ID 刷新周期"
            hint="SysConfig: cpa_project_id_refresh_seconds；默认 86400 秒，最小 300 秒。"
            htmlFor="cpa_project_id_refresh_seconds"
            last
          >
            <NumberInput
              id="cpa_project_id_refresh_seconds"
              min="300"
              max="31536000"
              unit="秒"
              value={values.cpa_project_id_refresh_seconds}
              onChange={(value) => handleChange('cpa_project_id_refresh_seconds', value)}
            />
          </FormRow>
        </FormRow.Group>

        <FormRow.Group
          title="安全与鉴权"
          sub="管理审核鉴权 key 与仅本机代理可用的 TLS 跳过开关。"
        >
          <FormRow
            label="内容审核 API Key"
            hint="SysConfig: moderation_cliproxy_api_key；为空则回退到同地址 cliproxy 渠道 API key，再回退到 cliproxy_key。"
            htmlFor="moderation_cliproxy_api_key"
          >
            <div className="w-full md:w-[420px]">
              <TextInput
                id="moderation_cliproxy_api_key"
                type={mask.moderation_cliproxy_api_key ? 'text' : 'password'}   
                value={values.moderation_cliproxy_api_key}
                onChange={(e) => handleChange('moderation_cliproxy_api_key', e.target.value)}
                placeholder="留空则使用渠道 key"
                prefix={KeyRound}
                suffix={mask.moderation_cliproxy_api_key ? EyeOff : Eye}
                onSuffixClick={() => toggleMask('moderation_cliproxy_api_key')}
                className="font-mono"
              />
            </div>
          </FormRow>
          <FormRow
            label="本机代理跳过 TLS 校验"
            hint="SysConfig: proxy_tls_skip_verify；默认 false。仅对本机或内网代理生效。"
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

        <FormRow.Group title="API 日志清理" sub="控制审计日志保留周期和单次清理 规模。">
          <FormRow
            label="保留天数"
            hint="SysConfig: apilog_retention_days；默认 90 天，0 表示不清理。" 
            htmlFor="apilog_retention_days"
          >
            <NumberInput
              id="apilog_retention_days"
              min="0"
              max="3650"
              unit="天"
              value={values.apilog_retention_days}
              onChange={(value) => handleChange('apilog_retention_days', value)}
            />
          </FormRow>
          <FormRow
            label="清理批次"
            hint="SysConfig: apilog_cleanup_batch_size；默认 5000 条，单次越大锁表时间可能越长。"
            htmlFor="apilog_cleanup_batch_size"
            last
          >
            <NumberInput
              id="apilog_cleanup_batch_size"
              min="1"
              max="100000"
              unit="条"
              value={values.apilog_cleanup_batch_size}
              onChange={(value) => handleChange('apilog_cleanup_batch_size', value)}
            />
          </FormRow>
        </FormRow.Group>
      </div>

      <div className="mt-6 flex items-start gap-2 text-xs text-on-surface-variant">
        <ShieldAlert size={14} className="mt-0.5 shrink-0 text-warning" />      
        <p>敏感 key 会按后端统一规则脱敏显示；保存掩码值时后端会跳过该字段，避免覆盖真实密钥。</p>
      </div>

      <div className="mt-8">
        <SaveBar loading={saving} onSave={save} />
      </div>
    </PageContainer>
  );
};

export default SyncPage;
