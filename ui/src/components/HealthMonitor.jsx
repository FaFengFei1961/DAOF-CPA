import React, { useMemo, useState, useRef, useEffect, useCallback } from 'react';
import { createPortal } from 'react-dom';
import { useTranslation } from 'react-i18next';

// ─── Grid constants ───────────────────────────────────────────────────────────
// 7 rows × 48 cols = 336 buckets. grid-auto-flow: column so time runs L→R,
// each column fills top-to-bottom. Matches CPA USAGE KEEPER's visual density.
const ROWS = 7;
const COLS = 48;
const TOTAL_BLOCKS = ROWS * COLS; // 336

const BLOCK_PX  = 8;   // px per cell (both sides)
const GAP_PX    = 2;   // gap between cells
const GRID_H    = ROWS * BLOCK_PX + (ROWS - 1) * GAP_PX; // 68px total

const TOOLTIP_SAFE_W  = 220;
const TOOLTIP_SAFE_H  = 110;
const TOOLTIP_OFFSET  = 10;

// ─── Smooth colour scale: red(0%) → yellow(50%) → green(100%) ────────────────
// Matches CPA USAGE KEEPER's rateToColor with three-stop linear RGB blend.
function rateToColor(rate) {
    if (rate < 0) return null; // idle → CSS variable fallback
    const clamp = Math.min(1, Math.max(0, rate));
    if (clamp <= 0.5) {
        const t = clamp / 0.5;
        return `rgb(${lerp(239, 250, t)},${lerp(68, 204, t)},${lerp(68, 21, t)})`;
    }
    const t = (clamp - 0.5) / 0.5;
    return `rgb(${lerp(250, 16, t)},${lerp(204, 185, t)},${lerp(21, 129, t)})`;
}
const lerp = (a, b, t) => Math.round(a + (b - a) * t);

// ─── Build time-bucketed health data from raw log array ──────────────────────
function buildHealthData(logs, summary) {
    const now = Date.now();

    let totalSuccess = 0;
    let totalFailure = 0;
    let minTime = now - 24 * 3_600_000; // default: last 24 h
    const maxTime = now;

    if (logs && logs.length > 0) {
        let earliest = now;
        logs.forEach(log => {
            const t = new Date(log.created_at).getTime();
            if (t < earliest) earliest = t;
            if (log.failed) totalFailure++; else totalSuccess++;
        });
        // Never let minTime exceed maxTime − 1 h so blocks have meaningful width
        minTime = Math.min(earliest, now - 3_600_000);
    } else if (summary) {
        // Derive totals from summary even when detailed logs are unavailable
        totalSuccess = summary.successReqs ?? 0;
        totalFailure = summary.failedReqs ?? 0;
    }

    const blockMs = (maxTime - minTime) / TOTAL_BLOCKS;

    const blocks = Array.from({ length: TOTAL_BLOCKS }, (_, i) => ({
        success: 0,
        failure: 0,
        startTime: minTime + i * blockMs,
        endTime:   minTime + (i + 1) * blockMs,
        rate: -1,
    }));

    if (logs) {
        logs.forEach(log => {
            const t = new Date(log.created_at).getTime();
            let idx = Math.floor((t - minTime) / blockMs);
            if (idx >= TOTAL_BLOCKS) idx = TOTAL_BLOCKS - 1;
            if (idx < 0) idx = 0;
            if (log.failed) blocks[idx].failure++; else blocks[idx].success++;
        });
    }

    blocks.forEach(b => {
        const total = b.success + b.failure;
        if (total > 0) b.rate = b.success / total;
    });

    const hasData = totalSuccess + totalFailure > 0;
    const successRate = hasData ? (totalSuccess / (totalSuccess + totalFailure)) * 100 : null;

    return { blocks, totalSuccess, totalFailure, successRate, minTime, maxTime, hasData };
}

// ─── Formatting helpers ───────────────────────────────────────────────────────
const fmtDt = (ts) => {
    const d = new Date(ts);
    const mm  = String(d.getMonth() + 1).padStart(2, '0');
    const dd  = String(d.getDate()).padStart(2, '0');
    const hh  = String(d.getHours()).padStart(2, '0');
    const min = String(d.getMinutes()).padStart(2, '0');
    return `${mm}/${dd} ${hh}:${min}`;
};

