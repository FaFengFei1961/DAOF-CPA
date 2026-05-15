import React from 'react';
import { useTranslation } from 'react-i18next';
import EmptyState from './EmptyState';
import Skeleton from './Skeleton';

/**
 * DataTable: standard data-table shell.
 *
 * Goals:
 *  - sticky header for long tables
 *  - edge gradient that hints at horizontal overflow
 *  - row click support for drill-down surfaces
 *  - unified column API: { key, header, render, align, width, truncate }
 *  - complete loading, empty, and data states
 *  - optional pagination
 *
 * Usage:
 *   <DataTable
 *     columns={columns}
 *     rows={rows}
 *     rowKey={r => r.id}
 *     loading={loading}
 *     emptyTitle="No request events"
 *     onRowClick={r => setDrawerRow(r)}
 *     pagination={{ page, pageSize, total, onPageChange }}
 *   />
 *
 * Column examples:
 *   { key: 'time', header: 'Time', width: 140, render: r => fmt(r.created_at) },
 *   { key: 'amount', header: 'Amount', align: 'right', render: r => `$${r.cost}` },
 *   { key: 'model', header: 'Model', truncate: 200 },
 */
const ALIGN_CLS = { left: 'text-left', right: 'text-right', center: 'text-center' };

const DataTable = ({
  columns,
  rows,
  rowKey,
  loading = false,
  loadingRows = 5,
  emptyTitle,
  emptySub,
  emptyIcon,
  onRowClick,
  pagination,
  // Table behavior flags.
  stickyHeader = true,
  edgeGradient = true,
  className = '',
}) => {
  const { t } = useTranslation();
  const headerCls = stickyHeader ? '' : 'fl-table-no-sticky';

  const colCount = columns.length;
  const renderCell = (col, row) => {
    const value = col.render ? col.render(row) : row[col.key];
    return value;
  };

  return (
    <div className={`fl-table-shell ${className}`}>
      <div className={`fl-table-scroll ${edgeGradient ? '' : 'no-edge-gradient'}`}>
        <table className="w-full text-left text-sm">
          <thead className={headerCls}>
            <tr className="border-b border-outline-variant">
              {columns.map(col => (
                <th
                  key={col.key}
                  scope="col"
                  style={col.width ? { width: typeof col.width === 'number' ? `${col.width}px` : col.width } : undefined}
                  className={`px-3 py-2.5 font-semibold text-[11px] uppercase tracking-wider text-on-surface-variant whitespace-nowrap ${ALIGN_CLS[col.align] || 'text-left'}`}
                >
                  {col.header}
                </th>
              ))}
            </tr>
          </thead>
          <tbody className="divide-y divide-outline-variant/30">
            {loading && rows.length === 0 ? (
              Array.from({ length: loadingRows }).map((_, i) => (
                <Skeleton.Row key={`skeleton-${i}`} cols={colCount} />
              ))
            ) : rows.length === 0 ? (
              <tr>
                <td colSpan={colCount} className="p-0">
                  <EmptyState
                    icon={emptyIcon}
                    title={emptyTitle || t('COMMON.EMPTY', '暂无数据')}
                    sub={emptySub}
                  />
                </td>
              </tr>
            ) : (
              rows.map((row) => {
                const key = rowKey ? rowKey(row) : (row.id ?? Math.random());
                const clickable = !!onRowClick;
                return (
                  <tr
                    key={key}
                    onClick={clickable ? () => onRowClick(row) : undefined}
                    className={`${clickable ? 'cursor-pointer hover:bg-on-surface/[0.04]' : 'hover:bg-surface-container/40'} transition`}
                  >
                    {columns.map(col => (
                      <td
                        key={col.key}
                        className={`px-3 py-2.5 text-on-surface ${ALIGN_CLS[col.align] || 'text-left'} ${col.truncate ? 'truncate' : ''} ${col.mono ? 'font-mono text-xs' : ''}`}
                        style={col.truncate ? { maxWidth: typeof col.truncate === 'number' ? `${col.truncate}px` : col.truncate } : undefined}
                        title={col.truncate && typeof renderCell(col, row) === 'string' ? renderCell(col, row) : undefined}
                      >
                        {renderCell(col, row)}
                      </td>
                    ))}
                  </tr>
                );
              })
            )}
          </tbody>
        </table>
      </div>
      {pagination && (
        <DataTablePagination {...pagination} />
      )}
    </div>
  );
};

const DataTablePagination = ({ page, pageSize, total, onPageChange }) => {
  const { t } = useTranslation();
  const pageCount = Math.max(1, Math.ceil(total / pageSize));
  const from = total === 0 ? 0 : (page - 1) * pageSize + 1;
  const to = Math.min(page * pageSize, total);
  return (
    <div className="flex items-center justify-between gap-3 px-4 py-2.5 border-t border-outline-variant/40 bg-surface-container/40 text-xs text-on-surface-variant">
      <div className="tabular-nums">
        {t('COMMON.PAGINATION_RANGE', { from, to, total, defaultValue: '{{from}}-{{to}} / 共 {{total}} 条' })}
      </div>
      <div className="flex items-center gap-1">
        <button
          type="button"
          disabled={page <= 1}
          onClick={() => onPageChange(page - 1)}
          className="px-2 py-1 rounded text-xs border border-outline-variant text-on-surface-variant hover:text-on-surface hover:bg-on-surface/[0.04] disabled:opacity-40 disabled:cursor-not-allowed"
        >
          {t('COMMON.PREV', '上一页')}
        </button>
        <span className="px-2 tabular-nums">{page} / {pageCount}</span>
        <button
          type="button"
          disabled={page >= pageCount}
          onClick={() => onPageChange(page + 1)}
          className="px-2 py-1 rounded text-xs border border-outline-variant text-on-surface-variant hover:text-on-surface hover:bg-on-surface/[0.04] disabled:opacity-40 disabled:cursor-not-allowed"
        >
          {t('COMMON.NEXT', '下一页')}
        </button>
      </div>
    </div>
  );
};

export default DataTable;
