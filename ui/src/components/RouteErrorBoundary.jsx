import React from 'react';
import { useRouteError, isRouteErrorResponse, useNavigate, Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { AlertTriangle, RefreshCw, Home } from 'lucide-react';
import NotFound from './NotFound';

/**
 * RouteErrorBoundary: catch-all for router-level errors.
 *
 * IA audit C2 fix — wired as `errorElement` on the root route so any:
 *   - lazy-chunk network failure
 *   - thrown loader / action error
 *   - uncaught render error inside a route
 * shows a recoverable error UI instead of a blank screen.
 *
 * 404 responses route through <NotFound /> so the user gets the same
 * "page does not exist" affordance whether they typed a bad URL or
 * the route loader returned a 404.
 */
const RouteErrorBoundary = () => {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const error = useRouteError();

  // Router-thrown 404 → render the dedicated NotFound page.
  if (isRouteErrorResponse(error) && error.status === 404) {
    return <NotFound />;
  }

  const status = isRouteErrorResponse(error) ? error.status : null;
  const detail =
    (error && typeof error === 'object' && 'message' in error && error.message) ||
    (isRouteErrorResponse(error) ? error.statusText : '') ||
    '';

  // Defensive log so the operator can see what blew up in DevTools / Sentry.
  // (Plain console.error is acceptable for boundary diagnostics; production
  //  log shipping is handled by a top-level window.onerror hook elsewhere.)
  // eslint-disable-next-line no-console
  console.error('[RouteErrorBoundary]', error);

  return (
    <div
      role="alert"
      aria-labelledby="route-error-title"
      className="min-h-[60vh] w-full flex flex-col items-center justify-center px-6 py-16 text-center"
    >
      <div className="w-16 h-16 rounded-overlay bg-error/10 flex items-center justify-center mb-6">
        <AlertTriangle className="w-8 h-8 text-error" aria-hidden="true" />
      </div>

      <h1 id="route-error-title" className="text-xl md:text-2xl font-semibold text-on-surface mb-3">
        {t('COMMON.RUNTIME_ERROR_TITLE', '页面发生异常')}
      </h1>

      <p className="text-sm text-on-surface-variant max-w-md mb-2">
        {t(
          'COMMON.RUNTIME_ERROR_DESC',
          '页面加载或运行时出错。可以尝试刷新当前页，或返回首页继续。',
        )}
      </p>

      {(status || detail) && (
        <p className="text-xs text-on-surface-variant/60 max-w-md mb-8 font-mono break-all">
          {status ? `${status} · ` : ''}
          {String(detail).slice(0, 240)}
        </p>
      )}
      {!status && !detail && <div className="mb-8" />}

      <div className="flex flex-wrap items-center justify-center gap-3">
        <button
          type="button"
          onClick={() => navigate(0)}
          className="inline-flex items-center gap-1.5 px-4 py-2 rounded-control bg-primary text-on-primary text-sm hover:opacity-90"
        >
          <RefreshCw className="w-4 h-4" aria-hidden="true" />
          {t('COMMON.RELOAD', '重新加载')}
        </button>
        <Link
          to="/"
          className="inline-flex items-center gap-1.5 px-4 py-2 rounded-control border border-outline-variant text-sm hover:bg-on-surface/[0.04]"
        >
          <Home className="w-4 h-4" aria-hidden="true" />
          {t('COMMON.GO_HOME', '返回首页')}
        </Link>
      </div>
    </div>
  );
};

export default RouteErrorBoundary;
