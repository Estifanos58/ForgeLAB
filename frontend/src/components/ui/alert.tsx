import React from 'react';
import { AlertCircle, CheckCircle2, Info, AlertTriangle, X } from 'lucide-react';
import { cn } from '@/lib/utils/cn';

export interface AlertProps {
  variant?: 'error' | 'warning' | 'success' | 'info';
  title?: string;
  children: React.ReactNode;
  onClose?: () => void;
  className?: string;
}

export function Alert({ variant = 'info', title, children, onClose, className }: AlertProps) {
  const styles = {
    error: 'bg-red-950/20 border-red-900/40 text-red-200',
    warning: 'bg-amber-950/20 border-amber-900/40 text-amber-200',
    success: 'bg-emerald-950/20 border-emerald-900/40 text-emerald-200',
    info: 'bg-neutral-900/60 border-neutral-800 text-neutral-300',
  };

  const icons = {
    error: <AlertCircle className="w-4 h-4 text-red-400 shrink-0 mt-0.5" />,
    warning: <AlertTriangle className="w-4 h-4 text-amber-400 shrink-0 mt-0.5" />,
    success: <CheckCircle2 className="w-4 h-4 text-emerald-400 shrink-0 mt-0.5" />,
    info: <Info className="w-4 h-4 text-neutral-400 shrink-0 mt-0.5" />,
  };

  return (
    <div
      role="alert"
      className={cn('flex items-start gap-2.5 p-3 rounded-md border text-xs leading-relaxed', styles[variant], className)}
    >
      {icons[variant]}
      <div className="flex-1">
        {title && <h5 className="font-semibold text-white mb-0.5">{title}</h5>}
        <div className="text-neutral-300">{children}</div>
      </div>
      {onClose && (
        <button
          onClick={onClose}
          className="text-neutral-400 hover:text-white p-0.5 -mr-1 -mt-0.5 rounded transition-colors"
          aria-label="Dismiss alert"
        >
          <X className="w-3.5 h-3.5" />
        </button>
      )}
    </div>
  );
}
