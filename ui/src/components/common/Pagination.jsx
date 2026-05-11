// Pagination - 通用分页控件
//
// fix MAJOR（gemini 第十六轮）：原 4 个组件粘贴了相似但不一致的分页 UI，
// 项目无历史包袱 → 抽公共组件统一交互、a11y、文案。
//
// Props:
//   page          - 当前页（1-based）
//   pageSize      - 每页条数（仅用于显示范围）
//   total         - 总条数（来自 meta.total）
//   loading       - 是否加载中（按钮 disabled 防双击）
//   onPageChange  - (newPage) => void
//   className     - 可选额外样式
//   showRange     - 是否显示 "第 X-Y / 共 Z 条"（默认 true）
//
// 行为约定：
//   - total <= pageSize 时不渲染（让调用方无需自己写条件渲染）
//   - 上一页/下一页边界时禁用
//   - aria-disabled 与原生 disabled 同步（不冗余设置 aria-disabled，遵循 a11y 规范）
//   - 通过 t('COMMON.PAGINATION_RANGE'/'COMMON.PREV'/'COMMON.NEXT') i18n
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
                    {t('COMMON.PAGINATION_RANGE',
                        `第 ${from}-${to} / 共 ${total} 条`,
                        { from, to, total })}
                </span>
            )}
            <div className="flex items-center gap-2">
                <button
                    type="button"
                    onClick={handlePrev}
                    disabled={page <= 1 || loading}
                    className="px-3 py-1 rounded border border-outline-variant/40 hover:bg-on-surface/[0.04] disabled:opacity-40 disabled:cursor-not-allowed"
                >
                    {t('COMMON.PREV', '上一页')}
                </button>
                <span aria-current="page">{page} / {totalPages}</span>
                <button
                    type="button"
                    onClick={handleNext}
                    disabled={page >= totalPages || loading}
                    className="px-3 py-1 rounded border border-outline-variant/40 hover:bg-on-surface/[0.04] disabled:opacity-40 disabled:cursor-not-allowed"
                >
                    {t('COMMON.NEXT', '下一页')}
                </button>
            </div>
        </div>
    );
};

export default Pagination;
