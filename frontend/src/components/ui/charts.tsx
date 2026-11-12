'use client';

import React, { useId, useMemo, useState } from 'react';
import { cn } from '@/lib/utils/cn';

export interface ChartSeries {
  id: string;
  name: string;
  color: string;
  data: number[];
  fillOpacity?: number;
}

export interface AreaLineChartProps {
  series: ChartSeries[];
  labels?: string[];
  height?: number;
  minY?: number;
  maxY?: number;
  formatY?: (val: number) => string;
  referenceLine?: {
    value: number;
    label?: string;
    color?: string;
  };
  emptyMessage?: string;
  showLegend?: boolean;
  className?: string;
  ariaLabel?: string;
}

function buildSmoothPath(points: Array<{ x: number; y: number }>): string {
  if (points.length === 0) return '';
  if (points.length === 1) {
    return `M ${points[0].x.toFixed(2)} ${points[0].y.toFixed(2)}`;
  }
  if (points.length === 2) {
    return `M ${points[0].x.toFixed(2)} ${points[0].y.toFixed(2)} L ${points[1].x.toFixed(2)} ${points[1].y.toFixed(2)}`;
  }

  let d = `M ${points[0].x.toFixed(2)} ${points[0].y.toFixed(2)}`;
  for (let i = 0; i < points.length - 1; i++) {
    const p0 = points[i === 0 ? i : i - 1];
    const p1 = points[i];
    const p2 = points[i + 1];
    const p3 = points[i + 2 < points.length ? i + 2 : i + 1];

    // Catmull-Rom to cubic Bezier with gentle tension so curves never overshoot wildly
    const cp1x = p1.x + (p2.x - p0.x) / 6;
    const cp1y = p1.y + (p2.y - p0.y) / 6;
    const cp2x = p2.x - (p3.x - p1.x) / 6;
    const cp2y = p2.y - (p3.y - p1.y) / 6;

    d += ` C ${cp1x.toFixed(2)} ${cp1y.toFixed(2)}, ${cp2x.toFixed(2)} ${cp2y.toFixed(2)}, ${p2.x.toFixed(2)} ${p2.y.toFixed(2)}`;
  }
  return d;
}

