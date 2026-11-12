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

  const maxDurationMs = Math.max(
    1000,
    ...deployments.map((d) => (d.duration_ms && d.duration_ms > 0 ? d.duration_ms : 0))
  );

  const recentChronological = [...deployments].slice(0, 12).reverse();

  const barColorClass = (status: string) => {
    switch (status) {
      case 'running':
      case 'completed':
        return 'bg-emerald-500/80 hover:bg-emerald-400';
      case 'failed':
      case 'crashed':
        return 'bg-rose-500/80 hover:bg-rose-400';
      case 'degraded':
      case 'partially_running':
      case 'deploying':
      case 'building':
      case 'starting':
      case 'health_checking':
        return 'bg-amber-400/80 hover:bg-amber-300';
      default:
        return 'bg-neutral-500/70 hover:bg-neutral-400';
    }
  };

  return (
    <div className="rounded-md border border-surface-border bg-surface overflow-hidden">
      {/* Mini Release Timeline & Duration Bar Graph */}
      {recentChronological.length > 1 && (
        <div className="p-2.5 border-b border-surface-border bg-surface-elevated/35 space-y-1.5">
          <div className="flex items-center justify-between text-[10px] font-mono text-neutral-400">
            <span className="uppercase tracking-wider">Release Duration Trend</span>
            <span>Peak {(maxDurationMs / 1000).toFixed(1)}s</span>
          </div>
          <div className="h-9 flex items-end gap-1" role="img" aria-label="Deployment duration bar graph">
            {recentChronological.map((d) => {
              const dur = d.duration_ms && d.duration_ms > 0 ? d.duration_ms : maxDurationMs * 0.2;
              const hPct = Math.max(18, Math.min(100, (dur / maxDurationMs) * 100));
              const isSelected = d.id === selectedDeploymentId;
              return (
                <button
                  key={d.id}
                  type="button"
                  onClick={() => onSelectDeployment(d)}
                  title={`#${d.deploy_number} (${d.status})${d.duration_ms ? ` · ${(d.duration_ms / 1000).toFixed(1)}s` : ''}`}
                  className={cn(
                    'flex-1 rounded-t transition-all min-w-[6px]',
                    barColorClass(d.status),
                    isSelected && 'ring-1 ring-white'
                  )}
                  style={{ height: `${hPct}%` }}
                />
              );
            })}
          </div>
        </div>
      )}

      <div className="divide-y divide-surface-border max-h-[420px] overflow-y-auto">
        {deployments.map((d) => {
          const isSelected = d.id === selectedDeploymentId;
          const duration = formatDuration(d.duration_ms);
          const durationBarPct =
            d.duration_ms && d.duration_ms > 0
              ? Math.max(8, Math.min(100, (d.duration_ms / maxDurationMs) * 100))
              : 0;

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
                    <span className="flex items-center gap-1.5 text-neutral-400">
                      <span className="w-10 h-1.5 rounded-full bg-surface-border/70 overflow-hidden inline-block">
                        <span
                          className={cn('block h-full rounded-full', barColorClass(d.status))}
                          style={{ width: `${durationBarPct}%` }}
                        />
                      </span>
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
    </div>
  );
}
