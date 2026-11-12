'use client';

import React, { useEffect, useState, useCallback } from 'react';
import { api } from '@/lib/api/client';
import {
  AuditLogEntry,
  DiagnosticsReport,
  ProjectTopology,
} from '@/lib/api/types';
import { cn } from '@/lib/utils/cn';
import { formatBytes } from '@/lib/utils/format';
import {
  HorizontalBarChart,
  RadialGauge,
  StackedDistributionBar,
} from '@/components/ui/charts';
import {
  Network,
  RefreshCw,
  AlertTriangle,
  AlertCircle,
  Info,
  CheckCircle2,
  Server,
  ArrowRight,
  ExternalLink,
  ShieldCheck,
  Layers,
  Activity,
  History,
  HardDrive,
} from 'lucide-react';

interface TopologySectionProps {
  projectId: string;
}

const AUDIT_RESOURCE_FILTERS: { id: string; label: string }[] = [
  { id: 'all', label: 'All' },
  { id: 'deployment', label: 'Releases' },
  { id: 'service_deployment', label: 'Deploys' },
  { id: 'service', label: 'Services' },
  { id: 'env_var', label: 'Secrets' },
  { id: 'domain', label: 'Domains' },
  { id: 'volume', label: 'Volumes' },
  { id: 'project', label: 'Project' },
];

