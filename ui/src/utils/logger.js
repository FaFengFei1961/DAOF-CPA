// 统一日志包装：dev 模式打印到 console，prod 模式静默。
//
// 动机：
//   - 散落的 console.log/warn/error 在生产环境会泄露内部组件名 / 端点路径给打开 DevTools 的访客
//   - 但开发期间又确实需要看到详细错误，否则调试痛苦
//
// 用法：
//   import { logger } from '../utils/logger';
//   logger.warn('[Tickets] loadList', e);
//   logger.error('[Topup] gateway', e);
//
// 扩展点：未来可以接入 Sentry / DataDog 等，只要在这里加个 prod 分支转发即可。
//
// 判定 prod 的方式：Vite 构建时 import.meta.env.PROD === true。
// dev / preview 模式 PROD === false。

const isProd = typeof import.meta !== 'undefined' && import.meta.env && import.meta.env.PROD === true;

const noop = () => {};

export const logger = isProd
  ? { log: noop, info: noop, warn: noop, error: noop, debug: noop }
  : {
      log: (...args) => console.log(...args), // eslint-disable-line no-console
      info: (...args) => console.info(...args), // eslint-disable-line no-console
      warn: (...args) => console.warn(...args), // eslint-disable-line no-console
      error: (...args) => console.error(...args), // eslint-disable-line no-console
      debug: (...args) => console.debug(...args), // eslint-disable-line no-console
    };

export default logger;
