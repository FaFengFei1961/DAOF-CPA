const cacheStore = new Map();

export const readPageCache = (key) => cacheStore.get(key)?.data || null;

export const writePageCache = (key, data) => {
  cacheStore.set(key, { data, updatedAt: Date.now() });
};

export const isPageCacheFresh = (key, ttlMs) => {
  const entry = cacheStore.get(key);
  return !!entry && Date.now() - entry.updatedAt < ttlMs;
};

export const clearPageCache = (keyPrefix) => {
  for (const key of cacheStore.keys()) {
    if (key.startsWith(keyPrefix)) cacheStore.delete(key);
  }
};
