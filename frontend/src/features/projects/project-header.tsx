'use client';

import React from 'react';
import { Project, Deployment } from '@/lib/api/types';
import { Badge } from '@/components/ui/badge';
import { LifecycleControls } from '@/features/deployments/lifecycle-controls';
import { ExternalLink, Radio, Layers, Github, Folder, HardDrive } from 'lucide-react';

interface ProjectHeaderProps {
  project: Project;
  onActionComplete: () => void;
  onError: (msg: string) => void;
  onDeploymentCreated?: (deployment: Deployment) => void;
}

export function ProjectHeader({
  project,
  onActionComplete,
  onError,
  onDeploymentCreated,
}: ProjectHeaderProps) {
  const services = project.services || [];

  // Determine the primary preview URL across running services.
  // Preview is ONLY displayed when the corresponding public service or project is confirmed running.
  let primaryPreviewUrl: string | null = null;
  const runningServices = services.filter((s) => s.status === 'running');
  if (services.length > 0) {
    const frontendSvc = runningServices.find(
      (s) =>
        (s.role === 'frontend' || s.name.toLowerCase() === 'frontend') &&
        (s.public_exposed || s.preview_url || s.host_port)
    );

    if (frontendSvc) {
      primaryPreviewUrl =
        frontendSvc.preview_url ||
        (frontendSvc.host_port
          ? `http://${typeof window !== 'undefined' ? window.location.hostname : 'localhost'}:${frontendSvc.host_port}`
          : null);
    } else {
      const anyPublicSvc = runningServices.find(
        (s) => (s.public_exposed || s.role === 'frontend') && (s.preview_url || s.host_port)
      );
      if (anyPublicSvc) {
        primaryPreviewUrl =
          anyPublicSvc.preview_url ||
          (anyPublicSvc.host_port
            ? `http://${typeof window !== 'undefined' ? window.location.hostname : 'localhost'}:${anyPublicSvc.host_port}`
            : null);
      }
    }
  }

  // Fallback if not resolved from individual services: check project-level preview_url or port
  // strictly when project is confirmed running or partially_running
  if (
    !primaryPreviewUrl &&
    (project.status === 'running' || project.status === 'partially_running') &&
    (project.preview_url || project.port)
  ) {
    primaryPreviewUrl =
      project.preview_url ||
      `http://${typeof window !== 'undefined' ? window.location.hostname : 'localhost'}:${project.port}`;
  }

  const getSourceIcon = () => {
    switch (project.source_type) {
      case 'github':
        return <Github className="w-3 h-3 text-neutral-400" />;
      case 'local_agent':
      case 'local_directory':
        return <Folder className="w-3 h-3 text-neutral-400" />;
      case 'local_upload':
      default:
        return <HardDrive className="w-3 h-3 text-neutral-400" />;
    }
  };

  const getSourceLabel = () => {
    switch (project.source_type) {
      case 'github':
        return project.source_reference || 'GitHub';
      case 'local_agent':
        return 'Local Agent Direct';
      case 'local_directory':
        return project.repository_path || 'Local Directory';
      case 'local_upload':
        return 'Uploaded Source';
      default:
        return project.source_type;
    }
  };

  return (
    <div className="flex flex-col md:flex-row md:items-center justify-between gap-4 pb-4 border-b border-surface-border">
      <div className="space-y-1.5 min-w-0">
        <div className="flex items-center gap-3 flex-wrap">
          <h1 className="text-xl sm:text-2xl font-bold text-white tracking-tight truncate">
            {project.name}
          </h1>
          <Badge status={project.status} />

          {/* Primary Preview Link */}
          {primaryPreviewUrl && (
            <a
              href={primaryPreviewUrl}
              target="_blank"
              rel="noreferrer"
              className="inline-flex items-center gap-1.5 px-2.5 py-0.5 rounded border border-emerald-500/30 bg-emerald-500/10 text-xs font-mono text-emerald-300 hover:text-emerald-200 hover:border-emerald-500/50 transition-colors shadow-sm"
              title="Open application preview in new tab"
            >
              <Radio className="w-3 h-3 text-emerald-400 animate-pulse" />
              <span>Preview</span>
              <ExternalLink className="w-3 h-3" />
            </a>
          )}
        </div>

        <div className="flex items-center gap-2.5 text-[11px] font-mono text-neutral-500 flex-wrap">
          <span>slug: {project.slug}</span>
          <span>•</span>
          <span className="inline-flex items-center gap-1 text-neutral-400">
            {getSourceIcon()}
            <span className="truncate max-w-xs">{getSourceLabel()}</span>
          </span>
          {services.length > 0 && (
            <>
              <span>•</span>
              <span className="inline-flex items-center gap-1 text-neutral-400">
                <Layers className="w-3 h-3 text-neutral-500" />
                <span>{services.length} {services.length === 1 ? 'service' : 'services'}</span>
              </span>
            </>
          )}
        </div>
      </div>

      {/* Project-wide Actions */}
      <LifecycleControls
        project={project}
        onActionComplete={onActionComplete}
        onError={onError}
        onDeploymentCreated={onDeploymentCreated}
      />
    </div>
  );
}
