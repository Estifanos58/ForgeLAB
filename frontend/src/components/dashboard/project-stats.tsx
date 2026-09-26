import React from 'react';
import { Project } from '@/lib/api/types';
import { Layers, Activity, CheckCircle2, AlertOctagon } from 'lucide-react';

interface ProjectStatsProps {
  projects: Project[];
}

export function ProjectStats({ projects }: ProjectStatsProps) {
  const total = projects.length;
  const running = projects.filter((p) => p.status === 'running').length;
  const deploying = projects.filter((p) => p.status === 'deploying').length;
  const failed = projects.filter((p) => p.status === 'failed').length;

  const stats = [
    {
      label: 'Total Projects',
      value: total,
      icon: <Layers className="w-4 h-4 text-primary-400" />,
      color: 'text-white',
    },
    {
      label: 'Active & Serving',
      value: running,
      icon: <CheckCircle2 className="w-4 h-4 text-emerald-400" />,
      color: 'text-emerald-400',
    },
    {
      label: 'Deploying Releases',
      value: deploying,
      icon: <Activity className="w-4 h-4 text-amber-400" />,
      color: 'text-amber-400',
    },
    {
      label: 'Failed Deployments',
      value: failed,
      icon: <AlertOctagon className="w-4 h-4 text-rose-400" />,
      color: 'text-rose-400',
    },
  ];

  return (
    <div className="grid grid-cols-2 sm:grid-cols-4 gap-4">
      {stats.map((s, idx) => (
        <div
          key={idx}
          className="p-4 rounded-xl border border-surface-border bg-surface/80 backdrop-blur-sm flex items-center justify-between"
        >
          <div>
            <p className="text-xs text-slate-400">{s.label}</p>
            <p className={`text-2xl font-bold mt-1 ${s.color}`}>{s.value}</p>
          </div>
          <div className="w-9 h-9 rounded-lg bg-surface-elevated flex items-center justify-center">
            {s.icon}
          </div>
        </div>
      ))}
    </div>
  );
}
