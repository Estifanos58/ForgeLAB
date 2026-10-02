'use client';

import React from 'react';
import { Project, Service, ServiceDeployment } from '@/lib/api/types';
import { ServiceCard } from './service-card';
import { Button } from '@/components/ui/button';
import {
  Layers,
  Sliders,
  FolderGit2,
  GitBranch,
  FileCode,
  HeartPulse,
  Rocket,
  Globe,
  Server,
  Cpu,
} from 'lucide-react';

interface ServicesSectionProps {
  project: Project;
  deployingServices: { [serviceId: string]: boolean };
  rollingBackServices?: { [serviceId: string]: boolean };
  actionLoading: { [serviceId: string]: string | null };
  onDeployService: (serviceId: string) => void;
  onRollbackService?: (serviceId: string) => void;
  onDeployAll?: () => void;
  isDeployingAll?: boolean;
  onServiceAction: (serviceId: string, action: 'start' | 'stop' | 'restart') => void;
  onViewLogs: (serviceId: string) => void;
  onSelectServiceDeployment?: (serviceDeployment: ServiceDeployment, service: Service) => void;
  selectedServiceDeploymentId?: string | null;
  onServiceUpdated?: (updated: Service) => void;
}

export function ServicesSection({
  project,
  deployingServices,
  rollingBackServices = {},
  actionLoading,
  onDeployService,
  onRollbackService,
  onDeployAll,
  isDeployingAll = false,
  onServiceAction,
  onViewLogs,
  onSelectServiceDeployment,
  selectedServiceDeploymentId,
  onServiceUpdated,
}: ServicesSectionProps) {
  const services = project.services || [];

  const frontendServices = services.filter((s) => s.role === 'frontend');
  const backendServices = services.filter((s) => s.role === 'backend');
  const otherServices = services.filter((s) => s.role !== 'frontend' && s.role !== 'backend');

  if (services.length > 0) {
    return (
      <div className="rounded-md border border-surface-border bg-surface p-4 sm:p-5 space-y-5">
        {/* Top Header & Release Deploy All Button */}
        <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-3 pb-3 border-b border-surface-border/70">
          <div>
            <div className="text-[11px] font-mono uppercase tracking-wider text-neutral-400 font-semibold flex items-center gap-1.5">
              <Layers className="w-3.5 h-3.5 text-emerald-400" />
              <span>Independent Services ({services.length})</span>
            </div>
            <div className="text-[10px] text-neutral-500 font-mono mt-0.5">
              Isolated Project Mesh: forgelab-net-{project.id.slice(0, 8)}
            </div>
          </div>

          {onDeployAll && (
            <div className="flex items-center gap-2">
              <Button
                variant="primary"
                size="sm"
                onClick={onDeployAll}
                loading={isDeployingAll}
                disabled={isDeployingAll}
                className="h-8 text-xs px-3 shadow-sm bg-gradient-to-r from-emerald-600 to-teal-600 hover:from-emerald-500 hover:to-teal-500"
                icon={<Rocket className="w-3.5 h-3.5" />}
                title="Trigger a full project release deploying all services"
              >
                {isDeployingAll ? 'Deploying Release...' : 'Deploy All Services'}
              </Button>
            </div>
          )}
        </div>

        {/* Frontend Services Section */}
        {frontendServices.length > 0 && (
          <div className="space-y-2.5">
            <div className="flex items-center gap-2 text-xs font-mono text-blue-400 font-medium">
              <Globe className="w-3.5 h-3.5 text-blue-400" />
              <span className="uppercase tracking-wider text-[11px] font-bold">
                Frontend ({frontendServices.length})
              </span>
              <span className="text-[10px] text-neutral-500 font-sans font-normal">
                User interfaces & client applications
              </span>
            </div>
            <div className="space-y-3">
              {frontendServices.map((svc) => (
                <ServiceCard
                  key={svc.id}
                  projectId={project.id}
                  service={svc}
                  isDeployPending={!!deployingServices[svc.id]}
                  isRollbackPending={!!rollingBackServices[svc.id]}
                  actionLoading={actionLoading[svc.id] || null}
                  onDeploy={onDeployService}
                  onRollback={onRollbackService}
                  onAction={onServiceAction}
                  onViewLogs={onViewLogs}
                  onSelectServiceDeployment={onSelectServiceDeployment}
                  selectedServiceDeploymentId={selectedServiceDeploymentId}
                  onServiceUpdated={onServiceUpdated}
                />
              ))}
            </div>
          </div>
        )}

        {/* Backend Services Section */}
        {backendServices.length > 0 && (
          <div className="space-y-2.5">
            <div className="flex items-center gap-2 text-xs font-mono text-emerald-400 font-medium">
              <Server className="w-3.5 h-3.5 text-emerald-400" />
              <span className="uppercase tracking-wider text-[11px] font-bold">
                Backend ({backendServices.length})
              </span>
              <span className="text-[10px] text-neutral-500 font-sans font-normal">
                APIs, business logic & internal services
              </span>
            </div>
            <div className="space-y-3">
              {backendServices.map((svc) => (
                <ServiceCard
                  key={svc.id}
                  projectId={project.id}
                  service={svc}
                  isDeployPending={!!deployingServices[svc.id]}
                  isRollbackPending={!!rollingBackServices[svc.id]}
                  actionLoading={actionLoading[svc.id] || null}
                  onDeploy={onDeployService}
                  onRollback={onRollbackService}
                  onAction={onServiceAction}
                  onViewLogs={onViewLogs}
                  onSelectServiceDeployment={onSelectServiceDeployment}
                  selectedServiceDeploymentId={selectedServiceDeploymentId}
                  onServiceUpdated={onServiceUpdated}
                />
              ))}
            </div>
          </div>
        )}

        {/* Worker & Other Services Section */}
        {otherServices.length > 0 && (
          <div className="space-y-2.5">
            <div className="flex items-center gap-2 text-xs font-mono text-purple-400 font-medium">
              <Cpu className="w-3.5 h-3.5 text-purple-400" />
              <span className="uppercase tracking-wider text-[11px] font-bold">
                Workers & Other ({otherServices.length})
              </span>
              <span className="text-[10px] text-neutral-500 font-sans font-normal">
                Background tasks, queue processors & scheduled jobs
              </span>
            </div>
            <div className="space-y-3">
              {otherServices.map((svc) => (
                <ServiceCard
                  key={svc.id}
                  projectId={project.id}
                  service={svc}
                  isDeployPending={!!deployingServices[svc.id]}
                  isRollbackPending={!!rollingBackServices[svc.id]}
                  actionLoading={actionLoading[svc.id] || null}
                  onDeploy={onDeployService}
                  onRollback={onRollbackService}
                  onAction={onServiceAction}
                  onViewLogs={onViewLogs}
                  onSelectServiceDeployment={onSelectServiceDeployment}
                  selectedServiceDeploymentId={selectedServiceDeploymentId}
                  onServiceUpdated={onServiceUpdated}
                />
              ))}
            </div>
          </div>
        )}
      </div>
    );
  }

  // Single-service fallback details for projects without multi-service definition
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
