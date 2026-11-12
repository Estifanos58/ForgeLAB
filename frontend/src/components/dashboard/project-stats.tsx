'use client';

import React from 'react';
import { Project } from '@/lib/api/types';
import { DonutChart, StackedDistributionBar } from '@/components/ui/charts';

interface ProjectStatsProps {
  projects: Project[];
}

export function ProjectStats({ projects }: ProjectStatsProps) {
  const total = projects.length;
  const running = projects.filter((p) => p.status === 'running').length;
  const degraded = projects.filter(
    (p) => p.status === 'degraded' || p.status === 'partially_running'
  ).length;
  const deploying = projects.filter((p) =>
    ['deploying', 'building', 'starting', 'health_checking', 'cloning', 'queued'].includes(
      p.status
    )
  ).length;
  const failed = projects.filter((p) => ['failed', 'crashed'].includes(p.status)).length;
  const idle = Math.max(0, total - running - degraded - deploying - failed);

  const segments = [
    { id: 'running', label: 'Running', value: running, color: '#10b981' },
    { id: 'degraded', label: 'Degraded', value: degraded, color: '#f59e0b' },
    { id: 'deploying', label: 'Deploying', value: deploying, color: '#38bdf8' },
    { id: 'failed', label: 'Failed', value: failed, color: '#f43f5e' },
    { id: 'idle', label: 'Stopped / Idle', value: idle, color: '#52525b' },
  ];

  return (
    <div className="border border-surface-border rounded-md bg-surface p-4 space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-2 text-xs font-mono">
        <span className="uppercase tracking-wider text-neutral-400 font-semibold">
          Fleet Status Distribution
        </span>
        <span className="text-neutral-500">
          {total} total project{total === 1 ? '' : 's'}
        </span>
      </div>

      <div className="grid grid-cols-1 lg:grid-cols-12 gap-4 items-center">
        <div className="lg:col-span-7">
          <DonutChart
            size={112}
            thickness={14}
            centerValue={String(total)}
            centerLabel="Projects"
            emptyLabel="No projects"
            segments={segments.filter((s) => s.value > 0 || s.id === 'running' || s.id === 'failed')}
          />
        </div>

        <div className="lg:col-span-5 p-3 rounded bg-surface-elevated/40 border border-surface-border/70 space-y-2">
          <span className="text-[10px] font-mono uppercase tracking-wider text-neutral-400 block">
            Fleet Health Ratio
          </span>
          <StackedDistributionBar
            height={12}
            segments={segments}
          />
        </div>
      </div>
    </div>
  );
}
