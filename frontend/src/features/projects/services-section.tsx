'use client';

import React from 'react';
import { Project, Service } from '@/lib/api/types';
import { ServiceCard } from './service-card';
import { Layers, Sliders, FolderGit2, GitBranch, FileCode, HeartPulse } from 'lucide-react';

interface ServicesSectionProps {
  project: Project;
  deployingServices: { [serviceId: string]: boolean };
  actionLoading: { [serviceId: string]: string | null };
  isAnyDeploying: boolean;
  onDeployService: (serviceId: string) => void;
  onServiceAction: (serviceId: string, action: 'start' | 'stop' | 'restart') => void;
  onViewLogs: (serviceId: string) => void;
}

export function ServicesSection({
  project,
  deployingServices,
  actionLoading,
  isAnyDeploying,
  onDeployService,
  onServiceAction,
  onViewLogs,
}: ServicesSectionProps) {
  const services = project.services || [];

  if (services.length > 0) {
    return (
      <div className="rounded-md border border-surface-border bg-surface p-4 sm:p-5 space-y-3.5">
        <div className="text-[11px] font-mono uppercase tracking-wider text-neutral-400 font-semibold flex items-center justify-between">
          <span className="flex items-center gap-1.5">
            <Layers className="w-3.5 h-3.5 text-emerald-400" />
            <span>Services ({services.length})</span>
          </span>
          <span className="text-[10px] text-neutral-500 font-sans normal-case">
            Network: forgelab-net-{project.id.slice(0, 8)}
          </span>
        </div>

        <div className="space-y-3 pt-1">
          {services.map((svc) => (
            <ServiceCard
              key={svc.id}
              service={svc}
              isDeployPending={!!deployingServices[svc.id]}
              actionLoading={actionLoading[svc.id] || null}
              isAnyDeploying={isAnyDeploying}
              onDeploy={onDeployService}
              onAction={onServiceAction}
              onViewLogs={onViewLogs}
            />
          ))}
        </div>
      </div>
    );
  }

  // Single-service fallback details
  return (
    <div className="rounded-md border border-surface-border bg-surface p-4 sm:p-5 space-y-3">
      <div className="text-[11px] font-mono uppercase tracking-wider text-neutral-400 font-semibold flex items-center gap-1.5">
        <Sliders className="w-3.5 h-3.5" />
        <span>Build & Runtime Configuration</span>
      </div>

      <div className="divide-y divide-surface-border font-mono text-xs">
        <div className="py-2 flex items-start justify-between gap-2">
          <span className="text-neutral-400 flex items-center gap-1 shrink-0">
            <FolderGit2 className="w-3 h-3 text-neutral-500" /> Source Path
          </span>
          <span className="text-neutral-200 truncate text-right" title={project.repository_path}>
            {project.repository_path || 'Direct Workspace'}
          </span>
        </div>

        <div className="py-2 flex items-center justify-between gap-2">
          <span className="text-neutral-400 flex items-center gap-1">
            <GitBranch className="w-3 h-3 text-neutral-500" /> Branch
          </span>
          <span className="text-neutral-200">{project.branch}</span>
        </div>

        <div className="py-2 flex items-center justify-between gap-2">
          <span className="text-neutral-400 flex items-center gap-1">
            <FileCode className="w-3 h-3 text-neutral-500" /> Dockerfile
          </span>
          <span className="text-neutral-200">{project.dockerfile_path}</span>
        </div>

        <div className="py-2 flex items-center justify-between gap-2">
          <span className="text-neutral-400">Build Context</span>
          <span className="text-neutral-200">{project.build_context}</span>
        </div>

        <div className="py-2 flex items-center justify-between gap-2">
          <span className="text-neutral-400 flex items-center gap-1">
            <HeartPulse className="w-3 h-3 text-neutral-500" /> Health Path
          </span>
          <span className="text-neutral-200">{project.health_check_path || 'None'}</span>
        </div>

        <div className="py-2 flex items-center justify-between gap-2">
          <span className="text-neutral-400">Allocated Host Port</span>
          <span className="text-neutral-200 font-semibold">
            {project.port ? `:${project.port}` : 'None (stopped)'}
          </span>
        </div>
      </div>
    </div>
  );
}
