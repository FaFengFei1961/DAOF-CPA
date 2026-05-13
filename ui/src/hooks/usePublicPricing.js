import { useEffect, useMemo, useState } from 'react';

const CACHE_KEY = 'daof_public_pricing_v2';
const CACHE_TTL_MS = 5 * 60 * 1000;
const CACHE_EVENT = 'daof-public-pricing-updated';

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

    window.addEventListener(CACHE_EVENT, onCache);

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
    };
  }, []);

  return {
    models: cache?.data || [],
    loading,
    error,
    fetchedAt: cache?.fetchedAt || 0,
  };
}
