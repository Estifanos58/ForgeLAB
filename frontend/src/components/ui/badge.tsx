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
  let computedVariant = variant || 'neutral';
  const normStatus = (status || '').toLowerCase();

  const isLive = ['deploying', 'building', 'starting', 'health_checking', 'cloning'].includes(normStatus);

  if (!variant && status) {
    switch (normStatus) {
      case 'running':
        computedVariant = 'success';
        break;
      case 'partially_running':
        computedVariant = 'warning';
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
      case 'inactive':
      default:
        computedVariant = 'neutral';
        break;
    }
  }

  const dotColors = {
    success: 'bg-emerald-500',
    warning: cn('bg-amber-400', isLive && 'animate-pulse'),
    danger: 'bg-red-500',
    info: 'bg-blue-400',
    neutral: 'bg-neutral-500',
    default: 'bg-neutral-400',
  };

  const textColors = {
    success: 'text-emerald-400',
    warning: 'text-amber-400',
    danger: 'text-red-400',
    info: 'text-blue-400',
    neutral: 'text-neutral-400',
    default: 'text-neutral-300',
  };

  const displayText = children || (status ? status.replace(/_/g, ' ') : '');

  return (
    <span
      className={cn(
        'inline-flex items-center gap-1.5 px-2 py-0.5 rounded border border-surface-border bg-surface-elevated/80 text-[11px] font-mono leading-none select-none',
        className
      )}
    >
      {showDot && (
        <span
          className={cn('inline-block h-1.5 w-1.5 rounded-full shrink-0', dotColors[computedVariant])}
          aria-hidden="true"
        />
      )}
      <span className={cn('capitalize font-sans font-medium', textColors[computedVariant])}>
        {displayText}
      </span>
    </span>
  );
}
