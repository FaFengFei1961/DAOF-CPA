// Pagination - shared pagination control.
//
// Centralizes duplicated pagination UI into one consistent, accessible component.
//
// Props:
//   page          - current page, 1-based
//   pageSize      - items per page, used for the visible range
//   total         - total item count
//   loading       - disables controls while data is loading
//   onPageChange  - (newPage) => void
//   className     - optional extra classes
//   showRange     - whether to show the item range
//
// Behavior:
//   - Does not render when total <= pageSize.
//   - Disables previous/next at bounds.
//   - Native disabled is sufficient for a11y; no redundant aria-disabled.
//   - Uses COMMON pagination keys.
import React from 'react';
import { useTranslation } from 'react-i18next';

const Pagination = ({
    page,
    pageSize,
    total,
    loading = false,
    onPageChange,
    className = '',
    showRange = true,
}) => {
    const { t } = useTranslation();
    if (total <= pageSize) return null;
    const totalPages = Math.max(1, Math.ceil(total / pageSize));
    const from = (page - 1) * pageSize + 1;
    const to = Math.min(page * pageSize, total);

    const handlePrev = () => onPageChange(Math.max(1, page - 1));
    const handleNext = () => onPageChange(Math.min(totalPages, page + 1));

    return (
        <div className={`flex items-center justify-between text-xs text-on-surface/70 ${className}`}>
            {showRange && (
                <span>
                    {t('COMMON.PAGINATION_RANGE', {
                        from,
                        to,
                        total,
                        defaultValue: '第 {{from}}-{{to}} / 共 {{total}} 条',
                    })}
                </span>
            )}
            <div className="flex items-center gap-2">
                <button
                    type="button"
                    onClick={handlePrev}
                    disabled={page <= 1 || loading}
                    className="px-3 py-1 rounded-control border border-outline-variant/40 hover:bg-on-surface/[0.04] disabled:opacity-40 disabled:cursor-not-allowed"
                >
                    {t('COMMON.PREV', '上一页')}
                </button>
                <span aria-current="page">{page} / {totalPages}</span>
                <button
                    type="button"
                    onClick={handleNext}
                    disabled={page >= totalPages || loading}
                    className="px-3 py-1 rounded-control border border-outline-variant/40 hover:bg-on-surface/[0.04] disabled:opacity-40 disabled:cursor-not-allowed"
                >
                    {t('COMMON.NEXT', '下一页')}
                </button>
            </div>
        </div>
    );
};

export default Pagination;
