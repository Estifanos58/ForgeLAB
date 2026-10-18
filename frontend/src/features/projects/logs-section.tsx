'use client';

import React from 'react';
import { Deployment, DeploymentLog, LogTarget, Service } from '@/lib/api/types';
import { TerminalViewer } from '@/features/deployments/terminal-viewer';
import { WSConnectionState } from '@/lib/websocket/use-deployment-ws';
import { cn } from '@/lib/utils/cn';
import { Radio, ExternalLink } from 'lucide-react';

export type LogPhaseFilter = 'all' | 'deployment' | 'production';

interface LogsSectionProps {
  deployments: Deployment[];
  selectedDeployment?: Deployment | null;
  selectedLogTarget?: LogTarget | null;
  onSelectDeployment: (deployment: Deployment) => void;
  services: Service[];
  selectedServiceId: string | null;
  onSelectServiceScope: (serviceId: string | null) => void;
  logs: DeploymentLog[];
  connected: boolean;
  connectionState: WSConnectionState;
  onClearLogs?: () => void;
  primaryPreviewUrl?: string | null;
}

export function LogsSection({
  deployments,
  selectedDeployment,
  selectedLogTarget,
  onSelectDeployment,
  services,
  selectedServiceId,
  onSelectServiceScope,
  logs,
  connected,
  connectionState,
  onClearLogs,
  primaryPreviewUrl,
}: LogsSectionProps) {
  const [phaseFilter, setPhaseFilter] = React.useState<LogPhaseFilter>('all');

  // 1. Service Scope:
  // If target is a service deployment, show its logs directly.
  // Otherwise if a specific service is selected, filter to that service.
  // Otherwise ("All Services"), include all services (frontend, backend, databases, workers, independent).
  const scopedLogs =
    selectedLogTarget?.type === 'service'
      ? logs
      : selectedServiceId
      ? logs.filter((l) => l.service_id === selectedServiceId)
      : logs;

  // Calculate counts for phase filter badges
  const countAll = scopedLogs.length;
  const countDeployment = scopedLogs.filter((l) =>
    ['source', 'build', 'startup', 'health'].includes(l.phase)
  ).length;
  const countProduction = scopedLogs.filter((l) => l.phase === 'runtime').length;

  // 2. Phase Category Filter:
  // - "deployment": source, build, startup, and health phases
  // - "production": ongoing runtime stdout/stderr
  // - "all": everything
  const displayedLogs = scopedLogs.filter((log) => {
    if (phaseFilter === 'all') return true;
    if (phaseFilter === 'deployment') {
      return ['source', 'build', 'startup', 'health'].includes(log.phase);
    }
    if (phaseFilter === 'production') {
      return log.phase === 'runtime';
    }
    return true;
  });

  const currentDeployNumber =
    selectedLogTarget?.type === 'service'
      ? selectedLogTarget.serviceDeployment.deploy_number
      : selectedLogTarget?.type === 'release'
      ? selectedLogTarget.deployment.deploy_number
      : selectedDeployment?.deploy_number;

  const currentDeployId =
    selectedLogTarget?.type === 'service'
      ? selectedLogTarget.serviceDeployment.id
      : selectedLogTarget?.type === 'release'
      ? selectedLogTarget.deployment.id
      : selectedDeployment?.id;

  return (
    <div className="h-full flex flex-col min-h-0 space-y-3">
      {/* Service Target Active Pill */}
      {selectedLogTarget?.type === 'service' && (
        <div className="shrink-0 flex items-center justify-between gap-2 px-3 py-2 rounded-md bg-surface border border-surface-border text-xs font-mono">
          <div className="flex items-center gap-2 min-w-0">
            <span className="text-[10px] text-neutral-500 uppercase tracking-wider font-semibold">Service Target:</span>
            <span className="font-semibold text-emerald-400 truncate">
              {selectedLogTarget.service?.name || selectedLogTarget.serviceDeployment.service_name || 'Service'}
            </span>
            <span className="text-neutral-500">·</span>
            <span className="text-white font-bold shrink-0">
              Deploy #{selectedLogTarget.serviceDeployment.deploy_number}
            </span>
            <span className="text-neutral-400 shrink-0 text-[11px]">
              ({selectedLogTarget.serviceDeployment.status})
            </span>
          </div>
          {deployments.length > 0 && (
            <button
              type="button"
              onClick={() => onSelectDeployment(deployments[0])}
              className="text-[11px] text-neutral-400 hover:text-white underline underline-offset-2 transition-colors font-sans shrink-0"
            >
              Switch to Release Logs →
            </button>
          )}
        </div>
      )}

      {/* Release Selector Bar (when multiple releases exist) */}
      {deployments.length > 0 && (
        <div className="shrink-0 flex items-center justify-between gap-2.5 min-w-0">
          <div className="flex items-center gap-1.5 overflow-x-auto pb-1 text-xs font-mono min-w-0 max-w-full">
            <span className="text-neutral-500 shrink-0 text-[11px] uppercase tracking-wider font-semibold">
              Release:
            </span>
            {deployments.map((d) => {
              const isSelected = selectedLogTarget
                ? selectedLogTarget.type === 'release' && selectedLogTarget.deployment.id === d.id
                : selectedDeployment?.id === d.id;
              return (
                <button
                  key={d.id}
                  type="button"
                  id={`release-selector-${d.deploy_number}`}
                  onClick={() => onSelectDeployment(d)}
                  className={cn(
                    'px-2.5 py-1 rounded border text-[11px] shrink-0 transition-colors',
                    isSelected
                      ? 'border-white bg-surface-elevated text-white font-semibold shadow-sm'
                      : 'border-surface-border text-neutral-400 hover:text-white hover:bg-surface-elevated/40'
                  )}
                >
                  #{d.deploy_number} ({d.status})
                </button>
              );
            })}
          </div>

          {primaryPreviewUrl && (
            <a
              href={primaryPreviewUrl}
              target="_blank"
              rel="noreferrer"
              className="inline-flex items-center gap-1.5 px-2.5 py-1 rounded bg-emerald-500 hover:bg-emerald-400 text-black text-xs font-semibold shadow-sm transition-colors shrink-0 font-sans ml-auto"
              title="Open application preview in new tab"
            >
              <Radio className="w-3 h-3 text-black animate-pulse" />
              <span>Preview</span>
              <ExternalLink className="w-3 h-3" />
            </a>
          )}
        </div>
      )}

      {/* Scope & Phase Filter Toolbar */}
      <div className="shrink-0 flex flex-wrap items-center justify-between gap-2.5 min-w-0">
        {/* Phase Filter Tabs: All Logs | Deployment | Production */}
        <div className="flex items-center gap-1 p-1 rounded-md bg-surface border border-surface-border text-xs font-mono shrink-0">
          <button
            type="button"
            id="log-filter-all"
            onClick={() => setPhaseFilter('all')}
            className={cn(
              'px-2.5 py-1 rounded transition-colors text-xs font-medium shrink-0 flex items-center gap-1.5',
              phaseFilter === 'all'
                ? 'bg-white text-black font-semibold shadow-sm'
                : 'text-neutral-400 hover:text-white'
            )}
          >
            <span>All Logs</span>
            <span className="text-[10px] opacity-75 font-sans">({countAll})</span>
          </button>
          <button
            type="button"
            id="log-filter-deployment"
            onClick={() => setPhaseFilter('deployment')}
            className={cn(
              'px-2.5 py-1 rounded transition-colors text-xs font-medium shrink-0 flex items-center gap-1.5',
              phaseFilter === 'deployment'
                ? 'bg-white text-black font-semibold shadow-sm'
                : 'text-neutral-400 hover:text-white'
            )}
          >
            <span>Deployment</span>
            <span className="text-[10px] opacity-75 font-sans">({countDeployment})</span>
          </button>
          <button
            type="button"
            id="log-filter-production"
            onClick={() => setPhaseFilter('production')}
            className={cn(
              'px-2.5 py-1 rounded transition-colors text-xs font-medium shrink-0 flex items-center gap-1.5',
              phaseFilter === 'production'
                ? 'bg-white text-black font-semibold shadow-sm'
                : 'text-neutral-400 hover:text-white'
            )}
          >
            <span>Production</span>
            <span className="text-[10px] opacity-75 font-sans">({countProduction})</span>
          </button>
        </div>

        {/* Service Scope Filter Tabs */}
        {services.length > 0 && (
          <div className="flex items-center gap-1 p-1 rounded-md bg-surface border border-surface-border text-xs font-mono overflow-x-auto max-w-full min-w-0 shrink-0 ml-auto">
            <span className="text-neutral-500 text-[10px] px-2 uppercase tracking-wider font-semibold shrink-0">
              Scope:
            </span>
            <button
              type="button"
              id="log-scope-all"
              onClick={() => onSelectServiceScope(null)}
              className={cn(
                'px-2.5 py-1 rounded transition-colors text-xs font-medium shrink-0 whitespace-nowrap',
                selectedServiceId === null
                  ? 'bg-white text-black font-semibold shadow-sm'
                  : 'text-neutral-400 hover:text-white'
              )}
            >
              All Services
            </button>
            {services.map((svc) => {
              const isServiceActive =
                (selectedLogTarget?.type === 'service' && selectedLogTarget.serviceDeployment.service_id === svc.id) ||
                selectedServiceId === svc.id;
              return (
                <button
                  key={svc.id}
                  type="button"
                  id={`log-scope-${svc.name}`}
                  onClick={() => onSelectServiceScope(svc.id)}
                  className={cn(
                    'px-2.5 py-1 rounded transition-colors text-xs font-medium flex items-center gap-1.5 shrink-0 whitespace-nowrap',
                    isServiceActive
                      ? 'bg-white text-black font-semibold shadow-sm'
                      : 'text-neutral-400 hover:text-white'
                  )}
                >
                  <span
                    className={cn(
                      'w-1.5 h-1.5 rounded-full shrink-0',
                      svc.status === 'running'
                        ? 'bg-emerald-500'
                        : svc.status === 'failed'
                        ? 'bg-rose-500'
                        : svc.status === 'deploying' || svc.status === 'building' || svc.status === 'starting'
                        ? 'bg-amber-400 animate-pulse'
                        : 'bg-neutral-500'
                    )}
                  />
                  <span>{svc.name}</span>
                  <span className="text-[10px] opacity-70 uppercase font-sans">({svc.role})</span>
                </button>
              );
            })}
          </div>
        )}
      </div>

      {/* Terminal Logs Output */}
      <div className="flex-1 min-h-0 flex flex-col">
        <TerminalViewer
          logs={displayedLogs}
          connected={connected}
          connectionState={connectionState}
          onClear={onClearLogs}
          deploymentNumber={currentDeployNumber}
          deploymentId={currentDeployId}
          services={services}
          selectedServiceId={selectedServiceId}
        />
      </div>
    </div>
  );
}
