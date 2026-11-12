'use client';

import React, { useCallback, useEffect, useRef, useState } from 'react';
import { api } from '@/lib/api/client';
import { ProjectVolumeInfo, ProjectVolumesResponse } from '@/lib/api/types';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Alert } from '@/components/ui/alert';
import { formatBytes } from '@/lib/utils/format';
import {
  AlertTriangle,
  Archive,
  CheckCircle2,
  Database,
  Download,
  HardDrive,
  Layers,
  RefreshCw,
  Upload,
} from 'lucide-react';

interface VolumesPanelProps {
  projectId: string;
}

export function VolumesPanel({ projectId }: VolumesPanelProps) {
  const [data, setData] = useState<ProjectVolumesResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [statusMessage, setStatusMessage] = useState<string | null>(null);
  const [backingUpVolume, setBackingUpVolume] = useState<string | null>(null);
  const [restoringVolume, setRestoringVolume] = useState<string | null>(null);
  const fileInputRefs = useRef<Record<string, HTMLInputElement | null>>({});

  const loadVolumes = useCallback(async () => {
    try {
      setError(null);
      const res = await api.projects.getVolumes(projectId);
      setData(res);
    } catch (err: any) {
      setError(err?.message || 'Failed to load persistent volumes and disk usage');
    } finally {
      setLoading(false);
    }
  }, [projectId]);

  useEffect(() => {
    setLoading(true);
    loadVolumes();
  }, [loadVolumes]);

  const handleBackup = async (vol: ProjectVolumeInfo) => {
    setBackingUpVolume(vol.name);
    setError(null);
    setStatusMessage(null);
    try {
      const { blob, filename } = await api.projects.backupVolume(projectId, vol.name);
      const downloadName = filename || `${vol.name}-backup.tar.gz`;
      const objectUrl = URL.createObjectURL(blob);
      try {
        const anchor = document.createElement('a');
        anchor.href = objectUrl;
        anchor.download = downloadName;
        document.body.appendChild(anchor);
        anchor.click();
        anchor.remove();
      } finally {
        URL.revokeObjectURL(objectUrl);
      }
      setStatusMessage(`Downloaded archive ${downloadName} for volume "${vol.name}".`);
    } catch (err: any) {
      setError(err?.message || `Failed to back up volume "${vol.name}"`);
    } finally {
      setBackingUpVolume(null);
    }
  };

  const handleSelectRestoreFile = async (
    vol: ProjectVolumeInfo,
    e: React.ChangeEvent<HTMLInputElement>
  ) => {
    const file = e.target.files?.[0];
    e.target.value = '';
    if (!file) return;

    if (vol.in_use) {
      setError(`Volume "${vol.name}" is mounted by a running container. Stop the service before restoring.`);
      return;
    }

    const confirmed = window.confirm(
      `Restore archive "${file.name}" into volume "${vol.name}"?\n\nExisting files inside "${vol.docker_name}" will be replaced with the contents of this .tar.gz archive.`
    );
    if (!confirmed) return;

    setRestoringVolume(vol.name);
    setError(null);
    setStatusMessage(null);
    try {
      const result = await api.projects.restoreVolume(projectId, vol.name, file);
      setStatusMessage(
        `${result.message} (${result.files_restored} files, ${formatBytes(result.bytes_restored)} restored).`
      );
      await loadVolumes();
    } catch (err: any) {
      setError(err?.message || `Failed to restore volume "${vol.name}"`);
    } finally {
      setRestoringVolume(null);
    }
  };

  if (loading && !data) {
    return (
      <div className="flex items-center justify-center p-8 text-xs text-neutral-400 gap-2">
        <RefreshCw className="w-4 h-4 animate-spin" />
        <span>Inspecting persistent volumes and Docker Engine disk usage...</span>
      </div>
    );
  }

  const volumes = data?.volumes || [];
  const disk = data?.disk_usage;

  return (
    <div className="space-y-4">
      <div className="rounded-md border border-surface-border bg-surface p-4 sm:p-5 space-y-4">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="flex items-center gap-2.5">
            <HardDrive className="w-4 h-4 text-emerald-400" />
            <div>
              <h3 className="text-sm font-semibold text-white">Persistent Volumes &amp; Disk Usage</h3>
              <p className="text-[11px] text-neutral-400">
                Inspect project storage, download offline .tar.gz backups, and restore stopped volumes safely.
              </p>
            </div>
          </div>
          <Button
            variant="secondary"
            size="sm"
            onClick={loadVolumes}
            disabled={loading}
            className="h-7 text-xs px-2.5"
            icon={<RefreshCw className={`w-3 h-3 ${loading ? 'animate-spin' : ''}`} />}
          >
            Refresh
          </Button>
        </div>

        {error && (
          <Alert variant="error" className="text-xs">
            <div className="flex items-center gap-2">
              <AlertTriangle className="w-3.5 h-3.5 shrink-0" />
              <span>{error}</span>
            </div>
          </Alert>
        )}

        {statusMessage && (
          <Alert variant="success" className="text-xs">
            <div className="flex items-center gap-2">
              <CheckCircle2 className="w-3.5 h-3.5 shrink-0" />
              <span>{statusMessage}</span>
            </div>
          </Alert>
        )}

        {disk?.warning && (
          <Alert variant="warning" className="text-xs">
            <div className="flex items-center gap-2">
              <AlertTriangle className="w-3.5 h-3.5 shrink-0" />
              <span>{disk.warning}</span>
            </div>
          </Alert>
        )}

        {disk && (
          <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-5 gap-2.5 text-xs font-mono">
            <div className="p-3 rounded bg-surface-elevated/40 border border-surface-border space-y-1">
              <span className="text-[10px] text-neutral-400 uppercase block">Project Volumes</span>
              <span className="text-base font-bold text-white block">
                {formatBytes(disk.project_volumes_bytes)}
              </span>
              <span className="text-[10px] text-neutral-500 block">
                {disk.project_volumes_count} volume{disk.project_volumes_count === 1 ? '' : 's'}
              </span>
            </div>

            <div className="p-3 rounded bg-surface-elevated/40 border border-surface-border space-y-1">
              <span className="text-[10px] text-neutral-400 uppercase block">All Containers</span>
              <span className="text-base font-bold text-white block">
                {formatBytes(disk.containers.total_bytes)}
              </span>
              <span className="text-[10px] text-neutral-500 block">
                {disk.containers.active_count}/{disk.containers.count} active ·{' '}
                {formatBytes(disk.containers.reclaimable_bytes)} reclaimable
              </span>
            </div>

            <div className="p-3 rounded bg-surface-elevated/40 border border-surface-border space-y-1">
              <span className="text-[10px] text-neutral-400 uppercase block">Images</span>
              <span className="text-base font-bold text-white block">
                {formatBytes(disk.images.total_bytes)}
              </span>
              <span className="text-[10px] text-neutral-500 block">
                {disk.images.active_count}/{disk.images.count} in use ·{' '}
                {formatBytes(disk.images.reclaimable_bytes)} reclaimable
              </span>
            </div>

            <div className="p-3 rounded bg-surface-elevated/40 border border-surface-border space-y-1">
              <span className="text-[10px] text-neutral-400 uppercase block">Build Cache</span>
              <span className="text-base font-bold text-white block">
                {formatBytes(disk.build_cache.total_bytes)}
              </span>
              <span className="text-[10px] text-neutral-500 block">
                {disk.build_cache.count} entries ·{' '}
                {formatBytes(disk.build_cache.reclaimable_bytes)} reclaimable
              </span>
            </div>

            <div className="p-3 rounded bg-surface-elevated/40 border border-surface-border space-y-1">
              <span className="text-[10px] text-neutral-400 uppercase block">Engine Total</span>
              <span className="text-base font-bold text-white block">
                {formatBytes(disk.total_bytes)}
              </span>
              <span className="text-[10px] text-neutral-500 block">
                {formatBytes(disk.total_reclaimable_bytes)} reclaimable
              </span>
            </div>
          </div>
        )}
      </div>

      <div className="rounded-md border border-surface-border bg-surface p-4 sm:p-5 space-y-3">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-2 text-xs font-semibold text-white">
            <Database className="w-3.5 h-3.5 text-sky-400" />
            <span>Project Persistent Volumes ({volumes.length})</span>
          </div>
          <span className="text-[11px] font-mono text-neutral-500">
            Archives are validated against traversal &amp; device nodes before restore
          </span>
        </div>

        {volumes.length === 0 ? (
          <div className="p-6 rounded border border-dashed border-surface-border text-center text-xs text-neutral-400 space-y-1">
            <Archive className="w-5 h-5 mx-auto text-neutral-500" />
            <p>No named persistent volumes declared or created for this project yet.</p>
          </div>
        ) : (
          <div className="space-y-3">
            {volumes.map((vol) => {
              const isBackingUp = backingUpVolume === vol.name;
              const isRestoring = restoringVolume === vol.name;
              const restoreDisabled =
                !data?.docker_available || vol.in_use || isBackingUp || isRestoring;

              return (
                <div
                  key={vol.docker_name}
                  className="rounded-lg border border-surface-border bg-surface-elevated/35 p-4 space-y-3"
                >
                  <div className="flex flex-wrap items-start justify-between gap-3">
                    <div className="space-y-1 min-w-0">
                      <div className="flex flex-wrap items-center gap-2">
                        <span className="text-sm font-semibold text-white font-mono">
                          {vol.name}
                        </span>
                        <Badge variant={vol.in_use ? 'success' : vol.backup_ready ? 'neutral' : 'warning'}>
                          {vol.in_use ? 'Mounted (Running)' : vol.backup_ready ? 'Idle' : 'Not Provisioned'}
                        </Badge>
                        <span className="px-1.5 py-0.5 rounded bg-surface border border-surface-border text-[10px] font-mono text-neutral-400">
                          {vol.driver || 'local'}
                        </span>
                      </div>
                      <div className="text-[11px] font-mono text-neutral-500 truncate">
                        Docker volume: {vol.docker_name}
                      </div>
                    </div>

                    <div className="flex flex-wrap items-center gap-2">
                      <Button
                        variant="secondary"
                        size="sm"
                        onClick={() => handleBackup(vol)}
                        disabled={!data?.docker_available || !vol.backup_ready || isBackingUp || isRestoring}
                        loading={isBackingUp}
                        className="h-7 text-xs px-2.5"
                        icon={<Download className="w-3 h-3" />}
                      >
                        Backup .tar.gz
                      </Button>

                      <input
                        ref={(el) => {
                          fileInputRefs.current[vol.name] = el;
                        }}
                        type="file"
                        accept=".tar.gz,.tgz,application/gzip,application/x-gzip"
                        className="hidden"
                        onChange={(e) => handleSelectRestoreFile(vol, e)}
                      />

                      <Button
                        variant="secondary"
                        size="sm"
                        onClick={() => fileInputRefs.current[vol.name]?.click()}
                        disabled={restoreDisabled}
                        loading={isRestoring}
                        title={
                          vol.in_use
                            ? 'Stop mounted service(s) before restoring this volume'
                            : 'Upload a .tar.gz archive to restore this volume'
                        }
                        className="h-7 text-xs px-2.5"
                        icon={<Upload className="w-3 h-3" />}
                      >
                        Restore
                      </Button>
                    </div>
                  </div>

                  <div className="grid grid-cols-2 sm:grid-cols-4 gap-2 text-xs font-mono">
                    <div className="p-2 rounded bg-surface/60 border border-surface-border/60">
                      <span className="text-[10px] text-neutral-500 block uppercase">Size</span>
                      <span className="text-white">
                        {vol.size_bytes >= 0 ? formatBytes(vol.size_bytes) : 'Pending'}
                      </span>
                    </div>
                    <div className="p-2 rounded bg-surface/60 border border-surface-border/60">
                      <span className="text-[10px] text-neutral-500 block uppercase">Container Refs</span>
                      <span className="text-white">{vol.ref_count >= 0 ? vol.ref_count : vol.mounts.length}</span>
                    </div>
                    <div className="p-2 rounded bg-surface/60 border border-surface-border/60 sm:col-span-2">
                      <span className="text-[10px] text-neutral-500 block uppercase">Restore Guard</span>
                      <span className={vol.in_use ? 'text-amber-300' : 'text-emerald-400'}>
                        {vol.in_use
                          ? 'Stop service(s) to unlock restore'
                          : 'Safe to restore offline'}
                      </span>
                    </div>
                  </div>

                  {vol.mounts.length > 0 && (
                    <div className="space-y-1.5">
                      <div className="flex items-center gap-1.5 text-[11px] font-mono text-neutral-400">
                        <Layers className="w-3 h-3" />
                        <span>Service Mounts</span>
                      </div>
                      <div className="flex flex-wrap gap-1.5">
                        {vol.mounts.map((m, idx) => (
                          <div
                            key={`${m.service_name}-${m.target}-${idx}`}
                            className="inline-flex items-center gap-1.5 px-2 py-1 rounded bg-surface border border-surface-border text-[11px] font-mono text-neutral-300"
                          >
                            <span
                              className={`w-1.5 h-1.5 rounded-full ${
                                m.running ? 'bg-emerald-400' : 'bg-neutral-500'
                              }`}
                            />
                            <span className="text-white">{m.service_name}</span>
                            <span className="text-neutral-500">-&gt;</span>
                            <span>{m.target}</span>
                            {m.read_only && (
                              <span className="text-[10px] text-amber-300">:ro</span>
                            )}
                          </div>
                        ))}
                      </div>
                    </div>
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
