// Package: ui/src/hooks
//
// fix Major Codex UX 审查（第二十五轮）：Dashboard 和 CreditsPoolCard 之前各自 setInterval 60s
// 独立轮询 /api/credits-pool/summary，后端 6/min IP 限流可能被两端各占一半→ Dashboard 数据漂移、
// 节奏不一致。抽成共享 hook，每个挂载实例都看到同一份数据 + 同一个定时器（基于全局 cache）。

import { useEffect, useState, useRef } from 'react';

const POLL_INTERVAL_MS = 60_000;

// 模块级单例：所有 hook 实例共享同一份数据 + 同一个定时器。
// 避免页面同时挂载多个消费者（Dashboard + CreditsPoolCard）时双倍消耗后端限流配额。
let _cache = { models: [], at: 0 };
const _listeners = new Set();
let _timer = null;
let _inflight = false;

async function fetchOnce() {
  if (_inflight) return;
  _inflight = true;
  try {
    if (document.hidden) return; // 后台 tab 不浪费配额
    const res = await fetch('/api/credits-pool/summary', { cache: 'no-store' });
    if (!res.ok) return;
    const json = await res.json();
    if (json?.success) {
      _cache = { models: json.data?.models || [], at: Date.now() };
      _listeners.forEach((cb) => cb(_cache));
    }
  } catch {
    /* 静默：下一轮再试 */
  } finally {
    _inflight = false;
  }
}

function startTimer() {
  if (_timer) return;
  _timer = setInterval(fetchOnce, POLL_INTERVAL_MS);
}

function stopTimer() {
  if (_timer && _listeners.size === 0) {
    clearInterval(_timer);
    _timer = null;
  }
}

/**
 * 订阅号池 summary。返回 { models, loadedAt }。
 * 第一次挂载时立即 fetch；之后定时刷新；同时段多个组件挂载共享同一份数据。
 */
export function useCreditsPoolSummary() {
  const [state, setState] = useState(_cache);
  const subRef = useRef(null);

  useEffect(() => {
    const cb = (next) => setState(next);
    subRef.current = cb;
    _listeners.add(cb);

    // 首次挂载：若 cache 是空的或超过 POLL_INTERVAL 没刷新，立即拉一次
    if (!_cache.at || Date.now() - _cache.at > POLL_INTERVAL_MS) {
      fetchOnce();
    }

    startTimer();
    return () => {
      _listeners.delete(cb);
      stopTimer();
    };
  }, []);

  return {
    models: state.models,
    loadedAt: state.at,
  };
}
