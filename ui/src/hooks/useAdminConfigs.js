/**
 * useAdminConfigs — admin /api/admin/config GET + POST 共享 hook（Phase 3）
 *
 * 抽自 Settings.jsx 的内联 fetch/handleSave/handleChange/validateCreditsConfig 逻辑。
 * 给所有 admin form page 用：
 *   - configs / setConfigs 表单状态
 *   - loading 标志
 *   - handleChange(key, val)
 *   - handleSave(partialPayload?, successMsg?)
 *
 * 实现保持原 Settings 行为：
 *   - GET 一次（mount 或 refetch 触发）
 *   - POST 全 configs（除非 partialPayload 传入）
 *   - moderation_provider 强制 'cliproxy_model'
 *   - 旧 gemini moderation 字段从 payload 删
 *   - validateCreditsConfig 数值范围校验
 */
import { useState, useEffect, useCallback } from 'react';
import toast from 'react-hot-toast';
import { useTranslation } from 'react-i18next';
import { authFetch } from '../utils/authFetch';

const DEFAULT_CONFIGS = {
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
  exchange_rate_rmb_per_usd_micros: '',
  balance_consume_default_enabled: 'false',
  balance_consume_default_limit_usd: '0',
  balance_consume_default_window_secs: '2592000',
  cliproxy_url: '',
  cliproxy_key: '',
  credits_refresh_interval: '15',
  credits_max_retries: '3',
  credits_retry_interval: '5',
  moderation_provider: 'cliproxy_model',
  moderation_cliproxy_model: 'gpt-5.4-mini',
  moderation_threshold: '0.8',
  moderation_api_timeout_seconds: '15',
  moderation_image_policy: 'reject',
  moderation_autoban_enabled: 'false',
  moderation_autoban_keyword_threshold: '1',
  moderation_autoban_policy_threshold: '0',
  moderation_autoban_risk_rule_threshold: '1',
  moderation_autoban_risk_score_threshold: '0',
  moderation_autoban_image_threshold: '2',
  moderation_autoban_oversize_threshold: '0',
  moderation_autoban_window_seconds: '86400',
  moderation_keyword_ai_max_candidates: '80',
};

// 与 Settings.jsx 内 validateCreditsConfig 完全一致
const validateConfigs = (cfg) => {
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
    const w = parseInt(cfg.balance_consume_default_window_secs, 10);
    if (Number.isNaN(w) || w < 60 || w > 365 * 24 * 60 * 60) {
      errors.push('新用户余额消费默认窗口必须在 60 秒到 365 天之间');
    }
  }
  if (cfg.moderation_autoban_enabled !== undefined) {
    const enabled = String(cfg.moderation_autoban_enabled).trim().toLowerCase();
    if (!['true', 'false'].includes(enabled)) {
      errors.push('自动封禁开关必须是 true/false');
    }
  }
  ['moderation_autoban_keyword_threshold', 'moderation_autoban_policy_threshold',
   'moderation_autoban_risk_rule_threshold', 'moderation_autoban_risk_score_threshold',
   'moderation_autoban_image_threshold', 'moderation_autoban_oversize_threshold',
  ].forEach((key) => {
    if (cfg[key] !== undefined) {
      const n = parseInt(cfg[key], 10);
      if (Number.isNaN(n) || n < 0 || n > 100) errors.push('自动封禁阈值必须是 0-100 之间的整数');
    }
  });
  if (cfg.moderation_autoban_window_seconds !== undefined) {
    const n = parseInt(cfg.moderation_autoban_window_seconds, 10);
    if (Number.isNaN(n) || n < 60 || n > 365 * 24 * 60 * 60) {
      errors.push('自动封禁统计窗口必须在 60 秒到 365 天之间');
    }
  }
  if (cfg.moderation_keyword_ai_max_candidates !== undefined) {
    const n = parseInt(cfg.moderation_keyword_ai_max_candidates, 10);
    if (Number.isNaN(n) || n < 1 || n > 200) errors.push('AI 词库候选数量必须是 1-200 之间的整数');
  }
  if (cfg.moderation_api_timeout_seconds !== undefined) {
    const n = parseInt(cfg.moderation_api_timeout_seconds, 10);
    if (Number.isNaN(n) || n < 1 || n > 120) errors.push('审核模型超时必须是 1-120 秒之间的整数');
  }
  return errors;
};

