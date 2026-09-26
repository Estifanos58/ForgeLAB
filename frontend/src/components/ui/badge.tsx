import React from 'react';
import { cn } from '@/lib/utils/cn';

export interface BadgeProps {
  status?: string;
  variant?: 'default' | 'success' | 'warning' | 'danger' | 'info' | 'neutral';
  className?: string;
  children?: React.ReactNode;
  showDot?: boolean;
}

export function Badge({ status, variant, className, children, showDot = true }: BadgeProps) {
  // Map deployment and project statuses to colors
  let computedVariant = variant || 'neutral';
  const normStatus = (status || '').toLowerCase();

  if (!variant && status) {
    switch (normStatus) {
      case 'running':
        computedVariant = 'success';
        break;
      case 'deploying':
      case 'building':
      case 'starting':
      case 'health_checking':
      case 'cloning':
      case 'queued':
        computedVariant = 'warning';
        break;
      case 'failed':
      case 'crashed':
        computedVariant = 'danger';
        break;
      case 'stopped':
        computedVariant = 'neutral';
        break;
      case 'inactive':
      default:
        computedVariant = 'neutral';
        break;
    }
  }

  const variants = {
    success: 'bg-emerald-950/70 border-emerald-500/40 text-emerald-300 shadow-sm shadow-emerald-900/30',
    warning: 'bg-amber-950/70 border-amber-500/40 text-amber-300 shadow-sm shadow-amber-900/30',
    danger: 'bg-rose-950/70 border-rose-500/40 text-rose-300 shadow-sm shadow-rose-900/30',
    info: 'bg-primary-950/70 border-primary-500/40 text-primary-300 shadow-sm shadow-primary-900/30',
    neutral: 'bg-slate-900 border-slate-700/60 text-slate-300',
    default: 'bg-slate-900 border-slate-700/60 text-slate-300',
  };

  const dotColors = {
    success: 'bg-emerald-400 animate-pulse',
    warning: 'bg-amber-400 animate-ping',
    danger: 'bg-rose-400',
    info: 'bg-primary-400',
    neutral: 'bg-slate-400',
    default: 'bg-slate-400',
  };

  const displayText = children || (status ? status.replace(/_/g, ' ') : '');

  return (
    <span
      className={cn(
        'inline-flex items-center gap-1.5 px-2.5 py-0.5 rounded-full text-xs font-medium uppercase tracking-wider border',
        variants[computedVariant],
        className
      )}
    >
      {showDot && (
        <span className="relative flex h-2 w-2">
          <span className={cn('relative inline-flex rounded-full h-2 w-2', dotColors[computedVariant])} />
        </span>
      )}
      {displayText}
    </span>
  );
}