// ─── HealthMonitor component ──────────────────────────────────────────────────
export function HealthMonitor({ logs, summary }) {
    const { t } = useTranslation();
    const [activeTooltip, setActiveTooltip] = useState(null);
    const gridRef = useRef(null);

    const { blocks, totalSuccess, totalFailure, successRate, minTime, maxTime, hasData } =
        useMemo(() => buildHealthData(logs, summary), [logs, summary]);

    // Use summary successRate as fallback when no detailed logs are available
    const displayRate = successRate ??
        (summary?.totalReqs > 0 ? (summary.successReqs / summary.totalReqs) * 100 : null);

    const rateColour = displayRate == null ? 'text-on-surface-variant'
        : displayRate >= 95 ? 'text-success'
        : displayRate >= 80 ? 'text-warning'
        : 'text-error';

    // ── Tooltip positioning ──────────────────────────────────────────────────
    const buildTooltipState = useCallback((idx, anchorEl) => {
        if (!anchorEl?.isConnected) return null;
        const rect = anchorEl.getBoundingClientRect();
        const cx = rect.left + rect.width / 2;
        const horizontal = cx <= TOOLTIP_SAFE_W / 2 ? 'left'
            : cx >= window.innerWidth - TOOLTIP_SAFE_W / 2 ? 'right'
            : 'center';
        const left = horizontal === 'center' ? cx
            : horizontal === 'right' ? rect.right : rect.left;
        const vertical  = rect.top <= TOOLTIP_SAFE_H ? 'below' : 'above';
        const top       = vertical === 'below' ? rect.bottom + TOOLTIP_OFFSET : rect.top - TOOLTIP_OFFSET;
        const translateX = horizontal === 'center' ? '-50%' : horizontal === 'right' ? '-100%' : '0';
        const translateY = vertical === 'below' ? '0' : '-100%';
        return { idx, anchorEl, left: Math.round(left), top: Math.round(top),
                 transform: `translate(${translateX}, ${translateY})` };
    }, []);

    const openTooltip = useCallback((idx, el) =>
        setActiveTooltip(buildTooltipState(idx, el)), [buildTooltipState]);

    // Close on outside click
    useEffect(() => {
        if (!activeTooltip) return;
        const handler = (e) => {
            if (gridRef.current && !gridRef.current.contains(e.target)) setActiveTooltip(null);
        };
        document.addEventListener('pointerdown', handler);
        return () => document.removeEventListener('pointerdown', handler);
    }, [activeTooltip]);

    // Reposition on scroll/resize
    useEffect(() => {
        if (!activeTooltip) return;
        const update = () => {
            if (!document.body.contains(activeTooltip.anchorEl)) { setActiveTooltip(null); return; }
            setActiveTooltip(buildTooltipState(activeTooltip.idx, activeTooltip.anchorEl));
        };
        window.addEventListener('resize', update);
        window.addEventListener('scroll', update, true);
        return () => {
            window.removeEventListener('resize', update);
            window.removeEventListener('scroll', update, true);
        };
    }, [activeTooltip, buildTooltipState]);

    // ── Tooltip renderer (portal so it escapes overflow:hidden parents) ───────
    const renderTooltip = (block, state) => {
        const total = block.success + block.failure;
        const node = (
            <div
                role="tooltip"
                className="fixed z-50 pointer-events-none min-w-[180px]
                           bg-surface-container-high border border-outline-variant
                           rounded-control p-3 shadow-lg shadow-black/50"
                style={{ left: `${state.left}px`, top: `${state.top}px`, transform: state.transform }}
            >
                <div className="text-[10px] text-on-surface-variant mb-2 font-mono">
                    {fmtDt(block.startTime)} – {fmtDt(block.endTime)}
                </div>
                {total > 0 ? (
                    <div className="space-y-1.5 text-xs">
                        <div className="flex items-center justify-between gap-6">
                            <span className="text-on-surface-variant">{t('HEALTH.SUCCESS', '成功')}</span>
                            <span className="font-mono font-semibold text-success">{block.success}</span>
                        </div>
                        <div className="flex items-center justify-between gap-6">
                            <span className="text-on-surface-variant">{t('HEALTH.FAILURE', '失败')}</span>
                            <span className="font-mono font-semibold text-error">{block.failure}</span>
                        </div>
                        <div className="flex items-center justify-between gap-6 border-t border-outline-variant/60 pt-1.5">
                            <span className="text-on-surface-variant">{t('HEALTH.RATE', '成功率')}</span>
                            <span className={`font-mono font-bold text-sm
                                ${block.rate >= 0.95 ? 'text-success'
                                  : block.rate >= 0.80 ? 'text-warning'
                                  : 'text-error'}`}>
                                {(block.rate * 100).toFixed(1)}%
                            </span>
                        </div>
                    </div>
                ) : (
                    <div className="text-xs text-on-surface-variant">{t('HEALTH.NO_REQUESTS', '无请求')}</div>
                )}
            </div>
        );
        return typeof document === 'undefined' ? node : createPortal(node, document.body);
    };

    // ── Render ────────────────────────────────────────────────────────────────
    return (
        <div className="bg-surface border border-outline-variant rounded-overlay p-5 w-full mb-6">

            {/* ─ Header ─ */}
            <div className="flex items-start justify-between mb-4">
                <div className="min-w-0">
                    <div className="flex items-center gap-2 mb-1">
                        <span className="inline-flex items-center text-[10px] font-mono font-bold tracking-widest uppercase
                                         text-on-surface-variant border border-outline-variant
                                         px-1.5 py-0.5 rounded-control select-none">
                            {t('HEALTH.RELIABILITY_LABEL', 'RELIABILITY')}
                        </span>
                    </div>
                    <h3 className="text-sm font-semibold text-on-surface leading-tight">
                        {t('HEALTH.TIMELINE_TITLE', 'Request Health Timeline')}
                    </h3>
                    <p className="text-[11px] text-on-surface-variant font-mono mt-0.5">
                        {hasData
                            ? `${fmtDt(minTime)} – ${fmtDt(maxTime)}`
                            : t('HEALTH.NO_DATA', '暂无请求数据')}
                    </p>
                </div>

                {/* Right: big rate + success/failure counts */}
                <div className="text-right shrink-0 ml-4">
                    <div className={`text-2xl font-bold font-mono leading-none ${rateColour}`}>
                        {displayRate != null ? `${displayRate.toFixed(1)}%` : '--'}
                    </div>
                    {hasData && (
                        <div className="flex items-center justify-end gap-3 mt-1.5 text-[11px] font-mono">
                            <span className="flex items-center gap-1 text-success">
                                <span className="w-1.5 h-1.5 rounded-full bg-success" />
                                {totalSuccess.toLocaleString()}
                            </span>
                            <span className="flex items-center gap-1 text-error">
                                <span className="w-1.5 h-1.5 rounded-full bg-error" />
                                {totalFailure.toLocaleString()}
                            </span>
                        </div>
                    )}
                </div>
            </div>

            {/* ─ Block matrix (7 rows × 48 cols, time flows left → right) ─ */}
            <div
                ref={gridRef}
                style={{
                    display: 'grid',
                    gridTemplateRows: `repeat(${ROWS}, ${BLOCK_PX}px)`,
                    gridAutoFlow: 'column',
                    gridAutoColumns: '1fr',
                    gap: `${GAP_PX}px`,
                    height: `${GRID_H}px`,
                }}
            >
                {blocks.map((block, idx) => {
                    const bg  = rateToColor(block.rate);
                    const isActive = activeTooltip?.idx === idx;
                    return (
                        <div
                            key={idx}
                            className={`rounded-[1px] transition-all duration-150 cursor-pointer
                                ${isActive
                                    ? 'scale-125 opacity-90 z-10 relative'
                                    : 'hover:scale-110 hover:opacity-80'}`}
                            style={{ backgroundColor: bg ?? 'rgba(255,255,255,0.05)' }}
                            onPointerEnter={(e) => { if (e.pointerType === 'mouse') openTooltip(idx, e.currentTarget); }}
                            onPointerLeave={(e) => { if (e.pointerType === 'mouse') setActiveTooltip(null); }}
                            onPointerDown={(e) => {
                                if (e.pointerType === 'touch') {
                                    e.preventDefault();
                                    setActiveTooltip(prev =>
                                        prev?.idx === idx ? null : buildTooltipState(idx, e.currentTarget));
                                }
                            }}
                        >
                            {isActive && activeTooltip && renderTooltip(block, activeTooltip)}
                        </div>
                    );
                })}
            </div>

            {/* ─ Footer: time labels + legend ─ */}
            <div className="flex items-center justify-between mt-2.5 text-[10px] text-on-surface-variant font-mono">
                <span>{hasData ? fmtDt(minTime) : '─'}</span>

                <div className="flex items-center gap-3">
                    {[
                        { cls: 'bg-white/[0.05] border border-white/10', label: t('HEALTH.IDLE',     '空闲') },
                        { cls: 'bg-error',                                label: t('HEALTH.FAULTY',   '故障') },
                        { cls: 'bg-warning',                              label: t('HEALTH.DEGRADED', '降级') },
                        { cls: 'bg-success',                              label: t('HEALTH.NORMAL',   '正常') },
                    ].map(({ cls, label }) => (
                        <span key={label} className="flex items-center gap-1">
                            <span className={`w-2 h-2 rounded-[1px] shrink-0 ${cls}`} />
                            {label}
                        </span>
                    ))}
                </div>

                <span>{t('HEALTH.NOW', '现在')}</span>
            </div>
        </div>
    );
}
