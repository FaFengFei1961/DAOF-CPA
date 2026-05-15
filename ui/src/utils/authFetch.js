// 统一鉴权 fetch 封装。同时支持 admin (cookie) 和普通用户 (Bearer token) 两种鉴权方式。
//
// 使用方式：
//   const json = await authFetch('/api/subscriptions/mine');
//   const json = await authFetch('/api/subscriptions/purchase', { method: 'POST', body: { package_id: 1 } });
//
// 调用方拿到的是已经 .json() 后的对象，失败时抛出 Error；
// 401/403 也作为正常 json 返回让调用方决策（避免破坏现有 success/false 响应链路）。

import toast from 'react-hot-toast';

const ADMIN_FLAG_KEY = 'daof_admin_unlocked';
const USER_TOKEN_KEY = 'daof_token';

/**
 * @returns {{ isAdmin: boolean, userToken: string | null }}
 */
export const readAuthState = () => {
  let isAdmin = false;
  let userToken = null;
  try {
    isAdmin = localStorage.getItem(ADMIN_FLAG_KEY) === '1';
    userToken = localStorage.getItem(USER_TOKEN_KEY);
  } catch {
    // localStorage 不可用（隐私模式）静默
  }
  return { isAdmin, userToken };
};

/**
 * 构造已带鉴权信息的 fetch 选项。
 * @param {RequestInit & { body?: any }} init
 * @returns {RequestInit}
 */
export const buildAuthOptions = (init = {}) => {
  const { isAdmin, userToken } = readAuthState();
  const headers = { ...(init.headers || {}) };
  let body = init.body;

  if (body && typeof body === 'object' && !(body instanceof FormData) && !(body instanceof Blob)) {
    headers['Content-Type'] = headers['Content-Type'] || 'application/json';
    body = JSON.stringify(body);
  }

  const opts = { ...init, headers, body };

  if (isAdmin) {
    opts.credentials = 'include';
  } else if (userToken) {
    headers['Authorization'] = `Bearer ${userToken}`;
  }
  return opts;
};

/**
 * 统一 fetch 封装：自动注入鉴权 + 自动解析 JSON。
 * @param {string} url
 * @param {RequestInit & { body?: any }} init
 * @returns {Promise<any>}
 */
export const authFetch = async (url, init = {}) => {
  const opts = buildAuthOptions(init);
  // fix MAJOR F3（gemini 第二十一轮）：fetch 在网络异常 / DNS 解析失败 / CORS 拦截 / offline 时
  // **直接抛 TypeError**，不会变成 HTTP 4xx/5xx。原代码没有外层 try-catch，导致任何调用方
  // 如果没自己包 try-catch 就会触发 React 未捕获 promise rejection，整棵组件树崩溃。
  // 统一拦截：归一化为 { success: false, status: 0, message: '网络请求失败' } 的契约对象。
  let res;
  try {
    res = await fetch(url, opts);
  } catch (err) {
    return {
      success: false,
      status: 0,
      message: (err && err.message) ? `网络请求失败：${err.message}` : '网络请求失败，请检查网络连接',
    };
  }

  // 始终把 HTTP status 透传给调用方，便于区分 401/403/4xx/5xx
  const ct = res.headers.get('Content-Type') || '';
  
  if (res.status === 402) {
    toast('余额不足，请前往充值页面补充余额或购买套餐。', { duration: 5000 });
  }

  if (!ct.includes('application/json')) {
    return {
      success: false,
      status: res.status,
      message: res.status >= 500
        ? `服务端异常 (HTTP ${res.status})`
        : res.status === 401 || res.status === 403
          ? '未授权或权限不足'
          : `响应非 JSON (HTTP ${res.status})`,
    };
  }
  let json;
  try {
    json = await res.json();
  } catch {
    return { success: false, status: res.status, message: '响应解析失败' };
  }
  // 把 status 注入返回对象（不覆盖已有字段），调用方按需读
  if (json && typeof json === 'object' && json.status === undefined) {
    json.status = res.status;
  }
  // 后端有时 4xx 但 success 缺省 → 显式纠正
  if (json && typeof json === 'object' && !res.ok && json.success === undefined) {
    json.success = false;
  }
  return json;
};

/**
 * 检查用户是否已登录（admin 或普通用户）。
 * @returns {boolean}
 */
export const isLoggedIn = () => {
  const { isAdmin, userToken } = readAuthState();
  return isAdmin || !!userToken;
};
