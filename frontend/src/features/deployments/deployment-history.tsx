'use client';

import React from 'react';
import { Deployment } from '@/lib/api/types';
import { Badge } from '@/components/ui/badge';
import { Clock, AlertCircle } from 'lucide-react';
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
      <div className="p-6 text-center text-xs text-neutral-500 rounded-md border border-surface-border bg-surface">
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
    <div className="divide-y divide-surface-border rounded-md border border-surface-border bg-surface max-h-[420px] overflow-y-auto">
      {deployments.map((d) => {
        const isSelected = d.id === selectedDeploymentId;
        const duration = formatDuration(d.duration_ms);

        return (
          <button
            key={d.id}
            type="button"
            onClick={() => onSelectDeployment(d)}
            className={cn(
              'w-full text-left p-3 transition-colors text-xs select-none block',
              isSelected
                ? 'bg-surface-elevated text-white'
                : 'hover:bg-surface-elevated/40 text-neutral-300'
            )}
          >
            <div className="flex items-center justify-between gap-2">
              <div className="flex items-center gap-2">
                <span className="font-mono font-semibold text-white">#{d.deploy_number}</span>
                <Badge status={d.status} />
              </div>

              <div className="flex items-center gap-2 font-mono text-[11px] text-neutral-400">
                {duration && (
                  <span className="flex items-center gap-1 text-neutral-400">
                    <Clock className="w-3 h-3 text-neutral-500" />
                    <span>{duration}</span>
                  </span>
                )}
                <span>{new Date(d.created_at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}</span>
              </div>
            </div>

            {d.failure_reason && (
              <div className="mt-1.5 pt-1.5 border-t border-red-900/30 flex items-start gap-1.5 text-[11px] text-red-300 font-mono">
                <AlertCircle className="w-3 h-3 text-red-400 shrink-0 mt-0.5" />
                <span className="truncate">{d.failure_reason}</span>
              </div>
            )}
          </button>
        );
      })}
    </div>
  );
}
