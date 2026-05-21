import React from 'react';
import { Link, useLocation, useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Home, ArrowLeft, AlertCircle } from 'lucide-react';

/**
 * NotFound: 404 page.
 *
 * IA audit C1 fix — replaces the silent `Navigate to="/"` fallback that was
 * silently teleporting mistyped URLs, stale notification links, and broken
 * bookmarks to the home page without any feedback.
 *
 * Shows the path the user tried, plus three escape routes:
 *   - Back (browser history)
 *   - Home (root)
 *   - Pricing (a known-good public page, useful when arriving from
 *     external links / marketing copy that may reference old paths).
 */
const NotFound = () => {
  const { t } = useTranslation();
  const location = useLocation();
  const navigate = useNavigate();

  return (
    <div
      role="alert"
      aria-labelledby="not-found-title"
      className="min-h-[60vh] w-full flex flex-col items-center justify-center px-6 py-16 text-center"
    >
      <div className="w-16 h-16 rounded-overlay bg-warning/10 flex items-center justify-center mb-6">
        <AlertCircle className="w-8 h-8 text-warning" aria-hidden="true" />
      </div>

      <p className="text-6xl font-bold tracking-tight text-on-surface mb-2 num-tabular">404</p>

      <h1 id="not-found-title" className="text-xl md:text-2xl font-semibold text-on-surface mb-3">
        {t('COMMON.NOT_FOUND_TITLE', '页面不存在')}
      </h1>

      <p className="text-sm text-on-surface-variant max-w-md mb-1">
        {t('COMMON.NOT_FOUND_DESC', '我们找不到你想访问的页面。链接可能已失效或被移动。')}
      </p>

      <p className="text-xs text-on-surface-variant/60 max-w-md mb-8 font-mono break-all">
        {location.pathname}
        {location.search}
      </p>

      <div className="flex flex-wrap items-center justify-center gap-3">
        <button
          type="button"
          onClick={() => navigate(-1)}
          className="inline-flex items-center gap-1.5 px-4 py-2 rounded-control border border-outline-variant text-sm hover:bg-on-surface/[0.04]"
        >
          <ArrowLeft className="w-4 h-4" aria-hidden="true" />
          {t('COMMON.GO_BACK', '返回上一页')}
        </button>
        <Link
          to="/"
          className="inline-flex items-center gap-1.5 px-4 py-2 rounded-control bg-primary text-on-primary text-sm hover:opacity-90"
        >
          <Home className="w-4 h-4" aria-hidden="true" />
          {t('COMMON.GO_HOME', '返回首页')}
        </Link>
        <Link
          to="/pricing"
          className="inline-flex items-center gap-1.5 px-4 py-2 rounded-control text-sm text-on-surface-variant hover:text-on-surface hover:bg-on-surface/[0.04]"
        >
          {t('COMMON.GO_PRICING', '查看定价')}
        </Link>
      </div>
    </div>
  );
};

export default NotFound;
