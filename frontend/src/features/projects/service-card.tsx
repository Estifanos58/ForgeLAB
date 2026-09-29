'use client';

import React from 'react';
import { Service } from '@/lib/api/types';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import {
  ExternalLink,
  Lock,
  Play,
  Square,
  RotateCcw,
  Rocket,
  Terminal,
  Server,
  Globe,
  Radio,
} from 'lucide-react';
import { cn } from '@/lib/utils/cn';

interface ServiceCardProps {
  service: Service;
  isDeployPending?: boolean;
  actionLoading?: string | null;
  isAnyDeploying?: boolean;
  onDeploy: (serviceId: string) => void;
  onAction: (serviceId: string, action: 'start' | 'stop' | 'restart') => void;
  onViewLogs: (serviceId: string) => void;
}

export function ServiceCard({
  service,
  isDeployPending = false,
  actionLoading = null,
  isAnyDeploying = false,
  onDeploy,
  onAction,
  onViewLogs,
}: ServiceCardProps) {
  const isFrontend = service.role === 'frontend';
  const isBackend = service.role === 'backend';
  const isRunning = service.status === 'running';
  const isServiceDeploying = [
    'deploying',
    'building',
    'starting',
    'health_checking',
    'queued',
  ].includes(service.status);

  // Construct browser-accessible preview URL if public
  const previewUrl =
    service.preview_url ||
    (service.public_exposed && service.host_port
      ? `http://${typeof window !== 'undefined' ? window.location.hostname : 'localhost'}:${service.host_port}`
      : null);

  const internalEndpoint = `${service.name}:${service.internal_port}`;

  return (
    <div className="rounded-lg border border-surface-border bg-surface-elevated/40 p-4 space-y-3.5 transition-colors hover:border-surface-border/90">
      {/* Top Header: Role badge, Name, Status Badge */}
      <div className="flex items-center justify-between gap-2">
        <div className="flex items-center gap-2.5 min-w-0">
          <span
            className={cn(
              'px-2 py-0.5 rounded text-[10px] font-mono font-bold uppercase tracking-wider border shrink-0',
              isFrontend
                ? 'bg-blue-500/10 text-blue-400 border-blue-500/20'
                : isBackend
                ? 'bg-emerald-500/10 text-emerald-400 border-emerald-500/20'
                : 'bg-purple-500/10 text-purple-400 border-purple-500/20'
            )}
          >
            {service.role}
          </span>
          <span className="text-sm font-semibold text-white truncate" title={service.name}>
            {service.name}
          </span>
        </div>
        <div className="shrink-0">
          <Badge status={service.status} />
        </div>
      </div>

      {/* Meta Grid: Runtime, Source Path, Container Port, Endpoint */}
      <div className="grid grid-cols-1 sm:grid-cols-2 gap-2.5 text-xs font-mono bg-surface/60 p-2.5 rounded border border-surface-border/50">
        <div>
          <span className="text-[10px] text-neutral-500 block uppercase tracking-wider">Runtime</span>
          <span className="text-neutral-200 capitalize truncate block">
            {service.framework && service.framework !== 'generic'
              ? service.framework
              : service.runtime_type || 'generic'}
          </span>
        </div>

        <div>
          <span className="text-[10px] text-neutral-500 block uppercase tracking-wider">Path</span>
          <span className="text-neutral-200 truncate block" title={service.source_path}>
            {service.source_path || '.'}
          </span>
        </div>

        <div>
          <span className="text-[10px] text-neutral-500 block uppercase tracking-wider">Internal Port</span>
          <span className="text-neutral-300">:{service.internal_port}</span>
        </div>

        <div>
          <span className="text-[10px] text-neutral-500 block uppercase tracking-wider">
            {service.public_exposed ? 'Public Endpoint' : 'Internal Mesh Endpoint'}
          </span>
          {service.public_exposed ? (
            isRunning && previewUrl ? (
              <a
                href={previewUrl}
                target="_blank"
                rel="noreferrer"
                className="inline-flex items-center gap-1 text-emerald-400 hover:text-emerald-300 font-semibold truncate"
                title={previewUrl}
              >
                <span>:{service.host_port}</span>
                <ExternalLink className="w-3 h-3 shrink-0" />
              </a>
            ) : service.host_port ? (
              <span className="text-neutral-400">:{service.host_port} (stopped)</span>
            ) : (
              <span className="text-neutral-500">Unallocated</span>
            )
          ) : (
            <span
              className="inline-flex items-center gap-1 text-neutral-400 text-[11px]"
              title={`Internal mesh: ${internalEndpoint}`}
            >
              <Lock className="w-2.5 h-2.5 text-neutral-500 shrink-0" />
              <span className="truncate">Internal · {internalEndpoint}</span>
            </span>
          )}
        </div>
      </div>

      {/* Prominent Preview Banner for Running Public Services */}
      {service.public_exposed && isRunning && previewUrl && (
        <div className="flex items-center justify-between gap-2 px-3 py-2 rounded-md bg-emerald-500/10 border border-emerald-500/20 text-xs">
          <div className="flex items-center gap-2 min-w-0">
            <Radio className="w-3.5 h-3.5 text-emerald-400 shrink-0 animate-pulse" />
            <span className="text-neutral-300 text-[11px] font-sans truncate">
              Preview accessible at{' '}
              <span className="font-mono text-emerald-300 font-semibold">{previewUrl}</span>
            </span>
          </div>
          <a
            href={previewUrl}
            target="_blank"
            rel="noreferrer"
            className="inline-flex items-center gap-1.5 px-2.5 py-1 rounded bg-emerald-500 text-black text-xs font-semibold hover:bg-emerald-400 transition-colors shrink-0 shadow-sm"
          >
            <span>Preview</span>
            <ExternalLink className="w-3 h-3" />
          </a>
        </div>
      )}

      {/* Bottom Actions Bar */}
      <div className="flex flex-wrap items-center justify-between gap-2 pt-2 border-t border-surface-border/60">
        <div className="flex items-center gap-1.5 flex-wrap">
          {/* Independent Service Deploy Button */}
          <Button
            variant="primary"
            size="sm"
            onClick={() => onDeploy(service.id)}
            loading={isDeployPending}
            disabled={isDeployPending || isServiceDeploying || isAnyDeploying}
            className="h-7 text-xs px-2.5"
            icon={<Rocket className="w-3 h-3" />}
            title="Deploy only this service"
          >
            {isServiceDeploying ? 'Deploying...' : 'Deploy'}
          </Button>

          {/* Start / Stop / Restart Controls */}
          {isRunning ? (
            <>
              <Button
                variant="secondary"
                size="sm"
                onClick={() => onAction(service.id, 'stop')}
                loading={actionLoading === 'stop'}
                disabled={actionLoading !== null || isServiceDeploying}
                className="h-7 text-xs px-2.5"
                icon={<Square className="w-2.5 h-2.5" />}
              >
                Stop
              </Button>
              <Button
                variant="secondary"
                size="sm"
                onClick={() => onAction(service.id, 'restart')}
                loading={actionLoading === 'restart'}
                disabled={actionLoading !== null || isServiceDeploying}
                className="h-7 text-xs px-2.5"
                icon={<RotateCcw className="w-2.5 h-2.5" />}
              >
                Restart
              </Button>
            </>
          ) : (
            <Button
              variant="secondary"
              size="sm"
              onClick={() => onAction(service.id, 'start')}
              loading={actionLoading === 'start'}
              disabled={actionLoading !== null || isServiceDeploying}
              className="h-7 text-xs px-2.5"
              icon={<Play className="w-2.5 h-2.5" />}
            >
              Start
            </Button>
          )}
        </div>

        {/* View Logs Button */}
        <button
          type="button"
          onClick={() => onViewLogs(service.id)}
          className="text-xs text-neutral-400 hover:text-white font-mono flex items-center gap-1 transition-colors px-1 py-1 rounded"
        >
          <Terminal className="w-3 h-3 text-neutral-500" />
          <span>Logs →</span>
        </button>
      </div>
    </div>
  );
}
