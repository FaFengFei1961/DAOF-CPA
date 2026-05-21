import { useEffect, useMemo, useState } from 'react';

const CACHE_KEY = 'daof_public_pricing_v3';
// Cache TTL（用户反馈"admin 关掉模型 /pricing 还显示"）：
//   - 之前 5 分钟 TTL 是公开页面减压用的，但 admin 改完模型/渠道 status 后，
//     用户 /pricing 仍会展示旧模型最长 5 分钟，体感"不同步"。
//   - 改成 60s：公开 pricing 数据变化不频繁，60s 的 lag 用户能接受；admin 操作
//     会通过 invalidatePublicPricing() 立刻打穿 cache，无需等 TTL。
const CACHE_TTL_MS = 60 * 1000;
const CACHE_EVENT = 'daof-public-pricing-updated';
// 跨 tab + 同 tab 失效信号：admin 在 /admin/channels 改 channel_model.status 后
// 触发，所有挂着 usePublicPricing 的页面（公开 /pricing、sidebar 模型计数 chip 等）
// 立即重新拉取，不再等 60s TTL。
const INVALIDATE_EVENT = 'daof-public-pricing-invalidate';
// localStorage 用作跨 tab 信号载体（sessionStorage 不跨 tab 触发 storage 事件）。
// 写一个时间戳进 key，其它 tab 的 storage listener 立即触发刷新。
const INVALIDATE_STORAGE_KEY = 'daof_public_pricing_invalidate_at';

let memoryCache = null;
let inflight = null;

const isBrowser = () => typeof window !== 'undefined' && typeof window.sessionStorage !== 'undefined';

const isUsableCache = (value) => {
  return value && Array.isArray(value.data) && Number.isFinite(value.fetchedAt);
};

const readSessionCache = () => {
  if (!isBrowser()) return null;
  try {
    const raw = window.sessionStorage.getItem(CACHE_KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw);
    return isUsableCache(parsed) ? parsed : null;
  } catch {
    return null;
  }
};

const writeSessionCache = (cache) => {
  if (!isBrowser()) return;
  try {
    window.sessionStorage.setItem(CACHE_KEY, JSON.stringify(cache));
  } catch {
    // Storage can be unavailable in private mode; memory cache still works.
  }
};

const readCache = () => {
  if (isUsableCache(memoryCache)) return memoryCache;
  const stored = readSessionCache();
  if (stored) memoryCache = stored;
  return memoryCache;
};

const isFresh = (cache) => {
  return isUsableCache(cache) && Date.now() - cache.fetchedAt < CACHE_TTL_MS;
};

const publishCache = (cache) => {
  memoryCache = cache;
  writeSessionCache(cache);
  if (isBrowser()) {
    window.dispatchEvent(new CustomEvent(CACHE_EVENT, { detail: cache }));
  }
};

const fetchPricing = () => {
  if (!inflight) {
    inflight = fetch('/api/pricing')
      .then(async (res) => {
        if (!res.ok) throw new Error(`pricing http ${res.status}`);
        return res.json();
      })
      .then((json) => {
        if (!json?.success) throw new Error(json?.message || 'pricing request failed');
        const cache = { data: json.data || [], fetchedAt: Date.now() };
        publishCache(cache);
        return cache;
      })
      .finally(() => {
        inflight = null;
      });
  }
  return inflight;
};

/**
 * 显式作废 public pricing 缓存 + 立即重新拉取。
 *
 * 使用场景：admin 在 /admin/channels 改了 channel.status / channel_model.status
 * 等会影响 /api/pricing 返回结果的字段，需要让所有挂着 usePublicPricing 的页面
 * 立刻同步，不要让 TTL 滞后。
 *
 * 跨 tab 同步走 localStorage storage 事件 —— sessionStorage 不跨 tab，
 * 用一个时间戳 key 做单向广播，其它 tab 的 storage listener 立刻 refetch。
 */
export const invalidatePublicPricing = () => {
  memoryCache = null;
  if (!isBrowser()) return;
  try {
    window.sessionStorage.removeItem(CACHE_KEY);
  } catch {
    // 某些隐身模式禁用 storage，忽略；memory cache 已清，本 tab 仍能刷新。
  }
  try {
    window.localStorage.setItem(INVALIDATE_STORAGE_KEY, String(Date.now()));
  } catch {
    // 同上：写不进 localStorage 没关系，本 tab 的 dispatchEvent 还会触发自家 hook。
  }
  window.dispatchEvent(new CustomEvent(INVALIDATE_EVENT));
};

export function usePublicPricing() {
  const initial = useMemo(() => readCache(), []);
  const [cache, setCache] = useState(initial);
  const [loading, setLoading] = useState(!initial);
  const [error, setError] = useState(null);

  useEffect(() => {
    let alive = true;

    const onCache = (event) => {
      if (!alive || !isUsableCache(event.detail)) return;
      setCache(event.detail);
      setLoading(false);
      setError(null);
    };

    // 本 tab 的 invalidate 事件 + 跨 tab 的 storage 事件，都需要触发同一个 refetch
    // 路径：清旧 state → setLoading(true) 让用户看到刷新中 → fetch → publish。
    const refetchAfterInvalidate = () => {
      if (!alive) return;
      memoryCache = null;
      setLoading(true);
      fetchPricing()
        .then((next) => {
          if (!alive) return;
          setCache(next);
          setError(null);
        })
        .catch((err) => {
          if (!alive) return;
          setError(err);
        })
        .finally(() => {
          if (alive) setLoading(false);
        });
    };

    const onStorage = (event) => {
      if (event.key === INVALIDATE_STORAGE_KEY) {
        refetchAfterInvalidate();
      }
    };

    window.addEventListener(CACHE_EVENT, onCache);
    window.addEventListener(INVALIDATE_EVENT, refetchAfterInvalidate);
    window.addEventListener('storage', onStorage);

    const current = readCache();
    if (!isFresh(current)) {
      fetchPricing()
        .then((next) => {
          if (!alive) return;
          setCache(next);
          setError(null);
        })
        .catch((err) => {
          if (!alive) return;
          setError(err);
        })
        .finally(() => {
          if (alive) setLoading(false);
        });
    }

    return () => {
      alive = false;
      window.removeEventListener(CACHE_EVENT, onCache);
      window.removeEventListener(INVALIDATE_EVENT, refetchAfterInvalidate);
      window.removeEventListener('storage', onStorage);
    };
  }, []);

  return {
    models: cache?.data || [],
    loading,
    error,
    fetchedAt: cache?.fetchedAt || 0,
  };
}
