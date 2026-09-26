'use client';

import React from 'react';
import { Deployment } from '@/lib/api/types';
import { Badge } from '@/components/ui/badge';
import { Clock, AlertTriangle } from 'lucide-react';
import { cn } from '@/lib/utils/cn';

interface DeploymentHistoryProps {
  deployments: Deployment[];
  selectedDeploymentId: string | null;
  onSelectDeployment: (deployment: Deployment) => void;
}

export function DeploymentHistory({
  deployments,
  selectedDeploymentId,
  onSelectDeployment,
}: DeploymentHistoryProps) {
  if (deployments.length === 0) {
    return (
      <div className="p-6 text-center text-xs text-slate-500 font-sans border border-surface-border rounded-xl bg-surface/40">
        No deployments recorded yet.
      </div>
    );
  }

  const formatDuration = (ms: number | null) => {
    if (!ms) return null;
    const sec = (ms / 1000).toFixed(1);
    return `${sec}s`;
  };

  return (
    <div className="space-y-2 max-h-[400px] overflow-y-auto pr-1">
      {deployments.map((d) => {
        const isSelected = d.id === selectedDeploymentId;
        const duration = formatDuration(d.duration_ms);

        return (
          <button
            key={d.id}
            type="button"
            onClick={() => onSelectDeployment(d)}
            className={cn(
              'w-full text-left p-3 rounded-xl border transition-all select-none',
              isSelected
                ? 'bg-surface-elevated border-primary-500/60 shadow-md shadow-primary-950'
                : 'bg-surface/60 border-surface-border hover:bg-surface-elevated/40 hover:border-slate-600'
            )}
          >
            <div className="flex items-center justify-between gap-2 mb-1">
              <span className="font-mono text-xs font-bold text-white">#{d.deploy_number}</span>
              <Badge status={d.status} />
            </div>

            <div className="flex items-center justify-between text-[11px] text-slate-400 mt-2 font-mono">
              <span>{new Date(d.created_at).toLocaleDateString()} {new Date(d.created_at).toLocaleTimeString()}</span>
              {duration && (
                <span className="flex items-center gap-1 text-slate-400">
                  <Clock className="w-3 h-3 text-slate-500" />
                  {duration}
                </span>
              )}
            </div>

            {d.failure_reason && (
              <div className="mt-2 pt-2 border-t border-rose-900/30 flex items-start gap-1.5 text-[11px] text-rose-300">
                <AlertTriangle className="w-3.5 h-3.5 shrink-0 text-rose-400 mt-0.5" />
                <span className="truncate">{d.failure_reason}</span>
              </div>
            )}
          </button>
        );
      })}
    </div>
  );
}
