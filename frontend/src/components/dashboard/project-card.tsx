import React from 'react';
import Link from 'next/link';
import { Project } from '@/lib/api/types';
import { Badge } from '@/components/ui/badge';
import { Card, CardContent } from '@/components/ui/card';
import { ExternalLink, GitBranch, FolderGit2, ArrowRight } from 'lucide-react';

interface ProjectCardProps {
  project: Project;
}

export function ProjectCard({ project }: ProjectCardProps) {
  return (
    <Card glow className="hover:border-primary-500/50 transition-all flex flex-col justify-between">
      <CardContent className="p-5 flex flex-col justify-between h-full space-y-4">
        <div>
          {/* Header row: Name + Status */}
          <div className="flex items-start justify-between gap-3 mb-2">
            <div>
              <Link href={`/projects/${project.id}`} className="hover:underline">
                <h3 className="text-base font-bold text-white group-hover:text-primary-400 transition-colors">
                  {project.name}
                </h3>
              </Link>
              <p className="text-xs text-slate-400 font-mono mt-0.5">{project.slug}</p>
            </div>
            <Badge status={project.status} />
          </div>

          {/* Metadata */}
          <div className="space-y-1.5 pt-2 text-xs text-slate-400">
            <div className="flex items-center gap-2 truncate">
              <FolderGit2 className="w-3.5 h-3.5 text-slate-500 shrink-0" />
              <span className="font-mono text-slate-300 truncate" title={project.repository_path}>
                {project.repository_path}
              </span>
            </div>
            <div className="flex items-center gap-2">
              <GitBranch className="w-3.5 h-3.5 text-slate-500 shrink-0" />
              <span className="text-slate-300">{project.branch}</span>
            </div>
          </div>
        </div>

        {/* Footer row: Live Port + Action */}
        <div className="pt-3 border-t border-surface-border/50 flex items-center justify-between">
          <div>
            {project.status === 'running' && project.port ? (
              <a
                href={`http://localhost:${project.port}`}
                target="_blank"
                rel="noreferrer"
                className="inline-flex items-center gap-1.5 text-xs text-emerald-400 hover:text-emerald-300 font-mono font-medium hover:underline"
              >
                <span>:{project.port}</span>
                <ExternalLink className="w-3 h-3" />
              </a>
            ) : (
              <span className="text-xs text-slate-500 font-mono">no port bound</span>
            )}
          </div>

          <Link
            href={`/projects/${project.id}`}
            className="inline-flex items-center gap-1 text-xs text-primary-400 hover:text-primary-300 font-medium group"
          >
            <span>Manage</span>
            <ArrowRight className="w-3.5 h-3.5 group-hover:translate-x-0.5 transition-transform" />
          </Link>
        </div>
      </CardContent>
    </Card>
  );
}
