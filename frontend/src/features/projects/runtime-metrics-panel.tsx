'use client';

import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { api } from '@/lib/api/client';
import {
  ProjectMetricsHistoryResponse,
  ProjectRuntimeMetrics,
  ServiceMetricsSample,
  ServiceRuntimeMetrics,
} from '@/lib/api/types';
import { Badge } from '@/components/ui/badge';
import {
  AreaLineChart,
  HorizontalBarChart,
  RadialGauge,
  StackedDistributionBar,
} from '@/components/ui/charts';
import { cn } from '@/lib/utils/cn';
import { formatBytes, formatUptime } from '@/lib/utils/format';
export { formatBytes, formatUptime };
import {
  Activity,
  Cpu,
  HardDrive,
  HeartPulse,
  Network,
  RefreshCw,
  AlertTriangle,
  Clock,
  RotateCcw,
  TrendingUp,
} from 'lucide-react';

interface RuntimeMetricsPanelProps {
  projectId: string;
}

interface LiveAggregatePoint {
  timestamp: string;
  label: string;
  cpuPercent: number;
  memoryBytes: number;
  memoryPercent: number;
  rxBytes: number;
  txBytes: number;
}

interface LiveServicePoint {
  timestamp: string;
  label: string;
  cpuPercent: number;
  memoryPercent: number;
  memoryBytes: number;
  rxBytes: number;
  txBytes: number;
}

function getHealthBadgeVariant(
  health: string,
  running: boolean
): 'success' | 'warning' | 'danger' | 'info' | 'neutral' {
  if (!running) return 'neutral';
  switch (health) {
    case 'healthy':
      return 'success';
    case 'starting':
      return 'warning';
    case 'unhealthy':
      return 'danger';
    default:
      return 'info';
  }
}

function formatTimeLabel(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '';
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
}

