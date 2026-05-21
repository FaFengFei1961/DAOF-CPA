// Phase H-5（2026-05-20）：用户视角的"第三方账号绑定"卡片，嵌在账号设置页。
//
// 行为：
//   - 进场：并发拉 /api/public-config（找出 admin 配过哪些 provider）
//     + /api/user/oauth/identities（找出当前已绑定的）。
//   - 渲染：把"已配置 provider 全集"和"当前 active identity"做差集渲染：
//     · 已绑定：显示「✓ 已绑定 / 绑定于 XX」+ Unlink 按钮
//     · 未绑定：显示「立即绑定」按钮 → 调 /api/user/oauth/:provider/link/prepare
//       → 拼跳转 URL 到该 provider 的 authorize 端点（与 AuthModal 同范式）
//   - 解绑：confirm → POST /api/user/oauth/:provider/unlink；后端兜底"至少保留一种 auth"，
//     失败 toast；成功重新 load。
import React, { useState, useEffect, useCallback } from 'react';
import { useTranslation } from 'react-i18next';
import { Link2, ShieldCheck, Unlink as UnlinkIcon } from 'lucide-react';
import toast from 'react-hot-toast';
import { authFetch, isLoggedIn } from '../utils/authFetch';
import { useConfirm } from '../context/ConfirmContext';

// 内联 SVG 图标（lucide-react 没有 Github / Google brand logos）
const GitHubIconSVG = () => (
  <svg viewBox="0 0 24 24" width="18" height="18" aria-hidden="true" fill="currentColor">
    <path d="M12 .5C5.65.5.5 5.65.5 12c0 5.07 3.29 9.36 7.85 10.88.57.1.78-.25.78-.55v-1.93c-3.19.69-3.87-1.54-3.87-1.54-.52-1.34-1.27-1.69-1.27-1.69-1.04-.71.08-.7.08-.7 1.15.08 1.76 1.18 1.76 1.18 1.02 1.76 2.69 1.25 3.34.96.1-.74.4-1.25.73-1.54-2.55-.29-5.23-1.28-5.23-5.7 0-1.26.45-2.29 1.18-3.1-.12-.29-.51-1.46.11-3.04 0 0 .97-.31 3.18 1.18a11.05 11.05 0 0 1 5.79 0c2.2-1.49 3.17-1.18 3.17-1.18.63 1.58.23 2.75.12 3.04.73.81 1.18 1.84 1.18 3.1 0 4.43-2.69 5.41-5.25 5.69.41.36.78 1.07.78 2.17v3.21c0 .31.21.66.79.55C20.21 21.36 23.5 17.07 23.5 12 23.5 5.65 18.35.5 12 .5z"/>
  </svg>
);

const GoogleIconSVG = () => (
  <svg viewBox="0 0 48 48" width="18" height="18" aria-hidden="true">
    <path fill="#FFC107" d="M43.6 20.5H42V20H24v8h11.3a12 12 0 0 1-11.3 8 12 12 0 1 1 0-24c3 0 5.7 1.1 7.8 3l5.7-5.7A20 20 0 1 0 24 44a20 20 0 0 0 19.6-23.5z"/>
    <path fill="#FF3D00" d="M6.3 14.7l6.6 4.8A12 12 0 0 1 24 12c3 0 5.7 1.1 7.8 3l5.7-5.7A20 20 0 0 0 6.3 14.7z"/>
    <path fill="#4CAF50" d="M24 44a20 20 0 0 0 13.5-5.2l-6.2-5.3a12 12 0 0 1-7.3 2.5 12 12 0 0 1-11.3-8l-6.5 5A20 20 0 0 0 24 44z"/>
    <path fill="#1976D2" d="M43.6 20.5H42V20H24v8h11.3a12 12 0 0 1-4.1 5.5l6.2 5.3C36.8 36.5 44 31 44 24c0-1.2-.1-2.4-.4-3.5z"/>
  </svg>
);

// 仅图标按 icon_key 选内置 brand SVG，其它字段（label / authorize URL / default params）
// 全部从后端 GET /api/public-config.oauth_provider_metadata 拉取。
//
// fix H-Audit L8（2026-05-21）：原 PROVIDER_META 是 hardcoded github/google map +
// 每个 provider 一个 authorizeUrl 拼装函数，添加新 provider（Discord / Microsoft 等）
// 必须前端发版。现在前端纯渲染层，加新 provider 仅后端 oauth_provider_<key>.go 一份。
const PROVIDER_ICONS = {
  github: GitHubIconSVG,
  google: GoogleIconSVG,
  // 未知 provider 用 fallback（在 render 处处理）
};

