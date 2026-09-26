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
    error: 'bg-rose-950/60 border-rose-600/40 text-rose-200',
    warning: 'bg-amber-950/60 border-amber-600/40 text-amber-200',
    success: 'bg-emerald-950/60 border-emerald-600/40 text-emerald-200',
    info: 'bg-primary-950/60 border-primary-600/40 text-primary-200',
  };

  const icons = {
    error: <AlertCircle className="w-5 h-5 text-rose-400 shrink-0" />,
    warning: <AlertTriangle className="w-5 h-5 text-amber-400 shrink-0" />,
    success: <CheckCircle2 className="w-5 h-5 text-emerald-400 shrink-0" />,
    info: <Info className="w-5 h-5 text-primary-400 shrink-0" />,
  };

  return (
    <div
      role="alert"
      className={cn('flex items-start gap-3 p-4 rounded-xl border backdrop-blur-sm text-sm', styles[variant], className)}
    >
      {icons[variant]}
      <div className="flex-1">
        {title && <h5 className="font-semibold mb-1">{title}</h5>}
        <div className="text-xs leading-relaxed opacity-90">{children}</div>
      </div>
      {onClose && (
        <button
          onClick={onClose}
          className="text-current opacity-60 hover:opacity-100 p-1 -mr-1 -mt-1 rounded"
          aria-label="Dismiss alert"
        >
          <X className="w-4 h-4" />
        </button>
      )}
    </div>
  );
}
