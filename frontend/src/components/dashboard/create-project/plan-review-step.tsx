'use client';

import React from 'react';
import { DeploymentPlan } from '@/lib/api/types';
import { Button } from '@/components/ui/button';
import { cn } from '@/lib/utils/cn';
import {
  GitCommit,
  Network,
  HardDrive,
  Workflow,
  ArrowRight,
  ArrowLeft,
  CheckCircle2,
  FileText,
} from 'lucide-react';

interface PlanReviewStepProps {
  deploymentPlan: DeploymentPlan;
  setDeploymentPlan: React.Dispatch<React.SetStateAction<DeploymentPlan | null>>;
  loading: boolean;
  onBack: () => void;
  onCancel: () => void;
  onDeploy: (e: React.FormEvent) => void;
}

export function PlanReviewStep({
  deploymentPlan,
  setDeploymentPlan,
  loading,
  onBack,
  onCancel,
  onDeploy,
}: PlanReviewStepProps) {
  return (
    <div className="space-y-5">
      {/* Header Card: Strategy & Revision */}
      <div className="p-4 rounded-lg bg-surface-elevated/70 border border-surface-border space-y-3">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex items-center gap-2">
            <span className="text-xs text-neutral-400 font-medium">Deployment Strategy:</span>
            <span
              className={cn(
                'px-2.5 py-0.5 rounded-full text-xs font-semibold uppercase tracking-wider border',
                deploymentPlan.strategy === 'compose'
                  ? 'bg-emerald-500/10 text-emerald-400 border-emerald-500/30'
                  : deploymentPlan.strategy === 'dockerfile'
                  ? 'bg-sky-500/10 text-sky-400 border-sky-500/30'
                  : 'bg-amber-500/10 text-amber-400 border-amber-500/30'
              )}
            >
              {deploymentPlan.strategy === 'compose'
                ? 'Docker Compose'
                : deploymentPlan.strategy === 'dockerfile'
                ? 'Dockerfile'
                : 'ForgeLAB Auto'}
            </span>
            <span className="text-xs text-neutral-500 font-mono">({deploymentPlan.topology})</span>
          </div>

          <div className="flex items-center gap-1.5 text-xs text-neutral-300 font-mono bg-black/40 px-2.5 py-1 rounded border border-neutral-800">
            <GitCommit className="w-3.5 h-3.5 text-neutral-400" />
            <span className="text-neutral-500">Revision:</span>
            <span className="text-primary-light font-semibold">
              {deploymentPlan.source_revision ? deploymentPlan.source_revision.substring(0, 8) : 'pinned'}
            </span>
          </div>
        </div>

        {/* Network & Volumes info */}
        <div className="flex flex-wrap items-center gap-4 text-xs text-neutral-400 pt-2 border-t border-surface-border/50">
          <div className="flex items-center gap-1.5">
            <Network className="w-3.5 h-3.5 text-emerald-400" />
            <span>Isolated Network:</span>
            <span className="font-mono text-neutral-300">{deploymentPlan.networks?.[0] || 'forgelab-net'}</span>
          </div>
          {deploymentPlan.volumes && deploymentPlan.volumes.length > 0 && (
            <div className="flex items-center gap-1.5">
              <HardDrive className="w-3.5 h-3.5 text-amber-400" />
              <span>Named Volumes:</span>
              <span className="font-mono text-neutral-300">{deploymentPlan.volumes.length} persistent</span>
            </div>
          )}
        </div>
      </div>

      {/* Execution Tiers / DAG */}
      {deploymentPlan.execution_tiers && deploymentPlan.execution_tiers.length > 0 && (
        <div className="p-4 rounded-lg bg-surface-elevated/40 border border-surface-border space-y-3">
          <div className="flex items-center gap-2 text-xs font-semibold text-white">
            <Workflow className="w-4 h-4 text-primary-light" />
            <span>Dependency Execution Stages (DAG)</span>
          </div>
          <p className="text-[11px] text-neutral-400">
            ForgeLAB enforces strict dependency startup ordering. Each tier must achieve healthy status before dependent tiers are launched.
          </p>
          <div className="flex flex-wrap items-center gap-2 pt-1">
            {deploymentPlan.execution_tiers.map((tier, idx) => (
              <React.Fragment key={idx}>
                <div className="flex items-center gap-1.5 px-3 py-1.5 rounded-md bg-surface border border-surface-border text-xs font-mono">
                  <span className="text-primary-light font-bold">Tier {idx + 1}:</span>
                  <span className="text-neutral-200">{tier.join(', ')}</span>
                </div>
                {idx < deploymentPlan.execution_tiers.length - 1 && (
                  <ArrowRight className="w-3.5 h-3.5 text-neutral-500" />
                )}
              </React.Fragment>
            ))}
          </div>
        </div>
      )}

      {/* Services List */}
      <div className="space-y-3">
        <div className="flex items-center justify-between">
          <span className="text-xs font-semibold text-white uppercase tracking-wider">
            Planned Services ({deploymentPlan.services.length})
          </span>
          <span className="text-[11px] text-neutral-400">
            Independent containers & logs per service
          </span>
        </div>

        <div className="space-y-2">
          {deploymentPlan.services.map((svc, idx) => (
            <div
              key={svc.name}
              className="p-3.5 rounded-lg bg-surface border border-surface-border space-y-2.5 text-xs"
            >
              <div className="flex flex-wrap items-center justify-between gap-2">
                <div className="flex items-center gap-2 font-mono">
                  <span className="font-bold text-white text-sm">{svc.name}</span>
                  <span
                    className={cn(
                      'px-2 py-0.5 rounded text-[10px] font-semibold uppercase tracking-wider border',
                      svc.classification === 'infrastructure'
                        ? 'bg-amber-500/10 text-amber-400 border-amber-500/20'
                        : svc.classification === 'worker'
                        ? 'bg-purple-500/10 text-purple-400 border-purple-500/20'
                        : svc.classification === 'job'
                        ? 'bg-teal-500/10 text-teal-400 border-teal-500/20'
                        : 'bg-sky-500/10 text-sky-400 border-sky-500/20'
                    )}
                  >
                    {svc.classification}
                  </span>
                  <span className="text-neutral-500 text-[11px]">({svc.role})</span>
                </div>

                <div className="flex items-center gap-2 text-[11px]">
                  {svc.public_exposed ? (
                    <button
                      type="button"
                      onClick={() => {
                        setDeploymentPlan((prev) =>
                          prev
                            ? {
                                ...prev,
                                services: prev.services.map((s, i) =>
                                  i === idx ? { ...s, public_exposed: false } : s
                                ),
                              }
                            : prev
                        );
                      }}
                      className="px-2 py-0.5 rounded bg-emerald-500/10 hover:bg-emerald-500/20 text-emerald-400 border border-emerald-500/20 font-mono transition-colors cursor-pointer"
                      title="Click to toggle to Internal service (not exposed to internet)"
                    >
                      Public: :{svc.host_port || svc.internal_port} &rarr; :{svc.internal_port} (Click to make Internal)
                    </button>
                  ) : (
                    <button
                      type="button"
                      onClick={() => {
                        setDeploymentPlan((prev) =>
                          prev
                            ? {
                                ...prev,
                                services: prev.services.map((s, i) =>
                                  i === idx ? { ...s, public_exposed: true } : s
                                ),
                              }
                            : prev
                        );
                      }}
                      className="px-2 py-0.5 rounded bg-neutral-800 hover:bg-neutral-700 text-neutral-400 border border-neutral-700 font-mono transition-colors cursor-pointer"
                      title="Click to toggle to Public service (exposed with preview URL)"
                    >
                      Internal: {svc.name}:{svc.internal_port} (Click to expose Publicly)
                    </button>
                  )}
                </div>
              </div>

              <div className="grid grid-cols-1 sm:grid-cols-2 gap-2 text-[11px] text-neutral-400 font-mono">
                <div>
                  <span className="text-neutral-500">Build: </span>
                  {svc.build_strategy === 'image' ? (
                    <span className="text-amber-400">Pre-built image ({svc.image})</span>
                  ) : svc.build_strategy === 'dockerfile' ? (
                    <span className="text-sky-300">Dockerfile ({svc.dockerfile_path || 'Dockerfile'})</span>
                  ) : (
                    <span className="text-emerald-400">ForgeLAB Generated ({svc.runtime_type})</span>
                  )}
                </div>

                {svc.depends_on && svc.depends_on.length > 0 && (
                  <div>
                    <span className="text-neutral-500">Depends on: </span>
                    <span className="text-primary-light">{svc.depends_on.join(', ')}</span>
                  </div>
                )}
              </div>

              {svc.volumes && svc.volumes.length > 0 && (
                <div className="text-[11px] text-neutral-400 font-mono bg-black/20 p-2 rounded border border-neutral-800/60">
                  <span className="text-neutral-500">Volumes: </span>
                  {svc.volumes.map((v) => `${v.source || v.name} -> ${v.target || v.container_path}${v.read_only ? ' (ro)' : ''}`).join(', ')}
                </div>
              )}
            </div>
          ))}
        </div>
      </div>

      {/* Environment Variables Provenance & Conflicts */}
      {deploymentPlan.environment && deploymentPlan.environment.length > 0 && (
        <div className="p-4 rounded-lg bg-surface border border-surface-border space-y-3">
          <div className="flex items-center justify-between">
            <div className="flex items-center gap-2 text-xs font-semibold text-white">
              <FileText className="w-4 h-4 text-emerald-400" />
              <span>Discovered Environment Variables ({deploymentPlan.environment.length})</span>
            </div>
            <span className="text-[11px] text-neutral-400">
              Imported from .env / Compose
            </span>
          </div>

          <div className="max-h-48 overflow-y-auto space-y-1.5 pr-1">
            {deploymentPlan.environment.map((env) => (
              <div
                key={`${env.service_name || 'root'}-${env.key}`}
                className="flex items-center justify-between p-2 rounded bg-surface-elevated/40 border border-surface-border/50 text-xs font-mono"
              >
                <div className="flex items-center gap-2 overflow-hidden">
                  <span className="text-emerald-400 font-semibold">{env.key}</span>
                  <span className="text-neutral-500">=</span>
                  <span className="text-neutral-300 truncate max-w-[150px]">
                    {env.is_secret ? '••••••••' : env.value}
                  </span>
                </div>

                <div className="flex items-center gap-2 text-[10px]">
                  {env.source_file && (
                    <span className="text-neutral-500">{env.source_file}</span>
                  )}
                  {env.has_conflict ? (
                    <span className="px-1.5 py-0.5 rounded bg-amber-500/20 text-amber-300 border border-amber-500/30">
                      ForgeLAB Secret Preserved
                    </span>
                  ) : (
                    <span className="px-1.5 py-0.5 rounded bg-emerald-500/10 text-emerald-400 border border-emerald-500/20">
                      Imported
                    </span>
                  )}
                </div>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Sticky Actions */}
      <div className="sticky bottom-0 -mx-5 sm:-mx-6 -mb-5 sm:-mb-6 px-5 sm:px-6 py-3.5 bg-surface/95 backdrop-blur border-t border-surface-border flex items-center justify-between z-10 mt-6">
        <Button
          type="button"
          variant="ghost"
          size="sm"
          onClick={onBack}
          icon={<ArrowLeft className="w-3.5 h-3.5" />}
        >
          Back to Configuration
        </Button>
        <div className="flex items-center gap-2">
          <Button type="button" variant="ghost" size="sm" onClick={onCancel} disabled={loading}>
            Cancel
          </Button>
          <Button
            type="button"
            variant="primary"
            size="sm"
            onClick={onDeploy}
            loading={loading}
            icon={<CheckCircle2 className="w-3.5 h-3.5" />}
          >
            Deploy Project
          </Button>
        </div>
      </div>
    </div>
  );
}