export function RuntimeMetricsPanel({ projectId }: RuntimeMetricsPanelProps) {
  const [metrics, setMetrics] = useState<ProjectRuntimeMetrics | null>(null);
  const [history, setHistory] = useState<ProjectMetricsHistoryResponse | null>(null);
  const [historyHours, setHistoryHours] = useState<1 | 6 | 24>(1);
  const [historyMetricView, setHistoryMetricView] = useState<'compute' | 'network'>('compute');
  const [historyError, setHistoryError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [autoRefresh, setAutoRefresh] = useState(true);
  const [liveAggSeries, setLiveAggSeries] = useState<LiveAggregatePoint[]>([]);
  const [liveServiceSeries, setLiveServiceSeries] = useState<Record<string, LiveServicePoint[]>>({});

  const appendLiveSnapshot = useCallback((data: ProjectRuntimeMetrics) => {
    const ts = data.collected_at || new Date().toISOString();
    const label = formatTimeLabel(ts);
    const totalMemLimit = data.services.reduce((sum, s) => {
      const limit =
        s.memory_limit_bytes > 0
          ? s.memory_limit_bytes
          : (s.memory_limit_mb || 1024) * 1024 * 1024;
      return sum + limit;
    }, 0);
    const aggMemPct =
      totalMemLimit > 0 ? Math.min(100, (data.total_memory_used_bytes / totalMemLimit) * 100) : 0;

    setLiveAggSeries((prev) => {
      if (prev.length > 0 && prev[prev.length - 1].timestamp === ts && !data.cached) {
        return prev;
      }
      const nextPoint: LiveAggregatePoint = {
        timestamp: ts,
        label,
        cpuPercent: data.total_cpu_percent,
        memoryBytes: data.total_memory_used_bytes,
        memoryPercent: aggMemPct,
        rxBytes: data.total_network_rx_bytes,
        txBytes: data.total_network_tx_bytes,
      };
      return [...prev.slice(-29), nextPoint];
    });

    setLiveServiceSeries((prev) => {
      const next = { ...prev };
      for (const svc of data.services || []) {
        const existing = next[svc.service_id] || [];
        const pt: LiveServicePoint = {
          timestamp: svc.collected_at || ts,
          label: formatTimeLabel(svc.collected_at || ts),
          cpuPercent: svc.cpu_percent,
          memoryPercent: svc.memory_percent,
          memoryBytes: svc.memory_used_bytes,
          rxBytes: svc.network_rx_bytes,
          txBytes: svc.network_tx_bytes,
        };
        if (existing.length > 0 && existing[existing.length - 1].timestamp === pt.timestamp) {
          continue;
        }
        next[svc.service_id] = [...existing.slice(-29), pt];
      }
      return next;
    });
  }, []);

  const loadMetrics = useCallback(
    async (forceRefresh = false) => {
      try {
        setError(null);
        const data = await api.projects.getMetrics(
          projectId,
          forceRefresh ? { refresh: true } : undefined
        );
        setMetrics(data);
        appendLiveSnapshot(data);
      } catch (err: any) {
        setError(err?.message || 'Failed to load runtime metrics');
      } finally {
        setLoading(false);
        setRefreshing(false);
      }
    },
    [projectId, appendLiveSnapshot]
  );

  const loadHistory = useCallback(
    async (hours: number) => {
      try {
        setHistoryError(null);
        const hist = await api.observability.getMetricsHistory(projectId, {
          hours,
          limit: 240,
        });
        setHistory(hist);
      } catch (err: any) {
        setHistoryError(err?.message || 'Historical metrics unavailable');
      }
    },
    [projectId]
  );

  useEffect(() => {
    setLiveAggSeries([]);
    setLiveServiceSeries({});
  }, [projectId]);

  useEffect(() => {
    setLoading(true);
    loadMetrics(false);
    loadHistory(historyHours);
  }, [loadMetrics, loadHistory, historyHours]);

  useEffect(() => {
    if (!autoRefresh) return;
    const id = setInterval(() => {
      loadMetrics(false);
      loadHistory(historyHours);
    }, 5000);
    return () => clearInterval(id);
  }, [autoRefresh, loadMetrics, loadHistory, historyHours]);

  const handleManualRefresh = () => {
    setRefreshing(true);
    loadMetrics(true);
    loadHistory(historyHours);
  };

  const services: ServiceRuntimeMetrics[] = metrics?.services || [];

  const samplesByService = useMemo(() => {
    const map = new Map<string, ServiceMetricsSample[]>();
    for (const sample of history?.samples || []) {
      const existing = map.get(sample.service_id);
      if (existing) {
        existing.push(sample);
      } else {
        map.set(sample.service_id, [sample]);
      }
    }
    return map;
  }, [history?.samples]);

  // Combine historical bucketed timeline + live rolling buffer for project-wide charts
  const projectTimeline = useMemo(() => {
    const buckets = new Map<
      string,
      { ts: string; cpu: number; memSum: number; memLimitSum: number; rx: number; tx: number }
    >();
    for (const s of history?.samples || []) {
      const key = s.sampled_at.slice(0, 19);
      const cur = buckets.get(key) || {
        ts: s.sampled_at,
        cpu: 0,
        memSum: 0,
        memLimitSum: 0,
        rx: 0,
        tx: 0,
      };
      cur.cpu += s.cpu_percent;
      cur.memSum += s.memory_used_bytes;
      cur.memLimitSum += s.memory_limit_bytes > 0 ? s.memory_limit_bytes : 1024 * 1024 * 1024;
      cur.rx += s.network_rx_bytes;
      cur.tx += s.network_tx_bytes;
      buckets.set(key, cur);
    }

    const fromHistory = Array.from(buckets.values())
      .sort((a, b) => a.ts.localeCompare(b.ts))
      .slice(-36)
      .map((b) => ({
        label: formatTimeLabel(b.ts),
        cpuPercent: b.cpu,
        memoryMB: b.memSum / (1024 * 1024),
        rxBytes: b.rx,
        txBytes: b.tx,
      }));

    if (fromHistory.length >= 2) {
      return fromHistory;
    }

    return liveAggSeries.map((pt) => ({
      label: pt.label,
      cpuPercent: pt.cpuPercent,
      memoryMB: pt.memoryBytes / (1024 * 1024),
      rxBytes: pt.rxBytes,
      txBytes: pt.txBytes,
    }));
  }, [history?.samples, liveAggSeries]);

  const aggregateLimits = useMemo(() => {
    const totalCpuLimitPct = Math.max(
      100,
      services.reduce((sum, s) => sum + ((s.cpu_limit_millicores || 1000) / 10), 0)
    );
    const totalMemLimitBytes = services.reduce((sum, s) => {
      const limit =
        s.memory_limit_bytes > 0
          ? s.memory_limit_bytes
          : (s.memory_limit_mb || 1024) * 1024 * 1024;
      return sum + limit;
    }, 0);
    const aggMemPct =
      totalMemLimitBytes > 0 && metrics
        ? Math.min(100, (metrics.total_memory_used_bytes / totalMemLimitBytes) * 100)
        : 0;
    const aggCpuPctOfQuota =
      totalCpuLimitPct > 0 && metrics
        ? Math.min(100, (metrics.total_cpu_percent / totalCpuLimitPct) * 100)
        : 0;
    return {
      totalCpuLimitPct,
      totalMemLimitBytes,
      aggMemPct,
      aggCpuPctOfQuota,
    };
  }, [services, metrics]);

  if (loading && !metrics) {
    return (
      <div className="flex-1 flex flex-col items-center justify-center p-8 space-y-3">
        <RefreshCw className="w-6 h-6 animate-spin text-neutral-400" />
        <p className="text-sm text-neutral-400">
          Collecting live container CPU, memory &amp; network telemetry...
        </p>
      </div>
    );
  }

  return (
    <div className="flex-1 flex flex-col min-h-0 space-y-5 overflow-y-auto pr-1">
      {/* Header Controls */}
      <div className="flex flex-wrap items-center justify-between gap-3 bg-surface-card border border-surface-border rounded-lg p-4">
        <div className="flex items-center gap-3">
          <div className="p-2 rounded-md bg-surface-elevated border border-surface-border">
            <Activity className="w-5 h-5 text-emerald-400" />
          </div>
          <div>
            <div className="flex items-center gap-2">
              <h3 className="text-sm font-semibold text-white">Runtime Telemetry &amp; Health</h3>
              {metrics && (
                <Badge variant={metrics.cached ? 'neutral' : 'success'}>
                  {metrics.cached
                    ? `Cached (${Math.max(0, Math.round(metrics.cache_age_seconds ?? 0))}s)`
                    : 'Live'}
                </Badge>
              )}
            </div>
            <p className="text-xs text-neutral-400">
              {metrics?.docker_available
                ? `Visual telemetry graphs updated ${metrics?.collected_at ? new Date(metrics.collected_at).toLocaleTimeString() : 'just now'}`
                : 'Docker daemon telemetry unavailable'}
            </p>
          </div>
        </div>

        <div className="flex items-center gap-2">
          <button
            type="button"
            onClick={() => setAutoRefresh((v) => !v)}
            className={cn(
              'px-2.5 py-1.5 rounded text-xs font-mono border transition-colors',
              autoRefresh
                ? 'bg-emerald-500/15 text-emerald-300 border-emerald-500/30'
                : 'bg-surface-elevated text-neutral-400 border-surface-border hover:text-white'
            )}
          >
            Auto-refresh: {autoRefresh ? '5s' : 'Paused'}
          </button>
          <button
            type="button"
            onClick={handleManualRefresh}
            disabled={loading || refreshing}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded bg-surface-elevated hover:bg-surface-border text-xs text-neutral-300 hover:text-white transition-colors border border-surface-border"
          >
            <RefreshCw className={cn('w-3.5 h-3.5', (loading || refreshing) && 'animate-spin')} />
            <span>Refresh</span>
          </button>
        </div>
      </div>

      {error && (
        <div className="p-3 rounded-lg bg-rose-500/10 border border-rose-500/30 text-xs text-rose-300 flex items-center gap-2">
          <AlertTriangle className="w-4 h-4 shrink-0" />
          <span>{error}</span>
        </div>
      )}

      {/* Aggregate Telemetry Graphs (Graphs prioritized over plain numeric cards) */}
      {metrics && (
        <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
          {/* Compute Overview Graph: CPU & Memory Live Gauges + Time-Series Graph */}
          <div className="rounded-lg border border-surface-border bg-surface p-4 space-y-3.5">
            <div className="flex items-center justify-between gap-2">
              <div className="flex items-center gap-2 text-xs font-semibold text-white">
                <Cpu className="w-4 h-4 text-blue-400" />
                <span>Aggregate Compute Utilization</span>
              </div>
              <span className="text-[11px] font-mono text-neutral-400">
                CPU {metrics.total_cpu_percent.toFixed(1)}% · Mem {formatBytes(metrics.total_memory_used_bytes)}
              </span>
            </div>

            <div className="grid grid-cols-2 gap-3 items-center py-1 px-2 rounded-md bg-surface-elevated/40 border border-surface-border/60">
              <RadialGauge
                value={aggregateLimits.aggCpuPctOfQuota}
                max={100}
                size={96}
                label="CPU Quota"
                valueLabel={`${metrics.total_cpu_percent.toFixed(1)}%`}
                sublabel={`of ${aggregateLimits.totalCpuLimitPct.toFixed(0)}% cap`}
                color="#3b82f6"
              />
              <RadialGauge
                value={aggregateLimits.aggMemPct}
                max={100}
                size={96}
                label="Memory"
                valueLabel={`${aggregateLimits.aggMemPct.toFixed(0)}%`}
                sublabel={`${formatBytes(metrics.total_memory_used_bytes)} / ${formatBytes(aggregateLimits.totalMemLimitBytes)}`}
              />
            </div>

            <AreaLineChart
              height={150}
              labels={projectTimeline.map((p) => p.label)}
              series={[
                {
                  id: 'cpu',
                  name: 'CPU %',
                  color: '#3b82f6',
                  data: projectTimeline.map((p) => p.cpuPercent),
                },
                {
                  id: 'mem',
                  name: 'Memory MB',
                  color: '#10b981',
                  data: projectTimeline.map((p) => p.memoryMB),
                },
              ]}
              formatY={(v) => v.toFixed(1)}
              emptyMessage="Collecting aggregate compute samples..."
              ariaLabel="Aggregate CPU and Memory utilization graph"
            />
          </div>

          {/* Network I/O & Service Resource Comparison Graphs */}
          <div className="rounded-lg border border-surface-border bg-surface p-4 space-y-3.5">
            <div className="flex items-center justify-between gap-2">
              <div className="flex items-center gap-2 text-xs font-semibold text-white">
                <Network className="w-4 h-4 text-indigo-400" />
                <span>Network Traffic &amp; Service Distribution</span>
              </div>
              <span className="text-[11px] font-mono text-neutral-400">
                Total {formatBytes(metrics.total_network_rx_bytes + metrics.total_network_tx_bytes)}
              </span>
            </div>

            <div className="p-3 rounded-md bg-surface-elevated/40 border border-surface-border/60 space-y-2">
              <div className="flex items-center justify-between text-[11px] font-mono text-neutral-400">
                <span>Ingress vs Egress Share</span>
                <span className="text-neutral-300">
                  Rx {formatBytes(metrics.total_network_rx_bytes)} · Tx {formatBytes(metrics.total_network_tx_bytes)}
                </span>
              </div>
              <StackedDistributionBar
                height={11}
                formatValue={(v) => formatBytes(v)}
                segments={[
                  {
                    id: 'rx',
                    label: 'Ingress (Rx)',
                    value: metrics.total_network_rx_bytes,
                    color: '#6366f1',
                  },
                  {
                    id: 'tx',
                    label: 'Egress (Tx)',
                    value: metrics.total_network_tx_bytes,
                    color: '#a855f7',
                  },
                ]}
              />
            </div>

            <AreaLineChart
              height={150}
              labels={projectTimeline.map((p) => p.label)}
              series={[
                {
                  id: 'rx',
                  name: 'Ingress (Rx)',
                  color: '#6366f1',
                  data: projectTimeline.map((p) => p.rxBytes),
                },
                {
                  id: 'tx',
                  name: 'Egress (Tx)',
                  color: '#a855f7',
                  data: projectTimeline.map((p) => p.txBytes),
                },
              ]}
              formatY={(v) => formatBytes(v)}
              emptyMessage="Collecting network I/O samples..."
              ariaLabel="Aggregate Network Ingress and Egress graph"
            />
          </div>
        </div>
      )}

      {/* Comparative Service Utilization Bar Graph */}
      {services.length > 0 && (
        <div className="rounded-lg border border-surface-border bg-surface p-4 space-y-3">
          <div className="flex items-center justify-between gap-2">
            <div className="flex items-center gap-2 text-xs font-semibold text-white">
              <HardDrive className="w-4 h-4 text-emerald-400" />
              <span>Per-Service Resource Load Comparison</span>
            </div>
            <span className="text-[11px] font-mono text-neutral-500">
              Memory % vs CPU load across {services.length} service{services.length === 1 ? '' : 's'}
            </span>
          </div>

          <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
            <div className="p-3 rounded-md bg-surface-elevated/35 border border-surface-border/60 space-y-2">
              <span className="text-[10px] font-mono uppercase tracking-wider text-neutral-400 block">
                CPU Load by Service
              </span>
              <HorizontalBarChart
                items={services.map((s) => ({
                  id: `${s.service_id}-cpu`,
                  label: s.service_name,
                  sublabel: s.running ? undefined : s.status,
                  value: s.cpu_percent,
                  maxValue: Math.max(100, (s.cpu_limit_millicores || 1000) / 10),
                  valueLabel: `${s.cpu_percent.toFixed(1)}% / ${((s.cpu_limit_millicores || 1000) / 10).toFixed(0)}%`,
                  color: s.cpu_percent >= 80 ? '#f43f5e' : '#3b82f6',
                }))}
              />
            </div>

            <div className="p-3 rounded-md bg-surface-elevated/35 border border-surface-border/60 space-y-2">
              <span className="text-[10px] font-mono uppercase tracking-wider text-neutral-400 block">
                Memory Saturation by Service
              </span>
              <HorizontalBarChart
                items={services.map((s) => ({
                  id: `${s.service_id}-mem`,
                  label: s.service_name,
                  sublabel: s.running ? undefined : s.status,
                  value: s.memory_percent,
                  maxValue: 100,
                  valueLabel: `${s.memory_percent.toFixed(1)}% (${formatBytes(s.memory_used_bytes)})`,
                  color:
                    s.memory_percent >= 85
                      ? '#f43f5e'
                      : s.memory_percent >= 70
                      ? '#f59e0b'
                      : '#10b981',
                }))}
              />
            </div>
          </div>
        </div>
      )}

      {/* Per-Service Visual Telemetry Cards */}
      <div className="space-y-3">
        {services.length === 0 ? (
          <div className="p-6 rounded-lg bg-surface-card border border-surface-border text-center text-xs text-neutral-400">
            No services found for this project.
          </div>
        ) : (
          services.map((svc) => {
            const histSamples = samplesByService.get(svc.service_id) || [];
            const liveSamples = liveServiceSeries[svc.service_id] || [];
            const useHist = histSamples.length >= 2;
            const trendLabels = useHist
              ? histSamples.slice(-24).map((s) => formatTimeLabel(s.sampled_at))
              : liveSamples.map((s) => s.label);
            const cpuTrend = useHist
              ? histSamples.slice(-24).map((s) => s.cpu_percent)
              : liveSamples.map((s) => s.cpuPercent);
            const memTrend = useHist
              ? histSamples.slice(-24).map((s) => s.memory_percent)
              : liveSamples.map((s) => s.memoryPercent);

            const cpuMaxPct = Math.max(100, (svc.cpu_limit_millicores || 1000) / 10);
            const pidsLimit = 256;
            const pidsPct = Math.min(100, (svc.pids_current / pidsLimit) * 100);

            return (
              <div
                key={svc.service_id}
                className="rounded-lg border border-surface-border bg-surface-elevated/40 p-4 space-y-4"
              >
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <div className="flex items-center gap-2 min-w-0">
                    <span className="text-sm font-semibold text-white truncate">
                      {svc.service_name}
                    </span>
                    <span className="px-1.5 py-0.5 rounded bg-surface border border-surface-border text-[10px] font-mono uppercase text-neutral-400">
                      {svc.role || svc.classification}
                    </span>
                    <Badge status={svc.status} />
                    {svc.oom_killed && <Badge variant="danger">OOM Killed</Badge>}
                  </div>

                  <div className="flex items-center gap-2 text-xs font-mono">
                    <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded bg-surface border border-surface-border text-[11px] text-neutral-300">
                      <Clock className="w-3 h-3 text-neutral-400" />
                      {formatUptime(svc.uptime_seconds)}
                    </span>
                    <span
                      className={cn(
                        'inline-flex items-center gap-1 px-2 py-0.5 rounded border text-[11px]',
                        svc.restart_count > 0
                          ? 'bg-amber-500/10 border-amber-500/30 text-amber-300'
                          : 'bg-surface border-surface-border text-neutral-400'
                      )}
                    >
                      <RotateCcw className="w-3 h-3" />
                      {svc.restart_count} restarts
                    </span>
                    <Badge variant={getHealthBadgeVariant(svc.health_status, svc.running)}>
                      <span className="inline-flex items-center gap-1">
                        <HeartPulse className="w-3 h-3" />
                        <span>Docker: {svc.running ? svc.health_status || 'none' : 'stopped'}</span>
                      </span>
                    </Badge>
                    {svc.health_failing_streak > 0 && (
                      <span className="text-[11px] text-rose-400">
                        Streak: {svc.health_failing_streak}
                      </span>
                    )}
                  </div>
                </div>

                {svc.metric_error && (
                  <div className="px-2.5 py-1.5 rounded bg-amber-500/10 border border-amber-500/20 text-[11px] font-mono text-amber-300">
                    Telemetry note: {svc.metric_error}
                  </div>
                )}

                {/* Visual Gauges + Live Trend Charts + Proportional Bars */}
                <div className="grid grid-cols-1 lg:grid-cols-12 gap-3 items-stretch">
                  {/* CPU & Memory Radial Gauges */}
                  <div className="lg:col-span-4 grid grid-cols-2 gap-2.5 p-3 rounded-md bg-surface/70 border border-surface-border/60 items-center">
                    <RadialGauge
                      value={(svc.cpu_percent / cpuMaxPct) * 100}
                      max={100}
                      size={88}
                      label="CPU"
                      valueLabel={`${svc.cpu_percent.toFixed(1)}%`}
                      sublabel={`Cap ${svc.cpu_limit_millicores ? `${svc.cpu_limit_millicores}m` : '1000m'}`}
                      color="#3b82f6"
                    />
                    <RadialGauge
                      value={svc.memory_percent}
                      max={100}
                      size={88}
                      label="Memory"
                      valueLabel={`${svc.memory_percent.toFixed(0)}%`}
                      sublabel={`${formatBytes(svc.memory_used_bytes)} / ${
                        svc.memory_limit_bytes > 0
                          ? formatBytes(svc.memory_limit_bytes)
                          : `${svc.memory_limit_mb || 1024} MB`
                      }`}
                    />
                  </div>

                  {/* Live CPU & Memory Trend Area Chart */}
                  <div className="lg:col-span-5 p-3 rounded-md bg-surface/70 border border-surface-border/60 space-y-1.5">
                    <div className="flex items-center justify-between text-[10px] font-mono uppercase tracking-wider text-neutral-400">
                      <span>CPU &amp; Memory Trend</span>
                      <span className="text-neutral-500">{cpuTrend.length} pts</span>
                    </div>
                    <AreaLineChart
                      height={115}
                      maxY={100}
                      labels={trendLabels}
                      series={[
                        { id: 'cpu', name: 'CPU %', color: '#3b82f6', data: cpuTrend },
                        { id: 'mem', name: 'Mem %', color: '#10b981', data: memTrend },
                      ]}
                      formatY={(v) => `${v.toFixed(0)}%`}
                      emptyMessage="Awaiting live telemetry samples..."
                      ariaLabel={`${svc.service_name} live CPU and Memory trend`}
                    />
                  </div>

                  {/* Network I/O & Process Saturation Bars */}
                  <div className="lg:col-span-3 p-3 rounded-md bg-surface/70 border border-surface-border/60 flex flex-col justify-between space-y-3">
                    <div className="space-y-1.5">
                      <div className="flex items-center justify-between text-[10px] font-mono uppercase tracking-wider text-neutral-400">
                        <span>Network Rx / Tx</span>
                        <span className="text-neutral-300">
                          {formatBytes(svc.network_rx_bytes + svc.network_tx_bytes)}
                        </span>
                      </div>
                      <StackedDistributionBar
                        height={8}
                        formatValue={(v) => formatBytes(v)}
                        segments={[
                          { id: 'rx', label: 'Rx', value: svc.network_rx_bytes, color: '#6366f1' },
                          { id: 'tx', label: 'Tx', value: svc.network_tx_bytes, color: '#a855f7' },
                        ]}
                      />
                    </div>

                    <div className="space-y-1.5 pt-2 border-t border-surface-border/40">
                      <div className="flex items-center justify-between text-[10px] font-mono uppercase tracking-wider text-neutral-400">
                        <span>Active PIDs</span>
                        <span className="text-neutral-300">{svc.pids_current}</span>
                      </div>
                      <div className="h-2 w-full rounded-full bg-surface-border/60 overflow-hidden">
                        <div
                          className="h-full rounded-full bg-amber-400 transition-all duration-300"
                          style={{ width: `${Math.max(svc.pids_current > 0 ? 4 : 0, pidsPct)}%` }}
                        />
                      </div>
                    </div>
                  </div>
                </div>

                {svc.last_health_output && (
                  <div
                    className="p-2 rounded bg-black/30 border border-surface-border/50 text-[11px] font-mono text-neutral-300 truncate"
                    title={svc.last_health_output}
                  >
                    <span className="text-neutral-500 mr-1.5">Last health probe:</span>
                    {svc.last_health_output}
                  </div>
                )}
              </div>
            );
          })
        )}
      </div>

      {/* Historical Telemetry Graphs Section */}
      <div className="rounded-lg border border-surface-border bg-surface-card p-4 space-y-4">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="flex items-center gap-2">
            <TrendingUp className="w-4 h-4 text-blue-400" />
            <h4 className="text-xs font-semibold text-white uppercase tracking-wider">
              Historical Telemetry Graphs
            </h4>
            {history && (
              <span className="text-[11px] font-mono text-neutral-400">
                ({history.retention_days}d retention &middot; {history.sample_interval_seconds}s cadence &middot; {history.total_samples} samples)
              </span>
            )}
          </div>

          <div className="flex flex-wrap items-center gap-2 text-xs font-mono">
            <div className="inline-flex rounded border border-surface-border bg-surface p-0.5">
              <button
                type="button"
                onClick={() => setHistoryMetricView('compute')}
                className={cn(
                  'px-2 py-1 rounded text-[11px] transition-colors',
                  historyMetricView === 'compute'
                    ? 'bg-surface-elevated text-white font-semibold'
                    : 'text-neutral-400 hover:text-white'
                )}
              >
                CPU &amp; Memory
              </button>
              <button
                type="button"
                onClick={() => setHistoryMetricView('network')}
                className={cn(
                  'px-2 py-1 rounded text-[11px] transition-colors',
                  historyMetricView === 'network'
                    ? 'bg-surface-elevated text-white font-semibold'
                    : 'text-neutral-400 hover:text-white'
                )}
              >
                Network I/O
              </button>
            </div>

            <div className="flex items-center gap-1">
              {([1, 6, 24] as const).map((h) => (
                <button
                  key={h}
                  type="button"
                  onClick={() => setHistoryHours(h)}
                  className={cn(
                    'px-2.5 py-1 rounded border transition-colors',
                    historyHours === h
                      ? 'bg-white text-black border-white font-semibold'
                      : 'bg-surface-elevated text-neutral-400 border-surface-border hover:text-white'
                  )}
                >
                  {h}h
                </button>
              ))}
            </div>
          </div>
        </div>

        {historyError && (
          <div className="p-2.5 rounded bg-amber-500/10 border border-amber-500/20 text-[11px] font-mono text-amber-300">
            {historyError}
          </div>
        )}

        {!history || history.summaries.length === 0 ? (
          <div className="p-4 rounded border border-surface-border/60 bg-surface/40 text-center text-xs text-neutral-400">
            Background metrics sampler records periodic CPU, memory, network, and restart snapshots here once services are running.
          </div>
        ) : (
          <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
            {history.summaries.map((summary) => {
              const svcSamples = samplesByService.get(summary.service_id) || [];
              const labels = svcSamples.map((s) => formatTimeLabel(s.sampled_at));
              const cpuSeries = svcSamples.map((s) => s.cpu_percent);
              const memSeries = svcSamples.map((s) => s.memory_percent);
              const rxSeries = svcSamples.map((s) => s.network_rx_bytes);
              const txSeries = svcSamples.map((s) => s.network_tx_bytes);

              return (
                <div
                  key={summary.service_id}
                  className="p-3.5 rounded-lg border border-surface-border bg-surface-elevated/40 space-y-3"
                >
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <span className="text-xs font-semibold text-white truncate">
                      {summary.service_name}
                    </span>
                    <div className="flex flex-wrap items-center gap-1.5 text-[10px] font-mono">
                      <span className="px-1.5 py-0.5 rounded bg-surface border border-surface-border text-neutral-300">
                        Avg {summary.avg_cpu_percent.toFixed(1)}% · Peak {summary.peak_cpu_percent.toFixed(1)}%
                      </span>
                      <span className="px-1.5 py-0.5 rounded bg-surface border border-surface-border text-neutral-300">
                        Peak Mem {formatBytes(summary.peak_memory_bytes)}
                      </span>
                      {summary.max_restart_count > 0 && (
                        <Badge variant="warning">{summary.max_restart_count} restarts</Badge>
                      )}
                      {summary.oom_count > 0 && (
                        <Badge variant="danger">{summary.oom_count} OOM</Badge>
                      )}
                    </div>
                  </div>

                  {historyMetricView === 'compute' ? (
                    <AreaLineChart
                      height={165}
                      maxY={Math.max(100, summary.peak_cpu_percent)}
                      labels={labels}
                      series={[
                        { id: 'cpu', name: 'CPU %', color: '#3b82f6', data: cpuSeries },
                        { id: 'mem', name: 'Memory %', color: '#10b981', data: memSeries },
                      ]}
                      referenceLine={{
                        value: summary.peak_cpu_percent,
                        label: `Peak CPU ${summary.peak_cpu_percent.toFixed(1)}%`,
                        color: '#f59e0b',
                      }}
                      formatY={(v) => `${v.toFixed(0)}%`}
                      ariaLabel={`${summary.service_name} historical CPU and Memory chart`}
                    />
                  ) : (
                    <AreaLineChart
                      height={165}
                      labels={labels}
                      series={[
                        { id: 'rx', name: 'Rx (Ingress)', color: '#6366f1', data: rxSeries },
                        { id: 'tx', name: 'Tx (Egress)', color: '#a855f7', data: txSeries },
                      ]}
                      formatY={(v) => formatBytes(v)}
                      ariaLabel={`${summary.service_name} historical network chart`}
                    />
                  )}
                </div>
              );
            })}
          </div>
        )}
      </div>
    </div>
  );
}