export function TopologySection({ projectId }: TopologySectionProps) {
  const [topology, setTopology] = useState<ProjectTopology | null>(null);
  const [diagnosticsReport, setDiagnosticsReport] = useState<DiagnosticsReport | null>(null);
  const [auditLogs, setAuditLogs] = useState<AuditLogEntry[]>([]);
  const [auditFilter, setAuditFilter] = useState<string>('all');
  const [auditError, setAuditError] = useState<string | null>(null);
  const [diagError, setDiagError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [filterCategory, setFilterCategory] = useState<string>('all');

  const fetchTopology = useCallback(async () => {
    try {
      setLoading(true);
      setError(null);
      setDiagError(null);
      setAuditError(null);

      const [topoRes, diagRes, auditRes] = await Promise.allSettled([
        api.projects.getTopology(projectId),
        api.observability.getProjectDiagnostics(projectId),
        api.observability.getProjectAuditLogs(projectId, {
          resource_type: auditFilter === 'all' ? undefined : auditFilter,
          limit: 30,
        }),
      ]);

      if (topoRes.status === 'fulfilled') {
        setTopology(topoRes.value);
      } else {
        setError(topoRes.reason?.message || 'Failed to load project service topology');
      }

      if (diagRes.status === 'fulfilled') {
        setDiagnosticsReport(diagRes.value);
      } else {
        setDiagError(diagRes.reason?.message || 'Operational diagnostics unavailable');
      }

      if (auditRes.status === 'fulfilled') {
        setAuditLogs(auditRes.value.logs || []);
      } else {
        setAuditError(auditRes.reason?.message || 'Operational audit logs unavailable');
      }
    } finally {
      setLoading(false);
    }
  }, [projectId, auditFilter]);

  useEffect(() => {
    fetchTopology();
  }, [fetchTopology]);

  if (loading && !topology) {
    return (
      <div className="flex-1 flex flex-col items-center justify-center p-8 space-y-3">
        <RefreshCw className="w-6 h-6 animate-spin text-neutral-400" />
        <p className="text-sm text-neutral-400">Loading service topology & connection diagnostics...</p>
      </div>
    );
  }

  if (error && !topology) {
    return (
      <div className="p-6 rounded-lg bg-surface-card border border-rose-500/20 text-center space-y-3">
        <AlertCircle className="w-8 h-8 text-rose-400 mx-auto" />
        <p className="text-sm text-rose-300 font-medium">{error}</p>
        <button
          type="button"
          onClick={fetchTopology}
          className="px-3 py-1.5 rounded bg-surface-elevated text-xs text-white hover:bg-surface-border transition-colors"
        >
          Retry
        </button>
      </div>
    );
  }

  const services = topology?.services || [];
  const tiers = topology?.tiers || [];
  const diagnostics = topology?.diagnostics || [];

  const errorCount = diagnostics.filter((d) => d.severity === 'error').length;
  const warningCount = diagnostics.filter((d) => d.severity === 'warning').length;
  const infoCount = diagnostics.filter((d) => d.severity === 'info').length;

  const filteredDiagnostics =
    filterCategory === 'all'
      ? diagnostics
      : diagnostics.filter((d) => d.category === filterCategory);

  return (
    <div className="flex-1 flex flex-col min-h-0 space-y-6 overflow-y-auto pr-1">
      {/* Topology Header Controls */}
      <div className="flex items-center justify-between bg-surface-card border border-surface-border rounded-lg p-4">
        <div className="flex items-center gap-3">
          <div className="p-2 rounded-md bg-surface-elevated border border-surface-border">
            <Network className="w-5 h-5 text-indigo-400" />
          </div>
          <div>
            <h3 className="text-sm font-semibold text-white flex items-center gap-2">
              Service Topology & Network Diagnostics
            </h3>
            <p className="text-xs text-neutral-400">
              Private Docker network:{' '}
              <span className="font-mono text-neutral-300">{topology?.network_name}</span>
            </p>
          </div>
        </div>

        <div className="flex items-center gap-2">
          <button
            type="button"
            onClick={fetchTopology}
            disabled={loading}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded bg-surface-elevated hover:bg-surface-border text-xs text-neutral-300 hover:text-white transition-colors border border-surface-border"
          >
            <RefreshCw className={cn('w-3.5 h-3.5', loading && 'animate-spin')} />
            <span>Refresh</span>
          </button>
        </div>
      </div>

      {/* Actionable Diagnostics Card */}
      <div className="bg-surface-card border border-surface-border rounded-lg p-4 space-y-4">
        <div className="flex items-center justify-between flex-wrap gap-2">
          <div className="flex items-center gap-2">
            <ShieldCheck className="w-4 h-4 text-emerald-400" />
            <h4 className="text-xs font-semibold text-white uppercase tracking-wider">
              Connection Diagnostics
            </h4>
            <div className="flex items-center gap-1.5 text-xs font-mono ml-2">
              {errorCount > 0 && (
                <span className="px-2 py-0.5 rounded-full bg-rose-500/20 text-rose-400 border border-rose-500/30">
                  {errorCount} {errorCount === 1 ? 'Error' : 'Errors'}
                </span>
              )}
              {warningCount > 0 && (
                <span className="px-2 py-0.5 rounded-full bg-amber-500/20 text-amber-400 border border-amber-500/30">
                  {warningCount} {warningCount === 1 ? 'Warning' : 'Warnings'}
                </span>
              )}
              {infoCount > 0 && (
                <span className="px-2 py-0.5 rounded-full bg-blue-500/20 text-blue-400 border border-blue-500/30">
                  {infoCount} Info
                </span>
              )}
              {diagnostics.length === 0 && (
                <span className="px-2 py-0.5 rounded-full bg-emerald-500/20 text-emerald-400 border border-emerald-500/30">
                  All Systems Healthy
                </span>
              )}
            </div>
          </div>

          {/* Diagnostics Filter Tabs */}
          {diagnostics.length > 0 && (
            <div className="flex items-center gap-1 text-xs">
              {['all', 'dns', 'port', 'dependency', 'env_var', 'network'].map((cat) => (
                <button
                  key={cat}
                  type="button"
                  onClick={() => setFilterCategory(cat)}
                  className={cn(
                    'px-2 py-1 rounded transition-colors text-[11px] uppercase tracking-wider',
                    filterCategory === cat
                      ? 'bg-white text-black font-semibold'
                      : 'text-neutral-400 hover:text-neutral-200 bg-surface-elevated'
                  )}
                >
                  {cat}
                </button>
              ))}
            </div>
          )}
        </div>

        {diagnostics.length === 0 ? (
          <div className="flex items-center gap-3 p-3 rounded-lg bg-emerald-500/10 border border-emerald-500/20 text-emerald-300 text-xs">
            <CheckCircle2 className="w-4 h-4 text-emerald-400 shrink-0" />
            <span>
              All services have validated DNS names, non-conflicting ports, network connectivity, and healthy upstream dependencies.
            </span>
          </div>
        ) : (
          <div className="space-y-3">
            <div className="p-2.5 rounded-md bg-surface-elevated/40 border border-surface-border/70">
              <StackedDistributionBar
                height={9}
                segments={[
                  { id: 'errors', label: 'Errors', value: errorCount, color: '#f43f5e' },
                  { id: 'warnings', label: 'Warnings', value: warningCount, color: '#f59e0b' },
                  { id: 'info', label: 'Info', value: infoCount, color: '#3b82f6' },
                ]}
              />
            </div>
            <div className="space-y-2.5">
            {filteredDiagnostics.map((diag, idx) => (
              <div
                key={idx}
                className={cn(
                  'p-3 rounded-lg border text-xs space-y-1.5 transition-colors',
                  diag.severity === 'error' && 'bg-rose-500/10 border-rose-500/30 text-rose-200',
                  diag.severity === 'warning' && 'bg-amber-500/10 border-amber-500/30 text-amber-200',
                  diag.severity === 'info' && 'bg-blue-500/10 border-blue-500/30 text-blue-200'
                )}
              >
                <div className="flex items-center justify-between">
                  <div className="flex items-center gap-2">
                    {diag.severity === 'error' && <AlertCircle className="w-4 h-4 text-rose-400" />}
                    {diag.severity === 'warning' && <AlertTriangle className="w-4 h-4 text-amber-400" />}
                    {diag.severity === 'info' && <Info className="w-4 h-4 text-blue-400" />}
                    <span className="font-semibold">{diag.title}</span>
                    {diag.service_name && (
                      <span className="px-1.5 py-0.5 rounded bg-black/40 font-mono text-[10px] text-white">
                        {diag.service_name}
                      </span>
                    )}
                  </div>
                  <span className="uppercase text-[10px] font-mono tracking-wider opacity-80">
                    {diag.category}
                  </span>
                </div>
                <p className="text-neutral-300 leading-relaxed">{diag.message}</p>
                <div className="p-2 rounded bg-black/30 border border-white/10 text-white flex items-start gap-1.5 font-sans">
                  <span className="font-semibold text-emerald-400 shrink-0">Action:</span>
                  <span>{diag.action}</span>
                </div>
              </div>
            ))}
            </div>
          </div>
        )}
      </div>

      {/* DAG Execution Tiers & Service Topology Graph */}
      <div className="space-y-4">
        <div className="flex items-center justify-between">
          <h4 className="text-xs font-semibold text-neutral-300 uppercase tracking-wider flex items-center gap-2">
            <Layers className="w-4 h-4 text-indigo-400" />
            <span>Dependency Execution Tiers ({tiers.length} {tiers.length === 1 ? 'Tier' : 'Tiers'})</span>
          </h4>
          <span className="text-xs text-neutral-400">
            Tiers execute sequentially; services within a tier execute concurrently.
          </span>
        </div>

        {tiers.length === 0 ? (
          <div className="p-6 rounded-lg bg-surface-card border border-surface-border text-center text-xs text-neutral-400">
            No services configured for this project.
          </div>
        ) : (
          <div className="space-y-4">
            {tiers.map((tierServices, tierIdx) => (
              <div
                key={tierIdx}
                className="bg-surface-card border border-surface-border rounded-lg p-4 space-y-3"
              >
                <div className="flex items-center justify-between border-b border-surface-border pb-2">
                  <span className="text-xs font-bold text-white uppercase tracking-wider flex items-center gap-2">
                    <span className="w-5 h-5 rounded-full bg-indigo-600/30 text-indigo-400 border border-indigo-500/40 flex items-center justify-center text-[10px] font-mono">
                      {tierIdx + 1}
                    </span>
                    Tier {tierIdx + 1}
                  </span>
                  <span className="text-[11px] text-neutral-400">
                    {tierServices.length} {tierServices.length === 1 ? 'Service' : 'Services'}
                  </span>
                </div>

                <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-3">
                  {tierServices.map((serviceName) => {
                    const svc = services.find((s) => s.name === serviceName);
                    if (!svc) return null;

                    const isRunning = svc.status === 'running';
                    const isFailed = svc.status === 'failed';

                    return (
                      <div
                        key={svc.id}
                        className={cn(
                          'p-3.5 rounded-lg border bg-surface-elevated/70 flex flex-col justify-between space-y-3 transition-colors',
                          isRunning && 'border-emerald-500/30',
                          isFailed && 'border-rose-500/30',
                          !isRunning && !isFailed && 'border-surface-border'
                        )}
                      >
                        <div className="space-y-1.5">
                          <div className="flex items-center justify-between">
                            <span className="font-semibold text-sm text-white flex items-center gap-1.5">
                              <Server className="w-3.5 h-3.5 text-neutral-400" />
                              {svc.name}
                            </span>
                            <span
                              className={cn(
                                'px-2 py-0.5 rounded text-[10px] font-mono capitalize',
                                isRunning && 'bg-emerald-500/20 text-emerald-400',
                                isFailed && 'bg-rose-500/20 text-rose-400',
                                !isRunning && !isFailed && 'bg-neutral-800 text-neutral-400'
                              )}
                            >
                              {svc.status}
                            </span>
                          </div>

                          <div className="flex items-center gap-1.5 flex-wrap">
                            <span className="px-1.5 py-0.5 rounded bg-surface-border text-[10px] font-mono text-neutral-300">
                              {svc.role}
                            </span>
                            <span className="px-1.5 py-0.5 rounded bg-surface-border text-[10px] font-mono text-neutral-300">
                              {svc.classification}
                            </span>
                          </div>
                        </div>

                        {/* Network & Connection details */}
                        <div className="space-y-1 text-xs border-t border-surface-border pt-2 text-neutral-300">
                          <div className="flex items-center justify-between text-[11px]">
                            <span className="text-neutral-400">Internal DNS:</span>
                            <span className="font-mono text-white">
                              {svc.internal_hostname}:{svc.internal_port}
                            </span>
                          </div>

                          {svc.host_port && (
                            <div className="flex items-center justify-between text-[11px]">
                              <span className="text-neutral-400">Host Port:</span>
                              <span className="font-mono text-white">:{svc.host_port}</span>
                            </div>
                          )}

                          {svc.preview_url && isRunning && (
                            <div className="flex items-center justify-between text-[11px] pt-1">
                              <span className="text-neutral-400">Live Preview:</span>
                              <a
                                href={svc.preview_url}
                                target="_blank"
                                rel="noreferrer"
                                className="text-indigo-400 hover:text-indigo-300 flex items-center gap-1 font-mono truncate max-w-[150px]"
                              >
                                <span>{svc.preview_url}</span>
                                <ExternalLink className="w-2.5 h-2.5 shrink-0" />
                              </a>
                            </div>
                          )}

                          {svc.depends_on && svc.depends_on.length > 0 && (
                            <div className="pt-1.5 flex items-center gap-1 flex-wrap text-[10px]">
                              <span className="text-neutral-400 flex items-center gap-0.5">
                                <ArrowRight className="w-2.5 h-2.5" /> Depends on:
                              </span>
                              {svc.depends_on.map((dep) => (
                                <span
                                  key={dep}
                                  className="px-1.5 py-0.2 rounded bg-surface-border font-mono text-neutral-300"
                                >
                                  {dep}
                                </span>
                              ))}
                            </div>
                          )}
                        </div>
                      </div>
                    );
                  })}
                </div>
              </div>
            ))}
          </div>
        )}
      </div>

      {/* Operational Diagnostics Card */}
      <div className="bg-surface-card border border-surface-border rounded-lg p-4 space-y-4">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="flex items-center gap-2">
            <Activity className="w-4 h-4 text-sky-400" />
            <h4 className="text-xs font-semibold text-white uppercase tracking-wider">
              Operational Diagnostics
            </h4>
            {diagnosticsReport && (
              <span
                className={cn(
                  'px-2 py-0.5 rounded-full text-[11px] font-mono border uppercase',
                  diagnosticsReport.overall_status === 'healthy' &&
                    'bg-emerald-500/20 text-emerald-400 border-emerald-500/30',
                  diagnosticsReport.overall_status === 'degraded' &&
                    'bg-amber-500/20 text-amber-400 border-amber-500/30',
                  diagnosticsReport.overall_status === 'critical' &&
                    'bg-rose-500/20 text-rose-400 border-rose-500/30'
                )}
              >
                {diagnosticsReport.overall_status}
              </span>
            )}
          </div>
          {diagnosticsReport && (
            <span className="text-[11px] font-mono text-neutral-400">
              Collected {new Date(diagnosticsReport.collected_at).toLocaleTimeString()}
            </span>
          )}
        </div>

        {diagError && (
          <div className="p-2.5 rounded bg-amber-500/10 border border-amber-500/20 text-xs text-amber-300">
            {diagError}
          </div>
        )}

        {diagnosticsReport && (
          <>
            <div className="grid grid-cols-1 lg:grid-cols-12 gap-3 text-xs font-mono">
              {/* Docker Ping Latency Gauge */}
              <div className="lg:col-span-3 p-3 rounded-lg bg-surface/60 border border-surface-border/60 flex flex-col items-center justify-center space-y-1.5">
                <span className="text-[10px] text-neutral-500 uppercase self-start">Docker Daemon</span>
                <RadialGauge
                  value={diagnosticsReport.docker.available ? Math.min(100, (diagnosticsReport.docker.ping_latency_ms / 250) * 100) : 100}
                  max={100}
                  size={84}
                  label="Ping"
                  valueLabel={diagnosticsReport.docker.available ? `${diagnosticsReport.docker.ping_latency_ms}ms` : 'Down'}
                  color={
                    !diagnosticsReport.docker.available
                      ? '#f43f5e'
                      : diagnosticsReport.docker.ping_latency_ms > 150
                      ? '#f59e0b'
                      : '#10b981'
                  }
                  sublabel={
                    diagnosticsReport.docker.server_version
                      ? `v${diagnosticsReport.docker.server_version}`
                      : diagnosticsReport.docker.error || 'Unreachable'
                  }
                />
              </div>

              {/* Host Storage Active vs Reclaimable Bar */}
              <div className="lg:col-span-5 p-3 rounded-lg bg-surface/60 border border-surface-border/60 flex flex-col justify-between space-y-2.5">
                <div className="flex items-center justify-between gap-2">
                  <span className="text-[10px] text-neutral-500 uppercase flex items-center gap-1">
                    <HardDrive className="w-3 h-3 text-neutral-400" />
                    Host Disk Allocation
                  </span>
                  <span className="text-[10px] text-neutral-400">
                    {diagnosticsReport.resources.host_cpus || 0} CPUs &middot;{' '}
                    {formatBytes(diagnosticsReport.resources.host_memory_total_bytes || 0)} RAM
                  </span>
                </div>
                <StackedDistributionBar
                  height={11}
                  formatValue={(v) => formatBytes(v)}
                  segments={[
                    {
                      id: 'in-use',
                      label: 'Active',
                      value: Math.max(
                        0,
                        (diagnosticsReport.resources.disk_usage?.total_bytes || 0) -
                          (diagnosticsReport.resources.disk_usage?.total_reclaimable_bytes || 0)
                      ),
                      color: '#10b981',
                    },
                    {
                      id: 'reclaimable',
                      label: 'Reclaimable',
                      value: diagnosticsReport.resources.disk_usage?.total_reclaimable_bytes || 0,
                      color: '#f59e0b',
                    },
                  ]}
                />
                <div className="text-[10px] text-neutral-500">
                  Reconciliation: {diagnosticsReport.docker.reconciliation?.status || 'idle'}
                </div>
              </div>

              {/* Operational Events Bar Graph */}
              <div className="lg:col-span-4 p-3 rounded-lg bg-surface/60 border border-surface-border/60 space-y-2">
                <span className="text-[10px] text-neutral-500 block uppercase">Operational Signal Breakdown</span>
                <HorizontalBarChart
                  formatValue={(v) => String(Math.round(v))}
                  items={[
                    {
                      id: 'exhausted',
                      label: 'Resource Warnings',
                      value: diagnosticsReport.resources.exhausted_services.length,
                      maxValue: Math.max(4, services.length),
                      color: diagnosticsReport.resources.exhausted_services.length > 0 ? '#f59e0b' : '#10b981',
                    },
                    {
                      id: 'failures',
                      label: 'Failed Deploys',
                      value: diagnosticsReport.deployment_failures.length,
                      maxValue: Math.max(4, diagnosticsReport.deployment_failures.length),
                      color: diagnosticsReport.deployment_failures.length > 0 ? '#f43f5e' : '#10b981',
                    },
                    {
                      id: 'imports',
                      label: 'Tracked Imports',
                      value: diagnosticsReport.imports.length,
                      maxValue: Math.max(4, diagnosticsReport.imports.length),
                      color: '#38bdf8',
                    },
                  ]}
                />
              </div>
            </div>

            {diagnosticsReport.issues.length === 0 ? (
              <div className="flex items-center gap-2.5 p-3 rounded-lg bg-emerald-500/10 border border-emerald-500/20 text-emerald-300 text-xs">
                <CheckCircle2 className="w-4 h-4 text-emerald-400 shrink-0" />
                <span>No Docker connectivity, resource exhaustion, deployment failure, or stalled import issues detected.</span>
              </div>
            ) : (
              <div className="space-y-2.5">
                {diagnosticsReport.issues.map((issue, idx) => (
                  <div
                    key={`${issue.category}-${idx}`}
                    className={cn(
                      'p-3 rounded-lg border text-xs space-y-1.5',
                      issue.severity === 'critical' && 'bg-rose-500/10 border-rose-500/30 text-rose-200',
                      issue.severity === 'warning' && 'bg-amber-500/10 border-amber-500/30 text-amber-200',
                      issue.severity !== 'critical' &&
                        issue.severity !== 'warning' &&
                        'bg-blue-500/10 border-blue-500/30 text-blue-200'
                    )}
                  >
                    <div className="flex items-center justify-between gap-2">
                      <span className="font-semibold">{issue.title}</span>
                      <span className="uppercase text-[10px] font-mono opacity-80">
                        {issue.severity} &middot; {issue.category}
                      </span>
                    </div>
                    <p className="text-neutral-300 leading-relaxed">{issue.message}</p>
                    <div className="p-2 rounded bg-black/30 border border-white/10 text-neutral-100">
                      <span className="font-semibold text-emerald-400 mr-1.5">Remediation:</span>
                      <span>{issue.remediation}</span>
                    </div>
                  </div>
                ))}
              </div>
            )}
          </>
        )}
      </div>

      {/* Operational Audit Trail Card */}
      <div className="bg-surface-card border border-surface-border rounded-lg p-4 space-y-4">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="flex items-center gap-2">
            <History className="w-4 h-4 text-purple-400" />
            <h4 className="text-xs font-semibold text-white uppercase tracking-wider">
              Operational Audit Trail
            </h4>
            <span className="text-[11px] font-mono text-neutral-400">
              ({auditLogs.length} events)
            </span>
          </div>

          <div className="flex flex-wrap items-center gap-1 text-xs">
            {AUDIT_RESOURCE_FILTERS.map((f) => (
              <button
                key={f.id}
                type="button"
                onClick={() => setAuditFilter(f.id)}
                className={cn(
                  'px-2 py-1 rounded transition-colors text-[11px] uppercase tracking-wider',
                  auditFilter === f.id
                    ? 'bg-white text-black font-semibold'
                    : 'text-neutral-400 hover:text-neutral-200 bg-surface-elevated'
                )}
              >
                {f.label}
              </button>
            ))}
          </div>
        </div>

        {auditError && (
          <div className="p-2.5 rounded bg-amber-500/10 border border-amber-500/20 text-xs text-amber-300">
            {auditError}
          </div>
        )}

        {auditLogs.length === 0 ? (
          <div className="p-4 rounded border border-surface-border/60 bg-surface/40 text-center text-xs text-neutral-400">
            No operational audit events recorded for this filter yet. Deployments, rollbacks, secret updates, custom domains, volume backups, and lifecycle actions appear here.
          </div>
        ) : (
          <div className="space-y-2">
            {auditLogs.map((entry) => (
              <div
                key={entry.id}
                className="p-3 rounded-lg border border-surface-border bg-surface-elevated/40 text-xs space-y-1"
              >
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <div className="flex items-center gap-2 min-w-0">
                    <span
                      className={cn(
                        'px-1.5 py-0.5 rounded font-mono text-[10px] uppercase border',
                        entry.status === 'success' && 'bg-emerald-500/15 text-emerald-300 border-emerald-500/30',
                        entry.status === 'warning' && 'bg-amber-500/15 text-amber-300 border-amber-500/30',
                        entry.status === 'failure' && 'bg-rose-500/15 text-rose-300 border-rose-500/30'
                      )}
                    >
                      {entry.status}
                    </span>
                    <span className="font-mono font-semibold text-white">{entry.action}</span>
                    <span className="px-1.5 py-0.5 rounded bg-surface border border-surface-border text-[10px] font-mono text-neutral-400">
                      {entry.resource_type}
                    </span>
                  </div>
                  <span className="text-[11px] font-mono text-neutral-400">
                    {new Date(entry.created_at).toLocaleString()}
                  </span>
                </div>

                <p className="text-neutral-300">{entry.message}</p>

                <div className="flex flex-wrap items-center gap-2 text-[10px] font-mono text-neutral-500">
                  <span>Actor: {entry.is_guest ? 'Guest Workspace' : entry.actor_label || entry.actor_id}</span>
                  {entry.metadata &&
                    Object.entries(entry.metadata)
                      .slice(0, 4)
                      .map(([k, v]) => (
                        <span
                          key={k}
                          className="px-1.5 py-0.5 rounded bg-black/30 border border-surface-border/50 text-neutral-300"
                        >
                          {k}={String(v)}
                        </span>
                      ))}
                </div>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}
