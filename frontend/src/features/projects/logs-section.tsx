'use client';

import React from 'react';
import { Deployment, DeploymentLog, Service } from '@/lib/api/types';
import { TerminalViewer } from '@/features/deployments/terminal-viewer';
import { WSConnectionState } from '@/lib/websocket/use-deployment-ws';
import { cn } from '@/lib/utils/cn';

interface LogsSectionProps {
  deployments: Deployment[];
  selectedDeployment: Deployment | null;
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
  onSelectDeployment,
  services,
  selectedServiceId,
  onSelectServiceScope,
  logs,
  connected,
  connectionState,
  onClearLogs,
}: LogsSectionProps) {
  // Client-side filtering by selected service ID
  // Because backend attaches service_id to log events, we filter client-side with zero socket churn!
  const displayedLogs = selectedServiceId
    ? logs.filter((l) => l.service_id === selectedServiceId)
    : logs;

  return (
    <div className="space-y-3.5">
      {/* Scope Toolbar: Release Selector & Service Scope Filter */}
      <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-3">
        {/* Release Selector Bar */}
        {deployments.length > 1 && (
          <div className="flex items-center gap-1.5 overflow-x-auto pb-1 text-xs font-mono">
            <span className="text-neutral-500 shrink-0 text-[11px] uppercase tracking-wider font-semibold">
              Release:
            </span>
            {deployments.map((d) => (
              <button
                key={d.id}
                type="button"
                onClick={() => onSelectDeployment(d)}
                className={cn(
                  'px-2.5 py-1 rounded border text-[11px] shrink-0 transition-colors',
                  selectedDeployment?.id === d.id
                    ? 'border-white bg-surface-elevated text-white font-semibold shadow-sm'
                    : 'border-surface-border text-neutral-400 hover:text-white hover:bg-surface-elevated/40'
                )}
              >
                #{d.deploy_number} ({d.status})
              </button>
            ))}
          </div>
        )}

        {/* Service Scope Tabs (Client-Side Filtered) */}
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
                selectedServiceId === null
                  ? 'bg-white text-black font-semibold shadow-sm'
                  : 'text-neutral-400 hover:text-white'
              )}
            >
              All Services
            </button>
            {services.map((svc) => (
              <button
                key={svc.id}
                type="button"
                onClick={() => onSelectServiceScope(svc.id)}
                className={cn(
                  'px-2.5 py-1 rounded transition-colors text-xs font-medium flex items-center gap-1.5',
                  selectedServiceId === svc.id
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
            ))}
          </div>
        )}
      </div>

      {/* Terminal Logs Output */}
      <TerminalViewer
        logs={displayedLogs}
        connected={connected}
        connectionState={connectionState}
        onClear={onClearLogs}
        deploymentNumber={selectedDeployment?.deploy_number}
        deploymentId={selectedDeployment?.id}
      />
    </div>
  );
}