// buildOAuthAuthorizeURL 根据 server 元数据拼 authorize URL。
// 强制注入参数（client_id / redirect_uri / state / code_challenge / code_challenge_method）
// 永远从运行时来，不可被 server defaults 覆盖；server 提供的 default_params 用来塞
// provider-specific 选项（如 Google 的 scope / access_type / prompt）。
const buildOAuthAuthorizeURL = (meta, { callbackUri, state, codeChallenge, codeChallengeMethod }) => {
  const params = new URLSearchParams();
  // 1. 先填 server 默认（按 key 排序保证幂等）
  Object.entries(meta.default_params || {}).forEach(([k, v]) => {
    params.set(k, v);
  });
  // 2. 运行时强制覆盖（哪怕 server 误传也以前端为准）
  params.set('client_id', meta.client_id || '');
  params.set('redirect_uri', callbackUri);
  params.set('state', state);
  params.set('code_challenge', codeChallenge);
  params.set('code_challenge_method', codeChallengeMethod || 'S256');
  return `${meta.authorize_endpoint}?${params.toString()}`;
};

const formatLinkedAt = (iso) => {
  if (!iso) return '';
  try {
    return new Date(iso).toLocaleString();
  } catch {
    return '';
  }
};

const UserLinkedAccounts = () => {
  const { t } = useTranslation();
  const confirm = useConfirm();
  const [loading, setLoading] = useState(true);
  const [submittingProvider, setSubmittingProvider] = useState(''); // 当前 in-flight 的 provider key
  const [identities, setIdentities] = useState([]); // [{ provider, external_id, linked_at, ... }]
  const [publicConfig, setPublicConfig] = useState(null); // { oauth_provider_metadata: [...], server_address }

  const load = useCallback(async () => {
    if (!isLoggedIn()) {
      setLoading(false);
      return;
    }
    try {
      const [idsJson, pubRes] = await Promise.all([
        authFetch('/api/user/oauth/identities'),
        fetch('/api/public-config', { credentials: 'same-origin' }),
      ]);
      if (idsJson?.success && Array.isArray(idsJson.identities)) {
        setIdentities(idsJson.identities);
      }
      if (pubRes?.ok) {
        const pub = await pubRes.json();
        if (pub?.success) setPublicConfig(pub);
      }
    } catch {
      // soft fail; below renders fallback message
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { load(); }, [load]);

  // 从 publicConfig.oauth_provider_metadata 找指定 provider 的元数据
  const findProviderMeta = (providerKey) => {
    const list = publicConfig?.oauth_provider_metadata;
    if (!Array.isArray(list)) return null;
    return list.find((m) => m?.key === providerKey) || null;
  };

  const handleLink = async (providerKey) => {
    if (submittingProvider) return;
    const meta = findProviderMeta(providerKey);
    if (!meta) {
      toast.error(t('API.ERR_OAUTH_PROVIDER_UNKNOWN', '未知的第三方登录渠道'));
      return;
    }
    if (!meta.client_id) {
      toast.error(t('API.ERR_OAUTH_PROVIDER_NOT_CONFIGURED', '该第三方登录未配置'));
      return;
    }
    setSubmittingProvider(providerKey);
    try {
      const json = await authFetch(`/api/user/oauth/${encodeURIComponent(providerKey)}/link/prepare`, {
        method: 'POST',
        body: {},
      });
      if (!json?.success) {
        toast.error(json?.message_code ? t('API.' + json.message_code, json.message) : (json?.message || t('API.ERR_OAUTH_INTERNAL', '无法启动绑定')));
        setSubmittingProvider('');
        return;
      }
      const baseAddress = (() => {
        const raw = (publicConfig?.server_address || '').trim().replace(/\/$/, '');
        if (!raw) return window.location.origin;
        try {
          const u = new URL(raw);
          if (u.origin === window.location.origin) return raw;
        } catch {
          // fall through
        }
        return window.location.origin;
      })();
      const url = buildOAuthAuthorizeURL(meta, {
        callbackUri: `${baseAddress}/oauth/${providerKey}`,
        state: json.state,
        codeChallenge: json.code_challenge,
        codeChallengeMethod: json.code_challenge_method || 'S256',
      });
      window.location.assign(url);
    } catch {
      toast.error(t('API.ERR_OAUTH_INTERNAL', '无法启动绑定'));
      setSubmittingProvider('');
    }
  };

  const handleUnlink = async (providerKey, providerLabel) => {
    if (submittingProvider) return;
    const ok = await confirm(t('ACCOUNT.OAUTH_UNLINK_CONFIRM', {
      provider: providerLabel,
      defaultValue: `解绑 ${providerLabel} 后将无法再用该第三方账号登录。确定继续？`,
    }));
    if (!ok) return;
    setSubmittingProvider(providerKey);
    try {
      const json = await authFetch(`/api/user/oauth/${encodeURIComponent(providerKey)}/unlink`, {
        method: 'POST',
        body: {},
      });
      if (json?.success) {
        toast.success(t(`API.${json.message_code || 'SUCCESS_OAUTH_UNLINKED'}`, '已解绑'));
        await load();
      } else {
        toast.error(json?.message_code ? t('API.' + json.message_code, json.message) : (json?.message || t('API.ERR_OAUTH_INTERNAL', '解绑失败')));
      }
    } catch {
      toast.error(t('API.ERR_OAUTH_INTERNAL', '解绑失败'));
    } finally {
      setSubmittingProvider('');
    }
  };

  if (loading) {
    return <div className="text-sm text-on-surface-variant py-4">{t('COMMON.LOADING', '加载中…')}</div>;
  }

  // admin 已配置的 provider 元数据列表（含 label / authorize_endpoint /
  // default_params / icon_key）。Phase H cleanup：删除 oauth_providers ([]string) 兜底，
  // 公测期同仓部署不会有"前端旧 / 后端新"窗口期。
  const configuredMeta = Array.isArray(publicConfig?.oauth_provider_metadata)
    ? publicConfig.oauth_provider_metadata
    : [];
  if (configuredMeta.length === 0) {
    return (
      <section className="rounded-overlay border border-outline-variant bg-surface-container p-6">
        <div className="flex items-start gap-3">
          <Link2 className="text-on-surface-variant shrink-0 mt-0.5" size={20} />
          <div>
            <h3 className="text-base font-semibold text-on-surface">
              {t('ACCOUNT.OAUTH_LINKED_TITLE', '第三方账号')}
            </h3>
            <p className="text-sm text-on-surface-variant mt-1">
              {t('ACCOUNT.OAUTH_NO_PROVIDERS', '管理员尚未配置任何第三方登录渠道。')}
            </p>
          </div>
        </div>
      </section>
    );
  }

  // identities 用 (provider → row) map 便于查找
  const linkedMap = new Map();
  identities.forEach((row) => {
    if (row && row.provider) linkedMap.set(String(row.provider).toLowerCase(), row);
  });

  return (
    <section className="rounded-overlay border border-outline-variant bg-surface-container p-6 space-y-4">
      <header className="flex items-center gap-3">
        <div className="w-9 h-9 rounded-control bg-primary/15 text-primary flex items-center justify-center">
          <Link2 size={18} />
        </div>
        <div>
          <h3 className="text-base font-semibold text-on-surface">
            {t('ACCOUNT.OAUTH_LINKED_TITLE', '第三方账号')}
          </h3>
          <p className="text-xs text-on-surface-variant mt-0.5">
            {t('ACCOUNT.OAUTH_LINKED_DESC', '绑定 GitHub / Google 等账号后即可用第三方一键登录，并作为账号兜底凭据。')}
          </p>
        </div>
      </header>

      <ul className="space-y-2">
        {configuredMeta.map((meta) => {
          const providerKey = meta.key;
          // i18n key 优先（用户可在 i18n 里覆盖），fallback 用 server label
          const i18nKey = `ACCOUNT.OAUTH_${providerKey.toUpperCase()}`;
          const label = t(i18nKey, meta.label || providerKey);
          const linked = linkedMap.get(providerKey);
          const IconSVG = PROVIDER_ICONS[meta.icon_key] || PROVIDER_ICONS[providerKey] || null;
          const isSubmitting = submittingProvider === providerKey;
          return (
            <li
              key={providerKey}
              className="flex flex-col md:flex-row md:items-center justify-between gap-3 bg-black/20 rounded-control border border-outline-variant px-4 py-3"
            >
              <div className="flex items-center gap-3 min-w-0">
                <span className="w-9 h-9 rounded-control bg-surface-container-high border border-outline-variant flex items-center justify-center shrink-0 text-on-surface">
                  {IconSVG ? <IconSVG /> : <Link2 size={18} />}
                </span>
                <div className="min-w-0">
                  <div className="text-sm font-semibold text-on-surface">{label}</div>
                  <div className="text-[11px] mt-0.5">
                    {linked ? (
                      <span className="inline-flex items-center gap-1 text-success">
                        <ShieldCheck size={12} />
                        {t('ACCOUNT.OAUTH_LINKED_AT', '绑定于')} {formatLinkedAt(linked.linked_at)}
                      </span>
                    ) : (
                      <span className="text-on-surface-variant">{t('ACCOUNT.OAUTH_LINK_BTN', '立即绑定')}</span>
                    )}
                  </div>
                </div>
              </div>

              <div className="flex items-center gap-2 shrink-0">
                {linked ? (
                  <button
                    type="button"
                    onClick={() => handleUnlink(providerKey, label)}
                    disabled={isSubmitting}
                    className="h-9 px-3 bg-surface-container-high border border-error/40 hover:border-error text-error text-xs rounded-control inline-flex items-center gap-1.5 disabled:opacity-50"
                  >
                    <UnlinkIcon size={14} />
                    {isSubmitting
                      ? t('ACCOUNT.OAUTH_UNLINKING', '解绑中...')
                      : t('ACCOUNT.OAUTH_UNLINK_BTN', '解绑')}
                  </button>
                ) : (
                  <button
                    type="button"
                    onClick={() => handleLink(providerKey)}
                    disabled={isSubmitting}
                    className="h-9 px-3 bg-primary hover:opacity-90 disabled:opacity-50 text-on-primary text-xs rounded-control inline-flex items-center gap-1.5 font-medium"
                  >
                    <Link2 size={14} />
                    {isSubmitting
                      ? t('ACCOUNT.OAUTH_LINKING', '正在跳转...')
                      : t('ACCOUNT.OAUTH_LINK_BTN', '立即绑定')}
                  </button>
                )}
              </div>
            </li>
          );
        })}
      </ul>
    </section>
  );
};

export default UserLinkedAccounts;
