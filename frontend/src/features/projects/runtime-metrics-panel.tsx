'use client';

import React, { useCallback, useEffect, useState } from 'react';
import { api } from '@/lib/api/client';
import {
  ProjectMetricsHistoryResponse,
  ProjectRuntimeMetrics,
  ServiceMetricsSample,
  ServiceRuntimeMetrics,
} from '@/lib/api/types';
import { Badge } from '@/components/ui/badge';
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

function getHealthBadgeVariant(health: string, running: boolean): 'success' | 'warning' | 'danger' | 'info' | 'neutral' {
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

function MetricSparkline({
  values,
  colorClass,
  maxScale = 100,
  label,
}: {
  values: number[];
  colorClass: string;
  maxScale?: number;
  label: string;
}) {
  const width = 180;
  const height = 40;
  if (values.length === 0) {
    return (
      <div className="h-10 flex items-center justify-center text-[10px] font-mono text-neutral-500 border border-surface-border/40 rounded bg-black/20">
        No {label} samples yet
      </div>
    );
  }

  const peak = Math.max(maxScale, ...values, 1);
  const points =
    values.length === 1
      ? `0,${height - Math.min(1, Math.max(0, values[0] / peak)) * (height - 6) - 3} ${width},${height - Math.min(1, Math.max(0, values[0] / peak)) * (height - 6) - 3}`
      : values
          .map((val, idx) => {
            const x = (idx / (values.length - 1)) * width;
            const norm = Math.min(1, Math.max(0, val / peak));
            const y = height - norm * (height - 6) - 3;
            return `${x.toFixed(1)},${y.toFixed(1)}`;
          })
          .join(' ');

  return (
    <svg
      viewBox={`0 0 ${width} ${height}`}
      className={cn('w-full h-10 rounded border border-surface-border/40 bg-black/20 overflow-visible', colorClass)}
      role="img"
      aria-label={`${label} history sparkline`}
    >
      <polyline
        fill="none"
        stroke="currentColor"
        strokeWidth="1.75"
        strokeLinecap="round"
        strokeLinejoin="round"
        points={points}
      />
    </svg>
  );
}

export function RuntimeMetricsPanel({ projectId }: RuntimeMetricsPanelProps) {
  const [metrics, setMetrics] = useState<ProjectRuntimeMetrics | null>(null);
  const [history, setHistory] = useState<ProjectMetricsHistoryResponse | null>(null);
  const [historyHours, setHistoryHours] = useState<1 | 6 | 24>(1);
  const [historyError, setHistoryError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [autoRefresh, setAutoRefresh] = useState(true);

  const loadMetrics = useCallback(
    async (forceRefresh = false) => {
      try {
        setError(null);
        const data = await api.projects.getMetrics(
          projectId,
          forceRefresh ? { refresh: true } : undefined
        );
        setMetrics(data);
      } catch (err: any) {
        setError(err?.message || 'Failed to load runtime metrics');
      } finally {
        setLoading(false);
        setRefreshing(false);
      }
    },
    [projectId]
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

  if (loading && !metrics) {
    return (
      <div className="flex-1 flex flex-col items-center justify-center p-8 space-y-3">
        <RefreshCw className="w-6 h-6 animate-spin text-neutral-400" />
        <p className="text-sm text-neutral-400">Collecting live container CPU, memory & network telemetry...</p>
      </div>
    );
  }

  const services: ServiceRuntimeMetrics[] = metrics?.services || [];
  const samplesByService = new Map<string, ServiceMetricsSample[]>();
  for (const sample of history?.samples || []) {
    const existing = samplesByService.get(sample.service_id);
    if (existing) {
      existing.push(sample);
    } else {
      samplesByService.set(sample.service_id, [sample]);
    }
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
              <h3 className="text-sm font-semibold text-white">Runtime Telemetry & Health</h3>
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
                ? `Last sampled ${metrics?.collected_at ? new Date(metrics.collected_at).toLocaleTimeString() : 'just now'}`
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

      {/* Aggregate Resource Cards */}
      {metrics && (
        <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-3">
          <div className="rounded-lg border border-surface-border bg-surface p-3.5 space-y-1">
            <div className="flex items-center justify-between text-[11px] font-mono uppercase tracking-wider text-neutral-400">
              <span>Aggregate CPU</span>
              <Cpu className="w-3.5 h-3.5 text-blue-400" />
            </div>
            <div className="text-xl font-bold font-mono text-white">
              {metrics.total_cpu_percent.toFixed(2)}%
            </div>
          </div>

          <div className="rounded-lg border border-surface-border bg-surface p-3.5 space-y-1">
            <div className="flex items-center justify-between text-[11px] font-mono uppercase tracking-wider text-neutral-400">
              <span>Memory Used</span>
              <HardDrive className="w-3.5 h-3.5 text-emerald-400" />
            </div>
            <div className="text-xl font-bold font-mono text-white">
              {formatBytes(metrics.total_memory_used_bytes)}
            </div>
          </div>

          <div className="rounded-lg border border-surface-border bg-surface p-3.5 space-y-1">
            <div className="flex items-center justify-between text-[11px] font-mono uppercase tracking-wider text-neutral-400">
              <span>Network Ingress</span>
              <Network className="w-3.5 h-3.5 text-indigo-400" />
            </div>
            <div className="text-xl font-bold font-mono text-white">
              {formatBytes(metrics.total_network_rx_bytes)}
            </div>
          </div>

          <div className="rounded-lg border border-surface-border bg-surface p-3.5 space-y-1">
            <div className="flex items-center justify-between text-[11px] font-mono uppercase tracking-wider text-neutral-400">
              <span>Network Egress</span>
              <Network className="w-3.5 h-3.5 text-purple-400" />
            </div>
            <div className="text-xl font-bold font-mono text-white">
              {formatBytes(metrics.total_network_tx_bytes)}
            </div>
          </div>
        </div>
      )}

      {/* Per-Service Metrics Cards */}
      <div className="space-y-3">
        {services.length === 0 ? (
          <div className="p-6 rounded-lg bg-surface-card border border-surface-border text-center text-xs text-neutral-400">
            No services found for this project.
          </div>
        ) : (
          services.map((svc) => (
            <div
              key={svc.service_id}
              className="rounded-lg border border-surface-border bg-surface-elevated/40 p-4 space-y-3"
            >
              <div className="flex flex-wrap items-center justify-between gap-2">
                <div className="flex items-center gap-2 min-w-0">
                  <span className="text-sm font-semibold text-white truncate">{svc.service_name}</span>
                  <span className="px-1.5 py-0.5 rounded bg-surface border border-surface-border text-[10px] font-mono uppercase text-neutral-400">
                    {svc.role || svc.classification}
                  </span>
                  <Badge status={svc.status} />
                  {svc.oom_killed && (
                    <Badge variant="danger">OOM Killed</Badge>
                  )}
                </div>

                <div className="flex items-center gap-2 text-xs font-mono">
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

              <div className="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-6 gap-2.5 text-xs font-mono">
                <div className="p-2.5 rounded bg-surface/60 border border-surface-border/50 space-y-1">
                  <span className="text-[10px] text-neutral-500 block uppercase">CPU</span>
                  <span className="text-white font-semibold">{svc.cpu_percent.toFixed(2)}%</span>
                  <span className="text-[10px] text-neutral-500 block">
                    Limit: {svc.cpu_limit_millicores ? `${svc.cpu_limit_millicores}m` : '1000m'}
                  </span>
                </div>

                <div className="p-2.5 rounded bg-surface/60 border border-surface-border/50 space-y-1">
                  <span className="text-[10px] text-neutral-500 block uppercase">Memory</span>
                  <span className="text-white font-semibold">{formatBytes(svc.memory_used_bytes)}</span>
                  <span className="text-[10px] text-neutral-500 block">
                    {svc.memory_percent.toFixed(1)}% of{' '}
                    {svc.memory_limit_bytes > 0 ? formatBytes(svc.memory_limit_bytes) : `${svc.memory_limit_mb || 1024} MB`}
                  </span>
                </div>

                <div className="p-2.5 rounded bg-surface/60 border border-surface-border/50 space-y-1">
                  <span className="text-[10px] text-neutral-500 block uppercase">Network Rx</span>
                  <span className="text-white font-semibold">{formatBytes(svc.network_rx_bytes)}</span>
                </div>

                <div className="p-2.5 rounded bg-surface/60 border border-surface-border/50 space-y-1">
                  <span className="text-[10px] text-neutral-500 block uppercase">Network Tx</span>
                  <span className="text-white font-semibold">{formatBytes(svc.network_tx_bytes)}</span>
                </div>

                <div className="p-2.5 rounded bg-surface/60 border border-surface-border/50 space-y-1">
                  <span className="text-[10px] text-neutral-500 block uppercase">PIDs</span>
                  <span className="text-white font-semibold">{svc.pids_current}</span>
                </div>

                <div className="p-2.5 rounded bg-surface/60 border border-surface-border/50 space-y-1">
                  <span className="text-[10px] text-neutral-500 block uppercase">Uptime / Restarts</span>
                  <span className="text-white font-semibold inline-flex items-center gap-1">
                    <Clock className="w-3 h-3 text-neutral-400" />
                    {formatUptime(svc.uptime_seconds)}
                  </span>
                  <span className="text-[10px] text-neutral-500 flex items-center gap-1">
                    <RotateCcw className="w-2.5 h-2.5" />
                    {svc.restart_count} restarts
                  </span>
                </div>
              </div>

              {svc.last_health_output && (
                <div className="p-2 rounded bg-black/30 border border-surface-border/50 text-[11px] font-mono text-neutral-300 truncate" title={svc.last_health_output}>
                  <span className="text-neutral-500 mr-1.5">Last health probe:</span>
                  {svc.last_health_output}
                </div>
              )}
            </div>
          ))
        )}
      </div>

      {/* Historical Telemetry Section */}
      <div className="rounded-lg border border-surface-border bg-surface-card p-4 space-y-4">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="flex items-center gap-2">
            <TrendingUp className="w-4 h-4 text-blue-400" />
            <h4 className="text-xs font-semibold text-white uppercase tracking-wider">
              Historical Telemetry &amp; Restarts
            </h4>
            {history && (
              <span className="text-[11px] font-mono text-neutral-400">
                ({history.retention_days}d retention &middot; {history.sample_interval_seconds}s cadence &middot; {history.total_samples} samples)
              </span>
            )}
          </div>

          <div className="flex items-center gap-1 text-xs font-mono">
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

        {historyError && (
          <div className="p-2.5 rounded bg-amber-500/10 border border-amber-500/20 text-[11px] font-mono text-amber-300">
            {historyError}
          </div>
        )}

        {(!history || history.summaries.length === 0) ? (
          <div className="p-4 rounded border border-surface-border/60 bg-surface/40 text-center text-xs text-neutral-400">
            Background metrics sampler records periodic CPU, memory, network, and restart snapshots here once services are running.
          </div>
        ) : (
          <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
            {history.summaries.map((summary) => {
              const svcSamples = samplesByService.get(summary.service_id) || [];
              const cpuSeries = svcSamples.map((s) => s.cpu_percent);
              const memSeries = svcSamples.map((s) => s.memory_percent);

              return (
                <div
                  key={summary.service_id}
                  className="p-3.5 rounded-lg border border-surface-border bg-surface-elevated/40 space-y-3"
                >
                  <div className="flex items-center justify-between gap-2">
                    <span className="text-xs font-semibold text-white truncate">
                      {summary.service_name}
                    </span>
                    <div className="flex items-center gap-1.5 text-[10px] font-mono">
                      <span className="px-1.5 py-0.5 rounded bg-surface border border-surface-border text-neutral-300">
                        {summary.sample_count} samples
                      </span>
                      {summary.oom_count > 0 && (
                        <Badge variant="danger">{summary.oom_count} OOM</Badge>
                      )}
                    </div>
                  </div>

                  <div className="grid grid-cols-4 gap-2 text-[11px] font-mono">
                    <div className="p-2 rounded bg-surface/60 border border-surface-border/40">
                      <span className="text-[10px] text-neutral-500 block">Peak CPU</span>
                      <span className="text-white font-semibold">{summary.peak_cpu_percent.toFixed(1)}%</span>
                    </div>
                    <div className="p-2 rounded bg-surface/60 border border-surface-border/40">
                      <span className="text-[10px] text-neutral-500 block">Avg CPU</span>
                      <span className="text-white font-semibold">{summary.avg_cpu_percent.toFixed(1)}%</span>
                    </div>
                    <div className="p-2 rounded bg-surface/60 border border-surface-border/40">
                      <span className="text-[10px] text-neutral-500 block">Peak Mem</span>
                      <span className="text-white font-semibold">{formatBytes(summary.peak_memory_bytes)}</span>
                    </div>
                    <div className="p-2 rounded bg-surface/60 border border-surface-border/40">
                      <span className="text-[10px] text-neutral-500 block">Restarts</span>
                      <span className="text-white font-semibold">{summary.max_restart_count}</span>
                    </div>
                  </div>

                  <div className="grid grid-cols-2 gap-2.5">
                    <div className="space-y-1">
                      <span className="text-[10px] font-mono text-neutral-400 block">CPU %</span>
                      <MetricSparkline values={cpuSeries} colorClass="text-blue-400" maxScale={100} label="CPU" />
                    </div>
                    <div className="space-y-1">
                      <span className="text-[10px] font-mono text-neutral-400 block">Memory %</span>
                      <MetricSparkline values={memSeries} colorClass="text-emerald-400" maxScale={100} label="memory" />
                    </div>
                  </div>
                </div>
              );
            })}
          </div>
        )}
      </div>
    </div>
  );
}
