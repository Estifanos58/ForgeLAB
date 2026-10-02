'use client';

import React, { useState } from 'react';
import { Service, UpdateServiceResourcesInput } from '@/lib/api/types';
import { Modal } from '@/components/ui/modal';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Alert } from '@/components/ui/alert';
import { api } from '@/lib/api/client';
import { Cpu, HardDrive, ShieldAlert, Sliders, Info, Hash } from 'lucide-react';

interface ResourceModalProps {
  isOpen: boolean;
  onClose: () => void;
  projectId: string;
  service: Service;
  onServiceUpdated?: (updated: Service) => void;
}

export function ResourceModal({
  isOpen,
  onClose,
  projectId,
  service,
  onServiceUpdated,
}: ResourceModalProps) {
  const [cpuMillicores, setCpuMillicores] = useState<number>(service.cpu_millicores || 1000);
  const [memoryMB, setMemoryMB] = useState<number>(service.memory_mb || 1024);
  const [pidsLimit, setPidsLimit] = useState<number>(service.pids_limit || 256);
  const [ephemeralStorageMB, setEphemeralStorageMB] = useState<string>(
    service.ephemeral_storage_mb ? String(service.ephemeral_storage_mb) : ''
  );

  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [successMsg, setSuccessMsg] = useState<string | null>(null);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setSaving(true);
    setError(null);
    setSuccessMsg(null);

    const input: UpdateServiceResourcesInput = {
      cpu_millicores: cpuMillicores,
      memory_mb: memoryMB,
      pids_limit: pidsLimit,
      ephemeral_storage_mb: ephemeralStorageMB.trim() === '' ? null : parseInt(ephemeralStorageMB, 10),
    };

    try {
      const updated = await api.services.updateResources(projectId, service.id, input);
      setSuccessMsg('Resource configuration updated successfully. Next deployment will apply these limits.');
      if (onServiceUpdated) {
        onServiceUpdated(updated);
      }
      setTimeout(() => {
        onClose();
      }, 1000);
    } catch (err: any) {
      setError(err.message || 'Failed to update resource limits');
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal
      isOpen={isOpen}
      onClose={onClose}
      title={`Configure Resources · ${service.name}`}
      description="Set hardware resource limits for this service's container runtime."
      maxWidth="md"
    >
      <form onSubmit={handleSubmit} className="space-y-4">
        {error && (
          <Alert variant="error" onClose={() => setError(null)}>
            {error}
          </Alert>
        )}
        {successMsg && (
          <Alert variant="success" onClose={() => setSuccessMsg(null)}>
            {successMsg}
          </Alert>
        )}

        {/* CPU Millicores */}
        <div className="space-y-1.5">
          <div className="flex items-center justify-between text-xs font-mono">
            <label className="text-neutral-300 font-semibold flex items-center gap-1.5">
              <Cpu className="w-3.5 h-3.5 text-blue-400" />
              <span>CPU Limit</span>
            </label>
            <span className="text-neutral-400">
              {(cpuMillicores / 1000).toFixed(2)} CPU Cores ({cpuMillicores}m)
            </span>
          </div>
          <Input
            type="number"
            min={100}
            max={64000}
            step={100}
            value={cpuMillicores}
            onChange={(e) => setCpuMillicores(parseInt(e.target.value, 10) || 100)}
            required
            className="font-mono text-xs"
          />
          <div className="flex items-center gap-1.5 pt-1">
            {[500, 1000, 2000, 4000].map((val) => (
              <button
                key={val}
                type="button"
                onClick={() => setCpuMillicores(val)}
                className={`text-[10px] font-mono px-2 py-0.5 rounded border transition-colors ${
                  cpuMillicores === val
                    ? 'border-blue-500 bg-blue-500/20 text-blue-300'
                    : 'border-surface-border text-neutral-400 hover:text-white hover:border-neutral-500'
                }`}
              >
                {val / 1000} Core{val >= 2000 ? 's' : ''} ({val}m)
              </button>
            ))}
          </div>
          <p className="text-[11px] text-neutral-500 font-sans">
            Configured in millicores (1000 millicores = 1 physical/virtual CPU core).
          </p>
        </div>

        {/* Memory (MB) */}
        <div className="space-y-1.5">
          <div className="flex items-center justify-between text-xs font-mono">
            <label className="text-neutral-300 font-semibold flex items-center gap-1.5">
              <Sliders className="w-3.5 h-3.5 text-emerald-400" />
              <span>Memory Limit</span>
            </label>
            <span className="text-neutral-400">
              {(memoryMB / 1024).toFixed(2)} GB ({memoryMB} MB)
            </span>
          </div>
          <Input
            type="number"
            min={64}
            max={524288}
            step={128}
            value={memoryMB}
            onChange={(e) => setMemoryMB(parseInt(e.target.value, 10) || 64)}
            required
            className="font-mono text-xs"
          />
          <div className="flex items-center gap-1.5 pt-1">
            {[512, 1024, 2048, 4096].map((val) => (
              <button
                key={val}
                type="button"
                onClick={() => setMemoryMB(val)}
                className={`text-[10px] font-mono px-2 py-0.5 rounded border transition-colors ${
                  memoryMB === val
                    ? 'border-emerald-500 bg-emerald-500/20 text-emerald-300'
                    : 'border-surface-border text-neutral-400 hover:text-white hover:border-neutral-500'
                }`}
              >
                {val >= 1024 ? `${val / 1024} GB` : `${val} MB`}
              </button>
            ))}
          </div>
          <p className="text-[11px] text-neutral-500 font-sans">
            Container memory allocation limit in megabytes.
          </p>
        </div>

        {/* PID Limit */}
        <div className="space-y-1.5">
          <div className="flex items-center justify-between text-xs font-mono">
            <label className="text-neutral-300 font-semibold flex items-center gap-1.5">
              <Hash className="w-3.5 h-3.5 text-amber-400" />
              <span>Max Process Limit (PIDs)</span>
            </label>
            <span className="text-neutral-400">{pidsLimit} PIDs</span>
          </div>
          <Input
            type="number"
            min={16}
            max={32768}
            step={16}
            value={pidsLimit}
            onChange={(e) => setPidsLimit(parseInt(e.target.value, 10) || 16)}
            required
            className="font-mono text-xs"
          />
          <p className="text-[11px] text-neutral-500 font-sans">
            Maximum number of concurrent processes inside the container to prevent fork-bombs and resource exhaustion.
          </p>
        </div>

        {/* Ephemeral Storage (MB) - Clearly labeled as informational */}
        <div className="space-y-1.5 rounded-md border border-neutral-800 bg-neutral-900/60 p-3">
          <div className="flex items-center justify-between text-xs font-mono">
            <label className="text-neutral-300 font-semibold flex items-center gap-1.5">
              <HardDrive className="w-3.5 h-3.5 text-purple-400" />
              <span>Ephemeral Storage Limit</span>
            </label>
            <span className="text-[10px] uppercase font-mono px-1.5 py-0.5 rounded bg-amber-500/10 text-amber-400 border border-amber-500/20">
              Informational Only
            </span>
          </div>
          <Input
            type="number"
            min={64}
            placeholder="e.g. 2048 (MB) - optional"
            value={ephemeralStorageMB}
            onChange={(e) => setEphemeralStorageMB(e.target.value)}
            className="font-mono text-xs"
          />

          <div className="flex items-start gap-2 pt-1 text-[11px] text-amber-300/80 leading-relaxed">
            <Info className="w-3.5 h-3.5 text-amber-400 shrink-0 mt-0.5" />
            <span>
              <strong>Not universally enforceable:</strong> Docker does not provide portable storage quotas across standard host filesystems without specialized xfs/ext4 quota configurations. This field is persisted for capacity documentation and snapshot tracking.
            </span>
          </div>
        </div>

        {/* Actions */}
        <div className="flex items-center justify-end gap-2 pt-2 border-t border-surface-border">
          <Button type="button" variant="secondary" size="sm" onClick={onClose} disabled={saving}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" size="sm" loading={saving} disabled={saving}>
            Save Resource Limits
          </Button>
        </div>
      </form>
    </Modal>
  );
}
