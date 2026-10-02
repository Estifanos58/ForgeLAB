'use client';

import React from 'react';
import { Deployment, DeploymentLog, LogTarget, Service } from '@/lib/api/types';
import { TerminalViewer } from '@/features/deployments/terminal-viewer';
import { WSConnectionState } from '@/lib/websocket/use-deployment-ws';
import { cn } from '@/lib/utils/cn';

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
}: LogsSectionProps) {
  // If target is a service deployment, show all its logs directly.
  // Otherwise filter release logs client-side if a specific service scope is selected.
  const displayedLogs =
    selectedLogTarget?.type === 'service'
      ? logs
      : selectedServiceId
      ? logs.filter((l) => l.service_id === selectedServiceId)
      : logs;

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
    <div className="space-y-3.5">
      {/* Service Target Active Pill */}
      {selectedLogTarget?.type === 'service' && (
        <div className="flex items-center justify-between gap-2 px-3 py-2 rounded-md bg-surface border border-surface-border text-xs font-mono">
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

      {/* Scope Toolbar: Release Selector & Service Scope Filter */}
      <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-3">
        {/* Release Selector Bar */}
        {deployments.length > 0 && (
          <div className="flex items-center gap-1.5 overflow-x-auto pb-1 text-xs font-mono">
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
        )}

        {/* Service Scope Tabs */}
        {services.length > 0 && (
          <div className="flex items-center gap-1 p-1 rounded-md bg-surface border border-surface-border text-xs font-mono ml-auto">
            <span className="text-neutral-500 text-[10px] px-2 uppercase tracking-wider font-semibold">
              Scope:
            </span>
            <button
              type="button"
              onClick={() => onSelectServiceScope(null)}
              className={cn(
                'px-2.5 py-1 rounded transition-colors text-xs font-medium',
                selectedLogTarget?.type === 'release' && selectedServiceId === null
                  ? 'bg-white text-black font-semibold shadow-sm'
                  : 'text-neutral-400 hover:text-white'
              )}
            >
              All Services
            </button>
            {services.map((svc) => {
              const isServiceActive =
                (selectedLogTarget?.type === 'service' && selectedLogTarget.serviceDeployment.service_id === svc.id) ||
                (selectedLogTarget?.type === 'release' && selectedServiceId === svc.id);
              return (
                <button
                  key={svc.id}
                  type="button"
                  onClick={() => onSelectServiceScope(svc.id)}
                  className={cn(
                    'px-2.5 py-1 rounded transition-colors text-xs font-medium flex items-center gap-1.5',
                    isServiceActive
                      ? 'bg-white text-black font-semibold shadow-sm'
                      : 'text-neutral-400 hover:text-white'
                  )}
                >
                  <span
                    className={cn(
                      'w-1.5 h-1.5 rounded-full',
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
      <TerminalViewer
        logs={displayedLogs}
        connected={connected}
        connectionState={connectionState}
        onClear={onClearLogs}
        deploymentNumber={currentDeployNumber}
        deploymentId={currentDeployId}
      />
    </div>
  );
}