// ─── module-level cache + dedupe（Phase 5 codex 审查 P5-3）───────
//
// 原实现：每个 admin form page mount 都 GET /api/admin/config，频繁切换页面会反复
// 拉同一份配置；admin cookie 30s 校验也是独立请求。codex 审查指出这是 Major 性能问题。
//
// 修复：
//   - 模块级缓存：第一个 mount 触发 fetch，后续 mount 直接复用
//   - inflight 共享：多 page 同时 mount 只发 1 个 fetch，其它 page 等同一个 promise
//   - 30s TTL：超期下次 mount 才重新 fetch（与 AuthContext 30s 校验节奏一致）
//   - refetch() 强制刷新：保存配置后 caller 调用，立即更新所有 page
//
const CACHE_TTL_MS = 30000;
let cachedAt = 0;
let cachedData = null;
let inflight = null;
const subscribers = new Set();

const isFresh = () => cachedData && (Date.now() - cachedAt < CACHE_TTL_MS);

const fetchAndCache = async () => {
  if (inflight) return inflight;
  inflight = (async () => {
    try {
      const res = await fetch('/api/admin/config', { credentials: 'include' });
      const data = await res.json();
      if (data.success && data.data) {
        cachedData = { ...DEFAULT_CONFIGS, ...data.data };
        cachedAt = Date.now();
        // 通知所有订阅者
        subscribers.forEach(notify => notify(cachedData));
        return cachedData;
      }
      throw new Error('admin config response failed');
    } finally {
      inflight = null;
    }
  })();
  return inflight;
};

export const useAdminConfigs = () => {
  const { t } = useTranslation();
  const [configs, setConfigs] = useState(() => cachedData || DEFAULT_CONFIGS);
  const [loading, setLoading] = useState(false);
  const [fetched, setFetched] = useState(() => isFresh());

  const fetchConfigs = useCallback(async (force = false) => {
    if (!force && isFresh()) {
      setConfigs(cachedData);
      setFetched(true);
      return;
    }
    try {
      const data = await fetchAndCache();
      setConfigs(data);
    } catch {
      toast.error('加载系统配置失败');
    } finally {
      setFetched(true);
    }
  }, []);

  useEffect(() => {
    // 订阅模块级 cache 更新（其他 hook 实例触发 fetch/save 时同步）
    const sub = (next) => setConfigs(next);
    subscribers.add(sub);
    fetchConfigs();
    return () => subscribers.delete(sub);
  }, [fetchConfigs]);

  const handleChange = useCallback((key, val) => {
    setConfigs(prev => ({ ...prev, [key]: val }));
  }, []);

  const handleSave = useCallback(async (partialPayload = null, successMsg = null) => {
    const payload = { ...(partialPayload || configs) };
    // 强制 moderation provider，移除已废弃 gemini 字段
    if (payload.moderation_provider !== undefined) payload.moderation_provider = 'cliproxy_model';
    delete payload.moderation_gemini_endpoint;
    delete payload.moderation_gemini_model;
    delete payload.moderation_gemini_auth_index;
    delete payload.moderation_gemini_safety_threshold;

    const errs = validateConfigs(payload);
    if (errs.length > 0) {
      toast.error(errs.join('；'));
      return false;
    }

    setLoading(true);
    try {
      const data = await authFetch('/api/admin/config', {
        method: 'POST',
        body: payload,
      });
      if (data.success) {
        const msg = successMsg
          || (data.message_code ? t('API.' + data.message_code) : null)
          || data.message
          || t('SETTINGS.SAVE_SUCCESS', '保存成功');
        toast.success(msg);
        // 保存成功后让 cache 失效，下个 mount 会重新 fetch（保证全 admin page 看到最新）
        cachedData = null;
        cachedAt = 0;
        return true;
      }
      toast.error((data.message_code ? t('API.' + data.message_code) : data.message)
        || t('SETTINGS.SAVE_FAILED', '保存失败'));
      return false;
    } catch {
      toast.error(t('SETTINGS.SAVE_FAILED', '保存失败'));
      return false;
    } finally {
      setLoading(false);
    }
  }, [configs, t]);

  return {
    configs, setConfigs, fetched, loading, handleChange, handleSave,
    refetch: () => fetchConfigs(true),
  };
};
