import React, { useMemo, useState, useRef, useEffect, useCallback } from 'react';
import { createPortal } from 'react-dom';
import { useTranslation } from 'react-i18next';

const BLOCKS_COUNT = 60;
const TOOLTIP_SAFE_WIDTH = 240;
const TOOLTIP_SAFE_HEIGHT = 100;
const TOOLTIP_OFFSET = 12;

function calculateServiceHealthData(logs, globalSummary) {
    if (!logs || logs.length === 0) {
        return {
            successRate: globalSummary ? (globalSummary.successReqs / (globalSummary.totalReqs || 1)) * 100 : 100,
            totalSuccess: globalSummary ? globalSummary.successReqs : 0,
            totalFailure: globalSummary ? globalSummary.failedReqs : 0,
            blockDetails: Array.from({ length: BLOCKS_COUNT }).map(() => ({
                success: 0,
                failure: 0,
                rate: -1,
                startTime: Date.now(),
                endTime: Date.now()
            }))
        };
    }

    let minTime = Number.MAX_SAFE_INTEGER;
    let maxTime = Number.MIN_SAFE_INTEGER;
    let totalSuccess = 0;
    let totalFailure = 0;

    logs.forEach(log => {
        const t = new Date(log.created_at).getTime();
        if (t < minTime) minTime = t;
        if (t > maxTime) maxTime = t;
        if (log.failed) {
            totalFailure++;
        } else {
            totalSuccess++;
        }
    });

    if (minTime === maxTime || maxTime === Number.MIN_SAFE_INTEGER) {
        minTime = Date.now() - 3600000; // default 1 hour fallback
        maxTime = Date.now();
    }

    const timeSpan = maxTime - minTime;
    const blockDuration = Math.max(1000, timeSpan / BLOCKS_COUNT);

    const blocks = Array.from({ length: BLOCKS_COUNT }, (_, i) => ({
        success: 0,
        failure: 0,
        startTime: minTime + i * blockDuration,
        endTime: minTime + (i + 1) * blockDuration,
        rate: -1,
    }));

    logs.forEach(log => {
        const t = new Date(log.created_at).getTime();
        let idx = Math.floor((t - minTime) / blockDuration);
        if (idx >= BLOCKS_COUNT) idx = BLOCKS_COUNT - 1;
        if (idx < 0) idx = 0;

        if (log.failed) {
            blocks[idx].failure++;
        } else {
            blocks[idx].success++;
        }
    });

    blocks.forEach(b => {
        const total = b.success + b.failure;
        b.rate = total === 0 ? -1 : b.success / total;
    });

    const successRate = (totalSuccess / (totalSuccess + totalFailure || 1)) * 100;

    return {
        successRate,
        totalSuccess,
        totalFailure,
        blockDetails: blocks
    };
}

const rateToColor = (rate) => {
    if (rate === -1) return '#ffffff0a';
    if (rate >= 0.9) return '#10b981';
    if (rate >= 0.5) return '#f59e0b';
    return '#ef4444';
};

const formatDateTime = (ts) => {
    const d = new Date(ts);
    const month = String(d.getMonth() + 1).padStart(2, '0');
    const day = String(d.getDate()).padStart(2, '0');
    const h = String(d.getHours()).padStart(2, '0');
    const m = String(d.getMinutes()).padStart(2, '0');
    return `${month}/${day} ${h}:${m}`;
};

