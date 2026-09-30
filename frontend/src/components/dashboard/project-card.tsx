import React from 'react';
import Link from 'next/link';
import { Project } from '@/lib/api/types';
import { Badge } from '@/components/ui/badge';
import { ExternalLink, GitBranch } from 'lucide-react';

interface ProjectCardProps {
  project: Project;
}

export function ProjectCard({ project }: ProjectCardProps) {
  return (
    <div className="p-4 rounded-md border border-surface-border bg-surface hover:border-neutral-700 transition-colors flex flex-col justify-between gap-3 text-xs">
      <div>
        <div className="flex items-start justify-between gap-2 mb-1.5">
          <div>
            <Link
              href={`/projects/${project.id}`}
              className="font-semibold text-white hover:underline text-sm tracking-tight"
            >
              {project.name}
            </Link>
            <div className="font-mono text-[11px] text-neutral-500 mt-0.5">{project.slug}</div>
          </div>
          <Badge status={project.status} />
        </div>

        <div className="space-y-1 pt-1 font-mono text-[11px] text-neutral-400">
          <div className="truncate" title={project.repository_path}>
            <span className="text-neutral-500">path: </span>
            <span>{project.repository_path}</span>
          </div>
          <div className="flex items-center gap-1">
            <GitBranch className="w-3 h-3 text-neutral-500 shrink-0" />
            <span>{project.branch}</span>
          </div>
        </div>
      </div>

      <div className="pt-2.5 border-t border-surface-border flex items-center justify-between font-mono text-[11px]">
        <div>
          {project.status === 'running' && (project.preview_url || project.port) ? (
            <a
              href={
                project.preview_url ||
                `http://${typeof window !== 'undefined' ? window.location.hostname : 'localhost'}:${project.port}`
              }
              target="_blank"
              rel="noreferrer"
              className="inline-flex items-center gap-1 text-emerald-400 hover:underline"
            >
              <span>{project.port ? `:${project.port}` : 'preview'}</span>
              <ExternalLink className="w-3 h-3" />
            </a>
          ) : (
            <span className="text-neutral-500">—</span>
          )}
        </div>

        <Link
          href={`/projects/${project.id}`}
          className="text-neutral-300 hover:text-white font-medium"
        >
          Manage →
        </Link>
      </div>
    </div>
  );
}
