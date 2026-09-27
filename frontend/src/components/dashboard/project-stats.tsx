import React from 'react';
import { Project } from '@/lib/api/types';

interface ProjectStatsProps {
  projects: Project[];
}

export function ProjectStats({ projects }: ProjectStatsProps) {
  const total = projects.length;
  const running = projects.filter((p) => p.status === 'running').length;
  const deploying = projects.filter((p) => ['deploying', 'building', 'starting', 'health_checking', 'cloning', 'queued'].includes(p.status)).length;
  const failed = projects.filter((p) => ['failed', 'crashed'].includes(p.status)).length;

  return (
    <div className="grid grid-cols-2 sm:grid-cols-4 border border-surface-border rounded-md bg-surface divide-y sm:divide-y-0 sm:divide-x divide-surface-border text-xs">
      <div className="p-3 sm:px-4 flex items-center justify-between">
        <span className="text-neutral-400 font-medium">Projects</span>
        <span className="font-mono text-sm font-semibold text-white">{total}</span>
      </div>

      <div className="p-3 sm:px-4 flex items-center justify-between">
        <span className="text-neutral-400 font-medium flex items-center gap-1.5">
          <span className="w-1.5 h-1.5 rounded-full bg-emerald-500" />
          <span>Running</span>
        </span>
        <span className="font-mono text-sm font-semibold text-emerald-400">{running}</span>
      </div>

      <div className="p-3 sm:px-4 flex items-center justify-between">
        <span className="text-neutral-400 font-medium flex items-center gap-1.5">
          <span className="w-1.5 h-1.5 rounded-full bg-amber-400" />
          <span>Deploying</span>
        </span>
        <span className="font-mono text-sm font-semibold text-amber-400">{deploying}</span>
      </div>

      <div className="p-3 sm:px-4 flex items-center justify-between">
        <span className="text-neutral-400 font-medium flex items-center gap-1.5">
          <span className="w-1.5 h-1.5 rounded-full bg-red-400" />
          <span>Failed</span>
        </span>
        <span className="font-mono text-sm font-semibold text-red-400">{failed}</span>
      </div>
    </div>
  );
}
