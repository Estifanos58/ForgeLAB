'use client';

import React, { useState } from 'react';
import { DeploymentMode, Service, ServiceDeployment } from '@/lib/api/types';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { api } from '@/lib/api/client';
import { ResourceModal } from './resource-modal';
import {
  ExternalLink,
  Lock,
  Play,
  Square,
  RotateCcw,
  Rocket,
  Terminal,
  Radio,
  History,
  ChevronDown,
  ChevronUp,
  Clock,
  Undo2,
  Cpu,
  Sliders,
  HardDrive,
  ArrowLeftRight,
} from 'lucide-react';
import { cn } from '@/lib/utils/cn';

interface ServiceCardProps {
  projectId: string;
  service: Service;
  isDeployPending?: boolean;
  isRollbackPending?: boolean;
  actionLoading?: string | null;
  onDeploy: (serviceId: string) => void;
  onRollback?: (serviceId: string) => void;
  onAction: (serviceId: string, action: 'start' | 'stop' | 'restart') => void;
  onViewLogs: (serviceId: string) => void;
  onSelectServiceDeployment?: (serviceDeployment: ServiceDeployment, service: Service) => void;
  selectedServiceDeploymentId?: string | null;
  onServiceUpdated?: (updated: Service) => void;
}

export function ServiceCard({
  projectId,
  service,
  isDeployPending = false,
  isRollbackPending = false,
  actionLoading = null,
  onDeploy,
  onRollback,
  onAction,
  onViewLogs,
  onSelectServiceDeployment,
  selectedServiceDeploymentId,
  onServiceUpdated,
}: ServiceCardProps) {
  const [showResourceModal, setShowResourceModal] = useState(false);
  const [showHistory, setShowHistory] = useState(false);
  const [historyLoading, setHistoryLoading] = useState(false);
  const [historyDeployments, setHistoryDeployments] = useState<ServiceDeployment[]>([]);
  const [historyError, setHistoryError] = useState<string | null>(null);
  const [modeUpdating, setModeUpdating] = useState(false);
  const [modeError, setModeError] = useState<string | null>(null);

  const isFrontend = service.role === 'frontend';
  const isBackend = service.role === 'backend';
  const isJob = service.classification === 'job';
  const isRunning = service.status === 'running' || service.status === 'degraded';

  const isServiceDeploying = [
    'deploying',
    'building',
    'starting',
    'health_checking',
    'cloning',
    'queued',
  ].includes(service.status);

  // Status label for active deployment phases
  const getDeployingLabel = () => {
    switch (service.status) {
      case 'cloning':
        return 'Cloning...';
      case 'building':
        return 'Building...';
      case 'starting':
        return 'Starting...';
      case 'health_checking':
        return 'Health Checking...';
      case 'queued':
        return 'Queued...';
      default:
        return 'Deploying...';
    }
  };
  const isPublicOrExposed = Boolean(
    service.public_exposed || service.role === 'frontend' || service.preview_url
  );
  // Authoritative preview URL from backend when confirmed running
  const previewUrl = isRunning ? service.preview_url || null : null;

  const wasServiceDeployed = Boolean(
    service.current_service_deployment_id ||
      service.status === 'stopped' ||
      service.status === 'failed' || service.status === 'degraded'
  );

  const internalEndpoint = `${service.name}:${service.internal_port}`;

  const loadHistory = async () => {
    setHistoryLoading(true);
    setHistoryError(null);
    try {
      const data = await api.services.listDeployments(projectId, service.id);
      setHistoryDeployments(data);
    } catch (err: any) {
      setHistoryError(err.message || 'Failed to load deployment history');
    } finally {
      setHistoryLoading(false);
    }
  };

  const handleToggleHistory = () => {
    if (!showHistory && historyDeployments.length === 0) {
      loadHistory();
    }
    setShowHistory((prev) => !prev);
  };

  const currentMode: DeploymentMode = service.deployment_mode === 'blue_green' ? 'blue_green' : 'standard';
  const hasVolumes = Boolean(service.volumes && service.volumes.length > 0);

  const handleChangeDeploymentMode = async (nextMode: DeploymentMode) => {
    if (nextMode === currentMode || modeUpdating || isJob) return;
    setModeUpdating(true);
    setModeError(null);
    try {
      const updated = await api.services.updateDeploymentMode(projectId, service.id, nextMode);
      onServiceUpdated?.(updated);
    } catch (err: any) {
      setModeError(err.message || 'Failed to update deployment mode');
    } finally {
      setModeUpdating(false);
    }
  };

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
        <div className="flex items-center gap-2 shrink-0">
          {service.classification === 'job' && service.status === 'stopped' ? (
            <Badge status="completed" variant="success">
              Completed
            </Badge>
          ) : (
            <Badge status={service.status} />
          )}
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
          <span className="text-[10px] text-neutral-500 block uppercase tracking-wider">Source Path</span>
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
            {isPublicOrExposed ? 'Public Endpoint' : 'Internal Mesh Endpoint'}
          </span>
          {isPublicOrExposed ? (
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

      {/* Resource Limits Row with Visual Allocation Bars & Edit Button */}
      <div className="space-y-2 px-2.5 py-2 rounded bg-surface/50 border border-surface-border/50 text-xs font-mono">
        <div className="flex items-center justify-between gap-2">
          <span className="text-[10px] text-neutral-500 uppercase tracking-wider">
            Allocated Container Quotas
          </span>
          <button
            type="button"
            onClick={() => setShowResourceModal(true)}
            className="text-[11px] text-neutral-400 hover:text-white flex items-center gap-1 px-1.5 py-0.5 rounded hover:bg-surface-elevated transition-colors border border-transparent hover:border-surface-border font-mono"
            title="Configure CPU, Memory, PID, and ephemeral storage limits"
          >
            <Sliders className="w-3 h-3 text-neutral-400" />
            <span>Edit Limits</span>
          </button>
        </div>

        <div className="grid grid-cols-1 sm:grid-cols-3 gap-2">
          <div className="space-y-1" title="Configured CPU allocation">
            <div className="flex items-center justify-between text-[10px] text-neutral-300">
              <span className="inline-flex items-center gap-1">
                <Cpu className="w-3 h-3 text-blue-400" />
                CPU
              </span>
              <span className="text-neutral-400">
                {service.cpu_millicores ? `${(service.cpu_millicores / 1000).toFixed(1)}c` : '1.0c'}
              </span>
            </div>
            <div className="h-1.5 w-full rounded-full bg-surface-border/60 overflow-hidden">
              <div
                className="h-full rounded-full bg-blue-400"
                style={{ width: `${Math.min(100, Math.max(8, ((service.cpu_millicores || 1000) / 4000) * 100))}%` }}
              />
            </div>
          </div>

          <div className="space-y-1" title="Configured Memory allocation">
            <div className="flex items-center justify-between text-[10px] text-neutral-300">
              <span className="inline-flex items-center gap-1">
                <Sliders className="w-3 h-3 text-emerald-400" />
                Memory
              </span>
              <span className="text-neutral-400">
                {service.memory_mb ? `${service.memory_mb} MB` : '1024 MB'}
              </span>
            </div>
            <div className="h-1.5 w-full rounded-full bg-surface-border/60 overflow-hidden">
              <div
                className="h-full rounded-full bg-emerald-400"
                style={{ width: `${Math.min(100, Math.max(8, ((service.memory_mb || 1024) / 4096) * 100))}%` }}
              />
            </div>
          </div>

          <div className="space-y-1" title="Process limit">
            <div className="flex items-center justify-between text-[10px] text-neutral-300">
              <span className="inline-flex items-center gap-1">
                <span className="text-amber-400 font-bold text-[10px]">#</span>
                PIDs
              </span>
              <span className="text-neutral-400">{service.pids_limit || 256}</span>
            </div>
            <div className="h-1.5 w-full rounded-full bg-surface-border/60 overflow-hidden">
              <div
                className="h-full rounded-full bg-amber-400"
                style={{ width: `${Math.min(100, Math.max(8, ((service.pids_limit || 256) / 1024) * 100))}%` }}
              />
            </div>
          </div>
        </div>

        {service.ephemeral_storage_mb && (
          <div className="flex items-center gap-1.5 text-[10px] text-neutral-400 pt-0.5" title="Informational ephemeral storage quota">
            <HardDrive className="w-3 h-3 text-purple-400" />
            <span>Ephemeral quota (info): {service.ephemeral_storage_mb} MB</span>
          </div>
        )}
      </div>

      {/* Deployment Mode Selector */}
      {!isJob && (
        <div className="space-y-1">
          <div className="flex items-center justify-between gap-2 px-2.5 py-1.5 rounded bg-surface/50 border border-surface-border/50 text-xs font-mono">
            <div className="flex items-center gap-1.5 text-[11px] text-neutral-400 min-w-0">
              <ArrowLeftRight className="w-3 h-3 text-sky-400 shrink-0" />
              <span>Deploy Strategy</span>
              {hasVolumes && currentMode === 'blue_green' && (
                <span
                  className="text-[10px] text-amber-400 truncate"
                  title="Stateful services with writable volumes safely fall back to recreate during deployment"
                >
                  (volume fallback)
                </span>
              )}
            </div>
            <div className="inline-flex rounded border border-surface-border bg-surface p-0.5 shrink-0">
              <button
                type="button"
                disabled={modeUpdating || isServiceDeploying}
                onClick={() => handleChangeDeploymentMode('standard')}
                className={cn(
                  'px-2 py-0.5 rounded text-[10px] font-mono transition-colors disabled:opacity-50',
                  currentMode === 'standard'
                    ? 'bg-surface-elevated text-white font-semibold'
                    : 'text-neutral-400 hover:text-neutral-200'
                )}
              >
                Standard
              </button>
              <button
                type="button"
                disabled={modeUpdating || isServiceDeploying}
                onClick={() => handleChangeDeploymentMode('blue_green')}
                title="Start healthy green container before switching traffic and draining old container"
                className={cn(
                  'px-2 py-0.5 rounded text-[10px] font-mono transition-colors disabled:opacity-50',
                  currentMode === 'blue_green'
                    ? 'bg-sky-500/20 text-sky-300 font-semibold border border-sky-500/30'
                    : 'text-neutral-400 hover:text-neutral-200'
                )}
              >
                {modeUpdating && currentMode !== 'blue_green' ? 'Saving...' : 'Blue-Green'}
              </button>
            </div>
          </div>
          {modeError && (
            <div className="text-[11px] text-red-400 px-1">{modeError}</div>
          )}
        </div>
      )}

      {/* Prominent Preview Banner for Running Public Services */}
      {isPublicOrExposed && isRunning && previewUrl && (
        <div className="flex items-center justify-between gap-2 px-3 py-2 rounded-md bg-emerald-500/10 border border-emerald-500/20 text-xs">
          <div className="flex items-center gap-2 min-w-0">
            <Radio className="w-3.5 h-3.5 text-emerald-400 shrink-0 animate-pulse" />
            <span className="text-neutral-300 text-[11px] font-sans truncate">
              Preview live at{' '}
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
          {/* Independent Service Deploy / Redeploy Button - Never blocked by other services */}
          <Button
            variant="primary"
            size="sm"
            onClick={() => onDeploy(service.id)}
            loading={isDeployPending}
            disabled={isDeployPending || isServiceDeploying}
            className="h-7 text-xs px-2.5"
            icon={wasServiceDeployed && (!isRunning || service.status === 'degraded') ? <RotateCcw className="w-3 h-3" /> : <Rocket className="w-3 h-3" />}
            title={isJob ? (wasServiceDeployed ? 'Re-run this job' : 'Run this job') : wasServiceDeployed && (!isRunning || service.status === 'degraded') ? 'Redeploy this service' : 'Deploy only this service concurrently'}
          >
            {isServiceDeploying
              ? getDeployingLabel()
              : isJob
              ? wasServiceDeployed
                ? 'Re-run Job'
                : 'Run Job'
              : wasServiceDeployed && (!isRunning || service.status === 'degraded')
              ? 'Redeploy'
              : 'Deploy Service'}
          </Button>

          {/* Service-Specific Rollback */}
          {onRollback && (
            <Button
              variant="secondary"
              size="sm"
              onClick={() => onRollback(service.id)}
              loading={isRollbackPending}
              disabled={isRollbackPending || isServiceDeploying}
              className="h-7 text-xs px-2.5"
              icon={<Undo2 className="w-3 h-3" />}
              title="Roll back only this service to its previous successful deployment"
            >
              Rollback
            </Button>
          )}

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
          ) : service.status === 'stopped' && service.container_id ? (
            <Button
              variant="secondary"
              size="sm"
              onClick={() => onAction(service.id, 'start')}
              loading={actionLoading === 'start'}
              disabled={actionLoading !== null || isServiceDeploying}
              className="h-7 text-xs px-2.5"
              icon={<Play className="w-2.5 h-2.5" />}
              title="Start existing stopped container"
            >
              Start
            </Button>
          ) : null}
        </div>

        {/* View Logs & History Buttons */}
        <div className="flex items-center gap-2">
          <button
            type="button"
            onClick={handleToggleHistory}
            className="text-xs text-neutral-400 hover:text-white font-mono flex items-center gap-1 transition-colors px-1 py-1 rounded"
            title="View service deployment history"
          >
            <History className="w-3 h-3 text-neutral-400" />
            <span>History</span>
            {showHistory ? <ChevronUp className="w-3 h-3" /> : <ChevronDown className="w-3 h-3" />}
          </button>

          <button
            type="button"
            onClick={() => onViewLogs(service.id)}
            className="text-xs text-neutral-400 hover:text-white font-mono flex items-center gap-1 transition-colors px-1 py-1 rounded"
          >
            <Terminal className="w-3 h-3 text-neutral-400" />
            <span>Logs →</span>
          </button>
        </div>
      </div>

      {/* Collapsible Service Deployment History */}
      {showHistory && (
        <div className="pt-2 border-t border-surface-border/50 space-y-2">
          <div className="flex items-center justify-between text-[11px] font-mono text-neutral-400">
            <span className="uppercase tracking-wider font-semibold">Deployment History</span>
            <button
              type="button"
              onClick={loadHistory}
              disabled={historyLoading}
              className="text-emerald-400 hover:underline flex items-center gap-1"
            >
              <RotateCcw className={cn('w-2.5 h-2.5', historyLoading && 'animate-spin')} />
              <span>Refresh</span>
            </button>
          </div>

          {historyLoading ? (
            <div className="text-xs font-mono text-neutral-500 py-2 text-center">Loading history...</div>
          ) : historyError ? (
            <div className="text-xs text-red-400 py-1">{historyError}</div>
          ) : historyDeployments.length === 0 ? (
            <div className="text-xs font-mono text-neutral-500 py-2 text-center">No service deployments yet.</div>
          ) : (
            <div className="space-y-1.5 max-h-48 overflow-y-auto pr-1">
              {historyDeployments.map((sd) => {
                const isCurrent = service.current_service_deployment_id === sd.id;
                const isSelected = selectedServiceDeploymentId === sd.id;
                return (
                  <button
                    key={sd.id}
                    type="button"
                    onClick={() => {
                      if (onSelectServiceDeployment) {
                        onSelectServiceDeployment(sd, service);
                      } else {
                        onViewLogs(service.id);
                      }
                    }}
                    className={cn(
                      'w-full text-left flex flex-col p-2.5 rounded text-xs font-mono border transition-colors cursor-pointer hover:border-neutral-500 gap-1.5',
                      isSelected
                        ? 'bg-emerald-500/20 border-emerald-500 text-white shadow-sm'
                        : isCurrent
                        ? 'bg-emerald-500/10 border-emerald-500/30 text-white'
                        : 'bg-surface/50 border-surface-border/40 text-neutral-300'
                    )}
                    title={`Select deployment #${sd.deploy_number} logs`}
                  >
                    <div className="flex items-center justify-between w-full">
                      <div className="flex items-center gap-2 min-w-0">
                        <span className="font-bold text-neutral-200 shrink-0">#{sd.deploy_number}</span>
                        <Badge status={sd.status} showDot={false} className="py-0 px-1.5 text-[10px]" />
                        {isCurrent && (
                          <span className="px-1.5 py-0.2 rounded bg-emerald-500/20 text-emerald-300 text-[9px] uppercase tracking-wider font-bold">
                            Current
                          </span>
                        )}
                        {sd.failure_reason && (
                          <span className="text-red-400 text-[10px] truncate max-w-[140px]" title={sd.failure_reason}>
                            {sd.failure_reason}
                          </span>
                        )}
                      </div>

                      <div className="flex items-center gap-2 shrink-0 text-[11px] text-neutral-400">
                        {sd.duration_ms && (
                          <span className="flex items-center gap-0.5 text-neutral-500 text-[10px]">
                            <Clock className="w-2.5 h-2.5" />
                            <span>{(sd.duration_ms / 1000).toFixed(1)}s</span>
                          </span>
                        )}
                        <span>
                          {sd.created_at ? new Date(sd.created_at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) : ''}
                        </span>
                      </div>
                    </div>

                    {/* Snapshot Metadata: Resources, Digest, Revision, Env Config Hash */}
                    <div className="flex items-center gap-2 flex-wrap text-[10px] text-neutral-400 pt-1 border-t border-surface-border/30">
                      <span>{sd.cpu_millicores || 1000}m · {sd.memory_mb || 1024}MB · {sd.pids_limit || 256}pids</span>
                      {sd.ephemeral_storage_mb && (
                        <span>· {sd.ephemeral_storage_mb}MB (info)</span>
                      )}
                      {sd.source_revision && (
                        <span className="px-1 py-0.2 rounded bg-neutral-800 text-neutral-300" title={`Source revision: ${sd.source_revision}`}>
                          rev: {sd.source_revision.slice(0, 10)}
                        </span>
                      )}
                      {sd.env_config_hash && (
                        <span className="px-1 py-0.2 rounded bg-neutral-800 text-teal-300 font-mono" title={`Env snapshot hash: ${sd.env_config_hash}`}>
                          env: #{sd.env_config_hash.slice(0, 7)}
                        </span>
                      )}
                      {sd.image_digest && (
                        <span className="px-1 py-0.2 rounded bg-neutral-800 text-neutral-400 font-mono" title={`Image digest: ${sd.image_digest}`}>
                          digest: {sd.image_digest.slice(0, 16)}...
                        </span>
                      )}
                    </div>
                  </button>
                );
              })}
            </div>
          )}
        </div>
      )}

      {/* Resource Configuration Modal */}
      {showResourceModal && (
        <ResourceModal
          isOpen={showResourceModal}
          onClose={() => setShowResourceModal(false)}
          projectId={projectId}
          service={service}
          onServiceUpdated={onServiceUpdated}
        />
      )}
    </div>
  );
}