export function AreaLineChart({
  series,
  labels = [],
  height = 180,
  minY = 0,
  maxY,
  formatY = (v) => v.toFixed(1),
  referenceLine,
  emptyMessage = 'Collecting samples for graph...',
  showLegend = true,
  className,
  ariaLabel = 'Telemetry time-series chart',
}: AreaLineChartProps) {
  const rawUid = useId();
  const uid = rawUid.replace(/[^a-zA-Z0-9_-]/g, '');
  const [hoverIdx, setHoverIdx] = useState<number | null>(null);
  const [hiddenSeries, setHiddenSeries] = useState<Record<string, boolean>>({});

  const visibleSeries = useMemo(
    () => series.filter((s) => !hiddenSeries[s.id]),
    [series, hiddenSeries]
  );

  const maxPoints = useMemo(
    () => Math.max(0, ...series.map((s) => s.data.length)),
    [series]
  );

  const vbWidth = 600;
  const vbHeight = Math.max(110, height);
  const padLeft = 46;
  const padRight = 14;
  const padTop = 14;
  const padBottom = labels.length > 0 ? 24 : 12;
  const plotW = vbWidth - padLeft - padRight;
  const plotH = vbHeight - padTop - padBottom;

  const { domainMin, domainMax } = useMemo(() => {
    const allVals = visibleSeries.flatMap((s) =>
      s.data.filter((v) => Number.isFinite(v))
    );
    const refVal = referenceLine?.value;
    if (typeof refVal === 'number' && Number.isFinite(refVal)) {
      allVals.push(refVal);
    }
    const rawMax = allVals.length > 0 ? Math.max(...allVals) : 1;
    const dMin = minY;
    let dMax = typeof maxY === 'number' ? maxY : rawMax * 1.15;
    if (dMax <= dMin) {
      dMax = dMin + 1;
    }
    return { domainMin: dMin, domainMax: dMax };
  }, [visibleSeries, referenceLine?.value, minY, maxY]);

  if (maxPoints === 0) {
    return (
      <div
        className={cn(
          'flex items-center justify-center rounded-md border border-dashed border-surface-border bg-black/25 text-xs font-mono text-neutral-500',
          className
        )}
        style={{ height }}
      >
        {emptyMessage}
      </div>
    );
  }

  const getX = (idx: number, count: number) => {
    if (count <= 1) return padLeft + plotW / 2;
    return padLeft + (idx / (count - 1)) * plotW;
  };

  const getY = (val: number) => {
    const clamped = Math.max(domainMin, Math.min(domainMax, Number.isFinite(val) ? val : domainMin));
    const ratio = (clamped - domainMin) / (domainMax - domainMin);
    return padTop + plotH - ratio * plotH;
  };

  const yTicks = [0, 0.333, 0.666, 1].map((t) => domainMin + t * (domainMax - domainMin));
  const baselineY = padTop + plotH;

  const xLabelIndices: number[] = [];
  if (labels.length > 0 && maxPoints > 0) {
    if (maxPoints === 1) {
      xLabelIndices.push(0);
    } else if (maxPoints === 2) {
      xLabelIndices.push(0, 1);
    } else {
      xLabelIndices.push(0, Math.floor((maxPoints - 1) / 2), maxPoints - 1);
    }
  }

  const activeHoverIdx =
    hoverIdx !== null ? Math.max(0, Math.min(maxPoints - 1, hoverIdx)) : null;

  const handleMouseMove = (e: React.MouseEvent<SVGSVGElement>) => {
    if (maxPoints === 0) return;
    const rect = e.currentTarget.getBoundingClientRect();
    if (rect.width <= 0) return;
    const svgX = ((e.clientX - rect.left) / rect.width) * vbWidth;
    if (maxPoints === 1) {
      setHoverIdx(0);
      return;
    }
    const rel = Math.max(0, Math.min(1, (svgX - padLeft) / plotW));
    setHoverIdx(Math.round(rel * (maxPoints - 1)));
  };

  const toggleSeries = (id: string) => {
    setHiddenSeries((prev) => {
      const next = { ...prev, [id]: !prev[id] };
      const remaining = series.filter((s) => !next[s.id]);
      if (remaining.length === 0) return prev;
      return next;
    });
  };

  return (
    <div className={cn('space-y-2', className)}>
      {showLegend && series.length > 1 && (
        <div className="flex flex-wrap items-center gap-2 text-[11px] font-mono">
          {series.map((s) => {
            const isHidden = Boolean(hiddenSeries[s.id]);
            const latest = s.data.length > 0 ? s.data[s.data.length - 1] : 0;
            return (
              <button
                key={s.id}
                type="button"
                onClick={() => toggleSeries(s.id)}
                className={cn(
                  'inline-flex items-center gap-1.5 px-2 py-0.5 rounded border transition-colors',
                  isHidden
                    ? 'border-surface-border bg-surface/40 text-neutral-500 opacity-60'
                    : 'border-surface-border bg-surface-elevated/70 text-neutral-200 hover:border-neutral-600'
                )}
              >
                <span
                  className="w-2 h-2 rounded-full shrink-0"
                  style={{ backgroundColor: s.color }}
                />
                <span>{s.name}</span>
                <span className="text-neutral-400 tabular-nums">{formatY(latest)}</span>
              </button>
            );
          })}
        </div>
      )}

      <div className="relative w-full select-none">
        <svg
          viewBox={`0 0 ${vbWidth} ${vbHeight}`}
          className="w-full h-auto block overflow-visible rounded-md border border-surface-border/70 bg-black/25"
          style={{ maxHeight: height + 36 }}
          role="img"
          aria-label={ariaLabel}
          onMouseMove={handleMouseMove}
          onMouseLeave={() => setHoverIdx(null)}
        >
          <defs>
            {visibleSeries.map((s) => (
              <linearGradient
                key={s.id}
                id={`grad-${uid}-${s.id}`}
                x1="0"
                y1="0"
                x2="0"
                y2="1"
              >
                <stop offset="0%" stopColor={s.color} stopOpacity={s.fillOpacity ?? 0.34} />
                <stop offset="100%" stopColor={s.color} stopOpacity={0.02} />
              </linearGradient>
            ))}
          </defs>

          {/* Gridlines & Y-Axis Ticks */}
          {yTicks.map((tickVal, idx) => {
            const y = getY(tickVal);
            return (
              <g key={idx}>
                <line
                  x1={padLeft}
                  y1={y}
                  x2={vbWidth - padRight}
                  y2={y}
                  stroke="#27272a"
                  strokeDasharray={idx === 0 ? undefined : '3 3'}
                  strokeWidth="1"
                />
                <text
                  x={padLeft - 6}
                  y={y + 3.5}
                  textAnchor="end"
                  className="fill-neutral-500 text-[9.5px] font-mono tabular-nums"
                >
                  {formatY(tickVal)}
                </text>
              </g>
            );
          })}

          {/* Reference Threshold / Peak Line */}
          {referenceLine && Number.isFinite(referenceLine.value) && (
            <g>
              <line
                x1={padLeft}
                y1={getY(referenceLine.value)}
                x2={vbWidth - padRight}
                y2={getY(referenceLine.value)}
                stroke={referenceLine.color || '#f59e0b'}
                strokeDasharray="4 3"
                strokeWidth="1"
                opacity="0.75"
              />
              {referenceLine.label && (
                <text
                  x={vbWidth - padRight - 4}
                  y={Math.max(padTop + 9, getY(referenceLine.value) - 4)}
                  textAnchor="end"
                  fill={referenceLine.color || '#fbbf24'}
                  className="text-[9px] font-mono"
                >
                  {referenceLine.label}
                </text>
              )}
            </g>
          )}

          {/* Area Fills & Smooth Lines */}
          {visibleSeries.map((s) => {
            const pts = s.data.map((val, idx) => ({
              x: getX(idx, s.data.length),
              y: getY(val),
            }));
            if (pts.length === 0) return null;

            if (pts.length === 1) {
              return (
                <g key={s.id}>
                  <line
                    x1={padLeft}
                    y1={pts[0].y}
                    x2={vbWidth - padRight}
                    y2={pts[0].y}
                    stroke={s.color}
                    strokeWidth="1.75"
                    strokeDasharray="4 4"
                    opacity="0.7"
                  />
                  <circle
                    cx={pts[0].x}
                    cy={pts[0].y}
                    r="4"
                    fill={s.color}
                    stroke="#09090b"
                    strokeWidth="1.5"
                  />
                </g>
              );
            }

            const linePath = buildSmoothPath(pts);
            const areaPath = `${linePath} L ${pts[pts.length - 1].x.toFixed(2)} ${baselineY.toFixed(
              2
            )} L ${pts[0].x.toFixed(2)} ${baselineY.toFixed(2)} Z`;

            return (
              <g key={s.id}>
                <path d={areaPath} fill={`url(#grad-${uid}-${s.id})`} />
                <path
                  d={linePath}
                  fill="none"
                  stroke={s.color}
                  strokeWidth="2"
                  strokeLinecap="round"
                  strokeLinejoin="round"
                />
              </g>
            );
          })}

          {/* X-Axis Timestamp Labels */}
          {xLabelIndices.map((idx) => {
            const label = labels[idx];
            if (!label) return null;
            const x = getX(idx, maxPoints);
            const anchor =
              idx === 0 && maxPoints > 1
                ? 'start'
                : idx === maxPoints - 1 && maxPoints > 1
                ? 'end'
                : 'middle';
            return (
              <text
                key={idx}
                x={x}
                y={vbHeight - 6}
                textAnchor={anchor}
                className="fill-neutral-500 text-[9.5px] font-mono tabular-nums"
              >
                {label}
              </text>
            );
          })}

          {/* Hover Crosshair & Active Dots */}
          {activeHoverIdx !== null && (
            <g>
              <line
                x1={getX(activeHoverIdx, maxPoints)}
                y1={padTop}
                x2={getX(activeHoverIdx, maxPoints)}
                y2={baselineY}
                stroke="#52525b"
                strokeDasharray="2 2"
                strokeWidth="1"
              />
              {visibleSeries.map((s) => {
                const val = s.data[activeHoverIdx];
                if (val === undefined) return null;
                const x = getX(activeHoverIdx, s.data.length);
                const y = getY(val);
                return (
                  <circle
                    key={s.id}
                    cx={x}
                    cy={y}
                    r="4"
                    fill={s.color}
                    stroke="#09090b"
                    strokeWidth="1.5"
                  />
                );
              })}
            </g>
          )}
        </svg>

        {/* Floating Tooltip */}
        {activeHoverIdx !== null && (
          <div
            className="pointer-events-none absolute top-2 z-20 rounded-md border border-neutral-700 bg-zinc-950/95 px-2.5 py-1.5 shadow-xl text-[11px] font-mono backdrop-blur-sm"
            style={{
              left:
                activeHoverIdx > maxPoints * 0.65
                  ? undefined
                  : `${Math.min(72, Math.max(8, (getX(activeHoverIdx, maxPoints) / vbWidth) * 100))}%`,
              right:
                activeHoverIdx > maxPoints * 0.65
                  ? `${Math.min(72, Math.max(4, 100 - (getX(activeHoverIdx, maxPoints) / vbWidth) * 100))}%`
                  : undefined,
            }}
          >
            {labels[activeHoverIdx] && (
              <div className="text-[10px] text-neutral-400 border-b border-neutral-800 pb-1 mb-1">
                {labels[activeHoverIdx]}
              </div>
            )}
            <div className="space-y-0.5">
              {visibleSeries.map((s) => {
                const v = s.data[activeHoverIdx];
                if (v === undefined) return null;
                return (
                  <div key={s.id} className="flex items-center justify-between gap-3">
                    <span className="inline-flex items-center gap-1.5 text-neutral-300">
                      <span
                        className="w-1.5 h-1.5 rounded-full"
                        style={{ backgroundColor: s.color }}
                      />
                      {s.name}
                    </span>
                    <span className="text-white font-semibold tabular-nums">{formatY(v)}</span>
                  </div>
                );
              })}
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

export interface RadialGaugeProps {
  value: number;
  max?: number;
  size?: number;
  strokeWidth?: number;
  color?: string;
  thresholds?: { warn: number; danger: number };
  label?: string;
  sublabel?: string;
  valueLabel?: string;
  className?: string;
}

export function RadialGauge({
  value,
  max = 100,
  size = 96,
  strokeWidth = 8,
  color,
  thresholds = { warn: 70, danger: 90 },
  label,
  sublabel,
  valueLabel,
  className,
}: RadialGaugeProps) {
  const safeMax = max > 0 ? max : 100;
  const pct = Math.max(0, Math.min(100, (Number.isFinite(value) ? value / safeMax : 0) * 100));

  const resolvedColor =
    color ||
    (pct >= thresholds.danger
      ? '#f43f5e'
      : pct >= thresholds.warn
      ? '#f59e0b'
      : '#10b981');

  const r = (size - strokeWidth) / 2;
  const cx = size / 2;
  const cy = size / 2;
  // 240-degree open arc gauge (rotated 150deg so gap sits cleanly at the bottom)
  const arcFraction = 240 / 360;
  const circumference = 2 * Math.PI * r;
  const arcLength = circumference * arcFraction;
  const filledLength = arcLength * (pct / 100);

  return (
    <div className={cn('flex flex-col items-center justify-center text-center', className)}>
      <div className="relative" style={{ width: size, height: size }}>
        <svg
          width={size}
          height={size}
          viewBox={`0 0 ${size} ${size}`}
          role="img"
          aria-label={label ? `${label}: ${valueLabel || `${pct.toFixed(0)}%`}` : 'Radial gauge'}
        >
          {/* Background track arc */}
          <circle
            cx={cx}
            cy={cy}
            r={r}
            fill="none"
            stroke="#27272a"
            strokeWidth={strokeWidth}
            strokeLinecap="round"
            strokeDasharray={`${arcLength} ${circumference}`}
            transform={`rotate(150 ${cx} ${cy})`}
          />
          {/* Value arc */}
          <circle
            cx={cx}
            cy={cy}
            r={r}
            fill="none"
            stroke={resolvedColor}
            strokeWidth={strokeWidth}
            strokeLinecap="round"
            strokeDasharray={`${filledLength} ${circumference}`}
            transform={`rotate(150 ${cx} ${cy})`}
            style={{
              transition: 'stroke-dasharray 350ms ease, stroke 250ms ease',
              filter: `drop-shadow(0 0 6px ${resolvedColor}40)`,
            }}
          />
        </svg>

        <div className="absolute inset-0 flex flex-col items-center justify-center pt-1">
          <span className="text-xs font-mono font-bold text-white tabular-nums leading-tight">
            {valueLabel ?? `${pct.toFixed(0)}%`}
          </span>
          {label && (
            <span className="text-[9px] font-mono uppercase tracking-wider text-neutral-400 mt-0.5">
              {label}
            </span>
          )}
        </div>
      </div>
      {sublabel && (
        <span className="text-[10px] font-mono text-neutral-400 -mt-1.5 leading-tight truncate max-w-full">
          {sublabel}
        </span>
      )}
    </div>
  );
}

export interface DonutSegment {
  id: string;
  label: string;
  value: number;
  color: string;
  detail?: string;
}

export interface DonutChartProps {
  segments: DonutSegment[];
  size?: number;
  thickness?: number;
  centerLabel?: string;
  centerValue?: string;
  formatValue?: (val: number) => string;
  emptyLabel?: string;
  className?: string;
}

export function DonutChart({
  segments,
  size = 136,
  thickness = 16,
  centerLabel,
  centerValue,
  formatValue = (v) => String(v),
  emptyLabel = 'No data',
  className,
}: DonutChartProps) {
  const [hoveredId, setHoveredId] = useState<string | null>(null);

  const nonZero = segments.filter((s) => Number.isFinite(s.value) && s.value > 0);
  const total = nonZero.reduce((sum, s) => sum + s.value, 0);

  const r = (size - thickness) / 2;
  const cx = size / 2;
  const cy = size / 2;
  const circumference = 2 * Math.PI * r;

  let cumulativeOffset = 0;
  const arcs = nonZero.map((seg) => {
    const share = total > 0 ? seg.value / total : 0;
    const dashLen = Math.max(0, share * circumference - (nonZero.length > 1 ? 2 : 0));
    const offset = cumulativeOffset;
    cumulativeOffset += share * circumference;
    return { ...seg, share, dashLen, offset };
  });

  const activeSeg = arcs.find((a) => a.id === hoveredId) || null;

  return (
    <div className={cn('flex flex-col sm:flex-row items-center gap-4', className)}>
      <div className="relative shrink-0" style={{ width: size, height: size }}>
        <svg
          width={size}
          height={size}
          viewBox={`0 0 ${size} ${size}`}
          role="img"
          aria-label={centerLabel || 'Distribution donut chart'}
        >
          <circle
            cx={cx}
            cy={cy}
            r={r}
            fill="none"
            stroke="#1f1f23"
            strokeWidth={thickness}
          />
          {total > 0 &&
            arcs.map((arc) => {
              const isHovered = hoveredId === arc.id;
              const isDimmed = hoveredId !== null && !isHovered;
              return (
                <circle
                  key={arc.id}
                  cx={cx}
                  cy={cy}
                  r={r}
                  fill="none"
                  stroke={arc.color}
                  strokeWidth={isHovered ? thickness + 3 : thickness}
                  strokeDasharray={`${arc.dashLen} ${circumference}`}
                  strokeDashoffset={-arc.offset}
                  transform={`rotate(-90 ${cx} ${cy})`}
                  style={{
                    opacity: isDimmed ? 0.35 : 1,
                    transition: 'opacity 180ms ease, stroke-width 180ms ease',
                    cursor: 'pointer',
                  }}
                  onMouseEnter={() => setHoveredId(arc.id)}
                  onMouseLeave={() => setHoveredId(null)}
                />
              );
            })}
        </svg>

        <div className="pointer-events-none absolute inset-0 flex flex-col items-center justify-center px-3 text-center">
          {total <= 0 ? (
            <span className="text-[10px] font-mono text-neutral-500">{emptyLabel}</span>
          ) : activeSeg ? (
            <>
              <span className="text-xs font-mono font-bold text-white tabular-nums leading-tight">
                {(activeSeg.share * 100).toFixed(0)}%
              </span>
              <span className="text-[10px] font-mono text-neutral-300 truncate max-w-[78px]">
                {activeSeg.label}
              </span>
            </>
          ) : (
            <>
              {centerValue && (
                <span className="text-xs font-mono font-bold text-white tabular-nums leading-tight">
                  {centerValue}
                </span>
              )}
              {centerLabel && (
                <span className="text-[9px] font-mono uppercase tracking-wider text-neutral-400 mt-0.5">
                  {centerLabel}
                </span>
              )}
            </>
          )}
        </div>
      </div>

      {/* Proportional Graph Legend */}
      <div className="flex-1 w-full space-y-2 min-w-0">
        {segments.map((seg) => {
          const share = total > 0 ? (seg.value / total) * 100 : 0;
          const isHovered = hoveredId === seg.id;
          return (
            <div
              key={seg.id}
              onMouseEnter={() => setHoveredId(seg.id)}
              onMouseLeave={() => setHoveredId(null)}
              className={cn(
                'space-y-1 rounded px-2 py-1 transition-colors cursor-default',
                isHovered ? 'bg-surface-elevated/80' : 'hover:bg-surface-elevated/40'
              )}
            >
              <div className="flex items-center justify-between gap-2 text-[11px] font-mono">
                <div className="flex items-center gap-1.5 min-w-0">
                  <span
                    className="w-2 h-2 rounded-full shrink-0"
                    style={{ backgroundColor: seg.color }}
                  />
                  <span className="text-neutral-200 truncate">{seg.label}</span>
                  {seg.detail && (
                    <span className="text-[10px] text-neutral-500 truncate hidden sm:inline">
                      ({seg.detail})
                    </span>
                  )}
                </div>
                <span className="text-neutral-400 tabular-nums shrink-0">
                  {formatValue(seg.value)}{' '}
                  <span className="text-neutral-500">({share.toFixed(0)}%)</span>
                </span>
              </div>
              <div className="h-1.5 w-full rounded-full bg-surface-border/60 overflow-hidden">
                <div
                  className="h-full rounded-full transition-all duration-300"
                  style={{
                    width: `${Math.max(seg.value > 0 ? 3 : 0, share)}%`,
                    backgroundColor: seg.color,
                  }}
                />
              </div>
            </div>
          );
        })}
      </div>
    </div>
  );
}

export interface StackedBarSegment {
  id: string;
  label: string;
  value: number;
  color: string;
  hint?: string;
}

export function StackedDistributionBar({
  segments,
  height = 10,
  formatValue = (v) => String(v),
  className,
}: {
  segments: StackedBarSegment[];
  height?: number;
  formatValue?: (v: number) => string;
  className?: string;
}) {
  const total = segments.reduce((sum, s) => sum + Math.max(0, s.value), 0);

  return (
    <div className={cn('space-y-2', className)}>
      <div
        className="w-full rounded-full bg-surface-elevated border border-surface-border overflow-hidden flex"
        style={{ height }}
        role="img"
        aria-label="Distribution bar graph"
      >
        {total <= 0 ? (
          <div className="h-full w-full bg-neutral-800/60" />
        ) : (
          segments
            .filter((s) => s.value > 0)
            .map((seg) => {
              const pct = (seg.value / total) * 100;
              return (
                <div
                  key={seg.id}
                  title={`${seg.label}: ${formatValue(seg.value)} (${pct.toFixed(1)}%)${seg.hint ? ` · ${seg.hint}` : ''}`}
                  className="h-full transition-all duration-300 first:rounded-l-full last:rounded-r-full"
                  style={{
                    width: `${pct}%`,
                    backgroundColor: seg.color,
                  }}
                />
              );
            })
        )}
      </div>

      <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-[11px] font-mono text-neutral-400">
        {segments.map((seg) => {
          const pct = total > 0 ? (Math.max(0, seg.value) / total) * 100 : 0;
          return (
            <div key={seg.id} className="inline-flex items-center gap-1.5">
              <span
                className="w-2 h-2 rounded-full shrink-0"
                style={{ backgroundColor: seg.color }}
              />
              <span className="text-neutral-300">{seg.label}</span>
              <span className="text-neutral-500 tabular-nums">
                {formatValue(seg.value)} ({pct.toFixed(0)}%)
              </span>
            </div>
          );
        })}
      </div>
    </div>
  );
}

export interface HorizontalBarItem {
  id: string;
  label: string;
  value: number;
  maxValue?: number;
  valueLabel?: string;
  secondaryValue?: number;
  secondaryColor?: string;
  color: string;
  sublabel?: string;
}

export function HorizontalBarChart({
  items,
  formatValue = (v) => v.toFixed(1),
  emptyMessage = 'No data available',
  className,
}: {
  items: HorizontalBarItem[];
  formatValue?: (v: number) => string;
  emptyMessage?: string;
  className?: string;
}) {
  if (items.length === 0) {
    return (
      <div className="p-4 rounded border border-dashed border-surface-border text-center text-xs font-mono text-neutral-500">
        {emptyMessage}
      </div>
    );
  }

  const globalMax = Math.max(
    1,
    ...items.map((item) => Math.max(item.maxValue ?? 0, item.value, item.secondaryValue ?? 0))
  );

  return (
    <div className={cn('space-y-2.5', className)}>
      {items.map((item) => {
        const denom = item.maxValue && item.maxValue > 0 ? item.maxValue : globalMax;
        const pct = Math.max(0, Math.min(100, (item.value / denom) * 100));
        const secPct =
          typeof item.secondaryValue === 'number'
            ? Math.max(0, Math.min(100, (item.secondaryValue / denom) * 100))
            : null;

        return (
          <div key={item.id} className="space-y-1">
            <div className="flex items-center justify-between gap-2 text-[11px] font-mono">
              <div className="flex items-center gap-1.5 min-w-0">
                <span className="text-neutral-200 font-medium truncate">{item.label}</span>
                {item.sublabel && (
                  <span className="text-[10px] text-neutral-500 truncate">{item.sublabel}</span>
                )}
              </div>
              <span className="text-neutral-300 tabular-nums shrink-0">
                {item.valueLabel ?? formatValue(item.value)}
              </span>
            </div>

            <div className="h-2 w-full rounded-full bg-surface-border/60 overflow-hidden relative">
              {secPct !== null && (
                <div
                  className="absolute inset-y-0 left-0 rounded-full opacity-35 transition-all duration-300"
                  style={{
                    width: `${Math.max(item.secondaryValue! > 0 ? 2 : 0, secPct)}%`,
                    backgroundColor: item.secondaryColor || item.color,
                  }}
                />
              )}
              <div
                className="relative h-full rounded-full transition-all duration-300"
                style={{
                  width: `${Math.max(item.value > 0 ? 2 : 0, pct)}%`,
                  backgroundColor: item.color,
                }}
              />
            </div>
          </div>
        );
      })}
    </div>
  );
}
