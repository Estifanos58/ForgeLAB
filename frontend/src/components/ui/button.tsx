import React from 'react';
import { cn } from '@/lib/utils/cn';
import { Loader2 } from 'lucide-react';

export interface ButtonProps extends React.ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: 'primary' | 'secondary' | 'outline' | 'ghost' | 'danger' | 'success';
  size?: 'sm' | 'md' | 'lg' | 'icon';
  loading?: boolean;
  icon?: React.ReactNode;
}

export const Button = React.forwardRef<HTMLButtonElement, ButtonProps>(
  ({ className, variant = 'primary', size = 'md', loading = false, disabled, children, icon, ...props }, ref) => {
    const baseStyles =
      'inline-flex items-center justify-center font-medium rounded-md transition-colors duration-150 focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-neutral-300 disabled:opacity-40 disabled:pointer-events-none select-none border text-xs sm:text-sm';

    const variants = {
      primary:
        'bg-white text-neutral-950 border-white hover:bg-neutral-200 hover:border-neutral-200 font-semibold shadow-sm',
      secondary:
        'bg-surface-elevated text-neutral-200 border-surface-border hover:bg-surface-hover hover:border-neutral-600 hover:text-white shadow-subtle',
      outline:
        'bg-transparent text-neutral-300 border-surface-border hover:border-neutral-500 hover:text-white',
      ghost:
        'bg-transparent text-neutral-400 border-transparent hover:bg-surface-elevated hover:text-white',
      danger:
        'bg-red-950/30 text-red-300 border-red-900/50 hover:bg-red-900/30 hover:border-red-700/60 hover:text-red-200',
      success:
        'bg-emerald-950/30 text-emerald-300 border-emerald-900/50 hover:bg-emerald-900/30 hover:border-emerald-700/60 hover:text-emerald-200',
    };

    const sizes = {
      sm: 'h-7 px-2.5 text-xs gap-1.5',
      md: 'h-8 sm:h-9 px-3.5 text-xs sm:text-sm gap-2',
      lg: 'h-10 px-4 text-sm gap-2',
      icon: 'h-8 w-8 p-0',
    };

    return (
      <button
        ref={ref}
        disabled={disabled || loading}
        className={cn(baseStyles, variants[variant], sizes[size], className)}
        {...props}
      >
        {loading ? <Loader2 className="w-3.5 h-3.5 animate-spin" /> : icon ? icon : null}
        {children}
      </button>
    );
  }
);

Button.displayName = 'Button';