export function HealthMonitor({ logs, summary }) {
    const { t } = useTranslation();
    const [activeTooltip, setActiveTooltip] = useState(null);
    const gridRef = useRef(null);

    const healthData = useMemo(() => calculateServiceHealthData(logs, summary), [logs, summary]);
    const hasData = healthData.totalSuccess + healthData.totalFailure > 0;

    useEffect(() => {
        if (!activeTooltip) return;
        const handler = (e) => {
            if (gridRef.current && !gridRef.current.contains(e.target)) {
                setActiveTooltip(null);
            }
        };
        document.addEventListener('pointerdown', handler);
        return () => document.removeEventListener('pointerdown', handler);
    }, [activeTooltip]);

    const buildTooltipState = useCallback((idx, anchorEl) => {
        if (!anchorEl || !anchorEl.isConnected) return null;
        const rect = anchorEl.getBoundingClientRect();
        const centerX = rect.left + rect.width / 2;

        let horizontal = 'center';
        let left = centerX;

        if (centerX <= TOOLTIP_SAFE_WIDTH / 2) {
            horizontal = 'left';
            left = rect.left;
        } else if (centerX >= window.innerWidth - TOOLTIP_SAFE_WIDTH / 2) {
            horizontal = 'right';
            left = rect.right;
        }

        const vertical = rect.top <= TOOLTIP_SAFE_HEIGHT ? 'below' : 'above';
        const top = vertical === 'below' ? rect.bottom + TOOLTIP_OFFSET : rect.top - TOOLTIP_OFFSET;
        const translateX = horizontal === 'center' ? '-50%' : horizontal === 'right' ? '-100%' : '0';
        const translateY = vertical === 'below' ? '0' : '-100%';

        return {
            idx, anchorEl, horizontal, vertical,
            left: Math.round(left), top: Math.round(top),
            transform: `translate(${translateX}, ${translateY})`
        };
    }, []);

    const openTooltip = useCallback((idx, anchorEl) => {
        setActiveTooltip(buildTooltipState(idx, anchorEl));
    }, [buildTooltipState]);

    const handlePointerEnter = useCallback((e, idx) => {
        if (e.pointerType === "mouse") openTooltip(idx, e.currentTarget);
    }, [openTooltip]);

    const handlePointerLeave = useCallback((e) => {
        if (e.pointerType === "mouse") setActiveTooltip(null);
    }, []);

    const handlePointerDown = useCallback((e, idx) => {
        if (e.pointerType === "touch") {
            e.preventDefault();
            const anchorEl = e.currentTarget;
            setActiveTooltip((prev) => (prev?.idx === idx ? null : buildTooltipState(idx, anchorEl)));
        }
    }, [buildTooltipState]);

    useEffect(() => {
        if (!activeTooltip) return;
        const updateTooltipPosition = () => {
            if (!document.body.contains(activeTooltip.anchorEl)) {
                setActiveTooltip(null);
                return;
            }
            setActiveTooltip(buildTooltipState(activeTooltip.idx, activeTooltip.anchorEl));
        };
        window.addEventListener('resize', updateTooltipPosition);
        window.addEventListener('scroll', updateTooltipPosition, true);
        return () => {
            window.removeEventListener('resize', updateTooltipPosition);
            window.removeEventListener('scroll', updateTooltipPosition, true);
        };
    }, [activeTooltip, buildTooltipState]);

    const renderTooltip = (detail, tooltipState) => {
        const total = detail.success + detail.failure;
        const timeRange = `${formatDateTime(detail.startTime)} - ${formatDateTime(detail.endTime)}`;

        const tooltip = (
            <div role="tooltip" className="fixed z-50 bg-surface-container-high border border-outline-variant p-3 rounded-control shadow-black/50 pointer-events-none"
                 style={{ left: `${tooltipState.left}px`, top: `${tooltipState.top}px`, transform: tooltipState.transform }}>
                <div className="text-[10px] text-on-surface-variant mb-1.5 whitespace-nowrap">{timeRange}</div>
                {total > 0 ? (
                    <div className="flex items-center gap-3 text-xs">
                        <span className="text-success font-mono">{t('HEALTH.SUCCESS', '成功')}: {detail.success}</span>
                        <span className="text-error font-mono">{t('HEALTH.FAILURE', '失败')}: {detail.failure}</span>
                        <span className="text-on-surface font-mono">({(detail.rate * 100).toFixed(1)}%)</span>
                    </div>
                ) : (
                    <div className="text-xs text-on-surface-variant font-mono">{t('HEALTH.NO_REQUESTS', '无请求')}</div>
                )}
            </div>
        );
        return typeof document === 'undefined' ? tooltip : createPortal(tooltip, document.body);
    };

    return (
        <div className="bg-surface border border-outline-variant rounded-overlay p-5 w-full mb-6 relative overflow-hidden group">
            <div className="flex items-center justify-between mb-4">
                <div className="flex items-center gap-2">
                    <h3 className="text-sm font-semibold text-on-surface-variant">{t('HEALTH.TITLE', '服务健康监测')}</h3>
                    <div className="px-2 py-0.5 rounded-control text-[10px] bg-surface-container border border-outline-variant text-on-surface-variant">{t('HEALTH.RECENT_LOGS', '最近日志')}</div>
                </div>
                <div className="flex items-center gap-2">
                    <span className="text-xs text-on-surface-variant">{t('HEALTH.OVERALL_AVAILABILITY', '整体可用率')}</span>
                    <span className={`text-sm font-bold font-mono ${healthData.successRate >= 90 ? 'text-success' : healthData.successRate >= 50 ? 'text-warning' : 'text-error'}`}>
                        {hasData ? `${healthData.successRate.toFixed(1)}%` : '--'}
                    </span>
                </div>
            </div>

            <div className="w-full flex justify-between gap-1 h-12">
                {healthData.blockDetails.map((detail, idx) => {
                    const isIdle = detail.rate === -1;
                    const blockStyle = {
                        backgroundColor: rateToColor(detail.rate),
                        opacity: isIdle ? 0.3 : 1
                    };
                    const isActive = activeTooltip?.idx === idx;

                    return (
                        <div
                            key={idx}
                            className={`flex-1 h-full rounded-control-[2px] transition-all duration-200 cursor-pointer ${isActive ? 'scale-110 brightness-125 z-10' : 'hover:scale-105 hover:brightness-110'}`}
                            style={blockStyle}
                            onPointerEnter={(e) => handlePointerEnter(e, idx)}
                            onPointerLeave={handlePointerLeave}
                            onPointerDown={(e) => handlePointerDown(e, idx)}
                        >
                            {isActive && activeTooltip && renderTooltip(detail, activeTooltip)}
                        </div>
                    );
                })}
            </div>

            <div className="flex items-center justify-between mt-3 px-1 text-[10px] text-on-surface-variant font-mono">
                <span>{hasData ? formatDateTime(healthData.blockDetails[0].startTime) : t('HEALTH.LONG_AGO', '很久以前')}</span>

                <div className="flex items-center gap-3">
                    <div className="flex items-center gap-1"><span className="w-2 h-2 rounded-control-[1px] bg-white/10"></span>{t('HEALTH.IDLE', '闲置')}</div>
                    <div className="flex items-center gap-1"><span className="w-2 h-2 rounded-control-[1px] bg-error"></span>{t('HEALTH.FAULTY', '故障')}</div>
                    <div className="flex items-center gap-1"><span className="w-2 h-2 rounded-control-[1px] bg-warning"></span>{t('HEALTH.DEGRADED', '部分可用')}</div>
                    <div className="flex items-center gap-1"><span className="w-2 h-2 rounded-control-[1px] bg-success"></span>{t('HEALTH.NORMAL', '正常')}</div>
                </div>

                <span>{t('HEALTH.NOW', '现在')}</span>
            </div>
        </div>
    );
}
